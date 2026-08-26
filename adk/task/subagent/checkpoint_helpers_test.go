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

package subagent

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

type cursorExecutionRuntime struct {
	background.ExecutionRuntime
	err            error
	calls          int
	expectedCursor int64
	cursor         int64
}

func (r *cursorExecutionRuntime) AdvanceInputCursor(
	_ context.Context,
	expectedCursor, cursor int64,
) error {
	r.calls++
	r.expectedCursor = expectedCursor
	r.cursor = cursor
	return r.err
}

func TestCaptureCheckpointStore(t *testing.T) {
	ctx := context.Background()
	initial := []byte("initial")
	store := newCaptureCheckpointStore(initial, true)
	initial[0] = 'X'

	checkpoint, exists, err := store.Get(ctx, "ignored")
	require.NoError(t, err)
	require.True(t, exists)
	require.Equal(t, []byte("initial"), checkpoint)
	checkpoint[0] = 'X'

	require.NoError(t, store.Set(ctx, "ignored", []byte("captured")))
	checkpoint, exists = store.snapshot()
	require.True(t, exists)
	require.Equal(t, []byte("captured"), checkpoint)
	checkpoint[0] = 'X'
	checkpoint, exists = store.snapshot()
	require.True(t, exists)
	require.Equal(t, []byte("captured"), checkpoint)

	require.NoError(t, store.Delete(ctx, "ignored"))
	checkpoint, exists = store.snapshot()
	require.False(t, exists)
	require.Nil(t, checkpoint)

	oldAttempt := newCaptureCheckpointStore([]byte("committed"), true)
	currentAttempt := newCaptureCheckpointStore([]byte("committed"), true)
	require.NoError(t, currentAttempt.Set(ctx, "ignored", []byte("current")))
	require.NoError(t, oldAttempt.Set(ctx, "ignored", []byte("stale")))
	require.NoError(t, oldAttempt.Delete(ctx, "ignored"))
	checkpoint, exists = currentAttempt.snapshot()
	require.True(t, exists)
	require.Equal(t, []byte("current"), checkpoint)
}

func TestCaptureCheckpointStoreRunsCommitAfterPublishingLatestBytes(t *testing.T) {
	ctx := context.Background()
	store := newCaptureCheckpointStore([]byte("old"), true)
	commitErr := errors.New("commit failed")
	store.setOnSet(func(_ context.Context, checkpoint []byte) error {
		require.Equal(t, []byte("latest"), checkpoint)
		published, exists := store.snapshot()
		require.True(t, exists)
		require.Equal(t, checkpoint, published)
		return commitErr
	})

	err := store.Set(ctx, "ignored", []byte("latest"))
	require.ErrorIs(t, err, commitErr)
	checkpoint, exists := store.snapshot()
	require.True(t, exists)
	require.Equal(t, []byte("latest"), checkpoint)
}

func TestValidateSparseAcksRejectsInvalidSequences(t *testing.T) {
	require.NoError(t, validateSparseAcks(nil, 2, 5))
	require.NoError(t, validateSparseAcks([]int64{4, 5}, 2, 5))

	for _, testCase := range []struct {
		name       string
		sparseAcks []int64
		wantError  string
	}{
		{name: "duplicate", sparseAcks: []int64{4, 4}, wantError: "task/subagent: runtime checkpoint sparse acks are invalid"},
		{name: "descending", sparseAcks: []int64{5, 4}, wantError: "task/subagent: runtime checkpoint sparse acks are invalid"},
		{name: "at cursor", sparseAcks: []int64{2}, wantError: "task/subagent: runtime checkpoint sparse acks are invalid"},
		{name: "below cursor", sparseAcks: []int64{1}, wantError: "task/subagent: runtime checkpoint sparse acks are invalid"},
		{name: "above latest", sparseAcks: []int64{6}, wantError: "task/subagent: runtime checkpoint sparse acks are invalid"},
		{name: "unfolded cursor successor", sparseAcks: []int64{3}, wantError: "task/subagent: runtime checkpoint sparse acks are not folded"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.EqualError(
				t,
				validateSparseAcks(testCase.sparseAcks, 2, 5),
				testCase.wantError,
			)
		})
	}
}

func TestSparseAcksBound(t *testing.T) {
	const expectedMaxSparseAcks = 4096
	atLimit := make([]int64, expectedMaxSparseAcks)
	for index := range atLimit {
		atLimit[index] = int64(index + 2)
	}
	require.NoError(
		t,
		validateSparseAcks(atLimit, 0, int64(expectedMaxSparseAcks+1)),
	)

	overLimit := append(append([]int64(nil), atLimit...), expectedMaxSparseAcks+2)
	err := validateSparseAcks(overLimit, 0, int64(expectedMaxSparseAcks+2))
	require.EqualError(
		t,
		err,
		"task/subagent: runtime checkpoint sparse acks exceed limit",
	)

	inputs := make([]*task.InputRecord, expectedMaxSparseAcks+1)
	for index := range inputs {
		inputs[index] = &task.InputRecord{Sequence: int64(index + 2)}
	}
	_, _, err = acknowledgeInputRecords(0, nil, inputs)
	require.EqualError(
		t,
		err,
		"task/subagent: runtime checkpoint sparse acks exceed limit",
	)
}

func TestEqualSequencesRequiresSameLengthAndValues(t *testing.T) {
	require.True(t, equalSequences(nil, nil))
	require.True(t, equalSequences([]int64{2, 4}, []int64{2, 4}))
	require.False(t, equalSequences([]int64{2}, []int64{2, 4}))
	require.False(t, equalSequences([]int64{2, 4}, []int64{2, 5}))
}

func TestControllerAdvanceCursor(t *testing.T) {
	t.Run("attached propagates mailbox read error without advancing", func(t *testing.T) {
		getMailboxErr := errors.New("mailbox unavailable")
		store := &stage3FaultStore{
			InMemoryStore: background.NewInMemoryStore(nil),
			getMailboxErr: getMailboxErr,
		}
		manager := newStage3Manager(t, store)
		controller := &Controller[*schema.Message]{manager: manager}

		err := controller.advanceCursor(
			context.Background(), nil, "attached-task", 2, 3, true,
		)

		require.Same(t, getMailboxErr, err)
		require.Equal(t, 1, store.getMailboxCalls)
		require.Zero(t, store.advanceCursorCalls)
		require.Empty(t, store.advanceCursorRequests)
	})

	t.Run("detached delegates cursor and error to execution runtime", func(t *testing.T) {
		advanceErr := errors.New("advance unavailable")
		execution := &cursorExecutionRuntime{err: advanceErr}
		controller := &Controller[*schema.Message]{}

		err := controller.advanceCursor(
			context.Background(), execution, "detached-task", 4, 7, false,
		)

		require.Same(t, advanceErr, err)
		require.Equal(t, 1, execution.calls)
		require.Equal(t, int64(4), execution.expectedCursor)
		require.Equal(t, int64(7), execution.cursor)
	})
}

func TestDeleteCheckpointBestEffortBoundaries(t *testing.T) {
	tests := []struct {
		name         string
		checkpointID string
		store        func(adk.CheckPointStore) adk.CheckPointStore
		want         bool
	}{
		{
			name:         "empty checkpoint ID skips deletion",
			checkpointID: "",
			store: func(store adk.CheckPointStore) adk.CheckPointStore {
				return store
			},
			want: true,
		},
		{
			name:         "store without deleter reports unsupported",
			checkpointID: "checkpoint",
			store: func(store adk.CheckPointStore) adk.CheckPointStore {
				return struct{ adk.CheckPointStore }{CheckPointStore: store}
			},
			want: false,
		},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			var calls []string
			captured := newCaptureCheckpointStore([]byte("retained"), true)
			recording := &recordingCheckpointStore{
				store: captured,
				record: func(call string) {
					calls = append(calls, call)
				},
			}

			require.Equal(t, testCase.want, deleteCheckpointBestEffort(
				context.Background(),
				testCase.store(recording),
				testCase.checkpointID,
			))
			require.Empty(t, calls)
			checkpoint, exists := captured.snapshot()
			require.True(t, exists)
			require.Equal(t, []byte("retained"), checkpoint)
		})
	}
}

func TestPollActivationControl(t *testing.T) {
	t.Run("arrival preserves timeout reason", func(t *testing.T) {
		controls := make(chan background.ControlRequest, 1)
		want := background.ControlRequest{
			Kind: background.ControlTimeout, Reason: "lease expired",
		}
		controls <- want
		require.Equal(t, want, pollActivationControl(controls))
	})

	t.Run("not ready", func(t *testing.T) {
		require.Equal(
			t,
			background.ControlRequest{},
			pollActivationControl(make(chan background.ControlRequest)),
		)
	})

	t.Run("nil", func(t *testing.T) {
		require.Equal(t, background.ControlRequest{}, pollActivationControl(nil))
	})

	t.Run("closed", func(t *testing.T) {
		controls := make(chan background.ControlRequest)
		close(controls)
		require.Equal(t, background.ControlRequest{}, pollActivationControl(controls))
	})
}
