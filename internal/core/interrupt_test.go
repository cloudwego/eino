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

package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Define AddressSegmentType constants locally to avoid dependency cycles
const (
	AddressSegmentAgent AddressSegmentType = "agent"
	AddressSegmentTool  AddressSegmentType = "tool"
	AddressSegmentNode  AddressSegmentType = "node"
)

func TestInterruptConversion(t *testing.T) {
	// Test Case 1: Simple Chain (A -> B -> C)
	t.Run("SimpleChain", func(t *testing.T) {
		// Manually construct the user-facing contexts with parent pointers
		ctxA := &InterruptCtx{ID: "A", IsRootCause: false}
		ctxB := &InterruptCtx{ID: "B", Parent: ctxA, IsRootCause: false}
		ctxC := &InterruptCtx{ID: "C", Parent: ctxB, IsRootCause: true}

		// The input to FromInterruptContexts is just the root cause leaf node
		contexts := []*InterruptCtx{ctxC}

		// Convert from user-facing contexts to internal signal tree
		signal := FromInterruptContexts(contexts)

		// Assertions for the signal tree structure
		assert.NotNil(t, signal)
		assert.Equal(t, "A", signal.ID)
		assert.Len(t, signal.Subs, 1)
		assert.Equal(t, "B", signal.Subs[0].ID)
		assert.Len(t, signal.Subs[0].Subs, 1)
		assert.Equal(t, "C", signal.Subs[0].Subs[0].ID)
		assert.True(t, signal.Subs[0].Subs[0].IsRootCause)

		// Convert back from the signal tree to user-facing contexts
		finalContexts := ToInterruptContexts(signal, nil)

		// Assertions for the final list of contexts
		assert.Len(t, finalContexts, 1)
		finalC := finalContexts[0]
		assert.Equal(t, "C", finalC.ID)
		assert.True(t, finalC.IsRootCause)
		assert.NotNil(t, finalC.Parent)
		assert.Equal(t, "B", finalC.Parent.ID)
		assert.NotNil(t, finalC.Parent.Parent)
		assert.Equal(t, "A", finalC.Parent.Parent.ID)
		assert.Nil(t, finalC.Parent.Parent.Parent)
	})

	// Test Case 2: Multiple Root Causes with Shared Parent (B -> D, C -> D)
	t.Run("MultipleRootsSharedParent", func(t *testing.T) {
		// Manually construct the contexts
		ctxD := &InterruptCtx{ID: "D", IsRootCause: false}
		ctxB := &InterruptCtx{ID: "B", Parent: ctxD, IsRootCause: true}
		ctxC := &InterruptCtx{ID: "C", Parent: ctxD, IsRootCause: true}

		// The input contains both root cause leaves
		contexts := []*InterruptCtx{ctxB, ctxC}

		// Convert to signal tree
		signal := FromInterruptContexts(contexts)

		// Assertions for the signal tree structure (should merge at D)
		assert.NotNil(t, signal)
		assert.Equal(t, "D", signal.ID)
		assert.Len(t, signal.Subs, 2)
		// Order of subs is not guaranteed, so we check for presence
		subIDs := []string{signal.Subs[0].ID, signal.Subs[1].ID}
		assert.Contains(t, subIDs, "B")
		assert.Contains(t, subIDs, "C")

		// Convert back to user-facing contexts
		finalContexts := ToInterruptContexts(signal, nil)

		// Assertions for the final list of contexts
		assert.Len(t, finalContexts, 2)
		finalIDs := []string{finalContexts[0].ID, finalContexts[1].ID}
		assert.Contains(t, finalIDs, "B")
		assert.Contains(t, finalIDs, "C")

		// Check parent linking for one of the branches
		var finalB *InterruptCtx
		if finalContexts[0].ID == "B" {
			finalB = finalContexts[0]
		} else {
			finalB = finalContexts[1]
		}
		assert.NotNil(t, finalB.Parent)
		assert.Equal(t, "D", finalB.Parent.ID)
		assert.Nil(t, finalB.Parent.Parent)
	})

	// Test Case 3: Nil and Empty Inputs
	t.Run("NilAndEmpty", func(t *testing.T) {
		assert.Nil(t, FromInterruptContexts(nil))
		assert.Nil(t, FromInterruptContexts([]*InterruptCtx{}))
		assert.Nil(t, ToInterruptContexts(nil, nil))
	})
}

func TestSignalToPersistenceMaps(t *testing.T) {
	// Test Case 1: Nil Signal
	t.Run("NilSignal", func(t *testing.T) {
		id2addr, id2state := SignalToPersistenceMaps(nil)
		assert.NotNil(t, id2addr)
		assert.NotNil(t, id2state)
		assert.Empty(t, id2addr)
		assert.Empty(t, id2state)
	})

	// Test Case 2: Single Node Signal
	t.Run("SingleNode", func(t *testing.T) {
		signal := &InterruptSignal{
			ID: "node1",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
			},
			InterruptState: InterruptState{
				State:                "test state",
				LayerSpecificPayload: "test payload",
			},
		}

		id2addr, id2state := SignalToPersistenceMaps(signal)

		assert.Len(t, id2addr, 1)
		assert.Len(t, id2state, 1)

		assert.Equal(t, signal.Address, id2addr["node1"])
		assert.Equal(t, signal.InterruptState, id2state["node1"])
	})

	// Test Case 3: Simple Tree Structure
	t.Run("SimpleTree", func(t *testing.T) {
		child1 := &InterruptSignal{
			ID: "child1",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
				{Type: AddressSegmentTool, ID: "tool1"},
			},
			InterruptState: InterruptState{
				State: "child1 state",
			},
		}

		child2 := &InterruptSignal{
			ID: "child2",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
				{Type: AddressSegmentTool, ID: "tool2"},
			},
			InterruptState: InterruptState{
				State: "child2 state",
			},
		}

		parent := &InterruptSignal{
			ID: "parent",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
			},
			InterruptState: InterruptState{
				State: "parent state",
			},
			Subs: []*InterruptSignal{child1, child2},
		}

		id2addr, id2state := SignalToPersistenceMaps(parent)

		// Should contain all 3 nodes
		assert.Len(t, id2addr, 3)
		assert.Len(t, id2state, 3)

		// Check parent node
		assert.Equal(t, parent.Address, id2addr["parent"])
		assert.Equal(t, parent.InterruptState, id2state["parent"])

		// Check child nodes
		assert.Equal(t, child1.Address, id2addr["child1"])
		assert.Equal(t, child1.InterruptState, id2state["child1"])
		assert.Equal(t, child2.Address, id2addr["child2"])
		assert.Equal(t, child2.InterruptState, id2state["child2"])
	})

	// Test Case 4: Deeply Nested Tree
	t.Run("DeeplyNestedTree", func(t *testing.T) {
		leaf1 := &InterruptSignal{
			ID: "leaf1",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
				{Type: AddressSegmentTool, ID: "tool1"},
				{Type: AddressSegmentNode, ID: "node1"},
			},
			InterruptState: InterruptState{
				State: "leaf1 state",
			},
		}

		leaf2 := &InterruptSignal{
			ID: "leaf2",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
				{Type: AddressSegmentTool, ID: "tool1"},
				{Type: AddressSegmentNode, ID: "node2"},
			},
			InterruptState: InterruptState{
				State: "leaf2 state",
			},
		}

		middle := &InterruptSignal{
			ID: "middle",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
				{Type: AddressSegmentTool, ID: "tool1"},
			},
			InterruptState: InterruptState{
				State: "middle state",
			},
			Subs: []*InterruptSignal{leaf1, leaf2},
		}

		root := &InterruptSignal{
			ID: "root",
			Address: Address{
				{Type: AddressSegmentAgent, ID: "agent1"},
			},
			InterruptState: InterruptState{
				State: "root state",
			},
			Subs: []*InterruptSignal{middle},
		}

		id2addr, id2state := SignalToPersistenceMaps(root)

		// Should contain all 4 nodes
		assert.Len(t, id2addr, 4)
		assert.Len(t, id2state, 4)

		// Verify all nodes are present
		assert.Equal(t, root.Address, id2addr["root"])
		assert.Equal(t, root.InterruptState, id2state["root"])
		assert.Equal(t, middle.Address, id2addr["middle"])
		assert.Equal(t, middle.InterruptState, id2state["middle"])
		assert.Equal(t, leaf1.Address, id2addr["leaf1"])
		assert.Equal(t, leaf1.InterruptState, id2state["leaf1"])
		assert.Equal(t, leaf2.Address, id2addr["leaf2"])
		assert.Equal(t, leaf2.InterruptState, id2state["leaf2"])
	})

	// Test Case 5: Complex Tree with Multiple Branches
	t.Run("ComplexTree", func(t *testing.T) {
		// Create a complex tree structure with multiple branches
		branch1Leaf1 := &InterruptSignal{ID: "b1l1", Address: Address{{Type: AddressSegmentAgent, ID: "a1"}}, InterruptState: InterruptState{State: "b1l1"}}
		branch1Leaf2 := &InterruptSignal{ID: "b1l2", Address: Address{{Type: AddressSegmentAgent, ID: "a1"}}, InterruptState: InterruptState{State: "b1l2"}}
		branch1 := &InterruptSignal{ID: "b1", Address: Address{{Type: AddressSegmentAgent, ID: "a1"}}, InterruptState: InterruptState{State: "b1"}, Subs: []*InterruptSignal{branch1Leaf1, branch1Leaf2}}

		branch2Leaf1 := &InterruptSignal{ID: "b2l1", Address: Address{{Type: AddressSegmentAgent, ID: "a1"}}, InterruptState: InterruptState{State: "b2l1"}}
		branch2 := &InterruptSignal{ID: "b2", Address: Address{{Type: AddressSegmentAgent, ID: "a1"}}, InterruptState: InterruptState{State: "b2"}, Subs: []*InterruptSignal{branch2Leaf1}}

		root := &InterruptSignal{ID: "root", Address: Address{{Type: AddressSegmentAgent, ID: "a1"}}, InterruptState: InterruptState{State: "root"}, Subs: []*InterruptSignal{branch1, branch2}}

		id2addr, id2state := SignalToPersistenceMaps(root)

		// Should contain all 6 nodes
		assert.Len(t, id2addr, 6)
		assert.Len(t, id2state, 6)

		// Verify all nodes are present
		expectedNodes := []string{"root", "b1", "b2", "b1l1", "b1l2", "b2l1"}
		for _, nodeID := range expectedNodes {
			assert.Contains(t, id2addr, nodeID)
			assert.Contains(t, id2state, nodeID)
		}
	})

	// Test Case 6: Empty InterruptState Values
	t.Run("EmptyInterruptState", func(t *testing.T) {
		signal := &InterruptSignal{
			ID:             "node1",
			Address:        Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			InterruptState: InterruptState{
				// Empty state values
			},
		}

		id2addr, id2state := SignalToPersistenceMaps(signal)

		assert.Len(t, id2addr, 1)
		assert.Len(t, id2state, 1)
		assert.Equal(t, signal.Address, id2addr["node1"])
		assert.Equal(t, signal.InterruptState, id2state["node1"])
	})
}

func TestGetCurrentAddress(t *testing.T) {
	// Test Case 1: No Address in Context
	t.Run("NoAddressInContext", func(t *testing.T) {
		ctx := context.Background()
		addr := GetCurrentAddress(ctx)
		assert.Nil(t, addr)
	})

	// Test Case 2: Address in Context
	t.Run("AddressInContext", func(t *testing.T) {
		ctx := context.Background()
		expectedAddr := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool1"},
		}

		// Create a context with address using internal addrCtx
		runCtx := &addrCtx{
			addr: expectedAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		addr := GetCurrentAddress(ctx)
		assert.Equal(t, expectedAddr, addr)
	})
}

func TestGetNextResumptionPoints(t *testing.T) {
	// Test Case 1: No Resume Info in Context
	t.Run("NoResumeInfo", func(t *testing.T) {
		ctx := context.Background()
		_, err := GetNextResumptionPoints(ctx)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get resume info")
	})

	// Test Case 2: Empty Resume Info
	t.Run("EmptyResumeInfo", func(t *testing.T) {
		ctx := context.Background()
		rInfo := &globalResumeInfo{
			id2Addr: make(map[string]Address),
		}
		ctx = context.WithValue(ctx, globalResumeInfoKey{}, rInfo)

		points, err := GetNextResumptionPoints(ctx)
		assert.NoError(t, err)
		assert.Empty(t, points)
	})

	// Test Case 3: Valid Resume Points
	t.Run("ValidResumePoints", func(t *testing.T) {
		ctx := context.Background()

		// Set up current address
		currentAddr := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
		}
		runCtx := &addrCtx{
			addr: currentAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		// Set up resume info with child addresses
		rInfo := &globalResumeInfo{
			id2Addr: map[string]Address{
				"child1": {
					{Type: AddressSegmentAgent, ID: "agent1"},
					{Type: AddressSegmentTool, ID: "tool1"},
				},
				"child2": {
					{Type: AddressSegmentAgent, ID: "agent1"},
					{Type: AddressSegmentTool, ID: "tool2"},
				},
				"unrelated": {
					{Type: AddressSegmentAgent, ID: "agent2"},
				},
			},
		}
		ctx = context.WithValue(ctx, globalResumeInfoKey{}, rInfo)

		points, err := GetNextResumptionPoints(ctx)
		assert.NoError(t, err)
		assert.Len(t, points, 2)
		assert.True(t, points["tool1"])
		assert.True(t, points["tool2"])
	})

	// Test Case 4: Root Address (Empty Parent)
	t.Run("RootAddress", func(t *testing.T) {
		ctx := context.Background()

		// Empty current address (root)
		runCtx := &addrCtx{
			addr: Address{},
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		// Set up resume info with various addresses
		rInfo := &globalResumeInfo{
			id2Addr: map[string]Address{
				"agent1": {
					{Type: AddressSegmentAgent, ID: "agent1"},
				},
				"agent2": {
					{Type: AddressSegmentAgent, ID: "agent2"},
				},
			},
		}
		ctx = context.WithValue(ctx, globalResumeInfoKey{}, rInfo)

		points, err := GetNextResumptionPoints(ctx)
		assert.NoError(t, err)
		assert.Len(t, points, 2)
		assert.True(t, points["agent1"])
		assert.True(t, points["agent2"])
	})
}

func TestBatchResumeWithData(t *testing.T) {
	// Test Case 1: New Resume Data
	t.Run("NewResumeData", func(t *testing.T) {
		ctx := context.Background()
		resumeData := map[string]any{
			"id1": "data1",
			"id2": "data2",
		}

		newCtx := BatchResumeWithData(ctx, resumeData)

		// Verify the data was set correctly
		rInfo, ok := newCtx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
		assert.True(t, ok)
		assert.NotNil(t, rInfo)
		assert.Equal(t, "data1", rInfo.id2ResumeData["id1"])
		assert.Equal(t, "data2", rInfo.id2ResumeData["id2"])
	})

	// Test Case 2: Merge with Existing Resume Data
	t.Run("MergeWithExisting", func(t *testing.T) {
		ctx := context.Background()

		// First call with initial data
		initialData := map[string]any{
			"id1": "initial",
		}
		ctx = BatchResumeWithData(ctx, initialData)

		// Second call with additional data
		additionalData := map[string]any{
			"id2": "additional",
		}
		newCtx := BatchResumeWithData(ctx, additionalData)

		// Verify both data sets are present
		rInfo, ok := newCtx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
		assert.True(t, ok)
		assert.NotNil(t, rInfo)
		assert.Equal(t, "initial", rInfo.id2ResumeData["id1"])
		assert.Equal(t, "additional", rInfo.id2ResumeData["id2"])
	})

	// Test Case 3: Empty Resume Data
	t.Run("EmptyResumeData", func(t *testing.T) {
		ctx := context.Background()
		newCtx := BatchResumeWithData(ctx, map[string]any{})

		rInfo, ok := newCtx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
		assert.True(t, ok)
		assert.NotNil(t, rInfo)
		assert.Empty(t, rInfo.id2ResumeData)
	})
}

func TestGetInterruptState(t *testing.T) {
	// Test Case 1: No Interrupt State
	t.Run("NoInterruptState", func(t *testing.T) {
		ctx := context.Background()
		wasInterrupted, hasState, state := GetInterruptState[string](ctx)
		assert.False(t, wasInterrupted)
		assert.False(t, hasState)
		assert.Equal(t, "", state)
	})

	// Test Case 2: With Interrupt State
	t.Run("WithInterruptState", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with interrupt state
		runCtx := &addrCtx{
			interruptState: &InterruptState{
				State: "test state",
			},
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		wasInterrupted, hasState, state := GetInterruptState[string](ctx)
		assert.True(t, wasInterrupted)
		assert.True(t, hasState)
		assert.Equal(t, "test state", state)
	})

	// Test Case 3: Wrong Type for Interrupt State
	t.Run("WrongType", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with interrupt state of wrong type
		runCtx := &addrCtx{
			interruptState: &InterruptState{
				State: 123, // int instead of string
			},
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		wasInterrupted, hasState, state := GetInterruptState[string](ctx)
		assert.True(t, wasInterrupted)
		assert.False(t, hasState) // Should be false due to type mismatch
		assert.Equal(t, "", state)
	})

	// Test Case 4: Nil Interrupt State
	t.Run("NilInterruptState", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with nil interrupt state
		runCtx := &addrCtx{
			interruptState: nil,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		wasInterrupted, hasState, state := GetInterruptState[string](ctx)
		assert.False(t, wasInterrupted) // Should be false because interruptState is nil
		assert.False(t, hasState)       // Should be false because state is nil
		assert.Equal(t, "", state)
	})
}

func TestGetResumeContext(t *testing.T) {
	// Test Case 1: Not Resume Target
	t.Run("NotResumeTarget", func(t *testing.T) {
		ctx := context.Background()
		isResumeTarget, hasData, data := GetResumeContext[string](ctx)
		assert.False(t, isResumeTarget)
		assert.False(t, hasData)
		assert.Equal(t, "", data)
	})

	// Test Case 2: Resume Target with Data
	t.Run("ResumeTargetWithData", func(t *testing.T) {
		ctx := context.Background()

		// Create a context as resume target with data
		runCtx := &addrCtx{
			isResumeTarget: true,
			resumeData:     "resume data",
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		isResumeTarget, hasData, data := GetResumeContext[string](ctx)
		assert.True(t, isResumeTarget)
		assert.True(t, hasData)
		assert.Equal(t, "resume data", data)
	})

	// Test Case 3: Resume Target without Data
	t.Run("ResumeTargetWithoutData", func(t *testing.T) {
		ctx := context.Background()

		// Create a context as resume target without data
		runCtx := &addrCtx{
			isResumeTarget: true,
			resumeData:     nil,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		isResumeTarget, hasData, data := GetResumeContext[string](ctx)
		assert.True(t, isResumeTarget)
		assert.False(t, hasData)
		assert.Equal(t, "", data)
	})

	// Test Case 4: Wrong Type for Resume Data
	t.Run("WrongType", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with resume data of wrong type
		runCtx := &addrCtx{
			isResumeTarget: true,
			resumeData:     123, // int instead of string
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		isResumeTarget, hasData, data := GetResumeContext[string](ctx)
		assert.True(t, isResumeTarget)
		assert.False(t, hasData) // Should be false due to type mismatch
		assert.Equal(t, "", data)
	})
}

func TestWithLayerPayload(t *testing.T) {
	// Test Case 1: Basic Usage
	t.Run("BasicUsage", func(t *testing.T) {
		config := &InterruptConfig{}
		opt := WithLayerPayload("test payload")
		opt(config)
		assert.Equal(t, "test payload", config.LayerPayload)
	})

	// Test Case 2: Nil Payload
	t.Run("NilPayload", func(t *testing.T) {
		config := &InterruptConfig{LayerPayload: "existing"}
		opt := WithLayerPayload(nil)
		opt(config)
		assert.Nil(t, config.LayerPayload)
	})

	// Test Case 3: Complex Payload
	t.Run("ComplexPayload", func(t *testing.T) {
		config := &InterruptConfig{}
		payload := map[string]any{
			"key1": "value1",
			"key2": 123,
		}
		opt := WithLayerPayload(payload)
		opt(config)
		assert.Equal(t, payload, config.LayerPayload)
	})
}

func TestInterruptFunction(t *testing.T) {
	// Test Case 1: Simple Interrupt without SubContexts
	t.Run("SimpleInterrupt", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a mock address
		expectedAddr := Address{{Type: AddressSegmentAgent, ID: "test-agent"}}
		runCtx := &addrCtx{
			addr: expectedAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		info := "test info"
		state := "test state"

		signal, err := Interrupt(ctx, info, state, nil)
		assert.NoError(t, err)
		assert.NotNil(t, signal)
		assert.NotEmpty(t, signal.ID)
		assert.Equal(t, info, signal.Info)
		assert.Equal(t, state, signal.State)
		assert.True(t, signal.IsRootCause)
		assert.Equal(t, expectedAddr, signal.Address)
	})

	// Test Case 2: Interrupt with SubContexts
	t.Run("InterruptWithSubContexts", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a mock address
		expectedAddr := Address{{Type: AddressSegmentAgent, ID: "parent-agent"}}
		runCtx := &addrCtx{
			addr: expectedAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		// Create sub contexts
		subContexts := []*InterruptSignal{
			{
				ID:      "child1",
				Address: Address{{Type: AddressSegmentAgent, ID: "child1"}},
			},
			{
				ID:      "child2",
				Address: Address{{Type: AddressSegmentAgent, ID: "child2"}},
			},
		}

		info := "parent info"
		state := "parent state"

		signal, err := Interrupt(ctx, info, state, subContexts)
		assert.NoError(t, err)
		assert.NotNil(t, signal)
		assert.NotEmpty(t, signal.ID)
		assert.Equal(t, info, signal.Info)
		assert.Equal(t, state, signal.State)
		assert.False(t, signal.IsRootCause) // Should be false when there are sub contexts
		assert.Len(t, signal.Subs, 2)
		assert.Equal(t, "child1", signal.Subs[0].ID)
		assert.Equal(t, "child2", signal.Subs[1].ID)
	})

	// Test Case 3: Interrupt with Options
	t.Run("InterruptWithOptions", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a mock address
		expectedAddr := Address{{Type: AddressSegmentAgent, ID: "test-agent"}}
		runCtx := &addrCtx{
			addr: expectedAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		info := "test info"
		state := "test state"
		layerPayload := "layer payload"

		signal, err := Interrupt(ctx, info, state, nil, WithLayerPayload(layerPayload))
		assert.NoError(t, err)
		assert.NotNil(t, signal)
		assert.Equal(t, layerPayload, signal.LayerSpecificPayload)
	})

	// Test Case 4: Empty SubContexts
	t.Run("EmptySubContexts", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a mock address
		expectedAddr := Address{{Type: AddressSegmentAgent, ID: "test-agent"}}
		runCtx := &addrCtx{
			addr: expectedAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		info := "test info"
		state := "test state"

		signal, err := Interrupt(ctx, info, state, []*InterruptSignal{})
		assert.NoError(t, err)
		assert.NotNil(t, signal)
		assert.True(t, signal.IsRootCause) // Should be true when sub contexts is empty
		assert.Empty(t, signal.Subs)
	})
}

func TestAddressMethods(t *testing.T) {
	// Test Case 1: Address.String()
	t.Run("AddressString", func(t *testing.T) {
		addr := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool1"},
			{Type: AddressSegmentNode, ID: "node1", SubID: "sub1"},
		}

		result := addr.String()
		expected := "agent:agent1;tool:tool1;node:node1:sub1"
		assert.Equal(t, expected, result)
	})

	// Test Case 2: Address.String() with empty address
	t.Run("EmptyAddressString", func(t *testing.T) {
		var addr Address
		result := addr.String()
		assert.Equal(t, "", result)
	})

	// Test Case 3: Address.Equals() with equal addresses
	t.Run("AddressEquals", func(t *testing.T) {
		addr1 := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool1"},
		}
		addr2 := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool1"},
		}

		assert.True(t, addr1.Equals(addr2))
	})

	// Test Case 4: Address.Equals() with different addresses
	t.Run("AddressNotEquals", func(t *testing.T) {
		addr1 := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool1"},
		}
		addr2 := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool2"},
		}

		assert.False(t, addr1.Equals(addr2))
	})

	// Test Case 5: Address.Equals() with different lengths
	t.Run("AddressDifferentLengths", func(t *testing.T) {
		addr1 := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
			{Type: AddressSegmentTool, ID: "tool1"},
		}
		addr2 := Address{
			{Type: AddressSegmentAgent, ID: "agent1"},
		}

		assert.False(t, addr1.Equals(addr2))
	})

	// Test Case 6: Address.Equals() with SubID differences
	t.Run("AddressSubIDDifference", func(t *testing.T) {
		addr1 := Address{
			{Type: AddressSegmentAgent, ID: "agent1", SubID: "sub1"},
		}
		addr2 := Address{
			{Type: AddressSegmentAgent, ID: "agent1", SubID: "sub2"},
		}

		assert.False(t, addr1.Equals(addr2))
	})
}

func TestAppendAddressSegment(t *testing.T) {
	// Test Case 1: Append to empty address
	t.Run("AppendToEmpty", func(t *testing.T) {
		ctx := context.Background()

		newCtx := AppendAddressSegment(ctx, AddressSegmentAgent, "agent1", "")

		addr := GetCurrentAddress(newCtx)
		assert.Len(t, addr, 1)
		assert.Equal(t, AddressSegmentAgent, addr[0].Type)
		assert.Equal(t, "agent1", addr[0].ID)
		assert.Equal(t, "", addr[0].SubID)
	})

	// Test Case 2: Append to existing address
	t.Run("AppendToExisting", func(t *testing.T) {
		ctx := context.Background()

		// First append
		ctx = AppendAddressSegment(ctx, AddressSegmentAgent, "agent1", "")

		// Second append
		newCtx := AppendAddressSegment(ctx, AddressSegmentTool, "tool1", "call1")

		addr := GetCurrentAddress(newCtx)
		assert.Len(t, addr, 2)
		assert.Equal(t, AddressSegmentAgent, addr[0].Type)
		assert.Equal(t, "agent1", addr[0].ID)
		assert.Equal(t, AddressSegmentTool, addr[1].Type)
		assert.Equal(t, "tool1", addr[1].ID)
		assert.Equal(t, "call1", addr[1].SubID)
	})

	// Test Case 3: Append with SubID
	t.Run("AppendWithSubID", func(t *testing.T) {
		ctx := context.Background()

		newCtx := AppendAddressSegment(ctx, AddressSegmentTool, "tool1", "call123")

		addr := GetCurrentAddress(newCtx)
		assert.Len(t, addr, 1)
		assert.Equal(t, AddressSegmentTool, addr[0].Type)
		assert.Equal(t, "tool1", addr[0].ID)
		assert.Equal(t, "call123", addr[0].SubID)
	})
}

func TestPopulateInterruptState(t *testing.T) {
	// Test Case 1: Populate with matching address
	t.Run("PopulateMatchingAddress", func(t *testing.T) {
		ctx := context.Background()

		// Set up current address
		currentAddr := Address{{Type: AddressSegmentAgent, ID: "agent1"}}
		runCtx := &addrCtx{
			addr: currentAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		// Set up interrupt state data
		id2Addr := map[string]Address{
			"interrupt1": currentAddr,
		}
		id2State := map[string]InterruptState{
			"interrupt1": {State: "test state"},
		}

		newCtx := PopulateInterruptState(ctx, id2Addr, id2State)

		// Verify the state was populated
		wasInterrupted, hasState, state := GetInterruptState[string](newCtx)
		assert.True(t, wasInterrupted)
		assert.True(t, hasState)
		assert.Equal(t, "test state", state)
	})

	// Test Case 2: Populate with non-matching address
	t.Run("PopulateNonMatchingAddress", func(t *testing.T) {
		ctx := context.Background()

		// Set up current address
		currentAddr := Address{{Type: AddressSegmentAgent, ID: "agent1"}}
		runCtx := &addrCtx{
			addr: currentAddr,
		}
		ctx = context.WithValue(ctx, addrCtxKey{}, runCtx)

		// Set up interrupt state data with different address
		id2Addr := map[string]Address{
			"interrupt1": {{Type: AddressSegmentAgent, ID: "agent2"}},
		}
		id2State := map[string]InterruptState{
			"interrupt1": {State: "test state"},
		}

		newCtx := PopulateInterruptState(ctx, id2Addr, id2State)

		// Verify the state was NOT populated (no matching address)
		wasInterrupted, hasState, state := GetInterruptState[string](newCtx)
		assert.False(t, wasInterrupted)
		assert.False(t, hasState)
		assert.Equal(t, "", state)
	})

	// Test Case 3: Populate with empty data
	t.Run("PopulateEmptyData", func(t *testing.T) {
		ctx := context.Background()

		newCtx := PopulateInterruptState(ctx, map[string]Address{}, map[string]InterruptState{})

		// Verify no state was populated
		wasInterrupted, hasState, state := GetInterruptState[string](newCtx)
		assert.False(t, wasInterrupted)
		assert.False(t, hasState)
		assert.Equal(t, "", state)
	})
}

func TestStringMethods(t *testing.T) {
	// Test Case 1: InterruptSignal.Error()
	t.Run("InterruptSignalError", func(t *testing.T) {
		signal := &InterruptSignal{
			ID:      "test-id",
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			InterruptInfo: InterruptInfo{
				Info: "test info",
			},
			InterruptState: InterruptState{
				State:                "test state",
				LayerSpecificPayload: "test payload",
			},
			Subs: []*InterruptSignal{
				{ID: "sub1"},
			},
		}

		errorStr := signal.Error()
		expectedContains := []string{
			"interrupt signal:",
			"ID=test-id",
			"Addr=agent:agent1",
			"Info=interrupt info: Info=test info, IsRootCause=false",
			"State=interrupt state: State=test state, LayerSpecificPayload=test payload",
			"SubsLen=1",
		}

		for _, expected := range expectedContains {
			assert.Contains(t, errorStr, expected)
		}
	})

	// Test Case 2: InterruptState.String()
	t.Run("InterruptStateString", func(t *testing.T) {
		state := &InterruptState{
			State:                "test state",
			LayerSpecificPayload: "test payload",
		}

		result := state.String()
		expected := "interrupt state: State=test state, LayerSpecificPayload=test payload"
		assert.Equal(t, expected, result)
	})

	// Test Case 3: InterruptState.String() with nil
	t.Run("InterruptStateStringNil", func(t *testing.T) {
		var state *InterruptState
		result := state.String()
		assert.Equal(t, "", result)
	})

	// Test Case 4: InterruptInfo.String()
	t.Run("InterruptInfoString", func(t *testing.T) {
		info := &InterruptInfo{
			Info:        "test info",
			IsRootCause: true,
		}

		result := info.String()
		expected := "interrupt info: Info=test info, IsRootCause=true"
		assert.Equal(t, expected, result)
	})

	// Test Case 5: InterruptInfo.String() with nil
	t.Run("InterruptInfoStringNil", func(t *testing.T) {
		var info *InterruptInfo
		result := info.String()
		assert.Equal(t, "", result)
	})
}

func TestInterruptCtxEqualsWithoutID(t *testing.T) {
	// Test Case 1: Equal contexts
	t.Run("EqualContexts", func(t *testing.T) {
		ctx1 := &InterruptCtx{
			ID:          "id1",
			Address:     Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Info:        "info1",
			IsRootCause: true,
		}
		ctx2 := &InterruptCtx{
			ID:          "id2", // Different ID should be ignored
			Address:     Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Info:        "info1",
			IsRootCause: true,
		}

		assert.True(t, ctx1.EqualsWithoutID(ctx2))
	})

	// Test Case 2: Different addresses
	t.Run("DifferentAddresses", func(t *testing.T) {
		ctx1 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
		}
		ctx2 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent2"}},
		}

		assert.False(t, ctx1.EqualsWithoutID(ctx2))
	})

	// Test Case 3: Different root cause flags
	t.Run("DifferentRootCause", func(t *testing.T) {
		ctx1 := &InterruptCtx{
			Address:     Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			IsRootCause: true,
		}
		ctx2 := &InterruptCtx{
			Address:     Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			IsRootCause: false,
		}

		assert.False(t, ctx1.EqualsWithoutID(ctx2))
	})

	// Test Case 4: Different info
	t.Run("DifferentInfo", func(t *testing.T) {
		ctx1 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Info:    "info1",
		}
		ctx2 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Info:    "info2",
		}

		assert.False(t, ctx1.EqualsWithoutID(ctx2))
	})

	// Test Case 5: Nil contexts
	t.Run("NilContexts", func(t *testing.T) {
		var ctx1 *InterruptCtx
		var ctx2 *InterruptCtx

		assert.True(t, ctx1.EqualsWithoutID(ctx2))

		ctx3 := &InterruptCtx{}
		assert.False(t, ctx1.EqualsWithoutID(ctx3))
		assert.False(t, ctx3.EqualsWithoutID(ctx1))
	})

	// Test Case 6: With parent contexts
	t.Run("WithParentContexts", func(t *testing.T) {
		parent1 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "parent"}},
		}
		parent2 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "parent"}},
		}

		ctx1 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Parent:  parent1,
		}
		ctx2 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Parent:  parent2,
		}

		assert.True(t, ctx1.EqualsWithoutID(ctx2))
	})

	// Test Case 7: Different parent contexts
	t.Run("DifferentParentContexts", func(t *testing.T) {
		parent1 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "parent1"}},
		}
		parent2 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "parent2"}},
		}

		ctx1 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Parent:  parent1,
		}
		ctx2 := &InterruptCtx{
			Address: Address{{Type: AddressSegmentAgent, ID: "agent1"}},
			Parent:  parent2,
		}

		assert.False(t, ctx1.EqualsWithoutID(ctx2))
	})
}
