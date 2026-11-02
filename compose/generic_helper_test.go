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
	ctx := context.Background()

	g := NewGraph[map[string]any, string]()
	require.NoError(t, g.AddLambdaNode("n", InvokableLambda(
		func(_ context.Context, in string) (string, error) { return in, nil },
	), WithInputKey("x")))
	require.NoError(t, g.AddEdge(START, "n"))
	require.NoError(t, g.AddEdge("n", END))

	r, err := g.Compile(ctx)
	require.NoError(t, err)

	input := schema.StreamReaderFromArray([]map[string]any{{"x": nil}})
	_, err = r.Transform(ctx, input)
	require.Error(t, err)
	require.NotContains(t, err.Error(), "panic error")
	require.Contains(t, err.Error(), "value type[<nil>] isn't expected type[string]")
}
