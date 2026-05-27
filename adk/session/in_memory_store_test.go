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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
)

func TestInMemoryStoreConformance(t *testing.T) {
	session.RunConformanceTests(t, func(testing.TB) adk.SessionStore {
		return session.NewInMemoryStore()
	})
}

func TestInMemoryStoreCheckpointSetGetDelete(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()

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

func TestInMemoryStoreForwardKindFilter(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()

	events := []adk.SessionEventPayload{
		{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte("d1")},
		{EventID: "e2", Kind: adk.SessionEventSpanModelRequestStart, Data: []byte("d2")},
		{EventID: "e3", Kind: adk.SessionEventTurnEnd, Data: []byte("d3")},
		{EventID: "e4", Kind: adk.SessionEventSessionStatusIdle, Data: []byte("d4")},
		{EventID: "e5", Kind: adk.SessionEventMessage, Data: []byte("d5")},
	}
	require.NoError(t, store.AppendEvents(ctx, "s", events))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Kinds: []adk.SessionEventKind{adk.SessionEventMessage, adk.SessionEventTurnEnd},
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 3)
	assert.Equal(t, "e1", res.Events[0].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[0].Kind)
	assert.Equal(t, "e3", res.Events[1].EventID)
	assert.Equal(t, adk.SessionEventTurnEnd, res.Events[1].Kind)
	assert.Equal(t, "e5", res.Events[2].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[2].Kind)
}

func TestInMemoryStoreExtensionKindFilter(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()
	extensionKind := adk.SessionEventKind("x.outcome.started")

	events := []adk.SessionEventPayload{
		{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte("d1")},
		{EventID: "e2", Kind: extensionKind, Data: []byte("d2")},
		{EventID: "e3", Kind: adk.SessionEventKind("x.ticket.updated"), Data: []byte("d3")},
		{EventID: "e4", Kind: extensionKind, Data: []byte("d4")},
	}
	require.NoError(t, store.AppendEvents(ctx, "s", events))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Kinds: []adk.SessionEventKind{extensionKind},
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	assert.Equal(t, "e2", res.Events[0].EventID)
	assert.Equal(t, extensionKind, res.Events[0].Kind)
	assert.Equal(t, "e4", res.Events[1].EventID)
	assert.Equal(t, extensionKind, res.Events[1].Kind)
}

func TestInMemoryStoreReverseKindFilter(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()

	events := []adk.SessionEventPayload{
		{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte("d1")},
		{EventID: "e2", Kind: adk.SessionEventSpanModelRequestStart, Data: []byte("d2")},
		{EventID: "e3", Kind: adk.SessionEventTurnEnd, Data: []byte("d3")},
		{EventID: "e4", Kind: adk.SessionEventSessionStatusIdle, Data: []byte("d4")},
		{EventID: "e5", Kind: adk.SessionEventMessage, Data: []byte("d5")},
	}
	require.NoError(t, store.AppendEvents(ctx, "s", events))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Reverse: true,
		Kinds:   []adk.SessionEventKind{adk.SessionEventMessage, adk.SessionEventTurnEnd},
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 3)
	assert.Equal(t, "e5", res.Events[0].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[0].Kind)
	assert.Equal(t, "e3", res.Events[1].EventID)
	assert.Equal(t, adk.SessionEventTurnEnd, res.Events[1].Kind)
	assert.Equal(t, "e1", res.Events[2].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[2].Kind)
}

func TestInMemoryStoreCursorOverFullLogWithKindFilter(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()

	events := []adk.SessionEventPayload{
		{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte("d1")},
		{EventID: "e2", Kind: adk.SessionEventSpanModelRequestStart, Data: []byte("d2")},
		{EventID: "e3", Kind: adk.SessionEventTurnEnd, Data: []byte("d3")},
		{EventID: "e4", Kind: adk.SessionEventMessage, Data: []byte("d4")},
	}
	require.NoError(t, store.AppendEvents(ctx, "s", events))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		After: "e2",
		Kinds: []adk.SessionEventKind{adk.SessionEventMessage, adk.SessionEventTurnEnd},
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 2)
	assert.Equal(t, "e3", res.Events[0].EventID)
	assert.Equal(t, adk.SessionEventTurnEnd, res.Events[0].Kind)
	assert.Equal(t, "e4", res.Events[1].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[1].Kind)
}

func TestInMemoryStoreFilteredPagination(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()

	events := []adk.SessionEventPayload{
		{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte("d1")},
		{EventID: "e2", Kind: adk.SessionEventSpanModelRequestStart, Data: []byte("d2")},
		{EventID: "e3", Kind: adk.SessionEventTurnEnd, Data: []byte("d3")},
		{EventID: "e4", Kind: adk.SessionEventSpanToolCallStart, Data: []byte("d4")},
		{EventID: "e5", Kind: adk.SessionEventMessage, Data: []byte("d5")},
	}
	require.NoError(t, store.AppendEvents(ctx, "s", events))

	kinds := []adk.SessionEventKind{adk.SessionEventMessage, adk.SessionEventTurnEnd}

	// First page
	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Limit: 1,
		Kinds: kinds,
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "e1", res.Events[0].EventID)
	assert.Equal(t, "e1", res.Next)

	// Second page
	res, err = store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Limit: 1,
		After: "e1",
		Kinds: kinds,
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "e3", res.Events[0].EventID)
	assert.Equal(t, adk.SessionEventTurnEnd, res.Events[0].Kind)
	assert.Equal(t, "e3", res.Next)

	// Third page
	res, err = store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
		Limit: 1,
		After: "e3",
		Kinds: kinds,
	})
	require.NoError(t, err)
	require.Len(t, res.Events, 1)
	assert.Equal(t, "e5", res.Events[0].EventID)
	assert.Equal(t, adk.SessionEventMessage, res.Events[0].Kind)
	assert.Equal(t, "", res.Next)
}
