/*
 * Copyright 2024 CloudWeGo Authors
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

package compose

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type searchArgs struct {
	Query string `json:"query"`
}

func TestToolNameAliases(t *testing.T) {
	ctx := context.Background()

	// Create test tool
	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search for information",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string", Desc: "Search query"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "search result", nil
	})

	// Configure aliases
	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: []ToolAliasConfig{
			{
				ToolName:    "search",
				NameAliases: []string{"search_v1", "query", "find"},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Test calling tool with alias
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search_v1", // Using alias
				Arguments: `{"query": "test"}`,
			},
		},
	})

	output, err := node.Invoke(ctx, input)
	require.NoError(t, err)
	require.Len(t, output, 1)
	assert.Equal(t, "call_1", output[0].ToolCallID)
	assert.Contains(t, output[0].Content, "search result")
}

type searchArgsWithLimit struct {
	Query string `json:"query"`
	Limit int    `json:"limit"`
}

func TestArgumentsAliases(t *testing.T) {
	ctx := context.Background()

	receivedArgs := ""
	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search for information",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
			"limit": {Type: "integer"},
		}),
	}, func(ctx context.Context, args *searchArgsWithLimit) (string, error) {
		b, _ := json.Marshal(args)
		receivedArgs = string(b)
		return "result", nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: []ToolAliasConfig{
			{
				ToolName: "search",
				ArgumentsAliases: map[string][]string{
					"query": {"q", "search_term"},
					"limit": {"max_results", "count"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Use alias parameters
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search",
				Arguments: `{"q": "test", "max_results": 10}`, // Using aliases
			},
		},
	})

	_, err = node.Invoke(ctx, input)
	require.NoError(t, err)

	// Verify tool received canonical parameter names
	var args map[string]any
	err = json.Unmarshal([]byte(receivedArgs), &args)
	require.NoError(t, err)
	assert.Equal(t, "test", args["query"])
	assert.Equal(t, float64(10), args["limit"])
	assert.NotContains(t, args, "q")
	assert.NotContains(t, args, "max_results")
}

type emptyArgs struct{}

func TestAliasConflict(t *testing.T) {
	ctx := context.Background()

	tool1 := newTool(&schema.ToolInfo{Name: "search", Desc: "Search"}, func(ctx context.Context, args *emptyArgs) (string, error) {
		return "result", nil
	})
	tool2 := newTool(&schema.ToolInfo{Name: "query", Desc: "Query"}, func(ctx context.Context, args *emptyArgs) (string, error) {
		return "result", nil
	})

	t.Run("tool name alias conflict", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{tool1, tool2},
			ToolAliases: []ToolAliasConfig{
				{
					ToolName:    "search",
					NameAliases: []string{"find"},
				},
				{
					ToolName:    "query",
					NameAliases: []string{"find"}, // Conflict: find already used by search
				},
			},
		}

		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "alias conflict")
	})

	t.Run("argument alias conflict", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{tool1},
			ToolAliases: []ToolAliasConfig{
				{
					ToolName: "search",
					ArgumentsAliases: map[string][]string{
						"query": {"q"},
						"limit": {"q"}, // Conflict: q maps to multiple parameters
					},
				},
			},
		}

		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting arg alias")
	})
}

func TestArgumentsAliasesWithHandler(t *testing.T) {
	ctx := context.Background()

	executionOrder := []string{}

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		executionOrder = append(executionOrder, "tool_invoke")
		return "result", nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: []ToolAliasConfig{
			{
				ToolName: "search",
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
		ToolArgumentsHandler: func(ctx context.Context, name, args string) (string, error) {
			executionOrder = append(executionOrder, "args_handler")
			// Verify alias remapping has already been done
			var m map[string]any
			err := json.Unmarshal([]byte(args), &m)
			require.NoError(t, err)
			assert.Contains(t, m, "query")
			assert.NotContains(t, m, "q")
			return args, nil
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search",
				Arguments: `{"q": "test"}`,
			},
		},
	})

	_, err = node.Invoke(ctx, input)
	require.NoError(t, err)

	// Verify execution order: alias remapping → ToolArgumentsHandler → tool execution
	assert.Equal(t, []string{"args_handler", "tool_invoke"}, executionOrder)
}

func TestNonExistentToolInAliasConfig(t *testing.T) {
	ctx := context.Background()

	tool1 := newTool(&schema.ToolInfo{Name: "search", Desc: "Search"}, func(ctx context.Context, args *emptyArgs) (string, error) {
		return "result", nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{tool1},
		ToolAliases: []ToolAliasConfig{
			{
				ToolName:    "non_existent_tool", // Non-existent tool
				NameAliases: []string{"alias1"},
			},
		},
	}

	_, err := NewToolNode(ctx, config)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "non-existent tool")
}

type weatherArgs struct {
	Location string `json:"location"`
}

func TestToolAliasesE2E(t *testing.T) {
	ctx := context.Background()

	// Create multiple tools
	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search for information",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
			"limit": {Type: "integer"},
		}),
	}, func(ctx context.Context, args *searchArgsWithLimit) (string, error) {
		return "search result", nil
	})

	weatherTool := newTool(&schema.ToolInfo{
		Name: "weather",
		Desc: "Get weather information",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"location": {Type: "string"},
		}),
	}, func(ctx context.Context, args *weatherArgs) (string, error) {
		return "weather result", nil
	})

	// Configure aliases for multiple tools
	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool, weatherTool},
		ToolAliases: []ToolAliasConfig{
			{
				ToolName:    "search",
				NameAliases: []string{"search_v1", "query"},
				ArgumentsAliases: map[string][]string{
					"query": {"q", "search_term"},
					"limit": {"max_results"},
				},
			},
			{
				ToolName:    "weather",
				NameAliases: []string{"get_weather"},
				ArgumentsAliases: map[string][]string{
					"location": {"loc", "city"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Construct message with multiple tool calls using different aliases
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search_v1",                       // Tool name alias
				Arguments: `{"q": "test", "max_results": 5}`, // Parameter aliases
			},
		},
		{
			ID: "call_2",
			Function: schema.FunctionCall{
				Name:      "get_weather",         // Tool name alias
				Arguments: `{"city": "Beijing"}`, // Parameter alias
			},
		},
	})

	output, err := node.Invoke(ctx, input)
	require.NoError(t, err)
	require.Len(t, output, 2)

	// Verify both tools executed successfully
	assert.Equal(t, "call_1", output[0].ToolCallID)
	assert.Equal(t, "call_2", output[1].ToolCallID)
	assert.Contains(t, output[0].Content, "search result")
	assert.Contains(t, output[1].Content, "weather result")
}
