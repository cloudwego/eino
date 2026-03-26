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
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type taskToolConfig struct {
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.Agent) (string, error)
	subAgents                    []adk.Agent

	withoutGeneralSubAgent bool
	chatModel              model.BaseChatModel
	instruction            string
	toolsConfig            adk.ToolsConfig
	maxIteration           int
	middlewares            []adk.AgentMiddleware
	handlers               []adk.ChatModelAgentMiddleware

	explore *ExploreAgentConfig
	plan    *PlanAgentConfig
}

func newTaskToolMiddleware(ctx context.Context, cfg *taskToolConfig) (adk.ChatModelAgentMiddleware, error) {
	t, err := newTaskTool(ctx, cfg)
	if err != nil {
		return nil, err
	}
	prompt := internal.SelectPrompt(internal.I18nPrompts{
		English: taskPrompt,
		Chinese: taskPromptChinese,
	})

	return buildAppendPromptTool(prompt, t), nil
}

func newTaskTool(ctx context.Context, cfg *taskToolConfig) (tool.InvokableTool, error) {
	t := &taskTool{
		subAgents: make(map[string]tool.InvokableTool),
		descGen:   defaultTaskToolDescription,
	}

	if cfg.taskToolDescriptionGenerator != nil {
		t.descGen = cfg.taskToolDescriptionGenerator
	}

	if !cfg.withoutGeneralSubAgent {
		agentDesc := internal.SelectPrompt(internal.I18nPrompts{
			English: generalAgentDescription,
			Chinese: generalAgentDescriptionChinese,
		})
		generalAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:          generalAgentName,
			Description:   agentDesc,
			Instruction:   cfg.instruction,
			Model:         cfg.chatModel,
			ToolsConfig:   cfg.toolsConfig,
			MaxIterations: cfg.maxIteration,
			Middlewares:   cfg.middlewares,
			Handlers:      cfg.handlers,
			GenModelInput: genModelInput,
		})
		if err != nil {
			return nil, err
		}

		if err = t.addSubAgent(ctx, generalAgent); err != nil {
			return nil, err
		}
	}

	if cfg.explore != nil && cfg.explore.Enable {
		agent, err := newExploreAgent(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to build explore agent: %w", err)
		}
		if err = t.addSubAgent(ctx, agent); err != nil {
			return nil, err
		}
	}

	if cfg.plan != nil && cfg.plan.Enable {
		agent, err := newPlanAgent(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to build plan agent: %w", err)
		}
		if err = t.addSubAgent(ctx, agent); err != nil {
			return nil, err
		}
	}

	for _, a := range cfg.subAgents {
		if err := t.addSubAgent(ctx, a); err != nil {
			return nil, err
		}
	}

	return t, nil
}

func newReadOnlyHandlers(disabledTools []string) []adk.ChatModelAgentMiddleware {
	if len(disabledTools) == 0 {
		disabledTools = defaultReadOnlyDisabledTools
	}
	disabledSet := make(map[string]bool, len(disabledTools))
	for _, name := range disabledTools {
		disabledSet[name] = true
	}

	reminder := internal.SelectPrompt(internal.I18nPrompts{
		English: readOnlyReminder,
		Chinese: readOnlyReminderChinese,
	})

	return []adk.ChatModelAgentMiddleware{
		&disableToolsMiddleware{
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
			disabledTools:                disabledSet,
		},
		&readOnlyReminderMiddleware{
			BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
			reminder:                     reminder,
		},
	}
}

func newExploreAgent(ctx context.Context, cfg *taskToolConfig) (adk.Agent, error) {
	cm := cfg.explore.ChatModel
	if cm == nil {
		cm = cfg.chatModel
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: exploreAgentName,
		Description: internal.SelectPrompt(internal.I18nPrompts{
			English: exploreAgentDescription,
			Chinese: exploreAgentDescriptionChinese,
		}),
		Instruction: internal.SelectPrompt(internal.I18nPrompts{
			English: exploreAgentInstruction,
			Chinese: exploreAgentInstructionChinese,
		}),
		Model:         cm,
		ToolsConfig:   cfg.toolsConfig,
		MaxIterations: cfg.maxIteration,
		Middlewares:   cfg.middlewares,
		Handlers:      append(cfg.handlers, newReadOnlyHandlers(cfg.explore.DisabledTools)...),
		GenModelInput: genModelInput,
	})
}

func newPlanAgent(ctx context.Context, cfg *taskToolConfig) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name: planAgentName,
		Description: internal.SelectPrompt(internal.I18nPrompts{
			English: planAgentDescription,
			Chinese: planAgentDescriptionChinese,
		}),
		Instruction: internal.SelectPrompt(internal.I18nPrompts{
			English: planAgentInstruction,
			Chinese: planAgentInstructionChinese,
		}),
		Model:         cfg.chatModel,
		ToolsConfig:   cfg.toolsConfig,
		MaxIterations: cfg.maxIteration,
		Middlewares:   cfg.middlewares,
		Handlers:      append(cfg.handlers, newReadOnlyHandlers(cfg.plan.DisabledTools)...),
		GenModelInput: genModelInput,
	})
}

type taskTool struct {
	subAgents     map[string]tool.InvokableTool
	subAgentSlice []adk.Agent
	descGen       func(ctx context.Context, subAgents []adk.Agent) (string, error)
}

func (t *taskTool) addSubAgent(ctx context.Context, agent adk.Agent) error {
	it, err := assertAgentTool(adk.NewAgentTool(ctx, agent))
	if err != nil {
		return err
	}
	t.subAgents[agent.Name(ctx)] = it
	t.subAgentSlice = append(t.subAgentSlice, agent)
	return nil
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

	return a.InvokableRun(ctx, params, opts...)
}

func defaultTaskToolDescription(ctx context.Context, subAgents []adk.Agent) (string, error) {
	subAgentsDescBuilder := strings.Builder{}
	for _, a := range subAgents {
		name := a.Name(ctx)
		desc := a.Description(ctx)
		subAgentsDescBuilder.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}
	toolDesc := internal.SelectPrompt(internal.I18nPrompts{
		English: taskToolDescription,
		Chinese: taskToolDescriptionChinese,
	})
	return pyfmt.Fmt(toolDesc, map[string]any{
		"other_agents": subAgentsDescBuilder.String(),
	})
}
