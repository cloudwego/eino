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

package compose

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/internal"
)

func triggeredGraphCancel(timeout *time.Duration) *graphCancelSignal {
	cancel := &graphCancelSignal{done: make(chan struct{})}
	cancel.trigger(timeout)
	return cancel
}

func TestGraphCancelBroadcast(t *testing.T) {
	timeout := 100 * time.Millisecond
	cancel := triggeredGraphCancel(&timeout)

	for i := 0; i < 3; i++ {
		select {
		case <-cancel.done:
			gotTimeout, deadline := cancel.request()
			require.NotNil(t, gotTimeout)
			assert.Equal(t, timeout, *gotTimeout)
			assert.NotNil(t, deadline)
		case <-time.After(time.Second):
			t.Fatalf("listener %d did not observe graph cancellation", i)
		}
	}
}

func TestTaskManagerSettleCanceledSubgraphUsesPublishedCheckpoint(t *testing.T) {
	ready := make(chan *subGraphInterruptError, 1)
	published := &subGraphInterruptError{CheckPoint: &checkpoint{}}
	ready <- published

	subGraphTask := &task{
		nodeKey:                 "subgraph",
		subGraphCheckpointReady: ready,
		finished:                make(chan struct{}),
	}
	manager := &taskManager{
		num:          1,
		runningTasks: map[string]*task{"subgraph": subGraphTask},
	}

	completed, canceled := manager.settleCanceledSubgraphs()

	require.Len(t, completed, 1)
	assert.Same(t, published, completed[0].err)
	assert.Empty(t, canceled)
}

func TestAttack_SubGraphCheckpointPublisherIsOneHop(t *testing.T) {
	rawOpts, ready := withSubGraphCheckpointPublisher(nil)
	opts, err := convertOption[Option](rawOpts...)
	require.NoError(t, err)

	publish := getSubGraphCheckpointPublisher(opts...)
	require.NotNil(t, publish)
	want := &subGraphInterruptError{CheckPoint: &checkpoint{}}
	publish(want)
	assert.Same(t, want, <-ready)

	nodes := map[string]*chanCall{
		"nested": {action: &composableRunnable{optionType: nil}},
	}
	forwarded, err := extractOption(nodes, opts...)
	assert.NoError(t, err)
	assert.Empty(t, forwarded, "the parent publisher must not propagate into a grandchild graph")
}

func TestTaskManagerPreHandlerFailureClosesFinished(t *testing.T) {
	wantErr := errors.New("pre-handler failed")
	preProcessor := &composableRunnable{}
	failedTask := &task{
		nodeKey: "subgraph",
		call: &chanCall{
			action:       &composableRunnable{},
			preProcessor: preProcessor,
		},
		subGraphCheckpointReady: make(chan *subGraphInterruptError),
	}
	manager := &taskManager{
		runWrapper: func(_ context.Context, runnable *composableRunnable, _ any, _ ...any) (any, error) {
			if runnable == preProcessor {
				return nil, wantErr
			}
			return nil, nil
		},
		done:         internal.NewUnboundedChan[*task](),
		runningTasks: make(map[string]*task),
	}

	require.NoError(t, manager.submit([]*task{failedTask}))
	select {
	case <-failedTask.finished:
	case <-time.After(time.Second):
		t.Fatal("pre-handler failure did not mark task as finished")
	}

	completed, canceled := manager.settleCanceledSubgraphs()
	require.Len(t, completed, 1)
	assert.ErrorIs(t, completed[0].err, wantErr)
	assert.Empty(t, canceled)
}

// TestReceiveWithListening_ZeroTimeout_WinsAgainstDelayedTaskCompletion
// covers https://github.com/cloudwego/eino/issues/1148 at the compose level.
// It mirrors the real production timing that triggers the bug: the cancel
// signal is delivered synchronously (a single buffered channel send, as
// sendImmediateInterrupt does), while the task's own completion is delayed
// (as it is in ADK, where a stream-cancel monitor's injected error reaches
// the task only after a multi-hop goroutine/pipe chain). Before the fix, a
// zero timeout still raced the task's real completion against a
// time.After(0) timer — which is not instantaneous, since it goes through
// the runtime's timer heap — so the task's own (cancellation-induced) result
// could occasionally win and be mistaken for a normal completion, skipping
// the interrupt/checkpoint path entirely.
//
// Note: this does not claim to close every conceivable race (e.g. a cancel
// signal arriving a few nanoseconds after an *unrelated* task completion is
// already mid-flight through the outer select is not fully resolvable
// without additional synchronization); it targets the specific, realistic
// timing pattern that produces the reported bug.
func TestReceiveWithListening_ZeroTimeout_WinsAgainstDelayedTaskCompletion(t *testing.T) {
	for i := 0; i < 200; i++ {
		recvReleased := make(chan struct{})
		recv := func() (*task, bool) {
			<-recvReleased
			return &task{nodeKey: "n"}, true
		}

		go func() {
			time.Sleep(time.Microsecond)
			close(recvReleased)
		}()
		zero := time.Duration(0)

		ta, _, immediateCanceled, canceled, _ := receiveWithListening(recv, triggeredGraphCancel(&zero))

		assert.True(t, canceled, "iteration %d", i)
		assert.True(t, immediateCanceled, "iteration %d: zero-timeout cancel must win against a slightly-delayed task completion", i)
		assert.Nil(t, ta, "iteration %d: task result must be discarded (rerun on resume), not treated as a normal completion", i)
	}
}

// TestReceiveWithListening_UnlimitedGrace_UsesRealTaskResult verifies that a
// cancel signal with a nil timeout (unlimited grace / plain safe-point mode)
// still waits for and returns the task's real result, unlike the zero-timeout
// case above.
func TestReceiveWithListening_UnlimitedGrace_UsesRealTaskResult(t *testing.T) {
	recvReleased := make(chan struct{})
	want := &task{nodeKey: "n"}
	recv := func() (*task, bool) {
		<-recvReleased
		return want, true
	}

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(recvReleased)
	}()

	ta, _, immediateCanceled, canceled, deadline := receiveWithListening(recv, triggeredGraphCancel(nil))

	assert.True(t, canceled)
	assert.False(t, immediateCanceled)
	assert.Same(t, want, ta)
	assert.Nil(t, deadline)
}

// TestReceiveWithListening_PositiveTimeout_TaskFinishesWithinGrace verifies
// that a safe-point cancel with a positive escalation timeout still uses the
// task's real result if it completes before the deadline.
func TestReceiveWithListening_PositiveTimeout_TaskFinishesWithinGrace(t *testing.T) {
	recvReleased := make(chan struct{})
	want := &task{nodeKey: "n"}
	recv := func() (*task, bool) {
		<-recvReleased
		return want, true
	}

	timeout := 200 * time.Millisecond

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(recvReleased)
	}()

	ta, _, immediateCanceled, canceled, deadline := receiveWithListening(recv, triggeredGraphCancel(&timeout))

	assert.True(t, canceled)
	assert.False(t, immediateCanceled)
	assert.Same(t, want, ta)
	assert.NotNil(t, deadline)
}

// TestReceiveWithListening_PositiveTimeout_DeadlineExpiresFirst verifies that
// a safe-point cancel escalates to an immediate cancel (discarding the task)
// once its grace period elapses before the task completes.
func TestReceiveWithListening_PositiveTimeout_DeadlineExpiresFirst(t *testing.T) {
	recvReleased := make(chan struct{})
	recv := func() (*task, bool) {
		<-recvReleased
		return &task{nodeKey: "n"}, true
	}
	defer close(recvReleased)

	timeout := 20 * time.Millisecond

	ta, _, immediateCanceled, canceled, deadline := receiveWithListening(recv, triggeredGraphCancel(&timeout))

	assert.True(t, canceled)
	assert.True(t, immediateCanceled)
	assert.Nil(t, ta)
	assert.NotNil(t, deadline)
}
