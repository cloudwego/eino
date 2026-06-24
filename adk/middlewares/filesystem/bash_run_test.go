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

package filesystem

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func intPtr(v int) *int { return &v }

func waitAllTasks(t *testing.T, mgr *backgroundtask.Manager) {
	t.Helper()
	require.Eventually(t, func() bool {
		for _, task := range mgr.List() {
			if task.Status == backgroundtask.StatusRunning {
				return false
			}
		}
		return true
	}, time.Second, 10*time.Millisecond)
}

// A filesystem.Backend is a direct backgroundtask.OutputStore (no adapter): the
// Manager persists task output through it, and the file is readable back.
func TestBackendAsOutputStore_PersistsTaskOutput(t *testing.T) {
	backend := setupTestBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{
		OutputStore: backend, // drop-in: Backend satisfies OutputStore
		OutputDir:   "/tasks",
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell:   &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}},
		Manager: mgr,
	})
	require.NoError(t, err)

	_, err = invokeTool(t, tools[0], `{"command":"echo hi"}`)
	require.NoError(t, err)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	path := tasks[0].OutputFile
	require.NotEmpty(t, path)

	got, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	assert.Equal(t, "the output", got.Content)
}

func TestIsAutoBackgroundAllowed(t *testing.T) {
	cases := []struct {
		command string
		want    bool
	}{
		{"sleep", false},              // bare sleep is blocked
		{"sleep 5", true},             // sleep with args allowed (matched exactly)
		{"  sleep  ", false},          // surrounding space trimmed
		{"npm run build", true},       // normal command
		{"echo hi && sleep 5", true},  // sleep not in first segment
		{"sleep && echo done", false}, // bare sleep as first segment
		{"", true},                    // empty → allowed
	}
	for _, c := range cases {
		assert.Equalf(t, c.want, IsAutoBackgroundAllowed(c.command), "command=%q", c.command)
	}
}

// slowShell is a Shell whose Execute blocks for delay (honoring ctx cancellation)
// before returning out.
type slowShell struct {
	delay time.Duration
	out   string
}

func (s *slowShell) Execute(ctx context.Context, _ *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	select {
	case <-time.After(s.delay):
		return &filesystem.ExecuteResponse{Output: s.out}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestManagedExecuteTool_Foreground(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell:   &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}},
		Manager: mgr,
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	result, err := invokeTool(t, tools[0], `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	// The run is tracked by the Manager and tagged as a bash task.
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "echo hi", tasks[0].Description)
	assert.Equal(t, ExecuteTaskType, tasks[0].Type)
}

func TestManagedExecuteTool_Background(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{
		OutputStore: setupTestBackend(), // so a background launch reports an output path
		OutputDir:   "/tasks",
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell:   &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "done"}},
		Manager: mgr,
	})
	require.NoError(t, err)

	result, err := invokeTool(t, tools[0], `{"command":"sleep 1","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")

	waitAllTasks(t, mgr)
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.True(t, tasks[0].RunInBackground)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)

	// The background-launch message reports the (reserved) output-file path so the
	// agent can read it once the task completes.
	assert.Contains(t, result, tasks[0].OutputFile)
	assert.NotEmpty(t, tasks[0].OutputFile)
}

// A foreground command that outlives its timeout is moved to the background
// (kept running) when the Manager's ShouldAutoBackground hook permits it.
func TestManagedExecuteTool_TimeoutMovesToBackground(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{
		ForegroundTimeoutMs:  intPtr(0),
		ShouldAutoBackground: func(context.Context, *backgroundtask.Task) bool { return true },
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell:   &slowShell{delay: 200 * time.Millisecond, out: "slow done"},
		Manager: mgr,
	})
	require.NoError(t, err)

	// timeout=50ms < 200ms command → moved to background.
	result, err := invokeTool(t, tools[0], `{"command":"sleep","timeout":50}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")

	waitAllTasks(t, mgr)
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Equal(t, "slow done", tasks[0].Result)
}

// Without a ShouldAutoBackground hook, a command that outlives its timeout is
// stopped and reported as timed out.
func TestManagedExecuteTool_TimeoutKills(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{ForegroundTimeoutMs: intPtr(0)})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell:   &slowShell{delay: time.Second, out: "never"},
		Manager: mgr,
	})
	require.NoError(t, err)

	_, err = invokeTool(t, tools[0], `{"command":"sleep","timeout":50}`)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "timed out")

	waitAllTasks(t, mgr)
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusFailed, tasks[0].Status)
}

// With a Manager, the execute tool schema gains run_in_background and timeout fields.
// With a StreamingShell backend the managed execute tool is a StreamableTool that
// streams foreground output live while still tracking the run in the Manager.
func TestManagedExecuteTool_StreamingForeground(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(mgr, nil, &mockStreamingShellMultiChunk{}, "", "")
	require.NoError(t, err)

	st, ok := executeTool.(tool.StreamableTool)
	require.True(t, ok, "managed execute tool with StreamingShell must be a StreamableTool")

	sr, err := st.StreamableRun(context.Background(), `{"command":"echo hi"}`)
	require.NoError(t, err)
	got := drainToolStream(t, sr)
	assert.Contains(t, got, "chunk1")
	assert.Contains(t, got, "chunk3")

	waitAllTasks(t, mgr)
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Equal(t, ExecuteTaskType, tasks[0].Type)
	// The streamed chunks are also the persisted result.
	assert.Contains(t, tasks[0].Result, "chunk1")
	assert.Contains(t, tasks[0].Result, "chunk3")
}

// An explicit background launch on a streaming managed tool emits only the
// background notice on the caller's stream; the output lands in the task result.
func TestManagedExecuteTool_StreamingExplicitBackground(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{
		OutputStore: setupTestBackend(),
		OutputDir:   "/tasks",
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(mgr, nil, &mockStreamingShellMultiChunk{}, "", "")
	require.NoError(t, err)
	st := executeTool.(tool.StreamableTool)

	sr, err := st.StreamableRun(context.Background(), `{"command":"echo hi","run_in_background":true}`)
	require.NoError(t, err)
	got := drainToolStream(t, sr)
	assert.Contains(t, got, "is running in the background")
	assert.NotContains(t, got, "moved to the background")
	assert.NotContains(t, got, "chunk1")

	waitAllTasks(t, mgr)
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.True(t, tasks[0].RunInBackground)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Contains(t, tasks[0].Result, "chunk1")
}

// drainToolStream reads a tool's string stream to EOF and returns the joined text.
func drainToolStream(t *testing.T, sr *schema.StreamReader[string]) string {
	t.Helper()
	defer sr.Close()
	var b strings.Builder
	for {
		chunk, err := sr.Recv()
		if err == io.EOF {
			return b.String()
		}
		require.NoError(t, err)
		b.WriteString(chunk)
	}
}

func TestManagedExecuteTool_Schema(t *testing.T) {
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(mgr, &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}}, nil, "", "")
	require.NoError(t, err)

	info, err := executeTool.Info(context.Background())
	require.NoError(t, err)
	js, err := info.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	assert.Equal(t, 3, js.Properties.Len())
	_, ok := js.Properties.Get("command")
	assert.True(t, ok)
	_, ok = js.Properties.Get("run_in_background")
	assert.True(t, ok)
	_, ok = js.Properties.Get("timeout")
	assert.True(t, ok)
}

// Without a Manager, the execute tool is command-only and untracked.
func TestExecuteTool_NoManager_NotTracked(t *testing.T) {
	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell: &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}},
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	result, err := invokeTool(t, tools[0], `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}
