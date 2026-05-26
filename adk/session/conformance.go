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

// Package session provides SessionStore implementations and a reusable
// conformance test suite for validating SessionStore implementations.
package session

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/cloudwego/eino/adk"
)

// RunConformanceTests validates the SessionStore contract shared by
// Runner-managed session persistence implementations.
//
// The contract assumes single-writer-per-session: tests do NOT exercise
// concurrent AppendEvents calls for the same sessionID.
func RunConformanceTests(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	t.Helper()

	t.Run("AppendEvents and forward LoadEvents", func(t *testing.T) { testAppendAndForwardLoad(t, factory) })
	t.Run("LoadEvents reverse pagination", func(t *testing.T) { testReversePagination(t, factory) })
	t.Run("After forward pagination", func(t *testing.T) { testForwardPagination(t, factory) })
	t.Run("sessionID isolates events", func(t *testing.T) { testSessionIsolation(t, factory) })
	t.Run("Empty session returns no events", func(t *testing.T) { testEmptySession(t, factory) })
	t.Run("AppendEvents is idempotent on duplicate EventID", func(t *testing.T) { testIdempotentAppend(t, factory) })
	t.Run("AppendEvents skips duplicate EventID within same batch", func(t *testing.T) { testIdempotentAppendWithinBatch(t, factory) })
	t.Run("AppendEvents rejects empty EventID with ErrInvalidEventID", func(t *testing.T) { testRejectEmptyEventID(t, factory) })
	t.Run("After resumes by EventID forward", func(t *testing.T) { testAfterForward(t, factory) })
	t.Run("After resumes by EventID reverse", func(t *testing.T) { testAfterReverse(t, factory) })
	t.Run("Unknown After returns ErrEventIDOutOfRange", func(t *testing.T) { testUnknownAfter(t, factory) })
	t.Run("Empty page when After=last forward and After=first reverse", func(t *testing.T) { testEmptyPageBoundary(t, factory) })
	t.Run("Opaque binary Data round-trips correctly", func(t *testing.T) { testOpaqueDataRoundTrip(t, factory) })
}

func testAppendAndForwardLoad(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := adk.SessionEventPayload{EventID: "e1", Kind: adk.SessionEventMessage, Data: []byte(`{"i":1}`)}
	second := adk.SessionEventPayload{EventID: "e2", Kind: adk.SessionEventTurnEnd, Data: []byte(`{"i":2}`)}
	third := adk.SessionEventPayload{EventID: "e3", Kind: adk.SessionEventMessage, Data: []byte(`{"i":3}`)}
	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first, second}))
	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{third}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	if res == nil {
		t.Fatalf("LoadEvents returned nil result")
	}
	requireEventsEqual(t, []adk.SessionEventPayload{first, second, third}, res.Events)
}

func testReversePagination(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	payloads := make([]adk.SessionEventPayload, 5)
	for i := 0; i < 5; i++ {
		payloads[i] = adk.SessionEventPayload{EventID: fmt.Sprintf("r%d", i), Kind: adk.SessionEventMessage, Data: []byte(fmt.Sprintf(`{"ch":"%c"}`, 'a'+i))}
		requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{payloads[i]}))
	}

	var collected []string
	var after string
	for {
		res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{
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

func testForwardPagination(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	for i := 0; i < 80; i++ {
		payload := adk.SessionEventPayload{EventID: fmt.Sprintf("f%d", i), Kind: adk.SessionEventMessage, Data: []byte(fmt.Sprintf(`{"i":%d}`, i))}
		requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{payload}))
	}

	var collected []adk.SessionEventPayload
	req := &adk.LoadEventsRequest{Limit: 10}
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
		req = &adk.LoadEventsRequest{Limit: 10, After: res.Next}
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

func testSessionIsolation(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	alpha := adk.SessionEventPayload{EventID: "alpha-1", Kind: adk.SessionEventMessage, Data: []byte(`{"tag":"alpha"}`)}
	beta := adk.SessionEventPayload{EventID: "beta-1", Kind: adk.SessionEventTurnEnd, Data: []byte(`{"tag":"beta"}`)}
	requireNoError(t, store.AppendEvents(ctx, "alpha", []adk.SessionEventPayload{alpha}))
	requireNoError(t, store.AppendEvents(ctx, "beta", []adk.SessionEventPayload{beta}))

	alphaRes, err := store.LoadEvents(ctx, "alpha", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []adk.SessionEventPayload{alpha}, alphaRes.Events)

	betaRes, err := store.LoadEvents(ctx, "beta", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []adk.SessionEventPayload{beta}, betaRes.Events)
}

func testEmptySession(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	res, err := store.LoadEvents(ctx, "nonexistent", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	if res != nil && len(res.Events) != 0 {
		t.Fatalf("expected empty result for nonexistent session, got %d events", len(res.Events))
	}
}

func testIdempotentAppend(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := adk.SessionEventPayload{EventID: "dup-1", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"first"}`)}
	dup := adk.SessionEventPayload{EventID: "dup-1", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"second"}`)}
	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first}))
	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []adk.SessionEventPayload{first}, res.Events)
}

func testIdempotentAppendWithinBatch(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := adk.SessionEventPayload{EventID: "dup-batch-1", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"first"}`)}
	dup := adk.SessionEventPayload{EventID: "dup-batch-1", Kind: adk.SessionEventMessage, Data: []byte(`{"payload":"second"}`)}
	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{first, dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, []adk.SessionEventPayload{first}, res.Events)
}

func testRejectEmptyEventID(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	err := store.AppendEvents(ctx, "s", []adk.SessionEventPayload{{EventID: "", Data: []byte(`{}`)}})
	if !errors.Is(err, adk.ErrInvalidEventID) {
		t.Fatalf("expected ErrInvalidEventID, got %v", err)
	}
}

func testAfterForward(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	payloads := make([]adk.SessionEventPayload, 5)
	for i := 0; i < 5; i++ {
		payloads[i] = adk.SessionEventPayload{EventID: fmt.Sprintf("fwd-%d", i), Kind: adk.SessionEventMessage, Data: []byte(fmt.Sprintf(`{"i":%d}`, i))}
		requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{payloads[i]}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{After: "fwd-2"})
	requireNoError(t, err)
	requireEventsEqual(t, []adk.SessionEventPayload{payloads[3], payloads[4]}, res.Events)
}

func testAfterReverse(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	payloads := make([]adk.SessionEventPayload, 5)
	for i := 0; i < 5; i++ {
		payloads[i] = adk.SessionEventPayload{EventID: fmt.Sprintf("rev-%d", i), Kind: adk.SessionEventMessage, Data: []byte(fmt.Sprintf(`{"i":%d}`, i))}
		requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{payloads[i]}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{Reverse: true, After: "rev-2"})
	requireNoError(t, err)
	requireEventsEqual(t, []adk.SessionEventPayload{payloads[1], payloads[0]}, res.Events)
}

func testUnknownAfter(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{
		{EventID: "only-1", Kind: adk.SessionEventMessage, Data: []byte(`{}`)},
	}))

	_, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{After: "ghost"})
	if !errors.Is(err, adk.ErrEventIDOutOfRange) {
		t.Fatalf("forward unknown After expected ErrEventIDOutOfRange, got %v", err)
	}
	_, err = store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{After: "ghost", Reverse: true})
	if !errors.Is(err, adk.ErrEventIDOutOfRange) {
		t.Fatalf("reverse unknown After expected ErrEventIDOutOfRange, got %v", err)
	}
}

func testEmptyPageBoundary(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	ids := []string{"e0", "e1", "e2"}
	for _, id := range ids {
		requireNoError(t, store.AppendEvents(ctx, "s",
			[]adk.SessionEventPayload{{EventID: id, Kind: adk.SessionEventMessage, Data: []byte(`{}`)}}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{After: "e2"})
	requireNoError(t, err)
	if res == nil || len(res.Events) != 0 || res.Next != "" {
		t.Fatalf("forward empty page expected, got events=%d next=%q", len(res.Events), res.Next)
	}

	res, err = store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{Reverse: true, After: "e0"})
	requireNoError(t, err)
	if res == nil || len(res.Events) != 0 || res.Next != "" {
		t.Fatalf("reverse empty page expected, got events=%d next=%q", len(res.Events), res.Next)
	}
}

func testOpaqueDataRoundTrip(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	// Use opaque bytes that are line-safe (no raw \n or \r) so the test
	// works for both InMemoryStore and FileStore. Includes \t, null bytes,
	// and high bytes to verify stores treat Data as opaque.
	opaqueData := []byte{0x00, 0xFF, '\t', 0x80, 0x7F, 0x01}
	event := adk.SessionEventPayload{EventID: "opaque-test-1", Kind: adk.SessionEventMessage, Data: opaqueData}
	requireNoError(t, store.AppendEvents(ctx, "s", []adk.SessionEventPayload{event}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	if res == nil || len(res.Events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(res.Events))
	}
	if res.Events[0].EventID != "opaque-test-1" {
		t.Fatalf("EventID mismatch: got=%q want=%q", res.Events[0].EventID, "opaque-test-1")
	}
	if !bytes.Equal(res.Events[0].Data, opaqueData) {
		t.Fatalf("Data mismatch: got=%v want=%v", res.Events[0].Data, opaqueData)
	}
}

func newStore(t testing.TB, factory func(testing.TB) adk.SessionStore) adk.SessionStore {
	t.Helper()
	store := factory(t)
	if store == nil {
		t.Fatalf("factory returned nil SessionStore")
	}
	return store
}

func requireNoError(t testing.TB, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func requireEventsEqual(t testing.TB, want, got []adk.SessionEventPayload) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("events length mismatch: got=%d want=%d", len(got), len(want))
	}
	for i := range want {
		if got[i].EventID != want[i].EventID {
			t.Fatalf("event[%d].EventID mismatch: got=%q want=%q", i, got[i].EventID, want[i].EventID)
		}
		if got[i].Kind != want[i].Kind {
			t.Fatalf("event[%d].Kind mismatch: got=%q want=%q", i, got[i].Kind, want[i].Kind)
		}
		if !bytes.Equal(got[i].Data, want[i].Data) {
			t.Fatalf("event[%d].Data mismatch: got=%q want=%q", i, got[i].Data, want[i].Data)
		}
	}
}
