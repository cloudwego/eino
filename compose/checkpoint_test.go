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
	"io"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type inMemoryStore struct {
	m map[string][]byte
}

func (i *inMemoryStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	v, ok := i.m[checkPointID]
	return v, ok, nil
}

func (i *inMemoryStore) Set(ctx context.Context, checkPointID string, checkPoint []byte) error {
	i.m[checkPointID] = checkPoint
	return nil
}

func newInMemoryStore() *inMemoryStore {
	return &inMemoryStore{
		m: make(map[string][]byte),
	}
}

type testStruct struct {
	A string
}

func TestSimpleCheckPoint(t *testing.T) {
	RegisterSerializableType[testStruct]("test_struct")

	store := newInMemoryStore()

	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))

	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "2", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
		return in + state.A, nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)
	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor), WithCheckPointStore(store), WithInterruptAfterNodes([]string{"1"}), WithInterruptBeforeNodes([]string{"2"}))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		State:       &testStruct{A: ""},
		BeforeNodes: []string{"2"},
		AfterNodes:  []string{"1"},
	}, info)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 0, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	assert.Equal(t, "start1state2", result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		State:       &testStruct{A: ""},
		BeforeNodes: []string{"2"},
		AfterNodes:  []string{"1"},
	}, info)

	streamResult, err := r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 0, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	result = ""
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		result += chunk
	}

	assert.Equal(t, "start1state2", result)
}

func TestCustomStructInAny(t *testing.T) {
	RegisterSerializableType[testStruct]("test_struct")
	store := newInMemoryStore()
	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output *testStruct, err error) {
		return &testStruct{A: input + "1"}, nil
	}), WithOutputKey("1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input map[string]any) (output string, err error) {
		return input["1"].(*testStruct).A + "2", nil
	}), WithStatePreHandler(func(ctx context.Context, in map[string]any, state *testStruct) (map[string]any, error) {
		in["1"].(*testStruct).A += state.A
		return in, nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(store), WithInterruptAfterNodes([]string{"1"}))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		State:      &testStruct{A: ""},
		AfterNodes: []string{"1"},
	}, info)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 0, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	assert.Equal(t, "start1state2", result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		State:      &testStruct{A: ""},
		AfterNodes: []string{"1"},
	}, info)

	streamResult, err := r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 0, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	result = ""
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		result += chunk
	}

	assert.Equal(t, "start1state2", result)
}

func TestSubGraph(t *testing.T) {
	RegisterSerializableType[testStruct]("test_struct")
	subG := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))
	err := subG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "2", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
		return in + state.A, nil
	}))
	assert.NoError(t, err)

	err = subG.AddEdge(START, "1")
	assert.NoError(t, err)
	err = subG.AddEdge("1", "2")
	assert.NoError(t, err)
	err = subG.AddEdge("2", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("2", subG, WithGraphCompileOptions(WithInterruptAfterNodes([]string{"1"})))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "3", nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g.AddEdge("2", "3")
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: ""},
				AfterNodes: []string{"1"},
			},
		},
	}, info)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	assert.Equal(t, "start11state23", result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: ""},
				AfterNodes: []string{"1"},
			},
		},
	}, info)

	streamResult, err := r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	result = ""
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		result += chunk
	}

	assert.Equal(t, "start11state23", result)
}

func TestNestedSubGraph(t *testing.T) {
	RegisterSerializableType[testStruct]("test_struct")
	ssubG := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))
	err := ssubG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = ssubG.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "2", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
		return in + state.A, nil
	}))
	assert.NoError(t, err)

	err = ssubG.AddEdge(START, "1")
	assert.NoError(t, err)
	err = ssubG.AddEdge("1", "2")
	assert.NoError(t, err)
	err = ssubG.AddEdge("2", END)
	assert.NoError(t, err)

	subG := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))
	err = subG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddGraphNode("2", ssubG, WithGraphCompileOptions(WithInterruptAfterNodes([]string{"1"})), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
		return in + state.A, nil
	}), WithOutputKey("2"))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "3", nil
	}), WithOutputKey("3"))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("4", InvokableLambda(func(ctx context.Context, input map[string]any) (output string, err error) {
		return input["2"].(string) + "4\n" + input["3"].(string) + "4\n" + input["state"].(string) + "4\n", nil
	}), WithStatePreHandler(func(ctx context.Context, in map[string]any, state *testStruct) (map[string]any, error) {
		in["state"] = state.A
		return in, nil
	}))
	assert.NoError(t, err)
	err = subG.AddEdge(START, "1")
	assert.NoError(t, err)
	err = subG.AddEdge("1", "2")
	assert.NoError(t, err)
	err = subG.AddEdge("1", "3")
	assert.NoError(t, err)
	err = subG.AddEdge("3", "4")
	assert.NoError(t, err)
	err = subG.AddEdge("2", "4")
	assert.NoError(t, err)
	err = subG.AddEdge("4", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("2", subG, WithGraphCompileOptions(WithInterruptAfterNodes([]string{"1", "3"}), WithInterruptBeforeNodes([]string{"4"})))
	assert.NoError(t, err)
	err = g.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "3", nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g.AddEdge("2", "3")
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: ""},
				AfterNodes: []string{"1"},
			},
		},
	}, info)
	times := 0
	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: "state"},
				AfterNodes: []string{"3"},
				SubGraphs: map[string]*InterruptInfo{
					"2": {
						State:      &testStruct{A: ""},
						AfterNodes: []string{"1"},
					},
				},
			},
		},
	}, info)
	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		if times == 0 {
			assert.Equal(t, 1, len(path.path))
		} else {
			assert.Equal(t, []string{"2", "2"}, path.path)
			state.(*testStruct).A = "state"
		}
		times++
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:       &testStruct{A: "state"},
				BeforeNodes: []string{"4"},
			},
		},
	}, info)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state2"
		return nil
	}))
	assert.NoError(t, err)
	assert.Equal(t, `start11state1state24
start1134
state24
3`, result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: ""},
				AfterNodes: []string{"1"},
			},
		},
	}, info)
	times = 0
	_, err = r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: "state"},
				AfterNodes: []string{"3"},
				SubGraphs: map[string]*InterruptInfo{
					"2": {
						State:      &testStruct{A: ""},
						AfterNodes: []string{"1"},
					},
				},
			},
		},
	}, info)
	_, err = r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		if times == 0 {
			assert.Equal(t, 1, len(path.path))
		} else {
			assert.Equal(t, []string{"2", "2"}, path.path)
			state.(*testStruct).A = "state"
		}
		times++
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:       &testStruct{A: "state"},
				BeforeNodes: []string{"4"},
			},
		},
	}, info)
	streamResult, err := r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state2"
		return nil
	}))
	assert.NoError(t, err)
	result = ""
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		result += chunk
	}
	assert.Equal(t, `start11state1state24
start1134
state24
3`, result)

	// dag
	r, err = g.Compile(ctx, WithCheckPointStore(newInMemoryStore()), WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: ""},
				AfterNodes: []string{"1"},
			},
		},
	}, info)
	times = 0
	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: "state"},
				AfterNodes: []string{"3"},
				SubGraphs: map[string]*InterruptInfo{
					"2": {
						State:      &testStruct{A: ""},
						AfterNodes: []string{"1"},
					},
				},
			},
		},
	}, info)
	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		if times == 0 {
			assert.Equal(t, 1, len(path.path))
		} else {
			assert.Equal(t, []string{"2", "2"}, path.path)
			state.(*testStruct).A = "state"
		}
		times++
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:       &testStruct{A: "state"},
				BeforeNodes: []string{"4"},
			},
		},
	}, info)
	result, err = r.Invoke(ctx, "start", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state2"
		return nil
	}))
	assert.NoError(t, err)
	assert.Equal(t, `start11state1state24
start1134
state24
3`, result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: ""},
				AfterNodes: []string{"1"},
			},
		},
	}, info)
	times = 0
	_, err = r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:      &testStruct{A: "state"},
				AfterNodes: []string{"3"},
				SubGraphs: map[string]*InterruptInfo{
					"2": {
						State:      &testStruct{A: ""},
						AfterNodes: []string{"1"},
					},
				},
			},
		},
	}, info)
	_, err = r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		if times == 0 {
			assert.Equal(t, 1, len(path.path))
		} else {
			assert.Equal(t, []string{"2", "2"}, path.path)
			state.(*testStruct).A = "state"
		}
		times++
		return nil
	}))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &InterruptInfo{
		SubGraphs: map[string]*InterruptInfo{
			"2": {
				State:       &testStruct{A: "state"},
				BeforeNodes: []string{"4"},
			},
		},
	}, info)
	streamResult, err = r.Stream(ctx, "start", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		assert.Equal(t, 1, len(path.path))
		state.(*testStruct).A = "state2"
		return nil
	}))
	assert.NoError(t, err)
	result = ""
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		result += chunk
	}
	assert.Equal(t, `start11state1state24
start1134
state24
3`, result)
}

func TestDAGInterrupt(t *testing.T) {
	g := NewGraph[string, map[string]any]()
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		time.Sleep(time.Millisecond * 100)
		return input, nil
	}), WithOutputKey("1"))
	assert.NoError(t, err)
	err = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		time.Sleep(time.Millisecond * 200)
		return input, nil
	}), WithOutputKey("2"))
	assert.NoError(t, err)
	err = g.AddPassthroughNode("3")
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge(START, "2")
	assert.NoError(t, err)
	err = g.AddEdge("1", "3")
	assert.NoError(t, err)
	err = g.AddEdge("2", "3")
	assert.NoError(t, err)
	err = g.AddEdge("3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()), WithInterruptAfterNodes([]string{"1", "2"}))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "input", WithCheckPointID("1"))
	info, existed := ExtractInterruptInfo(err)
	assert.True(t, existed)
	assert.Equal(t, []string{"1", "2"}, info.AfterNodes)

	result, err := r.Invoke(ctx, "", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{"1": "input", "2": "input"}, result)
}

func TestRerunNodeInterrupt(t *testing.T) {
	RegisterSerializableType[testStruct]("test struct")

	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{}
	}))

	times := 0
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		defer func() { times++ }()
		if times%2 == 0 {
			return "", InterruptAndRerun
		}
		return input, nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
		return state.A, nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "input", WithCheckPointID("1"))
	info, existed := ExtractInterruptInfo(err)
	assert.True(t, existed)
	assert.Equal(t, []string{"1"}, info.RerunNodes)

	result, err := r.Invoke(ctx, "", WithCheckPointID("1"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	assert.Equal(t, "state", result)

	_, err = r.Stream(ctx, "input", WithCheckPointID("2"))
	info, existed = ExtractInterruptInfo(err)
	assert.True(t, existed)
	assert.Equal(t, []string{"1"}, info.RerunNodes)

	streamResult, err := r.Stream(ctx, "", WithCheckPointID("2"), WithStateModifier(func(ctx context.Context, path NodePath, state any) error {
		state.(*testStruct).A = "state"
		return nil
	}))
	assert.NoError(t, err)
	chunk, err := streamResult.Recv()
	assert.NoError(t, err)
	assert.Equal(t, "state", chunk)
	_, err = streamResult.Recv()
	assert.Equal(t, io.EOF, err)
}
