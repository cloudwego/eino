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

package deepagents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	. "github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"
)

// mockMemoryStore is a simple in-memory implementation of MemoryStore for testing.
type mockMemoryStore struct {
	memories map[string]*Memory
}

func newMockMemoryStore() *mockMemoryStore {
	return &mockMemoryStore{
		memories: make(map[string]*Memory),
	}
}

// NewMockMemoryStore creates a new mock memory store instance for testing.
// This is exported so it can be used by other test files.
func NewMockMemoryStore() MemoryStore {
	return newMockMemoryStore()
}

func (m *mockMemoryStore) Add(ctx context.Context, memory *Memory) error {
	if _, exists := m.memories[memory.Key]; exists {
		return errors.New("memory with key already exists: " + memory.Key)
	}
	// Make a copy to avoid external modifications
	memCopy := &Memory{
		Key:       memory.Key,
		Value:     memory.Value,
		Metadata:  make(map[string]string),
		Timestamp: memory.Timestamp,
	}
	for k, v := range memory.Metadata {
		memCopy.Metadata[k] = v
	}
	m.memories[memory.Key] = memCopy
	return nil
}

func (m *mockMemoryStore) Get(ctx context.Context, key string) (*Memory, error) {
	memory, exists := m.memories[key]
	if !exists {
		return nil, nil
	}
	// Return a copy
	memCopy := &Memory{
		Key:       memory.Key,
		Value:     memory.Value,
		Metadata:  make(map[string]string),
		Timestamp: memory.Timestamp,
	}
	for k, v := range memory.Metadata {
		memCopy.Metadata[k] = v
	}
	return memCopy, nil
}

func (m *mockMemoryStore) Search(ctx context.Context, query string, limit int) ([]*Memory, error) {
	results := make([]*Memory, 0)
	for _, memory := range m.memories {
		// Simple search: check if query appears in key, value, or tags
		valueStr := ""
		if str, ok := memory.Value.(string); ok {
			valueStr = str
		} else {
			valueStr = strings.ToLower(fmt.Sprintf("%v", memory.Value))
		}

		queryLower := strings.ToLower(query)
		if strings.Contains(strings.ToLower(memory.Key), queryLower) ||
			strings.Contains(valueStr, queryLower) {
			results = append(results, memory)
		}

		// Check tags
		if tags, ok := memory.Metadata["tags"]; ok {
			if strings.Contains(strings.ToLower(tags), queryLower) {
				results = append(results, memory)
			}
		}

		if limit > 0 && len(results) >= limit {
			break
		}
	}
	return results, nil
}

func (m *mockMemoryStore) Update(ctx context.Context, key string, memory *Memory) error {
	if _, exists := m.memories[key]; !exists {
		return errors.New("memory not found: " + key)
	}
	// Make a copy
	memCopy := &Memory{
		Key:       key,
		Value:     memory.Value,
		Metadata:  make(map[string]string),
		Timestamp: memory.Timestamp,
	}
	for k, v := range memory.Metadata {
		memCopy.Metadata[k] = v
	}
	m.memories[key] = memCopy
	return nil
}

func (m *mockMemoryStore) Delete(ctx context.Context, key string) error {
	if _, exists := m.memories[key]; !exists {
		return errors.New("memory not found: " + key)
	}
	delete(m.memories, key)
	return nil
}

func (m *mockMemoryStore) List(ctx context.Context) ([]string, error) {
	keys := make([]string, 0, len(m.memories))
	for key := range m.memories {
		keys = append(keys, key)
	}
	return keys, nil
}

func TestMemoryTools(t *testing.T) {
	PatchConvey("TestMemoryTools", t, func() {
		ctx := context.Background()

		// Test remember tool
		Convey("Test remember tool", func() {
			memoryStore := newMockMemoryStore()
			rememberTool := &rememberTool{memoryStore: memoryStore}

			// Test tool info
			info, err := rememberTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "remember")

			// Test successful storage
			args := `{"key": "user_preference", "value": "User prefers dark mode"}`
			result, err := rememberTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully stored memory")
			So(result, ShouldContainSubstring, "user_preference")

			// Verify memory was stored
			memory, err := memoryStore.Get(ctx, "user_preference")
			So(err, ShouldBeNil)
			So(memory, ShouldNotBeNil)
			So(memory.Value, ShouldEqual, "User prefers dark mode")

			// Test storage with tags
			argsWithTags := `{"key": "project_decision", "value": "Use Python 3.11", "tags": "decision,language"}`
			result, err = rememberTool.InvokableRun(ctx, argsWithTags)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully stored memory")

			// Verify tags were stored
			memory, err = memoryStore.Get(ctx, "project_decision")
			So(err, ShouldBeNil)
			So(memory, ShouldNotBeNil)
			So(memory.Metadata["tags"], ShouldEqual, "decision,language")

			// Test duplicate key error (should fail when trying to add same key again)
			// Note: The remember tool doesn't check for duplicates, it just calls Add
			// The error would come from the memory store
			_, err = rememberTool.InvokableRun(ctx, args)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to store memory")
		})

		// Test recall tool
		Convey("Test recall tool", func() {
			memoryStore := newMockMemoryStore()
			recallTool := &recallTool{memoryStore: memoryStore}

			// Setup: add a memory first
			memory := &Memory{
				Key:       "user_preference",
				Value:     "User prefers dark mode",
				Metadata:  make(map[string]string),
				Timestamp: time.Now(),
			}
			err := memoryStore.Add(ctx, memory)
			So(err, ShouldBeNil)

			// Test tool info
			info, err := recallTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "recall")

			// Test successful recall
			args := `{"key": "user_preference"}`
			result, err := recallTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "user_preference")
			So(result, ShouldContainSubstring, "User prefers dark mode")

			// Test non-existent key
			argsNotFound := `{"key": "non_existent"}`
			result, err = recallTool.InvokableRun(ctx, argsNotFound)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "No memory found")
		})

		// Test update_memory tool
		Convey("Test update_memory tool", func() {
			memoryStore := newMockMemoryStore()
			updateTool := &updateMemoryTool{memoryStore: memoryStore}

			// Setup: add memories first
			memory1 := &Memory{
				Key:       "user_preference",
				Value:     "User prefers dark mode",
				Metadata:  make(map[string]string),
				Timestamp: time.Now(),
			}
			err := memoryStore.Add(ctx, memory1)
			So(err, ShouldBeNil)

			memory2 := &Memory{
				Key:       "project_decision",
				Value:     "Use Python 3.11",
				Metadata:  map[string]string{"tags": "decision,language"},
				Timestamp: time.Now(),
			}
			err = memoryStore.Add(ctx, memory2)
			So(err, ShouldBeNil)

			// Test tool info
			info, err := updateTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "update_memory")

			// Test successful update
			args := `{"key": "user_preference", "value": "User prefers light mode now"}`
			result, err := updateTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully updated memory")

			// Verify update
			memory, err := memoryStore.Get(ctx, "user_preference")
			So(err, ShouldBeNil)
			So(memory, ShouldNotBeNil)
			So(memory.Value, ShouldEqual, "User prefers light mode now")

			// Test update with tags
			argsWithTags := `{"key": "project_decision", "value": "Use Python 3.12", "tags": "decision,language,updated"}`
			result, err = updateTool.InvokableRun(ctx, argsWithTags)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully updated memory")

			// Verify tags were updated
			memory, err = memoryStore.Get(ctx, "project_decision")
			So(err, ShouldBeNil)
			So(memory, ShouldNotBeNil)
			So(memory.Metadata["tags"], ShouldEqual, "decision,language,updated")

			// Test update non-existent key
			argsNotFound := `{"key": "non_existent", "value": "test"}`
			_, err = updateTool.InvokableRun(ctx, argsNotFound)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to update memory")
		})

		// Test forget tool
		Convey("Test forget tool", func() {
			memoryStore := newMockMemoryStore()
			forgetTool := &forgetTool{memoryStore: memoryStore}

			// Setup: add a memory first
			memory := &Memory{
				Key:       "user_preference",
				Value:     "User prefers dark mode",
				Metadata:  make(map[string]string),
				Timestamp: time.Now(),
			}
			err := memoryStore.Add(ctx, memory)
			So(err, ShouldBeNil)

			// Test tool info
			info, err := forgetTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "forget")

			// Test successful deletion
			args := `{"key": "user_preference"}`
			result, err := forgetTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully removed memory")

			// Verify deletion
			memory, err = memoryStore.Get(ctx, "user_preference")
			So(err, ShouldBeNil)
			So(memory, ShouldBeNil)

			// Test delete non-existent key
			argsNotFound := `{"key": "non_existent"}`
			_, err = forgetTool.InvokableRun(ctx, argsNotFound)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to delete memory")
		})

		// Test list_memories tool
		Convey("Test list_memories tool", func() {
			memoryStore := newMockMemoryStore()
			listTool := &listMemoriesTool{memoryStore: memoryStore}

			// Setup: add some memories first
			memory1 := &Memory{
				Key:       "project_decision",
				Value:     "Use Python 3.11",
				Metadata:  make(map[string]string),
				Timestamp: time.Now(),
			}
			err := memoryStore.Add(ctx, memory1)
			So(err, ShouldBeNil)

			memory2 := &Memory{
				Key:       "user_preference",
				Value:     "Dark mode",
				Metadata:  make(map[string]string),
				Timestamp: time.Now(),
			}
			err = memoryStore.Add(ctx, memory2)
			So(err, ShouldBeNil)

			// Test tool info
			info, err := listTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "list_memories")

			// Test listing memories
			args := `{}`
			result, err := listTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "project_decision")
			So(result, ShouldContainSubstring, "user_preference")

			// Clear all memories and test empty list
			memoryStore.memories = make(map[string]*Memory)
			result, err = listTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldEqual, "No memories stored yet.")
		})
	})
}

func TestMemoryToolsCreation(t *testing.T) {
	PatchConvey("TestMemoryToolsCreation", t, func() {
		memoryStore := newMockMemoryStore()
		tools := NewMemoryTools(memoryStore)

		// Should return 5 tools: remember, recall, update_memory, forget, list_memories
		So(len(tools), ShouldEqual, 5)

		ctx := context.Background()

		// Verify each tool's info
		toolNames := make([]string, 0)
		for _, tool := range tools {
			info, err := tool.Info(ctx)
			So(err, ShouldBeNil)
			So(info, ShouldNotBeNil)
			toolNames = append(toolNames, info.Name)
		}

		So(toolNames, ShouldContain, "remember")
		So(toolNames, ShouldContain, "recall")
		So(toolNames, ShouldContain, "update_memory")
		So(toolNames, ShouldContain, "forget")
		So(toolNames, ShouldContain, "list_memories")

		// Test with nil memory store
		toolsNil := NewMemoryTools(nil)
		So(len(toolsNil), ShouldEqual, 0)
	})
}

