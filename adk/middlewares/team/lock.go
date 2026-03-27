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

// namedLockManager provides a shared per-name lock so that all writers
// targeting the same named resource (inbox file, config, etc.) use the same mutex.
// This prevents lost updates when multiple agents write concurrently.
type namedLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex
}

func newNamedLockManager() *namedLockManager {
	return &namedLockManager{locks: make(map[string]*sync.RWMutex)}
}

// ForName returns the shared RWMutex for the given name.
// It lazily creates a new one if none exists yet.
func (m *namedLockManager) ForName(name string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lk, ok := m.locks[name]; ok {
		return lk
	}
	lk := &sync.RWMutex{}
	m.locks[name] = lk
	return lk
}

// Remove deletes the lock for the given name, freeing memory.
// Should be called when the associated resource (e.g. inbox) is no longer needed.
func (m *namedLockManager) Remove(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.locks, name)
}
