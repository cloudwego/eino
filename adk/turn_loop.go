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
	"sync"
	"sync/atomic"
)

// TurnLoopResumeDecision is returned by GenResume to choose what to do with a
// pending runner checkpoint.
type TurnLoopResumeDecision int

const (
	// TurnLoopResumeDecisionResume resumes the suspended runner checkpoint.
	TurnLoopResumeDecisionResume TurnLoopResumeDecision = iota
	// TurnLoopResumeDecisionStartNewTurn abandons the checkpoint and starts a
	// fresh Runner.Run turn using GenResumeResult.Input.
	TurnLoopResumeDecisionStartNewTurn
)

var (
	ErrTurnLoopStopped = errors.New("adk: turn loop stopped")
)

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any, M MessageType] struct {
	// GenInput receives the TurnLoop instance and all buffered items, and decides what to process.
	// It returns which items to consume now vs keep for later turns.
	// The loop parameter allows calling Push() or Stop() directly from within the callback.
	// Required.
	GenInput func(ctx context.Context, loop *TurnLoop[T, M], items []T) (*GenInputResult[T, M], error)

	// GenResume is called when the loop restores a pending runner checkpoint
	// from Store and needs user policy to continue.
	//
	// It receives:
	//   - interruptedItems: the items being processed when the prior run was interrupted / canceled
	//   - unhandledItems: normal items buffered but not processed
	//   - newItems: items treated as resume intent (pre-run Push items for legacy
	//     checkpoints with no persisted ResumeItems, or persisted ResumeItems for
	//     checkpoints that already had accepted resume items).
	//
	// It returns a GenResumeResult choosing whether to resume the suspended
	// runner checkpoint or abandon it and start a fresh turn.
	GenResume func(ctx context.Context, loop *TurnLoop[T, M], interruptedItems, unhandledItems, newItems []T) (*GenResumeResult[T, M], error)

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
	// (InterruptedItems, UnhandledItems) via gob encoding, so T must be gob-encodable
	// when Store is used.
	Store CheckPointStore

	// CheckpointID, when set together with Store, enables automatic
	// checkpoint-based resume. On Run(), the TurnLoop queries Store for this ID:
	//   - If a checkpoint exists with runner state (mid-turn interrupt / cancel),
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

	// Session fields are passed through to the internal Runner used by TurnLoop.
	// They let the Runner reconstruct session context for both fresh turns and
	// resumed turns from a persisted checkpoint.
	SessionID     string
	SessionStore  SessionEventStore[M]
	SessionConfig *SessionConfig[M]
}

// GenInputResult contains the result of GenInput processing.
type GenInputResult[T any, M MessageType] struct {
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
type GenResumeResult[T any, M MessageType] struct {
	// RunCtx, if non-nil, overrides the context for this resumed turn's execution
	// (PrepareAgent, agent resume, OnAgentEvents).
	RunCtx context.Context

	// RunOpts are the options for this agent resume run.
	// Note: do not pass WithCheckPointID here; the TurnLoop automatically
	// injects the checkpointID into the Runner.
	RunOpts []AgentRunOption

	// ResumeParams are optional parameters for resuming an interrupted agent.
	ResumeParams *ResumeParams

	// Decision selects whether to resume the suspended checkpoint or abandon it
	// and start a fresh turn. The zero value resumes for compatibility.
	Decision TurnLoopResumeDecision

	// Input is required when Decision is TurnLoopResumeDecisionStartNewTurn.
	Input *TypedAgentInput[M]

	// Consumed are the items selected for this resumed turn.
	// They are removed from the buffer and passed to PrepareAgent.
	Consumed []T

	// Remaining are the items to keep in the buffer for a future turn.
	// TurnLoop pushes Remaining back into the buffer before resuming the agent.
	//
	// Items from (interruptedItems, unhandledItems, resume items) that are in neither Consumed
	// nor Remaining are dropped by the loop.
	Remaining []T
}

// InterruptError is the ExitReason when the TurnLoop exits due to a business
// interrupt (AgentAction.Interrupted). It carries InterruptContexts needed for
// targeted resumption via ResumeParams, parallel to CancelError.
//
// Unlike CancelError (which indicates forceful cancellation), InterruptError
// indicates the agent voluntarily paused execution at a business-defined point.
type InterruptError struct {
	// InterruptContexts provides the interrupt contexts needed for targeted
	// resumption via ResumeParams. Each context represents a step in the agent
	// hierarchy that was interrupted. Use each InterruptCtx.ID as a key in
	// ResumeParams.Targets.
	InterruptContexts []*InterruptCtx
}

func (e *InterruptError) Error() string {
	return fmt.Sprintf("agent interrupted: %d context(s)", len(e.InterruptContexts))
}

// TurnLoopExitState is returned when TurnLoop exits, containing the exit reason
// and any items that were not processed.
type TurnLoopExitState[T any, M MessageType] struct {
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

	// InterruptedItems contains the items whose turn was interrupted — either by
	// a cancel (Stop with cancel options → *CancelError) or by a business
	// interrupt (AgentAction.Interrupted → *InterruptError).
	// On resume, these are passed to GenResume's interruptedItems parameter.
	InterruptedItems []T

	// StopCause is the business-supplied reason passed via WithStopCause.
	// Empty if Stop was not called or no cause was provided.
	StopCause string

	// CheckpointAttempted indicates whether a checkpoint save was attempted when the loop exited.
	// True when Store is configured, CheckpointID is set, the loop was not idle
	// at exit time, WithSkipCheckpoint was not used, and the exit was caused by
	// Stop() (clean or cancel) or a business interrupt (*InterruptError).
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
type TurnContext[T any, M MessageType] struct {
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
type TurnLoop[T any, M MessageType] struct {
	config TurnLoopConfig[T, M]

	buffer *turnBuffer[T]

	stopped int32
	started int32

	done chan struct{}

	result *TurnLoopExitState[T, M]

	runOnce sync.Once

	stopCtrl *stopController

	preemptCtrl *preemptController

	runErr error

	pendingResume *turnLoopPendingResume[T]
	resumeMu      sync.Mutex

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

func defaultTurnLoopOnAgentEvents[T any, M MessageType](_ context.Context, _ *TurnContext[T, M], events *AsyncIterator[*TypedAgentEvent[M]]) error {
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
func NewTurnLoop[T any, M MessageType](cfg TurnLoopConfig[T, M]) *TurnLoop[T, M] {
	if cfg.GenInput == nil {
		panic("adk: NewTurnLoop: GenInput is required")
	}
	if cfg.PrepareAgent == nil {
		panic("adk: NewTurnLoop: PrepareAgent is required")
	}

	l := &TurnLoop[T, M]{
		config:      cfg,
		buffer:      newTurnBuffer[T](),
		done:        make(chan struct{}),
		stopCtrl:    newStopController(),
		preemptCtrl: newPreemptController(),
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
