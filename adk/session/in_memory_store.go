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
	"fmt"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// InMemoryStoreConfig configures InMemoryStore.
type InMemoryStoreConfig struct {
	// EventSerializer encodes typed session events before storage. Defaults to
	// schema.HumanReadableSerializer.
	EventSerializer schema.Serializer
}

// InMemoryStore is a thread-safe, in-memory implementation of adk.SessionEventStore
// and CheckPointStore (with Delete support). Suitable for testing and
// single-process deployments where durability is not required.
type InMemoryStore[M adk.MessageType] struct {
	mu          sync.Mutex
	events      map[string][][]byte
	eventIDs    map[string][]string
	eventKinds  map[string][]adk.SessionEventKind
	eventIDIdx  map[string]map[string]int
	serializer  schema.Serializer
	checkpoints map[string][]byte
}

type pendingEvent struct {
	eventID string
	kind    adk.SessionEventKind
	data    []byte
}

// NewInMemoryStore creates a new InMemoryStore.
func NewInMemoryStore[M adk.MessageType](cfg *InMemoryStoreConfig) *InMemoryStore[M] {
	return &InMemoryStore[M]{
		events:      make(map[string][][]byte),
		eventIDs:    make(map[string][]string),
		eventKinds:  make(map[string][]adk.SessionEventKind),
		eventIDIdx:  make(map[string]map[string]int),
		serializer:  normalizeSerializer(cfg),
		checkpoints: make(map[string][]byte),
	}
}

// AppendEvents appends events to the session's event log.
func (s *InMemoryStore[M]) AppendEvents(_ context.Context, sessionID string, events []*adk.SessionEvent[M]) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx, ok := s.eventIDIdx[sessionID]
	if !ok {
		idx = make(map[string]int)
		s.eventIDIdx[sessionID] = idx
	}
	seen := make(map[string]struct{}, len(events))
	pending := make([]pendingEvent, 0, len(events))
	for _, e := range events {
		if e == nil || e.EventID == "" {
			return adk.ErrInvalidEventID
		}
		if _, dup := seen[e.EventID]; dup {
			return adk.ErrDuplicateEventID
		}
		seen[e.EventID] = struct{}{}
		if _, dup := idx[e.EventID]; dup {
			return adk.ErrDuplicateEventID
		}
		if err := adk.NormalizeSessionEventKind(e); err != nil {
			return err
		}
		data, err := s.serializer.Marshal(e)
		if err != nil {
			return err
		}
		pending = append(pending, pendingEvent{
			eventID: e.EventID,
			kind:    e.Kind,
			data:    append([]byte{}, data...),
		})
	}
	for _, event := range pending {
		s.events[sessionID] = append(s.events[sessionID], event.data)
		s.eventIDs[sessionID] = append(s.eventIDs[sessionID], event.eventID)
		s.eventKinds[sessionID] = append(s.eventKinds[sessionID], event.kind)
		idx[event.eventID] = len(s.events[sessionID]) - 1
	}
	return nil
}

// LoadEvents loads events with pagination and direction support.
func (s *InMemoryStore[M]) LoadEvents(_ context.Context, sessionID string, opts *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if opts == nil {
		opts = &adk.LoadSessionEventsRequest{}
	}
	if opts.Reverse {
		return s.loadReverse(sessionID, opts)
	}
	return s.loadForward(sessionID, opts)
}

func (s *InMemoryStore[M]) loadForward(sessionID string, opts *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	all := s.events[sessionID]
	idx := s.eventIDIdx[sessionID]
	kinds := s.eventKinds[sessionID]

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

	kindSet := buildKindSet(opts.Kinds)

	var out []*adk.SessionEvent[M]
	hasMore := false
	for i := start; i < len(all); i++ {
		if kindSet != nil {
			if _, match := kindSet[kinds[i]]; !match {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		event, err := s.decodeEvent(all[i], s.eventIDs[sessionID][i], kinds[i])
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}

	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadSessionEventsResult[M]{Events: out, Next: next}, nil
}

func (s *InMemoryStore[M]) loadReverse(sessionID string, opts *adk.LoadSessionEventsRequest) (*adk.LoadSessionEventsResult[M], error) {
	all := s.events[sessionID]
	idx := s.eventIDIdx[sessionID]
	kinds := s.eventKinds[sessionID]

	end := len(all)
	if opts.After != "" {
		pos, ok := idx[opts.After]
		if !ok {
			return nil, adk.ErrEventIDOutOfRange
		}
		end = pos // strictly older: [0, pos)
	}
	if end <= 0 {
		return &adk.LoadSessionEventsResult[M]{}, nil
	}

	kindSet := buildKindSet(opts.Kinds)

	var out []*adk.SessionEvent[M]
	hasMore := false
	for i := end - 1; i >= 0; i-- {
		if kindSet != nil {
			if _, match := kindSet[kinds[i]]; !match {
				continue
			}
		}
		if opts.Limit > 0 && len(out) >= opts.Limit {
			hasMore = true
			break
		}
		event, err := s.decodeEvent(all[i], s.eventIDs[sessionID][i], kinds[i])
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}

	var next string
	if hasMore && len(out) > 0 {
		next = out[len(out)-1].EventID
	}
	return &adk.LoadSessionEventsResult[M]{Events: out, Next: next}, nil
}

func (s *InMemoryStore[M]) currentTailLocked(sessionID string) string {
	ids := s.eventIDs[sessionID]
	if len(ids) == 0 {
		return ""
	}
	return ids[len(ids)-1]
}

func (s *InMemoryStore[M]) decodeEvent(data []byte, eventID string, kind adk.SessionEventKind) (*adk.SessionEvent[M], error) {
	var event adk.SessionEvent[M]
	if err := s.serializer.Unmarshal(data, &event); err != nil {
		return nil, err
	}
	if err := adk.NormalizeSessionEventKind(&event); err != nil {
		return nil, err
	}
	if event.EventID != eventID || event.Kind != kind {
		return nil, fmt.Errorf("adk/session: in-memory event index mismatch for event_id %q", eventID)
	}
	return &event, nil
}

func normalizeSerializer(cfg *InMemoryStoreConfig) schema.Serializer {
	if cfg != nil && cfg.EventSerializer != nil {
		return cfg.EventSerializer
	}
	return &schema.HumanReadableSerializer{}
}

func buildKindSet(kinds []adk.SessionEventKind) map[adk.SessionEventKind]struct{} {
	if len(kinds) == 0 {
		return nil
	}
	set := make(map[adk.SessionEventKind]struct{}, len(kinds))
	for _, k := range kinds {
		set[k] = struct{}{}
	}
	return set
}

// Set stores a checkpoint value.
func (s *InMemoryStore[M]) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[checkPointID] = append([]byte{}, checkPoint...)
	return nil
}

// Get retrieves a checkpoint value. Returns an independent copy.
func (s *InMemoryStore[M]) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.checkpoints[checkPointID]
	if !ok {
		return nil, false, nil
	}
	return append([]byte{}, v...), true, nil
}

// Delete removes a checkpoint.
func (s *InMemoryStore[M]) Delete(_ context.Context, checkPointID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkpoints, checkPointID)
	return nil
}
