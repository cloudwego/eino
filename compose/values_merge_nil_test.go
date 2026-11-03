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
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

// Merging fan-in values used to call reflect.Value.Type on a zero Value when a
// branch produced a nil interface, panicking the whole run. These cases cover
// the nil paths through mergeValues.

func Test_mergeValues_nil(t *testing.T) {
	t.Run("all values nil reports an error instead of panicking", func(t *testing.T) {
		_, err := mergeValues([]any{nil, nil}, nil)
		require.ErrorContains(t, err, "every fan-in value is nil")
	})

	t.Run("nil leading value falls back to the first typed value", func(t *testing.T) {
		type mergedType struct {
			N int
		}
		RegisterValuesMergeFunc(func(vs []*mergedType) (*mergedType, error) {
			ret := &mergedType{}
			for _, v := range vs {
				if v == nil {
					continue
				}
				ret.N += v.N
			}
			return ret, nil
		})

		vs := []any{nil, &mergedType{N: 2}, nil, &mergedType{N: 3}}
		got, err := mergeValues(vs, nil)
		require.NoError(t, err)
		assert.Equal(t, &mergedType{N: 5}, got)
	})

	t.Run("nil is rejected for a non-nilable merge type", func(t *testing.T) {
		RegisterValuesMergeFunc(func(vs []int) (int, error) {
			sum := 0
			for _, v := range vs {
				sum += v
			}
			return sum, nil
		})

		_, err := mergeValues([]any{1, nil, 2}, nil)
		require.ErrorContains(t, err, "type mismatch")
	})

	t.Run("nil map contributes no entries", func(t *testing.T) {
		got, err := mergeValues([]any{nil, map[int]int{1: 1}, nil, map[int]int{2: 2}}, nil)
		require.NoError(t, err)
		assert.Equal(t, map[int]int{1: 1, 2: 2}, got)
	})

	t.Run("nil among streams reports an error instead of panicking", func(t *testing.T) {
		ass := []any{
			nil,
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{1: "1"}})),
		}
		_, err := mergeValues(ass, nil)
		require.ErrorContains(t, err, "unexpected type")
		require.ErrorContains(t, err, "<nil>")
	})

	t.Run("nil after a stream reports the offending type", func(t *testing.T) {
		ass := []any{
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{1: "1"}})),
			nil,
		}
		_, err := mergeValues(ass, nil)
		require.ErrorContains(t, err, "unexpected type")
	})
}

// A graph whose branches fan in with nil outputs must not panic the runner.
func Test_graph_fan_in_nil_output(t *testing.T) {
	t.Run("all branches nil", func(t *testing.T) {
		g := NewGraph[any, any]()

		require.NoError(t, g.AddLambdaNode("a", InvokableLambda(func(_ context.Context, _ any) (any, error) {
			return nil, nil
		})))
		require.NoError(t, g.AddLambdaNode("b", InvokableLambda(func(_ context.Context, _ any) (any, error) {
			return nil, nil
		})))
		require.NoError(t, g.AddLambdaNode("c", InvokableLambda(func(_ context.Context, in any) (any, error) {
			return in, nil
		})))

		require.NoError(t, g.AddEdge(START, "a"))
		require.NoError(t, g.AddEdge(START, "b"))
		require.NoError(t, g.AddEdge("a", "c"))
		require.NoError(t, g.AddEdge("b", "c"))
		require.NoError(t, g.AddEdge("c", END))

		r, err := g.Compile(context.Background())
		require.NoError(t, err)

		// Must not panic. The merge cannot infer a type from nil interfaces,
		// so it surfaces an error the caller can act on.
		_, err = r.Invoke(context.Background(), "input")
		require.ErrorContains(t, err, "every fan-in value is nil")
	})

	t.Run("one branch nil merges through the registered func", func(t *testing.T) {
		type nilMerge struct {
			N int
		}
		RegisterValuesMergeFunc(func(vs []*nilMerge) (*nilMerge, error) {
			ret := &nilMerge{}
			for _, v := range vs {
				if v == nil {
					continue
				}
				ret.N += v.N
			}
			return ret, nil
		})

		g := NewGraph[any, any]()

		require.NoError(t, g.AddLambdaNode("a", InvokableLambda(func(_ context.Context, _ any) (any, error) {
			return &nilMerge{N: 2}, nil
		})))
		require.NoError(t, g.AddLambdaNode("b", InvokableLambda(func(_ context.Context, _ any) (any, error) {
			return nil, nil
		})))
		require.NoError(t, g.AddLambdaNode("c", InvokableLambda(func(_ context.Context, in any) (any, error) {
			return in, nil
		})))

		require.NoError(t, g.AddEdge(START, "a"))
		require.NoError(t, g.AddEdge(START, "b"))
		require.NoError(t, g.AddEdge("a", "c"))
		require.NoError(t, g.AddEdge("b", "c"))
		require.NoError(t, g.AddEdge("c", END))

		r, err := g.Compile(context.Background())
		require.NoError(t, err)

		out, err := r.Invoke(context.Background(), "input")
		require.NoError(t, err)
		require.NotNil(t, out)
		assert.Equal(t, &nilMerge{N: 2}, out)
	})
}

// Stream fan-in with a nil value must not panic either.
func Test_mergeValues_nil_stream_control(t *testing.T) {
	// Control: streams without nil still merge normally.
	ass := []any{
		packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{1: "1"}})),
		packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{2: "2"}})),
	}
	isr, err := mergeValues(ass, nil)
	require.NoError(t, err)
	ret, ok := unpackStreamReader[map[int]string](isr.(streamReader))
	require.True(t, ok)
	defer ret.Close()

	got := make(map[int]string)
	for {
		m, e := ret.Recv()
		if e == io.EOF {
			break
		}
		require.NoError(t, e)
		for k, v := range m {
			got[k] = v
		}
	}
	assert.Equal(t, map[int]string{1: "1", 2: "2"}, got)
}
