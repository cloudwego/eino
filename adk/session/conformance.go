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

// Package session provides SessionService implementations and a reusable
// conformance test suite for validating SessionService implementations.
package session

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// RunConformanceTests validates the SessionService contract shared by
// Runner-managed session persistence implementations.
//
// The contract assumes single-writer-per-session: tests do NOT exercise
// concurrent AppendEvents calls for the same sessionID.
func RunConformanceTests[M adk.MessageType](
	t *testing.T,
	factory func(testing.TB) adk.SessionService[M],
	makeMessage func(content string) M,
) {
	t.Helper()

	t.Run("AppendEvents and forward LoadEvents", func(t *testing.T) { testAppendAndForwardLoad(t, factory, makeMessage) })
	t.Run("LoadEvents reverse pagination", func(t *testing.T) { testReversePagination(t, factory, makeMessage) })
	t.Run("After forward pagination", func(t *testing.T) { testForwardPagination(t, factory, makeMessage) })
	t.Run("sessionID isolates events", func(t *testing.T) { testSessionIsolation(t, factory, makeMessage) })
	t.Run("Empty session returns no events", func(t *testing.T) { testEmptySession(t, factory) })
	t.Run("AppendEvents is idempotent on duplicate EventID", func(t *testing.T) { testIdempotentAppend(t, factory, makeMessage) })
	t.Run("AppendEvents skips duplicate EventID within same batch", func(t *testing.T) { testIdempotentAppendWithinBatch(t, factory, makeMessage) })
	t.Run("AppendEvents rejects empty EventID with ErrInvalidEventID", func(t *testing.T) { testRejectEmptyEventID(t, factory, makeMessage) })
	t.Run("After resumes by EventID forward", func(t *testing.T) { testAfterForward(t, factory, makeMessage) })
	t.Run("After resumes by EventID reverse", func(t *testing.T) { testAfterReverse(t, factory, makeMessage) })
	t.Run("Unknown After returns ErrEventIDOutOfRange", func(t *testing.T) { testUnknownAfter(t, factory, makeMessage) })
	t.Run("Empty page when After=last forward and After=first reverse", func(t *testing.T) { testEmptyPageBoundary(t, factory, makeMessage) })
	t.Run("Extension kind filters correctly", func(t *testing.T) { testExtensionKindFilter(t, factory) })
	t.Run("event body round-trips", func(t *testing.T) { testEventBodyRoundTrip(t, factory, makeMessage) })
}

// RunSerializerConformanceTests validates that a concrete SessionService
// implementation honors its implementation-local serializer configuration.
func RunSerializerConformanceTests[M adk.MessageType](
	t *testing.T,
	factory func(testing.TB, schema.Serializer) adk.SessionService[M],
	makeMessage func(content string) M,
) {
	t.Helper()
	t.Run("custom serializer is honored", func(t *testing.T) {
		serializer := &countingEventSerializer{inner: &schema.HumanReadableSerializer{}}
		store := factory(t, serializer)
		if store == nil {
			t.Fatalf("factory returned nil SessionService")
		}

		ctx := context.Background()
		event := messageEvent("custom-serializer-1", makeMessage("custom serializer"))
		requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{event}))
		if serializer.marshalCount == 0 {
			t.Fatalf("custom serializer Marshal was not called")
		}

		res, err := store.LoadEvents(ctx, "s", nil)
		requireNoError(t, err)
		if serializer.unmarshalCount == 0 {
			t.Fatalf("custom serializer Unmarshal was not called")
		}
		requireEventsEqual(t, []*adk.SessionEvent[M]{event}, res.Events)
	})
}

func testAppendAndForwardLoad[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := messageEvent("e1", makeMessage("first"))
	second := turnEndEvent[M]("e2", "turn-1")
	third := messageEvent("e3", makeMessage("third"))
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{first, second}))
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{third}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	if res == nil {
		t.Fatalf("LoadEvents returned nil result")
	}
	requireEventsEqual(t, []*adk.SessionEvent[M]{first, second, third}, res.Events)
}

func testExtensionKindFilter[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M]) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := extensionEvent[M]("custom-1", "x.conformance.custom")
	second := turnEndEvent[M]("turn-1", "turn-1")
	third := extensionEvent[M]("custom-2", "x.conformance.custom")
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{first, second, third}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{
		Kinds: []adk.SessionEventKind{adk.SessionEventKind("x.conformance.custom")},
	})
	requireNoError(t, err)
	if res == nil {
		t.Fatalf("LoadEvents returned nil result")
	}
	requireEventsEqual(t, []*adk.SessionEvent[M]{first, third}, res.Events)
}

func testReversePagination[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	events := make([]*adk.SessionEvent[M], 5)
	for i := 0; i < 5; i++ {
		events[i] = messageEvent(fmt.Sprintf("r%d", i), makeMessage(fmt.Sprintf("%c", 'a'+i)))
		requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{events[i]}))
	}

	var collected []string
	var after string
	for {
		res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{
			Reverse: true,
			Limit:   2,
			After:   after,
		})
		requireNoError(t, err)
		if res == nil || len(res.Events) == 0 {
			break
		}
		for _, ep := range res.Events {
			collected = append(collected, ep.EventID)
		}
		if res.Next == "" {
			break
		}
		after = res.Next
	}

	expected := []string{"r4", "r3", "r2", "r1", "r0"}
	if len(collected) != len(expected) {
		t.Fatalf("reverse collected length=%d want=%d (got=%v)", len(collected), len(expected), collected)
	}
	for i := range expected {
		if collected[i] != expected[i] {
			t.Fatalf("reverse[%d]=%q want=%q (got=%v)", i, collected[i], expected[i], collected)
		}
	}
}

func testForwardPagination[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	for i := 0; i < 80; i++ {
		event := messageEvent(fmt.Sprintf("f%d", i), makeMessage(fmt.Sprintf("%d", i)))
		requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{event}))
	}

	var collected []*adk.SessionEvent[M]
	req := &adk.LoadSessionEventsRequest{Limit: 10}
	for {
		res, err := store.LoadEvents(ctx, "s", req)
		requireNoError(t, err)
		if res == nil || len(res.Events) == 0 {
			break
		}
		collected = append(collected, res.Events...)
		if res.Next == "" {
			break
		}
		req = &adk.LoadSessionEventsRequest{Limit: 10, After: res.Next}
	}
	if len(collected) != 80 {
		t.Fatalf("expected 80 events, got %d", len(collected))
	}
	for i, ep := range collected {
		expectedID := fmt.Sprintf("f%d", i)
		if ep.EventID != expectedID {
			t.Fatalf("event[%d].EventID=%q, want=%q", i, ep.EventID, expectedID)
		}
	}
}

func testSessionIsolation[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	alpha := messageEvent("alpha-1", makeMessage("alpha"))
	beta := turnEndEvent[M]("beta-1", "beta-turn")
	requireNoError(t, store.AppendEvents(ctx, "alpha", []*adk.SessionEvent[M]{alpha}))
	requireNoError(t, store.AppendEvents(ctx, "beta", []*adk.SessionEvent[M]{beta}))

	alphaRes, err := store.LoadEvents(ctx, "alpha", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []*adk.SessionEvent[M]{alpha}, alphaRes.Events)

	betaRes, err := store.LoadEvents(ctx, "beta", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []*adk.SessionEvent[M]{beta}, betaRes.Events)
}

func testEmptySession[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M]) {
	store := newStore(t, factory)
	ctx := context.Background()

	res, err := store.LoadEvents(ctx, "nonexistent", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	if res != nil && len(res.Events) != 0 {
		t.Fatalf("expected empty result for nonexistent session, got %d events", len(res.Events))
	}
}

func testIdempotentAppend[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := messageEvent("dup-1", makeMessage("first"))
	dup := messageEvent("dup-1", makeMessage("second"))
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{first}))
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []*adk.SessionEvent[M]{first}, res.Events)
}

func testIdempotentAppendWithinBatch[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := messageEvent("dup-batch-1", makeMessage("first"))
	dup := messageEvent("dup-batch-1", makeMessage("second"))
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{first, dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []*adk.SessionEvent[M]{first}, res.Events)
}

func testRejectEmptyEventID[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	err := store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{{Kind: adk.SessionEventMessage, Message: makeMessage("empty")}})
	if !errors.Is(err, adk.ErrInvalidEventID) {
		t.Fatalf("expected ErrInvalidEventID, got %v", err)
	}
}

func testAfterForward[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	events := make([]*adk.SessionEvent[M], 5)
	for i := 0; i < 5; i++ {
		events[i] = messageEvent(fmt.Sprintf("fwd-%d", i), makeMessage(fmt.Sprintf("%d", i)))
		requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{events[i]}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{After: "fwd-2"})
	requireNoError(t, err)
	requireEventsEqual(t, []*adk.SessionEvent[M]{events[3], events[4]}, res.Events)
}

func testAfterReverse[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	events := make([]*adk.SessionEvent[M], 5)
	for i := 0; i < 5; i++ {
		events[i] = messageEvent(fmt.Sprintf("rev-%d", i), makeMessage(fmt.Sprintf("%d", i)))
		requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{events[i]}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{Reverse: true, After: "rev-2"})
	requireNoError(t, err)
	requireEventsEqual(t, []*adk.SessionEvent[M]{events[1], events[0]}, res.Events)
}

func testUnknownAfter[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{messageEvent("only-1", makeMessage("only"))}))

	_, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{After: "ghost"})
	if !errors.Is(err, adk.ErrEventIDOutOfRange) {
		t.Fatalf("forward unknown After expected ErrEventIDOutOfRange, got %v", err)
	}
	_, err = store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{After: "ghost", Reverse: true})
	if !errors.Is(err, adk.ErrEventIDOutOfRange) {
		t.Fatalf("reverse unknown After expected ErrEventIDOutOfRange, got %v", err)
	}
}

func testEmptyPageBoundary[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	ids := []string{"e0", "e1", "e2"}
	for _, id := range ids {
		requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{messageEvent(id, makeMessage(id))}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{After: "e2"})
	requireNoError(t, err)
	if res == nil || len(res.Events) != 0 || res.Next != "" {
		t.Fatalf("forward empty page expected, got events=%d next=%q", len(res.Events), res.Next)
	}

	res, err = store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{Reverse: true, After: "e0"})
	requireNoError(t, err)
	if res == nil || len(res.Events) != 0 || res.Next != "" {
		t.Fatalf("reverse empty page expected, got events=%d next=%q", len(res.Events), res.Next)
	}
}

func testEventBodyRoundTrip[M adk.MessageType](t *testing.T, factory func(testing.TB) adk.SessionService[M], makeMessage func(string) M) {
	store := newStore(t, factory)
	ctx := context.Background()

	event := messageEvent("body-test-1", makeMessage("body"))
	requireNoError(t, store.AppendEvents(ctx, "s", []*adk.SessionEvent[M]{event}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadSessionEventsRequest{})
	requireNoError(t, err)
	if res == nil || len(res.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(res.Events))
	}
	requireEventsEqual(t, []*adk.SessionEvent[M]{event}, res.Events)
}

func newStore[M adk.MessageType](t testing.TB, factory func(testing.TB) adk.SessionService[M]) adk.SessionService[M] {
	t.Helper()
	store := factory(t)
	if store == nil {
		t.Fatalf("factory returned nil SessionService")
	}
	return store
}

func requireNoError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireEventsEqual[M adk.MessageType](t testing.TB, want, got []*adk.SessionEvent[M]) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("events length mismatch: got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("event[%d] mismatch:\n got: %#v\nwant: %#v", i, got[i], want[i])
		}
	}
}

type countingEventSerializer struct {
	inner          schema.Serializer
	marshalCount   int
	unmarshalCount int
}

func (s *countingEventSerializer) Marshal(v any) ([]byte, error) {
	s.marshalCount++
	return s.inner.Marshal(v)
}

func (s *countingEventSerializer) Unmarshal(data []byte, v any) error {
	s.unmarshalCount++
	return s.inner.Unmarshal(data, v)
}

func messageEvent[M adk.MessageType](id string, msg M) *adk.SessionEvent[M] {
	return &adk.SessionEvent[M]{EventID: id, Kind: adk.SessionEventMessage, Message: msg}
}

func turnEndEvent[M adk.MessageType](id, turnID string) *adk.SessionEvent[M] {
	return &adk.SessionEvent[M]{
		EventID: id,
		Kind:    adk.SessionEventTurnEnd,
		TurnID:  turnID,
		TurnEnd: &adk.TurnEndState[M]{},
	}
}

func extensionEvent[M adk.MessageType](id, kind string) *adk.SessionEvent[M] {
	return &adk.SessionEvent[M]{
		EventID: id,
		Kind:    adk.SessionEventKind(kind),
		Extension: &adk.SessionExtensionEvent{
			Data: []byte(`{"ok":true}`),
		},
	}
}
