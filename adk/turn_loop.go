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

type turnLoopStopSig struct {
	done   chan struct{}
	config atomic.Value
}

func newTurnLoopStopSig() *turnLoopStopSig {
	return &turnLoopStopSig{
		done: make(chan struct{}),
	}
}

func (cs *turnLoopStopSig) stop(cfg *stopConfig) {
	cs.config.Store(cfg)
	close(cs.done)
}

func (cs *turnLoopStopSig) isStopped() bool {
	select {
	case <-cs.done:
		return true
	default:
		return false
	}
}

func (cs *turnLoopStopSig) getConfig() *stopConfig {
	if v := cs.config.Load(); v != nil {
		return v.(*stopConfig)
	}
	return nil
}

func (cs *turnLoopStopSig) getDoneChan() <-chan struct{} {
	if cs != nil {
		return cs.done
	}
	return nil
}

type preemptSignal struct {
	mu              sync.Mutex
	cond            *sync.Cond
	paused          bool
	signaled        bool
	gen             uint64
	agentCancelOpts []AgentCancelOption
	pendingAcks     []chan struct{}
	notify          chan struct{}
}

func newPreemptSignal() *preemptSignal {
	s := &preemptSignal{notify: make(chan struct{}, 1)}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *preemptSignal) pause() {
	s.mu.Lock()
	s.paused = true
	s.mu.Unlock()
}

func (s *preemptSignal) signal(opts ...AgentCancelOption) {
	s.signalWithAck(nil, opts...)
}

func (s *preemptSignal) signalWithAck(ack chan struct{}, opts ...AgentCancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.paused {
		if ack != nil {
			close(ack)
		}
		return
	}

	s.signaled = true
	s.gen++
	s.agentCancelOpts = opts
	if ack != nil {
		s.pendingAcks = append(s.pendingAcks, ack)
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
	s.cond.Broadcast()
}

func (s *preemptSignal) check() (bool, uint64, []AgentCancelOption, []chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.signaled {
		acks := s.pendingAcks
		s.pendingAcks = nil
		return true, s.gen, s.agentCancelOpts, acks
	}
	return false, 0, nil, nil
}

func (s *preemptSignal) waitIfPaused() (signaled bool, opts []AgentCancelOption, acks []chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.paused {
		return false, nil, nil
	}

	for s.paused && !s.signaled {
		s.cond.Wait()
	}

	if s.signaled {
		acks = s.pendingAcks
		s.pendingAcks = nil
		return true, s.agentCancelOpts, acks
	}
	return false, nil, nil
}

func (s *preemptSignal) release() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.paused = false
	s.signaled = false
	s.gen = 0
	s.agentCancelOpts = nil
	s.pendingAcks = nil
	select {
	case <-s.notify:
	default:
	}
	s.cond.Broadcast()
}

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any] struct {
	// GenInput receives the TurnLoop instance and all buffered items, and decides what to process.
	// It returns which items to consume now vs keep for later turns.
	// The loop parameter allows calling Push() or Stop() directly from within the callback.
	// Required.
	GenInput func(ctx context.Context, loop *TurnLoop[T], items []T) (*GenInputResult[T], error)

	// PrepareAgent returns an Agent configured to handle the consumed items.
	// This callback should set up the agent with appropriate system prompt,
	// tools, and middlewares based on what items are being processed.
	// Called once per turn with the items that GenInput decided to consume.
	// The loop parameter allows calling Push() or Stop() directly from within the callback.
	// Required.
	PrepareAgent func(ctx context.Context, loop *TurnLoop[T], consumed []T) (Agent, error)

	// OnAgentEvents is called to handle events emitted by the agent.
	// The TurnContext provides per-turn info and control:
	//   - tc.Consumed: items that triggered this agent execution
	//   - tc.Loop: allows calling Push() or Stop() directly from within the callback
	//   - tc.Preempted / tc.Stopped: signals while processing events
	// Optional. If not provided, events are drained and errors (except CancelError
	// from Stop-triggered cancellation) are returned as ExitReason.
	OnAgentEvents func(ctx context.Context, tc *TurnContext[T], events *AsyncIterator[*AgentEvent]) error

	// Store is the checkpoint store for persistence and resume. Optional.
	Store CheckPointStore
}

// GenInputResult contains the result of GenInput processing.
type GenInputResult[T any] struct {
	// RunCtx, if non-nil, overrides the context for this turn's execution
	// (PrepareAgent, agent run, OnAgentEvents).
	//
	// Must be derived from the ctx passed to GenInput to preserve the
	// TurnLoop's cancellation semantics and inherited values. For example:
	//
	//   runCtx := context.WithValue(ctx, traceKey{}, extractTraceID(items))
	//   return &GenInputResult[T]{RunCtx: runCtx, ...}, nil
	//
	// If nil, the TurnLoop's context is used unchanged.
	RunCtx context.Context

	// Input is the agent input to execute
	Input *AgentInput

	// RunOpts are the options for this agent run
	RunOpts []AgentRunOption

	// ResumeFromCheckpointID specifies the checkpoint ID to resume from.
	// Required when IsResume is true.
	// When not resuming, this can be left empty as the checkpoint ID
	// is automatically generated by the agent runner.
	ResumeFromCheckpointID string

	// IsResume indicates this is a resume run (not a fresh run).
	// When true, ResumeFromCheckpointID must be set.
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
	// nil means clean exit (Stop() was called and completed normally).
	// Non-nil values include context errors, callback errors, *CancelError, etc.
	// When Stop() cancels a running agent, ExitReason will be a *CancelError.
	// If the agent was configured with a checkpoint store, the CancelError's
	// CheckPointID field contains the checkpoint ID of the interrupted turn,
	// which can be used to resume via TurnLoop. Use errors.As to extract it.
	ExitReason error

	// UnhandledItems contains items that were buffered but not processed.
	// This is always valid regardless of ExitReason.
	UnhandledItems []T

	// CanceledItems contains the items whose turn was canceled by Stop().
	// This is set when Stop() is called during a running turn, even if it
	// did not contribute to the final CancelError.
	// It can be used to reconstruct GenInput/PrepareAgent inputs when resuming.
	CanceledItems []T
}

// TurnContext provides per-turn context to the OnAgentEvents callback.
type TurnContext[T any] struct {
	// Loop is the TurnLoop instance, allowing Push() or Stop() calls.
	Loop *TurnLoop[T]

	// Consumed contains items that triggered this agent execution.
	Consumed []T

	// Preempted is closed when a preempt signal fires for the current turn
	// (via Push with WithPreempt) and at least one preemptive Push contributed
	// to the CancelError for the current turn.
	// "Contributed" means the preempt's cancel options were included in the
	// CancelError before it was finalized. Remains open if no preempt contributed.
	// Use in a select to detect preemption while processing events.
	Preempted <-chan struct{}

	// Stopped is closed when a Stop() call contributed to the CancelError for the
	// current turn.
	// "Contributed" means Stop's cancel options were included in the CancelError
	// before it was finalized. Remains open if Stop did not contribute.
	// Use in a select to detect stop while processing events.
	Stopped <-chan struct{}
}

// TurnLoop is a push-based event loop for agent execution.
// Users push items via Push() and the loop processes them through the agent.
//
// Create with NewTurnLoop, then start with Run:
//
//	loop := NewTurnLoop(cfg)
//	// pass loop to other components, push initial items, etc.
//	loop.Run(ctx)
//
// # Permissive API
//
// All methods are valid on a not-yet-running loop:
//   - Push: items are buffered and will be processed once Run is called.
//   - Stop: sets the stopped flag; a subsequent Run will exit immediately.
//   - Wait: blocks until Run is called AND the loop exits. If Run is never
//     called, Wait blocks forever (this is a programming error, analogous
//     to reading from a channel that nobody writes to).
type TurnLoop[T any] struct {
	config TurnLoopConfig[T]

	buffer *internal.UnboundedChan[T]

	stopped int32
	started int32

	done chan struct{}

	result *TurnLoopExitState[T]

	stopOnce sync.Once

	runOnce sync.Once

	stopSig *turnLoopStopSig

	preemptSig *preemptSignal

	stopCfg *stopConfig

	runErr error

	canceledItems []T
}

type stopConfig struct {
	agentCancelOpts []AgentCancelOption
}

// StopOption is an option for Stop().
type StopOption func(*stopConfig)

// WithAgentCancel sets the agent cancel options to use when stopping the loop.
// These options control how the currently running agent is cancelled.
func WithAgentCancel(opts ...AgentCancelOption) StopOption {
	return func(cfg *stopConfig) {
		cfg.agentCancelOpts = opts
	}
}

type pushConfig struct {
	preempt         bool
	preemptDelay    time.Duration
	agentCancelOpts []AgentCancelOption
}

// PushOption is an option for Push().
type PushOption func(*pushConfig)

// WithPreempt signals that the current agent should be canceled after pushing.
// This enables atomic "push + preempt" to avoid race conditions between
// pushing an urgent item and triggering preemption.
// The loop will cancel the current agent turn and continue with the next turn,
// where GenInput will see all buffered items including the newly pushed one.
func WithPreempt(agentCancelOpts ...AgentCancelOption) PushOption {
	return func(cfg *pushConfig) {
		cfg.preempt = true
		cfg.agentCancelOpts = agentCancelOpts
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

// NewTurnLoop creates a new TurnLoop without starting it.
// The returned loop accepts Push and Stop calls immediately; pushed items
// are buffered until Run is called.
// Call Run to start the processing goroutine.
func NewTurnLoop[T any](cfg TurnLoopConfig[T]) *TurnLoop[T] {
	return &TurnLoop[T]{
		config:     cfg,
		buffer:     internal.NewUnboundedChan[T](),
		done:       make(chan struct{}),
		stopSig:    newTurnLoopStopSig(),
		preemptSig: newPreemptSignal(),
	}
}

// Run starts the loop's processing goroutine. It is non-blocking: the loop
// runs in the background and results are obtained via Wait.
// Run may be called at most once; subsequent calls are no-ops.
func (l *TurnLoop[T]) Run(ctx context.Context) {
	l.runOnce.Do(func() {
		atomic.StoreInt32(&l.started, 1)
		go l.run(ctx)
	})
}

// Push adds an item to the loop's buffer for processing.
// This method is non-blocking and thread-safe.
// Returns false if the loop has stopped, true otherwise. If a preemptive push
// succeeds, the second return value is a channel that is closed when the loop
// has acknowledged the preempt signal (by either initiating cancellation of the
// current agent run or reaching a point where no cancellation is needed).
// If the loop has not been started yet (Run not called), items are buffered
// and will be processed once Run is called.
//
// Use WithPreempt() to atomically push an item and signal preemption of the current agent.
// This is useful for urgent items that should interrupt the current processing.
// The returned channel may be waited on if the caller needs to ensure the preempt
// signal has been observed.
//
// Use WithPreemptDelay() together with WithPreempt() to delay the preemption signal.
// Push returns immediately after the item is buffered, and a goroutine is spawned
// to signal preemption after the delay.
func (l *TurnLoop[T]) Push(item T, opts ...PushOption) (bool, <-chan struct{}) {
	cfg := &pushConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	if atomic.LoadInt32(&l.stopped) != 0 {
		return false, nil
	}

	if cfg.preempt {
		l.preemptSig.pause()

		if !l.buffer.TrySend(item) {
			l.preemptSig.release()
			return false, nil
		}

		ack := make(chan struct{})
		if atomic.LoadInt32(&l.started) == 0 {
			l.preemptSig.release()
			close(ack)
			return true, ack
		}

		if cfg.preemptDelay > 0 {
			go func() {
				select {
				case <-time.After(cfg.preemptDelay):
					l.preemptSig.signalWithAck(ack, cfg.agentCancelOpts...)
				case <-l.done:
					l.preemptSig.release()
					close(ack)
				}
			}()
		} else {
			l.preemptSig.signalWithAck(ack, cfg.agentCancelOpts...)
		}
		return true, ack
	}

	return l.buffer.TrySend(item), nil
}

// Stop signals the loop to stop and returns immediately (non-blocking).
// The loop will finish the current turn (or cancel it via WithAgentCancel options),
// then exit without starting a new turn.
// Use WithAgentCancel to control how the currently running agent is cancelled.
// This method is idempotent - multiple calls have no additional effect.
// Call Wait() to block until the loop has fully exited and get the result.
//
// Stop may be called before Run. In that case, the stopped flag is set and
// a subsequent Run will exit the loop immediately.
//
// If the running agent does not support the WithCancel AgentRunOption,
// Stop degrades to "exit the loop on entering the next iteration" — the
// current agent turn runs to completion before the loop exits.
func (l *TurnLoop[T]) Stop(opts ...StopOption) {
	l.stopOnce.Do(func() {
		cfg := &stopConfig{}
		for _, opt := range opts {
			opt(cfg)
		}

		l.stopCfg = cfg
		l.stopSig.stop(cfg)

		atomic.StoreInt32(&l.stopped, 1)

		l.buffer.Close()
	})
}

// Wait blocks until the loop exits and returns the result.
// This method is safe to call from multiple goroutines.
// All callers will receive the same result.
//
// Wait blocks until Run is called AND the loop exits. If Run is never called,
// Wait blocks forever.
func (l *TurnLoop[T]) Wait() *TurnLoopExitState[T] {
	<-l.done
	return l.result
}

func (l *TurnLoop[T]) run(ctx context.Context) {
	defer l.cleanup()

	// Monitor context cancellation: close the buffer so that a blocking
	// Receive() unblocks. The loop will then check ctx.Err() and exit.
	go func() {
		select {
		case <-ctx.Done():
			l.buffer.Close()
		case <-l.done:
		}
	}()

	for {
		if l.stopSig.isStopped() {
			return
		}

		first, ok := l.buffer.Receive()
		if !ok {
			if err := ctx.Err(); err != nil {
				l.runErr = err
			}
			return
		}

		if err := ctx.Err(); err != nil {
			l.buffer.PushFront([]T{first})
			l.runErr = err
			return
		}

		if l.stopSig.isStopped() {
			l.buffer.PushFront([]T{first})
			return
		}

		rest := l.buffer.TakeAll()
		items := append([]T{first}, rest...)

		if signaled, _, acks := l.preemptSig.waitIfPaused(); signaled {
			for _, ack := range acks {
				close(ack)
			}
			l.preemptSig.release()
		}

		result, err := l.config.GenInput(ctx, l, items)
		if err != nil {
			l.buffer.PushFront(items)
			l.runErr = err
			return
		}

		if l.stopSig.isStopped() {
			l.buffer.PushFront(items)
			return
		}

		turnCtx := ctx
		if result.RunCtx != nil {
			turnCtx = result.RunCtx
		}

		agent, err := l.config.PrepareAgent(turnCtx, l, result.Consumed)
		if err != nil {
			l.buffer.PushFront(items)
			l.runErr = err
			return
		}

		if l.stopSig.isStopped() {
			l.buffer.PushFront(items)
			return
		}

		l.buffer.PushFront(result.Remaining)

		runErr := l.runAgentAndHandleEvents(turnCtx, agent, result)

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
	defer func() {
		if l.stopSig.isStopped() && len(l.canceledItems) == 0 {
			l.canceledItems = append([]T(nil), result.Consumed...)
		}
	}()

	cps := l.config.Store
	if cps != nil && result.ResumeFromCheckpointID == "" {
		cps = nil
	}

	runOpts := result.RunOpts
	if result.ResumeFromCheckpointID != "" && cps != nil {
		runOpts = append(runOpts, WithCheckPointID(result.ResumeFromCheckpointID))
	}

	cancelOpt, agentCancelFunc := WithCancel()
	runOpts = append(runOpts, cancelOpt)

	enableStreaming := result.Input != nil && result.Input.EnableStreaming
	runner := NewRunner(ctx, RunnerConfig{
		EnableStreaming: enableStreaming,
		Agent:           agent,
		CheckPointStore: cps,
	})

	if result.IsResume && result.ResumeFromCheckpointID != "" {
		var err error
		if result.ResumeParams != nil {
			iter, err = runner.ResumeWithParams(ctx, result.ResumeFromCheckpointID, result.ResumeParams, runOpts...)
		} else {
			iter, err = runner.Resume(ctx, result.ResumeFromCheckpointID, runOpts...)
		}
		if err != nil {
			return fmt.Errorf("failed to resume agent: %w", err)
		}
	} else {
		iter = runner.Run(ctx, result.Input.Messages, runOpts...)
	}

	preemptDone := make(chan struct{})
	stoppedDone := make(chan struct{})

	handleEvents := func() error {
		if l.config.OnAgentEvents != nil {
			tc := &TurnContext[T]{
				Loop:      l,
				Consumed:  result.Consumed,
				Preempted: preemptDone,
				Stopped:   stoppedDone,
			}
			return l.config.OnAgentEvents(ctx, tc, iter)
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
	go func() {
		var lastGen uint64
		for {
			select {
			case <-done:
				return
			case <-l.preemptSig.notify:
				if preempted, gen, opts, acks := l.preemptSig.check(); preempted {
					if gen != lastGen {
						firstPreempt := lastGen == 0
						lastGen = gen
						handle, contributed := agentCancelFunc(opts...)
						go func() { _ = handle.Wait() }()
						if firstPreempt && contributed {
							// Close preemptDone after agentCancelFunc so observers are
							// guaranteed that cancellation has been initiated. We must NOT
							// wait on handle.Wait() here — that blocks until the agent run
							// finishes, which would prevent preemptDone from closing before
							// done, making the bottom select always return CancelError.
							close(preemptDone)
						}
						for _, ack := range acks {
							close(ack)
						}
					}
				}
			}
		}
	}()
	go func() {
		select {
		case <-done:
			return
		case <-l.stopSig.getDoneChan():
			cfg := l.stopSig.getConfig()
			if cfg != nil {
				// Initiate cancellation, then close stoppedDone so observers are
				// guaranteed that agentCancelFunc has been called. Wait on the
				// handle in a background goroutine — the flowAgent wrapper ensures
				// markDone() is always deferred, so this won't deadlock even if
				// the agent doesn't explicitly support WithCancel.
				handle, contributed := agentCancelFunc(cfg.agentCancelOpts...)
				go func() { _ = handle.Wait() }()
				if contributed {
					close(stoppedDone)
				}
				return
			}
		}
	}()

	select {
	case <-done:
		return handleErr
	case <-preemptDone:
		<-done
		return nil
	case <-stoppedDone:
		<-done
		return handleErr
	}
}

func (l *TurnLoop[T]) cleanup() {
	atomic.StoreInt32(&l.stopped, 1)

	l.result = &TurnLoopExitState[T]{
		ExitReason:     l.runErr,
		UnhandledItems: l.buffer.TakeAll(),
		CanceledItems:  l.canceledItems,
	}

	l.buffer.Close()
	close(l.done)
}
