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
	"io"
	"sync"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/callbacks"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/internal/serialization"
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
	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) (state *testStruct) {
		return &testStruct{}
	}))

	times := 0
	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		defer func() { times++ }()
		if times%2 == 0 {
			return "", NewInterruptAndRerunErr("test extra")
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
	assert.Equal(t, "test extra", info.RerunNodesExtra["1"].(string))

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

type myInterface interface {
	A()
}

func TestInterfaceResume(t *testing.T) {
	g := NewGraph[myInterface, string]()
	times := 0
	assert.NoError(t, g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input myInterface) (output string, err error) {
		if times == 0 {
			times++
			return "", NewInterruptAndRerunErr("test extra")
		}
		return "success", nil
	})))
	assert.NoError(t, g.AddEdge(START, "1"))
	assert.NoError(t, g.AddEdge("1", END))

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, nil, WithCheckPointID("1"))
	info, existed := ExtractInterruptInfo(err)
	assert.True(t, existed)
	assert.Equal(t, []string{"1"}, info.RerunNodes)
	result, err := r.Invoke(ctx, nil, WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "success", result)
}

func TestEarlyFailCallback(t *testing.T) {
	g := NewGraph[string, string]()
	assert.NoError(t, g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	})))
	assert.NoError(t, g.AddEdge(START, "1"))
	assert.NoError(t, g.AddEdge("1", END))

	ctx := context.Background()
	r, err := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))
	assert.NoError(t, err)
	tGCB := &testGraphCallback{}
	_, _ = r.Invoke(ctx, "", WithCallbacks(tGCB), WithRuntimeMaxSteps(1))
	assert.Equal(t, 1, tGCB.onStartTimes)
	assert.Equal(t, 1, tGCB.onErrorTimes)
	assert.Equal(t, 0, tGCB.onEndTimes)
}

func TestGraphStartInterrupt(t *testing.T) {
	subG := NewGraph[string, string]()
	_ = subG.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "sub1", nil
	}))
	_ = subG.AddEdge(START, "1")
	_ = subG.AddEdge("1", END)

	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	_ = g.AddGraphNode("2", subG, WithGraphCompileOptions(WithInterruptBeforeNodes([]string{"1"})))
	_ = g.AddEdge(START, "1")
	_ = g.AddEdge("1", "2")
	_ = g.AddEdge("2", END)

	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "input", WithCheckPointID("1"))
	info, existed := ExtractInterruptInfo(err)
	assert.True(t, existed)
	assert.Equal(t, []string{"1"}, info.SubGraphs["2"].BeforeNodes)
	result, err := r.Invoke(ctx, "", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "input1sub1", result)
}

func TestWithForceNewRun(t *testing.T) {
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	_ = g.AddEdge(START, "1")
	_ = g.AddEdge("1", END)
	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(&failStore{t: t}))
	assert.NoError(t, err)
	result, err := r.Invoke(ctx, "input", WithCheckPointID("1"), WithForceNewRun())
	assert.NoError(t, err)
	assert.Equal(t, "input1", result)
}

type failStore struct {
	t *testing.T
}

func (f *failStore) Get(_ context.Context, _ string) ([]byte, bool, error) {
	f.t.Fatalf("cannot call store")
	return nil, false, errors.New("fail")
}

func (f *failStore) Set(_ context.Context, _ string, _ []byte) error {
	f.t.Fatalf("cannot call store")
	return errors.New("fail")
}

func TestPreHandlerInterrupt(t *testing.T) {
	type state struct{}
	assert.NoError(t, serialization.GenericRegister[state]("_eino_TestPreHandlerInterrupt_state"))
	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) state {
		return state{}
	}))
	times := 0
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, state state) (string, error) {
		if times == 0 {
			times++
			return "", NewInterruptAndRerunErr("")
		}
		return in, nil
	}))
	_ = g.AddEdge(START, "1")
	_ = g.AddEdge("1", END)
	ctx := context.Background()
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)
	_, err = r.Invoke(ctx, "input", WithCheckPointID("1"))
	info, existed := ExtractInterruptInfo(err)
	assert.True(t, existed)
	assert.Equal(t, []string{"1"}, info.RerunNodes)
	result, err := r.Invoke(ctx, "", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "1", result)
}

func TestCancelInterrupt(t *testing.T) {
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		time.Sleep(3 * time.Second)
		return input + "1", nil
	}))
	_ = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "2", nil
	}))
	_ = g.AddEdge(START, "1")
	_ = g.AddEdge("1", "2")
	_ = g.AddEdge("2", END)
	ctx := context.Background()

	// pregel
	r, err := g.Compile(ctx, WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)
	// interrupt after nodes
	canceledCtx, cancel := WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(time.Hour))
	}()
	_, err = r.Invoke(canceledCtx, "input", WithCheckPointID("1"))
	assert.Error(t, err)
	info, success := ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	result, err := r.Invoke(ctx, "input", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "input12", result)
	// infinite timeout
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	_, err = r.Invoke(canceledCtx, "input", WithCheckPointID("2"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	result, err = r.Invoke(ctx, "input", WithCheckPointID("2"))
	assert.NoError(t, err)
	assert.Equal(t, "input12", result)

	// interrupt rerun nodes - with auto-enabled PersistRerunInput, input is preserved
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()
	_, err = r.Invoke(canceledCtx, "input", WithCheckPointID("3"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, []string{"1"}, info.RerunNodes)
	result, err = r.Invoke(ctx, "input", WithCheckPointID("3"))
	assert.NoError(t, err)
	assert.Equal(t, "input12", result)

	// dag
	g = NewGraph[string, string]()
	_ = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		time.Sleep(3 * time.Second)
		return input + "1", nil
	}))
	_ = g.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "2", nil
	}))
	_ = g.AddEdge(START, "1")
	_ = g.AddEdge("1", "2")
	_ = g.AddEdge("2", END)
	r, err = g.Compile(ctx, WithNodeTriggerMode(AllPredecessor), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)
	// interrupt after nodes
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(time.Hour))
	}()
	_, err = r.Invoke(canceledCtx, "input", WithCheckPointID("1"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	result, err = r.Invoke(ctx, "input", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, "input12", result)
	// infinite timeout
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel()
	}()
	_, err = r.Invoke(canceledCtx, "input", WithCheckPointID("2"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, []string{"1"}, info.AfterNodes)
	result, err = r.Invoke(ctx, "input", WithCheckPointID("2"))
	assert.NoError(t, err)
	assert.Equal(t, "input12", result)

	// interrupt rerun nodes - with auto-enabled PersistRerunInput, input is preserved
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()
	_, err = r.Invoke(canceledCtx, "input", WithCheckPointID("3"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, []string{"1"}, info.RerunNodes)
	result, err = r.Invoke(ctx, "input", WithCheckPointID("3"))
	assert.NoError(t, err)
	assert.Equal(t, "input12", result)

	// dag multi canceled nodes
	gg := NewGraph[string, map[string]any]()
	_ = gg.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "1", nil
	}))
	_ = gg.AddLambdaNode("2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		time.Sleep(3 * time.Second)
		return input + "2", nil
	}), WithOutputKey("2"))
	_ = gg.AddLambdaNode("3", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		time.Sleep(3 * time.Second)
		return input + "3", nil
	}), WithOutputKey("3"))
	_ = gg.AddLambdaNode("4", InvokableLambda(func(ctx context.Context, input map[string]any) (output map[string]any, err error) {
		return input, nil
	}))
	_ = gg.AddEdge(START, "1")
	_ = gg.AddEdge("1", "2")
	_ = gg.AddEdge("1", "3")
	_ = gg.AddEdge("2", "4")
	_ = gg.AddEdge("3", "4")
	_ = gg.AddEdge("4", END)
	ctx = context.Background()
	rr, err := gg.Compile(ctx, WithNodeTriggerMode(AllPredecessor), WithCheckPointStore(newInMemoryStore()))
	assert.NoError(t, err)
	// interrupt after nodes
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(time.Hour))
	}()
	_, err = rr.Invoke(canceledCtx, "input", WithCheckPointID("1"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, 2, len(info.AfterNodes))
	result2, err := rr.Invoke(ctx, "input", WithCheckPointID("1"))
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"2": "input12",
		"3": "input13",
	}, result2)

	// interrupt rerun nodes - with auto-enabled PersistRerunInput, input is preserved
	canceledCtx, cancel = WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(500 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()
	_, err = rr.Invoke(canceledCtx, "input", WithCheckPointID("2"))
	assert.Error(t, err)
	info, success = ExtractInterruptInfo(err)
	assert.True(t, success)
	assert.Equal(t, 2, len(info.RerunNodes))
	result2, err = rr.Invoke(ctx, "input", WithCheckPointID("2"))
	assert.NoError(t, err)
	assert.Equal(t, map[string]any{
		"2": "input12",
		"3": "input13",
	}, result2)
}

func TestPersistRerunInputNonStream(t *testing.T) {
	store := newInMemoryStore()

	var mu sync.Mutex
	var receivedInput string
	var callCount int

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		mu.Lock()
		callCount++
		currentCount := callCount
		receivedInput = input
		mu.Unlock()

		if currentCount == 1 {
			time.Sleep(2 * time.Second)
		}
		return input + "_processed", nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)
	assert.NoError(t, err)

	canceledCtx, cancel := WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()

	_, err = r.Invoke(canceledCtx, "test_input", WithCheckPointID("cp1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, []string{"1"}, info.RerunNodes)

	mu.Lock()
	assert.Equal(t, "test_input", receivedInput)
	mu.Unlock()

	result, err := r.Invoke(ctx, "", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "test_input_processed", result)

	mu.Lock()
	assert.Equal(t, "test_input", receivedInput)
	assert.Equal(t, 2, callCount)
	mu.Unlock()
}

func TestPersistRerunInputStream(t *testing.T) {
	store := newInMemoryStore()

	var mu sync.Mutex
	var receivedInput string
	var callCount int

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("1", TransformableLambda(func(ctx context.Context, input *schema.StreamReader[string]) (output *schema.StreamReader[string], err error) {
		mu.Lock()
		callCount++
		currentCount := callCount
		mu.Unlock()

		var sb string
		for {
			chunk, err := input.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			sb += chunk
		}

		mu.Lock()
		receivedInput = sb
		mu.Unlock()

		if currentCount == 1 {
			time.Sleep(2 * time.Second)
		}

		return schema.StreamReaderFromArray([]string{sb + "_processed"}), nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)
	assert.NoError(t, err)

	inputStream := schema.StreamReaderFromArray([]string{"chunk1", "chunk2", "chunk3"})

	canceledCtx, cancel := WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()

	_, err = r.Transform(canceledCtx, inputStream, WithCheckPointID("cp1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, []string{"1"}, info.RerunNodes)

	mu.Lock()
	assert.Equal(t, "chunk1chunk2chunk3", receivedInput)
	mu.Unlock()

	emptyInputStream := schema.StreamReaderFromArray([]string{})

	resultStream, err := r.Transform(ctx, emptyInputStream, WithCheckPointID("cp1"))
	assert.NoError(t, err)

	var result string
	for {
		chunk, err := resultStream.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		result += chunk
	}

	assert.Equal(t, "chunk1chunk2chunk3_processed", result)

	mu.Lock()
	assert.Equal(t, "chunk1chunk2chunk3", receivedInput)
	assert.Equal(t, 2, callCount)
	mu.Unlock()
}

type testPersistRerunInputState struct {
	Prefix string
}

func TestPersistRerunInputWithPreHandler(t *testing.T) {
	store := newInMemoryStore()

	var mu sync.Mutex
	var receivedInput string
	var callCount int

	schema.Register[testPersistRerunInputState]()

	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) *testPersistRerunInputState {
		return &testPersistRerunInputState{Prefix: "prefix_"}
	}))

	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		mu.Lock()
		callCount++
		currentCount := callCount
		receivedInput = input
		mu.Unlock()

		if currentCount == 1 {
			time.Sleep(2 * time.Second)
		}
		return input + "_processed", nil
	}), WithStatePreHandler(func(ctx context.Context, in string, s *testPersistRerunInputState) (string, error) {
		return s.Prefix + in, nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)
	assert.NoError(t, err)

	canceledCtx, cancel := WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()

	_, err = r.Invoke(canceledCtx, "test_input", WithCheckPointID("cp1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	if ok {
		assert.Equal(t, []string{"1"}, info.RerunNodes)
	}

	mu.Lock()
	assert.Equal(t, "prefix_test_input", receivedInput)
	mu.Unlock()

	result, err := r.Invoke(ctx, "", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "prefix_test_input_processed", result)

	mu.Lock()
	assert.Equal(t, "prefix_test_input", receivedInput)
	assert.Equal(t, 2, callCount)
	mu.Unlock()
}

func TestPersistRerunInputBackwardCompatibility(t *testing.T) {
	store := newInMemoryStore()

	var receivedInput string
	var callCount int

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		callCount++
		receivedInput = input
		if len(input) > 0 {
			return "", StatefulInterrupt(ctx, "interrupt", input)
		}

		_, _, restoredInput := GetInterruptState[string](ctx)
		return restoredInput + "_processed", nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "test_input", WithCheckPointID("cp1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, []string{"1"}, info.RerunNodes)

	assert.Equal(t, "test_input", receivedInput)

	result, err := r.Invoke(ctx, "", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "test_input_processed", result)
	assert.Equal(t, "", receivedInput)
	assert.Equal(t, 2, callCount)
}

func TestPersistRerunInputSubGraph(t *testing.T) {
	store := newInMemoryStore()

	var mu sync.Mutex
	var receivedInput string
	var callCount int

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		mu.Lock()
		callCount++
		currentCount := callCount
		receivedInput = input
		mu.Unlock()

		if currentCount == 1 {
			time.Sleep(2 * time.Second)
		}
		return input + "_sub_processed", nil
	}))
	assert.NoError(t, err)
	err = subG.AddEdge(START, "sub1")
	assert.NoError(t, err)
	err = subG.AddEdge("sub1", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "_main", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("2", subG)
	assert.NoError(t, err)
	err = g.AddEdge(START, "1")
	assert.NoError(t, err)
	err = g.AddEdge("1", "2")
	assert.NoError(t, err)
	err = g.AddEdge("2", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)
	assert.NoError(t, err)

	canceledCtx, cancel := WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()

	_, err = r.Invoke(canceledCtx, "test", WithCheckPointID("cp1"))
	assert.NotNil(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok, "Expected interrupt error, got: %v", err)
	if len(info.SubGraphs) > 0 {
		assert.Contains(t, info.SubGraphs, "2")
		subInfo := info.SubGraphs["2"]
		assert.Equal(t, []string{"sub1"}, subInfo.RerunNodes)
	} else {
		assert.Equal(t, []string{"2"}, info.RerunNodes)
	}

	mu.Lock()
	assert.Equal(t, "test_main", receivedInput)
	mu.Unlock()

	result, err := r.Invoke(ctx, "", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "test_main_sub_processed", result)

	mu.Lock()
	assert.Equal(t, "test_main", receivedInput)
	assert.Equal(t, 2, callCount)
	mu.Unlock()
}

type longRunningToolInput struct {
	Input string `json:"input"`
}

func TestToolsNodeWithExternalGraphInterrupt(t *testing.T) {
	store := newInMemoryStore()
	ctx := context.Background()

	var mu sync.Mutex
	var callCount int

	longRunningToolInfo := &schema.ToolInfo{
		Name: "long_running_tool",
		Desc: "A tool that takes a long time to run",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string", Desc: "input"},
		}),
	}

	longRunningTool := newCheckpointTestTool(longRunningToolInfo, func(ctx context.Context, in *longRunningToolInput) (string, error) {
		mu.Lock()
		callCount++
		currentCount := callCount
		mu.Unlock()

		if currentCount == 1 {
			time.Sleep(2 * time.Second)
		}
		return "result_" + in.Input, nil
	})

	toolsNode, err := NewToolNode(ctx, &ToolsNodeConfig{
		Tools: []tool.BaseTool{longRunningTool},
	})
	assert.NoError(t, err)

	g := NewGraph[*schema.Message, []*schema.Message]()
	err = g.AddToolsNode("tools", toolsNode)
	assert.NoError(t, err)
	err = g.AddEdge(START, "tools")
	assert.NoError(t, err)
	err = g.AddEdge("tools", END)
	assert.NoError(t, err)

	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)
	assert.NoError(t, err)

	inputMsg := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:   "call_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "long_running_tool",
				Arguments: `{"input": "test"}`,
			},
		}},
	}

	canceledCtx, cancel := WithGraphInterrupt(ctx)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel(WithGraphInterruptTimeout(0))
	}()

	_, err = r.Invoke(canceledCtx, inputMsg, WithCheckPointID("cp1"))
	assert.Error(t, err)
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok, "Expected interrupt error, got: %v", err)
	if ok {
		assert.Equal(t, []string{"tools"}, info.RerunNodes)
	}

	result, err := r.Invoke(ctx, &schema.Message{}, WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, `"result_test"`, result[0].Content)

	mu.Lock()
	assert.Equal(t, 2, callCount)
	mu.Unlock()
}

type checkpointTestTool[I, O any] struct {
	info *schema.ToolInfo
	fn   func(ctx context.Context, in I) (O, error)
}

func newCheckpointTestTool[I, O any](info *schema.ToolInfo, f func(ctx context.Context, in I) (O, error)) tool.InvokableTool {
	return &checkpointTestTool[I, O]{
		info: info,
		fn:   f,
	}
}

func (f *checkpointTestTool[I, O]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return f.info, nil
}

func (f *checkpointTestTool[I, O]) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	t := generic.NewInstance[I]()
	err := sonic.UnmarshalString(argumentsInJSON, t)
	if err != nil {
		return "", err
	}
	o, err := f.fn(ctx, t)
	if err != nil {
		return "", err
	}
	return sonic.MarshalString(o)
}
