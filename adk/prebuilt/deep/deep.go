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

// Package deep provides a prebuilt agent with deep task orchestration.
package deep

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[TODO]("_eino_adk_prebuilt_deep_todo")
	schema.RegisterName[[]TODO]("_eino_adk_prebuilt_deep_todo_slice")
}

// Config defines the configuration for creating a DeepAgent.
type Config struct {
	// Name is the identifier for the Deep agent.
	Name string
	// Description provides a brief explanation of the agent's purpose.
	Description string

	// ChatModel is the model used by DeepAgent for reasoning and task execution.
	ChatModel model.ToolCallingChatModel
	// Instruction contains the system prompt that guides the agent's behavior.
	// When empty, a built-in default system prompt will be used, which includes general assistant
	// behavior guidelines, security policies, coding style guidelines, and tool usage policies.
	Instruction string
	// SubAgents are specialized agents that can be invoked by the agent.
	SubAgents []adk.Agent
	// ToolsConfig provides the tools and tool-calling configurations available for the agent to invoke.
	ToolsConfig adk.ToolsConfig
	// MaxIteration limits the maximum number of reasoning iterations the agent can perform.
	MaxIteration int

	// WithoutWriteTodos disables the built-in write_todos tool when set to true.
	WithoutWriteTodos bool
	// WithoutGeneralSubAgent disables the general-purpose subagent when set to true.
	WithoutGeneralSubAgent bool
	// TaskToolDescriptionGenerator allows customizing the description for the task tool.
	// If provided, this function generates the tool description based on available subagents.
	TaskToolDescriptionGenerator func(ctx context.Context, availableAgents []adk.Agent) (string, error)

	Middlewares []adk.AgentMiddleware

	ModelRetryConfig *adk.ModelRetryConfig

	// OutputKey stores the agent's response in the session.
	// Optional. When set, stores output via AddSessionValue(ctx, outputKey, msg.Content).
	OutputKey string
}

// New creates a new Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured ChatModelAgent ready for execution.
func New(ctx context.Context, cfg *Config) (adk.ResumableAgent, error) {
	middlewares, err := buildBuiltinAgentMiddlewares(cfg.WithoutWriteTodos)
	if err != nil {
		return nil, err
	}

	instruction := cfg.Instruction
	if len(instruction) == 0 {
		instruction = baseAgentInstruction
	}

	if !cfg.WithoutGeneralSubAgent || len(cfg.SubAgents) > 0 {
		tt, err := newTaskToolMiddleware(
			ctx,
			cfg.TaskToolDescriptionGenerator,
			cfg.SubAgents,

			cfg.WithoutGeneralSubAgent,
			cfg.ChatModel,
			instruction,
			cfg.ToolsConfig,
			cfg.MaxIteration,
			append(middlewares, cfg.Middlewares...),
		)
		if err != nil {
			return nil, fmt.Errorf("failed to new task tool: %w", err)
		}
		middlewares = append(middlewares, tt)
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          cfg.Name,
		Description:   cfg.Description,
		Instruction:   instruction,
		Model:         cfg.ChatModel,
		ToolsConfig:   cfg.ToolsConfig,
		MaxIterations: cfg.MaxIteration,
		Middlewares:   append(middlewares, cfg.Middlewares...),

		GenModelInput:    genModelInput,
		ModelRetryConfig: cfg.ModelRetryConfig,
		OutputKey:        cfg.OutputKey,
	})
}

func genModelInput(ctx context.Context, instruction string, input *adk.AgentInput) ([]*schema.Message, error) {
	msgs := make([]*schema.Message, 0, len(input.Messages)+1)

	if instruction != "" {
		msgs = append(msgs, schema.SystemMessage(instruction))
	}

	msgs = append(msgs, input.Messages...)

	return msgs, nil
}

func buildBuiltinAgentMiddlewares(withoutWriteTodos bool) ([]adk.AgentMiddleware, error) {
	var ms []adk.AgentMiddleware
	if !withoutWriteTodos {
		t, err := newWriteTodos()
		if err != nil {
			return nil, err
		}
		ms = append(ms, t)
	}

	return ms, nil
}

type TODO struct {
	Content    string `json:"content"`
	ActiveForm string `json:"activeForm"`
	Status     string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed"`
}

type writeTodosArguments struct {
	Todos []TODO `json:"todos"`
}

func newWriteTodos() (adk.AgentMiddleware, error) {
	t, err := utils.InferTool("write_todos", writeTodosToolDescription, func(ctx context.Context, input writeTodosArguments) (output string, err error) {
		adk.AddSessionValue(ctx, SessionKeyTodos, input.Todos)
		todos, err := sonic.MarshalString(input.Todos)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated todo list to %s", todos), nil
	})
	if err != nil {
		return adk.AgentMiddleware{}, err
	}

	return adk.AgentMiddleware{
		AdditionalInstruction: writeTodosPrompt,
		AdditionalTools:       []tool.BaseTool{t},
	}, nil
}
