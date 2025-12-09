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
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Memory represents a single long-term memory entry.
// Memories are persisted across sessions using CheckPoint mechanism.
type Memory struct {
	// Key is the unique identifier for this memory
	Key string `json:"key"`

	// Value is the actual content of the memory
	// It can be any serializable type (string, struct, etc.)
	Value interface{} `json:"value"`

	// Metadata provides additional information about the memory
	// Examples: tags, categories, importance score, etc.
	Metadata map[string]string `json:"metadata"`

	// Timestamp records when this memory was created or last updated
	Timestamp time.Time `json:"timestamp"`
}

// MemoryStore is the interface for long-term memory storage.
//
// MemoryStore provides persistent storage across agent sessions using CheckPoint.
// It stores knowledge, experiences, and user preferences that should be retained
// beyond a single agent execution.
//
// Implementation Strategy:
// Concrete implementations should be provided in eino-ext, such as:
//   - CheckPointMemoryStore: Uses compose.CheckPointStore for persistence
//   - VectorMemoryStore: Uses vector databases for semantic search
//   - SQLMemoryStore: Uses relational databases for structured queries
//   - RedisMemoryStore: Uses Redis for fast key-value access
type MemoryStore interface {
	// Add creates a new memory entry.
	// Returns an error if a memory with the same key already exists.
	Add(ctx context.Context, memory *Memory) error

	// Get retrieves a memory by its key.
	// Returns nil if the memory doesn't exist.
	Get(ctx context.Context, key string) (*Memory, error)

	// Search finds memories matching the given query.
	// The query semantics depend on the implementation:
	//   - Simple implementations might match against metadata
	//   - Advanced implementations might use semantic/vector search
	// limit specifies the maximum number of results to return (0 = no limit)
	Search(ctx context.Context, query string, limit int) ([]*Memory, error)

	// Update modifies an existing memory.
	// Returns an error if the memory doesn't exist.
	Update(ctx context.Context, key string, memory *Memory) error

	// Delete removes a memory by its key.
	// Returns an error if the memory doesn't exist.
	Delete(ctx context.Context, key string) error

	// List returns all memory keys.
	// Useful for browsing available memories.
	List(ctx context.Context) ([]string, error)
}

// rememberTool stores information in long-term memory.
type rememberTool struct {
	memoryStore MemoryStore
}

// Info returns the tool information for remember.
func (r *rememberTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "remember",
		Desc: "Store important information in long-term memory for future sessions. Use this to save key insights, decisions, facts, or learnings that should persist beyond the current task.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"key": {
					Type:     schema.String,
					Desc:     "Unique identifier for this memory (e.g., 'user_preference_theme', 'project_architecture_decision')",
					Required: true,
				},
				"value": {
					Type:     schema.String,
					Desc:     "The information to store",
					Required: true,
				},
				"tags": {
					Type:     schema.String,
					Desc:     "Comma-separated tags for categorization (e.g., 'preference,ui' or 'decision,architecture')",
					Required: false,
				},
			},
		),
	}, nil
}

// InvokableRun executes the remember tool.
func (r *rememberTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type rememberParams struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Tags  string `json:"tags,omitempty"`
	}

	var params rememberParams
	if err := sonic.UnmarshalString(argumentsInJSON, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	metadata := make(map[string]string)
	if params.Tags != "" {
		metadata["tags"] = params.Tags
	}

	memory := &Memory{
		Key:       params.Key,
		Value:     params.Value,
		Metadata:  metadata,
		Timestamp: time.Now(),
	}

	if err := r.memoryStore.Add(ctx, memory); err != nil {
		return "", fmt.Errorf("failed to store memory: %w", err)
	}

	return fmt.Sprintf("Successfully stored memory with key '%s'. This information will be available in future sessions.", params.Key), nil
}

// recallTool retrieves information from long-term memory.
type recallTool struct {
	memoryStore MemoryStore
}

// Info returns the tool information for recall.
func (r *recallTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "recall",
		Desc: "Retrieve information from long-term memory. Use this to access previously stored knowledge, insights, or decisions.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"key": {
					Type:     schema.String,
					Desc:     "The unique identifier of the memory to retrieve",
					Required: true,
				},
			},
		),
	}, nil
}

// InvokableRun executes the recall tool.
func (r *recallTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type recallParams struct {
		Key string `json:"key"`
	}

	var params recallParams
	if err := sonic.UnmarshalString(argumentsInJSON, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	memory, err := r.memoryStore.Get(ctx, params.Key)
	if err != nil {
		return "", fmt.Errorf("failed to retrieve memory: %w", err)
	}

	if memory == nil {
		return fmt.Sprintf("No memory found with key '%s'.", params.Key), nil
	}

	valueJSON, err := sonic.MarshalString(memory.Value)
	if err != nil {
		// Fallback to string representation
		valueJSON = fmt.Sprintf("%v", memory.Value)
	}

	var tagsInfo string
	if tags, ok := memory.Metadata["tags"]; ok && tags != "" {
		tagsInfo = fmt.Sprintf("\nTags: %s", tags)
	}

	return fmt.Sprintf("Memory '%s':\n%s%s\n(Stored: %s)",
		memory.Key, valueJSON, tagsInfo, memory.Timestamp.Format(time.RFC3339)), nil
}

// updateMemoryTool modifies an existing memory.
type updateMemoryTool struct {
	memoryStore MemoryStore
}

// Info returns the tool information for update_memory.
func (u *updateMemoryTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "update_memory",
		Desc: "Update an existing memory with new information. Use this when you need to refine or correct previously stored knowledge.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"key": {
					Type:     schema.String,
					Desc:     "The unique identifier of the memory to update",
					Required: true,
				},
				"value": {
					Type:     schema.String,
					Desc:     "The new value for this memory",
					Required: true,
				},
				"tags": {
					Type:     schema.String,
					Desc:     "Updated comma-separated tags (optional, will replace existing tags if provided)",
					Required: false,
				},
			},
		),
	}, nil
}

// InvokableRun executes the update_memory tool.
func (u *updateMemoryTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type updateParams struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Tags  string `json:"tags,omitempty"`
	}

	var params updateParams
	if err := sonic.UnmarshalString(argumentsInJSON, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	metadata := make(map[string]string)
	if params.Tags != "" {
		metadata["tags"] = params.Tags
	}

	memory := &Memory{
		Key:       params.Key,
		Value:     params.Value,
		Metadata:  metadata,
		Timestamp: time.Now(),
	}

	if err := u.memoryStore.Update(ctx, params.Key, memory); err != nil {
		return "", fmt.Errorf("failed to update memory: %w", err)
	}

	return fmt.Sprintf("Successfully updated memory '%s'.", params.Key), nil
}

// forgetTool removes a memory from long-term storage.
type forgetTool struct {
	memoryStore MemoryStore
}

// Info returns the tool information for forget.
func (f *forgetTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "forget",
		Desc: "Remove a memory from long-term storage. Use this to clean up outdated, sensitive, or no longer relevant information.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"key": {
					Type:     schema.String,
					Desc:     "The unique identifier of the memory to remove",
					Required: true,
				},
			},
		),
	}, nil
}

// InvokableRun executes the forget tool.
func (f *forgetTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type forgetParams struct {
		Key string `json:"key"`
	}

	var params forgetParams
	if err := sonic.UnmarshalString(argumentsInJSON, &params); err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	if err := f.memoryStore.Delete(ctx, params.Key); err != nil {
		return "", fmt.Errorf("failed to delete memory: %w", err)
	}

	return fmt.Sprintf("Successfully removed memory '%s'.", params.Key), nil
}

// listMemoriesTool lists all available memory keys.
type listMemoriesTool struct {
	memoryStore MemoryStore
}

// Info returns the tool information for list_memories.
func (l *listMemoriesTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "list_memories",
		Desc: "List all available memory keys. Use this to browse what information has been stored in long-term memory.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{},
		),
	}, nil
}

// InvokableRun executes the list_memories tool.
func (l *listMemoriesTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	keys, err := l.memoryStore.List(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to list memories: %w", err)
	}

	if len(keys) == 0 {
		return "No memories stored yet.", nil
	}

	return fmt.Sprintf("Available memories (%d total):\n- %s", len(keys), strings.Join(keys, "\n- ")), nil
}

// NewMemoryTools creates all memory-related tools.
// Returns a slice of BaseTool that can be added to an agent's tool configuration.
//
// If memoryStore is nil, returns an empty slice (no memory tools available).
//
// Tools created:
//   - remember: Store new memories
//   - recall: Retrieve existing memories
//   - update_memory: Update existing memories
//   - forget: Delete memories
//   - list_memories: List all memory keys
func NewMemoryTools(memoryStore MemoryStore) []tool.BaseTool {
	if memoryStore == nil {
		return []tool.BaseTool{}
	}

	return []tool.BaseTool{
		&rememberTool{memoryStore: memoryStore},
		&recallTool{memoryStore: memoryStore},
		&updateMemoryTool{memoryStore: memoryStore},
		&forgetTool{memoryStore: memoryStore},
		&listMemoriesTool{memoryStore: memoryStore},
	}
}

