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
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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
			se:   &SessionEvent[*schema.Message]{Span: &SpanEvent{SpanID: spanID, Kind: SpanKindModel, StartedAt: now}},
			kind: SessionEventSpanModelRequestStart,
		},
		{
			name: "span end",
			se:   &SessionEvent[*schema.Message]{Span: &SpanEvent{SpanID: spanID, Kind: SpanKindModel, StartedAt: now, EndedAt: now.Add(time.Millisecond)}},
			kind: SessionEventSpanModelRequestEnd,
		},
		{
			name: "thinking",
			se:   &SessionEvent[*schema.Message]{AgentObservation: &AgentObservationEvent{Thinking: &AgentThinkingEvent{}}},
			kind: SessionEventAgentThinking,
		},
		{
			name: "tool use",
			se:   &SessionEvent[*schema.Message]{AgentObservation: &AgentObservationEvent{ToolUse: &AgentToolUseEvent{ToolUseID: "call_1", Name: "lookup", Input: map[string]any{"q": "x"}}}},
			kind: SessionEventAgentToolUse,
		},
		{
			name: "tool result",
			se:   &SessionEvent[*schema.Message]{AgentObservation: &AgentObservationEvent{ToolResult: &AgentToolResultEvent{ToolUseID: "call_1", Content: "ok"}}},
			kind: SessionEventAgentToolResult,
		},
		{
			name: "interrupt",
			se:   &SessionEvent[*schema.Message]{UserObservation: &UserObservationEvent{Interrupt: &UserInterruptEvent{Reason: "user"}}},
			kind: SessionEventUserInterrupt,
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
		})
	}
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
		{EventID: uuid.NewString(), Kind: SessionEventAgentThinking, AgentObservation: &AgentObservationEvent{Thinking: &AgentThinkingEvent{}}},
		{EventID: uuid.NewString(), Kind: SessionEventSessionError, Error: &SessionErrorEvent{Type: "transient", RetryStatus: &RetryStatus{Type: "retrying"}}},
		{EventID: uuid.NewString(), Kind: SessionEventTurnEnd, TurnEnd: &TurnEndState[*schema.Message]{SessionValues: map[string]any{"k": "v"}}},
	}
	for _, se := range events {
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, []SessionEventPayload{{EventID: se.EventID, Data: data}}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 1)
	assert.Equal(t, "hello", result.state.Messages[0].Content)
	assert.Equal(t, map[string]any{"k": "v"}, result.state.SessionValues)
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
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, []SessionEventPayload{{EventID: se.EventID, Data: data}}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize, nil)
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
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		require.NoError(t, store.AppendEvents(ctx, sid, []SessionEventPayload{{EventID: se.EventID, Data: data}}))
	}

	_, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize, nil)
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
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: "timeline-default", SessionStore: store, SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1}})
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
	})

	t.Run("exposed when requested", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: "timeline-visible", SessionStore: store, SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1}})
		var kinds []SessionEventKind
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
			}
		}
		assert.Contains(t, kinds, SessionEventSessionStatusRunning)
		assert.Contains(t, kinds, SessionEventSessionStatusIdle)
	})
}

func TestSessionTimeline_AgentObservationMustBeOneOf(t *testing.T) {
	se := &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(),
		AgentObservation: &AgentObservationEvent{
			Thinking: &AgentThinkingEvent{},
			ToolUse:  &AgentToolUseEvent{ToolUseID: "call_1", Name: "lookup"},
		},
	}

	err := NormalizeSessionEventKind(se)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "agent observation must have exactly one active payload")
}

func TestToolPermissionDecisionScopedByToolUseID(t *testing.T) {
	ctx := contextWithToolPermissionDecisionStore(context.Background())
	SetToolPermissionDecision(ctx, "call_1", "allowed")
	SetToolPermissionDecision(ctx, "call_2", "denied")

	assert.Equal(t, "allowed", GetToolPermissionDecision(ctx, "call_1"))
	assert.Equal(t, "denied", GetToolPermissionDecision(ctx, "call_2"))
	assert.Empty(t, GetToolPermissionDecision(ctx, "missing"))
}

func TestAgentThinkingEventIsMarkerOnly(t *testing.T) {
	assert.Equal(t, 0, reflect.TypeOf(AgentThinkingEvent{}).NumField())
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
	original := &AgentEvent{
		EventID:   uuid.NewString(),
		Timestamp: newEventTimestamp(),
		SessionEvent: &SessionEvent[*schema.Message]{
			EventID: uuid.NewString(),
			Kind:    SessionEventAgentThinking,
			AgentObservation: &AgentObservationEvent{
				Thinking: &AgentThinkingEvent{},
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
	assert.Equal(t, SessionEventAgentThinking, decoded.SessionEvent.Kind)
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
	_, err := toSessionEventChecked(&AgentEvent{
		EventID: uuid.NewString(),
		SessionEvent: &SessionEvent[*schema.Message]{
			EventID: uuid.NewString(),
			Kind:    SessionEventAgentThinking,
			AgentObservation: &AgentObservationEvent{
				Thinking: &AgentThinkingEvent{},
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "session event identity mismatch")
}

func TestSessionTimeline_NormalizeAgentSessionEventMaterializesEnvelope(t *testing.T) {
	t.Run("both ids empty", func(t *testing.T) {
		original := &SessionEvent[*schema.Message]{
			Kind: SessionEventAgentThinking,
			AgentObservation: &AgentObservationEvent{
				Thinking: &AgentThinkingEvent{},
			},
		}
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
		assert.True(t, original.Timestamp.IsZero())
	})

	t.Run("envelope id and timestamp backfill session event", func(t *testing.T) {
		ts := time.Date(2026, 5, 24, 12, 0, 0, 0, time.UTC)
		id := uuid.NewString()
		event := &AgentEvent{
			EventID:   id,
			Timestamp: ts,
			SessionEvent: &SessionEvent[*schema.Message]{
				Kind: SessionEventAgentThinking,
				AgentObservation: &AgentObservationEvent{
					Thinking: &AgentThinkingEvent{},
				},
			},
		}
		se, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		assert.Equal(t, id, se.EventID)
		assert.Equal(t, ts, se.Timestamp)
	})

	t.Run("session event id and timestamp backfill envelope", func(t *testing.T) {
		ts := time.Date(2026, 5, 24, 12, 1, 0, 0, time.UTC)
		id := uuid.NewString()
		event := &AgentEvent{
			SessionEvent: &SessionEvent[*schema.Message]{
				EventID:   id,
				Timestamp: ts,
				Kind:      SessionEventAgentThinking,
				AgentObservation: &AgentObservationEvent{
					Thinking: &AgentThinkingEvent{},
				},
			},
		}
		se, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		assert.Equal(t, id, event.EventID)
		assert.Equal(t, ts, event.Timestamp)
		assert.Equal(t, id, se.EventID)
		assert.Equal(t, ts, se.Timestamp)
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
		Agent:              &timelineErrorAgent{name: "retry-exhausted", err: &RetryExhaustedError{LastErr: errors.New("still failing"), TotalRetries: 1}},
		SessionID:          "timeline-retry-exhausted",
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "hi")
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}

	idleEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSessionStatusIdle
	})
	require.NotEmpty(t, idleEvents)
	require.NotNil(t, idleEvents[len(idleEvents)-1].Lifecycle)
	require.NotNil(t, idleEvents[len(idleEvents)-1].Lifecycle.StopReason)
	assert.Equal(t, "retries_exhausted", idleEvents[len(idleEvents)-1].Lifecycle.StopReason.Type)
}

func TestRunnerTimelineFailedStopReason(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:              &timelineErrorAgent{name: "failed", err: errors.New("boom")},
		SessionID:          "timeline-failed",
		SessionStore:       store,
		SessionPersistence: &SessionPersistenceConfig{EventFlushBatchSize: 1},
	})

	iter := runner.Query(ctx, "hi")
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}

	idleEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSessionStatusIdle
	})
	require.NotEmpty(t, idleEvents)
	require.NotNil(t, idleEvents[len(idleEvents)-1].Lifecycle)
	require.NotNil(t, idleEvents[len(idleEvents)-1].Lifecycle.StopReason)
	assert.Equal(t, "failed", idleEvents[len(idleEvents)-1].Lifecycle.StopReason.Type)
}
