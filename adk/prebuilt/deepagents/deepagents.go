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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
)

// Config provides configuration options for creating a DeepAgents agent.
//
// DeepAgents is an ADK-based agent that provides enhanced capabilities for
// planning, storage management, sub-agent generation, and long-term memory.
//
// Memory Architecture:
//   - Session: Used internally for current execution context (todos, intermediate results)
//   - MemoryStore: Provides long-term persistent memory across sessions (based on CheckPoint)
//
// The agent can be used standalone or integrated into larger workflows (e.g., as a node in compose.StateGraph).
type Config struct {
	// Model is the chat model used by the main agent.
	// Required.
	Model model.ToolCallingChatModel

	// Storage is the storage interface implementation for file operations.
	// Required. Users should provide an implementation (e.g., from eino-ext).
	Storage Storage

	// MemoryStore is the memory store for long-term persistence.
	// Optional. If provided, enables long-term memory tools (remember, recall, etc.).
	// Concrete implementations should be in eino-ext (e.g., CheckPointMemoryStore).
	MemoryStore MemoryStore

	// ToolsConfig specifies additional tools available to the agent.
	// Optional.
	ToolsConfig adk.ToolsConfig

	// MaxIterations defines the upper limit of ChatModel generation cycles.
	// Optional. Defaults to 20.
	MaxIterations int

	// Instruction is the system prompt for the agent.
	// Optional.
	Instruction string
}

// New creates a new DeepAgents agent with the given configuration.
//
// DeepAgents provides the following capabilities:
// 1. Planning and task decomposition: write_todos tool
// 2. Storage management: ls, read_file, write_file, edit_file tools
// 3. Sub-agent generation: task tool
// 4. Long-term memory: remember, recall, update_memory, forget, list_memories tools (if MemoryStore provided)
//
// The agent uses Session (adk.Session) for managing current execution context (todos, etc.).
// For long-term persistence across sessions, MemoryStore is used (backed by CheckPoint).
//
// The agent uses the provided Storage interface for all file operations,
// allowing for flexible storage backends (in-memory, file system, S3, etc.).
//
// The returned agent can be used standalone or integrated into a StateGraph as a node.
func New(ctx context.Context, cfg *Config) (adk.Agent, error) {
	if cfg == nil {
		return nil, errors.New("config is required")
	}

	if cfg.Model == nil {
		return nil, errors.New("model is required")
	}

	if cfg.Storage == nil {
		return nil, errors.New("storage is required")
	}

	// Build the tools list
	tools := make([]tool.BaseTool, 0)

	// 1. Add write_todos tool for planning
	writeTodosTool := NewWriteTodosTool()
	tools = append(tools, writeTodosTool)

	// 2. Add storage tools
	storageTools := NewStorageTools(cfg.Storage)
	tools = append(tools, storageTools...)

	// 3. Add memory tools (if MemoryStore is provided)
	if cfg.MemoryStore != nil {
		memoryTools := NewMemoryTools(cfg.MemoryStore)
		tools = append(tools, memoryTools...)
	}

	// 4. Add task tool for sub-agent creation
	taskTool := NewTaskTool(cfg.Model)
	tools = append(tools, taskTool)

	// 5. Add any additional tools from ToolsConfig
	if cfg.ToolsConfig.Tools != nil {
		tools = append(tools, cfg.ToolsConfig.Tools...)
	}

	if cfg.Instruction == "" {
		cfg.Instruction = defaultInstruction
	}

	// Create the main agent
	toolsConfig := adk.ToolsConfig{
		ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools: tools,
		},
		ReturnDirectly: cfg.ToolsConfig.ReturnDirectly,
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          "DeepAgents",
		Description:   "A DeepAgents agent with planning, storage management, sub-agent generation, and long-term memory capabilities",
		Instruction:   cfg.Instruction,
		Model:         cfg.Model,
		ToolsConfig:   toolsConfig,
		MaxIterations: cfg.MaxIterations,
	})
	if err != nil {
		return nil, err
	}

	return agent, nil
}
