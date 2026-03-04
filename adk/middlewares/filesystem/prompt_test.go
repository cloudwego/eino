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

package filesystem

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type mockTool struct {
	name string
	desc string
}

func (m *mockTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: m.name,
		Desc: m.desc,
	}, nil
}

func TestGenerateToolsPrompt(t *testing.T) {
	t.Run("empty tools list", func(t *testing.T) {
		result := generateToolsPrompt(nil)
		assert.Empty(t, result)
	})

	t.Run("single file tool", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, ToolNameLs)
		assert.Contains(t, result, "Filesystem Tools")
		assert.Contains(t, result, "list files in a directory")
	})

	t.Run("multiple file tools", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
			&mockTool{name: ToolNameReadFile, desc: "read file"},
			&mockTool{name: ToolNameWriteFile, desc: "write file"},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, ToolNameLs)
		assert.Contains(t, result, ToolNameReadFile)
		assert.Contains(t, result, ToolNameWriteFile)
		assert.Contains(t, result, "Filesystem Tools")
	})

	t.Run("single execute tool", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameExecute, desc: "execute command"},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, ToolNameExecute)
		assert.Contains(t, result, "Execute Tool")
		assert.Contains(t, result, "sandboxed environment")
	})

	t.Run("mixed file and execute tools", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
			&mockTool{name: ToolNameReadFile, desc: "read file"},
			&mockTool{name: ToolNameExecute, desc: "execute command"},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, ToolNameLs)
		assert.Contains(t, result, ToolNameReadFile)
		assert.Contains(t, result, ToolNameExecute)
		assert.Contains(t, result, "Filesystem Tools")
		assert.Contains(t, result, "Execute Tool")
	})

	t.Run("custom tool names", func(t *testing.T) {
		customLsName := "my_list"
		customExecuteName := "my_execute"

		tools := []tool.BaseTool{
			&mockTool{name: customLsName, desc: "custom list"},
			&mockTool{name: customExecuteName, desc: "custom execute"},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, customLsName)
		assert.Contains(t, result, customExecuteName)
		assert.Contains(t, result, "Filesystem Tools")
	})

	t.Run("tool with error should be skipped", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
			&errorTool{},
			&mockTool{name: ToolNameReadFile, desc: "read file"},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, ToolNameLs)
		assert.Contains(t, result, ToolNameReadFile)
	})

	t.Run("custom tool without description uses fallback", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: "custom_tool", desc: ""},
		}

		result := generateToolsPrompt(tools)
		require.NotEmpty(t, result)

		assert.Contains(t, result, "custom_tool")
		assert.Contains(t, result, "filesystem tool, refer to")
	})
}

func TestGenerateToolsPromptFormat(t *testing.T) {
	t.Run("prompt format with filesystem tools", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
			&mockTool{name: ToolNameReadFile, desc: "read file"},
		}

		prompt := generateToolsPrompt(tools)

		assert.Contains(t, prompt, "# Filesystem Tools")
		assert.Contains(t, prompt, "'"+ToolNameLs+"'")
		assert.Contains(t, prompt, "'"+ToolNameReadFile+"'")
		assert.Contains(t, prompt, "You have access to a filesystem")
		assert.Contains(t, prompt, "All file paths must start with a '/'")

		assert.Contains(t, prompt, "- "+ToolNameLs+":")
		assert.Contains(t, prompt, "- "+ToolNameReadFile+":")
	})

	t.Run("execute tool prompt", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameExecute, desc: "execute command"},
		}

		prompt := generateToolsPrompt(tools)

		assert.Contains(t, prompt, "# Execute Tool")
		assert.Contains(t, prompt, ToolNameExecute)
		assert.Contains(t, prompt, "sandboxed environment")
		assert.Contains(t, prompt, "- "+ToolNameExecute+":")
	})

	t.Run("combined file and execute tools", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
			&mockTool{name: ToolNameExecute, desc: "execute command"},
		}

		prompt := generateToolsPrompt(tools)

		assert.Contains(t, prompt, "# Filesystem Tools")
		assert.Contains(t, prompt, "# Execute Tool")
	})

	t.Run("tool descriptions from getToolDescription", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "original desc"},
		}

		prompt := generateToolsPrompt(tools)

		expectedDesc := getToolDescription(ToolNameLs)
		assert.Contains(t, prompt, expectedDesc)
	})

	t.Run("skip tool with info error", func(t *testing.T) {
		tools := []tool.BaseTool{
			&mockTool{name: ToolNameLs, desc: "list files"},
			&errorTool{},
			&mockTool{name: ToolNameReadFile, desc: "read file"},
		}

		prompt := generateToolsPrompt(tools)

		assert.Contains(t, prompt, ToolNameLs)
		assert.Contains(t, prompt, ToolNameReadFile)
		assert.NotContains(t, prompt, "error_tool")
	})
}

func TestGetToolDescription(t *testing.T) {
	t.Run("known tool ls", func(t *testing.T) {
		desc := getToolDescription(ToolNameLs)
		assert.NotEmpty(t, desc)
		assert.Contains(t, desc, "list")
	})

	t.Run("execute tool", func(t *testing.T) {
		desc := getToolDescription(ToolNameExecute)
		assert.NotEmpty(t, desc)
		assert.Contains(t, desc, "shell")
	})

	t.Run("unknown tool returns fallback", func(t *testing.T) {
		desc := getToolDescription("unknown_tool")
		assert.Equal(t, "filesystem tool, refer to the tool's description for details", desc)
	})

	t.Run("all default tools have descriptions", func(t *testing.T) {
		toolNames := []string{
			ToolNameLs,
			ToolNameReadFile,
			ToolNameWriteFile,
			ToolNameEditFile,
			ToolNameGlob,
			ToolNameGrep,
			ToolNameExecute,
		}

		for _, name := range toolNames {
			desc := getToolDescription(name)
			assert.NotEmpty(t, desc, "tool %s should have description", name)
			assert.NotContains(t, desc, "refer to the tool's description for details",
				"default tool %s should not use fallback description", name)
		}
	})
}

type errorTool struct{}

func (e *errorTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return nil, assert.AnError
}
