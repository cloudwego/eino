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

package reduction

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestReductionMiddlewareTrunc(t *testing.T) {
	ctx := context.Background()
	it := mockInvokableTool()
	st := mockStreamableTool()

	t.Run("test invokable max length trunc", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "mock_invokable_tool",
			CallID: "12345",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"mock_invokable_tool": {
					Backend:           backend,
					SkipTruncation:    false,
					ReadFileToolName:  "read_file",
					RootDir:           "/tmp",
					MaxLengthForTrunc: 70,
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		exp := "hello worldhello worldhello worldhello worldhello worldhello worldhell<persisted-output>\nOutput too large (199). Full output saved to: /tmp/trunc/12345\nPreview (first 35):\nhello worldhello worldhello worldhe\n\nPreview (last 35):\nldhello worldhello worldhello world\n\n</persisted-output>"

		edp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, tCtx)
		assert.NoError(t, err)
		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		assert.Equal(t, exp, resp)
		content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/12345"})
		assert.NoError(t, err)
		expOrigContent := `     1	hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
     2	hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, content)
	})

	t.Run("test streamable line and max length trunc", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "mock_streamable_tool",
			CallID: "54321",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation: true,
			ToolConfig: map[string]*ToolReductionConfig{
				"mock_streamable_tool": {
					Backend:           backend,
					SkipTruncation:    false,
					ReadFileToolName:  "read_file",
					RootDir:           "/tmp",
					MaxLengthForTrunc: 70,
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)
		exp := "hello worldhello worldhello worldhello worldhello worldhello worldhell<persisted-output>\nOutput too large (199). Full output saved to: /tmp/trunc/54321\nPreview (first 35):\nhello worldhello worldhello worldhe\n\nPreview (last 35):\nldhello worldhello worldhello world\n\n</persisted-output>"

		edp, err := mw.WrapStreamableToolCall(ctx, st.StreamableRun, tCtx)
		assert.NoError(t, err)
		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		s, err := resp.Recv()
		assert.NoError(t, err)
		resp.Close()
		assert.Equal(t, exp, s)
		content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/54321"})
		assert.NoError(t, err)
		expOrigContent := `     1	hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
     2	hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, content)
	})
}

func TestReductionMiddlewareClear(t *testing.T) {
	ctx := context.Background()
	it := mockInvokableTool()
	st := mockStreamableTool()
	tools := []tool.BaseTool{it, st}
	var toolsInfo []*schema.ToolInfo
	for _, bt := range tools {
		ti, _ := bt.Info(ctx)
		toolsInfo = append(toolsInfo, ti)
	}
	type OffloadContent struct {
		Arguments map[string]string `json:"arguments"`
		Result    string            `json:"result"`
	}

	t.Run("test default clear", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 0,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:          backend,
					SkipTruncation:   false,
					SkipClear:        false,
					ReadFileToolName: "read_file",
					RootDir:          "/tmp",
					ClearHandler:     defaultClearHandler("/tmp/clear", true, "read_file"),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("you are a helpful assistant"),
				schema.UserMessage("If it's warmer than 20°C in London, set the thermostat to 20°C, otherwise set it to 18°C."),
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:       "call_987654321",
						Type:     "function",
						Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
					},
				}),
				schema.ToolMessage("Sunny", "call_123456789"),
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:       "call_123456789",
						Type:     "function",
						Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
					},
				}),
				schema.ToolMessage("Sunny", "call_123456789"),
			},
		}, &adk.ModelContext{
			Tools: toolsInfo,
		})
		assert.NoError(t, err)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_987654321",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[2].ToolCalls)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_123456789",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[4].ToolCalls)
		assert.Equal(t, "<persisted-output>Tool result saved to: /tmp/clear/call_987654321\nUse read_file to view</persisted-output>", s.Messages[3].Content)
		fileContent, err := backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/clear/call_987654321",
		})
		assert.NoError(t, err)
		fileContent = strings.TrimPrefix(strings.TrimSpace(fileContent), "1\t")
		assert.Equal(t, "Sunny", fileContent)
	})

	t.Run("test default clear without offloading", func(t *testing.T) {
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 0,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					SkipTruncation:   true,
					SkipClear:        false,
					ReadFileToolName: "read_file",
					RootDir:          "/tmp",
					ClearHandler:     defaultClearHandler("", false, ""),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("you are a helpful assistant"),
				schema.UserMessage("If it's warmer than 20°C in London, set the thermostat to 20°C, otherwise set it to 18°C."),
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:       "call_987654321",
						Type:     "function",
						Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
					},
				}),
				schema.ToolMessage("Sunny", "call_123456789"),
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:       "call_123456789",
						Type:     "function",
						Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
					},
				}),
				schema.ToolMessage("Sunny", "call_123456789"),
			},
		}, &adk.ModelContext{
			Tools: toolsInfo,
		})
		assert.NoError(t, err)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_987654321",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[2].ToolCalls)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_123456789",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[4].ToolCalls)
		assert.Equal(t, "[Old tool result content cleared]", s.Messages[3].Content)
	})

	t.Run("test clear", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		handler := func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error) {
			arguments := make(map[string]string)
			if err := json.Unmarshal([]byte(detail.ToolArgument.TextArgument), &arguments); err != nil {
				return nil, err
			}
			offloadContent := &OffloadContent{
				Arguments: arguments,
				Result:    detail.ToolResult.Parts[0].Text,
			}
			replacedArguments := make(map[string]string, len(arguments))
			filePath := fmt.Sprintf("/tmp/%s", detail.ToolContext.CallID)
			for k := range arguments {
				replacedArguments[k] = "argument offloaded"
			}
			detail.ToolArgument.TextArgument = toJson(replacedArguments)
			detail.ToolResult.Parts[0].Text = "result offloaded, retrieve it from " + filePath
			return &OffloadInfo{
				NeedClear:      true,
				NeedOffload:    true,
				FilePath:       filePath,
				OffloadContent: toJson(offloadContent),
			}, nil
		}
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 0,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:          backend,
					SkipTruncation:   false,
					SkipClear:        false,
					ReadFileToolName: "read_file",
					RootDir:          "/tmp",
					ClearHandler:     handler,
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("you are a helpful assistant"),
				schema.UserMessage("If it's warmer than 20°C in London, set the thermostat to 20°C, otherwise set it to 18°C."),
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:       "call_987654321",
						Type:     "function",
						Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
					},
				}),
				schema.ToolMessage("Sunny", "call_123456789"),
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:       "call_123456789",
						Type:     "function",
						Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
					},
				}),
				schema.ToolMessage("Sunny", "call_123456789"),
			},
		}, &adk.ModelContext{
			Tools: toolsInfo,
		})
		assert.NoError(t, err)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_987654321",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location":"argument offloaded","unit":"argument offloaded"}`},
			},
		}, s.Messages[2].ToolCalls)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_123456789",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[4].ToolCalls)
		assert.Equal(t, "result offloaded, retrieve it from /tmp/call_987654321", s.Messages[3].Content)
		fileContent, err := backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/call_987654321",
		})
		assert.NoError(t, err)
		fileContent = strings.TrimPrefix(strings.TrimSpace(fileContent), "1\t")
		oc := &OffloadContent{}
		err = json.Unmarshal([]byte(fileContent), oc)
		assert.NoError(t, err)
		assert.Equal(t, &OffloadContent{
			Arguments: map[string]string{
				"location": "London, UK",
				"unit":     "c",
			},
			Result: "Sunny",
		}, oc)
	})
}

func TestDefaultOffloadHandler(t *testing.T) {
	ctx := context.Background()
	detail := &ToolDetail{
		ToolContext: &adk.ToolContext{
			Name:   "mock_name",
			CallID: "mock_call_id_12345",
		},
		ToolArgument: &schema.ToolArgument{TextArgument: "anything"},
		ToolResult:   &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "hello"}}},
	}

	fn := defaultClearHandler("/tmp", true, "read_file")
	info, err := fn(ctx, detail)
	assert.NoError(t, err)
	assert.Equal(t, &OffloadInfo{
		NeedClear:      true,
		NeedOffload:    true,
		FilePath:       "/tmp/mock_call_id_12345",
		OffloadContent: "hello",
	}, info)

}

func mockInvokableTool() tool.InvokableTool {
	type ContentContainer struct {
		Value string `json:"value"`
	}
	s1 := strings.Repeat("hello world", 10) + "\n"
	s2 := strings.Repeat("hello world", 8)
	s3 := s1 + s2
	t, _ := utils.InferTool("mock_invokable_tool", "test desc", func(ctx context.Context, input *ContentContainer) (output string, err error) {
		return s3, nil
	})
	return t
}

func mockStreamableTool() tool.StreamableTool {
	type ContentContainer struct {
		Value string `json:"value"`
	}
	s1 := strings.Repeat("hello world", 10) + "\n"
	s2 := strings.Repeat("hello world", 8)
	s3 := s1 + s2
	t, _ := utils.InferStreamTool("mock_streamable_tool", "test desc", func(ctx context.Context, input ContentContainer) (output *schema.StreamReader[string], err error) {
		sr, sw := schema.Pipe[string](11)
		for _, part := range splitStrings(s3, 10) {
			sw.Send(part, nil)
		}
		sw.Close()
		return sr, nil
	})
	return t
}

func splitStrings(s string, n int) []string {
	if n <= 0 {
		n = 1
	}
	if n == 1 {
		return []string{s}
	}
	if len(s) <= n {
		parts := make([]string, n)
		for i := 0; i < len(s); i++ {
			parts[i] = string(s[i])
		}
		return parts
	}
	baseLen := len(s) / n
	extra := len(s) % n
	parts := make([]string, 0, n)
	start := 0
	for i := 0; i < n; i++ {
		end := start + baseLen
		if i < extra {
			end++
		}
		parts = append(parts, s[start:end])
		start = end
	}
	return parts
}

func ptrOf[T any](v T) *T {
	return &v
}

func toJson(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
