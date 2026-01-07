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

package adk

import (
	"context"
	"fmt"
)

type MultiAgentConfig struct {
	Agent       Agent             // required
	Name        string            // optional, default using Agent.Name
	Description string            // optional, default using Agent.Description
	Middlewares []AgentMiddleware // optional
}

// The NewMultiAgent method enables wrapping any individual Agent into a Multi-Agent structure.
// It assigns a new Name and Description to the wrapped Agent and adds middleware-based runtime capabilities around it.
// This method is essentially a simple wrapper for Agents, designed to address the following specific use cases:
//  1. Customizing Agent Metadata: Modify the public-facing Name and Description of any existing Agent, while adding middleware to its execution flow.
//  2. Trace Aggregation in Isolated Multi-Agent Systems: In multi-Agent architectures (e.g., Supervisor patterns) where internal TransferToAgent operations exist,
//     individual Agents operate in separate layers with isolated Contexts. This isolation means there is no "root span" for tracing across all sub-Agents.
//     By using NewMultiAgent with appropriate trace middleware, a unified root span is created to aggregate all child spans, enabling coherent tracing across the system.
func NewMultiAgent(ctx context.Context, config MultiAgentConfig) (ResumableAgent, error) {
	if config.Agent == nil {
		return nil, fmt.Errorf("missing agent")
	}
	ma := &multiAgent{
		agent:       toFlowAgent(ctx, config.Agent),
		name:        config.Name,
		description: config.Description,
		middlewares: config.Middlewares,
	}
	if ma.name == "" {
		ma.name = config.Agent.Name(ctx)
	}
	if ma.description == "" {
		ma.description = config.Agent.Description(ctx)
	}
	return ma, nil
}

type multiAgent struct {
	agent       *flowAgent
	name        string
	description string
	middlewares []AgentMiddleware
}

func (ma *multiAgent) Name(ctx context.Context) string {
	return ma.name
}

func (ma *multiAgent) Description(ctx context.Context) string {
	return ma.description
}

func (ma *multiAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	agentContext := &AgentContext{
		AgentInput:      input,
		AgentRunOptions: options,
		agentName:       ma.name,
		entrance:        InvocationTypeRun,
	}

	mwHelper := newAgentMWHelper(append(globalAgentMiddlewares, ma.middlewares...)...)

	ctx, termIter := mwHelper.execBeforeAgents(ctx, agentContext)
	if termIter != nil {
		return termIter
	}

	iter := ma.agent.Run(ctx, agentContext.AgentInput, agentContext.AgentRunOptions...)

	return mwHelper.execOnEvents(ctx, agentContext, iter)
}

func (ma *multiAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	agentContext := &AgentContext{
		ResumeInfo:      info,
		AgentRunOptions: opts,
		agentName:       ma.name,
		entrance:        InvocationTypeResume,
	}

	mwHelper := newAgentMWHelper(append(globalAgentMiddlewares, ma.middlewares...)...)

	ctx, termIter := mwHelper.execBeforeAgents(ctx, agentContext)
	if termIter != nil {
		return termIter
	}

	iter := ma.agent.Resume(ctx, agentContext.ResumeInfo, agentContext.AgentRunOptions...)

	return mwHelper.execOnEvents(ctx, agentContext, iter)
}

func (ma *multiAgent) IsAgentMiddlewareEnabled() bool {
	return true
}
