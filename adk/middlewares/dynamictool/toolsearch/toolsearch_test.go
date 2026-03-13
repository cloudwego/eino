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
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func makeToolMap(tools ...*schema.ToolInfo) map[string]*schema.ToolInfo {
	m := make(map[string]*schema.ToolInfo, len(tools))
	for _, t := range tools {
		m[t.Name] = t
	}
	return m
}

func ti(name, desc string) *schema.ToolInfo {
	return &schema.ToolInfo{Name: name, Desc: desc}
}

func toolNames(infos []*schema.ToolInfo) []string {
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
	}
	sort.Strings(names)
	return names
}

func searchJSON(query string, maxResults *int) string {
	args := toolSearchArgs{Query: query, MaxResults: maxResults}
	b, _ := json.Marshal(args)
	return string(b)
}

func intPtr(v int) *int { return &v }

// ---------------------------------------------------------------------------
// TestSearch — unit tests for the search() function
// ---------------------------------------------------------------------------

func TestSearch(t *testing.T) {
	tools := makeToolMap(
		ti("get_weather", "Get current weather for a city"),
		ti("search_flights", "Search available flights"),
		ti("mcp__slack__send_message", "Send a message to Slack channel"),
		ti("mcp__slack__read_channel", "Read messages from Slack channel"),
		ti("create_calendar_event", "Create a new calendar event"),
		ti("NotebookEdit", "Edit Jupyter notebook cells"),
	)

	tests := []struct {
		name      string
		json      string
		wantNames []string // sorted; nil means expect empty
		wantErr   bool
	}{
		{
			name:      "keyword exact name part match",
			json:      searchJSON("weather", nil),
			wantNames: []string{"get_weather"},
		},
		{
			name:      "keyword matches multiple tools",
			json:      searchJSON("slack", nil),
			wantNames: []string{"mcp__slack__read_channel", "mcp__slack__send_message"},
		},
		{
			name:      "multi-word ranking - send_message ranked first",
			json:      searchJSON("send message", nil),
			wantNames: []string{"mcp__slack__send_message"}, // check first element only
		},
		{
			name:      "required keyword filters to slack only",
			json:      searchJSON("+slack send", nil),
			wantNames: []string{"mcp__slack__read_channel", "mcp__slack__send_message"},
		},
		{
			name:      "required keyword no match",
			json:      searchJSON("+github send", nil),
			wantNames: nil,
		},
		{
			name:      "direct select single",
			json:      searchJSON("select:get_weather", nil),
			wantNames: []string{"get_weather"},
		},
		{
			name:      "direct select multiple",
			json:      searchJSON("select:get_weather,NotebookEdit", nil),
			wantNames: []string{"NotebookEdit", "get_weather"},
		},
		{
			name:      "direct select nonexistent",
			json:      searchJSON("select:nonexistent", nil),
			wantNames: nil,
		},
		{
			name:      "max_results limits output",
			json:      searchJSON("slack", intPtr(1)),
			wantNames: []string{"mcp__slack__read_channel"}, // just check length below
		},
		{
			name:      "camelCase split matches notebook",
			json:      searchJSON("notebook", nil),
			wantNames: []string{"NotebookEdit"},
		},
		{
			name:    "empty query returns error",
			json:    searchJSON("", nil),
			wantErr: true,
		},
		{
			name:      "description match - jupyter",
			json:      searchJSON("jupyter", nil),
			wantNames: []string{"NotebookEdit"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := search(tt.json, tools)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			require.NoError(t, err)

			// special case: max_results limit
			if tt.name == "max_results limits output" {
				assert.Len(t, got, 1)
				return
			}

			// special case: ranking — just check first element
			if tt.name == "multi-word ranking - send_message ranked first" {
				require.NotEmpty(t, got)
				assert.Equal(t, "mcp__slack__send_message", got[0].Name)
				return
			}

			gotNames := toolNames(got)
			if tt.wantNames == nil {
				assert.Empty(t, gotNames)
			} else {
				assert.Equal(t, tt.wantNames, gotNames)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// TestMiddlewareFlow — integration test for UseModelToolSearch=false
// ---------------------------------------------------------------------------

// simpleTool is a minimal InvokableTool for testing.
type simpleTool struct {
	name   string
	desc   string
	called bool
	mu     sync.Mutex
}

func (s *simpleTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: s.name,
		Desc: s.desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: schema.String, Desc: "input", Required: true},
		}),
	}, nil
}

func (s *simpleTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	s.mu.Lock()
	s.called = true
	s.mu.Unlock()
	return `{"result":"ok"}`, nil
}

func (s *simpleTool) wasCalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.called
}

// mockChatModel implements model.ToolCallingChatModel.
// It drives a 3-turn conversation:
//
//	Turn 1: call tool_search with select:dynamic_tool_a
//	Turn 2: call dynamic_tool_a
//	Turn 3: return final text
type mockChatModel struct {
	mu           sync.Mutex
	generateCall int
	// toolsPerCall records the tool names passed via model.WithTools for each Generate call.
	toolsPerCall [][]string
}

func (m *mockChatModel) Generate(_ context.Context, _ []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	options := model.GetCommonOptions(nil, opts...)
	var names []string
	for _, t := range options.Tools {
		names = append(names, t.Name)
	}
	sort.Strings(names)

	m.mu.Lock()
	m.generateCall++
	call := m.generateCall
	m.toolsPerCall = append(m.toolsPerCall, names)
	m.mu.Unlock()

	switch call {
	case 1:
		// Ask tool_search to select dynamic_tool_a
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "tc1",
				Function: schema.FunctionCall{
					Name:      toolSearchToolName,
					Arguments: `{"query":"select:dynamic_tool_a","max_results":5}`,
				},
			},
		}), nil
	case 2:
		// Call dynamic_tool_a
		return schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "tc2",
				Function: schema.FunctionCall{
					Name:      "dynamic_tool_a",
					Arguments: `{"input":"hello"}`,
				},
			},
		}), nil
	default:
		// Final response
		return schema.AssistantMessage("done", nil), nil
	}
}

func (m *mockChatModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockChatModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *mockChatModel) getToolsPerCall() [][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ret := make([][]string, len(m.toolsPerCall))
	copy(ret, m.toolsPerCall)
	return ret
}

func TestMiddlewareFlow(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}
	staticTool := &simpleTool{name: "static_tool", desc: "Static tool"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA, dynamicB},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	cm := &mockChatModel{}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "test_agent",
		Description: "test",
		Instruction: "you are a test agent",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{staticTool},
			},
		},
		Handlers: []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	input := &adk.AgentInput{
		Messages: []adk.Message{schema.UserMessage("test")},
	}
	iter := agent.Run(ctx, input)

	var events []*adk.AgentEvent
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, ev)
	}

	// Verify no error event.
	for _, ev := range events {
		if ev.Err != nil {
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}

	// Verify final output is "done".
	lastEvent := events[len(events)-1]
	require.NotNil(t, lastEvent.Output)
	require.NotNil(t, lastEvent.Output.MessageOutput)
	assert.Equal(t, "done", lastEvent.Output.MessageOutput.Message.Content)

	// Verify dynamic_tool_a was actually called.
	assert.True(t, dynamicA.wasCalled(), "dynamic_tool_a should have been called")
	assert.False(t, dynamicB.wasCalled(), "dynamic_tool_b should not have been called")

	// Verify tool lists per Generate call.
	toolsPerCall := cm.getToolsPerCall()
	require.Len(t, toolsPerCall, 3, "expected 3 Generate calls")

	// Call 1: tool_search + static_tool; dynamic tools are hidden.
	assert.Contains(t, toolsPerCall[0], "tool_search")
	assert.Contains(t, toolsPerCall[0], "static_tool")
	assert.NotContains(t, toolsPerCall[0], "dynamic_tool_a")
	assert.NotContains(t, toolsPerCall[0], "dynamic_tool_b")

	// Call 2: after selecting dynamic_tool_a, it becomes visible.
	assert.Contains(t, toolsPerCall[1], "tool_search")
	assert.Contains(t, toolsPerCall[1], "static_tool")
	assert.Contains(t, toolsPerCall[1], "dynamic_tool_a")
	assert.NotContains(t, toolsPerCall[1], "dynamic_tool_b")

	// Call 3: same as call 2.
	assert.Contains(t, toolsPerCall[2], "tool_search")
	assert.Contains(t, toolsPerCall[2], "static_tool")
	assert.Contains(t, toolsPerCall[2], "dynamic_tool_a")
	assert.NotContains(t, toolsPerCall[2], "dynamic_tool_b")

	// Verify reminder is present in messages (checked via tool list — the wrapper inserts it).
	// The model received messages, and the reminder contains "<available-deferred-tools>".
	// We indirectly verify this by checking that the middleware ran without error and the
	// 3-turn flow completed successfully, which requires the tool_search tool to work.

	// Additional: verify that the reminder contains the dynamic tool names.
	mwImpl := mw.(*middleware)
	assert.True(t, strings.Contains(mwImpl.sr, "dynamic_tool_a"))
	assert.True(t, strings.Contains(mwImpl.sr, "dynamic_tool_b"))
	assert.True(t, strings.Contains(mwImpl.sr, "<available-deferred-tools>"))
}

// ---------------------------------------------------------------------------
// TestNew — error paths for New()
// ---------------------------------------------------------------------------

func TestNew(t *testing.T) {
	ctx := context.Background()

	t.Run("nil config", func(t *testing.T) {
		_, err := New(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("empty DynamicTools", func(t *testing.T) {
		_, err := New(ctx, &Config{})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "tools is required")
	})

	t.Run("success", func(t *testing.T) {
		st := &simpleTool{name: "t1", desc: "tool 1"}
		mw, err := New(ctx, &Config{DynamicTools: []tool.BaseTool{st}})
		require.NoError(t, err)
		assert.NotNil(t, mw)
	})
}

// ---------------------------------------------------------------------------
// TestSplitCamelCase
// ---------------------------------------------------------------------------

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"", nil},
		{"hello", []string{"hello"}},
		{"NotebookEdit", []string{"Notebook", "Edit"}},
		{"camelCase", []string{"camel", "Case"}},
		{"HTMLParser", []string{"HTML", "Parser"}},
		{"getURL", []string{"get", "URL"}},
		{"A", []string{"A"}},
		{"AB", []string{"AB"}},
		{"HTTP", []string{"HTTP"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitCamelCase(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// TestInsertReminder
// ---------------------------------------------------------------------------

func TestInsertReminder(t *testing.T) {
	w := &wrapper{reminder: "<reminder>"}

	t.Run("normal: system then user", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hi"},
		}
		got := w.insertReminder(input)
		require.Len(t, got, 3)
		assert.Equal(t, schema.System, got[0].Role)
		assert.Equal(t, schema.User, got[1].Role)
		assert.Equal(t, "<reminder>", got[1].Content)
		assert.Equal(t, schema.User, got[2].Role)
		assert.Equal(t, "hi", got[2].Content)
	})

	t.Run("all system messages", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys1"},
			{Role: schema.System, Content: "sys2"},
		}
		got := w.insertReminder(input)
		require.Len(t, got, 3)
		// Reminder appended at the end since no non-system message found during iteration.
		assert.Equal(t, schema.System, got[0].Role)
		assert.Equal(t, schema.System, got[1].Role)
		assert.Equal(t, "<reminder>", got[2].Content)
	})

	t.Run("empty input", func(t *testing.T) {
		got := w.insertReminder(nil)
		require.Len(t, got, 1)
		assert.Equal(t, "<reminder>", got[0].Content)
	})

	t.Run("no system messages", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.User, Content: "hi"},
			{Role: schema.Assistant, Content: "hello"},
		}
		got := w.insertReminder(input)
		require.Len(t, got, 3)
		// Reminder inserted before the first non-system message.
		assert.Equal(t, "<reminder>", got[0].Content)
		assert.Equal(t, "hi", got[1].Content)
		assert.Equal(t, "hello", got[2].Content)
	})
}

// ---------------------------------------------------------------------------
// TestExtractSelectedTools
// ---------------------------------------------------------------------------

func TestExtractSelectedTools(t *testing.T) {
	ctx := context.Background()

	t.Run("accumulates from multiple tool_search results", func(t *testing.T) {
		messages := []*schema.Message{
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: `{"matches":["tool_a"]}`},
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: `{"matches":["tool_b","tool_c"]}`},
		}
		got, err := extractSelectedTools(ctx, messages)
		require.NoError(t, err)
		assert.Equal(t, []string{"tool_a", "tool_b", "tool_c"}, got)
	})

	t.Run("ignores non tool_search messages", func(t *testing.T) {
		messages := []*schema.Message{
			{Role: schema.User, Content: "hello"},
			{Role: schema.Tool, ToolName: "other_tool", Content: `{"matches":["should_ignore"]}`},
			{Role: schema.Assistant, Content: "world"},
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: `{"matches":["tool_a"]}`},
		}
		got, err := extractSelectedTools(ctx, messages)
		require.NoError(t, err)
		assert.Equal(t, []string{"tool_a"}, got)
	})

	t.Run("malformed JSON returns error", func(t *testing.T) {
		messages := []*schema.Message{
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: `not json`},
		}
		_, err := extractSelectedTools(ctx, messages)
		assert.Error(t, err)
	})

	t.Run("nil messages returns nil", func(t *testing.T) {
		got, err := extractSelectedTools(ctx, nil)
		require.NoError(t, err)
		assert.Nil(t, got)
	})
}

// ---------------------------------------------------------------------------
// TestCalculateTools
// ---------------------------------------------------------------------------

func TestCalculateTools(t *testing.T) {
	ctx := context.Background()

	staticTool := ti("static_tool", "static")
	toolSearchInfo := getToolSearchToolInfo()
	dynA := ti("dynamic_a", "A")
	dynB := ti("dynamic_b", "B")

	allTools := []*schema.ToolInfo{staticTool, toolSearchInfo, dynA, dynB}
	dynamicTools := []*schema.ToolInfo{dynA, dynB}

	t.Run("no selection: dynamic tools hidden", func(t *testing.T) {
		messages := []*schema.Message{
			{Role: schema.User, Content: "hello"},
		}
		opts, err := calculateTools(ctx, allTools, dynamicTools, messages, false)
		require.NoError(t, err)

		options := model.GetCommonOptions(nil, opts...)
		var names []string
		for _, t := range options.Tools {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		assert.Equal(t, []string{"static_tool", "tool_search"}, names)
	})

	t.Run("partial selection: selected tool visible", func(t *testing.T) {
		messages := []*schema.Message{
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: `{"matches":["dynamic_a"]}`},
		}
		opts, err := calculateTools(ctx, allTools, dynamicTools, messages, false)
		require.NoError(t, err)

		options := model.GetCommonOptions(nil, opts...)
		var names []string
		for _, t := range options.Tools {
			names = append(names, t.Name)
		}
		sort.Strings(names)
		assert.Equal(t, []string{"dynamic_a", "static_tool", "tool_search"}, names)
	})

	t.Run("useModelToolSearch: dynamic tools and tool_search removed from WithTools", func(t *testing.T) {
		opts, err := calculateTools(ctx, allTools, dynamicTools, nil, true)
		require.NoError(t, err)

		options := model.GetCommonOptions(nil, opts...)
		var names []string
		for _, t := range options.Tools {
			names = append(names, t.Name)
		}
		assert.Equal(t, []string{"static_tool"}, names)
		// ToolSearchTool should be set.
		assert.NotNil(t, options.ToolSearchTool)
		assert.Equal(t, toolSearchToolName, options.ToolSearchTool.Name)
	})
}
