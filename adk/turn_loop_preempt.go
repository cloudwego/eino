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
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// SafePoint describes at which boundary the agent may be cancelled.
//
// SafePoint is used only in the preemption API (WithPreempt/WithPreemptTimeout).
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
// timeout must be positive; passing a zero or negative duration panics.
func WithPreemptTimeout[T any, M MessageType](safePoint SafePoint, timeout time.Duration) PushOption[T, M] {
	if safePoint == 0 {
		panic("adk: SafePoint must not be zero; use AfterToolCalls, AfterChatModel, or AnySafePoint")
	}
	if timeout <= 0 {
		panic("adk: WithPreemptTimeout: timeout must be positive")
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

	return l.pushAfterSnapshot(snapshot, item, cfg)
}

func (l *TurnLoop[T, M]) pushAfterSnapshot(snapshot preemptTurnSnapshot, item T, cfg *pushConfig[T, M]) (bool, <-chan struct{}) {
	if atomic.LoadInt32(&l.stopped) != 0 {
		l.appendLate(item)
		return false, nil
	}

	if !l.buffer.TrySend(item) {
		l.appendLate(item)
		return false, nil
	}

	if !cfg.preempt {
		return true, nil
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
	if cfg.preempt {
		snapshot := l.preemptCtrl.beginPush()
		defer l.preemptCtrl.endPush()
		return l.pushAfterSnapshot(snapshot, item, cfg)
	}

	return l.pushAfterSnapshot(preemptTurnSnapshot{}, item, cfg)
}

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
	cancel cancelRequestState
	ackChs []chan struct{}
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

func (s *cancelRequestState) cancelOptions(now time.Time) []AgentCancelOption {
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
		req.ackChs = append(req.ackChs, ack)
	}
	return req
}

func (r *preemptRequest) ack() {
	if r == nil {
		return
	}
	for _, ack := range r.ackChs {
		close(ack)
	}
	r.ackChs = nil
}

func (r *preemptRequest) merge(ack chan struct{}, opts []AgentCancelOption, now time.Time) {
	if ack != nil {
		r.ackChs = append(r.ackChs, ack)
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
