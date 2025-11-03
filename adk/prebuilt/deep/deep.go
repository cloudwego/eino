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

package deep

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
)

type AgentContext struct {
	Model        model.ToolCallingChatModel
	Instruction  string
	ToolsConfig  adk.ToolsConfig
	MaxIteration int
}

type Middleware func(*AgentContext)

// Config defines the configuration for creating a DeepAgent.
type Config struct {
	// Name is the identifier for the Deep agent.
	Name string
	// Description provides a brief explanation of the agent's purpose.
	Description string

	// ChatModel is the model used by DeepAgent for reasoning and task execution.
	ChatModel model.ToolCallingChatModel
	// Instruction contains the system prompt that guides the agent's behavior.
	Instruction string
	// SubAgents are specialized agents that can be invoked by the agent.
	SubAgents []adk.Agent
	// ToolsConfig provides the tools and tool-calling configurations available for the agent to invoke.
	ToolsConfig adk.ToolsConfig
	// MaxIteration limits the maximum number of reasoning iterations the agent can perform.
	MaxIteration int

	Middlewares []Middleware

	// WithoutWriteTodos disables the built-in write_todos tool when set to true.
	WithoutWriteTodos bool
	// WithoutGeneralSubAgent disables the general-purpose subagent when set to true.
	WithoutGeneralSubAgent bool
	// TaskToolDescriptionGenerator allows customizing the description for the task tool.
	// If provided, this function generates the tool description based on available subagents.
	TaskToolDescriptionGenerator func(ctx context.Context, availableAgents []adk.Agent) (string, error)
}

// New creates a new Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured ChatModelAgent ready for execution.
func New(ctx context.Context, cfg *Config) (adk.Agent, error) {
	middlewares, err := buildBuiltinMiddlewares(cfg.WithoutWriteTodos)
	if err != nil {
		return nil, err
	}

	actx := AgentContext{
		Model:        cfg.ChatModel,
		Instruction:  cfg.Instruction,
		ToolsConfig:  cfg.ToolsConfig,
		MaxIteration: cfg.MaxIteration,
	}

	tt, err := buildTaskToolMiddleware(
		ctx,
		cfg.TaskToolDescriptionGenerator,
		cfg.SubAgents,

		cfg.WithoutGeneralSubAgent,
		actx,
		append(cfg.Middlewares, middlewares...),
	)
	if err != nil {
		return nil, fmt.Errorf("new task tool: %w", err)
	}
	middlewares = append(middlewares, tt)

	return newAgentWithMiddlewares(cfg.Name, cfg.Description, actx, append(cfg.Middlewares, middlewares...)), nil
}

func newAgentWithMiddlewares(name, desc string, actx AgentContext, middlewares []Middleware) adk.Agent {
	return &agent{
		name:        name,
		desc:        desc,
		actx:        actx,
		middlewares: middlewares,
	}
}

type agent struct {
	name string
	desc string

	actx        AgentContext
	middlewares []Middleware
}

func (a *agent) Name(_ context.Context) string {
	return a.name
}

func (a *agent) Description(_ context.Context) string {
	return a.desc
}

func (a *agent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	cma, err := loadAgentContext(ctx, a.actx, a.name, a.desc, a.middlewares)
	if err != nil {
		iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		gen.Send(&adk.AgentEvent{Err: err})
		gen.Close()
		return iter
	}
	return cma.Run(ctx, input, options...)
}

func (a *agent) Resume(ctx context.Context, info *adk.ResumeInfo, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	cma, err := loadAgentContext(ctx, a.actx, a.name, a.desc, a.middlewares)
	if err != nil {
		iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		gen.Send(&adk.AgentEvent{Err: err})
		gen.Close()
		return iter
	}
	return cma.(adk.ResumableAgent).Resume(ctx, info, opts...)
}

func loadAgentContext(ctx context.Context, actx AgentContext, name, desc string, middlewares []Middleware) (adk.Agent, error) {
	p := &actx
	for _, m := range middlewares {
		m(p)
	}
	cma, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          name,
		Description:   desc,
		Instruction:   p.Instruction,
		Model:         p.Model,
		ToolsConfig:   p.ToolsConfig,
		MaxIterations: p.MaxIteration,
	})
	if err != nil {
		return nil, err
	}
	return cma, nil
}
