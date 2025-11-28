package adk

import (
	"context"
	"runtime/debug"
	"sync/atomic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
)

// TODO(n3ko): comment

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

	BeforeAgent func(ctx context.Context, arc *AgentContext) (nextContext context.Context, err error)

	OnEvents func(ctx context.Context, arc *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent])
}

type AgentMiddlewareChecker interface {
	IsAgentMiddlewareEnabled() bool
}

// ChatModelAgentState represents the state of a chat model agent during conversation.
type ChatModelAgentState struct {
	// Messages contains all messages in the current conversation session.
	Messages []Message
}

type EntranceType string

const (
	EntranceTypeRun    EntranceType = "Run"
	EntranceTypeResume EntranceType = "Resume"
)

type AgentContext struct {
	AgentInput      *AgentInput
	ResumeInfo      *ResumeInfo
	AgentRunOptions []AgentRunOption

	// internal properties, read only
	agentName   string
	isRootAgent bool
	entrance    EntranceType
}

func (a *AgentContext) AgentName() string {
	return a.agentName
}

func (a *AgentContext) IsRootAgent() bool {
	return a.isRootAgent
}

func (a *AgentContext) EntranceType() EntranceType {
	return a.entrance
}

type (
	runnerPassedMiddlewaresCtxKey struct{}
	runnerPassedMiddlewaresInfo   struct {
		middlewares []AgentMiddleware
		isRootAgent int32
	}
)

func isRootAgent(ctx context.Context) bool {
	if v, ok := ctx.Value(runnerPassedMiddlewaresCtxKey{}).(*runnerPassedMiddlewaresInfo); ok && v != nil {
		val := atomic.SwapInt32(&v.isRootAgent, 1)
		return val == 0
	}
	return false
}

func getRunnerPassedAgentMWs(ctx context.Context) []AgentMiddleware {
	if v, ok := ctx.Value(runnerPassedMiddlewaresCtxKey{}).(*runnerPassedMiddlewaresInfo); ok && v != nil {
		return v.middlewares
	}
	return nil
}

func isAgentMiddlewareEnabled(a Agent) bool {
	if c, ok := a.(AgentMiddlewareChecker); ok && c.IsAgentMiddlewareEnabled() {
		return true
	}
	return false
}

type agentMWRunner struct {
	beforeAgentFns []func(ctx context.Context, arc *AgentContext) (nextContext context.Context, err error)
	onEventsFns    []func(ctx context.Context, arc *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent])
}

func (a *agentMWRunner) execBeforeAgents(ctx context.Context, ac *AgentContext) (context.Context, *AsyncIterator[*AgentEvent]) {
	var err error
	for i, beforeAgent := range a.beforeAgentFns {
		if beforeAgent == nil {
			continue
		}
		ctx, err = beforeAgent(ctx, ac)
		if err != nil {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			gen.Send(&AgentEvent{Err: err})
			gen.Close()
			return ctx, a.execOnEventsFromIndex(ctx, ac, i-1, iter)
		}
	}
	return ctx, nil
}

func (a *agentMWRunner) execOnEvents(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent]) *AsyncIterator[*AgentEvent] {
	return a.execOnEventsFromIndex(ctx, ac, len(a.onEventsFns)-1, iter)
}

func (a *agentMWRunner) execOnEventsFromIndex(ctx context.Context, ac *AgentContext, fromIdx int, iter *AsyncIterator[*AgentEvent]) *AsyncIterator[*AgentEvent] {
	for idx := fromIdx; idx >= 0; idx-- {
		onEvents := a.onEventsFns[idx]
		if onEvents == nil {
			continue
		}
		i, g := NewAsyncIteratorPair[*AgentEvent]()
		go func() {
			defer func() {
				panicErr := recover()
				if panicErr != nil {
					e := safe.NewPanicErr(panicErr, debug.Stack())
					g.Send(&AgentEvent{Err: e})
				}
				g.Close()
			}()
			onEvents(ctx, ac, iter, g)
		}()
		iter = i
	}
	return iter
}

func NewAsyncIteratorPairWithConversion(
	ctx context.Context,
	iter *AsyncIterator[*AgentEvent],
	fn func(ctx context.Context, srcIter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]),
) *AsyncIterator[*AgentEvent] {
	i, g := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				g.Send(&AgentEvent{Err: e})
			}
			g.Close()
		}()

		fn(ctx, iter, g)
	}()

	return i
}

func BypassIterator(ctx context.Context, srcIter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
	defer gen.Close()
	for {
		event, ok := srcIter.Next()
		if !ok {
			break
		}
		gen.Send(event)
	}
}

type OnEventFn[T any] func(ctx context.Context, input T, event *AgentEvent) (stop bool, err error)

func NewOnEventProcessor[T any](onEvent OnEventFn[T]) func(ctx context.Context, input T, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
	return func(ctx context.Context, input T, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
		go func() {
			defer func() {
				panicErr := recover()
				if panicErr != nil {
					e := safe.NewPanicErr(panicErr, debug.Stack())
					gen.Send(&AgentEvent{Err: e})
				}
				gen.Close()
			}()

			for {
				event, ok := iter.Next()
				if !ok {
					break
				}

				breakIter, err := onEvent(ctx, input, event)
				if err != nil {
					gen.Send(&AgentEvent{Err: err})
				}
				if breakIter {
					break
				}
			}
		}()
	}
}

func iterWithEvents(events ...*AgentEvent) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	for _, event := range events {
		gen.Send(event)
	}
	gen.Close()
	return iter
}
