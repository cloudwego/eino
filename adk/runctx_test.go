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
