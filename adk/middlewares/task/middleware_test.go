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

package task

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	taskcore "github.com/cloudwego/eino/adk/task"
	bgtask "github.com/cloudwego/eino/adk/task/background"
	backgroundlocal "github.com/cloudwego/eino/adk/task/local"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *bgtask.Config,
) *bgtask.Manager {
	t.Helper()
	if config == nil {
		config = &bgtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *bgtask.TaskSnapshot) error { return nil }
	}
	manager, err := bgtask.New(ctx, config)
	require.NoError(t, err)
	return manager
}

func TestTypedConfigUsesExecutorKeyedProgressReaders_BitsUT(t *testing.T) {
	configType := reflect.TypeOf(TypedConfig[*schema.Message]{})
	_, hasReaders := configType.FieldByName("ProgressReadersByExecutorKey")
	_, hasFallback := configType.FieldByName("ReadTaskProgress")
	require.True(t, hasReaders)
	require.False(t, hasFallback)
}

func newBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *bgtask.Config,
) *bgtask.Manager {
	if config == nil {
		config = &bgtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	manager := mustNewBackgroundManager(t, ctx, config)
	return manager
}

func closeWithTimeout(m *bgtask.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = m.Close(ctx)
}

func runWork(
	m *bgtask.Manager,
	description string,
	background bool,
	work backgroundlocal.WorkFunc,
) (*backgroundlocal.RunResult, error) {
	runner, err := backgroundlocal.New(&backgroundlocal.Config{
		Manager: m,
	})
	if err != nil {
		return nil, err
	}
	return runner.Run(context.Background(), &backgroundlocal.Input{
		Description:     description,
		RunInBackground: background,
	}, work)
}

func requireRunTask(
	t testing.TB,
	result *backgroundlocal.RunResult,
) *bgtask.TaskSnapshot {
	t.Helper()
	task, ok := result.Task()
	require.True(t, ok)
	require.NotNil(t, task)
	return task
}

func waitUntilTerminal(
	t *testing.T,
	ctx context.Context,
	manager *bgtask.Manager,
	taskID string,
) *bgtask.TaskSnapshot {
	t.Helper()
	task, err := manager.Get(ctx, taskID)
	require.NoError(t, err)
	for task.Status != bgtask.StatusCompleted &&
		task.Status != bgtask.StatusFailed &&
		task.Status != bgtask.StatusCanceled {
		task, err = manager.WaitForTaskVersion(ctx, &bgtask.WaitForTaskVersionRequest{
			TaskID: taskID, AfterVersion: task.Version,
		})
		require.NoError(t, err)
	}
	return task
}

func waitUntilRunning(
	t *testing.T,
	ctx context.Context,
	manager *bgtask.Manager,
	taskID string,
) *bgtask.TaskSnapshot {
	t.Helper()
	task, err := manager.Get(ctx, taskID)
	require.NoError(t, err)
	for task.Status == bgtask.StatusPending {
		task, err = manager.WaitForTaskVersion(ctx, &bgtask.WaitForTaskVersionRequest{
			TaskID: taskID, AfterVersion: task.Version,
		})
		require.NoError(t, err)
	}
	return task
}

func createAndStartTask(
	t *testing.T,
	store *bgtask.InMemoryStore,
	spec bgtask.Spec,
	policy bgtask.LeaseExpiryPolicy,
) *bgtask.TaskSnapshot {
	t.Helper()
	task, err := store.Create(context.Background(), &bgtask.CreateTaskRequest{
		Spec: spec, LeaseExpiryPolicy: policy,
	})
	require.NoError(t, err)
	task, err = store.Start(context.Background(), &bgtask.StartTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: task.Version,
	})
	require.NoError(t, err)
	return task
}

func completedWork(result string) backgroundlocal.WorkFunc {
	return func(ctx context.Context, _ bgtask.ExecutionRuntime) (string, error) {
		return result, nil
	}
}

func blockingWork() backgroundlocal.WorkFunc {
	return func(ctx context.Context, _ bgtask.ExecutionRuntime) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
}

type staleFirstGetStore struct {
	bgtask.LifecycleStore
	mu    sync.Mutex
	first bool
}

func (s *staleFirstGetStore) Get(ctx context.Context, taskID string) (*bgtask.TaskSnapshot, error) {
	task, err := s.LifecycleStore.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.first {
		s.first = false
		stale := *task
		stale.Status = bgtask.StatusRunning
		stale.ResultData = nil
		stale.ResultError = ""
		return &stale, nil
	}
	return task, nil
}

func (s *staleFirstGetStore) Receive(
	ctx context.Context,
	req *bgtask.ReceiveNotificationsRequest,
) (*bgtask.ReceiveNotificationsResult, error) {
	return s.LifecycleStore.(bgtask.NotificationOutbox).Receive(ctx, req)
}

func (s *staleFirstGetStore) Ack(ctx context.Context, receipt bgtask.NotificationReceipt) error {
	return s.LifecycleStore.(bgtask.NotificationOutbox).Ack(ctx, receipt)
}

// findTool returns the named tool from a tool list.
func findTool(t *testing.T, tools []tool.BaseTool, name string) tool.InvokableTool {
	t.Helper()
	for _, bt := range tools {
		info, err := bt.Info(context.Background())
		require.NoError(t, err)
		if info.Name == name {
			it, ok := bt.(tool.InvokableTool)
			require.True(t, ok)
			return it
		}
	}
	t.Fatalf("tool %q not found", name)
	return nil
}

func injectedTools(t *testing.T, m *bgtask.Manager) []tool.BaseTool {
	t.Helper()
	mw, err := New(context.Background(), &Config{Manager: m})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	return runCtx.Tools
}

func TestNew_NilManager(t *testing.T) {
	_, err := New(context.Background(), nil)
	assert.Error(t, err)
}

func TestMiddleware_InjectsControlTools(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	tools := injectedTools(t, mgr)
	require.Len(t, tools, 2)

	// Both control tools present.
	findTool(t, tools, taskOutputToolName)
	findTool(t, tools, taskStopToolName)
}

func TestMiddleware_ToolConfig_NameOverrideAndDisable(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	customDesc := "custom output desc"
	mw, err := New(context.Background(), &Config{
		Manager:              mgr,
		TaskOutputToolConfig: &ToolConfig{Name: "get_output", Desc: &customDesc},
		TaskStopToolConfig:   &ToolConfig{Disable: true},
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)

	// task_stop disabled → only the renamed task_output remains.
	require.Len(t, runCtx.Tools, 1)
	info, err := runCtx.Tools[0].Info(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "get_output", info.Name)
	assert.Equal(t, customDesc, info.Desc)
}

func TestMiddleware_ToolConfig_DisableBoth(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{
		Manager:              mgr,
		TaskOutputToolConfig: &ToolConfig{Disable: true},
		TaskStopToolConfig:   &ToolConfig{Disable: true},
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	assert.Empty(t, runCtx.Tools)
}

func TestMiddleware_InjectsInstruction(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{Manager: mgr})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{Instruction: "base"})
	require.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "base")
	assert.Contains(t, runCtx.Instruction, "task_output")
	assert.Contains(t, runCtx.Instruction, "task_stop")
	assert.Contains(t, runCtx.Instruction, "you will be notified when they complete")
}

// TestMiddleware_InstructionUsesRenamedTool verifies the instruction names the
// tool as registered: a renamed task_output is referenced by its new name, and
// the default name no longer appears.
func TestMiddleware_InstructionUsesRenamedTool(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{
		Manager:              mgr,
		TaskOutputToolConfig: &ToolConfig{Name: "get_task_result"},
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "get_task_result")
	assert.NotContains(t, runCtx.Instruction, "task_output")
	assert.Contains(t, runCtx.Instruction, "task_stop")
}

// TestMiddleware_InstructionOmitsDisabledTool verifies a disabled tool's sentence
// is dropped so the model is never told to call a tool that was not registered.
func TestMiddleware_InstructionOmitsDisabledTool(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{
		Manager:            mgr,
		TaskStopToolConfig: &ToolConfig{Disable: true},
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{})
	require.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "task_output")
	assert.NotContains(t, runCtx.Instruction, "task_stop")
}

// TestMiddleware_InstructionEmptyWhenAllDisabled verifies a fully-disabled
// middleware injects neither tools nor a task instruction.
func TestMiddleware_InstructionEmptyWhenAllDisabled(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{
		Manager:              mgr,
		TaskOutputToolConfig: &ToolConfig{Disable: true},
		TaskStopToolConfig:   &ToolConfig{Disable: true},
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{Instruction: "base"})
	require.NoError(t, err)
	assert.Equal(t, "base", runCtx.Instruction)
	assert.Empty(t, runCtx.Tools)
}

// TestMiddleware_CustomSystemPrompt verifies CustomSystemPrompt fully replaces the
// built-in instruction and receives the default control-tool names.
func TestMiddleware_CustomSystemPrompt(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	var got *SystemPromptInput
	mw, err := New(context.Background(), &Config{
		Manager: mgr,
		CustomSystemPrompt: func(_ context.Context, in *SystemPromptInput) string {
			got = in
			return "CUSTOM " + in.DefaultTaskOutputToolName
		},
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{Instruction: "base"})
	require.NoError(t, err)

	require.NotNil(t, got)
	assert.Equal(t, taskOutputToolName, got.DefaultTaskOutputToolName)
	assert.Equal(t, taskStopToolName, got.DefaultTaskStopToolName)
	assert.Equal(t, "base\nCUSTOM task_output", runCtx.Instruction)
	assert.NotContains(t, runCtx.Instruction, "you will be notified when they complete")
}

// TestMiddleware_CustomSystemPromptEmptyInjectsNothing verifies returning "" from
// CustomSystemPrompt appends no instruction.
func TestMiddleware_CustomSystemPromptEmptyInjectsNothing(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{
		Manager:            mgr,
		CustomSystemPrompt: func(context.Context, *SystemPromptInput) string { return "" },
	})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{Instruction: "base"})
	require.NoError(t, err)
	assert.Equal(t, "base", runCtx.Instruction)
}

func TestTaskOutputTool(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "test task", true, completedWork("task result"))
	require.NoError(t, err)
	result := requireRunTask(t, runResult)
	result = waitUntilTerminal(t, context.Background(), mgr, result.Spec.ID)
	require.Equal(t, bgtask.StatusCompleted, result.Status)

	tl := findTool(t, injectedTools(t, mgr), taskOutputToolName)
	output, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, result.Spec.ID))
	require.NoError(t, err)
	assert.Contains(t, output, "test task")
	assert.Contains(t, output, "completed")
	assert.Contains(t, output, "Result: task result")
}

func TestTaskOutputTool_NotFound(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	tl := findTool(t, injectedTools(t, mgr), taskOutputToolName)
	result, err := tl.InvokableRun(context.Background(), `{"task_id":"nonexistent"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "not found")
}

func TestTaskOutputTool_NonBlockingRunningThenTerminal(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "running task", true, blockingWork())
	require.NoError(t, err)
	task := requireRunTask(t, runResult)
	task = waitUntilRunning(t, context.Background(), mgr, task.Spec.ID)

	tl := findTool(t, injectedTools(t, mgr), taskOutputToolName)
	out, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s","block":false}`, task.Spec.ID))
	require.NoError(t, err)
	assert.Contains(t, out, "running")

	_, err = mgr.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	task = waitUntilTerminal(t, waitCtx, mgr, task.Spec.ID)
	require.NotNil(t, task)

	_, err = tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s","block":false}`, task.Spec.ID))
	require.NoError(t, err)
}

func TestTaskOutputNonBlockingReturnsCurrentSnapshot(t *testing.T) {
	store := bgtask.NewInMemoryStore(nil)
	submitter := newBackgroundManager(t, context.Background(), &bgtask.Config{Tasks: store})
	runResult, err := runWork(submitter, "racing task", true, completedWork("done"))
	require.NoError(t, err)
	task := requireRunTask(t, runResult)
	task = waitUntilTerminal(t, context.Background(), submitter, task.Spec.ID)
	closeWithTimeout(submitter)

	racingStore := &staleFirstGetStore{LifecycleStore: store, first: true}
	reader := newBackgroundManager(t, context.Background(), &bgtask.Config{
		Tasks: racingStore, TaskEvents: store,
	})
	defer closeWithTimeout(reader)
	outputTool := findTool(t, injectedTools(t, reader), taskOutputToolName)
	output, err := outputTool.InvokableRun(context.Background(), fmt.Sprintf(
		`{"task_id":%q,"block":false}`, task.Spec.ID,
	))
	require.NoError(t, err)

	assert.Contains(t, output, "Status: running")
	assert.NotContains(t, output, "Result:")
	assert.NotContains(t, output, "Error:")
}

func TestTaskStopTool(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "running task", true, blockingWork())
	require.NoError(t, err)
	task := requireRunTask(t, runResult)

	tl := findTool(t, injectedTools(t, mgr), taskStopToolName)
	result, err := tl.InvokableRun(
		context.Background(),
		fmt.Sprintf(`{"task_id":"%s","reason":"no longer needed"}`, task.Spec.ID),
	)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Successfully stopped task: %s", task.Spec.ID), result)

	task = waitUntilTerminal(t, context.Background(), mgr, task.Spec.ID)
	assert.Equal(t, bgtask.StatusCanceled, task.Status)
	assert.Equal(t, "no longer needed", task.CancelReason)
	assert.Equal(t, "no longer needed", task.ResultError)
}

func TestTaskStopTool_DurableRequestedAndCanceledText(t *testing.T) {
	store := bgtask.NewInMemoryStore(nil)
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{Tasks: store})
	defer closeWithTimeout(mgr)
	tl := findTool(t, injectedTools(t, mgr), taskStopToolName)

	running := createAndStartTask(
		t, store, bgtask.Spec{
			ID: "durable-running", ExecutorKey: "test",
		},
		bgtask.LeaseExpiryRetry,
	)
	result, err := tl.InvokableRun(
		context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, running.Spec.ID),
	)
	require.NoError(t, err)
	assert.Equal(t, "Stop requested for task durable-running", result)

	failOnExpiry := createAndStartTask(
		t, store, bgtask.Spec{
			ID: "durable-fail-on-expiry", ExecutorKey: "test",
		},
		bgtask.LeaseExpiryFail,
	)
	result, err = tl.InvokableRun(
		context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, failOnExpiry.Spec.ID),
	)
	require.NoError(t, err)
	assert.Equal(t, "Stop requested for task durable-fail-on-expiry", result)

	pending, err := store.Create(context.Background(), &bgtask.CreateTaskRequest{
		Spec: bgtask.Spec{
			ID: "durable-pending", ExecutorKey: "test",
		},
		LeaseExpiryPolicy: bgtask.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	result, err = tl.InvokableRun(
		context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, pending.Spec.ID),
	)
	require.NoError(t, err)
	assert.Equal(t, "Successfully stopped task: durable-pending", result)
}

func TestTaskStopTool_AlreadyDone(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "done task", false, completedWork("done"))
	require.NoError(t, err)
	outcome, ok := runResult.Foreground()
	require.True(t, ok)
	require.Equal(t, taskcore.OutcomeCompleted, outcome.Status)

	tl := findTool(t, injectedTools(t, mgr), taskStopToolName)
	result, err := tl.InvokableRun(
		context.Background(),
		fmt.Sprintf(`{"task_id":"%s"}`, runResult.ID()),
	)
	require.NoError(t, err)
	assert.Contains(t, result, "Failed to stop")
}

func TestControlToolsUsePossessionOfTaskID(t *testing.T) {
	mgr := newBackgroundManager(t, context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)
	runResult, err := runWork(mgr, "secret task", true, blockingWork())
	require.NoError(t, err)
	task := requireRunTask(t, runResult)
	task = waitUntilRunning(t, context.Background(), mgr, task.Spec.ID)

	tools := injectedTools(t, mgr)
	output := findTool(t, tools, taskOutputToolName)
	stop := findTool(t, tools, taskStopToolName)
	otherContext := context.WithValue(context.Background(), struct{}{}, "other-agent")
	response, err := output.InvokableRun(
		otherContext, fmt.Sprintf(`{"task_id":%q,"block":false}`, task.Spec.ID),
	)
	require.NoError(t, err)
	assert.Contains(t, response, "Status: running")
	response, err = stop.InvokableRun(
		otherContext, fmt.Sprintf(`{"task_id":%q}`, task.Spec.ID),
	)
	require.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Successfully stopped task: %s", task.Spec.ID), response)
}

func TestFormatTaskStableNonTerminalStates(t *testing.T) {
	for _, test := range []struct {
		status bgtask.Status
		want   string
	}{
		{bgtask.StatusPending, "Task ID: task_secret\nDescription: work\nStatus: pending"},
		{bgtask.StatusWaitingInput, "Task ID: task_secret\nDescription: work\nStatus: waiting_input"},
		{bgtask.StatusSuspended, "Task ID: task_secret\nDescription: work\nStatus: suspended"},
	} {
		t.Run(string(test.status), func(t *testing.T) {
			assert.Equal(t, test.want, formatTask(&bgtask.TaskSnapshot{
				Spec:   bgtask.Spec{ID: "task_secret", Description: "work"},
				Status: test.status,
			}))
		})
	}
}

func TestFormatTaskOutputTranscriptSemantics(t *testing.T) {
	reliableSubagent := &bgtask.TaskSnapshot{
		Spec: bgtask.Spec{
			ID: "subagent_secret", Description: "research", Kind: "subagent",
			OutputFile: "/tasks/events.output",
		},
		Status: bgtask.StatusCompleted, ResultData: []byte("authoritative answer"),
	}
	rendered := formatTask(reliableSubagent)
	assert.Contains(t, rendered, "Event transcript: /tasks/events.output")
	assert.NotContains(t, rendered, "JSONL")
	assert.Contains(t, rendered, "Result: authoritative answer")

	incomplete := *reliableSubagent
	incomplete.OutputFileErr = "write failed"
	rendered = formatTask(&incomplete)
	assert.Contains(t, rendered, "Result: authoritative answer")
	assert.Contains(t, rendered, "incomplete — a write failed: write failed")

	noFile := *reliableSubagent
	noFile.Spec.OutputFile = ""
	rendered = formatTask(&noFile)
	assert.Contains(t, rendered, "Result: authoritative answer")
}

func TestResolveDurableTaskAddsProgressWithoutReplacingTerminalResult(t *testing.T) {
	block := false
	task := &bgtask.TaskSnapshot{
		Spec: bgtask.Spec{
			ID: "subagent_secret", Description: "research", Kind: "subagent",
		},
		Status: bgtask.StatusCompleted, ResultData: []byte("authoritative answer"),
	}
	result, err := resolveDurableTask(
		context.Background(), nil, task,
		taskOutputInput{TaskID: task.Spec.ID, Block: &block},
		func(_ context.Context, got *bgtask.TaskSnapshot) (string, error) {
			assert.Same(t, task, got)
			return "Transcript:\nworker: progress", nil
		},
	)
	require.NoError(t, err)
	assert.Contains(t, result, "Transcript:\nworker: progress")
	assert.Contains(t, result, "Result: authoritative answer")

	result, err = resolveDurableTask(
		context.Background(), nil, task,
		taskOutputInput{TaskID: task.Spec.ID, Block: &block},
		func(context.Context, *bgtask.TaskSnapshot) (string, error) {
			return "", errors.New("session unavailable")
		},
	)
	require.NoError(t, err)
	assert.Contains(t, result, "Transcript unavailable: session unavailable")
	assert.Contains(t, result, "Result: authoritative answer")
}

type waitErrorStore struct {
	bgtask.LifecycleStore
	err error
}

func (s waitErrorStore) WaitForTaskVersion(context.Context, *bgtask.WaitForTaskVersionRequest) (*bgtask.TaskSnapshot, error) {
	return nil, s.err
}

func TestResolveDurableTaskBlockingBoundaries(t *testing.T) {
	store := bgtask.NewInMemoryStore(nil)
	pending, err := store.Create(context.Background(), &bgtask.CreateTaskRequest{
		Spec:              bgtask.Spec{ID: "pending", ExecutorKey: "test"},
		LeaseExpiryPolicy: bgtask.LeaseExpiryRetry,
	})
	require.NoError(t, err)
	manager := newBackgroundManager(t, context.Background(), &bgtask.Config{Tasks: store})

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	result, err := resolveDurableTask(
		canceledCtx, manager, pending,
		taskOutputInput{TaskID: pending.Spec.ID, Timeout: maxTaskOutputTimeoutMs + 1},
		nil,
	)
	require.NoError(t, err)
	require.Contains(t, result, "Status: pending")

	wantErr := errors.New("wait failed")
	failingManager := newBackgroundManager(t, context.Background(), &bgtask.Config{
		Tasks: waitErrorStore{LifecycleStore: store, err: wantErr}, TaskEvents: store,
	})
	_, err = resolveDurableTask(
		context.Background(), failingManager, pending,
		taskOutputInput{TaskID: pending.Spec.ID, Timeout: 1},
		nil,
	)
	require.ErrorIs(t, err, wantErr)
}

type progressReaderStub struct {
	value string
}

func (r progressReaderStub) ReadProgress(context.Context, *bgtask.TaskSnapshot) (string, error) {
	return r.value, nil
}

func TestResolveDurableTaskSelectsProgressReaderByExecutorKey(t *testing.T) {
	block := false
	task := &bgtask.TaskSnapshot{
		Spec: bgtask.Spec{
			ID: "managed", ExecutorKey: "eino.dev/background-tool",
			Description: "managed operation",
		},
		Status: bgtask.StatusRunning,
	}
	result, err := resolveDurableTaskWithReaders(
		context.Background(), nil, task,
		taskOutputInput{TaskID: task.Spec.ID, Block: &block},
		map[string]ProgressReader{
			task.Spec.ExecutorKey: progressReaderStub{value: "Recent progress:\nhello"},
		},
	)
	require.NoError(t, err)
	require.Contains(t, result, "Recent progress:\nhello")
}
