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
	"errors"
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	durablesubagent "github.com/cloudwego/eino/adk/backgroundtask/subagent"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

const (
	agentToolName    = "agent"
	TaskTypeSubagent = "subagent"
)

type agentInput struct {
	SubagentType string `json:"subagent_type" jsonschema:"required" jsonschema_description:"The type of specialized agent to use for this task"`
	Prompt       string `json:"prompt" jsonschema:"required" jsonschema_description:"The task for the agent to perform"`
	Description  string `json:"description" jsonschema:"required" jsonschema_description:"A short (3-5 word) description of the task"`
}

type agentManagedInput struct {
	agentInput
	RunInBackground bool `json:"run_in_background,omitempty" jsonschema_description:"Set to true to run this agent in the background. You will be notified when it completes."`
}

func newAgentTool(subAgents map[string]tool.InvokableTool, name, desc string) (tool.BaseTool, error) {
	return utils.InferOptionableTool(name, desc,
		func(ctx context.Context, in agentInput, opts ...tool.Option) (string, error) {
			agent, params, err := resolveSubAgent(subAgents, in.SubagentType, in.Prompt, in.Description)
			if err != nil {
				return "", err
			}
			return agent.InvokableRun(ctx, params, opts...)
		})
}

func newDurableAgentTool[M adk.MessageType](
	ctx context.Context,
	config *TypedBackgroundConfig[M],
	agents []adk.TypedAgent[M],
	name, desc string,
) (tool.BaseTool, error) {
	registry := durablesubagent.NewAgentRegistry[M]()
	for _, agent := range agents {
		resumable, ok := agent.(adk.TypedResumableAgent[M])
		if !ok {
			return nil, fmt.Errorf("subagent: agent %q is not resumable", agent.Name(ctx))
		}
		if err := registry.Register(config.AgentRefs[agent.Name(ctx)], resumable); err != nil {
			return nil, err
		}
	}
	executor := &durablesubagent.Executor[M]{
		Agents: registry, CheckPointStore: config.CheckPointStore, SessionStore: config.SessionStore,
	}
	if existing, ok := config.Manager.Executors().Resolve(durablesubagent.ExecutorKey); ok {
		typed, typeOK := existing.(*durablesubagent.Executor[M])
		if !typeOK {
			return nil, errors.New("subagent: registered durable executor has incompatible message type")
		}
		for _, agent := range agents {
			if err := typed.Agents.Register(
				config.AgentRefs[agent.Name(ctx)],
				agent.(adk.TypedResumableAgent[M]),
			); err != nil {
				return nil, err
			}
		}
	} else if err := config.Manager.Executors().Register(executor); err != nil {
		return nil, err
	}

	return utils.InferTool(name, desc, func(callCtx context.Context, in agentManagedInput) (string, error) {
		ref, ok := config.AgentRefs[in.SubagentType]
		if !ok {
			return "", fmt.Errorf("subagent %q not found", in.SubagentType)
		}
		sessionID, err := config.SessionID(callCtx)
		if err != nil || sessionID == "" {
			if err == nil {
				err = errors.New("empty parent session id")
			}
			return "", fmt.Errorf("subagent: resolve parent session: %w", err)
		}
		prompt := in.Prompt
		if prompt == "" {
			prompt = in.Description
		}
		task, err := durablesubagent.Submit(callCtx, config.Manager, &durablesubagent.SubmitRequest{
			Agent: ref, Prompt: prompt, Description: in.Description,
			SessionID: sessionID,
		})
		if err != nil {
			return "", err
		}
		execute := func(executionContext context.Context) error {
			return config.Manager.Execute(executionContext, task.Spec.ID)
		}
		if in.RunInBackground {
			go func() { _ = execute(context.Background()) }()
			return fmt.Sprintf("Agent running in background with ID: %s. You will be notified when it completes.", task.Spec.ID), nil
		}
		if err = execute(callCtx); err != nil {
			return "", err
		}
		task, err = config.Manager.GetTask(callCtx, task.Spec.ID)
		if err != nil {
			return "", err
		}
		return formatDurableTaskResult(in.SubagentType, task)
	})
}

func formatDurableTaskResult(agentType string, task *backgroundtask.Task) (string, error) {
	switch task.Status {
	case backgroundtask.StatusCompleted:
		if task.Result == nil || task.Result.Data == nil {
			return "", errors.New("subagent: completed task has no result")
		}
		return string(task.Result.Data), nil
	case backgroundtask.StatusWaitingInput:
		return fmt.Sprintf("Agent task %s requires input. Use task_output to inspect the request.", task.Spec.ID), nil
	case backgroundtask.StatusSuspended, backgroundtask.StatusPending, backgroundtask.StatusRunning, backgroundtask.StatusCanceling:
		return fmt.Sprintf("Agent task %s is %s.", task.Spec.ID, task.Status), nil
	case backgroundtask.StatusCanceled:
		return "", fmt.Errorf("subagent %q task %q was canceled", agentType, task.Spec.ID)
	case backgroundtask.StatusFailed:
		return "", fmt.Errorf("subagent %q task %q failed: %s", agentType, task.Spec.ID, task.Result.Error)
	default:
		return "", fmt.Errorf("subagent %q task %q has unknown status %q", agentType, task.Spec.ID, task.Status)
	}
}

func resolveSubAgent(subAgents map[string]tool.InvokableTool, subagentType, prompt, description string) (tool.InvokableTool, string, error) {
	agent, ok := subAgents[subagentType]
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
	return agent, params, nil
}

// defaultAgentToolDescription returns the agent tool description. The available
// agent types are injected as a mid-conversation system message; the
// {background_prompt} placeholder is filled by the middleware.
func defaultAgentToolDescription[M adk.MessageType](context.Context, []adk.TypedAgent[M]) (string, error) {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: agentToolDescription,
		Chinese: agentToolDescriptionChinese,
	}), nil
}
