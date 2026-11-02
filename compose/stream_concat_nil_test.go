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
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func TestInvokeConcatenatesNilInterfaceChunks(t *testing.T) {
	t.Run("any", func(t *testing.T) {
		testInvokeConcatenatesNilChunks[any](t, []any{nil, nil}, nil)
	})
	t.Run("non-empty interface", func(t *testing.T) {
		testInvokeConcatenatesNilChunks[fmt.Stringer](t, []fmt.Stringer{nil, nil}, nil)
	})
	t.Run("typed nil pointer", func(t *testing.T) {
		testInvokeConcatenatesNilChunks[*strings.Builder](t, []*strings.Builder{nil, nil}, nil)
	})
	t.Run("one non-nil value", func(t *testing.T) {
		value := &strings.Builder{}
		testInvokeConcatenatesNilChunks[any](t, []any{nil, value, nil}, value)
	})
}

func testInvokeConcatenatesNilChunks[O any](t *testing.T, chunks []O, expected O) {
	for _, transform := range []bool{false, true} {
		t.Run(fmt.Sprintf("transform=%v", transform), func(t *testing.T) {
			var lambda *Lambda
			if transform {
				lambda = TransformableLambda(func(_ context.Context, input *schema.StreamReader[string]) (*schema.StreamReader[O], error) {
					input.Close()
					return schema.StreamReaderFromArray(chunks), nil
				})
			} else {
				lambda = StreamableLambda(func(_ context.Context, _ string) (*schema.StreamReader[O], error) {
					return schema.StreamReaderFromArray(chunks), nil
				})
			}
			graph := NewGraph[string, O]()
			require.NoError(t, graph.AddLambdaNode("produce", lambda))
			require.NoError(t, graph.AddEdge(START, "produce"))
			require.NoError(t, graph.AddEdge("produce", END))
			runnable, err := graph.Compile(context.Background())
			require.NoError(t, err)

			output, err := runnable.Invoke(context.Background(), "input")

			require.NoError(t, err)
			require.Equal(t, expected, output)
		})
	}
}
