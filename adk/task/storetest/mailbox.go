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

package storetest

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
)

// MailboxStoreConfig configures mailbox conformance.
type MailboxStoreConfig struct {
	New func(testing.TB) task.MailboxStore
}

// RunMailboxStoreConformance validates replay, ordering, waiting, sealing, and
// nested ownership.
func RunMailboxStoreConformance( //nolint:funlen // Conformance cases stay together for provider reuse.
	t *testing.T,
	config MailboxStoreConfig,
) {
	t.Helper()
	require.NotNil(t, config.New)

	t.Run("registration_replay_and_conflict", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		first, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "task-1", InvocationID: "invocation",
			Identity: []byte("identity"), RootSessionID: "session",
		})
		require.NoError(t, err)
		require.True(t, first.Created)
		replay, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "task-2", InvocationID: "invocation",
			Identity: []byte("identity"), RootSessionID: "session",
		})
		require.NoError(t, err)
		require.False(t, replay.Created)
		require.Equal(t, "task-1", replay.Mailbox.TaskID)
		_, err = store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "task-3", InvocationID: "invocation",
			Identity: []byte("changed"), RootSessionID: "session",
		})
		require.ErrorIs(t, err, task.ErrMailboxIdentityConflict)
		_, err = store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "task-4", InvocationID: "invocation",
			Identity: []byte("identity"), RootSessionID: "session",
			ChildSessionID: "changed",
		})
		require.ErrorIs(t, err, task.ErrMailboxIdentityConflict)
	})

	t.Run("invocation_identity_is_scoped_to_owner", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		first, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "task-1", InvocationID: "call",
			Identity: []byte("first"), RootSessionID: "session-1",
		})
		require.NoError(t, err)
		second, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "task-2", InvocationID: "call",
			Identity: []byte("second"), RootSessionID: "session-2",
		})
		require.NoError(t, err)
		require.NotEqual(t, first.Mailbox.TaskID, second.Mailbox.TaskID)
	})

	t.Run("fifo_idempotency_and_cursor", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		mailbox := registerMailbox(t, store, "fifo")
		first, err := store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input: task.Input{
				EventID: "one", Kind: "event",
				Data: []byte("first"), Delivery: task.InputQueued,
			},
		})
		require.NoError(t, err)
		require.True(t, first.Inserted)
		replay, err := store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input: task.Input{
				EventID: "one", Kind: "event",
				Data: []byte("first"), Delivery: task.InputQueued,
			},
		})
		require.NoError(t, err)
		require.False(t, replay.Inserted)
		_, err = store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input: task.Input{
				EventID: "one", Kind: "event", Data: []byte("changed"),
			},
		})
		require.ErrorIs(t, err, task.ErrInputConflict)
		_, err = store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input: task.Input{
				EventID: "two", Kind: "urgent", Delivery: task.InputPreempt,
			},
		})
		require.NoError(t, err)
		page, err := store.ListInputs(ctx, &task.ListInputsRequest{
			TaskID: mailbox.TaskID,
		})
		require.NoError(t, err)
		require.Len(t, page.Inputs, 2)
		require.Equal(t, int64(1), page.Inputs[0].Sequence)
		require.Equal(t, int64(2), page.Inputs[1].Sequence)
		require.Equal(t, task.InputPreempt, page.Inputs[1].Delivery)
		require.NoError(t, store.AdvanceCursor(ctx, &task.AdvanceCursorRequest{
			TaskID: mailbox.TaskID, ExpectedCursor: 0, Cursor: 2,
			ExpectedGeneration: mailbox.Generation,
		}))
		updated, err := store.GetMailbox(ctx, mailbox.TaskID)
		require.NoError(t, err)
		require.Equal(t, int64(2), updated.ConsumedCursor)
	})

	t.Run("wait_observes_enqueue", func(t *testing.T) {
		store := config.New(t)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mailbox := registerMailbox(t, store, "wait")
		result := make(chan *task.ListInputsResult, 1)
		go func() {
			page, _ := store.WaitInputs(ctx, &task.WaitInputsRequest{
				TaskID: mailbox.TaskID,
			})
			result <- page
		}()
		_, err := store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input:  task.Input{EventID: "wake", Kind: "event"},
		})
		require.NoError(t, err)
		require.Len(t, (<-result).Inputs, 1)
	})

	t.Run("seal_if_idle_and_reject_late_input", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		mailbox := registerMailbox(t, store, "seal")
		sealed, err := store.SealIfIdle(ctx, &task.SealMailboxRequest{
			TaskID: mailbox.TaskID, ExpectedGeneration: mailbox.Generation,
		})
		require.NoError(t, err)
		require.Equal(t, task.MailboxSealed, sealed.State)
		_, err = store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input:  task.Input{EventID: "late", Kind: "event"},
		})
		require.ErrorIs(t, err, task.ErrMailboxSealed)
	})

	t.Run("pending_input_prevents_seal", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		mailbox := registerMailbox(t, store, "pending")
		_, err := store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input:  task.Input{EventID: "pending", Kind: "event"},
		})
		require.NoError(t, err)
		current, err := store.SealIfIdle(ctx, &task.SealMailboxRequest{
			TaskID: mailbox.TaskID, ExpectedGeneration: mailbox.Generation,
		})
		require.ErrorIs(t, err, task.ErrInputsPending)
		require.Equal(t, task.MailboxForeground, current.State)
	})

	t.Run("abandon_discards_pending_input", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		mailbox := registerMailbox(t, store, "abandon")
		_, err := store.SendInput(ctx, &task.SendInputRequest{
			TaskID: mailbox.TaskID,
			Input:  task.Input{EventID: "pending", Kind: "event"},
		})
		require.NoError(t, err)
		sealed, err := store.Abandon(ctx, &task.AbandonMailboxRequest{
			TaskID: mailbox.TaskID, ExpectedGeneration: mailbox.Generation,
		})
		require.NoError(t, err)
		require.Equal(t, task.MailboxSealed, sealed.State)
		require.Equal(t, sealed.LatestSequence, sealed.ConsumedCursor)
	})

	t.Run("nested_registration_requires_owner", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		parent := registerMailbox(t, store, "parent")
		_, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "conflicting-child", InvocationID: "conflicting-child",
			RootSessionID: "forged-root",
			ParentExecution: &task.ExecutionContext{
				TaskID: parent.TaskID, Owner: task.OwnerParent,
				Generation: parent.Generation,
			},
		})
		require.Error(t, err)
		child, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "child", InvocationID: "child-invocation",
			Identity: []byte("child"),
			ParentExecution: &task.ExecutionContext{
				TaskID: parent.TaskID, Owner: task.OwnerParent,
				Generation: parent.Generation,
			},
		})
		require.NoError(t, err)
		require.Equal(t, parent.TaskID, child.Mailbox.ParentTaskID)
		require.Equal(t, parent.RootSessionID, child.Mailbox.RootSessionID)
		children, err := store.ListChildren(ctx, &task.ListChildrenRequest{
			ParentTaskID: parent.TaskID,
		})
		require.NoError(t, err)
		require.Len(t, children.Mailboxes, 1)
		require.Equal(t, "child", children.Mailboxes[0].TaskID)
	})

	t.Run("child_session_has_one_active_task", func(t *testing.T) {
		store := config.New(t)
		ctx := context.Background()
		first, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "first", InvocationID: "first",
			ChildSessionID: "child-session",
		})
		require.NoError(t, err)
		_, err = store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "second", InvocationID: "second",
			ChildSessionID: "child-session",
		})
		require.ErrorIs(t, err, task.ErrSessionBusy)
		_, err = store.SealIfIdle(ctx, &task.SealMailboxRequest{
			TaskID:             first.Mailbox.TaskID,
			ExpectedGeneration: first.Mailbox.Generation,
		})
		require.NoError(t, err)
		second, err := store.Register(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "second", InvocationID: "second",
			ChildSessionID: "child-session",
		})
		require.NoError(t, err)
		require.Equal(t, "second", second.Mailbox.TaskID)
		active, err := store.GetActiveMailboxBySession(ctx, "child-session")
		require.NoError(t, err)
		require.Equal(t, second.Mailbox.TaskID, active.TaskID)
	})
}

func registerMailbox(
	t testing.TB,
	store task.MailboxStore,
	name string,
) *task.Mailbox {
	t.Helper()
	result, err := store.Register(context.Background(), &task.RegisterMailboxRequest{
		CandidateTaskID: name, InvocationID: name + "-invocation",
		Identity: []byte(name), RootSessionID: "session",
	})
	require.NoError(t, err)
	return result.Mailbox
}
