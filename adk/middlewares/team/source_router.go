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
// to the registered TurnLoop for the target agent. Items with an empty or unknown
// TargetAgent are delivered to the default agent (leader).
type sourceRouter struct {
	defaultAgent string
	logger       Logger

	mu    sync.RWMutex
	loops map[string]*adk.TurnLoop[TurnInput]
}

// newSourceRouter creates a push-based sourceRouter.
func newSourceRouter(defaultAgent string, logger Logger) *sourceRouter {
	return &sourceRouter{
		defaultAgent: defaultAgent,
		logger:       logger,
		loops:        make(map[string]*adk.TurnLoop[TurnInput]),
	}
}

// RegisterLoop registers a TurnLoop for the given agent name.
func (r *sourceRouter) RegisterLoop(agentName string, loop *adk.TurnLoop[TurnInput]) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.loops[agentName] = loop
}

// UnregisterLoop removes the TurnLoop registration for the given agent.
func (r *sourceRouter) UnregisterLoop(agentName string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.loops, agentName)
}

// getLoop returns the TurnLoop for the given agent, or nil if not registered.
func (r *sourceRouter) getLoop(agentName string) *adk.TurnLoop[TurnInput] {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.loops[agentName]
}

// Push routes a TurnInput to the appropriate agent's TurnLoop.
// Items with empty or unknown TargetAgent go to the default agent.
func (r *sourceRouter) Push(item TurnInput, opts ...adk.PushOption[TurnInput]) (bool, <-chan struct{}) {
	target := item.TargetAgent
	if target == "" {
		target = r.defaultAgent
	}

	r.mu.RLock()
	loop, ok := r.loops[target]
	if !ok {
		if target != r.defaultAgent {
			r.logger.Printf("sourceRouter: unknown target agent %q, routing to default %q", target, r.defaultAgent)
		}
		loop = r.loops[r.defaultAgent]
	}
	r.mu.RUnlock()

	if loop == nil {
		return false, nil
	}

	return loop.Push(item, opts...)
}
