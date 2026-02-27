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
	"time"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/safe"
)

var (
	// ErrTurnLoopNotStarted is returned when Wait() is called on a TurnLoop
	// that has not been started via Run().
	ErrTurnLoopNotStarted = errors.New("turn loop has not been started")

	// ErrTurnLoopAlreadyStarted is returned when Run() is called more than once
	// on the same TurnLoop.
	ErrTurnLoopAlreadyStarted = errors.New("turn loop has already been started")
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
	cond       *sync.Cond
	paused     bool
	signaled   bool
	cancelOpts []CancelOption
}

func newPreemptSignal() *preemptSignal {
	s := &preemptSignal{}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *preemptSignal) pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

func (s *preemptSignal) signal(opts ...CancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.signaled = true
	s.cancelOpts = opts
	s.cond.Broadcast()
}

func (s *preemptSignal) check() (bool, []CancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.signaled {
		return true, s.cancelOpts
	}
	return false, nil
}

func (s *preemptSignal) waitIfPaused() (signaled bool, opts []CancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.paused {
		return false, nil
	}

	for s.paused && !s.signaled {
		s.cond.Wait()
	}

	if s.signaled {
		return true, s.cancelOpts
	}
	return false, nil
}

func (s *preemptSignal) release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.paused = false
	s.signaled = false
	s.cancelOpts = nil
	s.cond.Broadcast()
}

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any] struct {
	// GenInput receives all buffered items and decides what to process.
	// It returns which items to consume now vs keep for later turns.
	// Required.
	GenInput func(ctx context.Context, items []T) (*GenInputResult[T], error)

	// PrepareAgent returns an Agent configured to handle the consumed items.
	// This callback should set up the agent with appropriate system prompt,
	// tools, and middlewares based on what items are being processed.
	// Called once per turn with the items that GenInput decided to consume.
	// Required.
	PrepareAgent func(ctx context.Context, consumed []T) (Agent, error)

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

// TurnLoopExitState is returned when TurnLoop exits, containing the exit reason
// and any items that were not processed.
type TurnLoopExitState[T any] struct {
	// ExitReason indicates why the loop exited.
	// nil means clean exit (Cancel() was called and completed normally).
	// Non-nil values include context errors, callback errors, etc.
	ExitReason error

	// UnhandledItems contains items that were buffered but not processed.
	// This is always valid regardless of ExitReason.
	UnhandledItems []T
}

// TurnLoop is a push-based event loop for agent execution.
// Users push items via Push() and the loop processes them through the agent.
// Create with NewTurnLoop() and start with Run().
type TurnLoop[T any] struct {
	config TurnLoopConfig[T]

	buffer *internal.UnboundedChan[T]

	stopped int32
	started int32

	runOnce sync.Once

	done chan struct{}

	result *TurnLoopExitState[T]

	cancelOnce sync.Once

	cancelSig *turnLoopCancelSig

	preemptSig *preemptSignal

	cancelErr error

	cancelCfg *cancelConfig

	runErr error
}

type pushConfig struct {
	preempt      bool
	preemptDelay time.Duration
	cancelOpts   []CancelOption
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

// WithPreemptDelay sets a delay duration before preemption takes effect.
// When used with WithPreempt, the push will succeed immediately, but the
// preemption signal will be delayed by the specified duration.
// This allows the current agent to continue processing for a grace period
// before being preempted.
func WithPreemptDelay(delay time.Duration) PushOption {
	return func(cfg *pushConfig) {
		cfg.preemptDelay = delay
	}
}

// NewTurnLoop creates a new TurnLoop but does not start it.
// Call Run() to start processing items.
// Items can be pushed via Push() before Run() is called - they will be buffered.
func NewTurnLoop[T any](cfg TurnLoopConfig[T]) *TurnLoop[T] {
	return &TurnLoop[T]{
		config:     cfg,
		buffer:     internal.NewUnboundedChan[T](),
		done:       make(chan struct{}),
		cancelSig:  newTurnLoopCancelSig(),
		preemptSig: newPreemptSignal(),
	}
}

// Run starts the turn loop. It returns immediately after starting the background goroutine.
// Returns ErrTurnLoopAlreadyStarted if called more than once.
// Items pushed via Push() before Run() will be processed once the loop starts.
func (l *TurnLoop[T]) Run(ctx context.Context) error {
	alreadyStarted := true
	l.runOnce.Do(func() {
		alreadyStarted = false
		atomic.StoreInt32(&l.started, 1)
		go l.run(ctx)
	})
	if alreadyStarted {
		return ErrTurnLoopAlreadyStarted
	}
	return nil
}

// Push adds an item to the loop's buffer for processing.
// This method is non-blocking and thread-safe.
// Returns false if the loop has stopped, true otherwise.
//
// Use WithPreempt() to atomically push an item and signal preemption of the current agent.
// This is useful for urgent items that should interrupt the current processing.
// When preempt is set, Push will wait until the preempt is handled (agent cancelled or
// no agent was running), ensuring correct preempt semantics.
//
// Use WithPreemptDelay() together with WithPreempt() to delay the preemption signal.
// Push returns immediately after the item is buffered, and a goroutine is spawned
// to signal preemption after the delay.
func (l *TurnLoop[T]) Push(item T, opts ...PushOption) bool {
	cfg := &pushConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if atomic.LoadInt32(&l.stopped) != 0 {
		return false
	}

	if cfg.preempt {
		l.preemptSig.pause()

		if !l.buffer.TrySend(item) {
			l.preemptSig.release()
			return false
		}

		if cfg.preemptDelay > 0 {
			go func() {
				select {
				case <-time.After(cfg.preemptDelay):
					l.preemptSig.signal(cfg.cancelOpts...)
				case <-l.done:
					l.preemptSig.release()
				}
			}()
		} else {
			l.preemptSig.signal(cfg.cancelOpts...)
		}
		return true
	}

	return l.buffer.TrySend(item)
}

// Cancel signals the loop to stop and returns immediately (non-blocking).
// The loop will stop at the next safe point determined by CancelMode.
// This method is idempotent - multiple calls have no additional effect.
// If Run() was never called, Cancel() marks the loop as stopped so future
// Push() calls will return false, and Wait() will return immediately.
// Call Wait() to block until the loop has fully exited and get the result.
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

		if atomic.LoadInt32(&l.started) == 0 {
			l.result = &TurnLoopExitState[T]{
				ExitReason:     nil,
				UnhandledItems: nil,
			}
			close(l.done)
		}
	})
}

// Wait blocks until the loop exits and returns the result.
// This method is safe to call from multiple goroutines.
// All callers will receive the same result.
// If Run() was never called and Cancel() was not called, returns immediately
// with ExitReason = ErrTurnLoopNotStarted.
func (l *TurnLoop[T]) Wait() *TurnLoopExitState[T] {
	select {
	case <-l.done:
		return l.result
	default:
	}

	if atomic.LoadInt32(&l.started) == 0 {
		return &TurnLoopExitState[T]{
			ExitReason:     ErrTurnLoopNotStarted,
			UnhandledItems: l.buffer.TakeAll(),
		}
	}
	<-l.done
	return l.result
}

func (l *TurnLoop[T]) run(ctx context.Context) {
	defer l.cleanup()

	for {
		select {
		case <-ctx.Done():
			l.runErr = ctx.Err()
			return
		default:
		}

		if l.cancelSig.isCancelled() {
			return
		}

		first, ok := l.buffer.Receive()
		if !ok {
			return
		}

		if l.cancelSig.isCancelled() {
			l.buffer.PushFront([]T{first})
			return
		}

		rest := l.buffer.TakeAll()
		items := append([]T{first}, rest...)

		if signaled, _ := l.preemptSig.waitIfPaused(); signaled {
			l.preemptSig.release()
		}

		result, err := l.config.GenInput(ctx, items)
		if err != nil {
			l.buffer.PushFront(items)
			l.runErr = err
			return
		}

		l.buffer.PushFront(result.Remaining)

		if l.cancelSig.isCancelled() {
			l.buffer.PushFront(result.Consumed)
			return
		}

		agent, err := l.config.PrepareAgent(ctx, result.Consumed)
		if err != nil {
			l.buffer.PushFront(result.Consumed)
			l.runErr = err
			return
		}

		if l.cancelSig.isCancelled() {
			l.buffer.PushFront(result.Consumed)
			return
		}

		runErr := l.runAgentAndHandleEvents(ctx, agent, result)

		l.preemptSig.release()

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
		if result.ResumeParams != nil {
			iter, agentCancelFunc, err = runner.ResumeWithParamsAndCancel(ctx, result.CheckpointID, result.ResumeParams, runOpts...)
		} else {
			iter, agentCancelFunc, err = runner.ResumeWithCancel(ctx, result.CheckpointID, runOpts...)
		}
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
			return handleErr
		case <-preemptDone:
			<-done
			return nil
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

	l.result = &TurnLoopExitState[T]{
		ExitReason:     l.runErr,
		UnhandledItems: l.buffer.TakeAll(),
	}

	if l.cancelErr != nil && l.result.ExitReason == nil {
		l.result.ExitReason = l.cancelErr
	}

	l.buffer.Close()
	close(l.done)
}
