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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

type autoCheckpointTestState struct {
	Counter int
}

func init() {
	schema.Register[autoCheckpointTestState]()
}

func TestAutoCheckpointAfterNode(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, node2Executed atomic.Int32

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("expensive_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		node1Executed.Add(1)
		return input + "_expensive", nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("normal_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		node2Executed.Add(1)
		return input + "_normal", nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "expensive_node")
	assert.NoError(t, err)
	err = g.AddEdge("expensive_node", "normal_node")
	assert.NoError(t, err)
	err = g.AddEdge("normal_node", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"expensive_node"},
		}),
		WithGraphName("auto_cp_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("test_cp"))
	assert.NoError(t, err)
	assert.Equal(t, "start_expensive_normal", result)
	assert.Equal(t, int32(1), node1Executed.Load())
	assert.Equal(t, int32(1), node2Executed.Load())

	_, exists := store.m["test_cp"]
	assert.True(t, exists, "checkpoint should be saved after expensive_node")
}

func TestAutoCheckpointBeforeNode(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, node2Executed atomic.Int32

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("normal_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		node1Executed.Add(1)
		return input + "_normal", nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("risky_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		node2Executed.Add(1)
		return input + "_risky", nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "normal_node")
	assert.NoError(t, err)
	err = g.AddEdge("normal_node", "risky_node")
	assert.NoError(t, err)
	err = g.AddEdge("risky_node", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithAutoCheckpoint(AutoCheckpointConfig{
			BeforeNodes: []string{"risky_node"},
		}),
		WithGraphName("auto_cp_before_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("test_cp_before"))
	assert.NoError(t, err)
	assert.Equal(t, "start_normal_risky", result)
	assert.Equal(t, int32(1), node1Executed.Load())
	assert.Equal(t, int32(1), node2Executed.Load())

	_, exists := store.m["test_cp_before"]
	assert.True(t, exists, "checkpoint should be saved before risky_node")
}

func TestAutoCheckpointConfig(t *testing.T) {
	config := AutoCheckpointConfig{
		BeforeNodes: []string{"chatmodel", "external_api"},
		AfterNodes:  []string{"chatmodel", "expensive_compute"},
	}

	assert.Equal(t, []string{"chatmodel", "external_api"}, config.BeforeNodes)
	assert.Equal(t, []string{"chatmodel", "expensive_compute"}, config.AfterNodes)
}

func TestWithAutoCheckpointOption(t *testing.T) {
	opts := newGraphCompileOptions(
		WithAutoCheckpoint(AutoCheckpointConfig{
			BeforeNodes: []string{"node1"},
			AfterNodes:  []string{"node2"},
		}),
	)

	assert.NotNil(t, opts.autoCheckpointConfig)
	assert.Equal(t, []string{"node1"}, opts.autoCheckpointConfig.BeforeNodes)
	assert.Equal(t, []string{"node2"}, opts.autoCheckpointConfig.AfterNodes)
}

func TestAutoCheckpointWithState(t *testing.T) {
	store := newInMemoryStore()

	g := NewGraph[string, string](WithGenLocalState(func(ctx context.Context) *autoCheckpointTestState {
		return &autoCheckpointTestState{Counter: 0}
	}))

	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		_ = ProcessState(ctx, func(ctx context.Context, state *autoCheckpointTestState) error {
			state.Counter++
			return nil
		})
		return input + "_1", nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		_ = ProcessState(ctx, func(ctx context.Context, state *autoCheckpointTestState) error {
			state.Counter++
			return nil
		})
		return input + "_2", nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "node2")
	assert.NoError(t, err)
	err = g.AddEdge("node2", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"node1"},
		}),
		WithGraphName("auto_cp_state_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("state_cp"))
	assert.NoError(t, err)
	assert.Equal(t, "start_1_2", result)

	_, exists := store.m["state_cp"]
	assert.True(t, exists, "checkpoint should be saved with state")
}

func TestAutoCheckpointNoStoreConfigured(t *testing.T) {
	g := NewGraph[string, string]()

	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		return input + "_1", nil
	}))
	assert.NoError(t, err)

	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"node1"},
		}),
		WithGraphName("no_store_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start")
	assert.NoError(t, err)
	assert.Equal(t, "start_1", result)
}

func TestShouldAutoCheckpointBefore(t *testing.T) {
	r := &runner{
		autoCheckpointConfig: &AutoCheckpointConfig{
			BeforeNodes: []string{"node1", "node2"},
		},
	}

	tasks := []*task{
		{nodeKey: "node1"},
	}
	assert.True(t, r.shouldAutoCheckpointBefore(tasks))

	tasks = []*task{
		{nodeKey: "node3"},
	}
	assert.False(t, r.shouldAutoCheckpointBefore(tasks))

	r.autoCheckpointConfig = nil
	assert.False(t, r.shouldAutoCheckpointBefore(tasks))
}

func TestShouldAutoCheckpointAfter(t *testing.T) {
	r := &runner{
		autoCheckpointConfig: &AutoCheckpointConfig{
			AfterNodes: []string{"node1", "node2"},
		},
	}

	tasks := []*task{
		{nodeKey: "node1"},
	}
	assert.True(t, r.shouldAutoCheckpointAfter(tasks))

	tasks = []*task{
		{nodeKey: "node3"},
	}
	assert.False(t, r.shouldAutoCheckpointAfter(tasks))

	r.autoCheckpointConfig = nil
	assert.False(t, r.shouldAutoCheckpointAfter(tasks))
}

func TestAutoCheckpointParentAfterSubGraph(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, subNode1Executed, subNode2Executed, node3Executed atomic.Int32
	var failNode3 atomic.Bool
	failNode3.Store(true)

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		subNode1Executed.Add(1)
		return input + "_sub1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("sub2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		subNode2Executed.Add(1)
		return input + "_sub2", nil
	}))
	assert.NoError(t, err)
	err = subG.AddEdge(START, "sub1")
	assert.NoError(t, err)
	err = subG.AddEdge("sub1", "sub2")
	assert.NoError(t, err)
	err = subG.AddEdge("sub2", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		node1Executed.Add(1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG)
	assert.NoError(t, err)
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		node3Executed.Add(1)
		if failNode3.Load() {
			return "", errors.New("node3 failed")
		}
		return input + "_node3", nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "subgraph")
	assert.NoError(t, err)
	err = g.AddEdge("subgraph", "node3")
	assert.NoError(t, err)
	err = g.AddEdge("node3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithCheckpointConfig(CheckpointConfig{
			PersistRerunInput: true,
		}),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"subgraph"},
		}),
		WithGraphName("parent_subgraph_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.Error(t, err)
	assert.Equal(t, int32(1), node1Executed.Load())
	assert.Equal(t, int32(1), subNode1Executed.Load())
	assert.Equal(t, int32(1), subNode2Executed.Load())
	assert.Equal(t, int32(1), node3Executed.Load())

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved after subgraph")
	assert.NotEmpty(t, cpData)

	failNode3.Store(false)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_sub1_sub2_node3", result)

	assert.Equal(t, int32(1), node1Executed.Load(), "node1 should not be re-executed")
	assert.Equal(t, int32(1), subNode1Executed.Load(), "sub1 should not be re-executed")
	assert.Equal(t, int32(1), subNode2Executed.Load(), "sub2 should not be re-executed")
	assert.Equal(t, int32(2), node3Executed.Load(), "node3 should be re-executed")
}

func TestAutoCheckpointSubGraphProactive(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, expensiveExecuted, riskyExecuted atomic.Int32
	var failRisky atomic.Bool
	failRisky.Store(true)

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("expensive", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		expensiveExecuted.Add(1)
		return input + "_expensive", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("risky", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		riskyExecuted.Add(1)
		if failRisky.Load() {
			return "", errors.New("risky node failed")
		}
		return input + "_risky", nil
	}))
	assert.NoError(t, err)
	err = subG.AddEdge(START, "expensive")
	assert.NoError(t, err)
	err = subG.AddEdge("expensive", "risky")
	assert.NoError(t, err)
	err = subG.AddEdge("risky", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		node1Executed.Add(1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG, WithGraphCompileOptions(
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"expensive"},
		}),
	))
	assert.NoError(t, err)
	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "subgraph")
	assert.NoError(t, err)
	err = g.AddEdge("subgraph", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithCheckpointConfig(CheckpointConfig{
			PersistRerunInput:    true,
			EnableAutoCheckpoint: true,
		}),
		WithGraphName("subgraph_proactive_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.Error(t, err)
	assert.Equal(t, int32(1), node1Executed.Load())
	assert.Equal(t, int32(1), expensiveExecuted.Load())
	assert.Equal(t, int32(1), riskyExecuted.Load())

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved by sub-graph proactive trigger")
	assert.NotEmpty(t, cpData)

	failRisky.Store(false)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_expensive_risky", result)

	assert.Equal(t, int32(1), node1Executed.Load(), "node1 should not be re-executed")
	assert.Equal(t, int32(1), expensiveExecuted.Load(), "expensive should not be re-executed")
	assert.Equal(t, int32(2), riskyExecuted.Load(), "risky should be re-executed")
}

func TestAutoCheckpointSubGraphWithoutOwnConfig(t *testing.T) {

	store := newInMemoryStore()
	var node1Executed, subNode1Executed, subNode2Executed, node3Executed atomic.Int32
	var failNode3 atomic.Bool
	failNode3.Store(true)

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		subNode1Executed.Add(1)
		return input + "_sub1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("sub2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		subNode2Executed.Add(1)
		return input + "_sub2", nil
	}))
	assert.NoError(t, err)
	err = subG.AddEdge(START, "sub1")
	assert.NoError(t, err)
	err = subG.AddEdge("sub1", "sub2")
	assert.NoError(t, err)
	err = subG.AddEdge("sub2", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		node1Executed.Add(1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG)
	assert.NoError(t, err)
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		node3Executed.Add(1)
		if failNode3.Load() {
			return "", errors.New("node3 failed")
		}
		return input + "_node3", nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "subgraph")
	assert.NoError(t, err)
	err = g.AddEdge("subgraph", "node3")
	assert.NoError(t, err)
	err = g.AddEdge("node3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithCheckpointConfig(CheckpointConfig{
			PersistRerunInput: true,
		}),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"subgraph"},
		}),
		WithGraphName("subgraph_no_config_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.Error(t, err)

	_, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved even when sub-graph has no auto checkpoint config")

	failNode3.Store(false)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_sub1_sub2_node3", result)

	assert.Equal(t, int32(1), node1Executed.Load())
	assert.Equal(t, int32(1), subNode1Executed.Load())
	assert.Equal(t, int32(1), subNode2Executed.Load())
	assert.Equal(t, int32(2), node3Executed.Load())
}
