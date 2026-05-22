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
	"encoding/base64"
	"fmt"
	"strconv"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// InMemoryStore is an in-memory SessionStore and CheckPointStore implementation
// suitable for development, testing, and quick prototyping.
type InMemoryStore struct {
	mu          sync.Mutex
	checkpoints map[string][]byte
	events      map[string][][]byte
	turnEnds    map[string]turnEndRecord
}

type turnEndRecord struct {
	afterCursor string
	data        []byte
}

// NewInMemoryStore creates a new in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		checkpoints: make(map[string][]byte),
		events:      make(map[string][][]byte),
		turnEnds:    make(map[string]turnEndRecord),
	}
}

func (s *InMemoryStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoints[key] = append([]byte{}, value...)
	return nil
}

func (s *InMemoryStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.checkpoints[key]
	if !ok {
		return nil, false, nil
	}
	return append([]byte{}, value...), true, nil
}

func (s *InMemoryStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.checkpoints, key)
	return nil
}

// AppendEvents appends JSON-encoded SessionEvent payloads to the session log.
func (s *InMemoryStore) AppendEvents(_ context.Context, sessionID string, events [][]byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range events {
		s.events[sessionID] = append(s.events[sessionID], append([]byte{}, e...))
	}
	return nil
}

// LoadEvents loads session events with pagination support.
func (s *InMemoryStore) LoadEvents(_ context.Context, sessionID string, opts *adk.LoadEventsRequest) (*adk.LoadEventsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	all := s.events[sessionID]
	total := len(all)

	if opts == nil {
		opts = &adk.LoadEventsRequest{}
	}

	if opts.Reverse {
		// Reverse pagination: After encodes the offset of the next event
		// to return when reading backwards. Initial state: total (read total-1 first).
		var nextOffset int
		if opts.After == "" {
			nextOffset = total
		} else {
			parsed, err := decodeOffset(opts.After)
			if err != nil {
				return nil, fmt.Errorf("invalid After cursor: %w", err)
			}
			nextOffset = parsed
		}
		if nextOffset < 0 {
			nextOffset = 0
		}
		if nextOffset > total {
			nextOffset = total
		}
		limit := opts.Limit
		if limit <= 0 || limit > nextOffset {
			limit = nextOffset
		}
		out := make([][]byte, 0, limit)
		for i := 0; i < limit; i++ {
			idx := nextOffset - 1 - i
			if idx < 0 {
				break
			}
			out = append(out, append([]byte{}, all[idx]...))
		}
		newOffset := nextOffset - limit
		var nextToken string
		if newOffset > 0 {
			nextToken = encodeOffset(newOffset)
		}
		return &adk.LoadEventsResult{Events: out, Next: nextToken}, nil
	}

	// Forward pagination from After. When After is non-empty, only events
	// strictly after that position are returned (used for both initial
	// afterCursor loads and continuation pages).
	startOffset := 0
	if opts.After != "" {
		parsed, err := decodeOffset(opts.After)
		if err != nil {
			return nil, fmt.Errorf("invalid After cursor: %w", err)
		}
		startOffset = parsed
	}
	if startOffset < 0 {
		startOffset = 0
	}
	if startOffset > total {
		startOffset = total
	}
	return paginateForward(all, startOffset, opts.Limit), nil
}

// paginateForward returns up to limit events starting at startOffset.
// limit <= 0 means no limit.
func paginateForward(all [][]byte, startOffset, limit int) *adk.LoadEventsResult {
	total := len(all)
	end := total
	if limit > 0 && startOffset+limit < total {
		end = startOffset + limit
	}
	out := make([][]byte, 0, end-startOffset)
	for i := startOffset; i < end; i++ {
		out = append(out, append([]byte{}, all[i]...))
	}
	var nextToken string
	if end < total {
		nextToken = encodeOffset(end)
	}
	return &adk.LoadEventsResult{Events: out, Next: nextToken}
}

// SaveTurnEnd persists a TurnEndState snapshot. The store captures the current
// event-log tail position internally so tail replay can reload events appended
// after this snapshot via the After field in LoadEventsRequest.
func (s *InMemoryStore) SaveTurnEnd(_ context.Context, sessionID string, turnEnd []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cursor := encodeOffset(len(s.events[sessionID]))
	s.turnEnds[sessionID] = turnEndRecord{
		afterCursor: cursor,
		data:        append([]byte{}, turnEnd...),
	}
	return nil
}

// LoadLatestTurnEnd loads the most recent TurnEndState snapshot for the session.
func (s *InMemoryStore) LoadLatestTurnEnd(_ context.Context, sessionID string) (string, []byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.turnEnds[sessionID]
	if !ok {
		return "", nil, false, nil
	}
	return rec.afterCursor, append([]byte{}, rec.data...), true, nil
}

// encodeOffset encodes an integer offset as an opaque base64-encoded cursor.
func encodeOffset(offset int) string {
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

// decodeOffset decodes a cursor produced by encodeOffset.
func decodeOffset(s string) (int, error) {
	raw, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return 0, err
	}
	n, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, err
	}
	return n, nil
}
