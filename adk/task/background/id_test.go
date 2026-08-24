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

package background

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAllocateTaskIDIsOpaqueAndDoesNotCreateRecord(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	defer closeWithTimeout(manager)

	seen := make(map[string]struct{}, 1000)
	for _, kind := range []string{"", "subagent", "bash"} {
		for i := 0; i < 1000; i++ {
			id, err := manager.AllocateTaskID(
				context.Background(), &AllocateTaskIDRequest{Kind: kind},
			)
			require.NoError(t, err)
			prefix := taskIDPrefix(kind) + "_"
			require.True(t, strings.HasPrefix(id, prefix))
			entropy, decodeErr := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, prefix))
			require.NoError(t, decodeErr)
			assert.Len(t, entropy, taskIDEntropyBytes)
			_, duplicate := seen[id]
			require.False(t, duplicate)
			seen[id] = struct{}{}
		}
	}
	for id := range seen {
		_, err := manager.Get(context.Background(), id)
		assert.ErrorIs(t, err, ErrNotFound)
		break
	}
}

type taskIDContextKey struct{}

func TestAllocateTaskIDGeneratorReceivesKind(t *testing.T) {
	const wantID = "opaque-id"
	ctx := context.WithValue(context.Background(), taskIDContextKey{}, "trace-1")
	manager := mustNewManager(t, context.Background(), &Config{
		IDGen: func(ctx context.Context, request *AllocateTaskIDRequest) (string, error) {
			assert.Equal(t, "subagent", request.Kind)
			assert.Equal(t, "trace-1", ctx.Value(taskIDContextKey{}))
			return wantID, nil
		},
	})
	defer closeWithTimeout(manager)

	id, err := manager.AllocateTaskID(ctx, &AllocateTaskIDRequest{Kind: "subagent"})
	require.NoError(t, err)
	assert.Equal(t, wantID, id)
	_, err = manager.Get(context.Background(), id)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestAllocateTaskIDRejectsInvalidGeneratorOutput(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		manager := mustNewManager(t, context.Background(), &Config{
			IDGen: func(context.Context, *AllocateTaskIDRequest) (string, error) { return "", nil },
		})
		defer closeWithTimeout(manager)
		_, err := manager.AllocateTaskID(context.Background(), &AllocateTaskIDRequest{})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty id")
	})

	t.Run("error", func(t *testing.T) {
		wantErr := errors.New("allocate id")
		manager := mustNewManager(t, context.Background(), &Config{
			IDGen: func(context.Context, *AllocateTaskIDRequest) (string, error) { return "", wantErr },
		})
		defer closeWithTimeout(manager)
		_, err := manager.AllocateTaskID(context.Background(), &AllocateTaskIDRequest{})
		assert.ErrorIs(t, err, wantErr)
	})
}

func TestAllocateTaskIDRejectsUnsafeKind_BitsUT(t *testing.T) {
	manager := mustNewManager(t, context.Background(), nil)
	defer closeWithTimeout(manager)
	for _, kind := range []string{"path/segment", "has space", strings.Repeat("x", maxTaskIDKindBytes+1)} {
		_, err := manager.AllocateTaskID(
			context.Background(), &AllocateTaskIDRequest{Kind: kind},
		)
		require.ErrorContains(t, err, "safe identifier segment")
	}
}

func TestAllocateTaskIDRejectsClosedManagerBeforeCustomGenerator(t *testing.T) {
	var calls int
	manager := mustNewManager(t, context.Background(), &Config{
		IDGen: func(context.Context, *AllocateTaskIDRequest) (string, error) {
			calls++
			return "host-id", nil
		},
	})
	require.NoError(t, manager.Close(context.Background()))
	_, err := manager.AllocateTaskID(context.Background(), &AllocateTaskIDRequest{Kind: "bash"})
	require.ErrorContains(t, err, "shut down")
	assert.Zero(t, calls)
}
