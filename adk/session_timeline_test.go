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

func requireStoredIdleStopReason(t *testing.T, raw []storedSessionEvent, want string) *SessionEvent[*schema.Message] {
	t.Helper()
	idleEvents := filterStoredSessionEvents(t, raw, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSessionStatusIdle
	})
	require.NotEmpty(t, idleEvents)
	last := idleEvents[len(idleEvents)-1]
	require.NotNil(t, last.Lifecycle)
	require.NotNil(t, last.Lifecycle.StopReason)
	assert.Equal(t, want, last.Lifecycle.StopReason.Type)
	return last
}

func TestSessionTimeline_ClassifyAndSerializeVariants(t *testing.T) {
	now := time.Now().UTC()
	spanID := uuid.NewString()
	cases := []struct {
		name string
		se   *SessionEvent[*schema.Message]
		kind SessionEventKind
	}{
		{
			name: "lifecycle",
			se:   &SessionEvent[*schema.Message]{Lifecycle: &LifecycleEvent{Scope: LifecycleScopeSession, State: SessionRunStateRunning}},
			kind: SessionEventSessionStatusRunning,
		},
		{
			name: "session error",
			se:   &SessionEvent[*schema.Message]{Error: &SessionErrorEvent{Type: SessionErrorTypeModelRetry, Message: "busy", RetryStatus: &RetryStatus{Type: "retrying"}}},
			kind: SessionEventSessionError,
		},
		{
			name: "span start",
			se:   &SessionEvent[*schema.Message]{Span: &SpanEvent{SpanID: spanID, Kind: SpanKindModel, StartedAt: now, Model: &ModelSpanMeta{}}},
			kind: SessionEventSpanModelRequestStart,
		},
		{
			name: "span end",
			se:   &SessionEvent[*schema.Message]{Span: &SpanEvent{SpanID: spanID, Kind: SpanKindModel, StartedAt: now, EndedAt: now.Add(time.Millisecond), Model: &ModelSpanMeta{}}},
			kind: SessionEventSpanModelRequestEnd,
		},
		{
			name: "tool span start",
			se: &SessionEvent[*schema.Message]{Span: &SpanEvent{
				SpanID: spanID, Kind: SpanKindTool, StartedAt: now,
				Tool: &ToolSpanMeta{ToolUseID: "call_1", Name: "lookup"},
			}},
			kind: SessionEventSpanToolCallStart,
		},
		{
			name: "tool span end",
			se: &SessionEvent[*schema.Message]{Span: &SpanEvent{
				SpanID: spanID, Kind: SpanKindTool, StartedAt: now, EndedAt: now.Add(time.Millisecond),
				Status: "ok",
				Tool:   &ToolSpanMeta{ToolUseID: "call_1", Name: "lookup", ToolCallStartEventID: uuid.NewString()},
			}},
			kind: SessionEventSpanToolCallEnd,
		},
		{
			name: "interrupt",
			se:   &SessionEvent[*schema.Message]{UserObservation: &UserObservationEvent{Interrupt: &UserInterruptEvent{Reason: "user"}}},
			kind: SessionEventUserInterrupt,
		},
		{
			name: "agent interrupt",
			se: &SessionEvent[*schema.Message]{AgentInterrupt: &AgentInterruptEvent{
				Contexts: []*AgentInterruptContext{
					{
						Cause:       AgentInterruptCauseGeneric,
						InterruptID: "agent:timeline-agent",
						Info:        "confirm?",
					},
				},
			}},
			kind: SessionEventAgentInterrupt,
		},
		{
			name: "extension",
			se: &SessionEvent[*schema.Message]{
				Kind:      SessionEventKind("x.outcome.started"),
				Extension: &SessionExtensionEvent{Data: []byte(`{"outcome_name":"code_review"}`)},
			},
			kind: SessionEventKind("x.outcome.started"),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.se.EventID = uuid.NewString()
			require.NoError(t, NormalizeSessionEventKind(tc.se))
			assert.Equal(t, tc.kind, tc.se.Kind)

			data, err := encodeSessionEvent(tc.se)
			require.NoError(t, err)
			decoded, err := decodeSessionEvent[*schema.Message](data)
			require.NoError(t, err)
			assert.Equal(t, tc.kind, decoded.Kind)
			if tc.se.Extension != nil {
				require.NotNil(t, decoded.Extension)
				assert.Equal(t, []byte(`{"outcome_name":"code_review"}`), []byte(decoded.Extension.Data))
			}
		})
	}
}

func TestSessionTimeline_ExtensionValidation(t *testing.T) {
	t.Run("empty kind rejected", func(t *testing.T) {
		err := NormalizeSessionEventKind(&SessionEvent[*schema.Message]{
			Extension: &SessionExtensionEvent{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must set kind")
	})

	t.Run("non extension kind rejected", func(t *testing.T) {
		err := NormalizeSessionEventKind(&SessionEvent[*schema.Message]{
			Kind:      SessionEventKind("outcome.started"),
			Extension: &SessionExtensionEvent{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must start with")
	})

	t.Run("built in kind rejected", func(t *testing.T) {
		err := NormalizeSessionEventKind(&SessionEvent[*schema.Message]{
			Kind:      SessionEventMessage,
			Extension: &SessionExtensionEvent{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must start with")
	})

	t.Run("built in payload cannot use extension namespace", func(t *testing.T) {
		err := NormalizeSessionEventKind(&SessionEvent[*schema.Message]{
			Kind:    SessionEventKind("x.message"),
			Message: schema.UserMessage("hello"),
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "does not match payload")
	})

	t.Run("one active payload invariant", func(t *testing.T) {
		err := NormalizeSessionEventKind(&SessionEvent[*schema.Message]{
			Kind:      SessionEventKind("x.outcome.started"),
			Message:   schema.UserMessage("hello"),
			Extension: &SessionExtensionEvent{},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "exactly one active payload")
	})

	t.Run("invalid data rejected", func(t *testing.T) {
		err := NormalizeSessionEventKind(&SessionEvent[*schema.Message]{
			Kind:      SessionEventKind("x.outcome.started"),
			Extension: &SessionExtensionEvent{Data: []byte(`{"broken"`)},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "must be valid JSON")
	})

	t.Run("zero length data becomes marker", func(t *testing.T) {
		se := &SessionEvent[*schema.Message]{
			Kind:      SessionEventKind("x.outcome.started"),
			Extension: &SessionExtensionEvent{Data: []byte{}},
		}
		require.NoError(t, NormalizeSessionEventKind(se))
		assert.Nil(t, se.Extension.Data)
	})

	t.Run("pretty data is compacted", func(t *testing.T) {
		se := &SessionEvent[*schema.Message]{
			Kind: SessionEventKind("x.outcome.started"),
			Extension: &SessionExtensionEvent{
				Data: []byte("{\n  \"outcome_name\": \"code_review\",\n  \"attempt\": 1\n}"),
			},
		}
		require.NoError(t, NormalizeSessionEventKind(se))
		assert.Equal(t, []byte(`{"outcome_name":"code_review","attempt":1}`), []byte(se.Extension.Data))
	})

	t.Run("human readable round trip", func(t *testing.T) {
		se := &SessionEvent[*schema.Message]{
			EventID:   uuid.NewString(),
			Timestamp: time.Now().UTC(),
			Kind:      SessionEventKind("x.outcome.grading"),
			Extension: &SessionExtensionEvent{Data: []byte(`{"attempt":1}`)},
		}
		require.NoError(t, NormalizeSessionEventKind(se))
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.Extension)
		assert.Equal(t, se.Kind, decoded.Kind)
		assert.Equal(t, []byte(`{"attempt":1}`), []byte(decoded.Extension.Data))
	})
}

func TestSessionTimeline_ReconstructionIgnoresNonContextVariants(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "timeline-replay"

	msg := schema.UserMessage("hello")
	EnsureMessageID(msg)
	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusRunning, Lifecycle: &LifecycleEvent{Scope: LifecycleScopeSession, State: SessionRunStateRunning}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg},
		{EventID: uuid.NewString(), Kind: SessionEventSpanModelRequestStart, Span: &SpanEvent{SpanID: uuid.NewString(), Kind: SpanKindModel, StartedAt: time.Now().UTC(), Model: &ModelSpanMeta{}}},
		{EventID: uuid.NewString(), Kind: SessionEventKind("x.outcome.started"), Extension: &SessionExtensionEvent{Data: []byte(`{"attempt":1}`)}},
		{EventID: uuid.NewString(), Kind: SessionEventAgentInterrupt, AgentInterrupt: &AgentInterruptEvent{
			Contexts: []*AgentInterruptContext{
				{
					Cause:       AgentInterruptCauseGeneric,
					InterruptID: "agent:timeline-agent",
					Info:        "confirm?",
				},
			},
		}},
		{EventID: uuid.NewString(), Kind: SessionEventSessionError, Error: &SessionErrorEvent{Type: "transient", RetryStatus: &RetryStatus{Type: "retrying"}}},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"k": "v"}}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 1)
	assert.Equal(t, "hello", result.state.Messages[0].Content)
	assert.Equal(t, map[string]any{"k": "v"}, result.state.SessionValues)
}

func TestSessionTimeline_AgentInterruptRoundTripPreservesContexts(t *testing.T) {
	se := &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(),
		AgentInterrupt: &AgentInterruptEvent{
			Contexts: []*AgentInterruptContext{
				{
					Cause:       AgentInterruptCauseToolPermission,
					InterruptID: "agent:timeline-agent;tool:lookup:call_1",
					Info:        "tool info",
					ToolUseID:   "call_1",
				},
			},
		},
	}
	require.NoError(t, NormalizeSessionEventKind(se))
	require.Equal(t, SessionEventAgentInterrupt, se.Kind)

	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.AgentInterrupt)
	assert.Equal(t, SessionEventAgentInterrupt, decoded.Kind)
	require.Len(t, decoded.AgentInterrupt.Contexts, 1)
	ctx0 := decoded.AgentInterrupt.Contexts[0]
	assert.Equal(t, AgentInterruptCauseToolPermission, ctx0.Cause)
	assert.Equal(t, "agent:timeline-agent;tool:lookup:call_1", ctx0.InterruptID)
	assert.Equal(t, "tool info", ctx0.Info)
	assert.Equal(t, "call_1", ctx0.ToolUseID)
}

func TestBuildAgentInterruptEvent_ToolCauseAndToolUseID(t *testing.T) {
	contexts := []*InterruptCtx{
		{
			ID: "agent:timeline-agent;tool:lookup:call_1",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "timeline-agent"},
				{Type: AddressSegmentTool, ID: "lookup", SubID: "call_1"},
			},
			Info:        "tool info",
			IsRootCause: true,
		},
	}

	event := buildAgentInterruptEvent(contexts)
	require.NotNil(t, event)
	require.Len(t, event.Contexts, 1)
	assert.Equal(t, AgentInterruptCauseToolPermission, event.Contexts[0].Cause)
	assert.Equal(t, "agent:timeline-agent;tool:lookup:call_1", event.Contexts[0].InterruptID)
	assert.Equal(t, "tool info", event.Contexts[0].Info)
	assert.Equal(t, "call_1", event.Contexts[0].ToolUseID)

	// Fallback to segment ID when SubID is empty.
	contexts[0].Address[1].SubID = ""
	contexts[0].Address[1].ID = "legacy-call-id"
	event = buildAgentInterruptEvent(contexts)
	assert.Equal(t, "legacy-call-id", event.Contexts[0].ToolUseID)
}

func TestRunner_PersistsAgentInterruptSessionEvent(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	const checkpointID = "agent-interrupt-cp"
	agent := &myAgent{
		name: "timeline-agent",
		runFn: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				gen.Send(Interrupt(ctx, "confirm?"))
			}()
			return iter
		},
		resumeFn: func(_ context.Context, _ *ResumeInfo, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			gen.Close()
			return iter
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
		SessionID:       "agent-interrupt-session",
		SessionService:  store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})

	var liveInterruptContexts []*InterruptCtx
	iter := runner.Query(ctx, "hello", WithCheckPointID(checkpointID))
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			liveInterruptContexts = event.Action.Interrupted.InterruptContexts
		}
	}
	require.NotEmpty(t, liveInterruptContexts)

	interrupts := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventAgentInterrupt
	})
	require.Len(t, interrupts, 1)
	require.NotNil(t, interrupts[0].AgentInterrupt)
	require.Len(t, interrupts[0].AgentInterrupt.Contexts, 1)
	ctx0 := interrupts[0].AgentInterrupt.Contexts[0]
	assert.Equal(t, AgentInterruptCauseGeneric, ctx0.Cause)
	assert.Equal(t, liveInterruptContexts[0].ID, ctx0.InterruptID)
	assert.Equal(t, liveInterruptContexts[0].Info, ctx0.Info)
	requireStoredIdleStopReason(t, store.events, "interrupted")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventTurnEnd
	})
	assert.Empty(t, turnEnds, "business interrupt should remain an in-flight turn without TurnEnd")
}

func TestSessionTimeline_ReconstructionIncludesPartialContextAfterLatestTurnEnd(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "timeline-partial"

	committedUser := schema.UserMessage("committed user")
	committedAssistant := schema.AssistantMessage("committed assistant", nil)
	partialUser := schema.UserMessage("partial user")
	partialAssistant := schema.AssistantMessage("partial assistant", nil)
	for _, msg := range []*schema.Message{committedUser, committedAssistant, partialUser, partialAssistant} {
		EnsureMessageID(msg)
	}

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: committedUser},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: committedAssistant},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnID: "turn-1", TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"turn": "committed"}}},
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusRunning, Lifecycle: &LifecycleEvent{Scope: LifecycleScopeSession, State: SessionRunStateRunning}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: partialUser},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: partialAssistant},
		{EventID: uuid.NewString(), Kind: SessionEventSessionError, Error: &SessionErrorEvent{Type: SessionErrorTypeModelRetry, RetryStatus: &RetryStatus{Type: "retrying"}}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 4)
	assert.Equal(t, "committed user", result.state.Messages[0].Content)
	assert.Equal(t, "committed assistant", result.state.Messages[1].Content)
	assert.Equal(t, "partial user", result.state.Messages[2].Content)
	assert.Equal(t, "partial assistant", result.state.Messages[3].Content)
	assert.Equal(t, map[string]any{"turn": "committed"}, result.state.SessionValues)
}

func TestSessionTimeline_ReconstructionPartialContextMissingAnchorFails(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "timeline-partial-missing-anchor"

	committedUser := schema.UserMessage("committed user")
	EnsureMessageID(committedUser)
	inserted := schema.SystemMessage("inserted")
	EnsureMessageID(inserted)

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: committedUser},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnID: "turn-1", TurnEnd: &TurnEndState[*schema.Message]{}},
		{EventID: uuid.NewString(), Kind: SessionEventMessageInserted, MessageInserted: &MessageInsertedEvent[*schema.Message]{
			Message:         inserted,
			BeforeMessageID: "missing-anchor",
		}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	_, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-anchor")
}

func TestSessionTimeline_LatestCommittedTurnEndPrefersTurnIDBoundary(t *testing.T) {
	committedUser := schema.UserMessage("committed user")
	partialUser := schema.UserMessage("partial user")
	for _, msg := range []*schema.Message{committedUser, partialUser} {
		EnsureMessageID(msg)
	}

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: committedUser},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnID: "turn-1", TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"turn": "committed"}}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: partialUser},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"turn": "legacy-tail"}}},
	}

	idx := latestCommittedTurnEnd(events)
	require.Equal(t, 1, idx)

	state, err := replayDurableContextEvents(events, idx, idx)
	require.NoError(t, err)
	require.Len(t, state.Messages, 1)
	assert.Equal(t, "committed user", state.Messages[0].Content)
	assert.Equal(t, map[string]any{"turn": "committed"}, state.SessionValues)
}

func TestWithTimelineEvents_LiveExposure(t *testing.T) {
	ctx := context.Background()
	agent := &runnerSessionAgent{
		name:    "timeline-agent",
		turnEnd: &TurnEndState[*schema.Message]{Messages: []*schema.Message{schema.AssistantMessage("ok", nil)}},
	}

	t.Run("stripped by default", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: "timeline-default", SessionService: store, SessionConfig: &SessionConfig{EventFlushBatchSize: 1}})
		iter := runner.Query(ctx, "hello")
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			assert.Nil(t, event.SessionEvent)
		}
		lifecycle := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == SessionEventSessionStatusRunning || se.Kind == SessionEventSessionStatusIdle
		})
		require.Len(t, lifecycle, 2)
		requireStoredIdleStopReason(t, store.events, "end_turn")
	})

	t.Run("exposed when requested", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: "timeline-visible", SessionService: store, SessionConfig: &SessionConfig{EventFlushBatchSize: 1}})
		var kinds []SessionEventKind
		var liveUserInput bool
		iter := runner.Query(ctx, "hello", WithTimelineEvents())
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			if event.SessionEvent != nil {
				assert.Equal(t, event.EventID, event.SessionEvent.EventID)
				kinds = append(kinds, event.SessionEvent.Kind)
				if event.SessionEvent.Kind == SessionEventMessage && event.SessionEvent.Message != nil &&
					event.SessionEvent.Message.Role == schema.User && event.SessionEvent.Message.Content == "hello" {
					liveUserInput = true
				}
			}
		}
		assert.Contains(t, kinds, SessionEventSessionStatusRunning)
		assert.True(t, liveUserInput, "caller input should be emitted on the live timeline")
		assert.Contains(t, kinds, SessionEventSessionStatusIdle)

		storedUserInput := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == SessionEventMessage && se.Message != nil &&
				se.Message.Role == schema.User && se.Message.Content == "hello"
		})
		require.Len(t, storedUserInput, 1)
	})
}

type extensionEventModel struct{}

func (m *extensionEventModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}

func (m *extensionEventModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func TestRunner_ExtensionEventSentWithTypedSendEventIsLiveAndPersisted(t *testing.T) {
	ctx := context.Background()
	extensionKind := SessionEventKind("x.outcome.grading")
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "extension-event-agent",
		Instruction: "test",
		Model:       &extensionEventModel{},
		Middlewares: []AgentMiddleware{
			{
				AfterChatModel: func(ctx context.Context, _ *ChatModelAgentState) error {
					return SendEvent(ctx, &AgentEvent{
						SessionEvent: &SessionEvent[*schema.Message]{
							Kind: extensionKind,
							Extension: &SessionExtensionEvent{
								Data: []byte("{\n  \"outcome_name\": \"code_review\",\n  \"attempt\": 1\n}"),
							},
						},
					})
				},
			},
		},
	})
	require.NoError(t, err)

	t.Run("visible when timeline requested", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{
			Agent:          agent,
			SessionID:      "extension-event-session-visible",
			SessionService: store,
			SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
		})

		var liveExtension *SessionEvent[*schema.Message]
		iter := runner.Query(ctx, "hello", WithTimelineEvents())
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			if event.SessionEvent != nil && event.SessionEvent.Kind == extensionKind {
				liveExtension = event.SessionEvent
			}
		}

		require.NotNil(t, liveExtension)
		require.NotEmpty(t, liveExtension.EventID)
		require.NotEmpty(t, liveExtension.TurnID)
		require.NotNil(t, liveExtension.Extension)
		assert.Equal(t, []byte(`{"outcome_name":"code_review","attempt":1}`), []byte(liveExtension.Extension.Data))

		stored := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == extensionKind
		})
		require.Len(t, stored, 1)
		assert.Equal(t, liveExtension.EventID, stored[0].EventID)
		assert.Equal(t, liveExtension.TurnID, stored[0].TurnID)
		require.NotNil(t, stored[0].Extension)
		assert.Equal(t, []byte(`{"outcome_name":"code_review","attempt":1}`), []byte(stored[0].Extension.Data))

		var extensionIndex, idleIndex = -1, -1
		for i, payload := range store.events {
			switch payload.Kind {
			case extensionKind:
				extensionIndex = i
			case SessionEventSessionStatusIdle:
				idleIndex = i
			}
		}
		require.NotEqual(t, -1, extensionIndex)
		require.NotEqual(t, -1, idleIndex)
		assert.Less(t, extensionIndex, idleIndex, "extension event should enter Runner persistence before the closing idle lifecycle event")
	})

	t.Run("stripped from live stream by default", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{
			Agent:          agent,
			SessionID:      "extension-event-session-stripped",
			SessionService: store,
			SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
		})

		iter := runner.Query(ctx, "hello")
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			assert.Nil(t, event.SessionEvent)
		}

		stored := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == extensionKind
		})
		require.Len(t, stored, 1)
	})
}

func TestTypedSendEventOutsideExecutionReturnsError(t *testing.T) {
	err := SendEvent(context.Background(), &AgentEvent{
		SessionEvent: &SessionEvent[*schema.Message]{
			Kind:      SessionEventKind("x.outcome.started"),
			Extension: &SessionExtensionEvent{},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must be called within")
}

func TestSessionTimeline_SpanMetaMustBeOneOf(t *testing.T) {
	se := &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(),
		Span: &SpanEvent{
			SpanID:    uuid.NewString(),
			Kind:      SpanKindModel,
			StartedAt: time.Now().UTC(),
			Model:     &ModelSpanMeta{},
			Tool:      &ToolSpanMeta{ToolUseID: "call_1"},
		},
	}

	err := NormalizeSessionEventKind(se)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one of Model or Tool")
}

func TestToolPermissionDecisionScopedByToolUseID(t *testing.T) {
	ctx := contextWithToolPermissionDecisionStore(context.Background())
	SetToolPermissionDecision(ctx, "call_1", "allowed")
	SetToolPermissionDecision(ctx, "call_2", "denied")

	assert.Equal(t, "allowed", GetToolPermissionDecision(ctx, "call_1"))
	assert.Equal(t, "denied", GetToolPermissionDecision(ctx, "call_2"))
	assert.Empty(t, GetToolPermissionDecision(ctx, "missing"))
}

func TestRetryTimelineEmitsRescheduleSequence(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:              gen,
		internalTimelineEvents: true,
	})

	emitRetryingTimeline[*schema.Message](ctx, assert.AnError)
	gen.Close()

	var kinds []SessionEventKind
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		require.NotNil(t, event.SessionEvent)
		require.Equal(t, event.EventID, event.SessionEvent.EventID)
		kinds = append(kinds, event.SessionEvent.Kind)
	}

	require.Equal(t, []SessionEventKind{
		SessionEventSessionError,
		SessionEventSessionStatusRescheduled,
		SessionEventSessionStatusRunning,
	}, kinds)
}

func TestModelUsageFromAssistantMapsNormalizedUsage(t *testing.T) {
	usage := &schema.TokenUsage{
		PromptTokens:     10,
		CompletionTokens: 5,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 7,
		},
	}
	msg := schema.AssistantMessage("ok", nil)
	msg.ResponseMeta = &schema.ResponseMeta{Usage: usage}

	got := modelUsageFromAssistant[*schema.Message](msg)
	require.NotNil(t, got)
	assert.Equal(t, 10, got.InputTokens)
	assert.Equal(t, 5, got.OutputTokens)
	assert.Equal(t, 7, got.CacheReadInputTokens)
	assert.Zero(t, got.CacheCreationInputTokens)
	assert.Same(t, usage, got.Raw)
}

func TestModelSpanEndCarriesAssistantUsage(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:              gen,
		internalTimelineEvents: true,
	})
	usage := &schema.TokenUsage{
		PromptTokens:     12,
		CompletionTokens: 6,
		PromptTokenDetails: schema.PromptTokenDetails{
			CachedTokens: 4,
		},
	}
	inner := newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		msg := schema.AssistantMessage("ok", nil)
		msg.ResponseMeta = &schema.ResponseMeta{Usage: usage, FinishReason: "stop"}
		return msg, nil
	}, nil)
	wrapped := &typedEventSenderModel[*schema.Message]{inner: inner}

	_, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	gen.Close()

	var spanEnd *SessionEvent[*schema.Message]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEvent != nil && event.SessionEvent.Kind == SessionEventSpanModelRequestEnd {
			spanEnd = event.SessionEvent
		}
	}
	require.NotNil(t, spanEnd)
	require.NotNil(t, spanEnd.Span.Model)
	require.NotNil(t, spanEnd.Span.Model.Usage)
	assert.Equal(t, 12, spanEnd.Span.Model.Usage.InputTokens)
	assert.Equal(t, 6, spanEnd.Span.Model.Usage.OutputTokens)
	assert.Equal(t, 4, spanEnd.Span.Model.Usage.CacheReadInputTokens)
	assert.Equal(t, "stop", spanEnd.Span.Model.FinishReason)
	assert.True(t, spanEnd.Span.Model.Accepted)
}

func TestSessionTimeline_EmittedKindMustBeExplicit(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:              gen,
		internalTimelineEvents: true,
	})

	sendSessionTimelineEvent(ctx, &SessionEvent[*schema.Message]{
		EventID:   uuid.NewString(),
		Timestamp: newEventTimestamp(),
		Lifecycle: &LifecycleEvent{
			Scope: LifecycleScopeSession,
			State: SessionRunStateRunning,
		},
	})
	gen.Close()

	event, ok := iter.Next()
	require.True(t, ok)
	require.Error(t, event.Err)
	assert.Contains(t, event.Err.Error(), "non-empty Kind")
}

func TestSessionTimeline_TypedAgentEventGobRoundTripPreservesSessionEvent(t *testing.T) {
	now := time.Now().UTC()
	spanID := uuid.NewString()
	original := &AgentEvent{
		EventID:   uuid.NewString(),
		Timestamp: newEventTimestamp(),
		SessionEvent: &SessionEvent[*schema.Message]{
			EventID:   uuid.NewString(),
			Timestamp: now,
			Kind:      SessionEventSpanToolCallStart,
			Span: &SpanEvent{
				SpanID:    spanID,
				Kind:      SpanKindTool,
				Name:      "tool_call",
				StartedAt: now,
				Tool:      &ToolSpanMeta{ToolUseID: "call_1", Name: "lookup"},
			},
		},
	}
	original.SessionEvent.EventID = original.EventID

	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(original))

	var decoded AgentEvent
	require.NoError(t, gob.NewDecoder(&buf).Decode(&decoded))
	require.NotNil(t, decoded.SessionEvent)
	assert.Equal(t, original.EventID, decoded.EventID)
	assert.Equal(t, original.EventID, decoded.SessionEvent.EventID)
	assert.Equal(t, SessionEventSpanToolCallStart, decoded.SessionEvent.Kind)
}

func TestModelSpanMetaFromContextPopulatesFailoverAndModelFields(t *testing.T) {
	parentSpanID := uuid.NewString()
	ctx := context.Background()
	ctx = typedSetFailoverCurrentModel[*schema.Message](ctx, newFakeChatModel(nil, nil))
	ctx = withFailoverTimeline(ctx, parentSpanID, 3)

	started := newEventTimestamp()
	start := newModelSpanStartEvent[*schema.Message](ctx, uuid.NewString(), started, model.WithModel("claude-sonnet"))
	require.NotNil(t, start.Span)
	require.NotNil(t, start.Span.Model)
	assert.Equal(t, parentSpanID, start.Span.ParentSpanID)
	assert.Equal(t, "fake_chat_model", start.Span.Model.Provider)
	assert.Equal(t, "claude-sonnet", start.Span.Model.Model)
	assert.Equal(t, 3, start.Span.Model.Attempt)

	end := newModelSpanEndEvent[*schema.Message](
		ctx,
		modelSpanEndEventInput[*schema.Message]{
			spanID:       start.Span.SpanID,
			startEventID: start.EventID,
			started:      started,
			ended:        started.Add(time.Millisecond),
			msg:          schema.AssistantMessage("ok", nil),
			accepted:     true,
		},
		model.WithModel("claude-sonnet"),
	)
	require.NotNil(t, end.Span)
	require.NotNil(t, end.Span.Model)
	assert.Equal(t, parentSpanID, end.Span.ParentSpanID)
	assert.Equal(t, start.EventID, end.Span.Model.ModelRequestStartEventID)
	assert.Equal(t, 3, end.Span.Model.Attempt)
}

func TestRetryTimelineUsesRejectReasonMessage(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:              gen,
		internalTimelineEvents: true,
	})

	var calls int
	inner := newFakeChatModel(func(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return schema.AssistantMessage("bad", nil), nil
		}
		return schema.AssistantMessage("ok", nil), nil
	}, nil)
	wrapper := newTypedRetryModelWrapper[*schema.Message](inner, &ModelRetryConfig{
		MaxRetries: 1,
		ShouldRetry: func(_ context.Context, retryCtx *RetryContext) *RetryDecision {
			if retryCtx.OutputMessage != nil && retryCtx.OutputMessage.Content == "bad" {
				return &RetryDecision{Retry: true, RejectReason: "policy rejected"}
			}
			return &RetryDecision{}
		},
		BackoffFunc: func(context.Context, int) time.Duration { return 0 },
	})

	msg, err := wrapper.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	assert.Equal(t, "ok", msg.Content)
	gen.Close()

	var found bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.SessionEvent != nil && event.SessionEvent.Kind == SessionEventSessionError {
			require.NotNil(t, event.SessionEvent.Error)
			assert.Equal(t, "policy rejected", event.SessionEvent.Error.Message)
			found = true
		}
	}
	assert.True(t, found)
}

func TestFailoverTimelineLinksAttemptsAndEmitsSessionErrors(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	modelErr := errors.New("first failed")
	m1 := newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return nil, modelErr
	}, nil)
	m2 := newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return schema.AssistantMessage("ok", nil), nil
	}, nil)
	wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
		failoverConfig: &ModelFailoverConfig[*schema.Message]{
			MaxRetries:     1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(context.Context, *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		},
	})
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:                gen,
		internalTimelineEvents:   true,
		failoverLastSuccessModel: m1,
	})

	msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")}, model.WithModel("logical-model"))
	require.NoError(t, err)
	assert.Equal(t, "ok", msg.Content)
	gen.Close()

	var starts []*SessionEvent[*schema.Message]
	var failoverErrors []*SessionEvent[*schema.Message]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEvent == nil {
			continue
		}
		switch event.SessionEvent.Kind {
		case SessionEventSpanModelRequestStart:
			starts = append(starts, event.SessionEvent)
		case SessionEventSessionError:
			if event.SessionEvent.Error != nil && event.SessionEvent.Error.Type == SessionErrorTypeModelFailover {
				failoverErrors = append(failoverErrors, event.SessionEvent)
			}
		}
	}
	require.Len(t, starts, 2)
	require.NotEmpty(t, starts[0].Span.ParentSpanID)
	assert.Equal(t, starts[0].Span.ParentSpanID, starts[1].Span.ParentSpanID)
	assert.Equal(t, 1, starts[0].Span.Model.Attempt)
	assert.Equal(t, 2, starts[1].Span.Model.Attempt)
	assert.Equal(t, "logical-model", starts[0].Span.Model.Model)
	require.Len(t, failoverErrors, 1)
	assert.Equal(t, "retrying", failoverErrors[0].Error.RetryStatus.Type)
}

func TestSessionTimeline_EventIDMismatchRejectedAtPersistenceBoundary(t *testing.T) {
	now := time.Now().UTC()
	_, err := toSessionEventChecked(&AgentEvent{
		EventID: uuid.NewString(),
		SessionEvent: &SessionEvent[*schema.Message]{
			EventID:   uuid.NewString(),
			Timestamp: now,
			Kind:      SessionEventSpanToolCallStart,
			Span: &SpanEvent{
				SpanID:    uuid.NewString(),
				Kind:      SpanKindTool,
				StartedAt: now,
				Tool:      &ToolSpanMeta{ToolUseID: "call_1"},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session event identity mismatch")
}

func TestSessionTimeline_NormalizeAgentSessionEventMaterializesEnvelope(t *testing.T) {
	now := time.Now().UTC()
	makeToolStartSpan := func() *SessionEvent[*schema.Message] {
		return &SessionEvent[*schema.Message]{
			Kind: SessionEventSpanToolCallStart,
			Span: &SpanEvent{
				SpanID:    uuid.NewString(),
				Kind:      SpanKindTool,
				StartedAt: now,
				Tool:      &ToolSpanMeta{ToolUseID: "call_1"},
			},
		}
	}

	t.Run("both ids empty", func(t *testing.T) {
		original := makeToolStartSpan()
		event := &AgentEvent{SessionEvent: original}
		se, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		require.NotEmpty(t, event.EventID)
		assert.Equal(t, event.EventID, se.EventID)
		assert.Equal(t, event.EventID, event.SessionEvent.EventID)
		require.False(t, event.Timestamp.IsZero())
		assert.Equal(t, event.Timestamp, se.Timestamp)
		assert.Equal(t, event.Timestamp, event.SessionEvent.Timestamp)
		assert.Empty(t, original.EventID)
	})

	t.Run("envelope id and timestamp backfill session event", func(t *testing.T) {
		ts := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
		id := uuid.NewString()
		se := makeToolStartSpan()
		se.Span.StartedAt = ts
		event := &AgentEvent{
			EventID:      id,
			Timestamp:    ts,
			SessionEvent: se,
		}
		out, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		assert.Equal(t, id, out.EventID)
		assert.Equal(t, ts, out.Timestamp)
	})

	t.Run("session event id and timestamp backfill envelope", func(t *testing.T) {
		ts := time.Date(2026, 5, 24, 12, 1, 0, 0, time.UTC)
		id := uuid.NewString()
		se := makeToolStartSpan()
		se.EventID = id
		se.Timestamp = ts
		se.Span.StartedAt = ts
		event := &AgentEvent{SessionEvent: se}
		out, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		assert.Equal(t, id, event.EventID)
		assert.Equal(t, ts, event.Timestamp)
		assert.Equal(t, id, out.EventID)
		assert.Equal(t, ts, out.Timestamp)
	})

	t.Run("turn end messages stripped without mutating producer event", func(t *testing.T) {
		msg := schema.AssistantMessage("kept only by producer", nil)
		original := &SessionEvent[*schema.Message]{
			Kind: SessionEventTurnEnd,
			TurnEnd: &TurnEndState[*schema.Message]{
				Messages:      []*schema.Message{msg},
				SessionValues: map[string]any{"answer": "ok"},
			},
		}
		event := &AgentEvent{SessionEvent: original}
		se, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		require.NotNil(t, se.TurnEnd)
		assert.Nil(t, se.TurnEnd.Messages)
		require.NotNil(t, event.SessionEvent.TurnEnd)
		assert.Nil(t, event.SessionEvent.TurnEnd.Messages)
		require.NotNil(t, original.TurnEnd)
		require.Len(t, original.TurnEnd.Messages, 1)
		assert.Equal(t, "ok", se.TurnEnd.SessionValues["answer"])
	})
}

func TestRetryOnlyModelSpansHaveNoParentSpanID(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	var calls int
	modelErr := errors.New("retry me")
	inner := newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		calls++
		if calls == 1 {
			return nil, modelErr
		}
		return schema.AssistantMessage("ok", nil), nil
	}, nil)
	wrapped := buildModelWrappers[*schema.Message](inner, &modelWrapperConfig{
		retryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			BackoffFunc: func(context.Context, int) time.Duration {
				return 0
			},
		},
	})
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:              gen,
		internalTimelineEvents: true,
	})

	msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	assert.Equal(t, "ok", msg.Content)
	gen.Close()

	var starts []*SessionEvent[*schema.Message]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEvent != nil && event.SessionEvent.Kind == SessionEventSpanModelRequestStart {
			starts = append(starts, event.SessionEvent)
		}
	}
	require.NotEmpty(t, starts)
	for _, start := range starts {
		assert.Empty(t, start.Span.ParentSpanID)
	}
}

func TestRetryAndFailoverTimelineKeepsDistinctErrorTypes(t *testing.T) {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	m1 := newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return nil, errors.New("primary failed")
	}, nil)
	m2 := newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
		return schema.AssistantMessage("fallback ok", nil), nil
	}, nil)
	wrapped := buildModelWrappers[*schema.Message](m1, &modelWrapperConfig{
		retryConfig: &ModelRetryConfig{
			MaxRetries: 1,
			BackoffFunc: func(context.Context, int) time.Duration {
				return 0
			},
		},
		failoverConfig: &ModelFailoverConfig[*schema.Message]{
			MaxRetries:     1,
			ShouldFailover: func(context.Context, *schema.Message, error) bool { return true },
			GetFailoverModel: func(context.Context, *FailoverContext[*schema.Message]) (model.BaseChatModel, []*schema.Message, error) {
				return m2, nil, nil
			},
		},
	})
	ctx := withTypedChatModelAgentExecCtx(context.Background(), &chatModelAgentExecCtx{
		generator:                gen,
		internalTimelineEvents:   true,
		failoverLastSuccessModel: m1,
	})

	msg, err := wrapped.Generate(ctx, []*schema.Message{schema.UserMessage("hi")})
	require.NoError(t, err)
	assert.Equal(t, "fallback ok", msg.Content)
	gen.Close()

	var retryExhausted bool
	var failoverRetrying bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEvent == nil || event.SessionEvent.Kind != SessionEventSessionError || event.SessionEvent.Error == nil {
			continue
		}
		switch event.SessionEvent.Error.Type {
		case SessionErrorTypeModelRetry:
			if event.SessionEvent.Error.RetryStatus != nil && event.SessionEvent.Error.RetryStatus.Type == "exhausted" {
				retryExhausted = true
			}
		case SessionErrorTypeModelFailover:
			if event.SessionEvent.Error.RetryStatus != nil && event.SessionEvent.Error.RetryStatus.Type == "retrying" {
				failoverRetrying = true
			}
		}
	}
	assert.True(t, retryExhausted)
	assert.True(t, failoverRetrying)
}

type timelineErrorAgent struct {
	name string
	err  error
}

func (a *timelineErrorAgent) Name(context.Context) string {
	return a.name
}

func (a *timelineErrorAgent) Description(context.Context) string {
	return "timeline error agent"
}

func (a *timelineErrorAgent) Run(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		gen.Send(&AgentEvent{Err: a.err})
	}()
	return iter
}

func TestRunnerTimelineRetryExhaustedStopReason(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          &timelineErrorAgent{name: "retry-exhausted", err: &RetryExhaustedError{LastErr: errors.New("still failing"), TotalRetries: 1}},
		SessionID:      "timeline-retry-exhausted",
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "hi")
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}

	requireStoredIdleStopReason(t, store.events, "retries_exhausted")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventTurnEnd
	})
	assert.Empty(t, turnEnds, "retry exhaustion should not commit a TurnEnd")
}

func TestRunnerTimelineFailedStopReason(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          &timelineErrorAgent{name: "failed", err: errors.New("boom")},
		SessionID:      "timeline-failed",
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "hi")
	var gotErrs []error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			gotErrs = append(gotErrs, event.Err)
		}
	}
	require.NotEmpty(t, gotErrs)
	assert.EqualError(t, gotErrs[0], "boom")
	for _, err := range gotErrs {
		assert.NotContains(t, err.Error(), "missing SessionEventTurnEnd")
	}

	sessionErrors := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSessionError
	})
	require.NotEmpty(t, sessionErrors)
	require.NotNil(t, sessionErrors[len(sessionErrors)-1].Error)
	assert.Equal(t, SessionErrorTypeFatal, sessionErrors[len(sessionErrors)-1].Error.Type)
	assert.Equal(t, "boom", sessionErrors[len(sessionErrors)-1].Error.Message)
	requireStoredIdleStopReason(t, store.events, "failed")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventTurnEnd
	})
	assert.Empty(t, turnEnds, "failed turn should not commit a TurnEnd")
}

func TestRunnerTimelineModelCallFatalDoesNotRequireTurnEnd(t *testing.T) {
	ctx := context.Background()
	modelErr := errors.New("model exploded")
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "fatal-model-agent",
		Model: newFakeChatModel(func(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
			return nil, modelErr
		}, nil),
	})
	require.NoError(t, err)

	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      "timeline-fatal-model",
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})

	var gotErrs []error
	iter := runner.Query(ctx, "hi")
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			gotErrs = append(gotErrs, event.Err)
		}
	}

	require.NotEmpty(t, gotErrs)
	assert.ErrorIs(t, gotErrs[0], modelErr)
	for _, err := range gotErrs {
		assert.NotContains(t, err.Error(), "missing SessionEventTurnEnd")
	}

	sessionErrors := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSessionError
	})
	require.NotEmpty(t, sessionErrors)
	require.NotNil(t, sessionErrors[len(sessionErrors)-1].Error)
	assert.Equal(t, SessionErrorTypeFatal, sessionErrors[len(sessionErrors)-1].Error.Type)
	assert.Contains(t, sessionErrors[len(sessionErrors)-1].Error.Message, modelErr.Error())
	requireStoredIdleStopReason(t, store.events, "failed")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventTurnEnd
	})
	assert.Empty(t, turnEnds, "fatal model call should abort without committing a TurnEnd")
}

func TestRunnerTimelineCancelStopReasonAndUserInterruptPersisted(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	started := make(chan struct{})
	release := make(chan struct{})

	agent := &myAgent{
		name: "timeline-cancel",
		runFn: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			go func() {
				defer gen.Close()
				close(started)
				<-release
				gen.Send(Interrupt(ctx, "cancel point"))
			}()
			return iter
		},
	}
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
		SessionID:       "timeline-cancel",
		SessionService:  store,
		SessionConfig:   &SessionConfig{EventFlushBatchSize: 1},
	})
	cancelOpt, cancelFn := WithCancel()
	iter := runner.Query(ctx, "hi", cancelOpt, WithCheckPointID("timeline-cancel-cp"))

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("agent did not start")
	}
	cancelHandle, contributed := cancelFn(WithAgentCancelMode(CancelImmediate))
	require.True(t, contributed)
	close(release)

	var sawCancelErr bool
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var cancelErr *CancelError
		if event.Err != nil && errors.As(event.Err, &cancelErr) {
			sawCancelErr = true
		}
	}
	require.True(t, sawCancelErr, "expected CancelError in event stream")
	require.NoError(t, cancelHandle.Wait())

	userInterrupts := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventUserInterrupt
	})
	require.Len(t, userInterrupts, 1)
	require.NotNil(t, userInterrupts[0].UserObservation)
	require.NotNil(t, userInterrupts[0].UserObservation.Interrupt)
	assert.Equal(t, "cancelled", userInterrupts[0].UserObservation.Interrupt.Reason)
	requireStoredIdleStopReason(t, store.events, "cancelled")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventTurnEnd
	})
	assert.Empty(t, turnEnds, "cancelled turn should not commit a TurnEnd")
}

func TestToolSpan_PersistedAroundToolCallAndLinksToMessages(t *testing.T) {
	ctx := context.Background()
	testTool := &invokableTestTool{name: "tool_span_tool", result: "tool result"}
	mockModel := &mockToolCallingModel{toolCallName: "tool_span_tool"}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ToolSpanAgent",
		Description: "tool span agent",
		Model:       mockModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
		},
	})
	require.NoError(t, err)

	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      "tool-span-around",
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	iter := runner.Query(ctx, "go")
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}

	stored := filterStoredSessionEvents(t, store.events, func(_ *SessionEvent[*schema.Message]) bool { return true })
	var (
		assistantMsgEventID string
		toolResultEventID   string
		toolStart           *SessionEvent[*schema.Message]
		toolEnd             *SessionEvent[*schema.Message]
		toolUseObservations int
	)
	for _, se := range stored {
		switch se.Kind {
		case SessionEventMessage:
			if se.Message != nil && se.Message.Role == schema.Assistant && len(se.Message.ToolCalls) > 0 {
				assistantMsgEventID = se.EventID
			}
			if se.Message != nil && se.Message.Role == schema.Tool {
				toolResultEventID = se.EventID
			}
		case SessionEventSpanToolCallStart:
			toolStart = se
		case SessionEventSpanToolCallEnd:
			toolEnd = se
		case "agent.tool_use", "agent.tool_result", "agent.thinking":
			toolUseObservations++
		}
	}

	require.NotNil(t, toolStart, "expected tool_call_start span")
	require.NotNil(t, toolEnd, "expected tool_call_end span")
	require.NotNil(t, toolStart.Span.Tool)
	require.NotNil(t, toolEnd.Span.Tool)
	assert.Equal(t, "tool_span_tool", toolStart.Span.Tool.Name)
	assert.Equal(t, "tool_span_tool", toolEnd.Span.Tool.Name)
	assert.Equal(t, "tc-1", toolStart.Span.Tool.ToolUseID)
	assert.Equal(t, toolStart.EventID, toolEnd.Span.Tool.ToolCallStartEventID)
	assert.Equal(t, "ok", toolEnd.Span.Status)
	assert.Equal(t, 0, toolUseObservations, "no observation kinds should be persisted")

	if assistantMsgEventID != "" {
		assert.Equal(t, assistantMsgEventID, toolStart.Span.Tool.AssistantMessageEventID)
		assert.Equal(t, assistantMsgEventID, toolEnd.Span.Tool.AssistantMessageEventID)
	}
	if toolResultEventID != "" {
		assert.Equal(t, toolResultEventID, toolEnd.Span.Tool.ToolResultMessageEventID)
	}
	assert.NotEmpty(t, toolStart.Span.ParentSpanID, "parent should be the model request span")
	assert.Equal(t, toolStart.Span.ParentSpanID, toolEnd.Span.ParentSpanID)
}

type kindsRecordingStore struct {
	SessionService[*schema.Message]
	recordedKinds [][]SessionEventKind
}

func (s *kindsRecordingStore) LoadEvents(ctx context.Context, sessionID string, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	if opts != nil {
		s.recordedKinds = append(s.recordedKinds, opts.Kinds)
	}
	return s.SessionService.LoadEvents(ctx, sessionID, opts)
}

func TestSessionTimeline_ReconstructionUsesKindFilter(t *testing.T) {
	ctx := context.Background()
	inner := newSessionHelperStore()
	wrapper := &kindsRecordingStore{SessionService: inner}
	sid := "timeline-kind-filter"

	msg1 := schema.UserMessage("hello")
	EnsureMessageID(msg1)
	msg2 := schema.AssistantMessage("world", nil)
	EnsureMessageID(msg2)

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg1},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg2},
		{EventID: uuid.NewString(), Kind: SessionEventSpanModelRequestStart, Span: &SpanEvent{SpanID: uuid.NewString(), Kind: SpanKindModel, StartedAt: time.Now().UTC(), Model: &ModelSpanMeta{}}},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"done": true}}},
	}
	for _, se := range events {
		require.NoError(t, inner.AppendEvents(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, wrapper, sid, defaultLoadPageSize)
	require.NoError(t, err)

	// All recorded Kinds slices should equal modelContextSessionEventKinds.
	require.NotEmpty(t, wrapper.recordedKinds)
	for _, kinds := range wrapper.recordedKinds {
		assert.Equal(t, modelContextSessionEventKinds, kinds)
	}

	// Verify reconstruction result.
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "hello", result.state.Messages[0].Content)
	assert.Equal(t, "world", result.state.Messages[1].Content)
	assert.Equal(t, map[string]any{"done": true}, result.state.SessionValues)
}

func TestToolSpan_StreamableToolEmitsEndAfterEOF(t *testing.T) {
	ctx := context.Background()
	streamTool := &streamableTestTool{name: "stream_span_tool", result: "stream chunk"}
	mockModel := &mockToolCallingModel{toolCallName: "stream_span_tool"}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "StreamSpanAgent",
		Description: "stream span agent",
		Model:       mockModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{streamTool}},
		},
	})
	require.NoError(t, err)

	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:          agent,
		SessionID:      "tool-span-stream",
		SessionService: store,
		SessionConfig:  &SessionConfig{EventFlushBatchSize: 1},
	})
	iter := runner.Query(ctx, "stream go")
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
	}

	stored := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSpanToolCallStart || se.Kind == SessionEventSpanToolCallEnd
	})
	require.Len(t, stored, 2)
	assert.Equal(t, SessionEventSpanToolCallStart, stored[0].Kind)
	assert.Equal(t, SessionEventSpanToolCallEnd, stored[1].Kind)
	assert.Equal(t, "ok", stored[1].Span.Status)
	require.NotNil(t, stored[1].Span.Tool)
	assert.NotEmpty(t, stored[1].Span.Tool.ToolResultMessageEventID)
}
