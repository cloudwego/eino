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
		var myErr *errMapKeyNotFound
		assert.True(t, errors.As(err, &myErr))
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
		var myErr *errMapKeyNotFound
		assert.True(t, errors.As(err, &myErr))
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
		var myErr *errInterfaceNotValidForFieldMapping
		assert.True(t, errors.As(err, &myErr))
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
}
