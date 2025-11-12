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

func newTaskToolMiddleware(
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.Agent) (string, error),
	subAgents []adk.Agent,

	withoutGeneralSubAgent bool,
	cm model.ToolCallingChatModel,
	instruction string,
	toolsConfig adk.ToolsConfig,
	maxIteration int,
	middlewares []adk.AgentMiddleware,
) (adk.AgentMiddleware, error) {
	t, err := newTaskTool(ctx, taskToolDescriptionGenerator, subAgents, withoutGeneralSubAgent, cm, instruction, toolsConfig, maxIteration, middlewares)
	if err != nil {
		return adk.AgentMiddleware{}, err
	}
	return adk.AgentMiddleware{
		AdditionalInstruction: taskPrompt,
		AdditionalTools:       []tool.BaseTool{t},
	}, nil
}

func newTaskTool(
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.Agent) (string, error),
	subAgents []adk.Agent,

	withoutGeneralSubAgent bool,
	Model model.ToolCallingChatModel,
	Instruction string,
	ToolsConfig adk.ToolsConfig,
	MaxIteration int,
	middlewares []adk.AgentMiddleware,
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
		generalAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name:          generalAgentName,
			Description:   generalAgentDescription,
			Instruction:   Instruction,
			Model:         Model,
			ToolsConfig:   ToolsConfig,
			MaxIterations: MaxIteration,
			Middlewares:   middlewares,
		})
		if err != nil {
			return nil, err
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
