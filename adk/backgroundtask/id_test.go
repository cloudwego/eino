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
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	for _, n := range []int64{1, 61, 100, 1 << 40, (1 << 63) - 1} {
		value := base62(n)
		require.NotEmpty(t, value)
		for _, character := range value {
			assert.True(t, strings.ContainsRune(alphabet, character))
		}
	}
}

func TestAllocateTaskIDIsOpaqueAndDoesNotCreateRecord(t *testing.T) {
	manager := New(context.Background(), nil)
	defer closeWithTimeout(manager)

	seen := make(map[string]struct{}, 20_000)
	for i := 0; i < 20_000; i++ {
		id, err := manager.AllocateTaskID(context.Background())
		require.NoError(t, err)
		require.NotEmpty(t, id)
		_, duplicate := seen[id]
		require.False(t, duplicate)
		seen[id] = struct{}{}
	}
	assert.Empty(t, manager.List())
}

type taskIDContextKey struct{}

func TestAllocateTaskIDGeneratorReceivesNoDomainInput(t *testing.T) {
	const wantID = "opaque-id"
	ctx := context.WithValue(context.Background(), taskIDContextKey{}, "trace-1")
	manager := New(context.Background(), &Config{
		IDGen: func(ctx context.Context, input *RunInput) (string, error) {
			assert.Empty(t, input.Type)
			assert.Equal(t, "trace-1", ctx.Value(taskIDContextKey{}))
			return wantID, nil
		},
	})
	defer closeWithTimeout(manager)

	id, err := manager.AllocateTaskID(ctx)
	require.NoError(t, err)
	assert.Equal(t, wantID, id)
	assert.Empty(t, manager.List())
}

func TestAllocateTaskIDRejectsInvalidGeneratorOutput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		manager := New(context.Background(), &Config{
			IDGen: func(context.Context, *RunInput) (string, error) { return "", nil },
		})
		defer closeWithTimeout(manager)
		_, err := manager.AllocateTaskID(context.Background())
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty id")
	})

	t.Run("error", func(t *testing.T) {
		wantErr := errors.New("allocate id")
		manager := New(context.Background(), &Config{
			IDGen: func(context.Context, *RunInput) (string, error) { return "", wantErr },
		})
		defer closeWithTimeout(manager)
		_, err := manager.AllocateTaskID(context.Background())
		assert.ErrorIs(t, err, wantErr)
	})
}
