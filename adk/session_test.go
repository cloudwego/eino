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
	"bytes"
	"context"
	"encoding/gob"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// loadLatestTypedTurnEnd decodes the latest TurnEndState snapshot from a SessionStore.
// This is a test helper that wraps the raw store call with deserialization.
// It is unexported because external users should interact with session state
// through TurnContext within TurnLoop callbacks, not by reading the store directly.
func loadLatestTypedTurnEnd[M MessageType](
	ctx context.Context,
	store SessionStore,
	sessionID string,
) (*TurnEndState[M], int, error) {
	if store == nil {
		return nil, 0, errors.New("session store is nil")
	}
	turnIndex, payload, exists, err := store.LoadLatestTurnEnd(ctx, sessionID)
	if err != nil {
		return nil, 0, err
	}
	if !exists {
		return nil, 0, nil
	}
	state, err := decodeTurnEndState[M](payload)
	if err != nil {
		return nil, 0, err
	}
	return state, turnIndex, nil
}

// loadTypedEvents decodes persisted AgentEvents from a bounded turn-index range.
// This is a test helper that wraps the raw store call with deserialization.
// It is unexported because external users should interact with session state
// through TurnContext within TurnLoop callbacks, not by reading the store directly.
func loadTypedEvents[M MessageType](
	ctx context.Context,
	store SessionStore,
	sessionID string,
	fromTurnIndex, toTurnIndex int,
) ([]*TypedAgentEvent[M], error) {
	if store == nil {
		return nil, errors.New("session store is nil")
	}
	records, err := store.LoadEvents(ctx, sessionID, fromTurnIndex, toTurnIndex)
	if err != nil {
		return nil, err
	}
	events := make([]*TypedAgentEvent[M], 0, len(records))
	for i := range records {
		event, err := decodeAgentEvent[M](records[i].Payload)
		if err != nil {
			return nil, fmt.Errorf("decode event turn=%d seq=%d: %w", records[i].TurnIndex, records[i].Seq, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func decodeAgentEvent[M MessageType](payload []byte) (*TypedAgentEvent[M], error) {
	var event TypedAgentEvent[M]
	if err := gob.NewDecoder(bytes.NewReader(payload)).Decode(&event); err != nil {
		return nil, err
	}
	return &event, nil
}

type sessionHelperStore struct {
	checkpoints map[string][]byte

	events      []EventRecord
	loadErr     error
	turnIndex   int
	turnPayload []byte
	turnExists  bool
	turnErr     error
}

type runnerSessionAgent struct {
	name    string
	inputs  [][]*schema.Message
	values  []map[string]any
	turnEnd *TurnEndState[*schema.Message]
}

func (a *runnerSessionAgent) Name(_ context.Context) string        { return a.name }
func (a *runnerSessionAgent) Description(_ context.Context) string { return "runner session agent" }
func (a *runnerSessionAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	a.inputs = append(a.inputs, append([]*schema.Message{}, input.Messages...))
	a.values = append(a.values, GetSessionValues(ctx))
	turnEnd := a.turnEnd
	if turnEnd == nil {
		turnEnd = &TurnEndState[*schema.Message]{Messages: append([]*schema.Message{}, input.Messages...)}
	}
	go func() {
		defer gen.Close()
		gen.Send(&AgentEvent{
			AgentName: a.name,
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{Message: schema.AssistantMessage("ok", nil), Role: schema.Assistant},
			},
		})
		gen.Send(&AgentEvent{AgentName: a.name, TurnEndState: turnEnd})
	}()
	return iter
}

func newSessionHelperStore() *sessionHelperStore {
	return &sessionHelperStore{checkpoints: make(map[string][]byte)}
}

func (s *sessionHelperStore) Set(_ context.Context, key string, value []byte) error {
	s.checkpoints[key] = append([]byte{}, value...)
	return nil
}

func (s *sessionHelperStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := s.checkpoints[key]
	return append([]byte{}, v...), ok, nil
}

func (s *sessionHelperStore) Delete(_ context.Context, key string) error {
	delete(s.checkpoints, key)
	return nil
}

func (s *sessionHelperStore) AppendEvents(_ context.Context, _ string, _ int, entries []EventRecord) error {
	s.events = append(s.events, entries...)
	return nil
}

func (s *sessionHelperStore) LoadEvents(_ context.Context, _ string, _, _ int) ([]EventRecord, error) {
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	return append([]EventRecord{}, s.events...), nil
}

func (s *sessionHelperStore) LoadLatestTurnEnd(_ context.Context, _ string) (int, []byte, bool, error) {
	if s.turnErr != nil {
		return 0, nil, false, s.turnErr
	}
	return s.turnIndex, append([]byte{}, s.turnPayload...), s.turnExists, nil
}

func (s *sessionHelperStore) SaveTurnEnd(_ context.Context, _ string, turnIndex int, turnEnd []byte) error {
	s.turnIndex = turnIndex
	s.turnPayload = append([]byte{}, turnEnd...)
	s.turnExists = true
	return nil
}

func TestLoadLatestTurnEndErrorPaths(t *testing.T) {
	ctx := context.Background()

	state, turnIndex, err := loadLatestTypedTurnEnd[*schema.Message](ctx, nil, "session")
	require.Error(t, err)
	assert.Nil(t, state)
	assert.Equal(t, 0, turnIndex)
	assert.Contains(t, err.Error(), "session store is nil")

	store := newSessionHelperStore()
	state, turnIndex, err = loadLatestTypedTurnEnd[*schema.Message](ctx, store, "session")
	require.NoError(t, err)
	assert.Nil(t, state)
	assert.Equal(t, 0, turnIndex)

	store.turnErr = errors.New("load latest failed")
	state, turnIndex, err = loadLatestTypedTurnEnd[*schema.Message](ctx, store, "session")
	require.ErrorIs(t, err, store.turnErr)
	assert.Nil(t, state)
	assert.Equal(t, 0, turnIndex)

	store.turnErr = nil
	store.turnExists = true
	store.turnIndex = 3
	store.turnPayload = []byte("not gob")
	state, turnIndex, err = loadLatestTypedTurnEnd[*schema.Message](ctx, store, "session")
	require.Error(t, err)
	assert.Nil(t, state)
	assert.Equal(t, 0, turnIndex)
}

func TestLoadTypedEventsErrorPaths(t *testing.T) {
	ctx := context.Background()

	events, err := loadTypedEvents[*schema.Message](ctx, nil, "session", 1, 1)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.Contains(t, err.Error(), "session store is nil")

	store := newSessionHelperStore()
	store.loadErr = errors.New("load events failed")
	events, err = loadTypedEvents[*schema.Message](ctx, store, "session", 1, 1)
	require.ErrorIs(t, err, store.loadErr)
	assert.Nil(t, events)

	store.loadErr = nil
	store.events = []EventRecord{{TurnIndex: 2, Seq: 7, Payload: []byte("not gob")}}
	events, err = loadTypedEvents[*schema.Message](ctx, store, "session", 1, 3)
	require.Error(t, err)
	assert.Nil(t, events)
	assert.Contains(t, err.Error(), "decode event turn=2 seq=7")
}

func TestSessionEventSplitAndRecord(t *testing.T) {
	outputEvent := EventFromMessage(schema.AssistantMessage("answer", nil), nil, schema.Assistant, "")
	event := &AgentEvent{
		AgentName: "agent",
		Output:    outputEvent.Output,
		TurnEndState: &TurnEndState[*schema.Message]{
			Messages:      []*schema.Message{schema.UserMessage("question"), schema.AssistantMessage("answer", nil)},
			SessionValues: map[string]any{"answer": "answer"},
		},
	}

	persisted, live := splitPersistentAndLiveEvent(event)
	require.NotNil(t, persisted)
	require.NotNil(t, live)
	require.NotNil(t, persisted.TurnEndState)
	require.NotNil(t, persisted.Output)
	require.NotNil(t, live.TurnEndState)
	strippedLive := stripSessionEventFields(live)
	require.NotNil(t, strippedLive)
	assert.Nil(t, strippedLive.TurnEndState)
	require.NotNil(t, live.Output)
	assert.Equal(t, "answer", live.Output.MessageOutput.Message.Content)

	record, err := makeEventRecord(5, 9, persisted)
	require.NoError(t, err)
	assert.Equal(t, 5, record.TurnIndex)
	assert.Equal(t, int64(9), record.Seq)
	assert.Equal(t, "output,turn_end", record.Kind)
	require.NotEmpty(t, record.Payload)

	decoded, err := decodeAgentEvent[*schema.Message](record.Payload)
	require.NoError(t, err)
	require.NotNil(t, decoded.TurnEndState)
	assert.Equal(t, "answer", decoded.TurnEndState.SessionValues["answer"])
	require.NotNil(t, decoded.Output)
	assert.Equal(t, "answer", decoded.Output.MessageOutput.Message.Content)

	turnEndOnly := stripSessionEventFields(&AgentEvent{TurnEndState: event.TurnEndState})
	assert.Nil(t, turnEndOnly)

	errOnly := stripSessionEventFields(&AgentEvent{Err: errors.New("visible"), TurnEndState: event.TurnEndState})
	require.NotNil(t, errOnly)
	assert.Nil(t, errOnly.TurnEndState)
	assert.EqualError(t, errOnly.Err, "visible")

	emptyRecord, err := makeEventRecord(1, 1, &AgentEvent{Err: errors.New("not persisted")})
	require.NoError(t, err)
	assert.Equal(t, EventRecord{}, emptyRecord)
}

func TestRunnerSessionModePrependsCommittedMessagesOnce(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "runner-session"
	firstAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages:      []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil)},
			SessionValues: map[string]any{"k": "restored"},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              firstAgent,
		SessionID:          sessionID,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))

	secondAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages:      []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil), schema.UserMessage("second"), schema.AssistantMessage("answer2", nil)},
			SessionValues: map[string]any{"k": "next"},
		},
	}
	runner = NewRunner(ctx, RunnerConfig{
		Agent:              secondAgent,
		SessionID:          sessionID,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "second", WithSessionValues(map[string]any{"override": "value"})))

	require.Len(t, secondAgent.inputs, 1)
	require.Len(t, secondAgent.inputs[0], 3)
	assert.Equal(t, "first", secondAgent.inputs[0][0].Content)
	assert.Equal(t, "answer1", secondAgent.inputs[0][1].Content)
	assert.Equal(t, "second", secondAgent.inputs[0][2].Content)
	require.Len(t, secondAgent.values, 1)
	assert.Equal(t, "restored", secondAgent.values[0]["k"])
	assert.Equal(t, "value", secondAgent.values[0]["override"])
}

func TestRunnerSessionModeRejectsPendingCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "runner-pending-session"
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{TurnIndex: 2, NextEventSeq: 1, Payload: []byte("opaque")})
	require.NoError(t, err)
	require.NoError(t, store.Set(ctx, sessionRunnerCheckpointID(sessionID), cpBytes))

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerSessionAgent{name: "runner-session-agent"},
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})
	iter := runner.Query(ctx, "new input")
	event, ok := iter.Next()
	require.True(t, ok)
	require.ErrorIs(t, event.Err, ErrPendingSessionCheckpoint)
	_, ok = iter.Next()
	require.False(t, ok)
}

func TestRunnerSessionModeDeletesStaleCheckpointOnResume(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "runner-stale-session"

	turnEndBytes, err := encodeTurnEndState(&TurnEndState[*schema.Message]{
		Messages: []*schema.Message{schema.AssistantMessage("committed", nil)},
	})
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sessionID, 2, turnEndBytes))

	checkpointID := sessionRunnerCheckpointID(sessionID)
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		TurnIndex:    2,
		NextEventSeq: 3,
		Payload:      []byte("stale"),
	})
	require.NoError(t, err)
	require.NoError(t, store.Set(ctx, checkpointID, cpBytes))

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerSessionAgent{name: "runner-session-agent"},
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})

	iter, err := runner.Resume(ctx, "")
	require.Error(t, err)
	assert.Nil(t, iter)
	assert.Contains(t, err.Error(), "no pending session checkpoint")

	_, exists, err := store.Get(ctx, checkpointID)
	require.NoError(t, err)
	assert.False(t, exists)
}

func drainSessionEvents(t *testing.T, iter *AsyncIterator[*AgentEvent]) {
	t.Helper()
	for {
		event, ok := iter.Next()
		if !ok {
			return
		}
		require.NoError(t, event.Err)
	}
}

// runnerInterruptAgent is a test agent for Runner-level interrupt/resume tests.
// On first Run it produces an interrupt event; on Resume it emits "resumed ok".
type runnerInterruptAgent struct {
	callCount int32
}

func (a *runnerInterruptAgent) Name(_ context.Context) string        { return "InterruptAgent" }
func (a *runnerInterruptAgent) Description(_ context.Context) string { return "runner interrupt agent" }

func (a *runnerInterruptAgent) Run(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	atomic.AddInt32(&a.callCount, 1)
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		event := Interrupt(ctx, "confirm?")
		gen.Send(event)
	}()
	return iter
}

func (a *runnerInterruptAgent) Resume(ctx context.Context, info *ResumeInfo, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	atomic.AddInt32(&a.callCount, 1)
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(&AgentEvent{
			AgentName: "InterruptAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.AssistantMessage("resumed ok", nil),
					Role:    schema.Assistant,
				},
			},
		})
		gen.Send(&AgentEvent{
			AgentName: "InterruptAgent",
			TurnEndState: &TurnEndState[*schema.Message]{
				Messages: []*schema.Message{schema.AssistantMessage("resumed ok", nil)},
			},
		})
	}()
	return iter
}

func TestRunnerSessionModeResumeWithEmptyCheckpointID(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "resume-test"

	agent := &runnerInterruptAgent{}

	// Step 1: run query to produce an interrupt and persist a session checkpoint.
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})

	iter := runner.Query(ctx, "hello")
	var sawInterrupt bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			sawInterrupt = true
		}
	}
	require.True(t, sawInterrupt, "should receive interrupt event from initial query")

	// Step 2: Resume with empty checkpoint ID — should resolve from session.
	t.Run("Resume", func(t *testing.T) {
		resumeIter, err := runner.Resume(ctx, "")
		require.NoError(t, err)

		var gotResumedOK bool
		for {
			event, ok := resumeIter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err, "Resume with empty checkpoint ID should not error")
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Content == "resumed ok" {
				gotResumedOK = true
			}
		}
		assert.True(t, gotResumedOK, "should see 'resumed ok' message after resume")
	})

	// Step 3: Re-interrupt so we can test ResumeWithParams.
	agent2 := &runnerInterruptAgent{}
	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           agent2,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})

	// Run again to create a fresh interrupt checkpoint.
	iter2 := runner2.Query(ctx, "hello again")
	var sawInterrupt2 bool
	for {
		event, ok := iter2.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			sawInterrupt2 = true
		}
	}
	require.True(t, sawInterrupt2, "should receive interrupt event for ResumeWithParams test")

	t.Run("ResumeWithParams", func(t *testing.T) {
		resumeIter, err := runner2.ResumeWithParams(ctx, "", &ResumeParams{
			Targets: map[string]any{"agent:InterruptAgent": "override"},
		})
		require.NoError(t, err)

		var gotResumedOK bool
		for {
			event, ok := resumeIter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err, "ResumeWithParams with empty checkpoint ID should not error")
			if event.Output != nil && event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Message != nil &&
				event.Output.MessageOutput.Message.Content == "resumed ok" {
				gotResumedOK = true
			}
		}
		assert.True(t, gotResumedOK, "should see 'resumed ok' message after ResumeWithParams")
	})
}

// failingAppendStore wraps sessionHelperStore but always returns an error from AppendEvents.
type failingAppendStore struct {
	*sessionHelperStore
	appendErr error
}

func (s *failingAppendStore) AppendEvents(_ context.Context, _ string, _ int, _ []EventRecord) error {
	return s.appendErr
}

func TestRunnerSessionModeFlushFailurePreventsCommit(t *testing.T) {
	ctx := context.Background()
	inner := newSessionHelperStore()
	store := &failingAppendStore{
		sessionHelperStore: inner,
		appendErr:          errors.New("disk full"),
	}

	agent := &runnerSessionAgent{
		name: "flush-fail-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("done", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          "flush-fail-session",
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "trigger")
	var lastErr error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			lastErr = event.Err
		}
	}

	require.Error(t, lastErr, "should get an error event from flush failure")
	assert.Contains(t, lastErr.Error(), "failed to persist session events")

	// SaveTurnEnd should NOT have been called because flush failed.
	assert.False(t, inner.turnExists, "SaveTurnEnd must not be called when event flush fails")
}

// TestSessionPersister_EnqueueAfterClose verifies that calling enqueue after
// closeAndWait does not panic (send on closed channel), confirming the atomic
// closed-flag guard works correctly.
func TestSessionPersister_EnqueueAfterClose(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "enqueue-after-close", 1,
		&SessionPersistenceConfig{
			EventFlushBatchSize: 1,
			EventFlushInterval:  time.Millisecond,
			EventBufferSize:     8,
		},
	)

	err := persister.closeAndWait()
	require.NoError(t, err)

	event := &AgentEvent{
		AgentName: "agent",
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				Message: schema.AssistantMessage("late-event", nil),
				Role:    schema.Assistant,
			},
		},
	}
	record, err := makeEventRecord(1, 1, event)
	require.NoError(t, err)
	require.NotEmpty(t, record.Payload)

	// Must not panic.
	err = persister.enqueue(record)
	assert.NoError(t, err)
}

// TestTurnEndState_GobRoundtripNilFields verifies that gob encode/decode
// roundtrip preserves nil semantics for all TurnEndState fields.
func TestTurnEndState_GobRoundtripNilFields(t *testing.T) {
	original := &TurnEndState[*schema.Message]{
		Messages:          nil,
		ToolInfos:         nil,
		DeferredToolInfos: nil,
		SessionValues:     nil,
	}

	encoded, err := encodeTurnEndState(original)
	require.NoError(t, err)
	require.NotEmpty(t, encoded)

	decoded, err := decodeTurnEndState[*schema.Message](encoded)
	require.NoError(t, err)

	assert.Nil(t, decoded.Messages, "nil Messages should roundtrip as nil")
	assert.Nil(t, decoded.ToolInfos, "nil ToolInfos should roundtrip as nil")
	assert.Nil(t, decoded.DeferredToolInfos, "nil DeferredToolInfos should roundtrip as nil")
	assert.Nil(t, decoded.SessionValues, "nil SessionValues should roundtrip as nil")
}

// TestSessionPersister_EmptyPayloadSkipped verifies that enqueue silently
// discards records with empty Payload without error.
func TestSessionPersister_EmptyPayloadSkipped(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "empty-payload", 1,
		&SessionPersistenceConfig{
			EventFlushBatchSize: 1,
			EventFlushInterval:  time.Millisecond,
			EventBufferSize:     8,
		},
	)

	// Empty payload records should be skipped.
	emptyRecord := EventRecord{TurnIndex: 1, Seq: 1, Kind: "output", Payload: nil}
	assert.NoError(t, persister.enqueue(emptyRecord))

	emptyRecord2 := EventRecord{TurnIndex: 1, Seq: 2, Kind: "output", Payload: []byte{}}
	assert.NoError(t, persister.enqueue(emptyRecord2))

	// A real event should still work.
	event := &AgentEvent{
		AgentName: "agent",
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				Message: schema.AssistantMessage("real", nil),
				Role:    schema.Assistant,
			},
		},
	}
	record, err := makeEventRecord(1, 3, event)
	require.NoError(t, err)
	require.NotEmpty(t, record.Payload)
	require.NoError(t, persister.enqueue(record))

	err = persister.closeAndWait()
	require.NoError(t, err)

	require.Len(t, store.events, 1, "only the real event should be persisted")
	assert.Equal(t, int64(3), store.events[0].Seq)
}

func TestSplitPersistentAndLiveEvent_StreamingCopiesBothStreams(t *testing.T) {
	chunk1 := schema.AssistantMessage("hello ", nil)
	chunk2 := schema.AssistantMessage("world", nil)
	stream := schema.StreamReaderFromArray([]*schema.Message{chunk1, chunk2})

	event := &AgentEvent{
		AgentName: "agent",
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				IsStreaming:   true,
				MessageStream: stream,
				Role:          schema.Assistant,
			},
		},
		TurnEndState: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello world", nil)},
		},
	}

	persisted, live := splitPersistentAndLiveEvent(event)
	require.NotNil(t, persisted)
	require.NotNil(t, live)
	require.NotNil(t, persisted.Output)
	require.NotNil(t, live.Output)
	require.NotNil(t, persisted.Output.MessageOutput)
	require.NotNil(t, live.Output.MessageOutput)
	assert.True(t, persisted.Output.MessageOutput.IsStreaming)
	assert.True(t, live.Output.MessageOutput.IsStreaming)

	// Both streams should be independently consumable.
	require.NotNil(t, persisted.Output.MessageOutput.MessageStream)
	require.NotNil(t, live.Output.MessageOutput.MessageStream)

	pMsg, err := schema.ConcatMessageStream(persisted.Output.MessageOutput.MessageStream)
	require.NoError(t, err)
	assert.Equal(t, "hello world", pMsg.Content)

	lMsg, err := schema.ConcatMessageStream(live.Output.MessageOutput.MessageStream)
	require.NoError(t, err)
	assert.Equal(t, "hello world", lMsg.Content)

	// TurnEndState should be on persisted but not live
	require.NotNil(t, persisted.TurnEndState)
	assert.NotNil(t, live.TurnEndState) // live retains the original TurnEndState
}

func TestPersisterTimerFlush_FlushesBeforeBatchSizeReached(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	// Large batch size (100) ensures batch-size flush won't trigger.
	// Short timer (10ms) ensures timer flush will trigger.
	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "timer-flush", 1,
		&SessionPersistenceConfig{
			EventFlushBatchSize: 100,
			EventFlushInterval:  10 * time.Millisecond,
			EventBufferSize:     16,
		},
	)

	event := &AgentEvent{
		AgentName: "agent",
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				Message: schema.AssistantMessage("timer-event", nil),
				Role:    schema.Assistant,
			},
		},
	}
	record, err := makeEventRecord(1, 1, event)
	require.NoError(t, err)
	require.NoError(t, persister.enqueue(record))

	// Wait long enough for the timer to flush (50ms >> 10ms interval).
	time.Sleep(50 * time.Millisecond)

	// Close and verify.
	require.NoError(t, persister.closeAndWait())
	require.Len(t, store.events, 1, "timer should have flushed the single event")
	assert.Equal(t, int64(1), store.events[0].Seq)
}

func TestNormalizeSessionPersistenceConfig_Variations(t *testing.T) {
	// nil input: all defaults
	cfg := normalizeSessionPersistenceConfig(nil)
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)
	assert.Equal(t, defaultSessionEventBufferSize, cfg.EventBufferSize)

	// All-zero input: all defaults
	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{})
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)
	assert.Equal(t, defaultSessionEventBufferSize, cfg.EventBufferSize)

	// Partial: only BatchSize set
	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{EventFlushBatchSize: 32})
	assert.Equal(t, 32, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)
	assert.Equal(t, defaultSessionEventBufferSize, cfg.EventBufferSize)

	// All custom
	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{
		EventFlushBatchSize: 8,
		EventFlushInterval:  200 * time.Millisecond,
		EventBufferSize:     128,
	})
	assert.Equal(t, 8, cfg.EventFlushBatchSize)
	assert.Equal(t, 200*time.Millisecond, cfg.EventFlushInterval)
	assert.Equal(t, 128, cfg.EventBufferSize)

	// Negative values: treated as zero, use defaults
	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{
		EventFlushBatchSize: -1,
		EventFlushInterval:  -time.Second,
		EventBufferSize:     -5,
	})
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)
	assert.Equal(t, defaultSessionEventBufferSize, cfg.EventBufferSize)
}
