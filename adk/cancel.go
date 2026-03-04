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
	"encoding/gob"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/compose"
)

func init() {
	gob.Register(&CancelError{})
	gob.Register(&AgentCancelInfo{})
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

// WithAgentCancelTimeout sets a timeout duration for CancelImmediate mode.
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
	Info            *AgentCancelInfo
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

// WithCancel creates an AgentRunOption that enables cancellation for an agent run.
// It returns the option to pass to Run/Resume and a cancel function.
func WithCancel(opts ...AgentCancelOption) (AgentRunOption, AgentCancelFunc) {
	cc := newCancelContext()
	opt := WrapImplSpecificOptFn(func(o *options) {
		o.cancelCtx = cc
	})
	cancelFn := cc.buildCancelFunc(opts...)
	return opt, cancelFn
}

// cancelContext state constants (for int32 CAS).
const (
	stateRunning       int32 = 0
	stateCancelling    int32 = 1
	stateCompleted     int32 = 2
	stateInterrupted   int32 = 3
	stateError         int32 = 4
	stateCancelHandled int32 = 5 // cancel was handled by the runFunc
)

// interruptSent constants (for int32 CAS).
const (
	interruptNotSent   int32 = 0
	interruptGraceful  int32 = 1
	interruptImmediate int32 = 2
)

type cancelContext struct {
	config *agentCancelConfig

	cancelChan    chan struct{} // closed when cancel requested (safe-point modes)
	immediateChan chan struct{} // closed when immediate cancel fires
	doneChan      chan struct{} // closed when execution completes
	doneOnce      sync.Once     // ensures doneChan is closed exactly once

	state         int32 // stateRunning, stateCancelling, stateCompleted, stateInterrupted, stateError
	interruptSent int32 // interruptNotSent, interruptGraceful, interruptImmediate
	escalated     int32 // 1 if escalated to immediate

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

// sendInterrupt sends the graph interrupt signal.
// Returns true if the interrupt was successfully sent.
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

// escalateToImmediate upgrades a safe-point cancel to an immediate cancel.
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

// triggerSafePointInterrupt triggers the graph interrupt at a safe-point
// (after chat model or after tool calls).
func (cc *cancelContext) triggerSafePointInterrupt() {
	cc.sendInterrupt(false)
}

// setGraphInterruptFunc stores the graph interrupt function.
// If an immediate cancel was already requested, fires it retroactively.
func (cc *cancelContext) setGraphInterruptFunc(interrupt func(...compose.GraphInterruptOption)) {
	cc.mu.Lock()
	cc.graphInterruptFunc = interrupt
	cc.mu.Unlock()

	// If immediate cancel was already requested but couldn't fire (func wasn't set),
	// fire now to cover the Invoke-already-running race.
	if atomic.LoadInt32(&cc.interruptSent) == interruptImmediate {
		interrupt(compose.WithGraphInterruptTimeout(0))
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
	}
	return &CancelError{
		Info: info,
	}
}

// buildCancelFunc builds the AgentCancelFunc for external use.
// The defaultOpts are applied as defaults when the cancel func is called.
func (cc *cancelContext) buildCancelFunc(defaultOpts ...AgentCancelOption) AgentCancelFunc {
	var once sync.Once
	var result error

	return func(callOpts ...AgentCancelOption) error {
		cfg := &agentCancelConfig{
			Mode: CancelImmediate,
		}
		// Apply default options first, then call-time options
		for _, opt := range defaultOpts {
			opt(cfg)
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
				// Safe-point mode: safe-point checks will trigger the interrupt
				// when they detect shouldCancel() == true.
			}

			if cfg.Timeout != nil && *cfg.Timeout > 0 {
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
				// Cancel succeeded (stateCancelling or stateCancelHandled)
				result = nil
			}
		})

		return result
	}
}
