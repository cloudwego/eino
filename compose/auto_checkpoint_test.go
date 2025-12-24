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
	"sync"
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
	var node1Executed, node2Executed int32
	var callbackInfo *AutoCheckpointInfo

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("expensive_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		atomic.AddInt32(&node1Executed, 1)
		return input + "_expensive", nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("normal_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		atomic.AddInt32(&node2Executed, 1)
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
			OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
				callbackInfo = info
			},
		}),
		WithGraphName("auto_cp_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("test_cp"))
	assert.NoError(t, err)
	assert.Equal(t, "start_expensive_normal", result)
	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&node2Executed))

	_, exists := store.m["test_cp"]
	assert.True(t, exists, "checkpoint should be saved after expensive_node")

	assert.NotNil(t, callbackInfo, "callback should be invoked")
	assert.Equal(t, "test_cp", callbackInfo.CheckpointID)
	assert.Equal(t, "auto_cp_test", callbackInfo.GraphName)
	assert.Equal(t, "expensive_node", callbackInfo.TriggerNode)
	assert.Equal(t, "after", callbackInfo.TriggerType)
	assert.NoError(t, callbackInfo.Error)
}

func TestAutoCheckpointBeforeNode(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, node2Executed int32
	var callbackInfo *AutoCheckpointInfo

	g := NewGraph[string, string]()

	err := g.AddLambdaNode("normal_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		atomic.AddInt32(&node1Executed, 1)
		return input + "_normal", nil
	}))
	assert.NoError(t, err)

	err = g.AddLambdaNode("risky_node", InvokableLambda(func(ctx context.Context, input string) (output string, err error) {
		atomic.AddInt32(&node2Executed, 1)
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
			OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
				callbackInfo = info
			},
		}),
		WithGraphName("auto_cp_before_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("test_cp_before"))
	assert.NoError(t, err)
	assert.Equal(t, "start_normal_risky", result)
	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&node2Executed))

	_, exists := store.m["test_cp_before"]
	assert.True(t, exists, "checkpoint should be saved before risky_node")

	assert.NotNil(t, callbackInfo, "callback should be invoked")
	assert.Equal(t, "test_cp_before", callbackInfo.CheckpointID)
	assert.Equal(t, "auto_cp_before_test", callbackInfo.GraphName)
	assert.Equal(t, "risky_node", callbackInfo.TriggerNode)
	assert.Equal(t, "before", callbackInfo.TriggerType)
	assert.NoError(t, callbackInfo.Error)
}

func TestAutoCheckpointConfig(t *testing.T) {
	callbackCalled := false
	config := AutoCheckpointConfig{
		BeforeNodes: []string{"chatmodel", "external_api"},
		AfterNodes:  []string{"chatmodel", "expensive_compute"},
		OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
			callbackCalled = true
		},
	}

	assert.Equal(t, []string{"chatmodel", "external_api"}, config.BeforeNodes)
	assert.Equal(t, []string{"chatmodel", "expensive_compute"}, config.AfterNodes)
	assert.NotNil(t, config.OnCheckpoint)
	config.OnCheckpoint(context.Background(), &AutoCheckpointInfo{})
	assert.True(t, callbackCalled)
}

func TestWithAutoCheckpointOption(t *testing.T) {
	callbackCalled := false
	opts := newGraphCompileOptions(
		WithAutoCheckpoint(AutoCheckpointConfig{
			BeforeNodes: []string{"node1"},
			AfterNodes:  []string{"node2"},
			OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
				callbackCalled = true
			},
		}),
	)

	assert.NotNil(t, opts.autoCheckpointConfig)
	assert.Equal(t, []string{"node1"}, opts.autoCheckpointConfig.BeforeNodes)
	assert.Equal(t, []string{"node2"}, opts.autoCheckpointConfig.AfterNodes)
	assert.NotNil(t, opts.autoCheckpointConfig.OnCheckpoint)
	opts.autoCheckpointConfig.OnCheckpoint(context.Background(), &AutoCheckpointInfo{})
	assert.True(t, callbackCalled)
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
	var node1Executed, subNode1Executed, subNode2Executed, node3Executed int32
	var failNode3 int32
	atomic.StoreInt32(&failNode3, 1)

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode1Executed, 1)
		return input + "_sub1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("sub2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode2Executed, 1)
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
		atomic.AddInt32(&node1Executed, 1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG)
	assert.NoError(t, err)
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node3Executed, 1)
		if atomic.LoadInt32(&failNode3) != 0 {
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
	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode2Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&node3Executed))

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved after subgraph")
	assert.NotEmpty(t, cpData)

	atomic.StoreInt32(&failNode3, 0)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_sub1_sub2_node3", result)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed), "node1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode1Executed), "sub1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode2Executed), "sub2 should not be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&node3Executed), "node3 should be re-executed")
}

func TestAutoCheckpointSubGraphProactive(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, expensiveExecuted, riskyExecuted int32
	var failRisky int32
	atomic.StoreInt32(&failRisky, 1)

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("expensive", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&expensiveExecuted, 1)
		return input + "_expensive", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("risky", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&riskyExecuted, 1)
		if atomic.LoadInt32(&failRisky) != 0 {
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
		atomic.AddInt32(&node1Executed, 1)
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
	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&expensiveExecuted))
	assert.Equal(t, int32(1), atomic.LoadInt32(&riskyExecuted))

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved by sub-graph proactive trigger")
	assert.NotEmpty(t, cpData)

	atomic.StoreInt32(&failRisky, 0)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_expensive_risky", result)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed), "node1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&expensiveExecuted), "expensive should not be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&riskyExecuted), "risky should be re-executed")
}

func TestAutoCheckpointSubGraphWithoutOwnConfig(t *testing.T) {

	store := newInMemoryStore()
	var node1Executed, subNode1Executed, subNode2Executed, node3Executed int32
	var failNode3 int32
	atomic.StoreInt32(&failNode3, 1)

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode1Executed, 1)
		return input + "_sub1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("sub2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode2Executed, 1)
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
		atomic.AddInt32(&node1Executed, 1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG)
	assert.NoError(t, err)
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node3Executed, 1)
		if atomic.LoadInt32(&failNode3) != 0 {
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

	atomic.StoreInt32(&failNode3, 0)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_sub1_sub2_node3", result)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode2Executed))
	assert.Equal(t, int32(2), atomic.LoadInt32(&node3Executed))
}

func TestAutoCheckpointDownwardPropagationWithBlockedSubGraph(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, slowNodeExecuted, subNode1Executed, subNode2Executed int32
	var slowNodeStarted, slowNodeCanProceed chan struct{}
	slowNodeStarted = make(chan struct{})
	slowNodeCanProceed = make(chan struct{})

	subG := NewGraph[string, string]()
	err := subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode1Executed, 1)
		return input + "_sub1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("slow_node", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&slowNodeExecuted, 1)
		close(slowNodeStarted)
		<-slowNodeCanProceed
		return input + "_slow", nil
	}))
	assert.NoError(t, err)
	err = subG.AddLambdaNode("sub2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode2Executed, 1)
		return input + "_sub2", nil
	}))
	assert.NoError(t, err)
	err = subG.AddEdge(START, "sub1")
	assert.NoError(t, err)
	err = subG.AddEdge("sub1", "slow_node")
	assert.NoError(t, err)
	err = subG.AddEdge("slow_node", "sub2")
	assert.NoError(t, err)
	err = subG.AddEdge("sub2", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node1Executed, 1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG)
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
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"node1"},
		}),
		WithGraphName("downward_blocked_test"),
	)
	assert.NoError(t, err)

	resultChan := make(chan string, 1)
	errChan := make(chan error, 1)
	go func() {
		result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
		if err != nil {
			errChan <- err
		} else {
			resultChan <- result
		}
	}()

	<-slowNodeStarted

	_, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved even when sub-graph is blocked in tm.wait()")

	close(slowNodeCanProceed)

	select {
	case result := <-resultChan:
		assert.Equal(t, "start_node1_sub1_slow_sub2", result)
	case err := <-errChan:
		t.Fatalf("unexpected error: %v", err)
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&slowNodeExecuted))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode2Executed))
}

func TestAutoCheckpointNestedSubGraphs(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, subNode1Executed, nestedNode1Executed, nestedNode2Executed int32
	var failNestedNode2 int32
	atomic.StoreInt32(&failNestedNode2, 1)

	nestedG := NewGraph[string, string]()
	err := nestedG.AddLambdaNode("nested1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&nestedNode1Executed, 1)
		return input + "_nested1", nil
	}))
	assert.NoError(t, err)
	err = nestedG.AddLambdaNode("nested2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&nestedNode2Executed, 1)
		if atomic.LoadInt32(&failNestedNode2) != 0 {
			return "", errors.New("nested2 failed")
		}
		return input + "_nested2", nil
	}))
	assert.NoError(t, err)
	err = nestedG.AddEdge(START, "nested1")
	assert.NoError(t, err)
	err = nestedG.AddEdge("nested1", "nested2")
	assert.NoError(t, err)
	err = nestedG.AddEdge("nested2", END)
	assert.NoError(t, err)

	subG := NewGraph[string, string]()
	err = subG.AddLambdaNode("sub1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&subNode1Executed, 1)
		return input + "_sub1", nil
	}))
	assert.NoError(t, err)
	err = subG.AddGraphNode("nested", nestedG)
	assert.NoError(t, err)
	err = subG.AddEdge(START, "sub1")
	assert.NoError(t, err)
	err = subG.AddEdge("sub1", "nested")
	assert.NoError(t, err)
	err = subG.AddEdge("nested", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node1Executed, 1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("subgraph", subG)
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
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"node1"},
		}),
		WithGraphName("nested_subgraph_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.Error(t, err)

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved after node1")
	assert.NotEmpty(t, cpData)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&subNode1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&nestedNode1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&nestedNode2Executed))

	atomic.StoreInt32(&failNestedNode2, 0)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_sub1_nested1_nested2", result)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed), "node1 should not be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&subNode1Executed), "sub1 should be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&nestedNode1Executed), "nested1 should be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&nestedNode2Executed), "nested2 should be re-executed")
}

func TestAutoCheckpointConcurrentSubGraphs(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, node2Executed int32
	var sub1Executed, sub2Executed int32
	var failNode2 int32
	atomic.StoreInt32(&failNode2, 1)

	subG1 := NewGraph[map[string]any, map[string]any]()
	err := subG1.AddLambdaNode("s1n1", InvokableLambda(func(ctx context.Context, input map[string]any) (map[string]any, error) {
		atomic.AddInt32(&sub1Executed, 1)
		return map[string]any{"sub1": true}, nil
	}))
	assert.NoError(t, err)
	err = subG1.AddEdge(START, "s1n1")
	assert.NoError(t, err)
	err = subG1.AddEdge("s1n1", END)
	assert.NoError(t, err)

	subG2 := NewGraph[map[string]any, map[string]any]()
	err = subG2.AddLambdaNode("s2n1", InvokableLambda(func(ctx context.Context, input map[string]any) (map[string]any, error) {
		atomic.AddInt32(&sub2Executed, 1)
		return map[string]any{"sub2": true}, nil
	}))
	assert.NoError(t, err)
	err = subG2.AddEdge(START, "s2n1")
	assert.NoError(t, err)
	err = subG2.AddEdge("s2n1", END)
	assert.NoError(t, err)

	g := NewGraph[map[string]any, map[string]any]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input map[string]any) (map[string]any, error) {
		atomic.AddInt32(&node1Executed, 1)
		return map[string]any{"node1": true}, nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("sub1", subG1)
	assert.NoError(t, err)
	err = g.AddGraphNode("sub2", subG2)
	assert.NoError(t, err)
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input map[string]any) (map[string]any, error) {
		atomic.AddInt32(&node2Executed, 1)
		if atomic.LoadInt32(&failNode2) != 0 {
			return nil, errors.New("node2 failed")
		}
		return map[string]any{"node2": true}, nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "sub1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "sub2")
	assert.NoError(t, err)
	err = g.AddEdge("sub1", "node2")
	assert.NoError(t, err)
	err = g.AddEdge("sub2", "node2")
	assert.NoError(t, err)
	err = g.AddEdge("node2", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithCheckpointConfig(CheckpointConfig{
			PersistRerunInput:    true,
			EnableAutoCheckpoint: true,
		}),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"sub1", "sub2"},
		}),
		WithGraphName("concurrent_subgraph_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, map[string]any{"start": true}, WithCheckPointID("cp1"))
	assert.Error(t, err)

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved after sub1 or sub2 completes")
	assert.NotEmpty(t, cpData)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&sub1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&sub2Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&node2Executed))

	atomic.StoreInt32(&failNode2, 0)
	result, err := r.Invoke(ctx, map[string]any{"start": true}, WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.NotNil(t, result)
	assert.True(t, result["node2"].(bool))

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed), "node1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&sub1Executed), "sub1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&sub2Executed), "sub2 should not be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&node2Executed), "node2 should be re-executed")
}

func TestAutoCheckpointMiddleGraphProactive(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, node3Executed int32
	var middleExpensiveExecuted, middleRiskyExecuted int32
	var nestedNodeExecuted int32
	var failMiddleRisky int32
	atomic.StoreInt32(&failMiddleRisky, 1)

	var callbackInfos []*AutoCheckpointInfo
	var callbackMu sync.Mutex

	nestedG := NewGraph[string, string]()
	err := nestedG.AddLambdaNode("nested_node", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&nestedNodeExecuted, 1)
		return input + "_nested", nil
	}))
	assert.NoError(t, err)
	err = nestedG.AddEdge(START, "nested_node")
	assert.NoError(t, err)
	err = nestedG.AddEdge("nested_node", END)
	assert.NoError(t, err)

	middleG := NewGraph[string, string]()
	err = middleG.AddLambdaNode("middle_expensive", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&middleExpensiveExecuted, 1)
		return input + "_expensive", nil
	}))
	assert.NoError(t, err)
	err = middleG.AddGraphNode("nested", nestedG)
	assert.NoError(t, err)
	err = middleG.AddLambdaNode("middle_risky", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&middleRiskyExecuted, 1)
		if atomic.LoadInt32(&failMiddleRisky) != 0 {
			return "", errors.New("middle_risky failed")
		}
		return input + "_risky", nil
	}))
	assert.NoError(t, err)
	err = middleG.AddEdge(START, "middle_expensive")
	assert.NoError(t, err)
	err = middleG.AddEdge("middle_expensive", "nested")
	assert.NoError(t, err)
	err = middleG.AddEdge("nested", "middle_risky")
	assert.NoError(t, err)
	err = middleG.AddEdge("middle_risky", END)
	assert.NoError(t, err)

	g := NewGraph[string, string]()
	err = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node1Executed, 1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddGraphNode("middle", middleG, WithGraphCompileOptions(
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"middle_expensive"},
			OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
				callbackMu.Lock()
				callbackInfos = append(callbackInfos, info)
				callbackMu.Unlock()
			},
		}),
	))
	assert.NoError(t, err)
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node3Executed, 1)
		return input + "_node3", nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "middle")
	assert.NoError(t, err)
	err = g.AddEdge("middle", "node3")
	assert.NoError(t, err)
	err = g.AddEdge("node3", END)
	assert.NoError(t, err)

	ctx := context.Background()
	r, err := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithCheckpointConfig(CheckpointConfig{
			PersistRerunInput:    true,
			EnableAutoCheckpoint: true,
		}),
		WithGraphName("middle_proactive_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.Error(t, err)

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved when middle graph triggers auto-checkpoint")
	assert.NotEmpty(t, cpData)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&middleExpensiveExecuted))
	assert.Equal(t, int32(1), atomic.LoadInt32(&nestedNodeExecuted))
	assert.Equal(t, int32(1), atomic.LoadInt32(&middleRiskyExecuted))
	assert.Equal(t, int32(0), atomic.LoadInt32(&node3Executed))

	callbackMu.Lock()
	assert.GreaterOrEqual(t, len(callbackInfos), 1, "callback should be invoked at least once")
	if len(callbackInfos) > 0 {
		assert.Equal(t, "middle_expensive", callbackInfos[0].TriggerNode)
		assert.Equal(t, "after", callbackInfos[0].TriggerType)
		assert.NoError(t, callbackInfos[0].Error)
	}
	callbackMu.Unlock()

	atomic.StoreInt32(&failMiddleRisky, 0)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_expensive_nested_risky_node3", result)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed), "node1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&middleExpensiveExecuted), "middle_expensive should not be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&nestedNodeExecuted), "nested_node should be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&middleRiskyExecuted), "middle_risky should be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&node3Executed), "node3 should be executed once")
}

func TestAutoCheckpointCallbackOnError(t *testing.T) {
	failingStore := &failingCheckpointStore{
		shouldFail: true,
	}
	var callbackInfo *AutoCheckpointInfo

	g := NewGraph[string, string]()
	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_node2", nil
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
		WithCheckPointStore(failingStore),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"node1"},
			OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
				callbackInfo = info
			},
		}),
		WithGraphName("callback_error_test"),
	)
	assert.NoError(t, err)

	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_node2", result)

	assert.NotNil(t, callbackInfo, "callback should be invoked even on error")
	assert.Equal(t, "node1", callbackInfo.TriggerNode)
	assert.Equal(t, "after", callbackInfo.TriggerType)
	assert.Error(t, callbackInfo.Error, "callback should receive error when store fails")
	assert.Contains(t, callbackInfo.Error.Error(), "simulated store failure")
}

type failingCheckpointStore struct {
	shouldFail bool
}

func (s *failingCheckpointStore) Get(ctx context.Context, id string) ([]byte, bool, error) {
	return nil, false, nil
}

func (s *failingCheckpointStore) Set(ctx context.Context, id string, data []byte) error {
	if s.shouldFail {
		return errors.New("simulated store failure")
	}
	return nil
}

func TestAutoCheckpointBeforeAndAfterNodesTogether(t *testing.T) {
	store := newInMemoryStore()
	var node1Executed, node2Executed, node3Executed int32
	var failNode3 int32
	atomic.StoreInt32(&failNode3, 1)

	var callbackInfos []*AutoCheckpointInfo
	var callbackMu sync.Mutex

	g := NewGraph[string, string]()
	err := g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node1Executed, 1)
		return input + "_node1", nil
	}))
	assert.NoError(t, err)
	err = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node2Executed, 1)
		return input + "_node2", nil
	}))
	assert.NoError(t, err)
	err = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		atomic.AddInt32(&node3Executed, 1)
		if atomic.LoadInt32(&failNode3) != 0 {
			return "", errors.New("node3 failed")
		}
		return input + "_node3", nil
	}))
	assert.NoError(t, err)
	err = g.AddEdge(START, "node1")
	assert.NoError(t, err)
	err = g.AddEdge("node1", "node2")
	assert.NoError(t, err)
	err = g.AddEdge("node2", "node3")
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
			AfterNodes:  []string{"node1"},
			BeforeNodes: []string{"node3"},
			OnCheckpoint: func(ctx context.Context, info *AutoCheckpointInfo) {
				callbackMu.Lock()
				callbackInfos = append(callbackInfos, info)
				callbackMu.Unlock()
			},
		}),
		WithGraphName("before_after_together_test"),
	)
	assert.NoError(t, err)

	_, err = r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.Error(t, err)

	cpData, exists := store.m["cp1"]
	assert.True(t, exists, "checkpoint should be saved")
	assert.NotEmpty(t, cpData)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&node2Executed))
	assert.Equal(t, int32(1), atomic.LoadInt32(&node3Executed))

	callbackMu.Lock()
	assert.Equal(t, 2, len(callbackInfos), "callback should be invoked twice (after node1 and before node3)")
	if len(callbackInfos) >= 2 {
		assert.Equal(t, "node1", callbackInfos[0].TriggerNode)
		assert.Equal(t, "after", callbackInfos[0].TriggerType)
		assert.NoError(t, callbackInfos[0].Error)

		assert.Equal(t, "node3", callbackInfos[1].TriggerNode)
		assert.Equal(t, "before", callbackInfos[1].TriggerType)
		assert.NoError(t, callbackInfos[1].Error)
	}
	callbackMu.Unlock()

	atomic.StoreInt32(&failNode3, 0)
	result, err := r.Invoke(ctx, "start", WithCheckPointID("cp1"))
	assert.NoError(t, err)
	assert.Equal(t, "start_node1_node2_node3", result)

	assert.Equal(t, int32(1), atomic.LoadInt32(&node1Executed), "node1 should not be re-executed")
	assert.Equal(t, int32(1), atomic.LoadInt32(&node2Executed), "node2 should not be re-executed")
	assert.Equal(t, int32(2), atomic.LoadInt32(&node3Executed), "node3 should be re-executed")
}

func BenchmarkGraphRunWithoutAutoCheckpoint(b *testing.B) {
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_1", nil
	}))
	_ = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_2", nil
	}))
	_ = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_3", nil
	}))
	_ = g.AddEdge(START, "node1")
	_ = g.AddEdge("node1", "node2")
	_ = g.AddEdge("node2", "node3")
	_ = g.AddEdge("node3", END)

	ctx := context.Background()
	r, _ := g.Compile(ctx, WithNodeTriggerMode(AllPredecessor))

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Invoke(ctx, "start")
	}
}

func BenchmarkGraphRunWithAutoCheckpointDisabled(b *testing.B) {
	store := newInMemoryStore()
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_1", nil
	}))
	_ = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_2", nil
	}))
	_ = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_3", nil
	}))
	_ = g.AddEdge(START, "node1")
	_ = g.AddEdge("node1", "node2")
	_ = g.AddEdge("node2", "node3")
	_ = g.AddEdge("node3", END)

	ctx := context.Background()
	r, _ := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Invoke(ctx, "start")
	}
}

func BenchmarkGraphRunWithAutoCheckpointEnabled(b *testing.B) {
	store := newInMemoryStore()
	g := NewGraph[string, string]()
	_ = g.AddLambdaNode("node1", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_1", nil
	}))
	_ = g.AddLambdaNode("node2", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_2", nil
	}))
	_ = g.AddLambdaNode("node3", InvokableLambda(func(ctx context.Context, input string) (string, error) {
		return input + "_3", nil
	}))
	_ = g.AddEdge(START, "node1")
	_ = g.AddEdge("node1", "node2")
	_ = g.AddEdge("node2", "node3")
	_ = g.AddEdge("node3", END)

	ctx := context.Background()
	r, _ := g.Compile(ctx,
		WithNodeTriggerMode(AllPredecessor),
		WithCheckPointStore(store),
		WithCheckpointConfig(CheckpointConfig{
			EnableAutoCheckpoint: true,
		}),
		WithAutoCheckpoint(AutoCheckpointConfig{
			AfterNodes: []string{"node2"},
		}),
	)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = r.Invoke(ctx, "start", WithCheckPointID("bench_cp"))
	}
}
