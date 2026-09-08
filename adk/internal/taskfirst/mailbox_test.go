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

package taskfirst

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
)

type finalizationStore struct {
	*background.InMemoryStore
	sealErr      error
	abandonErr   error
	sealCalls    int64
	abandonCalls int64
}

func (s *finalizationStore) SealIfIdle(
	ctx context.Context,
	req *task.SealMailboxRequest,
) (*task.Mailbox, error) {
	atomic.AddInt64(&s.sealCalls, 1)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > foregroundMailboxFinalizationTimeout {
		return nil, errors.New("finalization context is not bounded")
	}
	if s.sealErr != nil {
		return nil, s.sealErr
	}
	return s.InMemoryStore.SealIfIdle(ctx, req)
}

func (s *finalizationStore) Abandon(
	ctx context.Context,
	req *task.AbandonMailboxRequest,
) (*task.Mailbox, error) {
	atomic.AddInt64(&s.abandonCalls, 1)
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > foregroundMailboxFinalizationTimeout {
		return nil, errors.New("finalization context is not bounded")
	}
	if s.abandonErr != nil {
		return nil, s.abandonErr
	}
	return s.InMemoryStore.Abandon(ctx, req)
}

func TestForegroundMailboxFinalizerPreservesPendingInput(t *testing.T) {
	store := &finalizationStore{InMemoryStore: background.NewInMemoryStore(nil)}
	manager, err := background.New(context.Background(), &background.Config{
		Tasks: store,
	})
	require.NoError(t, err)
	registered, err := manager.RegisterMailbox(
		context.Background(),
		&task.RegisterMailboxRequest{
			CandidateTaskID: "task", InvocationID: "invocation",
			RootSessionID: "session",
		},
	)
	require.NoError(t, err)
	_, err = manager.SendInput(context.Background(), &task.SendInputRequest{
		TaskID: "task",
		Input: task.Input{
			EventID: "pending", Kind: "message", Data: []byte("keep"),
		},
	})
	require.NoError(t, err)

	finalizer := NewForegroundMailboxFinalizer(
		manager,
		registered.Mailbox.TaskID,
		registered.Mailbox.Generation,
		registered.Mailbox.ConsumedCursor,
	)
	require.ErrorIs(t, finalizer.SealIfIdle(), task.ErrInputsPending)
	require.ErrorIs(t, finalizer.Abandon(), task.ErrInputsPending)
	require.Equal(t, int64(1), atomic.LoadInt64(&store.sealCalls))
	require.Zero(t, atomic.LoadInt64(&store.abandonCalls))

	mailbox, err := manager.GetMailbox(context.Background(), "task")
	require.NoError(t, err)
	require.Equal(t, task.MailboxForeground, mailbox.State)
	require.Equal(t, int64(1), mailbox.LatestSequence)
	require.Zero(t, mailbox.ConsumedCursor)
}

func TestForegroundMailboxFinalizerReturnsStoreErrors(t *testing.T) {
	t.Run("seal", func(t *testing.T) {
		wantErr := errors.New("seal failed")
		store := &finalizationStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			sealErr:       wantErr,
		}
		manager, err := background.New(context.Background(), &background.Config{
			Tasks: store,
		})
		require.NoError(t, err)
		finalizer := NewForegroundMailboxFinalizer(manager, "task", 1, 0)
		require.ErrorIs(t, finalizer.SealIfIdle(), wantErr)
		require.ErrorIs(t, finalizer.SealIfIdle(), wantErr)
		require.Equal(t, int64(1), atomic.LoadInt64(&store.sealCalls))
		require.Zero(t, atomic.LoadInt64(&store.abandonCalls))
	})

	t.Run("abandon", func(t *testing.T) {
		wantErr := errors.New("abandon failed")
		store := &finalizationStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			abandonErr:    wantErr,
		}
		manager, err := background.New(context.Background(), &background.Config{
			Tasks: store,
		})
		require.NoError(t, err)
		finalizer := NewForegroundMailboxFinalizer(manager, "task", 1, 0)
		require.ErrorIs(t, finalizer.Abandon(), wantErr)
		require.ErrorIs(t, finalizer.Abandon(), wantErr)
		require.Equal(t, int64(1), atomic.LoadInt64(&store.abandonCalls))
		require.Zero(t, atomic.LoadInt64(&store.sealCalls))
	})
}

func TestCombineForegroundErrors(t *testing.T) {
	operationErr := errors.New("operation failed")
	finalizationErr := errors.New("finalization failed")

	tests := []struct {
		name            string
		operationErr    error
		finalizationErr error
		want            error
		wantMessage     string
	}{
		{
			name:        "no errors",
			wantMessage: "",
		},
		{
			name:         "operation only",
			operationErr: operationErr,
			want:         operationErr,
			wantMessage:  "operation failed",
		},
		{
			name:            "finalization only",
			finalizationErr: finalizationErr,
			want:            finalizationErr,
			wantMessage:     "finalization failed",
		},
		{
			name:            "operation then finalization",
			operationErr:    operationErr,
			finalizationErr: finalizationErr,
			want:            operationErr,
			wantMessage: "operation failed " +
				"(foreground mailbox cleanup failed: finalization failed)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := CombineForegroundErrors(tt.operationErr, tt.finalizationErr)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantMessage)
			require.ErrorIs(t, err, tt.want)
			if tt.operationErr != nil && tt.finalizationErr != nil {
				require.ErrorIs(t, err, tt.finalizationErr)
				require.Same(t, operationErr, errors.Unwrap(err))
			}
		})
	}
}
