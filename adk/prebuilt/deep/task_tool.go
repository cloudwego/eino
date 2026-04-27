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

func typedTaskToolMiddleware[M messageType](
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error),
	subAgents []adk.TypedAgent[M],

	withoutGeneralSubAgent bool,
	cm model.BaseModel[M],
	instruction string,
	toolsConfig adk.ToolsConfig,
	maxIteration int,
	middlewares []adk.AgentMiddleware,
	handlers []adk.TypedChatModelAgentMiddleware[M],
	modelFailoverConfig *adk.TypedModelFailoverConfig[M],
) (adk.TypedChatModelAgentMiddleware[M], error) {
	t, err := typedNewTaskTool(ctx, taskToolDescriptionGenerator, subAgents, withoutGeneralSubAgent, cm, instruction, toolsConfig, maxIteration, middlewares, handlers, modelFailoverConfig)
	if err != nil {
		return nil, err
	}
	prompt := internal.SelectPrompt(internal.I18nPrompts{
		English: taskPrompt,
		Chinese: taskPromptChinese,
	})

	return typedBuildAppendPromptTool[M](prompt, t), nil
}

func newTaskToolMiddleware(
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.Agent) (string, error),
	subAgents []adk.Agent,

	withoutGeneralSubAgent bool,
	// cm is the chat model. Tools are configured via model.WithTools call option.
	cm model.BaseChatModel,
	instruction string,
	toolsConfig adk.ToolsConfig,
	maxIteration int,
	middlewares []adk.AgentMiddleware,
	handlers []adk.ChatModelAgentMiddleware,
	modelFailoverConfig *adk.ModelFailoverConfig,
) (adk.ChatModelAgentMiddleware, error) {
	return typedTaskToolMiddleware[*schema.Message](ctx, taskToolDescriptionGenerator, subAgents, withoutGeneralSubAgent, cm, instruction, toolsConfig, maxIteration, middlewares, handlers, modelFailoverConfig)
}

func typedNewTaskTool[M messageType](
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error),
	subAgents []adk.TypedAgent[M],

	withoutGeneralSubAgent bool,
	cm model.BaseModel[M],
	instruction string,
	toolsConfig adk.ToolsConfig,
	maxIteration int,
	middlewares []adk.AgentMiddleware,
	handlers []adk.TypedChatModelAgentMiddleware[M],
	modelFailoverConfig *adk.TypedModelFailoverConfig[M],
) (tool.InvokableTool, error) {
	t := &typedTaskTool[M]{
		subAgents:     map[string]tool.InvokableTool{},
		subAgentSlice: subAgents,
		descGen:       typedDefaultTaskToolDescription[M],
	}

	if taskToolDescriptionGenerator != nil {
		t.descGen = taskToolDescriptionGenerator
	}

	if !withoutGeneralSubAgent {
		agentDesc := internal.SelectPrompt(internal.I18nPrompts{
			English: generalAgentDescription,
			Chinese: generalAgentDescriptionChinese,
		})
		generalAgent, err := adk.NewTypedChatModelAgent[M](ctx, &adk.TypedChatModelAgentConfig[M]{
			Name:                generalAgentName,
			Description:         agentDesc,
			Instruction:         instruction,
			Model:               cm,
			ToolsConfig:         toolsConfig,
			MaxIterations:       maxIteration,
			Middlewares:         middlewares,
			Handlers:            handlers,
			GenModelInput:       typedGenModelInput[M],
			ModelFailoverConfig: modelFailoverConfig,
		})
		if err != nil {
			return nil, err
		}

		it, err := assertAgentTool(adk.NewTypedAgentTool[M](ctx, generalAgent))
		if err != nil {
			return nil, err
		}
		t.subAgents[generalAgent.Name(ctx)] = it
		t.subAgentSlice = append(t.subAgentSlice, generalAgent)
	}

	for _, a := range subAgents {
		name := a.Name(ctx)
		it, err := assertAgentTool(adk.NewTypedAgentTool[M](ctx, a))
		if err != nil {
			return nil, err
		}
		t.subAgents[name] = it
	}

	return t, nil
}

func newTaskTool(
	ctx context.Context,
	taskToolDescriptionGenerator func(ctx context.Context, subAgents []adk.Agent) (string, error),
	subAgents []adk.Agent,

	withoutGeneralSubAgent bool,
	// Model is the chat model. Tools are configured via model.WithTools call option.
	Model model.BaseChatModel,
	Instruction string,
	ToolsConfig adk.ToolsConfig,
	MaxIteration int,
	middlewares []adk.AgentMiddleware,
	handlers []adk.ChatModelAgentMiddleware,
	modelFailoverConfig *adk.ModelFailoverConfig,
) (tool.InvokableTool, error) {
	return typedNewTaskTool[*schema.Message](ctx, taskToolDescriptionGenerator, subAgents, withoutGeneralSubAgent, Model, Instruction, ToolsConfig, MaxIteration, middlewares, handlers, modelFailoverConfig)
}

type typedTaskTool[M messageType] struct {
	subAgents     map[string]tool.InvokableTool
	subAgentSlice []adk.TypedAgent[M]
	descGen       func(ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error)
}

type taskTool = typedTaskTool[*schema.Message]

func (t *typedTaskTool[M]) Info(ctx context.Context) (*schema.ToolInfo, error) {
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

func (t *typedTaskTool[M]) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
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

func typedDefaultTaskToolDescription[M messageType](ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error) {
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

func defaultTaskToolDescription(ctx context.Context, subAgents []adk.Agent) (string, error) {
	return typedDefaultTaskToolDescription[*schema.Message](ctx, subAgents)
}
