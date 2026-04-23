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
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func testTruncOffloadPath(root string) func(context.Context, *ToolDetail) (string, error) {
	return func(_ context.Context, detail *ToolDetail) (string, error) {
		callID := "generated"
		if detail != nil && detail.ToolContext != nil && detail.ToolContext.CallID != "" {
			callID = detail.ToolContext.CallID
		}
		return fmt.Sprintf("%s/trunc/%s", root, callID), nil
	}
}

func testClearOffloadPath(root string) func(context.Context, *ToolDetail) (string, error) {
	return func(_ context.Context, detail *ToolDetail) (string, error) {
		callID := "generated"
		if detail != nil && detail.ToolContext != nil && detail.ToolContext.CallID != "" {
			callID = detail.ToolContext.CallID
		}
		return fmt.Sprintf("%s/clear/%s", root, callID), nil
	}
}

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
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler:   defaultTruncHandler(testTruncOffloadPath("/tmp"), 70),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		exp := "<persisted-output>\nOutput too large (199). Full output saved to: /tmp/trunc/12345\nPreview (first 35):\nhello worldhello worldhello worldhe\n\nPreview (last 35):\nldhello worldhello worldhello world\n\n</persisted-output>"

		edp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, tCtx)
		assert.NoError(t, err)
		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		assert.Equal(t, exp, resp)
		content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/12345"})
		assert.NoError(t, err)
		expOrigContent := `hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, content.Content)
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
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler:   defaultTruncHandler(testTruncOffloadPath("/tmp"), 70),
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)
		exp := "<persisted-output>\nOutput too large (199). Full output saved to: /tmp/trunc/54321\nPreview (first 35):\nhello worldhello worldhello worldhe\n\nPreview (last 35):\nldhello worldhello worldhello world\n\n</persisted-output>"

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
		expOrigContent := `hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, content.Content)
	})

	t.Run("test streamable line and bypass error", func(t *testing.T) {
		stWithErr := mockStreamableToolWithError()
		tCtx := &adk.ToolContext{
			Name:   "mock_streamable_tool",
			CallID: "54321",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation: true,
			ToolConfig: map[string]*ToolReductionConfig{
				"mock_streamable_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler:   defaultTruncHandler(testTruncOffloadPath("/tmp"), 70),
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)
		edp, err := mw.WrapStreamableToolCall(ctx, stWithErr.StreamableRun, tCtx)
		assert.NoError(t, err)
		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		cnt := 0
		gotError := false
		for {
			_, err := resp.Recv()
			if err != nil {
				if err == io.EOF {
					break
				}
				assert.Equal(t, fmt.Errorf("mock error"), err)
				gotError = true
				break
			}
			cnt++
		}
		assert.True(t, gotError)
		assert.Equal(t, 10, cnt)
	})

	t.Run("test TruncExcludeTools with invokable tool", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "important_tool",
			CallID: "12345",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:           backend,
			TruncExcludeTools: []string{"important_tool"},
			MaxLengthForTrunc: 70,
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)

		edp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, tCtx)
		assert.NoError(t, err)

		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)

		expOrigContent := `hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, resp)

		_, err = backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/12345"})
		assert.Error(t, err)
	})

	t.Run("test TruncExcludeTools with streamable tool", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "important_stream_tool",
			CallID: "54321",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:           backend,
			TruncExcludeTools: []string{"important_stream_tool"},
			MaxLengthForTrunc: 70,
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)

		edp, err := mw.WrapStreamableToolCall(ctx, st.StreamableRun, tCtx)
		assert.NoError(t, err)

		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)

		var fullOutput strings.Builder
		for {
			chunk, recvErr := resp.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					break
				}
				assert.Fail(t, "unexpected error", recvErr)
			}
			fullOutput.WriteString(chunk)
		}
		resp.Close()

		expOrigContent := `hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, fullOutput.String())

		_, err = backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/54321"})
		assert.Error(t, err)
	})

	t.Run("test mixed tools with TruncExcludeTools", func(t *testing.T) {
		excludedToolCtx := &adk.ToolContext{
			Name:   "excluded_tool",
			CallID: "excluded_123",
		}
		normalToolCtx := &adk.ToolContext{
			Name:   "normal_tool",
			CallID: "normal_456",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:           backend,
			TruncExcludeTools: []string{"excluded_tool"},
			MaxLengthForTrunc: 70,
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)

		excludedEdp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, excludedToolCtx)
		assert.NoError(t, err)
		excludedResp, err := excludedEdp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		expOrigContent := `hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world
hello worldhello worldhello worldhello worldhello worldhello worldhello worldhello world`
		assert.Equal(t, expOrigContent, excludedResp)

		normalEdp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, normalToolCtx)
		assert.NoError(t, err)
		normalResp, err := normalEdp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		assert.NotEqual(t, expOrigContent, normalResp)
		assert.Contains(t, normalResp, "persisted-output")

		content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/normal_456"})
		assert.NoError(t, err)
		assert.Equal(t, expOrigContent, content.Content)
	})

	t.Run("test GenTruncOffloadFilePath used by default trunc handler", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "mock_invokable_tool",
			CallID: "custom_123",
		}
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:           backend,
			MaxLengthForTrunc: 70,
			RootDir:           "/tmp/ignored",
			GenTruncOffloadFilePath: func(_ context.Context, detail *ToolDetail) (string, error) {
				assert.Equal(t, "custom_123", detail.ToolContext.CallID)
				return "/custom/trunc/custom_123", nil
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)

		edp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, tCtx)
		assert.NoError(t, err)

		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		assert.Contains(t, resp, "/custom/trunc/custom_123")

		_, err = backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/custom/trunc/custom_123"})
		assert.NoError(t, err)
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
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
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
		fileContentStr := strings.TrimPrefix(strings.TrimSpace(fileContent.Content), "1\t")
		assert.Equal(t, "Sunny", fileContentStr)
	})

	t.Run("test GenClearOffloadFilePath used by default clear handler", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:                   backend,
			SkipTruncation:            true,
			TokenCounter:              func(context.Context, []adk.Message, []*schema.ToolInfo) (int64, error) { return 100, nil },
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 1,
			RootDir:                   "/tmp/ignored",
			GenClearOffloadFilePath: func(_ context.Context, detail *ToolDetail) (string, error) {
				return "/custom/clear/" + detail.ToolContext.CallID, nil
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)

		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.SystemMessage("you are a helpful assistant"),
				schema.UserMessage("trigger clear"),
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "call_a", Type: "function", Function: schema.FunctionCall{Name: "get_weather", Arguments: `{}`}},
				}),
				schema.ToolMessage("Sunny", "call_a"),
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "call_b", Type: "function", Function: schema.FunctionCall{Name: "get_weather", Arguments: `{}`}},
				}),
				schema.ToolMessage("Sunny", "call_b"),
			},
		}, &adk.ModelContext{Tools: toolsInfo})
		assert.NoError(t, err)

		assert.Equal(t, "<persisted-output>Tool result saved to: /custom/clear/call_a\nUse read_file to view</persisted-output>", s.Messages[3].Content)

		fileContent, err := backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/custom/clear/call_a",
		})
		assert.NoError(t, err)
		fileContentStr := strings.TrimPrefix(strings.TrimSpace(fileContent.Content), "1\t")
		assert.Equal(t, "Sunny", fileContentStr)
	})

	t.Run("test default clear without offloading", func(t *testing.T) {
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 0,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath(""), false, ""),
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
		handler := func(ctx context.Context, detail *ToolDetail) (*ClearResult, error) {
			arguments := make(map[string]string)
			if err := json.Unmarshal([]byte(detail.ToolArgument.Text), &arguments); err != nil {
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
			return &ClearResult{
				ToolArgument: &schema.ToolArgument{Text: toJson(replacedArguments)},
				ToolResult: &schema.ToolResult{
					Parts: []schema.ToolOutputPart{
						{Type: schema.ToolPartTypeText, Text: "result offloaded, retrieve it from " + filePath},
					},
				},
				NeedClear:       true,
				NeedOffload:     true,
				OffloadFilePath: filePath,
				OffloadContent:  toJson(offloadContent),
			}, nil
		}
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 1,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: handler,
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
		fileContentStr := strings.TrimPrefix(strings.TrimSpace(fileContent.Content), "1\t")
		oc := &OffloadContent{}
		err = json.Unmarshal([]byte(fileContentStr), oc)
		assert.NoError(t, err)
		assert.Equal(t, &OffloadContent{
			Arguments: map[string]string{
				"location": "London, UK",
				"unit":     "c",
			},
			Result: "Sunny",
		}, oc)
	})

	t.Run("test skip handled ones", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 0,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		msgs := []adk.Message{
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
		}
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: msgs}, &adk.ModelContext{Tools: toolsInfo})
		assert.NoError(t, err)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_987654321",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[2].ToolCalls)
		assert.NotNil(t, msgs[2].Extra[msgClearedFlag])
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
		fileContentStr := strings.TrimPrefix(strings.TrimSpace(fileContent.Content), "1\t")
		assert.Equal(t, "Sunny", fileContentStr)

		msgs = append(msgs, []*schema.Message{
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_8877665544",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny", "call_8877665544"),
		}...)
		_, s, err = mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: msgs}, &adk.ModelContext{Tools: toolsInfo})
		assert.NoError(t, err)
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_987654321",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[2].ToolCalls)
		assert.NotNil(t, msgs[2].Extra[msgClearedFlag])
		assert.Equal(t, []schema.ToolCall{
			{
				ID:       "call_123456789",
				Type:     "function",
				Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
			},
		}, s.Messages[4].ToolCalls)
		assert.NotNil(t, msgs[4].Extra[msgClearedFlag])
		assert.Equal(t, "<persisted-output>Tool result saved to: /tmp/clear/call_987654321\nUse read_file to view</persisted-output>", s.Messages[3].Content)
		assert.Equal(t, "<persisted-output>Tool result saved to: /tmp/clear/call_123456789\nUse read_file to view</persisted-output>", s.Messages[5].Content)
	})

	t.Run("test ClearExcludeTools", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation:            true,
			TokenCounter:              defaultTokenCounter,
			MaxTokensForClear:         20,
			ClearRetentionSuffixLimit: 0,
			ClearExcludeTools:         []string{"get_important_data"},
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
				"get_important_data": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		msgs := []adk.Message{
			schema.SystemMessage("you are a helpful assistant"),
			schema.UserMessage("If it's warmer than 20°C in London, set the thermostat to 20°C, otherwise set it to 18°C."),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_987654321",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny", "call_987654321"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_123456789",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_important_data", Arguments: `{"id": "123"}`},
				},
			}),
			schema.ToolMessage("Important Data Content", "call_123456789"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_999",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny", "call_999"),
		}
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: msgs}, &adk.ModelContext{Tools: toolsInfo})
		assert.NoError(t, err)
		assert.Equal(t, "<persisted-output>Tool result saved to: /tmp/clear/call_987654321\nUse read_file to view</persisted-output>", s.Messages[3].Content)
		assert.Equal(t, "Important Data Content", s.Messages[5].Content)
		b, err := backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/clear/call_987654321",
		})
		assert.NoError(t, err)
		assert.Equal(t, "Sunny", b.Content)
		b, err = backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/clear/call_123456789",
		})
		assert.Error(t, err)
		assert.Equal(t, "file not found: /tmp/clear/call_123456789", err.Error())
	})

	t.Run("test ClearAtLeastTokens - not enough tokens cleared", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation: true,
			TokenCounter: func(_ context.Context, msgs []adk.Message, _ []*schema.ToolInfo) (int64, error) {
				var size int
				for _, msg := range msgs {
					size += len(msg.Content)
					for _, tc := range msg.ToolCalls {
						size += len(tc.Function.Name)
						size += len(tc.Function.Arguments)
					}
				}
				return int64(size), nil
			},
			MaxTokensForClear:         50,
			ClearRetentionSuffixLimit: 1,
			ClearAtLeastTokens:        100,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
				"get_important_data": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		msgs := []adk.Message{
			schema.SystemMessage("you are a helpful assistant"),
			schema.UserMessage("If it's warmer than 20°C in London, set the thermostat to 20°C, otherwise set it to 18°C."),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_987654321",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny", "call_123456789"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_123456789",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_important_data", Arguments: `{"id": "123"}`},
				},
			}),
			schema.ToolMessage("Important Data Content, qweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqwe", "call_123456789"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_999",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny", "call_999"),
		}
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: msgs}, &adk.ModelContext{Tools: toolsInfo})
		assert.NoError(t, err)
		assert.Equal(t, "Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny", s.Messages[3].Content)
		assert.Equal(t, "Important Data Content, qweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqwe", s.Messages[5].Content)
		_, err = backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/clear/call_987654321",
		})
		assert.Error(t, err)
	})

	t.Run("test ClearAtLeastTokens - enough tokens cleared", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			SkipTruncation: true,
			TokenCounter: func(_ context.Context, msgs []adk.Message, _ []*schema.ToolInfo) (int64, error) {
				var size int
				for _, msg := range msgs {
					size += len(msg.Content)
					for _, tc := range msg.ToolCalls {
						size += len(tc.Function.Name)
						size += len(tc.Function.Arguments)
					}
				}
				return int64(size), nil
			},
			MaxTokensForClear:         50,
			ClearRetentionSuffixLimit: 1,
			ClearAtLeastTokens:        10,
			ToolConfig: map[string]*ToolReductionConfig{
				"get_weather": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
				"get_important_data": {
					Backend:      backend,
					SkipClear:    false,
					ClearHandler: defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file"),
				},
			},
		}

		mw, err := New(ctx, config)
		assert.NoError(t, err)
		msgs := []adk.Message{
			schema.SystemMessage("you are a helpful assistant"),
			schema.UserMessage("If it's warmer than 20°C in London, set the thermostat to 20°C, otherwise set it to 18°C."),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_987654321",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny Sunny", "call_123456789"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_123456789",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_important_data", Arguments: `{"id": "123"}`},
				},
			}),
			schema.ToolMessage("Important Data Content, qweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqweqwe", "call_123456789"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:       "call_999",
					Type:     "function",
					Function: schema.FunctionCall{Name: "get_weather", Arguments: `{"location": "London, UK", "unit": "c"}`},
				},
			}),
			schema.ToolMessage("Sunny", "call_999"),
		}
		_, s, err := mw.BeforeModelRewriteState(ctx, &adk.ChatModelAgentState{Messages: msgs}, &adk.ModelContext{Tools: toolsInfo})
		assert.NoError(t, err)
		assert.Equal(t, "<persisted-output>Tool result saved to: /tmp/clear/call_987654321\nUse read_file to view</persisted-output>", s.Messages[3].Content)
		_, err = backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/clear/call_987654321",
		})
		assert.NoError(t, err)
		assert.Equal(t, "<persisted-output>Tool result saved to: /tmp/clear/call_123456789\nUse read_file to view</persisted-output>", s.Messages[5].Content)
		_, err = backend.Read(ctx, &filesystem.ReadRequest{
			FilePath: "/tmp/clear/call_987654321",
		})
		assert.NoError(t, err)
	})
}

func TestGetJointToolResult(t *testing.T) {
	t.Run("test with ToolResult", func(t *testing.T) {
		detail := &ToolDetail{
			ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "hello world"}}},
		}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.NoError(t, err)
		assert.True(t, needProcess)
		assert.Len(t, toolOutputParts, 1)
		assert.Equal(t, schema.ToolPartTypeText, toolOutputParts[0].Type)
		assert.Equal(t, "hello world", toolOutputParts[0].Text)
	})

	t.Run("test with multiple ToolResult parts", func(t *testing.T) {
		detail := &ToolDetail{
			ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: "hello "},
				{Type: schema.ToolPartTypeText, Text: "world"},
			}},
		}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.NoError(t, err)
		assert.True(t, needProcess)
		assert.Len(t, toolOutputParts, 2)
		assert.Equal(t, schema.ToolPartTypeText, toolOutputParts[0].Type)
		assert.Equal(t, "hello ", toolOutputParts[0].Text)
		assert.Equal(t, schema.ToolPartTypeText, toolOutputParts[1].Type)
		assert.Equal(t, "world", toolOutputParts[1].Text)
	})

	t.Run("test with empty ToolResult parts", func(t *testing.T) {
		detail := &ToolDetail{
			ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{}},
		}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.NoError(t, err)
		assert.False(t, needProcess)
		assert.Nil(t, toolOutputParts)
	})

	t.Run("test with ToolResult multimodal", func(t *testing.T) {
		detail := &ToolDetail{
			ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeImage, Text: "https://example.com/image.png"}}},
		}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.NoError(t, err)
		assert.True(t, needProcess)
		assert.Len(t, toolOutputParts, 1)
		assert.Equal(t, schema.ToolPartTypeImage, toolOutputParts[0].Type)
		assert.Equal(t, "https://example.com/image.png", toolOutputParts[0].Text)
	})

	t.Run("test with StreamToolResult", func(t *testing.T) {
		sr, sw := schema.Pipe[*schema.ToolResult](10)
		go func() {
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "hello "}}}, nil)
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "world"}}}, nil)
			sw.Close()
		}()

		detail := &ToolDetail{
			StreamToolResult: sr,
		}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.NoError(t, err)
		assert.True(t, needProcess)
		assert.Len(t, toolOutputParts, 1)
		assert.Equal(t, schema.ToolPartTypeText, toolOutputParts[0].Type)
		assert.Equal(t, "hello world", toolOutputParts[0].Text)
	})

	t.Run("test with StreamToolResult error", func(t *testing.T) {
		sr, sw := schema.Pipe[*schema.ToolResult](10)
		go func() {
			sw.Send(nil, fmt.Errorf("stream error"))
			sw.Close()
		}()

		detail := &ToolDetail{
			StreamToolResult: sr,
		}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.NoError(t, err)
		assert.False(t, needProcess)
		assert.Nil(t, toolOutputParts)
	})

	t.Run("test with both ToolResult and StreamToolResult nil (should error)", func(t *testing.T) {
		detail := &ToolDetail{}
		toolOutputParts, needProcess, err := getJointToolResult(detail)
		assert.Error(t, err)
		assert.False(t, needProcess)
		assert.Nil(t, toolOutputParts)
	})
}

func TestDefaultTruncHandlerWithStreamToolResult(t *testing.T) {
	ctx := context.Background()

	t.Run("test short stream result no trunc", func(t *testing.T) {
		sr, sw := schema.Pipe[*schema.ToolResult](10)
		go func() {
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "short text"}}}, nil)
			sw.Close()
		}()

		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				Name:   "test",
				CallID: "call_id",
			},
			ToolArgument:     &schema.ToolArgument{Text: "{}"},
			StreamToolResult: sr,
		}

		fn := defaultTruncHandler(testTruncOffloadPath("/tmp"), 100)
		result, err := fn(ctx, detail)
		assert.NoError(t, err)
		assert.False(t, result.NeedTrunc)
	})

	t.Run("test long stream result need trunc", func(t *testing.T) {
		sr, sw := schema.Pipe[*schema.ToolResult](10)
		go func() {
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: strings.Repeat("hello world", 20)}}}, nil)
			sw.Close()
		}()

		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				Name:   "test",
				CallID: "call_id",
			},
			ToolArgument:     &schema.ToolArgument{Text: "{}"},
			StreamToolResult: sr,
		}

		fn := defaultTruncHandler(testTruncOffloadPath("/tmp"), 100)
		result, err := fn(ctx, detail)
		assert.NoError(t, err)
		assert.True(t, result.NeedTrunc)
	})

	t.Run("test gen offload file path error", func(t *testing.T) {
		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				Name:   "test",
				CallID: "call_id",
			},
			ToolArgument: &schema.ToolArgument{Text: "{}"},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: strings.Repeat("x", 1000)}},
			},
		}

		fn := defaultTruncHandler(func(context.Context, *ToolDetail) (string, error) {
			return "", fmt.Errorf("gen path error")
		}, 10)
		_, err := fn(ctx, detail)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "gen path error")
	})
}

func TestDefaultClearHandlerWithStreamToolResult(t *testing.T) {
	ctx := context.Background()

	t.Run("test with stream result need offload", func(t *testing.T) {
		sr, sw := schema.Pipe[*schema.ToolResult](10)
		go func() {
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "streaming content"}}}, nil)
			sw.Close()
		}()

		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				Name:   "test",
				CallID: "stream_call_id",
			},
			ToolArgument:     &schema.ToolArgument{Text: "{}"},
			StreamToolResult: sr,
		}

		fn := defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file")
		result, err := fn(ctx, detail)
		assert.NoError(t, err)
		assert.True(t, result.NeedClear)
		assert.True(t, result.NeedOffload)
		assert.Equal(t, "streaming content", result.OffloadContent)
	})

	t.Run("test gen offload file path error", func(t *testing.T) {
		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				Name:   "test",
				CallID: "call_id",
			},
			ToolArgument: &schema.ToolArgument{Text: "{}"},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "content"}},
			},
		}
		fn := defaultClearHandler(func(context.Context, *ToolDetail) (string, error) {
			return "", fmt.Errorf("gen path error")
		}, true, "read_file")
		_, err := fn(ctx, detail)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "gen path error")
	})
}

func TestDefaultOffloadHandler(t *testing.T) {
	ctx := context.Background()
	detail := &ToolDetail{
		ToolContext: &adk.ToolContext{
			Name:   "mock_name",
			CallID: "mock_call_id_12345",
		},
		ToolArgument: &schema.ToolArgument{Text: "anything"},
		ToolResult:   &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "hello"}}},
	}

	fn := defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file")
	info, err := fn(ctx, detail)
	assert.NoError(t, err)
	assert.Equal(t, &ClearResult{
		ToolArgument: &schema.ToolArgument{Text: "anything"},
		ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{
			{
				Type: schema.ToolPartTypeText,
				Text: "<persisted-output>Tool result saved to: /tmp/clear/mock_call_id_12345\nUse read_file to view</persisted-output>",
			},
		}},
		NeedClear:       true,
		NeedOffload:     true,
		OffloadFilePath: "/tmp/clear/mock_call_id_12345",
		OffloadContent:  "hello",
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

func mockStreamableToolWithError() tool.StreamableTool {
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
		sw.Send("", fmt.Errorf("mock error"))
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

func toJson(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestToolResultFromMessage(t *testing.T) {
	t.Run("test from content", func(t *testing.T) {
		msg := schema.ToolMessage("test content", "call_123")
		result, fromContent, err := toolResultFromMessage(msg)
		assert.NoError(t, err)
		assert.True(t, fromContent)
		assert.NotNil(t, result)
		assert.Len(t, result.Parts, 1)
		assert.Equal(t, schema.ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, "test content", result.Parts[0].Text)
	})

	t.Run("test from user input multi content", func(t *testing.T) {
		msg := schema.ToolMessage("", "call_456")
		msg.UserInputMultiContent = []schema.MessageInputPart{
			{
				Type: schema.ChatMessagePartTypeText,
				Text: "test text",
			},
		}
		result, fromContent, err := toolResultFromMessage(msg)
		assert.NoError(t, err)
		assert.False(t, fromContent)
		assert.NotNil(t, result)
		assert.Len(t, result.Parts, 1)
		assert.Equal(t, schema.ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, "test text", result.Parts[0].Text)
	})

	t.Run("test invalid role", func(t *testing.T) {
		msg := schema.UserMessage("test user message")
		_, _, err := toolResultFromMessage(msg)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "message role")
	})
}

func TestConvMessageInputPartToToolOutputPart(t *testing.T) {
	t.Run("test text type", func(t *testing.T) {
		part := schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeText,
			Text: "test text",
		}
		result, err := convMessageInputPartToToolOutputPart(part)
		assert.NoError(t, err)
		assert.Equal(t, schema.ToolPartTypeText, result.Type)
		assert.Equal(t, "test text", result.Text)
	})

	t.Run("test image url type", func(t *testing.T) {
		part := schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{},
		}
		result, err := convMessageInputPartToToolOutputPart(part)
		assert.NoError(t, err)
		assert.Equal(t, schema.ToolPartTypeImage, result.Type)
		assert.NotNil(t, result.Image)
	})

	t.Run("test audio url type", func(t *testing.T) {
		part := schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeAudioURL,
			Audio: &schema.MessageInputAudio{},
		}
		result, err := convMessageInputPartToToolOutputPart(part)
		assert.NoError(t, err)
		assert.Equal(t, schema.ToolPartTypeAudio, result.Type)
		assert.NotNil(t, result.Audio)
	})

	t.Run("test video url type", func(t *testing.T) {
		part := schema.MessageInputPart{
			Type:  schema.ChatMessagePartTypeVideoURL,
			Video: &schema.MessageInputVideo{},
		}
		result, err := convMessageInputPartToToolOutputPart(part)
		assert.NoError(t, err)
		assert.Equal(t, schema.ToolPartTypeVideo, result.Type)
		assert.NotNil(t, result.Video)
	})

	t.Run("test file url type", func(t *testing.T) {
		part := schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeFileURL,
			File: &schema.MessageInputFile{},
		}
		result, err := convMessageInputPartToToolOutputPart(part)
		assert.NoError(t, err)
		assert.Equal(t, schema.ToolPartTypeFile, result.Type)
		assert.NotNil(t, result.File)
	})

	t.Run("test unknown type", func(t *testing.T) {
		part := schema.MessageInputPart{
			Type: "unknown_type",
		}
		_, err := convMessageInputPartToToolOutputPart(part)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "unknown msg part type")
	})
}

func TestGetSetMsgOffloadedFlag(t *testing.T) {
	t.Run("test get offloaded flag - not set", func(t *testing.T) {
		msg := schema.UserMessage("test")
		assert.False(t, getMsgClearedFlag(msg))
	})

	t.Run("test get offloaded flag - set", func(t *testing.T) {
		msg := schema.UserMessage("test")
		setMsgClearedFlag(msg)
		assert.True(t, getMsgClearedFlag(msg))
	})

	t.Run("test set offloaded flag - nil extra", func(t *testing.T) {
		msg := schema.UserMessage("test")
		setMsgClearedFlag(msg)
		assert.True(t, getMsgClearedFlag(msg))
	})

	t.Run("test set offloaded flag - existing extra", func(t *testing.T) {
		msg := schema.UserMessage("test")
		msg.Extra = map[string]any{"existing": "value"}
		setMsgClearedFlag(msg)
		assert.True(t, getMsgClearedFlag(msg))
		assert.Equal(t, "value", msg.Extra["existing"])
	})
}

func TestNewErrors(t *testing.T) {
	ctx := context.Background()

	t.Run("test nil config", func(t *testing.T) {
		_, err := New(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config must not be nil")
	})

	t.Run("test no backend when not skipping truncation", func(t *testing.T) {
		config := &Config{
			Backend:        nil,
			SkipTruncation: false,
		}
		_, err := New(ctx, config)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend must be set")
	})
}

func TestGetToolConfig(t *testing.T) {
	ctx := context.Background()
	backend := filesystem.NewInMemoryBackend()

	t.Run("test no tool config", func(t *testing.T) {
		config := &Config{
			Backend:        backend,
			SkipTruncation: true,
			SkipClear:      true,
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)
		trmw, ok := mw.(*toolReductionMiddleware)
		assert.True(t, ok)

		cfg := trmw.getToolConfig("non_existent_tool", sceneTruncation)
		assert.NotNil(t, cfg)
	})

	t.Run("test with tool config", func(t *testing.T) {
		config := &Config{
			Backend:        backend,
			SkipTruncation: true,
			SkipClear:      true,
			ToolConfig: map[string]*ToolReductionConfig{
				"test_tool": {
					SkipTruncation: true,
					SkipClear:      true,
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)
		trmw, ok := mw.(*toolReductionMiddleware)
		assert.True(t, ok)

		cfg := trmw.getToolConfig("test_tool", sceneTruncation)
		assert.NotNil(t, cfg)
		assert.True(t, cfg.SkipTruncation)
	})

	t.Run("test with tool config needing default handler", func(t *testing.T) {
		config := &Config{
			Backend:        backend,
			SkipTruncation: false,
			ToolConfig: map[string]*ToolReductionConfig{
				"test_tool": {
					SkipTruncation: false,
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)
		trmw, ok := mw.(*toolReductionMiddleware)
		assert.True(t, ok)

		cfg := trmw.getToolConfig("test_tool", sceneTruncation)
		assert.NotNil(t, cfg)
		assert.NotNil(t, cfg.TruncHandler)
	})
}

func TestCopyAndFillDefaults(t *testing.T) {
	t.Run("test empty config", func(t *testing.T) {
		cfg := &Config{}
		result, err := cfg.copyAndFillDefaults()
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Equal(t, "/tmp", result.RootDir)
		assert.Equal(t, "read_file", result.ReadFileToolName)
		assert.Equal(t, 50000, result.MaxLengthForTrunc)
		assert.Equal(t, 1, result.ClearRetentionSuffixLimit)
		assert.NotNil(t, result.TokenCounter)
		assert.NotNil(t, result.GenTruncOffloadFilePath)
		assert.NotNil(t, result.GenClearOffloadFilePath)

		truncPath, err := result.GenTruncOffloadFilePath(context.Background(), &ToolDetail{
			ToolContext: &adk.ToolContext{CallID: "call_1"},
		})
		assert.NoError(t, err)
		assert.Equal(t, "/tmp/trunc/call_1", truncPath)

		clearPath, err := result.GenClearOffloadFilePath(context.Background(), &ToolDetail{
			ToolContext: &adk.ToolContext{CallID: "call_1"},
		})
		assert.NoError(t, err)
		assert.Equal(t, "/tmp/clear/call_1", clearPath)
	})

	t.Run("test default offload path generators create uuid when CallID is empty", func(t *testing.T) {
		cfg := &Config{}
		result, err := cfg.copyAndFillDefaults()
		assert.NoError(t, err)

		truncPath, err := result.GenTruncOffloadFilePath(context.Background(), &ToolDetail{
			ToolContext: &adk.ToolContext{CallID: ""},
		})
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(truncPath, "/tmp/trunc/"))
		assert.NotEqual(t, "/tmp/trunc/", truncPath)

		clearPath, err := result.GenClearOffloadFilePath(context.Background(), &ToolDetail{
			ToolContext: &adk.ToolContext{CallID: ""},
		})
		assert.NoError(t, err)
		assert.True(t, strings.HasPrefix(clearPath, "/tmp/clear/"))
		assert.NotEqual(t, "/tmp/clear/", clearPath)
	})

	t.Run("test with tool config", func(t *testing.T) {
		cfg := &Config{
			ToolConfig: map[string]*ToolReductionConfig{
				"test_tool": {
					SkipTruncation: true,
				},
			},
		}
		result, err := cfg.copyAndFillDefaults()
		assert.NoError(t, err)
		assert.NotNil(t, result.ToolConfig)
		assert.True(t, result.ToolConfig["test_tool"].SkipTruncation)
	})

	t.Run("test custom offload path generators preserved", func(t *testing.T) {
		cfg := &Config{
			RootDir: "/tmp/ignored",
			GenTruncOffloadFilePath: func(_ context.Context, detail *ToolDetail) (string, error) {
				return "/custom/trunc/" + detail.ToolContext.CallID, nil
			},
			GenClearOffloadFilePath: func(_ context.Context, detail *ToolDetail) (string, error) {
				return "/custom/clear/" + detail.ToolContext.CallID, nil
			},
		}
		result, err := cfg.copyAndFillDefaults()
		assert.NoError(t, err)

		filePath, err := result.GenClearOffloadFilePath(context.Background(), &ToolDetail{
			ToolContext: &adk.ToolContext{CallID: "call_custom"},
		})
		assert.NoError(t, err)
		assert.Equal(t, "/custom/clear/call_custom", filePath)
	})
}

func TestDefaultTokenCounter(t *testing.T) {
	ctx := context.Background()

	t.Run("test with nil messages", func(t *testing.T) {
		msgs := []*schema.Message{nil}
		tokens, err := defaultTokenCounter(ctx, msgs, nil)
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, tokens, int64(0))
	})

	t.Run("test with tool info", func(t *testing.T) {
		toolInfo := &schema.ToolInfo{
			Name: "test_tool",
			Desc: "test description",
		}
		tokens, err := defaultTokenCounter(ctx, nil, []*schema.ToolInfo{toolInfo})
		assert.NoError(t, err)
		assert.GreaterOrEqual(t, tokens, int64(0))
	})
}

func TestDefaultClearHandler(t *testing.T) {
	ctx := context.Background()

	t.Run("test empty parts", func(t *testing.T) {
		handler := defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file")
		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				CallID: "test_call",
			},
			ToolResult: &schema.ToolResult{Parts: []schema.ToolOutputPart{}},
		}
		result, err := handler(ctx, detail)
		assert.NoError(t, err)
		assert.False(t, result.NeedClear)
	})

	t.Run("test multimodal content", func(t *testing.T) {
		handler := defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file")
		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{
				CallID: "test_call",
			},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeImage}},
			},
		}
		result, err := handler(ctx, detail)
		assert.NoError(t, err)
		assert.Equal(t, detail.ToolResult, result.ToolResult)
	})

	t.Run("test no call id", func(t *testing.T) {
		handler := defaultClearHandler(testClearOffloadPath("/tmp"), true, "read_file")
		detail := &ToolDetail{
			ToolContext: &adk.ToolContext{},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "test"}},
			},
		}
		result, err := handler(ctx, detail)
		assert.NoError(t, err)
		assert.True(t, result.NeedClear)
		assert.Equal(t, "/tmp/clear/generated", result.OffloadFilePath)
	})
}

func TestStringifyToolOutputParts(t *testing.T) {
	t.Run("test empty parts", func(t *testing.T) {
		result := stringifyToolOutputParts([]schema.ToolOutputPart{})
		assert.Equal(t, "", result)
	})

	t.Run("test single text part", func(t *testing.T) {
		result := stringifyToolOutputParts([]schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "hello world"},
		})
		assert.Equal(t, "hello world", result)
	})

	t.Run("test multiple text parts", func(t *testing.T) {
		result := stringifyToolOutputParts([]schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "hello "},
			{Type: schema.ToolPartTypeText, Text: "world"},
		})
		expected := `[
	{
		"type": "text",
		"text": "hello "
	},
	{
		"type": "text",
		"text": "world"
	}
]`
		assert.JSONEq(t, expected, result)
	})

	t.Run("test with image part", func(t *testing.T) {
		result := stringifyToolOutputParts([]schema.ToolOutputPart{
			{Type: schema.ToolPartTypeImage, Text: "https://example.com/image.png"},
		})
		expected := `[
	{
		"type": "image",
		"text": "https://example.com/image.png"
	}
]`
		assert.JSONEq(t, expected, result)
	})

	t.Run("test mixed parts", func(t *testing.T) {
		result := stringifyToolOutputParts([]schema.ToolOutputPart{
			{Type: schema.ToolPartTypeText, Text: "hello world"},
			{Type: schema.ToolPartTypeImage, Text: "https://example.com/image.png"},
		})
		expected := `[
	{
		"type": "text",
		"text": "hello world"
	},
	{
		"type": "image",
		"text": "https://example.com/image.png"
	}
]`
		assert.JSONEq(t, expected, result)
	})
}

func TestClampPrefixToUTF8Boundary(t *testing.T) {
	t.Run("test n <= 0", func(t *testing.T) {
		result := clampPrefixToUTF8Boundary("hello world", 0)
		assert.Equal(t, "", result)

		result = clampPrefixToUTF8Boundary("hello world", -5)
		assert.Equal(t, "", result)
	})

	t.Run("test n >= len(s)", func(t *testing.T) {
		result := clampPrefixToUTF8Boundary("hello", 10)
		assert.Equal(t, "hello", result)
	})

	t.Run("test ASCII string at rune boundary", func(t *testing.T) {
		result := clampPrefixToUTF8Boundary("hello world", 5)
		assert.Equal(t, "hello", result)
	})

	t.Run("test ASCII string not at rune boundary (should not happen)", func(t *testing.T) {
		result := clampPrefixToUTF8Boundary("world", 3)
		assert.Equal(t, "wor", result)
	})

	t.Run("test multi-byte UTF-8 characters at boundary", func(t *testing.T) {
		s := "你好世界"
		result := clampPrefixToUTF8Boundary(s, 3)
		assert.Equal(t, "你", result)
	})

	t.Run("test multi-byte UTF-8 characters not at boundary", func(t *testing.T) {
		s := "你好世界"
		result := clampPrefixToUTF8Boundary(s, 2)
		assert.Equal(t, "", result)

		result = clampPrefixToUTF8Boundary(s, 4)
		assert.Equal(t, "你", result)
	})

	t.Run("test mixed ASCII and multi-byte characters", func(t *testing.T) {
		s := "hello你好world"
		result := clampPrefixToUTF8Boundary(s, 5+3)
		assert.Equal(t, "hello你", result)

		result = clampPrefixToUTF8Boundary(s, 5+4)
		assert.Equal(t, "hello你", result)

		result = clampPrefixToUTF8Boundary(s, 5+6)
		assert.Equal(t, "hello你好", result)
	})
}

func TestClampSuffixToUTF8Boundary(t *testing.T) {
	t.Run("test n <= 0", func(t *testing.T) {
		result := clampSuffixToUTF8Boundary("hello world", 0)
		assert.Equal(t, "", result)

		result = clampSuffixToUTF8Boundary("hello world", -5)
		assert.Equal(t, "", result)
	})

	t.Run("test n >= len(s)", func(t *testing.T) {
		result := clampSuffixToUTF8Boundary("hello", 10)
		assert.Equal(t, "hello", result)
	})

	t.Run("test ASCII string at rune boundary", func(t *testing.T) {
		result := clampSuffixToUTF8Boundary("hello world", 5)
		assert.Equal(t, "world", result)
	})

	t.Run("test ASCII string not at rune boundary (should not happen)", func(t *testing.T) {
		result := clampSuffixToUTF8Boundary("hello", 3)
		assert.Equal(t, "llo", result)
	})

	t.Run("test multi-byte UTF-8 characters at boundary", func(t *testing.T) {
		s := "你好世界"
		assert.Equal(t, 12, len(s))
		result := clampSuffixToUTF8Boundary(s, 3)
		assert.Equal(t, "界", result)
	})

	t.Run("test multi-byte UTF-8 characters not at boundary", func(t *testing.T) {
		s := "你好世界"
		result := clampSuffixToUTF8Boundary(s, 2)
		assert.Equal(t, "", result)

		result = clampSuffixToUTF8Boundary(s, 4)
		assert.Equal(t, "界", result)

		result = clampSuffixToUTF8Boundary(s, 6)
		assert.Equal(t, "世界", result)
	})

	t.Run("test mixed ASCII and multi-byte characters", func(t *testing.T) {
		s := "hello你好world"
		result := clampSuffixToUTF8Boundary(s, 5+3)
		assert.Equal(t, "好world", result)

		result = clampSuffixToUTF8Boundary(s, 5+4)
		assert.Equal(t, "好world", result)

		result = clampSuffixToUTF8Boundary(s, 5+6)
		assert.Equal(t, "你好world", result)
	})
}

func TestGetMsgClearedFlag(t *testing.T) {
	t.Run("test non-bool value in extra", func(t *testing.T) {
		msg := schema.UserMessage("test")
		msg.Extra = map[string]any{msgClearedFlag: "not a bool"}
		flag := getMsgClearedFlag(msg)
		assert.False(t, flag)
	})
}

func TestCopyMessages(t *testing.T) {
	t.Run("test empty messages", func(t *testing.T) {
		result := copyMessages(nil)
		assert.Len(t, result, 0)

		result = copyMessages([]*schema.Message{})
		assert.Len(t, result, 0)
	})

	t.Run("test message with tool calls and extra", func(t *testing.T) {
		msg := schema.AssistantMessage("", []schema.ToolCall{
			{
				ID:       "call_123",
				Type:     "function",
				Function: schema.FunctionCall{Name: "test_func", Arguments: `{"key": "value"}`},
			},
		})
		msg.Extra = map[string]any{"test_key": "test_value"}
		msg.UserInputMultiContent = []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "test text"},
		}
		msg.MultiContent = []schema.ChatMessagePart{
			{Type: schema.ChatMessagePartTypeText, Text: "multi content"},
		}
		msg.AssistantGenMultiContent = []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "assistant gen"},
		}

		result := copyMessages([]*schema.Message{msg})
		assert.Len(t, result, 1)
		assert.NotEqual(t, &msg, result[0])
		assert.Len(t, result[0].ToolCalls, 1)
		assert.Equal(t, "call_123", result[0].ToolCalls[0].ID)
		assert.Equal(t, "test_value", result[0].Extra["test_key"])
		assert.Len(t, result[0].UserInputMultiContent, 1)
		assert.Len(t, result[0].MultiContent, 1)
		assert.Len(t, result[0].AssistantGenMultiContent, 1)
	})
}

func TestReductionMiddlewareEnhancedTrunc(t *testing.T) {
	ctx := context.Background()

	t.Run("enhanced invokable - passthrough when truncation skipped", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:        backend,
			SkipTruncation: true,
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}

		called := 0
		endpoint := func(_ context.Context, ta *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			called++
			assert.Same(t, toolArg, ta)
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "ok"}}}, nil
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		got, err := wrapped(ctx, toolArg)
		assert.NoError(t, err)
		assert.Equal(t, 1, called)
		assert.Equal(t, "ok", got.Parts[0].Text)
	})

	t.Run("enhanced invokable - excluded tool bypasses truncation", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend:           backend,
			TruncExcludeTools: []string{"enh_tool"},
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						t.Fatalf("trunc handler should not be called when tool is excluded")
						return nil, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		got, err := wrapped(ctx, toolArg)
		assert.NoError(t, err)
		assert.Equal(t, "orig", got.Parts[0].Text)
	})

	t.Run("enhanced invokable - endpoint error returned", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		wantErr := errors.New("endpoint error")

		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						t.Fatalf("trunc handler should not be called on endpoint error")
						return nil, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			return nil, wantErr
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		_, err = wrapped(ctx, toolArg)
		assert.Equal(t, wantErr, err)
	})

	t.Run("enhanced invokable - trunc handler error returned", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		wantErr := errors.New("handler error")
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						return nil, wantErr
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		_, err = wrapped(ctx, toolArg)
		assert.Equal(t, wantErr, err)
	})

	t.Run("enhanced invokable - no trunc returns original result", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, detail *ToolDetail) (*TruncResult, error) {
						assert.NotNil(t, detail.ToolResult)
						assert.Equal(t, "orig", detail.ToolResult.Parts[0].Text)
						return &TruncResult{NeedTrunc: false}, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		got, err := wrapped(ctx, toolArg)
		assert.NoError(t, err)
		assert.Equal(t, "orig", got.Parts[0].Text)
	})

	t.Run("enhanced invokable - offload without backend returns error", func(t *testing.T) {
		// config.Backend must be non-nil to pass New(), but the per-tool cfg.Backend is nil.
		configBackend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: configBackend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_tool": {
					Backend:        nil,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						return &TruncResult{
							NeedTrunc:       true,
							NeedOffload:     true,
							OffloadFilePath: "/tmp/trunc/cid",
							OffloadContent:  "big",
							ToolResult:      &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "trunc"}}},
						}, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		_, err = wrapped(ctx, toolArg)
		assert.EqualError(t, err, "truncation: no backend for offload")
	})

	t.Run("enhanced invokable - trunc + offload writes and returns trunc result", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, detail *ToolDetail) (*TruncResult, error) {
						assert.NotNil(t, detail.ToolContext)
						assert.NotNil(t, detail.ToolArgument)
						assert.NotNil(t, detail.ToolResult)
						return &TruncResult{
							NeedTrunc:       true,
							NeedOffload:     true,
							OffloadFilePath: "/tmp/trunc/cid",
							OffloadContent:  "full content",
							ToolResult:      &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "truncated"}}},
						}, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_tool", CallID: "cid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, ta *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
			assert.Same(t, toolArg, ta)
			return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil
		}

		wrapped, err := mw.WrapEnhancedInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		got, err := wrapped(ctx, toolArg)
		assert.NoError(t, err)
		assert.Equal(t, "truncated", got.Parts[0].Text)

		content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/cid"})
		assert.NoError(t, err)
		assert.Equal(t, "full content", content.Content)
	})

	t.Run("enhanced streamable - no trunc returns original stream (Copy semantics)", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_stream_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, detail *ToolDetail) (*TruncResult, error) {
						// Consume the "analysis" copy; the returned reader should still have the full stream.
						parts, needProcess, err := getJointToolResult(detail)
						assert.NoError(t, err)
						assert.True(t, needProcess)
						assert.Len(t, parts, 1)
						assert.Equal(t, "hello world", parts[0].Text)
						return &TruncResult{NeedTrunc: false}, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_stream_tool", CallID: "scid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			sr, sw := schema.Pipe[*schema.ToolResult](4)
			go func() {
				sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "hello "}}}, nil)
				sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "world"}}}, nil)
				sw.Close()
			}()
			return sr, nil
		}

		wrapped, err := mw.WrapEnhancedStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		resp, err := wrapped(ctx, toolArg)
		assert.NoError(t, err)
		defer resp.Close()

		var chunks []*schema.ToolResult
		for {
			tr, recvErr := resp.Recv()
			if recvErr != nil {
				assert.Equal(t, io.EOF, recvErr)
				break
			}
			chunks = append(chunks, tr)
		}
		joined, err := schema.ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Equal(t, "hello world", joined.Parts[0].Text)
	})

	t.Run("enhanced streamable - trunc + offload writes and returns trunc stream", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_stream_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						sr, sw := schema.Pipe[*schema.ToolResult](1)
						sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "truncated"}}}, nil)
						sw.Close()
						return &TruncResult{
							NeedTrunc:        true,
							NeedOffload:      true,
							OffloadFilePath:  "/tmp/trunc/scid",
							OffloadContent:   "full content",
							StreamToolResult: sr,
						}, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_stream_tool", CallID: "scid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			sr, sw := schema.Pipe[*schema.ToolResult](2)
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil)
			sw.Close()
			return sr, nil
		}

		wrapped, err := mw.WrapEnhancedStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		resp, err := wrapped(ctx, toolArg)
		assert.NoError(t, err)
		defer resp.Close()

		tr, err := resp.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "truncated", tr.Parts[0].Text)

		_, err = resp.Recv()
		assert.Equal(t, io.EOF, err)

		content, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: "/tmp/trunc/scid"})
		assert.NoError(t, err)
		assert.Equal(t, "full content", content.Content)
	})

	t.Run("enhanced streamable - endpoint error returned", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		wantErr := errors.New("endpoint error")
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_stream_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						t.Fatalf("trunc handler should not be called on endpoint error")
						return nil, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_stream_tool", CallID: "scid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			return nil, wantErr
		}

		wrapped, err := mw.WrapEnhancedStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		_, err = wrapped(ctx, toolArg)
		assert.Equal(t, wantErr, err)
	})

	t.Run("enhanced streamable - trunc handler error returned", func(t *testing.T) {
		backend := filesystem.NewInMemoryBackend()
		wantErr := errors.New("handler error")
		config := &Config{
			Backend: backend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_stream_tool": {
					Backend:        backend,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						return nil, wantErr
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_stream_tool", CallID: "scid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			sr, sw := schema.Pipe[*schema.ToolResult](1)
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil)
			sw.Close()
			return sr, nil
		}

		wrapped, err := mw.WrapEnhancedStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		_, err = wrapped(ctx, toolArg)
		assert.Equal(t, wantErr, err)
	})

	t.Run("enhanced streamable - offload without backend returns error", func(t *testing.T) {
		// config.Backend must be non-nil to pass New(), but the per-tool cfg.Backend is nil.
		configBackend := filesystem.NewInMemoryBackend()
		config := &Config{
			Backend: configBackend,
			ToolConfig: map[string]*ToolReductionConfig{
				"enh_stream_tool": {
					Backend:        nil,
					SkipTruncation: false,
					TruncHandler: func(_ context.Context, _ *ToolDetail) (*TruncResult, error) {
						sr, sw := schema.Pipe[*schema.ToolResult](1)
						sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "truncated"}}}, nil)
						sw.Close()
						return &TruncResult{
							NeedTrunc:        true,
							NeedOffload:      true,
							OffloadFilePath:  "/tmp/trunc/scid",
							OffloadContent:   "full content",
							StreamToolResult: sr,
						}, nil
					},
				},
			},
		}
		mw, err := New(ctx, config)
		assert.NoError(t, err)

		tCtx := &adk.ToolContext{Name: "enh_stream_tool", CallID: "scid"}
		toolArg := &schema.ToolArgument{Text: `{"k":"v"}`}
		endpoint := func(_ context.Context, _ *schema.ToolArgument, _ ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
			sr, sw := schema.Pipe[*schema.ToolResult](1)
			sw.Send(&schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "orig"}}}, nil)
			sw.Close()
			return sr, nil
		}

		wrapped, err := mw.WrapEnhancedStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)
		_, err = wrapped(ctx, toolArg)
		assert.EqualError(t, err, "truncation: no backend for offload")
	})
}
