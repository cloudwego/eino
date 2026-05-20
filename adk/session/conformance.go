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
	"testing"

	"github.com/cloudwego/eino/adk"
)

// RunConformanceTests validates the SessionStore contract shared by
// Runner-managed session persistence implementations.
//
// The contract assumes single-writer-per-session: tests do NOT exercise
// concurrent AppendEvents/SaveTurnEnd for the same sessionID.
func RunConformanceTests(t *testing.T, factory func(testing.TB) adk.SessionStore) { //nolint:funlen // keep the store contract checks in one public helper
	t.Helper()

	t.Run("AppendEvents and forward LoadEvents", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		first := []byte(`{"i":1}`)
		second := []byte(`{"i":2}`)
		third := []byte(`{"i":3}`)
		requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{first, second}))
		requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{third}))

		res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsOptions{})
		requireNoError(t, err)
		if res == nil {
			t.Fatalf("LoadEvents returned nil result")
		}
		requireEventsEqual(t, [][]byte{first, second, third}, res.Events)
	})

	t.Run("LoadEvents reverse pagination", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		var all [][]byte
		for i := 0; i < 5; i++ {
			b := []byte{byte('a' + i)}
			all = append(all, b)
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{b}))
		}

		var collected [][]byte
		var pageToken string
		for {
			res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsOptions{
				Reverse:   true,
				Limit:     2,
				PageToken: pageToken,
			})
			requireNoError(t, err)
			if res == nil || len(res.Events) == 0 {
				break
			}
			collected = append(collected, res.Events...)
			if res.NextPageToken == "" {
				break
			}
			pageToken = res.NextPageToken
		}

		// Expect newest first.
		expected := [][]byte{{'e'}, {'d'}, {'c'}, {'b'}, {'a'}}
		requireEventsEqual(t, expected, collected)
	})

	t.Run("AfterCursor loads only post-snapshot events", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		for i := 0; i < 3; i++ {
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte('p' + i)}}))
		}
		requireNoError(t, store.SaveTurnEnd(ctx, "s", "", []byte("snap")))

		afterMsgID, afterCursor, payload, exists, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)
		if !exists {
			t.Fatalf("LoadLatestTurnEnd exists=false after SaveTurnEnd")
		}
		if afterMsgID != "" {
			t.Fatalf("afterMessageID=%q, want empty", afterMsgID)
		}
		if !bytes.Equal(payload, []byte("snap")) {
			t.Fatalf("payload=%q, want %q", payload, []byte("snap"))
		}
		if afterCursor == "" {
			t.Fatalf("afterEventCursor must be non-empty after SaveTurnEnd")
		}

		// Append more events after snapshot.
		for i := 0; i < 4; i++ {
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte('x' + i)}}))
		}

		// AfterCursor should return only post-snapshot events.
		res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsOptions{AfterCursor: afterCursor})
		requireNoError(t, err)
		expected := [][]byte{{'x'}, {'y'}, {'z'}, {'{'}}
		requireEventsEqual(t, expected, res.Events)
	})

	t.Run("AfterCursor with multi-page pagination", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		for i := 0; i < 50; i++ {
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte(i)}}))
		}
		requireNoError(t, store.SaveTurnEnd(ctx, "s", "", []byte("snap")))
		_, afterCursor, _, _, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)

		// Append 30 more events after snapshot.
		for i := 50; i < 80; i++ {
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte(i)}}))
		}

		var collected [][]byte
		opts := &adk.LoadEventsOptions{AfterCursor: afterCursor, Limit: 10}
		for {
			res, err := store.LoadEvents(ctx, "s", opts)
			requireNoError(t, err)
			if res == nil || len(res.Events) == 0 {
				break
			}
			collected = append(collected, res.Events...)
			if res.NextPageToken == "" {
				break
			}
			opts = &adk.LoadEventsOptions{Limit: 10, PageToken: res.NextPageToken}
		}
		if len(collected) != 30 {
			t.Fatalf("expected 30 events, got %d", len(collected))
		}
		for i, b := range collected {
			if len(b) != 1 || b[0] != byte(50+i) {
				t.Fatalf("event[%d]=%v, want %v", i, b, []byte{byte(50 + i)})
			}
		}
	})

	t.Run("LoadLatestTurnEnd not found", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		_, _, _, exists, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)
		if exists {
			t.Fatalf("LoadLatestTurnEnd exists=true before any SaveTurnEnd")
		}
	})

	t.Run("Cursor stability after appends", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		for i := 0; i < 5; i++ {
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte(i)}}))
		}
		requireNoError(t, store.SaveTurnEnd(ctx, "s", "msg-5", []byte("snap")))
		_, originalCursor, _, _, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)

		for i := 5; i < 25; i++ {
			requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte(i)}}))
		}

		// Cursor returned by LoadLatestTurnEnd should still be the same value.
		afterMsgID, sameCursor, _, exists, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)
		if !exists {
			t.Fatalf("snapshot disappeared after appends")
		}
		if afterMsgID != "msg-5" {
			t.Fatalf("afterMessageID=%q, want %q", afterMsgID, "msg-5")
		}
		if sameCursor != originalCursor {
			t.Fatalf("cursor changed after appends: original=%q new=%q", originalCursor, sameCursor)
		}

		// AfterCursor should return exactly the 20 new events.
		res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsOptions{AfterCursor: originalCursor})
		requireNoError(t, err)
		if len(res.Events) != 20 {
			t.Fatalf("AfterCursor returned %d events, want 20", len(res.Events))
		}
	})

	t.Run("sessionID isolates events and turn-end snapshots", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		alpha := []byte(`alpha-event`)
		beta := []byte(`beta-event`)
		requireNoError(t, store.AppendEvents(ctx, "alpha", [][]byte{alpha}))
		requireNoError(t, store.AppendEvents(ctx, "beta", [][]byte{beta}))

		alphaRes, err := store.LoadEvents(ctx, "alpha", &adk.LoadEventsOptions{})
		requireNoError(t, err)
		requireEventsEqual(t, [][]byte{alpha}, alphaRes.Events)

		betaRes, err := store.LoadEvents(ctx, "beta", &adk.LoadEventsOptions{})
		requireNoError(t, err)
		requireEventsEqual(t, [][]byte{beta}, betaRes.Events)

		requireNoError(t, store.SaveTurnEnd(ctx, "alpha", "alpha-msg", []byte("alpha-turn")))
		requireNoError(t, store.SaveTurnEnd(ctx, "beta", "beta-msg", []byte("beta-turn")))

		afterMsgID, _, payload, exists, err := store.LoadLatestTurnEnd(ctx, "alpha")
		requireNoError(t, err)
		requireTurnEnd(t, "alpha-msg", []byte("alpha-turn"), afterMsgID, payload, exists)

		afterMsgID, _, payload, exists, err = store.LoadLatestTurnEnd(ctx, "beta")
		requireNoError(t, err)
		requireTurnEnd(t, "beta-msg", []byte("beta-turn"), afterMsgID, payload, exists)
	})

	t.Run("SaveTurnEnd overwrites previous snapshot", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()
		requireNoError(t, store.SaveTurnEnd(ctx, "s", "first", []byte("first-turn")))
		requireNoError(t, store.SaveTurnEnd(ctx, "s", "second", []byte("second-turn")))
		afterMsgID, _, payload, exists, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)
		requireTurnEnd(t, "second", []byte("second-turn"), afterMsgID, payload, exists)
	})
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

func requireTurnEnd(t testing.TB, wantAfterMsgID string, wantPayload []byte, gotAfterMsgID string, gotPayload []byte, exists bool) {
	t.Helper()
	if !exists {
		t.Fatalf("LoadLatestTurnEnd exists=false")
	}
	if gotAfterMsgID != wantAfterMsgID {
		t.Fatalf("LoadLatestTurnEnd afterMessageID=%q, want %q", gotAfterMsgID, wantAfterMsgID)
	}
	if !bytes.Equal(gotPayload, wantPayload) {
		t.Fatalf("LoadLatestTurnEnd payload=%q, want %q", gotPayload, wantPayload)
	}
}
