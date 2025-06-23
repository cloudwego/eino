/*
 * Copyright 2024 CloudWeGo Authors
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
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/internal/mock/components/embedding"
	"github.com/cloudwego/eino/internal/mock/components/indexer"
	"github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWorkflow(t *testing.T) {
	ctx := context.Background()

	type structA struct {
		Field1 string
		Field2 int
		Field3 []any
	}

	type structB struct {
		Field1 string
		Field2 int
	}

	type structC struct {
		Field1 string
	}

	type structE struct {
		Field1 string
		Field2 string
		Field3 []any
	}

	type structF struct {
		Field1    string
		Field2    string
		Field3    []any
		B         int
		StateTemp string
	}
	RegisterStreamChunkConcatFunc(func(ts []*structF) (*structF, error) {
		ret := &structF{}
		for _, tt := range ts {
			ret.Field1 += tt.Field1
			ret.Field2 += tt.Field2
			ret.Field3 = append(ret.Field3, tt.Field3...)
			ret.B += tt.B
			ret.StateTemp += tt.StateTemp
		}
		return ret, nil
	})

	type state struct {
		temp string
	}

	type structEnd struct {
		Field1 string
	}

	subGraph := NewGraph[string, *structB]()
	_ = subGraph.AddLambdaNode(
		"1",
		InvokableLambda(func(ctx context.Context, input string) (*structB, error) {
			return &structB{Field1: input, Field2: 33}, nil
		}),
	)
	_ = subGraph.AddEdge(START, "1")
	_ = subGraph.AddEdge("1", END)

	subChain := NewChain[any, *structC]().
		AppendLambda(InvokableLambda(func(_ context.Context, in any) (*structC, error) {
			return &structC{Field1: fmt.Sprintf("%d", in)}, nil
		}))

	type struct2 struct {
		F map[string]any
	}
	subWorkflow := NewWorkflow[[]any, []any]()
	subWorkflow.AddLambdaNode(
		"1",
		InvokableLambda(func(_ context.Context, in []any) ([]any, error) {
			return in, nil
		}),
		WithOutputKey("key")).
		AddInput(START) // []any -> map["key"][]any
	subWorkflow.AddLambdaNode(
		"2",
		InvokableLambda(func(_ context.Context, in []any) ([]any, error) {
			return in, nil
		}),
		WithInputKey("key"),
		WithOutputKey("key1")).
		AddInput("1") // map["key"][]any -> []any -> map["key1"][]any
	subWorkflow.AddLambdaNode(
		"3",
		InvokableLambda(func(_ context.Context, in struct2) (map[string]any, error) {
			return in.F, nil
		}),
	).
		AddInput("2", ToField("F")) // map["key1"][]any -> map["F"]map["key1"][]any -> struct2{F: map["key1"]any} -> map["key1"][]any
	subWorkflow.AddLambdaNode(
		"4",
		InvokableLambda(func(_ context.Context, in []any) ([]any, error) {
			return in, nil
		}),
		WithInputKey("key1"),
	).
		AddInput("3") // map["key1"][]any -> []any
	subWorkflow.End().AddInput("4")

	w := NewWorkflow[*structA, *structEnd](WithGenLocalState(func(context.Context) *state { return &state{} }))

	w.
		AddGraphNode("B", subGraph,
			WithStatePostHandler(func(ctx context.Context, out *structB, state *state) (*structB, error) {
				state.temp = out.Field1
				return out, nil
			})).
		AddInput(START, FromField("Field1"))

	w.
		AddGraphNode("C", subChain).
		AddInput(START, FromField("Field2"))

	w.
		AddGraphNode("D", subWorkflow).
		AddInput(START, FromField("Field3"))

	w.
		AddLambdaNode(
			"E",
			TransformableLambda(func(_ context.Context, in *schema.StreamReader[structE]) (*schema.StreamReader[structE], error) {
				return schema.StreamReaderWithConvert(in, func(in structE) (structE, error) {
					if len(in.Field1) > 0 {
						in.Field1 = "E:" + in.Field1
					}
					if len(in.Field2) > 0 {
						in.Field2 = "E:" + in.Field2
					}

					return in, nil
				}), nil
			}),
			WithStreamStatePreHandler(func(ctx context.Context, in *schema.StreamReader[structE], state *state) (*schema.StreamReader[structE], error) {
				temp := state.temp
				return schema.StreamReaderWithConvert(in, func(v structE) (structE, error) {
					if len(v.Field3) > 0 {
						v.Field3 = append(v.Field3, "Pre:"+temp)
					}

					return v, nil
				}), nil
			}),
			WithStreamStatePostHandler(func(ctx context.Context, out *schema.StreamReader[structE], state *state) (*schema.StreamReader[structE], error) {
				return schema.StreamReaderWithConvert(out, func(v structE) (structE, error) {
					if len(v.Field1) > 0 {
						v.Field1 = v.Field1 + "+Post"
					}
					return v, nil
				}), nil
			})).
		AddInput("B", MapFields("Field1", "Field1")).
		AddInput("C", MapFields("Field1", "Field2")).
		AddInput("D", ToField("Field3"))

	w.
		AddLambdaNode(
			"F",
			InvokableLambda(func(ctx context.Context, in *structF) (string, error) {
				return fmt.Sprintf("%v_%v_%v_%v_%v", in.Field1, in.Field2, in.Field3, in.B, in.StateTemp), nil
			}),
			WithStatePreHandler(func(ctx context.Context, in *structF, state *state) (*structF, error) {
				in.StateTemp = state.temp
				return in, nil
			}),
		).
		AddInput("B", MapFields("Field2", "B")).
		AddInput("E",
			MapFields("Field1", "Field1"),
			MapFields("Field2", "Field2"),
			MapFields("Field3", "Field3"),
		)

	w.End().AddInput("F", ToField("Field1"))

	compiled, err := w.Compile(ctx)
	assert.NoError(t, err)

	input := &structA{
		Field1: "1",
		Field2: 2,
		Field3: []any{
			1, "good",
		},
	}
	out, err := compiled.Invoke(ctx, input)
	assert.NoError(t, err)
	assert.Equal(t, &structEnd{"E:1+Post_E:2_[1 good Pre:1]_33_1"}, out)

	outStream, err := compiled.Stream(ctx, input)
	assert.NoError(t, err)
	defer outStream.Close()
	for {
		chunk, err := outStream.Recv()
		if err != nil {
			if err == io.EOF {
				break
			}

			t.Error(err)
			return
		}

		assert.Equal(t, &structEnd{"E:1+Post_E:2_[1 good Pre:1]_33_1"}, chunk)
	}
}

func TestWorkflowWithMap(t *testing.T) {
	ctx := context.Background()

	type structA struct {
		F1 any
	}

	wf := NewWorkflow[map[string]any, map[string]any]()
	wf.AddLambdaNode("lambda1", InvokableLambda(func(ctx context.Context, in map[string]any) (map[string]any, error) {
		return in, nil
	})).AddInput(START, MapFields("map_key", "lambda1_key"))
	wf.AddLambdaNode("lambda2", InvokableLambda(func(ctx context.Context, in *structA) (*structA, error) {
		return in, nil
	})).AddInput(START, MapFields("map_key", "F1"))
	wf.End().AddInput("lambda1", MapFields("lambda1_key", "end_lambda1"))
	wf.End().AddInput("lambda2", MapFields("F1", "end_lambda2"))
	r, err := wf.Compile(ctx)
	assert.NoError(t, err)
	out, err := r.Invoke(ctx, map[string]any{"map_key": "value"})
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{"end_lambda1": "value", "end_lambda2": "value"}, out)
}

func TestWorkflowWithNestedFieldMappings(t *testing.T) {
	ctx := context.Background()

	type structA struct {
		F1 string
	}

	type structB struct {
		F1 *structA
		F2 map[string]any
		F3 int
		F4 any
		F5 map[string]structA
		F6 structA
	}

	t.Run("from struct.struct.field", func(t *testing.T) {
		wf := NewWorkflow[*structB, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, &structB{
			F1: &structA{
				F1: "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)

		wf = NewWorkflow[*structB, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F1", "F2"}))
		_, err = wf.Compile(ctx)
		assert.ErrorContains(t, err, "has no field[F2]")
	})

	t.Run("to struct.(non-ptr)struct.field", func(t *testing.T) {
		wf := NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F6", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, &structB{
			F6: structA{
				F1: "hello",
			},
		}, out)
	})

	t.Run("to map.(non-ptr)struct.field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]structA]()
		wf.End().AddInput(START, ToFieldPath([]string{"key", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]structA{
			"key": {
				F1: "hello",
			},
		}, out)
	})

	t.Run("from map.map.field", func(t *testing.T) {
		wf := NewWorkflow[map[string]map[string]string, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, map[string]map[string]string{
			"F1": {
				"F1": "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)

		_, err = r.Invoke(ctx, map[string]map[string]string{
			"F1": {
				"F2": "hello",
			},
		})

		var ie *internalError
		assert.True(t, errors.As(err, &ie))
		var myErr *errMapKeyNotFound
		assert.True(t, errors.As(ie.origError, &myErr))
	})

	t.Run("from struct.map.field", func(t *testing.T) {
		wf := NewWorkflow[*structB, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F2", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, &structB{
			F2: map[string]any{
				"F1": "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)

		_, err = r.Invoke(ctx, &structB{
			F2: map[string]any{
				"F2": "hello",
			},
		})
		var ie *internalError
		assert.True(t, errors.As(err, &ie))
		var myErr *errMapKeyNotFound
		assert.True(t, errors.As(ie.origError, &myErr))
	})

	t.Run("from map.struct.field", func(t *testing.T) {
		wf := NewWorkflow[map[string]*structA, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, map[string]*structA{
			"F1": {
				F1: "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)

		wf = NewWorkflow[map[string]*structA, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F1", "F2"}))
		_, err = wf.Compile(ctx)
		assert.ErrorContains(t, err, "has no field[F2]")
	})

	t.Run("from map[string]any.field", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, string]()
		wf.End().AddInput(START, FromFieldPath([]string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, map[string]any{
			"F1": &structA{
				F1: "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)

		out, err = r.Invoke(ctx, map[string]any{
			"F1": map[string]any{
				"F1": "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)

		_, err = r.Invoke(ctx, map[string]any{
			"F1": 1,
		})

		var ie *internalError
		assert.True(t, errors.As(err, &ie))
		var myErr *errInterfaceNotValidForFieldMapping
		assert.True(t, errors.As(ie.origError, &myErr))
	})

	t.Run("to struct.struct.field", func(t *testing.T) {
		wf := NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, &structB{
			F1: &structA{
				F1: "hello",
			},
		}, out)

		wf = NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F1", "F2"}))
		_, err = wf.Compile(ctx)
		assert.ErrorContains(t, err, "has no field[F2]")
	})

	t.Run("to map.map.field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]map[string]string]()
		wf.End().AddInput(START, ToFieldPath([]string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]map[string]string{
			"F1": {
				"F1": "hello",
			},
		}, out)

		wf1 := NewWorkflow[string, map[string]map[string]int]()
		wf1.End().AddInput(START, ToFieldPath([]string{"F1", "F1"}))
		_, err = wf1.Compile(ctx)
		assert.ErrorContains(t, err, "field[string]-[int] is absolutely not assignable")
	})

	t.Run("to struct.map.field", func(t *testing.T) {
		wf := NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F2", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, &structB{
			F2: map[string]any{
				"F1": "hello",
			},
		}, out)
	})

	t.Run("to map.struct.struct.field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]*structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F1", "F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]*structB{
			"F1": {
				F1: &structA{
					F1: "hello",
				},
			},
		}, out)
	})

	t.Run("to struct.map.struct.field", func(t *testing.T) {
		wf := NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F5", "key", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, &structB{
			F5: map[string]structA{
				"key": {
					F1: "hello",
				},
			},
		}, out)
	})

	t.Run("to map.map.struct(non-ptr).field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]map[string]structA]()
		wf.End().AddInput(START, ToFieldPath([]string{"key1", "key2", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]map[string]structA{
			"key1": {
				"key2": {
					F1: "hello",
				},
			},
		}, out)
	})

	t.Run("to struct.int.field", func(t *testing.T) {
		wf := NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F3", "F1", "F1"}))
		_, err := wf.Compile(ctx)
		assert.ErrorContains(t, err, "type[int] is not valid")
	})

	t.Run("to struct.any.field", func(t *testing.T) {
		wf := NewWorkflow[string, *structB]()
		wf.End().AddInput(START, ToFieldPath([]string{"F4", "F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, &structB{
			F4: map[string]any{
				"F1": map[string]any{
					"F1": "hello",
				},
			},
		}, out)
	})

	t.Run("to map.any.any.field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.End().AddInput(START, ToFieldPath([]string{"Key1", "Key2", "Key3"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"Key1": map[string]any{
				"Key2": map[string]any{
					"Key3": "hello",
				},
			},
		}, out)
	})

	t.Run("to any", func(t *testing.T) {
		wf := NewWorkflow[string, any]()
		wf.End().AddInput(START)
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)
	})

	t.Run("to any.field", func(t *testing.T) {
		wf := NewWorkflow[string, any]()
		wf.End().AddInput(START, ToFieldPath([]string{"Key1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"Key1": "hello",
		}, out)
	})

	t.Run("to interface.field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]fmt.Stringer]()
		wf.End().AddInput(START, ToFieldPath([]string{"Key1", "A"}))
		_, err := wf.Compile(ctx)
		assert.ErrorContains(t, err, "static check failed for mapping [from start to Key1\u001FA(field)], "+
			"the successor has intermediate interface type fmt.Stringer")
	})

	t.Run("both to map.any, and to map.any.field", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.End().AddInput(START, ToFieldPath([]string{"Key1"}), ToFieldPath([]string{"Key1", "Key2"}))
		_, err := wf.Compile(ctx)
		assert.ErrorContains(t, err, "two terminal field paths conflict")
	})

	t.Run("to map.any.any.field1, and to map.any.any.field2", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.End().AddInput(START, ToFieldPath([]string{"Key1", "Key2", "key3"}), ToFieldPath([]string{"Key1", "Key2", "key4"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"Key1": map[string]any{
				"Key2": map[string]any{
					"key3": "hello",
					"key4": "hello",
				},
			},
		}, out)
	})

	t.Run("from nested to nested", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, *structB]()
		wf.End().AddInput(START, MapFieldPaths([]string{"key1", "key2"}, []string{"F1", "F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, map[string]any{
			"key1": map[string]any{
				"key2": "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, &structB{
			F1: &structA{
				F1: "hello",
			},
		}, out)
	})

	t.Run("from nested to normal", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, *structA]()
		wf.End().AddInput(START, MapFieldPaths(FieldPath{"key1", "key2"}, FieldPath{"F1"}))
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, map[string]any{
			"key1": map[string]any{
				"key2": "hello",
			},
		})
		assert.NoError(t, err)
		assert.Equal(t, &structA{
			F1: "hello",
		}, out)
	})
}

func TestWorkflowCompile(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)

	t.Run("compile without add end", func(t *testing.T) {
		w := NewWorkflow[*schema.Message, []*schema.Message]()
		w.AddToolsNode("1", &ToolsNode{}).AddInput(START)
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "end node not set")
	})

	t.Run("type mismatch", func(t *testing.T) {
		w := NewWorkflow[string, string]()
		w.AddToolsNode("1", &ToolsNode{}).AddInput(START)
		w.End().AddInput("1")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, " mismatch")
	})

	t.Run("predecessor's output not struct/struct ptr/map, mapping has FromField", func(t *testing.T) {
		w := NewWorkflow[[]*schema.Document, []string]()

		w.AddIndexerNode("indexer", indexer.NewMockIndexer(ctrl)).AddInput(START, FromField("F1"))
		w.End().AddInput("indexer")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "predecessor output type should be struct")
	})

	t.Run("successor's input not struct/struct ptr/map, mapping has ToField", func(t *testing.T) {
		w := NewWorkflow[[]string, [][]float64]()
		w.AddEmbeddingNode("embedder", embedding.NewMockEmbedder(ctrl)).AddInput(START, ToField("F1"))
		w.End().AddInput("embedder")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "successor input type should be struct")
	})

	t.Run("map to non existing field in predecessor", func(t *testing.T) {
		w := NewWorkflow[*schema.Message, []*schema.Message]()
		w.AddToolsNode("tools_node", &ToolsNode{}).AddInput(START, FromField("non_exist"))
		w.End().AddInput("tools_node")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "type[schema.Message] has no field[non_exist]")
	})

	t.Run("map to not exported field in successor", func(t *testing.T) {
		w := NewWorkflow[string, *FieldMapping]()
		w.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return input, nil
		})).AddInput(START)
		w.End().AddInput("1", ToField("to"))
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "has an unexported field[to]")
	})

	t.Run("map from not exported field in predecessor", func(t *testing.T) {
		w := NewWorkflow[*FieldMapping, string]()
		w.End().AddInput(START, FromField("from"))
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "has an unexported field[from]")
	})

	t.Run("duplicate node key", func(t *testing.T) {
		w := NewWorkflow[[]*schema.Message, []*schema.Message]()
		w.AddChatModelNode("1", model.NewMockChatModel(ctrl)).AddInput(START)
		w.AddToolsNode("1", &ToolsNode{}).AddInput("1")
		w.End().AddInput("1")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "node '1' already present")
	})

	t.Run("from non-existing node", func(t *testing.T) {
		w := NewWorkflow[*schema.Message, []*schema.Message]()
		w.AddToolsNode("1", &ToolsNode{}).AddInput(START)
		w.End().AddInput("2")
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "edge start node '2' needs to be added to graph first")
	})

	t.Run("to map with non-string key type", func(t *testing.T) {
		w := NewWorkflow[string, map[int]any]()
		w.End().AddInput(START, ToField("1"))
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "type[map[int]interface {}] is not a map with string or string alias key")

		type stringAlias string
		w1 := NewWorkflow[string, map[stringAlias]any]()
		w1.End().AddInput(START, ToField("1"))
		r, err := w1.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[stringAlias]any{
			"1": "hello",
		}, out)
	})

	t.Run("from map with non-string key type", func(t *testing.T) {
		w := NewWorkflow[map[int]any, string]()
		w.End().AddInput(START, FromField("1"))
		_, err := w.Compile(ctx)
		assert.ErrorContains(t, err, "type[map[int]interface {}] is not a map with string or string alias key")
		type stringAlias string
		w1 := NewWorkflow[map[stringAlias]any, string]()
		w1.End().AddInput(START, FromField("1"))
		r, err := w1.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, map[stringAlias]any{
			"1": "hello",
		})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)
	})
}

func TestFanInToSameDest(t *testing.T) {
	t.Run("traditional outputKey fan-in with map[string]any", func(t *testing.T) {
		wf := NewWorkflow[string, []*schema.Message]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in, nil
		}), WithOutputKey("q1")).AddInput(START)
		wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in, nil
		}), WithOutputKey("q2")).AddInput(START)
		wf.AddChatTemplateNode("prompt", prompt.FromMessages(schema.Jinja2, schema.UserMessage("{{q1}}_{{q2}}"))).
			AddInput("1", MapFields("q1", "q1")).
			AddInput("2", MapFields("q2", "q2"))
		wf.End().AddInput("prompt")
		c, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := c.Invoke(context.Background(), "query")
		assert.NoError(t, err)
		assert.Equal(t, []*schema.Message{{Role: schema.User, Content: "query_query"}}, out)
	})

	t.Run("fan-in to a field of map", func(t *testing.T) {
		type dest struct {
			F map[string]any
		}

		type in struct {
			A string
			B int
		}

		wf := NewWorkflow[in, dest]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in, nil
		}), WithOutputKey("A")).AddInput(START, FromField("A"))
		wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in int) (output int, err error) {
			return in, nil
		}), WithOutputKey("B")).AddInput(START, FromField("B"))
		wf.End().AddInput("1", ToField("F")).AddInput("2", ToField("F"))
		_, err := wf.Compile(context.Background())
		assert.ErrorContains(t, err, "two terminal field paths conflict for node end: [F], [F]")
	})
}

func TestIndirectEdge(t *testing.T) {
	wf := NewWorkflow[string, map[string]any]()

	wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
		return in + "_" + in, nil
	})).AddInput(START)

	wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in map[string]string) (output string, err error) {
		return in["1"] + "_" + in[START], nil
	})).AddInput("1", ToField("1")).
		AddInputWithOptions(START, []*FieldMapping{ToField(START)}, WithNoDirectDependency())

	wf.End().AddInput("2", ToField("2")).
		AddInputWithOptions("1", []*FieldMapping{ToField("1")}, WithNoDirectDependency())

	r, err := wf.Compile(context.Background())
	assert.NoError(t, err)
	out, err := r.Invoke(context.Background(), "query")
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{"1": "query_query", "2": "query_query_query"}, out)
}

func TestDependencyWithNoInput(t *testing.T) {
	t.Run("simple case", func(t *testing.T) {
		wf := NewWorkflow[string, string]()
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return "useless", nil
		})).AddInput(START)
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_done", nil
		})).AddDependency("0").AddInputWithOptions(START, nil, WithNoDirectDependency())
		wf.End().AddInput("1")
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := r.Invoke(context.Background(), "hello")
		assert.NoError(t, err)
		assert.Equal(t, "hello_done", out)
	})

	t.Run("simple control flow: [Start] --> [Node '0'] --> [End]", func(t *testing.T) {
		// [Start] --> [Node "0"] --> [End]
		wf := NewWorkflow[map[string]any, map[string]any]()
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return map[string]any{
				"result": "result from node 0",
			}, nil
		})).AddDependency(START)
		wf.End().AddInput("0", ToField("final_result")).
			AddInputWithOptions(START, []*FieldMapping{ToField("final_from_start")}, WithNoDirectDependency())
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		ret, err := r.Invoke(context.Background(), map[string]any{
			"input": "hello",
		})
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"final_result": map[string]any{
				"result": "result from node 0",
			},
			"final_from_start": map[string]any{
				"input": "hello",
			},
		}, ret)

		sRet, err := r.Stream(context.Background(), map[string]any{
			"input": "hello",
		})
		assert.NoError(t, err)
		ret, err = concatStreamReader(sRet)
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"final_result": map[string]any{
				"result": "result from node 0",
			},
			"final_from_start": map[string]any{
				"input": "hello",
			},
		}, ret)
	})
}

func TestStaticValue(t *testing.T) {
	t.Run("prefill map", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return in, nil
		})).
			AddInput(START, ToField(START)).
			SetStaticValue(FieldPath{"prefilled"}, "yo-ho")
		wf.End().AddInput("0")
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := r.Invoke(context.Background(), "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{"prefilled": "yo-ho", START: "hello"}, out)
		streamOut, err := r.Stream(context.Background(), "hello")
		assert.NoError(t, err)
		out = map[string]any{}
		for {
			chunk, err := streamOut.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			for k, v := range chunk {
				out[k] = v
			}
		}
		assert.Equal(t, map[string]any{"prefilled": "yo-ho", START: "hello"}, out)
	})

	t.Run("static value and to-all mapping conflict", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, map[string]any]()
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return in, nil
		})).
			AddInput(START).
			SetStaticValue(
				FieldPath{"prefilled"},
				"yo-ho",
			)
		wf.End().AddInput("0")
		_, err := wf.Compile(context.Background())
		assert.ErrorContains(t, err, "entire output has already been mapped for node: 0")
	})

	t.Run("static value and dynamic mapping conflict", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return in, nil
		})).
			AddInput(START, ToField("prefilled")).
			SetStaticValue(FieldPath{"prefilled"}, "yo-ho")
		wf.End().AddInput("0")
		_, err := wf.Compile(context.Background())
		assert.ErrorContains(t, err, "two terminal field paths conflict for node 0: [prefilled], [prefilled]")
	})

	t.Run("all inputs are static values", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return in, nil
		})).
			AddDependency(START).
			SetStaticValue(FieldPath{"a", "b"}, "a_b").
			SetStaticValue(FieldPath{"c", "d"}, "c_d").
			SetStaticValue(FieldPath{"a", "d"}, "a_d")
		wf.End().AddInput("0")
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := r.Invoke(context.Background(), "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"a": map[string]any{
				"b": "a_b",
				"d": "a_d",
			},
			"c": map[string]any{
				"d": "c_d",
			},
		}, out)

		type a struct {
			B string
			D string
		}

		type s struct {
			A a
			C map[string]any
		}

		wf1 := NewWorkflow[string, *s]()
		wf1.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in map[string]any) (output map[string]any, err error) {
			return in, nil
		})).
			AddDependency(START).
			SetStaticValue(FieldPath{"A", "B"}, "a_b").
			SetStaticValue(FieldPath{"C", "D"}, "c_d").
			SetStaticValue(FieldPath{"A", "D"}, "a_d")
		wf1.End().AddInput("0", MapFieldPaths(FieldPath{"A", "B"}, FieldPath{"A", "B"}),
			MapFieldPaths(FieldPath{"A", "D"}, FieldPath{"A", "D"}),
			MapFields("C", "C"))
		r1, err := wf1.Compile(context.Background())
		assert.NoError(t, err)
		out1, err := r1.Stream(context.Background(), "hello")
		assert.NoError(t, err)
		outChunk, err := out1.Recv()
		out1.Close()
		assert.Equal(t, &s{
			A: a{
				B: "a_b",
				D: "a_d",
			},
			C: map[string]any{
				"D": "c_d",
			},
		}, outChunk)
	})
}

func TestBranch(t *testing.T) {
	ctx := context.Background()
	t.Run("simple branch: one predecessor, two successor, one of them is END", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_" + in, nil
		})).AddInputWithOptions(START, nil, WithNoDirectDependency())

		wf.AddPassthroughNode("branch_1").AddInput(START, ToField(START))

		branch := NewGraphBranch(func(ctx context.Context, in map[string]any) (string, error) {
			if in[START] == "hello" {
				return "1", nil
			}
			return END, nil
		}, map[string]bool{
			"1": true,
			END: true,
		})
		wf.AddBranch("branch_1", branch)
		wf.End().AddInput("1", ToField("1")).AddInputWithOptions(START, []*FieldMapping{ToField(START)}, WithNoDirectDependency())
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			"1":   "hello_hello",
			START: "hello",
		}, out)
		out, err = r.Invoke(ctx, "world")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{
			START: "world",
		}, out)
	})

	t.Run("multiple predecessors", func(t *testing.T) {
		wf := NewWorkflow[string, map[string]any]()
		wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_" + in, nil
		})).AddInput(START)
		wf.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_" + in, nil
		})).AddInputWithOptions("1", nil, WithNoDirectDependency())
		wf.AddLambdaNode("0", InvokableLambda(func(ctx context.Context, in string) (output string, err error) {
			return in + "_" + in, nil
		})).AddInput(START)

		wf.AddPassthroughNode("branch_1").AddInput(START, ToField(START)).AddInput("1", ToField("1")).AddDependency("0")
		wf.AddBranch("branch_1", NewGraphBranch(func(ctx context.Context, in map[string]any) (string, error) {
			if in[START].(string) == "hello" {
				return "2", nil
			}
			return END, nil
		}, map[string]bool{
			"2": true,
			END: true,
		}))
		wf.End().AddInput("2", ToField("2")).AddInputWithOptions(START, []*FieldMapping{ToField(START)}, WithNoDirectDependency())
		r, err := wf.Compile(ctx)
		assert.NoError(t, err)
		out, err := r.Invoke(ctx, "hello")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{"2": "hello_hello_hello_hello", START: "hello"}, out)
		out, err = r.Invoke(ctx, "world")
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{START: "world"}, out)
	})
	t.Run("empty input for node after branch", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, map[string]any]()
		wf.AddLambdaNode("start_1", InvokableLambda(func(ctx context.Context, input map[string]any) (map[string]any, error) {
			return map[string]any{}, nil
		})).AddInput("start")
		wf.AddLambdaNode("branch_1", InvokableLambda(func(ctx context.Context, input map[string]any) (map[string]any, error) {
			return map[string]any{}, nil
		}))
		wf.AddPassthroughNode("my_branch").AddInput("start_1")
		wf.AddBranch("my_branch", NewGraphBranch(func(ctx context.Context, input map[string]any) (string, error) {
			return END, nil
		}, map[string]bool{
			"branch_1": true,
			END:        true,
		}))
		wf.End().AddInput("branch_1")
		runner, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		resp, err := runner.Invoke(context.Background(), map[string]any{})
		assert.NoError(t, err)
		assert.Equal(t, resp, (map[string]any)(nil))
	})
}

type goodInterface interface {
	GOOD()
}
type goodStruct struct{}

func (g *goodStruct) GOOD() {}

func TestMayAssignableFieldMapping(t *testing.T) {
	type in struct {
		A goodInterface
	}
	wf := NewWorkflow[in, *goodStruct]()
	wf.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input *goodStruct) (output goodInterface, err error) { return input, nil })).
		AddInput(START, FromField("A"))
	wf.End().AddInput("1")
	ctx := context.Background()
	r, err := wf.Compile(ctx)
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, in{A: &goodStruct{}})
	assert.NoError(t, err)
	result.GOOD()
}

func TestNilValue(t *testing.T) {
	t.Run("from map key with a nil value to map key", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, map[string]any]()
		wf.End().AddInput(START, MapFields("a", "a"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)

		result, err := r.Invoke(context.Background(), map[string]any{"a": nil})
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{"a": nil}, result)
	})

	t.Run("from nil struct field to map key", func(t *testing.T) {
		type in struct {
			A *string
		}
		wf := NewWorkflow[in, map[string]any]()
		wf.End().AddInput(START, MapFields("A", "A"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		result, err := r.Invoke(context.Background(), in{A: nil})
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{"A": (*string)(nil)}, result)
	})

	t.Run("from map key with a nil value to struct field", func(t *testing.T) {
		type out struct {
			A *string
		}
		wf := NewWorkflow[map[string]any, out]()
		wf.End().AddInput(START, MapFields("A", "A"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		result, err := r.Invoke(context.Background(), map[string]any{"A": nil})
		assert.NoError(t, err)
		assert.Equal(t, out{A: (*string)(nil)}, result)
	})

	t.Run("from nil struct field to struct field", func(t *testing.T) {
		type inOut struct {
			A *string
		}
		wf := NewWorkflow[inOut, inOut]()
		wf.End().AddInput(START, MapFields("A", "A"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		result, err := r.Invoke(context.Background(), inOut{A: nil})
		assert.NoError(t, err)
		assert.Equal(t, inOut{A: (*string)(nil)}, result)
	})

	t.Run("from nil to a type that can't be nil", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, int]()
		wf.End().AddInput(START, FromField("a"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]any{"a": nil})
		assert.ErrorContains(t, err, "runtime check failed for mapping [from a(field) of start], field[<nil>]-[int] is absolutely not assignable")
	})

	t.Run("from nil to a map other than map[string]any", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, map[string]fmt.Stringer]()
		wf.End().AddInput(START, MapFields("a", "a"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := r.Invoke(context.Background(), map[string]any{"a": nil})
		assert.Equal(t, map[string]fmt.Stringer{
			"a": nil,
		}, out)
	})
}

func TestStreamFieldMap(t *testing.T) {
	t.Run("multiple incomplete chunks in source stream", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, map[string]any]()
		wf.End().AddInput(START, MapFields("a", "a"), MapFields("b", "b"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)

		sr, sw := schema.Pipe[map[string]any](2)
		sw.Send(map[string]any{"a": 1}, nil)
		sw.Send(map[string]any{"b": 2}, nil)
		sw.Close()
		outputS, err := r.Transform(context.Background(), sr)
		assert.NoError(t, err)
		result, err := concatStreamReader(outputS)
		assert.NoError(t, err)
		assert.Equal(t, map[string]any{"a": 1, "b": 2}, result)
	})
}

func TestRuntimeTypeCheck(t *testing.T) {
	g := NewWorkflow[map[string]any, any]()

	_ = g.
		AddLambdaNode("A", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
			return input, nil
		})).
		AddInput(START, FromField("A"))

	_ = g.AddLambdaNode("B", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	})).
		AddInput(START, FromField("B"))

	_ = g.AddLambdaNode("MergeA", InvokableLambda(func(ctx context.Context, input map[string]any) (output map[string]any, err error) {
		return input, nil
	})).
		AddInput("A", ToField("a")).
		AddInput("B", ToField("b"))

	g.End().AddInput("MergeA")

	ctx := context.Background()
	r, err := g.Compile(ctx)
	assert.NoError(t, err)
	result, err := r.Stream(ctx, map[string]any{"A": "1", "B": "2"})
	assert.NoError(t, err)
	chunk, err := result.Recv()
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{"a": "1", "b": "2"}, chunk)
	chunk, err = result.Recv()
	assert.True(t, errors.Is(err, io.EOF))
}

func TestIntermediateMappingSource(t *testing.T) {
	t.Run("intermediate any source is nil", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, any]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a", "b"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]any{
			"a": nil,
		})
		assert.ErrorContains(t, err, "intermediate source value on path=[a b] is nil for type [interface {}]")
		outStream, err := r.Transform(context.Background(), schema.StreamReaderFromArray([]map[string]any{
			{
				"a": nil,
			},
			{
				"b": "ok",
			},
		}))
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "intermediate source value on path=[a b] is nil for type [interface {}]")
		outStream.Close()
	})

	t.Run("intermediate map source is nil", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, any]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), nil)
		assert.ErrorContains(t, err, "intermediate source value on path=[a] is nil for map type [map[string]interface {}]")
		outStream, err := r.Stream(context.Background(), nil)
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "intermediate source value on path=[a] is nil for map type [map[string]interface {}]")
		outStream.Close()
	})

	t.Run("intermediate map ptr source is nil", func(t *testing.T) {
		wf := NewWorkflow[*map[string]any, any]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), nil)
		assert.ErrorContains(t, err, "intermediate source value on path=[a] is nil for type [*map[string]interface {}]")
		outStream, err := r.Stream(context.Background(), nil)
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "intermediate source value on path=[a] is nil for type [*map[string]interface {}]")
		outStream.Close()
	})

	t.Run("intermediate struct ptr source is nil", func(t *testing.T) {
		type inner struct {
			A string
		}

		wf := NewWorkflow[map[string]*inner, string]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"I", "A"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]*inner{"I": nil})
		assert.ErrorContains(t, err, "intermediate source value on path=[I A] is nil")
		outStream, err := r.Stream(context.Background(), map[string]*inner{"I": nil})
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "intermediate source value on path=[I A] is nil")
		outStream.Close()
	})

	t.Run("intermediate interface source is nil", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, string]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a", "b"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]fmt.Stringer{"a": nil})
		assert.ErrorContains(t, err, "intermediate source value on path=[a b] is nil for type [fmt.Stringer]")
		outStream, err := r.Stream(context.Background(), map[string]fmt.Stringer{"a": nil})
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "intermediate source value on path=[a b] is nil for type [fmt.Stringer]")
		outStream.Close()
	})

	t.Run("intermediate interface source valid", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, string]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a", "A"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		out, err := r.Invoke(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello"}})
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)
		outStream, err := r.Stream(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello"}})
		assert.NoError(t, err)
		out, err = outStream.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "hello", out)
		outStream.Close()
	})

	t.Run("intermediate interface source, source field not found at request time", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, string]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a", "B"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello"}})
		assert.ErrorContains(t, err, "field mapping from a struct field, but field not found. field=B")
		outStream, err := r.Stream(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello"}})
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "field mapping from a struct field, but field not found. field=B")
		outStream.Close()
	})

	t.Run("intermediate interface source, source field not exported at request time", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, string]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a", "c"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello", c: "c"}})
		assert.ErrorContains(t, err, "field mapping from a struct field, but field not exported.")
		outStream, err := r.Stream(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello", c: "c"}})
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "field mapping from a struct field, but field not exported.")
		outStream.Close()
	})

	t.Run("intermediate interface source, type mismatch at request time", func(t *testing.T) {
		wf := NewWorkflow[map[string]fmt.Stringer, int]()
		wf.End().AddInput(START, FromFieldPath(FieldPath{"a", "A"}))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		_, err = r.Invoke(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello"}})
		assert.ErrorContains(t, err, "runtime check failed for mapping [from a\x1fA(field) of start], field[string]-[int] is absolutely not assignable")
		outStream, err := r.Stream(context.Background(), map[string]fmt.Stringer{"a": &goodStruct2{A: "hello"}})
		assert.NoError(t, err)
		_, err = outStream.Recv()
		assert.ErrorContains(t, err, "runtime check failed for mapping [from a\u001FA(field) of start], field[string]-[int] is absolutely not assignable")
		outStream.Close()
	})
}

type goodStruct2 struct {
	A string
	c string
}

func (g *goodStruct2) String() string {
	return g.A
}

func TestSetFanInMergeConfig_RealStreamNode_Workflow(t *testing.T) {
	wf := NewWorkflow[int, map[string]int]()

	wf.AddLambdaNode("s1", StreamableLambda(func(ctx context.Context, input int) (*schema.StreamReader[int], error) {
		sr, sw := schema.Pipe[int](2)
		sw.Send(input+1, nil)
		sw.Send(input+2, nil)
		sw.Close()
		return sr, nil
	})).AddInput(START)

	wf.AddLambdaNode("s2", StreamableLambda(func(ctx context.Context, input int) (*schema.StreamReader[int], error) {
		sr, sw := schema.Pipe[int](2)
		sw.Send(input+10, nil)
		sw.Send(input+20, nil)
		sw.Close()
		return sr, nil
	})).AddInput(START)

	wf.End().AddInput("s1", ToField("s1")).AddInput("s2", ToField("s2"))

	r, err := wf.Compile(context.Background(),
		WithFanInMergeConfig(map[string]FanInMergeConfig{END: {StreamMergeWithSourceEOF: true}}))
	assert.NoError(t, err)

	sr, err := r.Stream(context.Background(), 1)
	assert.NoError(t, err)

	merged := make(map[string]map[int]bool)
	var sourceEOFCount int
	sourceNames := make(map[string]bool)
	for {
		m, e := sr.Recv()
		if e != nil {
			if name, ok := schema.GetSourceName(e); ok {
				sourceEOFCount++
				sourceNames[name] = true
				continue
			}
			if e == io.EOF {
				break
			}
			assert.NoError(t, e)
		}

		for k, v := range m {
			if merged[k] == nil {
				merged[k] = make(map[int]bool)
			}
			merged[k][v] = true
		}
	}

	assert.Equal(t, map[string]map[int]bool{"s1": {2: true, 3: true}, "s2": {11: true, 21: true}}, merged)
	assert.Equal(t, 2, sourceEOFCount, "should receive SourceEOF for each input stream when StreamMergeWithSourceEOF is true")
	assert.True(t, sourceNames["s1"], "should receive SourceEOF from s1")
	assert.True(t, sourceNames["s2"], "should receive SourceEOF from s2")
}

func TestCustomExtractor(t *testing.T) {
	t.Run("custom extract from array element", func(t *testing.T) {
		wf := NewWorkflow[[]int, map[string]int]()
		wf.End().AddInput(START, ToField("a", WithCustomExtractor(func(input any) (any, error) {
			return input.([]int)[0], nil
		})))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		result, err := r.Invoke(context.Background(), []int{1, 2})
		assert.NoError(t, err)
		assert.Equal(t, map[string]int{"a": 1}, result)
	})

	t.Run("mix custom extract with normal mapping", func(t *testing.T) {
		wf := NewWorkflow[map[string]any, map[string]int]()
		wf.End().AddInput(START, ToField("a", WithCustomExtractor(func(input any) (any, error) {
			return input.(map[string]any)["a"].([]any)[0].(map[string]any)["c"], nil
		})), MapFields("b", "b"))
		r, err := wf.Compile(context.Background())
		assert.NoError(t, err)
		result, err := r.Invoke(context.Background(), map[string]any{
			"a": []any{
				map[string]any{
					"c": 1,
				},
			},
			"b": 2,
		})
		assert.NoError(t, err)
		assert.Equal(t, map[string]int{"a": 1, "b": 2}, result)
	})
}
