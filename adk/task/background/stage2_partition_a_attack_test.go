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

package background

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	taskcore "github.com/cloudwego/eino/adk/task"
)

// TestAttack_PublishCompleteCancelRaceHasOneVisibleTerminal attacks the
// three-way publication, completion, and cancellation race. Publishing a
// terminal result twice or exposing a deferred terminal task would give the
// parent contradictory outcomes. Exactly one lifecycle transition must win,
// and notifications must agree with the atomic publication result.
func TestAttack_PublishCompleteCancelRaceHasOneVisibleTerminal(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		store := NewInMemoryStore(nil)
		created, err := store.Create(context.Background(), &CreateTaskRequest{
			Spec: Spec{
				ID: "race", ExecutorKey: "test", Kind: "test",
				RootSessionID: "session", NotifySession: true,
			},
			Publication:       PublicationDeferred,
			LeaseExpiryPolicy: LeaseExpiryRetry,
		})
		require.NoError(t, err)
		started, err := store.Start(context.Background(), &StartTaskRequest{
			TaskID: created.Spec.ID, ExpectedVersion: created.Version,
		})
		require.NoError(t, err)

		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(3)
		var publishErr, completeErr, cancelErr error
		go func() {
			defer group.Done()
			<-start
			_, publishErr = store.Publish(context.Background(), &PublishTaskRequest{
				TaskID: started.Spec.ID, ExpectedVersion: started.Version,
			})
		}()
		go func() {
			defer group.Done()
			<-start
			_, completeErr = store.CompleteIfNoInputs(
				context.Background(),
				&CompleteIfNoInputsRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
					Attempt: started.Attempt, InputCursor: 0,
					ResultData: []byte("done"),
				},
			)
		}()
		go func() {
			defer group.Done()
			<-start
			_, cancelErr = store.RequestCancel(
				context.Background(),
				&RequestCancelRequest{
					TaskID: started.Spec.ID, ExpectedVersion: started.Version,
					Reason: "stop",
				},
			)
		}()
		close(start)
		group.Wait()

		terminalWinners := 0
		if completeErr == nil {
			terminalWinners++
		} else {
			require.ErrorIs(t, completeErr, ErrVersionConflict)
		}
		if cancelErr == nil {
			terminalWinners++
		} else {
			require.ErrorIs(t, cancelErr, ErrVersionConflict)
		}
		require.Equal(t, 1, terminalWinners)

		current, err := store.Get(context.Background(), started.Spec.ID)
		require.NoError(t, err)
		if current.Status == StatusRunning {
			require.NotNil(t, current.CancelRequestedAt)
			current, err = store.AckCancel(
				context.Background(),
				&AckCancelRequest{
					TaskID: current.Spec.ID, ExpectedVersion: current.Version,
				},
			)
			require.NoError(t, err)
		}
		require.Contains(t, []Status{StatusCompleted, StatusCanceled}, current.Status)

		notifications := receiveAllNotifications(t, store)
		if publishErr == nil {
			require.Equal(t, PublicationOnBackground, current.Publication)
			require.Len(t, notifications, 2)
			require.Equal(t, NotificationTaskBackgrounded, notifications[0].Kind)
			require.Equal(t, eventForStatus(current.Status), notifications[1].Kind)
		} else {
			require.ErrorIs(t, publishErr, ErrVersionConflict)
			require.Equal(t, PublicationDeferred, current.Publication)
			require.Empty(t, notifications)
		}
	}
}

// TestAttack_SuspendedInputAndReleaseRacePreservesInput attacks the distinction
// between planned suspension and waiting for input. If ordinary input wakes a
// suspended task, completion-barrier policy is bypassed; if release drops the
// racing input, the resumed attempt loses user data. The result must be pending
// with the input durably queued exactly once.
func TestAttack_SuspendedInputAndReleaseRacePreservesInput(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		store := NewInMemoryStore(nil)
		manager := mustNewManager(t, context.Background(), &Config{
			Tasks: store, TaskEvents: store,
		})
		started := createAndStart(t, store, "suspended")
		suspended, err := store.SuspendIfNoInputs(
			context.Background(),
			&SuspendIfNoInputsRequest{
				TaskID: started.Spec.ID, ExpectedVersion: started.Version,
				Attempt: started.Attempt, InputCursor: 0,
				Checkpoint: []byte("checkpoint"),
			},
		)
		require.NoError(t, err)
		handle, err := manager.Handle(started.Spec.ID)
		require.NoError(t, err)

		start := make(chan struct{})
		var group sync.WaitGroup
		group.Add(2)
		var inputErr, releaseErr error
		go func() {
			defer group.Done()
			<-start
			inputErr = handle.SendInput(context.Background(), &taskcore.Input{
				EventID: "resume", Kind: "resume", Data: []byte("payload"),
			})
		}()
		go func() {
			defer group.Done()
			<-start
			_, releaseErr = manager.ReleaseSuspension(
				context.Background(),
				suspended.Spec.ID,
			)
		}()
		close(start)
		group.Wait()

		require.NoError(t, inputErr)
		require.NoError(t, releaseErr)
		current, err := store.Get(context.Background(), suspended.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, StatusPending, current.Status)
		require.Equal(t, suspended.Version+1, current.Version)
		inputs, err := store.ListInputs(
			context.Background(),
			&taskcore.ListInputsRequest{TaskID: suspended.Spec.ID},
		)
		require.NoError(t, err)
		require.Len(t, inputs.Inputs, 1)
		require.Equal(t, "resume", inputs.Inputs[0].EventID)
		require.Equal(t, int64(0), inputs.ConsumedCursor)
	}
}

// TestAttack_ForegroundMailboxFinalizationLinearizesWithInput attacks input
// arriving during SealIfIdle and Abandon. A successful idle seal must reject
// the late input, a prior input must prevent sealing, and abandon must leave no
// unconsumed input regardless of ordering.
func TestAttack_ForegroundMailboxFinalizationLinearizesWithInput(t *testing.T) {
	t.Run("seal", func(t *testing.T) {
		for iteration := 0; iteration < 100; iteration++ {
			store := NewInMemoryStore(nil)
			mailbox := registerMailboxForTest(t, store, "seal")
			start := make(chan struct{})
			var group sync.WaitGroup
			group.Add(2)
			var sendErr, sealErr error
			go func() {
				defer group.Done()
				<-start
				_, sendErr = store.SendInput(
					context.Background(),
					&taskcore.SendInputRequest{
						TaskID: mailbox.TaskID,
						Input:  taskcore.Input{EventID: "input", Kind: "event"},
					},
				)
			}()
			go func() {
				defer group.Done()
				<-start
				_, sealErr = store.SealIfIdle(
					context.Background(),
					&taskcore.SealMailboxRequest{
						TaskID:             mailbox.TaskID,
						ExpectedGeneration: mailbox.Generation,
					},
				)
			}()
			close(start)
			group.Wait()

			current, err := store.GetMailbox(context.Background(), mailbox.TaskID)
			require.NoError(t, err)
			switch {
			case sealErr == nil:
				require.ErrorIs(t, sendErr, taskcore.ErrMailboxSealed)
				require.Equal(t, taskcore.MailboxSealed, current.State)
				require.Zero(t, current.LatestSequence)
			case errors.Is(sealErr, taskcore.ErrInputsPending):
				require.NoError(t, sendErr)
				require.Equal(t, taskcore.MailboxForeground, current.State)
				require.Equal(t, int64(1), current.LatestSequence)
				require.Zero(t, current.ConsumedCursor)
			default:
				require.NoError(t, sealErr)
			}
		}
	})

	t.Run("abandon", func(t *testing.T) {
		for iteration := 0; iteration < 100; iteration++ {
			store := NewInMemoryStore(nil)
			mailbox := registerMailboxForTest(t, store, "abandon")
			start := make(chan struct{})
			var group sync.WaitGroup
			group.Add(2)
			var sendErr, abandonErr error
			go func() {
				defer group.Done()
				<-start
				_, sendErr = store.SendInput(
					context.Background(),
					&taskcore.SendInputRequest{
						TaskID: mailbox.TaskID,
						Input:  taskcore.Input{EventID: "input", Kind: "event"},
					},
				)
			}()
			go func() {
				defer group.Done()
				<-start
				_, abandonErr = store.Abandon(
					context.Background(),
					&taskcore.AbandonMailboxRequest{
						TaskID:             mailbox.TaskID,
						ExpectedGeneration: mailbox.Generation,
					},
				)
			}()
			close(start)
			group.Wait()

			require.NoError(t, abandonErr)
			require.True(t, sendErr == nil || errors.Is(sendErr, taskcore.ErrMailboxSealed))
			current, err := store.GetMailbox(context.Background(), mailbox.TaskID)
			require.NoError(t, err)
			require.Equal(t, taskcore.MailboxSealed, current.State)
			require.Equal(t, current.LatestSequence, current.ConsumedCursor)
			inputs, err := store.ListInputs(
				context.Background(),
				&taskcore.ListInputsRequest{TaskID: mailbox.TaskID},
			)
			require.NoError(t, err)
			require.Len(t, inputs.Inputs, int(current.LatestSequence))
		}
	})
}

// TestAttack_TaskEventReplayAndFinalRemainAttemptFenced attacks replay across
// attempt handoff and two concurrent identical final appends. Replay metadata
// must survive the handoff, but stale attempts must not use it to bypass
// authorization. Exactly one final append is inserted and later parts remain
// rejected.
func TestAttack_TaskEventReplayAndFinalRemainAttemptFenced(t *testing.T) {
	store := NewInMemoryStore(nil)
	firstAttempt := createAndStart(t, store, "event-replay")
	first, err := store.AppendTaskEvent(
		context.Background(),
		&AppendTaskEventRequest{
			TaskID: firstAttempt.Spec.ID, Attempt: firstAttempt.Attempt,
			EventID: "stream", PartID: "chunk-0", Data: []byte("one"),
		},
	)
	require.NoError(t, err)
	require.True(t, first.Inserted)

	yielded, err := store.Yield(context.Background(), &YieldTaskRequest{
		TaskID: firstAttempt.Spec.ID, ExpectedVersion: firstAttempt.Version,
	})
	require.NoError(t, err)
	secondAttempt, err := store.Start(context.Background(), &StartTaskRequest{
		TaskID: yielded.Spec.ID, ExpectedVersion: yielded.Version,
	})
	require.NoError(t, err)
	replayed, err := store.AppendTaskEvent(
		context.Background(),
		&AppendTaskEventRequest{
			TaskID: secondAttempt.Spec.ID, Attempt: secondAttempt.Attempt,
			EventID: "stream", PartID: "chunk-0", Data: []byte("one"),
		},
	)
	require.NoError(t, err)
	require.False(t, replayed.Inserted)
	_, err = store.AppendTaskEvent(
		context.Background(),
		&AppendTaskEventRequest{
			TaskID: firstAttempt.Spec.ID, Attempt: firstAttempt.Attempt,
			EventID: "stream", PartID: "chunk-0", Data: []byte("one"),
		},
	)
	require.ErrorIs(t, err, ErrLeaseLost)

	start := make(chan struct{})
	results := make(chan *AppendTaskEventResult, 2)
	errs := make(chan error, 2)
	var group sync.WaitGroup
	for index := 0; index < 2; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			result, appendErr := store.AppendTaskEvent(
				context.Background(),
				&AppendTaskEventRequest{
					TaskID: secondAttempt.Spec.ID, Attempt: secondAttempt.Attempt,
					EventID: "stream", PartID: "end",
					Data: []byte("done"), Final: true,
				},
			)
			results <- result
			errs <- appendErr
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errs)

	inserted := 0
	for appendErr := range errs {
		require.NoError(t, appendErr)
	}
	for result := range results {
		require.NotNil(t, result)
		if result.Inserted {
			inserted++
		}
	}
	require.Equal(t, 1, inserted)
	_, err = store.AppendTaskEvent(
		context.Background(),
		&AppendTaskEventRequest{
			TaskID: firstAttempt.Spec.ID, Attempt: firstAttempt.Attempt,
			EventID: "stream", PartID: "end",
			Data: []byte("done"), Final: true,
		},
	)
	require.ErrorIs(t, err, ErrLeaseLost)
	_, err = store.AppendTaskEvent(
		context.Background(),
		&AppendTaskEventRequest{
			TaskID: secondAttempt.Spec.ID, Attempt: secondAttempt.Attempt,
			EventID: "stream", PartID: "late", Data: []byte("late"),
		},
	)
	require.ErrorIs(t, err, ErrTaskEventClosed)

	page, err := store.ListTaskEvents(
		context.Background(),
		&ListTaskEventsRequest{TaskID: secondAttempt.Spec.ID},
	)
	require.NoError(t, err)
	require.Equal(t, []string{"chunk-0", "end"}, taskEventPartIDs(page.Parts))
	require.True(t, page.Parts[1].Final)
}

// TestAttack_RequestCancelHookTimeoutKeepsDurableIntent attacks a cancellation
// hook that consumes the request deadline. Losing the durable intent would
// orphan work, while poisoning the attempt would prevent a later retry. The
// first call must return the running snapshot plus timeout, and a fresh call
// must retry the hook and drive the same attempt to canceled.
func TestAttack_RequestCancelHookTimeoutKeepsDurableIntent(t *testing.T) {
	started := make(chan struct{})
	observedControl := make(chan ControlRequest, 1)
	var hookCalls int64
	hookReasons := make(chan string, 2)
	executor := &cancellationAcknowledgingExecutor{
		scriptedExecutor: &scriptedExecutor{execute: func(
			_ context.Context,
			_ *TaskSnapshot,
			runtime ExecutionRuntime,
		) (*ExecutionResult, error) {
			close(started)
			control := <-runtime.Controls()
			observedControl <- control
			return &ExecutionResult{Action: ExecutionActionCancel}, nil
		}},
		acknowledge: func(
			ctx context.Context,
			_ *TaskSnapshot,
			reason string,
		) error {
			hookReasons <- reason
			if atomic.AddInt64(&hookCalls, 1) == 1 {
				<-ctx.Done()
				return ctx.Err()
			}
			return nil
		},
	}
	store := NewInMemoryStore(nil)
	manager := managerWithExecutor(t, store, executor, time.Minute)
	defer closeWithTimeout(manager)
	submitted, err := manager.Submit(
		context.Background(),
		&SubmitRequest{Spec: validSpec("cancel-hook-timeout")},
	)
	require.NoError(t, err)
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- manager.Execute(context.Background(), submitted.Spec.ID)
	}()
	<-started

	cancelCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	requested, err := manager.RequestCancel(
		cancelCtx,
		submitted.Spec.ID,
		WithCancellationReason("operator stop"),
	)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.NotNil(t, requested)
	require.Equal(t, StatusRunning, requested.Status)
	require.NotNil(t, requested.CancelRequestedAt)
	stored, getErr := manager.Get(context.Background(), submitted.Spec.ID)
	require.NoError(t, getErr)
	require.NotNil(t, stored.CancelRequestedAt)
	require.Equal(t, "operator stop", stored.CancelReason)
	select {
	case control := <-observedControl:
		t.Fatalf("stop control arrived before cancellation hook succeeded: %+v", control)
	default:
	}

	retried, err := manager.RequestCancel(context.Background(), submitted.Spec.ID)
	require.NoError(t, err)
	require.NotNil(t, retried.CancelRequestedAt)
	require.Equal(t, int64(2), atomic.LoadInt64(&hookCalls))
	require.Equal(t, "operator stop", <-hookReasons)
	require.Equal(t, "operator stop", <-hookReasons)
	require.Equal(t, ControlRequest{
		Kind: ControlStop, Reason: "operator stop",
	}, <-observedControl)
	require.NoError(t, <-executeDone)
	terminal, err := manager.Get(context.Background(), submitted.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, StatusCanceled, terminal.Status)
	require.Equal(t, "operator stop", terminal.ResultError)
}

// TestAttack_HandleCancelAndCompletionResolveOneOutcome attacks the public
// Handle while completion and cancellation race. Cancel must remain idempotent,
// Wait must return the authoritative terminal state, and the mailbox must seal
// regardless of which side wins.
func TestAttack_HandleCancelAndCompletionResolveOneOutcome(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		started := make(chan struct{})
		finish := make(chan struct{})
		executor := &scriptedExecutor{
			leaseExpiryPolicy: LeaseExpiryFail,
			execute: func(
				_ context.Context,
				_ *TaskSnapshot,
				runtime ExecutionRuntime,
			) (*ExecutionResult, error) {
				close(started)
				select {
				case <-finish:
					return &ExecutionResult{Action: ExecutionActionComplete}, nil
				case control := <-runtime.Controls():
					require.Equal(t, ControlStop, control.Kind)
					return &ExecutionResult{Action: ExecutionActionCancel}, nil
				}
			},
		}
		store := NewInMemoryStore(nil)
		manager := managerWithExecutor(t, store, executor, time.Minute)
		submitted, err := manager.Submit(
			context.Background(),
			&SubmitRequest{Spec: validSpec("handle-race")},
		)
		require.NoError(t, err)
		handle, err := manager.Handle(submitted.Spec.ID)
		require.NoError(t, err)
		executeDone := make(chan error, 1)
		go func() {
			executeDone <- manager.Execute(context.Background(), submitted.Spec.ID)
		}()
		<-started

		start := make(chan struct{})
		cancelDone := make(chan error, 1)
		go func() {
			<-start
			cancelDone <- handle.Cancel(context.Background(), "stop")
		}()
		go func() {
			<-start
			close(finish)
		}()
		close(start)

		require.NoError(t, <-cancelDone)
		require.NoError(t, <-executeDone)
		outcome, err := handle.Wait(context.Background())
		require.NoError(t, err)
		require.Contains(t, []taskcore.OutcomeStatus{
			taskcore.OutcomeCompleted,
			taskcore.OutcomeCanceled,
		}, outcome.Status)
		current, err := store.Get(context.Background(), submitted.Spec.ID)
		require.NoError(t, err)
		require.True(t, terminalStatus(current.Status))
		mailbox, err := store.GetMailbox(context.Background(), submitted.Spec.ID)
		require.NoError(t, err)
		require.Equal(t, taskcore.MailboxSealed, mailbox.State)
		require.NoError(t, manager.Close(context.Background()))
	}
}
