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

// Package supervisor implements the supervisor pattern for multi-agent systems,
// where a central agent coordinates a set of sub-agents.
//
// # Unified Tracing
//
// The supervisor pattern provides unified tracing support through an internal container.
// When using callbacks (e.g., for tracing or observability), the entire supervisor structure
// (supervisor agent + all sub-agents) shares a single trace root. This means:
//   - OnStart is invoked once at the supervisor container level
//   - The callback-enriched context (containing parent span info) is propagated to all agents
//   - All agents within the supervisor appear as children of the same trace root
//
// This is achieved by wrapping the supervisor structure in an internal container that acts
// as the single entry point for tracing. The container delegates all execution to the
// underlying agents while providing a unified identity for callbacks.
package supervisor

import (
	"context"

	"github.com/cloudwego/eino/adk"
)

type Config struct {
	// Supervisor specifies the agent that will act as the supervisor, coordinating and managing the sub-agents.
	Supervisor adk.Agent

	// SubAgents specifies the list of agents that will be supervised and coordinated by the supervisor agent.
	SubAgents []adk.Agent
}

// supervisorContainer wraps the entire supervisor structure to provide unified tracing.
// When callbacks are registered (e.g., via Runner.Query with WithCallbacks), OnStart/OnEnd
// are invoked once for this container, creating a single trace root. The callback-enriched
// context is then propagated to the supervisor and all sub-agents, ensuring they share
// the same trace parent.
//
// This container implements Agent and ResumableAgent by delegating to the inner agent.
// It provides its own Name and GetType for callback identification.
type supervisorContainer struct {
	name  string
	inner adk.ResumableAgent
}

func (s *supervisorContainer) Name(_ context.Context) string {
	return s.name
}

func (s *supervisorContainer) Description(ctx context.Context) string {
	return s.inner.Description(ctx)
}

func (s *supervisorContainer) GetType() string {
	return "Supervisor"
}

func (s *supervisorContainer) Run(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return s.inner.Run(ctx, input, opts...)
}

func (s *supervisorContainer) Resume(ctx context.Context, info *adk.ResumeInfo, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	return s.inner.Resume(ctx, info, opts...)
}

// New creates a supervisor-based multi-agent system with the given configuration.
//
// In the supervisor pattern, a designated supervisor agent coordinates multiple sub-agents.
// The supervisor can delegate tasks to sub-agents and receive their responses, while
// sub-agents can only communicate with the supervisor (not with each other directly).
// This hierarchical structure enables complex problem-solving through coordinated agent interactions.
//
// The returned agent is wrapped in an internal container that provides unified tracing.
// When used with Runner and callbacks, all agents within the supervisor structure will
// share the same trace root, making it easy to observe the entire multi-agent execution
// as a single logical unit.
func New(ctx context.Context, conf *Config) (adk.ResumableAgent, error) {
	subAgents := make([]adk.Agent, 0, len(conf.SubAgents))
	supervisorName := conf.Supervisor.Name(ctx)
	for _, subAgent := range conf.SubAgents {
		subAgents = append(subAgents, adk.AgentWithDeterministicTransferTo(ctx, &adk.DeterministicTransferConfig{
			Agent:        subAgent,
			ToAgentNames: []string{supervisorName},
		}))
	}

	inner, err := adk.SetSubAgents(ctx, conf.Supervisor, subAgents)
	if err != nil {
		return nil, err
	}

	return &supervisorContainer{
		name:  supervisorName,
		inner: inner,
	}, nil
}
