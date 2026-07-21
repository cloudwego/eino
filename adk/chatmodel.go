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
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

var _ ResumableAgent = &TypedChatModelAgent[*schema.Message]{}
var _ TypedResumableAgent[*schema.AgenticMessage] = &TypedChatModelAgent[*schema.AgenticMessage]{}

type typedChatModelAgentExecCtx[M MessageType] struct {
	runtimeReturnDirectly map[string]bool
	generator             *AsyncGenerator[*TypedAgentEvent[M]]
	cancelCtx             *cancelContext

	failoverLastSuccessModel model.BaseModel[M]

	// suppressEventSend prevents eventSenderModel from emitting AgentEvents for the current
	// Generate call. Set to true before each rejected retry attempt and reset to false after.
	// Invariant: any code path that emits model output events MUST check this flag.
	suppressEventSend  bool
	retryVerdictSignal *retryVerdictSignal

	afterToolCallsHook     func(ctx context.Context) error
	sessionEvents          bool
	timelineEvents         bool
	internalTimelineEvents bool
	lastModelContext       *ModelContextEvent
	sawModelContext        bool
}

func (e *typedChatModelAgentExecCtx[M]) send(ctx context.Context, event *TypedAgentEvent[M]) {
	if e == nil || e.generator == nil {
		return
	}
	if e.cancelCtx != nil && e.cancelCtx.isImmediateCancelled() {
		return
	}
	if event == nil {
		return
	}
	ensureTypedAgentEventMessageIDs(event)
	sc := sessionEventContextFromContext[M](ctx)
	if event.SessionEventVariant != nil && event.SessionEventVariant.Event != nil {
		stampSessionEventTurnID(event.SessionEventVariant.Event, sc.turnID)
		gen := sc.generator
		if gen == nil {
			gen = DefaultSessionEventIDGenerator[M]
		}
		if _, err := normalizeAgentSessionEventWithAssigner(event, func(se *SessionEvent[M]) (string, error) {
			return gen(ctx, se)
		}); err != nil {
			event.Err = err
		}
	}
	if event.SessionEventVariant != nil && event.SessionEventVariant.MessageStreamRef != nil {
		if sc.turnID != "" {
			event.SessionEventVariant.MessageStreamRef.TurnID = sc.turnID
		}
	}
	e.generator.trySend(event)
}

func copyModelContextEvent(event *ModelContextEvent) *ModelContextEvent {
	if event == nil {
		return nil
	}
	return &ModelContextEvent{
		ToolInfos:         cloneToolInfos(event.ToolInfos),
		DeferredToolInfos: cloneToolInfos(event.DeferredToolInfos),
	}
}

func cloneToolInfos(infos []*schema.ToolInfo) []*schema.ToolInfo {
	if infos == nil {
		return nil
	}
	return append([]*schema.ToolInfo{}, infos...)
}

func modelContextEventEqual(a, b *ModelContextEvent) bool {
	if a == nil || b == nil {
		return a == b
	}
	return toolInfosEqual(a.ToolInfos, b.ToolInfos) &&
		toolInfosEqual(a.DeferredToolInfos, b.DeferredToolInfos)
}

func toolInfosEqual(a, b []*schema.ToolInfo) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !toolInfoEqual(a[i], b[i]) {
			return false
		}
	}
	return true
}

func toolInfoEqual(a, b *schema.ToolInfo) bool {
	if reflect.DeepEqual(a, b) {
		return true
	}
	aBytes, aErr := json.Marshal(a)
	bBytes, bErr := json.Marshal(b)
	if aErr != nil || bErr != nil {
		return false
	}
	return bytes.Equal(aBytes, bBytes)
}

func syncModelContextSessionEvent[M MessageType](ctx context.Context, state *TypedChatModelAgentState[M]) {
	execCtx := getTypedChatModelAgentExecCtx[M](ctx)
	if execCtx == nil || !execCtx.sessionEvents || state == nil {
		return
	}
	current := &ModelContextEvent{
		ToolInfos:         cloneToolInfos(state.ToolInfos),
		DeferredToolInfos: cloneToolInfos(state.DeferredToolInfos),
	}
	changed := !execCtx.sawModelContext || !modelContextEventEqual(execCtx.lastModelContext, current)
	if changed {
		execCtx.send(ctx, &TypedAgentEvent[M]{
			SessionEventVariant: &SessionEventVariant[M]{
				Event: &SessionEvent[M]{
					Kind:         SessionEventModelContext,
					ModelContext: copyModelContextEvent(current),
				},
			},
		})
	}
	execCtx.lastModelContext = copyModelContextEvent(current)
	execCtx.sawModelContext = true
}

type chatModelAgentExecCtx = typedChatModelAgentExecCtx[*schema.Message]

type typedChatModelAgentExecCtxKey[M MessageType] struct{}

func withTypedChatModelAgentExecCtx[M MessageType](ctx context.Context, execCtx *typedChatModelAgentExecCtx[M]) context.Context {
	return context.WithValue(ctx, typedChatModelAgentExecCtxKey[M]{}, execCtx)
}

func getTypedChatModelAgentExecCtx[M MessageType](ctx context.Context) *typedChatModelAgentExecCtx[M] {
	if v := ctx.Value(typedChatModelAgentExecCtxKey[M]{}); v != nil {
		return v.(*typedChatModelAgentExecCtx[M])
	}
	return nil
}

func newTypedChatModelAgentExecCtx[M MessageType](
	generator *AsyncGenerator[*TypedAgentEvent[M]],
	cancelCtx *cancelContext,
	sessionEvents bool,
	timelineEvents bool,
	internalTimelineEvents bool,
) *typedChatModelAgentExecCtx[M] {
	return &typedChatModelAgentExecCtx[M]{
		generator:              generator,
		cancelCtx:              cancelCtx,
		sessionEvents:          sessionEvents,
		timelineEvents:         timelineEvents,
		internalTimelineEvents: internalTimelineEvents,
	}
}

func configureTypedChatModelAgentExecCtx[M MessageType](
	ctx context.Context,
	generator *AsyncGenerator[*TypedAgentEvent[M]],
	cancelCtx *cancelContext,
	sessionEvents bool,
	timelineEvents bool,
	internalTimelineEvents bool,
) (context.Context, *typedChatModelAgentExecCtx[M]) {
	execCtx := getTypedChatModelAgentExecCtx[M](ctx)
	if execCtx == nil {
		execCtx = &typedChatModelAgentExecCtx[M]{}
		ctx = withTypedChatModelAgentExecCtx(ctx, execCtx)
	}
	execCtx.generator = generator
	execCtx.cancelCtx = cancelCtx
	execCtx.sessionEvents = sessionEvents
	execCtx.timelineEvents = timelineEvents
	execCtx.internalTimelineEvents = internalTimelineEvents
	return ctx, execCtx
}

type chatModelAgentRunOptions struct {
	chatModelOptions []model.Option
	toolOptions      []tool.Option
	agentToolOptions map[string][]AgentRunOption

	historyModifier func(context.Context, []Message) []Message

	afterToolCallsHook     func(ctx context.Context) error
	initialModelContext    *ModelContextEvent
	sawInitialModelContext bool
}

func withInitialModelContext(event *ModelContextEvent, saw bool) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		if !saw {
			return
		}
		t.initialModelContext = copyModelContextEvent(event)
		t.sawInitialModelContext = true
	})
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

// WithAfterToolCallsHook registers a per-run hook that fires synchronously after
// all tool calls in a react iteration complete, before the next ChatModel call.
//
// This is suitable for TurnLoop Push+Preempt patterns where the pushed item
// must be visible to the next turn's GenInput.
func WithAfterToolCallsHook(fn func(ctx context.Context) error) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *chatModelAgentRunOptions) {
		t.afterToolCallsHook = fn
	})
}

type ToolsConfig struct {
	compose.ToolsNodeConfig

	// ReturnDirectly specifies tools that cause the agent to return immediately when called.
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

// TypedGenModelInput transforms the agent's system instruction and user input into model input
// messages ([]M). This is the primary customization point for controlling what the model sees.
// The default implementation prepends a system message (if instruction is non-empty),
// followed by the user's input messages.
type TypedGenModelInput[M MessageType] func(ctx context.Context, instruction string, input *TypedAgentInput[M]) ([]M, error)

// GenModelInput transforms agent instructions and input into a format suitable for the model.
type GenModelInput = TypedGenModelInput[*schema.Message]

func defaultGenModelInput(ctx context.Context, instruction string, input *AgentInput) ([]Message, error) {
	inputMessages := input.Messages

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

		// Strip any existing leading system message from history to avoid
		// duplication when session state carries the previous turn's system
		// message. The fresh instruction (potentially re-formatted with current
		// SessionValues) always takes precedence.
		if len(inputMessages) > 0 && inputMessages[0].Role == schema.System {
			inputMessages = inputMessages[1:]
		}

		msgs := make([]Message, 0, len(inputMessages)+1)
		msgs = append(msgs, sp)
		msgs = append(msgs, inputMessages...)
		return msgs, nil
	}

	msgs := make([]Message, 0, len(inputMessages))
	msgs = append(msgs, inputMessages...)
	return msgs, nil
}

func newDefaultGenModelInput[M MessageType]() TypedGenModelInput[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(GenModelInput(defaultGenModelInput)).(TypedGenModelInput[M])
	case *schema.AgenticMessage:
		return any(TypedGenModelInput[*schema.AgenticMessage](func(_ context.Context, instruction string, input *TypedAgentInput[*schema.AgenticMessage]) ([]*schema.AgenticMessage, error) {
			inputMessages := input.Messages
			if instruction != "" {
				// Strip any existing leading system message from history to avoid
				// duplication when session state carries the previous turn's system
				// message.
				if len(inputMessages) > 0 && inputMessages[0].Role == schema.AgenticRoleTypeSystem {
					inputMessages = inputMessages[1:]
				}
				msgs := make([]*schema.AgenticMessage, 0, len(inputMessages)+1)
				msgs = append(msgs, schema.SystemAgenticMessage(instruction))
				msgs = append(msgs, inputMessages...)
				return msgs, nil
			}
			msgs := make([]*schema.AgenticMessage, 0, len(inputMessages))
			msgs = append(msgs, inputMessages...)
			return msgs, nil
		})).(TypedGenModelInput[M])
	default:
		panic("unreachable: unknown MessageType")
	}
}

const extraKeyRuntimeGeneratedSystemMessage = "_eino_adk_runtime_generated_system_message"

func ensureGeneratedMessageIDs[M MessageType](messages []M) {
	for _, msg := range messages {
		EnsureMessageID(msg)
	}
}

func isSystemRoleMessage[M MessageType](msg M) bool {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Role == schema.System
	case *schema.AgenticMessage:
		return m.Role == schema.AgenticRoleTypeSystem
	}
	return false
}

func getMessageExtra[M MessageType](msg M) map[string]any {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Extra
	case *schema.AgenticMessage:
		return m.Extra
	}
	return nil
}

func setMessageExtraField[M MessageType](msg M, key string, value any) {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m.Extra == nil {
			m.Extra = map[string]any{}
		}
		m.Extra[key] = value
	case *schema.AgenticMessage:
		if m.Extra == nil {
			m.Extra = map[string]any{}
		}
		m.Extra[key] = value
	}
}

func markRuntimeGeneratedLeadingSystem[M MessageType](inputMsgs, generatedMsgs []M) {
	inputIDs := make(map[string]struct{})
	for _, msg := range inputMsgs {
		if isNilMessage(msg) {
			continue
		}
		id := GetMessageID(msg)
		if id != "" {
			inputIDs[id] = struct{}{}
		}
	}
	for i := 0; i < len(generatedMsgs); i++ {
		msg := generatedMsgs[i]
		if isNilMessage(msg) || !isSystemRoleMessage(msg) {
			break
		}
		id := GetMessageID(msg)
		if id == "" {
			setMessageExtraField(msg, extraKeyRuntimeGeneratedSystemMessage, true)
			continue
		}
		if _, ok := inputIDs[id]; !ok {
			setMessageExtraField(msg, extraKeyRuntimeGeneratedSystemMessage, true)
		} else {
			break
		}
	}
}

// TypedChatModelAgentState represents the state of a chat model agent during conversation.
// This is the primary state type for both TypedChatModelAgentMiddleware and AgentMiddleware callbacks.
type TypedChatModelAgentState[M MessageType] struct {
	// Messages contains all messages in the current conversation session.
	Messages []M

	// ToolInfos contains the tool definitions passed to the model via model.WithTools.
	// BeforeModelRewriteState handlers can read and modify this field to control which tools
	// the model sees on each call.
	ToolInfos []*schema.ToolInfo

	// DeferredToolInfos contains tool definitions for server-side deferred retrieval,
	// passed to the model via model.WithDeferredTools. These tools are not included in the
	// immediate tool list but can be discovered by the model through its native search capability.
	// Nil when not in use.
	DeferredToolInfos []*schema.ToolInfo
}

// ChatModelAgentState is the default state type using *schema.Message.
type ChatModelAgentState = TypedChatModelAgentState[*schema.Message]

// Deprecated: Use ChatModelAgentMiddleware (interface-based Handlers) instead.
// AgentMiddleware will be removed in a future release.
//
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

// TypedChatModelAgentConfig is the generic configuration for ChatModelAgent.
type TypedChatModelAgentConfig[M MessageType] struct {
	// Name of the agent. Better be unique across all agents.
	// Optional. If empty, the agent can still run standalone but cannot be used as
	// a sub-agent tool via NewAgentTool (which requires a non-empty Name).
	Name string
	// Description of the agent's capabilities.
	// Helps other agents determine whether to transfer tasks to this agent.
	// Optional. If empty, the agent can still run standalone but cannot be used as
	// a sub-agent tool via NewAgentTool (which requires a non-empty Description).
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
	Model model.BaseModel[M]

	ToolsConfig ToolsConfig

	// GenModelInput transforms instructions and input messages into the model's input format.
	// Optional. Defaults to defaultGenModelInput which combines instruction and messages.
	GenModelInput TypedGenModelInput[M]

	// Exit defines the tool used to terminate the agent process.
	// Optional. If nil, no Exit Action will be generated.
	// You can use the provided 'ExitTool' implementation directly.
	//
	// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
	// to be more effective empirically. Consider using ChatModelAgent with AgentTool
	// or DeepAgent instead for most multi-agent scenarios.
	Exit tool.BaseTool

	// OutputKey stores the agent's response in the session.
	// Optional. When set, stores output via AddSessionValue(ctx, outputKey, msg.Content).
	//
	// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
	// to be more effective empirically. Consider using ChatModelAgent with AgentTool
	// or DeepAgent instead for most multi-agent scenarios.
	OutputKey string

	// MaxIterations defines the upper limit of ChatModel generation cycles.
	// The agent will terminate with an error if this limit is exceeded.
	// Optional. Defaults to 20.
	MaxIterations int

	// Deprecated: Use Handlers instead. Middlewares will be removed in a future release.
	// Handlers provides a more flexible interface-based approach for extending agent behavior.
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
	//  3. failoverModelWrapper (internal - failover between models, if configured)
	//  4. retryModelWrapper (internal - retries on failure, if configured)
	//  5. eventSenderModelWrapper (internal - sends model response events)
	//  6. ChatModelAgentMiddleware.WrapModel (wrapper, first registered is outermost)
	//  7. callbackInjectionModelWrapper (internal - injects callbacks if not enabled; when failover is enabled, this is handled per-model inside failoverProxyModel instead)
	//  8. failoverProxyModel (internal - dispatches to selected failover model, if configured) / Model.Generate/Stream
	//  9. ChatModelAgentMiddleware.AfterModelRewriteState (hook, can modify state after model call)
	// 10. AgentMiddleware.AfterChatModel (hook, runs after model call)
	//
	// Custom Event Sender Position:
	// By default, events are sent after all user middlewares (WrapModel) have processed the output,
	// containing the modified messages. To send events with original (unmodified) output, pass
	// NewEventSenderModelWrapper as a Handler after the modifying middleware:
	//
	//   agent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
	//       Handlers: []adk.ChatModelAgentMiddleware{
	//           myCustomHandler,                   // First registered = outermost wrapper
	//           adk.NewEventSenderModelWrapper(),  // Last registered = innermost, events sent with original output
	//       },
	//   })
	//
	// Handler order: first registered is outermost. So [A, B, C] becomes A(B(C(model))).
	// EventSenderModelWrapper sends events in post-processing, so placing it innermost
	// means it receives the original model output before outer handlers modify it.
	//
	// When EventSenderModelWrapper is detected in Handlers, the framework skips
	// the default event sender to avoid duplicate events.
	//
	// Tool call lifecycle (outermost to innermost):
	//  1. eventSenderToolWrapper (internal ToolMiddleware - sends tool result events after all processing)
	//  2. ToolsConfig.ToolCallMiddlewares (ToolMiddleware)
	//  3. AgentMiddleware.WrapToolCall (ToolMiddleware)
	//  4. cancelMonitoredToolHandler (internal - sets up cancel monitoring for stream tools)
	//  5. ChatModelAgentMiddleware.WrapToolCall (wrapper, first registered is outermost)
	//  6. callbackInjectedToolCall (internal - injects callbacks if tool doesn't handle them)
	//  7. Tool.InvokableRun/StreamableRun
	//
	// Custom Tool Event Sender Position:
	// By default, tool result events are emitted by an internal event sender placed before
	// all user middlewares (outermost), so events reflect the fully processed tool output.
	// To control exactly where in the handler chain tool events are emitted, pass
	// NewEventSenderToolWrapper() as one of the Handlers. Its position determines which
	// middlewares' effects are visible in the emitted event:
	//
	//   agent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
	//       Handlers: []adk.ChatModelAgentMiddleware{
	//           loggingHandler,                      // Outermost: sees event-sender output
	//           adk.NewEventSenderToolWrapper(),     // Events reflect output from handlers below
	//           sanitizationHandler,                 // Innermost: runs first, modifies tool output
	//       },
	//   })
	//
	// Handler order: first registered is outermost. So [A, B, C] wraps as A(B(C(tool))).
	// The event sender captures tool output in post-processing, so its position controls
	// which handlers' modifications are included in the emitted events.
	//
	// When NewEventSenderToolWrapper is detected in Handlers, the framework skips
	// the default event sender to avoid duplicate events.
	//
	// Tool List Modification:
	//
	// There are two ways to modify the tool list:
	//
	//  1. In BeforeAgent: Modify ChatModelAgentContext.Tools ([]tool.BaseTool) directly. This affects
	//     both the tool info list passed to ChatModel AND the actual tools available for
	//     execution. Changes persist for the entire agent run.
	//
	//  2. In BeforeModelRewriteState: Modify state.ToolInfos and state.DeferredToolInfos directly.
	//     This affects the tool info list passed to ChatModel for this and all subsequent model
	//     calls (changes are persisted in state). This is the recommended approach for dynamic
	//     tool filtering/selection based on conversation context.
	//
	// Modifying tools in WrapModel (e.g. via model.WithTools) is discouraged: changes there
	// are NOT persisted in state, only affect a single model call, and break prompt cache.
	Handlers []TypedChatModelAgentMiddleware[M]

	// ModelRetryConfig configures retry behavior for the ChatModel.
	// When set, the agent will automatically retry failed ChatModel calls
	// based on the configured policy.
	// Optional. If nil, no retry will be performed.
	ModelRetryConfig *TypedModelRetryConfig[M]

	// ModelFailoverConfig configures failover behavior for the ChatModel.
	// When set, the agent will first try the last successful model (initially the configured Model),
	// and on failure, call GetFailoverModel to select alternate models.
	// Model field is still required as it serves as the initial model.
	// Optional. If nil, no failover will be performed.
	ModelFailoverConfig *ModelFailoverConfig[M]
}

type ChatModelAgentConfig = TypedChatModelAgentConfig[*schema.Message]

// TypedChatModelAgent is a chat model-backed agent parameterized by message type.
//
// For M = *schema.Message, the full ReAct loop (model → tool calls → model) is used.
// For M = *schema.AgenticMessage, a single-shot chain is used since agentic models
// handle tool calling internally. Cancel monitoring and retry on the model stream
// are not yet supported for agentic models.
type TypedChatModelAgent[M MessageType] struct {
	name        string
	description string
	instruction string

	model       model.BaseModel[M]
	toolsConfig ToolsConfig

	genModelInput TypedGenModelInput[M]

	outputKey     string
	maxIterations int

	subAgents   []TypedAgent[M]
	parentAgent TypedAgent[M]

	disallowTransferToParent bool

	exit tool.BaseTool

	handlers    []TypedChatModelAgentMiddleware[M]
	middlewares []AgentMiddleware

	modelRetryConfig    *TypedModelRetryConfig[M]
	modelFailoverConfig *ModelFailoverConfig[M]

	once   sync.Once
	run    typedRunFunc[M]
	frozen uint32
	exeCtx *execContext
}

type ChatModelAgent = TypedChatModelAgent[*schema.Message]

// typedRunParams holds the parameters for a typedRunFunc invocation.
type typedRunParams[M MessageType] struct {
	input                  *TypedAgentInput[M]
	generator              *AsyncGenerator[*TypedAgentEvent[M]]
	store                  *bridgeStore
	instruction            string
	returnDirectly         map[string]bool
	cancelCtx              *cancelContext
	cancelCtxOwned         bool
	composeOpts            []compose.Option
	sessionEvents          bool
	timelineEvents         bool
	internalTimelineEvents bool

	afterToolCallsHook func(ctx context.Context) error
}

type typedRunFunc[M MessageType] func(ctx context.Context, p *typedRunParams[M])

func isCheckpointAwareCancelScope(cc *cancelContext) bool {
	return cc != nil && cc.parent != nil && !cc.abortOnly
}

func hasActiveChatModelAgentExecCtx(ctx context.Context) bool {
	return getTypedChatModelAgentExecCtx[*schema.Message](ctx) != nil ||
		getTypedChatModelAgentExecCtx[*schema.AgenticMessage](ctx) != nil
}

func resolveRunCancelContext(ctx context.Context, o *options) (*cancelContext, bool) {
	inherited := getCancelContext(ctx)
	if o.cancelCtx != nil {
		return o.cancelCtx, o.cancelCtx != inherited
	}
	if inherited != nil {
		// ADK-managed child boundaries pass explicit checkpoint-aware scopes.
		// Such scopes may reach ChatModelAgent through transparent wrappers.
		if isCheckpointAwareCancelScope(inherited) && !hasActiveChatModelAgentExecCtx(ctx) {
			return inherited, false
		}
		// Other inherited scopes are direct non-boundary nesting: only immediate
		// recursive abort should propagate, without safe-point checkpointing.
		return deriveAbortOnlyCancelContext(ctx, inherited), true
	}
	return nil, false
}

// NewChatModelAgent creates a new ChatModelAgent with the given config.
func NewChatModelAgent(ctx context.Context, config *ChatModelAgentConfig) (*ChatModelAgent, error) {
	return NewTypedChatModelAgent[*schema.Message](ctx, config)
}

// NewTypedChatModelAgent creates a new TypedChatModelAgent with the given config.
func NewTypedChatModelAgent[M MessageType](_ context.Context, config *TypedChatModelAgentConfig[M]) (*TypedChatModelAgent[M], error) {
	if config.ModelFailoverConfig != nil {
		if config.ModelFailoverConfig.GetFailoverModel == nil {
			return nil, errors.New("ModelFailoverConfig.GetFailoverModel is required when ModelFailoverConfig is set")
		}

		// ShouldFailover is required when ModelFailoverConfig is set
		if config.ModelFailoverConfig.ShouldFailover == nil {
			return nil, errors.New("ModelFailoverConfig.ShouldFailover is required when ModelFailoverConfig is set")
		}
	}

	if config.Model == nil {
		return nil, errors.New("agent 'Model' is required")
	}

	var genInput TypedGenModelInput[M]
	if config.GenModelInput != nil {
		genInput = config.GenModelInput
	} else {
		genInput = newDefaultGenModelInput[M]()
	}

	tc := config.ToolsConfig

	// Tool call middleware execution order (outermost to innermost):
	// 1. eventSenderToolWrapper (internal — emits tool result AgentEvents and
	//    tool_call_start/end SessionEvents; persistence-aware so a tool call's
	//    start and end may straddle interrupt/resume boundaries)
	// 2. User-provided ToolsConfig.ToolCallMiddlewares (original order preserved)
	// 3. Middlewares' WrapToolCall (in registration order)
	// 4. cancelMonitoredToolHandler (internal - cancel monitoring for stream tools)
	// 5. ChatModelAgentMiddleware.WrapToolCall (in registration order)
	// 6. callbackInjectedToolCall (internal - injects callbacks if tool doesn't handle them)
	if !hasUserEventSenderToolWrapper(config.Handlers) {
		defaultToolEventSender := handlersToToolMiddlewares([]TypedChatModelAgentMiddleware[M]{newTypedEventSenderToolWrapper[M]()})
		tc.ToolCallMiddlewares = append(defaultToolEventSender, tc.ToolCallMiddlewares...)
	}
	tc.ToolCallMiddlewares = append(tc.ToolCallMiddlewares, collectToolMiddlewaresFromMiddlewares(config.Middlewares)...)

	// Cancel monitoring middleware — wraps around ChatModelAgentMiddleware handlers.
	// Pre-processing sets up cancel signals; when immediateChan fires
	// (CancelImmediate or timeout escalation), the raw tool result stream is aborted.
	// Outer wrappers must propagate stream errors such as ErrStreamCanceled without swallowing them.
	cancelToolHandler := &cancelMonitoredToolHandler{}
	tc.ToolCallMiddlewares = append(tc.ToolCallMiddlewares, compose.ToolMiddleware{
		Streamable:         cancelToolHandler.WrapStreamableToolCall,
		EnhancedStreamable: cancelToolHandler.WrapEnhancedStreamableToolCall,
	})

	return &TypedChatModelAgent[M]{
		name:                config.Name,
		description:         config.Description,
		instruction:         config.Instruction,
		model:               config.Model,
		toolsConfig:         tc,
		genModelInput:       genInput,
		exit:                config.Exit,
		outputKey:           config.OutputKey,
		maxIterations:       config.MaxIterations,
		handlers:            config.Handlers,
		middlewares:         config.Middlewares,
		modelRetryConfig:    config.ModelRetryConfig,
		modelFailoverConfig: config.ModelFailoverConfig,
	}, nil
}

func collectToolMiddlewaresFromMiddlewares(mws []AgentMiddleware) []compose.ToolMiddleware {
	var middlewares []compose.ToolMiddleware
	for _, m := range mws {
		if m.WrapToolCall.Invokable == nil && m.WrapToolCall.Streamable == nil && m.WrapToolCall.EnhancedStreamable == nil && m.WrapToolCall.EnhancedInvokable == nil {
			continue
		}
		middlewares = append(middlewares, m.WrapToolCall)
	}
	return middlewares
}

const (
	TransferToAgentToolName        = "transfer_to_agent"
	TransferToAgentToolDesc        = "Transfer the question to another agent."
	TransferToAgentToolDescChinese = "将问题移交给其他 Agent。"
)

var (
	toolInfoTransferToAgent = &schema.ToolInfo{
		Name: TransferToAgentToolName,

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
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: TransferToAgentToolDesc,
		Chinese: TransferToAgentToolDescChinese,
	})
	info := *toolInfoTransferToAgent
	info.Desc = desc
	return &info, nil
}

func transferToAgentToolOutput(destName string) string {
	tpl := internal.SelectPrompt(internal.I18nPrompts{
		English: "successfully transferred to agent [%s]",
		Chinese: "成功移交任务至 agent [%s]",
	})
	return fmt.Sprintf(tpl, destName)
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

func (a *TypedChatModelAgent[M]) Name(_ context.Context) string {
	return a.name
}

func (a *TypedChatModelAgent[M]) Description(_ context.Context) string {
	return a.description
}

func (a *TypedChatModelAgent[M]) GetType() string {
	return "ChatModel"
}

// OnSetSubAgents implements OnSubAgents.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
func (a *TypedChatModelAgent[M]) OnSetSubAgents(_ context.Context, subAgents []TypedAgent[M]) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	if len(a.subAgents) > 0 {
		return errors.New("agent's sub-agents has already been set")
	}

	a.subAgents = subAgents
	return nil
}

// OnSetAsSubAgent implements OnSubAgents.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
func (a *TypedChatModelAgent[M]) OnSetAsSubAgent(_ context.Context, parent TypedAgent[M]) error {
	if atomic.LoadUint32(&a.frozen) == 1 {
		return errors.New("agent has been frozen after run")
	}

	if a.parentAgent != nil {
		return errors.New("agent has already been set as a sub-agent of another agent")
	}

	a.parentAgent = parent
	return nil
}

// OnDisallowTransferToParent implements OnSubAgents.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
func (a *TypedChatModelAgent[M]) OnDisallowTransferToParent(_ context.Context) error {
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

func extractTextContent[M MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Content
	case *schema.AgenticMessage:
		var texts []string
		for _, block := range v.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeAssistantGenText && block.AssistantGenText != nil {
				texts = append(texts, block.AssistantGenText.Text)
			}
		}
		return strings.Join(texts, "\n")
	default:
		return ""
	}
}

func setOutputToSession[M MessageType](ctx context.Context, msg M, msgStream *schema.StreamReader[M], outputKey string) error {
	if !isNilMessage(msg) {
		AddSessionValue(ctx, outputKey, extractTextContent(msg))
		return nil
	}

	concatenated, err := concatMessageStream(msgStream)
	if err != nil {
		return err
	}

	AddSessionValue(ctx, outputKey, extractTextContent(concatenated))
	return nil
}

func typedErrFunc[M MessageType](err error) typedRunFunc[M] {
	return func(ctx context.Context, p *typedRunParams[M]) {
		p.generator.Send(&TypedAgentEvent[M]{Err: err})
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

	toolSearchTool *schema.ToolInfo // set by BeforeAgent when the model supports native tool search

	rebuildGraph bool // whether needs to instantiate a new graph because of topology changes due to tool modifications
	toolUpdated  bool // whether needs to pass a compose.WithToolList option to ToolsNode due to tool list change
}

func (a *TypedChatModelAgent[M]) applyBeforeAgent(ctx context.Context, ec *execContext, agentInput *TypedAgentInput[M]) (
	context.Context, *execContext, *TypedAgentInput[M], error) {

	runCtx := &ChatModelAgentContext[M]{
		Instruction:    ec.instruction,
		AgentInput:     agentInput,
		Tools:          cloneSlice(ec.unwrappedTools),
		ReturnDirectly: copyMap(ec.returnDirectly),
	}

	var err error
	for i, handler := range a.handlers {
		ctx, runCtx, err = handler.BeforeAgent(ctx, runCtx)
		if err != nil {
			return ctx, nil, nil, fmt.Errorf("handler[%d] (%T) BeforeAgent failed: %w", i, handler, err)
		}
	}

	toolsNodeConf := ec.toolsNodeConf
	toolsNodeConf.Tools = runCtx.Tools
	toolsNodeConf.ToolCallMiddlewares = cloneSlice(ec.toolsNodeConf.ToolCallMiddlewares)

	runtimeEC := &execContext{
		instruction:    runCtx.Instruction,
		toolsNodeConf:  toolsNodeConf,
		returnDirectly: runCtx.ReturnDirectly,
		toolSearchTool: runCtx.ToolSearchTool,
		toolUpdated:    true,
		rebuildGraph: (len(ec.toolsNodeConf.Tools) == 0 && len(runCtx.Tools) > 0) ||
			(len(ec.returnDirectly) == 0 && len(runCtx.ReturnDirectly) > 0),
	}

	toolInfos, err := genToolInfos(ctx, &runtimeEC.toolsNodeConf)
	if err != nil {
		return ctx, nil, nil, err
	}

	runtimeEC.toolInfos = toolInfos

	return ctx, runtimeEC, runCtx.AgentInput, nil
}

func (a *TypedChatModelAgent[M]) applyAfterAgent(ctx context.Context) (context.Context, error) {
	if len(a.handlers) == 0 {
		return ctx, nil
	}

	var state TypedChatModelAgentState[M]
	_ = compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
		state.Messages = st.Messages
		state.ToolInfos = st.ToolInfos
		state.DeferredToolInfos = st.DeferredToolInfos
		return nil
	})

	var err error
	for i, handler := range a.handlers {
		ctx, err = handler.AfterAgent(ctx, &state)
		if err != nil {
			return ctx, fmt.Errorf("handler[%d] (%T) AfterAgent failed: %w", i, handler, err)
		}
	}
	return ctx, nil
}

func (a *TypedChatModelAgent[M]) prepareExecContext(ctx context.Context) (*execContext, error) {
	instruction := a.instruction
	toolsNodeConf := a.toolsConfig.ToolsNodeConfig
	toolsNodeConf.Tools = cloneSlice(a.toolsConfig.Tools)
	toolsNodeConf.ToolCallMiddlewares = cloneSlice(a.toolsConfig.ToolCallMiddlewares)
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

// handleRunFuncError is the common error handler for buildNoToolsRunFunc and buildReActRunFunc.
// It handles compose interrupts (both cancel-triggered and business)
// and generic errors, sending the appropriate event to the generator.
func (a *TypedChatModelAgent[M]) handleRunFuncError(
	ctx context.Context,
	err error,
	cancelCtx *cancelContext,
	cancelCtxOwned bool,
	store *bridgeStore,
	generator *AsyncGenerator[*TypedAgentEvent[M]],
) {
	if isAbortOnlyCancelled(cancelCtx) {
		if cancelCtxOwned {
			cancelCtx.markDone()
		}
		return
	}

	info, ok := compose.ExtractInterruptInfo(err)
	if ok {
		if cancelCtx != nil {
			if !cancelCtx.shouldCancel() {
				// Note: there is a benign TOCTOU window here. Between shouldCancel()
				// returning false and markDone() executing, a concurrent cancel could
				// transition stateRunning→stateCancelling. markDone() then does
				// stateCancelling→stateDone, and the cancel func receives
				// ErrExecutionEnded (execution finished before cancel took effect).
				cancelCtx.markDone()
			}
		}

		data, existed, sErr := store.Get(ctx, bridgeCheckpointID)
		if sErr != nil {
			generator.Send(&TypedAgentEvent[M]{AgentName: a.name, Err: fmt.Errorf("failed to get interrupt info: %w", sErr)})
			return
		}
		if !existed {
			generator.Send(&TypedAgentEvent[M]{AgentName: a.name, Err: fmt.Errorf("interrupt occurred but checkpoint data is missing")})
			return
		}

		is := FromInterruptContexts(info.InterruptContexts)
		event := TypedCompositeInterrupt[M](ctx, info, data, is)
		event.Action.Interrupted.Data = &ChatModelAgentInterruptInfo{
			Info: info,
			Data: data,
		}
		event.AgentName = a.name
		generator.Send(event)
		return
	}

	if cancelCtxOwned && cancelCtx != nil && cancelCtx.shouldCancel() && errors.Is(err, ErrStreamCanceled) {
		cancelErr, ok := cancelCtx.createAndMarkCancelHandled()
		if ok {
			generator.Send(&TypedAgentEvent[M]{Err: cancelErr})
		}
		return
	}

	if cancelCtxOwned && cancelCtx != nil {
		cancelCtx.markDone()
	}
	generator.Send(&TypedAgentEvent[M]{Err: err})
}

type typedNoToolsInput[M MessageType] struct {
	input       *TypedAgentInput[M]
	instruction string
}

func appendModelToChain[I, O any, M MessageType](chain *compose.Chain[I, O], m model.BaseModel[M]) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		chain.AppendChatModel(any(m).(model.BaseChatModel))
	case *schema.AgenticMessage:
		chain.AppendAgenticModel(any(m).(model.AgenticModel))
	}
}

func (a *TypedChatModelAgent[M]) buildNoToolsRunFunc(_ context.Context) (typedRunFunc[M], error) {
	return func(ctx context.Context, p *typedRunParams[M]) {
		cancelCtx := p.cancelCtx
		ctx = withCancelContext(ctx, cancelCtx)

		wrappedModel := buildModelWrappers(a.model, &typedModelWrapperConfig[M]{
			handlers:       a.handlers,
			middlewares:    a.middlewares,
			retryConfig:    a.modelRetryConfig,
			failoverConfig: a.modelFailoverConfig,
			cancelContext:  cancelCtx,
		})

		chain := compose.NewChain[typedNoToolsInput[M], M](
			compose.WithGenLocalState(func(ctx context.Context) (state *typedState[M]) {
				return &typedState[M]{}
			}))

		chain.AppendLambda(compose.InvokableLambda(func(ctx context.Context, in typedNoToolsInput[M]) ([]M, error) {
			messages, err := a.genModelInput(ctx, in.instruction, in.input)
			if err != nil {
				return nil, err
			}
			if p.sessionEvents {
				ensureGeneratedMessageIDs(messages)
				markRuntimeGeneratedLeadingSystem(in.input.Messages, messages)
			}
			if err := compose.ProcessState(ctx, func(_ context.Context, st *typedState[M]) error {
				st.Messages = append(st.Messages, messages...)
				return nil
			}); err != nil {
				return nil, err
			}
			return messages, nil
		}))

		appendModelToChain(chain, wrappedModel)

		chain.AppendLambda(compose.InvokableLambda(func(ctx context.Context, msg M) (M, error) {
			if len(a.handlers) > 0 {
				if _, err := a.applyAfterAgent(ctx); err != nil {
					return msg, err
				}
			}
			return msg, nil
		}))

		var compileOptions []compose.GraphCompileOption
		compileOptions = append(compileOptions,
			compose.WithGraphName(a.name),
			compose.WithCheckPointStore(p.store),
			compose.WithSerializer(&schema.GobSerializer{}))

		if cancelCtx != nil {
			var interrupt func(...compose.GraphInterruptOption)
			ctx, interrupt = compose.WithGraphInterrupt(ctx)
			cancelCtx.setGraphInterruptFunc(interrupt)
		}

		r, err := chain.Compile(ctx, compileOptions...)
		if err != nil {
			p.generator.Send(&TypedAgentEvent[M]{Err: err})
			return
		}

		var execCtx *typedChatModelAgentExecCtx[M]
		ctx, execCtx = configureTypedChatModelAgentExecCtx(ctx, p.generator, cancelCtx, p.sessionEvents, p.timelineEvents, p.internalTimelineEvents)
		execCtx.failoverLastSuccessModel = a.model

		if checkPreExecCancel(cancelCtx, p.generator) {
			return
		}

		in := typedNoToolsInput[M]{input: p.input, instruction: p.instruction}

		var msg M
		var msgStream *schema.StreamReader[M]
		if p.input.EnableStreaming {
			msgStream, err = r.Stream(ctx, in, p.composeOpts...)
		} else {
			msg, err = r.Invoke(ctx, in, p.composeOpts...)
		}

		if err == nil {
			if a.outputKey != "" {
				err = setOutputToSession(ctx, msg, msgStream, a.outputKey)
				if err != nil {
					p.generator.Send(&TypedAgentEvent[M]{Err: err})
					return
				}
			} else if msgStream != nil {
				msgStream.Close()
			}
			return
		}

		a.handleRunFuncError(ctx, err, cancelCtx, p.cancelCtxOwned, p.store, p.generator)
	}, nil
}

func (a *TypedChatModelAgent[M]) buildReActRunFunc(ctx context.Context, bc *execContext) (typedRunFunc[M], error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return a.buildMessageReActRunFunc(ctx, bc)
	case *schema.AgenticMessage:
		// single-shot: agentic models handle tool calling internally
		return a.buildAgenticReActRunFunc(ctx, bc)
	default:
		return nil, fmt.Errorf("unsupported message type %T for ReAct run mode", zero)
	}
}

type reactRunInput struct {
	input       *AgentInput
	instruction string
}

func (a *TypedChatModelAgent[M]) buildMessageReActRunFunc(_ context.Context, bc *execContext) (typedRunFunc[M], error) {
	// safe: only called when M = *schema.Message (guarded by type switch in buildReActRunFunc)
	msgModel := any(a.model).(model.BaseChatModel)
	msgHandlers := any(a.handlers).([]ChatModelAgentMiddleware)
	genModelInputFn := any(a.genModelInput).(GenModelInput)
	msgConf := &reactConfig{
		model:       msgModel,
		toolsConfig: &bc.toolsNodeConf,
		modelWrapperConf: &modelWrapperConfig{
			handlers:       msgHandlers,
			middlewares:    a.middlewares,
			retryConfig:    any(a.modelRetryConfig).(*ModelRetryConfig),
			failoverConfig: any(a.modelFailoverConfig).(*ModelFailoverConfig[*schema.Message]),
			toolInfos:      bc.toolInfos,
		},
		toolsReturnDirectly: bc.returnDirectly,
		agentName:           a.name,
		maxIterations:       a.maxIterations,
	}

	return func(ctx context.Context, p *typedRunParams[M]) {
		mp := any(p).(*typedRunParams[*schema.Message])
		cancelCtx := mp.cancelCtx
		msgConf.cancelCtx = cancelCtx
		if msgConf.modelWrapperConf != nil {
			msgConf.modelWrapperConf.cancelContext = cancelCtx
		}
		ctx = withCancelContext(ctx, cancelCtx)

		msgAgent := any(a).(*TypedChatModelAgent[*schema.Message])
		msgConf.afterAgentFunc = func(ctx context.Context, msg *schema.Message) (*schema.Message, error) {
			if len(a.handlers) > 0 {
				_, err := msgAgent.applyAfterAgent(ctx)
				if err != nil {
					return msg, err
				}
			}
			return msg, nil
		}

		g, err := newReact(ctx, msgConf)
		if err != nil {
			mp.generator.Send(&AgentEvent{Err: err})
			return
		}

		chain := compose.NewChain[reactRunInput, Message]().
			AppendLambda(
				compose.InvokableLambda(func(ctx context.Context, in reactRunInput) (*reactInput, error) {
					messages, genErr := genModelInputFn(ctx, in.instruction, in.input)
					if genErr != nil {
						return nil, genErr
					}
					if mp.sessionEvents {
						ensureGeneratedMessageIDs(messages)
						markRuntimeGeneratedLeadingSystem(in.input.Messages, messages)
					}
					return &reactInput{
						Messages: messages,
					}, nil
				}),
			).
			AppendGraph(g, compose.WithNodeName("ReAct"), compose.WithGraphCompileOptions(compose.WithMaxRunSteps(math.MaxInt)))

		var compileOptions []compose.GraphCompileOption
		compileOptions = append(compileOptions,
			compose.WithGraphName(a.name),
			compose.WithCheckPointStore(mp.store),
			compose.WithSerializer(&schema.GobSerializer{}),
			compose.WithMaxRunSteps(math.MaxInt))

		if cancelCtx != nil {
			var interrupt func(...compose.GraphInterruptOption)
			ctx, interrupt = compose.WithGraphInterrupt(ctx)
			cancelCtx.setGraphInterruptFunc(interrupt)
		}

		runnable, err_ := chain.Compile(ctx, compileOptions...)
		if err_ != nil {
			mp.generator.Send(&AgentEvent{Err: err_})
			return
		}

		ctx, execCtx := configureTypedChatModelAgentExecCtx(ctx, mp.generator, cancelCtx, mp.sessionEvents, mp.timelineEvents, mp.internalTimelineEvents)
		execCtx.runtimeReturnDirectly = mp.returnDirectly
		execCtx.failoverLastSuccessModel = msgModel
		execCtx.afterToolCallsHook = mp.afterToolCallsHook

		if checkPreExecCancel(cancelCtx, mp.generator) {
			return
		}

		in := reactRunInput{
			input:       mp.input,
			instruction: mp.instruction,
		}

		var runOpts []compose.Option
		runOpts = append(runOpts, mp.composeOpts...)
		if a.toolsConfig.EmitInternalEvents {
			runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withParentEventReceiver(mp.generator))))
		}
		if mp.input.EnableStreaming {
			runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEnableStreaming(true))))
		}

		var msg Message
		var msgStream MessageStream
		if mp.input.EnableStreaming {
			msgStream, err_ = runnable.Stream(ctx, in, runOpts...)
		} else {
			msg, err_ = runnable.Invoke(ctx, in, runOpts...)
		}

		if err_ == nil {
			if a.outputKey != "" {
				err_ = setOutputToSession[*schema.Message](ctx, msg, msgStream, a.outputKey)
				if err_ != nil {
					mp.generator.Send(&AgentEvent{Err: err_})
					return
				}
			} else if msgStream != nil {
				msgStream.Close()
			}

			return
		}

		a.handleRunFuncError(ctx, err_, cancelCtx, mp.cancelCtxOwned, mp.store, p.generator)
	}, nil
}

type agenticReactRunInput struct {
	input       *TypedAgentInput[*schema.AgenticMessage]
	instruction string
}

func (a *TypedChatModelAgent[M]) buildAgenticReActRunFunc(_ context.Context, bc *execContext) (typedRunFunc[M], error) {
	agenticModel := any(a.model).(model.AgenticModel)
	agenticHandlers := any(a.handlers).([]TypedChatModelAgentMiddleware[*schema.AgenticMessage])
	genModelInputFn := any(a.genModelInput).(TypedGenModelInput[*schema.AgenticMessage])
	agenticConf := &agenticReactConfig{
		model:       agenticModel,
		toolsConfig: &bc.toolsNodeConf,
		modelWrapperConf: &typedModelWrapperConfig[*schema.AgenticMessage]{
			handlers:       agenticHandlers,
			middlewares:    a.middlewares,
			retryConfig:    any(a.modelRetryConfig).(*TypedModelRetryConfig[*schema.AgenticMessage]),
			failoverConfig: any(a.modelFailoverConfig).(*ModelFailoverConfig[*schema.AgenticMessage]),
			toolInfos:      bc.toolInfos,
		},
		toolsReturnDirectly: bc.returnDirectly,
		agentName:           a.name,
		maxIterations:       a.maxIterations,
	}

	return func(ctx context.Context, p *typedRunParams[M]) {
		ap := any(p).(*typedRunParams[*schema.AgenticMessage])
		cancelCtx := ap.cancelCtx
		agenticConf.cancelCtx = cancelCtx
		if agenticConf.modelWrapperConf != nil {
			agenticConf.modelWrapperConf.cancelContext = cancelCtx
		}
		ctx = withCancelContext(ctx, cancelCtx)

		agenticAgent := any(a).(*TypedChatModelAgent[*schema.AgenticMessage])
		agenticConf.afterAgentFunc = func(ctx context.Context, msg *schema.AgenticMessage) (*schema.AgenticMessage, error) {
			if len(a.handlers) > 0 {
				_, err := agenticAgent.applyAfterAgent(ctx)
				if err != nil {
					return msg, err
				}
			}
			return msg, nil
		}

		g, err := newAgenticReact(ctx, agenticConf)
		if err != nil {
			ap.generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{Err: err})
			return
		}

		chain := compose.NewChain[agenticReactRunInput, *schema.AgenticMessage]().
			AppendLambda(
				compose.InvokableLambda(func(ctx context.Context, in agenticReactRunInput) (*agenticReactInput, error) {
					messages, genErr := genModelInputFn(ctx, in.instruction, in.input)
					if genErr != nil {
						return nil, genErr
					}
					if ap.sessionEvents {
						ensureGeneratedMessageIDs(messages)
						markRuntimeGeneratedLeadingSystem(in.input.Messages, messages)
					}
					return &agenticReactInput{
						Messages: messages,
					}, nil
				}),
			).
			AppendGraph(g, compose.WithNodeName("AgenticReAct"), compose.WithGraphCompileOptions(compose.WithMaxRunSteps(math.MaxInt)))

		var compileOptions []compose.GraphCompileOption
		compileOptions = append(compileOptions,
			compose.WithGraphName(a.name),
			compose.WithCheckPointStore(ap.store),
			compose.WithSerializer(&schema.GobSerializer{}),
			compose.WithMaxRunSteps(math.MaxInt))

		if cancelCtx != nil {
			var interrupt func(...compose.GraphInterruptOption)
			ctx, interrupt = compose.WithGraphInterrupt(ctx)
			cancelCtx.setGraphInterruptFunc(interrupt)
		}

		runnable, err_ := chain.Compile(ctx, compileOptions...)
		if err_ != nil {
			ap.generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{Err: err_})
			return
		}

		ctx, execCtx := configureTypedChatModelAgentExecCtx(ctx, ap.generator, cancelCtx, ap.sessionEvents, ap.timelineEvents, ap.internalTimelineEvents)
		execCtx.runtimeReturnDirectly = ap.returnDirectly
		execCtx.failoverLastSuccessModel = agenticModel
		execCtx.afterToolCallsHook = ap.afterToolCallsHook

		if checkPreExecCancel(cancelCtx, ap.generator) {
			return
		}

		in := agenticReactRunInput{input: ap.input, instruction: ap.instruction}

		var runOpts []compose.Option
		runOpts = append(runOpts, ap.composeOpts...)
		if a.toolsConfig.EmitInternalEvents {
			runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withParentEventReceiver[*schema.AgenticMessage](ap.generator))))
		}
		if ap.input.EnableStreaming {
			runOpts = append(runOpts, compose.WithToolsNodeOption(compose.WithToolOption(withAgentToolEnableStreaming(true))))
		}

		var msg *schema.AgenticMessage
		var msgStream *schema.StreamReader[*schema.AgenticMessage]
		if ap.input.EnableStreaming {
			msgStream, err_ = runnable.Stream(ctx, in, runOpts...)
		} else {
			msg, err_ = runnable.Invoke(ctx, in, runOpts...)
		}

		if err_ == nil {
			if a.outputKey != "" {
				err_ = setOutputToSession(ctx, msg, msgStream, a.outputKey)
				if err_ != nil {
					ap.generator.Send(&TypedAgentEvent[*schema.AgenticMessage]{Err: err_})
					return
				}
			} else if msgStream != nil {
				msgStream.Close()
			}

			return
		}

		a.handleRunFuncError(ctx, err_, cancelCtx, ap.cancelCtxOwned, ap.store, p.generator)
	}, nil
}

func (a *TypedChatModelAgent[M]) buildRunFunc(ctx context.Context) typedRunFunc[M] {
	a.once.Do(func() {
		ec, err := a.prepareExecContext(ctx)
		if err != nil {
			a.run = typedErrFunc[M](err)
			return
		}

		a.exeCtx = ec

		if len(ec.toolsNodeConf.Tools) == 0 {
			var run typedRunFunc[M]
			run, err = a.buildNoToolsRunFunc(ctx)
			if err != nil {
				a.run = typedErrFunc[M](err)
				return
			}
			a.run = run
			return
		}

		var run typedRunFunc[M]
		run, err = a.buildReActRunFunc(ctx, ec)
		if err != nil {
			a.run = typedErrFunc[M](err)
			return
		}
		a.run = run
	})

	atomic.StoreUint32(&a.frozen, 1)

	return a.run
}

func (a *TypedChatModelAgent[M]) getRunFunc(ctx context.Context, agentInput *TypedAgentInput[M]) (context.Context, typedRunFunc[M], *execContext, *TypedAgentInput[M], error) {
	defaultRun := a.buildRunFunc(ctx)
	bc := a.exeCtx

	if bc == nil {
		return ctx, defaultRun, bc, agentInput, nil
	}

	if len(a.handlers) == 0 {
		runtimeBC := &execContext{
			instruction:    bc.instruction,
			toolsNodeConf:  bc.toolsNodeConf,
			returnDirectly: bc.returnDirectly,
			toolInfos:      bc.toolInfos,
		}
		return ctx, defaultRun, runtimeBC, agentInput, nil
	}

	ctx, runtimeBC, agentInput, err := a.applyBeforeAgent(ctx, bc, agentInput)
	if err != nil {
		return ctx, nil, nil, nil, err
	}

	if !runtimeBC.rebuildGraph {
		return ctx, defaultRun, runtimeBC, agentInput, nil
	}

	var tempRun typedRunFunc[M]
	if len(runtimeBC.toolsNodeConf.Tools) == 0 {
		tempRun, err = a.buildNoToolsRunFunc(ctx)
		if err != nil {
			return ctx, nil, nil, nil, err
		}
	} else {
		tempRun, err = a.buildReActRunFunc(ctx, runtimeBC)
		if err != nil {
			return ctx, nil, nil, nil, err
		}
	}

	return ctx, tempRun, runtimeBC, agentInput, nil
}

func (a *TypedChatModelAgent[M]) Run(ctx context.Context, input *TypedAgentInput[M], opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	o := getCommonOptions(nil, opts...)
	cancelCtx, cancelCtxOwned := resolveRunCancelContext(ctx, o)
	var abortOnlyCancel context.CancelFunc
	if cancelCtxOwned && cancelCtx != nil && cancelCtx.abortOnly {
		ctx, abortOnlyCancel = withAbortOnlyCancelContext(ctx, cancelCtx)
	}
	ctx, execCtx := configureTypedChatModelAgentExecCtx(ctx, generator, cancelCtx, o.enableSessionEvents, o.enableTimelineEvents, o.enableInternalTimelineEvents)
	execCtx.failoverLastSuccessModel = a.model

	ctx, run, bc, input, err := a.getRunFunc(ctx, input)
	if err != nil {
		go func() {
			if abortOnlyCancel != nil {
				defer abortOnlyCancel()
			}
			if cancelCtxOwned && cancelCtx != nil {
				defer cancelCtx.markDone()
			}
			generator.Send(&TypedAgentEvent[M]{Err: fmt.Errorf("ChatModelAgent getRunFunc error: %w", err)})
			generator.Close()
		}()
		return iterator
	}

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))
	runOps := GetImplSpecificOptions[chatModelAgentRunOptions](nil, opts...)
	if execCtx := getTypedChatModelAgentExecCtx[M](ctx); execCtx != nil {
		execCtx.lastModelContext = copyModelContextEvent(runOps.initialModelContext)
		execCtx.sawModelContext = runOps.sawInitialModelContext
	}

	if bc != nil {
		if len(bc.toolInfos) > 0 {
			co = append(co, compose.WithChatModelOption(model.WithTools(bc.toolInfos)))
		}
		if bc.toolSearchTool != nil {
			co = append(co, compose.WithChatModelOption(model.WithToolSearchTool(bc.toolSearchTool)))
		}
		if bc.toolUpdated {
			co = append(co, compose.WithToolsNodeOption(compose.WithToolList(bc.toolsNodeConf.Tools...)))
		}
	}

	go func() {
		if abortOnlyCancel != nil {
			defer abortOnlyCancel()
		}
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&TypedAgentEvent[M]{Err: e})
			}

			generator.Close()
		}()

		var (
			instruction    string
			returnDirectly map[string]bool
		)

		if bc != nil {
			instruction = bc.instruction
			returnDirectly = bc.returnDirectly
		}

		run(ctx, &typedRunParams[M]{
			input:                  input,
			generator:              generator,
			store:                  newBridgeStore(),
			instruction:            instruction,
			returnDirectly:         returnDirectly,
			cancelCtx:              cancelCtx,
			cancelCtxOwned:         cancelCtxOwned,
			composeOpts:            co,
			sessionEvents:          o.enableSessionEvents,
			timelineEvents:         o.enableTimelineEvents,
			internalTimelineEvents: o.enableInternalTimelineEvents,
			afterToolCallsHook:     runOps.afterToolCallsHook,
		})
	}()

	if cancelCtxOwned {
		return wrapIterWithCancelCtx(iterator, cancelCtx)
	}
	return iterator
}

func (a *TypedChatModelAgent[M]) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	o := getCommonOptions(nil, opts...)
	cancelCtx, cancelCtxOwned := resolveRunCancelContext(ctx, o)
	var abortOnlyCancel context.CancelFunc
	if cancelCtxOwned && cancelCtx != nil && cancelCtx.abortOnly {
		ctx, abortOnlyCancel = withAbortOnlyCancelContext(ctx, cancelCtx)
	}
	ctx, execCtx := configureTypedChatModelAgentExecCtx(ctx, generator, cancelCtx, o.enableSessionEvents, o.enableTimelineEvents, o.enableInternalTimelineEvents)
	execCtx.failoverLastSuccessModel = a.model

	ctx, run, bc, _, err := a.getRunFunc(ctx, nil)
	if err != nil {
		go func() {
			if abortOnlyCancel != nil {
				defer abortOnlyCancel()
			}
			if cancelCtxOwned && cancelCtx != nil {
				defer cancelCtx.markDone()
			}
			generator.Send(&TypedAgentEvent[M]{Err: fmt.Errorf("ChatModelAgent getRunFunc error: %w", err)})
			generator.Close()
		}()
		return iterator
	}

	co := getComposeOptions(opts)
	co = append(co, compose.WithCheckPointID(bridgeCheckpointID))
	resumeRunOps := GetImplSpecificOptions[chatModelAgentRunOptions](nil, opts...)
	if execCtx := getTypedChatModelAgentExecCtx[M](ctx); execCtx != nil {
		execCtx.lastModelContext = copyModelContextEvent(resumeRunOps.initialModelContext)
		execCtx.sawModelContext = resumeRunOps.sawInitialModelContext
	}

	if bc != nil {
		if len(bc.toolInfos) > 0 {
			co = append(co, compose.WithChatModelOption(model.WithTools(bc.toolInfos)))
		}
		if bc.toolSearchTool != nil {
			co = append(co, compose.WithChatModelOption(model.WithToolSearchTool(bc.toolSearchTool)))
		}
		if bc.toolUpdated {
			co = append(co, compose.WithToolsNodeOption(compose.WithToolList(bc.toolsNodeConf.Tools...)))
		}
	}

	if info == nil {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but info is nil", a.Name(ctx)))
	}

	if info.InterruptState == nil {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
	}

	stateByte, ok := info.InterruptState.([]byte)
	if !ok {
		panic(fmt.Sprintf("ChatModelAgent.Resume: agent '%s' was asked to resume but has invalid interrupt state type: %T",
			a.Name(ctx), info.InterruptState))
	}

	// Migrate legacy checkpoints before resume.
	// This covers both:
	// - v0.7.*: state is stored as a struct wire type (stateV07) under the legacy name.
	// - v0.8.0-v0.8.3: state is stored as a GobEncoder payload under the same legacy name and must
	//   be routed to a GobDecode-compatible compat type via byte-patching.
	// The result is re-encoded so the resume path always operates on the current *State.
	stateByte, err = preprocessComposeCheckpoint(stateByte)
	if err != nil {
		go func() {
			generator.Send(&TypedAgentEvent[M]{Err: err})
			generator.Close()
		}()
		return iterator
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
		if abortOnlyCancel != nil {
			defer abortOnlyCancel()
		}
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&TypedAgentEvent[M]{Err: e})
			}

			generator.Close()
		}()

		var (
			instruction    string
			returnDirectly map[string]bool
		)

		if bc != nil {
			instruction = bc.instruction
			returnDirectly = bc.returnDirectly
		}

		run(ctx, &typedRunParams[M]{
			input:                  &TypedAgentInput[M]{EnableStreaming: info.EnableStreaming},
			generator:              generator,
			store:                  newResumeBridgeStore(bridgeCheckpointID, stateByte),
			instruction:            instruction,
			returnDirectly:         returnDirectly,
			cancelCtx:              cancelCtx,
			cancelCtxOwned:         cancelCtxOwned,
			composeOpts:            co,
			sessionEvents:          o.enableSessionEvents,
			timelineEvents:         o.enableTimelineEvents,
			internalTimelineEvents: o.enableInternalTimelineEvents,
			afterToolCallsHook:     resumeRunOps.afterToolCallsHook,
		})
	}()

	if cancelCtxOwned {
		return wrapIterWithCancelCtx(iterator, cancelCtx)
	}
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

// preprocessComposeCheckpoint migrates legacy compose checkpoints to the current format.
// It handles the v0.8.0-v0.8.3 format:
//   - gob name "_eino_adk_state_v080_" (already byte-patched by preprocessADKCheckpoint
//     from "_eino_adk_react_state"), opaque-bytes wire format → decoded as *stateV080
//
// v0.7 checkpoints need no migration — State is now a plain struct registered under the
// same gob name, and gob handles missing fields gracefully.
//
// Fast path: if the legacy name is not present, skip entirely.
func preprocessComposeCheckpoint(data []byte) ([]byte, error) {
	const lenPrefixedCompatName = "\x15" + stateGobNameV080
	if bytes.Contains(data, []byte(lenPrefixedCompatName)) {
		// v0.8.0-v0.8.3: already byte-patched by preprocessADKCheckpoint; decode as *stateV080.
		migrated, err := compose.MigrateCheckpointState(data, &schema.GobSerializer{}, func(state any) (any, bool, error) {
			sc, ok := state.(*stateV080)
			if !ok {
				return state, false, nil
			}
			return stateV080ToState(sc), true, nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to migrate v0.8.0-v0.8.3 compose checkpoint: %w", err)
		}
		return migrated, nil
	}

	return data, nil
}
