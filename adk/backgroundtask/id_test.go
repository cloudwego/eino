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
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		id, err := m.createTask(context.Background(), &RunInput{Type: "bash", Description: "x"})
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

	id, err := m.createTask(context.Background(), &RunInput{Description: "x"})
	assert.NoError(t, err)
	assert.True(t, strings.HasPrefix(id, defaultTaskIDPrefix+"_"), "id %q", id)
}

type taskIDContextKey struct{}

func TestManager_IDGenOverridesDefaultID(t *testing.T) {
	const wantID = "short_000001"
	ctx := context.WithValue(context.Background(), taskIDContextKey{}, "trace-1")
	called := false
	m := New(context.Background(), &Config{
		IDGen: func(ctx context.Context, input *RunInput) (string, error) {
			called = true
			assert.Equal(t, "bash", input.Type)
			assert.Equal(t, "call_1", input.ToolUseID)
			assert.Equal(t, "trace-1", ctx.Value(taskIDContextKey{}))
			return wantID, nil
		},
	})
	defer closeWithTimeout(m)

	result, err := m.Run(ctx, &RunInput{
		Description: "x",
		Type:        "bash",
		ToolUseID:   "call_1",
	}, workReturning("ok", nil))
	require.NoError(t, err)
	assert.True(t, called)
	assert.Equal(t, wantID, result.ID)

	task, ok := m.Get(wantID)
	require.True(t, ok)
	assert.Equal(t, wantID, task.ID)
	assert.Equal(t, "bash", task.Type)
}

func TestCreateTask_IDGenEmptyIDFails(t *testing.T) {
	m := New(context.Background(), &Config{
		IDGen: func(context.Context, *RunInput) (string, error) {
			return "", nil
		},
	})
	defer closeWithTimeout(m)

	_, err := m.createTask(context.Background(), &RunInput{Description: "x"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty id")
	assert.Empty(t, m.List())
}

func TestCreateTask_IDGenDuplicateIDFails(t *testing.T) {
	m := New(context.Background(), &Config{
		IDGen: func(context.Context, *RunInput) (string, error) {
			return "fixed", nil
		},
	})
	defer closeWithTimeout(m)

	id, err := m.createTask(context.Background(), &RunInput{Description: "first"})
	require.NoError(t, err)
	assert.Equal(t, "fixed", id)

	_, err = m.createTask(context.Background(), &RunInput{Description: "second"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), `task id "fixed" already exists`)
	assert.Len(t, m.List(), 1)
}

func TestManager_IDGenErrorFailsRun(t *testing.T) {
	wantErr := errors.New("allocate id")
	m := New(context.Background(), &Config{
		IDGen: func(context.Context, *RunInput) (string, error) {
			return "", wantErr
		},
	})
	defer closeWithTimeout(m)

	_, err := m.Run(context.Background(), &RunInput{Description: "x"}, workReturning("ok", nil))
	require.Error(t, err)
	assert.ErrorIs(t, err, wantErr)
	assert.Contains(t, err.Error(), "task id generator")
	assert.Empty(t, m.List())
}
