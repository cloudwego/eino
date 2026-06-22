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

package team

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewNamedLockManager(t *testing.T) {
	m := newNamedLockManager()
	assert.NotNil(t, m)
	assert.NotNil(t, m.locks)
	assert.Empty(t, m.locks)
}

func TestForName_SameName_ReturnsSameLock(t *testing.T) {
	m := newNamedLockManager()
	lk1 := m.ForName("agent-a")
	lk2 := m.ForName("agent-a")
	assert.Same(t, lk1, lk2)
	m.Release("agent-a")
	m.Release("agent-a")
}

func TestForName_DifferentNames_ReturnsDifferentLocks(t *testing.T) {
	m := newNamedLockManager()
	lk1 := m.ForName("agent-a")
	lk2 := m.ForName("agent-b")
	assert.NotSame(t, lk1, lk2)
	m.Release("agent-a")
	m.Release("agent-b")
}

func TestRelease_DropsToZero_NextForNameReturnsNewLock(t *testing.T) {
	m := newNamedLockManager()
	lk1 := m.ForName("agent-a")
	m.Release("agent-a") // refs back to 0, entry freed
	assert.Empty(t, m.locks)
	lk2 := m.ForName("agent-a")
	assert.NotSame(t, lk1, lk2)
	m.Release("agent-a")
}

// TestRelease_WhileReferenced_KeepsSameLock is the core invariant: while any
// holder still references a name, a Release by another holder must NOT swap the
// lock instance out from under it. This is what guarantees mailbox read-modify-
// write stays mutually exclusive even across member removal + same-name reuse.
func TestRelease_WhileReferenced_KeepsSameLock(t *testing.T) {
	m := newNamedLockManager()
	lk1 := m.ForName("agent-a") // refs = 1
	lk2 := m.ForName("agent-a") // refs = 2, same instance
	assert.Same(t, lk1, lk2)

	m.Release("agent-a") // refs = 1, entry must survive
	lk3 := m.ForName("agent-a")
	assert.Same(t, lk1, lk3, "lock must not be reallocated while still referenced")

	m.Release("agent-a")
	m.Release("agent-a")
}

func TestRelease_Unknown_NoPanic(t *testing.T) {
	m := newNamedLockManager()
	m.Release("never-acquired") // must not panic or underflow
	assert.Empty(t, m.locks)
}

func TestForName_ConcurrentAccess(t *testing.T) {
	m := newNamedLockManager()
	const goroutines = 50
	const names = 10

	results := make([][]*sync.RWMutex, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			locks := make([]*sync.RWMutex, names)
			for j := 0; j < names; j++ {
				locks[j] = m.ForName(fmt.Sprintf("name-%d", j))
			}
			results[idx] = locks
		}(i)
	}

	wg.Wait()

	for j := 0; j < names; j++ {
		expected := results[0][j]
		for i := 1; i < goroutines; i++ {
			assert.Same(t, expected, results[i][j])
		}
	}

	// Release all references so the manager reclaims every entry.
	for i := 0; i < goroutines; i++ {
		for j := 0; j < names; j++ {
			m.Release(fmt.Sprintf("name-%d", j))
		}
	}
	assert.Empty(t, m.locks)
}
