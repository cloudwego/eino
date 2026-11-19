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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestCloneSessionEventPayloads(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		testCloneSessionEventPayloads(t, schema.UserMessage("original"), func(m *schema.Message) { m.Content = "changed" })
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		message := &schema.AgenticMessage{ContentBlocks: []*schema.ContentBlock{{UserInputText: &schema.UserInputText{Text: "original"}}}}
		testCloneSessionEventPayloads(t, message, func(m *schema.AgenticMessage) { m.ContentBlocks[0].UserInputText.Text = "changed" })
	})
}

func testCloneSessionEventPayloads[M MessageType](t *testing.T, message M, mutate func(M)) {
	t.Helper()
	opaque := &struct{ Value string }{Value: "shared"}
	messages := []M{message}
	// Populate every payload at once: cloning preserves data even when the event
	// would be rejected at the write boundary for having multiple active payloads.
	source := &SessionEvent[M]{
		EventID: "event", TurnID: "turn", Timestamp: time.Now(), Kind: SessionEventMessage,
		Extra: map[string]any{"value": opaque}, Message: message,
		MessageStreamIncomplete: &MessageStreamIncompleteEvent[M]{Message: message, Error: "stream"},
		MessagesReplaced:        &messages,
		MessageUpdated:          &MessageUpdatedEvent[M]{MessageID: "message", Message: message},
		MessageInserted:         &MessageInsertedEvent[M]{BeforeMessageID: "before", Message: message},
		MessagesDeleted:         &MessagesDeletedEvent{MessageIDs: []string{"deleted"}},
		Rollback:                &SessionRollbackEvent{ToEventID: "target"},
		Lifecycle:               &LifecycleEvent{State: SessionRunStateIdle, StopReason: &StopReason{Type: StopReasonEndTurn}},
		Error:                   &SessionErrorEvent{RetryStatus: &RetryStatus{Type: RetryStatusRetrying}},
		Span: &SpanEvent{
			Model: &ModelSpanMeta{Usage: &schema.TokenUsage{TotalTokens: 10}, Timeout: &ModelTimeoutMeta{TimeoutMS: 100}},
			Tool:  &ToolSpanMeta{Name: "tool"},
		},
		Cancel:    &CancelEvent{Reason: "cancel"},
		Interrupt: &InterruptEvent{Contexts: []*InterruptContext{nil, {InterruptID: "interrupt", Info: opaque}}},
		Extension: &SessionExtensionEvent{Data: opaque},
	}
	cloned := cloneSessionEvent(source)
	require.Equal(t, source, cloned)
	require.Same(t, opaque, cloned.Extra["value"])
	require.Same(t, opaque, cloned.Interrupt.Contexts[1].Info)
	require.Same(t, opaque, cloned.Extension.Data)
	mutate(message)
	require.NotEqual(t, source.Message, cloned.Message)
	require.Equal(t, cloned.Message, cloned.MessageStreamIncomplete.Message)
	require.Equal(t, cloned.Message, (*cloned.MessagesReplaced)[0])
	require.Equal(t, cloned.Message, cloned.MessageUpdated.Message)
	require.Equal(t, cloned.Message, cloned.MessageInserted.Message)

	source.Extra["value"] = "replaced"
	source.MessageUpdated.MessageID = "changed"
	source.MessageInserted.BeforeMessageID = "changed"
	source.MessageStreamIncomplete.Error = "changed"
	source.MessagesDeleted.MessageIDs[0] = "changed"
	source.Rollback.ToEventID = "changed"
	source.Lifecycle.StopReason.Type = "changed"
	source.Error.RetryStatus.Type = "changed"
	source.Span.Model.Usage.TotalTokens = 0
	source.Span.Model.Timeout.TimeoutMS = 0
	source.Span.Tool.Name = "changed"
	source.Cancel.Reason = "changed"
	source.Interrupt.Contexts[1].InterruptID = "changed"
	source.Interrupt.Contexts[1].Info = "replaced"
	source.Extension.Data = "replaced"
	require.Same(t, opaque, cloned.Extra["value"])
	require.Equal(t, "message", cloned.MessageUpdated.MessageID)
	require.Equal(t, "before", cloned.MessageInserted.BeforeMessageID)
	require.Equal(t, "stream", cloned.MessageStreamIncomplete.Error)
	require.Equal(t, []string{"deleted"}, cloned.MessagesDeleted.MessageIDs)
	require.Equal(t, "target", cloned.Rollback.ToEventID)
	require.Equal(t, StopReasonEndTurn, cloned.Lifecycle.StopReason.Type)
	require.Equal(t, RetryStatusRetrying, cloned.Error.RetryStatus.Type)
	require.Equal(t, 10, cloned.Span.Model.Usage.TotalTokens)
	require.Equal(t, int64(100), cloned.Span.Model.Timeout.TimeoutMS)
	require.Equal(t, "tool", cloned.Span.Tool.Name)
	require.Equal(t, "cancel", cloned.Cancel.Reason)
	require.Equal(t, "interrupt", cloned.Interrupt.Contexts[1].InterruptID)
	require.Same(t, opaque, cloned.Interrupt.Contexts[1].Info)
	require.Same(t, opaque, cloned.Extension.Data)
}

func TestCloneSessionEventPresence(t *testing.T) {
	nilMessages := []Message(nil)
	emptyMessages := []Message{}
	withNilMessage := []Message{nil}
	for _, replaced := range []*[]Message{nil, &nilMessages, &emptyMessages, &withNilMessage} {
		source := &SessionEvent[Message]{MessagesReplaced: replaced}
		cloned := cloneSessionEvent(source)
		require.Equal(t, source, cloned)
		if replaced != nil {
			require.NotSame(t, replaced, cloned.MessagesReplaced)
		}
	}
	for _, source := range []*SessionEvent[Message]{
		{Extra: map[string]any{}, MessagesDeleted: &MessagesDeletedEvent{MessageIDs: []string{}}, Interrupt: &InterruptEvent{Contexts: []*InterruptContext{}}},
		{MessagesDeleted: &MessagesDeletedEvent{}, Interrupt: &InterruptEvent{}},
	} {
		require.Equal(t, source, cloneSessionEvent(source))
	}
}

func TestSessionPersisterCloneIsolatesQueuedFields(t *testing.T) {
	store := newSessionHelperStore()
	handle := &localSessionHandle[Message]{store: store, sessionID: "clone"}
	persister := newSessionEventPersister[Message](context.Background(), handle, "clone")
	event := &SessionEvent[Message]{
		EventID: "original", Kind: SessionEventMessage,
		Message: &schema.Message{Content: "original", Extra: map[string]any{"key": "original"}},
		Extra:   map[string]any{"key": "original"},
	}
	require.NoError(t, persister.enqueueAsync(event))
	event.EventID = "changed"
	event.Message.Content = "changed"
	event.Message.Extra["key"] = "changed"
	event.Extra["key"] = "changed"
	require.NoError(t, persister.closeAndWait())
	require.Len(t, store.events, 1)
	stored, err := decodeSessionEvent[Message](store.events[0].Data)
	require.NoError(t, err)
	require.Equal(t, "original", stored.EventID)
	require.Equal(t, "original", stored.Message.Content)
	require.Equal(t, "original", stored.Message.Extra["key"])
	require.Equal(t, "original", stored.Extra["key"])
}

func TestSessionPersisterDefersValidationAndSerializationToWrite(t *testing.T) {
	tests := []struct {
		name      string
		event     *SessionEvent[Message]
		errorText string
	}{
		{"missing kind", &SessionEvent[Message]{EventID: "event", Message: schema.UserMessage("hello")}, "non-empty Kind"},
		{"missing payload", &SessionEvent[Message]{EventID: "event", Kind: SessionEventMessage}, "exactly one"},
		{"unregistered extra", &SessionEvent[Message]{EventID: "event", Kind: SessionEventMessage, Message: schema.UserMessage("hello"), Extra: map[string]any{"custom": sessionEventExtraUnregisteredValue{Reason: "custom"}}}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newSessionHelperStore()
			handle := &localSessionHandle[Message]{store: store, sessionID: "clone"}
			persister := newSessionEventPersister[Message](context.Background(), handle, "clone")
			require.NoError(t, persister.enqueueAsync(tt.event))
			err := persister.closeAndWait()
			require.Error(t, err)
			if tt.errorText != "" {
				require.ErrorContains(t, err, tt.errorText)
			}
			require.Same(t, err, persister.getErr())
			require.Error(t, persister.enqueueAsync(validTestPayload()))
			require.Empty(t, store.events)
		})
	}
}

// Compare copying with the previous encode/decode/validate snapshot path.
func BenchmarkSessionEventClone(b *testing.B) {
	b.Run("Message", func(b *testing.B) {
		benchmarkSessionEventClone(b, &SessionEvent[Message]{
			EventID: "event", Kind: SessionEventMessage, Timestamp: time.Now(),
			Extra: map[string]any{"tenant": "tenant"},
			Message: &schema.Message{Role: schema.Assistant, Content: "hello", Extra: map[string]any{"id": "message"},
				ToolCalls:    []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "search", Arguments: `{"query":"hello"}`}}},
				ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: 42}},
			},
		})
	})
	b.Run("AgenticMessage", func(b *testing.B) {
		benchmarkSessionEventClone(b, &SessionEvent[AgenticMessage]{
			EventID: "event", Kind: SessionEventMessage, Timestamp: time.Now(),
			Message: &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{{Type: schema.ContentBlockTypeAssistantGenText, AssistantGenText: &schema.AssistantGenText{Text: "hello"}}},
				ResponseMeta:  &schema.AgenticResponseMeta{TokenUsage: &schema.TokenUsage{TotalTokens: 42}},
			},
		})
	})
}

var sessionCloneBenchmarkSink any

func benchmarkSessionEventClone[M MessageType](b *testing.B, event *SessionEvent[M]) {
	b.Run("Explicit", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			sessionCloneBenchmarkSink = cloneSessionEvent(event)
		}
	})
	b.Run("CodecRoundTrip", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			data, err := encodeSessionEvent(event)
			if err != nil {
				b.Fatal(err)
			}
			snapshot, err := decodeSessionEvent[M](data)
			if err != nil {
				b.Fatal(err)
			}
			if err := validateEmittedSessionEventKind(snapshot); err != nil {
				b.Fatal(err)
			}
			sessionCloneBenchmarkSink = snapshot
		}
	})
}
