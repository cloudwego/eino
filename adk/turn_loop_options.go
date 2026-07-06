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
