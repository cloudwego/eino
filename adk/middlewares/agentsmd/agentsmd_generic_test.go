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

package agentsmd

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// --- generic table-driven test helpers ---

func makeUserMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{Role: schema.User, Content: content}).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(content)).(M)
	default:
		panic("unreachable")
	}
}

func makeSystemMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{Role: schema.System, Content: content}).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(content)).(M)
	default:
		panic("unreachable")
	}
}

func makeAssistantMsg[M adk.MessageType](content string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(&schema.Message{Role: schema.Assistant, Content: content}).(M)
	case *schema.AgenticMessage:
		return any(&schema.AgenticMessage{
			Role:          schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: content})},
		}).(M)
	default:
		panic("unreachable")
	}
}

func getMsgRole[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return string(v.Role)
	case *schema.AgenticMessage:
		return string(v.Role)
	default:
		panic("unreachable")
	}
}

func getMsgContent[M adk.MessageType](msg M) string {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Content
	case *schema.AgenticMessage:
		for _, block := range v.ContentBlocks {
			if block == nil {
				continue
			}
			if block.UserInputText != nil {
				return block.UserInputText.Text
			}
			if block.AssistantGenText != nil {
				return block.AssistantGenText.Text
			}
		}
		return ""
	default:
		panic("unreachable")
	}
}

func getMsgExtra[M adk.MessageType](msg M) map[string]any {
	switch v := any(msg).(type) {
	case *schema.Message:
		return v.Extra
	case *schema.AgenticMessage:
		return v.Extra
	default:
		panic("unreachable")
	}
}

// --- generic table-driven test ---

func testAgentsMDGeneric[M adk.MessageType](t *testing.T) {
	type testCase struct {
		name string
		run  func(t *testing.T)
	}

	tests := []testCase{
		{
			name: "BasicInjection",
			run: func(t *testing.T) {
				b := newMemBackend()
				b.set("/agent.md", "You are a helpful assistant.")

				ctx := context.Background()
				mw, err := NewTyped[M](ctx, &Config{Backend: b, AgentsMDFiles: []string{"/agent.md"}})
				if err != nil {
					t.Fatal(err)
				}

				state := &adk.TypedChatModelAgentState[M]{Messages: []M{makeUserMsg[M]("hello")}}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}

				if len(state.Messages) != 2 {
					t.Fatalf("expected 2 messages, got %d", len(state.Messages))
				}
				if getMsgRole[M](state.Messages[0]) != "user" {
					t.Fatalf("expected first message role user, got %s", getMsgRole[M](state.Messages[0]))
				}
				if !strings.Contains(getMsgContent[M](state.Messages[0]), "You are a helpful assistant.") {
					t.Fatalf("expected agent.md content in first message, got %q", getMsgContent[M](state.Messages[0]))
				}
				if !strings.Contains(getMsgContent[M](state.Messages[0]), "<system-reminder>") {
					t.Fatalf("expected system-reminder tag, got %q", getMsgContent[M](state.Messages[0]))
				}
				if getMsgContent[M](state.Messages[1]) != "hello" {
					t.Fatalf("expected original message preserved, got %q", getMsgContent[M](state.Messages[1]))
				}
			},
		},
		{
			name: "InsertBeforeFirstUserMessage",
			run: func(t *testing.T) {
				b := newMemBackend()
				b.set("/agent.md", "agent instructions")

				ctx := context.Background()
				mw, err := NewTyped[M](ctx, &Config{Backend: b, AgentsMDFiles: []string{"/agent.md"}})
				if err != nil {
					t.Fatal(err)
				}

				input := []M{
					makeSystemMsg[M]("system prompt"),
					makeUserMsg[M]("hello"),
				}
				state := &adk.TypedChatModelAgentState[M]{Messages: input}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}

				if len(state.Messages) != 3 {
					t.Fatalf("expected 3 messages, got %d", len(state.Messages))
				}
				if getMsgRole[M](state.Messages[0]) != "system" {
					t.Fatalf("expected first message role system, got %s", getMsgRole[M](state.Messages[0]))
				}
				if getMsgContent[M](state.Messages[0]) != "system prompt" {
					t.Fatalf("expected system prompt preserved, got %q", getMsgContent[M](state.Messages[0]))
				}
				if getMsgRole[M](state.Messages[1]) != "user" || !strings.Contains(getMsgContent[M](state.Messages[1]), "agent instructions") {
					t.Fatalf("expected agentmd message at index 1, got role=%s content=%q", getMsgRole[M](state.Messages[1]), getMsgContent[M](state.Messages[1]))
				}
				if getMsgRole[M](state.Messages[2]) != "user" || getMsgContent[M](state.Messages[2]) != "hello" {
					t.Fatalf("expected original user message at index 2, got role=%s content=%q", getMsgRole[M](state.Messages[2]), getMsgContent[M](state.Messages[2]))
				}
			},
		},
		{
			name: "InsertWithNoUserMessage",
			run: func(t *testing.T) {
				b := newMemBackend()
				b.set("/agent.md", "agent instructions")

				ctx := context.Background()
				mw, err := NewTyped[M](ctx, &Config{Backend: b, AgentsMDFiles: []string{"/agent.md"}})
				if err != nil {
					t.Fatal(err)
				}

				input := []M{
					makeSystemMsg[M]("system prompt"),
					makeAssistantMsg[M]("assistant reply"),
				}
				state := &adk.TypedChatModelAgentState[M]{Messages: input}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}

				if len(state.Messages) != 3 {
					t.Fatalf("expected 3 messages, got %d", len(state.Messages))
				}
				if getMsgRole[M](state.Messages[0]) != "system" {
					t.Fatalf("expected System at index 0, got %s", getMsgRole[M](state.Messages[0]))
				}
				if getMsgRole[M](state.Messages[1]) != "assistant" {
					t.Fatalf("expected Assistant at index 1, got %s", getMsgRole[M](state.Messages[1]))
				}
				if getMsgRole[M](state.Messages[2]) != "user" || !strings.Contains(getMsgContent[M](state.Messages[2]), "agent instructions") {
					t.Fatalf("expected agentmd appended at end, got role=%s content=%q", getMsgRole[M](state.Messages[2]), getMsgContent[M](state.Messages[2]))
				}
			},
		},
		{
			name: "AllFilesEmpty",
			run: func(t *testing.T) {
				b := newMemBackend()
				b.set("/agent.md", "")

				ctx := context.Background()
				mw, err := NewTyped[M](ctx, &Config{Backend: b, AgentsMDFiles: []string{"/agent.md"}})
				if err != nil {
					t.Fatal(err)
				}

				state := &adk.TypedChatModelAgentState[M]{Messages: []M{makeUserMsg[M]("hello")}}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Messages) != 1 {
					t.Fatalf("expected 1 message (no agentmd prepended), got %d", len(state.Messages))
				}
				if getMsgContent[M](state.Messages[0]) != "hello" {
					t.Fatalf("expected original message unchanged, got %q", getMsgContent[M](state.Messages[0]))
				}
			},
		},
		{
			name: "Idempotency",
			run: func(t *testing.T) {
				b := newMemBackend()
				b.set("/agent.md", "agent instructions")

				ctx := context.Background()
				mw, err := NewTyped[M](ctx, &Config{Backend: b, AgentsMDFiles: []string{"/agent.md"}})
				if err != nil {
					t.Fatal(err)
				}

				state := &adk.TypedChatModelAgentState[M]{Messages: []M{makeUserMsg[M]("hello")}}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Messages) != 2 {
					t.Fatalf("expected 2 messages after first call, got %d", len(state.Messages))
				}

				// Verify the marker is set in Extra.
				extra := getMsgExtra[M](state.Messages[0])
				if extra == nil {
					t.Fatal("expected Extra to be set on injected message")
				}
				if _, ok := extra[agentsMDExtraKey]; !ok {
					t.Fatalf("expected agentsMDExtraKey in Extra, got %v", extra)
				}

				// Call again with the same state (which now contains the marker message).
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Messages) != 2 {
					t.Fatalf("expected 2 messages after second call (idempotent), got %d", len(state.Messages))
				}
				if !strings.Contains(getMsgContent[M](state.Messages[0]), "agent instructions") {
					t.Fatalf("expected agentmd content preserved, got %q", getMsgContent[M](state.Messages[0]))
				}
			},
		},
		{
			name: "ReinsertAfterRemoval",
			run: func(t *testing.T) {
				b := newMemBackend()
				b.set("/agent.md", "agent instructions")

				ctx := context.Background()
				mw, err := NewTyped[M](ctx, &Config{Backend: b, AgentsMDFiles: []string{"/agent.md"}})
				if err != nil {
					t.Fatal(err)
				}

				state := &adk.TypedChatModelAgentState[M]{Messages: []M{makeUserMsg[M]("hello")}}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Messages) != 2 {
					t.Fatalf("expected 2 messages after first call, got %d", len(state.Messages))
				}

				// Simulate removal of the marker message (e.g., by summarization).
				state = &adk.TypedChatModelAgentState[M]{Messages: []M{makeUserMsg[M]("hello")}}
				_, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Messages) != 2 {
					t.Fatalf("expected 2 messages after re-insert, got %d", len(state.Messages))
				}
				if !strings.Contains(getMsgContent[M](state.Messages[0]), "agent instructions") {
					t.Fatalf("expected agentmd content re-inserted, got %q", getMsgContent[M](state.Messages[0]))
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, tc.run)
	}
}

func TestAgentsMDGeneric(t *testing.T) {
	t.Run("Message", testAgentsMDGeneric[*schema.Message])
	t.Run("AgenticMessage", testAgentsMDGeneric[*schema.AgenticMessage])
}
