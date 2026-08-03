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
	checkpointErr        error
	normalizedResume     []byte
	leaseExpiryPolicy    LeaseExpiryPolicy
	execute              func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error)

	mu                  sync.Mutex
	validated           []Spec
	checkpointValidated [][]byte
	resumeCheckpoint    []byte
	resumeInput         []byte
	executed            []*Task
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

func (e *scriptedExecutor) SupportsDrain() bool { return true }

func (e *scriptedExecutor) ValidateCheckpoint(_ context.Context, _ Spec, checkpoint []byte) error {
	e.mu.Lock()
	e.checkpointValidated = append(e.checkpointValidated, cloneBytes(checkpoint))
	e.mu.Unlock()
	return e.checkpointErr
}

func (e *scriptedExecutor) ValidateResume(
	_ context.Context,
	_ Spec,
	checkpoint []byte,
	resumeInput []byte,
) ([]byte, error) {
	e.mu.Lock()
	e.resumeCheckpoint = cloneBytes(checkpoint)
	e.resumeInput = cloneBytes(resumeInput)
	e.mu.Unlock()
	return cloneBytes(e.normalizedResume), nil
}

func (e *scriptedExecutor) Execute(ctx context.Context, task *Task, runtime ExecutionRuntime) (*ExecutionResult, error) {
	e.mu.Lock()
	e.executed = append(e.executed, cloneTask(task))
	e.mu.Unlock()
	if e.execute != nil {
		return e.execute(ctx, task, runtime)
	}
	return &ExecutionResult{Status: StatusCompleted, Data: []byte("result")}, nil
}

func managerWithExecutor(t *testing.T, store Store, executor Executor, lease time.Duration) *Manager {
	t.Helper()
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	return New(context.Background(), &Config{
		Store: store, Executors: registry,
	})
}

func TestSimplifiedPublicModelHasNoOverlappingStateFields_BitsUT(t *testing.T) {
	deprecatedResumeField := "Resume" + "Data"
	deprecatedTaskCursorField := "Latest" + "Update" + "Sequence"
	deprecatedNotificationCursorField := "Update" + "Sequence"

	assertFieldsAbsent(t, reflect.TypeOf(Spec{}),
		"PayloadVersion", "Recovery", "Result", "PayloadEncoding", "TraceID", "SpecVersion",
		"Type", "ToolUseID", "Deadline", "LeaseExpiryPolicy")
	assertFieldsPresent(t, reflect.TypeOf(Task{}),
		"Spec", "LeaseExpiryPolicy", "Status", "Checkpoint", "ResultData", "ResultError",
		"PendingResume", "Version")
	field, exists := reflect.TypeOf(Task{}).FieldByName("PendingResume")
	require.True(t, exists)
	assert.Equal(t, reflect.TypeOf([]byte(nil)), field.Type)
	assertFieldsAbsent(t, reflect.TypeOf(Task{}),
		"ID", "Result", "ResultRef", "TerminalReason", deprecatedResumeField, "ResumeEncoding",
		deprecatedTaskCursorField, "LatestProgress", "TransitionVersion", "CheckpointVersion",
		"CancelTransitionVersion", "LeaseOwner", "LeaseGeneration", "LeaseExpiresAt")
	assertFieldsAbsent(t, reflect.TypeOf(Notification{}),
		"Status", "Progress", "Checkpoint", "Result", "Reason", "SessionID",
		deprecatedNotificationCursorField, "TransitionVersion", "NotificationID", "EventKind")
	assertFieldsPresent(t, reflect.TypeOf(Notification{}), "ID", "Version", "Kind", "Task")
	assertFieldsAbsent(t, reflect.TypeOf(NotificationDeliveryValidation{}), "Store")
	assertFieldsPresent(t, reflect.TypeOf(NotificationDeliveryValidation{}),
		"OutboxAvailable", "TargetKind")
	assertMethodsAbsent(t, reflect.TypeOf((*Manager)(nil)),
		"Store", "Executors", "MarkBackgrounded", "RequestControl", "RequestTimeout")
	assertMethodsPresent(t, reflect.TypeOf((*Manager)(nil)),
		"Submit", "Get", "ListPending", "Execute", "WaitUpdate", "ReadOutput",
		"RequestCancel", "Resume", "AllocateTaskID",
		"LoadOrRegisterExecutor", "ValidateNotificationDelivery", "Close")
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

	_, err := manager.Submit(context.Background(), spec)
	require.ErrorContains(t, err, "validate spec")
	_, getErr := store.Get(context.Background(), spec.ID)
	assert.ErrorIs(t, getErr, ErrNotFound)
	require.Len(t, executor.validated, 1)

	spec.ExecutorKey = "missing"
	_, err = manager.Submit(context.Background(), spec)
	assert.ErrorContains(t, err, `executor "missing" is unavailable`)
}

func TestManagerValidateSpecRunsBeforeSubmitAndStart_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{leaseExpiryPolicy: LeaseExpiryFail}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("validate-once"))
	require.NoError(t, err)
	assert.Equal(t, LeaseExpiryFail, task.LeaseExpiryPolicy)
	require.Len(t, executor.validated, 1)

	executor.validateErr = errors.New("invalid on worker")
	require.ErrorContains(t, manager.Execute(context.Background(), task.Spec.ID), "validate spec")
	require.Len(t, executor.validated, 2)
	pending, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StatePending, pending.Status)
}

func TestManagerExecutePersistsReturnedResultDirectly_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("complete"))
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	completed, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StateCompleted, completed.Status)
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
	task, err := manager.Submit(context.Background(), validSpec("failed"))
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	failed, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, failed.Status)
	assert.Len(t, failed.ResultError, 4096)
	assert.Nil(t, failed.ResultData)
}

func TestManagerValidatesCheckpointAndFallsBackToSpec_BitsUT(t *testing.T) {
	t.Run("compatible checkpoint is passed through", func(t *testing.T) {
		executor := &scriptedExecutor{}
		store, clock := recoveredTaskStore(t, "compatible", []byte("checkpoint"))
		manager := managerWithExecutor(t, store, executor, 5*time.Second)

		require.NoError(t, manager.Execute(context.Background(), "compatible"))
		require.Len(t, executor.executed, 1)
		assert.Equal(t, "checkpoint", string(executor.executed[0].Checkpoint))
		assert.Equal(t, time.Unix(106, 0), clock.Now())
	})

	t.Run("invalid checkpoint is unavailable to executor", func(t *testing.T) {
		executor := &scriptedExecutor{checkpointErr: errors.New("incompatible")}
		store, _ := recoveredTaskStore(t, "incompatible", []byte("bad"))
		manager := managerWithExecutor(t, store, executor, 5*time.Second)

		require.NoError(t, manager.Execute(context.Background(), "incompatible"))
		require.Len(t, executor.executed, 1)
		assert.Equal(t, int64(2), executor.executed[0].Attempt)
		assert.Nil(t, executor.executed[0].Checkpoint)
	})
}

func recoveredTaskStore(t *testing.T, id string, checkpoint []byte) (*InMemoryStore, *testClock) {
	t.Helper()
	clock := &testClock{now: time.Unix(100, 0)}
	store := NewInMemoryStore(&InMemoryStoreConfig{
		Clock: clock.Now, ActiveAttemptTimeout: 5 * time.Second,
	})
	started := createAndStart(t, store, id)
	store.mu.Lock()
	store.tasks[id].Checkpoint = checkpoint
	store.mu.Unlock()
	require.Equal(t, StateRunning, started.Status)
	clock.Advance(6 * time.Second)
	pending, err := store.Get(context.Background(), id)
	require.NoError(t, err)
	require.Equal(t, StatePending, pending.Status)
	return store, clock
}

func TestNonRestartableExecutorRejectsExactMissingCheckpointDiscriminator_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		checkpointErr: errors.New("corrupt"),
		execute: func(_ context.Context, task *Task, _ ExecutionRuntime) (*ExecutionResult, error) {
			if task.Attempt > 1 && len(task.Checkpoint) == 0 {
				return nil, errors.New("unsafe restart rejected")
			}
			return &ExecutionResult{Status: StatusCompleted, Data: []byte("ok")}, nil
		},
	}
	store, _ := recoveredTaskStore(t, "non-restartable", []byte("corrupt"))
	manager := managerWithExecutor(t, store, executor, 5*time.Second)

	require.NoError(t, manager.Execute(context.Background(), "non-restartable"))
	failed, err := store.Get(context.Background(), "non-restartable")
	require.NoError(t, err)
	assert.Equal(t, StateFailed, failed.Status)
	assert.Equal(t, "unsafe restart rejected", failed.ResultError)
}

func TestManagerResumeValidatesAndStoresNormalizedOpaqueInput_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{normalizedResume: []byte("normalized")}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	submitted, err := manager.Submit(context.Background(), validSpec("resume-manager"))
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
	assert.Equal(t, "checkpoint", string(executor.resumeCheckpoint))
	assert.Equal(t, "raw", string(executor.resumeInput))
	require.NotNil(t, resumed.PendingResume)
	assert.Equal(t, "normalized", string(resumed.PendingResume))
	assert.Equal(t, StatePending, resumed.Status)
}

func TestManagerWaitingInputPersistsCheckpointWithoutTerminalResult_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
			return &ExecutionResult{Status: StatusWaitingInput, Checkpoint: []byte("checkpoint")}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("input"))
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StateWaitingInput, waiting.Status)
	assert.Equal(t, "checkpoint", string(waiting.Checkpoint))
	assert.Empty(t, waiting.ResultData)
	assert.Empty(t, waiting.ResultError)
}

func TestManagerErrorDoesNotCreatePendingResume_BitsUT(t *testing.T) {
	executor := &scriptedExecutor{
		execute: func(_ context.Context, _ *Task, _ ExecutionRuntime) (*ExecutionResult, error) {
			return nil, errors.New("execution failed")
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	task, err := manager.Submit(context.Background(), validSpec("failed-after-input"))
	require.NoError(t, err)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	failed, err := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, StateFailed, failed.Status)
	assert.Equal(t, "execution failed", failed.ResultError)
	assert.Nil(t, failed.PendingResume)
}

func TestCheckpointUnavailableStopsRenewalWithoutPersistingFailure_BitsUT(t *testing.T) {
	clock := &testClock{now: time.Unix(500, 0)}
	store := NewInMemoryStore(&InMemoryStoreConfig{
		Clock: clock.Now, ActiveAttemptTimeout: 5 * time.Second,
	})
	executor := &scriptedExecutor{
		execute: func(context.Context, *Task, ExecutionRuntime) (*ExecutionResult, error) {
			return nil, ErrCheckpointUnavailable
		},
	}
	manager := managerWithExecutor(t, store, executor, 5*time.Second)
	task, err := manager.Submit(context.Background(), validSpec("drain"))
	require.NoError(t, err)

	err = manager.Execute(context.Background(), task.Spec.ID)
	assert.ErrorIs(t, err, ErrCheckpointUnavailable)
	running, getErr := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, getErr)
	assert.Equal(t, StateRunning, running.Status)
	assert.Empty(t, running.ResultData)
	assert.Empty(t, running.ResultError)

	clock.Advance(6 * time.Second)
	pending, getErr := store.Get(context.Background(), task.Spec.ID)
	require.NoError(t, getErr)
	assert.Equal(t, StatePending, pending.Status)
	assert.Empty(t, pending.ResultData)
	assert.Empty(t, pending.ResultError)
}
