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

package session_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
)

func TestInMemoryStoreConformance(t *testing.T) {
	session.RunConformanceTests(t, func(testing.TB) adk.SessionStore {
		return session.NewInMemoryStore()
	})
}

func TestInMemoryStoreCheckpointSetGetDelete(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore()

	_, exists, err := store.Get(ctx, "missing")
	require.NoError(t, err)
	assert.False(t, exists)

	require.NoError(t, store.Set(ctx, "k", []byte("payload")))

	got, exists, err := store.Get(ctx, "k")
	require.NoError(t, err)
	require.True(t, exists)
	assert.Equal(t, []byte("payload"), got)

	got[0] = 'X'
	again, _, err := store.Get(ctx, "k")
	require.NoError(t, err)
	assert.Equal(t, []byte("payload"), again, "Get must return an independent copy")

	require.NoError(t, store.Delete(ctx, "k"))
	_, exists, err = store.Get(ctx, "k")
	require.NoError(t, err)
	assert.False(t, exists)
}
