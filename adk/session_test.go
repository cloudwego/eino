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
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// sessionHelperStore is a single-session in-memory typed session service for unit tests.
// Mirrors the EventID-based cursor semantics of session.InMemoryStore so the
// in-package tests exercise the same protocol contract.
type sessionHelperStore struct {
	mu          sync.Mutex
	checkpoints map[string][]byte

	events     []storedSessionEvent
	eventIDs   []string
	eventIDIdx map[string]int
	loadErr    error
	appendErr  error
	deleteErr  error
}

type storedSessionEvent struct {
	EventID string
	Kind    SessionEventKind
	Data    []byte
}

type blockingAppendStore struct {
	sessionHelperStore
	appendStarted chan struct{}
	releaseAppend chan struct{}
	startOnce     sync.Once
}

func newBlockingAppendStore() *blockingAppendStore {
	return &blockingAppendStore{
		sessionHelperStore: *newSessionHelperStore(),
		appendStarted:      make(chan struct{}),
		releaseAppend:      make(chan struct{}),
	}
}

func (s *blockingAppendStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	s.startOnce.Do(func() {
		close(s.appendStarted)
	})
	select {
	case <-s.releaseAppend:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.sessionHelperStore.AppendEvents(ctx, sessionID, events)
}

// withTestEventID assigns a fresh UUIDv4 to the SessionEvent if its EventID is
// empty. Tests that construct SessionEvent literals directly bypass the Runner
// allocation paths, so they must still satisfy the AppendEvents wire contract.
func withTestEventID[M MessageType](se *SessionEvent[M]) *SessionEvent[M] {
	if se != nil && se.EventID == "" {
		se.EventID = uuid.NewString()
	}
	return se
}

// validTestPayload returns a storedSessionEvent that satisfies the AppendEvents
// wire contract (non-empty EventID) for persister-level tests that don't
// care about the SessionEvent body.
func validTestPayload() *SessionEvent[*schema.Message] {
	return &SessionEvent[*schema.Message]{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: schema.UserMessage("test")}
}

func decodeStoredSessionEvents(t *testing.T, raw []storedSessionEvent) []*SessionEvent[*schema.Message] {
	t.Helper()
	out := make([]*SessionEvent[*schema.Message], 0, len(raw))
	for _, ep := range raw {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		out = append(out, se)
	}
	return out
}

func filterStoredSessionEvents(t *testing.T, raw []storedSessionEvent, pred func(*SessionEvent[*schema.Message]) bool) []*SessionEvent[*schema.Message] {
	t.Helper()
	var out []*SessionEvent[*schema.Message]
	for _, se := range decodeStoredSessionEvents(t, raw) {
		if pred(se) {
			out = append(out, se)
		}
	}
	return out
}

func appendTestSessionEvent(t *testing.T, ctx context.Context, store SessionService[*schema.Message], sid string, se *SessionEvent[*schema.Message]) *SessionEvent[*schema.Message] {
	t.Helper()
	se = withTestEventID(se)
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	return se
}

func testMessageWithID(content string, role schema.RoleType) *schema.Message {
	var msg *schema.Message
	switch role {
	case schema.Assistant:
		msg = schema.AssistantMessage(content, nil)
	default:
		msg = schema.UserMessage(content)
	}
	EnsureMessageID(msg)
	return msg
}

func appendCommittedTestTurn(t *testing.T, ctx context.Context, store SessionService[*schema.Message], sid string, turnID string, contents ...string) *SessionEvent[*schema.Message] {
	t.Helper()
	for i, content := range contents {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
			Kind:    SessionEventMessage,
			TurnID:  turnID,
			Message: testMessageWithID(content, role),
		})
	}
	return appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind:    SessionEventTurnEnd,
		TurnID:  turnID,
		TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"turn": turnID}},
	})
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
		gen.Send(&AgentEvent{
			AgentName: a.name,
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: turnEnd,
			},
		})
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
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind: SessionEventTurnEnd,
				TurnEnd: &TurnEndState[*schema.Message]{
					Messages: []*schema.Message{schema.AssistantMessage("partial", nil)},
				},
			},
		})
	}()
	return iter
}

func newSessionHelperStore() *sessionHelperStore {
	return &sessionHelperStore{
		checkpoints: make(map[string][]byte),
		eventIDIdx:  make(map[string]int),
	}
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

func (s *sessionHelperStore) AppendEvents(_ context.Context, _ string, events []*SessionEvent[*schema.Message]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	for _, e := range events {
		if e == nil || e.EventID == "" {
			return ErrInvalidEventID
		}
		if err := NormalizeSessionEventKind(e); err != nil {
			return err
		}
		if _, dup := s.eventIDIdx[e.EventID]; dup {
			continue
		}
		data, err := encodeSessionEvent(e)
		if err != nil {
			return err
		}
		s.events = append(s.events, storedSessionEvent{
			EventID: e.EventID,
			Kind:    e.Kind,
			Data:    append([]byte{}, data...),
		})
		s.eventIDs = append(s.eventIDs, e.EventID)
		s.eventIDIdx[e.EventID] = len(s.events) - 1
	}
	return nil
}

func (s *sessionHelperStore) LoadEvents(_ context.Context, _ string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return nil, s.loadErr
	}
	if opts == nil {
		opts = &LoadSessionEventsRequest{}
	}
	all := s.events

	if opts.Reverse {
		end := len(all)
		if opts.After != "" {
			pos, ok := s.eventIDIdx[opts.After]
			if !ok {
				return nil, ErrEventIDOutOfRange
			}
			end = pos
		}
		if end <= 0 {
			return &LoadSessionEventsResult[*schema.Message]{}, nil
		}
		kindSet := buildTestKindSet(opts.Kinds)
		var out []*SessionEvent[*schema.Message]
		hasMore := false
		for i := end - 1; i >= 0; i-- {
			if kindSet != nil {
				if _, ok := kindSet[all[i].Kind]; !ok {
					continue
				}
			}
			if opts.Limit > 0 && len(out) >= opts.Limit {
				hasMore = true
				break
			}
			event, err := decodeSessionEvent[*schema.Message](all[i].Data)
			if err != nil {
				return nil, err
			}
			out = append(out, event)
		}
		var next string
		if hasMore && len(out) > 0 {
			next = out[len(out)-1].EventID
		}
		return &LoadSessionEventsResult[*schema.Message]{Events: out, Next: next}, nil
	}

	start := 0
	if opts.After != "" {
		pos, ok := s.eventIDIdx[opts.After]
		if !ok {
			return nil, ErrEventIDOutOfRange
		}
		start = pos + 1
	}
	if start > len(all) {
		start = len(all)
	}
	kindSet := buildTestKindSet(opts.Kinds)
	var out []*SessionEvent[*schema.Message]
	hasMore := false
	for i := start; i < len(all); i++ {
		if kindSet != nil {
			if _, ok := kindSet[all[i].Kind]; !ok {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		event, err := decodeSessionEvent[*schema.Message](all[i].Data)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &LoadSessionEventsResult[*schema.Message]{Events: out, Next: next}, nil
}

func buildTestKindSet(kinds []SessionEventKind) map[SessionEventKind]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	set := make(map[SessionEventKind]struct{}, len(kinds))
	for _, kind := range kinds {
		set[kind] = struct{}{}
	}
	return set
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
		Agent:          firstAgent,
		SessionID:      sessionID,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
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
		Agent:          secondAgent,
		SessionID:      sessionID,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "second", WithSessionValues(map[string]any{"override": "value"})))

	require.Len(t, secondAgent.inputs, 1)
	require.Len(t, secondAgent.inputs[0], 3)
	assert.Equal(t, "first", secondAgent.inputs[0][0].Content)
	assert.Equal(t, "ok", secondAgent.inputs[0][1].Content)
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

	agent := &runnerSessionAgent{name: "runner-session-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		SessionID:       sessionID,
		SessionService:  store,
		CheckPointStore: store,
	})
	iter := runner.Query(ctx, "new input")
	// Run should succeed — pending checkpoint is auto-abandoned.
	var sawErr bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			sawErr = true
		}
	}
	require.False(t, sawErr, "Run should not return any error when pending checkpoint exists")

	// Verify agent received the input messages (no prior history to reconstruct).
	require.Len(t, agent.inputs, 1)
	require.Len(t, agent.inputs[0], 1)
	assert.Equal(t, "new input", agent.inputs[0][0].Content)
}

func TestRunnerSessionModeDeleteCheckpointFailureIsReported(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	persister := newSessionEventPersister[*schema.Message](
		ctx,
		store,
		"delete-fail-session",
		normalizeSessionConfig(&SessionConfig{EventFlushBatchSize: 1}),
	)
	checkPointID := "delete-fail-checkpoint"
	store.deleteErr = errors.New("delete failed")

	res := &sessionTurnResult[*schema.Message]{
		persister:  persister,
		sawTurnEnd: true,
		sessionState: &runnerSessionRunState[*schema.Message]{
			enabled:        true,
			sessionID:      "delete-fail-session",
			sessionService: store,
		},
		store:        store,
		checkPointID: &checkPointID,
	}

	err := res.finalize(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to delete session checkpoint")
	assert.True(t, res.sawTurnEnd, "turn must have been seen before stale checkpoint cleanup")
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

	se := &SessionEvent[*schema.Message]{TurnEnd: state}
	data, err := encodeSessionEvent(withTestEventID(se))
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.TurnEnd)
	require.Len(t, decoded.TurnEnd.Messages, 1)
	assert.Equal(t, "hello", decoded.TurnEnd.Messages[0].Content)
	require.Len(t, decoded.TurnEnd.ToolInfos, 1)
	assert.Equal(t, "lookup", decoded.TurnEnd.ToolInfos[0].Name)
	assert.Equal(t, state.SessionValues, decoded.TurnEnd.SessionValues)
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
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       "streaming-session",
		SessionService:  store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
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
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind: SessionEventTurnEnd,
				TurnEnd: &TurnEndState[*schema.Message]{
					Messages: []*schema.Message{schema.AssistantMessage("resumed ok", nil)},
				},
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
		SessionService:  store,
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
		Agent:          agent,
		SessionID:      "flush-fail-session",
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
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
}

func TestRunnerSessionSyncModeBlocksDeliveryUntilAppendCompletes(t *testing.T) {
	ctx := context.Background()
	store := newBlockingAppendStore()
	agent := &runnerSessionAgent{
		name: "sync-block-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      "sync-block-session",
		SessionService: store,
		SessionConfig:  &SessionConfig{PersistenceMode: SessionPersistenceModeSync},
	})

	iter := runner.Query(ctx, "trigger")
	events := make(chan *AgentEvent, 1)
	go func() {
		ev, ok := iter.Next()
		if !ok {
			events <- nil
			return
		}
		events <- ev
	}()

	select {
	case <-store.appendStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sync persistence did not start appending")
	}
	select {
	case ev := <-events:
		t.Fatalf("observed event before sync append completed: %#v", ev)
	case <-time.After(50 * time.Millisecond):
	}

	close(store.releaseAppend)
	firstEvent := <-events
	var sawOutput bool
	if firstEvent != nil {
		require.NoError(t, firstEvent.Err)
		if firstEvent.Output != nil && firstEvent.Output.MessageOutput != nil &&
			firstEvent.Output.MessageOutput.Message != nil &&
			firstEvent.Output.MessageOutput.Message.Content == "ok" {
			sawOutput = true
		}
	}
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.Message != nil &&
			ev.Output.MessageOutput.Message.Content == "ok" {
			sawOutput = true
			break
		}
	}
	assert.True(t, sawOutput, "expected output after sync append completed")
}

func TestRunnerSessionSyncModeAppendFailureSuppressesOutput(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	store.appendErr = errors.New("sync append failed")
	agent := &runnerSessionAgent{
		name: "sync-fail-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      "sync-fail-session",
		SessionService: store,
		SessionConfig:  &SessionConfig{PersistenceMode: SessionPersistenceModeSync, MaxFlushRetries: -1},
	})

	iter := runner.Query(ctx, "trigger")
	var lastErr error
	var sawOutput bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			lastErr = ev.Err
		}
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			sawOutput = true
		}
	}

	require.Error(t, lastErr)
	assert.Contains(t, lastErr.Error(), "failed to persist session events")
	assert.False(t, sawOutput, "sync mode must not deliver output after append failure")
}

// TestSessionPersister_EnqueueAfterClose verifies that calling enqueue after
// closeAndWait does not panic (send on closed channel).
func TestSessionPersister_EnqueueAfterClose(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "enqueue-after-close",
		normalizeSessionConfig(&SessionConfig{
			EventFlushBatchSize: 1,
			EventFlushInterval:  time.Millisecond,
			EventBufferSize:     8,
		}),
	)

	require.NoError(t, persister.closeAndWait())
	// Must not panic.
	assert.NoError(t, persister.enqueue(validTestPayload()))
}

// TestSessionPersister_EmptyPayloadSkipped verifies enqueue silently discards
// records with empty payload.
func TestSessionPersister_EmptyPayloadSkipped(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](
		ctx, store, "empty-payload",
		normalizeSessionConfig(&SessionConfig{
			EventFlushBatchSize: 1,
			EventFlushInterval:  time.Millisecond,
			EventBufferSize:     8,
		}),
	)

	assert.NoError(t, persister.enqueue(nil))
	assert.NoError(t, persister.enqueue(&SessionEvent[*schema.Message]{}))

	se := makeInputSessionEvent(schema.UserMessage("real"))
	require.NoError(t, persister.enqueue(se))

	require.NoError(t, persister.closeAndWait())
	require.Len(t, store.events, 1, "only the real event should be persisted")
}

func TestSessionPersister_SyncModeAppendDuringEnqueue(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	cfg := normalizeSessionConfig(&SessionConfig{PersistenceMode: SessionPersistenceModeSync})
	persister := newSessionEventPersister[*schema.Message](ctx, store, "sync-enqueue", cfg)

	require.NoError(t, persister.enqueue(validTestPayload()))
	store.mu.Lock()
	assert.Len(t, store.events, 1, "sync mode must append during enqueue")
	store.mu.Unlock()

	require.NoError(t, persister.closeAndWait())
	store.mu.Lock()
	assert.Len(t, store.events, 1, "sync closeAndWait must not flush again")
	store.mu.Unlock()
}

func TestSessionPersister_SyncModeRetryAndLatch(t *testing.T) {
	t.Run("transient recovery", func(t *testing.T) {
		ctx := context.Background()
		store := &transientFailStore{
			sessionHelperStore: *newSessionHelperStore(),
			failsLeft:          2,
			appendErrVal:       errors.New("transient"),
		}
		cfg := normalizeSessionConfig(&SessionConfig{
			PersistenceMode:          SessionPersistenceModeSync,
			MaxFlushRetries:          3,
			FlushRetryInitialBackoff: time.Millisecond,
		})
		persister := newSessionEventPersister[*schema.Message](ctx, store, "sync-retry", cfg)

		require.NoError(t, persister.enqueue(validTestPayload()))
		assert.Equal(t, 3, store.getAppendCalls())
		assert.NoError(t, persister.closeAndWait())
	})

	t.Run("permanent failure latched", func(t *testing.T) {
		ctx := context.Background()
		store := &transientFailStore{
			sessionHelperStore: *newSessionHelperStore(),
			failsLeft:          100,
			appendErrVal:       errors.New("permanent"),
		}
		cfg := normalizeSessionConfig(&SessionConfig{
			PersistenceMode:          SessionPersistenceModeSync,
			MaxFlushRetries:          1,
			FlushRetryInitialBackoff: time.Millisecond,
		})
		persister := newSessionEventPersister[*schema.Message](ctx, store, "sync-latch", cfg)

		err := persister.enqueue(validTestPayload())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permanent")
		assert.Equal(t, 2, store.getAppendCalls())

		err = persister.enqueue(validTestPayload())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permanent")
		assert.Equal(t, 2, store.getAppendCalls(), "latched failure must prevent later appends")
		assert.Error(t, persister.closeAndWait())
	})
}

// TestTurnEndState_GobRoundtripNilFields verifies gob roundtrip preserves nil semantics.
func TestTurnEndState_GobRoundtripNilFields(t *testing.T) {
	original := &TurnEndState[*schema.Message]{}
	se := &SessionEvent[*schema.Message]{TurnEnd: original}
	encoded, err := encodeSessionEvent(withTestEventID(se))
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.TurnEnd)
	assert.Nil(t, decoded.TurnEnd.Messages)
	assert.Nil(t, decoded.TurnEnd.ToolInfos)
	assert.Nil(t, decoded.TurnEnd.DeferredToolInfos)
	assert.Nil(t, decoded.TurnEnd.SessionValues)
}

func TestNormalizeSessionConfig_Variations(t *testing.T) {
	cfg := normalizeSessionConfig(nil)
	assert.Equal(t, SessionPersistenceModeAsync, cfg.PersistenceMode)
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)
	assert.Equal(t, defaultSessionEventBufferSize, cfg.EventBufferSize)

	cfg = normalizeSessionConfig(&SessionConfig{})
	assert.Equal(t, SessionPersistenceModeAsync, cfg.PersistenceMode)
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)

	cfg = normalizeSessionConfig(&SessionConfig{PersistenceMode: SessionPersistenceModeSync})
	assert.Equal(t, SessionPersistenceModeSync, cfg.PersistenceMode)

	cfg = normalizeSessionConfig(&SessionConfig{PersistenceMode: SessionPersistenceMode("unknown")})
	assert.Equal(t, SessionPersistenceModeAsync, cfg.PersistenceMode)

	cfg = normalizeSessionConfig(&SessionConfig{EventFlushBatchSize: 32})
	assert.Equal(t, 32, cfg.EventFlushBatchSize)
	assert.Equal(t, defaultSessionEventFlushInterval, cfg.EventFlushInterval)

	cfg = normalizeSessionConfig(&SessionConfig{
		EventFlushBatchSize: 8,
		EventFlushInterval:  200 * time.Millisecond,
		EventBufferSize:     128,
	})
	assert.Equal(t, 8, cfg.EventFlushBatchSize)
	assert.Equal(t, 200*time.Millisecond, cfg.EventFlushInterval)
	assert.Equal(t, 128, cfg.EventBufferSize)

	cfg = normalizeSessionConfig(&SessionConfig{
		EventFlushBatchSize: -1,
		EventFlushInterval:  -time.Second,
		EventBufferSize:     -5,
	})
	assert.Equal(t, defaultSessionEventFlushBatchSize, cfg.EventFlushBatchSize)
}

type countingSerializer struct {
	inner          schema.Serializer
	marshalCalls   int32
	unmarshalCalls int32
}

func newCountingSerializer() *countingSerializer {
	return &countingSerializer{inner: &schema.HumanReadableSerializer{}}
}

func (s *countingSerializer) Marshal(v any) ([]byte, error) {
	atomic.AddInt32(&s.marshalCalls, 1)
	return s.inner.Marshal(v)
}

func (s *countingSerializer) Unmarshal(data []byte, v any) error {
	atomic.AddInt32(&s.unmarshalCalls, 1)
	return s.inner.Unmarshal(data, v)
}

func TestSessionEvent_HumanReadableSerializerDirectRoundTrip(t *testing.T) {
	serializer := &schema.HumanReadableSerializer{}
	se := &SessionEvent[*schema.Message]{
		EventID: "serializer-direct",
		Kind:    SessionEventSessionStatusIdle,
		Lifecycle: &LifecycleEvent{
			State: SessionRunStateIdle,
		},
	}

	data, err := serializer.Marshal(se)
	require.NoError(t, err)

	var decoded SessionEvent[*schema.Message]
	require.NoError(t, serializer.Unmarshal(data, &decoded))
	require.NoError(t, NormalizeSessionEventKind(&decoded))
	assert.Equal(t, se.EventID, decoded.EventID)
	assert.Equal(t, se.Kind, decoded.Kind)
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

	t.Run("MessagesDeleted", func(t *testing.T) {
		se := &SessionEvent[*schema.Message]{
			MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{"m1", "m2"}},
		}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.MessagesDeleted)
		assert.Equal(t, SessionEventMessagesDeleted, decoded.Kind)
		assert.Equal(t, []string{"m1", "m2"}, decoded.MessagesDeleted.MessageIDs)
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

	t.Run("MessagesDeleted removes multiple messages", func(t *testing.T) {
		a := makeMsg("a")
		b := makeMsg("b")
		c := makeMsg("c")
		d := makeMsg("d")
		msgs := []*schema.Message{a, b, c, d}
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{GetMessageID(b), GetMessageID(d)}},
		})
		require.NoError(t, err)
		require.Len(t, msgs, 2)
		assert.Equal(t, "a", msgs[0].Content)
		assert.Equal(t, "c", msgs[1].Content)
	})

	t.Run("MessagesDeleted missing target errors", func(t *testing.T) {
		a := makeMsg("a")
		b := makeMsg("b")
		c := makeMsg("c")
		msgs := []*schema.Message{a, b, c}
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{GetMessageID(b), "ghost-id"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ghost-id")
		assert.Equal(t, []*schema.Message{a, b, c}, msgs)
	})

	t.Run("MessagesDeleted rejects empty and duplicate ids", func(t *testing.T) {
		msgs := []*schema.Message{makeMsg("a")}
		err := applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessagesDeleted: &MessagesDeletedEvent{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must not be empty")

		err = applySessionEvent(&msgs, &SessionEvent[*schema.Message]{
			MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{"dup", "dup"}},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "duplicate")
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
		ts := time.Date(2026, 5, 22, 10, 0, 0, 0, time.UTC)
		ev := &AgentEvent{
			Timestamp: ts,
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{Message: schema.AssistantMessage("hi", nil), Role: schema.Assistant},
			},
		}
		stripped := stripSessionEventFields(ev)
		require.NotNil(t, stripped)
		assert.Equal(t, ts, stripped.Timestamp)
		assert.Equal(t, "hi", stripped.Output.MessageOutput.Message.Content)
	})

	t.Run("SessionEvent-only event drops to nil", func(t *testing.T) {
		ev := &AgentEvent{
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: &TurnEndState[*schema.Message]{},
			},
		}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})

	t.Run("message mutation SessionEvent-only event drops to nil", func(t *testing.T) {
		msgs := []*schema.Message{schema.UserMessage("x")}
		ev := &AgentEvent{
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:             SessionEventMessagesReplaced,
				MessagesReplaced: &msgs,
			},
		}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})

	t.Run("Err with SessionEvent keeps Err", func(t *testing.T) {
		ts := time.Date(2026, 5, 22, 10, 1, 0, 0, time.UTC)
		ev := &AgentEvent{
			Timestamp: ts,
			Err:       errors.New("visible"),
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: &TurnEndState[*schema.Message]{},
			},
			SessionID: "child-1",
		}
		stripped := stripSessionEventFields(ev)
		require.NotNil(t, stripped)
		assert.Nil(t, stripped.SessionEvent)
		assert.Empty(t, stripped.SessionID)
		assert.Equal(t, ts, stripped.Timestamp)
		assert.EqualError(t, stripped.Err, "visible")
	})

	t.Run("SessionID alone is stripped", func(t *testing.T) {
		ev := &AgentEvent{SessionID: "child-1"}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})
}

func TestSessionEventTimestamp(t *testing.T) {
	ts := time.Date(2026, 5, 22, 10, 2, 0, 0, time.UTC)
	msg := schema.AssistantMessage("hi", nil)
	EnsureMessageID(msg)
	event := &AgentEvent{
		EventID:   uuid.NewString(),
		Timestamp: ts,
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{Message: msg, Role: schema.Assistant},
		},
	}

	se := toSessionEvent(event)
	require.NotNil(t, se)
	assert.Equal(t, ts, se.Timestamp)

	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	assert.Equal(t, ts, decoded.Timestamp)
}

// TestReconstructFromEventLog_EmptySession verifies empty-session reconstruction.
func TestReconstructFromEventLog_EmptySession(t *testing.T) {
	store := newSessionHelperStore()
	ctx := context.Background()
	result, err := reconstructSessionState[*schema.Message](ctx, store, "empty", defaultLoadPageSize)
	require.NoError(t, err)
	assert.Nil(t, result)
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
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// Turn 2: input "Q2" + output "A2"
	q2 := schema.UserMessage("Q2")
	EnsureMessageID(q2)
	a2 := schema.AssistantMessage("A2", nil)
	EnsureMessageID(a2)
	for _, m := range []*schema.Message{q2, a2} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 4)
	assert.Equal(t, "Q1", result.state.Messages[0].Content)
	assert.Equal(t, "A1", result.state.Messages[1].Content)
	assert.Equal(t, "Q2", result.state.Messages[2].Content)
	assert.Equal(t, "A2", result.state.Messages[3].Content)

	// Verify pagination: use page size 2 so that 4 events require multiple pages.
	result2, err := reconstructSessionState[*schema.Message](ctx, store, sid, 2)
	require.NoError(t, err)
	require.NotNil(t, result2)
	require.NotNil(t, result2.state)
	require.Len(t, result2.state.Messages, 4)
	assert.Equal(t, "Q1", result2.state.Messages[0].Content)
	assert.Equal(t, "A1", result2.state.Messages[1].Content)
	assert.Equal(t, "Q2", result2.state.Messages[2].Content)
	assert.Equal(t, "A2", result2.state.Messages[3].Content)
}

func TestReconstructFromEventLog_CorruptEventReturnsError(t *testing.T) {
	store := newSessionHelperStore()
	ctx := context.Background()
	sid := "corrupt-event"

	msg := schema.UserMessage("valid")
	EnsureMessageID(msg)
	se := withTestEventID(&SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		Message: msg,
	})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	corruptPayload := []byte(`{"event_id":"` + uuid.NewString() + `","kind":"message","message":` + "\x00\xff invalid json")
	require.False(t, json.Valid(corruptPayload), "payload must be invalid JSON")
	corruptID := uuid.NewString()
	store.mu.Lock()
	store.events = append(store.events, storedSessionEvent{EventID: corruptID, Kind: SessionEventMessage, Data: corruptPayload})
	store.eventIDs = append(store.eventIDs, corruptID)
	store.eventIDIdx[corruptID] = len(store.events) - 1
	store.mu.Unlock()

	_, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.Error(t, err, "corrupt event must cause reconstruction failure")
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
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	// Boundary: summary of all messages.
	summary := schema.UserMessage("summary")
	EnsureMessageID(summary)
	repl := []*schema.Message{summary}
	se := withTestEventID(&SessionEvent[*schema.Message]{MessagesReplaced: &repl})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	// Post-boundary events.
	post := schema.AssistantMessage("post", nil)
	EnsureMessageID(post)
	se = withTestEventID(&SessionEvent[*schema.Message]{Message: post})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "summary", result.state.Messages[0].Content)
	assert.Equal(t, "post", result.state.Messages[1].Content)
}

func TestSessionRollbackEventRoundTrip(t *testing.T) {
	se := &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(),
		Kind:    SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID:             "turn-end-1",
			ToTurnID:              "turn-1",
			PreviousHeadTurnEndID: "turn-end-2",
			PreviousHeadTurnID:    "turn-2",
		},
	}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)

	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.Rollback)
	assert.Equal(t, SessionEventRollback, decoded.Kind)
	assert.Equal(t, "turn-end-1", decoded.Rollback.ToEventID)
	assert.Equal(t, "turn-1", decoded.Rollback.ToTurnID)
	assert.Equal(t, "turn-end-2", decoded.Rollback.PreviousHeadTurnEndID)
	assert.Equal(t, "turn-2", decoded.Rollback.PreviousHeadTurnID)
}

func TestRollbackSessionReconstructionHidesDeadBranchAndKeepsNewSuffix(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "rollback-reconstruct"

	t1 := appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "Q1", "A1")
	t2 := appendCommittedTestTurn(t, ctx, store, sid, "turn-2", "Q2", "A2")
	require.NoError(t, RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-1",
		WithRollbackSessionCheckPointStore(store),
		WithRollbackSessionExpectedHeadTurnID("turn-2"),
	))
	appendCommittedTestTurn(t, ctx, store, sid, "turn-3", "Q3", "A3")

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, 2)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 4)
	assert.Equal(t, "Q1", result.state.Messages[0].Content)
	assert.Equal(t, "A1", result.state.Messages[1].Content)
	assert.Equal(t, "Q3", result.state.Messages[2].Content)
	assert.Equal(t, "A3", result.state.Messages[3].Content)
	assert.Equal(t, "turn-3", result.state.SessionValues["turn"])

	rollbackEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventRollback
	})
	require.Len(t, rollbackEvents, 1)
	require.NotNil(t, rollbackEvents[0].Rollback)
	assert.Equal(t, t1.EventID, rollbackEvents[0].Rollback.ToEventID)
	assert.Equal(t, "turn-1", rollbackEvents[0].Rollback.ToTurnID)
	assert.Equal(t, t2.EventID, rollbackEvents[0].Rollback.PreviousHeadTurnEndID)
	assert.Equal(t, "turn-2", rollbackEvents[0].Rollback.PreviousHeadTurnID)
	assert.NotContains(t, store.checkpoints, sessionRunnerCheckpointID(sid))
}

func TestRollbackSessionMultipleRollbacksProjectActiveBranch(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "rollback-multiple"

	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "Q1", "A1")
	appendCommittedTestTurn(t, ctx, store, sid, "turn-2", "Q2", "A2")
	require.NoError(t, RollbackSession[*schema.Message](ctx, store, sid, "turn-1"))
	appendCommittedTestTurn(t, ctx, store, sid, "turn-3", "Q3", "A3")
	require.NoError(t, RollbackSession[*schema.Message](ctx, store, sid, "turn-1"))

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "Q1", result.state.Messages[0].Content)
	assert.Equal(t, "A1", result.state.Messages[1].Content)
	assert.Equal(t, "turn-1", result.state.SessionValues["turn"])

	rollbackEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventRollback
	})
	require.Len(t, rollbackEvents, 2)
}

func TestRunnerQueryAfterRollbackUsesActiveProjection(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "runner-query-after-rollback"

	firstAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil)},
		},
	}
	firstRunner := NewRunner(ctx, RunnerConfig{
		Agent:          firstAgent,
		SessionID:      sid,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, firstRunner.Query(ctx, "first"))
	firstTurnEndEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventTurnEnd
	})
	require.Len(t, firstTurnEndEvents, 1)
	firstTurnID := firstTurnEndEvents[0].TurnID

	secondAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil), schema.UserMessage("second"), schema.AssistantMessage("answer2", nil)},
		},
	}
	secondRunner := NewRunner(ctx, RunnerConfig{
		Agent:          secondAgent,
		SessionID:      sid,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, secondRunner.Query(ctx, "second"))

	require.NoError(t, RollbackSession[*schema.Message](ctx, store, sid, firstTurnID))

	thirdAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil), schema.UserMessage("third"), schema.AssistantMessage("answer3", nil)},
		},
	}
	thirdRunner := NewRunner(ctx, RunnerConfig{
		Agent:          thirdAgent,
		SessionID:      sid,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, thirdRunner.Query(ctx, "third"))

	require.Len(t, thirdAgent.inputs, 1)
	require.Len(t, thirdAgent.inputs[0], 3)
	assert.Equal(t, "first", thirdAgent.inputs[0][0].Content)
	assert.Equal(t, "ok", thirdAgent.inputs[0][1].Content)
	assert.Equal(t, "third", thirdAgent.inputs[0][2].Content)
}

func TestRollbackSessionTargetResolutionErrors(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "rollback-target-errors"

	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "Q1", "A1")
	appendCommittedTestTurn(t, ctx, store, sid, "turn-2", "Q2", "A2")
	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		TurnID:  "turn-pending",
		Message: testMessageWithID("pending", schema.User),
	})

	err := RollbackSession[*schema.Message](ctx, store, sid, "turn-pending")
	require.ErrorIs(t, err, ErrInvalidRollbackTarget)

	err = RollbackSession[*schema.Message](ctx, store, sid, "missing")
	require.ErrorIs(t, err, ErrRollbackTargetNotFound)

	err = RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-1",
		WithRollbackSessionExpectedHeadTurnID("stale-head"),
	)
	require.ErrorIs(t, err, ErrSessionHeadChanged)
	rollbackEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventRollback
	})
	require.Empty(t, rollbackEvents)

	require.NoError(t, RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-1",
		WithRollbackSessionExpectedHeadTurnID("turn-2"),
	))
	err = RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-2",
		WithRollbackSessionExpectedHeadTurnID("turn-2"),
	)
	require.ErrorIs(t, err, ErrRollbackTargetInactive)
}

func TestReconstructRollbackMalformedRecordsFailClosed(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "rollback-malformed"

	msg := appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		TurnID:  "turn-1",
		Message: testMessageWithID("Q1", schema.User),
	})
	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "A1")

	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind: SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID: msg.EventID,
			ToTurnID:  "turn-1",
		},
	})
	_, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.ErrorIs(t, err, ErrInvalidRollbackTarget)

	store = newSessionHelperStore()
	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "Q1", "A1")
	payloadEvent := &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(),
		Kind:    SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID: "missing-turn-end-event",
			ToTurnID:  "turn-1",
		},
	}
	data, encodeErr := encodeSessionEvent(payloadEvent)
	require.NoError(t, encodeErr)
	store.mu.Lock()
	store.events = append(store.events, storedSessionEvent{
		EventID: payloadEvent.EventID,
		Kind:    payloadEvent.Kind,
		Data:    append([]byte{}, data...),
	})
	store.eventIDs = append(store.eventIDs, payloadEvent.EventID)
	store.eventIDIdx[payloadEvent.EventID] = len(store.events) - 1
	store.mu.Unlock()
	_, err = reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.ErrorIs(t, err, ErrRollbackTargetInactive)

	store = newSessionHelperStore()
	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "Q1", "A1")
	staleTarget := appendCommittedTestTurn(t, ctx, store, sid, "turn-2", "Q2", "A2")
	require.NoError(t, RollbackSession[*schema.Message](ctx, store, sid, "turn-1"))
	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind: SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID: staleTarget.EventID,
			ToTurnID:  "turn-2",
		},
	})
	_, err = reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.ErrorIs(t, err, ErrRollbackTargetInactive)
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
		Agent:          firstAgent,
		SessionID:      sid,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))

	// Verify context-commit events were captured: caller input + assistant output + turn-end.
	commitEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage || se.Kind == SessionEventTurnEnd
	})
	require.Len(t, commitEvents, 3, "input event + assistant event + turn-end event should be in event log")

	// Capture the prepared session state before agent runs.
	capturedAgent := &runnerSessionAgent{
		name: "ra",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{},
		},
	}
	runner = NewRunner(ctx, RunnerConfig{
		Agent:          capturedAgent,
		SessionID:      sid,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
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
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "user-question"))

	// Single-turn run: 1 user input event + 1 assistant output event + 1 TurnEnd event,
	// plus non-context lifecycle timeline records.
	commitEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage || se.Kind == SessionEventTurnEnd
	})
	require.Len(t, commitEvents, 3)
	// The first message event should be the user input.
	first := commitEvents[0]
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
	mu       sync.Mutex
	calls    []string // "append" or "set:<key>"
	delaySet time.Duration
}

func newRecordingHelperStore() *recordingHelperStore {
	return &recordingHelperStore{sessionHelperStore: newSessionHelperStore()}
}

func (s *recordingHelperStore) AppendEvents(ctx context.Context, sid string, events []*SessionEvent[*schema.Message]) error {
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
		SessionService:  store,
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
		SessionService:  store,
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

	cfg := normalizeSessionConfig(&SessionConfig{
		EventFlushBatchSize: 1,
		EventFlushInterval:  10 * time.Millisecond,
		EventBufferSize:     8,
		MaxFlushRetries:     -1, // disable retries for fast failure
	})
	p := newSessionEventPersister[*schema.Message](ctx, store, "sid", cfg)
	defer p.closeAndWait()

	require.NoError(t, p.enqueue(validTestPayload()))
	// Wait for the run loop to attempt AppendEvents and record the error.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if p.getErr() != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	require.Error(t, p.getErr(), "persister must record the AppendEvents failure")

	err := p.enqueue(validTestPayload())
	require.Error(t, err, "enqueue after persist failure must return an error")
	assert.Contains(t, err.Error(), "append failed")

	for i := 0; i < 4; i++ {
		err = p.enqueue(validTestPayload())
		require.Error(t, err, "latched error must be returned consistently")
		assert.Contains(t, err.Error(), "append failed")
	}
}

// transientFailStore fails the first N AppendEvents calls then succeeds.
type transientFailStore struct {
	sessionHelperStore
	retryMu      sync.Mutex
	failsLeft    int
	appendCalls  int
	appendErrVal error
}

func (s *transientFailStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	s.retryMu.Lock()
	s.appendCalls++
	if s.failsLeft > 0 {
		s.failsLeft--
		s.retryMu.Unlock()
		return s.appendErrVal
	}
	s.retryMu.Unlock()
	return s.sessionHelperStore.AppendEvents(ctx, sessionID, events)
}

func (s *transientFailStore) getAppendCalls() int {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	return s.appendCalls
}

// TestSessionPersister_FlushRetryTransientRecovery verifies that transient
// AppendEvents failures are retried and the persister recovers on success.
func TestSessionPersister_FlushRetryTransientRecovery(t *testing.T) {
	ctx := context.Background()
	store := &transientFailStore{
		sessionHelperStore: *newSessionHelperStore(),
		failsLeft:          2,
		appendErrVal:       errors.New("transient"),
	}

	cfg := normalizeSessionConfig(&SessionConfig{
		EventFlushBatchSize:      1,
		EventFlushInterval:       10 * time.Millisecond,
		EventBufferSize:          8,
		MaxFlushRetries:          3,
		FlushRetryInitialBackoff: 5 * time.Millisecond,
	})
	p := newSessionEventPersister[*schema.Message](ctx, store, "sid", cfg)

	require.NoError(t, p.enqueue(validTestPayload()))

	err := p.closeAndWait()
	require.NoError(t, err, "persister should recover after transient failures")
	assert.Nil(t, p.getErr())
	// Should have called AppendEvents 3 times (2 failures + 1 success).
	assert.Equal(t, 3, store.getAppendCalls())
	// Event should be persisted.
	store.sessionHelperStore.mu.Lock()
	assert.Equal(t, 1, len(store.sessionHelperStore.events))
	store.sessionHelperStore.mu.Unlock()
}

// TestSessionPersister_FlushRetryPermanentFailure verifies that after exhausting
// all retries, the error is latched.
func TestSessionPersister_FlushRetryPermanentFailure(t *testing.T) {
	ctx := context.Background()
	store := &transientFailStore{
		sessionHelperStore: *newSessionHelperStore(),
		failsLeft:          100, // always fail
		appendErrVal:       errors.New("permanent"),
	}

	cfg := normalizeSessionConfig(&SessionConfig{
		EventFlushBatchSize:      1,
		EventFlushInterval:       10 * time.Millisecond,
		EventBufferSize:          8,
		MaxFlushRetries:          2,
		FlushRetryInitialBackoff: 5 * time.Millisecond,
	})
	p := newSessionEventPersister[*schema.Message](ctx, store, "sid", cfg)

	require.NoError(t, p.enqueue(validTestPayload()))

	err := p.closeAndWait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permanent")
	// Should have called AppendEvents exactly MaxFlushRetries+1 = 3 times.
	assert.Equal(t, 3, store.getAppendCalls())
}

// TestSessionPersister_FlushRetryContextCancellation verifies that the retry
// loop exits promptly when the context is cancelled during backoff.
func TestSessionPersister_FlushRetryContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &transientFailStore{
		sessionHelperStore: *newSessionHelperStore(),
		failsLeft:          100, // always fail
		appendErrVal:       errors.New("failing"),
	}

	cfg := normalizeSessionConfig(&SessionConfig{
		EventFlushBatchSize:      1,
		EventFlushInterval:       10 * time.Millisecond,
		EventBufferSize:          8,
		MaxFlushRetries:          5,
		FlushRetryInitialBackoff: 500 * time.Millisecond, // long backoff to ensure cancel fires during wait
	})
	p := newSessionEventPersister[*schema.Message](ctx, store, "sid", cfg)

	require.NoError(t, p.enqueue(validTestPayload()))

	// Wait for the first attempt to fail, then cancel during backoff.
	time.Sleep(50 * time.Millisecond)
	cancel()

	err := p.closeAndWait()
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	// Should NOT have exhausted all retries.
	assert.Less(t, store.getAppendCalls(), 5)
}

// --- Attack tests for TurnID / inFlightTurnID recovery ---

// TestAttack_InFlightTurnIDRecoveryOnResume verifies that reconstructSessionState
// correctly identifies an in-flight (interrupted) turn's TurnID from events
// after the last committed TurnEnd.
func TestAttack_InFlightTurnIDRecoveryOnResume(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "inflight-recovery"

	committedMsg := schema.UserMessage("committed-msg")
	EnsureMessageID(committedMsg)

	// A committed turn: TurnStart (lifecycle running) + Message + TurnEnd, all with TurnID "turn-committed"
	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusRunning, TurnID: "turn-committed", Lifecycle: &LifecycleEvent{Scope: LifecycleScopeSession, State: SessionRunStateRunning}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-committed", Message: committedMsg},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnID: "turn-committed", TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"k": "v"}}},
	}

	// An interrupted turn: a Message event with TurnID "turn-interrupted" and NO TurnEnd
	interruptedMsg := schema.AssistantMessage("interrupted-msg", nil)
	EnsureMessageID(interruptedMsg)
	events = append(events, &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-interrupted", Message: interruptedMsg,
	})

	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "turn-interrupted", result.inFlightTurnID)
	require.NotNil(t, result.state)
	// State should have messages from committed turn (1) + interrupted turn (1).
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "committed-msg", result.state.Messages[0].Content)
	assert.Equal(t, "interrupted-msg", result.state.Messages[1].Content)
}

func TestAttack_InFlightTurnIDRecoveryWithoutCommittedTurnEnd(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "inflight-no-committed-turn"

	msg := schema.UserMessage("first-turn")
	EnsureMessageID(msg)
	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-interrupted", Message: msg},
		{EventID: uuid.NewString(), Kind: SessionEventAgentInterrupt, TurnID: "turn-interrupted", AgentInterrupt: &AgentInterruptEvent{
			Contexts: []*AgentInterruptContext{
				{
					Cause:       AgentInterruptCauseGeneric,
					InterruptID: "agent:InterruptAgent",
					Info:        "approval_needed",
				},
			},
		}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "turn-interrupted", result.inFlightTurnID)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 1)
	assert.Equal(t, "first-turn", result.state.Messages[0].Content)
}

// TestAttack_InFlightTurnIDEmptyWhenNoPostTurnEndEvents verifies that when
// the last event is a TurnEnd (complete turn), inFlightTurnID is empty.
func TestAttack_InFlightTurnIDEmptyWhenNoPostTurnEndEvents(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "no-inflight"

	msg := schema.UserMessage("hello")
	EnsureMessageID(msg)

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-1", Message: msg},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnID: "turn-1", TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"done": true}}},
	}

	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "", result.inFlightTurnID)
}

// TestAttack_InFlightTurnIDMultipleTurnIDsInTail verifies that when multiple
// post-TurnEnd events have different TurnIDs, only the first one is used.
func TestAttack_InFlightTurnIDMultipleTurnIDsInTail(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "multi-turnid-tail"

	committedMsg := schema.UserMessage("committed")
	EnsureMessageID(committedMsg)

	msgA := schema.UserMessage("msg-A")
	EnsureMessageID(msgA)
	msgB := schema.AssistantMessage("msg-B", nil)
	EnsureMessageID(msgB)

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-committed", Message: committedMsg},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnID: "turn-committed", TurnEnd: &TurnEndState[*schema.Message]{}},
		// Post-TurnEnd events with different TurnIDs
		{EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-A", Message: msgA},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, TurnID: "turn-B", Message: msgB},
	}

	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "turn-A", result.inFlightTurnID, "should take the first TurnID found after committed TurnEnd")
}

// TestAttack_OldRunIDFieldIgnoredOnDeserialization verifies that a JSON payload
// containing a legacy "run_id" field is deserialized without error, and the
// field is silently ignored (no RunID field on the struct).
func TestAttack_OldRunIDFieldIgnoredOnDeserialization(t *testing.T) {
	// Manually craft JSON with a legacy "run_id" field alongside valid fields.
	rawJSON := []byte(`{
		"event_id": "evt-legacy",
		"run_id": "old-run",
		"turn_id": "turn-1",
		"kind": "message",
		"message": {"role": "user", "content": "hello from legacy"}
	}`)

	event, err := decodeSessionEventWithSerializer[*schema.Message](rawJSON, nil)
	require.NoError(t, err, "deserialization must not fail on unknown run_id field")
	require.NotNil(t, event)
	assert.Equal(t, "turn-1", event.TurnID)
	assert.Equal(t, "evt-legacy", event.EventID)
	require.NotNil(t, event.Message)
	assert.Equal(t, "hello from legacy", event.Message.Content)
}

// TestAttack_ResumePreservesTurnIDFromInterruptedRun verifies that Resume
// carries the same TurnID as the interrupted run's events.
func TestAttack_ResumePreservesTurnIDFromInterruptedRun(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "resume-turnid-preserve"

	// First, run a normal turn that completes (provides a committed TurnEnd baseline).
	normalAgent := &runnerSessionAgent{
		name: "normal-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("first answer", nil)},
		},
	}
	firstRunner := NewRunner(ctx, RunnerConfig{
		Agent:           normalAgent,
		SessionID:       sessionID,
		SessionService:  store,
		CheckPointStore: store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, firstRunner.Query(ctx, "first question"))

	// Now run a query that interrupts (building on the committed session).
	agent := &runnerInterruptAgent{}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		SessionID:       sessionID,
		SessionService:  store,
		CheckPointStore: store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "trigger interrupt")
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	// Find the TurnID used by the interrupted run. It must differ from the first run's TurnID.
	// Collect all unique TurnIDs from the store.
	turnIDSet := make(map[string]bool)
	for _, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.TurnID != "" {
			turnIDSet[se.TurnID] = true
		}
	}
	require.GreaterOrEqual(t, len(turnIDSet), 2, "must have at least 2 distinct TurnIDs (committed + interrupted)")

	// The interrupted TurnID is the one on reconstructable model-context events
	// after the last TurnEnd. Timeline status events are not replay anchors.
	var lastTurnEndIdx int
	for i, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Kind == SessionEventTurnEnd && se.TurnID != "" {
			lastTurnEndIdx = i
		}
	}
	var interruptedTurnID string
	for i := lastTurnEndIdx + 1; i < len(store.events); i++ {
		se, err := decodeSessionEvent[*schema.Message](store.events[i].Data)
		require.NoError(t, err)
		if se.Kind == SessionEventMessage && se.TurnID != "" {
			interruptedTurnID = se.TurnID
			break
		}
	}
	require.NotEmpty(t, interruptedTurnID, "interrupted run must have events with a TurnID after the last TurnEnd")

	// Record event count before resume.
	eventsBeforeResume := len(store.events)

	// Resume the runner.
	resumeIter, err := runner.Resume(ctx, "")
	require.NoError(t, err)
	for {
		_, ok := resumeIter.Next()
		if !ok {
			break
		}
	}

	// Check that resume events (added after the interrupted run) carry the same TurnID.
	var resumeTurnIDs []string
	for i := eventsBeforeResume; i < len(store.events); i++ {
		se, err := decodeSessionEvent[*schema.Message](store.events[i].Data)
		require.NoError(t, err)
		if se.TurnID != "" {
			resumeTurnIDs = append(resumeTurnIDs, se.TurnID)
		}
	}
	require.NotEmpty(t, resumeTurnIDs, "resume must produce events with TurnIDs")
	for _, tid := range resumeTurnIDs {
		assert.Equal(t, interruptedTurnID, tid, "resume events must carry the same TurnID as the interrupted run")
	}
}

// TestAttack_FreshRunIgnoresInFlightTurnID verifies that a fresh Run on a
// session with an interrupted turn does NOT reuse the interrupted TurnID.
func TestAttack_FreshRunIgnoresInFlightTurnID(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "fresh-run-ignores-inflight"

	// First, run a normal turn that completes (provides a committed TurnEnd baseline).
	normalAgent := &runnerSessionAgent{
		name: "normal-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("baseline", nil)},
		},
	}
	baselineRunner := NewRunner(ctx, RunnerConfig{
		Agent:           normalAgent,
		SessionID:       sessionID,
		SessionService:  store,
		CheckPointStore: store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, baselineRunner.Query(ctx, "baseline"))

	// Now run a query that interrupts.
	agent := &runnerInterruptAgent{}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		SessionID:       sessionID,
		SessionService:  store,
		CheckPointStore: store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "trigger interrupt")
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	// Identify the interrupted TurnID (events after the last committed TurnEnd).
	var lastTurnEndIdx int
	for i, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Kind == SessionEventTurnEnd && se.TurnID != "" {
			lastTurnEndIdx = i
		}
	}
	var interruptedTurnID string
	for i := lastTurnEndIdx + 1; i < len(store.events); i++ {
		se, err := decodeSessionEvent[*schema.Message](store.events[i].Data)
		require.NoError(t, err)
		if se.TurnID != "" {
			interruptedTurnID = se.TurnID
			break
		}
	}
	require.NotEmpty(t, interruptedTurnID)

	// Instead of resuming, create a NEW runner on the same session and run a new query (fresh Run).
	eventsBeforeFresh := len(store.events)
	freshAgent := &runnerSessionAgent{
		name: "fresh-agent",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("fresh answer", nil)},
		},
	}
	freshRunner := NewRunner(ctx, RunnerConfig{
		Agent:           freshAgent,
		SessionID:       sessionID,
		SessionService:  store,
		CheckPointStore: store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, freshRunner.Query(ctx, "new question"))

	// Collect TurnIDs from the fresh run's events.
	var freshTurnIDs []string
	for i := eventsBeforeFresh; i < len(store.events); i++ {
		se, err := decodeSessionEvent[*schema.Message](store.events[i].Data)
		require.NoError(t, err)
		if se.TurnID != "" {
			freshTurnIDs = append(freshTurnIDs, se.TurnID)
		}
	}
	require.NotEmpty(t, freshTurnIDs, "fresh run must have events with TurnIDs")
	for _, tid := range freshTurnIDs {
		assert.NotEqual(t, interruptedTurnID, tid, "fresh run must NOT reuse the interrupted TurnID")
	}
}
