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

	cancelErr error

	cancelCfg *cancelConfig

	runErr error
}

// ErrTurnLoopStopped is returned by Push() when the loop has stopped.
var ErrTurnLoopStopped = errors.New("turn loop has stopped")

// TurnLoopCancelOption is an option for Cancel().
type TurnLoopCancelOption func(*cancelConfig)

// WithTurnLoopCancelMode sets the cancel mode.
func WithTurnLoopCancelMode(mode CancelMode) TurnLoopCancelOption {
	return func(cfg *cancelConfig) {
		cfg.Mode = mode
	}
}

// RunTurnLoop creates and starts a new TurnLoop.
// The loop runs in a background goroutine and processes items pushed via Push().
// Use Cancel() to stop the loop and Wait() to get the result.
func RunTurnLoop[T any](ctx context.Context, cfg TurnLoopConfig[T]) *TurnLoop[T] {
	l := &TurnLoop[T]{
		config:    cfg,
		buffer:    internal.NewUnboundedChan[T](),
		done:      make(chan struct{}),
		cancelSig: newTurnLoopCancelSig(),
	}
	go l.run(ctx)
	return l
}

// Push adds an item to the loop's buffer for processing.
// This method is non-blocking and thread-safe.
// Returns ErrTurnLoopStopped if the loop has stopped.
func (l *TurnLoop[T]) Push(item T) (err error) {
	if atomic.LoadInt32(&l.stopped) != 0 {
		return ErrTurnLoopStopped
	}

	defer func() {
		if r := recover(); r != nil {
			err = ErrTurnLoopStopped
		}
	}()

	l.buffer.Send(item)
	return nil
}

// Cancel signals the loop to stop. This method is non-blocking and idempotent.
// Use Wait() to block until the loop exits and get the result.
func (l *TurnLoop[T]) Cancel(opts ...TurnLoopCancelOption) {
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

		runErr := l.runAgentAndHandleEvents(ctx, agent, result)
		if runErr != nil {
			l.runErr = runErr
			return
		}
	}
}

func (l *TurnLoop[T]) runAgentAndHandleEvents(
	ctx context.Context,
	agent Agent,
	result *GenInputResult[T],
) error {
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
		var err error
		iter, agentCancelFunc, err = runner.ResumeWithCancel(ctx, result.CheckpointID, runOpts...)
		if err != nil {
			return fmt.Errorf("failed to resume agent: %w", err)
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

		select {
		case <-done:
			return handleErr
		case <-l.cancelSig.getDoneChan():
			cfg := l.cancelSig.getConfig()
			if cfg != nil {
				_ = agentCancelFunc(WithCancelMode(cfg.Mode))
			}
			<-done
			return handleErr
		}
	}

	return handleEvents()
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
