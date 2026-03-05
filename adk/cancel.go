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

// AgentCancelFunc is called to cancel a running agent.
// Returns nil on success, or a sentinel error indicating the outcome.
type AgentCancelFunc func(...AgentCancelOption) error

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

// CancelError is sent via AgentEvent.Err when an agent is cancelled.
// Use errors.As to extract it from event errors.
type CancelError struct {
	Info *AgentCancelInfo

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
	return fmt.Sprintf("agent cancelled: mode=%v, escalated=%v", e.Info.Mode, e.Info.Escalated)
}

// Unwrap returns nil intentionally. This prevents errors.Is from accidentally
// matching through the chain. Use errors.As to match *CancelError.
func (e *CancelError) Unwrap() error {
	return nil
}

// Sentinel errors for cancel outcomes.
var (
	// ErrCancelTimeout is returned by AgentCancelFunc when the cancel operation timed out.
	ErrCancelTimeout = errors.New("cancel timed out")

	// ErrExecutionCompleted is returned by AgentCancelFunc when the agent has already completed.
	ErrExecutionCompleted = errors.New("execution already completed")

	// ErrExecutionInterrupted is returned by AgentCancelFunc when the agent was already interrupted.
	ErrExecutionInterrupted = errors.New("execution already interrupted by business logic")

	// ErrExecutionFailed is returned by AgentCancelFunc when the agent has already errored out.
	ErrExecutionFailed = errors.New("execution already failed")

	// ErrStreamCancelled is the error sent through the stream when CancelImmediate aborts it.
	ErrStreamCancelled = errors.New("stream cancelled")
)

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
//	stateRunning -> stateCompleted      (execution finished normally)
//	stateRunning -> stateInterrupted    (business logic interrupt, not cancel-triggered)
//	stateRunning -> stateError          (execution failed with an error)
//	stateCancelling -> stateCancelHandled (cancel path in runFunc emitted CancelError)
//	stateCancelling -> stateCompleted   (execution completed before cancel took effect)
//	stateCancelling -> stateInterrupted (business interrupt arrived during cancel)
//	stateCancelling -> stateError       (execution errored during cancel)
//
// Terminal states: stateCompleted, stateInterrupted, stateError, stateCancelHandled.
const (
	// stateRunning is the initial state: agent is executing normally.
	stateRunning int32 = 0
	// stateCancelling means AgentCancelFunc has been called and cancelChan is
	// closed, but the cancel has not yet been handled by the runFunc.
	stateCancelling int32 = 1
	// stateCompleted means execution finished normally (no cancel, no error).
	stateCompleted int32 = 2
	// stateInterrupted means execution was interrupted by business logic
	// (e.g. a compose.Interrupt not triggered by cancellation).
	stateInterrupted int32 = 3
	// stateError means execution failed with a non-cancel, non-interrupt error.
	stateError int32 = 4
	// stateCancelHandled means the cancel was processed by the runFunc and a
	// CancelError was emitted through the event stream. This is the success
	// terminal state for cancellation.
	stateCancelHandled int32 = 5
)

// interruptSent constants (for int32 CAS).
//
// Transition rules:
//
//	interruptNotSent -> interruptGraceful   (safe-point cancel: no graph interrupt sent yet)
//	interruptNotSent -> interruptImmediate  (CancelImmediate or escalation from not-sent)
//	interruptGraceful -> interruptImmediate (escalation: safe-point timed out)
const (
	// interruptNotSent means no compose graph interrupt has been sent.
	interruptNotSent int32 = 0
	// interruptGraceful means a safe-point cancel was requested; the graph
	// interrupt will fire only when the safe-point condition is met
	// (e.g. after chat model or tool calls complete).
	interruptGraceful int32 = 1
	// interruptImmediate means an immediate graph interrupt was sent with
	// timeout=0, forcing the graph to stop as soon as possible.
	interruptImmediate int32 = 2
)

type cancelContext struct {
	config *agentCancelConfig

	cancelChan    chan struct{} // closed when cancel is requested (all modes, not just safe-point)
	immediateChan chan struct{} // closed when an immediate graph interrupt fires
	doneChan      chan struct{} // closed when execution completes (by any mark* method)
	doneOnce      sync.Once     // ensures doneChan is closed exactly once

	state         int32 // stateRunning, stateCancelling, stateCompleted, stateInterrupted, stateError, stateCancelHandled
	interruptSent int32 // interruptNotSent, interruptGraceful, interruptImmediate
	escalated     int32 // 1 if escalated from safe-point to immediate

	mu                 sync.Mutex
	graphInterruptFunc func(...compose.GraphInterruptOption)
}

func newCancelContext() *cancelContext {
	return &cancelContext{
		cancelChan:    make(chan struct{}),
		immediateChan: make(chan struct{}),
		doneChan:      make(chan struct{}),
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

// sendInterrupt sends the compose graph interrupt signal via graphInterruptFunc.
// If immediate is true, also closes immediateChan (used by cancelMonitoredModel
// to abort an in-progress stream). Returns false if an interrupt was already sent
// or if graphInterruptFunc has not been set yet (the deferred fire in
// setGraphInterruptFunc will handle that case).
func (cc *cancelContext) sendInterrupt(immediate bool) bool {
	target := interruptGraceful
	if immediate {
		target = interruptImmediate
	}
	if !atomic.CompareAndSwapInt32(&cc.interruptSent, interruptNotSent, target) {
		return false
	}

	cc.mu.Lock()
	fn := cc.graphInterruptFunc
	cc.mu.Unlock()

	if immediate {
		close(cc.immediateChan)
	}

	if fn == nil {
		return false
	}

	if immediate {
		fn(compose.WithGraphInterruptTimeout(0))
	} else {
		fn()
	}
	return true
}

// escalateToImmediate upgrades a safe-point cancel to an immediate graph
// interrupt. Called by the timeout goroutine when the safe-point hasn't fired
// within the configured duration.
func (cc *cancelContext) escalateToImmediate() {
	atomic.StoreInt32(&cc.escalated, 1)

	// Try to upgrade interruptSent from graceful to immediate
	if atomic.CompareAndSwapInt32(&cc.interruptSent, interruptGraceful, interruptImmediate) {
		close(cc.immediateChan)

		cc.mu.Lock()
		fn := cc.graphInterruptFunc
		cc.mu.Unlock()

		if fn != nil {
			fn(compose.WithGraphInterruptTimeout(0))
		}
		return
	}

	// If no interrupt was sent yet, send an immediate one
	cc.sendInterrupt(true)
}

// setGraphInterruptFunc stores the graph interrupt function.
// If an immediate cancel was already requested, fires it retroactively.
func (cc *cancelContext) setGraphInterruptFunc(interrupt func(...compose.GraphInterruptOption)) {
	cc.mu.Lock()
	cc.graphInterruptFunc = interrupt
	cc.mu.Unlock()

	// If immediate cancel was already requested but couldn't fire because
	// graphInterruptFunc wasn't set yet, fire now. This covers the race where
	// cancelFn is called before the compose graph is compiled and sets the func.
	if atomic.LoadInt32(&cc.interruptSent) == interruptImmediate {
		cc.mu.Lock()
		fn := cc.graphInterruptFunc
		cc.mu.Unlock()
		if fn != nil {
			fn(compose.WithGraphInterruptTimeout(0))
		}
	}
}

// markCompleted marks the execution as completed normally.
func (cc *cancelContext) markCompleted() {
	if atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateCompleted) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
		return
	}
	// If cancel was requested but execution completed naturally (cancel path was
	// not reached, e.g. execution finished before interrupt took effect):
	if atomic.CompareAndSwapInt32(&cc.state, stateCancelling, stateCompleted) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
	}
	// If state is already a terminal state (markCancelHandled/markError/markInterrupted
	// was called), this is a no-op — doneChan was already closed.
}

// markCancelHandled signals that the cancel path in the runFunc has created
// and sent a CancelError. Transitions state to stateCancelHandled so that:
// 1. The deferred markCompleted() becomes a no-op (CAS from cancelling fails).
// 2. buildCancelFunc sees stateCancelHandled and returns nil (cancel succeeded).
func (cc *cancelContext) markCancelHandled() {
	atomic.CompareAndSwapInt32(&cc.state, stateCancelling, stateCancelHandled)
	cc.doneOnce.Do(func() { close(cc.doneChan) })
}

// markInterrupted marks the execution as interrupted by business logic.
func (cc *cancelContext) markInterrupted() {
	if atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateInterrupted) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
		return
	}
	if atomic.CompareAndSwapInt32(&cc.state, stateCancelling, stateInterrupted) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
	}
}

// markError marks the execution as failed with an error.
func (cc *cancelContext) markError() {
	if atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateError) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
		return
	}
	if atomic.CompareAndSwapInt32(&cc.state, stateCancelling, stateError) {
		cc.doneOnce.Do(func() { close(cc.doneChan) })
	}
}

// createCancelError creates a CancelError based on the current cancel state.
func (cc *cancelContext) createCancelError() *CancelError {
	info := &AgentCancelInfo{}
	if cc.config != nil {
		info.Mode = cc.config.Mode
	}
	if atomic.LoadInt32(&cc.escalated) == 1 {
		info.Escalated = true
		// Timeout is true when a safe-point mode was escalated to immediate
		// because the safe-point didn't fire within the configured duration.
		// For CancelImmediate the escalation flag is meaningless (already immediate).
		if cc.config != nil && cc.config.Mode != CancelImmediate {
			info.Timeout = true
		}
	}
	return &CancelError{
		Info: info,
	}
}

// buildCancelFunc builds the AgentCancelFunc for external use.
func (cc *cancelContext) buildCancelFunc() AgentCancelFunc {
	var once sync.Once
	var result error

	return func(callOpts ...AgentCancelOption) error {
		cfg := &agentCancelConfig{
			Mode: CancelImmediate,
		}
		for _, opt := range callOpts {
			opt(cfg)
		}

		once.Do(func() {
			cc.config = cfg

			// Transition to cancelling
			if !atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateCancelling) {
				// Execution already finished
				st := atomic.LoadInt32(&cc.state)
				switch st {
				case stateCompleted:
					result = ErrExecutionCompleted
				case stateInterrupted:
					result = ErrExecutionInterrupted
				case stateError:
					result = ErrExecutionFailed
				}
				return
			}

			// Close cancelChan to signal safe-point listeners
			close(cc.cancelChan)

			if cfg.Mode == CancelImmediate {
				cc.sendInterrupt(true)
			} else {
				// Safe-point mode: dedicated cancel check nodes and toolPostHandle will
				// check shouldCancel() and call compose.Interrupt when their
				// safe-point condition is met. No graph interrupt is sent.
			}

			if cfg.Timeout != nil && *cfg.Timeout > 0 && cfg.Mode != CancelImmediate {
				go func() {
					timer := time.NewTimer(*cfg.Timeout)
					defer timer.Stop()
					select {
					case <-timer.C:
						cc.escalateToImmediate()
					case <-cc.doneChan:
					}
				}()
			}

			// Wait for execution to finish
			<-cc.doneChan

			st := atomic.LoadInt32(&cc.state)
			switch st {
			case stateCompleted:
				result = ErrExecutionCompleted
			case stateInterrupted:
				result = ErrExecutionInterrupted
			case stateError:
				result = ErrExecutionFailed
			default:
				// stateCancelHandled: cancel was processed and CancelError was emitted.
				if atomic.LoadInt32(&cc.escalated) == 1 && cfg.Mode != CancelImmediate {
					result = ErrCancelTimeout
				} else {
					result = nil
				}
			}
		})

		return result
	}
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

	reader, writer := schema.Pipe[*schema.Message](1)
	cc := m.cancelContext

	go func() {
		// done is closed when this goroutine exits, unblocking the recv helper
		// if it's stuck trying to send to ch.
		done := make(chan struct{})
		defer close(done)
		defer writer.Close()
		defer stream.Close()

		ch := make(chan recvResult[*schema.Message])
		go func() {
			defer close(ch)
			for {
				chunk, recvErr := stream.Recv()
				select {
				case ch <- recvResult[*schema.Message]{chunk, recvErr}:
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
				// CancelImmediate or timeout escalation — abort stream
				writer.Send(nil, ErrStreamCancelled)
				return
				// defers: stream.Close() unblocks inner Recv,
				//         close(done) unblocks inner ch send

			case r, ok := <-ch:
				if !ok {
					return // inner goroutine exited unexpectedly
				}
				if r.err != nil {
					if r.err == io.EOF {
						return
					}
					writer.Send(nil, r.err)
					return
				}
				if closed := writer.Send(r.data, nil); closed {
					return
				}
			}
		}
	}()

	return reader, nil
}
