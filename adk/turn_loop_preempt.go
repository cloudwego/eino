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
	"time"
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
