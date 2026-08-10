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
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/backgroundtask"
	backgroundlocal "github.com/cloudwego/eino/adk/backgroundtask/local"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *backgroundtask.Config,
) *backgroundtask.Manager {
	t.Helper()
	if config == nil {
		config = &backgroundtask.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *backgroundtask.Task) error { return nil }
	}
	manager, err := backgroundtask.New(ctx, config)
	require.NoError(t, err)
	return manager
}

func testNotificationSessionID(context.Context) (string, error) {
	return "test-session", nil
}

type outputRuntimeStub struct {
	reportErr error
}

func (*outputRuntimeStub) Controls() <-chan backgroundtask.ControlRequest {
	return make(chan backgroundtask.ControlRequest)
}
func (*outputRuntimeStub) EmitProgress(
	context.Context,
	string,
	[]byte,
) (backgroundtask.ProgressEmission, error) {
	return backgroundtask.ProgressEmission{}, nil
}
func (r *outputRuntimeStub) ReportTranscriptFailure(context.Context, error) error {
	return r.reportErr
}

var testManagerStores sync.Map
var testManagerExecutors sync.Map

func newTestManager(t testing.TB, ctx context.Context) *backgroundtask.Manager {
	store := backgroundtask.NewInMemoryStore(nil)
	executors := backgroundtask.NewExecutorRegistry()
	manager := mustNewBackgroundManager(t, ctx, &backgroundtask.Config{
		Tasks: store, Executors: executors,
	})
	testManagerStores.Store(manager, store)
	testManagerExecutors.Store(manager, executors)
	return manager
}

func mustLocalRunner(
	t *testing.T,
	manager *backgroundtask.Manager,
	configure ...func(*backgroundlocal.Config),
) *backgroundlocal.Runner {
	t.Helper()
	executors, ok := testManagerExecutors.Load(manager)
	require.True(t, ok)
	config := &backgroundlocal.Config{
		Manager: manager, Executors: executors.(*backgroundtask.ExecutorRegistry),
	}
	for _, apply := range configure {
		apply(config)
	}
	runner, err := backgroundlocal.New(config)
	require.NoError(t, err)
	return runner
}

func TestManagedExecuteAcceptsBackgroundRunner(t *testing.T) {
	manager := newTestManager(t, context.Background())
	defer manager.Close(context.Background())
	_, err := New(context.Background(), &MiddlewareConfig{
		Shell: &mockShellBackend{},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, manager)},
		},
	})
	require.NoError(t, err)
}

func TestManagedExecutePromptPreservesCompletionNotification(t *testing.T) {
	assert.Contains(t, ManagedExecuteToolDesc, "You will be notified when the command completes")
	assert.Contains(t, ManagedExecuteToolDescChinese, "命令完成时你会收到通知")
}

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

func waitTerminalTask(t *testing.T, manager *backgroundtask.Manager) *backgroundtask.Task {
	t.Helper()
	store, ok := testManagerStores.Load(manager)
	require.True(t, ok, "test Manager Store is unavailable")
	outbox := store.(backgroundtask.NotificationOutbox)
	var terminal *backgroundtask.Task
	require.Eventually(t, func() bool {
		deliveries, err := outbox.Receive(
			context.Background(),
			&backgroundtask.ReceiveNotificationsRequest{
				Limit: 10, LeaseDuration: time.Millisecond,
			},
		)
		require.NoError(t, err)
		for _, delivery := range deliveries.Deliveries {
			task, getErr := manager.Get(context.Background(), delivery.Record.TaskID)
			require.NoError(t, getErr)
			if task.Status == backgroundtask.StatusCompleted ||
				task.Status == backgroundtask.StatusFailed ||
				task.Status == backgroundtask.StatusCanceled {
				terminal = task
				return true
			}
		}
		return false
	}, time.Second, 10*time.Millisecond)
	return terminal
}

func filesystemOutput(t *testing.T, backend *filesystem.InMemoryBackend) (string, bool) {
	t.Helper()
	infos, err := backend.LsInfo(context.Background(), &filesystem.LsInfoRequest{Path: "/tasks"})
	require.NoError(t, err)
	for _, info := range infos {
		if !info.IsDir && strings.HasSuffix(info.Path, ".output") {
			return "/tasks/" + info.Path, true
		}
	}
	return "", false
}

// With a Backend and OutputDir configured, the managed execute tool writes each
// task's output to a file under that directory, and the file is readable back.
func TestManagedExecuteTool_WritesOutputFile(t *testing.T) {
	backend := setupTestBackend()
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Backend: backend,
		Shell:   &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{
				Runner: mustLocalRunner(t, mgr), OutputStore: backend, OutputDir: "/tasks",
			},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)

	_, err = invokeTool(t, findExecuteTool(t, tools), `{"command":"echo hi"}`)
	require.NoError(t, err)

	task := waitTerminalTask(t, mgr)
	require.NotNil(t, task)
	path, found := filesystemOutput(t, backend)
	require.NotEmpty(t, path)
	assert.True(t, found)

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

type gatedShell struct {
	release <-chan struct{}
	out     string
}

func (s *gatedShell) Execute(
	ctx context.Context,
	_ *filesystem.ExecuteRequest,
) (*filesystem.ExecuteResponse, error) {
	select {
	case <-s.release:
		return &filesystem.ExecuteResponse{Output: s.out}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestManagedExecuteTool_Foreground(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell: &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, mgr)},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)
	require.Len(t, tools, 1)

	result, err := invokeTool(t, tools[0], `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)

	// The run is tracked by the Manager and tagged as a bash task.
	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	assert.Equal(t, "echo hi", task.Spec.Description)
	events, err := mgr.ListTaskEvents(context.Background(), &backgroundtask.ListTaskEventsRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, events.Events, 1)
	require.NotNil(t, events.Events[0])
	require.NotEmpty(t, events.Events[0].EventID)
}

func TestManagedExecuteTool_Background(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	backend := setupTestBackend() // so a background launch reports an output path
	release := make(chan struct{})
	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Backend: backend,
		Shell:   &gatedShell{release: release, out: "done"},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{
				Runner: mustLocalRunner(t, mgr), OutputStore: backend, OutputDir: "/tasks",
			},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)

	result, err := invokeTool(t, findExecuteTool(t, tools), `{"command":"sleep 1","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "Command running in background with ID:")

	close(release)
	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)

	// The background-launch message reports the (reserved) output-file path so the
	// agent can read it once the task completes.
	path, found := filesystemOutput(t, backend)
	assert.Contains(t, result, path)
	assert.True(t, found)
}

// A foreground command that outlives its timeout is moved to the background
// (kept running) when the Manager's ShouldAutoBackground hook permits it.
func TestManagedExecuteTool_TimeoutMovesToBackground(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell: &slowShell{delay: 200 * time.Millisecond, out: "slow done"},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{
				Runner: mustLocalRunner(t, mgr, func(config *backgroundlocal.Config) {
					config.ShouldAutoBackground = func(context.Context, *backgroundtask.Task) bool {
						return true
					}
				}),
			},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)

	// timeout=50ms < 200ms command → moved to background.
	result, err := invokeTool(t, tools[0], `{"command":"sleep","timeout":50}`)
	require.NoError(t, err)
	assert.Contains(t, result, "Command running in background with ID:")

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	assert.Equal(t, "slow done", string(task.ResultData))
}

// Without a ShouldAutoBackground hook, a command that outlives its timeout is
// stopped and reported as timed out.
func TestManagedExecuteTool_TimeoutKills(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell: &slowShell{delay: time.Second, out: "never"},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, mgr)},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)

	_, err = invokeTool(t, tools[0], `{"command":"sleep","timeout":50}`)
	require.ErrorContains(t, err, "timed out after 50ms")

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusFailed, task.Status)
	assert.Equal(t, "timed out after 50ms", task.ResultError)
}

func TestShellPayloadV1AndCommandFromTask(t *testing.T) {
	input, err := managedRunInput(executeManagedArgs{
		executeArgs: executeArgs{Command: "echo hello"},
	}, &bashOutputWriter{}, "test-session")
	require.NoError(t, err)
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{
		Kind: ExecuteTaskKind, Payload: input.Payload,
	}}
	assert.Equal(t, "echo hello", CommandFromTask(task))

	payload := shellPayloadV1{Version: 2, Command: "echo hello"}
	task.Spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	assert.Empty(t, CommandFromTask(task))
	_, err = decodeShellPayload(task.Spec.Payload)
	assert.ErrorIs(t, err, backgroundtask.ErrUnsupportedExecutorPayloadVersion)
}

// With a Manager, the execute tool schema gains run_in_background and timeout fields.
// With a StreamingShell backend the managed execute tool is a StreamableTool that
// streams foreground output live while still tracking the run in the Manager.
func TestManagedExecuteTool_StreamingForeground(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), nil, &mockStreamingShellMultiChunk{},
		testNotificationSessionID, outputSink{}, toolDefinition{},
	)
	require.NoError(t, err)

	st, ok := executeTool.(tool.StreamableTool)
	require.True(t, ok, "managed execute tool with StreamingShell must be a StreamableTool")

	sr, err := st.StreamableRun(context.Background(), `{"command":"echo hi"}`)
	require.NoError(t, err)
	got := drainToolStream(t, sr)
	assert.Contains(t, got, "chunk1")
	assert.Contains(t, got, "chunk3")

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	// The streamed chunks are also the persisted result.
	assert.Contains(t, string(task.ResultData), "chunk1")
	assert.Contains(t, string(task.ResultData), "chunk3")
}

// An explicit background launch on a streaming managed tool exposes startup
// output. This quick command completes inside the preview window, so its complete
// output reaches the caller without a stale background notice.
func TestManagedExecuteTool_StreamingExplicitBackground(t *testing.T) {
	backend := setupTestBackend()
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), nil, &mockStreamingShellMultiChunk{}, testNotificationSessionID,
		outputSink{store: backend, outputDir: "/tasks"}, toolDefinition{},
	)
	require.NoError(t, err)
	st := executeTool.(tool.StreamableTool)

	sr, err := st.StreamableRun(context.Background(), `{"command":"echo hi","run_in_background":true}`)
	require.NoError(t, err)
	got := drainToolStream(t, sr)
	assert.Contains(t, got, "chunk1")
	assert.Contains(t, got, "chunk3")
	assert.NotContains(t, got, "is running in the background")

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
	assert.Contains(t, string(task.ResultData), "chunk1")
	// The streamed output was teed to the output file as it drained in the background.
	path, found := filesystemOutput(t, backend)
	require.True(t, found)
	got2, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: path})
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
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	gate := &gatedStreamingShell{release: make(chan struct{})}
	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), nil, gate, testNotificationSessionID,
		outputSink{store: backend, outputDir: "/tasks"}, toolDefinition{},
	)
	require.NoError(t, err)
	st := executeTool.(tool.StreamableTool)

	sr, err := st.StreamableRun(context.Background(), `{"command":"run"}`)
	require.NoError(t, err)

	// Read the first chunk off the caller stream — by then it has also been teed to
	// the output file.
	first, err := sr.Recv()
	require.NoError(t, err)
	assert.Contains(t, first, "first")

	path, found := filesystemOutput(t, backend)
	require.NotEmpty(t, path)
	assert.True(t, found)

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
		if _, recvErr := sr.Recv(); recvErr == io.EOF {
			break
		} else {
			require.NoError(t, recvErr)
		}
	}

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusCompleted, task.Status)
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
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}}, nil,
		testNotificationSessionID, outputSink{}, toolDefinition{},
	)
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

type failingStreamingShell struct {
	err error
}

type writeErrorOpener struct{}

func (writeErrorOpener) OpenAppend(
	context.Context,
	*filesystem.OpenAppendRequest,
) (io.WriteCloser, error) {
	return writeErrorCloser{}, nil
}

type writeErrorCloser struct{}

func (writeErrorCloser) Write([]byte) (int, error) {
	return 0, errors.New("write failed")
}
func (writeErrorCloser) Close() error { return nil }

func (s failingStreamingShell) ExecuteStreaming(
	context.Context,
	*filesystem.ExecuteRequest,
) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	return nil, s.err
}

func TestBashOutputFailurePropagation(t *testing.T) {
	reportErr := errors.New("report failed")
	runtime := &outputRuntimeStub{reportErr: reportErr}
	writer := &bashOutputWriter{runtime: runtime, ctx: context.Background()}
	err := writer.fail(errors.New("write failed"))
	require.ErrorIs(t, err, reportErr)

	writer = &bashOutputWriter{
		store: &failingAppendOpener{
			backend: filesystem.NewInMemoryBackend(), failAfter: 0,
		},
		path: "/tasks/output",
	}
	err = writer.appendResult(context.Background(), runtime, "output")
	require.ErrorIs(t, err, reportErr)

	writer = &bashOutputWriter{store: writeErrorOpener{}, path: "/tasks/output"}
	err = writer.appendResult(context.Background(), runtime, "output")
	require.ErrorIs(t, err, reportErr)

	shellErr := errors.New("shell failed")
	work := bashStreamWork(
		failingStreamingShell{err: shellErr}, &filesystem.ExecuteRequest{}, &bashOutputWriter{},
	)
	stream, err := work(context.Background(), runtime)
	require.ErrorIs(t, err, shellErr)
	require.Nil(t, stream)
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
// so consumers fall back to the in-memory ResultData.
func TestManagedExecuteTool_ReservationFailure_NoOutputFile(t *testing.T) {
	backend := setupTestBackend()
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	opener := &failingAppendOpener{backend: backend, failAfter: 0}
	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}}, nil,
		testNotificationSessionID, outputSink{store: opener, outputDir: "/tasks"}, toolDefinition{},
	)
	require.NoError(t, err)

	result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "the output", result)

	task := waitTerminalTask(t, mgr)
	path, found := filesystemOutput(t, backend)
	assert.Empty(t, path)
	assert.False(t, found)
	assert.Equal(t, "the output", string(task.ResultData))
}

// When a write to the output file fails after reservation, the file is marked
// unreliable (OutputFileErr set) while the in-memory ResultData stays complete.
func TestManagedExecuteTool_WriteFailure_MarksUnreliable(t *testing.T) {
	backend := setupTestBackend()
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	// failAfter=1: the reservation open succeeds, the result open fails.
	opener := &failingAppendOpener{backend: backend, failAfter: 1}
	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "the output"}}, nil,
		testNotificationSessionID, outputSink{store: opener, outputDir: "/tasks"}, toolDefinition{},
	)
	require.NoError(t, err)

	result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
	require.NoError(t, err)
	assert.Equal(t, "the output", result)

	task := waitTerminalTask(t, mgr)
	path, found := filesystemOutput(t, backend)
	assert.NotEmpty(t, path)
	assert.True(t, found)
	assert.Equal(t, "the output", string(task.ResultData))
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
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	executeTool, err := newManagedExecuteTool(
		mustLocalRunner(t, mgr), nil, &erroringStreamingShell{}, testNotificationSessionID,
		outputSink{store: counter, outputDir: "/tasks"}, toolDefinition{},
	)
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

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, backgroundtask.StatusFailed, task.Status)

	opens := atomic.LoadInt32(&counter.opens)
	closes := atomic.LoadInt32(&counter.closes)
	assert.Equal(t, opens, closes,
		"every opened append session must be closed even when the source errors (opens=%d closes=%d)", opens, closes)
	assert.Greater(t, opens, int32(0), "the streaming run must have opened at least one append session")
}
