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

type sessionTimelineExtensionPayload struct {
	OutcomeName string `json:"outcome_name,omitempty"`
	Attempt     int    `json:"attempt,omitempty"`
}

func init() {
	schema.RegisterName[*sessionTimelineExtensionPayload]("_eino_adk_session_timeline_extension_payload")
}

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
			se:   &SessionEvent[*schema.Message]{Lifecycle: &LifecycleEvent{State: SessionRunStateRunning}},
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
			se:   &SessionEvent[*schema.Message]{Cancel: &CancelEvent{Reason: "user"}},
			kind: SessionEventCancel,
		},
		{
			name: "agent interrupt",
			se: &SessionEvent[*schema.Message]{Interrupt: &InterruptEvent{
				Contexts: []*InterruptContext{
					{
						InterruptID: "agent:timeline-agent",
						Info:        "confirm?",
					},
				},
			}},
			kind: SessionEventInterrupt,
		},
		{
			name: "extension",
			se: &SessionEvent[*schema.Message]{
				Kind:      SessionEventKind("x.outcome.started"),
				Extension: &SessionExtensionEvent{Data: &sessionTimelineExtensionPayload{OutcomeName: "code_review"}},
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
				payload, ok := decoded.Extension.Data.(*sessionTimelineExtensionPayload)
				require.True(t, ok)
				assert.Equal(t, "code_review", payload.OutcomeName)
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

	t.Run("human readable typed round trip", func(t *testing.T) {
		se := &SessionEvent[*schema.Message]{
			EventID:   uuid.NewString(),
			Timestamp: time.Now().UTC(),
			Kind:      SessionEventKind("x.outcome.grading"),
			Extension: &SessionExtensionEvent{Data: &sessionTimelineExtensionPayload{Attempt: 1}},
		}
		require.NoError(t, NormalizeSessionEventKind(se))
		data, err := encodeSessionEvent(se)
		require.NoError(t, err)
		decoded, err := decodeSessionEvent[*schema.Message](data)
		require.NoError(t, err)
		require.NotNil(t, decoded.Extension)
		assert.Equal(t, se.Kind, decoded.Kind)
		payload, ok := decoded.Extension.Data.(*sessionTimelineExtensionPayload)
		require.True(t, ok)
		assert.Equal(t, 1, payload.Attempt)
	})
}

func TestSessionTimeline_ReconstructionIgnoresNonContextVariants(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	sid := "timeline-replay"

	msg := schema.UserMessage("hello")
	EnsureMessageID(msg)
	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusRunning, Lifecycle: &LifecycleEvent{State: SessionRunStateRunning}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg},
		{EventID: uuid.NewString(), Kind: SessionEventSpanModelRequestStart, Span: &SpanEvent{SpanID: uuid.NewString(), Kind: SpanKindModel, StartedAt: time.Now().UTC(), Model: &ModelSpanMeta{}}},
		{EventID: uuid.NewString(), Kind: SessionEventKind("x.outcome.started"), Extension: &SessionExtensionEvent{Data: &sessionTimelineExtensionPayload{Attempt: 1}}},
		{EventID: uuid.NewString(), Kind: SessionEventInterrupt, Interrupt: &InterruptEvent{
			Contexts: []*InterruptContext{
				{
					InterruptID: "agent:timeline-agent",
					Info:        "confirm?",
				},
			},
		}},
		{EventID: uuid.NewString(), Kind: SessionEventSessionError, Error: &SessionErrorEvent{Type: "transient", RetryStatus: &RetryStatus{Type: "retrying"}}},
		{EventID: uuid.NewString(), Kind: SessionEventModelContext, ModelContext: &ModelContextEvent{}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 1)
	assert.Equal(t, "hello", result.state.Messages[0].Content)
	assert.True(t, result.state.sawModelContext)
}

func TestSessionTimeline_AgentInterruptRoundTripPreservesContexts(t *testing.T) {
	se := &SessionEvent[*schema.Message]{
		EventID: uuid.NewString(),
		Interrupt: &InterruptEvent{
			Contexts: []*InterruptContext{
				{
					InterruptID: "agent:timeline-agent;tool:lookup:call_1",
					Info:        "tool info",
					ToolUseID:   "call_1",
				},
			},
		},
	}
	require.NoError(t, NormalizeSessionEventKind(se))
	require.Equal(t, SessionEventInterrupt, se.Kind)

	data, err := encodeSessionEvent(se)
	require.NoError(t, err)
	decoded, err := decodeSessionEvent[*schema.Message](data)
	require.NoError(t, err)
	require.NotNil(t, decoded.Interrupt)
	assert.Equal(t, SessionEventInterrupt, decoded.Kind)
	require.Len(t, decoded.Interrupt.Contexts, 1)
	ctx0 := decoded.Interrupt.Contexts[0]
	assert.Equal(t, "agent:timeline-agent;tool:lookup:call_1", ctx0.InterruptID)
	assert.Equal(t, "tool info", ctx0.Info)
	assert.Equal(t, "call_1", ctx0.ToolUseID)
}

func TestBuildInterruptEvent_ToolUseID(t *testing.T) {
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

	event := buildInterruptEvent(contexts)
	require.NotNil(t, event)
	require.Len(t, event.Contexts, 1)
	assert.Equal(t, "agent:timeline-agent;tool:lookup:call_1", event.Contexts[0].InterruptID)
	assert.Equal(t, "tool info", event.Contexts[0].Info)
	assert.Equal(t, "call_1", event.Contexts[0].ToolUseID)

	// Fallback to segment ID when SubID is empty.
	contexts[0].Address[1].SubID = ""
	contexts[0].Address[1].ID = "legacy-call-id"
	event = buildInterruptEvent(contexts)
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
		SessionStore:    store,
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
		return se.Kind == SessionEventInterrupt
	})
	require.Len(t, interrupts, 1)
	require.NotNil(t, interrupts[0].Interrupt)
	require.Len(t, interrupts[0].Interrupt.Contexts, 1)
	ctx0 := interrupts[0].Interrupt.Contexts[0]
	assert.Equal(t, liveInterruptContexts[0].ID, ctx0.InterruptID)
	assert.Equal(t, liveInterruptContexts[0].Info, ctx0.Info)
	requireStoredIdleStopReason(t, store.events, "interrupted")

	committedIdleEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return isCommittedIdleEvent(se)
	})
	assert.Empty(t, committedIdleEvents, "business interrupt should not commit")
}

func TestSessionTimeline_ReconstructionIncludesPartialContextAfterLatestCommittedIdle(t *testing.T) {
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
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusIdle, TurnID: "turn-1", Lifecycle: &LifecycleEvent{State: SessionRunStateIdle, StopReason: &StopReason{Type: "end_turn"}}},
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusRunning, Lifecycle: &LifecycleEvent{State: SessionRunStateRunning}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: partialUser},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: partialAssistant},
		{EventID: uuid.NewString(), Kind: SessionEventSessionError, Error: &SessionErrorEvent{Type: SessionErrorTypeModelRetry, RetryStatus: &RetryStatus{Type: "retrying"}}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
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
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusIdle, TurnID: "turn-1", Lifecycle: &LifecycleEvent{State: SessionRunStateIdle, StopReason: &StopReason{Type: "end_turn"}}},
		{EventID: uuid.NewString(), Kind: SessionEventMessageInserted, MessageInserted: &MessageInsertedEvent[*schema.Message]{
			Message:         inserted,
			BeforeMessageID: "missing-anchor",
		}},
	}
	for _, se := range events {
		require.NoError(t, store.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	_, err := reconstructSessionState[*schema.Message](ctx, store, sid, defaultLoadPageSize)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing-anchor")
}

func TestSessionTimeline_CommittedIdleIsReplayBoundaryOnly(t *testing.T) {
	committedUser := schema.UserMessage("committed user")
	partialUser := schema.UserMessage("partial user")
	for _, msg := range []*schema.Message{committedUser, partialUser} {
		EnsureMessageID(msg)
	}

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: committedUser},
		{EventID: uuid.NewString(), Kind: SessionEventSessionStatusIdle, TurnID: "turn-1", Lifecycle: &LifecycleEvent{State: SessionRunStateIdle, StopReason: &StopReason{Type: "end_turn"}}},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: partialUser},
		{EventID: uuid.NewString(), Kind: "turn_end"},
	}

	state, err := replayDurableContextEvents(events)
	require.NoError(t, err)
	require.Len(t, state.Messages, 2)
	assert.Equal(t, "committed user", state.Messages[0].Content)
	assert.Equal(t, "partial user", state.Messages[1].Content)
}

func TestWithTimelineEvents_LiveExposure(t *testing.T) {
	ctx := context.Background()
	agent := &runnerSessionAgent{
		name: "timeline-agent",
	}

	t.Run("stripped by default", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: "timeline-default", SessionStore: store})
		iter := runner.Query(ctx, "hello")
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			assert.Nil(t, event.SessionEventVariant.GetEvent())
		}
		lifecycle := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == SessionEventSessionStatusRunning || se.Kind == SessionEventSessionStatusIdle
		})
		require.Len(t, lifecycle, 2)
		requireStoredIdleStopReason(t, store.events, "end_turn")
	})

	t.Run("exposed when requested", func(t *testing.T) {
		store := newSessionHelperStore()
		runner := NewRunner(ctx, RunnerConfig{Agent: agent, SessionID: "timeline-visible", SessionStore: store})
		var kinds []SessionEventKind
		var liveUserInput bool
		iter := runner.Query(ctx, "hello", WithTimelineEvents())
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			if event.SessionEventVariant.GetEvent() != nil {
				kinds = append(kinds, event.SessionEventVariant.GetEvent().Kind)
				if event.SessionEventVariant.GetEvent().Kind == SessionEventMessage && event.SessionEventVariant.GetEvent().Message != nil &&
					event.SessionEventVariant.GetEvent().Message.Role == schema.User && event.SessionEventVariant.GetEvent().Message.Content == "hello" {
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
						SessionEventVariant: &SessionEventVariant[*schema.Message]{
							Event: &SessionEvent[*schema.Message]{
								Kind: extensionKind,
								Extension: &SessionExtensionEvent{
									Data: &sessionTimelineExtensionPayload{
										OutcomeName: "code_review",
										Attempt:     1,
									},
								},
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
			Agent:        agent,
			SessionID:    "extension-event-session-visible",
			SessionStore: store,
		})

		var liveExtension *SessionEvent[*schema.Message]
		iter := runner.Query(ctx, "hello", WithTimelineEvents())
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			if event.SessionEventVariant.GetEvent() != nil && event.SessionEventVariant.GetEvent().Kind == extensionKind {
				liveExtension = event.SessionEventVariant.GetEvent()
			}
		}

		require.NotNil(t, liveExtension)
		require.NotEmpty(t, liveExtension.EventID)
		require.NotEmpty(t, liveExtension.TurnID)
		require.NotNil(t, liveExtension.Extension)
		livePayload, ok := liveExtension.Extension.Data.(*sessionTimelineExtensionPayload)
		require.True(t, ok)
		assert.Equal(t, "code_review", livePayload.OutcomeName)
		assert.Equal(t, 1, livePayload.Attempt)

		stored := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == extensionKind
		})
		require.Len(t, stored, 1)
		assert.Equal(t, liveExtension.EventID, stored[0].EventID)
		assert.Equal(t, liveExtension.TurnID, stored[0].TurnID)
		require.NotNil(t, stored[0].Extension)
		storedPayload, ok := stored[0].Extension.Data.(*sessionTimelineExtensionPayload)
		require.True(t, ok)
		assert.Equal(t, "code_review", storedPayload.OutcomeName)
		assert.Equal(t, 1, storedPayload.Attempt)

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
			Agent:        agent,
			SessionID:    "extension-event-session-stripped",
			SessionStore: store,
		})

		iter := runner.Query(ctx, "hello")
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			require.NoError(t, event.Err)
			assert.Nil(t, event.SessionEventVariant.GetEvent())
		}

		stored := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
			return se.Kind == extensionKind
		})
		require.Len(t, stored, 1)
	})
}

func TestTypedSendEventOutsideExecutionIsNoop(t *testing.T) {
	err := SendEvent(context.Background(), &AgentEvent{
		SessionEventVariant: &SessionEventVariant[*schema.Message]{
			Event: &SessionEvent[*schema.Message]{
				Kind:      SessionEventKind("x.outcome.started"),
				Extension: &SessionExtensionEvent{},
			},
		},
	})
	require.NoError(t, err)
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
		require.NotNil(t, event.SessionEventVariant.GetEvent())
		kinds = append(kinds, event.SessionEventVariant.GetEvent().Kind)
	}

	require.Equal(t, []SessionEventKind{
		SessionEventSessionError,
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
		if event.SessionEventVariant.GetEvent() != nil && event.SessionEventVariant.GetEvent().Kind == SessionEventSpanModelRequestEnd {
			spanEnd = event.SessionEventVariant.GetEvent()
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
		SessionEventVariant: &SessionEventVariant[*schema.Message]{
			Event: &SessionEvent[*schema.Message]{
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
		},
	}

	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(original))

	var decoded AgentEvent
	require.NoError(t, gob.NewDecoder(&buf).Decode(&decoded))
	require.NotNil(t, decoded.SessionEventVariant.GetEvent())
	assert.Equal(t, original.SessionEventVariant.GetEvent().EventID, decoded.SessionEventVariant.GetEvent().EventID)
	assert.Equal(t, SessionEventSpanToolCallStart, decoded.SessionEventVariant.GetEvent().Kind)
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
		if event.SessionEventVariant.GetEvent() != nil && event.SessionEventVariant.GetEvent().Kind == SessionEventSessionError {
			require.NotNil(t, event.SessionEventVariant.GetEvent().Error)
			assert.Equal(t, "policy rejected", event.SessionEventVariant.GetEvent().Error.Message)
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
		if event.SessionEventVariant.GetEvent() == nil {
			continue
		}
		switch event.SessionEventVariant.GetEvent().Kind {
		case SessionEventSpanModelRequestStart:
			starts = append(starts, event.SessionEventVariant.GetEvent())
		case SessionEventSessionError:
			if event.SessionEventVariant.GetEvent().Error != nil && event.SessionEventVariant.GetEvent().Error.Type == SessionErrorTypeModelFailover {
				failoverErrors = append(failoverErrors, event.SessionEventVariant.GetEvent())
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
	err := validateAgentSessionEventIdentity(&AgentEvent{
		SessionEventVariant: &SessionEventVariant[*schema.Message]{
			Event: &SessionEvent[*schema.Message]{
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
			MessageStreamRef: &MessageStreamRef{
				EventID:   uuid.NewString(),
				Timestamp: now,
				Kind:      SessionEventMessage,
			},
		},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
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

	t.Run("id empty", func(t *testing.T) {
		original := makeToolStartSpan()
		event := &AgentEvent{SessionEventVariant: &SessionEventVariant[*schema.Message]{Event: original}}
		se, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		require.NotEmpty(t, se.EventID)
		assert.Equal(t, se.EventID, event.SessionEventVariant.GetEvent().EventID)
		require.False(t, se.Timestamp.IsZero())
		assert.Equal(t, se.Timestamp, event.SessionEventVariant.GetEvent().Timestamp)
		assert.Empty(t, original.EventID)
	})

	t.Run("model context event normalizes without mutation", func(t *testing.T) {
		original := &SessionEvent[*schema.Message]{Kind: SessionEventModelContext, ModelContext: &ModelContextEvent{}}
		event := &AgentEvent{SessionEventVariant: &SessionEventVariant[*schema.Message]{Event: original}}
		se, err := normalizeAgentSessionEvent(event)
		require.NoError(t, err)
		require.NotNil(t, se.ModelContext)
		require.NotNil(t, event.SessionEventVariant.GetEvent().ModelContext)
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
		if event.SessionEventVariant.GetEvent() != nil && event.SessionEventVariant.GetEvent().Kind == SessionEventSpanModelRequestStart {
			starts = append(starts, event.SessionEventVariant.GetEvent())
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
		if event.SessionEventVariant.GetEvent() == nil || event.SessionEventVariant.GetEvent().Kind != SessionEventSessionError || event.SessionEventVariant.GetEvent().Error == nil {
			continue
		}
		switch event.SessionEventVariant.GetEvent().Error.Type {
		case SessionErrorTypeModelRetry:
			if event.SessionEventVariant.GetEvent().Error.RetryStatus != nil && event.SessionEventVariant.GetEvent().Error.RetryStatus.Type == "exhausted" {
				retryExhausted = true
			}
		case SessionErrorTypeModelFailover:
			if event.SessionEventVariant.GetEvent().Error.RetryStatus != nil && event.SessionEventVariant.GetEvent().Error.RetryStatus.Type == "retrying" {
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
		Agent:        &timelineErrorAgent{name: "retry-exhausted", err: &RetryExhaustedError{LastErr: errors.New("still failing"), TotalRetries: 1}},
		SessionID:    "timeline-retry-exhausted",
		SessionStore: store,
	})

	iter := runner.Query(ctx, "hi")
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}

	requireStoredIdleStopReason(t, store.events, "retries_exhausted")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return isCommittedIdleEvent(se)
	})
	assert.Empty(t, turnEnds, "retry exhaustion should not commit")
}

func TestRunnerTimelineFailedStopReason(t *testing.T) {
	ctx := context.Background()
	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        &timelineErrorAgent{name: "failed", err: errors.New("boom")},
		SessionID:    "timeline-failed",
		SessionStore: store,
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
		assert.NotContains(t, err.Error(), "missing committed idle")
	}

	sessionErrors := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return se.Kind == SessionEventSessionError
	})
	require.NotEmpty(t, sessionErrors)
	require.NotNil(t, sessionErrors[len(sessionErrors)-1].Error)
	assert.Equal(t, SessionErrorTypeFatal, sessionErrors[len(sessionErrors)-1].Error.Type)
	assert.Equal(t, "boom", sessionErrors[len(sessionErrors)-1].Error.Message)
	requireStoredIdleStopReason(t, store.events, "failed")

	committedIdleEvents := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return isCommittedIdleEvent(se)
	})
	assert.Empty(t, committedIdleEvents, "failed turn should not commit")
}

func TestRunnerTimelineModelCallFatalDoesNotRequireCommitMarker(t *testing.T) {
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
		Agent:        agent,
		SessionID:    "timeline-fatal-model",
		SessionStore: store,
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
		assert.NotContains(t, err.Error(), "missing committed idle")
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
		return isCommittedIdleEvent(se)
	})
	assert.Empty(t, turnEnds, "fatal model call should not commit")
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
		SessionStore:    store,
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
		return se.Kind == SessionEventCancel
	})
	require.Len(t, userInterrupts, 1)
	assert.Equal(t, SessionEventKind("cancel"), userInterrupts[0].Kind)
	require.NotNil(t, userInterrupts[0].Cancel)
	assert.Equal(t, "cancelled", userInterrupts[0].Cancel.Reason)
	requireStoredIdleStopReason(t, store.events, "cancelled")

	turnEnds := filterStoredSessionEvents(t, store.events, func(se *SessionEvent[*schema.Message]) bool {
		return isCommittedIdleEvent(se)
	})
	assert.Empty(t, turnEnds, "cancelled turn should not commit")
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
		Agent:        agent,
		SessionID:    "tool-span-around",
		SessionStore: store,
	})
	iter := runner.Query(ctx, "go", WithTimelineEvents())
	var liveToolEnd *SessionEvent[*schema.Message]
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.SessionEventVariant.GetEvent() != nil && event.SessionEventVariant.GetEvent().Kind == SessionEventSpanToolCallEnd {
			liveToolEnd = event.SessionEventVariant.GetEvent()
		}
	}
	require.NotNil(t, liveToolEnd, "expected live tool_call_end span emission")
	require.NotNil(t, liveToolEnd.Span)
	require.NotNil(t, liveToolEnd.Span.Tool)
	assert.Equal(t, "tool_span_tool", liveToolEnd.Span.Tool.Name)
	assert.Equal(t, "ok", liveToolEnd.Span.Status)

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

// TestSessionEventIDGenerator_CustomToolResultBusinessID 验证：configured
// generator 看到 tool result message 草稿时返回业务 ID，持久化的 message
// EventID 与对应 tool span end 的 ToolResultMessageEventID 必须等于该业务 ID
// （§8 CustomToolResult 验收）。
func TestSessionEventIDGenerator_CustomToolResultBusinessID(t *testing.T) {
	ctx := context.Background()
	testTool := &invokableTestTool{name: "tool_span_tool", result: "tool result"}
	mockModel := &mockToolCallingModel{toolCallName: "tool_span_tool"}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ToolSpanGenAgent",
		Description: "tool span agent with id generator",
		Model:       mockModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{testTool}},
		},
	})
	require.NoError(t, err)

	const toolResultBusinessID = "custom-result-id"
	gen := func(_ context.Context, e *SessionEvent[*schema.Message]) (string, error) {
		if e != nil && e.Kind == SessionEventMessage && e.Message != nil && e.Message.Role == schema.Tool {
			return toolResultBusinessID, nil
		}
		return DefaultSessionEventIDGenerator[*schema.Message](ctx, e)
	}

	store := newSessionHelperStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:        agent,
		SessionID:    "tool-result-business-id",
		SessionStore: store,
		SessionConfig: &SessionConfig[*schema.Message]{
			EventIDGenerator: gen,
		},
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
		toolResultMsg *SessionEvent[*schema.Message]
		toolEnd       *SessionEvent[*schema.Message]
	)
	for _, se := range stored {
		switch {
		case se.Kind == SessionEventMessage && se.Message != nil && se.Message.Role == schema.Tool:
			toolResultMsg = se
		case se.Kind == SessionEventSpanToolCallEnd:
			toolEnd = se
		}
	}
	require.NotNil(t, toolResultMsg, "expected persisted tool result message")
	require.NotNil(t, toolEnd, "expected tool_call_end span")
	require.NotNil(t, toolEnd.Span)
	require.NotNil(t, toolEnd.Span.Tool)

	assert.Equal(t, toolResultBusinessID, toolResultMsg.EventID,
		"tool result message must adopt the generator-supplied business ID")
	assert.Equal(t, toolResultBusinessID, toolEnd.Span.Tool.ToolResultMessageEventID,
		"tool span end ToolResultMessageEventID must match the tool result message business ID")
}

type kindsRecordingStore struct {
	inner         *sessionHelperStore
	recordedKinds [][]SessionEventKind
}

func (s *kindsRecordingStore) loadEvents(ctx context.Context, opts *LoadSessionEventsRequest) (*LoadSessionEventsResult[*schema.Message], error) {
	if opts != nil {
		s.recordedKinds = append(s.recordedKinds, opts.Kinds)
	}
	return s.inner.LoadEventsForSession(ctx, "", opts)
}

func (s *kindsRecordingStore) appendEvents(ctx context.Context, events []*SessionEvent[*schema.Message]) error {
	return s.inner.AppendEventsForSession(ctx, "", events)
}

func (s *kindsRecordingStore) close(context.Context) error { return nil }

func TestSessionTimeline_ReconstructionUsesKindFilter(t *testing.T) {
	ctx := context.Background()
	inner := newSessionHelperStore()
	wrapper := &kindsRecordingStore{inner: inner}
	sid := "timeline-kind-filter"

	msg1 := schema.UserMessage("hello")
	EnsureMessageID(msg1)
	msg2 := schema.AssistantMessage("world", nil)
	EnsureMessageID(msg2)

	events := []*SessionEvent[*schema.Message]{
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg1},
		{EventID: uuid.NewString(), Kind: SessionEventMessage, Message: msg2},
		{EventID: uuid.NewString(), Kind: SessionEventSpanModelRequestStart, Span: &SpanEvent{SpanID: uuid.NewString(), Kind: SpanKindModel, StartedAt: time.Now().UTC(), Model: &ModelSpanMeta{}}},
		{EventID: uuid.NewString(), Kind: SessionEventModelContext, ModelContext: &ModelContextEvent{}},
	}
	for _, se := range events {
		require.NoError(t, inner.AppendEventsForSession(ctx, sid, []*SessionEvent[*schema.Message]{se}))
	}

	result, err := reconstructSessionState[*schema.Message](ctx, wrapper, sid, defaultLoadPageSize)
	require.NoError(t, err)

	// All recorded Kinds slices should equal sessionReplayEventKinds.
	require.NotEmpty(t, wrapper.recordedKinds)
	for _, kinds := range wrapper.recordedKinds {
		assert.Equal(t, sessionReplayEventKinds, kinds)
	}

	// Verify reconstruction result.
	require.NotNil(t, result)
	require.NotNil(t, result.state)
	require.Len(t, result.state.Messages, 2)
	assert.Equal(t, "hello", result.state.Messages[0].Content)
	assert.Equal(t, "world", result.state.Messages[1].Content)
	assert.True(t, result.state.sawModelContext)
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
		Agent:        agent,
		SessionID:    "tool-span-stream",
		SessionStore: store,
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
