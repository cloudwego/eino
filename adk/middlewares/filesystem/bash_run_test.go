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
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
	backgroundlocal "github.com/cloudwego/eino/adk/task/local"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func mustNewBackgroundManager(
	t testing.TB,
	ctx context.Context,
	config *background.Config,
) *background.Manager {
	t.Helper()
	if config == nil {
		config = &background.Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *background.TaskSnapshot) error { return nil }
	}
	manager, err := background.New(ctx, config)
	require.NoError(t, err)
	return manager
}

func testNotificationSessionID(context.Context) (string, error) {
	return "test-session", nil
}

type outputRuntimeStub struct {
	reportErr error
}

type nilResultWaitStore struct {
	*background.InMemoryStore
}

func (s *nilResultWaitStore) WaitForTaskVersion(
	ctx context.Context,
	req *background.WaitForTaskVersionRequest,
) (*background.TaskSnapshot, error) {
	snapshot, err := s.InMemoryStore.WaitForTaskVersion(ctx, req)
	if err != nil {
		return nil, err
	}
	if snapshot.Status == background.StatusCompleted {
		copy := *snapshot
		copy.ResultData = nil
		return &copy, nil
	}
	return snapshot, nil
}

func (*outputRuntimeStub) Controls() <-chan background.ControlRequest {
	return make(chan background.ControlRequest)
}
func (*outputRuntimeStub) NewTaskEventWriter(
	eventID string,
) (background.TaskEventScope, background.TaskEventWriter) {
	if eventID == "" {
		eventID = "event"
	}
	scope := background.TaskEventScope{
		TaskID: "task", Attempt: 1, EventID: eventID,
	}
	return scope, outputTaskEventWriter{scope: scope}
}

type outputTaskEventWriter struct {
	scope background.TaskEventScope
}

func (w outputTaskEventWriter) Append(
	_ context.Context,
	part *background.TaskEventPartInput,
) (*background.AppendTaskEventResult, error) {
	return &background.AppendTaskEventResult{
		Part: &background.TaskEventPart{
			TaskID: w.scope.TaskID, EventID: w.scope.EventID,
			PartID: part.PartID, Data: append([]byte(nil), part.Data...),
			Final: part.Final,
		},
		Inserted: true,
	}, nil
}
func (r *outputRuntimeStub) ReportTranscriptFailure(context.Context, error) error {
	return r.reportErr
}
func (*outputRuntimeStub) ListInputs(context.Context, int64, int) (*task.ListInputsResult, error) {
	return &task.ListInputsResult{}, nil
}
func (*outputRuntimeStub) WaitInputs(ctx context.Context, _ int64) (*task.ListInputsResult, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (*outputRuntimeStub) AdvanceInputCursor(context.Context, int64, int64) error {
	return nil
}
func (*outputRuntimeStub) CommitInput(context.Context, int64, int64, []byte) error {
	return nil
}
func (*outputRuntimeStub) CommitStart(context.Context, []byte) error { return nil }

var testManagerStores sync.Map

func newTestManager(
	t testing.TB,
	ctx context.Context,
	configure ...func(*background.Config),
) *background.Manager {
	store := background.NewInMemoryStore(nil)
	config := &background.Config{Tasks: store}
	for _, apply := range configure {
		apply(config)
	}
	manager := mustNewBackgroundManager(t, ctx, config)
	testManagerStores.Store(manager, store)
	return manager
}

func mustLocalRunner(
	t *testing.T,
	manager *background.Manager,
	configure ...func(*backgroundlocal.Config),
) *backgroundlocal.Runner {
	t.Helper()
	config := &backgroundlocal.Config{
		Manager: manager,
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

func TestManagedExecutePromptDoesNotPromiseCompletionNotification(t *testing.T) {
	assert.NotContains(t, ManagedExecuteToolDesc, "You will be notified")
	assert.NotContains(t, ManagedExecuteToolDescChinese, "你会收到通知")
	assert.Contains(t, ManagedExecuteToolDesc, "task_output")
	assert.Contains(t, ManagedExecuteToolDescChinese, "task_output")
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

func waitTerminalTask(t *testing.T, manager *background.Manager) *background.TaskSnapshot {
	t.Helper()
	store, ok := testManagerStores.Load(manager)
	require.True(t, ok, "test Manager Store is unavailable")
	outbox := store.(background.NotificationOutbox)
	var terminal *background.TaskSnapshot
	require.Eventually(t, func() bool {
		deliveries, err := outbox.Receive(
			context.Background(),
			&background.ReceiveNotificationsRequest{
				Limit: 10, LeaseDuration: time.Millisecond,
			},
		)
		require.NoError(t, err)
		for _, delivery := range deliveries.Deliveries {
			task, getErr := manager.Get(context.Background(), delivery.Record.TaskID)
			require.NoError(t, getErr)
			if task.Status == background.StatusCompleted ||
				task.Status == background.StatusFailed ||
				task.Status == background.StatusCanceled {
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

func TestReserveBashOutputFallbackCreatesEmptyFile(t *testing.T) {
	backend := filesystem.NewInMemoryBackend()
	first := reserveBashOutput(context.Background(), outputSink{
		store: backend, outputDir: "/tasks",
	})
	second := reserveBashOutput(context.Background(), outputSink{
		store: backend, outputDir: "/tasks",
	})
	require.NotEmpty(t, first.path)
	require.NotEqual(t, first.path, second.path)
	require.Equal(t, "/tasks", path.Dir(first.path))
	require.Equal(t, ".output", path.Ext(first.path))
	_, err := uuid.Parse(strings.TrimSuffix(path.Base(first.path), ".output"))
	require.NoError(t, err)
	reserved, err := backend.Read(context.Background(), &filesystem.ReadRequest{
		FilePath: first.path,
	})
	require.NoError(t, err)
	require.Empty(t, reserved.Content)
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

	_, err = invokeTool(t, findExecuteTool(t, tools), `{"command":"echo hi","run_in_background":true}`)
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

type taskOwnedShell struct {
	started  chan struct{}
	release  chan struct{}
	canceled chan struct{}
}

type shellFunc func(
	context.Context,
	*filesystem.ExecuteRequest,
) (*filesystem.ExecuteResponse, error)

func (f shellFunc) Execute(
	ctx context.Context,
	req *filesystem.ExecuteRequest,
) (*filesystem.ExecuteResponse, error) {
	return f(ctx, req)
}

func (s *taskOwnedShell) Execute(
	ctx context.Context,
	_ *filesystem.ExecuteRequest,
) (*filesystem.ExecuteResponse, error) {
	close(s.started)
	select {
	case <-s.release:
		return &filesystem.ExecuteResponse{Output: "done"}, nil
	case <-ctx.Done():
		close(s.canceled)
		return nil, ctx.Err()
	}
}

func TestNewManagedBufferedExecuteToolErrors(t *testing.T) {
	t.Run("session resolution failure is returned before execution", func(t *testing.T) {
		manager := newTestManager(t, context.Background())
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		shell := &mockShellBackend{
			resp: &filesystem.ExecuteResponse{Output: "must not run"},
		}
		sessionErr := errors.New("resolve notification session")
		executeTool, err := newManagedBufferedExecuteTool(
			mustLocalRunner(t, manager),
			shell,
			func(context.Context) (string, error) {
				return "", sessionErr
			},
			outputSink{},
			toolDefinition{
				name: "managed_execute",
				desc: "Execute a managed command.",
			},
		)
		require.NoError(t, err)

		result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
		require.Empty(t, result)
		require.Nil(t, shell.req)
		require.ErrorIs(t, err, sessionErr)
		require.EqualError(
			t,
			err,
			"[LocalFunc] failed to invoke tool, toolName=managed_execute, err=resolve notification session",
		)
	})

	for _, testCase := range []struct {
		name      string
		shellErr  error
		wantError func(string) string
	}{
		{
			name:     "foreground failure",
			shellErr: errors.New("shell failed"),
			wantError: func(id string) string {
				return fmt.Sprintf(`execute %q failed: shell failed`, id)
			},
		},
		{
			name:     "foreground cancellation",
			shellErr: context.Canceled,
			wantError: func(id string) string {
				return fmt.Sprintf(`execute %q was canceled: context canceled`, id)
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			const taskID = "foreground-result"
			manager := newTestManager(t, context.Background(), func(config *background.Config) {
				config.IDGen = func(
					context.Context,
					*background.AllocateTaskIDRequest,
				) (string, error) {
					return taskID, nil
				}
			})
			t.Cleanup(func() {
				require.NoError(t, manager.Close(context.Background()))
			})
			executeTool, err := newManagedBufferedExecuteTool(
				mustLocalRunner(t, manager),
				shellFunc(func(
					context.Context,
					*filesystem.ExecuteRequest,
				) (*filesystem.ExecuteResponse, error) {
					return nil, testCase.shellErr
				}),
				testNotificationSessionID,
				outputSink{},
				toolDefinition{
					name: "managed_execute",
					desc: "Execute a managed command.",
				},
			)
			require.NoError(t, err)

			result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
			require.Empty(t, result)
			require.EqualError(
				t,
				err,
				"[LocalFunc] failed to invoke tool, toolName=managed_execute, err="+
					testCase.wantError(taskID),
			)
		})
	}

	t.Run("manager-owned completed result is returned", func(t *testing.T) {
		manager := newTestManager(t, context.Background())
		t.Cleanup(func() {
			require.NoError(t, manager.Close(context.Background()))
		})
		timeout := 100
		runner := mustLocalRunner(t, manager, func(config *backgroundlocal.Config) {
			config.ForegroundTimeoutMs = &timeout
			config.ShouldAutoBackground = func(
				context.Context,
				*foreground.CandidateInfo,
			) bool {
				return true
			}
		})
		executeTool, err := newManagedBufferedExecuteTool(
			runner,
			&mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "done"}},
			testNotificationSessionID,
			outputSink{},
			toolDefinition{
				name: "managed_execute",
				desc: "Execute a managed command.",
			},
		)
		require.NoError(t, err)

		result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
		require.NoError(t, err)
		require.Equal(t, "done", result)
	})
}

func TestManagedExecuteTool_Foreground(t *testing.T) {
	const taskID = "direct-foreground"
	mgr := newTestManager(t, context.Background(), func(config *background.Config) {
		config.IDGen = func(
			context.Context,
			*background.AllocateTaskIDRequest,
		) (string, error) {
			return taskID, nil
		}
	})
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

	_, err = mgr.Get(context.Background(), taskID)
	require.ErrorIs(t, err, background.ErrNotFound)
}

func TestManagedExecuteTool_ForegroundWithoutNotificationSession(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	middleware, err := New(context.Background(), &MiddlewareConfig{
		Shell: &mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "ok"}},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, mgr)},
		},
	})
	require.NoError(t, err)
	typed, ok := middleware.(*typedFilesystemMiddleware[*schema.Message])
	require.True(t, ok)

	result, err := invokeTool(
		t, findExecuteTool(t, typed.additionalTools), `{"command":"echo hi"}`,
	)
	require.NoError(t, err)
	assert.Equal(t, "ok", result)
}

func TestManagedExecuteTool_BackgroundWithoutNotificationSession(t *testing.T) {
	const taskID = "background-without-notification-session"
	mgr := newTestManager(t, context.Background(), func(config *background.Config) {
		config.IDGen = func(
			context.Context,
			*background.AllocateTaskIDRequest,
		) (string, error) {
			return taskID, nil
		}
	})
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	release := make(chan struct{})
	middleware, err := New(context.Background(), &MiddlewareConfig{
		Shell: &gatedShell{release: release, out: "done"},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, mgr)},
		},
	})
	require.NoError(t, err)
	typed, ok := middleware.(*typedFilesystemMiddleware[*schema.Message])
	require.True(t, ok)

	result, err := invokeTool(
		t,
		findExecuteTool(t, typed.additionalTools),
		`{"command":"sleep 1","run_in_background":true}`,
	)
	require.NoError(t, err)
	assert.Contains(t, result, "Use task_output")
	assert.NotContains(t, result, "You will be notified")

	task, err := mgr.Get(context.Background(), taskID)
	require.NoError(t, err)
	assert.Contains(t, []background.Status{
		background.StatusPending,
		background.StatusRunning,
	}, task.Status)
	assert.Empty(t, task.Spec.RootSessionID)
	assert.False(t, task.Spec.NotifySession)

	store, ok := testManagerStores.Load(mgr)
	require.True(t, ok)
	deliveries, err := store.(background.NotificationOutbox).Receive(
		context.Background(),
		&background.ReceiveNotificationsRequest{Limit: 10, LeaseDuration: time.Second},
	)
	require.NoError(t, err)
	assert.Empty(t, deliveries.Deliveries)
	close(release)
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
	middleware, err := New(context.Background(), &MiddlewareConfig{
		Backend: backend,
		Shell:   &gatedShell{release: release, out: "done"},
		Background: &BackgroundConfig{
			NotificationSessionID: testNotificationSessionID,
			Local: &LocalBackgroundConfig{
				Runner: mustLocalRunner(t, mgr), OutputStore: backend, OutputDir: "/tasks",
			},
		},
	})
	require.NoError(t, err)
	typed, ok := middleware.(*typedFilesystemMiddleware[*schema.Message])
	require.True(t, ok)

	result, err := invokeTool(t, findExecuteTool(t, typed.additionalTools), `{"command":"sleep 1","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "Command running in background with ID:")

	close(release)
	task := waitTerminalTask(t, mgr)
	assert.Equal(t, background.StatusCompleted, task.Status)
	assert.Equal(t, "test-session", task.Spec.RootSessionID)
	assert.True(t, task.Spec.NotifySession)
	events, err := mgr.ListTaskEvents(context.Background(), &background.ListTaskEventsRequest{
		TaskID: task.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, events.Parts, 1)
	assert.Equal(t, []byte("done"), events.Parts[0].Data)
	assert.True(t, events.Parts[0].Final)

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
		Shell: &slowShell{delay: 1200 * time.Millisecond, out: "slow done"},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{
				Runner: mustLocalRunner(t, mgr, func(config *backgroundlocal.Config) {
					config.ShouldAutoBackground = func(context.Context, *foreground.CandidateInfo) bool {
						return true
					}
				}),
			},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)

	// timeout=1s < 1.2s command → moved to background.
	result, err := invokeTool(t, tools[0], `{"command":"sleep","timeout":1}`)
	require.NoError(t, err)
	assert.Contains(t, result, "Command running in background with ID:")

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, background.StatusCompleted, task.Status)
	assert.Equal(t, "slow done", string(task.ResultData))
}

func TestManagedExecuteTool_CallerAbortDetachesTaskOwnedShell(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()
	timeout := 0
	shell := &taskOwnedShell{
		started:  make(chan struct{}),
		release:  make(chan struct{}),
		canceled: make(chan struct{}),
	}
	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell: shell,
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{
				Runner: mustLocalRunner(t, mgr, func(config *backgroundlocal.Config) {
					config.ForegroundTimeoutMs = &timeout
					config.ShouldAutoBackground = func(
						context.Context,
						*foreground.CandidateInfo,
					) bool {
						return true
					}
				}),
			},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan struct {
		value string
		err   error
	}, 1)
	go func() {
		value, runErr := tools[0].(tool.InvokableTool).InvokableRun(
			ctx,
			`{"command":"sleep"}`,
		)
		result <- struct {
			value string
			err   error
		}{value: value, err: runErr}
	}()
	select {
	case <-shell.started:
	case <-time.After(time.Second):
		t.Fatal("shell did not start")
	}
	store, ok := testManagerStores.Load(mgr)
	require.True(t, ok)
	beforeDetach, err := store.(background.NotificationOutbox).Receive(
		context.Background(),
		&background.ReceiveNotificationsRequest{
			Limit: 10, LeaseDuration: time.Second,
		},
	)
	require.NoError(t, err)
	require.Empty(t, beforeDetach.Deliveries)
	cancel()
	returned := <-result
	require.NoError(t, returned.err)
	require.Contains(t, returned.value, "Command running in background with ID:")
	select {
	case <-shell.canceled:
		t.Fatal("caller cancellation reached task-owned shell")
	default:
	}
	close(shell.release)
	task := waitTerminalTask(t, mgr)
	require.Equal(t, background.StatusCompleted, task.Status)
	require.Equal(t, background.PublicationOnBackground, task.Publication)
}

// Without a ShouldAutoBackground hook, a command that outlives its timeout is
// stopped with a structured foreground timeout.
func TestManagedExecuteTool_TimeoutKills(t *testing.T) {
	mgr := newTestManager(t, context.Background())
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = mgr.Close(ctx)
	}()

	tools, err := getFilesystemTools(context.Background(), &MiddlewareConfig{
		Shell: &slowShell{delay: 2 * time.Second, out: "never"},
		Background: &BackgroundConfig{
			Local: &LocalBackgroundConfig{Runner: mustLocalRunner(t, mgr)},
		},
		notificationSessionID: testNotificationSessionID,
	})
	require.NoError(t, err)

	_, err = invokeTool(t, tools[0], `{"command":"sleep","timeout":1}`)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	var timeoutErr *task.ForegroundTimeoutError
	require.ErrorAs(t, err, &timeoutErr)
	require.Equal(t, time.Second, timeoutErr.Timeout)
	require.NotEmpty(t, timeoutErr.TaskID)
}

func TestShellPayloadV1AndCommandFromTask(t *testing.T) {
	input, err := managedRunInput(executeManagedArgs{
		executeArgs: executeArgs{Command: "echo hello"},
	}, &bashOutputWriter{}, "test-session", false)
	require.NoError(t, err)
	task := &background.TaskSnapshot{Spec: background.Spec{
		Kind: ExecuteTaskKind, Payload: input.Payload,
	}}
	assert.Equal(t, "echo hello", CommandFromTask(task))

	input, err = managedRunInput(executeManagedArgs{
		executeArgs:    executeArgs{Command: "echo hello"},
		TimeoutSeconds: 2,
	}, &bashOutputWriter{}, "test-session", false)
	require.NoError(t, err)
	require.NotNil(t, input.ForegroundTimeoutMs)
	assert.Equal(t, 2000, *input.ForegroundTimeoutMs)

	payload := shellPayloadV1{Version: 2, Command: "echo hello"}
	task.Spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	assert.Empty(t, CommandFromTask(task))
	_, err = decodeShellPayload(task.Spec.Payload)
	assert.ErrorIs(t, err, background.ErrUnsupportedExecutorPayloadVersion)
}

func TestForegroundTimeoutMsForToolArgument(t *testing.T) {
	tests := []struct {
		name    string
		seconds int
		want    int
	}{
		{name: "omitted", seconds: 0},
		{name: "one second", seconds: 1, want: 1000},
		{name: "maximum", seconds: 3 * 24 * 60 * 60, want: 3 * 24 * 60 * 60 * 1000},
		{name: "clamped", seconds: 3*24*60*60 + 1, want: 3 * 24 * 60 * 60 * 1000},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			timeout := foregroundTimeoutMsForToolArgument(test.seconds)
			if test.seconds <= 0 {
				assert.Nil(t, timeout)
				return
			}
			require.NotNil(t, timeout)
			assert.Equal(t, test.want, *timeout)
		})
	}
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
}

// An explicit background launch on a streaming managed tool exposes the bounded
// startup preview when available, then drains the complete output in the
// background.
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
	assert.Contains(t, got, "is running in the background")

	task := waitTerminalTask(t, mgr)
	assert.Equal(t, background.StatusCompleted, task.Status)
	assert.Contains(t, string(task.ResultData), "chunk3")
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
	require.Equal(t, ToolNameExecute, info.Name)
	require.Equal(t, ManagedExecuteToolDesc, info.Desc)
	js, err := info.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	require.Equal(t, "object", js.Type)
	require.Equal(t, []string{"command"}, js.Required)
	require.Equal(t, 3, js.Properties.Len())
	for name, expected := range map[string]struct {
		schemaType  string
		description string
	}{
		"command": {
			schemaType:  "string",
			description: "The command to execute",
		},
		"run_in_background": {
			schemaType: "boolean",
			description: "Set to true to run the command in the background. " +
				"Use task_output to query it and task_stop to cancel it.",
		},
		"timeout": {
			schemaType: "integer",
			description: "Optional foreground wait in seconds, up to 3 days. " +
				"Ignored when run_in_background is true. At expiry, the command stops " +
				"unless the host allows automatic backgrounding for this command; then " +
				"it continues as a background task. Omit to use the configured default.",
		},
	} {
		property, ok := js.Properties.Get(name)
		require.True(t, ok, "missing schema property %q", name)
		require.Equal(t, expected.schemaType, property.Type)
		require.Equal(t, expected.description, property.Description)
	}
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
	mgr := newTestManager(t, context.Background(), func(config *background.Config) {
		config.IDGen = func(
			context.Context,
			*background.AllocateTaskIDRequest,
		) (string, error) {
			return "reservation-failure", nil
		}
	})
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

	result, err := invokeTool(t, executeTool, `{"command":"echo hi","run_in_background":true}`)
	require.NoError(t, err)
	require.Equal(
		t,
		"Command running in background with ID: reservation-failure. "+
			"You will be notified when it completes.",
		result,
	)

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

	result, err := invokeTool(t, executeTool, `{"command":"echo hi","run_in_background":true}`)
	require.NoError(t, err)
	assert.Contains(t, result, "running in background")

	task := waitTerminalTask(t, mgr)
	path, found := filesystemOutput(t, backend)
	assert.NotEmpty(t, path)
	assert.True(t, found)
	assert.Equal(t, "the output", string(task.ResultData))
	require.Contains(t, task.OutputFileErr, "append failed")
}

func TestManagedExecuteTool_CompletedTaskWithoutResultFails(t *testing.T) {
	store := &nilResultWaitStore{
		InMemoryStore: background.NewInMemoryStore(nil),
	}
	manager := mustNewBackgroundManager(t, context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "missing-result", nil
		},
	})
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = manager.Close(ctx)
	})
	timeout := 100
	runner := mustLocalRunner(t, manager, func(config *backgroundlocal.Config) {
		config.ForegroundTimeoutMs = &timeout
		config.ShouldAutoBackground = func(
			context.Context,
			*foreground.CandidateInfo,
		) bool {
			return true
		}
	})
	executeTool, err := newManagedExecuteTool(
		runner,
		&mockShellBackend{resp: &filesystem.ExecuteResponse{Output: "discarded"}},
		nil,
		testNotificationSessionID,
		outputSink{},
		toolDefinition{},
	)
	require.NoError(t, err)

	result, err := invokeTool(t, executeTool, `{"command":"echo hi"}`)
	require.Empty(t, result)
	require.EqualError(
		t,
		err,
		`[LocalFunc] failed to invoke tool, toolName=execute, err=execute task "missing-result" completed without a result`,
	)
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

	sr, err := st.StreamableRun(context.Background(), `{"command":"boom","run_in_background":true}`)
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
	assert.Equal(t, background.StatusFailed, task.Status)

	opens := atomic.LoadInt32(&counter.opens)
	closes := atomic.LoadInt32(&counter.closes)
	assert.Equal(t, opens, closes,
		"every opened append session must be closed even when the source errors (opens=%d closes=%d)", opens, closes)
	assert.Greater(t, opens, int32(0), "the streaming run must have opened at least one append session")
}
