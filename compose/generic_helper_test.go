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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// TestInputKeyedStreamNilMapValue verifies that a nil value under a node's
// input key is reported as a type mismatch instead of panicking while the
// error message is being built.
func TestInputKeyedStreamNilMapValue(t *testing.T) {
	newGraph := func(t *testing.T, node *Lambda) Runnable[map[string]any, string] {
		g := NewGraph[map[string]any, string]()
		require.NoError(t, g.AddLambdaNode("n", node, WithInputKey("x")))
		require.NoError(t, g.AddEdge(START, "n"))
		require.NoError(t, g.AddEdge("n", END))

		r, err := g.Compile(context.Background())
		require.NoError(t, err)
		return r
	}

	input := func() *schema.StreamReader[map[string]any] {
		return schema.StreamReaderFromArray([]map[string]any{{"x": nil}})
	}

	t.Run("invokable consumer", func(t *testing.T) {
		r := newGraph(t, InvokableLambda(
			func(_ context.Context, in string) (string, error) { return in, nil },
		))

		_, err := r.Transform(context.Background(), input())
		require.Error(t, err)
		require.NotContains(t, err.Error(), "panic error")
		require.Contains(t, err.Error(), "value type[<nil>] isn't expected type[string]")
	})

	t.Run("stream passthrough", func(t *testing.T) {
		r := newGraph(t, TransformableLambda(
			func(_ context.Context, in *schema.StreamReader[string]) (*schema.StreamReader[string], error) {
				return in, nil
			},
		))

		out, err := r.Transform(context.Background(), input())
		require.NoError(t, err)
		defer out.Close()

		var recvErr error
		require.NotPanics(t, func() {
			_, recvErr = out.Recv()
		})
		require.Error(t, recvErr)
		require.Contains(t, recvErr.Error(), "value type[<nil>] isn't expected type[string]")
	})
}
