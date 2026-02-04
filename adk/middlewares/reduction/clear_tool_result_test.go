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

package reduction

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func Test_reduceByTokens(t *testing.T) {
	type args struct {
		state                    *adk.ChatModelAgentState
		toolResultTokenThreshold int
		keepRecentTokens         int
		placeholder              string
		estimator                func(*schema.Message) int
	}
	tests := []struct {
		name          string
		args          args
		wantErr       assert.ErrorAssertionFunc
		validateState func(*testing.T, *adk.ChatModelAgentState)
	}{
		{
			name: "no reduction when tool result tokens under threshold",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("hello"),
						schema.AssistantMessage("hi", nil),
						schema.ToolMessage("short tool result", "call-1", schema.WithToolName("tool1")),
					},
				},
				toolResultTokenThreshold: 100,
				keepRecentTokens:         500,
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				assert.Equal(t, "short tool result", state.Messages[2].Content)
			},
		},
		{
			name: "clear old tool results when total exceeds threshold",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("msg1"),
						schema.ToolMessage(strings.Repeat("a", 40), "call-1", schema.WithToolName("tool1")), // ~10 tokens (old)
						schema.UserMessage("msg2"),
						schema.ToolMessage(strings.Repeat("b", 40), "call-2", schema.WithToolName("tool2")), // ~10 tokens (old)
						schema.UserMessage("msg3"),
						schema.ToolMessage(strings.Repeat("c", 40), "call-3", schema.WithToolName("tool3")), // ~10 tokens (recent, protected)
					},
				},
				toolResultTokenThreshold: 20,
				keepRecentTokens:         10,
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				assert.Equal(t, "[Old tool result content cleared]", state.Messages[1].Content)
				assert.Equal(t, "[Old tool result content cleared]", state.Messages[3].Content)
				assert.Equal(t, strings.Repeat("c", 40), state.Messages[5].Content)
			},
		},
		{
			name: "protect recent messages even when tool results exceed threshold",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("old msg"),
						schema.ToolMessage(strings.Repeat("x", 100), "call-1", schema.WithToolName("tool1")), // ~25 tokens (old)
						schema.UserMessage("recent msg"),
						schema.ToolMessage(strings.Repeat("x", 100), "call-2", schema.WithToolName("tool2")), // ~25 tokens (recent, protected)
					},
				},
				toolResultTokenThreshold: 10,
				keepRecentTokens:         20,
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				// Total tool result tokens = 50, exceeds threshold of 10
				// But last 200 tokens are protected (includes last 2 messages)
				// So only the first tool result should be cleared
				assert.Equal(t, "[Old tool result content cleared]", state.Messages[1].Content)
				assert.Equal(t, strings.Repeat("x", 100), state.Messages[3].Content)
			},
		},
		{
			name: "custom placeholder text",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("msg"),
						schema.ToolMessage(strings.Repeat("x", 100), "call-1", schema.WithToolName("tool1")),
						schema.UserMessage(strings.Repeat("x", 100)),
					},
				},
				toolResultTokenThreshold: 10,
				keepRecentTokens:         20,
				placeholder:              "[历史工具结果已清除]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				assert.Equal(t, "[历史工具结果已清除]", state.Messages[1].Content)
			},
		},
		{
			name: "no tool messages",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("msg 1"),
						schema.AssistantMessage("response 1", nil),
						schema.UserMessage("msg 2"),
						schema.AssistantMessage("response 2", nil),
					},
				},
				toolResultTokenThreshold: 10,
				keepRecentTokens:         10,
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				// All messages should remain unchanged
				assert.Equal(t, "msg 1", state.Messages[0].Content)
				assert.Equal(t, "response 1", state.Messages[1].Content)
				assert.Equal(t, "msg 2", state.Messages[2].Content)
				assert.Equal(t, "response 2", state.Messages[3].Content)
			},
		},
		{
			name: "empty messages",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{},
				},
				toolResultTokenThreshold: 100,
				keepRecentTokens:         500,
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				assert.Empty(t, state.Messages)
			},
		},
		{
			name: "custom token estimator - word count",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("hello world"),
						schema.ToolMessage("this is a long tool result", "call-1", schema.WithToolName("tool1")), // 6 words (old)
						schema.UserMessage("another message"),
						schema.ToolMessage("recent tool result here", "call-2", schema.WithToolName("tool2")), // 4 words (recent)
					},
				},
				toolResultTokenThreshold: 9, // 10 words total threshold
				keepRecentTokens:         5, // 15 words protection budget
				placeholder:              "[Old tool result content cleared]",
				estimator: func(msg *schema.Message) int {
					if msg.Content == "" {
						return 0
					}
					words := 1
					for _, ch := range msg.Content {
						if ch == ' ' {
							words++
						}
					}
					return words
				},
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				assert.Equal(t, "[Old tool result content cleared]", state.Messages[1].Content)
				assert.Equal(t, "recent tool result here", state.Messages[3].Content)
			},
		},
		{
			name: "already cleared results are not counted",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("msg1"),
						schema.ToolMessage("[Old tool result content cleared]", "call-1", schema.WithToolName("tool1")), // Already cleared
						schema.UserMessage("msg2"),
						schema.ToolMessage(strings.Repeat("a", 100), "call-2", schema.WithToolName("tool2")), // New long result
					},
				},
				toolResultTokenThreshold: 10,
				keepRecentTokens:         20,
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				// Only the new long result counts toward the threshold
				// Both should have placeholder
				assert.Equal(t, "[Old tool result content cleared]", state.Messages[1].Content)
				assert.Equal(t, strings.Repeat("a", 100), state.Messages[3].Content)
			},
		},
		{
			name: "all tool results within protected range",
			args: args{
				state: &adk.ChatModelAgentState{
					Messages: []adk.Message{
						schema.UserMessage("msg1"),
						schema.ToolMessage(strings.Repeat("a", 40), "call-1", schema.WithToolName("tool1")), // ~10 tokens
						schema.UserMessage("msg2"),
						schema.ToolMessage(strings.Repeat("b", 40), "call-2", schema.WithToolName("tool2")), // ~10 tokens
					},
				},
				toolResultTokenThreshold: 10,   // Low threshold (will exceed)
				keepRecentTokens:         1000, // Very high protection (protects all)
				placeholder:              "[Old tool result content cleared]",
				estimator:                defaultTokenCounter,
			},
			wantErr: assert.NoError,
			validateState: func(t *testing.T, state *adk.ChatModelAgentState) {
				// All messages are within protected range, nothing should be cleared
				assert.Equal(t, strings.Repeat("a", 40), state.Messages[1].Content)
				assert.Equal(t, strings.Repeat("b", 40), state.Messages[3].Content)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reduceByTokens(tt.args.state, tt.args.toolResultTokenThreshold, tt.args.keepRecentTokens, tt.args.placeholder, tt.args.estimator, []string{})
			tt.wantErr(t, err, fmt.Sprintf("reduceByTokens(%v, %v, %v, %v)", tt.args.state, tt.args.toolResultTokenThreshold, tt.args.keepRecentTokens, tt.args.placeholder))
			if tt.validateState != nil {
				tt.validateState(t, tt.args.state)
			}
		})
	}
}

func Test_newClearToolResult(t *testing.T) {
	ctx := context.Background()

	t.Run("nil config uses defaults", func(t *testing.T) {
		fn := newClearToolResult(ctx, nil)
		assert.NotNil(t, fn)

		// Test that function works with nil config (uses defaults)
		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage("hello"),
				schema.ToolMessage("short result", "call-1", schema.WithToolName("tool1")),
			},
		}
		err := fn(ctx, state)
		assert.NoError(t, err)
		// Default threshold is 20000, so short result should not be cleared
		assert.Equal(t, "short result", state.Messages[1].Content)
	})

	t.Run("empty config uses defaults", func(t *testing.T) {
		fn := newClearToolResult(ctx, &ClearToolResultConfig{})
		assert.NotNil(t, fn)

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage("hello"),
				schema.ToolMessage("short result", "call-1", schema.WithToolName("tool1")),
			},
		}
		err := fn(ctx, state)
		assert.NoError(t, err)
		assert.Equal(t, "short result", state.Messages[1].Content)
	})
}

func TestNewChatModelAgentMiddleware(t *testing.T) {
	ctx := context.Background()

	t.Run("valid config with default settings", func(t *testing.T) {
		backend := &clearToolResultMockBackend{}
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend: backend,
		})
		assert.NoError(t, err)
		assert.NotNil(t, m)

		tm, ok := m.(*toolResultMiddleware)
		assert.True(t, ok)
		assert.NotNil(t, tm.clearConfig)
		assert.NotNil(t, tm.offloading)
		assert.Equal(t, 20000, tm.clearConfig.ToolResultTokenThreshold)
		assert.Equal(t, 40000, tm.clearConfig.KeepRecentTokens)
		assert.Equal(t, "[Old tool result content cleared]", tm.clearConfig.ClearToolResultPlaceholder)
		assert.Equal(t, 20000, tm.offloading.tokenLimit)
		assert.Equal(t, "read_file", tm.offloading.toolName)
	})

	t.Run("custom config values", func(t *testing.T) {
		backend := &clearToolResultMockBackend{}
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend:                    backend,
			ClearingTokenThreshold:     5000,
			KeepRecentTokens:           10000,
			ClearToolResultPlaceholder: "[Custom placeholder]",
			OffloadingTokenLimit:       8000,
			ReadFileToolName:           "custom_read",
		})
		assert.NoError(t, err)

		tm, ok := m.(*toolResultMiddleware)
		assert.True(t, ok)
		assert.Equal(t, 5000, tm.clearConfig.ToolResultTokenThreshold)
		assert.Equal(t, 10000, tm.clearConfig.KeepRecentTokens)
		assert.Equal(t, "[Custom placeholder]", tm.clearConfig.ClearToolResultPlaceholder)
		assert.Equal(t, 8000, tm.offloading.tokenLimit)
		assert.Equal(t, "custom_read", tm.offloading.toolName)
	})
}

func TestToolResultMiddleware_BeforeModelRewriteState(t *testing.T) {
	ctx := context.Background()
	backend := &clearToolResultMockBackend{}

	t.Run("clears old tool results when threshold exceeded", func(t *testing.T) {
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend:                backend,
			ClearingTokenThreshold: 20,
			KeepRecentTokens:       10,
		})
		assert.NoError(t, err)

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage("msg1"),
				schema.ToolMessage(strings.Repeat("a", 40), "call-1", schema.WithToolName("tool1")),
				schema.UserMessage("msg2"),
				schema.ToolMessage(strings.Repeat("b", 40), "call-2", schema.WithToolName("tool2")),
				schema.UserMessage("msg3"),
				schema.ToolMessage(strings.Repeat("c", 40), "call-3", schema.WithToolName("tool3")),
			},
		}

		newCtx, newState, err := m.BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
		assert.NoError(t, err)
		assert.NotNil(t, newCtx)
		assert.NotNil(t, newState)
		assert.Equal(t, "[Old tool result content cleared]", newState.Messages[1].Content)
		assert.Equal(t, "[Old tool result content cleared]", newState.Messages[3].Content)
		assert.Equal(t, strings.Repeat("c", 40), newState.Messages[5].Content)
	})

	t.Run("no clearing when under threshold", func(t *testing.T) {
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend:                backend,
			ClearingTokenThreshold: 100000,
			KeepRecentTokens:       100000,
		})
		assert.NoError(t, err)

		state := &adk.ChatModelAgentState{
			Messages: []adk.Message{
				schema.UserMessage("msg1"),
				schema.ToolMessage("short result", "call-1", schema.WithToolName("tool1")),
			},
		}

		newCtx, newState, err := m.BeforeModelRewriteState(ctx, state, &adk.ModelContext{})
		assert.NoError(t, err)
		assert.NotNil(t, newCtx)
		assert.Equal(t, "short result", newState.Messages[1].Content)
	})
}

func TestToolResultMiddleware_WrapInvokableToolCall(t *testing.T) {
	ctx := context.Background()
	backend := &clearToolResultMockBackend{}

	t.Run("small result passes through unchanged", func(t *testing.T) {
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend: backend,
		})
		assert.NoError(t, err)

		endpoint := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
			return "small result", nil
		}

		tCtx := &adk.ToolContext{Name: "test_tool", CallID: "call-1"}
		wrapped, err := m.WrapInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)

		result, err := wrapped(ctx, "{}")
		assert.NoError(t, err)
		assert.Equal(t, "small result", result)
	})

	t.Run("large result is offloaded", func(t *testing.T) {
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend:              backend,
			OffloadingTokenLimit: 5,
		})
		assert.NoError(t, err)

		largeResult := strings.Repeat("x", 100)
		endpoint := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
			return largeResult, nil
		}

		tCtx := &adk.ToolContext{Name: "test_tool", CallID: "call-large"}
		wrapped, err := m.WrapInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)

		result, err := wrapped(ctx, "{}")
		assert.NoError(t, err)
		assert.Contains(t, result, "Tool result too large")
		assert.Contains(t, result, "/large_tool_result/call-large")
	})
}

func TestToolResultMiddleware_WrapStreamableToolCall(t *testing.T) {
	ctx := context.Background()
	backend := &clearToolResultMockBackend{}

	t.Run("small result passes through unchanged", func(t *testing.T) {
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend: backend,
		})
		assert.NoError(t, err)

		endpoint := func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
			return schema.StreamReaderFromArray([]string{"small", " result"}), nil
		}

		tCtx := &adk.ToolContext{Name: "test_tool", CallID: "call-1"}
		wrapped, err := m.WrapStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)

		sr, err := wrapped(ctx, "{}")
		assert.NoError(t, err)

		var result strings.Builder
		for {
			chunk, err := sr.Recv()
			if err != nil {
				break
			}
			result.WriteString(chunk)
		}
		assert.Equal(t, "small result", result.String())
	})

	t.Run("large result is offloaded", func(t *testing.T) {
		m, err := NewChatModelAgentMiddleware(ctx, &ToolResultConfig{
			Backend:              backend,
			OffloadingTokenLimit: 5,
		})
		assert.NoError(t, err)

		largeResult := strings.Repeat("x", 100)
		endpoint := func(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
			return schema.StreamReaderFromArray([]string{largeResult}), nil
		}

		tCtx := &adk.ToolContext{Name: "test_tool", CallID: "call-stream-large"}
		wrapped, err := m.WrapStreamableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)

		sr, err := wrapped(ctx, "{}")
		assert.NoError(t, err)

		var result strings.Builder
		for {
			chunk, err := sr.Recv()
			if err != nil {
				break
			}
			result.WriteString(chunk)
		}
		assert.Contains(t, result.String(), "Tool result too large")
		assert.Contains(t, result.String(), "/large_tool_result/call-stream-large")
	})
}

type clearToolResultMockBackend struct{}

func (m *clearToolResultMockBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	return nil
}
