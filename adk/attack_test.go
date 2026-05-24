package adk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestAttack_ResumeWhileStopped(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	interruptObserved := make(chan struct{})

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string, *schema.Message]{
		InterruptMode: TurnLoopInterruptWaitsForExplicitResume,
		GenInput:      genInputConsumeAllWithMsg,
		PrepareAgent:  prepareAgent(&turnLoopInterruptAgent{interruptInfo: "block"}),
		OnAgentEvents: func(_ context.Context, _ *TurnContext[string, *schema.Message], events *AsyncIterator[*AgentEvent]) error {
			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					close(interruptObserved)
				}
			}
			return nil
		},
	})

	loop.Push("trigger")
	waitOrFail(t, interruptObserved, "interrupt not observed")

	loop.Stop()

	err := loop.Resume("after-stop")
	require.ErrorIs(t, err, ErrTurnLoopStopped)

	exit := loop.Wait()
	require.NoError(t, exit.ExitReason)
}

func TestAttack_ConcurrentDuplicateResume(t *testing.T) {
	t.Parallel()

	loop := NewTurnLoop(TurnLoopConfig[string, *schema.Message]{
		InterruptMode: TurnLoopInterruptWaitsForExplicitResume,
		GenInput:      genInputConsumeAll,
		PrepareAgent:  prepareTestAgent,
	})
	loop.pendingResume = &turnLoopPendingResume[string]{
		source:      turnLoopPendingResumeSourceManagedInterrupt,
		resumeBytes: []byte("runner-checkpoint"),
	}

	const workers = 2
	results := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- loop.Resume(fmt.Sprintf("resume-%d", i))
		}(i)
	}
	wg.Wait()
	close(results)

	var accepted, duplicates int
	for err := range results {
		if err == nil {
			accepted++
		} else if errors.Is(err, ErrTurnLoopResumeInProgress) {
			duplicates++
		}
	}
	assert.Equal(t, 1, accepted, "exactly one Resume should succeed")
	assert.Equal(t, workers-1, duplicates, "remaining should get ErrTurnLoopResumeInProgress")
	assert.True(t, loop.pendingResume.resumeSubmitted)
	assert.Len(t, loop.pendingResume.resumeItems, 1)
}

func TestAttack_SessionEventPersisterLatchedError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSessionHelperStore()
	store.appendErr = errors.New("permanent disk failure")

	cfg := normalizeSessionPersistenceConfig(&SessionPersistenceConfig{
		EventFlushBatchSize:      1,
		EventFlushInterval:       5 * time.Millisecond,
		EventBufferSize:          8,
		MaxFlushRetries:          0,
		FlushRetryInitialBackoff: time.Millisecond,
	})
	p := newSessionEventPersister[*schema.Message](ctx, store, "latched-sid", cfg)

	require.NoError(t, p.enqueue(validTestPayload()))

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if p.getErr() != nil {
			break
		}
		time.Sleep(2 * time.Millisecond)
	}
	require.Error(t, p.getErr(), "persister must latch the store error")

	for i := 0; i < 5; i++ {
		err := p.enqueue(validTestPayload())
		require.Error(t, err, "enqueue after latch must return error")
		assert.Contains(t, err.Error(), "permanent disk failure")
	}

	_ = p.closeAndWait()
}

func TestAttack_ReconstructSessionWithCorruptEvent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "corrupt-event"

	msg := schema.UserMessage("valid")
	EnsureMessageID(msg)
	se := withTestEventID(&SessionEvent[*schema.Message]{Kind: SessionEventMessage, Message: msg})
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))

	corruptPayload := []byte(`{"event_id":"` + uuid.NewString() + `","kind":"message","message":` + "\x00\xff invalid json")
	require.False(t, json.Valid(corruptPayload), "payload must be invalid JSON")
	store.mu.Lock()
	store.events = append(store.events, corruptPayload)
	store.eventIDs = append(store.eventIDs, uuid.NewString())
	store.eventIDIdx[store.eventIDs[len(store.eventIDs)-1]] = len(store.events) - 1
	store.mu.Unlock()

	_, err = reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.Error(t, err, "corrupt event must cause reconstruction failure")
}

func TestAttack_EmptyResumeItems(t *testing.T) {
	t.Parallel()

	loop := NewTurnLoop(TurnLoopConfig[string, *schema.Message]{
		InterruptMode: TurnLoopInterruptWaitsForExplicitResume,
		GenInput:      genInputConsumeAll,
		PrepareAgent:  prepareTestAgent,
	})
	loop.pendingResume = &turnLoopPendingResume[string]{
		source:      turnLoopPendingResumeSourceManagedInterrupt,
		resumeBytes: []byte("checkpoint"),
	}

	err := loop.Resume()
	require.ErrorIs(t, err, ErrTurnLoopEmptyResume)
}

func TestAttack_PushAfterTakeLateItems(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string, *schema.Message]{
		GenInput:     genInputConsumeAll,
		PrepareAgent: prepareTestAgent,
	})

	loop.Stop()
	exit := loop.Wait()
	require.NoError(t, exit.ExitReason)

	exit.TakeLateItems()

	require.Panics(t, func() {
		loop.Push("after-sealed")
	})
}

func TestAttack_SessionEventIDMismatchGuard(t *testing.T) {
	t.Parallel()

	agentEventID := uuid.NewString()
	sessionEventID := uuid.NewString()
	require.NotEqual(t, agentEventID, sessionEventID)

	err := validateAgentSessionEventIdentity(&AgentEvent{
		EventID: agentEventID,
		SessionEvent: &SessionEvent[*schema.Message]{
			EventID: sessionEventID,
			Kind:    SessionEventAgentThinking,
			AgentObservation: &AgentObservationEvent{
				Thinking: &AgentThinkingEvent{},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session event identity mismatch")

	sameID := uuid.NewString()
	err = validateAgentSessionEventIdentity(&AgentEvent{
		EventID: sameID,
		SessionEvent: &SessionEvent[*schema.Message]{
			EventID: sameID,
			Kind:    SessionEventAgentThinking,
			AgentObservation: &AgentObservationEvent{
				Thinking: &AgentThinkingEvent{},
			},
		},
	})
	require.NoError(t, err)
}

func TestAttack_StopWhileWaitingForResume(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	interruptObserved := make(chan struct{})

	var prepareCount int32
	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string, *schema.Message]{
		InterruptMode: TurnLoopInterruptWaitsForExplicitResume,
		GenInput:      genInputConsumeAllWithMsg,
		PrepareAgent: func(_ context.Context, _ *TurnLoop[string, *schema.Message], _ []string) (Agent, error) {
			atomic.AddInt32(&prepareCount, 1)
			return &turnLoopInterruptAgent{interruptInfo: "wait_stop"}, nil
		},
		OnAgentEvents: func(_ context.Context, _ *TurnContext[string, *schema.Message], events *AsyncIterator[*AgentEvent]) error {
			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					close(interruptObserved)
				}
			}
			return nil
		},
	})

	loop.Push("trigger")
	waitOrFail(t, interruptObserved, "interrupt not observed")

	done := make(chan struct{})
	go func() {
		loop.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() deadlocked while waiting for resume")
	}

	exitCh := make(chan *TurnLoopExitState[string, *schema.Message], 1)
	go func() {
		exitCh <- loop.Wait()
	}()

	select {
	case exit := <-exitCh:
		require.NoError(t, exit.ExitReason)
	case <-time.After(2 * time.Second):
		t.Fatal("Wait() deadlocked after Stop()")
	}
}

func TestAttack_ManagedInterrupt_GenResumeError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	interruptObserved := make(chan struct{})
	genResumeErr := errors.New("policy: cannot resume this interrupt")

	loop := newAndRunTurnLoop(ctx, TurnLoopConfig[string, *schema.Message]{
		InterruptMode: TurnLoopInterruptWaitsForExplicitResume,
		GenInput:      genInputConsumeAllWithMsg,
		GenResume: func(_ context.Context, _ *TurnLoop[string, *schema.Message], _, _, _ []string) (*GenResumeResult[string, *schema.Message], error) {
			return nil, genResumeErr
		},
		PrepareAgent: func(_ context.Context, _ *TurnLoop[string, *schema.Message], _ []string) (Agent, error) {
			return &turnLoopInterruptAgent{interruptInfo: "test_resume_err"}, nil
		},
		OnAgentEvents: func(_ context.Context, _ *TurnContext[string, *schema.Message], events *AsyncIterator[*AgentEvent]) error {
			for {
				event, ok := events.Next()
				if !ok {
					break
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					close(interruptObserved)
				}
			}
			return nil
		},
	})

	loop.Push("trigger")
	waitOrFail(t, interruptObserved, "interrupt not observed")

	require.Eventually(t, func() bool {
		return loop.Resume("response") == nil
	}, 2*time.Second, 10*time.Millisecond, "Resume should eventually be accepted")

	exit := loop.Wait()
	require.Error(t, exit.ExitReason, "loop should exit with GenResume error")
	assert.ErrorIs(t, exit.ExitReason, genResumeErr)
}
