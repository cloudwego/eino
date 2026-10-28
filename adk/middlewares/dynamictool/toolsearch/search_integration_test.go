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
	"errors"
	"fmt"
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

type searchIntegrationModelCall[M adk.MessageType] struct {
	input     []M
	options   model.Options
	streaming bool
}

// searchIntegrationModel exercises the real agent loop: discover tools, call a
// discovered tool, and finish. Both model interfaces record their actual inputs.
type searchIntegrationModel[M adk.MessageType] struct {
	mu           sync.Mutex
	calls        []searchIntegrationModelCall[M]
	arguments    string
	selectedTool string
	skipSearch   bool
}

func (m *searchIntegrationModel[M]) next(input []M, streaming bool, opts []model.Option) M {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls = append(m.calls, searchIntegrationModelCall[M]{
		input:     append([]M(nil), input...),
		options:   *model.GetCommonOptions(nil, opts...),
		streaming: streaming,
	})
	if !m.skipSearch {
		switch len(m.calls) {
		case 1:
			return makeAssistantMsgWithToolCalls[M]([]testToolCall{{
				ID: "search-call", Name: toolSearchToolName, Arguments: m.arguments,
			}})
		case 2:
			if m.selectedTool != "" {
				return makeAssistantMsgWithToolCalls[M]([]testToolCall{{
					ID: "selected-call", Name: m.selectedTool, Arguments: `{"input":"test"}`,
				}})
			}
		}
	}
	return makeAssistantGenMsg[M]("done")
}

func (m *searchIntegrationModel[M]) Generate(_ context.Context, input []M, opts ...model.Option) (M, error) {
	return m.next(input, false, opts), nil
}

func (m *searchIntegrationModel[M]) Stream(_ context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	return schema.StreamReaderFromArray([]M{m.next(input, true, opts)}), nil
}

func (m *searchIntegrationModel[M]) snapshot() []searchIntegrationModelCall[M] {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]searchIntegrationModelCall[M](nil), m.calls...)
}

type searchIntegrationContextKey struct{}

type searchIntegrationRequest struct {
	query     string
	limit     int
	requestID any
}

func TestSearchFuncAgentFlow(t *testing.T) {
	t.Run("Message", testSearchFuncAgentFlow[*schema.Message])
	t.Run("AgenticMessage", testSearchFuncAgentFlow[*schema.AgenticMessage])
}

func testSearchFuncAgentFlow[M adk.MessageType](t *testing.T) {
	for _, native := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("native=%t/streaming=%t", native, streaming), func(t *testing.T) {
				for _, scenario := range []string{"custom", "nil", "select", "empty", "error", "cancel"} {
					t.Run(scenario, func(t *testing.T) {
						testSearchFuncAgentScenario[M](t, native, streaming, scenario)
					})
				}
				if native {
					t.Run("no_client_search", func(t *testing.T) {
						testSearchFuncAgentScenario[M](t, native, streaming, "no_client_search")
					})
				}
			})
		}
	}
}

func testSearchFuncAgentScenario[M adk.MessageType](t *testing.T, native, streaming bool, scenario string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), searchIntegrationContextKey{}, "request-id"))
	defer cancel()

	dynamicA := &simpleTool{name: "dynamic_tool_a", desc: "Read local files"}
	dynamicB := &simpleTool{name: "dynamic_tool_b", desc: "Get the weather forecast"}
	dynamicC := &simpleTool{name: "dynamic_tool_c", desc: "Send a message"}
	static := &simpleTool{name: "static_tool", desc: "Always available"}
	dynamic := []tool.BaseTool{dynamicA, dynamicB, dynamicC}

	var requestMu sync.Mutex
	var requests []searchIntegrationRequest
	searchErr := errors.New("search backend unavailable")
	searchFunc := SearchFunc(func(ctx context.Context, query string, limit int) ([]string, error) {
		requestMu.Lock()
		requests = append(requests, searchIntegrationRequest{query: query, limit: limit, requestID: ctx.Value(searchIntegrationContextKey{})})
		requestMu.Unlock()
		switch scenario {
		case "empty":
			return nil, nil
		case "error":
			return nil, searchErr
		case "cancel":
			cancel()
		}
		// Unknown and static tools cannot escape DynamicTools; duplicates must
		// not consume the limit or change the backend's ranking.
		return []string{"unknown", "dynamic_tool_b", "dynamic_tool_b", "static_tool", "dynamic_tool_a", "dynamic_tool_c"}, nil
	})
	cm := &searchIntegrationModel[M]{
		arguments:    `{"query":"  semantic intent  ","max_results":2}`,
		selectedTool: "dynamic_tool_b",
	}
	wantMatches := []string{"dynamic_tool_b", "dynamic_tool_a"}
	wantRequest := []searchIntegrationRequest{{query: "semantic intent", limit: 2, requestID: "request-id"}}
	var wantErr error
	switch scenario {
	case "nil":
		searchFunc = nil
		cm.arguments = `{"query":"forecast"}`
		wantMatches = []string{"dynamic_tool_b"}
		wantRequest = nil
	case "select":
		cm.arguments = `{"query":"select:dynamic_tool_b,dynamic_tool_b,dynamic_tool_a","max_results":1}`
		wantMatches = []string{"dynamic_tool_b", "dynamic_tool_b", "dynamic_tool_a"}
		wantRequest = nil
	case "empty":
		cm.arguments = `{"query":"  semantic intent  ","max_results":0}`
		cm.selectedTool = ""
		wantMatches = nil
		wantRequest[0].limit = 5
	case "error":
		wantErr = searchErr
	case "cancel":
		wantErr = context.Canceled
	case "no_client_search":
		cm.skipSearch = true
		cm.selectedTool = ""
		wantRequest = nil
	}

	mw, err := NewTyped[M](ctx, &Config{
		DynamicTools: dynamic, UseModelToolSearch: native, SearchFunc: searchFunc,
	})
	require.NoError(t, err)
	agent, err := adk.NewTypedChatModelAgent[M](ctx, &adk.TypedChatModelAgentConfig[M]{
		Name: "search_agent", Model: cm, MaxIterations: 4,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{static}}},
		Handlers:    []adk.TypedChatModelAgentMiddleware[M]{mw},
	})
	require.NoError(t, err)
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{Agent: agent, EnableStreaming: streaming})
	iter := runner.Query(ctx, "Find a tool and use it")
	var runErrors []error
	var messages []M
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			runErrors = append(runErrors, event.Err)
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msg, outputErr := event.Output.MessageOutput.GetMessage()
			if outputErr != nil {
				runErrors = append(runErrors, outputErr)
			} else {
				messages = append(messages, msg)
			}
		}
	}

	requestMu.Lock()
	gotRequests := append([]searchIntegrationRequest(nil), requests...)
	requestMu.Unlock()
	assert.Equal(t, wantRequest, gotRequests)
	calls := cm.snapshot()
	require.NotEmpty(t, calls)
	for _, call := range calls {
		assert.Equal(t, streaming, call.streaming)
		if native {
			assert.Equal(t, []string{"static_tool"}, toolNames(call.options.Tools))
			assert.Equal(t, []string{"dynamic_tool_a", "dynamic_tool_b", "dynamic_tool_c"}, toolNames(call.options.DeferredTools))
			require.NotNil(t, call.options.ToolSearchTool)
			assert.Equal(t, toolSearchToolName, call.options.ToolSearchTool.Name)
		} else {
			assert.Empty(t, call.options.DeferredTools)
			assert.Nil(t, call.options.ToolSearchTool)
		}
	}
	if !native {
		assert.Equal(t, []string{"static_tool", toolSearchToolName}, toolNames(calls[0].options.Tools))
	}
	assert.False(t, dynamicA.wasCalled())
	assert.False(t, dynamicC.wasCalled())
	assert.False(t, static.wasCalled())

	if wantErr != nil {
		require.NotEmpty(t, runErrors)
		matched := false
		for _, runErr := range runErrors {
			matched = matched || errors.Is(runErr, wantErr)
		}
		assert.True(t, matched, "runner errors %v must preserve %v", runErrors, wantErr)
		assert.Len(t, calls, 1, "a failed search must not advance the model loop")
		assert.False(t, dynamicB.wasCalled())
		return
	}

	require.Empty(t, runErrors)
	require.NotEmpty(t, messages)
	assert.Equal(t, "done", searchIntegrationAssistantText(messages[len(messages)-1]))
	if scenario == "no_client_search" {
		assert.Len(t, calls, 1)
		assert.False(t, dynamicB.wasCalled())
		return
	}
	wantCalls := 2
	if cm.selectedTool != "" {
		wantCalls = 3
	}
	require.Len(t, calls, wantCalls)
	assert.Equal(t, cm.selectedTool != "", dynamicB.wasCalled())

	if native {
		result := searchIntegrationNativeResult(t, calls[1].input)
		var names []string
		for _, info := range result.Tools {
			names = append(names, info.Name)
			var expected *schema.ToolInfo
			for _, candidate := range dynamic {
				candidateInfo, infoErr := candidate.Info(ctx)
				require.NoError(t, infoErr)
				if candidateInfo.Name == info.Name {
					expected = candidateInfo
				}
			}
			require.NotNil(t, expected)
			assert.Equal(t, expected, info, "native search must return complete registered definitions")
		}
		assert.Equal(t, wantMatches, names)
	} else {
		var result toolSearchResult
		found := false
		for _, msg := range calls[1].input {
			if content, ok := extractToolSearchResult(msg, toolSearchToolName); ok {
				require.NoError(t, json.Unmarshal([]byte(content), &result))
				found = true
			}
		}
		require.True(t, found, "the next model call must receive the search result")
		assert.Equal(t, wantMatches, result.Matches)
		wantVisible := []string{"static_tool", toolSearchToolName}
		if len(wantMatches) > 0 {
			wantVisible = append(wantVisible, "dynamic_tool_b")
			if scenario != "nil" {
				wantVisible = append(wantVisible, "dynamic_tool_a")
			}
		}
		for _, call := range calls[1:] {
			assert.ElementsMatch(t, wantVisible, toolNames(call.options.Tools))
		}
	}
}

func searchIntegrationNativeResult[M adk.MessageType](t *testing.T, messages []M) *schema.ToolSearchResult {
	t.Helper()
	for _, msg := range messages {
		switch v := any(msg).(type) {
		case *schema.Message:
			if v.ToolCallID == "search-call" {
				for _, part := range v.UserInputMultiContent {
					if part.ToolSearchResult != nil {
						assert.Equal(t, schema.ChatMessagePartTypeToolSearchResult, part.Type)
						return part.ToolSearchResult
					}
				}
			}
		case *schema.AgenticMessage:
			for _, block := range v.ContentBlocks {
				if block != nil && block.ToolSearchFunctionToolResult != nil && block.ToolSearchFunctionToolResult.CallID == "search-call" {
					assert.Equal(t, schema.ContentBlockTypeToolSearchResult, block.Type)
					assert.Equal(t, toolSearchToolName, block.ToolSearchFunctionToolResult.Name)
					require.NotNil(t, block.ToolSearchFunctionToolResult.Result)
					return block.ToolSearchFunctionToolResult.Result
				}
			}
		}
	}
	t.Fatal("the next model call must receive the structured tool search result")
	return nil
}

func searchIntegrationAssistantText[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Content
	case *schema.AgenticMessage:
		for _, block := range v.ContentBlocks {
			if block != nil && block.AssistantGenText != nil {
				return block.AssistantGenText.Text
			}
		}
	}
	return ""
}
