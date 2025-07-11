/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package adk

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
	ub "github.com/cloudwego/eino/utils/callbacks"
)

type ToolsConfig struct {
	compose.ToolsNodeConfig

	// Names of the tools that will make agent return directly when the tool is called.
	// When multiple tools are called and more than one tool is in the return directly list, only the first one will be returned.
	ReturnDirectly map[string]bool
}

type GenModelInput func(ctx context.Context, instruction string, input *AgentInput) ([]Message, error)

func defaultGenModelInput(ctx context.Context, instruction string, input *AgentInput) ([]Message, error) {
	msgs := make([]Message, 0, len(input.Messages)+1)

	if instruction != "" {
		sp := schema.SystemMessage(instruction)

		vs := GetSessionValues(ctx)
		if len(vs) > 0 {
			ct := prompt.FromMessages(schema.FString, sp)
			ms, err := ct.Format(ctx, vs)
			if err != nil {
				return nil, err
			}

			sp = ms[0]
		}

		msgs = append(msgs, sp)
	}

	msgs = append(msgs, input.Messages...)

	return msgs, nil
}

type ChatModelAgentConfig struct {
	Name        string
	Description string
	Instruction string

	Model model.ToolCallingChatModel

	ToolsConfig ToolsConfig

	// optional
	GenModelInput GenModelInput

	// Exit tool. Optional, defaults to nil, which will generate an Exit Action.
	// The built-in implementation is 'ExitTool'
	Exit tool.BaseTool

	// optional
	OutputKey string
}

type ChatModelAgent struct {
	name        string
	description string
	instruction string

	model       model.ToolCallingChatModel
	toolsConfig ToolsConfig

	genModelInput GenModelInput

	outputKey string

	subAgents   []Agent
	parentAgent Agent

	disallowTransferToParent bool

	exit tool.BaseTool

	// runner
	once   sync.Once
	run    runFunc
	frozen uint32
}

type runFunc func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent])

func NewChatModelAgent(_ context.Context, config *ChatModelAgentConfig) (*ChatModelAgent, error) {
	if config.Name == "" {
		return nil, errors.New("agent 'Name' is required")
	}
	if config.Description == "" {
		return nil, errors.New("agent 'Description' is required")
	}
	if config.Model == nil {
		return nil, errors.New("agent 'Model' is required")
	}

	genInput := defaultGenModelInput
	if config.GenModelInput != nil {
		genInput = config.GenModelInput
	}

	return &ChatModelAgent{
		name:          config.Name,
		description:   config.Description,
		instruction:   config.Instruction,
		model:         config.Model,
		toolsConfig:   config.ToolsConfig,
		genModelInput: genInput,
		exit:          config.Exit,
		outputKey:     config.OutputKey,
	}, nil
}

const (
	TransferToAgentToolName = "transfer_to_agent"
	TransferToAgentToolDesc = "Transfer the question to another agent."
)

var (
	toolInfoTransferToAgent = &schema.ToolInfo{
		Name: TransferToAgentToolName,
		Desc: TransferToAgentToolDesc,

		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"agent_name": {
				Desc:     "the name of the agent to transfer to",
				Required: true,
				Type:     schema.String,
			},
		}),
	}

	ToolInfoExit = &schema.ToolInfo{
		Name: "exit",
		Desc: "Exit the agent process and return the final result.",

		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"final_result": {
				Desc:     "the final result to return",
				Required: true,
				Type:     schema.String,
			},
		}),
	}
)

type ExitTool struct{}

func (et ExitTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return ToolInfoExit, nil
}

func (et ExitTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type exitParams struct {
		FinalResult string `json:"final_result"`
	}

	params := &exitParams{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	err = SendToolGenAction(ctx, "exit", NewExitAction())
	if err != nil {
		return "", err
	}

	return params.FinalResult, nil
}

type transferToAgent struct{}

func (tta transferToAgent) Info(_ context.Context) (*schema.ToolInfo, error) {
	return toolInfoTransferToAgent, nil
}

func transferToAgentToolOutput(destName string) string {
	return fmt.Sprintf("successfully transferred to agent [%s]", destName)
}

func (tta transferToAgent) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type transferParams struct {
		AgentName string `json:"agent_name"`
	}

	params := &transferParams{}
	err := sonic.UnmarshalString(argumentsInJSON, params)
	if err != nil {
		return "", err
	}

	err = SendToolGenAction(ctx, TransferToAgentToolName, NewTransferToAgentAction(params.AgentName))
	if err != nil {
		return "", err
	}

	return transferToAgentToolOutput(params.AgentName), nil
}

func (a *ChatModelAgent) Name(_ context.Context) string {
	return a.name
}

func (a *ChatModelAgent) Description(_ context.Context) string {
	return a.description
}

func (a *ChatModelAgent) OnSetSubAgents(_ context.Context, subAgents []Agent) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	if len(a.subAgents) > 0 {
		return errors.New("agent's sub-agents has already been set")
	}

	a.subAgents = subAgents
	return nil
}

func (a *ChatModelAgent) OnSetAsSubAgent(_ context.Context, parent Agent) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	if a.parentAgent != nil {
		return errors.New("agent has already been set as a sub-agent of another agent")
	}

	a.parentAgent = parent
	return nil
}

func (a *ChatModelAgent) OnDisallowTransferToParent(_ context.Context) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	a.disallowTransferToParent = true

	return nil
}

type cbHandler struct {
	*AsyncGenerator[*AgentEvent]
	agentName string
}

func (h *cbHandler) onChatModelEnd(ctx context.Context,
	_ *callbacks.RunInfo, output *model.CallbackOutput) context.Context {

	event := EventFromMessage(output.Message, nil, schema.Assistant, "")
	h.Send(event)
	return ctx
}

func (h *cbHandler) onChatModelEndWithStreamOutput(ctx context.Context,
	_ *callbacks.RunInfo, output *schema.StreamReader[*model.CallbackOutput]) context.Context {

	cvt := func(in *model.CallbackOutput) (Message, error) {
		return in.Message, nil
	}
	out := schema.StreamReaderWithConvert(output, cvt)
	event := EventFromMessage(nil, out, schema.Assistant, "")
	h.Send(event)

	return ctx
}

func (h *cbHandler) onToolEnd(ctx context.Context,
	runInfo *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {

	toolCallID := compose.GetToolCallID(ctx)
	msg := schema.ToolMessage(output.Response, toolCallID, schema.WithToolName(runInfo.Name))
	event := EventFromMessage(msg, nil, schema.Tool, runInfo.Name)

	action := popToolGenAction(ctx, runInfo.Name)
	event.Action = action

	h.Send(event)

	return ctx
}

func (h *cbHandler) onToolEndWithStreamOutput(ctx context.Context,
	runInfo *callbacks.RunInfo, output *schema.StreamReader[*tool.CallbackOutput]) context.Context {

	toolCallID := compose.GetToolCallID(ctx)
	cvt := func(in *tool.CallbackOutput) (Message, error) {
		return schema.ToolMessage(in.Response, toolCallID), nil
	}
	out := schema.StreamReaderWithConvert(output, cvt)
	event := EventFromMessage(nil, out, schema.Tool, runInfo.Name)
	h.Send(event)

	return ctx
}

func (h *cbHandler) onGraphError(ctx context.Context,
	_ *callbacks.RunInfo, err error) context.Context {

	h.Send(&AgentEvent{Err: err})

	return ctx
}

func genReactCallbacks(agentName string,
	generator *AsyncGenerator[*AgentEvent]) compose.Option {

	h := &cbHandler{generator, agentName}

	cmHandler := &ub.ModelCallbackHandler{
		OnEnd:                 h.onChatModelEnd,
		OnEndWithStreamOutput: h.onChatModelEndWithStreamOutput,
	}
	toolHandler := &ub.ToolCallbackHandler{
		OnEnd:                 h.onToolEnd,
		OnEndWithStreamOutput: h.onToolEndWithStreamOutput,
	}
	graphHandler := callbacks.NewHandlerBuilder().OnErrorFn(h.onGraphError).Build()

	cb := ub.NewHandlerHelper().ChatModel(cmHandler).Tool(toolHandler).Graph(graphHandler).Handler()

	return compose.WithCallbacks(cb)
}

func setOutputToSession(ctx context.Context, msg Message, msgStream MessageStream, outputKey string) error {
	msgVariant := &MessageVariant{IsStreaming: msgStream != nil, Message: msg, MessageStream: msgStream}

	msg, err := msgVariant.GetMessage()
	if err != nil {
		return err
	}

	SetSessionValue(ctx, outputKey, msg.Content)
	return nil
}

func errFunc(err error) runFunc {
	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent]) {
		generator.Send(&AgentEvent{Err: err})
	}
}

func (a *ChatModelAgent) buildRunFunc(ctx context.Context) runFunc {
	a.once.Do(func() {
		instruction := a.instruction
		toolsNodeConf := a.toolsConfig.ToolsNodeConfig
		returnDirectly := copyMap(a.toolsConfig.ReturnDirectly)

		transferToAgents := a.subAgents
		if a.parentAgent != nil && !a.disallowTransferToParent {
			transferToAgents = append(transferToAgents, a.parentAgent)
		}

		if len(transferToAgents) > 0 {
			transferInstruction := genTransferToAgentInstruction(ctx, transferToAgents)
			instruction = concatInstructions(instruction, transferInstruction)

			toolsNodeConf.Tools = append(toolsNodeConf.Tools, &transferToAgent{})
			returnDirectly[TransferToAgentToolName] = true
		}

		if a.exit != nil {
			toolsNodeConf.Tools = append(toolsNodeConf.Tools, a.exit)
			exitInfo, err := a.exit.Info(ctx)
			if err != nil {
				a.run = errFunc(err)
				return
			}
			returnDirectly[exitInfo.Name] = true
		}

		if len(toolsNodeConf.Tools) == 0 {
			a.run = func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent]) {
				var err error
				var msgs []Message
				msgs, err = a.genModelInput(ctx, instruction, input)
				if err != nil {
					generator.Send(&AgentEvent{Err: err})
					return
				}

				var msg Message
				var msgStream MessageStream
				if input.EnableStreaming {
					msgStream, err = a.model.Stream(ctx, msgs)
				} else {
					msg, err = a.model.Generate(ctx, msgs)
				}

				var event *AgentEvent
				if err == nil {
					event = EventFromMessage(msg, msgStream, schema.Assistant, "")
					if a.outputKey != "" {
						err = setOutputToSession(ctx, msg, msgStream, a.outputKey)
						if err != nil {
							generator.Send(&AgentEvent{Err: err})
						}
					}
				} else {
					event = &AgentEvent{Err: err}
				}

				generator.Send(event)

				generator.Close()

			}

			return
		}

		// react
		conf := &reactConfig{
			model:               a.model,
			toolsConfig:         &toolsNodeConf,
			toolsReturnDirectly: returnDirectly,
			agentName:           a.name,
		}

		g, err := newReact(ctx, conf)
		if err != nil {
			a.run = errFunc(err)
			return
		}

		runnable, err := g.Compile(ctx, compose.WithGraphName("React"))
		if err != nil {
			a.run = errFunc(err)
			return
		}

		a.run = func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent]) {
			var err_ error
			var msgs []Message
			msgs, err_ = a.genModelInput(ctx, instruction, input)
			if err_ != nil {
				generator.Send(&AgentEvent{Err: err_})
				return
			}

			callOpt := genReactCallbacks(a.name, generator)

			var msg Message
			var msgStream MessageStream
			if input.EnableStreaming {
				msgStream, err_ = runnable.Stream(ctx, msgs, callOpt)
			} else {
				msg, err_ = runnable.Invoke(ctx, msgs, callOpt)
			}

			if err_ == nil {
				if a.outputKey != "" {
					err_ = setOutputToSession(ctx, msg, msgStream, a.outputKey)
					if err_ != nil {
						generator.Send(&AgentEvent{Err: err})
					}
				}
			}
		}
	})

	atomic.StoreUint32(&a.frozen, 1)

	return a.run
}

func (a *ChatModelAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	run := a.buildRunFunc(ctx)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			}

			generator.Close()
		}()

		run(ctx, input, generator)
	}()

	return iterator
}
