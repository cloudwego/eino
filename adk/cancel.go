/*
 * Copyright 2026 CloudWeGo Authors
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
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*CancelError]("_eino_adk_cancel_error")
	schema.RegisterName[*AgentCancelInfo]("_eino_adk_agent_cancel_info")
	schema.RegisterName[*cancelSafePointInfo]("_eino_adk_cancel_safe_point_info")
	schema.RegisterName[*StreamCanceledError]("_eino_adk_stream_cancelled_error")
}

// CancelMode specifies when an agent should be canceled.
// Modes can be combined with bitwise OR to cancel at multiple execution points.
// For example, CancelAfterChatModel | CancelAfterToolCalls cancels the agent
// after whichever execution point is reached first.
type CancelMode int

const (
	// CancelImmediate cancels the agent immediately without waiting
	// for any execution point.
	CancelImmediate CancelMode = 0
	// CancelAfterChatModel cancels the agent after the current chat model call
	// completes, including all streaming output.
	CancelAfterChatModel CancelMode = 1 << iota
	// CancelAfterToolCalls cancels the agent after all concurrent tool calls complete.
	CancelAfterToolCalls
)

// CancelHandle represents a cancel operation that can be waited on.
type CancelHandle struct {
	wait func() error
}

func (h *CancelHandle) Wait() error {
	return h.wait()
}

// AgentCancelFunc is called to request cancellation of a running agent.
// It returns after the cancel request is committed; use the returned handle's
// Wait to block for completion and outcome.
//
// The returned bool reports whether this call contributed to the CancelError
// for the current execution. "Contributed" means this call's cancel options
// were included before cancellation was finalized. It is false when cancellation
// was already finalized (handled or execution completed).
type AgentCancelFunc func(...AgentCancelOption) (*CancelHandle, bool)

type agentCancelConfig struct {
	Mode    CancelMode
	Timeout *time.Duration
}

// AgentCancelOption configures cancel behavior.
type AgentCancelOption func(*agentCancelConfig)

// WithAgentCancelMode sets the cancel mode for the agent cancel operation.
func WithAgentCancelMode(mode CancelMode) AgentCancelOption {
	return func(config *agentCancelConfig) {
		config.Mode = mode
	}
}

// WithAgentCancelTimeout sets a timeout for the cancel operation.
// This only applies to safe-point modes (CancelAfterChatModel, CancelAfterToolCalls):
// if the safe-point hasn't fired within this duration, the cancel escalates to
// an immediate graph interrupt.
// For CancelImmediate this timeout is ignored — the graph interrupt fires
// immediately with timeout=0.
func WithAgentCancelTimeout(timeout time.Duration) AgentCancelOption {
	return func(config *agentCancelConfig) {
		config.Timeout = &timeout
	}
}

// AgentCancelInfo contains information about a cancel operation.
type AgentCancelInfo struct {
	Mode      CancelMode
	Escalated bool
	Timeout   bool
}

// CancelError is sent via AgentEvent.Err when an agent is canceled.
// Use errors.As to match and extract *CancelError from event errors.
type CancelError struct {
	Info *AgentCancelInfo

	// CheckPointID is the checkpoint ID associated with this cancel operation.
	// When non-empty, the cancelled agent's state has been persisted under this ID
	// and can be resumed via Runner.Resume or GenInputResult.ResumeFromCheckpointID.
	CheckPointID string

	// InterruptContexts provides the interrupt contexts needed for targeted
	// resumption via Runner.ResumeWithParams. Each context represents a step
	// in the agent hierarchy that was interrupted. This is a slice because
	// composite agents (e.g. parallel workflows) may interrupt at multiple
	// points simultaneously, matching the shape of AgentAction.Interrupted.InterruptContexts.
	// Use each InterruptCtx.ID as a key in ResumeParams.Targets.
	InterruptContexts []*InterruptCtx

	interruptSignal *InterruptSignal // unexported — only Runner needs it for checkpoint
}

func (e *CancelError) Error() string {
	return fmt.Sprintf("agent canceled: mode=%v, escalated=%v", e.Info.Mode, e.Info.Escalated)
}

// Sentinel errors for cancel outcomes.
var (
	// ErrCancelTimeout is returned by CancelHandle.Wait when the cancel operation timed out.
	ErrCancelTimeout = errors.New("cancel timed out")

	// ErrExecutionCompleted is returned by CancelHandle.Wait when the agent has already finished
	// (completed, interrupted, or errored) before the cancel took effect.
	ErrExecutionCompleted = errors.New("execution already completed")

	// ErrStreamCanceled is the error sent through the stream when CancelImmediate aborts it.
	// It is a *StreamCanceledError so it can be gob-serialized during checkpoint save
	// (when stored as agentEventWrapper.StreamErr).
	ErrStreamCanceled error = &StreamCanceledError{}
)

// StreamCanceledError is the concrete error type for ErrStreamCanceled.
// It is exported so that gob can serialize it during checkpoint save when the error
// is stored in agentEventWrapper.StreamErr.
type StreamCanceledError struct{}

func (e *StreamCanceledError) Error() string {
	return "stream canceled"
}

// cancelSafePointInfo is the typed info passed to compose.Interrupt when a
// safe-point cancel condition is met (CancelAfterChatModel or CancelAfterToolCalls).
// handleRunFuncError uses type assertion on InterruptCtx.Info to distinguish
// cancel-triggered safe-point interrupts from business interrupts.
type cancelSafePointInfo struct {
	Mode CancelMode
}

// extractCancelSafePointInfo looks for a *cancelSafePointInfo in the interrupt
// contexts' Info fields. Returns nil if none is found.
func extractCancelSafePointInfo(contexts []*InterruptCtx) *cancelSafePointInfo {
	for _, ctx := range contexts {
		if ctx == nil {
			continue
		}
		if sp, ok := ctx.Info.(*cancelSafePointInfo); ok && ctx.IsRootCause {
			return sp
		}
	}
	return nil
}

// WithCancel creates an AgentRunOption that enables cancellation for an agent run.
// It returns the option to pass to Run/Resume and a cancel function.
// Cancel options (mode, timeout) are passed to the returned AgentCancelFunc at call time.
func WithCancel() (AgentRunOption, AgentCancelFunc) {
	cc := newCancelContext()
	opt := WrapImplSpecificOptFn(func(o *options) {
		o.cancelCtx = cc
	})
	cancelFn := cc.buildCancelFunc()
	return opt, cancelFn
}

// cancelContext state constants (for int32 CAS).
//
// State transition rules:
//
//	stateRunning -> stateCancelling     (cancel requested by AgentCancelFunc)
//	stateRunning -> stateDone           (execution finished: completed, interrupted, or errored)
//	stateCancelling -> stateCancelHandled (cancel path in runFunc emitted CancelError)
//	stateCancelling -> stateDone        (execution finished before cancel took effect)
//
// Terminal states: stateDone, stateCancelHandled.
//
// Note: We intentionally do NOT distinguish between "completed", "interrupted", and "errored"
// terminal states. End-users get the actual outcome (interrupt action, error) from AgentEvent.
// This simplification keeps the state machine minimal — only the cancel/non-cancel distinction
// matters for the AgentCancelFunc return value.
const (
	// stateRunning is the initial state: agent is executing normally.
	stateRunning int32 = 0
	// stateCancelling means AgentCancelFunc has been called and cancelChan is
	// closed, but the cancel has not yet been handled by the runFunc.
	stateCancelling int32 = 1
	// stateDone means execution has finished through any non-cancel path:
	// normal completion, business interrupt, or error. The specific outcome
	// is conveyed through AgentEvent, not through the cancel state machine.
	stateDone int32 = 2
	// stateCancelHandled means the cancel was processed by the runFunc and a
	// CancelError was emitted through the event stream. This is the success
	// terminal state for cancellation.
	stateCancelHandled int32 = 5
)

// interruptSent constants (for int32 CAS).
//
// Transition rules:
//
//	interruptNotSent -> interruptImmediate (CancelImmediate or escalation)
const (
	// interruptNotSent means no compose graph interrupt has been sent.
	interruptNotSent int32 = 0
	// interruptImmediate means an immediate graph interrupt was sent with
	// timeout=0, forcing the graph to stop as soon as possible.
	interruptImmediate int32 = 1
)

type cancelContextKey struct{}

// withCancelContext stores a cancelContext in the Go context.
func withCancelContext(ctx context.Context, cc *cancelContext) context.Context {
	if cc == nil {
		return ctx
	}
	return context.WithValue(ctx, cancelContextKey{}, cc)
}

// getCancelContext retrieves the cancelContext from the Go context, or nil.
func getCancelContext(ctx context.Context) *cancelContext {
	if v := ctx.Value(cancelContextKey{}); v != nil {
		return v.(*cancelContext)
	}
	return nil
}

type cancelContext struct {
	mode int32 // atomic, CancelMode

	cancelChan    chan struct{} // closed when cancel is requested (all modes, not just safe-point)
	immediateChan chan struct{} // closed when an immediate graph interrupt fires
	doneChan      chan struct{} // closed when execution completes (by any mark* method)
	doneOnce      sync.Once     // ensures doneChan is closed exactly once

	state            int32 // stateRunning, stateCancelling, stateDone, stateCancelHandled
	interruptSent    int32 // interruptNotSent, interruptImmediate
	escalated        int32 // 1 if escalated from safe-point to immediate
	timeoutEscalated int32 // 1 if escalation was triggered by timeout
	startedMode      int32 // atomic, mode when state transitioned to cancelling
	deadlineUnixNano int64 // atomic, 0 means no deadline

	cancelMu      sync.Mutex
	timeoutOnce   sync.Once
	timeoutNotify chan struct{}

	mu                  sync.Mutex
	graphInterruptFuncs []func(...compose.GraphInterruptOption)
}

func newCancelContext() *cancelContext {
	return &cancelContext{
		cancelChan:    make(chan struct{}),
		immediateChan: make(chan struct{}),
		doneChan:      make(chan struct{}),
		timeoutNotify: make(chan struct{}, 1),
	}
}

func (cc *cancelContext) getMode() CancelMode {
	if cc == nil {
		return CancelImmediate
	}
	return CancelMode(atomic.LoadInt32(&cc.mode))
}

func (cc *cancelContext) setMode(mode CancelMode) {
	atomic.StoreInt32(&cc.mode, int32(mode))
}

func (cc *cancelContext) getDeadlineUnixNano() int64 {
	return atomic.LoadInt64(&cc.deadlineUnixNano)
}

func (cc *cancelContext) setDeadlineUnixNano(v int64) {
	atomic.StoreInt64(&cc.deadlineUnixNano, v)
}

func (cc *cancelContext) wakeTimeoutController() {
	select {
	case cc.timeoutNotify <- struct{}{}:
	default:
	}
}

// shouldCancel returns true if a cancel has been requested (cancelChan is closed).
func (cc *cancelContext) shouldCancel() bool {
	if cc == nil {
		return false
	}
	select {
	case <-cc.cancelChan:
		return true
	default:
		return false
	}
}

// sendImmediateInterrupt sends the compose graph interrupt signal via graphInterruptFuncs.
// Also closes immediateChan (used by cancelMonitoredModel to abort an in-progress stream).
// Returns false if an interrupt was already sent or if no graphInterruptFuncs have been
// registered yet (the deferred fire in setGraphInterruptFunc will handle that case).
func (cc *cancelContext) sendImmediateInterrupt() bool {
	if !atomic.CompareAndSwapInt32(&cc.interruptSent, interruptNotSent, interruptImmediate) {
		return false
	}

	close(cc.immediateChan)

	// Hold the lock across both the snapshot and the iteration. This prevents
	// setGraphInterruptFunc from appending a new function and retroactively
	// firing it between the snapshot and our iteration — which would call the
	// same compose interrupt function twice (compose.WithGraphInterrupt returns
	// a non-idempotent closure that panics on double-call).
	cc.mu.Lock()
	fns := make([]func(...compose.GraphInterruptOption), len(cc.graphInterruptFuncs))
	copy(fns, cc.graphInterruptFuncs)

	if len(fns) == 0 {
		cc.mu.Unlock()
		return false
	}

	// Holding mu across iteration prevents double-fire but couples lock hold
	// time to compose interrupt execution. If these functions ever block, this
	// becomes a latency or deadlock risk.
	for _, fn := range fns {
		fn(compose.WithGraphInterruptTimeout(0))
	}
	cc.mu.Unlock()
	return true
}

// setGraphInterruptFunc appends a graph interrupt function to the list.
// If an immediate cancel was already requested, fires it retroactively.
// Multiple functions can be registered (e.g. one per parallel sub-agent).
func (cc *cancelContext) setGraphInterruptFunc(interrupt func(...compose.GraphInterruptOption)) {
	cc.mu.Lock()
	cc.graphInterruptFuncs = append(cc.graphInterruptFuncs, interrupt)

	// If immediate cancel was already requested but couldn't fire because
	// no graphInterruptFuncs were registered yet, fire now. This covers the
	// race where cancelFn is called before the compose graph is compiled.
	// Holding the lock here prevents sendInterrupt from iterating the list
	// concurrently, which would double-fire the same function.
	shouldFire := atomic.LoadInt32(&cc.interruptSent) == interruptImmediate
	if shouldFire {
		interrupt(compose.WithGraphInterruptTimeout(0))
	}
	cc.mu.Unlock()
}

// markDone marks the execution as finished through any non-cancel path
// (normal completion, business interrupt, or error).
// This is safe to call even if a cancel is in progress — it allows the
// cancel func to detect that execution finished before cancel took effect.
func (cc *cancelContext) markDone() {
	if atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateDone) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
		return
	}
	// If cancel was requested but execution finished (cancel path was
	// not reached, e.g. execution finished before interrupt took effect):
	if atomic.CompareAndSwapInt32(&cc.state, stateCancelling, stateDone) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
	}
	// If state is already a terminal state (markCancelHandled was called),
	// this is a no-op — doneChan was already closed.
}

// markCancelHandled signals that the cancel path in the runFunc has created
// and sent a CancelError. Transitions state to stateCancelHandled so that:
// 1. The deferred markDone() becomes a no-op (CAS from cancelling fails).
// 2. buildCancelFunc sees stateCancelHandled and returns nil (cancel succeeded).
// Returns true if the transition succeeded, false if cancel was already handled
// (e.g., by a sub-agent). This prevents duplicate CancelError emission.
func (cc *cancelContext) markCancelHandled() bool {
	if atomic.CompareAndSwapInt32(&cc.state, stateCancelling, stateCancelHandled) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
		return true
	}
	return false
}

// createCancelError creates a CancelError based on the current cancel state.
func (cc *cancelContext) createCancelError() *CancelError {
	info := &AgentCancelInfo{}
	info.Mode = cc.getMode()
	if atomic.LoadInt32(&cc.escalated) == 1 {
		info.Escalated = true
		info.Timeout = atomic.LoadInt32(&cc.timeoutEscalated) == 1
	}
	return &CancelError{
		Info: info,
	}
}

func (cc *cancelContext) createAndMarkCancelHandled() (*CancelError, bool) {
	cc.cancelMu.Lock()
	defer cc.cancelMu.Unlock()
	cancelErr := cc.createCancelError()
	ok := cc.markCancelHandled()
	return cancelErr, ok
}

// buildCancelFunc builds the AgentCancelFunc for external use.
func (cc *cancelContext) buildCancelFunc() AgentCancelFunc {
	joinMode := func(a, b CancelMode) CancelMode {
		if a == CancelImmediate || b == CancelImmediate {
			return CancelImmediate
		}
		return a | b
	}

	parseReq := func(callOpts ...AgentCancelOption) *agentCancelConfig {
		cfg := &agentCancelConfig{Mode: CancelImmediate}
		for _, opt := range callOpts {
			opt(cfg)
		}
		return cfg
	}

	startTimeoutController := func() {
		cc.timeoutOnce.Do(func() {
			go func() {
				for {
					select {
					case <-cc.doneChan:
						return
					default:
					}

					mode := cc.getMode()
					if mode == CancelImmediate {
						return
					}

					deadline := cc.getDeadlineUnixNano()
					if deadline == 0 {
						select {
						case <-cc.timeoutNotify:
							continue
						case <-cc.doneChan:
							return
						}
					}

					now := time.Now().UnixNano()
					wait := time.Duration(deadline - now)
					if wait <= 0 {
						atomic.StoreInt32(&cc.escalated, 1)
						atomic.StoreInt32(&cc.timeoutEscalated, 1)
						cc.sendImmediateInterrupt()
						return
					}

					timer := time.NewTimer(wait)
					select {
					case <-timer.C:
						timer.Stop()
						atomic.StoreInt32(&cc.escalated, 1)
						atomic.StoreInt32(&cc.timeoutEscalated, 1)
						cc.sendImmediateInterrupt()
						return
					case <-cc.timeoutNotify:
						timer.Stop()
						continue
					case <-cc.doneChan:
						timer.Stop()
						return
					}
				}
			}()
		})
	}

	newHandle := func(wait func() error) *CancelHandle {
		return &CancelHandle{wait: wait}
	}

	waitForCompletion := func() error {
		<-cc.doneChan

		st := atomic.LoadInt32(&cc.state)
		switch st {
		case stateDone:
			return ErrExecutionCompleted
		default:
			if atomic.LoadInt32(&cc.timeoutEscalated) == 1 {
				return ErrCancelTimeout
			}
			return nil
		}
	}

	return func(callOpts ...AgentCancelOption) (*CancelHandle, bool) {
		req := parseReq(callOpts...)

		st := atomic.LoadInt32(&cc.state)
		switch st {
		case stateCancelHandled:
			return newHandle(func() error { return nil }), false
		case stateDone:
			return newHandle(func() error { return ErrExecutionCompleted }), false
		}

		var needImmediate, needTimeoutCtl bool

		cc.cancelMu.Lock()

		st = atomic.LoadInt32(&cc.state)
		switch st {
		case stateCancelHandled:
			cc.cancelMu.Unlock()
			return newHandle(func() error { return nil }), false
		case stateDone:
			cc.cancelMu.Unlock()
			return newHandle(func() error { return ErrExecutionCompleted }), false
		}

		curMode := cc.getMode()
		if st == stateRunning {
			if !atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateCancelling) {
				st = atomic.LoadInt32(&cc.state)
				cc.cancelMu.Unlock()
				if st == stateDone {
					return newHandle(func() error { return ErrExecutionCompleted }), false
				}
				return newHandle(waitForCompletion), true
			}

			curMode = req.Mode
			cc.setMode(curMode)
			atomic.StoreInt32(&cc.startedMode, int32(curMode))
			close(cc.cancelChan)
		} else {
			curMode = joinMode(curMode, req.Mode)
			cc.setMode(curMode)
		}

		if curMode == CancelImmediate {
			cc.setDeadlineUnixNano(0)
			needImmediate = true
		} else if req.Timeout != nil && *req.Timeout > 0 {
			proposed := time.Now().Add(*req.Timeout).UnixNano()
			existing := cc.getDeadlineUnixNano()
			if existing == 0 || proposed < existing {
				cc.setDeadlineUnixNano(proposed)
				cc.wakeTimeoutController()
			}
			needTimeoutCtl = cc.getDeadlineUnixNano() != 0
		}

		cc.cancelMu.Unlock()

		if needImmediate {
			if atomic.LoadInt32(&cc.startedMode) != int32(CancelImmediate) {
				atomic.StoreInt32(&cc.escalated, 1)
			}
			cc.sendImmediateInterrupt()
		}
		if needTimeoutCtl {
			startTimeoutController()
		}

		return newHandle(waitForCompletion), true
	}
}

// wrapIterWithMarkDone wraps an AsyncIterator so that markDone fires only when
// the inner iterator is fully drained. This is used by flowAgent for the
// workflowAgent path where the outer flowAgent owns the cancel lifecycle but
// the workflowAgent produces the stream asynchronously.
func wrapIterWithMarkDone(iter *AsyncIterator[*AgentEvent], cc *cancelContext) *AsyncIterator[*AgentEvent] {
	if cc == nil {
		return iter
	}
	outIter, outGen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer cc.markDone()
		defer outGen.Close()
		for {
			event, ok := iter.Next()
			if !ok {
				return
			}
			outGen.Send(event)
		}
	}()
	return outIter
}

// cancelMonitoredModel wraps a model with cancel monitoring.
// Generate: pure delegate to the inner model (CancelAfterChatModel is handled
// by a dedicated node after the ChatModel in the compose graph).
// Stream: pipes chunks through a goroutine that selects on immediateChan for
// CancelImmediate abort.
type cancelMonitoredModel struct {
	inner         model.BaseChatModel
	cancelContext *cancelContext
}

type recvResult[T any] struct {
	data T
	err  error
}

func (m *cancelMonitoredModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.inner.Generate(ctx, input, opts...)
}

func (m *cancelMonitoredModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	wrapped := wrapStreamWithCancelMonitoring(stream, m.cancelContext)
	return wrapped, nil
}

// wrapStreamWithCancelMonitoring wraps a stream with cancel monitoring.
// When immediateChan fires (CancelImmediate or timeout escalation), the output
// stream is terminated with ErrStreamCanceled.
func wrapStreamWithCancelMonitoring[T any](stream *schema.StreamReader[T], cc *cancelContext) *schema.StreamReader[T] {
	if cc == nil {
		return stream
	}

	// Already canceled — terminate immediately
	select {
	case <-cc.immediateChan:
		stream.Close()
		r, w := schema.Pipe[T](1)
		var zero T
		w.Send(zero, ErrStreamCanceled)
		w.Close()
		return r
	default:
	}

	reader, writer := schema.Pipe[T](1)

	go func() {
		done := make(chan struct{})
		defer close(done)
		defer writer.Close()
		defer stream.Close()

		ch := make(chan recvResult[T])
		go func() {
			defer close(ch)
			for {
				chunk, recvErr := stream.Recv()
				select {
				case ch <- recvResult[T]{chunk, recvErr}:
				case <-done:
					return
				}
				if recvErr != nil {
					return
				}
			}
		}()

		for {
			select {
			case <-cc.immediateChan:
				var zero T
				writer.Send(zero, ErrStreamCanceled)
				return

			case r, ok := <-ch:
				if !ok {
					return
				}
				if r.err != nil {
					if r.err == io.EOF {
						return
					}
					var zero T
					writer.Send(zero, r.err)
					return
				}
				if closed := writer.Send(r.data, nil); closed {
					return
				}
			}
		}
	}()

	return reader
}

// cancelMonitoredToolHandler wraps streamable tool calls with cancel monitoring.
// When CancelImmediate fires, the tool output stream is terminated with ErrStreamCanceled.
// This handler reads the cancelContext from the Go context via getCancelContext.
type cancelMonitoredToolHandler struct{}

func (h *cancelMonitoredToolHandler) WrapStreamableToolCall(next compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		output, err := next(ctx, input)
		if err != nil {
			return nil, err
		}

		cc := getCancelContext(ctx)
		if cc == nil {
			return output, nil
		}

		wrapped := wrapStreamWithCancelMonitoring(output.Result, cc)
		return &compose.StreamToolOutput{Result: wrapped}, nil
	}
}

func (h *cancelMonitoredToolHandler) WrapEnhancedStreamableToolCall(next compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
		output, err := next(ctx, input)
		if err != nil {
			return nil, err
		}

		cc := getCancelContext(ctx)
		if cc == nil {
			return output, nil
		}

		wrapped := wrapStreamWithCancelMonitoring(output.Result, cc)
		return &compose.EnhancedStreamableToolOutput{Result: wrapped}, nil
	}
}
