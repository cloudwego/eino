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
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

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

		cancelCh := make(chan *time.Duration, 1)

		go func() {
			time.Sleep(time.Microsecond)
			close(recvReleased)
		}()
		zero := time.Duration(0)
		cancelCh <- &zero

		ta, _, immediateCanceled, canceled, _ := receiveWithListening(recv, cancelCh)

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

	cancelCh := make(chan *time.Duration, 1)
	cancelCh <- nil // unlimited grace: no timeout

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(recvReleased)
	}()

	ta, _, immediateCanceled, canceled, deadline := receiveWithListening(recv, cancelCh)

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

	cancelCh := make(chan *time.Duration, 1)
	timeout := 200 * time.Millisecond
	cancelCh <- &timeout

	go func() {
		time.Sleep(20 * time.Millisecond)
		close(recvReleased)
	}()

	ta, _, immediateCanceled, canceled, deadline := receiveWithListening(recv, cancelCh)

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

	cancelCh := make(chan *time.Duration, 1)
	timeout := 20 * time.Millisecond
	cancelCh <- &timeout

	ta, _, immediateCanceled, canceled, deadline := receiveWithListening(recv, cancelCh)

	assert.True(t, canceled)
	assert.True(t, immediateCanceled)
	assert.Nil(t, ta)
	assert.NotNil(t, deadline)
}
