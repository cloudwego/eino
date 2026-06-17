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

package session_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
)

func TestInMemoryStoreConformance(t *testing.T) {
	session.RunConformanceTests[*schema.Message](t, func(testing.TB) adk.SessionEventStore[*schema.Message] {
		return session.NewInMemoryStore[*schema.Message](nil)
	}, func(content string) *schema.Message {
		return schema.UserMessage(content)
	})
	session.RunSerializerConformanceTests[*schema.Message](t, func(_ testing.TB, serializer schema.Serializer) adk.SessionEventStore[*schema.Message] {
		return session.NewInMemoryStore[*schema.Message](&session.InMemoryStoreConfig{EventSerializer: serializer})
	}, func(content string) *schema.Message {
		return schema.UserMessage(content)
	})
}

func TestInMemoryStoreCheckpointSetGetDelete(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)

	_, exists, err := store.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, store.Set(ctx, "k", []byte("payload")))

	got, exists, err := store.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, []byte("payload"), got)

	got[0] = 'X'
	again, _, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), again, "Get must return an independent copy")

	require.NoError(t, store.Delete(ctx, "k"))
	_, exists, err = store.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, exists)
}

func TestInMemoryStoreKindFilterAndPagination(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)
	events := []*adk.SessionEvent[*schema.Message]{
		testMessageEvent("e1", "one"),
		testSpanEvent("e2"),
		testTurnEndEvent("e3", "turn-1"),
		testMessageEvent("e4", "four"),
	}
	err := store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{SessionID: "s", Events: events})
	require.NoError(t, err)

	res, err := store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{
		SessionID: "s",
		After:     "e2",
		Kinds:     []adk.SessionEventKind{adk.SessionEventMessage, adk.SessionEventTurnEnd},
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "e3", res.Events[0].EventID)
	assert.Equal(t, "e3", res.Next)
}

func TestInMemoryStoreLoadReturnsIndependentEvents(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)
	err := store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{SessionID: "s", Events: []*adk.SessionEvent[*schema.Message]{
		testMessageEvent("e1", "one"),
	}})
	require.NoError(t, err)

	first, err := store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{SessionID: "s"})
	require.NoError(t, err)
	first.Events[0].EventID = "mutated"

	second, err := store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{SessionID: "s"})
	require.NoError(t, err)
	assert.Equal(t, "e1", second.Events[0].EventID)
}

func TestInMemoryStoreValidationReplayAndReversePagination(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)

	require.NoError(t, store.AppendEvents(ctx, nil))

	events := []*adk.SessionEvent[*schema.Message]{
		testMessageEvent("e1", "one"),
		testSpanEvent("e2"),
		testTurnEndEvent("e3", "turn-1"),
	}
	err := store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "s",
		Events:    events,
	})
	require.NoError(t, err)

	err = store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "s",
		Events:    []*adk.SessionEvent[*schema.Message]{testMessageEvent("e4", "four")},
	})
	require.NoError(t, err)

	err = store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "s2",
		Events:    []*adk.SessionEvent[*schema.Message]{nil},
	})
	require.ErrorIs(t, err, adk.ErrInvalidEventID)

	err = store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "s2",
		Events: []*adk.SessionEvent[*schema.Message]{
			testMessageEvent("dup", "one"),
			testMessageEvent("dup", "two"),
		},
	})
	require.ErrorIs(t, err, adk.ErrDuplicateEventID)

	err = store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "s",
		Events:    []*adk.SessionEvent[*schema.Message]{testMessageEvent("e1", "duplicate existing")},
	})
	require.ErrorIs(t, err, adk.ErrDuplicateEventID)

	err = store.AppendEvents(ctx, &adk.AppendSessionEventsRequest[*schema.Message]{
		SessionID: "s2",
		Events:    []*adk.SessionEvent[*schema.Message]{{EventID: "invalid-kind"}},
	})
	require.Error(t, err)

	reverseEmpty, err := store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{SessionID: "empty", Reverse: true})
	require.NoError(t, err)
	assert.Empty(t, reverseEmpty.Events)

	_, err = store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{SessionID: "s", After: "missing"})
	require.ErrorIs(t, err, adk.ErrEventIDOutOfRange)
	_, err = store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{SessionID: "s", Reverse: true, After: "missing"})
	require.ErrorIs(t, err, adk.ErrEventIDOutOfRange)

	reverse, err := store.LoadEvents(ctx, &adk.LoadSessionEventsRequest{
		SessionID: "s",
		Reverse:   true,
		After:     "e4",
		Limit:     1,
	})
	require.NoError(t, err)
	require.Len(t, reverse.Events, 1)
	assert.Equal(t, "e3", reverse.Events[0].EventID)
	assert.Equal(t, "e3", reverse.Next)
}

func testMessageEvent(id, content string) *adk.SessionEvent[*schema.Message] {
	return &adk.SessionEvent[*schema.Message]{
		EventID: id,
		Kind:    adk.SessionEventMessage,
		Message: schema.UserMessage(content),
	}
}

func testTurnEndEvent(id, turnID string) *adk.SessionEvent[*schema.Message] {
	return &adk.SessionEvent[*schema.Message]{
		EventID: id,
		Kind:    adk.SessionEventTurnEnd,
		TurnID:  turnID,
		TurnEnd: &adk.TurnEndState[*schema.Message]{},
	}
}

func testSpanEvent(id string) *adk.SessionEvent[*schema.Message] {
	return &adk.SessionEvent[*schema.Message]{
		EventID: id,
		Kind:    adk.SessionEventSpanModelRequestStart,
		Span: &adk.SpanEvent{
			Kind:      adk.SpanKindModel,
			StartedAt: time.Now(),
			Model:     &adk.ModelSpanMeta{},
		},
	}
}
