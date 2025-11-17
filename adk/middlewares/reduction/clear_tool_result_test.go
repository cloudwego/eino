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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
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
