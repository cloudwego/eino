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
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// sessionHelperStore is a single-session in-memory typed session store for unit tests.
// Mirrors the EventID-based cursor semantics of session.InMemoryStore so the
// in-package tests exercise the same protocol contract.
type sessionHelperStore struct {
	mu          sync.Mutex
	checkpoints map[string][]byte

	events        []storedSessionEvent
	eventIDs      []string
	eventIDIdx    map[string]int
	appendBatches [][]SessionEventKind
	loadErr       error
	appendErr     error
	userMsgErr    error
	kindErr       map[SessionEventKind]error
	deleteErr     error
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

type publicSessionHelperStore struct {
	*sessionHelperStore
}

func (s *publicSessionHelperStore) LoadEvents(ctx context.Context, sessionID string, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	res, err := s.sessionHelperStore.LoadEventsForSession(ctx, sessionID, req)
	if err != nil {
		return nil, err
	}
	return res, nil
}

func (s *publicSessionHelperStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	return s.sessionHelperStore.AppendEventsForSession(ctx, sessionID, events)
}

func newBlockingAppendStore() *blockingAppendStore {
	return &blockingAppendStore{
		sessionHelperStore: *newSessionHelperStore(),
		appendStarted:      make(chan struct{}),
		releaseAppend:      make(chan struct{}),
	}
}

func (s *blockingAppendStore) AppendEventsForSession(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	s.startOnce.Do(func() {
		close(s.appendStarted)
	})
	select {
	case <-s.releaseAppend:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.sessionHelperStore.AppendEventsForSession(ctx, sessionID, events)
}

func (s *blockingAppendStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, sessionID, events)
}

func (s *blockingAppendStore) openSession(_ context.Context, req *openSessionRequest) (*openSessionResult[*schema.Message], error) {
	sessionID := ""
	if req != nil {
		sessionID = req.sessionID
	}
	return &openSessionResult[*schema.Message]{handle: &legacyMessageTestHandle{store: s, sessionID: sessionID}}, nil
}

func (s *blockingAppendStore) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, "", events)
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

func withTestCommittedIdle[M MessageType](eventID string) *SessionEvent[M] {
	return withTestEventID(&SessionEvent[M]{
		EventID: eventID,
		Kind:    SessionEventSessionStatusIdle,
		Lifecycle: &LifecycleEvent{
			State:      SessionRunStateIdle,
			StopReason: &StopReason{Type: "end_turn"},
		},
	})
}

func testSequentialEventIDGenerator(prefix string) SessionEventIDGenerator[*schema.Message] {
	var n int64
	return func(_ context.Context, _ *SessionEvent[*schema.Message]) (string, error) {
		return fmt.Sprintf("%s%d", prefix, atomic.AddInt64(&n, 1)), nil
	}
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

type testSessionAppendStore interface {
	AppendEventsForSession(context.Context, string, []*SessionEvent[*schema.Message]) error
}

func appendTestSessionEvent(t *testing.T, ctx context.Context, store testSessionAppendStore, sid string, se *SessionEvent[*schema.Message]) *SessionEvent[*schema.Message] {
	t.Helper()
	se = withTestEventID(se)
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
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

func appendCommittedTestTurn(t *testing.T, ctx context.Context, store testSessionAppendStore, sid string, boundaryEventID string, contents ...string) *SessionEvent[*schema.Message] {
	t.Helper()
	for i, content := range contents {
		role := schema.User
		if i%2 == 1 {
			role = schema.Assistant
		}
		appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
			Kind:    SessionEventMessage,
			Message: testMessageWithID(content, role),
		})
	}
	return appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		EventID: boundaryEventID,
		Kind:    SessionEventSessionStatusIdle,
		Lifecycle: &LifecycleEvent{
			State:      SessionRunStateIdle,
			StopReason: &StopReason{Type: "end_turn"},
		},
	})
}

type runnerSessionAgent struct {
	name    string
	inputs  [][]*schema.Message
	values  []map[string]any
	turnEnd *testTurnState[*schema.Message]
}

type testTurnState[M MessageType] struct {
	Messages          []M
	ToolInfos         []*schema.ToolInfo
	DeferredToolInfos []*schema.ToolInfo
	SessionValues     map[string]any
}

func (a *runnerSessionAgent) Name(_ context.Context) string        { return a.name }
func (a *runnerSessionAgent) Description(_ context.Context) string { return "runner session agent" }
func (a *runnerSessionAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	a.inputs = append(a.inputs, append([]*schema.Message{}, input.Messages...))
	a.values = append(a.values, GetSessionValues(ctx))
	go func() {
		defer gen.Close()
		gen.Send(&AgentEvent{
			AgentName: a.name,
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{Message: schema.AssistantMessage("ok", nil), Role: schema.Assistant},
			},
		})
	}()
	return iter
}

type streamingSessionAgent struct {
	release chan struct{}
	variant *SessionEventVariant[*schema.Message]
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
			SessionEventVariant: a.variant,
		})
		<-a.release
		sw.Close()
	}()
	return iter
}

type erroredStreamingInterruptAgent struct {
	streamErr error
}

func (a *erroredStreamingInterruptAgent) Name(_ context.Context) string {
	return "errored-streaming-interrupt-agent"
}

func (a *erroredStreamingInterruptAgent) Description(_ context.Context) string {
	return "errored streaming interrupt agent"
}

func (a *erroredStreamingInterruptAgent) Run(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	sr, sw := schema.Pipe[*schema.Message](2)
	streamErr := a.streamErr
	if streamErr == nil {
		streamErr = errors.New("stream failed")
	}
	go func() {
		defer gen.Close()
		sw.Send(schema.AssistantMessage("partial", nil), nil)
		sw.Send(nil, streamErr)
		sw.Close()
		gen.Send(&AgentEvent{
			AgentName: a.Name(ctx),
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{IsStreaming: true, MessageStream: sr, Role: schema.Assistant},
			},
		})
		gen.Send(Interrupt(ctx, "checkpoint after errored stream"))
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

func (s *sessionHelperStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, sessionID, events)
}

func (s *sessionHelperStore) AppendEventsForSession(_ context.Context, _ string, events []*SessionEvent[*schema.Message]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.appendErr != nil {
		return s.appendErr
	}
	batch := make([]SessionEventKind, 0, len(events))
	for _, e := range events {
		if e == nil || e.EventID == "" {
			return ErrInvalidEventID
		}
		if err := NormalizeSessionEventKind(e); err != nil {
			return err
		}
		if err := s.kindErr[e.Kind]; err != nil {
			return err
		}
		if s.userMsgErr != nil && e.Message != nil && e.Message.Role == schema.User {
			return s.userMsgErr
		}
		if _, dup := s.eventIDIdx[e.EventID]; dup {
			continue
		}
		batch = append(batch, e.Kind)
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
	if len(batch) > 0 {
		s.appendBatches = append(s.appendBatches, batch)
	}
	return nil
}

func (s *sessionHelperStore) LoadEvents(ctx context.Context, sessionID string, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	return s.LoadEventsForSession(ctx, sessionID, req)
}

func (s *sessionHelperStore) LoadEventsForSession(_ context.Context, _ string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
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

func (s *sessionHelperStore) openSession(_ context.Context, req *openSessionRequest) (*openSessionResult[*schema.Message], error) {
	sessionID := ""
	if req != nil {
		sessionID = req.sessionID
	}
	return &openSessionResult[*schema.Message]{
		handle: &testSessionHandle{store: s, sessionID: sessionID},
	}, nil
}

func (s *sessionHelperStore) loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	return s.LoadEventsForSession(ctx, "", req)
}

func (s *sessionHelperStore) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, "", events)
}

func (s *sessionHelperStore) close(context.Context) error { return nil }

type testSessionHandle struct {
	store     *sessionHelperStore
	sessionID string
}

func (h *testSessionHandle) loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	if req == nil {
		req = &LoadSessionEventsRequest{}
	}
	return h.store.LoadEventsForSession(ctx, h.sessionID, req)
}

func (h *testSessionHandle) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return h.store.AppendEventsForSession(ctx, h.sessionID, events)
}

func (h *testSessionHandle) close(context.Context) error { return nil }

type legacyMessageTestStore interface {
	AppendEventsForSession(context.Context, string, []*SessionEvent[*schema.Message]) error
	LoadEventsForSession(context.Context, string, *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error)
}

type legacyMessageTestHandle struct {
	store     legacyMessageTestStore
	sessionID string
}

func (h *legacyMessageTestHandle) loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	if req == nil {
		req = &LoadSessionEventsRequest{}
	}
	return h.store.LoadEventsForSession(ctx, h.sessionID, req)
}

func (h *legacyMessageTestHandle) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return h.store.AppendEventsForSession(ctx, h.sessionID, events)
}

func (h *legacyMessageTestHandle) close(context.Context) error { return nil }

func mustOpenTestSession[M MessageType](t testing.TB, ctx context.Context, store SessionEventStore[M], sessionID string) sessionHandle[M] {
	t.Helper()
	res, err := openLocalSession(ctx, store, &openSessionRequest{sessionID: sessionID})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NotNil(t, res.handle)
	t.Cleanup(func() { _ = res.handle.close(ctx) })
	return res.handle
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
		turnEnd: &testTurnState[*schema.Message]{
			Messages:      []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil)},
			SessionValues: map[string]any{"k": "restored"},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        firstAgent,
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))

	secondAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages:      []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil), schema.UserMessage("second"), schema.AssistantMessage("answer2", nil)},
			SessionValues: map[string]any{"k": "next"},
		},
	}
	runner = NewRunner(ctx, RunnerConfig{
		Agent:        secondAgent,
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "second", WithSessionValues(map[string]any{"override": "value"})))

	require.Len(t, secondAgent.inputs, 1)
	require.Len(t, secondAgent.inputs[0], 3)
	assert.Equal(t, "first", secondAgent.inputs[0][0].Content)
	assert.Equal(t, "ok", secondAgent.inputs[0][1].Content)
	assert.Equal(t, "second", secondAgent.inputs[0][2].Content)
	require.Len(t, secondAgent.values, 1)
	assert.Nil(t, secondAgent.values[0]["k"])
	assert.Equal(t, "value", secondAgent.values[0]["override"])
}

func TestRunnerSessionModeSkipsDuplicateEmptyModelContext(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "runner-model-context-session"
	model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("ok", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "runner-model-context-agent",
		Description: "runner model context agent",
		Instruction: "You are a helpful assistant.",
		Model:       model,
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))
	drainSessionEvents(t, runner.Query(ctx, "second"))

	result, err := store.LoadEventsForSession(ctx, sessionID, &LoadSessionEventsRequest{
		Kinds: []SessionEventKind{SessionEventModelContext},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 0)
}

func TestRunnerSessionModeSkipsDuplicateToolModelContext(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "runner-tool-model-context-session"
	model := &sessionToolCallingModel{response: schema.AssistantMessage("ok", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "runner-tool-model-context-agent",
		Description: "runner tool model context agent",
		Instruction: "You are a helpful assistant.",
		Model:       model,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{modelContextExtraTool{}},
			},
		},
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sessionID,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))
	drainSessionEvents(t, runner.Query(ctx, "second"))

	result, err := store.LoadEventsForSession(ctx, sessionID, &LoadSessionEventsRequest{
		Kinds: []SessionEventKind{SessionEventModelContext},
	})
	require.NoError(t, err)
	require.Len(t, result.Events, 0)
}

func TestAttack_SessionEventIDGeneratorCoversRunnerEvents(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	prefix := "attack-runner-"
	agent := &runnerSessionAgent{name: "runner-event-id-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "runner-event-id-session",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: testSequentialEventIDGenerator(prefix),
		},
	})

	drainSessionEvents(t, runner.Query(ctx, "use configured ids"))

	events := decodeStoredSessionEvents(t, store.events)
	require.NotEmpty(t, events)
	for _, event := range events {
		require.NotEmpty(t, event.EventID)
		assert.Truef(t, strings.HasPrefix(event.EventID, prefix), "event %s used unexpected ID %q", event.Kind, event.EventID)
	}
}

func TestRunnerSessionTurnIDFreshRuns(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "turn-id-fresh"
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        &runnerSessionAgent{name: "turn-id-agent"},
		SessionID:    sessionID,
		SessionStore: store,
	})

	drainSessionEvents(t, runner.Query(ctx, "first"))
	first := decodeStoredSessionEvents(t, store.events)
	require.NotEmpty(t, first)
	require.Equal(t, SessionEventSessionStatusRunning, first[0].Kind)
	firstTurnID := first[0].EventID
	require.NotEmpty(t, firstTurnID)
	for _, event := range first {
		assert.Equal(t, firstTurnID, event.TurnID, "kind=%s event_id=%s", event.Kind, event.EventID)
	}

	beforeSecond := len(store.events)
	drainSessionEvents(t, runner.Query(ctx, "second"))
	second := decodeStoredSessionEvents(t, store.events[beforeSecond:])
	require.NotEmpty(t, second)
	require.Equal(t, SessionEventSessionStatusRunning, second[0].Kind)
	secondTurnID := second[0].EventID
	require.NotEmpty(t, secondTurnID)
	assert.NotEqual(t, firstTurnID, secondTurnID)
	for _, event := range second {
		assert.Equal(t, secondTurnID, event.TurnID, "kind=%s event_id=%s", event.Kind, event.EventID)
	}
}

func TestRunnerSessionTurnIDCustomGeneratorObservesDraft(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	const openingID = "business-opening-turn"
	var observedAfterOpening []string
	var sawOpening bool
	gen := func(ctx context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e.Kind == SessionEventSessionStatusRunning {
			sawOpening = true
			assert.Empty(t, e.TurnID)
			return openingID, nil
		}
		observedAfterOpening = append(observedAfterOpening, e.TurnID)
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        &runnerSessionAgent{name: "turn-id-generator-agent"},
		SessionID:    "turn-id-generator",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	drainSessionEvents(t, runner.Query(ctx, "hello"))

	require.True(t, sawOpening)
	require.NotEmpty(t, observedAfterOpening)
	for _, turnID := range observedAfterOpening {
		assert.Equal(t, openingID, turnID)
	}
	for _, event := range decodeStoredSessionEvents(t, store.events) {
		assert.Equal(t, openingID, event.TurnID)
	}
}

func TestAttack_RunnerHandlesSessionEventWithoutSessionStore(t *testing.T) {
	ctx := context.Background()
	runner := NewRunner(ctx, RunnerConfig{
		Agent: &runnerSessionAgent{name: "runner-session-event-no-service-agent"},
	})

	iter := runner.Query(ctx, "no managed session")
	var outputs []string
	var errs []error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errs = append(errs, event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil && event.Output.MessageOutput.Message != nil {
			outputs = append(outputs, event.Output.MessageOutput.Message.Content)
		}
	}

	require.Empty(t, errs, "session envelopes emitted outside managed-session mode must not panic or surface errors")
	assert.Equal(t, []string{"ok"}, outputs)
}

// TestSessionEventIDGenerator_UserMessageBusinessID 验证：generator 可以在
// 用户输入 message 草稿上识别业务身份并返回业务 ID（§8 UserMessage 验收）。
func TestSessionEventIDGenerator_UserMessageBusinessID(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	const businessID = "user-order-id"
	gen := func(_ context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message != nil && e.Message.Role == schema.User {
			return businessID, nil
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}
	agent := &runnerSessionAgent{name: "user-msg-business-id-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "user-msg-business-id-session",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	drainSessionEvents(t, runner.Query(ctx, "hello"))

	userMsgs := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage && se.Message != nil && se.Message.Role == schema.User
	})
	require.Len(t, userMsgs, 1)
	assert.Equal(t, businessID, userMsgs[0].EventID, "user input message must carry the generator-supplied business ID")
}

func TestSessionEventIDGenerator_OutputMessageDraftBusinessID(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	const businessID = "assistant-result-id"
	gen := func(_ context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message != nil && e.Message.Role == schema.Assistant && e.Message.Content == "ok" {
			return businessID, nil
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}
	agent := &runnerSessionAgent{name: "assistant-msg-business-id-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "assistant-msg-business-id-session",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	drainSessionEvents(t, runner.Query(ctx, "hello"))

	assistantMsgs := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage && se.Message != nil && se.Message.Role == schema.Assistant && se.Message.Content == "ok"
	})
	require.Len(t, assistantMsgs, 1)
	assert.Equal(t, businessID, assistantMsgs[0].EventID, "output message generator must see the materialized message draft")
}

// TestSessionEventIDGenerator_ControlEventsDefaultFallthrough 验证：generator
// 仅匹配业务事件时，控制事件（status_running/status_idle 等）应通过 default
// fallthrough 拿到 UUID，而非业务 ID。
func TestSessionEventIDGenerator_ControlEventsDefaultFallthrough(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	const businessID = "selective-user-id"
	gen := func(_ context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message != nil && e.Message.Role == schema.User {
			return businessID, nil
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}
	agent := &runnerSessionAgent{name: "control-fallthrough-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "control-fallthrough-session",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	drainSessionEvents(t, runner.Query(ctx, "hi"))

	controlKinds := map[SessionEventKind]struct{}{
		SessionEventSessionStatusRunning: {},
		SessionEventSessionStatusIdle:    {},
	}
	controlEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		_, ok := controlKinds[se.Kind]
		return ok
	})
	require.NotEmpty(t, controlEvents, "expected control events (status_running / status_idle) in store")
	for _, se := range controlEvents {
		require.NotEmpty(t, se.EventID)
		assert.NotEqual(t, businessID, se.EventID,
			"control event %s must default to UUID, not adopt the user-input business ID", se.Kind)
		// UUID v4 string length is 36; business ID is shorter and easily told apart.
		assert.Lenf(t, se.EventID, 36, "control event %s should be a UUID (got %q)", se.Kind, se.EventID)
	}
}

// TestSessionEventIDGenerator_FailClosedOnEmpty 验证：generator 返回空 ID 时
// runner fail closed —— 抛出 ErrSessionEventIDGeneratorEmpty 且对应草稿 event
// 不会落盘。
func TestSessionEventIDGenerator_FailClosedOnEmpty(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	gen := func(_ context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message != nil && e.Message.Role == schema.User {
			return "", nil
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}
	agent := &runnerSessionAgent{name: "fail-closed-empty-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "fail-closed-empty-session",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	iter := runner.Query(ctx, "trigger")
	var errs []error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			errs = append(errs, ev.Err)
		}
	}
	require.NotEmpty(t, errs, "expected at least one error event from fail-closed turn")
	var sawSentinel bool
	for _, err := range errs {
		if errors.Is(err, ErrSessionEventIDGeneratorEmpty) {
			sawSentinel = true
			break
		}
	}
	require.True(t, sawSentinel, "expected ErrSessionEventIDGeneratorEmpty in error stream, got %v", errs)

	// Fail-closed: the offending user-input message must NOT be persisted.
	userMsgs := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage && se.Message != nil && se.Message.Role == schema.User
	})
	assert.Empty(t, userMsgs, "user input message must not be persisted when its ID allocation failed")
}

// TestSessionEventIDGenerator_FailClosedOnError 验证：generator 返回 error 时
// runner 同样 fail closed，错误被包装并向上抛出。
func TestSessionEventIDGenerator_FailClosedOnError(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	genErr := errors.New("custom generator failure")
	gen := func(_ context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message != nil && e.Message.Role == schema.User {
			return "", genErr
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}
	agent := &runnerSessionAgent{name: "fail-closed-err-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "fail-closed-err-session",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	iter := runner.Query(ctx, "trigger")
	var errs []error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			errs = append(errs, ev.Err)
		}
	}
	require.NotEmpty(t, errs, "expected error event when generator returns error")
	var sawWrapped bool
	for _, err := range errs {
		if errors.Is(err, genErr) {
			sawWrapped = true
			break
		}
	}
	require.True(t, sawWrapped, "expected generator error to propagate via errors.Is, got %v", errs)

	userMsgs := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage && se.Message != nil && se.Message.Role == schema.User
	})
	assert.Empty(t, userMsgs, "user input message must not be persisted on generator error")
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
		SessionStore:    store,
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

func TestRunnerSessionCheckpointTurnIDRoundTrip(t *testing.T) {
	data, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		SessionID:    "s",
		CheckPointID: "cp",
		TurnID:       "turn-1",
		Payload:      []byte("opaque"),
	})
	require.NoError(t, err)

	decoded, err := decodeRunnerSessionCheckpoint(data)
	require.NoError(t, err)
	assert.Equal(t, "turn-1", decoded.TurnID)

	type oldRunnerSessionCheckpoint struct {
		SessionID    string
		CheckPointID string
		Payload      []byte
	}
	legacyPayload, err := encodeGob(&oldRunnerSessionCheckpoint{
		SessionID:    "s",
		CheckPointID: "cp",
		Payload:      []byte("legacy"),
	})
	require.NoError(t, err)
	legacy, err := decodeRunnerSessionCheckpoint(legacyPayload)
	require.NoError(t, err)
	assert.Empty(t, legacy.TurnID)
	assert.Equal(t, []byte("legacy"), legacy.Payload)
}

func TestAttack_RunClosesSessionHandleWhenCheckpointDecodeFails(t *testing.T) {
	ctx := context.Background()
	store := &publicSessionHelperStore{sessionHelperStore: newSessionHelperStore()}
	service := store
	sessionID := "checkpoint-decode-failure-closes-handle"
	cpKey := sessionRunnerCheckpointID(sessionID)
	require.NoError(t, store.Set(ctx, cpKey, []byte("not a runner checkpoint")))

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerSessionAgent{name: "checkpoint-decode-fail-agent"},
		SessionID:       sessionID,
		SessionStore:    service,
		CheckPointStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			SessionAcquireTimeout: time.Millisecond,
		},
	})
	iter := runner.Query(ctx, "first")
	var firstErrs []error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			firstErrs = append(firstErrs, ev.Err)
		}
	}
	require.NotEmpty(t, firstErrs)
	assert.ErrorContains(t, firstErrs[0], "failed to decode session checkpoint")

	require.NoError(t, store.Delete(ctx, cpKey))
	iter = runner.Query(ctx, "second")
	var secondErrs []error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			secondErrs = append(secondErrs, ev.Err)
		}
	}
	require.Empty(t, secondErrs, "session handle must be released after checkpoint decode failure")
}

func TestRunnerSessionModeDeleteCheckpointFailureIsReported(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	persister := newSessionEventPersister[*schema.Message](
		ctx,
		store,
		"delete-fail-session",
	)
	checkPointID := "delete-fail-checkpoint"
	store.deleteErr = errors.New("delete failed")

	res := &sessionTurnResult[*schema.Message]{
		persister: persister,
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
}

func TestModelContextEvent_JSONLikeRoundTrip(t *testing.T) {
	modelCtx := &ModelContextEvent{
		ToolInfos: []*schema.ToolInfo{
			{
				Name:        "lookup",
				Desc:        "lookup tool",
				ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{"q": {Type: schema.String}}),
			},
		},
	}

	se := &SessionEvent[*schema.Message]{Kind: SessionEventModelContext, ModelContext: modelCtx}
	data, err := encodeSessionEvent(withTestEventID(se))
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.ModelContext)
	require.Len(t, decoded.ModelContext.ToolInfos, 1)
	assert.Equal(t, "lookup", decoded.ModelContext.ToolInfos[0].Name)
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
		SessionStore:    store,
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

func TestRunnerSessionStreamingRefAllocatesMissingEventID(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	agent := &streamingSessionAgent{
		release: make(chan struct{}),
		variant: &SessionEventVariant[*schema.Message]{
			MessageStreamRef: &MessageStreamRef{Kind: SessionEventMessage},
		},
	}
	release := func() {
		select {
		case <-agent.release:
		default:
			close(agent.release)
		}
	}
	defer release()

	const businessID = "stream-business-id"
	var sawStreamDraft bool
	gen := func(ctx context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message == nil {
			sawStreamDraft = true
			assert.False(t, e.Timestamp.IsZero())
			return businessID, nil
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       "streaming-ref-session",
		SessionStore:    store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
	})

	iter := runner.Query(ctx, "start", WithTimelineEvents())
	var event *AgentEvent
	for {
		ev, ok := iter.Next()
		require.True(t, ok)
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.IsStreaming {
			event = ev
			break
		}
	}
	require.NotNil(t, event.SessionEventVariant)
	ref := event.SessionEventVariant.MessageStreamRef
	require.NotNil(t, ref)
	assert.Equal(t, businessID, ref.EventID)
	assert.NotEmpty(t, ref.TurnID)
	assert.Equal(t, SessionEventMessage, ref.Kind)
	assert.False(t, ref.Timestamp.IsZero())

	msg, err := event.Output.MessageOutput.MessageStream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "partial", msg.Content)
	release()
	drainSessionEvents(t, iter)

	require.True(t, sawStreamDraft)
	messages := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage && se.Message != nil && se.Message.Content == "partial"
	})
	require.Len(t, messages, 1)
	assert.Equal(t, businessID, messages[0].EventID)
	assert.Equal(t, ref.TurnID, messages[0].TurnID)
	assert.Equal(t, ref.Timestamp, messages[0].Timestamp)
}

func TestRunnerSessionPersistsIncompleteStreamingMessageBeforeCheckpoint(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name      string
		streamErr error
	}{
		{name: "stream canceled", streamErr: ErrStreamCanceled},
		{name: "will retry", streamErr: &WillRetryError{ErrStr: "retry", RetryAttempt: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newSessionHelperStore()
			agent := &erroredStreamingInterruptAgent{streamErr: tt.streamErr}
			checkpointID := "errored-stream-checkpoint-" + strings.ReplaceAll(tt.name, " ", "-")
			runner := NewRunner(ctx, RunnerConfig{
				Agent:           agent,
				EnableStreaming: true,
				SessionID:       "errored-stream-session-" + tt.name,
				SessionStore:    store,
				CheckPointStore: store,
			})

			iter := runner.Query(ctx, "start", WithCheckPointID(checkpointID))
			var sawInterrupt bool
			var sawStreamErr bool
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				require.NoError(t, event.Err)
				if event.Output != nil && event.Output.MessageOutput != nil &&
					event.Output.MessageOutput.IsStreaming {
					for {
						_, err := event.Output.MessageOutput.MessageStream.Recv()
						if err == nil {
							continue
						}
						require.NotEqual(t, io.EOF, err)
						sawStreamErr = true
						break
					}
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					sawInterrupt = true
				}
			}

			require.True(t, sawStreamErr)
			require.True(t, sawInterrupt)
			_, exists, err := store.Get(ctx, checkpointID)
			require.NoError(t, err)
			require.True(t, exists, "checkpoint should still be saved after incomplete message stream")

			persistedPartialMessages := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
				return se.Kind == SessionEventMessage &&
					se.Message != nil &&
					se.Message.Role == schema.Assistant &&
					se.Message.Content == "partial"
			})
			assert.Empty(t, persistedPartialMessages)
			incompleteMessages := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
				return se.Kind == SessionEventMessageStreamIncomplete
			})
			require.Len(t, incompleteMessages, 1)
			require.NotNil(t, incompleteMessages[0].MessageStreamIncomplete)
			assert.Equal(t, "partial", incompleteMessages[0].MessageStreamIncomplete.Message.Content)
			assert.Contains(t, incompleteMessages[0].MessageStreamIncomplete.Error, tt.streamErr.Error())
		})
	}
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
	}()
	return iter
}

type runnerCheckpointSanitizeAgent struct{}

func (a *runnerCheckpointSanitizeAgent) Name(_ context.Context) string {
	return "CheckpointSanitizeAgent"
}

func (a *runnerCheckpointSanitizeAgent) Description(_ context.Context) string {
	return "session checkpoint sanitizer test agent"
}

func (a *runnerCheckpointSanitizeAgent) Run(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(&AgentEvent{
			AgentName: "CheckpointSanitizeAgent",
			SessionEventVariant: &SessionEventVariant[*schema.Message]{
				Event: &SessionEvent[*schema.Message]{
					EventID: "checkpoint-session-only",
					Kind:    SessionEventSessionStatusRunning,
					Lifecycle: &LifecycleEvent{
						State: SessionRunStateRunning,
					},
				},
			},
		})
		gen.Send(&AgentEvent{
			AgentName: "CheckpointSanitizeAgent",
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.AssistantMessage("mixed output", nil),
					Role:    schema.Assistant,
				},
			},
			SessionEventVariant: &SessionEventVariant[*schema.Message]{
				Event: &SessionEvent[*schema.Message]{
					EventID: "checkpoint-output",
					Kind:    SessionEventMessage,
					Message: schema.AssistantMessage("mixed output", nil),
				},
			},
		})
		gen.Send(Interrupt(ctx, "confirm?"))
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
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("done", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "flush-fail-session",
		SessionStore: store,
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
	assert.Contains(t, lastErr.Error(), "disk full")
}

func TestRunnerSessionSyncModeBlocksDeliveryUntilAppendCompletes(t *testing.T) {
	ctx := context.Background()
	store := newBlockingAppendStore()
	agent := &runnerSessionAgent{
		name: "sync-block-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "sync-block-session",
		SessionStore: store,
	})

	iterCh := make(chan *AsyncIterator[*AgentEvent], 1)
	go func() {
		iterCh <- runner.Query(ctx, "trigger")
	}()
	select {
	case <-iterCh:
		t.Fatal("query returned before pre-run control append completed")
	case <-time.After(50 * time.Millisecond):
	}
	events := make(chan *AgentEvent, 1)

	select {
	case <-store.appendStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sync persistence did not start appending")
	}
	close(store.releaseAppend)
	iter := <-iterCh
	go func() {
		ev, ok := iter.Next()
		if !ok {
			events <- nil
			return
		}
		events <- ev
	}()
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
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "sync-fail-session",
		SessionStore: store,
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
	assert.Contains(t, lastErr.Error(), "sync append failed")
	assert.False(t, sawOutput, "sync mode must not deliver output after append failure")
}

func TestSessionPersister_EnqueueAfterFlush(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](ctx, store, "enqueue-after-flush")

	require.NoError(t, persister.closeAndWait())
	assert.NoError(t, persister.enqueueAsync(validTestPayload()))
	require.NoError(t, persister.closeAndWait())
	assert.Len(t, store.events, 1)
}

// TestSessionPersister_EmptyPayloadSkipped verifies enqueue silently discards
// records with empty payload.
func TestSessionPersister_EmptyPayloadSkipped(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()

	persister := newSessionEventPersister[*schema.Message](ctx, store, "empty-payload")

	assert.NoError(t, persister.enqueueAsync(nil))
	assert.NoError(t, persister.enqueueAsync(&SessionEvent[*schema.Message]{}))

	se := makeInputSessionEvent(schema.UserMessage("real"))
	se.EventID = uuid.NewString()
	require.NoError(t, persister.enqueueAsync(se))

	require.NoError(t, persister.closeAndWait())
	require.Len(t, store.events, 1, "only the real event should be persisted")
}

func TestSessionPersister_AsyncEnqueueFlushesOnClose(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	persister := newSessionEventPersister[*schema.Message](ctx, store, "async-enqueue")

	require.NoError(t, persister.enqueueAsync(validTestPayload()))
	store.mu.Lock()
	assert.Len(t, store.events, 0, "async annotations stay pending until a boundary or final flush")
	store.mu.Unlock()

	require.NoError(t, persister.closeAndWait())
	store.mu.Lock()
	assert.Len(t, store.events, 1, "closeAndWait flushes pending annotations")
	store.mu.Unlock()
}

func TestSessionPersister_CommitBoundaryFlushesPendingBatchShape(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	persister := newSessionEventPersister[*schema.Message](ctx, store, "boundary-shape")

	annotation := withTestEventID(&SessionEvent[*schema.Message]{
		Kind:      SessionEventKind(SessionEventExtensionPrefix + "annotation"),
		Extension: &SessionExtensionEvent{},
	})
	message := withTestEventID(&SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		Message: schema.AssistantMessage("durable", nil),
	})

	require.NoError(t, persister.enqueueAsync(annotation))
	require.NoError(t, persister.commitBoundary(message))
	require.NoError(t, persister.closeAndWait())

	assert.Equal(t, [][]SessionEventKind{
		{annotation.Kind},
		{SessionEventMessage},
	}, store.appendBatches)
}

func TestSessionPersister_CommitBoundaryPreservesPendingOnFlushFailure(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	persister := newSessionEventPersister[*schema.Message](ctx, store, "boundary-fail")

	annotation := withTestEventID(&SessionEvent[*schema.Message]{
		Kind:      SessionEventKind(SessionEventExtensionPrefix + "annotation"),
		Extension: &SessionExtensionEvent{},
	})
	message := withTestEventID(&SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		Message: schema.AssistantMessage("durable", nil),
	})
	require.NoError(t, persister.enqueueAsync(annotation))

	store.appendErr = errors.New("flush failed")
	err := persister.commitBoundary(message)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "flush failed")
	assert.Empty(t, store.events)
	require.Len(t, persister.pending, 1)
	assert.Equal(t, annotation.EventID, persister.pending[0].EventID)
}

func TestRunnerSessionDurableBoundaryBatchShape(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	agent := &runnerSessionAgent{name: "boundary-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "boundary-session",
		SessionStore: store,
	})

	iter := runner.Query(ctx, "hello")
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}

	assert.Equal(t, [][]SessionEventKind{
		{SessionEventSessionStatusRunning},
		{SessionEventMessage},
		{SessionEventMessage},
		{SessionEventSessionStatusIdle},
	}, store.appendBatches)
}

func TestRunnerSessionInputMessageBoundaryFailureStopsBeforeAgent(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	store.userMsgErr = errors.New("input append failed")
	agent := &runnerSessionAgent{name: "input-boundary-agent"}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "input-boundary-session",
		SessionStore: store,
	})

	iter := runner.Query(ctx, "hello")
	event, ok := iter.Next()
	require.True(t, ok)
	require.Error(t, event.Err)
	assert.Contains(t, event.Err.Error(), "input append failed")
	_, ok = iter.Next()
	assert.False(t, ok)
	assert.Empty(t, agent.inputs, "agent must not execute when input message boundary append fails")
	assert.Equal(t, [][]SessionEventKind{{SessionEventSessionStatusRunning}}, store.appendBatches)
}

func TestSessionPersister_DirectAppendNoRetryAndLatch(t *testing.T) {
	t.Run("transient failure is not retried by runner", func(t *testing.T) {
		ctx := context.Background()
		store := &transientFailStore{
			sessionHelperStore: *newSessionHelperStore(),
			failsLeft:          2,
			appendErrVal:       errors.New("transient"),
		}
		persister := newSessionEventPersister[*schema.Message](ctx, store, "no-retry")

		err := persister.commitBoundary(validTestPayload())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "transient")
		assert.Equal(t, 1, store.getAppendCalls())
	})

	t.Run("permanent failure latched", func(t *testing.T) {
		ctx := context.Background()
		store := &transientFailStore{
			sessionHelperStore: *newSessionHelperStore(),
			failsLeft:          100,
			appendErrVal:       errors.New("permanent"),
		}
		persister := newSessionEventPersister[*schema.Message](ctx, store, "latch")

		err := persister.commitBoundary(validTestPayload())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permanent")
		assert.Equal(t, 1, store.getAppendCalls())

		err = persister.enqueueAsync(validTestPayload())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "permanent")
		assert.Equal(t, 1, store.getAppendCalls(), "latched failure must prevent later appends")
		assert.Error(t, persister.closeAndWait())
	})
}

// TestModelContextEvent_GobRoundtripNilFields verifies gob roundtrip preserves nil semantics.
func TestModelContextEvent_GobRoundtripNilFields(t *testing.T) {
	se := &SessionEvent[*schema.Message]{Kind: SessionEventModelContext, ModelContext: &ModelContextEvent{}}
	encoded, err := encodeSessionEvent(withTestEventID(se))
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.ModelContext)
	assert.Nil(t, decoded.ModelContext.ToolInfos)
	assert.Nil(t, decoded.ModelContext.DeferredToolInfos)
}

func TestNormalizeSessionConfig_Variations(t *testing.T) {
	cfg := normalizeSessionConfig[*schema.Message](nil)
	assert.NotNil(t, cfg.EventIDGenerator)
	assert.Equal(t, defaultSessionAcquireTimeout, cfg.SessionAcquireTimeout)

	cfg = normalizeSessionConfig(&SessionConfig[*schema.Message]{})
	assert.NotNil(t, cfg.EventIDGenerator)
	assert.Equal(t, defaultSessionAcquireTimeout, cfg.SessionAcquireTimeout)

	customGen := func(context.Context, *SessionEvent[*schema.Message]) (string, error) {
		return "custom-id", nil
	}
	cfg = normalizeSessionConfig(&SessionConfig[*schema.Message]{
		EventIDGenerator:      customGen,
		SessionAcquireTimeout: 200 * time.Millisecond,
	})
	assert.Equal(t, 200*time.Millisecond, cfg.SessionAcquireTimeout)
	id, err := cfg.EventIDGenerator(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "custom-id", id)
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
		TurnID:  "serializer-turn",
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
	assert.Equal(t, se.TurnID, decoded.TurnID)
	assert.Equal(t, se.Kind, decoded.Kind)
}

func TestSessionEvent_OldPayloadWithoutTurnIDDecodesEmpty(t *testing.T) {
	rawJSON := []byte(`{
		"event_id": "evt-no-turn",
		"kind": "message",
		"message": {"role": "user", "content": "legacy"}
	}`)

	event, err := decodeSessionEventWithSerializer[*schema.Message](rawJSON, nil)
	require.NoError(t, err)
	require.NotNil(t, event)
	assert.Equal(t, "evt-no-turn", event.EventID)
	assert.Empty(t, event.TurnID)
}

// --- New tests covering the design doc ---

func TestSessionEvent_HumanReadableRoundTrip(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		msg := schema.UserMessage("hello")
		EnsureMessageID(msg)
		se := &SessionEvent[*schema.Message]{TurnID: "turn-message", Message: msg}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		assert.Equal(t, "turn-message", decoded.TurnID)
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
		ev := &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{Message: schema.AssistantMessage("hi", nil), Role: schema.Assistant},
			},
		}
		stripped := stripSessionEventFields(ev)
		require.NotNil(t, stripped)
		assert.Equal(t, "hi", stripped.Output.MessageOutput.Message.Content)
	})

	t.Run("SessionEvent-only event drops to nil", func(t *testing.T) {
		ev := &AgentEvent{
			SessionEventVariant: &SessionEventVariant[*schema.Message]{
				Event: &SessionEvent[*schema.Message]{
					Kind:         SessionEventModelContext,
					ModelContext: &ModelContextEvent{},
				},
			},
		}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})

	t.Run("message mutation SessionEvent-only event drops to nil", func(t *testing.T) {
		msgs := []*schema.Message{schema.UserMessage("x")}
		ev := &AgentEvent{
			SessionEventVariant: &SessionEventVariant[*schema.Message]{
				Event: &SessionEvent[*schema.Message]{
					Kind:             SessionEventMessagesReplaced,
					MessagesReplaced: &msgs,
				},
			},
		}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})

	t.Run("Err with SessionEvent keeps Err", func(t *testing.T) {
		ev := &AgentEvent{
			Err: errors.New("visible"),
			SessionEventVariant: &SessionEventVariant[*schema.Message]{
				SessionID: "child-1",
				Event: &SessionEvent[*schema.Message]{
					Kind:         SessionEventModelContext,
					ModelContext: &ModelContextEvent{},
				},
			},
		}
		stripped := stripSessionEventFields(ev)
		require.NotNil(t, stripped)
		assert.Nil(t, stripped.SessionEventVariant)
		assert.EqualError(t, stripped.Err, "visible")
	})

	t.Run("SessionEventVariant with SessionID alone is stripped", func(t *testing.T) {
		ev := &AgentEvent{SessionEventVariant: &SessionEventVariant[*schema.Message]{SessionID: "child-1"}}
		stripped := stripSessionEventFields(ev)
		assert.Nil(t, stripped)
	})
}

func TestSessionEventTimestamp(t *testing.T) {
	ts := time.Date(2026, 5, 22, 10, 2, 0, 0, time.UTC)
	msg := schema.AssistantMessage("hi", nil)
	EnsureMessageID(msg)
	event := &AgentEvent{
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{Message: msg, Role: schema.Assistant},
		},
		SessionEventVariant: &SessionEventVariant[*schema.Message]{
			Event: &SessionEvent[*schema.Message]{
				EventID:   uuid.NewString(),
				Timestamp: ts,
				Kind:      SessionEventMessage,
				Message:   msg,
			},
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
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// Turn 2: input "Q2" + output "A2"
	q2 := schema.UserMessage("Q2")
	EnsureMessageID(q2)
	a2 := schema.AssistantMessage("A2", nil)
	EnsureMessageID(a2)
	for _, m := range []*schema.Message{q2, a2} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
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
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))

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
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	// Boundary: summary of all messages.
	summary := schema.UserMessage("summary")
	EnsureMessageID(summary)
	repl := []*schema.Message{summary}
	se := withTestEventID(&SessionEvent[*schema.Message]{MessagesReplaced: &repl})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	// Post-boundary events.
	post := schema.AssistantMessage("post", nil)
	EnsureMessageID(post)
	se = withTestEventID(&SessionEvent[*schema.Message]{Message: post})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))

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
			ToEventID:                 "turn-end-1",
			PreviousHeadCommitEventID: "turn-end-2",
		},
	}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)

	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.Rollback)
	assert.Equal(t, SessionEventRollback, decoded.Kind)
	assert.Equal(t, "turn-end-1", decoded.Rollback.ToEventID)
	assert.Equal(t, "turn-end-2", decoded.Rollback.PreviousHeadCommitEventID)
}

func TestAttack_RollbackSessionUsesConfiguredEventIDGenerator(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "rollback-event-id-generator"
	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "Q1", "A1")
	appendCommittedTestTurn(t, ctx, store, sid, "turn-2", "Q2", "A2")

	require.NoError(t, RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-1",
		WithRollbackEventIDGenerator(testSequentialEventIDGenerator("attack-rollback-")),
	))

	rollbackEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventRollback
	})
	require.Len(t, rollbackEvents, 1)
	assert.Equal(t, "attack-rollback-1", rollbackEvents[0].EventID)
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
		WithRollbackSessionCheckPointStore[*schema.Message](store),
		WithRollbackSessionExpectedHeadEventID[*schema.Message]("turn-2"),
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

	rollbackEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventRollback
	})
	require.Len(t, rollbackEvents, 1)
	require.NotNil(t, rollbackEvents[0].Rollback)
	assert.Equal(t, t1.EventID, rollbackEvents[0].Rollback.ToEventID)
	assert.Equal(t, t2.EventID, rollbackEvents[0].Rollback.PreviousHeadCommitEventID)
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
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil)},
		},
	}
	firstRunner := NewRunner(ctx, RunnerConfig{
		Agent:        firstAgent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, firstRunner.Query(ctx, "first"))
	firstCommittedIdleEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return isCommittedIdleEvent(se)
	})
	require.Len(t, firstCommittedIdleEvents, 1)
	firstBoundaryEventID := firstCommittedIdleEvents[0].EventID

	secondAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil), schema.UserMessage("second"), schema.AssistantMessage("answer2", nil)},
		},
	}
	secondRunner := NewRunner(ctx, RunnerConfig{
		Agent:        secondAgent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, secondRunner.Query(ctx, "second"))

	require.NoError(t, RollbackSession[*schema.Message](ctx, store, sid, firstBoundaryEventID))

	thirdAgent := &runnerSessionAgent{
		name: "runner-session-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil), schema.UserMessage("third"), schema.AssistantMessage("answer3", nil)},
		},
	}
	thirdRunner := NewRunner(ctx, RunnerConfig{
		Agent:        thirdAgent,
		SessionID:    sid,
		SessionStore: store,
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
		EventID: "pending-event",
		Kind:    SessionEventMessage,
		Message: testMessageWithID("pending", schema.User),
	})

	err := RollbackSession[*schema.Message](ctx, store, sid, "pending-event")
	require.ErrorIs(t, err, ErrInvalidRollbackTarget)

	err = RollbackSession[*schema.Message](ctx, store, sid, "missing")
	require.ErrorIs(t, err, ErrRollbackTargetNotFound)

	err = RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-1",
		WithRollbackSessionExpectedHeadEventID[*schema.Message]("stale-head"),
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
		WithRollbackSessionExpectedHeadEventID[*schema.Message]("turn-2"),
	))
	err = RollbackSession[*schema.Message](
		ctx,
		store,
		sid,
		"turn-2",
		WithRollbackSessionExpectedHeadEventID[*schema.Message]("turn-2"),
	)
	require.ErrorIs(t, err, ErrRollbackTargetInactive)
}

func TestReconstructRollbackMalformedRecordsFailClosed(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "rollback-malformed"

	msg := appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		Message: testMessageWithID("Q1", schema.User),
	})
	appendCommittedTestTurn(t, ctx, store, sid, "turn-1", "A1")

	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind: SessionEventRollback,
		Rollback: &SessionRollbackEvent{
			ToEventID: msg.EventID,
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
		},
	})
	_, err = reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.ErrorIs(t, err, ErrRollbackTargetInactive)
}

// TestRunnerSessionReconstructsFromEventLog: Delete testTurnState from store,
// next turn should reconstruct from events.
func TestRunnerSessionReconstructsFromEventLog(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "reconstruct-session"

	firstAgent := &runnerSessionAgent{
		name: "ra",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("first"), schema.AssistantMessage("answer1", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        firstAgent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "first"))

	// Verify context-commit events were captured: caller input + assistant output + turn-end.
	commitEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage || isCommittedIdleEvent(se)
	})
	require.Len(t, commitEvents, 3, "input event + assistant event + turn-end event should be in event log")

	// Capture the prepared session state before agent runs.
	capturedAgent := &runnerSessionAgent{
		name: "ra",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{},
		},
	}
	runner = NewRunner(ctx, RunnerConfig{
		Agent:        capturedAgent,
		SessionID:    sid,
		SessionStore: store,
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
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("answer", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "user-question"))

	// Single-turn run: 1 user input event + 1 assistant output event + 1 idle commit event,
	// plus non-context lifecycle timeline records.
	commitEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage || isCommittedIdleEvent(se)
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

func (s *recordingHelperStore) AppendEventsForSession(ctx context.Context, sid string, events []*SessionEvent[*schema.Message]) error {
	s.mu.Lock()
	if s.sessionHelperStore.appendErr != nil {
		err := s.sessionHelperStore.appendErr
		s.mu.Unlock()
		return err
	}
	s.calls = append(s.calls, "append")
	s.mu.Unlock()
	return s.sessionHelperStore.AppendEventsForSession(ctx, sid, events)
}

func (s *recordingHelperStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, sessionID, events)
}

func (s *recordingHelperStore) openSession(_ context.Context, req *openSessionRequest) (*openSessionResult[*schema.Message], error) {
	sessionID := ""
	if req != nil {
		sessionID = req.sessionID
	}
	return &openSessionResult[*schema.Message]{handle: &legacyMessageTestHandle{store: s, sessionID: sessionID}}, nil
}

func (s *recordingHelperStore) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, "", events)
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
	store.sessionHelperStore.kindErr = map[SessionEventKind]error{
		SessionEventInterrupt: errors.New("simulated append failure"),
	}

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

func TestRunnerSessionInterruptCheckpointTailIsFinalIdle(t *testing.T) {
	ctx := context.Background()
	store := newRecordingHelperStore()
	sid := "interrupt-tail"

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerInterruptAgent{},
		CheckPointStore: store,
		SessionID:       sid,
		SessionStore:    store,
	})
	drainSessionEvents(t, runner.Query(ctx, "hi"))

	cpKey := sessionRunnerCheckpointID(sid)
	raw, ok := store.checkpoints[cpKey]
	require.True(t, ok, "expected interrupt checkpoint to be saved")
	cp, err := decodeRunnerSessionCheckpoint(raw)
	require.NoError(t, err)

	store.sessionHelperStore.mu.Lock()
	require.NotEmpty(t, store.events)
	tail := store.events[len(store.events)-1]
	store.sessionHelperStore.mu.Unlock()
	assert.Equal(t, SessionEventSessionStatusIdle, tail.Kind)

	_, runCtx, _, err := runnerLoadCheckPointBytes(ctx, cp.Payload)
	require.NoError(t, err)
	require.NotNil(t, runCtx)
	require.NotNil(t, runCtx.Session)
	for _, event := range runCtx.Session.Events {
		require.NotNil(t, event.AgentEvent)
		assert.Nil(t, event.SessionEventVariant)
	}
}

func TestRunnerSessionCheckpointPayloadStripsSessionEvents(t *testing.T) {
	ctx := context.Background()
	store := newRecordingHelperStore()
	sid := "checkpoint-strip-session-events"

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerCheckpointSanitizeAgent{},
		CheckPointStore: store,
		SessionID:       sid,
		SessionStore:    store,
	})
	iter := runner.Query(ctx, "hi", WithTimelineEvents())
	var liveSessionEventIDs []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEventVariant != nil && event.SessionEventVariant.Event != nil {
			liveSessionEventIDs = append(liveSessionEventIDs, event.SessionEventVariant.Event.EventID)
		}
	}
	assert.Contains(t, liveSessionEventIDs, "checkpoint-session-only")
	assert.Contains(t, liveSessionEventIDs, "checkpoint-output")

	cpKey := sessionRunnerCheckpointID(sid)
	raw, ok := store.checkpoints[cpKey]
	require.True(t, ok, "expected interrupt checkpoint to be saved")
	cp, err := decodeRunnerSessionCheckpoint(raw)
	require.NoError(t, err)

	_, runCtx, _, err := runnerLoadCheckPointBytes(ctx, cp.Payload)
	require.NoError(t, err)
	require.NotNil(t, runCtx)
	require.NotNil(t, runCtx.Session)

	var foundOutput bool
	for _, event := range runCtx.Session.Events {
		require.NotNil(t, event.AgentEvent)
		assert.Nil(t, event.SessionEventVariant)
		assert.True(t, event.Output != nil || event.Action != nil || event.Err != nil)
		if event.Output != nil &&
			event.Output.MessageOutput != nil &&
			event.Output.MessageOutput.Message != nil &&
			event.Output.MessageOutput.Message.Content == "mixed output" {
			foundOutput = true
		}
	}
	assert.True(t, foundOutput)

	var persistedKinds []SessionEventKind
	store.sessionHelperStore.mu.Lock()
	for _, event := range store.events {
		persistedKinds = append(persistedKinds, event.Kind)
	}
	store.sessionHelperStore.mu.Unlock()
	assert.Contains(t, persistedKinds, SessionEventSessionStatusRunning)
	assert.Contains(t, persistedKinds, SessionEventMessage)
}

func TestRunnerSessionAgentInterruptBoundaryFailureNotExposed(t *testing.T) {
	ctx := context.Background()
	store := newRecordingHelperStore()
	store.sessionHelperStore.kindErr = map[SessionEventKind]error{
		SessionEventInterrupt: errors.New("agent interrupt append failed"),
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           &runnerInterruptAgent{},
		CheckPointStore: store,
		SessionID:       "interrupt-not-exposed",
		SessionStore:    store,
	})

	iter := runner.Query(ctx, "hi", WithTimelineEvents())
	var kinds []SessionEventKind
	var errs []error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errs = append(errs, event.Err)
		}
		if event.SessionEventVariant != nil && event.SessionEventVariant.Event != nil {
			kinds = append(kinds, event.SessionEventVariant.Event.Kind)
		}
	}
	require.NotEmpty(t, errs)
	assert.NotContains(t, kinds, SessionEventInterrupt)

	cpKey := sessionRunnerCheckpointID("interrupt-not-exposed")
	_, existed := store.checkpoints[cpKey]
	assert.False(t, existed, "checkpoint must not be saved after interrupt boundary append failure")
}

func TestRunnerSessionInterruptPersistErrorSurfacesWithoutCheckpoint(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	store.kindErr = map[SessionEventKind]error{
		SessionEventInterrupt: errors.New("agent interrupt append failed"),
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        &runnerInterruptAgent{},
		SessionID:    "interrupt-no-checkpoint",
		SessionStore: store,
	})

	iter := runner.Query(ctx, "hi")
	var errs []error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errs = append(errs, event.Err)
		}
	}
	require.NotEmpty(t, errs)
	assert.ErrorContains(t, errs[len(errs)-1], "failed to persist session events")
}

// TestSessionPersister_EnqueueAfterAppendError verifies that once AppendEvents
// has failed, subsequent enqueue calls return that error rather than silently
// succeeding.
func TestSessionPersister_EnqueueAfterAppendError(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	store.appendErr = errors.New("append failed")

	p := newSessionEventPersister[*schema.Message](ctx, store, "sid")

	require.NoError(t, p.enqueueAsync(validTestPayload()))
	require.Error(t, p.closeAndWait())
	require.Error(t, p.getErr(), "persister must record the AppendEvents failure")

	err := p.enqueueAsync(validTestPayload())
	require.Error(t, err, "enqueue after persist failure must return an error")
	assert.Contains(t, err.Error(), "append failed")

	for i := 0; i < 4; i++ {
		err = p.enqueueAsync(validTestPayload())
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

func (s *transientFailStore) AppendEventsForSession(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	s.retryMu.Lock()
	s.appendCalls++
	if s.failsLeft > 0 {
		s.failsLeft--
		s.retryMu.Unlock()
		return s.appendErrVal
	}
	s.retryMu.Unlock()
	return s.sessionHelperStore.AppendEventsForSession(ctx, sessionID, events)
}

func (s *transientFailStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, sessionID, events)
}

func (s *transientFailStore) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return s.AppendEventsForSession(ctx, "", events)
}

func (s *transientFailStore) getAppendCalls() int {
	s.retryMu.Lock()
	defer s.retryMu.Unlock()
	return s.appendCalls
}

func TestSessionPersister_FlushDoesNotRetryTransientFailure(t *testing.T) {
	ctx := context.Background()
	store := &transientFailStore{
		sessionHelperStore: *newSessionHelperStore(),
		failsLeft:          2,
		appendErrVal:       errors.New("transient"),
	}

	p := newSessionEventPersister[*schema.Message](ctx, store, "sid")

	require.NoError(t, p.enqueueAsync(validTestPayload()))

	err := p.closeAndWait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "transient")
	assert.Equal(t, 1, store.getAppendCalls())
	store.sessionHelperStore.mu.Lock()
	assert.Empty(t, store.sessionHelperStore.events)
	store.sessionHelperStore.mu.Unlock()
}

func TestSessionPersister_FlushPermanentFailureLatched(t *testing.T) {
	ctx := context.Background()
	store := &transientFailStore{
		sessionHelperStore: *newSessionHelperStore(),
		failsLeft:          100, // always fail
		appendErrVal:       errors.New("permanent"),
	}

	p := newSessionEventPersister[*schema.Message](ctx, store, "sid")

	require.NoError(t, p.enqueueAsync(validTestPayload()))

	err := p.closeAndWait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "permanent")
	assert.Equal(t, 1, store.getAppendCalls())
}

func TestSessionPersister_FlushContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := &transientFailStore{
		sessionHelperStore: *newSessionHelperStore(),
		failsLeft:          100, // always fail
		appendErrVal:       errors.New("failing"),
	}

	p := newSessionEventPersister[*schema.Message](ctx, store, "sid")

	require.NoError(t, p.enqueueAsync(validTestPayload()))

	cancel()

	err := p.closeAndWait()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failing")
	assert.Equal(t, 1, store.getAppendCalls())
}

// --- Attack tests for reconstruction across committed and in-flight events ---

// TestAttack_ReconstructionIncludesInterruptedTailOnResume verifies that
// reconstructSessionState keeps interrupted-tail messages during replay.
func TestAttack_ReconstructionIncludesInterruptedTailOnResume(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "inflight-recovery"

	committedMsg := schema.UserMessage("committed-msg")
	EnsureMessageID(committedMsg)

	// A committed turn: TurnStart (lifecycle running) + Message + committed idle.
	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusRunning, Lifecycle: &LifecycleEvent{State: SessionRunStateRunning}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: committedMsg},
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusIdle, Lifecycle: &LifecycleEvent{State: SessionRunStateIdle, StopReason: &StopReason{Type: "end_turn"}}},
	}

	// An interrupted run: a Message event with no committed idle.
	interruptedMsg := schema.AssistantMessage("interrupted-msg", nil)
	EnsureMessageID(interruptedMsg)
	events = append(events, &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(), Kind: SessionEventMessage, Message: interruptedMsg,
	})

	for _, se := range events {
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	// State should have messages from committed turn (1) + interrupted turn (1).
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "committed-msg", result.state.Messages[0].Content)
	assert.Equal(t, "interrupted-msg", result.state.Messages[1].Content)
}

func TestAttack_ReconstructionWithoutCommittedIdle(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "inflight-no-committed-turn"

	msg := schema.UserMessage("first-turn")
	EnsureMessageID(msg)
	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg},
		{EventID: uuid.NewString(), Kind: SessionEventInterrupt, Interrupt: &InterruptEvent{
			Contexts: []*InterruptContext{
				{
					InterruptID: "agent:InterruptAgent",
					Info:        "approval_needed",
				},
			},
		}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 1)
	assert.Equal(t, "first-turn", result.state.Messages[0].Content)
}

// TestAttack_OldRunIDFieldIgnoredOnDeserialization verifies that a JSON payload
// containing a legacy "run_id" field is deserialized without error.
func TestAttack_OldRunIDFieldIgnoredOnDeserialization(t *testing.T) {
	rawJSON := []byte(`{
		"event_id": "evt-legacy",
		"run_id": "old-run",
		"kind": "message",
		"message": {"role": "user", "content": "hello from legacy"}
	}`)

	event, err := decodeSessionEventWithSerializer[*schema.Message](rawJSON, nil)
	require.NoError(t, err, "deserialization must not fail on unknown run_id field")
	require.NotNil(t, event)
	assert.Equal(t, "evt-legacy", event.EventID)
	require.NotNil(t, event.Message)
	assert.Equal(t, "hello from legacy", event.Message.Content)
}

func TestAttack_ResumeAfterInterruptedRunWritesSessionEvents(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "resume-after-interrupt"

	// First, run a normal turn that completes (provides a committed idle baseline).
	normalAgent := &runnerSessionAgent{
		name: "normal-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("first answer", nil)},
		},
	}
	firstRunner := NewRunner(ctx, RunnerConfig{
		Agent:           normalAgent,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})
	drainSessionEvents(t, firstRunner.Query(ctx, "first question"))

	// Now run a query that interrupts (building on the committed session).
	agent := &runnerInterruptAgent{}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})

	iter := runner.Query(ctx, "trigger interrupt")
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	interruptedEvents := decodeStoredSessionEvents(t, store.events)
	require.NotEmpty(t, interruptedEvents)
	var interruptedTurnID string
	for i := len(interruptedEvents) - 1; i >= 0; i-- {
		if interruptedEvents[i].Kind == SessionEventSessionStatusRunning {
			interruptedTurnID = interruptedEvents[i].TurnID
			assert.Equal(t, interruptedEvents[i].EventID, interruptedTurnID)
			break
		}
	}
	require.NotEmpty(t, interruptedTurnID)
	cp, existed, err := loadRunnerSessionCheckpoint(ctx, store, sessionRunnerCheckpointID(sessionID))
	require.NoError(t, err)
	require.True(t, existed)
	require.Equal(t, interruptedTurnID, cp.TurnID)

	eventsBeforeResume := len(store.events)

	resumeIter, err := runner.Resume(ctx, "")
	require.NoError(t, err)
	for {
		_, ok := resumeIter.Next()
		if !ok {
			break
		}
	}

	require.Greater(t, len(store.events), eventsBeforeResume)
	resumeEvents := filterStoredSessionEvents(t, store.events[eventsBeforeResume:], func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventKind(SessionEventExtensionPrefix+"resume.request_started") ||
			se.Kind == SessionEventSessionStatusIdle
	})
	require.NotEmpty(t, resumeEvents)
	for _, event := range resumeEvents {
		assert.Equal(t, interruptedTurnID, event.TurnID, "kind=%s", event.Kind)
	}
}

func TestAttack_FreshRunIgnoresInterruptedSuffixMetadata(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sessionID := "fresh-run-ignores-inflight"

	// First, run a normal turn that completes (provides a committed idle baseline).
	normalAgent := &runnerSessionAgent{
		name: "normal-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("baseline", nil)},
		},
	}
	baselineRunner := NewRunner(ctx, RunnerConfig{
		Agent:           normalAgent,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})
	drainSessionEvents(t, baselineRunner.Query(ctx, "baseline"))

	// Now run a query that interrupts.
	agent := &runnerInterruptAgent{}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})

	iter := runner.Query(ctx, "trigger interrupt")
	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	// Instead of resuming, create a NEW runner on the same session and run a new query (fresh Run).
	eventsBeforeFresh := len(store.events)
	freshAgent := &runnerSessionAgent{
		name: "fresh-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("fresh answer", nil)},
		},
	}
	freshRunner := NewRunner(ctx, RunnerConfig{
		Agent:           freshAgent,
		SessionID:       sessionID,
		SessionStore:    store,
		CheckPointStore: store,
	})
	drainSessionEvents(t, freshRunner.Query(ctx, "new question"))

	require.Greater(t, len(store.events), eventsBeforeFresh)
	require.Len(t, freshAgent.inputs, 1)
	require.Len(t, freshAgent.inputs[0], 4)
	assert.Equal(t, "baseline", freshAgent.inputs[0][0].Content)
	assert.Equal(t, "ok", freshAgent.inputs[0][1].Content)
	assert.Equal(t, "trigger interrupt", freshAgent.inputs[0][2].Content)
	assert.Equal(t, "new question", freshAgent.inputs[0][3].Content)
}

// sessionStreamingAgent emits a single streaming assistant output. Used to
// verify the runner's stream-copy/persist path.
type sessionStreamingAgent struct {
	chunks    []*schema.Message
	streamErr error
	turnEnd   *testTurnState[*schema.Message]
	role      schema.RoleType
	tool      string
	preEvent  *SessionEvent[*schema.Message]
}

func (a *sessionStreamingAgent) Name(_ context.Context) string        { return "session-stream-agent" }
func (a *sessionStreamingAgent) Description(_ context.Context) string { return "stream test agent" }
func (a *sessionStreamingAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		if a.preEvent != nil {
			gen.Send(&AgentEvent{
				AgentName: "session-stream-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: a.preEvent,
				},
			})
		}
		stream := testStreamReaderWithTerminalError(a.chunks, a.streamErr)
		role := a.role
		if role == "" {
			role = schema.Assistant
		}
		mv := &MessageVariant{IsStreaming: true, MessageStream: stream, Role: role, ToolName: a.tool}
		gen.Send(&AgentEvent{AgentName: "session-stream-agent", Output: &AgentOutput{MessageOutput: mv}})
	}()
	return iter
}

type agenticSessionStreamingAgent struct {
	chunks    []*schema.AgenticMessage
	streamErr error
	turnEnd   *testTurnState[*schema.AgenticMessage]
}

func (a *agenticSessionStreamingAgent) Name(_ context.Context) string {
	return "agentic-session-stream-agent"
}

func (a *agenticSessionStreamingAgent) Description(_ context.Context) string {
	return "agentic stream test agent"
}

func (a *agenticSessionStreamingAgent) Run(
	_ context.Context,
	_ *TypedAgentInput[*schema.AgenticMessage],
	_ ...AgentRunOption,
) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer gen.Close()
		gen.Send(&TypedAgentEvent[*schema.AgenticMessage]{
			AgentName: "agentic-session-stream-agent",
			Output: &TypedAgentOutput[*schema.AgenticMessage]{
				MessageOutput: &TypedMessageVariant[*schema.AgenticMessage]{
					IsStreaming:   true,
					MessageStream: testStreamReaderWithTerminalError(a.chunks, a.streamErr),
					AgenticRole:   schema.AgenticRoleTypeUser,
				},
			},
		})
	}()
	return iter
}

func testStreamReaderWithTerminalError[T any](chunks []T, streamErr error) *schema.StreamReader[T] {
	if streamErr == nil {
		return schema.StreamReaderFromArray(chunks)
	}
	reader, writer := schema.Pipe[T](len(chunks) + 1)
	go func() {
		defer writer.Close()
		for _, chunk := range chunks {
			writer.Send(chunk, nil)
		}
		var zero T
		writer.Send(zero, streamErr)
	}()
	return reader
}

// TestStreamPersistence_CopyAndConcat verifies that streaming assistant outputs
// produce a durable, fully-concatenated SessionEvent.Message AND remain consumable
// from the live stream. Regression test for the pre-evaluation bug where
// stream-only events (Message==nil, MessageStream!=nil) skipped persistence.
func TestStreamPersistence_CopyAndConcat(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "stream-session"

	chunks := []*schema.Message{
		schema.AssistantMessage("hello ", nil),
		schema.AssistantMessage("world", nil),
	}
	agent := &sessionStreamingAgent{
		chunks: chunks,
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello world", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	// Drain live events and verify the live stream still produces the concatenated content.
	iter := runner.Query(ctx, "q")
	var liveContent string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
			msg, err := schema.ConcatMessageStream(ev.Output.MessageOutput.MessageStream)
			require.NoError(t, err)
			liveContent = msg.Content
		}
	}
	assert.Equal(t, "hello world", liveContent, "live stream must yield concatenated content")

	// Find the persisted streaming event in the log: exactly one assistant output should be persisted.
	var assistantMessages []*schema.Message
	for _, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Message != nil && se.Message.Role == schema.Assistant {
			assistantMessages = append(assistantMessages, se.Message)
		}
	}
	require.Len(t, assistantMessages, 1, "streaming assistant output must be persisted exactly once")
	assert.Equal(t, "hello world", assistantMessages[0].Content,
		"persisted stream message must be the fully concatenated content")
}

func TestStreamPersistence_IncompleteStreamPrefixPersisted(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	streamErr := errors.New("model stream failed")
	agent := &sessionStreamingAgent{
		chunks: []*schema.Message{
			schema.AssistantMessage("hello ", nil),
			schema.AssistantMessage("partial", nil),
		},
		streamErr: streamErr,
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       "incomplete-stream-session",
		SessionStore:    store,
	})

	drainErroredStreamEvents(t, runner.Query(ctx, "q"), streamErr)

	events := decodeStoredSessionEvents(t, store.events)
	var incomplete []*SessionEvent[*schema.Message]
	var normalFailedMessages []*SessionEvent[*schema.Message]
	for _, se := range events {
		if se.Kind == SessionEventMessageStreamIncomplete {
			incomplete = append(incomplete, se)
		}
		if se.Kind == SessionEventMessage && se.Message != nil &&
			se.Message.Role == schema.Assistant && se.Message.Content == "hello partial" {
			normalFailedMessages = append(normalFailedMessages, se)
		}
	}
	require.Len(t, incomplete, 1)
	require.NotNil(t, incomplete[0].MessageStreamIncomplete)
	require.NotNil(t, incomplete[0].MessageStreamIncomplete.Message)
	assert.Equal(t, "hello partial", incomplete[0].MessageStreamIncomplete.Message.Content)
	assert.Contains(t, incomplete[0].MessageStreamIncomplete.Error, streamErr.Error())
	assert.Empty(t, normalFailedMessages, "failed stream prefix must not be persisted as a normal context message")
}

func TestAttack_IncompleteStreamPrefixCarriesDurableMetadata(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "attack-incomplete-metadata"
	streamErr := errors.New("stream transport failed")
	agent := &sessionStreamingAgent{
		chunks: []*schema.Message{
			schema.AssistantMessage("prefix", nil),
		},
		streamErr: streamErr,
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	drainErroredStreamEvents(t, runner.Query(ctx, "q"), streamErr)

	events := decodeStoredSessionEvents(t, store.events)
	var incomplete *SessionEvent[*schema.Message]
	var idle *SessionEvent[*schema.Message]
	for _, se := range events {
		switch se.Kind {
		case SessionEventMessageStreamIncomplete:
			incomplete = se
		case SessionEventSessionStatusIdle:
			idle = se
		}
	}

	require.NotNil(t, incomplete)
	require.NotNil(t, idle)
	assert.NotEmpty(t, incomplete.EventID)
	assert.True(t, incomplete.Timestamp.Before(idle.Timestamp) || incomplete.Timestamp.Equal(idle.Timestamp))
	assert.Equal(t, "prefix", incomplete.MessageStreamIncomplete.Message.Content)
	assert.Contains(t, incomplete.MessageStreamIncomplete.Error, streamErr.Error())
}

func TestAttack_IncompleteStreamPersistsAllTerminalErrors(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name      string
		streamErr error
	}{
		{name: "canceled", streamErr: ErrStreamCanceled},
		{name: "will retry", streamErr: &WillRetryError{ErrStr: "retry", RetryAttempt: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newSessionHelperStore()
			runner := NewRunner(ctx, RunnerConfig{
				Agent: &sessionStreamingAgent{
					chunks:    []*schema.Message{schema.AssistantMessage("transient", nil)},
					streamErr: tt.streamErr,
				},
				EnableStreaming: true,
				SessionID:       "attack-nondurable-" + tt.name,
				SessionStore:    store,
			})

			drainErroredStreamEvents(t, runner.Query(ctx, "q"), tt.streamErr)

			incomplete := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
				return se.Kind == SessionEventMessageStreamIncomplete
			})
			require.Len(t, incomplete, 1)
			require.NotNil(t, incomplete[0].MessageStreamIncomplete)
			assert.Equal(t, "transient", incomplete[0].MessageStreamIncomplete.Message.Content)
			assert.Contains(t, incomplete[0].MessageStreamIncomplete.Error, tt.streamErr.Error())
		})
	}
}

func drainErroredStreamEvents(t *testing.T, iter *AsyncIterator[*AgentEvent], streamErr error) {
	t.Helper()
	var sawStreamErr bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output == nil || ev.Output.MessageOutput == nil ||
			!ev.Output.MessageOutput.IsStreaming || ev.Output.MessageOutput.MessageStream == nil {
			continue
		}
		for {
			_, err := ev.Output.MessageOutput.MessageStream.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				assert.ErrorContains(t, err, streamErr.Error())
				sawStreamErr = true
				break
			}
		}
	}
	require.True(t, sawStreamErr, "live stream must surface the terminal stream error")
}

func TestStreamPersistence_IncompleteStreamExcludedFromReconstruction(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "incomplete-reconstruct-session"
	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind:    SessionEventMessage,
		Message: schema.UserMessage("q"),
	})
	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind: SessionEventMessageStreamIncomplete,
		MessageStreamIncomplete: &MessageStreamIncompleteEvent[*schema.Message]{
			Message: schema.AssistantMessage("partial", nil),
			Error:   "model stream failed",
		},
	})
	appendTestSessionEvent(t, ctx, store, sid, &SessionEvent[*schema.Message]{
		Kind: SessionEventSessionStatusIdle,
		Lifecycle: &LifecycleEvent{
			State:      SessionRunStateIdle,
			StopReason: &StopReason{Type: "end_turn"},
		},
	})

	result, err := reconstructSessionState[*schema.Message](ctx, mustOpenTestSession[*schema.Message](t, ctx, store, sid), sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 1)
	assert.Equal(t, "q", result.state.Messages[0].Content)
}

func TestMessageStreamIncompleteEvent_RoundTripAndValidation(t *testing.T) {
	event := withTestEventID(&SessionEvent[*schema.Message]{
		Kind: SessionEventMessageStreamIncomplete,
		MessageStreamIncomplete: &MessageStreamIncompleteEvent[*schema.Message]{
			Message: schema.AssistantMessage("partial", nil),
			Error:   "model stream failed",
		},
	})
	encoded, err := encodeSessionEvent(event)
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](encoded)
	require.NoError(t, err)
	require.NotNil(t, decoded.MessageStreamIncomplete)
	assert.Equal(t, SessionEventMessageStreamIncomplete, decoded.Kind)
	assert.Equal(t, "partial", decoded.MessageStreamIncomplete.Message.Content)
	assert.Equal(t, "model stream failed", decoded.MessageStreamIncomplete.Error)
	assert.False(t, isContextSessionEvent(decoded))

	_, err = encodeSessionEvent(withTestEventID(&SessionEvent[*schema.Message]{
		Kind:                    SessionEventMessageStreamIncomplete,
		MessageStreamIncomplete: &MessageStreamIncompleteEvent[*schema.Message]{Error: "missing message"},
	}))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "message stream incomplete event")
}

func TestStreamPersistence_StreamingLiveBeforeMaterializedBoundary(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "sync-stream-session"

	agent := &sessionStreamingAgent{
		chunks: []*schema.Message{
			schema.AssistantMessage("hello ", nil),
			schema.AssistantMessage("sync", nil),
		},
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello sync", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	iter := runner.Query(ctx, "q")
	var observed *MessageVariant
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			observed = ev.Output.MessageOutput
		}
	}

	require.NotNil(t, observed)
	assert.True(t, observed.IsStreaming, "streaming output remains live while persistence materializes a copy")
	msg, err := observed.GetMessage()
	require.NoError(t, err)
	assert.Equal(t, "hello sync", msg.Content)

	var stored bool
	store.mu.Lock()
	snapshot := append([]storedSessionEvent{}, store.events...)
	store.mu.Unlock()
	for _, ep := range snapshot {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Message != nil && se.Message.Role == schema.Assistant && se.Message.Content == "hello sync" {
			stored = true
		}
	}
	assert.True(t, stored, "materialized stream message must be persisted by finalization")
}

func TestStreamPersistence_PendingAnnotationFlushesBeforeMaterializedBoundary(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	annotationKind := SessionEventKind(SessionEventExtensionPrefix + "stream.annotation")
	agent := &sessionStreamingAgent{
		preEvent: &SessionEvent[*schema.Message]{
			Kind:      annotationKind,
			Extension: &SessionExtensionEvent{},
		},
		chunks: []*schema.Message{
			schema.AssistantMessage("hello ", nil),
			schema.AssistantMessage("stream", nil),
		},
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello stream", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       "stream-annotation-boundary",
		SessionStore:    store,
	})

	drainSessionEvents(t, runner.Query(ctx, "q"))

	assert.Equal(t, [][]SessionEventKind{
		{SessionEventSessionStatusRunning},
		{SessionEventMessage},
		{annotationKind},
		{SessionEventMessage},
		{SessionEventSessionStatusIdle},
	}, store.appendBatches)
}

func TestStreamPersistence_ToolResultStreamingLiveBeforeMaterializedBoundary(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "sync-tool-stream-session"

	agent := &sessionStreamingAgent{
		chunks: []*schema.Message{
			schema.ToolMessage("tool ", "tc-1", schema.WithToolName("t1")),
			schema.ToolMessage("result", "tc-1", schema.WithToolName("t1")),
		},
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.ToolMessage("tool result", "tc-1", schema.WithToolName("t1"))},
		},
		role: schema.Tool,
		tool: "t1",
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	iter := runner.Query(ctx, "q")
	var observed *MessageVariant
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil {
			observed = ev.Output.MessageOutput
		}
	}

	require.NotNil(t, observed)
	assert.True(t, observed.IsStreaming)
	msg, err := observed.GetMessage()
	require.NoError(t, err)
	assert.Equal(t, schema.Tool, msg.Role)
	assert.Equal(t, "tool result", msg.Content)

	var stored bool
	store.mu.Lock()
	snapshot := append([]storedSessionEvent{}, store.events...)
	store.mu.Unlock()
	for _, ep := range snapshot {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Message != nil && se.Message.Role == schema.Tool && se.Message.Content == "tool result" {
			stored = true
		}
	}
	assert.True(t, stored, "materialized tool-result stream must be persisted by finalization")
}

func TestStreamPersistence_AgenticToolResultChunksConcat(t *testing.T) {
	ctx := context.Background()
	store := newAgenticSessionHelperStore()
	sid := "agentic-tool-stream-session"

	agent := &agenticSessionStreamingAgent{
		chunks: []*schema.AgenticMessage{
			agenticToolResultMessage("call_1", "execute", "first\n"),
			agenticToolResultMessage("call_1", "execute", "second\n"),
		},
		turnEnd: &testTurnState[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("q"),
				agenticToolResultMessage("call_1", "execute", "first\nsecond\n"),
			},
		},
	}

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("q")})
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
			for {
				_, err := ev.Output.MessageOutput.MessageStream.Recv()
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
			}
		}
	}

	var stored *SessionEvent[*schema.AgenticMessage]
	res, err := store.LoadEventsForSession(ctx, sid, nil)
	require.NoError(t, err)
	for _, se := range res.Events {
		if se.Kind == SessionEventMessage && se.Message != nil &&
			len(se.Message.ContentBlocks) == 1 &&
			se.Message.ContentBlocks[0].Type == schema.ContentBlockTypeFunctionToolResult {
			stored = se
			break
		}
	}

	require.NotNil(t, stored)
	require.NotNil(t, stored.Message)
	require.Len(t, stored.Message.ContentBlocks, 1)
	ftr := stored.Message.ContentBlocks[0].FunctionToolResult
	require.NotNil(t, ftr)
	assert.Equal(t, "call_1", ftr.CallID)
	assert.Equal(t, "execute", ftr.Name)
	require.Len(t, ftr.Content, 1)
	assert.Equal(t, "first\nsecond\n", ftr.Content[0].Text.Text)
	assert.Nil(t, stored.Message.ContentBlocks[0].StreamingMeta)
}

func TestStreamPersistence_AgenticToolResultChunksWithStreamingMeta(t *testing.T) {
	ctx := context.Background()
	store := newAgenticSessionHelperStore()
	sid := "agentic-tool-stream-meta-session"

	first := agenticToolResultMessage("call_1", "execute", "first\n")
	second := agenticToolResultMessage("call_1", "execute", "second\n")
	first.ContentBlocks[0].StreamingMeta = &schema.StreamingMeta{Index: 0}
	second.ContentBlocks[0].StreamingMeta = &schema.StreamingMeta{Index: 0}

	agent := &agenticSessionStreamingAgent{
		chunks: []*schema.AgenticMessage{first, second},
		turnEnd: &testTurnState[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{
				schema.UserAgenticMessage("q"),
				agenticToolResultMessage("call_1", "execute", "first\nsecond\n"),
			},
		},
	}

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("q")})
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
			for {
				_, err := ev.Output.MessageOutput.MessageStream.Recv()
				if err == io.EOF {
					break
				}
				require.NoError(t, err)
			}
		}
	}

	var stored *schema.AgenticMessage
	res, err := store.LoadEventsForSession(ctx, sid, nil)
	require.NoError(t, err)
	for _, se := range res.Events {
		if se.Kind == SessionEventMessage && se.Message != nil &&
			len(se.Message.ContentBlocks) == 1 &&
			se.Message.ContentBlocks[0].Type == schema.ContentBlockTypeFunctionToolResult {
			stored = se.Message
			break
		}
	}

	require.NotNil(t, stored)
	require.Len(t, stored.ContentBlocks, 1)
	block := stored.ContentBlocks[0]
	assert.Nil(t, block.StreamingMeta)
	require.NotNil(t, block.FunctionToolResult)
	assert.Equal(t, "call_1", block.FunctionToolResult.CallID)
	assert.Equal(t, "execute", block.FunctionToolResult.Name)
	require.Len(t, block.FunctionToolResult.Content, 1)
	assert.Equal(t, "first\nsecond\n", block.FunctionToolResult.Content[0].Text.Text)
}

func TestStreamPersistence_AgenticIncompleteStreamPrefixPersisted(t *testing.T) {
	ctx := context.Background()
	store := newAgenticSessionHelperStore()
	sid := "agentic-incomplete-stream-session"
	streamErr := errors.New("agentic model stream failed")
	chunk := agenticToolResultMessage("call_1", "execute", "partial\n")
	agent := &agenticSessionStreamingAgent{
		chunks:    []*schema.AgenticMessage{chunk},
		streamErr: streamErr,
	}
	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	iter := runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage("q")})
	var sawStreamErr bool
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
			for {
				_, err := ev.Output.MessageOutput.MessageStream.Recv()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					assert.ErrorContains(t, err, streamErr.Error())
					sawStreamErr = true
					break
				}
			}
		}
	}
	require.True(t, sawStreamErr)

	res, err := store.LoadEventsForSession(ctx, sid, nil)
	require.NoError(t, err)
	var incomplete []*SessionEvent[*schema.AgenticMessage]
	var normalToolMessages []*SessionEvent[*schema.AgenticMessage]
	for _, se := range res.Events {
		if se.Kind == SessionEventMessageStreamIncomplete {
			incomplete = append(incomplete, se)
		}
		if se.Kind == SessionEventMessage && se.Message != nil &&
			len(se.Message.ContentBlocks) == 1 &&
			se.Message.ContentBlocks[0].Type == schema.ContentBlockTypeFunctionToolResult {
			normalToolMessages = append(normalToolMessages, se)
		}
	}
	require.Len(t, incomplete, 1)
	require.NotNil(t, incomplete[0].MessageStreamIncomplete)
	prefix := incomplete[0].MessageStreamIncomplete.Message
	require.NotNil(t, prefix)
	require.Len(t, prefix.ContentBlocks, 1)
	require.NotNil(t, prefix.ContentBlocks[0].FunctionToolResult)
	require.Len(t, prefix.ContentBlocks[0].FunctionToolResult.Content, 1)
	assert.Equal(t, "partial\n", prefix.ContentBlocks[0].FunctionToolResult.Content[0].Text.Text)
	assert.Contains(t, incomplete[0].MessageStreamIncomplete.Error, streamErr.Error())
	assert.Empty(t, normalToolMessages)

	reconstructed, err := reconstructSessionState[*schema.AgenticMessage](ctx, mustOpenTestSession[*schema.AgenticMessage](t, ctx, store, sid), sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, reconstructed)
	require.NotNil(t, reconstructed.state)
	require.Len(t, reconstructed.state.Messages, 1)
	assert.Equal(t, schema.AgenticRoleTypeUser, reconstructed.state.Messages[0].Role)
}

func agenticToolResultMessage(callID, name, text string) *schema.AgenticMessage {
	return &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			{
				Type: schema.ContentBlockTypeFunctionToolResult,
				FunctionToolResult: &schema.FunctionToolResult{
					CallID: callID,
					Name:   name,
					Content: []*schema.FunctionToolResultContentBlock{
						{
							Type: schema.FunctionToolResultContentBlockTypeText,
							Text: &schema.UserInputText{Text: text},
						},
					},
				},
			},
		},
	}
}

// TestStreamPersistence_GetMessageError_NotEnqueued verifies that a stream
// materialization error sets persistErr (failing the turn commit) and does NOT
// enqueue a corrupt SessionEvent.
func TestStreamPersistence_GetMessageError_NotEnqueued(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "stream-err-session"

	// Build a stream that errors on Recv.
	streamReader, streamWriter := schema.Pipe[*schema.Message](2)
	streamWriter.Send(schema.AssistantMessage("partial ", nil), nil)
	streamWriter.Send(nil, errors.New("simulated stream failure"))
	streamWriter.Close()

	agent := &streamingAgentRaw{
		stream: streamReader,
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
	})

	iter := runner.Query(ctx, "trigger")
	var lastErr error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			lastErr = ev.Err
		}
		// Drain any live stream so the goroutine doesn't leak.
		if ev.Output != nil && ev.Output.MessageOutput != nil &&
			ev.Output.MessageOutput.IsStreaming && ev.Output.MessageOutput.MessageStream != nil {
			_, _ = schema.ConcatMessageStream(ev.Output.MessageOutput.MessageStream)
		}
	}
	require.NoError(t, lastErr, "stream materialization errors should drop only the message event")

	// Verify no assistant SessionEvent is in the log.
	for _, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Message != nil {
			assert.NotEqual(t, schema.Assistant, se.Message.Role,
				"failed stream must not produce a persisted assistant event")
		}
	}
}

func TestStreamPersistence_GetMessageErrorSurfacesAfterLiveStreaming(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "sync-stream-err-session"

	streamReader, streamWriter := schema.Pipe[*schema.Message](2)
	streamWriter.Send(schema.AssistantMessage("partial ", nil), nil)
	streamWriter.Send(nil, errors.New("simulated stream failure"))
	streamWriter.Close()

	agent := &streamingAgentRaw{
		stream: streamReader,
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionStore:    store,
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
	require.NoError(t, lastErr)
	assert.True(t, sawOutput, "streaming output may already be live before materialization fails")

	for _, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.Message != nil {
			assert.NotEqual(t, schema.Assistant, se.Message.Role,
				"failed sync stream must not produce a persisted assistant event")
		}
	}
}

// streamingAgentRaw lets the test inject an arbitrary stream reader (including
// one that emits errors).
type streamingAgentRaw struct {
	stream  *schema.StreamReader[*schema.Message]
	turnEnd *testTurnState[*schema.Message]
}

func (a *streamingAgentRaw) Name(_ context.Context) string        { return "streaming-raw" }
func (a *streamingAgentRaw) Description(_ context.Context) string { return "stream-error test agent" }
func (a *streamingAgentRaw) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		mv := &MessageVariant{IsStreaming: true, MessageStream: a.stream, Role: schema.Assistant}
		gen.Send(&AgentEvent{AgentName: "streaming-raw", Output: &AgentOutput{MessageOutput: mv}})
	}()
	return iter
}

// TestSessionEvent_NilVsEmptyMessagesReplaced verifies that nil and empty
// MessagesReplaced are distinguishable after round-trip through the serializer.
func TestSessionEvent_NilVsEmptyMessagesReplaced(t *testing.T) {
	t.Run("nil MessagesReplaced", func(t *testing.T) {
		msg := schema.UserMessage("just a message")
		EnsureMessageID(msg)
		se := &SessionEvent[*schema.Message]{Message: msg}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		assert.Nil(t, decoded.MessagesReplaced, "absent MessagesReplaced must decode as nil pointer")
		require.NotNil(t, decoded.Message)
	})

	t.Run("empty MessagesReplaced", func(t *testing.T) {
		empty := []*schema.Message{}
		se := &SessionEvent[*schema.Message]{MessagesReplaced: &empty}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.MessagesReplaced, "&[]M{} must decode as non-nil pointer")
		assert.Empty(t, *decoded.MessagesReplaced)
	})
}

// TestRunnerInputEvents_MixedRoles verifies that callers can pass system + user
// messages and both are persisted with their original roles.
func TestRunnerInputEvents_MixedRoles(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "mixed-roles"

	agent := &runnerSessionAgent{
		name: "mr-agent",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})

	systemMsg := schema.SystemMessage("system instruction")
	userMsg := schema.UserMessage("hello")
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{systemMsg, userMsg}))

	// Find the first two message events: they must be the input messages with
	// preserved roles. Lifecycle timeline records may surround them.
	messageEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventMessage
	})
	require.GreaterOrEqual(t, len(messageEvents), 2)
	first := messageEvents[0]
	require.NotNil(t, first.Message)
	assert.Equal(t, schema.System, first.Message.Role)
	assert.Equal(t, "system instruction", first.Message.Content)

	second := messageEvents[1]
	require.NotNil(t, second.Message)
	assert.Equal(t, schema.User, second.Message.Role)
	assert.Equal(t, "hello", second.Message.Content)
}

func TestCustomAgentNormalCloseCommitsIdle(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "turn-end-only"

	agent := &turnEndOnlyAgent{}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "input"))

	var sawCommit bool
	for _, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if isCommittedIdleEvent(se) {
			sawCommit = true
		}
	}
	assert.True(t, sawCommit)
}

type turnEndOnlyAgent struct{}

func (a *turnEndOnlyAgent) Name(_ context.Context) string        { return "turn-end-only" }
func (a *turnEndOnlyAgent) Description(_ context.Context) string { return "" }
func (a *turnEndOnlyAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
	}()
	return iter
}

// TestTailReplay_PartialTurnWithoutCommittedIdle verifies that events appended
// after the last committed idle are replayed on reconstruction.
func TestTailReplay_PartialTurnWithoutCommittedIdle(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "tail-replay"

	// Phase 1: a normal completed turn (messages + committed idle event).
	a1 := schema.UserMessage("Q1")
	EnsureMessageID(a1)
	r1 := schema.AssistantMessage("A1", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{a1, r1} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	committedIdleSE := withTestCommittedIdle[*schema.Message]("turn-1")
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{committedIdleSE}))

	// Phase 2: simulate a partial second turn where events were appended but
	// no committed idle was persisted (interrupted).
	a2 := schema.UserMessage("Q2")
	EnsureMessageID(a2)
	r2 := schema.AssistantMessage("A2", nil)
	EnsureMessageID(r2)
	for _, m := range []*schema.Message{a2, r2} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	// Boot: prepareRunnerSessionRun reconstructs durable context through the log tail.
	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, nil, sid, store, nil)
	require.NoError(t, err)
	require.True(t, state.enabled)
	require.Len(t, state.latestState.Messages, 4)
	assert.Equal(t, "Q1", state.latestState.Messages[0].Content)
	assert.Equal(t, "A1", state.latestState.Messages[1].Content)
	assert.Equal(t, "Q2", state.latestState.Messages[2].Content)
	assert.Equal(t, "A2", state.latestState.Messages[3].Content)
}

// TestTailReplay_NoTailEvents verifies that the fast path is not disturbed when
// no events follow the snapshot.
func TestTailReplay_NoTailEvents(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "no-tail"

	q := schema.UserMessage("Q")
	EnsureMessageID(q)
	se := withTestEventID(&SessionEvent[*schema.Message]{Message: q})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	turnEndSE := withTestCommittedIdle[*schema.Message]("turn-1")
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, nil, sid, store, nil)
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 1)
	assert.Equal(t, "Q", state.latestState.Messages[0].Content)
}

// TestTailReplay_EmptySnapshotCursor verifies cursor-based replay correctly
// handles a snapshot that committed an empty Messages array — the cursor still
// excludes pre-boundary events.
func TestTailReplay_EmptySnapshotCursor(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "empty-snapshot"

	// Pre-boundary events.
	for i := 0; i < 3; i++ {
		m := schema.UserMessage("pre")
		EnsureMessageID(m)
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// MessagesReplaced boundary with empty slice — supersedes pre-boundary events.
	empty := []*schema.Message{}
	boundarySE := withTestEventID(&SessionEvent[*schema.Message]{MessagesReplaced: &empty})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{boundarySE}))

	// Post-boundary events.
	postMsg := schema.UserMessage("post")
	EnsureMessageID(postMsg)
	se := withTestEventID(&SessionEvent[*schema.Message]{Message: postMsg})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, nil, sid, store, nil)
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 1)
	assert.Equal(t, "post", state.latestState.Messages[0].Content)
}

type agenticSessionHelperStore struct {
	mu         sync.Mutex
	events     []storedSessionEvent
	eventIDIdx map[string]int
}

func newAgenticSessionHelperStore() *agenticSessionHelperStore {
	return &agenticSessionHelperStore{eventIDIdx: make(map[string]int)}
}

func (s *agenticSessionHelperStore) AppendEvents(ctx context.Context, sessionID string, events []*SessionEvent[*schema.AgenticMessage]) error {
	return s.AppendEventsForSession(ctx, sessionID, events)
}

func (s *agenticSessionHelperStore) AppendEventsForSession(_ context.Context, _ string, events []*SessionEvent[*schema.AgenticMessage]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, event := range events {
		if event == nil || event.EventID == "" {
			return ErrInvalidEventID
		}
		if err := NormalizeSessionEventKind(event); err != nil {
			return err
		}
		if _, ok := s.eventIDIdx[event.EventID]; ok {
			continue
		}
		data, err := encodeSessionEvent(event)
		if err != nil {
			return err
		}
		s.events = append(s.events, storedSessionEvent{EventID: event.EventID, Kind: event.Kind, Data: data})
		s.eventIDIdx[event.EventID] = len(s.events) - 1
	}
	return nil
}

func (s *agenticSessionHelperStore) LoadEvents(ctx context.Context, sessionID string, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.AgenticMessage], error) {
	return s.LoadEventsForSession(ctx, sessionID, req)
}

func (s *agenticSessionHelperStore) LoadEventsForSession(_ context.Context, _ string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.AgenticMessage], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if opts == nil {
		opts = &LoadSessionEventsRequest{}
	}
	start, end, step := 0, len(s.events), 1
	if opts.After != "" {
		pos, ok := s.eventIDIdx[opts.After]
		if !ok {
			return nil, ErrEventIDOutOfRange
		}
		if opts.Reverse {
			start, end, step = pos-1, -1, -1
		} else {
			start = pos + 1
		}
	} else if opts.Reverse {
		start, end, step = len(s.events)-1, -1, -1
	}
	kindSet := buildTestKindSet(opts.Kinds)
	var out []*SessionEvent[*schema.AgenticMessage]
	for i := start; i != end; i += step {
		if i < 0 || i >= len(s.events) {
			break
		}
		rec := s.events[i]
		if kindSet != nil {
			if _, ok := kindSet[rec.Kind]; !ok {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			break
		}
		event, err := decodeSessionEvent[*schema.AgenticMessage](rec.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return &LoadSessionEventsResult[*schema.AgenticMessage]{Events: out}, nil
}

func (s *agenticSessionHelperStore) openSession(_ context.Context, req *openSessionRequest) (*openSessionResult[*schema.AgenticMessage], error) {
	sessionID := ""
	if req != nil {
		sessionID = req.sessionID
	}
	return &openSessionResult[*schema.AgenticMessage]{
		handle: &agenticTestSessionHandle{store: s, sessionID: sessionID},
	}, nil
}

type agenticTestSessionHandle struct {
	store     *agenticSessionHelperStore
	sessionID string
}

func (h *agenticTestSessionHandle) loadEvents(ctx context.Context, req *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.AgenticMessage], error) {
	if req == nil {
		req = &LoadSessionEventsRequest{}
	}
	return h.store.LoadEventsForSession(ctx, h.sessionID, req)
}

func (h *agenticTestSessionHandle) appendEvents(ctx context.Context, events []*SessionEvent[*schema.AgenticMessage]) error {
	return h.store.AppendEventsForSession(ctx, h.sessionID, events)
}

func (h *agenticTestSessionHandle) close(context.Context) error { return nil }

// TestPartialInterrupted_ThenNewRun verifies that when a turn is interrupted
// after some events have been appended (but before the committed idle marker), a new
// Run with NO CheckPointStore (i.e. session-only mode) recovers the in-flight
// events via tail replay rather than treating the session as fresh.
//
// This test does not use CheckPointStore — Runner skips pending checkpoints
// on fresh Run, so checkpoint presence would not block regardless.
func TestPartialInterrupted_ThenNewRun(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "partial-interrupted"

	// Phase 1: simulate a normal completed turn.
	q1 := schema.UserMessage("first")
	EnsureMessageID(q1)
	r1 := schema.AssistantMessage("answer1", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{q1, r1} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	committedIdleSE := withTestCommittedIdle[*schema.Message]("turn-1")
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{committedIdleSE}))

	// Phase 2: simulate an interrupted turn with events appended but no committed idle.
	q2 := schema.UserMessage("partial")
	EnsureMessageID(q2)
	for _, m := range []*schema.Message{q2} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	// Phase 3: new Run (no CheckPointStore; Runner skips pending checkpoints on fresh Run).
	captured := &runnerSessionAgent{
		name: "ra",
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        captured,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "second"))

	// Fresh Run includes durable partial-turn context because Session
	// reconstruction replays context events through the log tail.
	require.Len(t, captured.inputs, 1)
	contents := []string{}
	for _, m := range captured.inputs[0] {
		contents = append(contents, m.Content)
	}
	assert.Equal(t, []string{"first", "answer1", "partial", "second"}, contents)
}

// TestSessionEvent_StreamCopyConcat_ByteIdentical verifies the round-trip of a
// streamed-then-persisted SessionEvent matches what the live consumer sees.
func TestSessionEvent_StreamCopyConcat_ByteIdentical(t *testing.T) {
	chunks := []*schema.Message{
		schema.AssistantMessage("foo ", nil),
		schema.AssistantMessage("bar ", nil),
		schema.AssistantMessage("baz", nil),
	}
	stream := schema.StreamReaderFromArray(chunks)

	// Mimic the runner's logic: copy, materialize one side, leave the other live.
	copies := stream.Copy(2)
	persistCopy := &TypedMessageVariant[*schema.Message]{IsStreaming: true, MessageStream: copies[0]}
	persistedMsg, err := persistCopy.GetMessage()
	require.NoError(t, err)
	require.NotNil(t, persistedMsg)

	se := &SessionEvent[*schema.Message]{Message: persistedMsg}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.Message)
	assert.Equal(t, "foo bar baz", decoded.Message.Content)

	// The live copy should yield the same concatenated content.
	liveMsg, err := schema.ConcatMessageStream(copies[1])
	require.NoError(t, err)
	assert.Equal(t, decoded.Message.Content, liveMsg.Content)
}

// TestExplicitCheckpointResume_WithSessionMode verifies that when a caller passes
// an explicit checkpoint ID alongside a configured SessionID/SessionStore[*schema.Message], the
// resume path still loads reconstructed session state.
func TestExplicitCheckpointResume_WithSessionMode(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "explicit-cp-session"

	// Seed the session store with events and a committed idle marker.
	prior := &testTurnState[*schema.Message]{
		Messages: []*schema.Message{schema.UserMessage("seed"), schema.AssistantMessage("seed-ans", nil)},
	}
	// Seed session events (messages + committed idle).
	for _, m := range prior.Messages {
		EnsureMessageID(m)
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	committedIdleSE := withTestCommittedIdle[*schema.Message]("turn-1")
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{committedIdleSE}))

	// Seed an arbitrary checkpoint ID with a runner-session-checkpoint wrapper
	// so runnerLoadCheckPointForSession can decode it.
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		Payload: []byte("opaque"),
	})
	require.NoError(t, err)
	explicitCheckpointID := "user-supplied-cp"
	require.NoError(t, store.Set(ctx, explicitCheckpointID, cpBytes))

	state, effective, err := prepareRunnerSessionResume[*schema.Message](ctx, store, sid, store, nil, explicitCheckpointID)
	require.NoError(t, err)
	require.True(t, state.enabled, "session mode must remain enabled when an explicit checkpoint ID is supplied")
	require.NotNil(t, state.latestState)
	assert.Equal(t, 2, len(state.latestState.Messages),
		"latest snapshot must be loaded for explicit-checkpoint resume in session mode")
	assert.Equal(t, explicitCheckpointID, effective,
		"caller-supplied checkpoint ID must be preserved")
}

func TestPrepareRunnerSessionResume_LegacyCheckpointUsesResumeEventTurnID(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "legacy-turn-id-resume"
	cpID := "legacy-turn-id-cp"
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		Payload: []byte("opaque"),
	})
	require.NoError(t, err)
	require.NoError(t, store.Set(ctx, cpID, cpBytes))

	state, effective, err := prepareRunnerSessionResume[*schema.Message](ctx, store, sid, store, nil, cpID)
	require.NoError(t, err)
	require.True(t, state.enabled)
	require.Equal(t, cpID, effective)
	require.Len(t, state.initialTimeline, 1)
	resumeEvent := state.initialTimeline[0]
	require.Equal(t, SessionEventKind(SessionEventExtensionPrefix+"resume.request_started"), resumeEvent.Kind)
	require.NotEmpty(t, resumeEvent.EventID)
	assert.Equal(t, resumeEvent.EventID, state.turnID)
	assert.Equal(t, resumeEvent.EventID, resumeEvent.TurnID)
}

// TestResumePath_TailReplay verifies that the resume path also performs tail
// replay (uses the same fast path as the run path).
func TestResumePath_TailReplay(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "resume-tail"

	q1 := schema.UserMessage("Q")
	EnsureMessageID(q1)
	r1 := schema.AssistantMessage("A", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{q1, r1} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	turnEndSE := withTestCommittedIdle[*schema.Message]("turn-1")
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

	// Append a tail event after the snapshot.
	tailMsg := schema.UserMessage("post-snapshot")
	EnsureMessageID(tailMsg)
	se := withTestEventID(&SessionEvent[*schema.Message]{Message: tailMsg})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	// Seed a runner session checkpoint so the resume path finds something to load.
	cpStore := newSessionHelperStore()
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		Payload: []byte("opaque"),
	})
	require.NoError(t, err)
	require.NoError(t, cpStore.Set(ctx, sessionRunnerCheckpointID(sid), cpBytes))

	state, _, err := prepareRunnerSessionResume[*schema.Message](ctx, cpStore, sid, store, nil, "")
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 3,
		"resume boot state should include durable context events through the log tail")
	assert.Equal(t, "Q", state.latestState.Messages[0].Content)
	assert.Equal(t, "A", state.latestState.Messages[1].Content)
	assert.Equal(t, "post-snapshot", state.latestState.Messages[2].Content)
}

// Ensure the io package import is used (for compile when chunks are empty).

// mutationAgent emits a sequence of caller-provided TypedAgentEvents. Used to
// verify the runner persists each session-mutation
// event variant (MessagesReplaced, MessageUpdated, MessageInserted) faithfully.
type mutationAgent struct {
	events  []*AgentEvent
	turnEnd *testTurnState[*schema.Message]
}

func (a *mutationAgent) Name(_ context.Context) string        { return "mutation-agent" }
func (a *mutationAgent) Description(_ context.Context) string { return "" }
func (a *mutationAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		for _, ev := range a.events {
			gen.Send(ev)
		}
	}()
	return iter
}

// TestRunnerPersists_MessagesReplaced verifies a MessagesReplaced event from
// any source (e.g. summarization) is persisted.
func TestRunnerPersists_MessagesReplaced(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "mr-session"

	summary := schema.AssistantMessage("summary content", nil)
	EnsureMessageID(summary)
	repl := []*schema.Message{summary}

	agent := &mutationAgent{
		events: []*AgentEvent{
			{
				AgentName: "mutation-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						Kind:             SessionEventMessagesReplaced,
						MessagesReplaced: &repl,
					},
				},
			},
		},
		turnEnd: &testTurnState[*schema.Message]{Messages: []*schema.Message{summary}},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "anything"))

	// Read events back via the store.
	res, err := store.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)

	var foundReplaced bool
	for _, se := range res.Events {
		if se.MessagesReplaced != nil {
			foundReplaced = true
			require.Len(t, *se.MessagesReplaced, 1)
			assert.Equal(t, "summary content", (*se.MessagesReplaced)[0].Content)
		}
	}
	assert.True(t, foundReplaced, "MessagesReplaced must be persisted")
}

// TestRunnerPersists_MessageUpdated_BothMessages verifies that when reduction
// emits two MessageUpdated events (one for the assistant tool-call message,
// one for the tool-result message), both reach the event log and reconstruction
// applies them correctly.
func TestRunnerPersists_MessageUpdated_BothMessages(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "mu-session"

	// Build two messages with stable IDs.
	toolCallMsg := schema.AssistantMessage("call me", nil)
	EnsureMessageID(toolCallMsg)
	toolResultMsg := schema.ToolMessage("result content", "tc-1", schema.WithToolName("t1"))
	EnsureMessageID(toolResultMsg)

	// Pretend reduction rewrites both: the assistant message's args (we just
	// reuse the same message pointer for the test, with a marker) and the tool
	// result content.
	updatedAssistant := schema.AssistantMessage("call me [cleared]", nil)
	updatedAssistant.Extra = map[string]any{"_eino_msg_id": GetMessageID(toolCallMsg), "cleared": true}
	updatedTool := schema.ToolMessage("[placeholder]", "tc-1", schema.WithToolName("t1"))
	updatedTool.Extra = map[string]any{"_eino_msg_id": GetMessageID(toolResultMsg)}

	agent := &mutationAgent{
		events: []*AgentEvent{
			{
				AgentName: "mutation-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: toolCallMsg, Role: schema.Assistant},
				},
			},
			{
				AgentName: "mutation-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: toolResultMsg, Role: schema.Tool, ToolName: "t1"},
				},
			},
			{
				AgentName: "mutation-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						Kind: SessionEventMessageUpdated,
						MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
							MessageID: GetMessageID(toolResultMsg),
							Message:   updatedTool,
						},
					},
				},
			},
			{
				AgentName: "mutation-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						Kind: SessionEventMessageUpdated,
						MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
							MessageID: GetMessageID(toolCallMsg),
							Message:   updatedAssistant,
						},
					},
				},
			},
		},
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{updatedAssistant, updatedTool},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "go"))

	res, err := store.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)

	var updates int
	for _, se := range res.Events {
		if se.MessageUpdated != nil {
			updates++
		}
	}
	assert.Equal(t, 2, updates, "both MessageUpdated events must be persisted")

	// Reconstruction must apply both updates correctly.
	result, err := reconstructSessionState[*schema.Message](ctx, mustOpenTestSession[*schema.Message](t, ctx, store, sid), sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	// Find updated content among reconstructed messages.
	var sawClearedAssistant, sawPlaceholderTool bool
	for _, m := range result.state.Messages {
		if m.Role == schema.Assistant && m.Content == "call me [cleared]" {
			sawClearedAssistant = true
		}
		if m.Role == schema.Tool && m.Content == "[placeholder]" {
			sawPlaceholderTool = true
		}
	}
	assert.True(t, sawClearedAssistant, "reconstruction must apply cleared assistant update")
	assert.True(t, sawPlaceholderTool, "reconstruction must apply placeholder tool update")
}

// TestRunnerPersists_MessageInserted_AnchorAndAppend verifies that
// MessageInserted events from middlewares (AgentsMD, ToolSearch, PatchToolCalls)
// flow through the runner, are persisted, and reconstruct correctly.
func TestRunnerPersists_MessageInserted_AnchorAndAppend(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "mi-session"

	// Anchor: the user message in the session, present from the input.
	userMsg := schema.UserMessage("hello")
	EnsureMessageID(userMsg)

	// AgentsMD-style insertion before the user message.
	agentsmdMsg := schema.UserMessage("[agentsmd content]")
	agentsmdMsg.Extra = map[string]any{"__agentsmd_content__": true}
	EnsureMessageID(agentsmdMsg)

	// PatchToolCalls-style append at end.
	patchedTool := schema.ToolMessage("[patched]", "tc-1", schema.WithToolName("t1"))
	EnsureMessageID(patchedTool)

	finalMessages := []*schema.Message{agentsmdMsg, userMsg, patchedTool}

	agent := &mutationAgent{
		events: []*AgentEvent{
			// Mimic input event flow: user message already appears in the input.
			// MessageInserted before the user message:
			{
				AgentName: "mutation-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						Kind: SessionEventMessageInserted,
						MessageInserted: &MessageInsertedEvent[*schema.Message]{
							Message:         agentsmdMsg,
							BeforeMessageID: GetMessageID(userMsg),
						},
					},
				},
			},
			// MessageInserted appended at end:
			{
				AgentName: "mutation-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						Kind: SessionEventMessageInserted,
						MessageInserted: &MessageInsertedEvent[*schema.Message]{
							Message:         patchedTool,
							BeforeMessageID: "",
						},
					},
				},
			},
		},
		turnEnd: &testTurnState[*schema.Message]{Messages: finalMessages},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})
	// We must pass the user message as input, with its existing ID already assigned,
	// so reconstruction's anchor lookup succeeds.
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{userMsg}))

	res, err := store.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)

	var inserts int
	for _, se := range res.Events {
		if se.MessageInserted != nil {
			inserts++
		}
	}
	assert.Equal(t, 2, inserts, "both MessageInserted events must be persisted")

	// Verify reconstruction applies insertions correctly.
	result, err := reconstructSessionState[*schema.Message](ctx, mustOpenTestSession[*schema.Message](t, ctx, store, sid), sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.GreaterOrEqual(t, len(result.state.Messages), 3)
	// The agentsmd message should appear before the user input.
	var idxAgentsmd, idxUser, idxPatched int
	idxAgentsmd, idxUser, idxPatched = -1, -1, -1
	for i, m := range result.state.Messages {
		switch GetMessageID(m) {
		case GetMessageID(agentsmdMsg):
			idxAgentsmd = i
		case GetMessageID(userMsg):
			idxUser = i
		case GetMessageID(patchedTool):
			idxPatched = i
		}
	}
	require.NotEqual(t, -1, idxAgentsmd)
	require.NotEqual(t, -1, idxUser)
	require.NotEqual(t, -1, idxPatched)
	assert.Less(t, idxAgentsmd, idxUser, "agentsmd must be inserted before the user message")
	assert.Greater(t, idxPatched, idxUser, "patched tool message must be appended at the end")
}

type leadingSystemTestModel[M MessageType] struct {
	response M
	inputs   [][]M
}

func (m *leadingSystemTestModel[M]) Generate(_ context.Context, input []M, _ ...model.Option) (M, error) {
	copied := append([]M{}, input...)
	m.inputs = append(m.inputs, copied)
	return m.response, nil
}

func (m *leadingSystemTestModel[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]M{msg}), nil
}

type sessionToolCallingModel struct {
	response *schema.Message
	inputs   [][]*schema.Message
}

func (m *sessionToolCallingModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message{}, input...))
	return m.response, nil
}

func (m *sessionToolCallingModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *sessionToolCallingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type modelContextExtraTool struct{}

func (modelContextExtraTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name:  "extra_tool",
		Desc:  "tool with json-normalized extra metadata",
		Extra: map[string]any{"version": 1},
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type: schema.String,
				Desc: "query",
			},
		}),
	}, nil
}

func (modelContextExtraTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "ok", nil
}

func drainAgenticSessionEvents(t *testing.T, iter *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]) {
	t.Helper()
	for {
		event, ok := iter.Next()
		if !ok {
			return
		}
		require.NoError(t, event.Err)
	}
}

func loadMessageSessionEvents(t *testing.T, ctx context.Context, store *sessionHelperStore, sid string) []*SessionEvent[*schema.Message] {
	t.Helper()
	res, err := store.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)
	return res.Events
}

func loadAgenticSessionEvents(t *testing.T, ctx context.Context, store *agenticSessionHelperStore, sid string) []*SessionEvent[*schema.AgenticMessage] {
	t.Helper()
	res, err := store.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)
	return res.Events
}

func TestRunnerNoPersist_GeneratedLeadingSystemMessage(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "no-persist-gen-system"
	model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("answer", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "gen-system-agent",
		Description: "test",
		Instruction: "system v1",
		Model:       model,
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: sid, SessionStore: store})
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{schema.UserMessage("hello")}))

	for _, event := range loadMessageSessionEvents(t, ctx, store, sid) {
		if event.Message != nil && event.Message.Role == schema.System {
			t.Fatalf("generated leading system message must not be persisted as SessionEventMessage")
		}
		if event.MessageInserted != nil && event.MessageInserted.Message.Role == schema.System {
			t.Fatalf("generated leading system message must not be persisted as SessionEventMessageInserted")
		}
		if event.MessageUpdated != nil && event.MessageUpdated.Message.Role == schema.System {
			t.Fatalf("generated leading system message must not be persisted as SessionEventMessageUpdated")
		}
	}

	require.Len(t, model.inputs, 1)
	require.GreaterOrEqual(t, len(model.inputs[0]), 2)
	assert.Equal(t, schema.System, model.inputs[0][0].Role)
	assert.Equal(t, "system v1", model.inputs[0][0].Content)

	handle := mustOpenTestSession[*schema.Message](t, ctx, store, sid)
	result, err := reconstructSessionState[*schema.Message](ctx, handle, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NoError(t, handle.close(ctx))
	for _, msg := range result.state.Messages {
		if msg.Role == schema.System {
			t.Fatalf("reconstructed state must not contain generated leading system message")
		}
	}
}

func TestRunnerNoPersist_GeneratedLeadingSystemEmptySession(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "no-persist-gen-system-empty"
	model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("answer", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "gen-system-empty-agent",
		Description: "test",
		Instruction: "system only",
		Model:       model,
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: sid, SessionStore: store})
	drainSessionEvents(t, runner.Run(ctx, nil))

	for _, event := range loadMessageSessionEvents(t, ctx, store, sid) {
		if event.Message != nil && event.Message.Role == schema.System {
			t.Fatalf("generated leading system message must not be persisted in empty session")
		}
	}
	require.Len(t, model.inputs, 1)
	require.GreaterOrEqual(t, len(model.inputs[0]), 1)
	assert.Equal(t, schema.System, model.inputs[0][0].Role)
	assert.Equal(t, "system only", model.inputs[0][0].Content)
}

func TestRunnerRecalculatesSystemMessageOnSecondRun(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "recalc-system-second-run"

	runTurn := func(instruction, user string) *leadingSystemTestModel[*schema.Message] {
		model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("answer "+user, nil)}
		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "recalc-system-agent",
			Description: "test",
			Instruction: instruction,
			Model:       model,
		})
		require.NoError(t, err)
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: sid, SessionStore: store})
		drainSessionEvents(t, runner.Run(ctx, []*schema.Message{schema.UserMessage(user)}))
		return model
	}

	m1 := runTurn("system v1", "one")
	require.Len(t, m1.inputs, 1)
	require.GreaterOrEqual(t, len(m1.inputs[0]), 2)
	assert.Equal(t, "system v1", m1.inputs[0][0].Content)
	assert.Equal(t, schema.System, m1.inputs[0][0].Role)

	m2 := runTurn("system v2", "two")
	require.Len(t, m2.inputs, 1)
	require.GreaterOrEqual(t, len(m2.inputs[0]), 3)
	assert.Equal(t, "system v2", m2.inputs[0][0].Content)
	assert.Equal(t, schema.System, m2.inputs[0][0].Role)
	assert.Equal(t, "one", m2.inputs[0][1].Content)
}

func TestRunnerNoPersist_CustomGenModelInputLeadingSystem(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "no-persist-custom-gen-system"
	model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("answer", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "custom-gen-system-agent",
		Description: "test",
		Instruction: "ignored",
		Model:       model,
		GenModelInput: func(_ context.Context, _ string, input *AgentInput) ([]*schema.Message, error) {
			system := schema.SystemMessage("custom system")
			messages := make([]*schema.Message, 0, len(input.Messages)+1)
			messages = append(messages, system)
			messages = append(messages, input.Messages...)
			return messages, nil
		},
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: sid, SessionStore: store})
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{schema.UserMessage("hello")}))

	for _, event := range loadMessageSessionEvents(t, ctx, store, sid) {
		if event.Message != nil && event.Message.Role == schema.System {
			t.Fatalf("custom GenModelInput leading system must not be persisted")
		}
		if event.MessageInserted != nil && event.MessageInserted.Message.Role == schema.System {
			t.Fatalf("custom GenModelInput leading system must not be inserted")
		}
		if event.MessageUpdated != nil && event.MessageUpdated.Message.Role == schema.System {
			t.Fatalf("custom GenModelInput leading system must not be updated")
		}
	}
	require.Len(t, model.inputs, 1)
	require.GreaterOrEqual(t, len(model.inputs[0]), 2)
	assert.Equal(t, schema.System, model.inputs[0][0].Role)
	assert.Equal(t, "custom system", model.inputs[0][0].Content)
}

func TestRunnerPreserves_CallerSuppliedLeadingSystemMessage(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "preserve-caller-system"
	model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("answer", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "caller-system-agent",
		Description: "test",
		Instruction: "",
		Model:       model,
	})
	require.NoError(t, err)

	systemMsg := schema.SystemMessage("caller system prompt")
	EnsureMessageID(systemMsg)
	userMsg := schema.UserMessage("hello")
	EnsureMessageID(userMsg)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: sid, SessionStore: store})
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{systemMsg, userMsg}))

	var systemEvents int
	for _, event := range loadMessageSessionEvents(t, ctx, store, sid) {
		if event.Message != nil && event.Message.Role == schema.System {
			systemEvents++
		}
	}
	assert.Equal(t, 1, systemEvents, "caller-supplied system message must be persisted")

	handle := mustOpenTestSession[*schema.Message](t, ctx, store, sid)
	result, err := reconstructSessionState[*schema.Message](ctx, handle, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NoError(t, handle.close(ctx))
	require.GreaterOrEqual(t, len(result.state.Messages), 2)
	assert.Equal(t, schema.System, result.state.Messages[0].Role)
	assert.Equal(t, "caller system prompt", result.state.Messages[0].Content)
}

func TestRunnerSkipsLeadingSystemEventWhenCustomGenModelInputHasNoSystem(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "leading-system-custom-none"
	model := &leadingSystemTestModel[*schema.Message]{response: schema.AssistantMessage("answer", nil)}
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "custom-no-system-agent",
		Description: "test",
		Instruction: "ignored by custom input",
		Model:       model,
		GenModelInput: func(_ context.Context, _ string, input *AgentInput) ([]*schema.Message, error) {
			return append([]*schema.Message{}, input.Messages...), nil
		},
	})
	require.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: sid, SessionStore: store})
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{schema.UserMessage("hello")}))

	for _, event := range loadMessageSessionEvents(t, ctx, store, sid) {
		switch {
		case event.Message != nil && event.Message.Role == schema.System:
			t.Fatalf("custom GenModelInput without leading system must not persist system message")
		case event.MessageInserted != nil && event.MessageInserted.Message.Role == schema.System:
			t.Fatalf("custom GenModelInput without leading system must not insert system message")
		case event.MessageUpdated != nil && event.MessageUpdated.Message.Role == schema.System:
			t.Fatalf("custom GenModelInput without leading system must not update system message")
		}
	}
	require.Len(t, model.inputs, 1)
	require.Len(t, model.inputs[0], 1)
	assert.Equal(t, schema.User, model.inputs[0][0].Role)
}

func TestRunnerNoPersist_AgenticLeadingSystemMessage(t *testing.T) {
	ctx := context.Background()
	store := newAgenticSessionHelperStore()
	sid := "no-persist-agentic-system"

	runTurn := func(instruction, user string) *leadingSystemTestModel[*schema.AgenticMessage] {
		model := &leadingSystemTestModel[*schema.AgenticMessage]{response: agenticAssistantMessage("answer " + user)}
		agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
			Name:        "agentic-system-agent",
			Description: "test",
			Instruction: instruction,
			Model:       model,
		})
		require.NoError(t, err)
		runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{
			Agent:        agent,
			SessionID:    sid,
			SessionStore: store,
		})
		drainAgenticSessionEvents(t, runner.Run(ctx, []*schema.AgenticMessage{schema.UserAgenticMessage(user)}))
		return model
	}

	m1 := runTurn("agentic system v1", "one")
	for _, event := range loadAgenticSessionEvents(t, ctx, store, sid) {
		if event.Message != nil && event.Message.Role == schema.AgenticRoleTypeSystem {
			t.Fatalf("agentic generated leading system must not be persisted as message")
		}
		if event.MessageInserted != nil && event.MessageInserted.Message.Role == schema.AgenticRoleTypeSystem {
			t.Fatalf("agentic generated leading system must not be persisted as inserted")
		}
		if event.MessageUpdated != nil && event.MessageUpdated.Message.Role == schema.AgenticRoleTypeSystem {
			t.Fatalf("agentic generated leading system must not be persisted as updated")
		}
	}
	require.Len(t, m1.inputs, 1)
	require.GreaterOrEqual(t, len(m1.inputs[0]), 2)
	assert.Equal(t, schema.AgenticRoleTypeSystem, m1.inputs[0][0].Role)

	m2 := runTurn("agentic system v2", "two")
	require.Len(t, m2.inputs, 1)
	require.GreaterOrEqual(t, len(m2.inputs[0]), 3)
	assert.Equal(t, schema.AgenticRoleTypeSystem, m2.inputs[0][0].Role)
}

func TestRunnerPersists_MessagesDeleted_Reconstructs(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "md-session"

	a := schema.UserMessage("a")
	b := schema.AssistantMessage("b", nil)
	c := schema.UserMessage("c")
	for _, msg := range []*schema.Message{a, b, c} {
		EnsureMessageID(msg)
	}

	agent := &mutationAgent{
		events: []*AgentEvent{
			{
				AgentName: "mutation-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: a, Role: schema.User},
				},
			},
			{
				AgentName: "mutation-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: b, Role: schema.Assistant},
				},
			},
			{
				AgentName: "mutation-agent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: c, Role: schema.User},
				},
			},
			{
				AgentName: "mutation-agent",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						Kind: SessionEventMessagesDeleted,
						MessagesDeleted: &MessagesDeletedEvent{
							MessageIDs: []string{GetMessageID(b)},
						},
					},
				},
			},
		},
		turnEnd: &testTurnState[*schema.Message]{Messages: []*schema.Message{a, c}},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})
	drainSessionEvents(t, runner.Run(ctx, nil))

	res, err := store.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)

	var foundDeleted bool
	for _, se := range res.Events {
		if se.MessagesDeleted != nil {
			foundDeleted = true
			assert.Equal(t, []string{GetMessageID(b)}, se.MessagesDeleted.MessageIDs)
		}
	}
	assert.True(t, foundDeleted, "MessagesDeleted must be persisted")

	result, err := reconstructSessionState[*schema.Message](ctx, mustOpenTestSession[*schema.Message](t, ctx, store, sid), sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "a", result.state.Messages[0].Content)
	assert.Equal(t, "c", result.state.Messages[1].Content)
}

func TestReconstructSessionState_MessagesDeletedMissingTargetFails(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "md-missing-target"

	a := schema.UserMessage("a")
	EnsureMessageID(a)
	msgEvent := withTestEventID(&SessionEvent[*schema.Message]{Message: a})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{msgEvent}))

	deleteEvent := withTestEventID(&SessionEvent[*schema.Message]{
		MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{"ghost-id"}},
	})
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{deleteEvent}))

	committedIdleEvent := withTestCommittedIdle[*schema.Message]("turn-1")
	require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{committedIdleEvent}))

	_, err := reconstructSessionState[*schema.Message](ctx, mustOpenTestSession[*schema.Message](t, ctx, store, sid), sid, defaultLoadPageSize)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ghost-id")
}

// TestAgentTool_ChildSessionID_FiltersFromParentLog verifies that events
// forwarded from an inner agent (via AgentTool) are tagged with the child
// SessionEvent.SessionID and are NOT persisted into the parent's session event
// log. The parent's log only contains events that belong to its own session.
func TestAgentTool_ChildSessionID_FiltersFromParentLog(t *testing.T) {
	ctx := context.Background()
	parentStore := newSessionHelperStore()
	sid := "parent-session"

	// Inner-agent forwarded event from AgentTool path. Tagging with a
	// SessionEvent.SessionID that does not match the parent session must be
	// filtered out of persistence.
	childMsg := schema.AssistantMessage("inner-agent-output", nil)
	EnsureMessageID(childMsg)
	parentMsg := schema.AssistantMessage("parent-output", nil)
	EnsureMessageID(parentMsg)

	agent := &mutationAgent{
		events: []*AgentEvent{
			// An event tagged as belonging to a different session — should not be persisted.
			{
				AgentName: "child",
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					SessionID: "agent_tool:abc-123",
					Event: &SessionEvent[*schema.Message]{
						Kind:    SessionEventMessage,
						Message: childMsg,
					},
				},
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: childMsg, Role: schema.Assistant},
				},
			},
			// The parent's own event — should be persisted.
			{
				AgentName: "parent",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: parentMsg, Role: schema.Assistant},
				},
			},
		},
		turnEnd: &testTurnState[*schema.Message]{
			Messages: []*schema.Message{parentMsg},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: parentStore,
	})
	drainSessionEvents(t, runner.Query(ctx, "go"))

	// Verify that childMsg is NOT in the parent's persistent log, but parentMsg is.
	res, err := parentStore.LoadEventsForSession(ctx, sid, &LoadSessionEventsRequest{})
	require.NoError(t, err)
	var sawChild, sawParent bool
	for _, se := range res.Events {
		if se.Message != nil {
			if GetMessageID(se.Message) == GetMessageID(childMsg) {
				sawChild = true
			}
			if GetMessageID(se.Message) == GetMessageID(parentMsg) {
				sawParent = true
			}
		}
	}
	assert.False(t, sawChild, "events tagged with a different SessionEvent.SessionID must NOT enter the parent session log")
	assert.True(t, sawParent, "parent's own events must be persisted")
}

func TestRunnerSessionTurnIDOverwritesProducerEventsAndSkipsChildSession(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "turn-id-producer"
	extensionKind := SessionEventKind(SessionEventExtensionPrefix + "producer")
	agent := &mutationAgent{
		events: []*AgentEvent{
			{
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					Event: &SessionEvent[*schema.Message]{
						TurnID:    "producer-selected-turn",
						Kind:      extensionKind,
						Extension: &SessionExtensionEvent{},
					},
				},
			},
			{
				SessionEventVariant: &SessionEventVariant[*schema.Message]{
					SessionID: "agent_tool:child",
					Event: &SessionEvent[*schema.Message]{
						TurnID:    "child-turn",
						Kind:      extensionKind,
						Extension: &SessionExtensionEvent{},
					},
				},
			},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    sid,
		SessionStore: store,
	})

	drainSessionEvents(t, runner.Query(ctx, "go", WithTimelineEvents()))

	events := decodeStoredSessionEvents(t, store.events)
	require.NotEmpty(t, events)
	parentTurnID := events[0].TurnID
	require.NotEmpty(t, parentTurnID)
	var sawExtension bool
	for _, event := range events {
		assert.Equal(t, parentTurnID, event.TurnID, "kind=%s", event.Kind)
		if event.Kind == extensionKind {
			sawExtension = true
			assert.NotEqual(t, "producer-selected-turn", event.TurnID)
		}
		assert.NotEqual(t, "child-turn", event.TurnID)
	}
	assert.True(t, sawExtension)
}

// TestAgentToolInterruptState_RoundTrip verifies the wrapper struct round-trips
// through JSON and preserves the child SessionID for resume.
func TestAgentToolInterruptState_RoundTrip(t *testing.T) {
	bridge := []byte("opaque-checkpoint-bytes")
	wrapped := agentToolInterruptState{
		ChildSessionID:   "agent_tool:abcd",
		BridgeCheckpoint: bridge,
	}
	// Use the same JSON marshal/unmarshal path as agent_tool.go.
	encoded, err := json.Marshal(wrapped)
	require.NoError(t, err)

	var decoded agentToolInterruptState
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	assert.Equal(t, wrapped.ChildSessionID, decoded.ChildSessionID)
	assert.Equal(t, wrapped.BridgeCheckpoint, decoded.BridgeCheckpoint)
}

func TestAttack_SessionEventVariantBothSet(t *testing.T) {
	ev := &TypedAgentEvent[Message]{
		SessionEventVariant: &SessionEventVariant[Message]{
			Event: &SessionEvent[Message]{
				EventID: "evt-1",
				Kind:    SessionEventMessage,
				Message: schema.UserMessage("hello"),
			},
			MessageStreamRef: &MessageStreamRef{
				EventID: "evt-2",
				Kind:    SessionEventMessage,
			},
		},
	}

	err := validateAgentSessionEventIdentity(ev)
	if err == nil {
		t.Fatal("expected error when both Event and MessageStreamRef are set, got nil")
	}
	t.Logf("correctly rejected both-set variant: %v", err)
}

func TestAttack_SessionEventVariantNeitherSet(t *testing.T) {
	ev := &TypedAgentEvent[Message]{
		SessionEventVariant: &SessionEventVariant[Message]{
			SessionID: "sess-1",
		},
	}

	err := validateAgentSessionEventIdentity(ev)
	if err == nil {
		t.Fatal("expected error when neither Event nor MessageStreamRef is set, got nil")
	}
	t.Logf("correctly rejected neither-set variant: %v", err)
}

func TestAttack_SessionEventVariantNil(t *testing.T) {
	ev := &TypedAgentEvent[Message]{}
	err := validateAgentSessionEventIdentity(ev)
	if err != nil {
		t.Fatalf("nil variant should be valid, got error: %v", err)
	}
	t.Log("nil variant accepted correctly")
}

func TestAttack_MessageStreamRefWrongKind(t *testing.T) {
	ev := &TypedAgentEvent[Message]{
		SessionEventVariant: &SessionEventVariant[Message]{
			MessageStreamRef: &MessageStreamRef{
				EventID: "evt-1",
				Kind:    SessionEventSessionStatusRunning,
			},
		},
	}

	err := validateAgentSessionEventIdentity(ev)
	if err == nil {
		t.Fatal("expected error for MessageStreamRef with non-message kind, got nil")
	}
	t.Logf("correctly rejected wrong kind on stream ref: %v", err)
}

func TestAttack_ClassifySessionEventZeroValue(t *testing.T) {
	ev := &SessionEvent[Message]{}

	_, err := ClassifySessionEvent(ev)
	if err == nil {
		t.Fatal("expected error for zero-value session event with no payload, got nil")
	}
	t.Logf("correctly rejected zero-value event: %v", err)
}

func TestAttack_ClassifySessionEventNil(t *testing.T) {
	_, err := ClassifySessionEvent[Message](nil)
	if err == nil {
		t.Fatal("expected error for nil session event, got nil")
	}
	t.Logf("correctly rejected nil event: %v", err)
}

func TestAttack_ClassifySessionEventMultiplePayloads(t *testing.T) {
	ev := &SessionEvent[Message]{
		Message: schema.UserMessage("hello"),
		Cancel:  &CancelEvent{Reason: "test"},
	}

	_, err := ClassifySessionEvent(ev)
	if err == nil {
		t.Fatal("expected error for event with multiple active payloads, got nil")
	}
	t.Logf("correctly rejected multiple-payload event: %v", err)
}

func TestAttack_NormalizeSessionEventKindMismatch(t *testing.T) {
	ev := &SessionEvent[Message]{
		Kind:    SessionEventCancel,
		Message: schema.UserMessage("hello"),
	}

	err := NormalizeSessionEventKind(ev)
	if err == nil {
		t.Fatal("expected error for kind mismatch, got nil")
	}
	t.Logf("correctly rejected kind mismatch: %v", err)
}

func TestAttack_NormalizeSessionEventKindUnknownKindTolerated(t *testing.T) {
	unknownKinds := []SessionEventKind{
		"turn_end",
		"session_started",
		"custom_thing",
		"future.new_kind",
	}
	for _, k := range unknownKinds {
		ev := &SessionEvent[Message]{
			Kind: k,
		}
		err := NormalizeSessionEventKind(ev)
		if err != nil {
			t.Fatalf("unknown kind %q should be tolerated, got error: %v", k, err)
		}
		if ev.Kind != k {
			t.Fatalf("unknown kind %q should be preserved, got %q", k, ev.Kind)
		}
	}
}

func TestAttack_NormalizeSessionEventKindKnownKindMissingPayloadStillErrors(t *testing.T) {
	ev := &SessionEvent[Message]{
		Kind: SessionEventMessage,
	}
	err := NormalizeSessionEventKind(ev)
	if err == nil {
		t.Fatal("expected error for known kind with missing payload, got nil")
	}
	t.Logf("correctly rejected known kind with missing payload: %v", err)
}

func TestAttack_NormalizeSessionEventKindUnknownKindWithPayloadStillErrors(t *testing.T) {
	ev := &SessionEvent[Message]{
		Kind:    "future.new_kind",
		Message: schema.UserMessage("hello"),
	}
	err := NormalizeSessionEventKind(ev)
	if err == nil {
		t.Fatal("expected error for unknown kind with recognized payload, got nil")
	}
	t.Logf("correctly rejected unknown kind with recognized payload: %v", err)
}

func TestAttack_ValidateEmittedSessionEventEmptyKind(t *testing.T) {
	ev := &SessionEvent[Message]{
		Message: schema.UserMessage("hello"),
	}

	err := ValidateEmittedSessionEventKind(ev)
	if err == nil {
		t.Fatal("expected error for emitted event with empty Kind, got nil")
	}
	t.Logf("correctly rejected empty-kind emitted event: %v", err)
}

func TestAttack_ValidateEmittedSessionEventNil(t *testing.T) {
	err := ValidateEmittedSessionEventKind[Message](nil)
	if err == nil {
		t.Fatal("expected error for nil emitted event, got nil")
	}
	t.Logf("correctly rejected nil emitted event: %v", err)
}

func TestAttack_SessionEventEncodeDecodeRoundtrip(t *testing.T) {
	original := &SessionEvent[Message]{
		EventID:   "roundtrip-1",
		Timestamp: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
		Kind:      SessionEventMessage,
		Message:   schema.UserMessage("roundtrip test"),
	}

	data, err := encodeSessionEvent(original)
	if err != nil {
		t.Fatalf("encode failed: %v", err)
	}

	decoded, err := decodeSessionEvent[Message](data)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decoded.EventID != original.EventID {
		t.Errorf("EventID mismatch: got %q want %q", decoded.EventID, original.EventID)
	}
	if decoded.Kind != original.Kind {
		t.Errorf("Kind mismatch: got %q want %q", decoded.Kind, original.Kind)
	}
	t.Log("encode/decode roundtrip OK")
}

func TestAttack_SessionEventVariantPayloadEncodeDecodeRoundtrip(t *testing.T) {
	original := &TypedAgentEvent[Message]{
		SessionEventVariant: &SessionEventVariant[Message]{
			SessionID: "sess-roundtrip",
			Event: &SessionEvent[Message]{
				EventID:   "evt-rt-1",
				Timestamp: time.Date(2026, 1, 15, 10, 30, 0, 0, time.UTC),
				Kind:      SessionEventMessage,
				Message:   schema.UserMessage("variant roundtrip"),
			},
		},
	}

	persistable, err := toSessionEventChecked(original)
	if err != nil {
		t.Fatalf("convert variant payload failed: %v", err)
	}
	if persistable == original.SessionEventVariant.Event {
		t.Fatal("persistable event must be copied out of live SessionEventVariant")
	}

	data, err := encodeSessionEvent(persistable)
	if err != nil {
		t.Fatalf("encode variant payload failed: %v", err)
	}

	decoded, err := decodeSessionEvent[Message](data)
	if err != nil {
		t.Fatalf("decode variant payload failed: %v", err)
	}

	if decoded.EventID != original.SessionEventVariant.Event.EventID {
		t.Errorf("EventID mismatch: got %q want %q", decoded.EventID, original.SessionEventVariant.Event.EventID)
	}
	t.Log("variant payload encode/decode roundtrip OK")
}

func TestAttack_AssignSessionEventIDEmptyGenerator(t *testing.T) {
	emptyGen := func(_ context.Context, _ *SessionEvent[Message]) (string, error) {
		return "", nil
	}

	ev := &SessionEvent[Message]{
		Kind:    SessionEventMessage,
		Message: schema.UserMessage("test"),
	}

	err := assignSessionEventID(context.Background(), ev, emptyGen)
	if !errors.Is(err, ErrSessionEventIDGeneratorEmpty) {
		t.Fatalf("expected ErrSessionEventIDGeneratorEmpty, got %v", err)
	}
	t.Logf("correctly handled empty generator: %v", err)
}

func TestAttack_AssignSessionEventIDGeneratorError(t *testing.T) {
	genErr := errors.New("generator failed")
	errGen := func(_ context.Context, _ *SessionEvent[Message]) (string, error) {
		return "", genErr
	}

	ev := &SessionEvent[Message]{
		Kind:    SessionEventMessage,
		Message: schema.UserMessage("test"),
	}

	err := assignSessionEventID(context.Background(), ev, errGen)
	if err == nil {
		t.Fatal("expected error from generator, got nil")
	}
	if !errors.Is(err, genErr) {
		t.Fatalf("expected wrapped generator error, got %v", err)
	}
	t.Logf("correctly propagated generator error: %v", err)
}

func TestAttack_ApplySessionEventMessagesDeletedEmptyIDs(t *testing.T) {
	messages := []Message{schema.UserMessage("a"), schema.UserMessage("b")}
	ev := &SessionEvent[Message]{
		Kind: SessionEventMessagesDeleted,
		MessagesDeleted: &MessagesDeletedEvent{
			MessageIDs: []string{},
		},
	}

	err := applySessionEvent(&messages, ev)
	if err == nil {
		t.Fatal("expected error for empty MessageIDs, got nil")
	}
	t.Logf("correctly rejected empty MessageIDs: %v", err)
}

func TestAttack_ApplySessionEventMessagesDeletedDuplicateIDs(t *testing.T) {
	messages := []Message{schema.UserMessage("a")}
	ev := &SessionEvent[Message]{
		Kind: SessionEventMessagesDeleted,
		MessagesDeleted: &MessagesDeletedEvent{
			MessageIDs: []string{"dup", "dup"},
		},
	}

	err := applySessionEvent(&messages, ev)
	if err == nil {
		t.Fatal("expected error for duplicate MessageIDs, got nil")
	}
	t.Logf("correctly rejected duplicate MessageIDs: %v", err)
}

func TestAttack_ApplySessionEventMessageUpdatedIdentityMismatch(t *testing.T) {
	msg := schema.UserMessage("original")
	EnsureMessageID(msg)

	messages := []Message{msg}
	newMsg := schema.UserMessage("updated")
	EnsureMessageID(newMsg)

	ev := &SessionEvent[Message]{
		Kind: SessionEventMessageUpdated,
		MessageUpdated: &MessageUpdatedEvent[Message]{
			MessageID: GetMessageID(msg),
			Message:   newMsg,
		},
	}

	err := applySessionEvent(&messages, ev)
	if err == nil {
		t.Log("MessageUpdated with matching ID applied OK")
	} else {
		t.Logf("MessageUpdated result: %v", err)
	}
}

func TestAttack_IsContextSessionEventEdgeCases(t *testing.T) {
	tests := []struct {
		name string
		ev   *SessionEvent[Message]
		want bool
	}{
		{"nil event", nil, false},
		{"empty event", &SessionEvent[Message]{}, false},
		{"cancel event", &SessionEvent[Message]{Cancel: &CancelEvent{}}, false},
		{"lifecycle event", &SessionEvent[Message]{Lifecycle: &LifecycleEvent{State: SessionRunStateRunning}}, false},
		{"error event", &SessionEvent[Message]{Error: &SessionErrorEvent{}}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := isContextSessionEvent(tt.ev)
			if got != tt.want {
				t.Errorf("isContextSessionEvent() = %v, want %v", got, tt.want)
			}
		})
	}
	t.Log("all isContextSessionEvent edge cases pass")
}

func TestAttack_ToSessionEventCheckedStreamingOutput(t *testing.T) {
	ev := &TypedAgentEvent[Message]{
		Output: &TypedAgentOutput[Message]{
			MessageOutput: &TypedMessageVariant[Message]{
				IsStreaming: true,
				Message:     nil,
			},
		},
		SessionEventVariant: nil,
	}

	se, err := toSessionEventChecked(ev)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if se != nil {
		t.Fatalf("expected nil SessionEvent for streaming output without variant, got %v", se)
	}
	t.Log("streaming output without variant correctly returns nil session event")
}

func TestAttack_StripSessionEventFields(t *testing.T) {
	tests := []struct {
		name string
		ev   *TypedAgentEvent[Message]
		nil  bool
	}{
		{"nil event", nil, true},
		{"no variant, with output", &TypedAgentEvent[Message]{
			Output: &TypedAgentOutput[Message]{
				MessageOutput: &TypedMessageVariant[Message]{Message: schema.UserMessage("test")},
			},
		}, false},
		{"only variant", &TypedAgentEvent[Message]{
			SessionEventVariant: &SessionEventVariant[Message]{
				Event: &SessionEvent[Message]{EventID: "x", Kind: SessionEventMessage, Message: schema.UserMessage("test")},
			},
		}, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := stripSessionEventFields(tt.ev)
			if tt.nil && result != nil {
				t.Errorf("expected nil result, got %v", result)
			}
			if !tt.nil && result == nil {
				t.Error("expected non-nil result, got nil")
			}
			if result != nil && result.SessionEventVariant != nil {
				t.Error("SessionEventVariant should be stripped")
			}
		})
	}
	t.Log("stripSessionEventFields all cases pass")
}

func TestAttack_ClassifySpanEventBothModelAndTool(t *testing.T) {
	span := &SpanEvent{
		SpanID: "span-1",
		Kind:   SpanKindModel,
		Model:  &ModelSpanMeta{},
		Tool:   &ToolSpanMeta{ToolUseID: "call-1"},
	}

	_, err := classifySpanSessionEvent(span)
	if err == nil {
		t.Fatal("expected error when both Model and Tool are set, got nil")
	}
	t.Logf("correctly rejected both-model-and-tool span: %v", err)
}

func TestAttack_ClassifySpanEventNeitherModelNorTool(t *testing.T) {
	span := &SpanEvent{
		SpanID: "span-1",
		Kind:   SpanKindModel,
	}

	_, err := classifySpanSessionEvent(span)
	if err == nil {
		t.Fatal("expected error when neither Model nor Tool is set, got nil")
	}
	t.Logf("correctly rejected no-meta span: %v", err)
}

func TestAttack_SessionEventCancelClassification(t *testing.T) {
	ev := &SessionEvent[Message]{
		Cancel: &CancelEvent{Reason: "user cancelled"},
	}

	kind, err := ClassifySessionEvent(ev)
	if err != nil {
		t.Fatalf("classification failed: %v", err)
	}
	if kind != SessionEventCancel {
		t.Errorf("kind = %q, want %q", kind, SessionEventCancel)
	}
	t.Logf("CancelEvent classified correctly as %q", kind)
}

func TestAttack_SessionEventInterruptClassification(t *testing.T) {
	ev := &SessionEvent[Message]{
		Interrupt: &InterruptEvent{
			Contexts: []*InterruptContext{
				{InterruptID: "tool:lookup:call_1", ToolUseID: "call_1"},
			},
		},
	}

	kind, err := ClassifySessionEvent(ev)
	if err != nil {
		t.Fatalf("classification failed: %v", err)
	}
	if kind != SessionEventInterrupt {
		t.Errorf("kind = %q, want %q", kind, SessionEventInterrupt)
	}
	t.Logf("InterruptEvent classified correctly as %q", kind)
}

func TestAttack_SessionRollbackEventValidation(t *testing.T) {
	ev := &SessionEvent[Message]{
		EventID: "rb-1",
		Rollback: &SessionRollbackEvent{
			ToEventID: "target-1",
		},
	}

	kind, err := ClassifySessionEvent(ev)
	if err != nil {
		t.Fatalf("classification failed: %v", err)
	}
	if kind != SessionEventRollback {
		t.Errorf("kind = %q, want %q", kind, SessionEventRollback)
	}
	t.Logf("RollbackEvent classified correctly as %q", kind)
}

func TestAttack_SessionRollbackEventMissingToEventID(t *testing.T) {
	ev := &SessionEvent[Message]{
		EventID:  "rb-1",
		Rollback: &SessionRollbackEvent{},
	}

	_, err := ClassifySessionEvent(ev)
	if err == nil {
		t.Fatal("expected error for rollback with empty ToEventID, got nil")
	}
	t.Logf("correctly rejected rollback with empty ToEventID: %v", err)
}

func TestAttack_SessionRollbackEventMissingOwnEventID(t *testing.T) {
	ev := &SessionEvent[Message]{
		Rollback: &SessionRollbackEvent{
			ToEventID: "target-1",
		},
	}

	_, err := ClassifySessionEvent(ev)
	if err == nil {
		t.Fatal("expected error for rollback with empty own EventID, got nil")
	}
	t.Logf("correctly rejected rollback with empty own EventID: %v", err)
}
