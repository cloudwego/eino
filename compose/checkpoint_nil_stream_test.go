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

package compose

import (
	"context"
	"fmt"
	"io"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestCheckpointPreservesNilStreamChunk(t *testing.T) {
	t.Run("pointer", testCheckpointPreservesNilStreamChunk[*checkpointTypedNilInput])
	t.Run("interface", testCheckpointPreservesNilStreamChunk[any])
	t.Run("map", testCheckpointPreservesNilStreamChunk[map[string]any])
	t.Run("slice", testCheckpointPreservesNilStreamChunk[[]string])
}

func TestCheckpointLegacyNilStreamRestoresAsEmpty(t *testing.T) {
	stream, err := defaultStreamConvertPair[*checkpointTypedNilInput]().restoreStream(nil)
	require.NoError(t, err)
	reader, ok := unpackStreamReader[*checkpointTypedNilInput](stream)
	require.True(t, ok)
	defer reader.Close()
	_, err = reader.Recv()
	require.ErrorIs(t, err, io.EOF)
}

func TestCheckpointNilChunkRejectsNonNullableTarget(t *testing.T) {
	stream, err := defaultStreamConvertPair[int]().restoreStream(nilStreamChunk{Present: true})
	require.Error(t, err)
	require.Nil(t, stream)
}

func testCheckpointPreservesNilStreamChunk[T any](t *testing.T) {
	for _, useGob := range []bool{false, true} {
		for _, count := range []int{0, 1} {
			t.Run(fmt.Sprintf("gob=%v/chunks=%d", useGob, count), func(t *testing.T) {
				testCheckpointNilStreamRoundTrip[T](t, useGob, count)
			})
		}
	}
}

func testCheckpointNilStreamRoundTrip[T any](t *testing.T, useGob bool, expectedCount int) {
	ctx := context.Background()
	graph := NewGraph[T, int]()
	require.NoError(t, graph.AddLambdaNode("count", CollectableLambda(
		func(_ context.Context, input *schema.StreamReader[T]) (int, error) {
			defer input.Close()
			count := 0
			for {
				chunk, err := input.Recv()
				if err == io.EOF {
					return count, nil
				}
				if err != nil {
					return 0, err
				}
				var zero T
				if !reflect.DeepEqual(chunk, zero) {
					return 0, fmt.Errorf("restored chunk = %#v, want nil", chunk)
				}
				count++
			}
		},
	)))
	require.NoError(t, graph.AddEdge(START, "count"))
	require.NoError(t, graph.AddEdge("count", END))
	options := []GraphCompileOption{
		WithCheckPointStore(newInMemoryStore()),
		WithInterruptBeforeNodes([]string{"count"}),
	}
	if useGob {
		options = append(options, WithSerializer(&checkpointGobSerializer{}))
	}
	runnable, err := graph.Compile(ctx, options...)
	require.NoError(t, err)

	_, err = runnable.Collect(ctx,
		schema.StreamReaderFromArray(make([]T, expectedCount)),
		WithCheckPointID("nil-chunk"),
	)
	_, interrupted := ExtractInterruptInfo(err)
	require.True(t, interrupted, "expected checkpoint interrupt, got %v", err)

	count, err := runnable.Collect(ctx,
		schema.StreamReaderFromArray([]T{}),
		WithCheckPointID("nil-chunk"),
	)
	require.NoError(t, err)
	require.Equal(t, expectedCount, count)
}
