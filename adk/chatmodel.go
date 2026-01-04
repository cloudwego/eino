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
	graphCallbacks   []callbacks.Handler

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

func WithGraphCallbacks(callbacks ...callbacks.Handler) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.graphCallbacks = callbacks
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
	// to the parent generator via a tool option injection at run-time.
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

	beforeAgents                      []func(context.Context, *AgentContext) (context.Context, error)
	beforeChatModels, afterChatModels []func(context.Context, *ChatModelAgentState) error
	onEvents                          []func(context.Context, *AgentContext, *AsyncIterator[*AgentEvent], *AsyncGenerator[*AgentEvent])

	modelRetryConfig *ModelRetryConfig

	// runner
	once   sync.Once
	run    runFunc
	helper *agentMWHelper
	frozen uint32
}

type runFunc func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, callbacks []callbacks.Handler, opts ...compose.Option)

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

	mwHelper := &chatModelMWHelper{
		instruction: config.Instruction,
		toolsConfig: config.ToolsConfig,
	}
	mwHelper = mwHelper.withMWs(config.Middlewares)

	return &ChatModelAgent{
		name:             config.Name,
		description:      config.Description,
		instruction:      mwHelper.instruction,
		model:            config.Model,
		toolsConfig:      mwHelper.toolsConfig,
		genModelInput:    genInput,
		exit:             config.Exit,
		outputKey:        config.OutputKey,
		maxIterations:    config.MaxIterations,
		beforeAgents:     mwHelper.beforeAgents,
		beforeChatModels: mwHelper.beforeChatModels,
		afterChatModels:  mwHelper.afterChatModels,
		onEvents:         mwHelper.onEvents,
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
	modelRetryConfigs *ModelRetryConfig) callbacks.Handler {

	h := &cbHandler{
		ctx:               ctx,
		addr:              core.GetCurrentAddress(ctx),
		AsyncGenerator:    generator,
		agentName:         agentName,
		store:             store,
		enableStreaming:   enableStreaming,
		modelRetryConfigs: modelRetryConfigs,
	}

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

	return cb
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

func genNoToolsCallbacks(generator *AsyncGenerator[*AgentEvent], modelRetryConfigs *ModelRetryConfig) callbacks.Handler {
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

	return cb
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
	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, _ []callbacks.Handler, _ ...compose.Option) {
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

func (a *ChatModelAgent) buildRunFunc(ctx context.Context) (*agentMWHelper, runFunc) {
	a.once.Do(func() {
		a.helper, a.run = func() (*agentMWHelper, runFunc) {
			helper := &chatModelMWHelper{
				instruction: a.instruction,
				toolsConfig: ToolsConfig{
					ToolsNodeConfig: a.toolsConfig.ToolsNodeConfig,
					ReturnDirectly:  copyMap(a.toolsConfig.ReturnDirectly),
				},
				beforeChatModels: a.beforeChatModels,
				afterChatModels:  a.afterChatModels,
				beforeAgents:     a.beforeAgents,
				onEvents:         a.onEvents,
			}

			if mws := GetGlobalAgentMiddlewares(); len(mws) > 0 {
				helper = helper.withMWs(mws)
			}

			transferToAgents := a.subAgents
			if a.parentAgent != nil && !a.disallowTransferToParent {
				transferToAgents = append(transferToAgents, a.parentAgent)
			}

			if len(transferToAgents) > 0 {
				helper = helper.withTransferToAgents(ctx, transferToAgents)
			}

			if a.exit != nil {
				var ef runFunc
				helper, ef = helper.withExitTool(ctx, a.exit)
				if ef != nil {
					return helper.toMWHelper(), ef
				}
			}

			// without tools, call chat model once
			if len(helper.toolsConfig.Tools) == 0 {
				return helper.toMWHelper(), a.buildSimpleChatModelChain(helper)
			}

			// with tools, react
			return helper.toMWHelper(), a.buildReActChain(ctx, helper)
		}()
	})

	atomic.StoreUint32(&a.frozen, 1)
	return a.helper, a.run
}

func (a *ChatModelAgent) buildSimpleChatModelChain(helper *chatModelMWHelper) runFunc {
	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, handlers []callbacks.Handler, opts ...compose.Option) {
		genState := func(ctx context.Context) *State {
			return &State{AgentName: a.name}
		}

		chatModel := a.model
		if a.modelRetryConfig != nil {
			chatModel = newRetryChatModel(a.model, a.modelRetryConfig)
		}

		modelPreHandle := func(ctx context.Context, input []Message, st *State) ([]Message, error) {
			s := &ChatModelAgentState{Messages: append(st.Messages, input...)}
			for _, bcm := range helper.beforeChatModels {
				if err := bcm(ctx, s); err != nil {
					return nil, err
				}
			}
			st.Messages = s.Messages
			return st.Messages, nil
		}

		modelPostHandle := func(ctx context.Context, input Message, st *State) (Message, error) {
			s := &ChatModelAgentState{Messages: append(st.Messages, input)}
			for _, acm := range helper.afterChatModels {
				if err := acm(ctx, s); err != nil {
					return nil, err
				}
			}
			st.Messages = s.Messages
			return input, nil
		}

		r, err := compose.NewChain[*AgentInput, Message](compose.WithGenLocalState(genState)).
			AppendLambda(compose.InvokableLambda(func(ctx context.Context, input *AgentInput) ([]Message, error) {
				return a.genModelInput(ctx, helper.instruction, input)
			})).
			AppendChatModel(chatModel,
				compose.WithStatePreHandler(modelPreHandle),
				compose.WithStatePostHandler(modelPostHandle),
			).
			Compile(ctx, compose.WithGraphName(a.name),
				compose.WithCheckPointStore(store),
				compose.WithSerializer(&gobSerializer{}))
		if err != nil {
			generator.Send(&AgentEvent{Err: err})
			return
		}

		opts = append(opts, compose.WithCallbacks(append(handlers, genNoToolsCallbacks(generator, a.modelRetryConfig))...))

		var msg Message
		var msgStream MessageStream
		if input.EnableStreaming {
			msgStream, err = r.Stream(ctx, input, opts...)
		} else {
			msg, err = r.Invoke(ctx, input, opts...)
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
}

func (a *ChatModelAgent) buildReActChain(ctx context.Context, helper *chatModelMWHelper) runFunc {
	g, err := newReact(ctx, &reactConfig{
		model:               a.model,
		toolsConfig:         &helper.toolsConfig.ToolsNodeConfig,
		toolsReturnDirectly: helper.toolsConfig.ReturnDirectly,
		agentName:           a.name,
		maxIterations:       a.maxIterations,
		beforeChatModel:     helper.beforeChatModels,
		afterChatModel:      helper.afterChatModels,
		modelRetryConfig:    a.modelRetryConfig,
	})
	if err != nil {
		return errFunc(err)
	}

	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, handlers []callbacks.Handler, opts ...compose.Option) {
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
					return a.genModelInput(ctx, helper.instruction, input)
				}),
			).
			AppendGraph(g, compose.WithNodeName("ReAct"), compose.WithGraphCompileOptions(compose.WithMaxRunSteps(math.MaxInt))).
			Compile(ctx, compileOptions...)
		if err_ != nil {
			generator.Send(&AgentEvent{Err: err_})
			return
		}

		reactCallback := genReactCallbacks(ctx, a.name, generator, input.EnableStreaming, store, a.modelRetryConfig)
		opts = append(opts, compose.WithCallbacks(append(handlers, reactCallback)...))
		if a.toolsConfig.EmitInternalEvents {
			opts = append(opts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEventGenerator(generator))))
		}
		if input.EnableStreaming {
			opts = append(opts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEnableStreaming(true))))
		}

		var msg Message
		var msgStream MessageStream
		if input.EnableStreaming {
			msgStream, err_ = runnable.Stream(ctx, input, opts...)
		} else {
			msg, err_ = runnable.Invoke(ctx, input, opts...)
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
}

func (a *ChatModelAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	agentContext := &AgentContext{
		AgentInput:      input,
		AgentRunOptions: opts,
		agentName:       a.name,
		entrance:        EntranceTypeRun,
	}

	mwHelper, run := a.buildRunFunc(ctx)

	ctx, termIter := mwHelper.execBeforeAgents(ctx, agentContext)
	if termIter != nil {
		return termIter
	}

	co, ch := getComposeOptions(agentContext.AgentRunOptions)
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

		store := newBridgeStore()
		
		run(ctx, agentContext.AgentInput, generator, store, ch, co...)
	}()

	return mwHelper.execOnEvents(ctx, agentContext, iterator)
}

func (a *ChatModelAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	agentContext := &AgentContext{
		ResumeInfo:      info,
		AgentRunOptions: opts,
		agentName:       a.name,
		entrance:        EntranceTypeResume,
	}

	mwHelper, run := a.buildRunFunc(ctx)

	ctx, termIter := mwHelper.execBeforeAgents(ctx, agentContext)
	if termIter != nil {
		return termIter
	}

	co, ch := getComposeOptions(agentContext.AgentRunOptions)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))

	if info.InterruptState == nil {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
	}

	stateByte, ok := agentContext.ResumeInfo.InterruptState.([]byte)
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

		store := newResumeBridgeStore(stateByte)
		if a.toolsConfig.EmitInternalEvents {
			co = append(co, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEventGenerator(generator))))
		}

		run(ctx, &AgentInput{EnableStreaming: info.EnableStreaming}, generator, store, ch, co...)
	}()

	return mwHelper.execOnEvents(ctx, agentContext, iterator)
}

func (a *ChatModelAgent) IsAgentMiddlewareEnabled() bool {
	return true
}

func getComposeOptions(opts []AgentRunOption) ([]compose.Option, []callbacks.Handler) {
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
	return co, o.graphCallbacks
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

type chatModelMWHelper struct {
	instruction      string
	toolsConfig      ToolsConfig
	beforeChatModels []func(context.Context, *ChatModelAgentState) error
	afterChatModels  []func(context.Context, *ChatModelAgentState) error
	beforeAgents     []func(context.Context, *AgentContext) (context.Context, error)
	onEvents         []func(context.Context, *AgentContext, *AsyncIterator[*AgentEvent], *AsyncGenerator[*AgentEvent])
}

func (c *chatModelMWHelper) withMWs(mws []AgentMiddleware) *chatModelMWHelper {
	dedup := make(map[string]struct{})
	beforeChatModels := make([]func(context.Context, *ChatModelAgentState) error, 0)
	afterChatModels := make([]func(context.Context, *ChatModelAgentState) error, 0)
	beforeAgents := make([]func(context.Context, *AgentContext) (context.Context, error), 0)
	onEvents := make([]func(context.Context, *AgentContext, *AsyncIterator[*AgentEvent], *AsyncGenerator[*AgentEvent]), 0)
	sb := &strings.Builder{}
	sb.WriteString(c.instruction)
	tc := c.toolsConfig
	for _, m := range mws {
		if _, found := dedup[m.Name]; m.Name != "" && found {
			continue
		}
		dedup[m.Name] = struct{}{}
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
		beforeAgents = append(beforeAgents, m.BeforeAgent)
		onEvents = append(onEvents, m.OnEvents)
	}

	c.instruction = sb.String()
	c.toolsConfig = tc
	c.beforeChatModels = append(beforeChatModels, c.beforeChatModels...)
	c.afterChatModels = append(afterChatModels, c.afterChatModels...)
	c.beforeAgents = append(beforeAgents, c.beforeAgents...)
	c.onEvents = append(onEvents, c.onEvents...)
	return c
}

func (c *chatModelMWHelper) withTransferToAgents(ctx context.Context, transferToAgents []Agent) *chatModelMWHelper {
	transferInstruction := genTransferToAgentInstruction(ctx, transferToAgents)
	c.instruction = concatInstructions(c.instruction, transferInstruction)
	c.toolsConfig.Tools = append(c.toolsConfig.Tools, &transferToAgent{})
	c.toolsConfig.ReturnDirectly[TransferToAgentToolName] = true

	return c
}

func (c *chatModelMWHelper) withExitTool(ctx context.Context, exitTool tool.BaseTool) (*chatModelMWHelper, runFunc) {
	c.toolsConfig.ToolsNodeConfig.Tools = append(c.toolsConfig.ToolsNodeConfig.Tools, exitTool)
	exitInfo, err := exitTool.Info(ctx)
	if err != nil {
		return nil, errFunc(err)
	}
	c.toolsConfig.ReturnDirectly[exitInfo.Name] = true

	return c, nil
}

func (c *chatModelMWHelper) toMWHelper() *agentMWHelper {
	return &agentMWHelper{
		beforeAgentFns: c.beforeAgents,
		onEventsFns:    c.onEvents,
	}
}
