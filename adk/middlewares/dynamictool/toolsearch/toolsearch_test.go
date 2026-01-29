/*
 * Copyright 2026 CloudWeGo Authors
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

package toolsearch

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
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

func newMockTool(name, desc string) *mockTool {
	return &mockTool{name: name, desc: desc}
}

func TestNew(t *testing.T) {
	ctx := context.Background()

	t.Run("nil config returns error", func(t *testing.T) {
		m, err := New(ctx, nil)
		assert.Nil(t, m)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("empty tools returns error", func(t *testing.T) {
		m, err := New(ctx, &Config{DynamicTools: []tool.BaseTool{}})
		assert.Nil(t, m)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tools is required")
	})

	t.Run("valid config returns middleware", func(t *testing.T) {
		tools := []tool.BaseTool{
			newMockTool("tool1", "desc1"),
			newMockTool("tool2", "desc2"),
		}
		m, err := New(ctx, &Config{DynamicTools: tools})
		assert.NoError(t, err)
		assert.NotNil(t, m)
	})
}

func TestMiddleware_BeforeAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("nil runCtx returns nil", func(t *testing.T) {
		tools := []tool.BaseTool{newMockTool("tool1", "desc1")}
		m, err := New(ctx, &Config{DynamicTools: tools})
		require.NoError(t, err)

		newCtx, newRunCtx, err := m.BeforeAgent(ctx, nil)
		assert.NoError(t, err)
		assert.Equal(t, ctx, newCtx)
		assert.Nil(t, newRunCtx)
	})

	t.Run("adds tool_search and dynamic tools", func(t *testing.T) {
		tools := []tool.BaseTool{
			newMockTool("tool1", "desc1"),
			newMockTool("tool2", "desc2"),
		}
		m, err := New(ctx, &Config{DynamicTools: tools})
		require.NoError(t, err)

		middleware := m.(*middleware)
		runCtx := &adk.ChatModelAgentContext{
			Tools: []tool.BaseTool{},
		}

		_, newRunCtx, err := middleware.BeforeAgent(ctx, runCtx)
		assert.NoError(t, err)
		assert.NotNil(t, newRunCtx)
		assert.Len(t, newRunCtx.Tools, 3)
	})
}

func TestToolSearchTool_Info(t *testing.T) {
	ctx := context.Background()
	toolNames := []string{"tool1", "tool2", "tool3"}
	tst := newToolSearchTool(toolNames)

	info, err := tst.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "tool_search", info.Name)
	assert.Contains(t, info.Desc, "regex pattern")
	assert.NotNil(t, info.ParamsOneOf)
}

func TestToolSearchTool_InvokableRun(t *testing.T) {
	ctx := context.Background()
	toolNames := []string{"get_weather", "get_time", "search_web", "calculate_sum"}
	tst := newToolSearchTool(toolNames)

	t.Run("empty regex pattern returns error", func(t *testing.T) {
		args := `{"regex_pattern": ""}`
		result, err := tst.InvokableRun(ctx, args)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "regex_pattern is required")
		assert.Empty(t, result)
	})

	t.Run("invalid json returns error", func(t *testing.T) {
		args := `{invalid json}`
		result, err := tst.InvokableRun(ctx, args)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal")
		assert.Empty(t, result)
	})

	t.Run("invalid regex returns error", func(t *testing.T) {
		args := `{"regex_pattern": "[invalid"}`
		result, err := tst.InvokableRun(ctx, args)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "invalid regex pattern")
		assert.Empty(t, result)
	})

	t.Run("matches tools with prefix pattern", func(t *testing.T) {
		args := `{"regex_pattern": "^get_"}`
		result, err := tst.InvokableRun(ctx, args)
		assert.NoError(t, err)

		var res toolSearchResult
		err = json.Unmarshal([]byte(result), &res)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"get_weather", "get_time"}, res.SelectedTools)
	})

	t.Run("matches tools with suffix pattern", func(t *testing.T) {
		args := `{"regex_pattern": "_sum$"}`
		result, err := tst.InvokableRun(ctx, args)
		assert.NoError(t, err)

		var res toolSearchResult
		err = json.Unmarshal([]byte(result), &res)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"calculate_sum"}, res.SelectedTools)
	})

	t.Run("matches all tools with wildcard", func(t *testing.T) {
		args := `{"regex_pattern": ".*"}`
		result, err := tst.InvokableRun(ctx, args)
		assert.NoError(t, err)

		var res toolSearchResult
		err = json.Unmarshal([]byte(result), &res)
		assert.NoError(t, err)
		assert.ElementsMatch(t, toolNames, res.SelectedTools)
	})

	t.Run("no matches returns empty list", func(t *testing.T) {
		args := `{"regex_pattern": "^nonexistent_"}`
		result, err := tst.InvokableRun(ctx, args)
		assert.NoError(t, err)

		var res toolSearchResult
		err = json.Unmarshal([]byte(result), &res)
		assert.NoError(t, err)
		assert.Empty(t, res.SelectedTools)
	})
}

func TestGetToolNames(t *testing.T) {
	ctx := context.Background()

	t.Run("returns tool names", func(t *testing.T) {
		tools := []tool.BaseTool{
			newMockTool("tool1", "desc1"),
			newMockTool("tool2", "desc2"),
			newMockTool("tool3", "desc3"),
		}
		names, err := getToolNames(ctx, tools)
		assert.NoError(t, err)
		assert.Equal(t, []string{"tool1", "tool2", "tool3"}, names)
	})

	t.Run("empty tools returns empty slice", func(t *testing.T) {
		names, err := getToolNames(ctx, []tool.BaseTool{})
		assert.NoError(t, err)
		assert.Empty(t, names)
	})
}

func TestExtractSelectedTools(t *testing.T) {
	ctx := context.Background()

	t.Run("extracts selected tools from messages", func(t *testing.T) {
		result := toolSearchResult{SelectedTools: []string{"tool1", "tool2"}}
		resultJSON, _ := json.Marshal(result)

		messages := []*schema.Message{
			schema.UserMessage("hello"),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		}

		selected, err := extractSelectedTools(ctx, messages)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"tool1", "tool2"}, selected)
	})

	t.Run("handles multiple tool_search results", func(t *testing.T) {
		result1 := toolSearchResult{SelectedTools: []string{"tool1"}}
		result1JSON, _ := json.Marshal(result1)
		result2 := toolSearchResult{SelectedTools: []string{"tool2", "tool3"}}
		result2JSON, _ := json.Marshal(result2)

		messages := []*schema.Message{
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(result1JSON)},
			schema.UserMessage("continue"),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(result2JSON)},
		}

		selected, err := extractSelectedTools(ctx, messages)
		assert.NoError(t, err)
		assert.ElementsMatch(t, []string{"tool1", "tool2", "tool3"}, selected)
	})

	t.Run("ignores non-tool_search messages", func(t *testing.T) {
		messages := []*schema.Message{
			schema.UserMessage("hello"),
			{Role: schema.Tool, ToolName: "other_tool", Content: "some content"},
			{Role: schema.Assistant, Content: "response"},
		}

		selected, err := extractSelectedTools(ctx, messages)
		assert.NoError(t, err)
		assert.Empty(t, selected)
	})

	t.Run("returns error for invalid json", func(t *testing.T) {
		messages := []*schema.Message{
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: "invalid json"},
		}

		selected, err := extractSelectedTools(ctx, messages)
		assert.Error(t, err)
		assert.Nil(t, selected)
	})
}

func TestInvertSelect(t *testing.T) {
	t.Run("returns items not in selected", func(t *testing.T) {
		all := []string{"a", "b", "c", "d"}
		selected := []string{"b", "d"}

		result := invertSelect(all, selected)
		assert.Len(t, result, 2)
		_, hasA := result["a"]
		_, hasC := result["c"]
		assert.True(t, hasA)
		assert.True(t, hasC)
	})

	t.Run("empty selected returns all", func(t *testing.T) {
		all := []string{"a", "b", "c"}
		selected := []string{}

		result := invertSelect(all, selected)
		assert.Len(t, result, 3)
	})

	t.Run("all selected returns empty", func(t *testing.T) {
		all := []string{"a", "b"}
		selected := []string{"a", "b"}

		result := invertSelect(all, selected)
		assert.Empty(t, result)
	})

	t.Run("works with integers", func(t *testing.T) {
		all := []int{1, 2, 3, 4, 5}
		selected := []int{2, 4}

		result := invertSelect(all, selected)
		assert.Len(t, result, 3)
		_, has1 := result[1]
		_, has3 := result[3]
		_, has5 := result[5]
		assert.True(t, has1)
		assert.True(t, has3)
		assert.True(t, has5)
	})
}

func TestRemoveTools(t *testing.T) {
	ctx := context.Background()

	t.Run("removes unselected dynamic tools", func(t *testing.T) {
		allTools := []*schema.ToolInfo{
			{Name: "static_tool"},
			{Name: "dynamic_tool1"},
			{Name: "dynamic_tool2"},
			{Name: "dynamic_tool3"},
		}

		dynamicTools := []tool.BaseTool{
			newMockTool("dynamic_tool1", ""),
			newMockTool("dynamic_tool2", ""),
			newMockTool("dynamic_tool3", ""),
		}

		result := toolSearchResult{SelectedTools: []string{"dynamic_tool1"}}
		resultJSON, _ := json.Marshal(result)
		messages := []*schema.Message{
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		}

		tools, err := removeTools(ctx, allTools, dynamicTools, messages)
		assert.NoError(t, err)
		assert.Len(t, tools, 2)

		toolNames := make([]string, len(tools))
		for i, t := range tools {
			toolNames[i] = t.Name
		}
		assert.ElementsMatch(t, []string{"static_tool", "dynamic_tool1"}, toolNames)
	})

	t.Run("remove all dynamic tools when no tool_search result", func(t *testing.T) {
		allTools := []*schema.ToolInfo{
			{Name: "static_tool"},
			{Name: "dynamic_tool1"},
		}

		dynamicTools := []tool.BaseTool{
			newMockTool("dynamic_tool1", ""),
		}

		messages := []*schema.Message{
			schema.UserMessage("hello"),
		}

		tools, err := removeTools(ctx, allTools, dynamicTools, messages)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, "static_tool", tools[0].Name)
	})

	t.Run("handles empty dynamic tools", func(t *testing.T) {
		allTools := []*schema.ToolInfo{
			{Name: "static_tool1"},
			{Name: "static_tool2"},
		}

		dynamicTools := []tool.BaseTool{}
		messages := []*schema.Message{}

		tools, err := removeTools(ctx, allTools, dynamicTools, messages)
		assert.NoError(t, err)
		assert.Len(t, tools, 2)
	})
}

type mockChatModel struct {
	generateFunc func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error)
	streamFunc   func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error)
}

func (m *mockChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, input, opts...)
	}
	return &schema.Message{Role: schema.Assistant, Content: "response"}, nil
}

func (m *mockChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.streamFunc != nil {
		return m.streamFunc(ctx, input, opts...)
	}
	return nil, nil
}

func TestWrapper_Generate(t *testing.T) {
	ctx := context.Background()

	t.Run("filters tools based on tool_search result", func(t *testing.T) {
		allTools := []*schema.ToolInfo{
			{Name: "static_tool"},
			{Name: "dynamic_tool1"},
			{Name: "dynamic_tool2"},
		}

		dynamicTools := []tool.BaseTool{
			newMockTool("dynamic_tool1", ""),
			newMockTool("dynamic_tool2", ""),
		}

		result := toolSearchResult{SelectedTools: []string{"dynamic_tool1"}}
		resultJSON, _ := json.Marshal(result)

		messages := []*schema.Message{
			schema.UserMessage("hello"),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		}

		w := &wrapper{
			allTools:     allTools,
			dynamicTools: dynamicTools,
			cm: &mockChatModel{
				generateFunc: func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
					options := model.GetCommonOptions(nil, opts...)
					assert.Len(t, options.Tools, 2)
					assert.Equal(t, "static_tool", options.Tools[0].Name)
					assert.Equal(t, "dynamic_tool1", options.Tools[1].Name)
					return nil, nil
				},
			},
		}

		_, err := w.Generate(ctx, messages)
		assert.NoError(t, err)
	})
}

func TestWrapper_Stream(t *testing.T) {
	ctx := context.Background()

	t.Run("filters tools based on tool_search result", func(t *testing.T) {
		allTools := []*schema.ToolInfo{
			{Name: "static_tool"},
			{Name: "dynamic_tool1"},
			{Name: "dynamic_tool2"},
		}

		dynamicTools := []tool.BaseTool{
			newMockTool("dynamic_tool1", ""),
			newMockTool("dynamic_tool2", ""),
		}

		result := toolSearchResult{SelectedTools: []string{"dynamic_tool1"}}
		resultJSON, _ := json.Marshal(result)

		messages := []*schema.Message{
			schema.UserMessage("hello"),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		}

		w := &wrapper{
			allTools:     allTools,
			dynamicTools: dynamicTools,
			cm: &mockChatModel{
				streamFunc: func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
					options := model.GetCommonOptions(nil, opts...)
					assert.Len(t, options.Tools, 2)
					assert.Equal(t, "static_tool", options.Tools[0].Name)
					assert.Equal(t, "dynamic_tool1", options.Tools[1].Name)
					return nil, nil
				},
			},
		}

		stream, err := w.Stream(ctx, messages)
		assert.NoError(t, err)
		assert.Nil(t, stream)
	})
}
