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

package session

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// InMemoryStore is a thread-safe, in-memory implementation of adk.SessionStore
// and CheckPointStore (with Delete support). Suitable for testing and
// single-process deployments where durability is not required.
//
// Memory cost note: in addition to the raw payload bytes, the store maintains
// a parallel slice of event IDs and an event_id → position map per session
// (~50–80 bytes per event for the index entry); this is the trade-off for
// supporting EventID-based cursors without re-parsing JSON on every page load.
type InMemoryStore struct {
	mu          sync.Mutex
	events      map[string][][]byte       // sessionID -> ordered payloads
	eventIDs    map[string][]string       // sessionID -> ordered event_ids (parallel to events)
	eventIDIdx  map[string]map[string]int // sessionID -> event_id -> position
	checkpoints map[string][]byte
}

// eventHeader is the minimal envelope used to pull event_id out of a payload
// without fully decoding it.
type eventHeader struct {
	EventID string `json:"event_id"`
}

// NewInMemoryStore creates a new InMemoryStore.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		events:      make(map[string][][]byte),
		eventIDs:    make(map[string][]string),
		eventIDIdx:  make(map[string]map[string]int),
		checkpoints: make(map[string][]byte),
	}
}

// AppendEvents appends events to the session's event log.
//
// Each payload MUST carry a non-empty event_id. Empty / unparsable / missing
// event_id payloads cause AppendEvents to return adk.ErrInvalidEventID. If a
// payload's event_id is already present in the session, it is silently
// skipped (first-write-wins; payload bytes are not compared).
func (s *InMemoryStore) AppendEvents(_ context.Context, sessionID string, events [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.eventIDIdx[sessionID]
	if !ok {
		idx = make(map[string]int)
		s.eventIDIdx[sessionID] = idx
	}
	for _, e := range events {
		var h eventHeader
		if err := json.Unmarshal(e, &h); err != nil {
			return fmt.Errorf("%w: %v", adk.ErrInvalidEventID, err)
		}
		if h.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if _, dup := idx[h.EventID]; dup {
			continue // idempotent skip; first-write-wins
		}
		cp := append([]byte{}, e...)
		s.events[sessionID] = append(s.events[sessionID], cp)
		s.eventIDs[sessionID] = append(s.eventIDs[sessionID], h.EventID)
		idx[h.EventID] = len(s.events[sessionID]) - 1
	}
	return nil
}

// LoadEvents loads events with pagination and direction support.
func (s *InMemoryStore) LoadEvents(_ context.Context, sessionID string, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if opts == nil {
		opts = &adk.LoadEventsRequest{}
	}

	if opts.Reverse {
		return s.loadReverse(sessionID, opts)
	}
	return s.loadForward(sessionID, opts)
}

func (s *InMemoryStore) loadForward(sessionID string, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	all := s.events[sessionID]
	ids := s.eventIDs[sessionID]
	idx := s.eventIDIdx[sessionID]

	start := 0
	if opts.After != "" {
		pos, ok := idx[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		start = pos + 1
	}
	if start > len(all) {
		start = len(all)
	}

	end := len(all)
	if opts.Limit > 0 && start+opts.Limit < end {
		end = start + opts.Limit
	}

	out := make([][]byte, end-start)
	for i := range out {
		out[i] = append([]byte{}, all[start+i]...)
	}

	var next string
	if end < len(all) && end > 0 {
		next = ids[end-1]
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

func (s *InMemoryStore) loadReverse(sessionID string, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	all := s.events[sessionID]
	ids := s.eventIDs[sessionID]
	idx := s.eventIDIdx[sessionID]

	end := len(all)
	if opts.After != "" {
		pos, ok := idx[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		end = pos // strictly older: [0, pos)
	}
	if end <= 0 {
		return &adk.LoadEventsResult{}, nil
	}

	count := end
	if opts.Limit > 0 && opts.Limit < count {
		count = opts.Limit
	}

	start := end - count
	out := make([][]byte, count)
	for i := 0; i < count; i++ {
		out[i] = append([]byte{}, all[end-1-i]...)
	}

	var next string
	if start > 0 {
		next = ids[start]
	}
	return &adk.LoadEventsResult{Events: out, Next: next}, nil
}

// Set stores a checkpoint value.
func (s *InMemoryStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[checkPointID] = append([]byte{}, checkPoint...)
	return nil
}

// Get retrieves a checkpoint value. Returns an independent copy.
func (s *InMemoryStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.checkpoints[checkPointID]
	if !ok {
		return nil, false, nil
	}
	return append([]byte{}, v...), true, nil
}

// Delete removes a checkpoint.
func (s *InMemoryStore) Delete(_ context.Context, checkPointID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkpoints, checkPointID)
	return nil
}
