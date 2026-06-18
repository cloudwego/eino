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

package subagent

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const (
	agentToolName      = "agent"
	taskOutputToolName = "task_output"
	taskStopToolName   = "task_stop"
)

// agentTool implements tool.InvokableTool for spawning sub-agents.
//
// When mgr is non-nil, agent runs are delegated to TaskMgr.Run which handles
// foreground/background/auto-background switching and task lifecycle tracking.
// When mgr is nil, agent runs are executed directly in the foreground with no tracking.
type typedAgentTool[M adk.MessageType] struct {
	name      string
	subAgents map[string]tool.InvokableTool // for non-TaskMgr foreground path
	// subAgentSlice preserves the original order for description generation.
	subAgentSlice         []adk.TypedAgent[M]
	descGen               func(ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error)
	mgr                   *TypedTaskMgr[M] // nil = foreground only, no task tracking
	enableRunInBackground bool
}

// agentInput is the unified input struct for the agent tool.
// A single struct is used regardless of enableRunInBackground; when background
// is not enabled, the RunInBackground field is simply ignored (or triggers a
// system-reminder if the model hallucinated it as true).
type agentInput struct {
	SubagentType    string `json:"subagent_type"`
	Prompt          string `json:"prompt"`
	Description     string `json:"description"`
	RunInBackground *bool  `json:"run_in_background,omitempty"`
}

func (t *typedAgentTool[M]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	desc, err := t.descGen(ctx, t.subAgentSlice)
	if err != nil {
		return nil, err
	}

	params := map[string]*schema.ParameterInfo{
		"subagent_type": {
			Type: schema.String,
			Desc: "The type of specialized agent to use for this task",
		},
		"prompt": {
			Type: schema.String,
			Desc: "The task for the agent to perform",
		},
		"description": {
			Type: schema.String,
			Desc: "A short (3-5 word) description of the task",
		},
	}

	if t.enableRunInBackground {
		params["run_in_background"] = &schema.ParameterInfo{
			Type: schema.Boolean,
			Desc: "Set to true to run this agent in the background. You will be notified when it completes.",
		}
	}

	return &schema.ToolInfo{
		Name:        t.name,
		Desc:        desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(params),
	}, nil
}

func (t *typedAgentTool[M]) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	input := &agentInput{}
	if err := sonic.UnmarshalString(argumentsInJSON, input); err != nil {
		return "", fmt.Errorf("failed to unmarshal agent tool input: %w", err)
	}

	// Backward compatibility: when prompt is empty but description has value,
	// treat description as the prompt. This handles fewshot hallucination where
	// the model may still use the old field layout.
	prompt := input.Prompt
	if prompt == "" {
		prompt = input.Description
	}
	description := input.Description

	// Handle run_in_background when not enabled: return a system-reminder.
	if input.RunInBackground != nil && *input.RunInBackground && !t.enableRunInBackground {
		return internal.SelectPrompt(internal.I18nPrompts{
			English: "<system-reminder>Background execution is not available. " +
				"The run_in_background parameter is not supported in the current configuration. " +
				"Please re-invoke the agent tool without run_in_background.</system-reminder>",
			Chinese: "<system-reminder>后台执行不可用。当前配置不支持 run_in_background 参数。" +
				"请在不带 run_in_background 的情况下重新调用 agent 工具。</system-reminder>",
		}), nil
	}

	a, ok := t.subAgents[input.SubagentType]
	if !ok {
		return "", fmt.Errorf("subagent type %q not found", input.SubagentType)
	}

	params, err := sonic.MarshalString(map[string]string{
		"request": prompt,
	})
	if err != nil {
		return "", err
	}

	// No TaskMgr: pure foreground, no task tracking.
	if t.mgr == nil {
		return a.InvokableRun(ctx, params, opts...)
	}

	// With TaskMgr: delegate execution and lifecycle management.
	background := input.RunInBackground != nil && *input.RunInBackground
	result, err := t.mgr.Run(ctx, &RunInput{
		SubagentType:    input.SubagentType,
		Prompt:          prompt,
		Description:     description,
		RunInBackground: background,
	})
	if err != nil {
		return "", err
	}

	switch result.Status {
	case StatusCompleted:
		return result.Result, nil
	case StatusRunning:
		return fmt.Sprintf("Task %s launched in background: %s", result.TaskID, description), nil
	case StatusFailed:
		return "", fmt.Errorf("subagent %q task %q (%s) failed: %s",
			input.SubagentType, result.TaskID, description, result.Error)
	default:
		return result.Result, nil
	}
}

// defaultAgentToolDescription generates the agent tool description with sub-agent list.
func defaultAgentToolDescription[M adk.MessageType](ctx context.Context, subAgents []adk.TypedAgent[M]) (string, error) {
	subAgentsDescBuilder := strings.Builder{}
	for _, a := range subAgents {
		name := a.Name(ctx)
		desc := a.Description(ctx)
		_, _ = fmt.Fprintf(&subAgentsDescBuilder, "- %s: %s\n", name, desc)
	}
	toolDesc := internal.SelectPrompt(internal.I18nPrompts{
		English: agentToolDescription,
		Chinese: agentToolDescriptionChinese,
	})
	return pyfmt.Fmt(toolDesc, map[string]any{
		"other_agents": subAgentsDescBuilder.String(),
	})
}
