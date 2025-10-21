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
	"encoding/json"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

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
	// MainAgentInputModifier allows custom modification of the main agent's input before processing.
	MainAgentInputModifier adk.GenModelInput
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
}

// New creates a new Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured ChatModelAgent ready for execution.
func New(ctx context.Context, cfg *Config) (adk.Agent, error) {
	builtinTools, err := newBuiltinTools(cfg.WithoutWriteTodos)
	if err != nil {
		return nil, err
	}

	tt, err := newTaskTool(
		ctx,
		cfg.TaskToolDescriptionGenerator,
		cfg.ChatModel,
		cfg.SubAgents,
		cfg.WithoutGeneralSubAgent,
		cfg.ToolsConfig,
		builtinTools,
	)
	if err != nil {
		return nil, fmt.Errorf("new task tool: %w", err)
	}

	cfg.ToolsConfig.Tools = append(cfg.ToolsConfig.Tools, append(builtinTools, tt)...)

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          cfg.Name,
		Description:   cfg.Description,
		Instruction:   cfg.Instruction + "\n" + baseAgentPrompt + "\n" + writeTodosPrompt + "\n" + taskPrompt,
		Model:         cfg.ChatModel,
		ToolsConfig:   cfg.ToolsConfig,
		GenModelInput: cfg.MainAgentInputModifier,
		MaxIterations: cfg.MaxIteration,
	})
}

func newTaskTool(
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.Agent) (string, error),
	cm model.ToolCallingChatModel,
	subAgents []adk.Agent,
	withoutGeneralSubAgent bool,
	customToolsConfig adk.ToolsConfig,
	generalBuiltInTools []tool.BaseTool,
) (tool.InvokableTool, error) {
	t := &taskTool{
		subAgents:     map[string]tool.InvokableTool{},
		subAgentSlice: subAgents,
		descGen:       defaultTaskToolDescription,
	}

	if taskToolDescriptionGenerator != nil {
		t.descGen = taskToolDescriptionGenerator
	}

	if !withoutGeneralSubAgent {
		tc := customToolsConfig
		tc.Tools = append(tc.Tools, generalBuiltInTools...)
		generalAgent, err := newGeneralAgent(ctx, cm, tc)
		if err != nil {
			return nil, fmt.Errorf("failed to new general agent: %w", err)
		}

		it, err := assertAgentTool(adk.NewAgentTool(ctx, generalAgent))
		if err != nil {
			return nil, err
		}
		t.subAgents[generalAgent.Name(ctx)] = it
		t.subAgentSlice = append(t.subAgentSlice, generalAgent)
	}

	for _, a := range subAgents {
		name := a.Name(ctx)
		it, err := assertAgentTool(adk.NewAgentTool(ctx, a))
		if err != nil {
			return nil, err
		}
		t.subAgents[name] = it
	}

	return t, nil
}

type taskTool struct {
	subAgents     map[string]tool.InvokableTool
	subAgentSlice []adk.Agent
	descGen       func(ctx context.Context, subAgents []adk.Agent) (string, error)
}

func (t *taskTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	desc, err := t.descGen(ctx, t.subAgentSlice)
	if err != nil {
		return nil, err
	}
	return &schema.ToolInfo{
		Name: taskToolName,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"subagent_type": {
				Type: schema.String,
				// todo: enum?
			},
			"description": {
				Type: schema.String,
			},
		}),
	}, nil
}

type taskToolArgument struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
}

func (t *taskTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	input := &taskToolArgument{}
	err := json.Unmarshal([]byte(argumentsInJSON), input)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal task tool input json: %w", err)
	}
	a, ok := t.subAgents[input.SubagentType]
	if !ok {
		return "", fmt.Errorf("subagent type %s not found", input.SubagentType)
	}

	params, err := sonic.MarshalString(map[string]string{
		"request": input.Description,
	})
	if err != nil {
		return "", err
	}

	return a.InvokableRun(ctx, params)
}

func defaultTaskToolDescription(ctx context.Context, subAgents []adk.Agent) (string, error) {
	subAgentsDescBuilder := strings.Builder{}
	for _, a := range subAgents {
		name := a.Name(ctx)
		desc := a.Description(ctx)
		subAgentsDescBuilder.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}
	return pyfmt.Fmt(taskToolDescription, map[string]any{
		"other_agents": subAgentsDescBuilder.String(),
	})
}

func newGeneralAgent(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	config adk.ToolsConfig,
) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        generalAgentName,
		Description: generalAgentDescription,
		Instruction: baseAgentPrompt + "\n" + writeTodosPrompt + "\n",
		Model:       cm,
		ToolsConfig: config,
	})
}
