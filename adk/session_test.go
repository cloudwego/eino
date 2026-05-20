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
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// sessionHelperStore is a single-session in-memory SessionStore for unit tests.
type sessionHelperStore struct {
	mu          sync.Mutex
	checkpoints map[string][]byte

	events           [][]byte
	loadErr          error
	afterMessageID   string
	afterEventCursor string
	turnPayload      []byte
	turnExists       bool
	turnErr          error
	appendErr        error
	deleteErr        error
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

type streamingSessionAgent struct {
	release chan struct{}
}

func (a *streamingSessionAgent) Name(_ context.Context) string { return "streaming-session-agent" }
func (a *streamingSessionAgent) Description(_ context.Context) string {
	return "streaming session agent"
}
func (a *streamingSessionAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	sr, sw := schema.Pipe[*schema.Message](1)
	go func() {
		defer gen.Close()
		if closed := sw.Send(schema.AssistantMessage("partial", nil), nil); closed {
			return
		}
		gen.Send(&AgentEvent{
			AgentName: a.Name(context.Background()),
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{IsStreaming: true, MessageStream: sr, Role: schema.Assistant},
			},
		})
		<-a.release
		sw.Close()
		gen.Send(&AgentEvent{
			AgentName: a.Name(context.Background()),
			TurnEndState: &TurnEndState[*schema.Message]{
				Messages: []*schema.Message{schema.AssistantMessage("partial", nil)},
			},
		})
	}()
	return iter
}

func newSessionHelperStore() *sessionHelperStore {
	return &sessionHelperStore{checkpoints: make(map[string][]byte)}
}

func (s *sessionHelperStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[key] = append([]byte{}, value...)
	return nil
}

func (s *sessionHelperStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.checkpoints[key]
	return append([]byte{}, v...), ok, nil
}

func (s *sessionHelperStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.deleteErr != nil {
		return s.deleteErr
	}
	delete(s.checkpoints, key)
	return nil
}

func (s *sessionHelperStore) AppendEvents(_ context.Context, _ string, events [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	for _, e := range events {
		s.events = append(s.events, append([]byte{}, e...))
	}
	return nil
}

func (s *sessionHelperStore) LoadEvents(_ context.Context, _ string, opts *LoadEventsOptions) (*LoadEventsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	all := append([][]byte{}, s.events...)
	if opts != nil && opts.AfterCursor != "" {
		// AfterCursor encoded as decimal index for simplicity in test helper.
		var idx int
		_, err := fmtSscan(opts.AfterCursor, &idx)
		if err != nil {
			return nil, err
		}
		if idx < 0 {
			idx = 0
		}
		if idx > len(all) {
			idx = len(all)
		}
		return &LoadEventsResult{Events: all[idx:]}, nil
	}
	if opts != nil && opts.Reverse {
		out := make([][]byte, 0, len(all))
		for i := len(all) - 1; i >= 0; i-- {
			out = append(out, all[i])
		}
		return &LoadEventsResult{Events: out}, nil
	}
	return &LoadEventsResult{Events: all}, nil
}

// fmtSscan is a tiny helper to parse the decimal cursor used by the helper store.
func fmtSscan(s string, out *int) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, errInvalidCursor
		}
		n = n*10 + int(c-'0')
	}
	*out = n
	return 1, nil
}

var errInvalidCursor = errors.New("invalid cursor")

func (s *sessionHelperStore) LoadLatestTurnEnd(_ context.Context, _ string) (string, string, []byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnErr != nil {
		return "", "", nil, false, s.turnErr
	}
	return s.afterMessageID, s.afterEventCursor, append([]byte{}, s.turnPayload...), s.turnExists, nil
}

func (s *sessionHelperStore) SaveTurnEnd(_ context.Context, _ string, afterMessageID string, turnEnd []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.afterMessageID = afterMessageID
	s.afterEventCursor = itoa(len(s.events))
	s.turnPayload = append([]byte{}, turnEnd...)
	s.turnExists = true
	return nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
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
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{Payload: []byte("opaque")})
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

func TestRunnerSessionModeDeleteCheckpointFailureIsReported(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	persister := newSessionEventPersister[*schema.Message](
		ctx,
		store,
		"delete-fail-session",
		&SessionPersistenceConfig{EventFlushBatchSize: 1},
	)
	checkPointID := "delete-fail-checkpoint"
	store.deleteErr = errors.New("delete failed")

	res := &sessionTurnResult[*schema.Message]{
		persister:    persister,
		turnEndBytes: []byte("turn-end"),
		sessionState: &runnerSessionRunState[*schema.Message]{
			enabled:      true,
			sessionID:    "delete-fail-session",
			sessionStore: store,
		},
		store:        store,
		checkPointID: &checkPointID,
	}

	err := res.finalize(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete session checkpoint")
	assert.True(t, store.turnExists, "turn snapshot is committed before stale checkpoint cleanup")
}

func TestTurnEndStateSessionValues_JSONLikeRoundTrip(t *testing.T) {
	state := &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{schema.UserMessage("hello")},
		ToolInfos: []*schema.ToolInfo{
			{
				Name:        "lookup",
				Desc:        "lookup tool",
				ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"q": {Type: schema.String}}),
			},
		},
		SessionValues: map[string]any{
			"nested": map[string]any{"count": int64(9007199254740993)},
			"list":   []any{"a", int64(7), true},
		},
	}

	data, err := encodeTurnEndState(state)
	require.NoError(t, err)
	decoded, err := decodeTurnEndState[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded)
	require.Len(t, decoded.Messages, 1)
	assert.Equal(t, "hello", decoded.Messages[0].Content)
	require.Len(t, decoded.ToolInfos, 1)
	assert.Equal(t, "lookup", decoded.ToolInfos[0].Name)
	assert.Equal(t, state.SessionValues, decoded.SessionValues)
}

func TestRunnerSessionStreamingDoesNotBlockLiveEvent(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	agent := &streamingSessionAgent{release: make(chan struct{})}
	release := func() {
		select {
		case <-agent.release:
		default:
			close(agent.release)
		}
	}
	defer release()

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		EnableStreaming:    true,
		SessionID:          "streaming-session",
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "start")
	type nextResult struct {
		event *AgentEvent
		ok    bool
	}
	nextCh := make(chan nextResult, 1)
	go func() {
		event, ok := iter.Next()
		nextCh <- nextResult{event: event, ok: ok}
	}()

	var res nextResult
	select {
	case res = <-nextCh:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("managed session persistence blocked live streaming event delivery")
	}

	require.True(t, res.ok)
	require.NoError(t, res.event.Err)
	require.NotNil(t, res.event.Output)
	require.NotNil(t, res.event.Output.MessageOutput)
	require.True(t, res.event.Output.MessageOutput.IsStreaming)
	require.NotNil(t, res.event.Output.MessageOutput.MessageStream)

	msg, err := res.event.Output.MessageOutput.MessageStream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "partial", msg.Content)

	release()
	drainSessionEvents(t, iter)
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

// runnerInterruptAgent: produces an interrupt on first Run; emits "resumed ok" on Resume.
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
	require.True(t, sawInterrupt)

	resumeIter, err := runner.Resume(ctx, "")
	require.NoError(t, err)
	var gotResumedOK bool
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Output != nil && event.Output.MessageOutput != nil &&
			event.Output.MessageOutput.Message != nil &&
			event.Output.MessageOutput.Message.Content == "resumed ok" {
			gotResumedOK = true
		}
	}
	assert.True(t, gotResumedOK)
}

func TestRunnerSessionModeFlushFailurePreventsCommit(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	store.appendErr = errors.New("disk full")

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

	require.Error(t, lastErr)
	assert.Contains(t, lastErr.Error(), "failed to persist session events")
	assert.False(t, store.turnExists, "SaveTurnEnd must not be called when event flush fails")
}

// TestSessionPersister_EnqueueAfterClose verifies that calling enqueue after
// closeAndWait does not panic (send on closed channel).
func TestSessionPersister_EnqueueAfterClose(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "enqueue-after-close",
		&SessionPersistenceConfig{
			EventFlushBatchSize: 1,
			EventFlushInterval:  time.Millisecond,
			EventBufferSize:     8,
		},
	)

	require.NoError(t, persister.closeAndWait())
	// Must not panic.
	assert.NoError(t, persister.enqueue([]byte(`{"x":1}`)))
}

// TestSessionPersister_EmptyPayloadSkipped verifies enqueue silently discards
// records with empty payload.
func TestSessionPersister_EmptyPayloadSkipped(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "empty-payload",
		&SessionPersistenceConfig{
			EventFlushBatchSize: 1,
			EventFlushInterval:  time.Millisecond,
			EventBufferSize:     8,
		},
	)

	assert.NoError(t, persister.enqueue(nil))
	assert.NoError(t, persister.enqueue([]byte{}))

	se := makeInputSessionEvent(schema.UserMessage("real"))
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, persister.enqueue(data))

	require.NoError(t, persister.closeAndWait())
	require.Len(t, store.events, 1, "only the real event should be persisted")
}

// TestTurnEndState_GobRoundtripNilFields verifies gob roundtrip preserves nil semantics.
func TestTurnEndState_GobRoundtripNilFields(t *testing.T) {
	original := &TurnEndState[*schema.Message]{}
	encoded, err := encodeTurnEndState(original)
	require.NoError(t, err)
	decoded, err := decodeTurnEndState[*schema.Message](encoded)
	require.NoError(t, err)
	assert.Nil(t, decoded.Messages)
	assert.Nil(t, decoded.ToolInfos)
	assert.Nil(t, decoded.DeferredToolInfos)
	assert.Nil(t, decoded.SessionValues)
}

func TestNormalizeSessionPersistenceConfig_Variations(t *testing.T) {
	cfg := normalizeSessionPersistenceConfig(nil)
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)
	assert.Equal(t, defaultSessionEventBufferSize, cfg.EventBufferSize)

	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{})
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)

	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{EventFlushBatchSize: 32})
	assert.Equal(t, 32, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)

	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{
		EventFlushBatchSize: 8,
		EventFlushInterval:  200 * time.Millisecond,
		EventBufferSize:     128,
	})
	assert.Equal(t, 8, cfg.EventFlushBatchSize)
	assert.Equal(t, 200*time.Millisecond, cfg.EventFlushInterval)
	assert.Equal(t, 128, cfg.EventBufferSize)

	cfg = normalizeSessionPersistenceConfig(&SessionPersistenceConfig{
		EventFlushBatchSize: -1,
		EventFlushInterval:  -time.Second,
		EventBufferSize:     -5,
	})
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
}

// --- New tests covering the design doc ---

func TestSessionEvent_HumanReadableRoundTrip(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		msg := schema.UserMessage("hello")
		EnsureMessageID(msg)
		se := &SessionEvent[*schema.Message]{Message: msg}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.Message)
		assert.Equal(t, "hello", decoded.Message.Content)
		assert.Equal(t, GetMessageID(msg), GetMessageID(decoded.Message))
	})

	t.Run("MessagesReplaced", func(t *testing.T) {
		msgs := []*schema.Message{schema.UserMessage("a"), schema.AssistantMessage("b", nil)}
		for _, m := range msgs {
			EnsureMessageID(m)
		}
		se := &SessionEvent[*schema.Message]{MessagesReplaced: &msgs}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.MessagesReplaced)
		assert.Equal(t, 2, len(*decoded.MessagesReplaced))
		assert.Equal(t, "a", (*decoded.MessagesReplaced)[0].Content)
	})

	t.Run("MessageUpdated", func(t *testing.T) {
		updated := schema.AssistantMessage("placeholder", nil)
		EnsureMessageID(updated)
		se := &SessionEvent[*schema.Message]{
			MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
				MessageID: GetMessageID(updated),
				Message:   updated,
			},
		}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.MessageUpdated)
		assert.Equal(t, GetMessageID(updated), decoded.MessageUpdated.MessageID)
		assert.Equal(t, "placeholder", decoded.MessageUpdated.Message.Content)
	})

	t.Run("MessageInserted", func(t *testing.T) {
		inserted := schema.UserMessage("agentsmd content")
		EnsureMessageID(inserted)
		se := &SessionEvent[*schema.Message]{
			MessageInserted: &MessageInsertedEvent[*schema.Message]{
				Message:         inserted,
				BeforeMessageID: "anchor-id",
			},
		}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.MessageInserted)
		assert.Equal(t, "anchor-id", decoded.MessageInserted.BeforeMessageID)
		assert.Equal(t, "agentsmd content", decoded.MessageInserted.Message.Content)
	})
}

// TestApplySessionEvent verifies all variants of the event-applier.
func TestApplySessionEvent(t *testing.T) {
	makeMsg := func(content string) *schema.Message {
		m := schema.UserMessage(content)
		EnsureMessageID(m)
		return m
	}

	t.Run("Message appends", func(t *testing.T) {
		var msgs []*schema.Message
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{Message: makeMsg("a")})
		require.NoError(t, err)
		require.Len(t, msgs, 1)
	})

	t.Run("MessagesReplaced replaces wholesale", func(t *testing.T) {
		msgs := []*schema.Message{makeMsg("old")}
		repl := []*schema.Message{makeMsg("new1"), makeMsg("new2")}
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{MessagesReplaced: &repl})
		require.NoError(t, err)
		require.Len(t, msgs, 2)
		assert.Equal(t, "new1", msgs[0].Content)
	})

	t.Run("MessageUpdated replaces in place", func(t *testing.T) {
		target := makeMsg("orig")
		msgs := []*schema.Message{makeMsg("a"), target, makeMsg("b")}
		newMsg := schema.AssistantMessage("placeholder", nil)
		newMsg.Extra = map[string]any{}
		// Force same ID
		setMessageIDForTest(newMsg, GetMessageID(target))
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
				MessageID: GetMessageID(target),
				Message:   newMsg,
			},
		})
		require.NoError(t, err)
		assert.Equal(t, "placeholder", msgs[1].Content)
	})

	t.Run("MessageUpdated identity mismatch", func(t *testing.T) {
		target := makeMsg("orig")
		msgs := []*schema.Message{target}
		other := makeMsg("other")
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
				MessageID: GetMessageID(target),
				Message:   other, // has its own different ID
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "identity mismatch")
	})

	t.Run("MessageInserted before anchor", func(t *testing.T) {
		anchor := makeMsg("anchor")
		msgs := []*schema.Message{makeMsg("a"), anchor, makeMsg("b")}
		ins := makeMsg("inserted")
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessageInserted: &MessageInsertedEvent[*schema.Message]{
				Message:         ins,
				BeforeMessageID: GetMessageID(anchor),
			},
		})
		require.NoError(t, err)
		require.Len(t, msgs, 4)
		assert.Equal(t, "inserted", msgs[1].Content)
		assert.Equal(t, "anchor", msgs[2].Content)
	})

	t.Run("MessageInserted append at end", func(t *testing.T) {
		msgs := []*schema.Message{makeMsg("a")}
		ins := makeMsg("appended")
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessageInserted: &MessageInsertedEvent[*schema.Message]{Message: ins, BeforeMessageID: ""},
		})
		require.NoError(t, err)
		require.Len(t, msgs, 2)
		assert.Equal(t, "appended", msgs[1].Content)
	})

	t.Run("MessageInserted missing anchor errors", func(t *testing.T) {
		msgs := []*schema.Message{makeMsg("a")}
		ins := makeMsg("ghost")
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessageInserted: &MessageInsertedEvent[*schema.Message]{
				Message:         ins,
				BeforeMessageID: "no-such-anchor",
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "anchor message")
	})

	t.Run("MessageUpdated missing target errors", func(t *testing.T) {
		msgs := []*schema.Message{makeMsg("a")}
		other := makeMsg("other")
		setMessageIDForTest(other, "ghost-id")
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
				MessageID: "ghost-id",
				Message:   other,
			},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found for update")
	})
}

func setMessageIDForTest(msg *schema.Message, id string) {
	if msg.Extra == nil {
		msg.Extra = map[string]any{}
	}
	msg.Extra["_eino_msg_id"] = id
}

// TestStripSessionEventFields verifies all session-internal fields are stripped.
func TestStripSessionEventFields(t *testing.T) {
	t.Run("non-session-internal event passes through", func(t *testing.T) {
		ev := &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{Message: schema.AssistantMessage("hi", nil), Role: schema.Assistant},
			},
		}
		stripped := stripSessionEventFields(ev)
		require.NotNil(t, stripped)
		assert.Equal(t, "hi", stripped.Output.MessageOutput.Message.Content)
	})

	t.Run("TurnEndState-only event drops to nil", func(t *testing.T) {
		ev := &AgentEvent{
			TurnEndState: &TurnEndState[*schema.Message]{},
		}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})

	t.Run("MessagesReplaced-only event drops to nil", func(t *testing.T) {
		msgs := []*schema.Message{schema.UserMessage("x")}
		ev := &AgentEvent{
			MessagesReplaced: &msgs,
		}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})

	t.Run("Err with TurnEndState keeps Err", func(t *testing.T) {
		ev := &AgentEvent{
			Err:          errors.New("visible"),
			TurnEndState: &TurnEndState[*schema.Message]{},
			SessionID:    "child-1",
		}
		stripped := stripSessionEventFields(ev)
		require.NotNil(t, stripped)
		assert.Nil(t, stripped.TurnEndState)
		assert.Empty(t, stripped.SessionID)
		assert.EqualError(t, stripped.Err, "visible")
	})

	t.Run("SessionID alone is stripped", func(t *testing.T) {
		ev := &AgentEvent{SessionID: "child-1"}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})
}

// TestReconstructFromEventLog_EmptySession verifies empty-session reconstruction.
func TestReconstructFromEventLog_EmptySession(t *testing.T) {
	store := newSessionHelperStore()
	ctx := context.Background()
	msgs, err := reconstructFromEventLog[*schema.Message](ctx, store, "empty")
	require.NoError(t, err)
	assert.Nil(t, msgs)
}

// TestReconstructFromEventLog_MultiTurn verifies multi-turn reconstruction.
func TestReconstructFromEventLog_MultiTurn(t *testing.T) {
	store := newSessionHelperStore()
	ctx := context.Background()
	sid := "multi-turn"

	// Turn 1: input "Q1" + output "A1"
	q1 := schema.UserMessage("Q1")
	EnsureMessageID(q1)
	a1 := schema.AssistantMessage("A1", nil)
	EnsureMessageID(a1)
	for _, m := range []*schema.Message{q1, a1} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}
	// Turn 2: input "Q2" + output "A2"
	q2 := schema.UserMessage("Q2")
	EnsureMessageID(q2)
	a2 := schema.AssistantMessage("A2", nil)
	EnsureMessageID(a2)
	for _, m := range []*schema.Message{q2, a2} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}

	msgs, err := reconstructFromEventLog[*schema.Message](ctx, store, sid)
	require.NoError(t, err)
	require.Len(t, msgs, 4)
	assert.Equal(t, "Q1", msgs[0].Content)
	assert.Equal(t, "A1", msgs[1].Content)
	assert.Equal(t, "Q2", msgs[2].Content)
	assert.Equal(t, "A2", msgs[3].Content)
}

// TestReconstructFromEventLog_WithSummarizationBoundary: events before
// MessagesReplaced are ignored; reconstruction starts from boundary.
func TestReconstructFromEventLog_WithSummarizationBoundary(t *testing.T) {
	store := newSessionHelperStore()
	ctx := context.Background()
	sid := "with-boundary"

	// Pre-boundary events (should be ignored).
	for i := 0; i < 3; i++ {
		m := schema.UserMessage("pre")
		EnsureMessageID(m)
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}

	// Boundary: summary of all messages.
	summary := schema.UserMessage("summary")
	EnsureMessageID(summary)
	repl := []*schema.Message{summary}
	se := &SessionEvent[*schema.Message]{MessagesReplaced: &repl}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))

	// Post-boundary events.
	post := schema.AssistantMessage("post", nil)
	EnsureMessageID(post)
	se = &SessionEvent[*schema.Message]{Message: post}
	data, err = encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))

	msgs, err := reconstructFromEventLog[*schema.Message](ctx, store, sid)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	assert.Equal(t, "summary", msgs[0].Content)
	assert.Equal(t, "post", msgs[1].Content)
}

// TestRunnerSessionReconstructsFromEventLog: Delete TurnEndState from store,
// next turn should reconstruct from events.
func TestRunnerSessionReconstructsFromEventLog(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "reconstruct-session"

	firstAgent := &runnerSessionAgent{
		name: "ra",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              firstAgent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))

	// Verify events were captured: caller input + assistant output for the first turn.
	require.Len(t, store.events, 2, "input event + assistant event should be in event log")

	// Wipe the snapshot to force fallback reconstruction.
	store.turnExists = false
	store.turnPayload = nil
	store.afterMessageID = ""
	store.afterEventCursor = ""

	// Capture the prepared session state before agent runs.
	capturedAgent := &runnerSessionAgent{
		name: "ra",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{},
		},
	}
	runner = NewRunner(ctx, RunnerConfig{
		Agent:              capturedAgent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "second"))

	// The agent should have received the reconstructed history before "second".
	require.Len(t, capturedAgent.inputs, 1)
	// Input order: reconstructed user "first" + reconstructed assistant "ok" + new user "second".
	require.Len(t, capturedAgent.inputs[0], 3)
	// The last message must be the new "second" input.
	assert.Equal(t, "second", capturedAgent.inputs[0][len(capturedAgent.inputs[0])-1].Content)
	// And the first reconstructed message must be the original "first" input.
	assert.Equal(t, "first", capturedAgent.inputs[0][0].Content)
}

// TestRunnerSessionInputEventsPersisted verifies that caller input messages
// are persisted to the event log at turn start.
func TestRunnerSessionInputEventsPersisted(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "input-events"

	agent := &runnerSessionAgent{
		name: "input-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("answer", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "user-question"))

	// Single-turn run: 1 user input event + 1 assistant output event.
	require.Len(t, store.events, 2)
	// The first event should be the user input.
	first, err := decodeSessionEvent[*schema.Message](store.events[0])
	require.NoError(t, err)
	require.NotNil(t, first.Message)
	assert.Equal(t, "user-question", first.Message.Content)
	assert.Equal(t, schema.User, first.Message.Role)
	// And it should have a message ID.
	assert.NotEmpty(t, GetMessageID(first.Message))
}

// recordingHelperStore wraps sessionHelperStore to record the order of
// AppendEvents and Set calls so tests can assert durability ordering.
type recordingHelperStore struct {
	*sessionHelperStore
	mu        sync.Mutex
	calls     []string // "append" or "set:<key>"
	delaySet  time.Duration
}

func newRecordingHelperStore() *recordingHelperStore {
	return &recordingHelperStore{sessionHelperStore: newSessionHelperStore()}
}

func (s *recordingHelperStore) AppendEvents(ctx context.Context, sid string, events [][]byte) error {
	s.mu.Lock()
	if s.sessionHelperStore.appendErr != nil {
		err := s.sessionHelperStore.appendErr
		s.mu.Unlock()
		return err
	}
	s.calls = append(s.calls, "append")
	s.mu.Unlock()
	return s.sessionHelperStore.AppendEvents(ctx, sid, events)
}

func (s *recordingHelperStore) Set(ctx context.Context, key string, value []byte) error {
	if s.delaySet > 0 {
		time.Sleep(s.delaySet)
	}
	s.mu.Lock()
	s.calls = append(s.calls, "set:"+key)
	s.mu.Unlock()
	return s.sessionHelperStore.Set(ctx, key, value)
}

func (s *recordingHelperStore) callsSnapshot() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

// TestRunnerSessionInterruptCheckpointSkippedOnPersistFailure proves the
// fail-closed invariant: if AppendEvents fails during a turn that ends in an
// interrupt, the checkpoint MUST NOT be written — otherwise resume would load
// a checkpoint referencing events that were never persisted.
func TestRunnerSessionInterruptCheckpointSkippedOnPersistFailure(t *testing.T) {
	ctx := context.Background()
	store := newRecordingHelperStore()
	store.sessionHelperStore.appendErr = errors.New("simulated append failure")

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerInterruptAgent{},
		CheckPointStore: store,
		SessionID:       "interrupt-persist-fail",
		SessionStore:    store,
	})
	iter := runner.Query(ctx, "go")
	var sawErr bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			sawErr = true
		}
	}
	require.True(t, sawErr, "expected runner to surface the persistence error")

	cpKey := sessionRunnerCheckpointID("interrupt-persist-fail")
	calls := store.callsSnapshot()
	for _, c := range calls {
		if c == "set:"+cpKey {
			t.Fatalf("checkpoint was written despite event persistence failure: calls=%v", calls)
		}
	}
}

// TestRunnerSessionCheckpointAfterPersisterFlush proves that on the interrupt
// path, the checkpoint is written ONLY after the persister has flushed events
// (AppendEvents before Set on checkpoint key).
func TestRunnerSessionCheckpointAfterPersisterFlush(t *testing.T) {
	ctx := context.Background()
	store := newRecordingHelperStore()

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerInterruptAgent{},
		CheckPointStore: store,
		SessionID:       "interrupt-order",
		SessionStore:    store,
	})
	iter := runner.Query(ctx, "hi")
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}
	calls := store.callsSnapshot()

	cpKey := sessionRunnerCheckpointID("interrupt-order")
	var lastAppend, firstSet int = -1, -1
	for i, c := range calls {
		if c == "append" {
			lastAppend = i
		}
		if c == "set:"+cpKey && firstSet == -1 {
			firstSet = i
		}
	}
	require.NotEqual(t, -1, lastAppend, "expected at least one AppendEvents call")
	require.NotEqual(t, -1, firstSet, "expected the runner-session checkpoint to be written")
	require.Greater(t, firstSet, lastAppend,
		"checkpoint Set must follow the final AppendEvents flush; got calls=%v", calls)
}

// TestSessionPersister_EnqueueAfterAppendError verifies that once AppendEvents
// has failed, subsequent enqueue calls return that error rather than silently
// succeeding.
func TestSessionPersister_EnqueueAfterAppendError(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	store.appendErr = errors.New("append failed")

	cfg := &SessionPersistenceConfig{
		EventFlushBatchSize: 1,
		EventFlushInterval:  10 * time.Millisecond,
		EventBufferSize:     8,
	}
	p := newSessionEventPersister[*schema.Message](ctx, store, "sid", cfg)
	defer p.closeAndWait()

	require.NoError(t, p.enqueue([]byte(`{"i":1}`)))
	// Wait for the run loop to attempt AppendEvents and record the error.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if p.getErr() != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Error(t, p.getErr(), "persister must record the AppendEvents failure")

	err := p.enqueue([]byte(`{"i":2}`))
	require.Error(t, err, "enqueue after persist failure must return an error")
}
