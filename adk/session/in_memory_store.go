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
	"bytes"
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// InMemoryStore is an in-memory SessionStore and CheckPointStore implementation
// suitable for development, testing, and quick prototyping.
type InMemoryStore struct {
	mu          sync.Mutex
	checkpoints map[string][]byte
	events      map[string]map[int]map[int64]adk.EventRecord
	turnEnds    map[string]map[int][]byte
}

// NewInMemoryStore creates a new in-memory store.
func NewInMemoryStore() *InMemoryStore {
	return &InMemoryStore{
		checkpoints: make(map[string][]byte),
		events:      make(map[string]map[int]map[int64]adk.EventRecord),
		turnEnds:    make(map[string]map[int][]byte),
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

func (s *InMemoryStore) AppendEvents(_ context.Context, sessionID string, turnIndex int, entries []adk.EventRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.events[sessionID] == nil {
		s.events[sessionID] = make(map[int]map[int64]adk.EventRecord)
	}
	if s.events[sessionID][turnIndex] == nil {
		s.events[sessionID][turnIndex] = make(map[int64]adk.EventRecord)
	}
	for _, entry := range entries {
		if existing, ok := s.events[sessionID][turnIndex][entry.Seq]; ok {
			if !sameRecord(existing, entry) {
				return fmt.Errorf("conflicting event record for turn=%d seq=%d", turnIndex, entry.Seq)
			}
			continue
		}
		s.events[sessionID][turnIndex][entry.Seq] = entry
	}
	return nil
}

func (s *InMemoryStore) LoadEvents(_ context.Context, sessionID string, fromTurnIndex, toTurnIndex int) ([]adk.EventRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []adk.EventRecord
	sessionEvents := s.events[sessionID]
	for turn := fromTurnIndex; turn <= toTurnIndex; turn++ {
		turnEvents := sessionEvents[turn]
		seqs := make([]int64, 0, len(turnEvents))
		for seq := range turnEvents {
			seqs = append(seqs, seq)
		}
		sort.Slice(seqs, func(i, j int) bool {
			return seqs[i] < seqs[j]
		})
		for _, seq := range seqs {
			records = append(records, turnEvents[seq])
		}
	}
	return records, nil
}

func (s *InMemoryStore) LoadLatestTurnEnd(_ context.Context, sessionID string) (int, []byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	latest := 0
	sessionTurnEnds := s.turnEnds[sessionID]
	for turnIndex := range sessionTurnEnds {
		if turnIndex > latest {
			latest = turnIndex
		}
	}
	if latest == 0 {
		return 0, nil, false, nil
	}
	return latest, append([]byte{}, sessionTurnEnds[latest]...), true, nil
}

func (s *InMemoryStore) SaveTurnEnd(_ context.Context, sessionID string, turnIndex int, turnEnd []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.turnEnds[sessionID] == nil {
		s.turnEnds[sessionID] = make(map[int][]byte)
	}
	s.turnEnds[sessionID][turnIndex] = append([]byte{}, turnEnd...)
	return nil
}

func sameRecord(a, b adk.EventRecord) bool {
	return a.TurnIndex == b.TurnIndex &&
		a.Seq == b.Seq &&
		a.Kind == b.Kind &&
		bytes.Equal(a.Payload, b.Payload)
}
