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

package adk

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestTurnLoop_StopWatcherDoesNotConsumeNextTurnRequest(t *testing.T) {
	loop := NewTurnLoop(TurnLoopConfig[string, *schema.Message]{
		GenInput:     genInputConsumeAll,
		PrepareAgent: prepareTestAgent,
	})
	oldNotify := loop.stopCtrl.beginActiveTurn()
	oldCancelCtx := newCancelContext()
	oldCancelCtx.markDone()
	oldCancel := oldCancelCtx.buildCancelFunc()
	oldDone := make(chan struct{})
	close(oldDone)
	loop.stopCtrl.endActiveTurn()

	currentNotify := loop.stopCtrl.beginActiveTurn()
	currentCancelCtx := newCancelContext()
	loop.Stop(WithImmediate(), WithStopCause("shutdown"), WithSkipCheckpoint())

	// Schedule the old watcher only after the new turn has a pending Stop.
	// Its cancel function belongs to a completed agent and cannot contribute.
	oldExited := make(chan struct{})
	oldCancelCalled := false
	go func() {
		defer close(oldExited)
		loop.watchStop(oldDone, func(opts ...AgentCancelOption) (*CancelHandle, bool) {
			oldCancelCalled = true
			return oldCancel(opts...)
		}, make(chan struct{}), oldNotify)
	}()
	waitOrFail(t, oldExited, "old Stop watcher did not exit")
	require.False(t, oldCancelCalled, "old watcher consumed the current turn's Stop request")

	// The current watcher must still receive both the request and its wakeup.
	currentDone := make(chan struct{})
	stopped := make(chan struct{})
	currentExited := make(chan struct{})
	go func() {
		defer close(currentExited)
		loop.watchStop(currentDone, currentCancelCtx.buildCancelFunc(), stopped, currentNotify)
	}()
	defer func() {
		close(currentDone)
		waitOrFail(t, currentExited, "current Stop watcher did not exit")
	}()
	waitOrFail(t, currentCancelCtx.cancelChan, "Stop did not cancel the current agent")
	waitOrFail(t, stopped, "Stop contribution was not reported")
	assert.Equal(t, CancelImmediate, currentCancelCtx.getMode())
	assert.True(t, currentCancelCtx.isRecursive())
	assert.Equal(t, "shutdown", loop.stopCtrl.cause())
	assert.True(t, loop.stopCtrl.skipCheckpointEnabled())
}

func TestTurnLoop_PreemptWatcherDoesNotConsumeNextTurnRequest(t *testing.T) {
	loop := NewTurnLoop(TurnLoopConfig[string, *schema.Message]{
		GenInput:     genInputConsumeAll,
		PrepareAgent: prepareTestAgent,
	})
	c := loop.preemptCtrl
	c.beginPlanningTurn()
	oldNotify := c.beginActiveTurn(context.Background(), "old")
	oldTarget := c.beginPush()
	c.endPush()
	oldAck := make(chan struct{})
	c.requestPreempt(oldTarget, oldAck, WithAgentCancelMode(CancelAfterToolCalls))
	// The old watcher has selected its notification but is paused before
	// receivePreempt. Finish that turn and enqueue a request for the next one.
	<-oldNotify
	c.endActiveTurn().ack()
	requireAckClosed(t, oldAck)
	c.beginPlanningTurn()
	currentNotify := c.beginActiveTurn(context.Background(), "current")
	currentTarget := c.beginPush()
	c.endPush()
	currentAck := make(chan struct{})
	c.requestPreempt(currentTarget, currentAck, WithAgentCancelMode(CancelAfterChatModel))

	req, ok := c.receivePreempt(oldNotify)
	require.False(t, ok, "old watcher consumed the current turn's Preempt request")
	require.Nil(t, req)
	requireAckOpen(t, currentAck)
	select {
	case <-oldNotify:
		t.Fatal("old watcher received the current turn's notification")
	default:
	}

	// In particular, the stale watcher must not steal the notification needed
	// by watchPreempt, which waits for a wakeup before receiving a request.
	currentCancelCtx := newCancelContext()
	currentDone := make(chan struct{})
	preempted := make(chan struct{})
	currentExited := make(chan struct{})
	go func() {
		defer close(currentExited)
		loop.watchPreempt(currentDone, currentCancelCtx.buildCancelFunc(), preempted, currentNotify)
	}()
	defer func() {
		close(currentDone)
		waitOrFail(t, currentExited, "current Preempt watcher did not exit")
	}()
	waitOrFail(t, currentAck, "current Preempt request was not acknowledged")
	waitOrFail(t, currentCancelCtx.cancelChan, "Preempt did not cancel the current agent")
	waitOrFail(t, preempted, "Preempt contribution was not reported")
	assert.Equal(t, CancelAfterChatModel, currentCancelCtx.getMode())
}
