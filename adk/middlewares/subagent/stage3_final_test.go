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
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type nonResumableTestAgent struct {
	name string
}

type controllerToolResumeFixture struct {
	rootCtx     context.Context
	manager     *background.Manager
	node        *compose.ToolsNode
	input       *schema.Message
	signal      *core.InterruptSignal
	targetID    string
	targetState runtimeAgentInterruptState
	agent       *checkpointInterruptAgent
}

func (a *nonResumableTestAgent) Name(context.Context) string {
	return a.name
}

func (*nonResumableTestAgent) Description(context.Context) string {
	return "one-shot worker"
}

func (*nonResumableTestAgent) Run(
	context.Context,
	*adk.AgentInput,
	...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Close()
	return iter
}

func newControllerToolResumeFixture(
	t *testing.T,
) *controllerToolResumeFixture {
	t.Helper()
	rootCtx, cancel := context.WithTimeout(runnerEnvironmentContext(t), 2*time.Second)
	t.Cleanup(cancel)
	manager := newTestManager(t, rootCtx)
	t.Cleanup(func() {
		require.NoError(t, manager.Close(context.Background()))
	})
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	controller, err := durablesubagent.NewController(
		&durablesubagent.ControllerConfig[*schema.Message]{
			Manager: manager,
			Barrier: runtimeBarrierFunc(func(
				context.Context,
				*durablesubagent.CompletionContext[*schema.Message],
			) (durablesubagent.CompletionAction, error) {
				return durablesubagent.CompletionComplete, nil
			}),
			InputsToAgentInput: func(
				context.Context,
				[]*task.InputRecord,
			) (*adk.AgentInput, error) {
				return &adk.AgentInput{
					Messages: []*schema.Message{schema.UserMessage("resume")},
				}, nil
			},
			SessionStore: sessionStore, CheckPointStore: sessionStore,
		},
	)
	require.NoError(t, err)
	agent := &checkpointInterruptAgent{name: "worker"}
	require.NoError(t, controller.RegisterAgent(
		agent.name,
		&durablesubagent.AgentRegistration[*schema.Message]{Agent: agent},
	))
	agentTool, err := newControllerAgentTool(
		rootCtx,
		controller,
		[]adk.TypedAgent[*schema.Message]{agent},
		"delegate",
		"delegate work",
	)
	require.NoError(t, err)
	node, err := compose.NewToolNode(rootCtx, &compose.ToolsNodeConfig{
		Tools: []tool.BaseTool{agentTool}, ExecuteSequentially: true,
	})
	require.NoError(t, err)
	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call-1",
		Function: schema.FunctionCall{
			Name:      "delegate",
			Arguments: `{"subagent_type":"worker","prompt":"work","description":"approval"}`,
		},
	}})
	output, err := node.Invoke(rootCtx, input)
	require.Nil(t, output)
	var signal *core.InterruptSignal
	require.ErrorAs(t, err, &signal)

	var targetID string
	var targetState runtimeAgentInterruptState
	_, states := core.SignalToPersistenceMaps(signal)
	for id, state := range states {
		if candidate, ok := state.State.(runtimeAgentInterruptState); ok {
			targetID = id
			targetState = candidate
			break
		}
	}
	require.NotEmpty(t, targetID)
	require.NotEmpty(t, targetState.TaskID)
	require.NotEmpty(t, targetState.ChildSessionID)
	require.Equal(t, "parent-session:call-1", targetState.InvocationID)
	require.Equal(t, int64(1), targetState.NextResumeSequence)

	return &controllerToolResumeFixture{
		rootCtx: rootCtx, manager: manager, node: node, input: input,
		signal: signal, targetID: targetID, targetState: targetState, agent: agent,
	}
}

func (f *controllerToolResumeFixture) resumeContext(
	resumeTarget string,
	data any,
	mutate func(map[string]core.InterruptState),
) context.Context {
	addresses, states := core.SignalToPersistenceMaps(f.signal)
	if mutate != nil {
		mutate(states)
	}
	ctx := compose.ResumeWithData(f.rootCtx, resumeTarget, data)
	return core.PopulateInterruptState(ctx, addresses, states)
}

func TestNewDurableAgentTool(t *testing.T) {
	const (
		toolName = "delegate"
		toolDesc = "Delegate work to a durable sub-agent."
	)

	t.Run("runtime is required", func(t *testing.T) {
		got, err := newDurableAgentTool[*schema.Message](
			context.Background(),
			&TypedDurableTaskConfig[*schema.Message]{},
			nil,
			toolName,
			toolDesc,
		)
		require.Nil(t, got)
		require.EqualError(t, err, "subagent: durable Controller is required")
	})

	t.Run("all agents must be resumable", func(t *testing.T) {
		ctx := context.Background()
		manager := newTestManager(t, ctx)
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		config := durableBackground(t, manager).Durable

		got, err := newDurableAgentTool[*schema.Message](
			ctx,
			config,
			[]adk.Agent{&nonResumableTestAgent{name: "one-shot"}},
			toolName,
			toolDesc,
		)
		require.Nil(t, got)
		require.EqualError(t, err, `subagent: agent "one-shot" is not resumable`)
	})

	t.Run("registration errors are preserved", func(t *testing.T) {
		ctx := context.Background()
		manager := newTestManager(t, ctx)
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		config := durableBackground(t, manager).Durable
		agent := &mockAgent{name: "worker", desc: "done"}
		require.NoError(t, config.Runtime.RegisterAgent(
			agent.name,
			&durablesubagent.AgentRegistration[*schema.Message]{Agent: agent},
		))

		got, err := newDurableAgentTool[*schema.Message](
			ctx,
			config,
			[]adk.Agent{agent},
			toolName,
			toolDesc,
		)
		require.Nil(t, got)
		require.ErrorIs(t, err, background.ErrAlreadyExists)
		require.EqualError(t, err, "task/background: task already exists")
	})

	t.Run("run options factory errors reach the caller", func(t *testing.T) {
		ctx := runnerEnvironmentContext(t)
		manager := newTestManager(t, ctx)
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		factoryErr := errors.New("load durable run options")
		config := durableBackground(t, manager).Durable
		config.RunOptionsFactories = map[string]durablesubagent.RunOptionsFactory{
			"worker": func() ([]adk.AgentRunOption, error) {
				return nil, factoryErr
			},
		}
		agent := &mockAgent{name: "worker", desc: "done"}
		got, err := newDurableAgentTool[*schema.Message](
			ctx,
			config,
			[]adk.Agent{agent},
			toolName,
			toolDesc,
		)
		require.NoError(t, err)

		result, err := got.(tool.InvokableTool).InvokableRun(
			ctx,
			`{"subagent_type":"worker","prompt":"work","description":"test"}`,
		)
		require.Empty(t, result)
		require.ErrorIs(t, err, factoryErr)
		require.EqualError(
			t,
			err,
			"[LocalFunc] failed to invoke tool, toolName=delegate, err=load durable run options",
		)
	})

	t.Run("schema exposes durable controls exactly", func(t *testing.T) {
		ctx := context.Background()
		manager := newTestManager(t, ctx)
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		config := durableBackground(t, manager).Durable
		agent := &mockAgent{name: "worker", desc: "done"}
		got, err := newDurableAgentTool[*schema.Message](
			ctx,
			config,
			[]adk.Agent{agent},
			toolName,
			toolDesc,
		)
		require.NoError(t, err)

		info, err := got.Info(ctx)
		require.NoError(t, err)
		require.Equal(t, toolName, info.Name)
		require.Equal(t, toolDesc, info.Desc)
		jsonSchema, err := info.ParamsOneOf.ToJSONSchema()
		require.NoError(t, err)
		require.Equal(t, "object", jsonSchema.Type)
		require.ElementsMatch(
			t,
			[]string{"subagent_type", "prompt", "description"},
			jsonSchema.Required,
		)
		require.Equal(t, 5, jsonSchema.Properties.Len())
		for name, description := range map[string]string{
			"subagent_type":     "The type of specialized agent to use for this task",
			"prompt":            "The task for the agent to perform",
			"description":       "A short (3-5 word) description of the task",
			"run_in_background": "Set to true to run this agent in the background. You will be notified when it completes.",
			"child_session_id":  "Continue a previous child session by ID and inherit its history. Omit to create a new child session.",
		} {
			property, ok := jsonSchema.Properties.Get(name)
			require.True(t, ok, "missing schema property %q", name)
			require.Equal(t, description, property.Description)
		}
	})
}

func TestNewControllerAgentTool(t *testing.T) {
	t.Run("rejects interrupted call without runtime state", func(t *testing.T) {
		fixture := newControllerToolResumeFixture(t)
		ctx := fixture.resumeContext(
			fixture.targetID,
			"approved",
			func(states map[string]core.InterruptState) {
				state := states[fixture.targetID]
				state.State = nil
				states[fixture.targetID] = state
			},
		)

		output, err := fixture.node.Invoke(ctx, fixture.input)
		require.Nil(t, output)
		require.EqualError(
			t,
			err,
			"failed to invoke tool[name:delegate id:call-1]: "+
				"[LocalFunc] failed to invoke tool, toolName=delegate, "+
				"err=subagent: runtime interrupt state is unavailable",
		)
	})

	t.Run("rejects interrupted call that is not a resume target", func(t *testing.T) {
		fixture := newControllerToolResumeFixture(t)
		ctx := fixture.resumeContext("different-target", "approved", nil)

		output, err := fixture.node.Invoke(ctx, fixture.input)
		require.Nil(t, output)
		require.EqualError(
			t,
			err,
			"failed to invoke tool[name:delegate id:call-1]: "+
				"[LocalFunc] failed to invoke tool, toolName=delegate, "+
				"err=subagent: runtime resume target is unavailable",
		)
	})

	t.Run("legacy state defaults invocation and resume sequence", func(t *testing.T) {
		fixture := newControllerToolResumeFixture(t)
		legacy := fixture.targetState
		legacy.InvocationID = ""
		legacy.NextResumeSequence = 0
		ctx := fixture.resumeContext(
			fixture.targetID,
			"approved",
			func(states map[string]core.InterruptState) {
				state := states[fixture.targetID]
				state.State = legacy
				states[fixture.targetID] = state
			},
		)

		output, err := fixture.node.Invoke(ctx, fixture.input)
		require.NoError(t, err)
		require.Len(t, output, 1)
		require.Equal(t, schema.Tool, output[0].Role)
		require.Equal(t, "call-1", output[0].ToolCallID)
		var result durableAgentToolResult
		require.NoError(t, sonic.UnmarshalString(output[0].Content, &result))
		require.Equal(t, fixture.targetState.TaskID, result.TaskID)
		require.Equal(t, fixture.targetState.ChildSessionID, result.ChildSessionID)
		require.Equal(t, background.StatusCompleted, result.Status)
		require.Equal(t, "approved", result.Result)
		require.Equal(t, int64(1), fixture.agent.runCalls)
		require.Equal(t, int64(1), fixture.agent.resumeCalls)

		inputs, err := fixture.manager.ListInputs(
			ctx,
			&task.ListInputsRequest{TaskID: fixture.targetState.TaskID},
		)
		require.NoError(t, err)
		require.Len(t, inputs.Inputs, 2)
		require.Equal(
			t,
			"resume:"+uuid.NewSHA1(
				uuid.Nil,
				[]byte(fmt.Sprintf("%s:%d", fixture.targetState.TaskID, 1)),
			).String(),
			inputs.Inputs[1].EventID,
		)
		require.Equal(t, durablesubagent.ResumeInputKind, inputs.Inputs[1].Kind)
		require.Equal(t, int64(2), inputs.ConsumedCursor)
		require.Equal(t, int64(2), inputs.LatestSequence)
	})
}

func TestFormatRuntimeHandle(t *testing.T) {
	t.Run("invalid handles fail closed", func(t *testing.T) {
		for _, handle := range []*durablesubagent.Handle{
			nil,
			{},
		} {
			result, err := formatRuntimeHandle(handle, background.StatusPending)
			require.Empty(t, result)
			require.EqualError(t, err, "subagent: runtime returned an invalid handle")
		}
	})

	t.Run("valid handle preserves durable identity and status", func(t *testing.T) {
		ctx := context.Background()
		manager := newTestManager(t, ctx)
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
		controller, err := durablesubagent.NewController(
			&durablesubagent.ControllerConfig[*schema.Message]{
				Manager: manager,
				Barrier: runtimeBarrierFunc(func(
					context.Context,
					*durablesubagent.CompletionContext[*schema.Message],
				) (durablesubagent.CompletionAction, error) {
					return durablesubagent.CompletionComplete, nil
				}),
				InputsToAgentInput: func(
					context.Context,
					[]*task.InputRecord,
				) (*adk.AgentInput, error) {
					return &adk.AgentInput{
						Messages: []*schema.Message{schema.UserMessage("event")},
					}, nil
				},
				SessionStore: sessionStore, CheckPointStore: sessionStore,
			},
		)
		require.NoError(t, err)
		agent := &mockAgent{name: "worker", desc: "done"}
		require.NoError(t, controller.RegisterAgent(
			agent.name,
			&durablesubagent.AgentRegistration[*schema.Message]{Agent: agent},
		))
		handle, err := controller.Start(ctx, &durablesubagent.StartRequest[*schema.Message]{
			InvocationID: "format-runtime-handle", ParentSessionID: "parent",
			AgentName: agent.name, StartMode: task.StartModeForeground,
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("work")},
			},
		})
		require.NoError(t, err)
		_, err = controller.Wait(ctx, handle.ID())
		require.NoError(t, err)

		result, err := formatRuntimeHandle(handle, background.StatusPending)
		require.NoError(t, err)
		var decoded durableAgentToolResult
		require.NoError(t, sonic.UnmarshalString(result, &decoded))
		require.Equal(t, durableAgentToolResult{
			TaskID: handle.ID(), ChildSessionID: handle.ChildSessionID(),
			Status: background.StatusPending,
		}, decoded)
	})
}

func TestNewTypedUserInput(t *testing.T) {
	t.Run("message", func(t *testing.T) {
		input := newTypedUserInput[*schema.Message]("review changes", true)
		require.Equal(t, &adk.AgentInput{
			Messages:        []*schema.Message{schema.UserMessage("review changes")},
			EnableStreaming: true,
		}, input)
	})

	t.Run("agentic message", func(t *testing.T) {
		input := newTypedUserInput[*schema.AgenticMessage]("review changes", false)
		require.Equal(t, &adk.TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("review changes"),
			},
			EnableStreaming: false,
		}, input)
	})
}
