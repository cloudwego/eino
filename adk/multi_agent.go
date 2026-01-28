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
	"errors"
)

// multiAgent wraps an agent with custom name and description.
// It delegates all operations to the inner agent while providing
// its own identity for callback and orchestration purposes.
type multiAgent struct {
	name        string
	description string
	agent       Agent
}

func (m *multiAgent) GetType() string {
	return "MultiAgent"
}

func (m *multiAgent) Name(_ context.Context) string {
	return m.name
}

func (m *multiAgent) Description(_ context.Context) string {
	return m.description
}

func (m *multiAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return m.agent.Run(ctx, input, opts...)
}

func (m *multiAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if ra, ok := m.agent.(ResumableAgent); ok {
		return ra.Resume(ctx, info, opts...)
	}
	return genErrorIter(errors.New("inner agent is not resumable"))
}

// NewMultiAgent creates a ResumableAgent that wraps the given agent with a custom name and description.
// The inner agent is wrapped in a flowAgent to enable callback support and proper event handling.
//
// Use this when you need to:
//   - Give an agent a different identity for orchestration purposes
//   - Enable callback support for an agent that doesn't have it
//   - Create a named entry point for a multi-agent workflow
func NewMultiAgent(ctx context.Context, name, desc string, agent Agent) ResumableAgent {
	fa := toFlowAgent(ctx, agent)
	return &multiAgent{name: name, description: desc, agent: fa}
}
