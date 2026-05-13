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
	"reflect"
	"testing"

	"github.com/cloudwego/eino/adk"
)

// RunConformanceTests validates the SessionStore contract shared by
// Runner-managed session persistence implementations.
func RunConformanceTests(t *testing.T, factory func(testing.TB) adk.SessionStore) {
	t.Helper()

	t.Run("AppendEvents idempotency and ordered bounded LoadEvents", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()
		first := adk.EventRecord{TurnIndex: 1, Seq: 1, Kind: "first", Payload: []byte("first")}
		second := adk.EventRecord{TurnIndex: 1, Seq: 2, Kind: "second", Payload: []byte("second")}
		gapped := adk.EventRecord{TurnIndex: 1, Seq: 4, Kind: "gapped", Payload: []byte("gapped")}
		turnTwo := adk.EventRecord{TurnIndex: 2, Seq: 1, Kind: "turn-two", Payload: []byte("turn-two")}
		turnThree := adk.EventRecord{TurnIndex: 3, Seq: 1, Kind: "turn-three", Payload: []byte("turn-three")}

		requireNoError(t, store.AppendEvents(ctx, "s", 1, []adk.EventRecord{second, first}))
		requireNoError(t, store.AppendEvents(ctx, "s", 1, []adk.EventRecord{first}))
		requireNoError(t, store.AppendEvents(ctx, "s", 1, []adk.EventRecord{gapped}))
		requireNoError(t, store.AppendEvents(ctx, "s", 2, []adk.EventRecord{turnTwo}))
		requireNoError(t, store.AppendEvents(ctx, "s", 3, []adk.EventRecord{turnThree}))

		records, err := store.LoadEvents(ctx, "s", 1, 1)
		requireNoError(t, err)
		requireRecordsEqual(t, []adk.EventRecord{first, second, gapped}, records)

		records, err = store.LoadEvents(ctx, "s", 1, 2)
		requireNoError(t, err)
		requireRecordsEqual(t, []adk.EventRecord{first, second, gapped, turnTwo}, records)

		conflict := first
		conflict.Payload = []byte("conflict")
		if err = store.AppendEvents(ctx, "s", 1, []adk.EventRecord{conflict}); err == nil {
			t.Fatalf("AppendEvents accepted conflicting duplicate event record")
		}
	})

	t.Run("LoadLatestTurnEnd latest snapshot", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()

		_, _, exists, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)
		if exists {
			t.Fatalf("LoadLatestTurnEnd exists=true before any SaveTurnEnd")
		}

		requireNoError(t, store.SaveTurnEnd(ctx, "s", 1, []byte("turn-one")))
		requireNoError(t, store.SaveTurnEnd(ctx, "s", 3, []byte("turn-three")))

		turnIndex, payload, exists, err := store.LoadLatestTurnEnd(ctx, "s")
		requireNoError(t, err)
		if !exists {
			t.Fatalf("LoadLatestTurnEnd exists=false after SaveTurnEnd")
		}
		if turnIndex != 3 {
			t.Fatalf("LoadLatestTurnEnd turnIndex=%d, want 3", turnIndex)
		}
		if !bytes.Equal(payload, []byte("turn-three")) {
			t.Fatalf("LoadLatestTurnEnd payload=%q, want %q", payload, []byte("turn-three"))
		}
	})

	t.Run("sessionID isolates events and turn-end snapshots", func(t *testing.T) {
		store := newStore(t, factory)
		ctx := context.Background()
		alphaEvent := adk.EventRecord{TurnIndex: 1, Seq: 1, Kind: "alpha", Payload: []byte("alpha-event")}
		betaEvent := adk.EventRecord{TurnIndex: 1, Seq: 1, Kind: "beta", Payload: []byte("beta-event")}

		requireNoError(t, store.AppendEvents(ctx, "alpha", 1, []adk.EventRecord{alphaEvent}))
		requireNoError(t, store.AppendEvents(ctx, "beta", 1, []adk.EventRecord{betaEvent}))

		alphaRecords, err := store.LoadEvents(ctx, "alpha", 1, 1)
		requireNoError(t, err)
		requireRecordsEqual(t, []adk.EventRecord{alphaEvent}, alphaRecords)

		betaRecords, err := store.LoadEvents(ctx, "beta", 1, 1)
		requireNoError(t, err)
		requireRecordsEqual(t, []adk.EventRecord{betaEvent}, betaRecords)

		requireNoError(t, store.SaveTurnEnd(ctx, "alpha", 1, []byte("alpha-turn")))
		requireNoError(t, store.SaveTurnEnd(ctx, "beta", 1, []byte("beta-turn")))

		turnIndex, payload, exists, err := store.LoadLatestTurnEnd(ctx, "alpha")
		requireNoError(t, err)
		requireTurnEnd(t, 1, []byte("alpha-turn"), turnIndex, payload, exists)

		turnIndex, payload, exists, err = store.LoadLatestTurnEnd(ctx, "beta")
		requireNoError(t, err)
		requireTurnEnd(t, 1, []byte("beta-turn"), turnIndex, payload, exists)
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

func requireRecordsEqual(t testing.TB, want, got []adk.EventRecord) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("records mismatch:\n got: %#v\nwant: %#v", got, want)
	}
}

func requireTurnEnd(t testing.TB, wantTurnIndex int, wantPayload []byte, gotTurnIndex int, gotPayload []byte, exists bool) {
	t.Helper()
	if !exists {
		t.Fatalf("LoadLatestTurnEnd exists=false")
	}
	if gotTurnIndex != wantTurnIndex {
		t.Fatalf("LoadLatestTurnEnd turnIndex=%d, want %d", gotTurnIndex, wantTurnIndex)
	}
	if !bytes.Equal(gotPayload, wantPayload) {
		t.Fatalf("LoadLatestTurnEnd payload=%q, want %q", gotPayload, wantPayload)
	}
}
