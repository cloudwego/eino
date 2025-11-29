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

package adk

import (
	"context"
	"sync"
)

// InMemorySessionStore is a simple in-memory implementation of SessionStore.
// It uses per-sessionID locking to ensure only one Runner can operate on a
// given sessionID at a time, while allowing concurrent access to different sessions.
// This is useful for testing and for scenarios where persistence is not required.
type InMemorySessionStore struct {
	data  map[string][][]byte
	locks map[string]*sync.Mutex
	mu    sync.Mutex // Protects the maps themselves
}

// NewInMemorySessionStore creates a new in-memory session store.
func NewInMemorySessionStore() *InMemorySessionStore {
	return &InMemorySessionStore{
		data:  make(map[string][][]byte),
		locks: make(map[string]*sync.Mutex),
	}
}

// getSessionLock returns the mutex for the given sessionID, creating one if it doesn't exist.
func (s *InMemorySessionStore) getSessionLock(sessionID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.locks[sessionID]; !exists {
		s.locks[sessionID] = &sync.Mutex{}
	}
	return s.locks[sessionID]
}

// Get retrieves the session history from the store.
func (s *InMemorySessionStore) Get(ctx context.Context, sessionID string) ([][]byte, error) {
	lock := s.getSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	// Return a copy to avoid concurrent modification
	data := s.data[sessionID]
	if data == nil {
		return [][]byte{}, nil
	}

	result := make([][]byte, len(data))
	copy(result, data)
	return result, nil
}

// Add appends new entries to the session history.
func (s *InMemorySessionStore) Add(ctx context.Context, sessionID string, entries [][]byte) error {
	lock := s.getSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	s.data[sessionID] = append(s.data[sessionID], entries...)
	return nil
}

// Set overwrites the session history with the given entries.
func (s *InMemorySessionStore) Set(ctx context.Context, sessionID string, entries [][]byte) error {
	lock := s.getSessionLock(sessionID)
	lock.Lock()
	defer lock.Unlock()

	// Make a copy to avoid external modification
	entriesCopy := make([][]byte, len(entries))
	copy(entriesCopy, entries)
	s.data[sessionID] = entriesCopy
	return nil
}
