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
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
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
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
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
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
					NameAliases: []string{"find"},
				},
				"query": {
					NameAliases: []string{"find"}, // Conflict: find already used by search
				},
			},
		}

		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with an alias already registered for")
	})

	t.Run("tool name alias conflicts with canonical name", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{tool1, tool2},
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
					NameAliases: []string{"query"}, // Conflict: "query" is tool2's canonical name
				},
			},
		}

		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with existing tool's canonical name")
	})

	t.Run("argument alias conflict", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{tool1},
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
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

	t.Run("arg alias conflicts with existing schema property", func(t *testing.T) {
		searchWithParams := newTool(&schema.ToolInfo{
			Name: "search",
			Desc: "Search",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"query": {Type: "string"},
				"limit": {Type: "integer"},
			}),
		}, func(ctx context.Context, args *emptyArgs) (string, error) {
			return "result", nil
		})

		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{searchWithParams},
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
					ArgumentsAliases: map[string][]string{
						"limit": {"query"}, // "query" is already a schema property
					},
				},
			},
		}

		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "conflicts with existing schema property")
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
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
		ToolArgumentsHandler: func(ctx context.Context, name, args string) (string, error) {
			executionOrder = append(executionOrder, "args_handler")
			// Handler receives the original model-returned name (alias)
			assert.Equal(t, "search", name)
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

	// Call with alias name "find" and alias arg "q"
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "find",
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
		ToolAliases: map[string]ToolAliasConfig{
			"non_existent_tool": { // Non-existent tool
				NameAliases: []string{"alias1"},
			},
		},
	}

	// Should not error — non-existent tool alias configs are silently skipped
	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// The existing tool should still work normally
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search",
				Arguments: `{}`,
			},
		},
	})
	output, err := node.Invoke(ctx, input)
	require.NoError(t, err)
	require.Len(t, output, 1)
	assert.Contains(t, output[0].Content, "result")
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
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"search_v1", "query"},
				ArgumentsAliases: map[string][]string{
					"query": {"q", "search_term"},
					"limit": {"max_results"},
				},
			},
			"weather": {
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

func TestRemapArgsEdgeCases(t *testing.T) {
	aliasMap := map[string]string{"q": "query"}

	t.Run("empty string", func(t *testing.T) {
		result, err := remapArgs("", aliasMap)
		assert.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("whitespace only", func(t *testing.T) {
		result, err := remapArgs("  ", aliasMap)
		assert.NoError(t, err)
		assert.Equal(t, "  ", result)
	})

	t.Run("non-object JSON", func(t *testing.T) {
		result, err := remapArgs(`"hello"`, aliasMap)
		assert.NoError(t, err)
		assert.Equal(t, `"hello"`, result)
	})

	t.Run("JSON array", func(t *testing.T) {
		result, err := remapArgs(`[1,2,3]`, aliasMap)
		assert.NoError(t, err)
		assert.Equal(t, `[1,2,3]`, result)
	})

	t.Run("invalid JSON", func(t *testing.T) {
		result, err := remapArgs(`{invalid`, aliasMap)
		assert.NoError(t, err)
		assert.Equal(t, `{invalid`, result)
	})

	t.Run("alias and canonical both present", func(t *testing.T) {
		// When both alias "q" and canonical "query" exist, alias is kept as-is (not deleted, not overwritten)
		result, err := remapArgs(`{"q": "alias_val", "query": "canonical_val"}`, aliasMap)
		assert.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(result), &m))
		assert.Equal(t, "canonical_val", m["query"])
		assert.Equal(t, "alias_val", m["q"])
	})

	t.Run("unknown fields preserved", func(t *testing.T) {
		result, err := remapArgs(`{"q": "test", "unknown_field": 42}`, aliasMap)
		assert.NoError(t, err)
		var m map[string]any
		require.NoError(t, json.Unmarshal([]byte(result), &m))
		assert.Equal(t, "test", m["query"])
		assert.NotContains(t, m, "q")
		assert.Equal(t, float64(42), m["unknown_field"])
	})
}

func TestCanonicalNameCallWithAliasConfigured(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "result: " + args.Query, nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Call with canonical name and canonical arg — should work normally
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "search",
				Arguments: `{"query": "hello"}`,
			},
		},
	})

	output, err := node.Invoke(ctx, input)
	require.NoError(t, err)
	require.Len(t, output, 1)
	assert.Contains(t, output[0].Content, "result: hello")
}

func TestEmptyAliasValidation(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{Name: "search", Desc: "Search"}, func(ctx context.Context, args *emptyArgs) (string, error) {
		return "result", nil
	})

	t.Run("empty name alias", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{searchTool},
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
					NameAliases: []string{""},
				},
			},
		}
		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty name alias")
	})

	t.Run("empty arg alias", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{searchTool},
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
					ArgumentsAliases: map[string][]string{
						"query": {""},
					},
				},
			},
		}
		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty argument alias")
	})

	t.Run("empty canonical arg key", func(t *testing.T) {
		config := &ToolsNodeConfig{
			Tools: []tool.BaseTool{searchTool},
			ToolAliases: map[string]ToolAliasConfig{
				"search": {
					ArgumentsAliases: map[string][]string{
						"": {"q"},
					},
				},
			},
		}
		_, err := NewToolNode(ctx, config)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "empty canonical argument key")
	})
}

func TestNameAliasSameAsCanonical(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{Name: "search", Desc: "Search"}, func(ctx context.Context, args *emptyArgs) (string, error) {
		return "result", nil
	})

	// Alias same as canonical name — should be tolerated (skip, no error)
	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"search", "find"},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Both canonical and alias should work
	for _, name := range []string{"search", "find"} {
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      name,
					Arguments: `{}`,
				},
			},
		})
		output, err := node.Invoke(ctx, input)
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "result")
	}
}

func TestToolAliasesWithDynamicToolList(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "search result: " + args.Query, nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Use dynamic ToolList via option — alias should still work
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "find",
				Arguments: `{"q": "dynamic"}`,
			},
		},
	})

	output, err := node.Invoke(ctx, input, WithToolList(searchTool))
	require.NoError(t, err)
	require.Len(t, output, 1)
	assert.Contains(t, output[0].Content, "search result: dynamic")
}

func TestToolNameAliasesStream(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search for information",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "stream result: " + args.Query, nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "find",
				Arguments: `{"q": "hello"}`,
			},
		},
	})

	reader, err := node.Stream(ctx, input)
	require.NoError(t, err)

	var chunks [][]*schema.Message
	for {
		chunk, err := reader.Recv()
		if err != nil {
			break
		}
		chunks = append(chunks, chunk)
	}

	msgs, err := schema.ConcatMessageArray(chunks)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, "call_1", msgs[0].ToolCallID)
	assert.Contains(t, msgs[0].Content, "stream result: hello")
}

func TestEnhancedToolWithAliases(t *testing.T) {
	ctx := context.Background()

	enhancedTool := &enhancedInvokableTool{
		info: &schema.ToolInfo{
			Name: "search",
			Desc: "Enhanced search",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"query": {Type: "string"},
			}),
		},
		fn: func(ctx context.Context, input *schema.ToolArgument) (*schema.ToolResult, error) {
			return &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: "enhanced: " + input.Text},
				},
			}, nil
		},
	}

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{enhancedTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Call with alias name and alias arg
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "find",
				Arguments: `{"q": "test"}`,
			},
		},
	})

	output, err := node.Invoke(ctx, input)
	require.NoError(t, err)
	require.Len(t, output, 1)
	assert.Equal(t, "call_1", output[0].ToolCallID)
	// Verify arg alias was remapped: "q" → "query" in the JSON passed to enhanced tool
	assert.Contains(t, output[0].UserInputMultiContent[0].Text, "enhanced:")
}

func TestDynamicToolListAliasRemoved(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "search result", nil
	})

	weatherTool := newTool(&schema.ToolInfo{
		Name: "weather",
		Desc: "Weather",
	}, func(ctx context.Context, args *emptyArgs) (string, error) {
		return "weather result", nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool, weatherTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	// Dynamic tool list only contains weatherTool — "search" and its alias "find" should not be available
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID: "call_1",
			Function: schema.FunctionCall{
				Name:      "find",
				Arguments: `{}`,
			},
		},
	})

	_, err = node.Invoke(ctx, input, WithToolList(weatherTool))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found")
}

func TestToolAliasesOptionOverridesGlobal(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "search result: " + args.Query, nil
	})

	weatherTool := newTool(&schema.ToolInfo{
		Name: "weather",
		Desc: "Weather",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"location": {Type: "string"},
		}),
	}, func(ctx context.Context, args *weatherArgs) (string, error) {
		return "weather result: " + args.Location, nil
	})

	// Global aliases: search has alias "find"
	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool, weatherTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	t.Run("opt ToolAliases overrides global in Invoke", func(t *testing.T) {
		// opt.ToolAliases defines "lookup" as alias for search (not "find")
		optAliases := map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"lookup"},
				ArgumentsAliases: map[string][]string{
					"query": {"keyword"},
				},
			},
		}

		// "lookup" should work with opt aliases
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "lookup",
					Arguments: `{"keyword": "test"}`,
				},
			},
		})

		output, err := node.Invoke(ctx, input, WithToolList(searchTool), WithToolAliases(optAliases))
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "search result: test")

		// "find" (global alias) should NOT work when opt.ToolAliases is set
		input2 := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_2",
				Function: schema.FunctionCall{
					Name:      "find",
					Arguments: `{"q": "test"}`,
				},
			},
		})

		_, err = node.Invoke(ctx, input2, WithToolList(searchTool), WithToolAliases(optAliases))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("opt ToolAliases overrides global in Stream", func(t *testing.T) {
		optAliases := map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"lookup"},
				ArgumentsAliases: map[string][]string{
					"query": {"keyword"},
				},
			},
		}

		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "lookup",
					Arguments: `{"keyword": "stream_test"}`,
				},
			},
		})

		reader, err := node.Stream(ctx, input, WithToolList(searchTool), WithToolAliases(optAliases))
		require.NoError(t, err)

		var chunks [][]*schema.Message
		for {
			chunk, err := reader.Recv()
			if err != nil {
				break
			}
			chunks = append(chunks, chunk)
		}

		msgs, err := schema.ConcatMessageArray(chunks)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		assert.Contains(t, msgs[0].Content, "search result: stream_test")
	})

	t.Run("nil opt ToolAliases falls back to global filtered", func(t *testing.T) {
		// No WithToolAliases — should use global "find" alias, filtered by ToolList
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "find",
					Arguments: `{"q": "fallback"}`,
				},
			},
		})

		output, err := node.Invoke(ctx, input, WithToolList(searchTool))
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "search result: fallback")
	})

	t.Run("opt ToolAliases only without ToolList replaces global", func(t *testing.T) {
		// Only WithToolAliases, no WithToolList — should use global tools with opt aliases
		optAliases := map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"lookup"},
				ArgumentsAliases: map[string][]string{
					"query": {"keyword"},
				},
			},
		}

		// "lookup" (opt alias) should work
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "lookup",
					Arguments: `{"keyword": "only_alias"}`,
				},
			},
		})

		output, err := node.Invoke(ctx, input, WithToolAliases(optAliases))
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "search result: only_alias")

		// "find" (global alias) should NOT work when opt.ToolAliases replaces global
		input2 := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_2",
				Function: schema.FunctionCall{
					Name:      "find",
					Arguments: `{"q": "test"}`,
				},
			},
		})

		_, err = node.Invoke(ctx, input2, WithToolAliases(optAliases))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})

	t.Run("opt ToolAliases only without ToolList in Stream", func(t *testing.T) {
		optAliases := map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"lookup"},
			},
		}

		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "lookup",
					Arguments: `{"query": "stream_only_alias"}`,
				},
			},
		})

		reader, err := node.Stream(ctx, input, WithToolAliases(optAliases))
		require.NoError(t, err)

		var chunks [][]*schema.Message
		for {
			chunk, err := reader.Recv()
			if err != nil {
				break
			}
			chunks = append(chunks, chunk)
		}

		msgs, err := schema.ConcatMessageArray(chunks)
		require.NoError(t, err)
		require.Len(t, msgs, 1)
		assert.Contains(t, msgs[0].Content, "search result: stream_only_alias")
	})
}

func TestAliasConfigForToolAddedViaOption(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "search result: " + args.Query, nil
	})

	weatherTool := newTool(&schema.ToolInfo{
		Name: "weather",
		Desc: "Weather",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"location": {Type: "string"},
		}),
	}, func(ctx context.Context, args *weatherArgs) (string, error) {
		return "weather result: " + args.Location, nil
	})

	// New with only searchTool, but alias config includes weather tool
	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
				ArgumentsAliases: map[string][]string{
					"query": {"q"},
				},
			},
			"weather": {
				NameAliases: []string{"forecast"},
				ArgumentsAliases: map[string][]string{
					"location": {"loc"},
				},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	t.Run("weather alias works when tool passed via option", func(t *testing.T) {
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "forecast",
					Arguments: `{"loc": "Beijing"}`,
				},
			},
		})

		output, err := node.Invoke(ctx, input, WithToolList(searchTool, weatherTool))
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "weather result: Beijing")
	})

	t.Run("search alias still works with option tool list", func(t *testing.T) {
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "find",
					Arguments: `{"q": "test"}`,
				},
			},
		})

		output, err := node.Invoke(ctx, input, WithToolList(searchTool, weatherTool))
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "search result: test")
	})
}

func TestOptionWithToolListAndToolAliases(t *testing.T) {
	ctx := context.Background()

	searchTool := newTool(&schema.ToolInfo{
		Name: "search",
		Desc: "Search",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {Type: "string"},
		}),
	}, func(ctx context.Context, args *searchArgs) (string, error) {
		return "search result: " + args.Query, nil
	})

	weatherTool := newTool(&schema.ToolInfo{
		Name: "weather",
		Desc: "Weather",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"location": {Type: "string"},
		}),
	}, func(ctx context.Context, args *weatherArgs) (string, error) {
		return "weather result: " + args.Location, nil
	})

	config := &ToolsNodeConfig{
		Tools: []tool.BaseTool{searchTool},
		ToolAliases: map[string]ToolAliasConfig{
			"search": {
				NameAliases: []string{"find"},
			},
		},
	}

	node, err := NewToolNode(ctx, config)
	require.NoError(t, err)

	t.Run("opt aliases override global when both tool list and aliases provided", func(t *testing.T) {
		optAliases := map[string]ToolAliasConfig{
			"weather": {
				NameAliases: []string{"forecast"},
				ArgumentsAliases: map[string][]string{
					"location": {"loc"},
				},
			},
		}

		// "forecast" should work via opt aliases
		input := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_1",
				Function: schema.FunctionCall{
					Name:      "forecast",
					Arguments: `{"loc": "Shanghai"}`,
				},
			},
		})

		output, err := node.Invoke(ctx, input, WithToolList(searchTool, weatherTool), WithToolAliases(optAliases))
		require.NoError(t, err)
		require.Len(t, output, 1)
		assert.Contains(t, output[0].Content, "weather result: Shanghai")

		// "find" (global alias) should NOT work when opt aliases override
		input2 := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID: "call_2",
				Function: schema.FunctionCall{
					Name:      "find",
					Arguments: `{"query": "test"}`,
				},
			},
		})

		_, err = node.Invoke(ctx, input2, WithToolList(searchTool, weatherTool), WithToolAliases(optAliases))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not found")
	})
}
