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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"math"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
	ub "github.com/cloudwego/eino/utils/callbacks"
)

const (
	addrDepthChain      = 1
	addrDepthReactGraph = 2
	addrDepthChatModel  = 3
	addrDepthToolsNode  = 3
	addrDepthTool       = 4
)

type chatModelAgentRunOptions struct {
	// run
	chatModelOptions []model.Option
	toolOptions      []tool.Option
	agentToolOptions map[ /*tool name*/ string][]AgentRunOption // todo: map or list?

	// resume
	historyModifier func(context.Context, []Message) []Message
}

// WithChatModelOptions sets options for the underlying chat model.
func WithChatModelOptions(opts []model.Option) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.chatModelOptions = opts
	})
}

// WithToolOptions sets options for tools used by the chat model agent.
func WithToolOptions(opts []tool.Option) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.toolOptions = opts
	})
}

// WithAgentToolRunOptions specifies per-tool run options for the agent.
func WithAgentToolRunOptions(opts map[string] /*tool name*/ []AgentRunOption) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.agentToolOptions = opts
	})
}

// WithHistoryModifier sets a function to modify history during resume.
// Deprecated: use ResumeWithData and ChatModelAgentResumeData instead.
func WithHistoryModifier(f func(context.Context, []Message) []Message) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.historyModifier = f
	})
}

type ToolsConfig struct {
	compose.ToolsNodeConfig

	// ReturnDirectly specifies tools that cause the agent to return immediately when called.
	// If multiple listed tools are called simultaneously, only the first one triggers the return.
	// The map keys are tool names indicate whether the tool should trigger immediate return.
	ReturnDirectly map[string]bool

	// EmitInternalEvents indicates whether internal events from agentTool should be emitted
	// to the parent agent's AsyncGenerator, allowing real-time streaming of nested agent output
	// to the end-user via Runner.
	//
	// Note that these forwarded events are NOT recorded in the parent agent's runSession.
	// They are only emitted to the end-user and have no effect on the parent agent's state
	// or checkpoint.
	//
	// Action Scoping:
	// Actions emitted by the inner agent are scoped to the agent tool boundary:
	//   - Interrupted: Propagated via CompositeInterrupt to allow proper interrupt/resume
	//   - Exit, TransferToAgent, BreakLoop: Ignored outside the agent tool
	EmitInternalEvents bool
}

// GenModelInput transforms agent instructions and input into a format suitable for the model.
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

// ChatModelAgentState represents the state of a chat model agent during conversation.
type ChatModelAgentState struct {
	// Messages contains all messages in the current conversation session.
	Messages []Message
}

// AgentMiddleware provides hooks to customize agent behavior at various stages of execution.
type AgentMiddleware struct {
	// AdditionalInstruction adds supplementary text to the agent's system instruction.
	// This instruction is concatenated with the base instruction before each chat model call.
	AdditionalInstruction string

	// AdditionalTools adds supplementary tools to the agent's available toolset.
	// These tools are combined with the tools configured for the agent.
	AdditionalTools []tool.BaseTool

	// BeforeChatModel is called before each ChatModel invocation, allowing modification of the agent state.
	BeforeChatModel func(context.Context, *ChatModelAgentState) error

	// AfterChatModel is called after each ChatModel invocation, allowing modification of the agent state.
	AfterChatModel func(context.Context, *ChatModelAgentState) error

	// WrapToolCall wraps tool calls with custom middleware logic.
	// Each middleware contains Invokable and/or Streamable functions for tool calls.
	WrapToolCall compose.ToolMiddleware
}

type ChatModelAgentConfig struct {
	// Name of the agent. Better be unique across all agents.
	Name string
	// Description of the agent's capabilities.
	// Helps other agents determine whether to transfer tasks to this agent.
	Description string
	// Instruction used as the system prompt for this agent.
	// Optional. If empty, no system prompt will be used.
	// Supports f-string placeholders for session values in default GenModelInput, for example:
	// "You are a helpful assistant. The current time is {Time}. The current user is {User}."
	// These placeholders will be replaced with session values for "Time" and "User".
	Instruction string

	Model model.ToolCallingChatModel

	ToolsConfig ToolsConfig

	// GenModelInput transforms instructions and input messages into the model's input format.
	// Optional. Defaults to defaultGenModelInput which combines instruction and messages.
	GenModelInput GenModelInput

	// Exit defines the tool used to terminate the agent process.
	// Optional. If nil, no Exit Action will be generated.
	// You can use the provided 'ExitTool' implementation directly.
	Exit tool.BaseTool

	// OutputKey stores the agent's response in the session.
	// Optional. When set, stores output via AddSessionValue(ctx, outputKey, msg.Content).
	OutputKey string

	// MaxIterations defines the upper limit of ChatModel generation cycles.
	// The agent will terminate with an error if this limit is exceeded.
	// Optional. Defaults to 20.
	MaxIterations int

	// Middlewares configures agent middleware for extending functionality.
	Middlewares []AgentMiddleware

	// ModelRetryConfig configures retry behavior for the ChatModel.
	// When set, the agent will automatically retry failed ChatModel calls
	// based on the configured policy.
	// Optional. If nil, no retry will be performed.
	ModelRetryConfig *ModelRetryConfig
}

type ChatModelAgent struct {
	name        string
	description string
	instruction string

	model       model.ToolCallingChatModel
	toolsConfig ToolsConfig

	genModelInput GenModelInput

	outputKey     string
	maxIterations int

	subAgents   []Agent
	parentAgent Agent

	disallowTransferToParent bool

	exit tool.BaseTool

	beforeChatModels, afterChatModels []func(context.Context, *ChatModelAgentState) error

	modelRetryConfig *ModelRetryConfig

	// runner
	once   sync.Once
	run    runFunc
	frozen uint32
}

type runFunc func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, opts ...compose.Option)

// NewChatModelAgent constructs a chat model-backed agent with the provided config.
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

	beforeChatModels := make([]func(context.Context, *ChatModelAgentState) error, 0)
	afterChatModels := make([]func(context.Context, *ChatModelAgentState) error, 0)
	sb := &strings.Builder{}
	sb.WriteString(config.Instruction)
	tc := config.ToolsConfig
	for _, m := range config.Middlewares {
		sb.WriteString("\n")
		sb.WriteString(m.AdditionalInstruction)
		tc.Tools = append(tc.Tools, m.AdditionalTools...)

		if m.WrapToolCall.Invokable != nil || m.WrapToolCall.Streamable != nil {
			tc.ToolCallMiddlewares = append(tc.ToolCallMiddlewares, m.WrapToolCall)
		}
		if m.BeforeChatModel != nil {
			beforeChatModels = append(beforeChatModels, m.BeforeChatModel)
		}
		if m.AfterChatModel != nil {
			afterChatModels = append(afterChatModels, m.AfterChatModel)
		}
	}

	return &ChatModelAgent{
		name:             config.Name,
		description:      config.Description,
		instruction:      sb.String(),
		model:            config.Model,
		toolsConfig:      tc,
		genModelInput:    genInput,
		exit:             config.Exit,
		outputKey:        config.OutputKey,
		maxIterations:    config.MaxIterations,
		beforeChatModels: beforeChatModels,
		afterChatModels:  afterChatModels,
		modelRetryConfig: config.ModelRetryConfig,
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

	enableStreaming         bool
	store                   *bridgeStore
	returnDirectlyToolEvent atomic.Value
	ctx                     context.Context
	addr                    Address

	modelRetryConfigs *ModelRetryConfig
}

func (h *cbHandler) onChatModelEnd(ctx context.Context,
	_ *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
	addr := core.GetCurrentAddress(ctx)
	if !isAddressAtDepth(addr, h.addr, addrDepthChatModel) {
		return ctx
	}

	event := EventFromMessage(output.Message, nil, schema.Assistant, "")
	h.Send(event)
	return ctx
}

func (h *cbHandler) onChatModelEndWithStreamOutput(ctx context.Context,
	_ *callbacks.RunInfo, output *schema.StreamReader[*model.CallbackOutput]) context.Context {
	addr := core.GetCurrentAddress(ctx)
	if !isAddressAtDepth(addr, h.addr, addrDepthChatModel) {
		return ctx
	}

	var convertOpts []schema.ConvertOption
	if h.modelRetryConfigs != nil {
		retryInfo, exists := getStreamRetryInfo(ctx)
		if !exists {
			retryInfo = &streamRetryInfo{attempt: 0}
		}
		convertOpts = append(convertOpts, schema.WithErrWrapper(genErrWrapper(ctx, *h.modelRetryConfigs, *retryInfo)))
	}

	cvt := func(in *model.CallbackOutput) (Message, error) {
		return in.Message, nil
	}
	out := schema.StreamReaderWithConvert(output, cvt, convertOpts...)
	event := EventFromMessage(nil, out, schema.Assistant, "")
	h.Send(event)

	return ctx
}

func (h *cbHandler) sendReturnDirectlyToolEvent() {
	if e, ok := h.returnDirectlyToolEvent.Load().(*AgentEvent); ok && e != nil {
		h.Send(e)
	}
}

func (h *cbHandler) onToolsNodeEnd(ctx context.Context, _ *callbacks.RunInfo, _ []*schema.Message) context.Context {
	addr := core.GetCurrentAddress(ctx)
	if !isAddressAtDepth(addr, h.addr, addrDepthToolsNode) {
		return ctx
	}
	h.sendReturnDirectlyToolEvent()
	return ctx
}

func (h *cbHandler) onToolsNodeEndWithStreamOutput(ctx context.Context, _ *callbacks.RunInfo, _ *schema.StreamReader[[]*schema.Message]) context.Context {
	addr := core.GetCurrentAddress(ctx)
	if !isAddressAtDepth(addr, h.addr, addrDepthToolsNode) {
		return ctx
	}
	h.sendReturnDirectlyToolEvent()
	return ctx
}

type ChatModelAgentInterruptInfo struct { // replace temp info by info when save the data
	Info *compose.InterruptInfo
	Data []byte
}

func init() {
	schema.RegisterName[*ChatModelAgentInterruptInfo]("_eino_adk_chat_model_agent_interrupt_info")
}

func (h *cbHandler) onGraphError(ctx context.Context,
	_ *callbacks.RunInfo, err error) context.Context {
	addr := core.GetCurrentAddress(ctx)
	if !isAddressAtDepth(addr, h.addr, addrDepthChain) {
		return ctx
	}

	info, ok := compose.ExtractInterruptInfo(err)
	if !ok {
		h.Send(&AgentEvent{Err: err})
		return ctx
	}

	data, existed, err := h.store.Get(ctx, bridgeCheckpointID)
	if err != nil {
		h.Send(&AgentEvent{AgentName: h.agentName, Err: fmt.Errorf("failed to get interrupt info: %w", err)})
		return ctx
	}
	if !existed {
		h.Send(&AgentEvent{AgentName: h.agentName, Err: fmt.Errorf("interrupt occurred but checkpoint data is missing")})
		return ctx
	}

	is := FromInterruptContexts(info.InterruptContexts)

	event := CompositeInterrupt(h.ctx, info, data, is)
	event.Action.Interrupted.Data = &ChatModelAgentInterruptInfo{ // for backward-compatibility with older checkpoints
		Info: info,
		Data: data,
	}
	event.AgentName = h.agentName
	h.Send(event)

	return ctx
}

func genReactCallbacks(ctx context.Context, agentName string,
	generator *AsyncGenerator[*AgentEvent],
	enableStreaming bool,
	store *bridgeStore,
	modelRetryConfigs *ModelRetryConfig) compose.Option {

	h := &cbHandler{
		ctx:               ctx,
		addr:              core.GetCurrentAddress(ctx),
		AsyncGenerator:    generator,
		agentName:         agentName,
		store:             store,
		enableStreaming:   enableStreaming,
		modelRetryConfigs: modelRetryConfigs}

	cmHandler := &ub.ModelCallbackHandler{
		OnEnd:                 h.onChatModelEnd,
		OnEndWithStreamOutput: h.onChatModelEndWithStreamOutput,
	}
	toolsNodeHandler := &ub.ToolsNodeCallbackHandlers{
		OnEnd:                 h.onToolsNodeEnd,
		OnEndWithStreamOutput: h.onToolsNodeEndWithStreamOutput,
	}
	createToolResultSender := func() adkToolResultSender {
		return func(toolCtx context.Context, toolName, callID, result string, prePopAction *AgentAction) {
			msg := schema.ToolMessage(result, callID, schema.WithToolName(toolName))
			event := EventFromMessage(msg, nil, schema.Tool, toolName)

			if prePopAction != nil {
				event.Action = prePopAction
			} else {
				event.Action = popToolGenAction(toolCtx, toolName)
			}

			returnDirectlyID, hasReturnDirectly := getReturnDirectlyToolCallID(toolCtx)
			if hasReturnDirectly && returnDirectlyID == callID {
				h.returnDirectlyToolEvent.Store(event)
			} else {
				h.Send(event)
			}
		}
	}
	createStreamToolResultSender := func() adkStreamToolResultSender {
		return func(toolCtx context.Context, toolName, callID string, resultStream *schema.StreamReader[string], prePopAction *AgentAction) {
			cvt := func(in string) (Message, error) {
				return schema.ToolMessage(in, callID, schema.WithToolName(toolName)), nil
			}
			msgStream := schema.StreamReaderWithConvert(resultStream, cvt)
			event := EventFromMessage(nil, msgStream, schema.Tool, toolName)
			event.Action = prePopAction

			returnDirectlyID, hasReturnDirectly := getReturnDirectlyToolCallID(toolCtx)
			if hasReturnDirectly && returnDirectlyID == callID {
				h.returnDirectlyToolEvent.Store(event)
			} else {
				h.Send(event)
			}
		}
	}
	reactGraphHandler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			currentAddr := core.GetCurrentAddress(ctx)
			if !isAddressAtDepth(currentAddr, h.addr, addrDepthReactGraph) {
				return ctx
			}
			return setToolResultSendersToCtx(ctx, h.addr, createToolResultSender(), createStreamToolResultSender())
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			currentAddr := core.GetCurrentAddress(ctx)
			if !isAddressAtDepth(currentAddr, h.addr, addrDepthReactGraph) {
				return ctx
			}
			return setToolResultSendersToCtx(ctx, h.addr, createToolResultSender(), createStreamToolResultSender())
		}).Build()
	chainHandler := callbacks.NewHandlerBuilder().OnErrorFn(h.onGraphError).Build()

	cb := ub.NewHandlerHelper().ChatModel(cmHandler).ToolsNode(toolsNodeHandler).Graph(reactGraphHandler).Chain(chainHandler).Handler()

	return compose.WithCallbacks(cb)
}

type noToolsCbHandler struct {
	*AsyncGenerator[*AgentEvent]
	modelRetryConfigs *ModelRetryConfig
}

func (h *noToolsCbHandler) onChatModelEnd(ctx context.Context,
	_ *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
	event := EventFromMessage(output.Message, nil, schema.Assistant, "")
	h.Send(event)
	return ctx
}

func (h *noToolsCbHandler) onChatModelEndWithStreamOutput(ctx context.Context,
	_ *callbacks.RunInfo, output *schema.StreamReader[*model.CallbackOutput]) context.Context {
	var convertOpts []schema.ConvertOption
	if h.modelRetryConfigs != nil {
		retryInfo, exists := getStreamRetryInfo(ctx)
		if !exists {
			retryInfo = &streamRetryInfo{attempt: 0}
		}
		convertOpts = append(convertOpts, schema.WithErrWrapper(genErrWrapper(ctx, *h.modelRetryConfigs, *retryInfo)))
	}

	cvt := func(in *model.CallbackOutput) (Message, error) {
		return in.Message, nil
	}
	out := schema.StreamReaderWithConvert(output, cvt, convertOpts...)
	event := EventFromMessage(nil, out, schema.Assistant, "")
	h.Send(event)
	return ctx
}

func (h *noToolsCbHandler) onGraphError(ctx context.Context,
	_ *callbacks.RunInfo, err error) context.Context {
	h.Send(&AgentEvent{Err: err})
	return ctx
}

func genNoToolsCallbacks(generator *AsyncGenerator[*AgentEvent], modelRetryConfigs *ModelRetryConfig) compose.Option {
	h := &noToolsCbHandler{
		AsyncGenerator:    generator,
		modelRetryConfigs: modelRetryConfigs,
	}

	cmHandler := &ub.ModelCallbackHandler{
		OnEnd:                 h.onChatModelEnd,
		OnEndWithStreamOutput: h.onChatModelEndWithStreamOutput,
	}
	graphHandler := callbacks.NewHandlerBuilder().OnErrorFn(h.onGraphError).Build()

	cb := ub.NewHandlerHelper().ChatModel(cmHandler).Chain(graphHandler).Handler()

	return compose.WithCallbacks(cb)
}

func setOutputToSession(ctx context.Context, msg Message, msgStream MessageStream, outputKey string) error {
	if msg != nil {
		AddSessionValue(ctx, outputKey, msg.Content)
		return nil
	}

	concatenated, err := schema.ConcatMessageStream(msgStream)
	if err != nil {
		return err
	}

	AddSessionValue(ctx, outputKey, concatenated.Content)
	return nil
}

func errFunc(err error) runFunc {
	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, _ ...compose.Option) {
		generator.Send(&AgentEvent{Err: err})
	}
}

// ChatModelAgentResumeData holds data that can be provided to a ChatModelAgent during a resume operation
// to modify its behavior. It is provided via the adk.ResumeWithData function.
type ChatModelAgentResumeData struct {
	// HistoryModifier is a function that can transform the agent's message history before it is sent to the model.
	// This allows for adding new information or context upon resumption.
	HistoryModifier func(ctx context.Context, history []Message) []Message
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
			var chatModel model.ToolCallingChatModel = a.model
			if a.modelRetryConfig != nil {
				chatModel = newRetryChatModel(a.model, a.modelRetryConfig)
			}

			a.run = func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent],
				store *bridgeStore, opts ...compose.Option) {
				r, err := compose.NewChain[*AgentInput, Message](compose.WithGenLocalState(func(ctx context.Context) (state *ChatModelAgentState) {
					return &ChatModelAgentState{}
				})).
					AppendLambda(compose.InvokableLambda(func(ctx context.Context, input *AgentInput) ([]Message, error) {
						messages, err := a.genModelInput(ctx, instruction, input)
						if err != nil {
							return nil, err
						}
						return messages, nil
					})).
					AppendChatModel(
						chatModel,
						compose.WithStatePreHandler(func(ctx context.Context, in []*schema.Message, state *ChatModelAgentState) ([]*schema.Message, error) {
							state.Messages = in
							for _, bc := range a.beforeChatModels {
								err := bc(ctx, state)
								if err != nil {
									return nil, err
								}
							}
							return state.Messages, nil
						}),
						compose.WithStatePostHandler(func(ctx context.Context, in *schema.Message, state *ChatModelAgentState) (*schema.Message, error) {
							state.Messages = append(state.Messages, in)
							for _, ac := range a.afterChatModels {
								err := ac(ctx, state)
								if err != nil {
									return nil, err
								}
							}
							return state.Messages[len(state.Messages)-1], nil
						}),
					).
					Compile(ctx, compose.WithGraphName(a.name),
						compose.WithCheckPointStore(store),
						compose.WithSerializer(&gobSerializer{}))
				if err != nil {
					generator.Send(&AgentEvent{Err: err})
					return
				}

				callOpt := genNoToolsCallbacks(generator, a.modelRetryConfig)
				var runOpts []compose.Option
				runOpts = append(runOpts, opts...)
				runOpts = append(runOpts, callOpt)

				var msg Message
				var msgStream MessageStream
				if input.EnableStreaming {
					msgStream, err = r.Stream(ctx, input, runOpts...)
				} else {
					msg, err = r.Invoke(ctx, input, runOpts...)
				}

				if err == nil {
					if a.outputKey != "" {
						err = setOutputToSession(ctx, msg, msgStream, a.outputKey)
						if err != nil {
							generator.Send(&AgentEvent{Err: err})
						}
					} else if msgStream != nil {
						msgStream.Close()
					}
				}

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
			maxIterations:       a.maxIterations,
			beforeChatModel:     a.beforeChatModels,
			afterChatModel:      a.afterChatModels,
			modelRetryConfig:    a.modelRetryConfig,
		}

		g, err := newReact(ctx, conf)
		if err != nil {
			a.run = errFunc(err)
			return
		}

		a.run = func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore,
			opts ...compose.Option) {
			var compileOptions []compose.GraphCompileOption
			compileOptions = append(compileOptions,
				compose.WithGraphName(a.name),
				compose.WithCheckPointStore(store),
				compose.WithSerializer(&gobSerializer{}),
				// ensure the graph won't exceed max steps due to max iterations
				compose.WithMaxRunSteps(math.MaxInt))

			runnable, err_ := compose.NewChain[*AgentInput, Message]().
				AppendLambda(
					compose.InvokableLambda(func(ctx context.Context, input *AgentInput) ([]Message, error) {
						return a.genModelInput(ctx, instruction, input)
					}),
				).
				AppendGraph(g, compose.WithNodeName("ReAct"), compose.WithGraphCompileOptions(compose.WithMaxRunSteps(math.MaxInt))).
				Compile(ctx, compileOptions...)
			if err_ != nil {
				generator.Send(&AgentEvent{Err: err_})
				return
			}

			callOpt := genReactCallbacks(ctx, a.name, generator, input.EnableStreaming, store, a.modelRetryConfig)
			var runOpts []compose.Option
			runOpts = append(runOpts, opts...)
			runOpts = append(runOpts, callOpt)
			if a.toolsConfig.EmitInternalEvents {
				runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEventGenerator(generator))))
			}
			if input.EnableStreaming {
				runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEnableStreaming(true))))
			}

			var msg Message
			var msgStream MessageStream
			if input.EnableStreaming {
				msgStream, err_ = runnable.Stream(ctx, input, runOpts...)
			} else {
				msg, err_ = runnable.Invoke(ctx, input, runOpts...)
			}

			if err_ == nil {
				if a.outputKey != "" {
					err_ = setOutputToSession(ctx, msg, msgStream, a.outputKey)
					if err_ != nil {
						generator.Send(&AgentEvent{Err: err_})
					}
				} else if msgStream != nil {
					msgStream.Close()
				}
			}

			generator.Close()
		}
	})

	atomic.StoreUint32(&a.frozen, 1)

	return a.run
}

func (a *ChatModelAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	run := a.buildRunFunc(ctx)

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))

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

		run(ctx, input, generator, newBridgeStore(), co...)
	}()

	return iterator
}

func (a *ChatModelAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	run := a.buildRunFunc(ctx)

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))

	if info.InterruptState == nil {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
	}

	stateByte, ok := info.InterruptState.([]byte)
	if !ok {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has invalid interrupt state type: %T",
			a.Name(ctx), info.InterruptState))
	}

	if info.ResumeData != nil {
		resumeData, ok := info.ResumeData.(*ChatModelAgentResumeData)
		if !ok {
			panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has invalid resume data type: %T",
				a.Name(ctx), info.ResumeData))
		}

		if resumeData.HistoryModifier != nil {
			co = append(co, compose.WithStateModifier(func(ctx context.Context, path compose.NodePath, state any) error {
				s, ok := state.(*State)
				if !ok {
					return fmt.Errorf("unexpected state type: %T, expected: %T", state, &State{})
				}
				s.Messages = resumeData.HistoryModifier(ctx, s.Messages)
				return nil
			}))
		}
	}

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

		run(ctx, &AgentInput{EnableStreaming: info.EnableStreaming}, generator,
			newResumeBridgeStore(stateByte), co...)
	}()

	return iterator
}

func getComposeOptions(opts []AgentRunOption) []compose.Option {
	o := GetImplSpecificOptions[chatModelAgentRunOptions](nil, opts...)
	var co []compose.Option
	if len(o.chatModelOptions) > 0 {
		co = append(co, compose.WithChatModelOption(o.chatModelOptions...))
	}
	var to []tool.Option
	if len(o.toolOptions) > 0 {
		to = append(to, o.toolOptions...)
	}
	for toolName, atos := range o.agentToolOptions {
		to = append(to, withAgentToolOptions(toolName, atos))
	}
	if len(to) > 0 {
		co = append(co, compose.WithToolsNodeOption(compose.WithToolOption(to...)))
	}
	if o.historyModifier != nil {
		co = append(co, compose.WithStateModifier(func(ctx context.Context, path compose.NodePath, state any) error {
			s, ok := state.(*State)
			if !ok {
				return fmt.Errorf("unexpected state type: %T, expected: %T", state, &State{})
			}
			s.Messages = o.historyModifier(ctx, s.Messages)
			return nil
		}))
	}
	return co
}

type gobSerializer struct{}

func (g *gobSerializer) Marshal(v any) ([]byte, error) {
	buf := new(bytes.Buffer)
	err := gob.NewEncoder(buf).Encode(v)
	if err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (g *gobSerializer) Unmarshal(data []byte, v any) error {
	buf := bytes.NewBuffer(data)
	return gob.NewDecoder(buf).Decode(v)
}
