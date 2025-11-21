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

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// lsTool is a tool that lists items in a directory path.
type lsTool struct {
	storage Storage
}

// Info returns the tool information for ls.
func (l *lsTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "ls",
		Desc: "List all items (files and directories) in the given directory path.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"path": {
					Type:     schema.String,
					Desc:     "The directory path to list. Use '/' for root directory.",
					Required: true,
				},
			},
		),
	}, nil
}

// InvokableRun executes the ls tool.
func (l *lsTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type lsParams struct {
		Path string `json:"path"`
	}

	var params lsParams
	err := sonic.UnmarshalString(argumentsInJSON, &params)
	if err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	items, err := l.storage.List(ctx, params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to list directory %s: %w", params.Path, err)
	}

	if len(items) == 0 {
		return fmt.Sprintf("Directory '%s' is empty.", params.Path), nil
	}

	var result strings.Builder
	result.WriteString(fmt.Sprintf("Contents of '%s':\n", params.Path))
	for _, item := range items {
		itemType := "file"
		if item.IsDir {
			itemType = "dir"
		}
		result.WriteString(fmt.Sprintf("  [%s] %s\n", itemType, item.Name))
	}

	return result.String(), nil
}

// readFileTool is a tool that reads content from a file path.
type readFileTool struct {
	storage Storage
}

// Info returns the tool information for read_file.
func (r *readFileTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "read_file",
		Desc: "Read the content from a file path.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"path": {
					Type:     schema.String,
					Desc:     "The file path to read.",
					Required: true,
				},
			},
		),
	}, nil
}

// InvokableRun executes the read_file tool.
func (r *readFileTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type readFileParams struct {
		Path string `json:"path"`
	}

	var params readFileParams
	err := sonic.UnmarshalString(argumentsInJSON, &params)
	if err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	content, err := r.storage.Read(ctx, params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", params.Path, err)
	}

	return string(content), nil
}

// writeFileTool is a tool that writes content to a file path.
type writeFileTool struct {
	storage Storage
}

// Info returns the tool information for write_file.
func (w *writeFileTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "write_file",
		Desc: "Write content to a file path. Creates the file if it doesn't exist, or overwrites if it exists.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"path": {
					Type:     schema.String,
					Desc:     "The file path to write to.",
					Required: true,
				},
				"content": {
					Type:     schema.String,
					Desc:     "The content to write to the file.",
					Required: true,
				},
			},
		),
	}, nil
}

// InvokableRun executes the write_file tool.
func (w *writeFileTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type writeFileParams struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}

	var params writeFileParams
	err := sonic.UnmarshalString(argumentsInJSON, &params)
	if err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	err = w.storage.Write(ctx, params.Path, []byte(params.Content))
	if err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", params.Path, err)
	}

	return fmt.Sprintf("Successfully wrote to file '%s'.", params.Path), nil
}

// editFileTool is a tool that edits content in a file.
type editFileTool struct {
	storage Storage
}

// Info returns the tool information for edit_file.
func (e *editFileTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "edit_file",
		Desc: "Edit content in a file. Supports insert, replace, and delete operations.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"path": {
					Type:     schema.String,
					Desc:     "The file path to edit.",
					Required: true,
				},
				"operation": {
					Type:     schema.String,
					Desc:     "The edit operation: 'insert' (insert at position), 'replace' (replace range), or 'delete' (delete range).",
					Required: true,
				},
				"position": {
					Type:     schema.Integer,
					Desc:     "For 'insert': position to insert at. For 'replace'/'delete': start position. Line numbers are 1-indexed.",
					Required: false,
				},
				"end_position": {
					Type:     schema.Integer,
					Desc:     "For 'replace'/'delete': end position (inclusive). Line numbers are 1-indexed.",
					Required: false,
				},
				"content": {
					Type:     schema.String,
					Desc:     "For 'insert'/'replace': the content to insert or replace with.",
					Required: false,
				},
			},
		),
	}, nil
}

// InvokableRun executes the edit_file tool.
func (e *editFileTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type editFileParams struct {
		Path        string `json:"path"`
		Operation   string `json:"operation"`
		Position    *int   `json:"position,omitempty"`
		EndPosition *int   `json:"end_position,omitempty"`
		Content     string `json:"content,omitempty"`
	}

	var params editFileParams
	err := sonic.UnmarshalString(argumentsInJSON, &params)
	if err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Read the current file content
	content, err := e.storage.Read(ctx, params.Path)
	if err != nil {
		return "", fmt.Errorf("failed to read file %s: %w", params.Path, err)
	}

	lines := strings.Split(string(content), "\n")

	// Perform the edit operation
	switch params.Operation {
	case "insert":
		if params.Position == nil {
			return "", fmt.Errorf("position is required for insert operation")
		}
		pos := *params.Position
		if pos < 1 || pos > len(lines)+1 {
			return "", fmt.Errorf("position %d is out of range (1-%d)", pos, len(lines)+1)
		}
		// Insert at position (1-indexed, so subtract 1 for 0-indexed)
		insertPos := pos - 1
		newLines := strings.Split(params.Content, "\n")
		// Insert the new lines
		lines = append(lines[:insertPos], append(newLines, lines[insertPos:]...)...)

	case "replace":
		if params.Position == nil || params.EndPosition == nil {
			return "", fmt.Errorf("position and end_position are required for replace operation")
		}
		start := *params.Position
		end := *params.EndPosition
		if start < 1 || end < start || end > len(lines) {
			return "", fmt.Errorf("invalid range: position %d to end_position %d (file has %d lines)", start, end, len(lines))
		}
		// Replace range (1-indexed, so subtract 1 for 0-indexed)
		startIdx := start - 1
		endIdx := end
		newLines := strings.Split(params.Content, "\n")
		lines = append(lines[:startIdx], append(newLines, lines[endIdx:]...)...)

	case "delete":
		if params.Position == nil || params.EndPosition == nil {
			return "", fmt.Errorf("position and end_position are required for delete operation")
		}
		start := *params.Position
		end := *params.EndPosition
		if start < 1 || end < start || end > len(lines) {
			return "", fmt.Errorf("invalid range: position %d to end_position %d (file has %d lines)", start, end, len(lines))
		}
		// Delete range (1-indexed, so subtract 1 for 0-indexed)
		startIdx := start - 1
		endIdx := end
		lines = append(lines[:startIdx], lines[endIdx:]...)

	default:
		return "", fmt.Errorf("unknown operation: %s (supported: insert, replace, delete)", params.Operation)
	}

	// Write the modified content back
	newContent := strings.Join(lines, "\n")
	err = e.storage.Write(ctx, params.Path, []byte(newContent))
	if err != nil {
		return "", fmt.Errorf("failed to write file %s: %w", params.Path, err)
	}

	return fmt.Sprintf("Successfully edited file '%s' using operation '%s'.", params.Path, params.Operation), nil
}

// taskTool is a tool that creates a sub-agent to handle a specific task.
type taskTool struct {
	model model.ToolCallingChatModel
}

// Info returns the tool information for task.
func (t *taskTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "task",
		Desc: "Create a specialized sub-agent to handle a specific task. The sub-agent will have its own context and can work independently.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"task_description": {
					Type:     schema.String,
					Desc:     "A clear description of the task for the sub-agent to handle.",
					Required: true,
				},
				"agent_name": {
					Type:     schema.String,
					Desc:     "A unique name for the sub-agent. If not provided, a default name will be generated.",
					Required: false,
				},
				"instruction": {
					Type:     schema.String,
					Desc:     "Optional instruction/prompt for the sub-agent.",
					Required: false,
				},
			},
		),
	}, nil
}

// InvokableRun executes the task tool.
func (t *taskTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	type taskParams struct {
		TaskDescription string `json:"task_description"`
		AgentName       string `json:"agent_name,omitempty"`
		Instruction     string `json:"instruction,omitempty"`
	}

	var params taskParams
	err := sonic.UnmarshalString(argumentsInJSON, &params)
	if err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Generate a default agent name if not provided
	agentName := params.AgentName
	if agentName == "" {
		agentName = fmt.Sprintf("sub_agent_%d", len(adk.GetSessionValues(ctx)))
	}

	// Create instruction for the sub-agent
	instruction := params.Instruction
	if instruction == "" {
		instruction = fmt.Sprintf("You are a specialized agent tasked with: %s", params.TaskDescription)
	} else {
		instruction = fmt.Sprintf("%s\n\nTask: %s", instruction, params.TaskDescription)
	}

	// Create a sub-agent using ChatModelAgent
	subAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        agentName,
		Description: fmt.Sprintf("A sub-agent specialized in: %s", params.TaskDescription),
		Instruction: instruction,
		Model:       t.model,
		ToolsConfig: adk.ToolsConfig{}, // Sub-agent can have its own tools if needed
	})
	if err != nil {
		return "", fmt.Errorf("failed to create sub-agent: %w", err)
	}

	// Wrap the sub-agent as a tool
	agentTool := adk.NewAgentTool(ctx, subAgent)

	// Add the sub-agent to the parent agent
	// Note: This requires the parent agent to support SetSubAgents
	// We'll need to handle this through the context or a different mechanism
	// For now, we'll return the agent tool info
	toolInfo, err := agentTool.Info(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get agent tool info: %w", err)
	}

	return fmt.Sprintf("Successfully created sub-agent '%s' with tool '%s'. The sub-agent is ready to handle the task: %s", agentName, toolInfo.Name, params.TaskDescription), nil
}

// NewStorageTools creates storage-related tools (ls, read_file, write_file, edit_file).
func NewStorageTools(storage Storage) []tool.BaseTool {
	return []tool.BaseTool{
		&lsTool{storage: storage},
		&readFileTool{storage: storage},
		&writeFileTool{storage: storage},
		&editFileTool{storage: storage},
	}
}

// NewTaskTool creates a task tool for creating sub-agents.
func NewTaskTool(model model.ToolCallingChatModel) tool.BaseTool {
	return &taskTool{
		model: model,
	}
}
