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

package adk

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestSessionValues(t *testing.T) {
	// Test Case 1: Basic AddSessionValues and GetSessionValues
	t.Run("BasicSessionValues", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add values to the session
		values := map[string]any{
			"key1": "value1",
			"key2": 42,
			"key3": true,
		}
		AddSessionValues(ctx, values)

		// Get all values from the session
		retrievedValues := GetSessionValues(ctx)

		// Verify the values were added correctly
		assert.Equal(t, "value1", retrievedValues["key1"])
		assert.Equal(t, 42, retrievedValues["key2"])
		assert.Equal(t, true, retrievedValues["key3"])
		assert.Len(t, retrievedValues, 3)
	})

	// Test Case 2: AddSessionValues with empty context
	t.Run("AddSessionValuesEmptyContext", func(t *testing.T) {
		ctx := context.Background()

		// Add values to a context without a run session
		values := map[string]any{
			"key1": "value1",
		}
		AddSessionValues(ctx, values)

		// Get values should return empty map
		retrievedValues := GetSessionValues(ctx)
		assert.Empty(t, retrievedValues)
	})

	// Test Case 3: GetSessionValues with empty context
	t.Run("GetSessionValuesEmptyContext", func(t *testing.T) {
		ctx := context.Background()

		// Get values from a context without a run session
		retrievedValues := GetSessionValues(ctx)
		assert.Empty(t, retrievedValues)
	})

	// Test Case 4: AddSessionValues with nil values
	t.Run("AddSessionValuesNilValues", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add nil values map
		AddSessionValues(ctx, nil)

		// Get values should still be empty
		retrievedValues := GetSessionValues(ctx)
		assert.Empty(t, retrievedValues)
	})

	// Test Case 5: AddSessionValues with empty values
	t.Run("AddSessionValuesEmptyValues", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add empty values map
		AddSessionValues(ctx, map[string]any{})

		// Get values should be empty
		retrievedValues := GetSessionValues(ctx)
		assert.Empty(t, retrievedValues)
	})

	// Test Case 6: AddSessionValues with complex data types
	t.Run("AddSessionValuesComplexTypes", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add complex values to the session
		values := map[string]any{
			"string": "hello world",
			"int":    123,
			"float":  45.67,
			"bool":   true,
			"slice":  []string{"a", "b", "c"},
			"map":    map[string]int{"x": 1, "y": 2},
			"struct": struct{ Name string }{Name: "test"},
		}
		AddSessionValues(ctx, values)

		// Get all values from the session
		retrievedValues := GetSessionValues(ctx)

		// Verify the complex values were added correctly
		assert.Equal(t, "hello world", retrievedValues["string"])
		assert.Equal(t, 123, retrievedValues["int"])
		assert.Equal(t, 45.67, retrievedValues["float"])
		assert.Equal(t, true, retrievedValues["bool"])
		assert.Equal(t, []string{"a", "b", "c"}, retrievedValues["slice"])
		assert.Equal(t, map[string]int{"x": 1, "y": 2}, retrievedValues["map"])
		assert.Equal(t, struct{ Name string }{Name: "test"}, retrievedValues["struct"])
		assert.Len(t, retrievedValues, 7)
	})

	// Test Case 7: AddSessionValues overwrites existing values
	t.Run("AddSessionValuesOverwrite", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add initial values
		initialValues := map[string]any{
			"key1": "initial1",
			"key2": "initial2",
		}
		AddSessionValues(ctx, initialValues)

		// Add values that overwrite some keys
		overwriteValues := map[string]any{
			"key1": "overwritten1",
			"key3": "new3",
		}
		AddSessionValues(ctx, overwriteValues)

		// Get all values from the session
		retrievedValues := GetSessionValues(ctx)

		// Verify the values were overwritten correctly
		assert.Equal(t, "overwritten1", retrievedValues["key1"]) // overwritten
		assert.Equal(t, "initial2", retrievedValues["key2"])     // unchanged
		assert.Equal(t, "new3", retrievedValues["key3"])         // new
		assert.Len(t, retrievedValues, 3)
	})

	// Test Case 8: Concurrent access to session values
	t.Run("ConcurrentSessionValues", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add initial values
		initialValues := map[string]any{
			"counter": 0,
		}
		AddSessionValues(ctx, initialValues)

		// Simulate concurrent access
		done := make(chan bool)

		// Goroutine 1: Add values
		go func() {
			for i := 0; i < 100; i++ {
				values := map[string]any{
					"goroutine1": i,
				}
				AddSessionValues(ctx, values)
			}
			done <- true
		}()

		// Goroutine 2: Add different values
		go func() {
			for i := 0; i < 100; i++ {
				values := map[string]any{
					"goroutine2": i,
				}
				AddSessionValues(ctx, values)
			}
			done <- true
		}()

		// Wait for both goroutines to complete
		<-done
		<-done

		// Verify that both values were set (last write wins)
		retrievedValues := GetSessionValues(ctx)
		assert.Equal(t, 0, retrievedValues["counter"])
		assert.Equal(t, 99, retrievedValues["goroutine1"])
		assert.Equal(t, 99, retrievedValues["goroutine2"])
	})

	// Test Case 9: GetSessionValue individual value
	t.Run("GetSessionValueIndividual", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add values to the session
		values := map[string]any{
			"key1": "value1",
			"key2": 42,
		}
		AddSessionValues(ctx, values)

		// Get individual values
		value1, exists1 := GetSessionValue(ctx, "key1")
		value2, exists2 := GetSessionValue(ctx, "key2")
		value3, exists3 := GetSessionValue(ctx, "nonexistent")

		// Verify individual values
		assert.True(t, exists1)
		assert.Equal(t, "value1", value1)

		assert.True(t, exists2)
		assert.Equal(t, 42, value2)

		assert.False(t, exists3)
		assert.Nil(t, value3)
	})

	// Test Case 10: AddSessionValue individual value
	t.Run("AddSessionValueIndividual", func(t *testing.T) {
		ctx := context.Background()

		// Create a context with a run session
		session := newRunSession()
		runCtx := &runContext{Session: session}
		ctx = setRunCtx(ctx, runCtx)

		// Add individual values
		AddSessionValue(ctx, "key1", "value1")
		AddSessionValue(ctx, "key2", 42)

		// Get all values
		retrievedValues := GetSessionValues(ctx)

		// Verify the values were added correctly
		assert.Equal(t, "value1", retrievedValues["key1"])
		assert.Equal(t, 42, retrievedValues["key2"])
		assert.Len(t, retrievedValues, 2)
	})

	// Test Case 11: AddSessionValue with empty context
	t.Run("AddSessionValueEmptyContext", func(t *testing.T) {
		ctx := context.Background()

		// Add individual value to a context without a run session
		AddSessionValue(ctx, "key1", "value1")

		// Get individual value should return false
		value, exists := GetSessionValue(ctx, "key1")
		assert.False(t, exists)
		assert.Nil(t, value)

		// Get all values should return empty map
		retrievedValues := GetSessionValues(ctx)
		assert.Empty(t, retrievedValues)
	})

	// Test Case 12: Integration with run context initialization
	t.Run("IntegrationWithRunContext", func(t *testing.T) {
		ctx := context.Background()

		// Initialize a run context with an agent
		input := &AgentInput{
			Messages: []Message{
				schema.UserMessage("test input"),
			},
		}
		ctx, runCtx := initRunCtx(ctx, "test-agent", input)

		// Verify the run context was created
		assert.NotNil(t, runCtx)
		assert.NotNil(t, runCtx.Session)

		// Add values to the session
		values := map[string]any{
			"integration_key": "integration_value",
		}
		AddSessionValues(ctx, values)

		// Get values from the session
		retrievedValues := GetSessionValues(ctx)
		assert.Equal(t, "integration_value", retrievedValues["integration_key"])

		// Verify the run path was set correctly
		assert.Len(t, runCtx.RunPath, 1)
		assert.Equal(t, "test-agent", runCtx.RunPath[0].agentName)
	})
}

func TestForkJoinRunCtx(t *testing.T) {
	// Helper to create a named event
	newEvent := func(name string) *AgentEvent {
		// Add a small sleep to ensure timestamps are distinct
		time.Sleep(1 * time.Millisecond)
		return &AgentEvent{AgentName: name}
	}

	// Helper to get event names from a slice of wrappers
	getEventNames := func(wrappers []*agentEventWrapper) []string {
		names := make([]string, len(wrappers))
		for i, w := range wrappers {
			names[i] = w.AgentName
		}
		return names
	}

	// 1. Setup: Create an initial runContext for the main execution path.
	mainCtx, mainRunCtx := initRunCtx(context.Background(), "Main", nil)

	// 2. Run Agent A
	eventA := newEvent("A")
	mainRunCtx.Session.addEvent(eventA)
	assert.Equal(t, []string{"A"}, getEventNames(mainRunCtx.Session.getEvents()), "After A")

	// 3. Fork for Par(B, C)
	ctxB := forkRunCtx(mainCtx)
	ctxC := forkRunCtx(mainCtx)

	// Assertions for Fork
	runCtxB := getRunCtx(ctxB)
	runCtxC := getRunCtx(ctxC)
	assert.NotSame(t, mainRunCtx.Session, runCtxB.Session, "Session B should be a new struct")
	assert.NotSame(t, mainRunCtx.Session, runCtxC.Session, "Session C should be a new struct")
	assert.NotSame(t, runCtxB.Session, runCtxC.Session, "Sessions B and C should be different")
	assert.Nil(t, mainRunCtx.Session.LaneEvents, "Main session should have no lane events yet")
	assert.NotNil(t, runCtxB.Session.LaneEvents, "Session B should have lane events")
	assert.NotNil(t, runCtxC.Session.LaneEvents, "Session C should have lane events")
	assert.Nil(t, runCtxB.Session.LaneEvents.Parent, "Lane B's parent should be the main (nil) lane")
	assert.Nil(t, runCtxC.Session.LaneEvents.Parent, "Lane C's parent should be the main (nil) lane")

	// 4. Run Agent B
	eventB := newEvent("B")
	runCtxB.Session.addEvent(eventB)
	assert.Equal(t, []string{"A", "B"}, getEventNames(runCtxB.Session.getEvents()), "After B")

	// 5. Run Agent C (and Nested Fork for Par(D, E))
	eventC1 := newEvent("C1")
	runCtxC.Session.addEvent(eventC1)
	assert.Equal(t, []string{"A", "C1"}, getEventNames(runCtxC.Session.getEvents()), "After C1")

	ctxD := forkRunCtx(ctxC)
	ctxE := forkRunCtx(ctxC)

	// Assertions for Nested Fork
	runCtxD := getRunCtx(ctxD)
	runCtxE := getRunCtx(ctxE)
	assert.NotNil(t, runCtxD.Session.LaneEvents.Parent, "Lane D's parent should be Lane C")
	assert.Same(t, runCtxC.Session.LaneEvents, runCtxD.Session.LaneEvents.Parent, "Lane D's parent must be Lane C's node")
	assert.Same(t, runCtxC.Session.LaneEvents, runCtxE.Session.LaneEvents.Parent, "Lane E's parent must be Lane C's node")

	// 6. Run Agents D and E
	eventD := newEvent("D")
	runCtxD.Session.addEvent(eventD)
	eventE := newEvent("E")
	runCtxE.Session.addEvent(eventE)

	assert.Equal(t, []string{"A", "C1", "D"}, getEventNames(runCtxD.Session.getEvents()), "After D")
	assert.Equal(t, []string{"A", "C1", "E"}, getEventNames(runCtxE.Session.getEvents()), "After E")

	// 7. Join Par(D, E)
	joinRunCtxs(ctxC, ctxD, ctxE)

	// Assertions for Nested Join
	// The events should now be committed to Lane C's event slice.
	assert.Equal(t, []string{"A", "C1", "D", "E"}, getEventNames(runCtxC.Session.getEvents()), "After joining D and E")

	// 8. Join Par(B, C)
	joinRunCtxs(mainCtx, ctxB, ctxC)

	// Assertions for Top-Level Join
	// The events should now be committed to the main session's Events slice.
	assert.Equal(t, []string{"A", "B", "C1", "D", "E"}, getEventNames(mainRunCtx.Session.getEvents()), "After joining B and C")

	// 9. Run Agent F
	eventF := newEvent("F")
	mainRunCtx.Session.addEvent(eventF)
	assert.Equal(t, []string{"A", "B", "C1", "D", "E", "F"}, getEventNames(mainRunCtx.Session.getEvents()), "After F")
}
