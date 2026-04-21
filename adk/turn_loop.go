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

	"github.com/cloudwego/eino/internal/safe"
)

// stopSignal coordinates the Stop() call with per-turn watcher goroutines.
//
// Lifecycle overview:
//
//  1. SIGNAL — Stop() calls signal() which bumps the generation counter,
//     stores the AgentCancelOptions, and deposits a one-shot notification
//     in the buffered notify channel.
//
//  2. DONE — Stop() calls closeDone() which permanently closes the done
//     channel. This acts as a durable "stopped" flag: any current or future
//     select on done fires immediately, ensuring that every watcher —
//     including watchers in turns that start after Stop() but before the
//     run loop observes isStopped() — can reliably detect the stop.
//
//  3. RECEIVE — The per-turn watchStopSignal goroutine selects on the done
//     channel (the durable flag) and the notify channel (to detect mode
//     escalation from a second Stop call). On either signal, it calls
//     agentCancelFunc to cancel the running agent.
//
// The generation counter (gen) de-duplicates wakes so that the watcher only
// acts when a new Stop() call has been made, supporting mode escalation
// (e.g. CancelAfterToolCalls followed by CancelImmediate).
type stopSignal struct {
	done chan struct{}

	mu  sync.Mutex
	gen uint64
	// agentCancelOpts controls how the stop interacts with the running agent:
	//   nil       → no cancel intent; the turn runs to completion
	//             (bare Stop, or UntilIdleFor without cancel opts)
	//   empty     → CancelImmediate (WithImmediate)
	//   non-empty → cancel with specific modes (WithGraceful, WithGracefulTimeout)
	agentCancelOpts []AgentCancelOption
	skipCheckpoint  bool
	stopCause       string
	idleFor         time.Duration
	notify          chan struct{}
}

func newStopSignal() *stopSignal {
	return &stopSignal{
		done:   make(chan struct{}),
		notify: make(chan struct{}, 1),
	}
}

// signal records a stop request and wakes the current turn's watcher (if any).
// The non-blocking send means the notification is silently coalesced when the
// buffer is already full — this is safe because gen de-duplicates in the watcher.
func (s *stopSignal) signal(cfg *stopConfig) {
	s.mu.Lock()
	s.gen++
	// Only overwrite when the caller explicitly provides cancel options.
	// A bare Stop() leaves cfg.agentCancelOpts nil (no cancel intent), which
	// must not de-escalate a previously set cancel policy.
	if cfg.agentCancelOpts != nil {
		s.agentCancelOpts = cfg.agentCancelOpts
	}
	if cfg.skipCheckpoint {
		s.skipCheckpoint = true
	}
	if cfg.stopCause != "" && s.stopCause == "" {
		s.stopCause = cfg.stopCause
	}
	if cfg.idleFor > 0 && s.idleFor == 0 {
		s.idleFor = cfg.idleFor
	}
	s.mu.Unlock()
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// isStopped returns true if closeDone() has been called.
func (s *stopSignal) isStopped() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

// closeDone permanently marks the stop as committed. All current and future
// selects on s.done will fire immediately after this call.
func (s *stopSignal) closeDone() {
	close(s.done)
}

// check returns the current generation and a snapshot of the cancel options.
// Returns nil opts when no cancel intent has been set (e.g. UntilIdleFor without
// WithGraceful/WithImmediate), preserving the nil vs empty-slice distinction
// that tryCancel relies on.
func (s *stopSignal) check() (uint64, []AgentCancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.agentCancelOpts == nil {
		return s.gen, nil
	}
	return s.gen, append([]AgentCancelOption{}, s.agentCancelOpts...)
}

func (s *stopSignal) isSkipCheckpoint() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.skipCheckpoint
}

func (s *stopSignal) getStopCause() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopCause
}

func (s *stopSignal) getIdleFor() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.idleFor
}

// preemptSignal coordinates preemption between Push callers and the run loop.
//
// Lifecycle overview:
//
//  1. HOLD — A Push caller (or the run loop itself) calls holdRunLoop() to
//     increment holdCount. While holdCount > 0 the run loop blocks at
//     waitForPreemptOrUnhold(), preventing it from starting a new turn.
//
//  2. REQUEST — The Push caller calls requestPreempt() which sets
//     preemptRequested=true, bumps preemptGen, stores cancelOpts/acks, and
//     wakes both the run-loop (via cond) and the in-turn watcher goroutine
//     (via notify channel).
//
//  3. RECEIVE — The per-turn watchPreemptSignal goroutine calls
//     receivePreempt(), obtains the cancel opts and ack channels, invokes
//     agentCancelFunc to cancel the running agent, and closes the ack
//     channels to notify Push callers.
//
//  4. UNHOLD — After the turn finishes (or if the Push caller decides not
//     to preempt), unholdRunLoop() / endTurnAndUnhold() decrements
//     holdCount. When holdCount reaches 0, all signal state is reset.
//
// The run loop brackets every turn with holdRunLoop() / endTurnAndUnhold()
// so that a concurrent Push caller's hold keeps holdCount > 0 even after
// the turn ends, preventing the loop from racing into a new turn before
// the Push caller's preempt request is delivered.
//
// Fields currentTC and currentRunCtx are stored here (rather than on
// TurnLoop) so that holdAndGetTurn() can atomically snapshot the turn
// state and increment holdCount under the same mu lock, eliminating the
// TOCTOU race between reading the turn and holding the loop.
type preemptSignal struct {
	mu               sync.Mutex
	cond             *sync.Cond
	holdCount        int
	preemptRequested bool
	preemptGen       uint64
	agentCancelOpts  []AgentCancelOption
	pendingAckList   []chan struct{}
	notify           chan struct{}

	currentTC     any
	currentRunCtx context.Context
}

func newPreemptSignal() *preemptSignal {
	s := &preemptSignal{notify: make(chan struct{}, 1)}
	s.cond = sync.NewCond(&s.mu)
	return s
}

func (s *preemptSignal) holdRunLoop() {
	s.mu.Lock()
	s.holdCount++
	s.mu.Unlock()
}

func (s *preemptSignal) setTurn(ctx context.Context, tc any) {
	s.mu.Lock()
	s.currentRunCtx = ctx
	s.currentTC = tc
	s.mu.Unlock()
}

func (s *preemptSignal) holdAndGetTurn() (context.Context, any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holdCount++
	return s.currentRunCtx, s.currentTC
}

// requestPreempt records a preempt request and wakes both waiters.
// If holdCount is 0, no one is listening — close the ack immediately as a no-op.
func (s *preemptSignal) requestPreempt(ack chan struct{}, opts ...AgentCancelOption) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.holdCount <= 0 {
		if ack != nil {
			close(ack)
		}
		return
	}

	s.preemptRequested = true
	s.preemptGen++
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

// receivePreempt is called by the per-turn watcher goroutine to consume a
// pending preempt. It drains pendingAckList (so the watcher can close them
// after invoking agentCancelFunc) but intentionally preserves preemptRequested
// and preemptGen — these are needed by waitForPreemptOrUnhold on the run loop.
func (s *preemptSignal) receivePreempt() (bool, uint64, []AgentCancelOption, []chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.preemptRequested {
		ackList := s.pendingAckList
		s.pendingAckList = nil
		return true, s.preemptGen, s.agentCancelOpts, ackList
	}
	return false, 0, nil, nil
}

// waitForPreemptOrUnhold blocks the run loop between turns. It returns early
// (preempted=false) when holdCount is 0 (no Push caller is holding). Otherwise
// it blocks until either a preempt is requested or all holders release.
func (s *preemptSignal) waitForPreemptOrUnhold() (preempted bool, opts []AgentCancelOption, ackList []chan struct{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.holdCount <= 0 {
		return false, nil, nil
	}

	for s.holdCount > 0 && !s.preemptRequested {
		s.cond.Wait()
	}

	if s.preemptRequested {
		ackList = s.pendingAckList
		s.pendingAckList = nil
		return true, s.agentCancelOpts, ackList
	}
	return false, nil, nil
}

// resetLocked clears all signal state and closes pending ack channels so the
// next cycle starts clean and blocked Push callers are unblocked. Must be
// called with s.mu held. Does NOT touch holdCount, currentTC, or currentRunCtx
// — callers are responsible for those.
func (s *preemptSignal) resetLocked() {
	s.preemptRequested = false
	s.preemptGen = 0
	s.agentCancelOpts = nil
	for _, ack := range s.pendingAckList {
		close(ack)
	}
	s.pendingAckList = nil
	select {
	case <-s.notify:
	default:
	}
}

// unholdRunLoop drops one hold. When holdCount reaches 0, all signal state is
// reset so the next cycle starts clean.
func (s *preemptSignal) unholdRunLoop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.holdCount--
	if s.holdCount < 0 {
		s.holdCount = 0
	}
	if s.holdCount == 0 {
		s.resetLocked()
	}
	s.cond.Broadcast()
}

// endTurnAndUnhold is called by the run loop after runAgentAndHandleEvents
// returns. It clears the current turn context and drops the run loop's hold.
func (s *preemptSignal) endTurnAndUnhold() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.currentTC = nil
	s.currentRunCtx = nil
	s.holdCount--
	if s.holdCount < 0 {
		s.holdCount = 0
	}
	if s.holdCount == 0 {
		s.resetLocked()
	}
	s.cond.Broadcast()
}

// drainAll forcefully resets all preemptSignal state and closes any pending
// ack channels. Called during TurnLoop cleanup to prevent ack channels from
// leaking when the run loop exits (e.g. due to Stop) while a Push caller
// still holds a reference.
func (s *preemptSignal) drainAll() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.holdCount = 0
	s.currentTC = nil
	s.currentRunCtx = nil
	s.resetLocked()
	s.cond.Broadcast()
}

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any, M messageType] struct {
	// GenInput receives the TurnLoop instance and all buffered items, and decides what to process.
	// It returns which items to consume now vs keep for later turns.
	// The loop parameter allows calling Push() or Stop() directly from within the callback.
	// Required.
	GenInput func(ctx context.Context, loop *TurnLoop[T, M], items []T) (*GenInputResult[T, M], error)

	// GenResume is called at most once during Run(). When CheckpointID is
	// configured, Run() queries Store for the checkpoint:
	//   - If the checkpoint contains runner state (i.e. an agent was interrupted
	//     mid-turn), Run() calls GenResume to plan a resume turn.
	//   - Otherwise (no checkpoint, or between-turns checkpoint), GenResume is
	//     never called and the loop proceeds via GenInput.
	//
	// It receives:
	//   - canceledItems: the items being processed when the prior run was canceled
	//   - unhandledItems: items buffered but not processed when the prior run exited
	//   - newItems: items that were Push()-ed before Run() was called
	//
	// It returns a GenResumeResult describing how to resume the interrupted agent
	// turn (optional ResumeParams) and how to manipulate the buffer
	// (Consumed/Remaining) before continuing.
	GenResume func(ctx context.Context, loop *TurnLoop[T, M], canceledItems, unhandledItems, newItems []T) (*GenResumeResult[T, M], error)

	// PrepareAgent returns an Agent configured to handle the consumed items.
	// This callback should set up the agent with appropriate system prompt,
	// tools, and middlewares based on what items are being processed.
	// Called once per turn with the items that GenInput decided to consume.
	// The loop parameter allows calling Push() or Stop() directly from within the callback.
	// Required.
	PrepareAgent func(ctx context.Context, loop *TurnLoop[T, M], consumed []T) (TypedAgent[M], error)

	// OnAgentEvents is called to handle events emitted by the agent.
	// The TurnContext provides per-turn info and control:
	//   - tc.Consumed: items that triggered this agent execution
	//   - tc.Loop: allows calling Push() or Stop() directly from within the callback
	//   - tc.Preempted / tc.Stopped: signals while processing events
	//
	// Error handling: the returned error is only used when the callback itself
	// wants to abort the TurnLoop. The callback should NEVER propagate
	// CancelError — the framework handles it automatically:
	//   - Stop: the framework propagates CancelError as ExitReason, loop exits.
	//   - Preempt: the framework does not propagate CancelError; if the callback
	//     also returns nil, the loop continues with the next turn.
	// In practice, return a non-nil error only for callback-internal failures
	// that should terminate the loop.
	//
	// Optional. If not provided, events are drained and the first error
	// (including CancelError from Stop) is returned as ExitReason.
	OnAgentEvents func(ctx context.Context, tc *TurnContext[T, M], events *AsyncIterator[*TypedAgentEvent[M]]) error

	// Store is the checkpoint store for persistence and resume. Optional.
	// When set together with CheckpointID, enables automatic checkpoint-based resume.
	// The TurnLoop always persists both runner checkpoint bytes and item bookkeeping
	// (CanceledItems, UnhandledItems) via gob encoding, so T must be gob-encodable
	// when Store is used.
	Store CheckPointStore

	// CheckpointID, when set together with Store, enables automatic
	// checkpoint-based resume. On Run(), the TurnLoop queries Store for this ID:
	//   - If a checkpoint exists with runner state (mid-turn interrupt),
	//     GenResume is called to plan the resume turn.
	//   - If a checkpoint exists without runner state (between-turns),
	//     the stored unhandled items are buffered and the loop proceeds
	//     normally via GenInput.
	//   - If no checkpoint exists, the loop starts fresh.
	//
	// On exit, if the TurnLoop saved a new checkpoint, it is saved under this
	// same CheckpointID. On clean exit (no checkpoint saved), the existing
	// checkpoint under CheckpointID is deleted to prevent stale resumption.
	CheckpointID string
}

// GenInputResult contains the result of GenInput processing.
type GenInputResult[T any, M messageType] struct {
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
	Input *TypedAgentInput[M]

	// RunOpts are the options for this agent run.
	// Note: do not pass WithCheckPointID here; the TurnLoop automatically
	// injects the checkpointID into the Runner.
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
type GenResumeResult[T any, M messageType] struct {
	// RunCtx, if non-nil, overrides the context for this resumed turn's execution
	// (PrepareAgent, agent resume, OnAgentEvents).
	RunCtx context.Context

	// RunOpts are the options for this agent resume run.
	// Note: do not pass WithCheckPointID here; the TurnLoop automatically
	// injects the checkpointID into the Runner.
	RunOpts []AgentRunOption

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

type turnRunSpec[T any, M messageType] struct {
	runCtx       context.Context
	input        *TypedAgentInput[M]
	runOpts      []AgentRunOption
	resumeParams *ResumeParams
	isResume     bool
	consumed     []T
	resumeBytes  []byte
}

type turnPlan[T any, M messageType] struct {
	turnCtx   context.Context
	remaining []T
	spec      *turnRunSpec[T, M]
}

func (l *TurnLoop[T, M]) planTurn(
	ctx context.Context,
	isResume bool,
	items []T,
	pr *turnLoopPendingResume[T],
) (*turnPlan[T, M], error) {
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
		return &turnPlan[T, M]{
			turnCtx:   turnCtx,
			remaining: result.Remaining,
			spec: &turnRunSpec[T, M]{
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
	turnCtx := ctx
	if resumeResult.RunCtx != nil {
		turnCtx = resumeResult.RunCtx
	}
	return &turnPlan[T, M]{
		turnCtx:   turnCtx,
		remaining: resumeResult.Remaining,
		spec: &turnRunSpec[T, M]{
			runCtx:       resumeResult.RunCtx,
			runOpts:      resumeResult.RunOpts,
			resumeParams: resumeResult.ResumeParams,
			isResume:     true,
			consumed:     resumeResult.Consumed,
			resumeBytes:  pr.resumeBytes,
		},
	}, nil
}

// TurnLoopExitState is returned when TurnLoop exits, containing the exit reason
// and any items that were not processed.
type TurnLoopExitState[T any, M messageType] struct {
	// ExitReason indicates why the loop exited.
	// nil means clean exit (Stop() was called without cancel options, or the
	// agent completed normally before Stop took effect).
	// Non-nil values include context errors, callback errors, *CancelError, etc.
	// When Stop(WithImmediate()) or Stop(WithGraceful()) cancels a running
	// agent, ExitReason will be a *CancelError.
	// This never contains checkpoint errors — see CheckpointErr for those.
	ExitReason error

	// UnhandledItems contains items that were buffered but not processed.
	// These are items for which Push returned true but were never consumed by a turn.
	// This is always valid regardless of ExitReason.
	UnhandledItems []T

	// CanceledItems contains the items whose turn was actually interrupted
	// by a cancel (Stop with WithImmediate, WithGraceful, or WithGracefulTimeout).
	// Only populated when ExitReason is a *CancelError — if the agent finishes
	// normally before the cancel takes effect, CanceledItems is empty.
	// On resume, these are passed to GenResume's CanceledItems parameter.
	CanceledItems []T

	// StopCause is the business-supplied reason passed via WithStopCause.
	// Empty if Stop was not called or no cause was provided.
	StopCause string

	// CheckpointAttempted indicates whether a checkpoint save was attempted when the loop exited.
	// True only when Store is configured, CheckpointID is set, Stop() was called,
	// the loop was not idle at exit time, and WithSkipCheckpoint was not used.
	CheckpointAttempted bool

	// CheckpointErr is the error from checkpoint save, if any.
	// nil when CheckpointAttempted is false (no attempt was made) or when the save succeeded.
	CheckpointErr error

	// TakeLateItems returns items that were pushed after the loop stopped
	// (i.e., Push returned false for these items). These items are NOT included
	// in the checkpoint.
	//
	// This function is idempotent: the first call computes and caches the result;
	// subsequent calls return the same slice.
	//
	// After TakeLateItems is called, any subsequent Push() will panic to
	// prevent items from being silently lost.
	//
	// It is safe to call TakeLateItems from any goroutine after Wait() returns.
	// If TakeLateItems is never called, late items are simply garbage collected.
	TakeLateItems func() []T
}

// TurnContext provides per-turn context to the OnAgentEvents callback.
type TurnContext[T any, M messageType] struct {
	// Loop is the TurnLoop instance, allowing Push() or Stop() calls.
	Loop *TurnLoop[T, M]

	// Consumed contains items that triggered this agent execution.
	Consumed []T

	// Preempted is closed when a preempt signal fires for the current turn
	// (via Push with WithPreempt/WithPreemptTimeout) and at least one
	// preemptive Push contributed to the CancelError for the current turn.
	// "Contributed" means the preempt's cancel options were included in the
	// CancelError before it was finalized. Remains open if no preempt contributed.
	// Use in a select to detect preemption while processing events.
	//
	// Both Preempted and Stopped may be closed within the same turn if both
	// signals arrive while the agent is still being cancelled. Whichever
	// arrives after the cancel is fully handled will not contribute.
	Preempted <-chan struct{}

	// Stopped is closed when a Stop() call contributed to the CancelError for the
	// current turn.
	// "Contributed" means Stop's cancel options were included in the CancelError
	// before it was finalized. Remains open if Stop did not contribute.
	// Use in a select to detect stop while processing events.
	//
	// See Preempted for the relationship between the two channels.
	Stopped <-chan struct{}

	// StopCause returns the business-supplied reason from WithStopCause.
	// This value is only meaningful after the Stopped channel is closed.
	// Before that, it returns an empty string.
	StopCause func() string
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
type TurnLoop[T any, M messageType] struct {
	config TurnLoopConfig[T, M]

	buffer *turnBuffer[T]

	stopped int32
	started int32

	done chan struct{}

	result *TurnLoopExitState[T, M]

	stopOnce sync.Once

	runOnce sync.Once

	stopSig *stopSignal

	preemptSig *preemptSignal

	runErr error

	canceledItems []T

	checkPointRunnerBytes []byte

	pendingResume *turnLoopPendingResume[T]

	loadCheckpointID string

	onAgentEvents func(ctx context.Context, tc *TurnContext[T, M], events *AsyncIterator[*TypedAgentEvent[M]]) error

	lateMu     sync.Mutex
	lateItems  []T
	lateSealed bool
}

func (l *TurnLoop[T, M]) appendLate(item T) {
	l.lateMu.Lock()
	defer l.lateMu.Unlock()
	if l.lateSealed {
		panic("TurnLoop: Push called after TakeLateItems")
	}
	l.lateItems = append(l.lateItems, item)
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

func (l *TurnLoop[T, M]) saveTurnLoopCheckpoint(ctx context.Context, checkPointID string, c *turnLoopCheckpoint[T]) error {
	if l.config.Store == nil {
		return errors.New("checkpoint store is nil")
	}
	data, err := marshalTurnLoopCheckpoint(c)
	if err != nil {
		return err
	}
	return l.config.Store.Set(ctx, checkPointID, data)
}

func (l *TurnLoop[T, M]) deleteTurnLoopCheckpoint(ctx context.Context, checkPointID string) error {
	if l.config.Store == nil {
		return nil
	}
	if deleter, ok := l.config.Store.(CheckPointDeleter); ok {
		return deleter.Delete(ctx, checkPointID)
	}
	return nil
}

func (l *TurnLoop[T, M]) tryLoadCheckpoint(ctx context.Context) error {
	checkPointID := l.config.CheckpointID
	if checkPointID == "" || l.config.Store == nil {
		return nil
	}

	l.loadCheckpointID = checkPointID

	data, existed, err := l.config.Store.Get(ctx, checkPointID)
	if err != nil {
		return fmt.Errorf("failed to load checkpoint[%s]: %w", checkPointID, err)
	}
	if !existed {
		return nil
	}

	var cp *turnLoopCheckpoint[T]
	if len(data) == 0 {
		return nil
	}
	cp, err = unmarshalTurnLoopCheckpoint[T](data)
	if err != nil {
		return fmt.Errorf("failed to unmarshal checkpoint[%s]: %w", checkPointID, err)
	}

	newItems := l.buffer.TakeAll()

	if cp.HasRunnerState {
		if len(cp.RunnerCheckpoint) == 0 {
			l.buffer.PushFront(newItems)
			return fmt.Errorf("checkpoint[%s] has runner state but bytes are empty", checkPointID)
		}
		l.pendingResume = &turnLoopPendingResume[T]{
			canceled:    append([]T{}, cp.CanceledItems...),
			unhandled:   append([]T{}, cp.UnhandledItems...),
			newItems:    append([]T{}, newItems...),
			resumeBytes: append([]byte{}, cp.RunnerCheckpoint...),
		}
	} else {
		items := make([]T, 0, len(cp.UnhandledItems)+len(newItems))
		items = append(items, cp.UnhandledItems...)
		items = append(items, newItems...)
		l.buffer.PushFront(items)
	}

	return nil
}

type turnLoopPendingResume[T any] struct {
	canceled    []T
	unhandled   []T
	newItems    []T
	resumeBytes []byte
}

// SafePoint describes at which boundary the agent may be cancelled.
// It is a bitmask: values can be combined with bitwise OR to accept multiple
// safe points (e.g. AfterToolCalls | AfterChatModel). Internally, SafePoint
// is translated to CancelMode via toCancelMode().
//
// SafePoint is used only in the preemption API (WithPreempt/WithPreemptTimeout).
// A key design constraint: preemption always targets a safe point — the user's
// intent is to cancel at a well-defined boundary, never to abort immediately.
// Immediate cancellation is only reachable as an automatic timeout escalation
// (via WithPreemptTimeout), not as a direct user choice. This is why SafePoint
// has no "immediate" value and why WithPreempt requires a non-zero SafePoint
// (panics otherwise).
type SafePoint int

const (
	// AfterChatModel allows the agent to finish the current chat-model
	// call before being cancelled.
	AfterChatModel SafePoint = 1 << iota
	// AfterToolCalls allows the agent to finish the current tool-call round
	// before being cancelled.
	AfterToolCalls
	// AnySafePoint is shorthand for AfterChatModel | AfterToolCalls.
	AnySafePoint = AfterChatModel | AfterToolCalls
)

func (sp SafePoint) toCancelMode() CancelMode {
	var mode CancelMode
	if sp&AfterToolCalls != 0 {
		mode |= CancelAfterToolCalls
	}
	if sp&AfterChatModel != 0 {
		mode |= CancelAfterChatModel
	}
	return mode
}

type stopConfig struct {
	agentCancelOpts []AgentCancelOption
	skipCheckpoint  bool
	stopCause       string
	idleFor         time.Duration
}

// StopOption is an option for Stop().
type StopOption func(*stopConfig)

// WithGraceful requests a graceful stop that waits at the nearest safe point
// (after tool calls or after a chat-model call) and propagates recursively to
// nested agents. It does not impose a time limit; use WithGracefulTimeout to
// add a grace period after which the stop escalates to immediate cancellation.
//
// WithGraceful and WithGracefulTimeout are mutually exclusive; if both are
// passed to the same Stop call, the last one wins.
func WithGraceful() StopOption {
	return func(cfg *stopConfig) {
		cfg.agentCancelOpts = []AgentCancelOption{
			WithAgentCancelMode(CancelAfterChatModel | CancelAfterToolCalls),
			WithRecursive(),
		}
	}
}

// WithImmediate aborts the running agent turn as soon as possible.
// The agent is cancelled immediately without waiting for any safe point.
// Nested agents inside AgentTools will also receive the cancel signal
// and be torn down.
//
// This is the most aggressive stop mode — typically used when the caller
// wants to shut down the TurnLoop with no intention of resuming.
func WithImmediate() StopOption {
	return func(cfg *stopConfig) {
		cfg.agentCancelOpts = []AgentCancelOption{
			WithRecursive(),
		}
	}
}

// WithGracefulTimeout is like WithGraceful but adds a grace period.
// If the agent has not reached a safe point within gracePeriod, the stop
// escalates to immediate cancellation.
//
// gracePeriod must be positive; passing a zero or negative duration panics.
//
// WithGraceful and WithGracefulTimeout are mutually exclusive; if both are
// passed to the same Stop call, the last one wins.
func WithGracefulTimeout(gracePeriod time.Duration) StopOption {
	if gracePeriod <= 0 {
		panic("adk: WithGracefulTimeout: gracePeriod must be positive")
	}
	return func(cfg *stopConfig) {
		cfg.agentCancelOpts = []AgentCancelOption{
			WithAgentCancelMode(CancelAfterChatModel | CancelAfterToolCalls),
			WithRecursive(),
			WithAgentCancelTimeout(gracePeriod),
		}
	}
}

// WithSkipCheckpoint tells the TurnLoop not to persist a checkpoint for this
// Stop call. Use this when the caller does not intend to resume in the future.
// The flag is sticky: once any Stop() call sets it, subsequent calls cannot undo it.
func WithSkipCheckpoint() StopOption {
	return func(cfg *stopConfig) {
		cfg.skipCheckpoint = true
	}
}

// WithStopCause attaches a business-supplied reason string to this Stop call.
// The cause is surfaced in TurnLoopExitState.StopCause and, after the Stopped
// channel closes, via TurnContext.StopCause().
// If multiple Stop() calls provide a cause, the first non-empty value wins.
func WithStopCause(cause string) StopOption {
	return func(cfg *stopConfig) {
		cfg.stopCause = cause
	}
}

// UntilIdleFor defers the stop until the TurnLoop has been continuously idle
// (blocked between turns with no pending items) for at least the given
// duration. Each time a new item arrives the timer resets from zero.
//
// This is useful when business code monitors agent activity externally and
// wants to shut down the loop once there has been no work for a while, without
// racing with concurrent Push calls.
//
// UntilIdleFor does not impact a running agent. It only takes effect when the
// loop is idle between turns. Cancel options (WithImmediate, WithGraceful,
// WithGracefulTimeout) in the same Stop call are silently ignored — they are
// meaningless alongside UntilIdleFor.
//
// To escalate after a prior UntilIdleFor, issue a separate Stop call:
//
//	loop.Stop(UntilIdleFor(30 * time.Second))  // wait for idle
//	// ... later, if you need to abort immediately:
//	loop.Stop(WithImmediate())                 // overrides the idle wait
//
// Only the first UntilIdleFor duration takes effect; subsequent calls with
// a different duration are ignored. A Stop() call without UntilIdleFor always
// shuts down the loop immediately regardless of any pending idle timer.
//
// UntilIdleFor is combinable with non-cancel StopOptions (WithSkipCheckpoint,
// WithStopCause) in the same call.
//
// duration must be positive; passing a zero or negative value panics.
func UntilIdleFor(duration time.Duration) StopOption {
	if duration <= 0 {
		panic("adk: UntilIdleFor: duration must be positive")
	}
	return func(cfg *stopConfig) {
		cfg.idleFor = duration
	}
}

type pushConfig[T any, M messageType] struct {
	preempt         bool
	preemptDelay    time.Duration
	agentCancelOpts []AgentCancelOption
	pushStrategy    func(context.Context, *TurnContext[T, M]) []PushOption[T, M]
}

// PushOption is an option for Push().
type PushOption[T any, M messageType] func(*pushConfig[T, M])

// WithPreempt signals that the current agent turn should be cancelled at the
// specified safePoint after pushing the new item. The loop cancels the current
// turn and starts a new one, where GenInput will see all buffered items
// including the newly pushed one.
// Use WithPreemptTimeout to add a timeout that escalates to immediate abort.
//
// Because safe points fire at turn-level boundaries (after the chat model
// returns or after all tool calls complete), no nested agent is running at
// the moment of cancellation — nested agents within AgentTools have either
// not started yet (AfterChatModel) or already finished (AfterToolCalls).
// Note: WithPreempt does NOT include WithRecursive (no escalation path exists).
// WithPreemptTimeout DOES include WithRecursive so that on timeout escalation,
// nested agents are properly torn down.
//
// WithPreempt and WithPreemptTimeout are mutually exclusive; if both are
// passed to the same Push call, the last one wins.
//
// safePoint must not be zero; passing SafePoint(0) panics.
func WithPreempt[T any, M messageType](safePoint SafePoint) PushOption[T, M] {
	if safePoint == 0 {
		panic("adk: SafePoint must not be zero; use AfterToolCalls, AfterChatModel, or AnySafePoint")
	}
	return func(cfg *pushConfig[T, M]) {
		cfg.preempt = true
		cfg.agentCancelOpts = []AgentCancelOption{
			WithAgentCancelMode(safePoint.toCancelMode()),
		}
	}
}

// WithPreemptTimeout is like WithPreempt but adds a timeout. If the agent has
// not reached the safe point within timeout, the preemption escalates to
// immediate cancellation. On escalation, nested agents inside AgentTools will
// also receive the cancel signal and be torn down.
//
// safePoint must not be zero; passing SafePoint(0) panics.
func WithPreemptTimeout[T any, M messageType](safePoint SafePoint, timeout time.Duration) PushOption[T, M] {
	if safePoint == 0 {
		panic("adk: SafePoint must not be zero; use AfterToolCalls, AfterChatModel, or AnySafePoint")
	}
	return func(cfg *pushConfig[T, M]) {
		cfg.preempt = true
		cfg.agentCancelOpts = []AgentCancelOption{
			WithAgentCancelMode(safePoint.toCancelMode()),
			WithAgentCancelTimeout(timeout),
			WithRecursive(),
		}
	}
}

// WithPreemptDelay sets a delay duration before preemption takes effect.
// When used with WithPreempt or WithPreemptTimeout, the push will succeed
// immediately, but the preemption signal will be delayed by the specified
// duration. This allows the current agent to continue processing for a grace
// period before being preempted.
func WithPreemptDelay[T any, M messageType](delay time.Duration) PushOption[T, M] {
	return func(cfg *pushConfig[T, M]) {
		cfg.preemptDelay = delay
	}
}

// WithPushStrategy provides dynamic push option resolution based on the current turn state.
// The callback receives the current turn's context and TurnContext (nil if no turn is active)
// and returns the actual PushOptions to apply. When WithPushStrategy is used, all other
// PushOptions passed to the same Push call are ignored.
//
// The returned options must not contain another WithPushStrategy; any nested
// strategy is silently stripped.
//
// Example: preempt only if the current turn is processing low-priority items:
//
//	loop.Push(urgentItem, WithPushStrategy(func(ctx context.Context, tc *TurnContext[MyItem, *schema.Message]) []PushOption[MyItem, *schema.Message] {
//	    if tc == nil {
//	        return nil // between turns, plain push
//	    }
//	    if isLowPriority(tc.Consumed) {
//	        return []PushOption[MyItem, *schema.Message]{WithPreempt[MyItem, *schema.Message](AnySafePoint)}
//	    }
//	    return nil // don't preempt high-priority work
//	}))
func WithPushStrategy[T any, M messageType](fn func(ctx context.Context, tc *TurnContext[T, M]) []PushOption[T, M]) PushOption[T, M] {
	return func(cfg *pushConfig[T, M]) {
		cfg.pushStrategy = fn
	}
}

func defaultTurnLoopOnAgentEvents[T any, M messageType](_ context.Context, _ *TurnContext[T, M], events *AsyncIterator[*TypedAgentEvent[M]]) error {
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
//
// NewTurnLoop panics if GenInput or PrepareAgent is nil.
func NewTurnLoop[T any, M messageType](cfg TurnLoopConfig[T, M]) *TurnLoop[T, M] {
	if cfg.GenInput == nil {
		panic("adk: NewTurnLoop: GenInput is required")
	}
	if cfg.PrepareAgent == nil {
		panic("adk: NewTurnLoop: PrepareAgent is required")
	}

	l := &TurnLoop[T, M]{
		config:     cfg,
		buffer:     newTurnBuffer[T](),
		done:       make(chan struct{}),
		stopSig:    newStopSignal(),
		preemptSig: newPreemptSignal(),
	}
	if cfg.OnAgentEvents != nil {
		l.onAgentEvents = cfg.OnAgentEvents
	} else {
		l.onAgentEvents = defaultTurnLoopOnAgentEvents[T, M]
	}
	return l
}

func (l *TurnLoop[T, M]) start(ctx context.Context) {
	l.runOnce.Do(func() {
		atomic.StoreInt32(&l.started, 1)
		go l.run(ctx)
	})
}

// Run starts the loop's processing goroutine. It is non-blocking: the loop
// runs in the background and results are obtained via Wait.
//
// If CheckpointID is configured in TurnLoopConfig and a matching checkpoint
// exists in Store, the loop automatically resumes from that checkpoint.
// Otherwise it starts fresh with whatever items were Push()-ed.
//
// Calling Run more than once is a no-op: only the first call starts the loop.
func (l *TurnLoop[T, M]) Run(ctx context.Context) {
	l.start(ctx)
}

// Push adds an item to the loop's buffer for processing.
// This method is non-blocking and thread-safe.
// Returns false if the loop has stopped, true otherwise. If a preemptive push
// succeeds, the second return value is a channel that callers can wait on to
// confirm the preempt signal has been received and the cancel request submitted
// — i.e., the current turn is guaranteed to be preempted. Specifically:
//   - If an agent is running: the channel closes after TurnLoop submits cancel.
//   - If no agent is running (loop idle or not yet started): the channel closes
//     immediately (nothing to cancel).
//
// If the loop has not been started yet (Run not called), items are buffered
// and will be processed once Run is called.
// After Wait() returns, failed pushes can be recovered via TurnLoopExitState.TakeLateItems().
// Once TakeLateItems() has been called, any subsequent push that would become a
// late item will panic instead of being silently dropped.
//
// Use WithPreempt() or WithPreemptTimeout() to atomically push an item and signal
// preemption of the current agent. This is useful for urgent items that should
// interrupt the current processing.
// The returned channel may be waited on if the caller needs to ensure the preempt
// signal has been observed.
//
// Use WithPreemptDelay() together with WithPreempt()/WithPreemptTimeout() to delay
// the preemption signal.
// Push returns immediately after the item is buffered, and a goroutine is spawned
// to signal preemption after the delay.
func (l *TurnLoop[T, M]) Push(item T, opts ...PushOption[T, M]) (bool, <-chan struct{}) {
	cfg := &pushConfig[T, M]{}
	for _, opt := range opts {
		opt(cfg)
	}

	if cfg.pushStrategy != nil {
		return l.pushWithStrategy(item, cfg)
	}

	return l.pushWithConfig(item, cfg)
}

// pushWithStrategy atomically holds the run loop and snapshots the current turn,
// then calls the strategy callback with a guaranteed-stable TurnContext. If the
// strategy returns preempt options, the hold is kept and a preempt is requested;
// otherwise the hold is released and the item is buffered as a plain push.
func (l *TurnLoop[T, M]) pushWithStrategy(item T, cfg *pushConfig[T, M]) (bool, <-chan struct{}) {
	strategy := cfg.pushStrategy

	runCtx, tcAny := l.preemptSig.holdAndGetTurn()
	if runCtx == nil {
		runCtx = context.Background()
	}
	var tc *TurnContext[T, M]
	if tcAny != nil {
		tc = tcAny.(*TurnContext[T, M])
	}
	realOpts := strategy(runCtx, tc)
	cfg = &pushConfig[T, M]{}
	for _, opt := range realOpts {
		opt(cfg)
	}
	cfg.pushStrategy = nil

	if !cfg.preempt {
		l.preemptSig.unholdRunLoop()
		if !l.buffer.TrySend(item) {
			l.appendLate(item)
			return false, nil
		}
		return true, nil
	}

	if atomic.LoadInt32(&l.stopped) != 0 {
		l.preemptSig.unholdRunLoop()
		l.appendLate(item)
		return false, nil
	}

	if !l.buffer.TrySend(item) {
		l.preemptSig.unholdRunLoop()
		l.appendLate(item)
		return false, nil
	}

	ack := make(chan struct{})
	if atomic.LoadInt32(&l.started) == 0 {
		l.preemptSig.unholdRunLoop()
		close(ack)
		return true, ack
	}

	if cfg.preemptDelay > 0 {
		go func() {
			select {
			case <-time.After(cfg.preemptDelay):
				l.preemptSig.requestPreempt(ack, cfg.agentCancelOpts...)
			case <-l.done:
				l.preemptSig.unholdRunLoop()
				close(ack)
			}
		}()
	} else {
		l.preemptSig.requestPreempt(ack, cfg.agentCancelOpts...)
	}
	return true, ack
}

func (l *TurnLoop[T, M]) pushWithConfig(item T, cfg *pushConfig[T, M]) (bool, <-chan struct{}) {
	if atomic.LoadInt32(&l.stopped) != 0 {
		l.appendLate(item)
		return false, nil
	}

	if cfg.preempt {
		l.preemptSig.holdRunLoop()

		if !l.buffer.TrySend(item) {
			l.preemptSig.unholdRunLoop()
			l.appendLate(item)
			return false, nil
		}

		ack := make(chan struct{})
		if atomic.LoadInt32(&l.started) == 0 {
			l.preemptSig.unholdRunLoop()
			close(ack)
			return true, ack
		}

		if cfg.preemptDelay > 0 {
			go func() {
				select {
				case <-time.After(cfg.preemptDelay):
					l.preemptSig.requestPreempt(ack, cfg.agentCancelOpts...)
				case <-l.done:
					l.preemptSig.unholdRunLoop()
					close(ack)
				}
			}()
		} else {
			l.preemptSig.requestPreempt(ack, cfg.agentCancelOpts...)
		}
		return true, ack
	}

	if !l.buffer.TrySend(item) {
		l.appendLate(item)
		return false, nil
	}
	return true, nil
}

// Stop signals the loop to stop and returns immediately (non-blocking).
// Without options, the current agent turn runs to completion and the loop
// exits at the turn boundary without starting a new turn. ExitReason is nil.
//
// Use WithImmediate() to abort the running agent turn immediately.
// Use WithGraceful() to cancel at the nearest safe point with recursive
// propagation to nested agents.
// Use WithGracefulTimeout() for safe-point cancel with an escalation deadline.
// Use UntilIdleFor() to defer the stop until the loop has been continuously
// idle for a given duration; the loop shuts down automatically once the idle
// timer fires.
//
// This method may be called multiple times; subsequent calls update cancel options.
// A Stop() call without UntilIdleFor shuts down the loop immediately, even if
// a prior UntilIdleFor is still waiting.
// Call Wait() to block until the loop has fully exited and get the result.
//
// Stop may be called before Run. In that case, the stopped flag is set and
// a subsequent Run will exit the loop immediately.
//
// If the running agent does not support the WithCancel AgentRunOption,
// all cancel-related options (WithImmediate, WithGraceful, WithGracefulTimeout)
// degrade to "exit the loop on entering the next iteration" — the current
// agent turn runs to completion before the loop exits.
func (l *TurnLoop[T, M]) Stop(opts ...StopOption) {
	cfg := &stopConfig{}
	for _, opt := range opts {
		opt(cfg)
	}

	// UntilIdleFor is incompatible with cancel options (WithImmediate,
	// WithGraceful, WithGracefulTimeout) in the same call. Cancel opts only
	// make sense for an immediate or escalated stop; UntilIdleFor defers the
	// stop until idle, and must not impact a running agent. Drop them silently.
	if cfg.idleFor > 0 {
		cfg.agentCancelOpts = nil
	}

	l.stopSig.signal(cfg)

	if cfg.idleFor > 0 {
		l.buffer.Wakeup()
		return
	}
	l.commitStop()
}

func (l *TurnLoop[T, M]) commitStop() {
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
// Wait blocks until Run is called AND the loop exits. If Run is
// never called, Wait blocks forever.
func (l *TurnLoop[T, M]) Wait() *TurnLoopExitState[T, M] {
	<-l.done
	return l.result
}

func (l *TurnLoop[T, M]) run(ctx context.Context) {
	defer l.cleanup(ctx)

	if err := l.tryLoadCheckpoint(ctx); err != nil {
		l.runErr = err
		return
	}

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
			var first T
			var ok bool

			if idleFor := l.stopSig.getIdleFor(); idleFor > 0 {
				l.buffer.ClearWakeup()
				idleTimer := time.NewTimer(idleFor)
				cancelIdle := make(chan struct{})
				// When the idle timer fires, commitStop closes the buffer via
				// buffer.Close(), which broadcasts to unblock the pending
				// Receive() call below.
				go func() {
					select {
					case <-idleTimer.C:
						l.commitStop()
					case <-cancelIdle:
					}
				}()

				first, ok = l.buffer.Receive()

				idleTimer.Stop()
				close(cancelIdle)
			} else {
				first, ok = l.buffer.Receive()
				// Woken up by Stop(UntilIdleFor); re-enter loop to start the idle timer.
				if !ok && l.stopSig.getIdleFor() > 0 {
					continue
				}
			}

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

		// Drain any pending preempt that arrived between turns. A Push caller
		// may have called holdRunLoop + requestPreempt while the loop was
		// between iterations; acknowledge and release before planning the
		// next turn. Use drainAll to release all pusher holds at once —
		// multiple concurrent Push(WithPreempt) callers each hold a ref.
		if preempted, _, ackList := l.preemptSig.waitForPreemptOrUnhold(); preempted {
			for _, ack := range ackList {
				close(ack)
			}
			l.preemptSig.drainAll()
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

		// Bracket the turn with holdRunLoop / endTurnAndUnhold. The run loop's
		// own hold ensures that if a Push caller also holds mid-turn, the total
		// holdCount stays > 0 after endTurnAndUnhold, blocking the loop at
		// waitForPreemptOrUnhold until the Push caller's preempt is resolved.
		l.preemptSig.holdRunLoop()
		runErr := l.runAgentAndHandleEvents(plan.turnCtx, agent, plan.spec)

		l.preemptSig.endTurnAndUnhold()

		if runErr != nil {
			if errors.As(runErr, new(*CancelError)) && len(l.canceledItems) == 0 {
				l.canceledItems = append([]T{}, plan.spec.consumed...)
			}
			l.runErr = runErr
			return
		}
	}
}

func (l *TurnLoop[T, M]) setupBridgeStore(spec *turnRunSpec[T, M], runOpts []AgentRunOption) ([]AgentRunOption, *bridgeStore, error) {
	store := l.config.Store
	if store == nil && spec.isResume {
		return nil, nil, fmt.Errorf("failed to resume agent: checkpoint store is nil")
	}
	if store == nil {
		return runOpts, nil, nil
	}
	runOpts = append(runOpts, WithCheckPointID(bridgeCheckpointID))
	if spec.isResume {
		if len(spec.resumeBytes) == 0 {
			return nil, nil, fmt.Errorf("resume checkpoint is empty")
		}
		return runOpts, newResumeBridgeStore(bridgeCheckpointID, spec.resumeBytes), nil
	}
	return runOpts, newBridgeStore(), nil
}

// watchPreemptSignal runs for the lifetime of a single turn. It listens on the
// notify channel for preempt requests and relays them to agentCancelFunc.
//
// preemptGen de-duplicates notifications: multiple notify wakes can fire for the
// same logical preempt (e.g. cond.Broadcast + channel send), so the watcher
// only acts when the generation advances.
//
// On the first preempt whose cancel actually contributed (i.e. the cancel options
// were accepted before the CancelError was finalized), preemptDone is closed to
// wake runAgentAndHandleEvents's select.
func (l *TurnLoop[T, M]) watchPreemptSignal(done <-chan struct{}, agentCancelFunc AgentCancelFunc, preemptDone chan struct{}) {
	var lastGen uint64
	for {
		select {
		case <-done:
			return
		case <-l.preemptSig.notify:
			if preempted, gen, opts, ackList := l.preemptSig.receivePreempt(); preempted {
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

// watchStopSignal runs for the lifetime of a single turn. It selects on two
// channels from stopSignal:
//
//   - done (permanently closed after Stop): the durable stop flag. Fires
//     immediately for any watcher, even those in turns started after
//     Stop() but before the run loop observed isStopped(). This eliminates
//     the race where a previous turn's watcher consumed the one-shot notify,
//     leaving the current turn unable to detect the stop.
//
//   - notify (one-shot, buffered 1): fires when a new Stop() call is made,
//     enabling cancel-mode escalation (e.g. CancelAfterToolCalls → CancelImmediate).
//     The generation counter de-duplicates wakes, analogous to preemptGen in
//     watchPreemptSignal.
//
// On the first cancel that actually contributed (i.e. the cancel was accepted
// before the CancelError was finalized), stoppedDone is closed to wake
// runAgentAndHandleEvents's select.
func (l *TurnLoop[T, M]) watchStopSignal(done <-chan struct{}, agentCancelFunc AgentCancelFunc, stoppedDone chan struct{}) {
	var lastGen uint64
	stoppedClosed := false

	tryCancel := func(gen uint64, opts []AgentCancelOption) {
		if gen == lastGen {
			return
		}
		lastGen = gen
		if opts == nil { // no cancel intent; see stopSignal.agentCancelOpts
			return
		}
		_, contributed := agentCancelFunc(opts...)
		if contributed && !stoppedClosed {
			close(stoppedDone)
			stoppedClosed = true
		}
	}

	for {
		select {
		case <-done:
			return
		case <-l.stopSig.notify:
			tryCancel(l.stopSig.check())
		case <-l.stopSig.done:
			tryCancel(l.stopSig.check())
			for {
				select {
				case <-done:
					return
				case <-l.stopSig.notify:
					tryCancel(l.stopSig.check())
				}
			}
		}
	}
}

func (l *TurnLoop[T, M]) runAgentAndHandleEvents(
	ctx context.Context,
	agent TypedAgent[M],
	spec *turnRunSpec[T, M],
) error {
	var iter *AsyncIterator[*TypedAgentEvent[M]]

	runOpts, ms, err := l.setupBridgeStore(spec, spec.runOpts)
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
	runner := NewTypedRunner[M](TypedRunnerConfig[M]{
		EnableStreaming: enableStreaming,
		Agent:           agent,
		CheckPointStore: ms,
	})

	preemptDone := make(chan struct{})
	stoppedDone := make(chan struct{})

	tc := &TurnContext[T, M]{
		Loop:      l,
		Consumed:  spec.consumed,
		Preempted: preemptDone,
		Stopped:   stoppedDone,
		StopCause: l.stopSig.getStopCause,
	}
	l.preemptSig.setTurn(ctx, tc)

	if spec.isResume {
		var err error
		if spec.resumeParams != nil {
			iter, err = runner.ResumeWithParams(ctx, bridgeCheckpointID, spec.resumeParams, runOpts...)
		} else {
			iter, err = runner.Resume(ctx, bridgeCheckpointID, runOpts...)
		}
		if err != nil {
			return fmt.Errorf("failed to resume agent: %w", err)
		}
	} else {
		iter = runner.Run(ctx, spec.input.Messages, runOpts...)
	}

	handleEvents := func() error {
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
			data, ok, err := ms.Get(ctx, bridgeCheckpointID)
			if err != nil {
				return fmt.Errorf("failed to read runner checkpoint: %w", err)
			}
			if ok {
				l.checkPointRunnerBytes = append([]byte{}, data...)
			}
		}
		return nil
	}

	// Wait for the turn to end. Three outcomes:
	//
	// done:         Events fully handled (normal or error). If Stop() was
	//               called, save checkpoint so the caller can resume later.
	//               Also handle the select race: if preemptDone is closed
	//               too, treat as a preempt (return nil) instead of leaking
	//               the CancelError.
	//
	// preemptDone:  A preemptive Push successfully cancelled the agent.
	//               Wait for the handleEvents goroutine to drain, then
	//               return nil — the run loop will start a new turn.
	//
	// stoppedDone:  Stop() cancelled the agent. Save checkpoint so the
	//               caller can resume later.
	select {
	case <-done:
		select {
		case <-preemptDone:
			return nil
		default:
		}
		if l.stopSig.isStopped() {
			if err := finalizeCheckpoint(); err != nil {
				if handleErr != nil {
					handleErr = fmt.Errorf("%w; checkpoint error: %v", handleErr, err)
				} else {
					handleErr = err
				}
			}
		}
		return handleErr
	case <-preemptDone:
		<-done
		return nil
	case <-stoppedDone:
		<-done
		if err := finalizeCheckpoint(); err != nil {
			if handleErr != nil {
				handleErr = fmt.Errorf("%w; checkpoint error: %v", handleErr, err)
			} else {
				handleErr = err
			}
		}
		return handleErr
	}
}

func (l *TurnLoop[T, M]) cleanup(ctx context.Context) {
	atomic.StoreInt32(&l.stopped, 1)

	unhandled := l.buffer.TakeAll()
	checkpointID := l.config.CheckpointID
	isIdle := len(l.checkPointRunnerBytes) == 0 && len(unhandled) == 0 && len(l.canceledItems) == 0

	// Only save checkpoint when the loop exited due to an explicit Stop().
	// If Stop() was called but a callback error happened concurrently,
	// the state may be inconsistent — don't checkpoint in that case.
	// We consider the exit Stop-caused if runErr is nil (clean stop between
	// turns) or a *CancelError (Stop canceled a running agent).
	exitCausedByStop := l.runErr == nil || errors.As(l.runErr, new(*CancelError))
	shouldSaveCheckpoint := l.config.Store != nil && checkpointID != "" && l.stopSig.isStopped() && exitCausedByStop && !isIdle && !l.stopSig.isSkipCheckpoint()

	var checkpointed bool
	var checkpointErr error

	if shouldSaveCheckpoint {
		cp := &turnLoopCheckpoint[T]{
			RunnerCheckpoint: l.checkPointRunnerBytes,
			HasRunnerState:   len(l.checkPointRunnerBytes) > 0,
			UnhandledItems:   unhandled,
			CanceledItems:    l.canceledItems,
		}
		checkpointed = true
		checkpointErr = l.saveTurnLoopCheckpoint(ctx, checkpointID, cp)
	} else if l.loadCheckpointID != "" {
		_ = l.deleteTurnLoopCheckpoint(ctx, l.loadCheckpointID)
	}

	var takeLateOnce sync.Once
	var takeLateResult []T

	l.result = &TurnLoopExitState[T, M]{
		ExitReason:          l.runErr,
		UnhandledItems:      unhandled,
		CanceledItems:       l.canceledItems,
		StopCause:           l.stopSig.getStopCause(),
		CheckpointAttempted: checkpointed,
		CheckpointErr:       checkpointErr,
		TakeLateItems: func() []T {
			takeLateOnce.Do(func() {
				l.lateMu.Lock()
				takeLateResult = append([]T{}, l.lateItems...)
				l.lateSealed = true
				l.lateMu.Unlock()
			})
			return takeLateResult
		},
	}

	l.preemptSig.drainAll()
	l.buffer.Close()
	close(l.done)
}
