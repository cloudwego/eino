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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
)

type stage3AdoptExecutor struct {
	validateErr error
	validated   []Spec
}

func (*stage3AdoptExecutor) Key() string { return "stage3-adopt" }

func (*stage3AdoptExecutor) LeaseExpiryPolicy() LeaseExpiryPolicy {
	return LeaseExpiryFail
}

func (e *stage3AdoptExecutor) ValidateSpec(spec Spec) error {
	e.validated = append(e.validated, cloneSpec(spec))
	return e.validateErr
}

func (*stage3AdoptExecutor) ValidateExecution(
	context.Context,
	*TaskSnapshot,
) error {
	return nil
}

func (*stage3AdoptExecutor) SupportsDrain() bool { return false }

func (*stage3AdoptExecutor) Execute(
	context.Context,
	*TaskSnapshot,
	ExecutionRuntime,
) (*ExecutionResult, error) {
	return &ExecutionResult{Action: ExecutionActionComplete}, nil
}

func registerStage3Foreground(
	t *testing.T,
	store *InMemoryStore,
	taskID string,
) *task.Mailbox {
	t.Helper()
	registered, err := store.Register(
		context.Background(),
		&task.RegisterMailboxRequest{
			CandidateTaskID: taskID,
			InvocationID:    "invocation-" + taskID,
			Identity:        []byte("identity-" + taskID),
			RootSessionID:   "session-" + taskID,
		},
	)
	require.NoError(t, err)
	require.True(t, registered.Created)
	require.NotNil(t, registered.Mailbox)
	return registered.Mailbox
}

func stage3AdoptRequest(
	mailbox *task.Mailbox,
	startPending bool,
) *AdoptForegroundStoreRequest {
	return &AdoptForegroundStoreRequest{
		AdoptForegroundRequest: AdoptForegroundRequest{
			Spec: Spec{
				ID: mailbox.TaskID, ExecutorKey: "stage3-adopt",
				Kind: "subagent", Payload: []byte("payload"),
				RootSessionID: mailbox.RootSessionID,
			},
			ExpectedGeneration: mailbox.Generation,
			InputCursor:        mailbox.ConsumedCursor,
			InitialCheckpoint:  []byte("checkpoint"),
			StartPending:       startPending,
		},
		LeaseExpiryPolicy: LeaseExpiryRetry,
		ContextSnapshot:   []byte("context"),
	}
}

func TestManagerAdoptForeground(t *testing.T) {
	t.Run("validates manager request and executor", func(t *testing.T) {
		var nilManager *Manager
		snapshot, err := nilManager.AdoptForeground(
			context.Background(),
			&AdoptForegroundRequest{},
		)
		require.Nil(t, snapshot)
		require.EqualError(
			t,
			err,
			"task/background: foreground adoption store is required",
		)

		manager, err := New(context.Background(), nil)
		require.NoError(t, err)
		snapshot, err = manager.AdoptForeground(context.Background(), nil)
		require.Nil(t, snapshot)
		require.EqualError(
			t,
			err,
			"task/background: foreground adoption request is required",
		)
		snapshot, err = manager.AdoptForeground(
			context.Background(),
			&AdoptForegroundRequest{
				Spec: Spec{ID: "task", ExecutorKey: "missing"},
			},
		)
		require.Nil(t, snapshot)
		require.EqualError(
			t,
			err,
			`task/background: executor "missing" is unavailable`,
		)

		wantErr := errors.New("invalid executor payload")
		executor := &stage3AdoptExecutor{validateErr: wantErr}
		_, _, err = manager.LoadOrRegisterExecutor(executor)
		require.NoError(t, err)
		snapshot, err = manager.AdoptForeground(
			context.Background(),
			&AdoptForegroundRequest{
				Spec: Spec{ID: "task", ExecutorKey: executor.Key()},
			},
		)
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, wantErr)
		require.EqualError(
			t,
			err,
			"task/background: validate spec: invalid executor payload",
		)
		require.Len(t, executor.validated, 1)
	})

	t.Run("passes validated policy to the store", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		mailbox := registerStage3Foreground(t, store, "manager-valid")
		manager, err := New(context.Background(), &Config{
			Tasks: store, TaskEvents: store,
		})
		require.NoError(t, err)
		executor := &stage3AdoptExecutor{}
		_, _, err = manager.LoadOrRegisterExecutor(executor)
		require.NoError(t, err)

		snapshot, err := manager.AdoptForeground(
			context.Background(),
			&AdoptForegroundRequest{
				Spec: Spec{
					ID: mailbox.TaskID, ExecutorKey: executor.Key(),
					RootSessionID: mailbox.RootSessionID,
				},
				ExpectedGeneration: mailbox.Generation,
				InitialCheckpoint:  []byte("checkpoint"),
			},
		)
		require.NoError(t, err)
		require.Equal(t, StatusSuspended, snapshot.Status)
		require.Equal(t, PublicationOnBackground, snapshot.Publication)
		require.Equal(t, LeaseExpiryFail, snapshot.LeaseExpiryPolicy)
		require.Equal(t, []byte("checkpoint"), snapshot.Checkpoint)
		require.Len(t, executor.validated, 1)
	})
}

func TestInMemoryStoreAdoptForegroundTransitions(t *testing.T) {
	t.Run("valid adoption suspends and transfers ownership", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		mailbox := registerStage3Foreground(t, store, "suspended")
		request := stage3AdoptRequest(mailbox, false)

		snapshot, err := store.AdoptForeground(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, StatusSuspended, snapshot.Status)
		require.Equal(t, PublicationOnBackground, snapshot.Publication)
		require.Equal(t, LeaseExpiryRetry, snapshot.LeaseExpiryPolicy)
		require.Equal(t, int64(1), snapshot.Version)
		require.Zero(t, snapshot.Attempt)
		require.Equal(t, []byte("checkpoint"), snapshot.Checkpoint)
		require.Equal(t, []byte("context"), snapshot.ContextSnapshot)

		adoptedMailbox, err := store.GetMailbox(
			context.Background(),
			mailbox.TaskID,
		)
		require.NoError(t, err)
		require.Equal(t, task.MailboxBackground, adoptedMailbox.State)
		require.Equal(t, mailbox.Generation+1, adoptedMailbox.Generation)
		require.Equal(t, mailbox.ConsumedCursor, adoptedMailbox.ConsumedCursor)

		err = store.AdvanceCursor(
			context.Background(),
			&task.AdvanceCursorRequest{
				TaskID:             mailbox.TaskID,
				ExpectedGeneration: mailbox.Generation,
				ExpectedCursor:     mailbox.ConsumedCursor,
				Cursor:             mailbox.ConsumedCursor,
			},
		)
		require.ErrorIs(t, err, task.ErrOwnershipLost)
	})

	t.Run("pending attempt is fenced and reaches terminal", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		mailbox := registerStage3Foreground(t, store, "pending")
		input, err := store.SendInput(
			context.Background(),
			&task.SendInputRequest{
				TaskID: mailbox.TaskID,
				Input: task.Input{
					EventID: "input", Kind: "message", Data: []byte("value"),
				},
			},
		)
		require.NoError(t, err)
		require.Equal(t, int64(1), input.Input.Sequence)

		request := stage3AdoptRequest(mailbox, true)
		pending, err := store.AdoptForeground(context.Background(), request)
		require.NoError(t, err)
		require.Equal(t, StatusPending, pending.Status)
		require.Zero(t, pending.Attempt)
		adoptedMailbox, err := store.GetMailbox(
			context.Background(),
			mailbox.TaskID,
		)
		require.NoError(t, err)
		require.Equal(t, task.MailboxBackground, adoptedMailbox.State)
		require.Equal(t, mailbox.Generation+1, adoptedMailbox.Generation)
		require.Equal(t, int64(1), adoptedMailbox.LatestSequence)
		require.Zero(t, adoptedMailbox.ConsumedCursor)

		err = store.AdvanceCursor(
			context.Background(),
			&task.AdvanceCursorRequest{
				TaskID: mailbox.TaskID, ExpectedCursor: 0, Cursor: 0,
				ExpectedGeneration: adoptedMailbox.Generation,
				Attempt:            0,
			},
		)
		require.ErrorIs(t, err, task.ErrOwnershipLost)

		started, err := store.Start(
			context.Background(),
			&StartTaskRequest{
				TaskID: mailbox.TaskID, ExpectedVersion: pending.Version,
			},
		)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, started.Status)
		require.Equal(t, int64(1), started.Attempt)

		err = store.AdvanceCursor(
			context.Background(),
			&task.AdvanceCursorRequest{
				TaskID: mailbox.TaskID, ExpectedCursor: 0, Cursor: 0,
				ExpectedGeneration: adoptedMailbox.Generation,
				Attempt:            started.Attempt + 1,
			},
		)
		require.ErrorIs(t, err, ErrLeaseLost)
		err = store.AdvanceCursor(
			context.Background(),
			&task.AdvanceCursorRequest{
				TaskID: mailbox.TaskID, ExpectedCursor: 0, Cursor: 0,
				ExpectedGeneration: mailbox.Generation,
				Attempt:            started.Attempt,
			},
		)
		require.ErrorIs(t, err, task.ErrOwnershipLost)

		pendingAgain, err := store.CompleteIfNoInputs(
			context.Background(),
			&CompleteIfNoInputsRequest{
				TaskID: mailbox.TaskID, ExpectedVersion: started.Version,
				Attempt: started.Attempt, InputCursor: 0,
				ResultData: []byte("premature"),
			},
		)
		require.ErrorIs(t, err, task.ErrInputsPending)
		require.Equal(t, StatusPending, pendingAgain.Status)
		require.Empty(t, pendingAgain.ResultData)

		restarted, err := store.Start(
			context.Background(),
			&StartTaskRequest{
				TaskID: mailbox.TaskID, ExpectedVersion: pendingAgain.Version,
			},
		)
		require.NoError(t, err)
		require.Equal(t, StatusRunning, restarted.Status)
		require.Equal(t, int64(2), restarted.Attempt)
		require.NoError(t, store.AdvanceCursor(
			context.Background(),
			&task.AdvanceCursorRequest{
				TaskID: mailbox.TaskID, ExpectedCursor: 0,
				Cursor:             input.Input.Sequence,
				ExpectedGeneration: adoptedMailbox.Generation,
				Attempt:            restarted.Attempt,
			},
		))

		completed, err := store.CompleteIfNoInputs(
			context.Background(),
			&CompleteIfNoInputsRequest{
				TaskID: mailbox.TaskID, ExpectedVersion: restarted.Version,
				Attempt: restarted.Attempt, InputCursor: input.Input.Sequence,
				ResultData: []byte("done"),
			},
		)
		require.NoError(t, err)
		require.Equal(t, StatusCompleted, completed.Status)
		require.Equal(t, []byte("done"), completed.ResultData)
		require.NotNil(t, completed.DoneAt)
		terminalMailbox, err := store.GetMailbox(
			context.Background(),
			mailbox.TaskID,
		)
		require.NoError(t, err)
		require.Equal(t, task.MailboxSealed, terminalMailbox.State)
		require.Equal(t, adoptedMailbox.Generation+1, terminalMailbox.Generation)

		err = store.AdvanceCursor(
			context.Background(),
			&task.AdvanceCursorRequest{
				TaskID:             mailbox.TaskID,
				ExpectedCursor:     input.Input.Sequence,
				Cursor:             input.Input.Sequence,
				ExpectedGeneration: terminalMailbox.Generation,
				Attempt:            restarted.Attempt,
			},
		)
		require.ErrorIs(t, err, task.ErrMailboxSealed)
		_, err = store.AdoptForeground(context.Background(), request)
		require.ErrorIs(t, err, ErrAlreadyExists)
	})

	t.Run("pending input blocks suspended adoption without mutation", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		mailbox := registerStage3Foreground(t, store, "input-race")
		_, err := store.SendInput(
			context.Background(),
			&task.SendInputRequest{
				TaskID: mailbox.TaskID,
				Input:  task.Input{EventID: "late", Kind: "message"},
			},
		)
		require.NoError(t, err)

		snapshot, err := store.AdoptForeground(
			context.Background(),
			stage3AdoptRequest(mailbox, false),
		)
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, task.ErrInputsPending)
		_, err = store.Get(context.Background(), mailbox.TaskID)
		require.ErrorIs(t, err, ErrNotFound)
		current, err := store.GetMailbox(context.Background(), mailbox.TaskID)
		require.NoError(t, err)
		require.Equal(t, task.MailboxForeground, current.State)
		require.Equal(t, mailbox.Generation, current.Generation)
		require.Equal(t, int64(1), current.LatestSequence)
		require.Zero(t, current.ConsumedCursor)
	})

	t.Run("sealed foreground mailbox rejects adoption", func(t *testing.T) {
		store := NewInMemoryStore(nil)
		mailbox := registerStage3Foreground(t, store, "sealed")
		sealed, err := store.SealIfIdle(
			context.Background(),
			&task.SealMailboxRequest{
				TaskID:             mailbox.TaskID,
				ExpectedGeneration: mailbox.Generation,
			},
		)
		require.NoError(t, err)
		require.Equal(t, task.MailboxSealed, sealed.State)

		snapshot, err := store.AdoptForeground(
			context.Background(),
			stage3AdoptRequest(sealed, false),
		)
		require.Nil(t, snapshot)
		require.ErrorIs(t, err, task.ErrMailboxSealed)
	})
}
