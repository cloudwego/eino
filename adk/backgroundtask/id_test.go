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

package backgroundtask

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBase62(t *testing.T) {
	assert.Equal(t, "0", base62(0))
	assert.Equal(t, "A", base62(10))
	assert.Equal(t, "10", base62(62))
	// Round-trippable shape: only alphabet chars, non-empty.
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	for _, n := range []int64{1, 61, 100, 1 << 40, (1 << 63) - 1} {
		s := base62(n)
		assert.NotEmpty(t, s)
		for _, c := range s {
			assert.True(t, strings.ContainsRune(alphabet, c), "char %q not in alphabet", c)
		}
	}
}

func TestTaskIDPrefix(t *testing.T) {
	assert.Equal(t, "bash", taskIDPrefix("bash"))
	assert.Equal(t, "subagent", taskIDPrefix("subagent"))
	assert.Equal(t, defaultTaskIDPrefix, taskIDPrefix(""))
}

// IDs minted in a tight loop within one process must never collide, and must
// carry the task-type prefix.
func TestCreateTask_IDsUniqueAndPrefixed(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	const n = 20000
	seen := make(map[string]struct{}, n)
	for i := 0; i < n; i++ {
		id, err := m.createTask(&RunInput{Type: "bash", Description: "x"})
		if err != nil {
			t.Fatalf("createTask: %v", err)
		}
		assert.True(t, strings.HasPrefix(id, "bash_"), "id %q missing type prefix", id)
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate id generated: %q", id)
		}
		seen[id] = struct{}{}
	}
	assert.Len(t, seen, n)
}

// An empty task type falls back to the generic prefix.
func TestCreateTask_EmptyTypePrefix(t *testing.T) {
	m := New(context.Background(), &Config{})
	defer closeWithTimeout(m)

	id, err := m.createTask(&RunInput{Description: "x"})
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(id, defaultTaskIDPrefix+"_"), "id %q", id)
}
