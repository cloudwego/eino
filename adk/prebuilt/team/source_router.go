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

// source_router.go routes TurnInput items to the correct agent's TurnLoop.

package team

import (
	"sync"

	"github.com/cloudwego/eino/adk"
)

// sourceRouter routes TurnInput items to the correct agent's TurnLoop by target name.
//
// It is push-based: callers push items via Push(), and the router forwards them
// to the registered TurnLoop for the target agent. Items with an empty
// TargetAgent are delivered to the default agent (leader). Items with an
// explicit but unknown TargetAgent are rejected rather than silently rerouted to
// the leader, so a typo'd or stale target cannot quietly pollute the leader's
// context with another agent's messages.
type sourceRouter struct {
	defaultAgent string
	logger       Logger

	mu    sync.RWMutex
	loops map[string]*adk.TurnLoop[TurnInput, adk.Message]
}

// newSourceRouter creates a push-based sourceRouter.
func newSourceRouter(defaultAgent string, logger Logger) *sourceRouter {
	return &sourceRouter{
		defaultAgent: defaultAgent,
		logger:       logger,
		loops:        make(map[string]*adk.TurnLoop[TurnInput, adk.Message]),
	}
}

// RegisterLoop registers a TurnLoop for the given agent name.
func (r *sourceRouter) RegisterLoop(agentName string, loop *adk.TurnLoop[TurnInput, adk.Message]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loops[agentName] = loop
}

// UnregisterLoop removes the TurnLoop registration for the given agent.
// A nil sourceRouter is a no-op: teammate middleware is constructed without a
// router (only the leader owns one), so routing operations through a teammate's
// manager must be harmless rather than panic.
func (r *sourceRouter) UnregisterLoop(agentName string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.loops, agentName)
}

// getLoop returns the TurnLoop for the given agent, or nil if not registered.
func (r *sourceRouter) getLoop(agentName string) *adk.TurnLoop[TurnInput, adk.Message] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loops[agentName]
}

// Push routes a TurnInput to the appropriate agent's TurnLoop.
// An empty TargetAgent is delivered to the default agent (leader). An explicit
// but unknown TargetAgent is rejected (returns false) instead of being rerouted
// to the leader, so a wrong or stale target surfaces to the caller rather than
// silently entering the leader's context.
func (r *sourceRouter) Push(item TurnInput, opts ...adk.PushOption[TurnInput, adk.Message]) (bool, <-chan struct{}) {
	target := item.TargetAgent
	if target == "" {
		target = r.defaultAgent
	}

	r.mu.RLock()
	loop, ok := r.loops[target]
	r.mu.RUnlock()

	if !ok {
		// Empty targets already resolved to the default agent above; reaching here
		// with ok=false for the default means no leader loop is registered yet.
		// An explicit unknown target is a routing error, not a leader message.
		if target != r.defaultAgent {
			r.logger.Printf("sourceRouter: rejecting item for unknown target agent %q", target)
		}
		return false, nil
	}

	return loop.Push(item, opts...)
}
