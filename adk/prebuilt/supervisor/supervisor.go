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

package supervisor

import (
	"context"

	"github.com/cloudwego/eino/adk"
)

type Config struct {
	Supervisor adk.Agent
	SubAgents  []adk.Agent
}

func New(ctx context.Context, conf *Config) (adk.Agent, error) {
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
