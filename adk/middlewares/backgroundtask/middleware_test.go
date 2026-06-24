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

package backgroundtask

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	bgtask "github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func closeWithTimeout(m *bgtask.Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	_ = m.Close(ctx)
}

func runWork(m *bgtask.Manager, description string, background bool, work bgtask.WorkFunc) (*bgtask.Task, error) {
	return m.Run(context.Background(), &bgtask.RunInput{
		Description:     description,
		RunInBackground: background,
	}, work)
}

func completedWork(result string) bgtask.WorkFunc {
	return func(ctx context.Context) (string, error) {
		return result, nil
	}
}

func blockingWork() bgtask.WorkFunc {
	return func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}
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
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	tools := injectedTools(t, mgr)
	require.Len(t, tools, 2)

	// Both control tools present.
	findTool(t, tools, taskOutputToolName)
	findTool(t, tools, taskStopToolName)
}

func TestMiddleware_ToolConfig_NameOverrideAndDisable(t *testing.T) {
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
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
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
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
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	mw, err := New(context.Background(), &Config{Manager: mgr})
	require.NoError(t, err)
	_, runCtx, err := mw.BeforeAgent(context.Background(), &adk.ChatModelAgentContext[*schema.Message]{Instruction: "base"})
	require.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "base")
	assert.Contains(t, runCtx.Instruction, "task_output")
}

func TestTaskOutputTool(t *testing.T) {
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	result, err := runWork(mgr, "test task", false, completedWork("task result"))
	require.NoError(t, err)
	require.Equal(t, bgtask.StatusCompleted, result.Status)

	tl := findTool(t, injectedTools(t, mgr), taskOutputToolName)
	output, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, result.ID))
	require.NoError(t, err)
	assert.Contains(t, output, "test task")
	assert.Contains(t, output, "task result")
	assert.Contains(t, output, "completed")
}

func TestTaskOutputTool_NotFound(t *testing.T) {
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	tl := findTool(t, injectedTools(t, mgr), taskOutputToolName)
	result, err := tl.InvokableRun(context.Background(), `{"task_id":"nonexistent"}`)
	require.NoError(t, err)
	assert.Contains(t, result, "not found")
}

func TestTaskOutputTool_NonBlockingRunningThenTerminal(t *testing.T) {
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "running task", true, blockingWork())
	require.NoError(t, err)

	tl := findTool(t, injectedTools(t, mgr), taskOutputToolName)
	out, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s","block":false}`, runResult.ID))
	require.NoError(t, err)
	assert.Contains(t, out, "running")

	require.NoError(t, mgr.Cancel(runResult.ID))
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	task, done := mgr.Wait(waitCtx, runResult.ID)
	require.True(t, done)
	require.NotNil(t, task)

	_, err = tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s","block":false}`, runResult.ID))
	require.NoError(t, err)
}

func TestTaskStopTool(t *testing.T) {
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "running task", true, blockingWork())
	require.NoError(t, err)

	tl := findTool(t, injectedTools(t, mgr), taskStopToolName)
	result, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, runResult.ID))
	require.NoError(t, err)
	assert.Contains(t, result, "Successfully stopped")

	task, ok := mgr.Get(runResult.ID)
	require.True(t, ok)
	assert.Equal(t, bgtask.StatusCanceled, task.Status)
}

func TestTaskStopTool_AlreadyDone(t *testing.T) {
	mgr := bgtask.New(context.Background(), &bgtask.Config{})
	defer closeWithTimeout(mgr)

	runResult, err := runWork(mgr, "done task", false, completedWork("done"))
	require.NoError(t, err)
	require.Equal(t, bgtask.StatusCompleted, runResult.Status)

	tl := findTool(t, injectedTools(t, mgr), taskStopToolName)
	result, err := tl.InvokableRun(context.Background(), fmt.Sprintf(`{"task_id":"%s"}`, runResult.ID))
	require.NoError(t, err)
	assert.Contains(t, result, "Failed to stop")
}
