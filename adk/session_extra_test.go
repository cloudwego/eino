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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// sessionStreamingAgent emits a single streaming assistant output followed by a
// SessionEventTurnEnd. Used to verify the runner's stream-copy/persist path.
type sessionStreamingAgent struct {
	chunks   []*schema.Message
	turnEnd  *TurnEndState[*schema.Message]
	role     schema.RoleType
	tool     string
	preEvent *SessionEvent[*schema.Message]
}

func (a *sessionStreamingAgent) Name(_ context.Context) string        { return "session-stream-agent" }
func (a *sessionStreamingAgent) Description(_ context.Context) string { return "stream test agent" }
func (a *sessionStreamingAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		if a.preEvent != nil {
			gen.Send(&AgentEvent{AgentName: "session-stream-agent", SessionEvent: a.preEvent})
		}
		stream := schema.StreamReaderFromArray(a.chunks)
		role := a.role
		if role == "" {
			role = schema.Assistant
		}
		mv := &MessageVariant{IsStreaming: true, MessageStream: stream, Role: role, ToolName: a.tool}
		gen.Send(&AgentEvent{AgentName: "session-stream-agent", Output: &AgentOutput{MessageOutput: mv}})
		gen.Send(&AgentEvent{
			AgentName: "session-stream-agent",
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: a.turnEnd,
			},
		})
	}()
	return iter
}

type agenticSessionStreamingAgent struct {
	chunks  []*schema.AgenticMessage
	turnEnd *TurnEndState[*schema.AgenticMessage]
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
					MessageStream: schema.StreamReaderFromArray(a.chunks),
					AgenticRole:   schema.AgenticRoleTypeUser,
				},
			},
		})
		gen.Send(&TypedAgentEvent[*schema.AgenticMessage]{
			AgentName: "agentic-session-stream-agent",
			SessionEvent: &SessionEvent[*schema.AgenticMessage]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: a.turnEnd,
			},
		})
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
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionService:  store,
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

func TestStreamPersistence_StreamingLiveBeforeMaterializedBoundary(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "sync-stream-session"

	agent := &sessionStreamingAgent{
		chunks: []*schema.Message{
			schema.AssistantMessage("hello ", nil),
			schema.AssistantMessage("sync", nil),
		},
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello sync", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionService:  store,
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("q"), schema.AssistantMessage("hello stream", nil)},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       "stream-annotation-boundary",
		SessionService:  store,
	})

	drainSessionEvents(t, runner.Query(ctx, "q"))

	assert.Equal(t, [][]SessionEventKind{
		{SessionEventSessionStatusRunning},
		{SessionEventMessage},
		{annotationKind},
		{SessionEventMessage},
		{SessionEventTurnEnd},
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.ToolMessage("tool result", "tc-1", schema.WithToolName("t1"))},
		},
		role: schema.Tool,
		tool: "t1",
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionService:  store,
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
		turnEnd: &TurnEndState[*schema.AgenticMessage]{
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
		SessionService:  store,
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
	res, err := store.LoadEvents(ctx, sid, nil)
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
		turnEnd: &TurnEndState[*schema.AgenticMessage]{
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
		SessionService:  store,
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
	res, err := store.LoadEvents(ctx, sid, nil)
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionService:  store,
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.AssistantMessage("ok", nil)},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		SessionID:       sid,
		SessionService:  store,
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
		gen.Send(&AgentEvent{
			AgentName: "streaming-raw",
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: a.turnEnd,
			},
		})
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
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
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

// TestTurnEndOnly_PersistedAsSessionEvent verifies that an event carrying only
// SessionEventTurnEnd (no message output, no mutations) persists the TurnEnd as
// a SessionEvent variant in the log.
func TestTurnEndOnly_PersistedAsSessionEvent(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "turn-end-only"

	// Custom agent that emits ONLY a TurnEnd event (no output, no mutations).
	agent := &turnEndOnlyAgent{
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{schema.UserMessage("x")},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "input"))

	// The log should contain: the input event + a TurnEnd event.
	var sawTurnEnd bool
	for _, ep := range store.events {
		se, err := decodeSessionEvent[*schema.Message](ep.Data)
		require.NoError(t, err)
		if se.TurnEnd != nil {
			sawTurnEnd = true
		}
	}
	assert.True(t, sawTurnEnd, "TurnEnd must be persisted as a SessionEvent")
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
		gen.Send(&AgentEvent{
			AgentName: "turn-end-only",
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: a.turnEnd,
			},
		})
	}()
	return iter
}

// TestTailReplay_PartialTurnWithoutTurnEnd verifies that events appended after
// the last TurnEnd event are replayed on reconstruction (partial/interrupted turn).
func TestTailReplay_PartialTurnWithoutTurnEnd(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
	sid := "tail-replay"

	// Phase 1: a normal completed turn (messages + TurnEnd event).
	a1 := schema.UserMessage("Q1")
	EnsureMessageID(a1)
	r1 := schema.AssistantMessage("A1", nil)
	EnsureMessageID(r1)
	for _, m := range []*schema.Message{a1, r1} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// Persist TurnEnd as a SessionEvent.
	turnEndSE := withTestEventID(&SessionEvent[*schema.Message]{TurnEnd: &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{a1, r1},
	}})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

	// Phase 2: simulate a partial second turn where events were appended but
	// no TurnEnd was persisted (interrupted).
	a2 := schema.UserMessage("Q2")
	EnsureMessageID(a2)
	r2 := schema.AssistantMessage("A2", nil)
	EnsureMessageID(r2)
	for _, m := range []*schema.Message{a2, r2} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	// Boot: prepareRunnerSessionRun reconstructs durable context through the log
	// tail. The latest TurnEnd remains the metadata boundary.
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
	store := NewInMemoryStoreLocal(t)
	sid := "no-tail"

	q := schema.UserMessage("Q")
	EnsureMessageID(q)
	se := withTestEventID(&SessionEvent[*schema.Message]{Message: q})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	// Persist TurnEnd as a SessionEvent.
	turnEndSE := withTestEventID(&SessionEvent[*schema.Message]{TurnEnd: &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{q},
	}})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

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
	store := NewInMemoryStoreLocal(t)
	sid := "empty-snapshot"

	// Pre-boundary events.
	for i := 0; i < 3; i++ {
		m := schema.UserMessage("pre")
		EnsureMessageID(m)
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// MessagesReplaced boundary with empty slice — supersedes pre-boundary events.
	empty := []*schema.Message{}
	boundarySE := withTestEventID(&SessionEvent[*schema.Message]{MessagesReplaced: &empty})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{boundarySE}))

	// Post-boundary events.
	postMsg := schema.UserMessage("post")
	EnsureMessageID(postMsg)
	se := withTestEventID(&SessionEvent[*schema.Message]{Message: postMsg})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	state, err := prepareRunnerSessionRun[*schema.Message](ctx, nil, nil, sid, store, nil)
	require.NoError(t, err)
	require.Len(t, state.latestState.Messages, 1)
	assert.Equal(t, "post", state.latestState.Messages[0].Content)
}

// NewInMemoryStoreLocal returns a minimal in-package store for tests.
func NewInMemoryStoreLocal(t *testing.T) *sessionHelperStore {
	t.Helper()
	return newSessionHelperStore()
}

type agenticSessionHelperStore struct {
	mu         sync.Mutex
	events     []storedSessionEvent
	eventIDIdx map[string]int
}

func newAgenticSessionHelperStore() *agenticSessionHelperStore {
	return &agenticSessionHelperStore{eventIDIdx: make(map[string]int)}
}

func (s *agenticSessionHelperStore) AppendEvents(_ context.Context, _ string, events []*SessionEvent[*schema.AgenticMessage]) error {
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

func (s *agenticSessionHelperStore) LoadEvents(_ context.Context, _ string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.AgenticMessage], error) {
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
	return h.store.LoadEvents(ctx, h.sessionID, req)
}

func (h *agenticTestSessionHandle) appendEvents(ctx context.Context, req *AppendSessionEventsRequest[*schema.AgenticMessage]) error {
	if req == nil {
		req = &AppendSessionEventsRequest[*schema.AgenticMessage]{}
	}
	return h.store.AppendEvents(ctx, h.sessionID, req.Events)
}

func (h *agenticTestSessionHandle) close(context.Context) error { return nil }
func (h *agenticTestSessionHandle) currentTailEventID() string  { return "" }

// TestPartialInterrupted_ThenNewRun verifies that when a turn is interrupted
// after some events have been appended (but before SaveTurnEnd commits), a new
// Run with NO CheckPointStore (i.e. session-only mode) recovers the in-flight
// events via tail replay rather than treating the session as fresh.
//
// This test does not use CheckPointStore — Runner skips pending checkpoints
// on fresh Run, so checkpoint presence would not block regardless.
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
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// Persist TurnEnd as a SessionEvent (marks end of completed turn).
	turnEndSE := withTestEventID(&SessionEvent[*schema.Message]{TurnEnd: &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{q1, r1},
	}})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

	// Phase 2: simulate an interrupted turn — events appended, no new SaveTurnEnd.
	q2 := schema.UserMessage("partial")
	EnsureMessageID(q2)
	for _, m := range []*schema.Message{q2} {
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	// Phase 3: new Run (no CheckPointStore; Runner skips pending checkpoints on fresh Run).
	captured := &runnerSessionAgent{
		name: "ra",
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          captured,
		SessionID:      sid,
		SessionService: store,
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
// an explicit checkpoint ID alongside a configured SessionID/SessionService[*schema.Message], the
// resume path still loads the latest TurnEndState (and runs tail replay).
func TestExplicitCheckpointResume_WithSessionMode(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "explicit-cp-session"

	// Seed the session store with events and a TurnEnd.
	prior := &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{schema.UserMessage("seed"), schema.AssistantMessage("seed-ans", nil)},
	}
	// Seed session events (messages + TurnEnd).
	for _, m := range prior.Messages {
		EnsureMessageID(m)
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	turnEndSE := withTestEventID(&SessionEvent[*schema.Message]{TurnEnd: prior})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

	// Seed an arbitrary checkpoint ID with a runner-session-checkpoint wrapper
	// so runnerLoadCheckPointForSession can decode it.
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		SessionTailEventID: store.currentTailEventID(),
		Payload:            []byte("opaque"),
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
		se := withTestEventID(&SessionEvent[*schema.Message]{Message: m})
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}
	// Persist TurnEnd as a SessionEvent.
	turnEndSE := withTestEventID(&SessionEvent[*schema.Message]{TurnEnd: &TurnEndState[*schema.Message]{
		Messages: []*schema.Message{q1, r1},
	}})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{turnEndSE}))

	// Append a tail event after the snapshot.
	tailMsg := schema.UserMessage("post-snapshot")
	EnsureMessageID(tailMsg)
	se := withTestEventID(&SessionEvent[*schema.Message]{Message: tailMsg})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))

	// Seed a runner session checkpoint so the resume path finds something to load.
	cpStore := newSessionHelperStore()
	cpBytes, err := encodeRunnerSessionCheckpoint(&runnerSessionCheckpoint{
		SessionTailEventID: store.currentTailEventID(),
		Payload:            []byte("opaque"),
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
var _ = io.EOF

// mutationAgent emits a sequence of caller-provided TypedAgentEvents and a
// final SessionEventTurnEnd. Used to verify the runner persists each session-mutation
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
		gen.Send(&AgentEvent{
			AgentName: "mutation-agent",
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind:    SessionEventTurnEnd,
				TurnEnd: a.turnEnd,
			},
		})
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
			{
				AgentName: "mutation-agent",
				SessionEvent: &SessionEvent[*schema.Message]{
					Kind:             SessionEventMessagesReplaced,
					MessagesReplaced: &repl,
				},
			},
		},
		turnEnd: &TurnEndState[*schema.Message]{Messages: []*schema.Message{summary}},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "anything"))

	// Read events back via the store.
	res, err := store.LoadEvents(ctx, sid, &LoadSessionEventsRequest{})
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
				SessionEvent: &SessionEvent[*schema.Message]{
					Kind: SessionEventMessageUpdated,
					MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
						MessageID: GetMessageID(toolResultMsg),
						Message:   updatedTool,
					},
				},
			},
			{
				AgentName: "mutation-agent",
				SessionEvent: &SessionEvent[*schema.Message]{
					Kind: SessionEventMessageUpdated,
					MessageUpdated: &MessageUpdatedEvent[*schema.Message]{
						MessageID: GetMessageID(toolCallMsg),
						Message:   updatedAssistant,
					},
				},
			},
		},
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{updatedAssistant, updatedTool},
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})
	drainSessionEvents(t, runner.Query(ctx, "go"))

	res, err := store.LoadEvents(ctx, sid, &LoadSessionEventsRequest{})
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
				SessionEvent: &SessionEvent[*schema.Message]{
					Kind: SessionEventMessageInserted,
					MessageInserted: &MessageInsertedEvent[*schema.Message]{
						Message:         agentsmdMsg,
						BeforeMessageID: GetMessageID(userMsg),
					},
				},
			},
			// MessageInserted appended at end:
			{
				AgentName: "mutation-agent",
				SessionEvent: &SessionEvent[*schema.Message]{
					Kind: SessionEventMessageInserted,
					MessageInserted: &MessageInsertedEvent[*schema.Message]{
						Message:         patchedTool,
						BeforeMessageID: "",
					},
				},
			},
		},
		turnEnd: &TurnEndState[*schema.Message]{Messages: finalMessages},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})
	// We must pass the user message as input, with its existing ID already assigned,
	// so reconstruction's anchor lookup succeeds.
	drainSessionEvents(t, runner.Run(ctx, []*schema.Message{userMsg}))

	res, err := store.LoadEvents(ctx, sid, &LoadSessionEventsRequest{})
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

func TestRunnerPersists_MessagesDeleted_Reconstructs(t *testing.T) {
	ctx := context.Background()
	store := NewInMemoryStoreLocal(t)
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
				SessionEvent: &SessionEvent[*schema.Message]{
					Kind: SessionEventMessagesDeleted,
					MessagesDeleted: &MessagesDeletedEvent{
						MessageIDs: []string{GetMessageID(b)},
					},
				},
			},
		},
		turnEnd: &TurnEndState[*schema.Message]{Messages: []*schema.Message{a, c}},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})
	drainSessionEvents(t, runner.Run(ctx, nil))

	res, err := store.LoadEvents(ctx, sid, &LoadSessionEventsRequest{})
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
	store := NewInMemoryStoreLocal(t)
	sid := "md-missing-target"

	a := schema.UserMessage("a")
	EnsureMessageID(a)
	msgEvent := withTestEventID(&SessionEvent[*schema.Message]{Message: a})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{msgEvent}))

	deleteEvent := withTestEventID(&SessionEvent[*schema.Message]{
		MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{"ghost-id"}},
	})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{deleteEvent}))

	turnEndEvent := withTestEventID(&SessionEvent[*schema.Message]{
		TurnID: "turn-1",
		TurnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{a},
		},
	})
	require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{turnEndEvent}))

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
	parentStore := NewInMemoryStoreLocal(t)
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
				SessionEvent: &SessionEvent[*schema.Message]{
					SessionID: "agent_tool:abc-123",
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
		turnEnd: &TurnEndState[*schema.Message]{
			Messages: []*schema.Message{parentMsg},
		},
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: parentStore,
	})
	drainSessionEvents(t, runner.Query(ctx, "go"))

	// Verify that childMsg is NOT in the parent's persistent log, but parentMsg is.
	res, err := parentStore.LoadEvents(ctx, sid, &LoadSessionEventsRequest{})
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
