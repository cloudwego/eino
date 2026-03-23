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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/compose"
)

func TestAgentCancelFunc_MultiCall_EscalateToImmediate(t *testing.T) {
	cc := newCancelContext()
	var interruptCalls int32
	cc.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
		atomic.AddInt32(&interruptCalls, 1)
	})
	cancelFn := cc.buildCancelFunc()

	handle1 := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	handle2 := cancelFn(WithAgentCancelMode(CancelImmediate))
	assert.Equal(t, int32(1), atomic.LoadInt32(&interruptCalls))

	cancelErr := cc.createCancelError()
	assert.Equal(t, CancelImmediate, cancelErr.Info.Mode)
	assert.True(t, cancelErr.Info.Escalated)
	assert.False(t, cancelErr.Info.Timeout)

	assert.True(t, cc.markCancelHandled())
	assert.NoError(t, handle1.Wait())
	assert.NoError(t, handle2.Wait())
}

func TestAgentCancelFunc_MultiCall_JoinSafePointModes(t *testing.T) {
	cc := newCancelContext()
	cancelFn := cc.buildCancelFunc()

	handle1 := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	handle2 := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))

	want := CancelAfterChatModel | CancelAfterToolCalls
	assert.Equal(t, want, cc.getMode())

	assert.True(t, cc.markCancelHandled())
	assert.NoError(t, handle1.Wait())
	assert.NoError(t, handle2.Wait())
}

func TestAgentCancelFunc_MultiCall_TimeoutDeadlineJoinUsesAbsoluteTime(t *testing.T) {
	cc := newCancelContext()
	cancelFn := cc.buildCancelFunc()

	handle1 := cancelFn(
		WithAgentCancelMode(CancelAfterChatModel),
		WithAgentCancelTimeout(200*time.Millisecond),
	)

	firstDeadline := cc.getDeadlineUnixNano()
	assert.NotZero(t, firstDeadline)

	time.Sleep(50 * time.Millisecond)

	handle2 := cancelFn(
		WithAgentCancelMode(CancelAfterToolCalls),
		WithAgentCancelTimeout(60*time.Millisecond),
	)

	secondDeadline := cc.getDeadlineUnixNano()
	assert.NotZero(t, secondDeadline)
	assert.Less(t, secondDeadline, firstDeadline)

	assert.True(t, cc.markCancelHandled())
	assert.NoError(t, handle1.Wait())
	assert.NoError(t, handle2.Wait())
}

func TestAgentCancelFunc_MultiCall_TimeoutEscalationReturnsErrCancelTimeout(t *testing.T) {
	cc := newCancelContext()
	var interruptCalls int32
	interruptCh := make(chan struct{}, 1)
	cc.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
		atomic.AddInt32(&interruptCalls, 1)
		select {
		case interruptCh <- struct{}{}:
		default:
		}
	})
	cancelFn := cc.buildCancelFunc()
	handle := cancelFn(
		WithAgentCancelMode(CancelAfterChatModel),
		WithAgentCancelTimeout(30*time.Millisecond),
	)

	select {
	case <-interruptCh:
	case <-time.After(1 * time.Second):
		t.Fatal("timeout escalation did not interrupt")
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&interruptCalls))

	cancelErr := cc.createCancelError()
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.True(t, cancelErr.Info.Escalated)
	assert.True(t, cancelErr.Info.Timeout)

	assert.True(t, cc.markCancelHandled())
	assert.Equal(t, ErrCancelTimeout, handle.Wait())
}
