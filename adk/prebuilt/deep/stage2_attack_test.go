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

package deep

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	filesystemmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	subagentmw "github.com/cloudwego/eino/adk/middlewares/subagent"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
	backgroundlocal "github.com/cloudwego/eino/adk/task/local"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type attackDeepResumableAgent struct {
	name string
}

func (a *attackDeepResumableAgent) Name(context.Context) string {
	return a.name
}

func (*attackDeepResumableAgent) Description(context.Context) string {
	return "attack worker"
}

func (a *attackDeepResumableAgent) Run(
	_ context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("done", nil),
		nil,
		schema.Assistant,
		a.name,
	))
	generator.Close()
	return iter
}

func (a *attackDeepResumableAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func attackDeepToolInfo(t *testing.T, target tool.BaseTool) *schema.ToolInfo {
	t.Helper()
	info, err := target.Info(context.Background())
	require.NoError(t, err)
	require.NotNil(t, info)
	return info
}

// TestAttack_GeneratedGeneralLaunchScopeExcludesManagedCapabilities verifies
// that generated children receive shared controls without inheriting top-level
// launch capabilities. Inheriting execute or task would let a child expand the
// configured background-work scope.
func TestAttack_GeneratedGeneralLaunchScopeExcludesManagedCapabilities(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name      string
		configure func(*TaskConfig)
	}{
		{
			name: "local shell",
			configure: func(config *TaskConfig) {
				config.LocalShell = &LocalShellConfig{Shell: &deepMockShell{}}
			},
		},
		{
			name: "recoverable shell",
			configure: func(config *TaskConfig) {
				config.RecoverableShell = &RecoverableShellConfig{
					Shell: &deepRecoverableShellStub{},
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager := mustNewBackgroundManager(t, ctx, nil)
			t.Cleanup(func() {
				closeDeepManager(t, manager)
			})
			tasks := &TaskConfig{
				Manager: manager,
				SubAgents: &DurableSubAgentConfig{
					Runtime: mustDeepController(t, manager),
				},
			}
			testCase.configure(tasks)

			handlers, err := buildGeneratedGeneralAgentMiddlewares(
				ctx,
				&Config{WithoutWriteTodos: true, Tasks: tasks},
			)
			require.NoError(t, err)
			tools := toolsFromDeepHandlers(t, handlers)
			require.ElementsMatch(
				t,
				[]string{"task_output", "task_stop"},
				mapKeys(tools),
			)
			require.NotContains(t, tools, filesystemmw.ToolNameExecute)
			require.NotContains(t, tools, taskToolName)
		})
	}
}

// TestAttack_GeneratedGeneralControlsUseRuntimeManager proves that a generated
// child's task_output tool reads work from the Controller's Manager. Binding a
// fresh Manager would expose controls that cannot observe top-level task IDs.
func TestAttack_GeneratedGeneralControlsUseRuntimeManager(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	manager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, manager)
	})
	controller := mustDeepController(t, manager)
	handlers, err := buildGeneratedGeneralAgentMiddlewares(ctx, &Config{
		WithoutWriteTodos: true,
		Tasks: &TaskConfig{SubAgents: &DurableSubAgentConfig{
			Runtime: controller,
		}},
	})
	require.NoError(t, err)
	tools := toolsFromDeepHandlers(t, handlers)

	runner, err := backgroundlocal.New(&backgroundlocal.Config{Manager: manager})
	require.NoError(t, err)
	runResult, err := runner.Run(
		ctx,
		&backgroundlocal.Input{
			Description:     "shared manager task",
			RunInBackground: true,
		},
		func(context.Context, background.ExecutionRuntime) (string, error) {
			return "shared-manager-result", nil
		},
	)
	require.NoError(t, err)
	snapshot, ok := runResult.Task()
	require.True(t, ok)
	require.Eventually(t, func() bool {
		current, getErr := manager.Get(ctx, snapshot.Spec.ID)
		return getErr == nil && current.Status == background.StatusCompleted
	}, time.Second, time.Millisecond)

	outputTool, ok := tools["task_output"].(tool.InvokableTool)
	require.True(t, ok)
	output, err := outputTool.InvokableRun(
		ctx,
		fmt.Sprintf(`{"task_id":%q,"block":false}`, snapshot.Spec.ID),
	)
	require.NoError(t, err)
	lines := strings.Split(output, "\n")
	require.Len(t, lines, 5)
	fields := make(map[string]string, len(lines))
	for _, line := range lines {
		key, value, found := strings.Cut(line, ": ")
		require.Truef(t, found, "malformed task output line %q", line)
		require.NotContains(t, fields, key, "duplicate task output field %q", key)
		fields[key] = value
	}
	elapsedText, found := fields["Elapsed"]
	require.True(t, found, "task output must include Elapsed")
	delete(fields, "Elapsed")
	require.Equal(t, map[string]string{
		"Task ID":     snapshot.Spec.ID,
		"Description": "shared manager task",
		"Status":      "completed",
		"Result":      "shared-manager-result",
	}, fields)
	elapsed, err := time.ParseDuration(elapsedText)
	require.NoError(t, err)
	require.GreaterOrEqual(t, elapsed, time.Duration(0))
	require.LessOrEqual(t, elapsed, 5*time.Second)

	started := make(chan struct{})
	blockedResult, err := runner.Run(
		ctx,
		&backgroundlocal.Input{
			Description:     "stoppable task",
			RunInBackground: true,
		},
		func(runCtx context.Context, _ background.ExecutionRuntime) (string, error) {
			close(started)
			<-runCtx.Done()
			return "", runCtx.Err()
		},
	)
	require.NoError(t, err)
	blocked, ok := blockedResult.Task()
	require.True(t, ok)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for task start: %v", ctx.Err())
	}
	stopTool, ok := tools["task_stop"].(tool.InvokableTool)
	require.True(t, ok)
	stopped, err := stopTool.InvokableRun(
		ctx,
		fmt.Sprintf(
			`{"task_id":%q,"reason":"stop from generated general"}`,
			blocked.Spec.ID,
		),
	)
	require.NoError(t, err)
	require.Equal(t, "Successfully stopped task: "+blocked.Spec.ID, stopped)
	require.Eventually(t, func() bool {
		current, getErr := manager.Get(ctx, blocked.Spec.ID)
		return getErr == nil &&
			current.Status == background.StatusCanceled &&
			current.ResultError == "stop from generated general"
	}, time.Second, time.Millisecond)
}

// TestAttack_PlainAndManagedShellKeepDistinctWireBehavior verifies that plain
// shell remains a direct command tool while managed shell advertises task
// controls and still preserves ordinary foreground execution.
func TestAttack_PlainAndManagedShellKeepDistinctWireBehavior(t *testing.T) {
	ctx := context.Background()
	plainHandlers, err := buildTypedBuiltinAgentMiddlewares(
		ctx,
		&Config{WithoutWriteTodos: true, Shell: &deepMockShell{}},
		nil,
	)
	require.NoError(t, err)
	plain := toolsFromDeepHandlers(t, plainHandlers)[filesystemmw.ToolNameExecute]
	requireRunInBackgroundField(t, ctx, plain, false)
	plainResult, err := plain.(tool.InvokableTool).InvokableRun(
		ctx,
		`{"command":"plain","run_in_background":true}`,
	)
	require.NoError(t, err)
	require.Equal(t, "ok", plainResult)

	manager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, manager)
	})
	managedHandlers, err := buildTypedBuiltinAgentMiddlewares(
		ctx,
		&Config{WithoutWriteTodos: true},
		&TaskConfig{
			Manager:    manager,
			LocalShell: &LocalShellConfig{Shell: &deepMockShell{}},
		},
	)
	require.NoError(t, err)
	managed := toolsFromDeepHandlers(t, managedHandlers)[filesystemmw.ToolNameExecute]
	requireRunInBackgroundField(t, ctx, managed, true)
	managedResult, err := managed.(tool.InvokableTool).InvokableRun(
		ctx,
		`{"command":"managed"}`,
	)
	require.NoError(t, err)
	require.Equal(t, "ok", managedResult)
}

// TestAttack_ManagedShellWireDescriptionMatchesSchema verifies that the
// model-facing description explains every managed-only field. A schema and
// description mismatch would cause models to invent unsupported control flow.
func TestAttack_ManagedShellWireDescriptionMatchesSchema(t *testing.T) {
	ctx := context.Background()
	manager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, manager)
	})
	handlers, err := buildTypedBuiltinAgentMiddlewares(
		ctx,
		&Config{WithoutWriteTodos: true},
		&TaskConfig{
			Manager:    manager,
			LocalShell: &LocalShellConfig{Shell: &deepMockShell{}},
		},
	)
	require.NoError(t, err)
	execute := toolsFromDeepHandlers(t, handlers)[filesystemmw.ToolNameExecute]
	info := attackDeepToolInfo(t, execute)
	params, err := info.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	for _, field := range []string{"command", "run_in_background", "timeout"} {
		_, ok := params.Properties.Get(field)
		require.True(t, ok, "managed execute schema must contain %q", field)
		require.Contains(t, info.Desc, field)
	}
	require.Contains(t, info.Desc, "task_output")
	require.Contains(t, info.Desc, "task_id")
}

// TestAttack_TaskToolWireDescriptionTracksManagedMode verifies that adding
// durable task execution changes both the task tool schema and its description.
// Advertising background semantics in only one of them would create an invalid
// wire contract for model calls.
func TestAttack_TaskToolWireDescriptionTracksManagedMode(t *testing.T) {
	ctx := context.Background()
	agent := &attackDeepResumableAgent{name: "worker"}

	plainMiddleware, err := subagentmw.New(ctx, &subagentmw.Config{
		SubAgents: []adk.Agent{agent},
		ToolName:  taskToolName,
	})
	require.NoError(t, err)
	plainTools := toolsFromDeepHandlers(
		t,
		[]adk.ChatModelAgentMiddleware{plainMiddleware},
	)
	plainInfo := attackDeepToolInfo(t, plainTools[taskToolName])
	plainSchema, err := plainInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	_, hasBackground := plainSchema.Properties.Get("run_in_background")
	require.False(t, hasBackground)
	require.NotContains(t, plainInfo.Desc, "run_in_background")

	manager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, manager)
	})
	controller := mustDeepController(t, manager)
	managedMiddleware, err := subagentmw.New(ctx, &subagentmw.Config{
		SubAgents: []adk.Agent{agent},
		ToolName:  taskToolName,
		Tasks: &subagentmw.TaskConfig{
			Durable: &subagentmw.DurableTaskConfig{Runtime: controller},
		},
	})
	require.NoError(t, err)
	managedTools := toolsFromDeepHandlers(
		t,
		[]adk.ChatModelAgentMiddleware{managedMiddleware},
	)
	managedInfo := attackDeepToolInfo(t, managedTools[taskToolName])
	managedSchema, err := managedInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	for _, field := range []string{
		"subagent_type", "prompt", "description",
		"run_in_background", "child_session_id",
	} {
		_, ok := managedSchema.Properties.Get(field)
		require.True(t, ok, "managed task schema must contain %q", field)
	}
	for _, term := range []string{
		"run_in_background", "child_session_id", "task_output",
	} {
		require.Contains(t, managedInfo.Desc, term)
	}
}

// TestAttack_DeepMapsDurableSubAgentConfiguration verifies that Deep maps
// durable sub-agent presentation and reconstruction hooks without replacing
// their identity.
func TestAttack_DeepMapsDurableSubAgentConfiguration(t *testing.T) {
	ctx := context.Background()
	manager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, manager)
	})
	controller := mustDeepController(t, manager)
	factoryCalled := false
	factory := func() ([]adk.AgentRunOption, error) {
		factoryCalled = true
		return []adk.AgentRunOption{adk.WithTimelineEvents()}, nil
	}
	format := func(
		_ context.Context,
		agentName string,
		message *schema.Message,
	) (string, error) {
		return strings.ToUpper(agentName + ":" + message.Content), nil
	}
	timeout := 0
	cfg := &Config{Tasks: &TaskConfig{
		Manager:              manager,
		ForegroundTimeoutMs:  &timeout,
		ShouldAutoBackground: func(context.Context, *foreground.CandidateInfo) bool { return true },
		TranscriptFormat:     format,
		SubAgents: &DurableSubAgentConfig{
			Runtime: controller,
			RunOptionsFactories: map[string]durablesubagent.RunOptionsFactory{
				"worker": factory,
			},
		},
	}}

	tasks := deepSubagentBackground(cfg)
	require.Same(t, controller, tasks.Durable.Runtime)
	options, err := tasks.Durable.RunOptionsFactories["worker"]()
	require.NoError(t, err)
	require.True(t, factoryCalled)
	require.Len(t, options, 1)
	formatted, err := tasks.TranscriptFormat(
		ctx,
		"worker",
		schema.AssistantMessage("done", nil),
	)
	require.NoError(t, err)
	require.Equal(t, "WORKER:DONE", formatted)
}

// TestAttack_NilZeroAndRuntimeOverridesFailClosed verifies nil configuration,
// missing runtime dependencies, zero timeout, derived Manager, and conflicting
// Manager overrides. These edges must not silently create split task domains.
func TestAttack_NilZeroAndRuntimeOverridesFailClosed(t *testing.T) {
	ctx := context.Background()
	require.ErrorContains(t, validateTypedConfig[*schema.Message](nil), "config is required")
	require.ErrorContains(
		t,
		validateTypedConfig(&Config{Tasks: &TaskConfig{}}),
		"Manager is required",
	)

	manager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, manager)
	})
	require.ErrorContains(t, validateTypedConfig(&Config{Tasks: &TaskConfig{
		Manager:   manager,
		SubAgents: &DurableSubAgentConfig{},
	}}), "Controller is required")

	controller := mustDeepController(t, manager)
	derived := &TaskConfig{
		SubAgents: &DurableSubAgentConfig{Runtime: controller},
	}
	require.NoError(t, validateTypedConfig(&Config{Tasks: derived}))
	require.Same(t, manager, deepBackgroundManager(derived))

	zero := 0
	withZeroTimeout := &TaskConfig{
		Manager:             manager,
		LocalShell:          &LocalShellConfig{Shell: &deepMockShell{}},
		ForegroundTimeoutMs: &zero,
	}
	require.NoError(t, validateTypedConfig(&Config{Tasks: withZeroTimeout}))
	runner, err := deepLocalShellRunner(withZeroTimeout)
	require.NoError(t, err)
	require.NotNil(t, runner)

	otherManager := mustNewBackgroundManager(t, ctx, nil)
	t.Cleanup(func() {
		closeDeepManager(t, otherManager)
	})
	derived.Manager = otherManager
	require.ErrorContains(
		t,
		validateTypedConfig(&Config{Tasks: derived}),
		"must share the same Manager",
	)
}

func mapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	return keys
}

var _ filesystem.Shell = (*deepMockShell)(nil)
var _ adk.ResumableAgent = (*attackDeepResumableAgent)(nil)
