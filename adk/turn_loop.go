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

type stopPhase uint8

const (
	stopOpen stopPhase = iota
	stopIdleWaiting
	stopCommitted
)

// TurnLoopInterruptMode controls how TurnLoop reacts to business interrupts
// emitted as AgentAction.Interrupted.
type TurnLoopInterruptMode int

const (
	// TurnLoopInterruptExits preserves the legacy behavior: a business interrupt
	// exits the loop with *InterruptError and persists a checkpoint when configured.
	TurnLoopInterruptExits TurnLoopInterruptMode = iota
	// TurnLoopInterruptWaitsForExplicitResume keeps the loop alive after a
	// business interrupt and waits for Resume(...) to provide explicit intent.
	TurnLoopInterruptWaitsForExplicitResume
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
	ErrTurnLoopStopped          = errors.New("adk: turn loop stopped")
	ErrTurnLoopNoPendingResume  = errors.New("adk: no pending resume")
	ErrTurnLoopResumeInProgress = errors.New("adk: resume already submitted")
	ErrTurnLoopEmptyResume      = errors.New("adk: resume items are empty")
)

type preemptTurnPhase uint8

const (
	preemptTurnIdle preemptTurnPhase = iota
	preemptTurnPlanning
	preemptTurnActive
)

func (p preemptTurnPhase) String() string {
	switch p {
	case preemptTurnIdle:
		return "idle"
	case preemptTurnPlanning:
		return "planning"
	case preemptTurnActive:
		return "active"
	default:
		return fmt.Sprintf("unknown(%d)", p)
	}
}

type preemptTurnSnapshot struct {
	hasTargetTurn bool
	turnID        uint64
	ctx           context.Context
	tc            any
}

type cancelRequestState struct {
	cfg             agentCancelConfig
	timeoutDeadline *time.Time
}

type preemptRequest struct {
	cancel   cancelRequestState
	ackChans []chan struct{}
}

func parseAgentCancelOptions(opts ...AgentCancelOption) agentCancelConfig {
	cfg := agentCancelConfig{Mode: CancelImmediate}
	for _, opt := range opts {
		opt(&cfg)
	}
	return cfg
}

func newCancelRequestState(opts []AgentCancelOption, now time.Time) cancelRequestState {
	cfg := parseAgentCancelOptions(opts...)
	var deadline *time.Time
	if cfg.Timeout != nil && *cfg.Timeout > 0 && cfg.Mode != CancelImmediate {
		d := now.Add(*cfg.Timeout)
		deadline = &d
	}
	cfg.Timeout = nil

	return cancelRequestState{
		cfg:             cfg,
		timeoutDeadline: deadline,
	}
}

func (s *cancelRequestState) merge(opts []AgentCancelOption, now time.Time) {
	if opts == nil {
		return
	}

	next := newCancelRequestState(opts, now)
	if s.cfg.Mode == CancelImmediate || next.cfg.Mode == CancelImmediate {
		s.cfg.Mode = CancelImmediate
		s.timeoutDeadline = nil
	} else {
		s.cfg.Mode |= next.cfg.Mode
		if next.timeoutDeadline != nil {
			if s.timeoutDeadline == nil || next.timeoutDeadline.Before(*s.timeoutDeadline) {
				deadline := *next.timeoutDeadline
				s.timeoutDeadline = &deadline
			}
		}
	}
	if next.cfg.Recursive {
		s.cfg.Recursive = true
	}
}

func (s cancelRequestState) cancelOptions(now time.Time) []AgentCancelOption {
	cfg := s.cfg
	if cfg.Mode != CancelImmediate && s.timeoutDeadline != nil {
		remaining := s.timeoutDeadline.Sub(now)
		if remaining <= 0 {
			cfg.Mode = CancelImmediate
			cfg.Timeout = nil
		} else {
			cfg.Timeout = &remaining
		}
	}

	opts := []AgentCancelOption{WithAgentCancelMode(cfg.Mode)}
	if cfg.Recursive {
		opts = append(opts, WithRecursive())
	}
	if cfg.Timeout != nil {
		opts = append(opts, WithAgentCancelTimeout(*cfg.Timeout))
	}
	return opts
}

func newPreemptRequest(ack chan struct{}, opts []AgentCancelOption, now time.Time) *preemptRequest {
	req := &preemptRequest{cancel: newCancelRequestState(opts, now)}
	if ack != nil {
		req.ackChans = append(req.ackChans, ack)
	}
	return req
}

func (r *preemptRequest) ack() {
	if r == nil {
		return
	}
	for _, ack := range r.ackChans {
		close(ack)
	}
	r.ackChans = nil
}

func (r *preemptRequest) merge(ack chan struct{}, opts []AgentCancelOption, now time.Time) {
	if ack != nil {
		r.ackChans = append(r.ackChans, ack)
	}
	r.cancel.merge(opts, now)
}

func (r *preemptRequest) cancelOptions(now time.Time) []AgentCancelOption {
	if r == nil {
		return nil
	}
	return r.cancel.cancelOptions(now)
}

// preemptController owns turn-targeted preempt requests and Push critical sections.
//
// Turn lifecycle:
//
//	idle ──beginPlanningTurn──▶ planning ──beginActiveTurn──▶ active ──endActiveTurn──▶ idle
//	                              │                                                      ▲
//	                              └────────abortPlanningTurn─────────────────────────────┘
//
// Push critical section (beginPush/endPush) overlaps with the turn lifecycle. The
// run loop calls waitForPushes before beginPlanningTurn to ensure no in-flight Push
// can observe stale turn state.
//
// Preempt request flow:
//   - Push captures a snapshot (turnID + hasTargetTurn) via beginPush.
//   - requestPreempt binds to the captured turnID; if the turn has moved on, the
//     request is resolved as a no-op.
//   - During active phase, receivePreempt transfers the pending request to the
//     watcher, which submits cancel and then acks.
type preemptController struct {
	mu   sync.Mutex
	cond *sync.Cond

	turnPhase     preemptTurnPhase
	turnID        uint64
	currentTC     any
	currentRunCtx context.Context

	pushInFlight int
	pending      *preemptRequest
	notify       chan struct{}
	closed       bool
}

func newPreemptController() *preemptController {
	c := &preemptController{notify: make(chan struct{}, 1)}
	c.cond = sync.NewCond(&c.mu)
	return c
}

func (c *preemptController) beginPlanningTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requirePhaseLocked(preemptTurnIdle, "beginPlanningTurn")
	c.requireNoPendingLocked("beginPlanningTurn")
	c.turnID++
	c.turnPhase = preemptTurnPlanning
	c.currentRunCtx = nil
	c.currentTC = nil
}

func (c *preemptController) abortPlanningTurn() *preemptRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requirePhaseLocked(preemptTurnPlanning, "abortPlanningTurn")
	c.turnPhase = preemptTurnIdle
	c.currentRunCtx = nil
	c.currentTC = nil
	req := c.pending
	c.pending = nil
	c.cond.Broadcast()
	return req
}

func (c *preemptController) beginActiveTurn(ctx context.Context, tc any) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requirePhaseLocked(preemptTurnPlanning, "beginActiveTurn")
	c.turnPhase = preemptTurnActive
	c.currentRunCtx = ctx
	c.currentTC = tc
	if c.pending != nil {
		c.notifyWatcherLocked()
	}
}

func (c *preemptController) endActiveTurn() *preemptRequest {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.requirePhaseLocked(preemptTurnActive, "endActiveTurn")
	c.turnPhase = preemptTurnIdle
	c.currentRunCtx = nil
	c.currentTC = nil
	req := c.pending
	c.pending = nil
	c.cond.Broadcast()
	return req
}

func (c *preemptController) requirePhaseLocked(expected preemptTurnPhase, op string) {
	if c.turnPhase != expected {
		panic(fmt.Sprintf("adk: preemptController.%s called while turn phase is %s; expected %s", op, c.turnPhase, expected))
	}
}

func (c *preemptController) requireNoPendingLocked(op string) {
	if c.pending != nil {
		panic(fmt.Sprintf("adk: preemptController.%s called with stale pending preempt request", op))
	}
}

func (c *preemptController) beginPush() preemptTurnSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pushInFlight++
	return preemptTurnSnapshot{
		hasTargetTurn: c.turnPhase == preemptTurnPlanning || c.turnPhase == preemptTurnActive,
		turnID:        c.turnID,
		ctx:           c.currentRunCtx,
		tc:            c.currentTC,
	}
}

func (c *preemptController) endPush() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.pushInFlight--
	if c.pushInFlight < 0 {
		panic("adk: preemptController.endPush called without matching beginPush")
	}
	c.cond.Broadcast()
}

func (c *preemptController) waitForPushes() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for c.pushInFlight > 0 {
		c.cond.Wait()
	}
}

func (c *preemptController) requestPreempt(target preemptTurnSnapshot, ack chan struct{}, opts ...AgentCancelOption) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed || !target.hasTargetTurn || c.turnPhase == preemptTurnIdle || c.turnID != target.turnID {
		if ack != nil {
			close(ack)
		}
		return
	}

	now := time.Now()
	if c.pending == nil {
		c.pending = newPreemptRequest(ack, opts, now)
	} else {
		c.pending.merge(ack, opts, now)
	}
	if c.turnPhase == preemptTurnActive {
		c.notifyWatcherLocked()
	}
}

func (c *preemptController) receivePreempt() (*preemptRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.turnPhase != preemptTurnActive || c.pending == nil {
		return nil, false
	}
	req := c.pending
	c.pending = nil
	return req, true
}

func (c *preemptController) closeForLoopExit() {
	c.mu.Lock()
	c.closed = true
	c.turnPhase = preemptTurnIdle
	c.currentRunCtx = nil
	c.currentTC = nil
	req := c.pending
	c.pending = nil
	select {
	case <-c.notify:
	default:
	}
	c.cond.Broadcast()
	c.mu.Unlock()

	req.ack()
}

func (c *preemptController) notifyWatcherLocked() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

type stopDecision struct {
	commit   bool
	wakeIdle bool
}

type stopCancelRequest struct {
	cancel cancelRequestState
}

func newStopCancelRequest(opts []AgentCancelOption, now time.Time) *stopCancelRequest {
	return &stopCancelRequest{cancel: newCancelRequestState(opts, now)}
}

func (r *stopCancelRequest) merge(opts []AgentCancelOption, now time.Time) {
	if r == nil {
		return
	}
	r.cancel.merge(opts, now)
}

func (r *stopCancelRequest) cancelOptions(now time.Time) []AgentCancelOption {
	if r == nil {
		return nil
	}
	return r.cancel.cancelOptions(now)
}

// stopController owns global Stop state and optional active-turn cancel requests.
//
// Stop has two independent layers:
//   - terminal loop intent: committed Stop prevents future turns and closes the buffer;
//   - optional active-turn cancel: cancel-capable Stop calls create a pending request
//     consumed by the watcher if the current turn is still active.
//
// Unlike preempt, Stop is not bound to a turnID. It is global and terminal.
// A pending cancel request is consumed by the active turn or dropped when that
// turn ends before consumption.
type stopController struct {
	mu sync.Mutex

	phase stopPhase

	hasActiveCancelTarget bool
	pending               *stopCancelRequest
	notify                chan struct{}

	idleFor        time.Duration
	skipCheckpoint bool
	stopCause      string

	closed bool
}

func newStopController() *stopController {
	return &stopController{notify: make(chan struct{}, 1)}
}

func (c *stopController) requestStop(cfg *stopConfig) stopDecision {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return stopDecision{}
	}
	if cfg.skipCheckpoint {
		c.skipCheckpoint = true
	}
	if cfg.stopCause != "" && c.stopCause == "" {
		c.stopCause = cfg.stopCause
	}
	if cfg.idleFor > 0 {
		if c.phase != stopCommitted && c.idleFor == 0 {
			c.phase = stopIdleWaiting
			c.idleFor = cfg.idleFor
		}
		return stopDecision{wakeIdle: c.phase == stopIdleWaiting}
	}

	committed := c.commitLocked()
	if cfg.agentCancelOpts != nil {
		now := time.Now()
		if c.pending == nil {
			c.pending = newStopCancelRequest(cfg.agentCancelOpts, now)
		} else {
			c.pending.merge(cfg.agentCancelOpts, now)
		}
		if c.hasActiveCancelTarget {
			c.notifyWatcherLocked()
		}
	}
	return stopDecision{commit: committed}
}

func (c *stopController) commit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitLocked()
}

func (c *stopController) commitLocked() bool {
	if c.closed || c.phase == stopCommitted {
		return false
	}
	c.phase = stopCommitted
	c.idleFor = 0
	return true
}

func (c *stopController) isCommitted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase == stopCommitted
}

func (c *stopController) idleDuration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != stopIdleWaiting {
		return 0
	}
	return c.idleFor
}

func (c *stopController) skipCheckpointEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skipCheckpoint
}

func (c *stopController) cause() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopCause
}

func (c *stopController) beginActiveTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.hasActiveCancelTarget = true
	if c.pending != nil {
		c.notifyWatcherLocked()
	}
}

func (c *stopController) endActiveTurn() *stopCancelRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hasActiveCancelTarget = false
	req := c.pending
	c.pending = nil
	return req
}

func (c *stopController) receiveCancel() (*stopCancelRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasActiveCancelTarget || c.pending == nil {
		return nil, false
	}
	req := c.pending
	c.pending = nil
	return req, true
}

func (c *stopController) closeForLoopExit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.hasActiveCancelTarget = false
	c.pending = nil
	select {
	case <-c.notify:
	default:
	}
}

func (c *stopController) notifyWatcherLocked() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}

// TurnLoopConfig is the configuration for creating a TurnLoop.
type TurnLoopConfig[T any, M MessageType] struct {
	// GenInput receives the TurnLoop instance and all buffered items, and decides what to process.
	// It returns which items to consume now vs keep for later turns.
	// The loop parameter allows calling Push() or Stop() directly from within the callback.
	// Required.
	GenInput func(ctx context.Context, loop *TurnLoop[T, M], items []T) (*GenInputResult[T, M], error)

	// GenResume is called when the loop has a pending runner checkpoint and
	// needs user policy to continue. This can happen when restoring a TurnLoop
	// checkpoint from Store, or in TurnLoopInterruptWaitsForExplicitResume mode
	// after Resume(...) accepts explicit interrupt-response items.
	//
	// It receives:
	//   - interruptedItems: the items being processed when the prior run was interrupted / canceled
	//   - unhandledItems: normal items buffered but not processed
	//   - newItems: restored-checkpoint legacy items, or explicit Resume(...) items
	//     in managed-interrupt mode. Normal Push(...) items never become resume
	//     intent in managed-interrupt mode.
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

	// InterruptMode controls whether business interrupts exit the loop or keep
	// it alive waiting for an explicit Resume(...) call. The zero value exits.
	InterruptMode TurnLoopInterruptMode

	// Session fields are passed through to the internal Runner used by TurnLoop.
	// They let fresh turns after managed interrupts reconstruct context from the
	// same managed session without TurnLoop inspecting SessionStore events.
	SessionID          string
	SessionStore       SessionStore
	Session *SessionConfig
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

type turnRunSpec[T any, M MessageType] struct {
	runCtx             context.Context
	input              *TypedAgentInput[M]
	runOpts            []AgentRunOption
	resumeParams       *ResumeParams
	isResume           bool
	consumed           []T
	resumeCheckpointID string
	resumeBytes        []byte
}

type turnPlan[T any, M MessageType] struct {
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
	resumeResult, err := l.config.GenResume(ctx, l, pr.interrupted, pr.unhandled, pr.resumeItems)
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
	switch resumeResult.Decision {
	case TurnLoopResumeDecisionResume:
		if resumeResult.Input != nil {
			return nil, errors.New("GenResumeResult.Input must be nil when resuming")
		}
		if len(pr.resumeBytes) == 0 {
			return nil, errors.New("resume checkpoint is empty")
		}
		resumeCheckpointID := pr.resumeCheckpointID
		if resumeCheckpointID == "" {
			resumeCheckpointID = bridgeCheckpointID
		}
		return &turnPlan[T, M]{
			turnCtx:   turnCtx,
			remaining: resumeResult.Remaining,
			spec: &turnRunSpec[T, M]{
				runCtx:             resumeResult.RunCtx,
				runOpts:            resumeResult.RunOpts,
				resumeParams:       resumeResult.ResumeParams,
				isResume:           true,
				consumed:           resumeResult.Consumed,
				resumeCheckpointID: resumeCheckpointID,
				resumeBytes:        pr.resumeBytes,
			},
		}, nil
	case TurnLoopResumeDecisionStartNewTurn:
		if resumeResult.Input == nil {
			return nil, errors.New("GenResumeResult.Input is nil for fresh turn")
		}
		return &turnPlan[T, M]{
			turnCtx:   turnCtx,
			remaining: resumeResult.Remaining,
			spec: &turnRunSpec[T, M]{
				runCtx:   resumeResult.RunCtx,
				input:    resumeResult.Input,
				runOpts:  resumeResult.RunOpts,
				consumed: resumeResult.Consumed,
			},
		}, nil
	default:
		return nil, fmt.Errorf("unknown GenResume decision: %d", resumeResult.Decision)
	}
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

	interruptedItems []T

	checkPointRunnerBytes []byte
	checkPointRunnerID    string
	interruptContexts     []*InterruptCtx
	capturedCancelErr     *CancelError

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

type turnLoopCheckpoint[T any] struct {
	RunnerCheckpointID string
	RunnerCheckpoint   []byte
	// HasRunnerState reports whether RunnerCheckpoint contains resumable runner state.
	// It is false for "between turns" checkpoints where no agent execution was
	// interrupted (e.g. Stop() before the first turn or between turns).
	HasRunnerState bool
	UnhandledItems []T
	ResumeItems    []T
	CanceledItems  []T // gob-compat: kept as CanceledItems for deserialization of existing checkpoints
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
		resumeCheckpointID := cp.RunnerCheckpointID
		if resumeCheckpointID == "" {
			resumeCheckpointID = bridgeCheckpointID
		}
		resumeItems := append([]T{}, cp.ResumeItems...)
		resumeSubmitted := len(resumeItems) > 0
		if !resumeSubmitted {
			resumeItems = append(resumeItems, newItems...)
		} else {
			unhandled := make([]T, 0, len(cp.UnhandledItems)+len(newItems))
			unhandled = append(unhandled, cp.UnhandledItems...)
			unhandled = append(unhandled, newItems...)
			cp.UnhandledItems = unhandled
		}
		l.pendingResume = &turnLoopPendingResume[T]{
			interrupted:        append([]T{}, cp.CanceledItems...),
			unhandled:          append([]T{}, cp.UnhandledItems...),
			resumeItems:        resumeItems,
			resumeSubmitted:    resumeSubmitted,
			source:             turnLoopPendingResumeSourceRestoredCheckpoint,
			resumeCheckpointID: resumeCheckpointID,
			resumeBytes:        append([]byte{}, cp.RunnerCheckpoint...),
		}
	} else {
		items := make([]T, 0, len(cp.UnhandledItems)+len(newItems))
		items = append(items, cp.UnhandledItems...)
		items = append(items, newItems...)
		l.buffer.PushFront(items)
	}

	return nil
}

type turnLoopPendingResumeSource uint8

const (
	turnLoopPendingResumeSourceRestoredCheckpoint turnLoopPendingResumeSource = iota
	turnLoopPendingResumeSourceManagedInterrupt
)

type turnLoopPendingResume[T any] struct {
	interrupted        []T
	unhandled          []T
	resumeItems        []T
	resumeSubmitted    bool
	source             turnLoopPendingResumeSource
	resumeCheckpointID string
	resumeBytes        []byte
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

type pushConfig[T any, M MessageType] struct {
	preempt         bool
	preemptDelay    time.Duration
	agentCancelOpts []AgentCancelOption
	pushStrategy    func(context.Context, *TurnContext[T, M]) []PushOption[T, M]
}

// PushOption is an option for Push().
type PushOption[T any, M MessageType] func(*pushConfig[T, M])

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
func WithPreempt[T any, M MessageType](safePoint SafePoint) PushOption[T, M] {
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
func WithPreemptTimeout[T any, M MessageType](safePoint SafePoint, timeout time.Duration) PushOption[T, M] {
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

// WithPreemptDelay sets a delay duration before resolving a preemptive Push.
// When used with WithPreempt or WithPreemptTimeout, the pushed item is buffered
// immediately, while the preempt request is resolved after the delay against the
// turn observed by Push. If that captured turn has already ended, the request is
// resolved as a no-op and must not cancel a later turn.
func WithPreemptDelay[T any, M MessageType](delay time.Duration) PushOption[T, M] {
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
func WithPushStrategy[T any, M MessageType](fn func(ctx context.Context, tc *TurnContext[T, M]) []PushOption[T, M]) PushOption[T, M] {
	return func(cfg *pushConfig[T, M]) {
		cfg.pushStrategy = fn
	}
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

// Push adds an item to the loop's buffer for processing.
// This method is non-blocking and thread-safe.
// Returns false if the loop has stopped, true otherwise. If a preemptive push
// succeeds, the second return value is a channel that callers can wait on to
// confirm the preempt request has been resolved. Specifically:
//   - If Push observes a planning or active turn that is still the target when
//     the request resolves, the channel closes after TurnLoop attempts to submit
//     cancel for that target turn.
//   - If Push observes no target turn, the loop has not started, the preempt
//     subsystem is closed, or a delayed target is already gone, the channel
//     closes as a no-op resolution.
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
// request resolution. Push returns immediately after the item is buffered, and
// the delayed request remains bound to the turn observed by Push.
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

// Resume submits an explicit response to a pending managed business interrupt.
// Unlike Push, Resume is not normal input and does not preempt an active turn.
// It synchronously accepts the items or returns an error explaining why they
// could not be accepted.
func (l *TurnLoop[T, M]) Resume(items ...T) error {
	if len(items) == 0 {
		return ErrTurnLoopEmptyResume
	}

	l.resumeMu.Lock()
	defer l.resumeMu.Unlock()

	if atomic.LoadInt32(&l.stopped) != 0 || l.buffer.IsClosed() {
		return ErrTurnLoopStopped
	}
	if l.pendingResume == nil {
		return ErrTurnLoopNoPendingResume
	}
	if l.pendingResume.resumeSubmitted {
		return ErrTurnLoopResumeInProgress
	}
	l.pendingResume.resumeItems = append([]T{}, items...)
	l.pendingResume.resumeSubmitted = true
	l.buffer.Wakeup()
	return nil
}

// pushWithStrategy snapshots the current target turn while the strategy decides
// how to enqueue the item. If it requests preempt, that request is bound to the
// captured turn identity, including delayed preempt requests.
func (l *TurnLoop[T, M]) pushWithStrategy(item T, cfg *pushConfig[T, M]) (bool, <-chan struct{}) {
	strategy := cfg.pushStrategy

	snapshot := l.preemptCtrl.beginPush()
	defer l.preemptCtrl.endPush()

	runCtx := snapshot.ctx
	if runCtx == nil {
		runCtx = context.Background()
	}
	var tc *TurnContext[T, M]
	if snapshot.tc != nil {
		tc = snapshot.tc.(*TurnContext[T, M])
	}
	realOpts := strategy(runCtx, tc)
	cfg = &pushConfig[T, M]{}
	for _, opt := range realOpts {
		opt(cfg)
	}
	cfg.pushStrategy = nil

	if !cfg.preempt {
		if !l.buffer.TrySend(item) {
			l.appendLate(item)
			return false, nil
		}
		return true, nil
	}

	if atomic.LoadInt32(&l.stopped) != 0 {
		l.appendLate(item)
		return false, nil
	}

	if !l.buffer.TrySend(item) {
		l.appendLate(item)
		return false, nil
	}

	ack := make(chan struct{})
	if atomic.LoadInt32(&l.started) == 0 {
		close(ack)
		return true, ack
	}

	if cfg.preemptDelay > 0 {
		go func() {
			select {
			case <-time.After(cfg.preemptDelay):
				l.preemptCtrl.requestPreempt(snapshot, ack, cfg.agentCancelOpts...)
			case <-l.done:
				close(ack)
			}
		}()
	} else {
		l.preemptCtrl.requestPreempt(snapshot, ack, cfg.agentCancelOpts...)
	}
	return true, ack
}

func (l *TurnLoop[T, M]) pushWithConfig(item T, cfg *pushConfig[T, M]) (bool, <-chan struct{}) {
	if atomic.LoadInt32(&l.stopped) != 0 {
		l.appendLate(item)
		return false, nil
	}

	if cfg.preempt {
		snapshot := l.preemptCtrl.beginPush()
		defer l.preemptCtrl.endPush()

		if !l.buffer.TrySend(item) {
			l.appendLate(item)
			return false, nil
		}

		ack := make(chan struct{})
		if atomic.LoadInt32(&l.started) == 0 {
			close(ack)
			return true, ack
		}

		if cfg.preemptDelay > 0 {
			go func() {
				select {
				case <-time.After(cfg.preemptDelay):
					l.preemptCtrl.requestPreempt(snapshot, ack, cfg.agentCancelOpts...)
				case <-l.done:
					close(ack)
				}
			}()
		} else {
			l.preemptCtrl.requestPreempt(snapshot, ack, cfg.agentCancelOpts...)
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

	decision := l.stopCtrl.requestStop(cfg)
	if decision.wakeIdle {
		l.buffer.Wakeup()
	}
	if decision.commit {
		l.finishStopCommit()
	}
}

func (l *TurnLoop[T, M]) commitStop() {
	if !l.stopCtrl.commit() {
		return
	}
	l.finishStopCommit()
}

func (l *TurnLoop[T, M]) finishStopCommit() {
	atomic.StoreInt32(&l.stopped, 1)
	l.buffer.Close()
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

func (l *TurnLoop[T, M]) takePendingResume(ctx context.Context) (*turnLoopPendingResume[T], bool) {
	for {
		l.resumeMu.Lock()
		pr := l.pendingResume
		if pr == nil {
			l.resumeMu.Unlock()
			return nil, false
		}
		if pr.source != turnLoopPendingResumeSourceManagedInterrupt || pr.resumeSubmitted {
			l.pendingResume = nil
			l.resumeMu.Unlock()
			return pr, true
		}
		l.resumeMu.Unlock()

		first, ok := l.buffer.Receive()
		if !ok {
			if err := ctx.Err(); err != nil {
				l.runErr = err
				return nil, false
			}
			if l.stopCtrl.isCommitted() || l.buffer.IsClosed() {
				return nil, false
			}
			continue
		}
		normalItems := append([]T{first}, l.buffer.TakeAll()...)
		l.resumeMu.Lock()
		if l.pendingResume != nil {
			l.pendingResume.unhandled = append(l.pendingResume.unhandled, normalItems...)
		} else {
			l.buffer.PushFront(normalItems)
		}
		l.resumeMu.Unlock()
	}
}

func (l *TurnLoop[T, M]) restorePendingResume(pr *turnLoopPendingResume[T]) {
	if pr == nil {
		return
	}
	l.resumeMu.Lock()
	defer l.resumeMu.Unlock()
	l.pendingResume = pr
}

type turnLoopNextItems[T any] struct {
	isResume bool
	pr       *turnLoopPendingResume[T]
	items    []T
	pushBack []T
}

func (l *TurnLoop[T, M]) collectNextTurnItems(ctx context.Context) (*turnLoopNextItems[T], bool) {
	next := &turnLoopNextItems[T]{}
	if l.pendingResume != nil {
		next.isResume = true
		var ok bool
		next.pr, ok = l.takePendingResume(ctx)
		if !ok {
			return nil, false
		}

		l.preemptCtrl.waitForPushes()
		buffered := l.buffer.TakeAll()
		if next.pr.source == turnLoopPendingResumeSourceRestoredCheckpoint && !next.pr.resumeSubmitted {
			next.pr.resumeItems = append(next.pr.resumeItems, buffered...)
		} else {
			next.pr.unhandled = append(next.pr.unhandled, buffered...)
		}

		next.pushBack = make([]T, 0, len(next.pr.interrupted)+len(next.pr.unhandled)+len(next.pr.resumeItems))
		next.pushBack = append(next.pushBack, next.pr.interrupted...)
		next.pushBack = append(next.pushBack, next.pr.unhandled...)
		next.pushBack = append(next.pushBack, next.pr.resumeItems...)
		return next, true
	}

	first, ok := l.receiveNextTurnItem(ctx)
	if !ok {
		return nil, false
	}

	if err := ctx.Err(); err != nil {
		l.buffer.PushFront([]T{first})
		l.runErr = err
		return nil, false
	}

	if l.stopCtrl.isCommitted() {
		l.buffer.PushFront([]T{first})
		return nil, false
	}

	l.preemptCtrl.waitForPushes()
	rest := l.buffer.TakeAll()
	next.items = append([]T{first}, rest...)
	next.pushBack = next.items
	return next, true
}

func (l *TurnLoop[T, M]) receiveNextTurnItem(ctx context.Context) (T, bool) {
	if idleFor := l.stopCtrl.idleDuration(); idleFor > 0 {
		return l.receiveNextTurnItemUntilIdle(ctx, idleFor)
	}
	first, ok := l.buffer.Receive()
	// Woken up by Stop(UntilIdleFor); re-enter loop to start the idle timer.
	if !ok && l.stopCtrl.idleDuration() > 0 {
		var zero T
		return zero, false
	}
	if !ok {
		if err := ctx.Err(); err != nil {
			l.runErr = err
		}
	}
	return first, ok
}

func (l *TurnLoop[T, M]) receiveNextTurnItemUntilIdle(ctx context.Context, idleFor time.Duration) (T, bool) {
	l.buffer.ClearWakeup()
	idleTimer := time.NewTimer(idleFor)
	cancelIdle := make(chan struct{})
	// When the idle timer fires, commitStop closes the buffer via buffer.Close(),
	// which broadcasts to unblock the pending Receive() call below.
	go func() {
		select {
		case <-idleTimer.C:
			l.commitStop()
		case <-cancelIdle:
		}
	}()

	first, ok := l.buffer.Receive()

	idleTimer.Stop()
	close(cancelIdle)

	if !ok {
		if err := ctx.Err(); err != nil {
			l.runErr = err
		}
		if !l.buffer.IsClosed() {
			var zero T
			return zero, false
		}
	}
	return first, ok
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
		if l.stopCtrl.isCommitted() {
			return
		}

		next, ok := l.collectNextTurnItems(ctx)
		if !ok {
			if l.stopCtrl.idleDuration() > 0 && !l.stopCtrl.isCommitted() && !l.buffer.IsClosed() && ctx.Err() == nil {
				continue
			}
			return
		}

		if next.isResume && l.stopCtrl.isCommitted() {
			l.restorePendingResume(next.pr)
			return
		}

		l.preemptCtrl.beginPlanningTurn()
		abortPlanning := func() {
			l.preemptCtrl.abortPlanningTurn().ack()
		}

		plan, err := l.planTurn(ctx, next.isResume, next.items, next.pr)
		if err != nil {
			abortPlanning()
			if len(next.pushBack) > 0 {
				l.buffer.PushFront(next.pushBack)
			}
			l.runErr = err
			return
		}

		if l.stopCtrl.isCommitted() {
			abortPlanning()
			if next.isResume && plan.spec.isResume {
				l.restorePendingResume(next.pr)
				return
			}
			if len(next.pushBack) > 0 {
				l.buffer.PushFront(next.pushBack)
			}
			return
		}

		agent, err := l.config.PrepareAgent(plan.turnCtx, l, plan.spec.consumed)
		if err != nil {
			abortPlanning()
			if len(next.pushBack) > 0 {
				l.buffer.PushFront(next.pushBack)
			}
			if next.isResume && !plan.spec.isResume {
				l.loadCheckpointID = ""
			}
			l.runErr = err
			return
		}

		if l.stopCtrl.isCommitted() {
			abortPlanning()
			if next.isResume && plan.spec.isResume {
				l.restorePendingResume(next.pr)
				return
			}
			if len(next.pushBack) > 0 {
				l.buffer.PushFront(next.pushBack)
			}
			return
		}

		if next.isResume && !plan.spec.isResume && l.loadCheckpointID != "" {
			checkpointID := l.loadCheckpointID
			if err := l.deleteTurnLoopCheckpoint(ctx, checkpointID); err != nil {
				abortPlanning()
				if len(next.pushBack) > 0 {
					l.buffer.PushFront(next.pushBack)
				}
				l.loadCheckpointID = ""
				l.runErr = fmt.Errorf("failed to abandon checkpoint[%s] before fresh turn: %w", checkpointID, err)
				return
			}
			l.loadCheckpointID = ""
		}

		l.buffer.PushFront(plan.remaining)

		runErr := l.runAgentAndHandleEvents(plan.turnCtx, agent, plan.spec)

		if runErr != nil {
			// Set interruptedItems when a cancel or interrupt was captured from the
			// event stream, regardless of what the user's callback returned. The items
			// were factually mid-execution when the signal arrived.
			if l.capturedCancelErr != nil || l.interruptContexts != nil {
				l.interruptedItems = append([]T{}, plan.spec.consumed...)
			}
			l.runErr = runErr
			return
		}

		// Business interrupt: agent produced an Interrupted action, exit to persist checkpoint.
		if l.interruptContexts != nil {
			if l.config.InterruptMode != TurnLoopInterruptWaitsForExplicitResume {
				l.interruptedItems = append([]T{}, plan.spec.consumed...)
				l.runErr = &InterruptError{InterruptContexts: l.interruptContexts}
				return
			}
			l.resumeMu.Lock()
			l.pendingResume = &turnLoopPendingResume[T]{
				interrupted:        append([]T{}, plan.spec.consumed...),
				unhandled:          append([]T{}, l.buffer.TakeAll()...),
				source:             turnLoopPendingResumeSourceManagedInterrupt,
				resumeCheckpointID: l.checkPointRunnerID,
				resumeBytes:        append([]byte{}, l.checkPointRunnerBytes...),
			}
			l.resumeMu.Unlock()
			l.interruptContexts = nil
			l.interruptedItems = nil
			l.checkPointRunnerID = ""
			l.checkPointRunnerBytes = nil
			l.capturedCancelErr = nil
			continue
		}
	}
}

func (l *TurnLoop[T, M]) setupBridgeStore(spec *turnRunSpec[T, M], runOpts []AgentRunOption) ([]AgentRunOption, *bridgeStore, error) {
	needsBridge := l.config.Store != nil || l.config.InterruptMode == TurnLoopInterruptWaitsForExplicitResume || spec.isResume
	if !needsBridge {
		return runOpts, nil, nil
	}
	checkpointID := bridgeCheckpointID
	if spec.resumeCheckpointID != "" {
		checkpointID = spec.resumeCheckpointID
	}
	runOpts = append(runOpts, WithCheckPointID(checkpointID))
	if spec.isResume {
		if len(spec.resumeBytes) == 0 {
			return nil, nil, fmt.Errorf("resume checkpoint is empty")
		}
		return runOpts, newResumeBridgeStore(checkpointID, spec.resumeBytes), nil
	}
	return runOpts, newBridgeStore(), nil
}

// watchPreempt runs for the lifetime of a single active turn. It consumes
// pending preempt requests exactly once and submits cancel for that turn.
func (l *TurnLoop[T, M]) watchPreempt(done <-chan struct{}, agentCancelFunc AgentCancelFunc, preemptDone chan struct{}) {
	preemptDoneClosed := false
	for {
		select {
		case <-done:
			return
		case <-l.preemptCtrl.notify:
			req, ok := l.preemptCtrl.receivePreempt()
			if !ok {
				continue
			}
			// CancelHandle is intentionally not awaited here: agentCancelFunc commits the cancel signal synchronously,
			// while waiting would block until the turn finishes and can deadlock this watcher against the done signal.
			_, contributed := agentCancelFunc(req.cancelOptions(time.Now())...)
			if contributed && !preemptDoneClosed {
				close(preemptDone)
				preemptDoneClosed = true
			}
			req.ack()
		}
	}
}

// watchStop runs for the lifetime of a single active turn. It consumes pending
// Stop cancel requests exactly once and submits them to that turn.
func (l *TurnLoop[T, M]) watchStop(done <-chan struct{}, agentCancelFunc AgentCancelFunc, stoppedDone chan struct{}) {
	stoppedClosed := false

	submit := func(req *stopCancelRequest) {
		_, contributed := agentCancelFunc(req.cancelOptions(time.Now())...)
		if contributed && !stoppedClosed {
			close(stoppedDone)
			stoppedClosed = true
		}
	}

	for {
		if req, ok := l.stopCtrl.receiveCancel(); ok {
			submit(req)
			continue
		}

		select {
		case <-done:
			return
		case <-l.stopCtrl.notify:
		}
	}
}

func (l *TurnLoop[T, M]) runAgentAndHandleEvents(
	ctx context.Context,
	agent TypedAgent[M],
	spec *turnRunSpec[T, M],
) error {
	l.interruptContexts = nil
	l.capturedCancelErr = nil
	l.checkPointRunnerBytes = nil
	l.checkPointRunnerID = ""

	var iter *AsyncIterator[*TypedAgentEvent[M]]

	runOpts, ms, err := l.setupBridgeStore(spec, spec.runOpts)
	if err != nil {
		l.preemptCtrl.abortPlanningTurn().ack()
		return err
	}
	cancelOpt, agentCancelFunc := WithCancel()
	runOpts = append(runOpts, cancelOpt)

	// For Run path the streaming mode comes from the input. For Resume path the
	// runner reads the streaming mode persisted in the checkpoint, so the value we
	// pass here is irrelevant.
	enableStreaming := false
	if spec.input != nil {
		enableStreaming = spec.input.EnableStreaming
	}
	runner := NewTypedRunner(TypedRunnerConfig[M]{
		EnableStreaming:    enableStreaming,
		Agent:              agent,
		CheckPointStore:    ms,
		SessionID:          l.config.SessionID,
		SessionStore:       l.config.SessionStore,
		Session: l.config.Session,
	})

	preemptDone := make(chan struct{})
	stoppedDone := make(chan struct{})

	tc := &TurnContext[T, M]{
		Loop:      l,
		Consumed:  spec.consumed,
		Preempted: preemptDone,
		Stopped:   stoppedDone,
		StopCause: l.stopCtrl.cause,
	}
	l.preemptCtrl.beginActiveTurn(ctx, tc)
	l.stopCtrl.beginActiveTurn()
	defer func() {
		l.stopCtrl.endActiveTurn()
		l.preemptCtrl.endActiveTurn().ack()
	}()

	if spec.isResume {
		var err error
		if spec.resumeParams != nil {
			iter, err = runner.ResumeWithParams(ctx, spec.resumeCheckpointID, spec.resumeParams, runOpts...)
		} else {
			iter, err = runner.Resume(ctx, spec.resumeCheckpointID, runOpts...)
		}
		if err != nil {
			return fmt.Errorf("failed to resume agent: %w", err)
		}
	} else {
		iter = runner.Run(ctx, spec.input.Messages, runOpts...)
	}

	// Wrap iterator to capture framework-level signals (CancelError, InterruptContexts)
	// from events before they flow to OnAgentEvents. This ensures the framework can
	// track these signals independently of what the user's callback returns.
	srcIter := iter
	proxyIter, proxyGen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go func() {
		defer proxyGen.Close()
		for {
			event, ok := srcIter.Next()
			if !ok {
				break
			}
			if event != nil {
				if event.Err != nil {
					var cancelErr *CancelError
					if errors.As(event.Err, &cancelErr) {
						l.capturedCancelErr = cancelErr
					}
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					l.interruptContexts = event.Action.Interrupted.InterruptContexts
				}
			}
			proxyGen.Send(event)
		}
	}()
	iter = proxyIter

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
	go l.watchPreempt(done, agentCancelFunc, preemptDone)
	go l.watchStop(done, agentCancelFunc, stoppedDone)

	finalizeCheckpoint := func() error {
		if ms != nil {
			key, data, ok := ms.LastCheckpoint()
			if ok {
				l.checkPointRunnerID = key
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
		if err := finalizeCheckpoint(); err != nil {
			if handleErr != nil {
				handleErr = fmt.Errorf("%w; checkpoint error: %v", handleErr, err)
			} else {
				handleErr = err
			}
		}
		return l.applyFrameworkCapturedError(handleErr)
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
		return l.applyFrameworkCapturedError(handleErr)
	}
}

// applyFrameworkCapturedError resolves the final error for runAgentAndHandleEvents.
// Priority scheme:
//   - If handleErr != nil: the user's callback error wins (framework does not overwrite).
//   - If handleErr == nil and a CancelError was captured: use the captured CancelError.
//   - If handleErr == nil and interrupt contexts were captured: this is handled by the
//     caller (run loop) via l.interruptContexts, so return nil here.
//
// In all cases, the caller uses l.capturedCancelErr and l.interruptContexts to
// determine interruptedItems independently of the returned error.
func (l *TurnLoop[T, M]) applyFrameworkCapturedError(handleErr error) error {
	if handleErr != nil {
		return handleErr
	}
	if l.capturedCancelErr != nil {
		return l.capturedCancelErr
	}
	return nil
}

func interruptedItemsForExit[T any](items []T, pending *turnLoopPendingResume[T]) []T {
	if pending != nil {
		return pending.interrupted
	}
	return items
}

func (l *TurnLoop[T, M]) cleanup(ctx context.Context) {
	atomic.StoreInt32(&l.stopped, 1)

	unhandled := l.buffer.TakeAll()
	l.resumeMu.Lock()
	pending := l.pendingResume
	l.resumeMu.Unlock()
	if pending != nil {
		unhandled = append(append([]T{}, pending.unhandled...), unhandled...)
	}
	checkpointID := l.config.CheckpointID
	hasPendingRunnerState := pending != nil && len(pending.resumeBytes) > 0
	isIdle := len(l.checkPointRunnerBytes) == 0 && !hasPendingRunnerState && len(unhandled) == 0 && len(l.interruptedItems) == 0

	// Only save checkpoint when the loop exited due to an explicit Stop(),
	// a CancelError, or a business interrupt (InterruptError).
	// Also checkpoint when a cancel/interrupt was captured from the event stream
	// but the user's callback returned a custom error (the items were still in-flight).
	exitCausedByStop := l.runErr == nil || errors.As(l.runErr, new(*CancelError)) || l.capturedCancelErr != nil
	businessInterrupt := errors.As(l.runErr, new(*InterruptError)) || l.interruptContexts != nil
	pendingResume := pending != nil
	shouldSaveCheckpoint := l.config.Store != nil && checkpointID != "" &&
		((l.stopCtrl.isCommitted() && exitCausedByStop) || businessInterrupt || pendingResume) &&
		!isIdle && !l.stopCtrl.skipCheckpointEnabled()

	var checkpointed bool
	var checkpointErr error

	if shouldSaveCheckpoint {
		runnerCheckpointID := l.checkPointRunnerID
		runnerCheckpoint := l.checkPointRunnerBytes
		interruptedItems := l.interruptedItems
		var resumeItems []T
		if pending != nil {
			runnerCheckpointID = pending.resumeCheckpointID
			runnerCheckpoint = pending.resumeBytes
			interruptedItems = pending.interrupted
			if pending.resumeSubmitted {
				resumeItems = append([]T{}, pending.resumeItems...)
			}
		}
		cp := &turnLoopCheckpoint[T]{
			RunnerCheckpointID: runnerCheckpointID,
			RunnerCheckpoint:   runnerCheckpoint,
			HasRunnerState:     len(runnerCheckpoint) > 0,
			UnhandledItems:     unhandled,
			ResumeItems:        resumeItems,
			CanceledItems:      interruptedItems,
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
		InterruptedItems:    interruptedItemsForExit(l.interruptedItems, pending),
		StopCause:           l.stopCtrl.cause(),
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

	l.stopCtrl.closeForLoopExit()
	l.preemptCtrl.closeForLoopExit()
	l.buffer.Close()
	close(l.done)
}
