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
	"sync/atomic"
	"time"
)

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
