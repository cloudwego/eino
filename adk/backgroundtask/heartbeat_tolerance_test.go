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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type heartbeatTestStore struct {
	TaskStore
	mu          sync.Mutex
	callTimes   []time.Time
	inFlight    int
	maxInFlight int
	heartbeat   func(int, context.Context, *HeartbeatRequest) (*Task, error)
	get         func(context.Context, string) (*Task, error)
}

func (s *heartbeatTestStore) Heartbeat(
	ctx context.Context,
	req *HeartbeatRequest,
) (*Task, error) {
	s.mu.Lock()
	call := len(s.callTimes) + 1
	s.callTimes = append(s.callTimes, time.Now())
	s.inFlight++
	if s.inFlight > s.maxInFlight {
		s.maxInFlight = s.inFlight
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.inFlight--
		s.mu.Unlock()
	}()
	return s.heartbeat(call, ctx, req)
}

func (s *heartbeatTestStore) Get(ctx context.Context, taskID string) (*Task, error) {
	if s.get != nil {
		return s.get(ctx, taskID)
	}
	return s.TaskStore.Get(ctx, taskID)
}

func (s *heartbeatTestStore) snapshotCalls() ([]time.Time, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]time.Time(nil), s.callTimes...), s.maxInFlight
}

func tolerantRuntime(
	tasks TaskStore,
	events TaskEventStore,
	started *Task,
	confirmedAt time.Time,
	interval time.Duration,
	leaseDuration time.Duration,
) *taskRuntime {
	return newTaskRuntimeWithConfig(taskRuntimeConfig{
		tasks:      tasks,
		taskEvents: events,
		taskID:     started.Spec.ID,
		attempt:    started.Attempt,
		version:    started.Version,
		lease: taskRuntimeLeaseConfig{
			confirmedAt:             confirmedAt,
			duration:                leaseDuration,
			safetyMargin:            interval / 2,
			tolerateHeartbeatErrors: true,
		},
	})
}

func TestHeartbeatResponseLossConfirmsSameAttemptAndReleasesWrites(t *testing.T) {
	base := NewInMemoryStore(&InMemoryStoreConfig{
		ActiveAttemptTimeout: time.Second,
	})
	started := createAndStart(t, base, "heartbeat-response-loss")
	temporaryErr := errors.New("response lost")
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		call int,
		ctx context.Context,
		req *HeartbeatRequest,
	) (*Task, error) {
		task, err := base.Heartbeat(ctx, req)
		if call == 1 && err == nil {
			return nil, temporaryErr
		}
		return task, err
	}
	runtime := tolerantRuntime(
		store, base, started, started.UpdatedAt, 20*time.Millisecond, time.Second,
	)
	initialExpiry := runtime.leaseExpiresAt

	require.ErrorIs(t, runtime.heartbeat(context.Background()), errHeartbeatRetry)
	require.Equal(t, initialExpiry, runtime.leaseExpiresAt)
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 5*time.Millisecond)
	require.ErrorIs(t, runtime.WaitForLease(waitCtx), context.DeadlineExceeded)
	cancelWait()

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- runtime.CommitStart(context.Background(), []byte("checkpoint"))
	}()
	select {
	case err := <-writeDone:
		t.Fatalf("versioned write returned while lease was uncertain: %v", err)
	case <-time.After(10 * time.Millisecond):
	}

	require.NoError(t, runtime.heartbeat(context.Background()))
	require.NoError(t, <-writeDone)
	current, err := base.Get(context.Background(), started.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, started.Attempt, current.Attempt)
	require.Equal(t, started.Version+2, current.Version)
	require.Greater(t, runtime.leaseExpiresAt, initialExpiry)
	calls, _ := store.snapshotCalls()
	require.Len(t, calls, 2)
	require.True(
		t,
		runtime.leaseExpiresAt.Before(calls[1].Add(time.Second-5*time.Millisecond)),
		"reconciliation must retain the first unconfirmed heartbeat's lease deadline",
	)
}

func TestHeartbeatTransientFailuresUseRegularCadenceAndSafetyDeadline(t *testing.T) {
	const (
		interval       = 20 * time.Millisecond
		leaseDuration  = 120 * time.Millisecond
		safetyMargin   = 10 * time.Millisecond
		timingSlack    = 8 * time.Millisecond
		completionWait = time.Second
	)
	base := NewInMemoryStore(&InMemoryStoreConfig{
		ActiveAttemptTimeout: leaseDuration,
	})
	started := createAndStart(t, base, "heartbeat-transient")
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		_ int,
		_ context.Context,
		_ *HeartbeatRequest,
	) (*Task, error) {
		return nil, errors.New("temporary storage failure")
	}
	confirmedAt := time.Now()
	runtime := newTaskRuntimeWithConfig(taskRuntimeConfig{
		tasks: store, taskEvents: base,
		taskID: started.Spec.ID, attempt: started.Attempt, version: started.Version,
		lease: taskRuntimeLeaseConfig{
			confirmedAt:             confirmedAt,
			duration:                leaseDuration,
			safetyMargin:            safetyMargin,
			tolerateHeartbeatErrors: true,
		},
	})
	manager := &Manager{heartbeatEvery: interval}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go manager.heartbeat(runCtx, cancel, runtime, make(chan struct{}), done)

	select {
	case <-done:
	case <-time.After(completionWait):
		t.Fatal("heartbeat loop did not stop at the lease safety deadline")
	}
	require.ErrorIs(t, runCtx.Err(), context.Canceled)
	runtime.mu.Lock()
	require.ErrorIs(t, runtime.poison, ErrLeaseLost)
	runtime.mu.Unlock()
	calls, maxInFlight := store.snapshotCalls()
	require.GreaterOrEqual(t, len(calls), 4)
	require.Equal(t, 1, maxInFlight)
	for i := 1; i < len(calls); i++ {
		require.GreaterOrEqual(t, calls[i].Sub(calls[i-1]), interval-timingSlack)
	}
	elapsed := time.Since(confirmedAt)
	require.GreaterOrEqual(t, elapsed, leaseDuration-safetyMargin-timingSlack)
}

func TestHeartbeatToleranceIsOptIn(t *testing.T) {
	base := NewInMemoryStore(nil)
	started := createAndStart(t, base, "heartbeat-opt-in")
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		_ int,
		_ context.Context,
		_ *HeartbeatRequest,
	) (*Task, error) {
		return nil, errors.New("temporary storage failure")
	}
	runtime := newTaskRuntime(
		store, base, started.Spec.ID, started.Attempt, started.Version, nil,
	)
	manager := &Manager{heartbeatEvery: time.Nanosecond}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go manager.heartbeat(runCtx, cancel, runtime, make(chan struct{}), done)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("legacy heartbeat handling did not stop after the first error")
	}
	require.ErrorIs(t, runCtx.Err(), context.Canceled)
	calls, _ := store.snapshotCalls()
	require.Len(t, calls, 1)
}

func TestHeartbeatSafetyDeadlineCancelsBlockedStoreRequest(t *testing.T) {
	const (
		interval      = 10 * time.Millisecond
		leaseDuration = 100 * time.Millisecond
		safetyMargin  = 20 * time.Millisecond
	)
	base := NewInMemoryStore(&InMemoryStoreConfig{
		ActiveAttemptTimeout: leaseDuration,
	})
	started := createAndStart(t, base, "heartbeat-blocked")
	entered := make(chan struct{})
	release := make(chan struct{})
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		_ int,
		ctx context.Context,
		req *HeartbeatRequest,
	) (*Task, error) {
		close(entered)
		<-release
		return base.Heartbeat(ctx, req)
	}
	runtime := newTaskRuntimeWithConfig(taskRuntimeConfig{
		tasks: store, taskEvents: base,
		taskID: started.Spec.ID, attempt: started.Attempt, version: started.Version,
		lease: taskRuntimeLeaseConfig{
			confirmedAt:             time.Now(),
			duration:                leaseDuration,
			safetyMargin:            safetyMargin,
			tolerateHeartbeatErrors: true,
		},
	})
	manager := &Manager{heartbeatEvery: interval}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go manager.heartbeat(runCtx, cancel, runtime, make(chan struct{}), done)

	<-entered
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- runtime.WaitForLease(context.Background())
	}()
	select {
	case err := <-gateDone:
		t.Fatalf("lease gate returned while heartbeat was blocked: %v", err)
	case <-time.After(10 * time.Millisecond):
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("blocked heartbeat prevented lease safety cancellation")
	}
	require.ErrorIs(t, runCtx.Err(), context.Canceled)
	require.ErrorIs(t, <-gateDone, ErrLeaseLost)
	_, maxInFlight := store.snapshotCalls()
	require.Equal(t, 1, maxInFlight)

	time.Sleep(leaseDuration)
	close(release)
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.inFlight == 0
	}, time.Second, time.Millisecond)
	current, err := base.Get(context.Background(), started.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, current.Status)
	require.Equal(t, started.Attempt, current.Attempt)
}

func TestManagerExecutionStopsWhenHeartbeatRequestHangs(t *testing.T) {
	const (
		interval      = 10 * time.Millisecond
		leaseDuration = 100 * time.Millisecond
	)
	base := NewInMemoryStore(&InMemoryStoreConfig{
		ActiveAttemptTimeout: leaseDuration,
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		_ int,
		ctx context.Context,
		req *HeartbeatRequest,
	) (*Task, error) {
		renewed, err := base.Heartbeat(ctx, req)
		close(entered)
		<-release
		return renewed, err
	}
	executionStarted := make(chan struct{})
	executor := &scriptedExecutor{
		execute: func(
			ctx context.Context,
			_ *Task,
			_ ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(executionStarted)
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	registry := NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := mustNewManager(t, context.Background(), &Config{
		Tasks:                            store,
		TaskEvents:                       base,
		Executors:                        registry,
		HeartbeatInterval:                interval,
		LeaseDuration:                    leaseDuration,
		TolerateTransientHeartbeatErrors: true,
	})
	spec := validSpec("heartbeat-blocked-execution")
	spec.SessionID = ""
	spec.NotifySession = false
	task, err := manager.Submit(context.Background(), &SubmitRequest{Spec: spec})
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), task.Spec.ID)
	}()

	<-executionStarted
	<-entered
	select {
	case err = <-executeDone:
		require.ErrorIs(t, err, ErrLeaseLost)
	case <-time.After(time.Second):
		t.Fatal("blocked heartbeat prevented execution cancellation")
	}

	time.Sleep(leaseDuration)
	close(release)
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.inFlight == 0
	}, time.Second, time.Millisecond)
	current, err := base.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusPending, current.Status)
	require.Equal(t, int64(1), current.Attempt)
}

func TestHeartbeatCancellationWinsBlockedRenewal(t *testing.T) {
	base := NewInMemoryStore(&InMemoryStoreConfig{
		ActiveAttemptTimeout: time.Second,
	})
	started := createAndStart(t, base, "heartbeat-cancel-race")
	entered := make(chan struct{})
	release := make(chan struct{})
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		_ int,
		ctx context.Context,
		req *HeartbeatRequest,
	) (*Task, error) {
		renewed, err := base.Heartbeat(ctx, req)
		close(entered)
		<-release
		return renewed, err
	}
	runtime := tolerantRuntime(
		store, base, started, time.Now(), 10*time.Millisecond, time.Second,
	)
	ready := make(chan struct{})
	close(ready)
	manager := &Manager{
		tasks: store,
		activeAttempts: map[string]*activeAttempt{
			started.Spec.ID: {runtime: runtime, ready: ready},
		},
	}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go (&Manager{heartbeatEvery: 10 * time.Millisecond}).heartbeat(
		runCtx, cancel, runtime, make(chan struct{}), done,
	)

	<-entered
	gateDone := make(chan error, 1)
	go func() {
		gateDone <- runtime.WaitForLease(context.Background())
	}()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- runtime.CommitStart(context.Background(), []byte("checkpoint"))
	}()
	requested, err := manager.RequestCancel(
		context.Background(),
		started.Spec.ID,
		WithCancellationReason("operator request"),
	)
	require.NoError(t, err)
	require.Equal(t, "operator request", requested.CancelReason)
	require.Equal(t, ControlRequest{
		Kind: ControlStop, Reason: "operator request",
	}, <-runtime.Controls())
	require.ErrorIs(t, <-gateDone, ErrLeaseLost)
	require.ErrorIs(t, <-writeDone, ErrLeaseLost)
	committed, err := runtime.commit(context.Background(), &ExecutionResult{
		Status: StatusCanceled,
	})
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, committed.Status)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat loop did not stop after explicit cancellation")
	}
	require.NoError(t, runCtx.Err())
	close(release)
	require.Eventually(t, func() bool {
		store.mu.Lock()
		defer store.mu.Unlock()
		return store.inFlight == 0
	}, time.Second, time.Millisecond)
}

func TestHeartbeatRejectsTakeoverImmediately(t *testing.T) {
	base := NewInMemoryStore(nil)
	started := createAndStart(t, base, "heartbeat-takeover")
	store := &heartbeatTestStore{
		TaskStore: base,
		heartbeat: func(
			_ int,
			_ context.Context,
			_ *HeartbeatRequest,
		) (*Task, error) {
			return nil, ErrVersionConflict
		},
		get: func(context.Context, string) (*Task, error) {
			takenOver := cloneTask(started)
			takenOver.Attempt++
			takenOver.Version++
			return takenOver, nil
		},
	}
	runtime := tolerantRuntime(
		store, base, started, time.Now(), 10*time.Millisecond, time.Second,
	)

	require.ErrorIs(t, runtime.heartbeat(context.Background()), ErrLeaseLost)
	runtime.mu.Lock()
	require.ErrorIs(t, runtime.poison, ErrLeaseLost)
	runtime.mu.Unlock()
}

func TestHeartbeatRejectsDefinitiveLeaseStatesImmediately(t *testing.T) {
	tests := []struct {
		name         string
		heartbeatErr error
		current      func(*Task) *Task
	}{
		{name: "lease lost", heartbeatErr: ErrLeaseLost},
		{
			name:         "terminal",
			heartbeatErr: ErrVersionConflict,
			current: func(started *Task) *Task {
				task := cloneTask(started)
				task.Status = StatusCompleted
				task.Version++
				return task
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base := NewInMemoryStore(nil)
			started := createAndStart(t, base, "heartbeat-definitive-"+test.name)
			store := &heartbeatTestStore{
				TaskStore: base,
				heartbeat: func(
					_ int,
					_ context.Context,
					_ *HeartbeatRequest,
				) (*Task, error) {
					return nil, test.heartbeatErr
				},
			}
			if test.current != nil {
				store.get = func(context.Context, string) (*Task, error) {
					return test.current(started), nil
				}
			}
			runtime := tolerantRuntime(
				store, base, started, time.Now(), 10*time.Millisecond, time.Second,
			)

			require.ErrorIs(t, runtime.heartbeat(context.Background()), ErrLeaseLost)
			runtime.mu.Lock()
			require.ErrorIs(t, runtime.poison, ErrLeaseLost)
			runtime.mu.Unlock()
		})
	}
}

func TestHeartbeatShutdownWaitsForLeaseConfirmation(t *testing.T) {
	const interval = 20 * time.Millisecond
	base := NewInMemoryStore(&InMemoryStoreConfig{
		ActiveAttemptTimeout: time.Second,
	})
	started := createAndStart(t, base, "heartbeat-shutdown")
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})
	store := &heartbeatTestStore{TaskStore: base}
	store.heartbeat = func(
		call int,
		ctx context.Context,
		req *HeartbeatRequest,
	) (*Task, error) {
		if call == 1 {
			close(firstStarted)
			<-firstRelease
			return nil, errors.New("temporary storage failure")
		}
		close(secondStarted)
		return base.Heartbeat(ctx, req)
	}
	runtime := tolerantRuntime(
		store, base, started, time.Now(), interval, time.Second,
	)
	manager := &Manager{heartbeatEvery: interval}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stop := make(chan struct{})
	done := make(chan struct{})
	go manager.heartbeat(runCtx, cancel, runtime, stop, done)

	<-firstStarted
	close(stop)
	time.Sleep(2 * interval)
	firstReleasedAt := time.Now()
	close(firstRelease)
	select {
	case <-done:
		t.Fatal("heartbeat loop stopped before the uncertain lease was confirmed")
	case <-time.After(interval / 2):
	}
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("heartbeat loop did not perform the next scheduled renewal")
	}
	secondStartedAt := time.Now()
	require.GreaterOrEqual(
		t,
		secondStartedAt.Sub(firstReleasedAt),
		interval-5*time.Millisecond,
	)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat loop did not stop after lease confirmation")
	}
	require.NoError(t, runCtx.Err())
	committed, err := runtime.commit(context.Background(), &ExecutionResult{
		Status: StatusCompleted,
		Data:   []byte("done"),
	})
	require.NoError(t, err)
	require.Equal(t, StatusCompleted, committed.Status)
	calls, maxInFlight := store.snapshotCalls()
	require.Len(t, calls, 2)
	require.Equal(t, 1, maxInFlight)
}
