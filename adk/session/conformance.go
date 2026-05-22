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
// concurrent AppendEvents calls for the same sessionID.
func RunConformanceTests(t *testing.T, factory func(testing.TB) adk.SessionStore) {
    t.Helper()

    t.Run("AppendEvents and forward LoadEvents", func(t *testing.T) {
        store := newStore(t, factory)
        ctx := context.Background()

        first := []byte(`{"i":1}`)
        second := []byte(`{"i":2}`)
        third := []byte(`{"i":3}`)
        requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{first, second}))
        requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{third}))

        res, err := store.LoadEvents(ctx, "s", &adk.LoadEventsRequest{})
        requireNoError(t, err)
        if res == nil {
            t.Fatalf("LoadEvents returned nil result")
        }
        requireEventsEqual(t, [][]byte{first, second, third}, res.Events)
    })

    t.Run("LoadEvents reverse pagination", func(t *testing.T) {
        store := newStore(t, factory)
        ctx := context.Background()

        for i := 0; i < 5; i++ {
            b := []byte{byte('a' + i)}
            requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{b}))
        }

        var collected [][]byte
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
            collected = append(collected, res.Events...)
            if res.Next == "" {
                break
            }
            after = res.Next
        }

        // Expect newest first.
        expected := [][]byte{{'e'}, {'d'}, {'c'}, {'b'}, {'a'}}
        requireEventsEqual(t, expected, collected)
    })

    t.Run("After forward pagination", func(t *testing.T) {
        store := newStore(t, factory)
        ctx := context.Background()

        for i := 0; i < 80; i++ {
            requireNoError(t, store.AppendEvents(ctx, "s", [][]byte{{byte(i)}}))
        }

        // Load with limit to paginate.
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
        for i, b := range collected {
            if len(b) != 1 || b[0] != byte(i) {
                t.Fatalf("event[%d]=%v, want %v", i, b, []byte{byte(i)})
            }
        }
    })

    t.Run("sessionID isolates events", func(t *testing.T) {
        store := newStore(t, factory)
        ctx := context.Background()

        alpha := []byte(`alpha-event`)
        beta := []byte(`beta-event`)
        requireNoError(t, store.AppendEvents(ctx, "alpha", [][]byte{alpha}))
        requireNoError(t, store.AppendEvents(ctx, "beta", [][]byte{beta}))

        alphaRes, err := store.LoadEvents(ctx, "alpha", &adk.LoadEventsRequest{})
        requireNoError(t, err)
        requireEventsEqual(t, [][]byte{alpha}, alphaRes.Events)

        betaRes, err := store.LoadEvents(ctx, "beta", &adk.LoadEventsRequest{})
        requireNoError(t, err)
        requireEventsEqual(t, [][]byte{beta}, betaRes.Events)
    })

    t.Run("Empty session returns no events", func(t *testing.T) {
        store := newStore(t, factory)
        ctx := context.Background()

        res, err := store.LoadEvents(ctx, "nonexistent", &adk.LoadEventsRequest{})
        requireNoError(t, err)
        if res != nil && len(res.Events) != 0 {
            t.Fatalf("expected empty result for nonexistent session, got %d events", len(res.Events))
        }
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
