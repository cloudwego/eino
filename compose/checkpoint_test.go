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

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/callbacks"
	"github.com/cloudwego/eino/schema"
)

type inMemoryStore struct {
	m map[string][]byte
}

func (i *inMemoryStore) Get(_ context.Context, checkPointID string) ([]byte, bool, error) {
	v, ok := i.m[checkPointID]
	return v, ok, nil
}

func (i *inMemoryStore) Set(_ context.Context, checkPointID string, checkPoint []byte) error {
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

func init() {
	schema.Register[testStruct]()
}

func TestSimpleCheckPoint(t *testing.T) {
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
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor), WithCheckPointStore(store), WithInterruptAfterNodes([]string{"1"}), WithInterruptBeforeNodes([]string{"2"}), WithGraphName("root"))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &testStruct{A: ""}, info.State)
	assert.Equal(t, []string{"2"}, info.BeforeNodes)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	assert.Empty(t, info.RerunNodesExtra)
	assert.Empty(t, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
	}))

	rCtx := ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	result, err := r.Invoke(rCtx, "start", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "start1state2", result)

	/*	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
		assert.NotNil(t, err)
		info, ok = ExtractInterruptInfo(err)
		assert.True(t, ok)
		assert.Equal(t, &testStruct{A: ""}, info.State)
		assert.Equal(t, []string{"2"}, info.BeforeNodes)
		assert.Equal(t, []string{"1"}, info.AfterNodes)
		assert.Empty(t, info.RerunNodesExtra)
		assert.Empty(t, info.SubGraphs)
		assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
			Info: &testStruct{
				A: "",
			},
			IsRootCause: true,
		}))

		rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
		streamResult, err := r.Stream(rCtx, "start", WithCheckPointID("2"))
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

		assert.Equal(t, "start1state2", result)*/
}

func TestCustomStructInAn2y(t *testing.T) {
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
	r, err := g.Compile(ctx, WithCheckPointStore(store), WithInterruptAfterNodes([]string{"1"}),
		WithGraphName("root"))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &testStruct{A: ""}, info.State)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	assert.Empty(t, info.RerunNodesExtra)
	assert.Empty(t, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
	}))
	rCtx := ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	result, err := r.Invoke(rCtx, "start", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "start1state2", result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, &testStruct{A: ""}, info.State)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	assert.Empty(t, info.RerunNodesExtra)
	assert.Empty(t, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
	}))

	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	streamResult, err := r.Stream(rCtx, "start", WithCheckPointID("2"))
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
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()), WithGraphName("root"))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: ""},
			AfterNodes:      []string{"1"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))

	rCtx := ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	result, err := r.Invoke(rCtx, "start", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "start11state23", result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: ""},
			AfterNodes:      []string{"1"},
			RerunNodesExtra: make(map[string]any),
			SubGraphs:       map[string]*InterruptInfo{},
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))

	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	streamResult, err := r.Stream(rCtx, "start", WithCheckPointID("2"))
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

type testGraphCallback struct {
	onStartTimes       int
	onEndTimes         int
	onStreamStartTimes int
	onStreamEndTimes   int
	onErrorTimes       int
}

func (t *testGraphCallback) OnStart(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
	if info.Component == ComponentOfGraph {
		t.onStartTimes++
	}
	return ctx
}

func (t *testGraphCallback) OnEnd(ctx context.Context, info *callbacks.RunInfo, _ callbacks.CallbackOutput) context.Context {
	if info.Component == ComponentOfGraph {
		t.onEndTimes++
	}
	return ctx
}

func (t *testGraphCallback) OnError(ctx context.Context, info *callbacks.RunInfo, _ error) context.Context {
	if info.Component == ComponentOfGraph {
		t.onErrorTimes++
	}
	return ctx
}

func (t *testGraphCallback) OnStartWithStreamInput(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
	input.Close()
	if info.Component == ComponentOfGraph {
		t.onStreamStartTimes++
	}
	return ctx
}

func (t *testGraphCallback) OnEndWithStreamOutput(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
	output.Close()
	if info.Component == ComponentOfGraph {
		t.onStreamEndTimes++
	}
	return ctx
}

func TestNestedSubGraph(t *testing.T) {
	sSubG := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))
	err := sSubG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = sSubG.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "2", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
		return in + state.A, nil
	}))
	assert.NoError(t, err)

	err = sSubG.AddEdge(START, "1")
	assert.NoError(t, err)
	err = sSubG.AddEdge("1", "2")
	assert.NoError(t, err)
	err = sSubG.AddEdge("2", END)
	assert.NoError(t, err)

	subG := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{A: ""}
	}))
	err = subG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddGraphNode("2", sSubG, WithGraphCompileOptions(WithInterruptAfterNodes([]string{"1"})), WithStatePreHandler(func(ctx context.Context, in string, state *testStruct) (string, error) {
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
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()), WithGraphName("root"))
	assert.NoError(t, err)

	tGCB := &testGraphCallback{}
	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"), WithCallbacks(tGCB))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: ""},
			AfterNodes:      []string{"1"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))

	rCtx := ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Invoke(rCtx, "start", WithCheckPointID("1"), WithCallbacks(tGCB))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			AfterNodes:      []string{"3"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs: map[string]*InterruptInfo{
				"2": {
					State:           &testStruct{A: ""},
					AfterNodes:      []string{"1"},
					RerunNodesExtra: make(map[string]interface{}),
					SubGraphs:       make(map[string]*InterruptInfo),
				},
			},
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
				{
					Type: AddressSegmentNode,
					ID:   "2",
				},
			},
			Info: &testStruct{
				A: "state",
			},
			Parent: &InterruptCtx{
				ID: "runnable:root",
				Address: Address{
					{
						Type: AddressSegmentRunnable,
						ID:   "root",
					},
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Invoke(rCtx, "start", WithCheckPointID("1"), WithCallbacks(tGCB))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			BeforeNodes:     []string{"4"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "state",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state2"})
	result, err := r.Invoke(rCtx, "start", WithCheckPointID("1"), WithCallbacks(tGCB))
	assert.NoError(t, err)
	assert.Equal(t, `start11state1state24
start1134
state24
3`, result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"), WithCallbacks(tGCB))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: ""},
			AfterNodes:      []string{"1"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Stream(rCtx, "start", WithCheckPointID("2"), WithCallbacks(tGCB))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			AfterNodes:      []string{"3"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs: map[string]*InterruptInfo{
				"2": {
					State:           &testStruct{A: ""},
					AfterNodes:      []string{"1"},
					RerunNodesExtra: make(map[string]interface{}),
					SubGraphs:       make(map[string]*InterruptInfo),
				},
			},
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
				{
					Type: AddressSegmentNode,
					ID:   "2",
				},
			},
			Info: &testStruct{
				A: "state",
			},
			Parent: &InterruptCtx{
				Address: Address{
					{
						Type: AddressSegmentRunnable,
						ID:   "root",
					},
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Stream(rCtx, "start", WithCheckPointID("2"), WithCallbacks(tGCB))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			BeforeNodes:     []string{"4"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "state",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state2"})
	streamResult, err := r.Stream(rCtx, "start", WithCheckPointID("2"), WithCallbacks(tGCB))
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

	assert.Equal(t, 10, tGCB.onStartTimes)       // 3+sSubG*1*3+subG*2*2+g*0
	assert.Equal(t, 3, tGCB.onEndTimes)          // success*3
	assert.Equal(t, 10, tGCB.onStreamStartTimes) // 3+sSubG*1*3+subG*2*2+g*0
	assert.Equal(t, 3, tGCB.onStreamEndTimes)    // success*3
	assert.Equal(t, 14, tGCB.onErrorTimes)       // 2*(sSubG*1*3+subG*2*2+g*0)

	// dag
	r, err = g.Compile(ctx, WithCheckPointStore(newInMemoryStore()), WithNodeTriggerMode(AllPredecessor),
		WithGraphName("root"))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: ""},
			AfterNodes:      []string{"1"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Invoke(rCtx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			AfterNodes:      []string{"3"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs: map[string]*InterruptInfo{
				"2": {
					State:           &testStruct{A: ""},
					AfterNodes:      []string{"1"},
					RerunNodesExtra: make(map[string]interface{}),
					SubGraphs:       make(map[string]*InterruptInfo),
				},
			},
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		ID: "runnable:root;node:2;node:2",
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
				{
					Type: AddressSegmentNode,
					ID:   "2",
				},
			},
			Info: &testStruct{
				A: "state",
			},
			Parent: &InterruptCtx{
				Address: Address{
					{
						Type: AddressSegmentRunnable,
						ID:   "root",
					},
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Invoke(rCtx, "start", WithCheckPointID("1"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			BeforeNodes:     []string{"4"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "state",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state2"})
	result, err = r.Invoke(rCtx, "start", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, `start11state1state24
start1134
state24
3`, result)

	_, err = r.Stream(ctx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: ""},
			AfterNodes:      []string{"1"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Stream(rCtx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			AfterNodes:      []string{"3"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs: map[string]*InterruptInfo{
				"2": {
					State:           &testStruct{A: ""},
					AfterNodes:      []string{"1"},
					RerunNodesExtra: make(map[string]interface{}),
					SubGraphs:       make(map[string]*InterruptInfo),
				},
			},
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
				{
					Type: AddressSegmentNode,
					ID:   "2",
				},
			},
			Info: &testStruct{
				A: "state",
			},
			Parent: &InterruptCtx{
				Address: Address{
					{
						Type: AddressSegmentRunnable,
						ID:   "root",
					},
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state"})
	_, err = r.Stream(rCtx, "start", WithCheckPointID("2"))
	assert.NotNil(t, err)
	info, ok = ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, map[string]*InterruptInfo{
		"2": {
			State:           &testStruct{A: "state"},
			BeforeNodes:     []string{"4"},
			RerunNodesExtra: make(map[string]interface{}),
			SubGraphs:       make(map[string]*InterruptInfo),
		},
	}, info.SubGraphs)
	assert.True(t, info.InterruptContexts[0].EqualsWithoutID(&InterruptCtx{
		Address: Address{
			{
				Type: AddressSegmentRunnable,
				ID:   "root",
			},
			{
				Type: AddressSegmentNode,
				ID:   "2",
			},
		},
		Info: &testStruct{
			A: "state",
		},
		IsRootCause: true,
		Parent: &InterruptCtx{
			Address: Address{
				{
					Type: AddressSegmentRunnable,
					ID:   "root",
				},
			},
		},
	}))
	rCtx = ResumeWithData(ctx, info.InterruptContexts[0].ID, &testStruct{A: "state2"})
	streamResult, err = r.Stream(rCtx, "start", WithCheckPointID("2"))
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
