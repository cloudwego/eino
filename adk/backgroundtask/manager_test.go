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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/internal/taskcontrol"
)

func closeWithTimeout(manager *Manager) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = manager.Close(ctx)
}

func TestManagerReadOutputDelegatesToStore_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := New(context.Background(), &Config{Store: store})
	defer closeWithTimeout(manager)
	started := createAndStart(t, store, "manager-output")
	_, err := store.AppendOutput(context.Background(), &AppendOutputRequest{
		TaskID: started.Spec.ID, Attempt: started.Attempt, Data: []byte("record"),
	})
	require.NoError(t, err)

	result, err := manager.ReadOutput(context.Background(), &ReadOutputRequest{
		TaskID: started.Spec.ID,
	})
	require.NoError(t, err)
	require.Len(t, result.Records, 1)
	require.Equal(t, "record", string(result.Records[0].Data))
}

func TestManagerExecuteBindsTimeoutController_BitsUT(t *testing.T) {
	observed := make(chan ControlRequest, 1)
	executor := &scriptedExecutor{
		execute: func(
			_ context.Context,
			_ *Task,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			control := <-runtime.Controls()
			observed <- control
			return &ExecutionResult{Status: StatusFailed, Error: control.Reason}, nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	task, err := manager.Submit(context.Background(), validSpec("timeout"))
	require.NoError(t, err)

	executeCtx, timeoutController := taskcontrol.WithTimeoutController(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- manager.Execute(executeCtx, task.Spec.ID)
	}()

	err = timeoutController.RequestTimeout(context.Background(), "")
	require.EqualError(t, err, "taskcontrol: timeout reason is required")
	require.NoError(t, timeoutController.RequestTimeout(
		context.Background(), "timed out after 10ms",
	))
	require.NoError(t, <-done)
	control := <-observed
	require.Equal(t, ControlTimeout, control.Kind)
	require.Equal(t, "timed out after 10ms", control.Reason)

	failed, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusFailed, failed.Status)
	require.Equal(t, "timed out after 10ms", failed.ResultError)
	require.Nil(t, failed.CancelRequestedAt)
	require.ErrorIs(
		t,
		timeoutController.RequestTimeout(context.Background(), "too late"),
		taskcontrol.ErrClosed,
	)
}

func TestManagerExecuteClosesTimeoutControllerOnEarlyFailure_BitsUT(t *testing.T) {
	manager := New(context.Background(), nil)
	defer closeWithTimeout(manager)
	executeCtx, timeoutController := taskcontrol.WithTimeoutController(context.Background())

	err := manager.Execute(executeCtx, "")
	require.EqualError(t, err, "backgroundtask: execute task id is required")
	require.ErrorIs(
		t,
		timeoutController.RequestTimeout(context.Background(), "too late"),
		taskcontrol.ErrClosed,
	)
}

func TestManagerLoadOrRegisterExecutorIsAtomic_BitsUT(t *testing.T) {
	manager := New(context.Background(), nil)
	defer closeWithTimeout(manager)

	const callers = 32
	type result struct {
		actual Executor
		loaded bool
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, callers)
	var group sync.WaitGroup
	for i := 0; i < callers; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			actual, loaded, err := manager.LoadOrRegisterExecutor(
				&scriptedExecutor{key: "atomic"},
			)
			results <- result{actual: actual, loaded: loaded, err: err}
		}()
	}
	close(start)
	group.Wait()
	close(results)

	var registered *scriptedExecutor
	var registrations int
	for result := range results {
		require.NoError(t, result.err)
		actual, ok := result.actual.(*scriptedExecutor)
		require.True(t, ok)
		if registered == nil {
			registered = actual
		} else {
			require.Same(t, registered, actual)
		}
		if !result.loaded {
			registrations++
		}
	}
	require.Equal(t, 1, registrations)

	require.NoError(t, manager.Close(context.Background()))
	_, _, err := manager.LoadOrRegisterExecutor(&scriptedExecutor{key: "closed"})
	require.ErrorContains(t, err, "has shut down")
}

type recordingNotificationDelivery struct {
	request *NotificationDeliveryValidation
}

type lifecycleStoreOnly struct {
	Store
}

func (r *recordingNotificationDelivery) ValidateNotificationDelivery(
	_ context.Context,
	request *NotificationDeliveryValidation,
) error {
	r.request = request
	return nil
}

func TestManagerValidateNotificationDeliveryReportsOwnedStoreCapabilities_BitsUT(t *testing.T) {
	store := NewInMemoryStore(nil)
	manager := New(context.Background(), &Config{Store: store})
	defer closeWithTimeout(manager)
	runtime := &recordingNotificationDelivery{}

	require.NoError(t, manager.ValidateNotificationDelivery(
		context.Background(), runtime, SessionInboxNotificationKind,
	))
	require.NotNil(t, runtime.request)
	require.True(t, runtime.request.OutboxAvailable)
	require.Equal(t, SessionInboxNotificationKind, runtime.request.TargetKind)

	managerWithoutOutbox := New(context.Background(), &Config{
		Store: lifecycleStoreOnly{Store: NewInMemoryStore(nil)},
	})
	defer closeWithTimeout(managerWithoutOutbox)
	runtimeWithoutOutbox := &recordingNotificationDelivery{}
	require.NoError(t, managerWithoutOutbox.ValidateNotificationDelivery(
		context.Background(), runtimeWithoutOutbox, SessionInboxNotificationKind,
	))
	require.False(t, runtimeWithoutOutbox.request.OutboxAvailable)

	err := manager.ValidateNotificationDelivery(
		context.Background(), nil, SessionInboxNotificationKind,
	)
	require.EqualError(
		t, err,
		"backgroundtask: notification delivery runtime and target kind are required",
	)
}
