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
	"errors"
	"io"
	"strings"
	"sync/atomic"
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

// findExecuteTool returns the execute tool from a tool set (which, when a Backend
// is configured, also contains the file tools).
func findExecuteTool(t *testing.T, tools []tool.BaseTool) tool.BaseTool {
	t.Helper()
	for _, to := range tools {
		info, err := to.Info(context.Background())
		require.NoError(t, err)
		if info.Name == ToolNameExecute {
			return to
		}
	}
	t.Fatalf("execute tool %q not found in tool set", ToolNameExecute)
	return nil
}

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

// With a Backend and OutputDir configured, the managed execute tool writes each
// task's output to a file under that directory, and the file is readable back.
func TestManagedExecuteTool_WritesOutputFile(t *testing.T) {
	backend := setupTestBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Backend: backend,
		Shell:   &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}},
		Background: &BackgroundConfig{
			Manager:     mgr,
			OutputStore: backend,
			OutputDir:   "/tasks",
		},
	})
	require.NoError(t, err)

	_, err = invokeTool(t, findExecuteTool(t, tools), `{"command":"echo hi"}`)
	require.NoError(t, err)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	path := tasks[0].OutputFile
	require.NotEmpty(t, path)

	got, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	assert.Equal(t, "the output", got.Content)
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

// gatedShell is a Shell whose Execute blocks until release is closed (honoring ctx
// cancellation), then returns out. It lets a test hold a background task in the
// running state deterministically, without relying on wall-clock timing.
type gatedShell struct {
	release chan struct{}
	out     string
}

func (s *gatedShell) Execute(ctx context.Context, _ *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	select {
	case <-s.release:
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
		Shell:      &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}},
		Background: &BackgroundConfig{Manager: mgr},
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
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	backend := setupTestBackend() // so a background launch reports an output path
	// A gated shell keeps the background task in the running state until we release
	// it, so the launch reliably returns the "running in background" notice rather
	// than racing the task to completion.
	shell := &gatedShell{release: make(chan struct{}), out: "done"}
	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Backend: backend,
		Shell:   shell,
		Background: &BackgroundConfig{
			Manager:     mgr,
			OutputStore: backend,
			OutputDir:   "/tasks",
		},
	})
	require.NoError(t, err)

	result, err := invokeTool(t, findExecuteTool(t, tools), `{"command":"sleep 1","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")

	// Let the held task finish, then confirm it reaches completion.
	close(shell.release)
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
		Shell:      &slowShell{delay: 200 * time.Millisecond, out: "slow done"},
		Background: &BackgroundConfig{Manager: mgr},
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
		Shell:      &slowShell{delay: time.Second, out: "never"},
		Background: &BackgroundConfig{Manager: mgr},
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

	executeTool, err := newManagedExecuteTool(mgr, nil, &mockStreamingShellMultiChunk{}, outputSink{}, "", "")
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

// An explicit background launch on a streaming managed tool exposes startup
// output. This quick command completes inside the preview window, so its complete
// output reaches the caller without a stale background notice.
func TestManagedExecuteTool_StreamingExplicitBackground(t *testing.T) {
	backend := setupTestBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(mgr, nil, &mockStreamingShellMultiChunk{}, outputSink{store: backend, outputDir: "/tasks"}, "", "")
	require.NoError(t, err)
	st := executeTool.(tool.StreamableTool)

	sr, err := st.StreamableRun(context.Background(), `{"command":"echo hi","run_in_background":true}`)
	require.NoError(t, err)
	got := drainToolStream(t, sr)
	assert.Contains(t, got, "chunk1")
	assert.Contains(t, got, "chunk3")
	assert.NotContains(t, got, "is running in the background")

	waitAllTasks(t, mgr)
	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.True(t, tasks[0].RunInBackground)
	assert.Equal(t, backgroundtask.StatusCompleted, tasks[0].Status)
	assert.Contains(t, tasks[0].Result, "chunk1")
	// The streamed output was teed to the output file as it drained in the background.
	require.NotEmpty(t, tasks[0].OutputFile)
	got2, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: tasks[0].OutputFile})
	require.NoError(t, err)
	assert.Contains(t, got2.Content, "chunk1")
}

// gatedStreamingShell emits "first\n", waits for release, then "second\n" and EOF.
// It lets a test observe interim output: the output file holds a growing prefix
// while the run is mid-stream.
type gatedStreamingShell struct {
	release chan struct{}
}

func (g *gatedStreamingShell) ExecuteStreaming(ctx context.Context, _ *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](2)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{Output: "first\n"}, nil)
		<-g.release
		sw.Send(&filesystem.ExecuteResponse{Output: "second\n", ExitCode: ptrOf(0)}, nil)
	}()
	return sr, nil
}

// The streaming execute tool tees chunks to the output file as they arrive, so a
// reader sees interim output (a growing prefix) before the run completes.
func TestManagedExecuteTool_StreamingInterimOutput(t *testing.T) {
	backend := setupTestBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	gate := &gatedStreamingShell{release: make(chan struct{})}
	executeTool, err := newManagedExecuteTool(mgr, nil, gate, outputSink{store: backend, outputDir: "/tasks"}, "", "")
	require.NoError(t, err)
	st := executeTool.(tool.StreamableTool)

	sr, err := st.StreamableRun(context.Background(), `{"command":"run"}`)
	require.NoError(t, err)

	// Read the first chunk off the caller stream — by then it has also been teed to
	// the output file.
	first, err := sr.Recv()
	require.NoError(t, err)
	assert.Contains(t, first, "first")

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	path := tasks[0].OutputFile
	require.NotEmpty(t, path)

	// Interim: the file holds the first chunk but not yet the second.
	require.Eventually(t, func() bool {
		got, readErr := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: path})
		return readErr == nil && strings.Contains(got.Content, "first")
	}, time.Second, 5*time.Millisecond)
	interim, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	assert.NotContains(t, interim.Content, "second", "second chunk must not be present before release")

	// Release the rest and drain.
	close(gate.release)
	for {
		if _, err := sr.Recv(); err == io.EOF {
			break
		} else {
			require.NoError(t, err)
		}
	}

	waitAllTasks(t, mgr)
	final, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: path})
	require.NoError(t, err)
	assert.Contains(t, final.Content, "first")
	assert.Contains(t, final.Content, "second")
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

	executeTool, err := newManagedExecuteTool(mgr, &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}}, nil, outputSink{}, "", "")
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

// failingAppendOpener wraps a Backend but fails to open an append stream after
// failAfter successful opens (failAfter=0 fails the very first open, i.e. the
// reservation). Reads delegate to the backend so any partial file is still
// observable. In the buffered path the reservation and the result are each one
// OpenAppend, so failAfter selects which logical append fails.
type failingAppendOpener struct {
	backend   *filesystem.InMemoryBackend
	failAfter int
	opens     int
}

func (f *failingAppendOpener) OpenAppend(ctx context.Context, req *filesystem.OpenAppendRequest) (io.WriteCloser, error) {
	if f.opens >= f.failAfter {
		f.opens++
		return nil, errors.New("append failed")
	}
	f.opens++
	return f.backend.OpenAppend(ctx, req)
}

// When the up-front reservation write fails, the task advertises no output file,
// so consumers fall back to the in-memory Result.
func TestManagedExecuteTool_ReservationFailure_NoOutputFile(t *testing.T) {
	backend := setupTestBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	opener := &failingAppendOpener{backend: backend, failAfter: 0}
	executeTool, err := newManagedExecuteTool(mgr, &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}}, nil,
		outputSink{store: opener, outputDir: "/tasks"}, "", "")
	require.NoError(t, err)

	result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "the output", result)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.Empty(t, tasks[0].OutputFile, "reservation failure must leave OutputFile unset")
	assert.Empty(t, tasks[0].OutputFileErr)
	assert.Equal(t, "the output", tasks[0].Result)
}

// When a write to the output file fails after reservation, the file is marked
// unreliable (OutputFileErr set) while the in-memory Result stays complete.
func TestManagedExecuteTool_WriteFailure_MarksUnreliable(t *testing.T) {
	backend := setupTestBackend()
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	// failAfter=1: the reservation open succeeds, the result open fails.
	opener := &failingAppendOpener{backend: backend, failAfter: 1}
	executeTool, err := newManagedExecuteTool(mgr, &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}}, nil,
		outputSink{store: opener, outputDir: "/tasks"}, "", "")
	require.NoError(t, err)

	result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "the output", result)

	tasks := mgr.List()
	require.Len(t, tasks, 1)
	assert.NotEmpty(t, tasks[0].OutputFile, "the path was reserved, so it is still recorded")
	assert.NotEmpty(t, tasks[0].OutputFileErr, "the failed write must mark the file unreliable")
	assert.Equal(t, "the output", tasks[0].Result, "Result stays complete regardless of file writes")
}

// countingAppendOpener wraps a Backend and counts every OpenAppend and every
// handle Close, so a test can assert that no opened append session was leaked
// (opens == closes).
type countingAppendOpener struct {
	backend *filesystem.InMemoryBackend
	opens   int32
	closes  int32
}

func (c *countingAppendOpener) OpenAppend(ctx context.Context, req *filesystem.OpenAppendRequest) (io.WriteCloser, error) {
	inner, err := c.backend.OpenAppend(ctx, req)
	if err != nil {
		return nil, err
	}
	atomic.AddInt32(&c.opens, 1)
	return &countingAppendWriter{WriteCloser: inner, parent: c}, nil
}

type countingAppendWriter struct {
	io.WriteCloser
	parent *countingAppendOpener
}

func (s *countingAppendWriter) Close() error {
	atomic.AddInt32(&s.parent.closes, 1)
	return s.WriteCloser.Close()
}

// erroringStreamingShell emits one chunk then a non-EOF error (no clean EOF), so the
// OnEOF hook never fires — only the error path does.
type erroringStreamingShell struct{}

func (e *erroringStreamingShell) ExecuteStreaming(ctx context.Context, _ *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](2)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{Output: "partial\n"}, nil)
		sw.Send(nil, errors.New("shell blew up"))
	}()
	return sr, nil
}

// When the streaming source errors (never reaching EOF), the append session must
// still be closed — via the error path, not only OnEOF — so a resource-holding
// backend does not leak the handle. Asserted by opens == closes.
func TestManagedExecuteTool_StreamingSourceError_ClosesStream(t *testing.T) {
	backend := setupTestBackend()
	counter := &countingAppendOpener{backend: backend}
	mgr := backgroundtask.New(context.Background(), &backgroundtask.Config{})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(mgr, nil, &erroringStreamingShell{},
		outputSink{store: counter, outputDir: "/tasks"}, "", "")
	require.NoError(t, err)
	st := executeTool.(tool.StreamableTool)

	sr, err := st.StreamableRun(context.Background(), `{"command":"boom"}`)
	require.NoError(t, err)
	// Drain to termination, tolerating the terminal shell error (the point of the
	// test is the append session lifecycle, not the surfaced error).
	for {
		if _, rerr := sr.Recv(); rerr != nil {
			break
		}
	}
	sr.Close()

	waitAllTasks(t, mgr)

	opens := atomic.LoadInt32(&counter.opens)
	closes := atomic.LoadInt32(&counter.closes)
	assert.Equal(t, opens, closes,
		"every opened append session must be closed even when the source errors (opens=%d closes=%d)", opens, closes)
	assert.Greater(t, opens, int32(0), "the streaming run must have opened at least one append session")
}
