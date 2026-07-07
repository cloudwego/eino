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
	"sync"
	"sync/atomic"
	"time"
)

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

type stopPhase uint8

const (
	stopOpen stopPhase = iota
	stopIdleWaiting
	stopCommitted
)

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
