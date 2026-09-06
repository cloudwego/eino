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

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/internal/serialization"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type chunkTestStore struct {
	m map[string][]byte
}

func (s *chunkTestStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	v, ok := s.m[checkPointID]
	return v, ok, nil
}

func (s *chunkTestStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
	s.m[checkPointID] = checkPoint
	return nil
}

func (s *chunkTestStore) Delete(_ context.Context, checkPointID string) error {
	delete(s.m, checkPointID)
	return nil
}

func newChunkTestStore() *chunkTestStore {
	return &chunkTestStore{m: make(map[string][]byte)}
}

func newChunkTestCheckpoint(inputs map[string]any) *checkpoint {
	return &checkpoint{
		Channels:       map[string]channel{},
		Inputs:         inputs,
		SkipPreHandler: map[string]bool{},
	}
}

func TestCheckPointChunkingRoundTrip(t *testing.T) {
	store := newChunkTestStore()
	cpr := newCheckPointer(nil, nil, store, nil)
	cpr.chunkSize = 16

	payload := strings.Repeat("x", 100)
	cp := newChunkTestCheckpoint(map[string]any{"node": payload})
	serialized, err := cpr.serializer.Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, cpr.set(context.Background(), "cp", cp))

	// header under the main key with the magic prefix
	header, ok := store.m["cp"]
	require.True(t, ok)
	require.True(t, bytes.HasPrefix(header, []byte(chunkedMagic)))

	var h chunkHeader
	require.NoError(t, json.Unmarshal(header[len(chunkedMagic):], &h))
	assert.Equal(t, len(serialized), h.Size, "header size should match serialized checkpoint size")
	assert.Equal(t, (len(serialized)+15)/16, h.Chunks)

	// each chunk key exists and respects the size limit
	for i := 0; i < h.Chunks; i++ {
		chunk, ok := store.m[chunkKey("cp", i)]
		require.True(t, ok, "chunk %d missing", i)
		assert.LessOrEqual(t, len(chunk), 16)
	}

	// no extra chunk keys
	_, ok = store.m[chunkKey("cp", h.Chunks)]
	assert.False(t, ok)

	// round trip
	got, existed, err := cpr.get(context.Background(), "cp")
	require.NoError(t, err)
	require.True(t, existed)
	assert.Equal(t, payload, got.Inputs["node"])
}

func TestCheckPointChunkingSmallPayloadStaysSingleValue(t *testing.T) {
	store := newChunkTestStore()
	cpr := newCheckPointer(nil, nil, store, nil)
	cpr.chunkSize = 1024

	cp := newChunkTestCheckpoint(map[string]any{"node": "small"})
	require.NoError(t, cpr.set(context.Background(), "cp", cp))

	// single value, no header, no chunk keys
	data := store.m["cp"]
	require.NotNil(t, data)
	assert.False(t, bytes.HasPrefix(data, []byte(chunkedMagic)))
	_, ok := store.m[chunkKey("cp", 0)]
	assert.False(t, ok)

	got, existed, err := cpr.get(context.Background(), "cp")
	require.NoError(t, err)
	require.True(t, existed)
	assert.Equal(t, "small", got.Inputs["node"])
}

func TestCheckPointChunkingZeroSizeDisablesChunking(t *testing.T) {
	store := newChunkTestStore()
	cpr := newCheckPointer(nil, nil, store, nil)

	payload := strings.Repeat("x", 4096)
	cp := newChunkTestCheckpoint(map[string]any{"node": payload})
	require.NoError(t, cpr.set(context.Background(), "cp", cp))

	assert.False(t, bytes.HasPrefix(store.m["cp"], []byte(chunkedMagic)))

	got, existed, err := cpr.get(context.Background(), "cp")
	require.NoError(t, err)
	require.True(t, existed)
	assert.Equal(t, payload, got.Inputs["node"])
}

func TestCheckPointChunkingBackwardCompatibleRead(t *testing.T) {
	store := newChunkTestStore()
	cpr := newCheckPointer(nil, nil, store, nil)

	// a checkpoint written by an older version as a single value is still readable
	cp := newChunkTestCheckpoint(map[string]any{"node": "legacy"})
	legacyData, err := cpr.serializer.Marshal(cp)
	require.NoError(t, err)
	store.m["cp"] = legacyData

	got, existed, err := cpr.get(context.Background(), "cp")
	require.NoError(t, err)
	require.True(t, existed)
	assert.Equal(t, "legacy", got.Inputs["node"])
}

func TestCheckPointChunkingRechunkOverwritesPreviousLayout(t *testing.T) {
	store := newChunkTestStore()
	cpr := newCheckPointer(nil, nil, store, nil)
	cpr.chunkSize = 16

	ctx := context.Background()

	// first write: chunked
	big := newChunkTestCheckpoint(map[string]any{"node": strings.Repeat("a", 100)})
	require.NoError(t, cpr.set(ctx, "cp", big))
	_, ok := store.m[chunkKey("cp", 3)]
	require.True(t, ok)

	// second write with chunking disabled: single value overwrites the chunked layout
	cpr.chunkSize = 0
	small := newChunkTestCheckpoint(map[string]any{"node": "tiny"})
	require.NoError(t, cpr.set(ctx, "cp", small))
	assert.False(t, bytes.HasPrefix(store.m["cp"], []byte(chunkedMagic)))

	got, existed, err := cpr.get(ctx, "cp")
	require.NoError(t, err)
	require.True(t, existed)
	assert.Equal(t, "tiny", got.Inputs["node"])
}

func TestCheckPointChunkingDelete(t *testing.T) {
	store := newChunkTestStore()
	cpr := newCheckPointer(nil, nil, store, nil)
	cpr.chunkSize = 16

	cp := newChunkTestCheckpoint(map[string]any{"node": strings.Repeat("a", 100)})
	require.NoError(t, cpr.set(context.Background(), "cp", cp))

	require.NoError(t, cpr.delete(context.Background(), "cp"))
	assert.Empty(t, store.m)
}

func buildComponentCheckpointGraph() *Graph[string, string] {
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("a", InvokableLambda(func(ctx context.Context, in string) (string, error) {
		return in + "a", nil
	}))
	_ = g.AddLambdaNode("b", InvokableLambda(func(ctx context.Context, in string) (string, error) {
		return in + "b", nil
	}))
	_ = g.AddEdge(START, "a")
	_ = g.AddEdge("a", "b")
	_ = g.AddEdge("b", END)
	return g
}

func TestComponentCheckpointRunCompletes(t *testing.T) {
	store := newChunkTestStore()
	ctx := context.Background()

	r, err := buildComponentCheckpointGraph().Compile(ctx,
		WithCheckPointStore(store), WithComponentCheckpoint())
	require.NoError(t, err)

	result, err := r.Invoke(ctx, "x", WithCheckPointID("run1"))
	require.NoError(t, err)
	assert.Equal(t, "xab", result)

	// a node-boundary checkpoint was persisted without any interrupt
	cpr := newCheckPointer(nil, nil, store, nil)
	cp, existed, err := cpr.get(ctx, "run1")
	require.NoError(t, err)
	require.True(t, existed)
	require.NotNil(t, cp)
	assert.NotEmpty(t, cp.Inputs, "checkpoint should carry next-task inputs")
	assert.Empty(t, cp.InterruptID2Addr, "no interrupt signals at node boundary")
}

func TestComponentCheckpointCrashRecovery(t *testing.T) {
	store := newChunkTestStore()
	ctx := context.Background()
	g := buildComponentCheckpointGraph()

	// run 1: completes normally while persisting node-boundary checkpoints
	r1, err := g.Compile(ctx, WithCheckPointStore(store), WithComponentCheckpoint())
	require.NoError(t, err)
	result1, err := r1.Invoke(ctx, "x", WithCheckPointID("run1"))
	require.NoError(t, err)
	assert.Equal(t, "xab", result1)

	// run 2: a fresh runner resumes from the last persisted boundary
	// (simulating a crash followed by recovery)
	r2, err := g.Compile(ctx, WithCheckPointStore(store), WithComponentCheckpoint())
	require.NoError(t, err)
	result2, err := r2.Invoke(ctx, "", WithCheckPointID("run1"))
	require.NoError(t, err)
	assert.Equal(t, result1, result2)
}

func TestComponentCheckpointStreamSkipped(t *testing.T) {
	store := newChunkTestStore()
	ctx := context.Background()

	r, err := buildComponentCheckpointGraph().Compile(ctx,
		WithCheckPointStore(store), WithComponentCheckpoint())
	require.NoError(t, err)

	out, err := r.Stream(ctx, "x", WithCheckPointID("run1"))
	require.NoError(t, err)
	got := ""
	for {
		chunk, err_ := out.Recv()
		if err_ == io.EOF {
			break
		}
		require.NoError(t, err_)
		got += chunk
	}
	assert.Equal(t, "xab", got)

	// streaming runs skip node-boundary checkpoints to avoid consuming streams
	_, existed := store.m["run1"]
	assert.False(t, existed)
}

func TestComponentCheckpointDisabledByDefault(t *testing.T) {
	store := newChunkTestStore()
	ctx := context.Background()

	r, err := buildComponentCheckpointGraph().Compile(ctx, WithCheckPointStore(store))
	require.NoError(t, err)

	result, err := r.Invoke(ctx, "x", WithCheckPointID("run1"))
	require.NoError(t, err)
	assert.Equal(t, "xab", result)

	_, existed := store.m["run1"]
	assert.False(t, existed)
}

func TestComponentCheckpointWithChunking(t *testing.T) {
	store := newChunkTestStore()
	ctx := context.Background()

	type bigState struct {
		Padding string
	}
	require.NoError(t, serialization.GenericRegister[*bigState]("_eino_TestComponentCheckpointWithChunking_state"))

	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) *bigState {
		return &bigState{}
	}))
	_ = g.AddLambdaNode("a", InvokableLambda(func(ctx context.Context, in string) (string, error) {
		return in + "a", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, st *bigState) (string, error) {
		st.Padding = strings.Repeat("p", 200)
		return in, nil
	}))
	_ = g.AddLambdaNode("b", InvokableLambda(func(ctx context.Context, in string) (string, error) {
		return in + "b", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, st *bigState) (string, error) {
		return in, nil
	}))
	_ = g.AddEdge(START, "a")
	_ = g.AddEdge("a", "b")
	_ = g.AddEdge("b", END)

	// chunk size small enough to force chunking of the state-bearing checkpoint
	r, err := g.Compile(ctx, WithCheckPointStore(store), WithComponentCheckpoint(),
		WithCheckpointChunkSize(64))
	require.NoError(t, err)

	result, err := r.Invoke(ctx, "x", WithCheckPointID("run1"))
	require.NoError(t, err)
	assert.Equal(t, "xab", result)

	// the checkpoint was chunked and remains readable
	assert.True(t, bytes.HasPrefix(store.m["run1"], []byte(chunkedMagic)))

	r2, err := g.Compile(ctx, WithCheckPointStore(store), WithComponentCheckpoint(),
		WithCheckpointChunkSize(64))
	require.NoError(t, err)
	result2, err := r2.Invoke(ctx, "", WithCheckPointID("run1"))
	require.NoError(t, err)
	assert.Equal(t, result, result2)
}
