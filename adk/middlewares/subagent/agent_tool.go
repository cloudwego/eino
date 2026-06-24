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
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
)

const (
	agentToolName = "agent"
	// TaskTypeSubagent is the backgroundtask Task.Type tag for sub-agent tasks
	// launched by the agent tool, letting a shared Manager distinguish them from
	// shell tasks.
	TaskTypeSubagent = "subagent"
)

// agentInput is the agent tool's input when no Manager is configured: spawn a
// sub-agent synchronously in the foreground.
type agentInput struct {
	SubagentType string `json:"subagent_type" jsonschema:"required" jsonschema_description:"The type of specialized agent to use for this task"`
	Prompt       string `json:"prompt" jsonschema:"required" jsonschema_description:"The task for the agent to perform"`
	Description  string `json:"description" jsonschema:"required" jsonschema_description:"A short (3-5 word) description of the task"`
}

// agentManagedInput is the agent tool's input when a Manager is configured: it adds
// run_in_background so the model can spawn the sub-agent in the background.
type agentManagedInput struct {
	agentInput
	RunInBackground bool `json:"run_in_background,omitempty" jsonschema_description:"Set to true to run this agent in the background. You will be notified when it completes."`
}

// newAgentTool builds the foreground-only agent tool (no Manager): it invokes the
// agent-as-tool adapter directly, forwarding opts so event forwarding, session
// sharing and interrupt/resume behave exactly as a normal agent-as-tool call.
func newAgentTool(subAgents map[string]tool.InvokableTool, name, desc string) (tool.BaseTool, error) {
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentInput, opts ...tool.Option) (string, error) {
			a, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}
			return a.InvokableRun(ctx, params, opts...)
		})
}

// newManagedAgentTool builds the Manager-backed agent tool. It wraps the same
// agent-as-tool invocation in a managed task, so foreground behavior is identical
// and only lifecycle/background switching is layered on top.
func newManagedAgentTool(mgr *backgroundtask.Manager, subAgents map[string]tool.InvokableTool, name, desc string) (tool.BaseTool, error) {
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentManagedInput, opts ...tool.Option) (string, error) {
			a, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}

			result, err := mgr.Run(ctx, &backgroundtask.RunInput{
				Description:     in.Description,
				Type:            TaskTypeSubagent,
				ToolUseID:       compose.GetToolCallID(ctx),
				RunInBackground: in.RunInBackground,
			}, func(workCtx context.Context) (string, error) {
				return a.InvokableRun(workCtx, params, opts...)
			})
			if err != nil {
				return "", err
			}

			switch result.Status {
			case backgroundtask.StatusCompleted:
				return result.Result, nil
			case backgroundtask.StatusRunning:
				msg := fmt.Sprintf("Agent running in background with ID: %s. You will be notified when it completes.", result.ID)
				if result.OutputFile != "" {
					msg += fmt.Sprintf(" Output is being written to: %s.", result.OutputFile)
				}
				msg += " Use task_output with this ID to check status or retrieve the result."
				return msg, nil
			case backgroundtask.StatusFailed:
				return "", fmt.Errorf("subagent %q task %q (%s) failed: %s",
					in.SubagentType, result.ID, in.Description, result.Error)
			case backgroundtask.StatusCanceled:
				return "", fmt.Errorf("subagent %q task %q (%s) was canceled",
					in.SubagentType, result.ID, in.Description)
			default:
				return result.Result, nil
			}
		})
}

// resolveSubAgent looks up the agent-as-tool adapter for subagentType and builds
// the marshaled request for it. If prompt is empty, description is used as the
// task request.
func resolveSubAgent(subAgents map[string]tool.InvokableTool, subagentType, prompt, description string) (tool.InvokableTool, string, error) {
	a, ok := subAgents[subagentType]
	if !ok {
		return nil, "", fmt.Errorf("subagent type %q not found", subagentType)
	}
	if prompt == "" {
		prompt = description
	}
	params, err := sonic.MarshalString(map[string]string{"request": prompt})
	if err != nil {
		return nil, "", err
	}
	return a, params, nil
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
