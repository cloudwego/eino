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
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/safe"
)

type turnLoopStopSig struct {
	done chan struct{}

	mu              sync.Mutex
	gen             uint64
	agentCancelOpts []AgentCancelOption
	notify          chan struct{}
}

func newTurnLoopStopSig() *turnLoopStopSig {
	return &turnLoopStopSig{
		done:   make(chan struct{}),
		notify: make(chan struct{}, 1),
	}
}

func (cs *turnLoopStopSig) signal(cfg *stopConfig) {
	cs.mu.Lock()
	cs.gen++
	cs.agentCancelOpts = cfg.agentCancelOpts
	cs.mu.Unlock()
	select {
	case cs.notify <- struct{}{}:
	default:
	}
}

func (cs *turnLoopStopSig) isStopped() bool {
	select {
	case <-cs.done:
		return true
	default:
		return false
	}
}

func (cs *turnLoopStopSig) closeDone() {
	close(cs.done)
}

func (cs *turnLoopStopSig) getNotifyChan() <-chan struct{} {
	if cs != nil {
		return cs.notify
	}
	return nil
}

func (cs *turnLoopStopSig) check() (uint64, []AgentCancelOption) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return cs.gen, append([]AgentCancelOption{}, cs.agentCancelOpts...)
}

type preemptSignal struct {
	mu              sync.Mutex
	cond            *sync.Cond
	paused          bool
	signaled        bool
	gen             uint64
	agentCancelOpts []AgentCancelOption
	pendingAckList  []chan struct{}
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
		s.pendingAckList = append(s.pendingAckList, ack)
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
		ackList := s.pendingAckList
		s.pendingAckList = nil
		return true, s.gen, s.agentCancelOpts, ackList
	}
	return false, 0, nil, nil
}

func (s *preemptSignal) waitIfPaused() (signaled bool, opts []AgentCancelOption, ackList []chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.paused {
		return false, nil, nil
	}

	for s.paused && !s.signaled {
		s.cond.Wait()
	}

	if s.signaled {
		ackList = s.pendingAckList
		s.pendingAckList = nil
		return true, s.agentCancelOpts, ackList
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
	s.pendingAckList = nil
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

	// GenResume is called exactly once when starting a resumed TurnLoop run
	// (via Resume). It receives:
	//   - canceledItems: the items that were being processed when the prior run was canceled
	//   - unhandledItems: items buffered but not processed when the prior run exited
	//   - newItems: items provided to Resume
	//
	// It returns a GenResumeResult describing how to resume the interrupted agent turn
	// (checkpoint ID + optional ResumeParams) and how to manipulate the buffer
	// (Consumed/Remaining) before continuing.
	GenResume func(ctx context.Context, loop *TurnLoop[T], canceledItems, unhandledItems, newItems []T) (*GenResumeResult[T], error)

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

	// ExternalTurnState controls checkpoint persistence mode.
	//
	// When false (default), the framework persists runner bytes and items via gob.
	// T must be gob-encodable.
	//
	// When true, the framework only persists runner checkpoint bytes to Store.
	// The user manages item persistence via TurnLoopExitState fields and provides
	// them back via WithExternalResumeItems on Resume.
	ExternalTurnState bool

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

	// Consumed are the items selected for this turn.
	// They are removed from the buffer and passed to PrepareAgent.
	Consumed []T

	// Remaining are the items to keep in the buffer for a future turn.
	// TurnLoop pushes Remaining back into the buffer before running the agent.
	//
	// Items from the GenInput input slice that are in neither Consumed nor Remaining
	// are dropped by the loop.
	Remaining []T
}

// GenResumeResult contains the result of GenResume processing.
type GenResumeResult[T any] struct {
	// RunCtx, if non-nil, overrides the context for this resumed turn's execution
	// (PrepareAgent, agent resume, OnAgentEvents).
	RunCtx context.Context

	// RunOpts are the options for this agent resume run.
	RunOpts []AgentRunOption

	// CheckPointID is the checkpoint ID to resume from. Required.
	CheckPointID string

	// ResumeParams are optional parameters for resuming an interrupted agent.
	ResumeParams *ResumeParams

	// Consumed are the items selected for this resumed turn.
	// They are removed from the buffer and passed to PrepareAgent.
	Consumed []T

	// Remaining are the items to keep in the buffer for a future turn.
	// TurnLoop pushes Remaining back into the buffer before resuming the agent.
	//
	// Items from (canceledItems, unhandledItems, newItems) that are in neither Consumed
	// nor Remaining are dropped by the loop.
	Remaining []T
}

type turnRunSpec[T any] struct {
	runCtx       context.Context
	input        *AgentInput
	runOpts      []AgentRunOption
	checkPointID string
	resumeParams *ResumeParams
	isResume     bool
	consumed     []T
	resumeBytes  []byte
}

type turnPlan[T any] struct {
	turnCtx   context.Context
	remaining []T
	spec      *turnRunSpec[T]
}

func (l *TurnLoop[T]) planTurn(
	ctx context.Context,
	isResume bool,
	items []T,
	pr *turnLoopPendingResume[T],
) (*turnPlan[T], error) {
	if !isResume {
		result, err := l.config.GenInput(ctx, l, items)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, errors.New("GenInputResult is nil")
		}
		if result.Input == nil {
			return nil, errors.New("agent input is nil")
		}
		turnCtx := ctx
		if result.RunCtx != nil {
			turnCtx = result.RunCtx
		}
		return &turnPlan[T]{
			turnCtx:   turnCtx,
			remaining: result.Remaining,
			spec: &turnRunSpec[T]{
				runCtx:   result.RunCtx,
				input:    result.Input,
				runOpts:  result.RunOpts,
				consumed: result.Consumed,
			},
		}, nil
	}
	if pr == nil {
		return nil, errors.New("resume payload is nil")
	}
	if l.config.GenResume == nil {
		return nil, errors.New("GenResume is required for resume")
	}
	resumeResult, err := l.config.GenResume(ctx, l, pr.canceled, pr.unhandled, pr.newItems)
	if err != nil {
		return nil, err
	}
	if resumeResult == nil {
		return nil, errors.New("GenResumeResult is nil")
	}
	if resumeResult.CheckPointID == "" {
		return nil, errors.New("GenResumeResult.CheckPointID is empty")
	}
	turnCtx := ctx
	if resumeResult.RunCtx != nil {
		turnCtx = resumeResult.RunCtx
	}
	return &turnPlan[T]{
		turnCtx:   turnCtx,
		remaining: resumeResult.Remaining,
		spec: &turnRunSpec[T]{
			runCtx:       resumeResult.RunCtx,
			runOpts:      resumeResult.RunOpts,
			checkPointID: resumeResult.CheckPointID,
			resumeParams: resumeResult.ResumeParams,
			isResume:     true,
			consumed:     resumeResult.Consumed,
			resumeBytes:  pr.resumeBytes,
		},
	}, nil
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

	// CheckPointID is the checkpoint ID persisted by TurnLoop on exit.
	// It is set when Store is configured and the loop exits due to Stop() or error.
	// In ExternalTurnState mode, it is the runner checkpoint ID (the key used to
	// persist runner bytes to Store).
	// It can be used to resume via TurnLoop.Resume (in ExternalTurnState mode, resume
	// also requires providing CanceledItems/UnhandledItems via WithExternalResumeItems).
	CheckPointID string

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

	runErr error

	canceledItems []T

	checkPointID          string
	checkPointRunnerBytes []byte

	pendingResume *turnLoopPendingResume[T]

	onAgentEvents func(ctx context.Context, tc *TurnContext[T], events *AsyncIterator[*AgentEvent]) error
}

type turnLoopCheckpoint[T any] struct {
	RunnerCheckpoint []byte
	// HasRunnerState reports whether RunnerCheckpoint contains resumable runner state.
	// It is false for "between turns" checkpoints where no agent execution was
	// interrupted (e.g. Stop() before the first turn or between turns).
	HasRunnerState bool
	UnhandledItems []T
	CanceledItems  []T
}

// ErrCheckpointStoreNil is returned when a checkpoint operation requires a Store
// but none was configured in TurnLoopConfig.
var ErrCheckpointStoreNil = errors.New("checkpoint store is nil")

func generateTurnLoopCheckpointID() string {
	return uuid.NewString()
}

func marshalTurnLoopCheckpoint[T any](c *turnLoopCheckpoint[T]) ([]byte, error) {
	buf := new(bytes.Buffer)
	if err := gob.NewEncoder(buf).Encode(c); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func unmarshalTurnLoopCheckpoint[T any](data []byte) (*turnLoopCheckpoint[T], error) {
	var c turnLoopCheckpoint[T]
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (l *TurnLoop[T]) loadTurnLoopCheckpoint(ctx context.Context, checkPointID string) (*turnLoopCheckpoint[T], error) {
	if l.config.Store == nil {
		return nil, ErrCheckpointStoreNil
	}
	data, existed, err := l.config.Store.Get(ctx, checkPointID)
	if err != nil {
		return nil, err
	}
	if !existed {
		return nil, fmt.Errorf("checkpoint[%s] does not exist", checkPointID)
	}
	if l.config.ExternalTurnState {
		return &turnLoopCheckpoint[T]{RunnerCheckpoint: data, HasRunnerState: len(data) > 0}, nil
	}
	return unmarshalTurnLoopCheckpoint[T](data)
}

func (l *TurnLoop[T]) saveTurnLoopCheckpoint(ctx context.Context, checkPointID string, c *turnLoopCheckpoint[T]) error {
	if l.config.Store == nil {
		return ErrCheckpointStoreNil
	}
	if l.config.ExternalTurnState {
		return l.config.Store.Set(ctx, checkPointID, append([]byte{}, c.RunnerCheckpoint...))
	}
	data, err := marshalTurnLoopCheckpoint(c)
	if err != nil {
		return err
	}
	return l.config.Store.Set(ctx, checkPointID, data)
}

type turnLoopPendingResume[T any] struct {
	checkPointID string
	canceled     []T
	unhandled    []T
	newItems     []T
	resumeBytes  []byte
}

type resumeOptions[T any] struct {
	canceledItems  []T
	unhandledItems []T
	hasResumeItems bool
}

// TurnLoopResumeOption configures how TurnLoop.Resume behaves.
// Use WithExternalResumeItems to supply canceled and unhandled items when ExternalTurnState is true.
type TurnLoopResumeOption[T any] func(*resumeOptions[T])

// WithExternalResumeItems provides the canceled and unhandled items for resuming when
// ExternalTurnState is enabled. In framework mode (ExternalTurnState=false), TurnLoop
// persists items in the checkpoint and Resume rejects this option.
func WithExternalResumeItems[T any](canceledItems, unhandledItems []T) TurnLoopResumeOption[T] {
	return func(o *resumeOptions[T]) {
		o.canceledItems = canceledItems
		o.unhandledItems = unhandledItems
		o.hasResumeItems = true
	}
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

func defaultTurnLoopOnAgentEvents[T any](_ context.Context, _ *TurnContext[T], events *AsyncIterator[*AgentEvent]) error {
	for {
		event, ok := events.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return event.Err
		}
	}
	return nil
}

// NewTurnLoop creates a new TurnLoop without starting it.
// The returned loop accepts Push and Stop calls immediately; pushed items
// are buffered until Run is called.
// Call Run to start the processing goroutine.
func NewTurnLoop[T any](cfg TurnLoopConfig[T]) (*TurnLoop[T], error) {
	if cfg.GenInput == nil {
		return nil, errors.New("GenInput is required")
	}
	if cfg.PrepareAgent == nil {
		return nil, errors.New("PrepareAgent is required")
	}

	l := &TurnLoop[T]{
		config:     cfg,
		buffer:     internal.NewUnboundedChan[T](),
		done:       make(chan struct{}),
		stopSig:    newTurnLoopStopSig(),
		preemptSig: newPreemptSignal(),
	}
	if cfg.OnAgentEvents != nil {
		l.onAgentEvents = cfg.OnAgentEvents
	} else {
		l.onAgentEvents = defaultTurnLoopOnAgentEvents[T]
	}
	return l, nil
}

// Run starts the loop's processing goroutine. It is non-blocking: the loop
// runs in the background and results are obtained via Wait.
// Run may be called at most once; subsequent calls are no-ops.
func (l *TurnLoop[T]) start(ctx context.Context) error {
	started := false
	l.runOnce.Do(func() {
		started = true
		atomic.StoreInt32(&l.started, 1)
		go l.run(ctx)
	})
	if !started {
		return errors.New("cannot start TurnLoop: loop already started")
	}
	return nil
}

// Run starts the loop's processing goroutine. It is non-blocking: the loop
// runs in the background and results are obtained via Wait.
func (l *TurnLoop[T]) Run(ctx context.Context) error {
	return l.start(ctx)
}

func (l *TurnLoop[T]) Resume(ctx context.Context, checkPointID string, newItems []T, opts ...TurnLoopResumeOption[T]) error {
	if checkPointID == "" {
		return errors.New("cannot resume TurnLoop: checkpointID is empty")
	}
	if l.config.Store == nil {
		return fmt.Errorf("cannot resume TurnLoop: %w", ErrCheckpointStoreNil)
	}

	ro := &resumeOptions[T]{}
	for _, opt := range opts {
		if opt != nil {
			opt(ro)
		}
	}
	if l.config.ExternalTurnState {
		if !ro.hasResumeItems {
			return errors.New("cannot resume TurnLoop: ExternalTurnState is enabled but resume items are missing")
		}
	} else {
		if ro.hasResumeItems {
			return errors.New("cannot resume TurnLoop: external resume items are only supported when ExternalTurnState is enabled")
		}
	}

	cp, err := l.loadTurnLoopCheckpoint(ctx, checkPointID)
	if err != nil {
		return fmt.Errorf("failed to load TurnLoop checkpoint: %w", err)
	}

	canceledItems := cp.CanceledItems
	unhandledItems := cp.UnhandledItems
	if l.config.ExternalTurnState {
		canceledItems = ro.canceledItems
		unhandledItems = ro.unhandledItems
	}

	if cp.HasRunnerState {
		if len(cp.RunnerCheckpoint) == 0 {
			return errors.New("failed to resume TurnLoop: checkpoint has runner state but bytes are empty")
		}
		l.pendingResume = &turnLoopPendingResume[T]{
			checkPointID: checkPointID,
			canceled:     append([]T{}, canceledItems...),
			unhandled:    append([]T{}, unhandledItems...),
			newItems:     append([]T{}, newItems...),
			resumeBytes:  append([]byte{}, cp.RunnerCheckpoint...),
		}
	} else {
		if len(canceledItems) > 0 {
			return errors.New("failed to resume TurnLoop: checkpoint has canceled items but no runner state")
		}
		for _, it := range unhandledItems {
			if !l.buffer.TrySend(it) {
				return errors.New("failed to buffer unhandled items: buffer closed")
			}
		}
		for _, it := range newItems {
			if !l.buffer.TrySend(it) {
				return errors.New("failed to buffer new items: buffer closed")
			}
		}
	}

	l.checkPointID = checkPointID

	return l.start(ctx)
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
// This method is idempotent - multiple calls update cancel options.
// Call Wait() to block until the loop has fully exited and get the result.
//
// Stop may be called before Run. In that case, the stopped flag is set and
// a subsequent Run will exit the loop immediately.
//
// If the running agent does not support the WithCancel AgentRunOption,
// Stop degrades to "exit the loop on entering the next iteration" — the
// current agent turn runs to completion before the loop exits.
func (l *TurnLoop[T]) Stop(opts ...StopOption) {
	cfg := &stopConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	l.stopSig.signal(cfg)

	l.stopOnce.Do(func() {
		l.stopSig.closeDone()
		atomic.StoreInt32(&l.stopped, 1)
		l.buffer.Close()
	})
}

// Wait blocks until the loop exits and returns the result.
// This method is safe to call from multiple goroutines.
// All callers will receive the same result.
//
// Wait blocks until Run or Resume is called AND the loop exits. If neither is
// ever called, Wait blocks forever.
func (l *TurnLoop[T]) Wait() *TurnLoopExitState[T] {
	<-l.done
	return l.result
}

func (l *TurnLoop[T]) run(ctx context.Context) {
	defer l.cleanup(ctx)

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

		isResume := false
		var pr *turnLoopPendingResume[T]
		var items []T
		var pushBack []T

		if l.pendingResume != nil {
			isResume = true
			pr = l.pendingResume
			l.pendingResume = nil

			pushBack = make([]T, 0, len(pr.canceled)+len(pr.unhandled)+len(pr.newItems))
			pushBack = append(pushBack, pr.canceled...)
			pushBack = append(pushBack, pr.unhandled...)
			pushBack = append(pushBack, pr.newItems...)
		} else {
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
			items = append([]T{first}, rest...)
			pushBack = items
		}

		if signaled, _, ackList := l.preemptSig.waitIfPaused(); signaled {
			for _, ack := range ackList {
				close(ack)
			}
			l.preemptSig.release()
		}

		plan, err := l.planTurn(ctx, isResume, items, pr)
		if err != nil {
			if len(pushBack) > 0 {
				l.buffer.PushFront(pushBack)
			}
			l.runErr = err
			return
		}

		if l.stopSig.isStopped() {
			if len(pushBack) > 0 {
				l.buffer.PushFront(pushBack)
			}
			return
		}

		agent, err := l.config.PrepareAgent(plan.turnCtx, l, plan.spec.consumed)
		if err != nil {
			if len(pushBack) > 0 {
				l.buffer.PushFront(pushBack)
			}
			l.runErr = err
			return
		}

		if l.stopSig.isStopped() {
			if len(pushBack) > 0 {
				l.buffer.PushFront(pushBack)
			}
			return
		}

		l.buffer.PushFront(plan.remaining)

		runErr := l.runAgentAndHandleEvents(plan.turnCtx, agent, plan.spec)

		l.preemptSig.release()

		if runErr != nil {
			l.runErr = runErr
			return
		}
	}
}

func (l *TurnLoop[T]) setupBridgeStore(spec *turnRunSpec[T], runOpts []AgentRunOption) ([]AgentRunOption, string, *bridgeStore, error) {
	store := l.config.Store
	o := getCommonOptions(nil, runOpts...)
	checkPointID := ""
	if o.checkPointID != nil {
		checkPointID = *o.checkPointID
	}
	needAppendCheckpointOpt := o.checkPointID == nil || checkPointID == ""
	if spec.checkPointID != "" {
		checkPointID = spec.checkPointID
	}
	var ms *bridgeStore
	if spec.isResume && checkPointID == "" {
		return nil, "", nil, fmt.Errorf("resume checkpointID is empty")
	}
	if store == nil && spec.isResume {
		return nil, "", nil, fmt.Errorf("failed to resume agent: %w", ErrCheckpointStoreNil)
	}
	if store != nil {
		if checkPointID == "" {
			checkPointID = generateTurnLoopCheckpointID()
			needAppendCheckpointOpt = true
		}
		if needAppendCheckpointOpt {
			runOpts = append(runOpts, WithCheckPointID(checkPointID))
		}
		if spec.isResume {
			if len(spec.resumeBytes) == 0 {
				return nil, "", nil, fmt.Errorf("resume checkpoint is empty")
			}
			ms = newResumeBridgeStore(checkPointID, spec.resumeBytes)
		} else {
			ms = newBridgeStore()
		}
	}
	return runOpts, checkPointID, ms, nil
}

func (l *TurnLoop[T]) watchPreemptSignal(done <-chan struct{}, agentCancelFunc AgentCancelFunc, preemptDone chan struct{}) {
	var lastGen uint64
	for {
		select {
		case <-done:
			return
		case <-l.preemptSig.notify:
			if preempted, gen, opts, ackList := l.preemptSig.check(); preempted {
				if gen != lastGen {
					firstPreempt := lastGen == 0
					lastGen = gen
					// CancelHandle is intentionally not awaited here: agentCancelFunc commits the cancel signal synchronously,
					// while waiting would block until the turn finishes and can deadlock this watcher against the done signal.
					_, contributed := agentCancelFunc(opts...)
					if firstPreempt && contributed {
						close(preemptDone)
					}
					for _, ack := range ackList {
						close(ack)
					}
				}
			}
		}
	}
}

func (l *TurnLoop[T]) watchStopSignal(done <-chan struct{}, agentCancelFunc AgentCancelFunc, stoppedDone chan struct{}) {
	var lastGen uint64
	stoppedClosed := false
	for {
		select {
		case <-done:
			return
		case <-l.stopSig.getNotifyChan():
			gen, opts := l.stopSig.check()
			if gen != lastGen {
				lastGen = gen
				// CancelHandle is intentionally not awaited here: agentCancelFunc commits the cancel signal synchronously,
				// while waiting would block until the turn finishes and can deadlock this watcher against the done signal.
				_, contributed := agentCancelFunc(opts...)
				if contributed && !stoppedClosed {
					close(stoppedDone)
					stoppedClosed = true
				}
			}
		}
	}
}

func (l *TurnLoop[T]) runAgentAndHandleEvents(
	ctx context.Context,
	agent Agent,
	spec *turnRunSpec[T],
) error {
	var iter *AsyncIterator[*AgentEvent]
	defer func() {
		if l.stopSig.isStopped() && len(l.canceledItems) == 0 {
			l.canceledItems = append([]T{}, spec.consumed...)
		}
	}()

	runOpts, checkPointID, ms, err := l.setupBridgeStore(spec, spec.runOpts)
	if err != nil {
		return err
	}
	store := l.config.Store
	cancelOpt, agentCancelFunc := WithCancel()
	runOpts = append(runOpts, cancelOpt)

	enableStreaming := false
	if spec.input != nil {
		enableStreaming = spec.input.EnableStreaming
	}
	runner := NewRunner(ctx, RunnerConfig{
		EnableStreaming: enableStreaming,
		Agent:           agent,
		CheckPointStore: ms,
	})

	if spec.isResume {
		var err error
		if spec.resumeParams != nil {
			iter, err = runner.ResumeWithParams(ctx, checkPointID, spec.resumeParams, runOpts...)
		} else {
			iter, err = runner.Resume(ctx, checkPointID, runOpts...)
		}
		if err != nil {
			return fmt.Errorf("failed to resume agent: %w", err)
		}
	} else {
		iter = runner.Run(ctx, spec.input.Messages, runOpts...)
	}

	preemptDone := make(chan struct{})
	stoppedDone := make(chan struct{})

	handleEvents := func() error {
		tc := &TurnContext[T]{
			Loop:      l,
			Consumed:  spec.consumed,
			Preempted: preemptDone,
			Stopped:   stoppedDone,
		}
		return l.onAgentEvents(ctx, tc, iter)
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
	go l.watchPreemptSignal(done, agentCancelFunc, preemptDone)
	go l.watchStopSignal(done, agentCancelFunc, stoppedDone)

	finalizeCheckpoint := func() error {
		if store != nil && ms != nil {
			data, ok, err := ms.Get(ctx, checkPointID)
			if err != nil {
				return fmt.Errorf("failed to read runner checkpoint: %w", err)
			}
			if ok {
				l.checkPointID = checkPointID
				l.checkPointRunnerBytes = append([]byte{}, data...)
			}
		}
		return nil
	}

	select {
	case <-done:
		if l.stopSig.isStopped() || handleErr != nil {
			if err := finalizeCheckpoint(); err != nil {
				handleErr = errors.Join(handleErr, err)
			}
		}
		return handleErr
	case <-preemptDone:
		<-done
		return nil
	case <-stoppedDone:
		<-done
		if err := finalizeCheckpoint(); err != nil {
			handleErr = errors.Join(handleErr, err)
		}
		return handleErr
	}
}

func (l *TurnLoop[T]) cleanup(ctx context.Context) {
	atomic.StoreInt32(&l.stopped, 1)

	unhandled := l.buffer.TakeAll()
	savedCheckPointID := ""
	shouldSaveCheckpoint := l.config.Store != nil && (l.stopSig.isStopped() || l.runErr != nil)
	if shouldSaveCheckpoint {
		checkPointID := l.checkPointID
		if checkPointID == "" {
			checkPointID = generateTurnLoopCheckpointID()
		}
		cp := &turnLoopCheckpoint[T]{
			RunnerCheckpoint: l.checkPointRunnerBytes,
			HasRunnerState:   len(l.checkPointRunnerBytes) > 0,
		}
		if !l.config.ExternalTurnState {
			cp.UnhandledItems = unhandled
			cp.CanceledItems = l.canceledItems
		}
		err := l.saveTurnLoopCheckpoint(ctx, checkPointID, cp)
		if err != nil {
			l.runErr = errors.Join(l.runErr, fmt.Errorf("failed to save turn loop checkpoint: %w", err))
		} else {
			l.checkPointID = checkPointID
			savedCheckPointID = checkPointID
		}
	}

	l.result = &TurnLoopExitState[T]{
		ExitReason:     l.runErr,
		CheckPointID:   savedCheckPointID,
		UnhandledItems: unhandled,
		CanceledItems:  l.canceledItems,
	}

	l.buffer.Close()
	close(l.done)
}
