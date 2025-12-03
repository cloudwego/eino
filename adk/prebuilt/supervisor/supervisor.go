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

// New creates a supervisor-based multi-agent system with the given configuration.
//
// In the supervisor pattern, a designated supervisor agent coordinates multiple sub-agents.
// The supervisor can delegate tasks to sub-agents and receive their responses, while
// sub-agents can only communicate with the supervisor (not with each other directly).
// This hierarchical structure enables complex problem-solving through coordinated agent interactions.
func New(ctx context.Context, conf *Config) (adk.ResumableAgent, error) {
	subAgents := make([]adk.Agent, 0, len(conf.SubAgents))
	supervisorName := conf.Supervisor.Name(ctx)
	for _, subAgent := range conf.SubAgents {
		subAgents = append(subAgents, adk.AgentWithDeterministicTransferTo(ctx, &adk.DeterministicTransferConfig{
			Agent:        subAgent,
			ToAgentNames: []string{supervisorName},
		}))
	}

	return adk.SetSubAgents(ctx, conf.Supervisor, subAgents)
}
