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

	err1Ch := make(chan error, 1)
	go func() {
		err1Ch <- cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	}()

	select {
	case <-cc.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("cancel did not start")
	}

	err2Ch := make(chan error, 1)
	go func() {
		err2Ch <- cancelFn(WithAgentCancelMode(CancelImmediate))
	}()

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&interruptCalls) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&interruptCalls))

	cancelErr := cc.createCancelError()
	assert.Equal(t, CancelImmediate, cancelErr.Info.Mode)
	assert.True(t, cancelErr.Info.Escalated)
	assert.False(t, cancelErr.Info.Timeout)

	assert.True(t, cc.markCancelHandled())

	assert.NoError(t, <-err1Ch)
	assert.NoError(t, <-err2Ch)
}

func TestAgentCancelFunc_MultiCall_JoinSafePointModes(t *testing.T) {
	cc := newCancelContext()
	cancelFn := cc.buildCancelFunc()

	err1Ch := make(chan error, 1)
	go func() {
		err1Ch <- cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	}()

	select {
	case <-cc.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("cancel did not start")
	}

	err2Ch := make(chan error, 1)
	go func() {
		err2Ch <- cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
	}()

	deadline := time.Now().Add(1 * time.Second)
	want := CancelAfterChatModel | CancelAfterToolCalls
	for time.Now().Before(deadline) && cc.getMode() != want {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, want, cc.getMode())

	assert.True(t, cc.markCancelHandled())
	assert.NoError(t, <-err1Ch)
	assert.NoError(t, <-err2Ch)
}

func TestAgentCancelFunc_MultiCall_TimeoutDeadlineJoinUsesAbsoluteTime(t *testing.T) {
	cc := newCancelContext()
	cancelFn := cc.buildCancelFunc()

	err1Ch := make(chan error, 1)
	go func() {
		err1Ch <- cancelFn(
			WithAgentCancelMode(CancelAfterChatModel),
			WithAgentCancelTimeout(200*time.Millisecond),
		)
	}()

	select {
	case <-cc.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("cancel did not start")
	}

	firstDeadline := cc.getDeadlineUnixNano()
	assert.NotZero(t, firstDeadline)

	time.Sleep(50 * time.Millisecond)

	err2Ch := make(chan error, 1)
	go func() {
		err2Ch <- cancelFn(
			WithAgentCancelMode(CancelAfterToolCalls),
			WithAgentCancelTimeout(60*time.Millisecond),
		)
	}()

	deadline := time.Now().Add(1 * time.Second)
	var secondDeadline int64
	for time.Now().Before(deadline) {
		secondDeadline = cc.getDeadlineUnixNano()
		if secondDeadline != 0 && secondDeadline != firstDeadline {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	assert.NotZero(t, secondDeadline)
	assert.Less(t, secondDeadline, firstDeadline)

	assert.True(t, cc.markCancelHandled())
	assert.NoError(t, <-err1Ch)
	assert.NoError(t, <-err2Ch)
}

func TestAgentCancelFunc_MultiCall_TimeoutEscalationReturnsErrCancelTimeout(t *testing.T) {
	cc := newCancelContext()
	var interruptCalls int32
	cc.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
		atomic.AddInt32(&interruptCalls, 1)
	})
	cancelFn := cc.buildCancelFunc()

	errCh := make(chan error, 1)
	go func() {
		errCh <- cancelFn(
			WithAgentCancelMode(CancelAfterChatModel),
			WithAgentCancelTimeout(30*time.Millisecond),
		)
	}()

	select {
	case <-cc.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("cancel did not start")
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) && atomic.LoadInt32(&interruptCalls) == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	assert.Equal(t, int32(1), atomic.LoadInt32(&interruptCalls))

	cancelErr := cc.createCancelError()
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.True(t, cancelErr.Info.Escalated)
	assert.True(t, cancelErr.Info.Timeout)

	assert.True(t, cc.markCancelHandled())
	assert.Equal(t, ErrCancelTimeout, <-errCh)
}

