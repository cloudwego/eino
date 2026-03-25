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

package dream

import (
	"context"
	"sync"
	"time"
)

// ScheduleState stores per-memory-directory scheduling state.
type ScheduleState struct {
	// LastConsolidatedAt is the completion time of the last successful run.
	LastConsolidatedAt time.Time

	// NextCheckAt is the next time the middleware should re-check this directory.
	NextCheckAt time.Time
}

// Store persists middleware scheduling state for one resolved `MemoryDirectory`,
// including touched sessions, backoff state, and the run lock.
type Store interface {
	// RecordSessionTouch records that a session produced new signal.
	RecordSessionTouch(ctx context.Context, memoryDir, sessionID string, at time.Time) error

	// ListSessionsTouchedSince returns distinct sessions touched after `since`.
	ListSessionsTouchedSince(ctx context.Context, memoryDir string, since time.Time) ([]string, error)

	// GetScheduleState loads the scheduling state for one memory directory.
	GetScheduleState(ctx context.Context, memoryDir string) (*ScheduleState, error)

	// SetScheduleState persists the scheduling state.
	// Passing nil should clear it when supported.
	SetScheduleState(ctx context.Context, memoryDir string, state *ScheduleState) error

	// AcquireRunLock tries to acquire the per-memory-directory run lock.
	// It returns `ok=false` when another process already holds the lock.
	AcquireRunLock(ctx context.Context, memoryDir string, ttl time.Duration) (unlock func(context.Context) error, ok bool, err error)
}

type localStore struct {
	mu      sync.Mutex
	touches map[string]map[string]time.Time
	states  map[string]ScheduleState
	locks   map[string]time.Time
}

// NewLocalStore returns an in-process `Store`.
// It is suitable for tests and single-process use only.
func NewLocalStore() Store {
	return &localStore{
		touches: make(map[string]map[string]time.Time),
		states:  make(map[string]ScheduleState),
		locks:   make(map[string]time.Time),
	}
}

func (s *localStore) RecordSessionTouch(_ context.Context, memoryDir, sessionID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touches[memoryDir] == nil {
		s.touches[memoryDir] = make(map[string]time.Time)
	}
	s.touches[memoryDir][sessionID] = at
	st := s.states[memoryDir]
	if st.NextCheckAt.IsZero() {
		st.NextCheckAt = at
		s.states[memoryDir] = st
	}
	return nil
}

func (s *localStore) ListSessionsTouchedSince(_ context.Context, memoryDir string, since time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.touches[memoryDir]
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(items))
	for sessionID, touchedAt := range items {
		if touchedAt.After(since) {
			out = append(out, sessionID)
		}
	}
	return out, nil
}

func (s *localStore) GetScheduleState(_ context.Context, memoryDir string) (*ScheduleState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[memoryDir]
	cp := st
	return &cp, nil
}

func (s *localStore) SetScheduleState(_ context.Context, memoryDir string, state *ScheduleState) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if state == nil {
		delete(s.states, memoryDir)
		return nil
	}
	s.states[memoryDir] = *state
	return nil
}

func (s *localStore) AcquireRunLock(_ context.Context, memoryDir string, ttl time.Duration) (func(context.Context) error, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if until, ok := s.locks[memoryDir]; ok && until.After(time.Now()) {
		return nil, false, nil
	}
	s.locks[memoryDir] = time.Now().Add(ttl)
	return func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		delete(s.locks, memoryDir)
		return nil
	}, true, nil
}
