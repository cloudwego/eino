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
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedExecutor struct {
	key                  string
	validateErr          error
	validateExecutionErr error
	leaseExpiryPolicy    LeaseExpiryPolicy
	disableDrain         bool
	execute              func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error)

	mu        sync.Mutex
	validated []Spec
	executed  []*Task
}

func mustNewManager(t testing.TB, ctx context.Context, config *Config) *Manager {
	t.Helper()
	if config == nil {
		config = &Config{}
	} else {
		copy := *config
		config = &copy
	}
	if config.SendTaskCreatedEvent == nil {
		config.SendTaskCreatedEvent = func(context.Context, *Task) error { return nil }
	}
	manager, err := New(ctx, config)
	require.NoError(t, err)
	return manager
}

func (e *scriptedExecutor) Key() string {
	if e.key == "" {
		return "test"
	}
	return e.key
}

func (e *scriptedExecutor) LeaseExpiryPolicy() LeaseExpiryPolicy {
	if e.leaseExpiryPolicy != "" {
		return e.leaseExpiryPolicy
	}
	return LeaseExpiryRetry
}

func (e *scriptedExecutor) ValidateSpec(spec Spec) error {
	e.mu.Lock()
	e.validated = append(e.validated, cloneSpec(spec))
	e.mu.Unlock()
	return e.validateErr
}

func (e *scriptedExecutor) ValidateExecution(context.Context, *Task) error {
	return e.validateExecutionErr
}

func (e *scriptedExecutor) SupportsDrain() bool { return !e.disableDrain }

func (e *scriptedExecutor) Execute(ctx context.Context, task *Task, runtime ExecutionRuntime) (*ExecutionResult, error) {
	e.mu.Lock()
	e.executed = append(e.executed, cloneTask(task))
	e.mu.Unlock()
	if e.execute != nil {
		return e.execute(ctx, task, runtime)
	}
	return &ExecutionResult{Status: StatusCompleted, Data: []byte("result")}, nil
}

func managerWithExecutor(t *testing.T, tasks TaskStore, executor Executor, lease time.Duration) *Manager {
	t.Helper()
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	taskEvents, ok := tasks.(TaskEventStore)
	if !ok {
		taskEvents = NewInMemoryStore(nil)
	}
	return mustNewManager(t, context.Background(), &Config{
		Tasks: tasks, TaskEvents: taskEvents, Executors: registry,
	})
}

func TestSimplifiedPublicModelHasNoOverlappingStateFields_BitsUT(t *testing.T) {
	deprecatedResumeField := "Resume" + "Data"
	deprecatedTaskCursorField := "Latest" + "Update" + "Sequence"
	deprecatedNotificationCursorField := "Update" + "Sequence"

	assertFieldsAbsent(t, reflect.TypeOf(Spec{}),
		"PayloadVersion", "Recovery", "Result", "PayloadEncoding", "TraceID", "SpecVersion",
		"Type", "ToolUseID", "Deadline", "LeaseExpiryPolicy", "Notify")
	assertFieldsPresent(t, reflect.TypeOf(Spec{}), "SessionID", "NotifySession")
	assertFieldsAbsent(t, reflect.TypeOf(Spec{}), "CreatedAt")
	assertFieldsPresent(t, reflect.TypeOf(Task{}),
		"Spec", "LeaseExpiryPolicy", "Status", "Checkpoint", "ResultData", "ResultError",
		"PendingResume", "Version", "CreatedAt")
	field, exists := reflect.TypeOf(Task{}).FieldByName("PendingResume")
	require.True(t, exists)
	assert.Equal(t, reflect.TypeOf([]byte(nil)), field.Type)
	assertFieldsAbsent(t, reflect.TypeOf(Task{}),
		"ID", "Result", "ResultRef", "TerminalReason", deprecatedResumeField, "ResumeEncoding",
		deprecatedTaskCursorField, "LatestProgress", "TransitionVersion", "CheckpointVersion",
		"CancelTransitionVersion", "LeaseOwner", "LeaseGeneration", "LeaseExpiresAt")
	assertFieldsAbsent(t, reflect.TypeOf(Notification{}),
		"Status", "Progress", "Checkpoint", "Result", "Reason", "Task",
		deprecatedNotificationCursorField, "TransitionVersion", "NotificationID", "EventKind", "Target")
	assertFieldsPresent(t, reflect.TypeOf(Notification{}),
		"ID", "TaskID", "SessionID", "Version", "Kind", "Data", "CreatedAt")
	assertFieldsPresent(t, reflect.TypeOf(NotifyParentRequest{}), "EventID", "Kind", "Data")
	assertFieldsPresent(t, reflect.TypeOf(ReceiveNotificationsRequest{}), "Limit", "LeaseDuration")
	assertFieldsAbsent(t, reflect.TypeOf(ReceiveNotificationsRequest{}), "ConsumerID", "VisibilityTime")
	assertFieldsPresent(t, reflect.TypeOf(ListTaskEventsRequest{}),
		"TaskID", "Cursor", "Limit", "NewestFirst")
	assertFieldsPresent(t, reflect.TypeOf(ListTaskEventsResult{}), "Events", "NextCursor")
	assertFieldsPresent(t, reflect.TypeOf(Config{}),
		"Tasks", "TaskEvents", "Executors", "SendTaskCreatedEvent", "IDGen")
	assertFieldsAbsent(t, reflect.TypeOf(Config{}), "Store", "NotificationWriter")
	assertMethodsAbsent(t, reflect.TypeOf((*TaskStore)(nil)).Elem(),
		"AppendTaskEvent", "ListTaskEvents", "ReadRecentTaskEvents", "ReportOutputFailure",
	)
	assertMethodsPresent(t, reflect.TypeOf((*TaskStore)(nil)).Elem(),
		"CommitStart", "ReportTranscriptFailure")
	assertMethodsPresent(t, reflect.TypeOf((*TaskEventStore)(nil)).Elem(),
		"AppendTaskEvent", "ListTaskEvents")
	assertMethodsPresent(t, reflect.TypeOf((*NotificationWriter)(nil)).Elem(),
		"EnqueueTaskNotification")
	assertMethodsAbsent(t, reflect.TypeOf((*TaskEventStore)(nil)).Elem(), "ReadRecentTaskEvents")
	assertMethodsAbsent(t, reflect.TypeOf((*Manager)(nil)),
		"Store", "Executors", "RequestControl", "RequestTimeout", "ReadOutput",
		"ReadRecentTaskEvents", "WaitUpdate", "CreateAndStart", "LoadOrRegisterExecutor",
		"ValidateNotificationDelivery", "MarkBackgrounded")
	assertMethodsPresent(t, reflect.TypeOf((*Manager)(nil)),
		"Submit", "Get", "ListPending", "ListSuspended", "Execute", "WaitForTaskVersion",
		"ListTaskEvents", "RequestCancel", "ReleaseSuspension", "Resume", "AllocateTaskID",
		"Close")
	assertFieldsPresent(t, reflect.TypeOf(SubmitRequest{}), "Spec", "InitialCheckpoint")
	assertFieldsPresent(t, reflect.TypeOf(CreateTaskRequest{}),
		"Spec", "LeaseExpiryPolicy", "Checkpoint")
	assertMethodsPresent(t, reflect.TypeOf((*ExecutorRegistry)(nil)), "LoadOrRegister")
	assertMethodsAbsent(t, reflect.TypeOf((*InMemoryStore)(nil)),
		"CreateAndStart", "Cancel", "ReadRecentTaskEvents")
	assertMethodsPresent(t, reflect.TypeOf((*InMemoryStore)(nil)),
		"ListSuspended", "AckCancel", "ReleaseSuspension", "CommitStart")
	assertMethodsAbsent(t, reflect.TypeOf((*Executor)(nil)).Elem(), "ValidateResume")
	assertMethodsAbsent(t, reflect.TypeOf((*ExecutionRuntime)(nil)).Elem(),
		"TaskID", "AppendTaskEvent", "ReportOutputFailure", "CommitStart")
	assertMethodsPresent(t, reflect.TypeOf((*ExecutionRuntime)(nil)).Elem(),
		"EmitProgress", "ReportTranscriptFailure")
	assertMethodsPresent(t, reflect.TypeOf((*StartCommitRuntime)(nil)).Elem(),
		"CommitStart")
	assertFieldsPresent(t, reflect.TypeOf(CommitStartRequest{}),
		"TaskID", "ExpectedVersion", "Checkpoint")
	assertFieldsPresent(t, reflect.TypeOf(ProgressEmission{}), "EventID", "FirstEmission")
	assertFieldsPresent(t, reflect.TypeOf(TaskEvent{}), "EventID", "TaskID", "Data", "CreatedAt")
	assertFieldsAbsent(t, reflect.TypeOf(TaskEvent{}), "SourceID", "Sequence", "Attempt")
}

func assertFieldsAbsent(t *testing.T, typ reflect.Type, names ...string) {
	t.Helper()
	for _, name := range names {
		_, exists := typ.FieldByName(name)
		assert.Falsef(t, exists, "%s must not expose %s", typ.Name(), name)
	}
}

func assertFieldsPresent(t *testing.T, typ reflect.Type, names ...string) {
	t.Helper()
	for _, name := range names {
		_, exists := typ.FieldByName(name)
		assert.Truef(t, exists, "%s must expose %s", typ.Name(), name)
	}
}

func assertMethodsAbsent(t *testing.T, typ reflect.Type, names ...string) {
	t.Helper()
	for _, name := range names {
		_, exists := typ.MethodByName(name)
		assert.Falsef(t, exists, "%s must not expose %s", typ, name)
	}
}

func assertMethodsPresent(t *testing.T, typ reflect.Type, names ...string) {
	t.Helper()
	for _, name := range names {
		_, exists := typ.MethodByName(name)
		assert.Truef(t, exists, "%s must expose %s", typ, name)
	}
}

func TestManagerSubmitUsesExecutorIdentityAndValidation_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{validateErr: errors.New("invalid payload")}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	spec := validSpec("invalid")

	_, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.ErrorContains(t, err, "validate spec")
	_, getErr := store.Get(context.Background(), spec.ID)
	assert.ErrorIs(t, getErr, ErrNotFound)
	require.Len(t, executor.validated, 1)

	spec.ExecutorKey = "missing"
	_, err = manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	assert.ErrorContains(t, err, `executor "missing" is unavailable`)
}

func TestManagerValidateSpecRunsBeforeSubmitAndStart_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{leaseExpiryPolicy: LeaseExpiryFail}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("validate-once")})
	require.NoError(t, err)
	assert.Equal(t, LeaseExpiryFail, task.LeaseExpiryPolicy)
	require.Len(t, executor.validated, 1)

	executor.validateErr = errors.New("invalid on worker")
	require.ErrorContains(t, manager.Execute(context.Background(), task.Spec.ID), "validate spec")
	require.Len(t, executor.validated, 2)
	pending, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusPending, pending.Status)
}

func TestManagerExecutePersistsReturnedResultDirectly_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("complete")})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	completed, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusCompleted, completed.Status)
	assert.Equal(t, "result", string(completed.ResultData))
	assert.Empty(t, completed.ResultError)
	require.Len(t, executor.executed, 1)
	assert.Equal(t, int64(1), executor.executed[0].Attempt)
}

func TestManagerReducesOrdinaryErrorsToBoundedDurableStrings_BitsUT(t *testing.T) {
	message := strings.Repeat("x", 5000)
	executor := &scriptedExecutor{
		execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
			return nil, errors.New(message)
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("failed")})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	failed, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, failed.Status)
	assert.Len(t, failed.ResultError, 4096)
	assert.Nil(t, failed.ResultData)
}

func TestManagerPassesCheckpointUnchangedToExecutor_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{}
	store, clock := recoveredTaskStore(t, "opaque", []byte("executor-owned"))
	manager := managerWithExecutor(t, store, executor, 5*time.Second)

	require.NoError(t, manager.Execute(context.Background(), "opaque"))
	require.Len(t, executor.executed, 1)
	assert.Equal(t, int64(2), executor.executed[0].Attempt)
	assert.Equal(t, "executor-owned", string(executor.executed[0].Checkpoint))
	assert.Equal(t, time.Unix(106, 0), clock.Now())
}

func recoveredTaskStore(t *testing.T, id string, checkpoint []byte) (*InMemoryStore, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Unix(100, 0)}
	store := newInMemoryStoreWithClock(
		&InMemoryStoreConfig{ActiveAttemptTimeout: 5 * time.Second},
		clock.Now,
	)
	started := createAndStart(t, store, id)
	store.mu.Lock()
	store.tasks[id].Checkpoint = checkpoint
	store.mu.Unlock()
	require.Equal(t, StatusRunning, started.Status)
	clock.Advance(6 * time.Second)
	pending, err := store.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, StatusPending, pending.Status)
	return store, clock
}

func TestExecutorOwnsCheckpointCompatibility_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(_ context.Context, task *Task, _ ExecutionRuntime) (*ExecutionResult, error) {
			if task.Attempt > 1 && string(task.Checkpoint) != "valid" {
				return nil, errors.New("unsafe checkpoint rejected")
			}
			return &ExecutionResult{Status: StatusCompleted, Data: []byte("ok")}, nil
		},
	}
	store, _ := recoveredTaskStore(t, "non-restartable", []byte("corrupt"))
	manager := managerWithExecutor(t, store, executor, 5*time.Second)

	require.NoError(t, manager.Execute(context.Background(), "non-restartable"))
	failed, err := store.Get(context.Background(), "non-restartable")
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, failed.Status)
	assert.Equal(t, "unsafe checkpoint rejected", failed.ResultError)
}

func TestManagerResumeStoresOpaqueInputWithoutExecutorContract_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	submitted, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("resume-manager")})
	require.NoError(t, err)
	started, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: submitted.Spec.ID, ExpectedVersion: submitted.Version,
	})
	require.NoError(t, err)
	waiting, err := store.WaitInput(context.Background(), &WaitInputTaskRequest{
		TaskID: submitted.Spec.ID, ExpectedVersion: started.Version, Checkpoint: []byte("checkpoint"),
	})
	require.NoError(t, err)

	resumed, err := manager.Resume(context.Background(), &ResumeRequest{
		TaskID: submitted.Spec.ID, ExpectedVersion: waiting.Version,
		Data: []byte("raw"),
	})
	require.NoError(t, err)
	require.NotNil(t, resumed.PendingResume)
	assert.Equal(t, "raw", string(resumed.PendingResume))
	assert.Equal(t, StatusPending, resumed.Status)
}

func TestManagerWaitingInputPersistsCheckpointWithoutTerminalResult_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
			return &ExecutionResult{Status: StatusWaitingInput, Checkpoint: []byte("checkpoint")}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("input")})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusWaitingInput, waiting.Status)
	assert.Equal(t, "checkpoint", string(waiting.Checkpoint))
	assert.Empty(t, waiting.ResultData)
	assert.Empty(t, waiting.ResultError)
}

func TestManagerRejectsMixedExecutionResultVariants_BitsUT(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result *ExecutionResult
	}{
		{
			name: "completed checkpoint",
			result: &ExecutionResult{
				Status: StatusCompleted, Checkpoint: []byte("checkpoint"),
			},
		},
		{
			name: "failed data",
			result: &ExecutionResult{
				Status: StatusFailed, Data: []byte("data"), Error: "failed",
			},
		},
		{
			name: "canceled data",
			result: &ExecutionResult{
				Status: StatusCanceled, Data: []byte("data"),
			},
		},
		{
			name: "waiting error",
			result: &ExecutionResult{
				Status: StatusWaitingInput, Error: "failed",
			},
		},
		{
			name: "suspended data",
			result: &ExecutionResult{
				Status: StatusSuspended, Data: []byte("data"),
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			executor := &scriptedExecutor{
				execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
					return testCase.result, nil
				},
			}
			store := NewInMemoryStore(nil)
			manager := managerWithExecutor(t, store, executor, time.Minute)
			task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("mixed-result")})
			require.NoError(t, err)

			err = manager.Execute(context.Background(), task.Spec.ID)
			require.ErrorIs(t, err, ErrInvalidExecutionResult)
		})
	}
}

func TestManagerErrorDoesNotCreatePendingResume_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(_ context.Context, _ *Task, _ ExecutionRuntime) (*ExecutionResult, error) {
			return nil, errors.New("execution failed")
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("failed-after-input")})
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	failed, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFailed, failed.Status)
	assert.Equal(t, "execution failed", failed.ResultError)
	assert.Nil(t, failed.PendingResume)
}

func TestCheckpointUnavailableStopsRenewalWithoutPersistingFailure_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(500, 0)}
	store := newInMemoryStoreWithClock(
		&InMemoryStoreConfig{ActiveAttemptTimeout: 5 * time.Second},
		clock.Now,
	)
	executor := &scriptedExecutor{
		execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
			return nil, ErrDrainCheckpointUnavailable
		},
	}
	manager := managerWithExecutor(t, store, executor, 5*time.Second)
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: validSpec("drain")})
	require.NoError(t, err)

	err = manager.Execute(context.Background(), task.Spec.ID)
	assert.ErrorIs(t, err, ErrDrainCheckpointUnavailable)
	running, getErr := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, getErr)
	assert.Equal(t, StatusRunning, running.Status)
	assert.Empty(t, running.ResultData)
	assert.Empty(t, running.ResultError)

	clock.Advance(6 * time.Second)
	pending, getErr := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, getErr)
	assert.Equal(t, StatusPending, pending.Status)
	assert.Empty(t, pending.ResultData)
	assert.Empty(t, pending.ResultError)
}
