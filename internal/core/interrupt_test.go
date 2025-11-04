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
	"testing"

	"github.com/stretchr/testify/assert"
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
