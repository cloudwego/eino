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
	"crypto/rand"
	"encoding/hex"
	"fmt"
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

// SessionTouch identifies one version of a session's pending dream signal.
type SessionTouch struct {
	SessionID string
	TouchedAt time.Time
	// Version must change every time RecordSessionTouch updates the session.
	Version string
}

// Store persists middleware scheduling state for one resolved `MemoryDirectory`,
// including touched sessions, backoff state, and the run lock.
type Store interface {
	// RecordSessionTouch records that a session produced new signal.
	RecordSessionTouch(ctx context.Context, memoryDir, sessionID string, at time.Time) error

	// ListSessionTouches returns a stable snapshot containing the latest
	// unacknowledged touch for each session.
	ListSessionTouches(ctx context.Context, memoryDir string) ([]SessionTouch, error)

	// AcknowledgeSessionTouches atomically removes only touch versions that still
	// match. A session touched again during a dream run must remain pending.
	AcknowledgeSessionTouches(ctx context.Context, memoryDir string, touches []SessionTouch) error

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
	touches map[string]map[string]SessionTouch
	states  map[string]ScheduleState
	locks   map[string]localRunLock
}

type localRunLock struct {
	token  string
	expiry time.Time
}

// NewLocalStore returns an in-process `Store`.
// It is suitable for tests and single-process use only.
func NewLocalStore() Store {
	return &localStore{
		touches: make(map[string]map[string]SessionTouch),
		states:  make(map[string]ScheduleState),
		locks:   make(map[string]localRunLock),
	}
}

func (s *localStore) RecordSessionTouch(_ context.Context, memoryDir, sessionID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.touches[memoryDir] == nil {
		s.touches[memoryDir] = make(map[string]SessionTouch)
	}
	s.touches[memoryDir][sessionID] = SessionTouch{
		SessionID: sessionID,
		TouchedAt: at,
		Version:   newStoreToken(),
	}
	st := s.states[memoryDir]
	if st.NextCheckAt.IsZero() {
		st.NextCheckAt = at
		s.states[memoryDir] = st
	}
	return nil
}

func (s *localStore) ListSessionTouches(_ context.Context, memoryDir string) ([]SessionTouch, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.touches[memoryDir]
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]SessionTouch, 0, len(items))
	for _, touch := range items {
		out = append(out, touch)
	}
	return out, nil
}

func (s *localStore) AcknowledgeSessionTouches(_ context.Context, memoryDir string, touches []SessionTouch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.touches[memoryDir]
	for _, touch := range touches {
		if latest, ok := current[touch.SessionID]; ok && latest.Version == touch.Version {
			delete(current, touch.SessionID)
		}
	}
	if len(current) == 0 {
		delete(s.touches, memoryDir)
	}
	return nil
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
	now := time.Now()
	if lock, ok := s.locks[memoryDir]; ok && lock.expiry.After(now) {
		return nil, false, nil
	}
	token := newStoreToken()
	s.locks[memoryDir] = localRunLock{token: token, expiry: now.Add(ttl)}
	return func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		lock, ok := s.locks[memoryDir]
		if !ok {
			return nil
		}
		if lock.token != token {
			return fmt.Errorf("dream: lock token mismatch")
		}
		delete(s.locks, memoryDir)
		return nil
	}, true, nil
}

func newStoreToken() string {
	var value [8]byte
	_, _ = rand.Read(value[:])
	return hex.EncodeToString(value[:])
}
