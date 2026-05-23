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

// Package session provides a memory-based SessionStore implementation and a
// reusable conformance test suite for validating SessionStore implementations.
package session

import (
	"bytes"
	"context"
	"encoding/json"
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
	t.Run("AppendEvents rejects empty EventID with ErrInvalidEventID", func(t *testing.T) { testRejectEmptyEventID(t, factory) })
	t.Run("AppendEvents rejects unparsable payload with ErrInvalidEventID", func(t *testing.T) { testRejectUnparsablePayload(t, factory) })
	t.Run("After resumes by EventID forward", func(t *testing.T) { testAfterForward(t, factory) })
	t.Run("After resumes by EventID reverse", func(t *testing.T) { testAfterReverse(t, factory) })
	t.Run("Unknown After returns ErrEventIDOutOfRange", func(t *testing.T) { testUnknownAfter(t, factory) })
	t.Run("Empty page when After=last forward and After=first reverse", func(t *testing.T) { testEmptyPageBoundary(t, factory) })
}

func testAppendAndForwardLoad(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	first := []byte(`{"event_id":"e1","i":1}`)
	second := []byte(`{"event_id":"e2","i":2}`)
	third := []byte(`{"event_id":"e3","i":3}`)
	requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{first, second}))
	requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{third}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	if res == nil {
		t.Fatalf("LoadEvents returned nil result")
	}
	requireEventsEqual(t, [][]byte{first, second, third}, res.Events)
}

func testReversePagination(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	payloads := make([][]byte, 5)
	for i := 0; i < 5; i++ {
		payloads[i] = []byte(fmt.Sprintf(`{"event_id":"r%d","ch":"%c"}`, i, 'a'+i))
		requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{payloads[i]}))
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
		for _, raw := range res.Events {
			var h struct {
				EventID string `json:"event_id"`
			}
			if err := json.Unmarshal(raw, &h); err != nil {
				t.Fatalf("decode page event: %v", err)
			}
			collected = append(collected, h.EventID)
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
		payload := []byte(fmt.Sprintf(`{"event_id":"f%d","i":%d}`, i, i))
		requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{payload}))
	}

	var collected [][]byte
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
	for i, raw := range collected {
		var h struct {
			EventID string `json:"event_id"`
			I       int    `json:"i"`
		}
		if err := json.Unmarshal(raw, &h); err != nil {
			t.Fatalf("decode forward[%d]: %v", i, err)
		}
		if h.I != i || h.EventID != fmt.Sprintf("f%d", i) {
			t.Fatalf("event[%d]=%+v, want event_id=f%d i=%d", i, h, i, i)
		}
	}
}

func testSessionIsolation(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	alpha := []byte(`{"event_id":"alpha-1","tag":"alpha"}`)
	beta := []byte(`{"event_id":"beta-1","tag":"beta"}`)
	requireNoError(t, store.AppendEvents(ctx, "alpha", [][]byte{alpha}))
	requireNoError(t, store.AppendEvents(ctx, "beta", [][]byte{beta}))

	alphaRes, err := store.LoadEvents(ctx, "alpha", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, [][]byte{alpha}, alphaRes.Events)

	betaRes, err := store.LoadEvents(ctx, "beta", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, [][]byte{beta}, betaRes.Events)
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

	first := []byte(`{"event_id":"dup-1","payload":"first"}`)
	dup := []byte(`{"event_id":"dup-1","payload":"second"}`)
	requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{first}))
	requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{dup}))

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
	requireNoError(t, err)
	requireEventsEqual(t, [][]byte{first}, res.Events)
}

func testRejectEmptyEventID(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	err := store.AppendEvents(ctx, "s", [][]byte{[]byte(`{"event_id":""}`)})
	if !errors.Is(err, adk.ErrInvalidEventID) {
		t.Fatalf("expected ErrInvalidEventID, got %v", err)
	}
}

func testRejectUnparsablePayload(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	err := store.AppendEvents(ctx, "s", [][]byte{[]byte("not-json")})
	if !errors.Is(err, adk.ErrInvalidEventID) {
		t.Fatalf("expected ErrInvalidEventID, got %v", err)
	}
}

func testAfterForward(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	payloads := make([][]byte, 5)
	for i := 0; i < 5; i++ {
		payloads[i] = []byte(fmt.Sprintf(`{"event_id":"fwd-%d","i":%d}`, i, i))
		requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{payloads[i]}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{After: "fwd-2"})
	requireNoError(t, err)
	requireEventsEqual(t, [][]byte{payloads[3], payloads[4]}, res.Events)
}

func testAfterReverse(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	payloads := make([][]byte, 5)
	for i := 0; i < 5; i++ {
		payloads[i] = []byte(fmt.Sprintf(`{"event_id":"rev-%d","i":%d}`, i, i))
		requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{payloads[i]}))
	}

	res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{Reverse: true, After: "rev-2"})
	requireNoError(t, err)
	requireEventsEqual(t, [][]byte{payloads[1], payloads[0]}, res.Events)
}

func testUnknownAfter(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	store := newStore(t, factory)
	ctx := context.Background()

	requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{
		[]byte(`{"event_id":"only-1"}`),
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
			[][]byte{[]byte(fmt.Sprintf(`{"event_id":%q}`, id))}))
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

func requireEventsEqual(t testing.TB, want, got [][]byte) {
	t.Helper()
	if len(want) != len(got) {
		t.Fatalf("events length mismatch: got=%d want=%d (got=%v want=%v)", len(got), len(want), got, want)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("event[%d] mismatch: got=%q want=%q", i, got[i], want[i])
		}
	}
}
