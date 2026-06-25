/*
 * Copyright 2025 CloudWeGo Authors
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

package sseha

import (
	"fmt"
	"testing"
)

func TestEventBuffer(t *testing.T) {
	t.Run("basic append and retrieve", func(t *testing.T) {
		buf := NewEventBuffer(10)

		for i := 0; i < 5; i++ {
			buf.Append(SSEEvent{
				EventID:   fmt.Sprintf("evt_%d", i),
				SessionID: "test",
				Data:      []byte(fmt.Sprintf("data_%d", i)),
			})
		}

		if buf.Len() != 5 {
			t.Errorf("expected 5 events, got %d", buf.Len())
		}

		if buf.LastEventID() != "evt_4" {
			t.Errorf("expected last event ID evt_4, got %s", buf.LastEventID())
		}
	})

	t.Run("events after specific ID", func(t *testing.T) {
		buf := NewEventBuffer(10)

		for i := 0; i < 5; i++ {
			buf.Append(SSEEvent{
				EventID:   fmt.Sprintf("evt_%d", i),
				SessionID: "test",
			})
		}

		events, found := buf.EventsAfter("evt_2")
		if !found {
			t.Fatal("expected to find events after evt_2")
		}
		if len(events) != 2 {
			t.Errorf("expected 2 events, got %d", len(events))
		}
		if events[0].EventID != "evt_3" {
			t.Errorf("expected evt_3, got %s", events[0].EventID)
		}
		if events[1].EventID != "evt_4" {
			t.Errorf("expected evt_4, got %s", events[1].EventID)
		}
	})

	t.Run("events after empty ID returns all", func(t *testing.T) {
		buf := NewEventBuffer(10)

		for i := 0; i < 3; i++ {
			buf.Append(SSEEvent{EventID: fmt.Sprintf("evt_%d", i)})
		}

		events, found := buf.EventsAfter("")
		if !found {
			t.Fatal("expected to find all events")
		}
		if len(events) != 3 {
			t.Errorf("expected 3 events, got %d", len(events))
		}
	})

	t.Run("events after unknown ID", func(t *testing.T) {
		buf := NewEventBuffer(10)
		buf.Append(SSEEvent{EventID: "evt_0"})

		_, found := buf.EventsAfter("unknown")
		if found {
			t.Error("expected not found for unknown event ID")
		}
	})

	t.Run("eviction when full", func(t *testing.T) {
		buf := NewEventBuffer(3)

		for i := 0; i < 5; i++ {
			buf.Append(SSEEvent{EventID: fmt.Sprintf("evt_%d", i)})
		}

		if buf.Len() != 3 {
			t.Errorf("expected 3 events after eviction, got %d", buf.Len())
		}

		events, found := buf.EventsAfter("")
		if !found {
			t.Fatal("expected to find events")
		}
		if events[0].EventID != "evt_2" {
			t.Errorf("expected first event to be evt_2, got %s", events[0].EventID)
		}
	})
}

func TestSessionState(t *testing.T) {
	tests := []struct {
		state    SessionState
		expected string
	}{
		{SessionStateActive, "active"},
		{SessionStateSuspended, "suspended"},
		{SessionStateMigrating, "migrating"},
		{SessionStateClosed, "closed"},
		{SessionState(99), "unknown(99)"},
	}

	for _, tt := range tests {
		if got := tt.state.String(); got != tt.expected {
			t.Errorf("SessionState(%d).String() = %s, want %s", int(tt.state), got, tt.expected)
		}
	}
}
