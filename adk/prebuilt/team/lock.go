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

// lock.go provides namedLockManager, a per-name mutex registry for
// serialising concurrent writers to the same resource (inbox file, config, etc.).

package team

import "sync"

// lockEntry pairs a per-name RWMutex with a reference count so the manager can
// reclaim memory once no one is using the lock, without ever handing out two
// different lock instances for the same name while a holder still exists.
type lockEntry struct {
	lock *sync.RWMutex
	refs int
}

// namedLockManager provides a shared per-name lock so that all writers
// targeting the same named resource (inbox file, config, etc.) use the same mutex.
// This prevents lost updates when multiple agents write concurrently.
//
// Locks are reference counted: ForName hands out the shared mutex and increments
// the count, Release decrements it and frees the entry when the count reaches
// zero. Because an entry is only ever removed when no caller holds a reference,
// concurrent callers for the same name are guaranteed to observe the same lock
// instance — even across a member being removed and a new member reusing the
// same name. This is what makes the mailbox read-modify-write mutually exclusive.
type namedLockManager struct {
	mu    sync.Mutex
	locks map[string]*lockEntry
}

func newNamedLockManager() *namedLockManager {
	return &namedLockManager{locks: make(map[string]*lockEntry)}
}

// ForName returns the shared RWMutex for the given name and increments its
// reference count. It lazily creates a new lock if none exists yet.
//
// Every ForName call MUST be paired with exactly one Release call (typically via
// defer) once the caller is done with the lock, so the manager can reclaim
// unused locks while keeping a single instance alive for all concurrent users.
func (m *namedLockManager) ForName(name string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.locks[name]
	if !ok {
		e = &lockEntry{lock: &sync.RWMutex{}}
		m.locks[name] = e
	}
	e.refs++
	return e.lock
}

// Release decrements the reference count for the given name and frees the lock
// entry once no references remain. It is the counterpart of ForName and must be
// called after the caller has finished using (and unlocked) the mutex.
//
// Releasing only deletes the entry when refs reach zero, i.e. when no holder can
// still be using it, so a later ForName for the same name safely allocates a
// fresh lock with no concurrent user of the old one.
func (m *namedLockManager) Release(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.locks[name]
	if !ok {
		return
	}
	e.refs--
	if e.refs <= 0 {
		delete(m.locks, name)
	}
}
