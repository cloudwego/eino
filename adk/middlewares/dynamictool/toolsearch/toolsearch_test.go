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

	// Call 1: static_tool visible; dynamic tools are hidden.
	assert.Contains(t, toolsPerCall[0], "static_tool")
	assert.NotContains(t, toolsPerCall[0], "dynamic_tool_a")
	assert.NotContains(t, toolsPerCall[0], "dynamic_tool_b")

	// Call 2: after selecting dynamic_tool_a, it becomes visible.
	assert.Contains(t, toolsPerCall[1], "static_tool")
	assert.Contains(t, toolsPerCall[1], "dynamic_tool_a")
	assert.NotContains(t, toolsPerCall[1], "dynamic_tool_b")

	// Call 3: same as call 2.
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
// TestEnsureReminder
// ---------------------------------------------------------------------------

func TestEnsureReminder(t *testing.T) {
	m := &middleware{sr: "<reminder>"}

	t.Run("normal: system then user", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hi"},
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 3)
		assert.Equal(t, schema.System, got[0].Role)
		assert.Equal(t, schema.User, got[1].Role)
		assert.Equal(t, "<reminder>", got[1].Content)
		assert.Equal(t, true, got[1].Extra[toolSearchReminderExtraKey])
		assert.Equal(t, schema.User, got[2].Role)
		assert.Equal(t, "hi", got[2].Content)
	})

	t.Run("all system messages", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys1"},
			{Role: schema.System, Content: "sys2"},
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 3)
		assert.Equal(t, schema.System, got[0].Role)
		assert.Equal(t, schema.System, got[1].Role)
		assert.Equal(t, "<reminder>", got[2].Content)
	})

	t.Run("empty input", func(t *testing.T) {
		got := m.ensureReminder(nil)
		require.Len(t, got, 1)
		assert.Equal(t, "<reminder>", got[0].Content)
	})

	t.Run("no system messages", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.User, Content: "hi"},
			{Role: schema.Assistant, Content: "hello"},
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 3)
		assert.Equal(t, "<reminder>", got[0].Content)
		assert.Equal(t, "hi", got[1].Content)
		assert.Equal(t, "hello", got[2].Content)
	})

	t.Run("idempotent: does not insert twice", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.User, Content: "<reminder>", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			{Role: schema.User, Content: "hi"},
		}
		got := m.ensureReminder(input)
		require.Len(t, got, 2)
		assert.Equal(t, "<reminder>", got[0].Content)
		assert.Equal(t, "hi", got[1].Content)
	})
}

// ---------------------------------------------------------------------------
// TestHelperFunctions
// ---------------------------------------------------------------------------

func TestHelperFunctions(t *testing.T) {
	t.Run("extractDynamicTools", func(t *testing.T) {
		m := &middleware{
			mapOfDynamicTools: map[string]*schema.ToolInfo{
				"dyn_a": ti("dyn_a", "A"),
				"dyn_b": ti("dyn_b", "B"),
			},
		}
		tools := []*schema.ToolInfo{ti("static", "S"), ti("dyn_a", "A"), ti("dyn_b", "B")}
		got := m.extractDynamicTools(tools)
		assert.Len(t, got, 2)
		names := toolNames(got)
		assert.Equal(t, []string{"dyn_a", "dyn_b"}, names)
	})

	t.Run("stripDynamicTools", func(t *testing.T) {
		m := &middleware{
			mapOfDynamicTools: map[string]*schema.ToolInfo{
				"dyn_a": ti("dyn_a", "A"),
				"dyn_b": ti("dyn_b", "B"),
			},
		}
		tools := []*schema.ToolInfo{ti("static", "S"), ti("dyn_a", "A"), ti("tool_search", "TS")}
		got := m.stripDynamicTools(tools)
		names := toolNames(got)
		assert.Equal(t, []string{"static", "tool_search"}, names)
	})

	t.Run("removeTool", func(t *testing.T) {
		tools := []*schema.ToolInfo{ti("a", "A"), ti("b", "B"), ti("c", "C")}
		got := removeTool(tools, "b")
		names := toolNames(got)
		assert.Equal(t, []string{"a", "c"}, names)
	})

	t.Run("toolNameSet", func(t *testing.T) {
		tools := []*schema.ToolInfo{ti("x", "X"), ti("y", "Y")}
		got := toolNameSet(tools)
		assert.True(t, got["x"])
		assert.True(t, got["y"])
		assert.False(t, got["z"])
	})
}

// ---------------------------------------------------------------------------
// TestBeforeModelRewriteState — direct unit tests for BeforeModelRewriteState
// ---------------------------------------------------------------------------

// Note: these tests call BeforeModelRewriteState without a full compose context,
// so RunLocalValue (used by isInitialized/markInitialized) always returns error.
// This means every call re-runs the initialization block. Tests are designed
// accordingly: they test single-call behavior or provide pre-initialized state.

func TestBeforeModelRewriteState_Mode1_Initialization(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA, dynamicB},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	// Simulate state: static_tool + tool_search + dynamic tools (as would come from backfill).
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hello"},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
			ti("dynamic_tool_a", "Dynamic tool A"),
			ti("dynamic_tool_b", "Dynamic tool B"),
		},
	}

	// Initialization strips dynamic tools, keeps tool_search and static tools.
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names := toolNames(state.ToolInfos)
	assert.Equal(t, []string{"static_tool", "tool_search"}, names)
	assert.Nil(t, state.DeferredToolInfos, "Mode 1 should not populate DeferredToolInfos")

	// Verify reminder was inserted.
	assert.Equal(t, 1, countReminders(state.Messages), "reminder should be inserted")
}

func TestBeforeModelRewriteState_Mode1_ForwardSelection(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA, dynamicB},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	// Simulate state AFTER initialization (dynamic tools already stripped).
	// Include a tool_search result message that selected dynamic_tool_a.
	toolSearchResultJSON, _ := json.Marshal(toolSearchResult{Matches: []string{"dynamic_tool_a"}})
	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hello", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc1", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(toolSearchResultJSON)},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
		},
	}

	// Forward selection should add dynamic_tool_a from the tool_search result.
	// Note: init block runs (no compose ctx) but ToolInfos has no dynamic tools to strip.
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names := toolNames(state.ToolInfos)
	assert.Equal(t, []string{"dynamic_tool_a", "static_tool", "tool_search"}, names)

	// Call again: forward selection should be idempotent (dynamic_tool_a already present).
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names = toolNames(state.ToolInfos)
	assert.Equal(t, []string{"dynamic_tool_a", "static_tool", "tool_search"}, names)
}

func TestBeforeModelRewriteState_Mode2_DeferredToolInfos(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA, dynamicB},
		UseModelToolSearch: true,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "hello"},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
			ti("dynamic_tool_a", "Dynamic tool A"),
			ti("dynamic_tool_b", "Dynamic tool B"),
		},
	}

	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	// Mode 2: static tools in ToolInfos (tool_search removed), dynamic in DeferredToolInfos.
	names := toolNames(state.ToolInfos)
	assert.Equal(t, []string{"static_tool"}, names, "ToolInfos should only have static tools")

	deferredNames := toolNames(state.DeferredToolInfos)
	assert.Equal(t, []string{"dynamic_tool_a", "dynamic_tool_b"}, deferredNames, "DeferredToolInfos should have all dynamic tools")
}

func TestBeforeModelRewriteState_ReminderReinsertAfterRemoval(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "hello"},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
			ti("dynamic_tool_a", "Dynamic tool A"),
		},
	}

	// First call: reminder inserted.
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	reminderCount := countReminders(state.Messages)
	assert.Equal(t, 1, reminderCount)

	// Simulate summarization removing the reminder message.
	var msgsWithoutReminder []*schema.Message
	for _, msg := range state.Messages {
		isReminder := false
		if msg.Extra != nil {
			if v, ok := msg.Extra[toolSearchReminderExtraKey].(bool); ok && v {
				isReminder = true
			}
		}
		if !isReminder {
			msgsWithoutReminder = append(msgsWithoutReminder, msg)
		}
	}
	state.Messages = msgsWithoutReminder
	assert.Equal(t, 0, countReminders(state.Messages), "reminder should be gone")

	// Next call: reminder should be re-inserted.
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	reminderCount = countReminders(state.Messages)
	assert.Equal(t, 1, reminderCount, "reminder should be re-inserted after removal")
}

func countReminders(msgs []*schema.Message) int {
	count := 0
	for _, msg := range msgs {
		if msg.Extra != nil {
			if v, _ := msg.Extra[toolSearchReminderExtraKey].(bool); v {
				count++
			}
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// Edge-case tests for BeforeModelRewriteState
// ---------------------------------------------------------------------------

func TestBeforeModelRewriteState_Mode1_MultipleToolSearchResultsAcrossTurns(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}
	dynamicC := &simpleTool{name: "dynamic_tool_c", desc: "Dynamic tool C"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA, dynamicB, dynamicC},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	// Build two separate tool_search result messages, each selecting a different tool.
	resultA, _ := json.Marshal(toolSearchResult{Matches: []string{"dynamic_tool_a"}})
	resultB, _ := json.Marshal(toolSearchResult{Matches: []string{"dynamic_tool_b"}})

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "reminder", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc1", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultA)},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc2", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_b"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultB)},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
		},
	}

	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names := toolNames(state.ToolInfos)
	assert.Contains(t, names, "dynamic_tool_a", "dynamic_tool_a should be added from first tool_search result")
	assert.Contains(t, names, "dynamic_tool_b", "dynamic_tool_b should be added from second tool_search result")
	assert.NotContains(t, names, "dynamic_tool_c", "dynamic_tool_c was never selected")
	assert.Contains(t, names, "static_tool", "static_tool should remain")
	assert.Contains(t, names, "tool_search", "tool_search should remain")
}

func TestBeforeModelRewriteState_Mode1_MalformedJSONInToolSearchResult(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "reminder", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc1", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: `{invalid json!!!`},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
		},
	}

	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err, "malformed JSON in tool_search result should not cause an error")

	names := toolNames(state.ToolInfos)
	assert.NotContains(t, names, "dynamic_tool_a", "malformed JSON result should be skipped")
	assert.Contains(t, names, "static_tool")
	assert.Contains(t, names, "tool_search")
}

func TestBeforeModelRewriteState_Mode1_NonExistentToolInForwardSelection(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	resultJSON, _ := json.Marshal(toolSearchResult{Matches: []string{"nonexistent_tool", "dynamic_tool_a"}})

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "reminder", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc1", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:nonexistent_tool,dynamic_tool_a"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
		},
	}

	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err, "nonexistent tool in forward selection should not cause an error")

	names := toolNames(state.ToolInfos)
	assert.Contains(t, names, "dynamic_tool_a", "valid tool should be added")
	assert.NotContains(t, names, "nonexistent_tool", "nonexistent tool should be silently ignored")
}

func TestBeforeModelRewriteState_Mode2_EmptyToolInfos(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA},
		UseModelToolSearch: true,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "hello"},
		},
		ToolInfos: []*schema.ToolInfo{}, // empty, not nil
	}

	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err, "empty ToolInfos should not cause an error")

	assert.Empty(t, state.ToolInfos, "ToolInfos should be empty")
	assert.Empty(t, state.DeferredToolInfos, "DeferredToolInfos should be empty when no dynamic tools found in ToolInfos")
}

func TestBeforeModelRewriteState_Mode1_DoubleInitWithoutComposeContext(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Dynamic tool B"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA, dynamicB},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	resultJSON, _ := json.Marshal(toolSearchResult{Matches: []string{"dynamic_tool_a"}})

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "reminder", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc1", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		},
		ToolInfos: []*schema.ToolInfo{
			ti("static_tool", "Static tool"),
			getToolSearchToolInfo(),
			ti("dynamic_tool_a", "Dynamic tool A"),
		},
	}

	// First call: init runs (strips dynamic_tool_a), then forward selection re-adds it.
	_, state, err = m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names := toolNames(state.ToolInfos)
	assert.Contains(t, names, "dynamic_tool_a",
		"forward selection should re-add dynamic_tool_a even after init re-strips it")
	assert.Contains(t, names, "static_tool")
	assert.Contains(t, names, "tool_search")

	// Second call: init runs AGAIN (no compose ctx), verify behavior is stable.
	_, state2, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	names2 := toolNames(state2.ToolInfos)
	assert.Contains(t, names2, "dynamic_tool_a",
		"second call should also have dynamic_tool_a re-added by forward selection")
}

func TestBeforeModelRewriteState_ToolInfosSliceMutation(t *testing.T) {
	ctx := context.Background()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Dynamic tool A"}

	mw, err := New(ctx, &Config{
		DynamicTools:       []tool.BaseTool{dynamicA},
		UseModelToolSearch: false,
	})
	require.NoError(t, err)

	m := mw.(*middleware)

	// Create ToolInfos with excess capacity so append could mutate in place.
	originalToolInfos := make([]*schema.ToolInfo, 2, 10)
	originalToolInfos[0] = ti("static_tool", "Static tool")
	originalToolInfos[1] = getToolSearchToolInfo()

	originalLen := len(originalToolInfos)

	resultJSON, _ := json.Marshal(toolSearchResult{Matches: []string{"dynamic_tool_a"}})

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{
			{Role: schema.User, Content: "reminder", Extra: map[string]any{toolSearchReminderExtraKey: true}},
			schema.AssistantMessage("", []schema.ToolCall{
				{ID: "tc1", Function: schema.FunctionCall{Name: toolSearchToolName, Arguments: `{"query":"select:dynamic_tool_a"}`}},
			}),
			{Role: schema.Tool, ToolName: toolSearchToolName, Content: string(resultJSON)},
		},
		ToolInfos: originalToolInfos,
	}

	_, newState, err := m.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)

	newNames := toolNames(newState.ToolInfos)
	assert.Contains(t, newNames, "dynamic_tool_a")
	assert.Equal(t, originalLen, len(originalToolInfos),
		"original ToolInfos slice length should not be mutated by the middleware")
}

// ---------------------------------------------------------------------------
// modelToolSearchTool (Mode 2) tests
// ---------------------------------------------------------------------------

func TestModelToolSearchTool(t *testing.T) {
	ctx := context.Background()

	tools := makeToolMap(
		ti("alpha", "Alpha tool description"),
		ti("beta", "Beta tool description"),
	)
	mts := &modelToolSearchTool{tools: tools}

	// Info should return the standard tool_search tool info.
	info, err := mts.Info(ctx)
	require.NoError(t, err)
	assert.Equal(t, toolSearchToolName, info.Name)

	// InvokableRun with a valid query selecting "alpha".
	arg := &schema.ToolArgument{Text: searchJSON("select:alpha", nil)}
	result, err := mts.InvokableRun(ctx, arg)
	require.NoError(t, err)
	require.Len(t, result.Parts, 1)
	assert.Equal(t, schema.ToolPartTypeToolSearchResult, result.Parts[0].Type)
	require.NotNil(t, result.Parts[0].ToolSearchResult)
	assert.Len(t, result.Parts[0].ToolSearchResult.Tools, 1)
	assert.Equal(t, "alpha", result.Parts[0].ToolSearchResult.Tools[0].Name)

	// InvokableRun with an empty query should return error.
	argEmpty := &schema.ToolArgument{Text: `{"query":""}`}
	_, err = mts.InvokableRun(ctx, argEmpty)
	assert.Error(t, err)
}
