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
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestReductionMiddlewareTrunc(t *testing.T) {
	ctx := context.Background()
	it := mockInvokableTool()
	st := mockStreamableTool()
	tools := []tool.BaseTool{it, st}
	config := &ToolReductionMiddlewareConfig{
		ToolTruncation: &ToolTruncation{
			ToolTruncationConfigMapping: make(map[string]ToolTruncationConfig, len(tools)),
		},
	}
	for _, t := range tools {
		info, _ := t.Info(ctx)
		config.ToolTruncation.ToolTruncationConfigMapping[info.Name] = ToolTruncationConfig{
			MaxLength:     ptrOf(70),
			MaxLineLength: ptrOf(30),
		}
	}

	t.Run("test invokable line and max length trunc", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "mock_invokable_tool",
			CallID: "12345",
		}
		mw := &toolReductionMiddleware{config: config}
		exp := `hello worldhello worldhello wo... (line truncated due to length limitation, 110 chars total)
... (content truncated due to length limitation, 88 chars total)`

		edp, err := mw.WrapInvokableToolCall(ctx, it.InvokableRun, tCtx)
		assert.NoError(t, err)
		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		assert.Equal(t, exp, resp)
	})

	t.Run("test streamable line and max length trunc", func(t *testing.T) {
		tCtx := &adk.ToolContext{
			Name:   "mock_streamable_tool",
			CallID: "12345",
		}
		mw := &toolReductionMiddleware{config: config}
		exp := `hello worldhello worldhello wo... (line truncated due to length limitation, 110 chars total)
... (content truncated due to length limitation, 88 chars total)`

		edp, err := mw.WrapStreamableToolCall(ctx, st.StreamableRun, tCtx)
		assert.NoError(t, err)
		resp, err := edp(ctx, `{"value":"asd"}`)
		assert.NoError(t, err)
		s, err := resp.Recv()
		assert.NoError(t, err)
		resp.Close()
		assert.Equal(t, exp, s)
	})
}

func TestReductionMiddlewareOffload(t *testing.T) {
	ctx := context.Background()
	backend := filesystem.NewInMemoryBackend()
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

	t.Run("test offload", func(t *testing.T) {
		handler := func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error) {
			arguments := make(map[string]string)
			if err := json.Unmarshal([]byte(detail.Input.Arguments), &arguments); err != nil {
				return nil, err
			}
			offloadContent := &OffloadContent{
				Arguments: arguments,
				Result:    detail.Output.Result,
			}
			replacedArguments := make(map[string]string, len(arguments))
			filePath := fmt.Sprintf("/tmp/%s", detail.Input.CallID)
			for k := range arguments {
				replacedArguments[k] = "argument offloaded"
			}
			detail.Input.Arguments = toJson(replacedArguments)
			detail.Output.Result = "result offloaded, retrieve it from " + filePath
			return &OffloadInfo{
				NeedOffload:    true,
				FilePath:       filePath,
				OffloadContent: toJson(offloadContent),
			}, nil
		}

		config := &ToolReductionMiddlewareConfig{
			ToolOffload: &ToolOffload{
				Tokenizer: defaultTokenizer,
				ToolOffloadThreshold: &ToolOffloadThresholdConfig{
					MaxTokens:            20,
					OffloadBatchSize:     1,
					RetentionSuffixLimit: 0,
				},
				ToolOffloadConfigMapping: map[string]ToolOffloadConfig{
					"get_weather":          {OffloadBackend: backend, OffloadHandler: handler},
					"mock_streamable_tool": {OffloadBackend: backend, OffloadHandler: handler},
				},
				ToolOffloadPostProcess: nil,
			},
		}

		mw := &toolReductionMiddleware{config: config}
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
				schema.ToolMessage("Sunny", "call_987654321"),
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

	t.Run("test retention", func(t *testing.T) {

	})
}

func TestDefaultOffloadHandler(t *testing.T) {
	ctx := context.Background()
	detail := &ToolDetail{
		Input: &compose.ToolInput{
			Name:      "mock_name",
			Arguments: "anything",
			CallID:    "mock_call_id_12345",
		},
		Output: &compose.ToolOutput{Result: "hello"},
	}

	fn := defaultOffloadHandler("/tmp")
	info, err := fn(ctx, detail)
	assert.NoError(t, err)
	assert.Equal(t, &OffloadInfo{
		NeedOffload:    true,
		FilePath:       "/tmp/mock_call_id_12345",
		OffloadContent: "hello",
	}, info)

}

func TestNewDefaultToolReductionMiddleware(t *testing.T) {
	ctx := context.Background()
	it := mockInvokableTool()
	st := mockStreamableTool()
	tools := []tool.BaseTool{it, st}
	mw, err := NewDefaultToolReductionMiddleware(ctx, tools)
	assert.NoError(t, err)
	assert.NotNil(t, mw)
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
		sr, sw := schema.Pipe[string](1)
		sw.Send(s3, nil)
		sw.Close()
		return sr, nil
	})
	return t
}

func ptrOf[T any](v T) *T {
	return &v
}

func toJson(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
