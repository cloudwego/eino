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
	"fmt"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/safe"
)

type turnLoopCancelSig struct {
	done   chan struct{}
	config atomic.Value
}

func newTurnLoopCancelSig() *turnLoopCancelSig {
	return &turnLoopCancelSig{
		done: make(chan struct{}),
	}
}

func (cs *turnLoopCancelSig) cancel(cfg *cancelConfig) {
	cs.config.Store(cfg)
	close(cs.done)
}

func (cs *turnLoopCancelSig) isCancelled() bool {
	select {
	case <-cs.done:
		return true
	default:
		return false
	}
}

func (cs *turnLoopCancelSig) getConfig() *cancelConfig {
	if v := cs.config.Load(); v != nil {
		return v.(*cancelConfig)
	}
	return nil
}

func (cs *turnLoopCancelSig) getDoneChan() <-chan struct{} {
	if cs != nil {
		return cs.done
	}
	return nil
}

type preemptSignal struct {
	mu         sync.Mutex
	signaled   bool
	cancelOpts []CancelOption
}

func newPreemptSignal() *preemptSignal {
	return &preemptSignal{}
}

func (s *preemptSignal) signal(opts ...CancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.signaled {
		s.signaled = true
		s.cancelOpts = opts
	}
}

func (s *preemptSignal) check() (bool, []CancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.signaled {
		opts := s.cancelOpts
		s.signaled = false
		s.cancelOpts = nil
		return true, opts
	}
	return false, nil
}

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any] struct {
	// GetAgent returns the Agent to run for the consumed items.
	// The consumed slice contains items that GenInput decided to process in this turn.
	// Required.
	GetAgent func(ctx context.Context, consumed []T) (Agent, error)

	// GenInput receives all buffered items and decides what to process.
	// It returns which items to consume now vs keep for later turns.
	// Required.
	GenInput func(ctx context.Context, items []T) (*GenInputResult[T], error)

	// OnAgentEvents is called to handle events emitted by the agent.
	// The consumed slice contains items that triggered this agent execution.
	// Optional. If not provided, events are drained silently.
	OnAgentEvents func(ctx context.Context, consumed []T, events *AsyncIterator[*AgentEvent]) error

	// Store is the checkpoint store for persistence and resume. Optional.
	Store CheckPointStore
}

// GenInputResult contains the result of GenInput processing.
type GenInputResult[T any] struct {
	// Input is the agent input to execute
	Input *AgentInput

	// RunOpts are the options for this agent run
	RunOpts []AgentRunOption

	// CheckpointID for this turn (used for interrupt/resume)
	CheckpointID string

	// IsResume indicates this is a resume run (not a fresh run).
	// When true, CheckpointID must be set.
	IsResume bool

	// ResumeParams contains parameters for resuming an interrupted agent.
	// Only used when IsResume is true.
	// If nil, uses implicit "resume all" strategy.
	ResumeParams *ResumeParams

	// Consumed are the items that were processed (will be removed from buffer)
	Consumed []T

	// Remaining are the items to keep in buffer for next turn
	Remaining []T
}

// TurnLoopResult is the result returned when TurnLoop exits.
type TurnLoopResult[T any] struct {
	// Error is the error that caused the loop to exit, if any.
	Error error

	// UnhandledItems contains items that were buffered but not processed.
	UnhandledItems []T
}

// TurnLoop is a push-based event loop for agent execution.
// Users push items via Push() and the loop processes them through the agent.
type TurnLoop[T any] struct {
	config TurnLoopConfig[T]

	buffer *internal.UnboundedChan[T]

	stopped int32

	done chan struct{}

	result *TurnLoopResult[T]

	cancelOnce sync.Once

	cancelSig *turnLoopCancelSig

	preemptSig *preemptSignal

	cancelErr error

	cancelCfg *cancelConfig

	runErr error
}

type pushConfig struct {
	preempt    bool
	cancelOpts []CancelOption
}

// PushOption is an option for Push().
type PushOption func(*pushConfig)

// WithPreempt signals that the current agent should be canceled after pushing.
// This enables atomic "push + preempt" to avoid race conditions between
// pushing an urgent item and triggering preemption.
// The loop will cancel the current agent turn and continue with the next turn,
// where GenInput will see all buffered items including the newly pushed one.
func WithPreempt(cancelOpts ...CancelOption) PushOption {
	return func(cfg *pushConfig) {
		cfg.preempt = true
		cfg.cancelOpts = cancelOpts
	}
}

// RunTurnLoop creates and starts a new TurnLoop.
// The loop runs in a background goroutine and processes items pushed via Push().
// Use Cancel() to stop the loop and Wait() to get the result.
func RunTurnLoop[T any](ctx context.Context, cfg TurnLoopConfig[T]) *TurnLoop[T] {
	l := &TurnLoop[T]{
		config:     cfg,
		buffer:     internal.NewUnboundedChan[T](),
		done:       make(chan struct{}),
		cancelSig:  newTurnLoopCancelSig(),
		preemptSig: newPreemptSignal(),
	}
	go l.run(ctx)
	return l
}

// Push adds an item to the loop's buffer for processing.
// This method is non-blocking and thread-safe.
// Returns false if the loop has stopped, true otherwise.
//
// Use WithPreempt() to atomically push an item and signal preemption of the current agent.
// This is useful for urgent items that should interrupt the current processing.
func (l *TurnLoop[T]) Push(item T, opts ...PushOption) (ok bool) {
	cfg := &pushConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if atomic.LoadInt32(&l.stopped) != 0 {
		return false
	}

	defer func() {
		if r := recover(); r != nil {
			ok = false
		}
	}()

	l.buffer.Send(item)

	if cfg.preempt {
		l.preemptSig.signal(cfg.cancelOpts...)
	}

	return true
}

// Cancel signals the loop to stop. This method is non-blocking and idempotent.
// Use Wait() to block until the loop exits and get the result.
func (l *TurnLoop[T]) Cancel(opts ...CancelOption) {
	l.cancelOnce.Do(func() {
		cfg := &cancelConfig{
			Mode: CancelImmediate,
		}
		for _, opt := range opts {
			opt(cfg)
		}

		l.cancelCfg = cfg
		l.cancelSig.cancel(cfg)

		atomic.StoreInt32(&l.stopped, 1)

		l.buffer.Close()
	})
}

// Wait blocks until the loop exits and returns the result.
// This method is safe to call from multiple goroutines.
// All callers will receive the same result.
func (l *TurnLoop[T]) Wait() *TurnLoopResult[T] {
	<-l.done
	return l.result
}

func (l *TurnLoop[T]) run(ctx context.Context) {
	defer l.cleanup()

	for {
		if err := ctx.Err(); err != nil {
			l.runErr = err
			return
		}
		if l.cancelSig.isCancelled() {
			return
		}

		first, ok := l.buffer.Receive()
		if !ok {
			return
		}

		if err := ctx.Err(); err != nil {
			l.buffer.PushFront([]T{first})
			l.runErr = err
			return
		}
		if l.cancelSig.isCancelled() {
			l.buffer.PushFront([]T{first})
			return
		}

		rest := l.buffer.TakeAll()
		items := append([]T{first}, rest...)

		result, err := l.config.GenInput(ctx, items)
		if err != nil {
			l.runErr = err
			return
		}

		l.buffer.PushFront(result.Remaining)

		if ctxErr := ctx.Err(); ctxErr != nil {
			l.buffer.PushFront(result.Consumed)
			l.runErr = ctxErr
			return
		}
		if l.cancelSig.isCancelled() {
			l.buffer.PushFront(result.Consumed)
			return
		}

		agent, err := l.config.GetAgent(ctx, result.Consumed)
		if err != nil {
			l.buffer.PushFront(result.Consumed)
			l.runErr = err
			return
		}

		if ctxErr := ctx.Err(); ctxErr != nil {
			l.buffer.PushFront(result.Consumed)
			l.runErr = ctxErr
			return
		}
		if l.cancelSig.isCancelled() {
			l.buffer.PushFront(result.Consumed)
			return
		}

		preempted, runErr := l.runAgentAndHandleEvents(ctx, agent, result)
		if runErr != nil {
			l.runErr = runErr
			return
		}
		if preempted {
			l.buffer.PushFront(result.Consumed)
			continue
		}
	}
}

func (l *TurnLoop[T]) runAgentAndHandleEvents(
	ctx context.Context,
	agent Agent,
	result *GenInputResult[T],
) (preempted bool, err error) {
	var iter *AsyncIterator[*AgentEvent]
	var agentCancelFunc CancelFunc

	cps := l.config.Store
	if cps != nil && result.CheckpointID == "" {
		cps = nil
	}

	runOpts := result.RunOpts
	if result.CheckpointID != "" && cps != nil {
		runOpts = append(runOpts, WithCheckPointID(result.CheckpointID))
	}

	enableStreaming := result.Input != nil && result.Input.EnableStreaming
	runner := NewRunner(ctx, RunnerConfig{
		EnableStreaming: enableStreaming,
		Agent:           agent,
		CheckPointStore: cps,
	})

	if result.IsResume && result.CheckpointID != "" {
		if result.ResumeParams != nil {
			iter, agentCancelFunc, err = runner.ResumeWithParamsAndCancel(ctx, result.CheckpointID, result.ResumeParams, runOpts...)
		} else {
			iter, agentCancelFunc, err = runner.ResumeWithCancel(ctx, result.CheckpointID, runOpts...)
		}
		if err != nil {
			return false, fmt.Errorf("failed to resume agent: %w", err)
		}
	} else {
		_, isAgentCancellable := agent.(CancellableAgent)
		if isAgentCancellable {
			iter, agentCancelFunc = runner.RunWithCancel(ctx, result.Input.Messages, runOpts...)
		} else {
			iter = runner.Run(ctx, result.Input.Messages, runOpts...)
		}
	}

	handleEvents := func() error {
		if l.config.OnAgentEvents != nil {
			return l.config.OnAgentEvents(ctx, result.Consumed, iter)
		}
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				return event.Err
			}
		}
		return nil
	}

	if agentCancelFunc != nil {
		done := make(chan struct{})
		var handleErr error

		go func() {
			defer func() {
				panicErr := recover()
				if panicErr != nil {
					handleErr = safe.NewPanicErr(panicErr, debug.Stack())
				}
				close(done)
			}()
			handleErr = handleEvents()
		}()

		preemptDone := make(chan struct{})
		go func() {
			for {
				select {
				case <-done:
					return
				case <-time.After(10 * time.Millisecond):
					if preempted, opts := l.preemptSig.check(); preempted {
						cancelCfg := &cancelConfig{Mode: CancelImmediate}
						for _, opt := range opts {
							opt(cancelCfg)
						}
						_ = agentCancelFunc(WithCancelMode(cancelCfg.Mode))
						close(preemptDone)
						return
					}
				}
			}
		}()

		select {
		case <-done:
			return false, handleErr
		case <-preemptDone:
			<-done
			return true, nil
		case <-l.cancelSig.getDoneChan():
			cfg := l.cancelSig.getConfig()
			if cfg != nil {
				_ = agentCancelFunc(WithCancelMode(cfg.Mode))
			}
			<-done
			return false, handleErr
		}
	}

	return false, handleEvents()
}

func (l *TurnLoop[T]) cleanup() {
	atomic.StoreInt32(&l.stopped, 1)

	l.result = &TurnLoopResult[T]{
		Error:          l.runErr,
		UnhandledItems: l.buffer.TakeAll(),
	}

	if l.cancelErr != nil && l.result.Error == nil {
		l.result.Error = l.cancelErr
	}

	l.buffer.Close()
	close(l.done)
}
