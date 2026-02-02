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
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type chatModelAgentExecCtx struct {
	runtimeReturnDirectly map[string]bool
	generator             *AsyncGenerator[*AgentEvent]
}

func (e *chatModelAgentExecCtx) send(event *AgentEvent) {
	if e != nil && e.generator != nil {
		e.generator.Send(event)
	}
}

type chatModelAgentExecCtxKey struct{}

func withChatModelAgentExecCtx(ctx context.Context, execCtx *chatModelAgentExecCtx) context.Context {
	return context.WithValue(ctx, chatModelAgentExecCtxKey{}, execCtx)
}

func getChatModelAgentExecCtx(ctx context.Context) *chatModelAgentExecCtx {
	if v := ctx.Value(chatModelAgentExecCtxKey{}); v != nil {
		return v.(*chatModelAgentExecCtx)
	}
	return nil
}

type chatModelAgentRunOptions struct {
	chatModelOptions []model.Option
	toolOptions      []tool.Option
	agentToolOptions map[string][]AgentRunOption

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
func WithAgentToolRunOptions(opts map[string][]AgentRunOption) AgentRunOption {
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
				return nil, fmt.Errorf("defaultGenModelInput: failed to format instruction using FString template. "+
					"This formatting is triggered automatically when SessionValues are present. "+
					"If your instruction contains literal curly braces (e.g., JSON), provide a custom GenModelInput that uses another format. If you are using "+
					"SessionValues for purposes other than instruction formatting, provide a custom GenModelInput that does no formatting at all: %w", err)
			}

			sp = ms[0]
		}

		msgs = append(msgs, sp)
	}

	msgs = append(msgs, input.Messages...)

	return msgs, nil
}

// ChatModelAgentState represents the state of a chat model agent during conversation.
// This is the primary state type for both ChatModelAgentMiddleware and AgentMiddleware callbacks.
type ChatModelAgentState struct {
	// Messages contains all messages in the current conversation session.
	Messages []Message
}

// AgentMiddleware provides hooks to customize agent behavior at various stages of execution.
//
// Limitations of AgentMiddleware (struct-based):
//   - Struct types are closed: users cannot add new methods
//   - Callbacks only return error, cannot return modified context
//   - Configuration is scattered across closures when using factory functions
//
// For new code requiring extensibility, consider using ChatModelAgentMiddleware (interface-based) instead.
// AgentMiddleware is kept for backward compatibility and remains suitable for simple,
// static additions like extra instruction or tools.
//
// See ChatModelAgentMiddleware documentation for detailed comparison.
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

	// Model is the chat model used by the agent.
	// If your ChatModelAgent uses any tools, this model must support the model.WithTools
	// call option, as that's how ChatModelAgent configures the model with tool information.
	Model model.BaseChatModel

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
	// Use for simple, static additions like extra instruction or tools.
	// Kept for backward compatibility; for new code, consider using Handlers instead.
	Middlewares []AgentMiddleware

	// Handlers configures interface-based handlers for extending agent behavior.
	// Unlike Middlewares (struct-based), Handlers allow users to:
	//   - Add custom methods to their handler implementations
	//   - Return modified context from handler methods
	//   - Centralize configuration in struct fields instead of closures
	//
	// Handlers are processed after Middlewares, in registration order.
	// See ChatModelAgentMiddleware documentation for when to use Handlers vs Middlewares.
	//
	// Execution Order (relative to AgentMiddleware and ToolsConfig):
	//
	// Model call lifecycle (outermost to innermost wrapper chain):
	//  1. AgentMiddleware.BeforeChatModel (hook, runs before model call)
	//  2. ChatModelAgentMiddleware.BeforeModelRewriteState (hook, can modify state before model call)
	//  3. retryModelWrapper (internal - retries on failure, if configured)
	//  4. eventSenderModelWrapper pre-processing (internal - prepares event sending)
	//  5. ChatModelAgentMiddleware.WrapModel pre-processing (wrapper, first registered runs first)
	//  6. callbackInjectionModelWrapper (internal - injects callbacks if not enabled)
	//  7. Model.Generate/Stream
	//  8. callbackInjectionModelWrapper post-processing
	//  9. ChatModelAgentMiddleware.WrapModel post-processing (wrapper, first registered runs last)
	// 10. eventSenderModelWrapper post-processing (internal - sends model response events)
	// 11. retryModelWrapper post-processing (internal - handles retry logic)
	// 12. ChatModelAgentMiddleware.AfterModelRewriteState (hook, can modify state after model call)
	// 13. AgentMiddleware.AfterChatModel (hook, runs after model call)
	//
	// Tool call lifecycle (outermost to innermost):
	//  1. eventSenderToolHandler (internal ToolMiddleware - sends tool result events after all processing)
	//  2. ToolsConfig.ToolCallMiddlewares (ToolMiddleware)
	//  3. AgentMiddleware.WrapToolCall (ToolMiddleware)
	//  4. ChatModelAgentMiddleware.WrapToolCall (wrapper, first registered is outermost)
	//  5. callbackInjectedToolCall (internal - injects callbacks if tool doesn't handle them)
	//  6. Tool.InvokableRun/StreamableRun
	//
	// Tool List Modification:
	//
	// There are two ways to modify the tool list:
	//
	//  1. In BeforeAgent: Modify ChatModelAgentContext.Tools ([]tool.BaseTool) directly. This affects
	//     both the tool info list passed to ChatModel AND the actual tools available for
	//     execution. Changes persist for the entire agent run.
	//
	//  2. In WrapModel: Create a model wrapper that modifies the tool info list per model
	//     request using model.WithTools(toolInfos). This ONLY affects the tool info list
	//     passed to ChatModel, NOT the actual tools available for execution. Use this for
	//     dynamic tool filtering/selection based on conversation context. The modification
	//     is scoped to this model request only.
	Handlers []ChatModelAgentMiddleware

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

	model       model.BaseChatModel
	toolsConfig ToolsConfig

	genModelInput GenModelInput

	outputKey     string
	maxIterations int

	subAgents   []Agent
	parentAgent Agent

	disallowTransferToParent bool

	exit tool.BaseTool

	handlers    []handlerInfo
	middlewares []AgentMiddleware

	modelRetryConfig *ModelRetryConfig

	once   sync.Once
	run    runFunc
	frozen uint32
	exeCtx *execContext
}

type runFunc func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, instruction string, returnDirectly map[string]bool, opts ...compose.Option)

// NewChatModelAgent constructs a chat model-backed agent with the provided config.
func NewChatModelAgent(ctx context.Context, config *ChatModelAgentConfig) (*ChatModelAgent, error) {
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

	handlerInfos := make([]handlerInfo, len(config.Handlers))
	for i, h := range config.Handlers {
		handlerInfos[i] = newHandlerInfo(h)
	}

	tc := config.ToolsConfig

	// Tool call middleware execution order (outermost to innermost):
	// 1. eventSenderToolHandler (internal - sends tool result events after all modifications)
	// 2. User-provided ToolsConfig.ToolCallMiddlewares (original order preserved)
	// 3. Middlewares' WrapToolCall (in registration order)
	// 4. ChatModelAgentMiddleware.WrapToolCall (in registration order)
	// 5. callbackInjectedToolCall (internal - injects callbacks if tool doesn't handle them)
	eventSender := &eventSenderToolHandler{}
	tc.ToolCallMiddlewares = append(
		[]compose.ToolMiddleware{{Invokable: eventSender.WrapInvokableToolCall,
			Streamable:         eventSender.WrapStreamableToolCall,
			EnhancedInvokable:  eventSender.WrapEnhancedInvokableToolCall,
			EnhancedStreamable: eventSender.WrapEnhancedStreamableToolCall,
		}},
		tc.ToolCallMiddlewares...,
	)
	tc.ToolCallMiddlewares = append(tc.ToolCallMiddlewares, collectToolMiddlewaresFromMiddlewares(config.Middlewares)...)

	return &ChatModelAgent{
		name:             config.Name,
		description:      config.Description,
		instruction:      config.Instruction,
		model:            config.Model,
		toolsConfig:      tc,
		genModelInput:    genInput,
		exit:             config.Exit,
		outputKey:        config.OutputKey,
		maxIterations:    config.MaxIterations,
		handlers:         handlerInfos,
		middlewares:      config.Middlewares,
		modelRetryConfig: config.ModelRetryConfig,
	}, nil
}

func collectToolMiddlewaresFromMiddlewares(mws []AgentMiddleware) []compose.ToolMiddleware {
	var middlewares []compose.ToolMiddleware
	for _, m := range mws {
		if m.WrapToolCall.Invokable == nil && m.WrapToolCall.Streamable == nil {
			continue
		}
		middlewares = append(middlewares, m.WrapToolCall)
	}
	return middlewares
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

func (a *ChatModelAgent) GetType() string {
	return "ChatModel"
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

type ChatModelAgentInterruptInfo struct {
	Info *compose.InterruptInfo
	Data []byte
}

func init() {
	schema.RegisterName[*ChatModelAgentInterruptInfo]("_eino_adk_chat_model_agent_interrupt_info")
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
	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore, _ string, _ map[string]bool, _ ...compose.Option) {
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

type execContext struct {
	instruction    string
	toolsNodeConf  compose.ToolsNodeConfig
	returnDirectly map[string]bool

	toolInfos      []*schema.ToolInfo
	unwrappedTools []tool.BaseTool

	rebuildGraph bool // whether needs to instantiate a new graph because of topology changes due to tool modifications
	toolUpdated  bool // whether needs to pass a compose.WithToolList option to ToolsNode due to tool list change
}

func (a *ChatModelAgent) applyBeforeAgent(ctx context.Context, ec *execContext) (context.Context, *execContext, error) {
	runCtx := &ChatModelAgentContext{
		Instruction:    ec.instruction,
		Tools:          cloneSlice(ec.unwrappedTools),
		ReturnDirectly: copyMap(ec.returnDirectly),
	}

	var err error
	for i, info := range a.handlers {
		if info.hasBeforeAgent {
			ctx, runCtx, err = info.handler.BeforeAgent(ctx, runCtx)
			if err != nil {
				return ctx, nil, fmt.Errorf("handler[%d] (%T) BeforeAgent failed: %w", i, info.handler, err)
			}
		}
	}

	runtimeEC := &execContext{
		instruction: runCtx.Instruction,
		toolsNodeConf: compose.ToolsNodeConfig{
			Tools:               runCtx.Tools,
			ToolCallMiddlewares: cloneSlice(ec.toolsNodeConf.ToolCallMiddlewares),
		},
		returnDirectly: runCtx.ReturnDirectly,
		toolUpdated:    true,
		rebuildGraph: (len(ec.toolsNodeConf.Tools) == 0 && len(runCtx.Tools) > 0) ||
			(len(ec.returnDirectly) == 0 && len(runCtx.ReturnDirectly) > 0),
	}

	toolInfos, err := genToolInfos(ctx, &runtimeEC.toolsNodeConf)
	if err != nil {
		return ctx, nil, err
	}

	runtimeEC.toolInfos = toolInfos

	return ctx, runtimeEC, nil
}

func (a *ChatModelAgent) prepareExecContext(ctx context.Context) (*execContext, error) {
	instruction := a.instruction
	toolsNodeConf := compose.ToolsNodeConfig{
		Tools:                cloneSlice(a.toolsConfig.Tools),
		ToolCallMiddlewares:  cloneSlice(a.toolsConfig.ToolCallMiddlewares),
		UnknownToolsHandler:  a.toolsConfig.UnknownToolsHandler,
		ExecuteSequentially:  a.toolsConfig.ExecuteSequentially,
		ToolArgumentsHandler: a.toolsConfig.ToolArgumentsHandler,
	}
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
			return nil, err
		}
		returnDirectly[exitInfo.Name] = true
	}

	for _, m := range a.middlewares {
		if m.AdditionalInstruction != "" {
			instruction = concatInstructions(instruction, m.AdditionalInstruction)
		}
		toolsNodeConf.Tools = append(toolsNodeConf.Tools, m.AdditionalTools...)
	}

	unwrappedTools := cloneSlice(toolsNodeConf.Tools)

	handlerMiddlewares := handlersToToolMiddlewares(a.handlers)
	toolsNodeConf.ToolCallMiddlewares = append(toolsNodeConf.ToolCallMiddlewares, handlerMiddlewares...)

	toolInfos, err := genToolInfos(ctx, &toolsNodeConf)
	if err != nil {
		return nil, err
	}

	return &execContext{
		instruction:    instruction,
		toolsNodeConf:  toolsNodeConf,
		returnDirectly: returnDirectly,
		toolInfos:      toolInfos,
		unwrappedTools: unwrappedTools,
	}, nil
}

func hasAnyAfterAgent(handlers []handlerInfo) bool {
	for _, h := range handlers {
		if h.hasAfterAgent {
			return true
		}
	}
	return false
}

func invokeAfterAgentHandlers(ctx context.Context, handlers []handlerInfo, state *ChatModelAgentState) error {
	for i, info := range handlers {
		if info.hasAfterAgent {
			if err := info.handler.AfterAgent(ctx, state); err != nil {
				return fmt.Errorf("handler[%d] (%T) AfterAgent failed: %w", i, info.handler, err)
			}
		}
	}
	return nil
}

func (a *ChatModelAgent) buildNoToolsRunFunc(_ context.Context) runFunc {
	wrappedModel := buildModelWrappers(a.model, &modelWrapperConfig{
		handlers:    a.handlers,
		middlewares: a.middlewares,
		retryConfig: a.modelRetryConfig,
	})

	type noToolsInput struct {
		input       *AgentInput
		instruction string
	}

	handlers := a.handlers
	hasAfterAgent := hasAnyAfterAgent(handlers)

	afterAgentLambda := compose.TransformableLambda(func(ctx context.Context, msgStream MessageStream) (MessageStream, error) {
		if !hasAfterAgent {
			return msgStream, nil
		}

		var stateMessages []Message
		_ = compose.ProcessState(ctx, func(_ context.Context, st *State) error {
			stateMessages = st.Messages
			return nil
		})

		state := &ChatModelAgentState{Messages: stateMessages}

		if err := invokeAfterAgentHandlers(ctx, handlers, state); err != nil {
			return nil, err
		}

		return msgStream, nil
	})

	chain := compose.NewChain[noToolsInput, Message](
		compose.WithGenLocalState(func(ctx context.Context) (state *State) {
			return &State{}
		})).
		AppendLambda(compose.InvokableLambda(func(ctx context.Context, in noToolsInput) ([]Message, error) {
			messages, err := a.genModelInput(ctx, in.instruction, in.input)
			if err != nil {
				return nil, err
			}
			return messages, nil
		})).
		AppendChatModel(wrappedModel).
		AppendLambda(afterAgentLambda)

	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent],
		store *bridgeStore, instruction string, _ map[string]bool, opts ...compose.Option) {

		r, err := chain.Compile(ctx, compose.WithGraphName(a.name),
			compose.WithCheckPointStore(store),
			compose.WithSerializer(&gobSerializer{}))
		if err != nil {
			generator.Send(&AgentEvent{Err: err})
			return
		}

		ctx = withChatModelAgentExecCtx(ctx, &chatModelAgentExecCtx{
			generator: generator,
		})

		in := noToolsInput{input: input, instruction: instruction}

		var msg Message
		var msgStream MessageStream
		if input.EnableStreaming {
			msgStream, err = r.Stream(ctx, in, opts...)
		} else {
			msg, err = r.Invoke(ctx, in, opts...)
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
		} else {
			generator.Send(&AgentEvent{Err: err})
		}
	}
}

func (a *ChatModelAgent) buildReactRunFunc(ctx context.Context, bc *execContext) (runFunc, error) {
	conf := &reactConfig{
		model:       a.model,
		toolsConfig: &bc.toolsNodeConf,
		modelWrapperConf: &modelWrapperConfig{
			handlers:    a.handlers,
			middlewares: a.middlewares,
			retryConfig: a.modelRetryConfig,
			toolInfos:   bc.toolInfos,
		},
		toolsReturnDirectly: bc.returnDirectly,
		agentName:           a.name,
		maxIterations:       a.maxIterations,
	}

	g, err := newReact(ctx, conf)
	if err != nil {
		return nil, err
	}

	type reactRunInput struct {
		input       *AgentInput
		instruction string
	}

	chain := compose.NewChain[reactRunInput, Message]().
		AppendLambda(
			compose.InvokableLambda(func(ctx context.Context, in reactRunInput) (*reactInput, error) {
				messages, err := a.genModelInput(ctx, in.instruction, in.input)
				if err != nil {
					return nil, err
				}
				return &reactInput{
					messages: messages,
				}, nil
			}),
		).
		AppendGraph(g, compose.WithNodeName("ReAct"), compose.WithGraphCompileOptions(compose.WithMaxRunSteps(math.MaxInt)))

	return func(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent], store *bridgeStore,
		instruction string, returnDirectly map[string]bool, opts ...compose.Option) {
		var compileOptions []compose.GraphCompileOption
		compileOptions = append(compileOptions,
			compose.WithGraphName(a.name),
			compose.WithCheckPointStore(store),
			compose.WithSerializer(&gobSerializer{}),
			compose.WithMaxRunSteps(math.MaxInt))

		runnable, err_ := chain.Compile(ctx, compileOptions...)
		if err_ != nil {
			generator.Send(&AgentEvent{Err: err_})
			return
		}

		ctx = withChatModelAgentExecCtx(ctx, &chatModelAgentExecCtx{
			runtimeReturnDirectly: returnDirectly,
			generator:             generator,
		})

		in := reactRunInput{
			input:       input,
			instruction: instruction,
		}

		var runOpts []compose.Option
		runOpts = append(runOpts, opts...)
		if a.toolsConfig.EmitInternalEvents {
			runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEventGenerator(generator))))
		}
		if input.EnableStreaming {
			runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEnableStreaming(true))))
		}

		var msg Message
		var msgStream MessageStream
		if input.EnableStreaming {
			msgStream, err_ = runnable.Stream(ctx, in, runOpts...)
		} else {
			msg, err_ = runnable.Invoke(ctx, in, runOpts...)
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

			return
		}

		info, ok := compose.ExtractInterruptInfo(err_)
		if !ok {
			generator.Send(&AgentEvent{Err: err_})
			return
		}

		data, existed, err := store.Get(ctx, bridgeCheckpointID)
		if err != nil {
			generator.Send(&AgentEvent{AgentName: a.name, Err: fmt.Errorf("failed to get interrupt info: %w", err)})
			return
		}
		if !existed {
			generator.Send(&AgentEvent{AgentName: a.name, Err: fmt.Errorf("interrupt occurred but checkpoint data is missing")})
			return
		}

		is := FromInterruptContexts(info.InterruptContexts)

		event := CompositeInterrupt(ctx, info, data, is)
		event.Action.Interrupted.Data = &ChatModelAgentInterruptInfo{
			Info: info,
			Data: data,
		}
		event.AgentName = a.name
		generator.Send(event)
	}, nil
}

func (a *ChatModelAgent) buildRunFunc(ctx context.Context) runFunc {
	a.once.Do(func() {
		ec, err := a.prepareExecContext(ctx)
		if err != nil {
			a.run = errFunc(err)
			return
		}

		a.exeCtx = ec

		if len(ec.toolsNodeConf.Tools) == 0 {
			a.run = a.buildNoToolsRunFunc(ctx)
			return
		}

		run, err := a.buildReactRunFunc(ctx, ec)
		if err != nil {
			a.run = errFunc(err)
			return
		}
		a.run = run
	})

	atomic.StoreUint32(&a.frozen, 1)

	return a.run
}

func (a *ChatModelAgent) getRunFunc(ctx context.Context) (context.Context, runFunc, *execContext, error) {
	defaultRun := a.buildRunFunc(ctx)
	bc := a.exeCtx

	if len(a.handlers) == 0 {
		runtimeBC := &execContext{
			instruction:    bc.instruction,
			toolsNodeConf:  bc.toolsNodeConf,
			returnDirectly: bc.returnDirectly,
			toolInfos:      bc.toolInfos,
		}
		return ctx, defaultRun, runtimeBC, nil
	}

	ctx, runtimeBC, err := a.applyBeforeAgent(ctx, bc)
	if err != nil {
		return ctx, nil, nil, err
	}

	if !runtimeBC.rebuildGraph {
		return ctx, defaultRun, runtimeBC, nil
	}

	var tempRun runFunc
	if len(runtimeBC.toolsNodeConf.Tools) == 0 {
		tempRun = a.buildNoToolsRunFunc(ctx)
	} else {
		tempRun, err = a.buildReactRunFunc(ctx, runtimeBC)
		if err != nil {
			return ctx, nil, nil, err
		}
	}

	return ctx, tempRun, runtimeBC, nil
}

func (a *ChatModelAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	ctx, run, bc, err := a.getRunFunc(ctx)
	if err != nil {
		go func() {
			generator.Send(&AgentEvent{Err: err})
			generator.Close()
		}()
		return iterator
	}

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))
	co = append(co, compose.WithChatModelOption(model.WithTools(bc.toolInfos)))
	if bc.toolUpdated {
		co = append(co, compose.WithToolsNodeOption(compose.WithToolList(bc.toolsNodeConf.Tools...)))
	}

	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			}

			generator.Close()
		}()

		run(ctx, input, generator, newBridgeStore(), bc.instruction, bc.returnDirectly, co...)
	}()

	return iterator
}

func (a *ChatModelAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	ctx, run, bc, err := a.getRunFunc(ctx)
	if err != nil {
		go func() {
			generator.Send(&AgentEvent{Err: err})
			generator.Close()
		}()
		return iterator
	}

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))
	co = append(co, compose.WithChatModelOption(model.WithTools(bc.toolInfos)))
	if bc.toolUpdated {
		co = append(co, compose.WithToolsNodeOption(compose.WithToolList(bc.toolsNodeConf.Tools...)))
	}

	if info.InterruptState == nil {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
	}

	stateByte, ok := info.InterruptState.([]byte)
	if !ok {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has invalid interrupt state type: %T",
			a.Name(ctx), info.InterruptState))
	}

	var historyModifier func(ctx context.Context, history []Message) []Message
	if info.ResumeData != nil {
		resumeData, ok := info.ResumeData.(*ChatModelAgentResumeData)
		if !ok {
			panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has invalid resume data type: %T",
				a.Name(ctx), info.ResumeData))
		}
		historyModifier = resumeData.HistoryModifier
	}

	if historyModifier != nil {
		co = append(co, compose.WithStateModifier(func(ctx context.Context, path compose.NodePath, state any) error {
			s, ok := state.(*State)
			if !ok {
				return nil
			}
			s.Messages = historyModifier(ctx, s.Messages)
			return nil
		}))
	}

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
			newResumeBridgeStore(stateByte), bc.instruction, bc.returnDirectly, co...)
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
