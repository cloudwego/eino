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
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// sessionStreamingAgent emits a single streaming assistant output followed by a
// TurnEndState. Used to verify the runner's stream-copy/persist path.
type sessionStreamingAgent struct {
	chunks  []*schema.Message
	turnEnd *TurnEndState[*schema.Message]
}

func (a *sessionStreamingAgent) Name(_ context.Context) string        { return "session-stream-agent" }
func (a *sessionStreamingAgent) Description(_ context.Context) string { return "stream test agent" }
func (a *sessionStreamingAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		stream := schema.StreamReaderFromArray(a.chunks)
		mv := &MessageVariant{IsStreaming: true, MessageStream: stream, Role: schema.Assistant}
		gen.Send(&AgentEvent{AgentName: "session-stream-agent", Output: &AgentOutput{MessageOutput: mv}})
		gen.Send(&AgentEvent{AgentName: "session-stream-agent", TurnEndState: a.turnEnd})
	}()
	return iter
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello world", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		EnableStreaming:    true,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
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
	for _, raw := range store.events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
		if se.Message != nil && se.Message.Role == schema.Assistant {
			assistantMessages = append(assistantMessages, se.Message)
		}
	}
	require.Len(t, assistantMessages, 1, "streaming assistant output must be persisted exactly once")
	assert.Equal(t, "hello world", assistantMessages[0].Content,
		"persisted stream message must be the fully concatenated content")
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		EnableStreaming:    true,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
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
	require.Error(t, lastErr, "turn must fail when persisted-stream materialization errors")
	assert.Contains(t, lastErr.Error(), "failed to persist session events")

	// Verify no assistant SessionEvent is in the log.
	for _, raw := range store.events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
		if se.Message != nil {
			assert.NotEqual(t, schema.Assistant, se.Message.Role,
				"failed stream must not produce a persisted assistant event")
		}
	}
	// Snapshot must NOT have been committed.
	assert.False(t, store.turnExists, "SaveTurnEnd must not run when persistence fails")
}

// streamingAgentRaw lets the test inject an arbitrary stream reader (including
// one that emits errors).
type streamingAgentRaw struct {
	stream  *schema.StreamReader[*schema.Message]
	turnEnd *TurnEndState[*schema.Message]
}

func (a *streamingAgentRaw) Name(_ context.Context) string        { return "streaming-raw" }
func (a *streamingAgentRaw) Description(_ context.Context) string { return "stream-error test agent" }
func (a *streamingAgentRaw) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		mv := &MessageVariant{IsStreaming: true, MessageStream: a.stream, Role: schema.Assistant}
		gen.Send(&AgentEvent{AgentName: "streaming-raw", Output: &AgentOutput{MessageOutput: mv}})
		gen.Send(&AgentEvent{AgentName: "streaming-raw", TurnEndState: a.turnEnd})
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})

	systemMsg := schema.SystemMessage("system instruction")
	userMsg := schema.UserMessage("hello")
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{systemMsg, userMsg}))

	// Find the first two persisted events: they must be the input messages with
	// preserved roles.
	require.GreaterOrEqual(t, len(store.events), 2)
	first, err := decodeSessionEvent[*schema.Message](store.events[0])
	require.NoError(t, err)
	require.NotNil(t, first.Message)
	assert.Equal(t, schema.System, first.Message.Role)
	assert.Equal(t, "system instruction", first.Message.Content)

	second, err := decodeSessionEvent[*schema.Message](store.events[1])
	require.NoError(t, err)
	require.NotNil(t, second.Message)
	assert.Equal(t, schema.User, second.Message.Role)
	assert.Equal(t, "hello", second.Message.Content)
}

// TestTurnEndStateOnly_CapturedNotPersisted verifies that an event carrying
// only TurnEndState (no message output, no mutations) drives SaveTurnEnd but
// does NOT add a SessionEvent to the log.
func TestTurnEndStateOnly_CapturedNotPersisted(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "turn-end-only"

	// Custom agent that emits ONLY a TurnEndState event (no output, no mutations).
	agent := &turnEndOnlyAgent{
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("x")},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "input"))

	require.True(t, store.turnExists, "TurnEndState must be saved")

	// The only event in the log should be the input event we provided.
	for _, raw := range store.events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
		// All events should be input message events (Role=User), never TurnEndState payloads.
		require.NotNil(t, se.Message, "TurnEndState event must not be persisted as SessionEvent")
	}
}

type turnEndOnlyAgent struct {
	turnEnd *TurnEndState[*schema.Message]
}

func (a *turnEndOnlyAgent) Name(_ context.Context) string        { return "turn-end-only" }
func (a *turnEndOnlyAgent) Description(_ context.Context) string { return "" }
func (a *turnEndOnlyAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(&AgentEvent{AgentName: "turn-end-only", TurnEndState: a.turnEnd})
	}()
	return iter
}

// TestTailReplay_AfterSaveTurnEndFailure verifies that events appended after the
// last successful snapshot survive a SaveTurnEnd failure on a subsequent turn.
// On boot, tail replay layers post-snapshot events on top of the snapshot's Messages.
func TestTailReplay_AfterSaveTurnEndFailure(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
	sid := "tail-replay"

	// Phase 1: a normal turn. Snapshot committed.
	a1 := schema.UserMessage("Q1")
	EnsureMessageID(a1)
	r1 := schema.AssistantMessage("A1", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{a1, r1} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}
	turnEnd := &TurnEndState[*schema.Message]{Messages: []*schema.Message{a1, r1}}
	teBytes, err := encodeTurnEndState(turnEnd)
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sid, GetMessageID(r1), teBytes))

	// Phase 2: simulate a partial second turn where events were appended but
	// SaveTurnEnd failed (i.e. snapshot was NOT updated).
	a2 := schema.UserMessage("Q2")
	EnsureMessageID(a2)
	r2 := schema.AssistantMessage("A2", nil)
	EnsureMessageID(r2)
	for _, m := range []*schema.Message{a2, r2} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}

	// Boot: prepareRunnerSessionRun should load the snapshot AND tail-replay the
	// post-snapshot events.
	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, sid, store, nil, nil, nil)
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
	store := NewInMemoryStoreLocal(t)
	sid := "no-tail"

	q := schema.UserMessage("Q")
	EnsureMessageID(q)
	se := &SessionEvent[*schema.Message]{Message: q}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))

	turnEnd := &TurnEndState[*schema.Message]{Messages: []*schema.Message{q}}
	teBytes, err := encodeTurnEndState(turnEnd)
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sid, GetMessageID(q), teBytes))

	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, sid, store, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 1)
	assert.Equal(t, "Q", state.latestState.Messages[0].Content)
}

// TestTailReplay_EmptySnapshotCursor verifies cursor-based replay correctly
// handles a snapshot that committed an empty Messages array — the cursor still
// excludes pre-snapshot events.
func TestTailReplay_EmptySnapshotCursor(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
	sid := "empty-snapshot"

	// Pre-snapshot events that should NOT be replayed.
	for i := 0; i < 3; i++ {
		m := schema.UserMessage("pre")
		EnsureMessageID(m)
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}
	// Snapshot with empty Messages and empty afterMessageID.
	teBytes, err := encodeTurnEndState(&TurnEndState[*schema.Message]{})
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sid, "", teBytes))

	// Post-snapshot events.
	postMsg := schema.UserMessage("post")
	EnsureMessageID(postMsg)
	se := &SessionEvent[*schema.Message]{Message: postMsg}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))

	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, sid, store, nil, nil, nil)
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 1)
	assert.Equal(t, "post", state.latestState.Messages[0].Content,
		"only post-snapshot events must be replayed; pre-snapshot events must stay excluded")
}

// NewInMemoryStoreLocal returns the InMemoryStore implementation from the
// session subpackage, accessed via its public constructor through the test
// helper SessionStore interface.
func NewInMemoryStoreLocal(t *testing.T) SessionStore {
	t.Helper()
	return &inMemoryAdapter{
		events:   map[string][][]byte{},
		turnEnds: map[string]inMemoryTurnEnd{},
	}
}

// inMemoryAdapter is a minimal in-package SessionStore used by tail-replay
// tests. It implements just enough of the cursor semantics: SaveTurnEnd captures
// the current event count as the cursor, After decodes that decimal index.
type inMemoryAdapter struct {
	events   map[string][][]byte
	turnEnds map[string]inMemoryTurnEnd
}

type inMemoryTurnEnd struct {
	afterMessageID   string
	afterCursor string
	data             []byte
}

func (s *inMemoryAdapter) AppendEvents(_ context.Context, sid string, events [][]byte) error {
	for _, e := range events {
		s.events[sid] = append(s.events[sid], append([]byte{}, e...))
	}
	return nil
}

func (s *inMemoryAdapter) LoadEvents(_ context.Context, sid string, opts *LoadEventsRequest) (*LoadEventsResult, error) {
	all := s.events[sid]
	if opts == nil {
		opts = &LoadEventsRequest{}
	}
	if opts.After != "" {
		var idx int
		_, _ = fmtSscan(opts.After, &idx)
		if idx > len(all) {
			idx = len(all)
		}
		out := make([][]byte, len(all)-idx)
		for i := range out {
			out[i] = append([]byte{}, all[idx+i]...)
		}
		return &LoadEventsResult{Events: out}, nil
	}
	if opts.Reverse {
		out := make([][]byte, 0, len(all))
		for i := len(all) - 1; i >= 0; i-- {
			out = append(out, append([]byte{}, all[i]...))
		}
		return &LoadEventsResult{Events: out}, nil
	}
	out := make([][]byte, len(all))
	for i := range all {
		out[i] = append([]byte{}, all[i]...)
	}
	return &LoadEventsResult{Events: out}, nil
}

func (s *inMemoryAdapter) SaveTurnEnd(_ context.Context, sid string, afterMessageID string, turnEnd []byte) error {
	s.turnEnds[sid] = inMemoryTurnEnd{
		afterMessageID:   afterMessageID,
		afterCursor: itoa(len(s.events[sid])),
		data:             append([]byte{}, turnEnd...),
	}
	return nil
}

func (s *inMemoryAdapter) LoadLatestTurnEnd(_ context.Context, sid string) (string, string, []byte, bool, error) {
	rec, ok := s.turnEnds[sid]
	if !ok {
		return "", "", nil, false, nil
	}
	return rec.afterMessageID, rec.afterCursor, append([]byte{}, rec.data...), true, nil
}

// TestPartialInterrupted_ThenNewRun verifies that when a turn is interrupted
// after some events have been appended (but before SaveTurnEnd commits), a new
// Run with NO CheckPointStore (i.e. session-only mode) recovers the in-flight
// events via tail replay rather than treating the session as fresh.
//
// This test does not use CheckPointStore so we sidestep ErrPendingSessionCheckpoint.
func TestPartialInterrupted_ThenNewRun(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
	sid := "partial-interrupted"

	// Phase 1: simulate a normal completed turn.
	q1 := schema.UserMessage("first")
	EnsureMessageID(q1)
	r1 := schema.AssistantMessage("answer1", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{q1, r1} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}
	turnEnd := &TurnEndState[*schema.Message]{Messages: []*schema.Message{q1, r1}}
	teBytes, err := encodeTurnEndState(turnEnd)
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sid, GetMessageID(r1), teBytes))

	// Phase 2: simulate an interrupted turn — events appended, no new SaveTurnEnd.
	q2 := schema.UserMessage("partial")
	EnsureMessageID(q2)
	for _, m := range []*schema.Message{q2} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}

	// Phase 3: new Run (no CheckPointStore so ErrPendingSessionCheckpoint cannot fire).
	captured := &runnerSessionAgent{
		name: "ra",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              captured,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "second"))

	// The reconstructed history fed to the agent must include the partial turn's "partial" input.
	require.Len(t, captured.inputs, 1)
	contents := []string{}
	for _, m := range captured.inputs[0] {
		contents = append(contents, m.Content)
	}
	assert.Equal(t, []string{"first", "answer1", "partial", "second"}, contents,
		"partial-turn message must survive via tail replay in stable order without duplicates")
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
// an explicit checkpoint ID alongside a configured SessionID/SessionStore, the
// resume path still loads the latest TurnEndState (and runs tail replay).
func TestExplicitCheckpointResume_WithSessionMode(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "explicit-cp-session"

	// Seed the session store with a snapshot.
	prior := &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{schema.UserMessage("seed"), schema.AssistantMessage("seed-ans", nil)},
	}
	teBytes, err := encodeTurnEndState(prior)
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sid, "", teBytes))

	// Seed an arbitrary checkpoint ID with a runner-session-checkpoint wrapper
	// so runnerLoadCheckPointForSession can decode it.
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{Payload: []byte("opaque")})
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

// TestResumePath_TailReplay verifies that the resume path also performs tail
// replay (uses the same fast path as the run path).
func TestResumePath_TailReplay(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
	sid := "resume-tail"

	q1 := schema.UserMessage("Q")
	EnsureMessageID(q1)
	r1 := schema.AssistantMessage("A", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{q1, r1} {
		se := &SessionEvent[*schema.Message]{Message: m}
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))
	}
	teBytes, err := encodeTurnEndState(&TurnEndState[*schema.Message]{Messages: []*schema.Message{q1, r1}})
	require.NoError(t, err)
	require.NoError(t, store.SaveTurnEnd(ctx, sid, GetMessageID(r1), teBytes))

	// Append a tail event after the snapshot.
	tailMsg := schema.UserMessage("post-snapshot")
	EnsureMessageID(tailMsg)
	se := &SessionEvent[*schema.Message]{Message: tailMsg}
	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	require.NoError(t, store.AppendEvents(ctx, sid, [][]byte{data}))

	// Seed a runner session checkpoint so the resume path finds something to load.
	cpStore := newSessionHelperStore()
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{Payload: []byte("opaque")})
	require.NoError(t, err)
	require.NoError(t, cpStore.Set(ctx, sessionRunnerCheckpointID(sid), cpBytes))

	state, _, err := prepareRunnerSessionResume[*schema.Message](ctx, cpStore, sid, store, nil, "")
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 3,
		"resume path must apply tail replay on top of the snapshot")
	assert.Equal(t, "post-snapshot", state.latestState.Messages[2].Content)
}

// Ensure the io package import is used (for compile when chunks are empty).
var _ = io.EOF

// mutationAgent emits a sequence of caller-provided TypedAgentEvents and a
// final TurnEndState. Used to verify the runner persists each session-mutation
// event variant (MessagesReplaced, MessageUpdated, MessageInserted) faithfully.
type mutationAgent struct {
	events  []*AgentEvent
	turnEnd *TurnEndState[*schema.Message]
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
		gen.Send(&AgentEvent{AgentName: "mutation-agent", TurnEndState: a.turnEnd})
	}()
	return iter
}

// TestRunnerPersists_MessagesReplaced verifies a MessagesReplaced event from
// any source (e.g. summarization) is persisted.
func TestRunnerPersists_MessagesReplaced(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
	sid := "mr-session"

	summary := schema.AssistantMessage("summary content", nil)
	EnsureMessageID(summary)
	repl := []*schema.Message{summary}

	agent := &mutationAgent{
		events: []*AgentEvent{
			{AgentName: "mutation-agent", MessagesReplaced: &repl},
		},
		turnEnd: &TurnEndState[*schema.Message]{Messages: []*schema.Message{summary}},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "anything"))

	// Read events back via the store.
	res, err := store.LoadEvents(ctx, sid, &LoadEventsRequest{})
	require.NoError(t, err)

	var foundReplaced bool
	for _, raw := range res.Events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
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
	store := NewInMemoryStoreLocal(t)
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
				MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
					MessageID: GetMessageID(toolResultMsg),
					Message:   updatedTool,
				},
			},
			{
				AgentName: "mutation-agent",
				MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
					MessageID: GetMessageID(toolCallMsg),
					Message:   updatedAssistant,
				},
			},
		},
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{updatedAssistant, updatedTool},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "go"))

	res, err := store.LoadEvents(ctx, sid, &LoadEventsRequest{})
	require.NoError(t, err)

	var updates int
	for _, raw := range res.Events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
		if se.MessageUpdated != nil {
			updates++
		}
	}
	assert.Equal(t, 2, updates, "both MessageUpdated events must be persisted")

	// Reconstruction (no snapshot path) must apply both updates correctly.
	// We simulate by deleting the snapshot from the store.
	if mem, ok := store.(*inMemoryAdapter); ok {
		delete(mem.turnEnds, sid)
	}
	msgs, err := reconstructFromEventLog[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	// Find updated content among reconstructed messages.
	var sawClearedAssistant, sawPlaceholderTool bool
	for _, m := range msgs {
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
	store := NewInMemoryStoreLocal(t)
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
				MessageInserted: &MessageInsertedEvent[*schema.Message]{
					Message:         agentsmdMsg,
					BeforeMessageID: GetMessageID(userMsg),
				},
			},
			// MessageInserted appended at end:
			{
				AgentName: "mutation-agent",
				MessageInserted: &MessageInsertedEvent[*schema.Message]{
					Message:         patchedTool,
					BeforeMessageID: "",
				},
			},
		},
		turnEnd: &TurnEndState[*schema.Message]{Messages: finalMessages},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	// We must pass the user message as input, with its existing ID already assigned,
	// so reconstruction's anchor lookup succeeds.
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{userMsg}))

	res, err := store.LoadEvents(ctx, sid, &LoadEventsRequest{})
	require.NoError(t, err)

	var inserts int
	for _, raw := range res.Events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
		if se.MessageInserted != nil {
			inserts++
		}
	}
	assert.Equal(t, 2, inserts, "both MessageInserted events must be persisted")

	// Force fallback reconstruction.
	if mem, ok := store.(*inMemoryAdapter); ok {
		delete(mem.turnEnds, sid)
	}
	msgs, err := reconstructFromEventLog[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(msgs), 3)
	// The agentsmd message should appear before the user input.
	var idxAgentsmd, idxUser, idxPatched int
	idxAgentsmd, idxUser, idxPatched = -1, -1, -1
	for i, m := range msgs {
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

// TestAgentTool_ChildSessionID_FiltersFromParentLog verifies that events
// forwarded from an inner agent (via AgentTool) are tagged with the child
// SessionID and are NOT persisted into the parent's session event log. The
// parent's log only contains events that belong to its own session.
func TestAgentTool_ChildSessionID_FiltersFromParentLog(t *testing.T) {
	ctx := context.Background()
	parentStore := NewInMemoryStoreLocal(t)
	sid := "parent-session"

	// Inner-agent forwarded event from AgentTool path. Tagging with a SessionID
	// that does not match the parent session must be filtered out of persistence.
	childMsg := schema.AssistantMessage("inner-agent-output", nil)
	EnsureMessageID(childMsg)
	parentMsg := schema.AssistantMessage("parent-output", nil)
	EnsureMessageID(parentMsg)

	agent := &mutationAgent{
		events: []*AgentEvent{
			// An event tagged as belonging to a different session — should not be persisted.
			{
				AgentName: "child",
				SessionID: "agent_tool:abc-123",
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{parentMsg},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:              agent,
		SessionID:          sid,
		SessionStore:       parentStore,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})
	drainSessionEvents(t, runner.Query(ctx, "go"))

	// Verify that childMsg is NOT in the parent's persistent log, but parentMsg is.
	res, err := parentStore.LoadEvents(ctx, sid, &LoadEventsRequest{})
	require.NoError(t, err)
	var sawChild, sawParent bool
	for _, raw := range res.Events {
		se, err := decodeSessionEvent[*schema.Message](raw)
		require.NoError(t, err)
		if se.Message != nil {
			if GetMessageID(se.Message) == GetMessageID(childMsg) {
				sawChild = true
			}
			if GetMessageID(se.Message) == GetMessageID(parentMsg) {
				sawParent = true
			}
		}
	}
	assert.False(t, sawChild, "events tagged with a different SessionID must NOT enter the parent session log")
	assert.True(t, sawParent, "parent's own events must be persisted")
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
	encoded, err := jsonMarshalForTest(wrapped)
	require.NoError(t, err)

	var decoded agentToolInterruptState
	require.NoError(t, jsonUnmarshalForTest(encoded, &decoded))
	assert.Equal(t, wrapped.ChildSessionID, decoded.ChildSessionID)
	assert.Equal(t, wrapped.BridgeCheckpoint, decoded.BridgeCheckpoint)
}

// jsonMarshalForTest / jsonUnmarshalForTest avoid an extra import line just for tests.
func jsonMarshalForTest(v any) ([]byte, error) {
	return json.Marshal(v)
}

func jsonUnmarshalForTest(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
