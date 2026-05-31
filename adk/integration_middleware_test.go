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

package adk_test

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/agentsmd"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	"github.com/cloudwego/eino/adk/middlewares/patchtoolcalls"
	"github.com/cloudwego/eino/adk/middlewares/reduction"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// stubChatModel returns a fixed final assistant message and stops the React loop.
type stubChatModel struct {
	reply string
}

func (m *stubChatModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.reply, nil), nil
}

func (m *stubChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(m.reply, nil)}), nil
}

// memBackend is a minimal Agents.md backend that serves an in-memory file.
type memBackend struct {
	files map[string]string
}

func (b *memBackend) Read(_ context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	content, ok := b.files[req.FilePath]
	if !ok {
		return nil, fmt.Errorf("file not found: %s: %w", req.FilePath, os.ErrNotExist)
	}
	return &filesystem.FileContent{Content: content}, nil
}

// TestAgentsMDIntegration_PersistsMessageInserted is a true end-to-end test:
// it runs a real ChatModelAgent with the real agentsmd middleware through the
// Runner with session mode enabled, and verifies that the persistent event log
// contains a MessageInserted event carrying the agentsmd content. This covers
// the evaluation's "real middleware event emission" gap.
func TestAgentsMDIntegration_PersistsMessageInserted(t *testing.T) {
	ctx := context.Background()

	backend := &memBackend{files: map[string]string{
		"AGENTS.md": "you are a careful agent",
	}}
	mw, err := agentsmd.New(ctx, &agentsmd.Config{
		Backend:       backend,
		AgentsMDFiles: []string{"AGENTS.md"},
	})
	require.NoError(t, err)

	model := &stubChatModel{reply: "ok"}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "agentsmd-integration",
		Description: "agentsmd integration test agent",
		Instruction: "you are a test agent",
		Model:       model,
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	store := session.NewInMemoryStore[*schema.Message](nil)
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:          agent,
		SessionID:      "agentsmd-test",
		SessionService: store,
	})

	iter := runner.Query(ctx, "hello")
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	// Read the persisted event log.
	res, err := store.LoadEvents(ctx, "agentsmd-test", &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)

	var sawInsertedAgentsmd bool
	for _, se := range res.Events {
		if se.MessageInserted == nil {
			continue
		}
		ins := se.MessageInserted.Message
		// The inserted message must carry the agentsmd marker so the next turn skips re-insertion.
		if ins != nil && ins.Extra != nil {
			if v, ok := ins.Extra["__agentsmd_content__"]; ok {
				if b, ok := v.(bool); ok && b {
					sawInsertedAgentsmd = true
					assert.Contains(t, ins.Content, "you are a careful agent",
						"persisted MessageInserted must carry the loaded agentsmd content")
				}
			}
		}
	}
	assert.True(t, sawInsertedAgentsmd,
		"agentsmd middleware running through ChatModelAgent + Runner must persist a MessageInserted event with the marker")
}

// TestAgentsMDIntegration_NextTurnSkipsReinsertion verifies the prompt-cache
// stability invariant: after the first turn persists the agentsmd MessageInserted
// event, the second turn boots from the persisted state and the middleware does
// NOT re-insert (so the prefix bytes stay byte-identical).
func TestAgentsMDIntegration_NextTurnSkipsReinsertion(t *testing.T) {
	ctx := context.Background()

	backend := &memBackend{files: map[string]string{
		"AGENTS.md": "stable agents.md prefix",
	}}
	mw, err := agentsmd.New(ctx, &agentsmd.Config{
		Backend:       backend,
		AgentsMDFiles: []string{"AGENTS.md"},
	})
	require.NoError(t, err)

	model := &stubChatModel{reply: "ok"}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "agentsmd-stable",
		Description: "agentsmd stable-prefix test",
		Instruction: "test agent",
		Model:       model,
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	store := session.NewInMemoryStore[*schema.Message](nil)
	sid := "agentsmd-stable-session"

	// Turn 1.
	runner1 := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, SessionID: sid, SessionService: store})
	for it := runner1.Query(ctx, "first"); ; {
		ev, ok := it.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	// Count agentsmd MessageInserted events after turn 1.
	countAgentsmdInserts := func() int {
		res, err := store.LoadEvents(ctx, sid, &adk.LoadSessionEventsRequest{})
		require.NoError(t, err)
		count := 0
		for _, se := range res.Events {
			if se.MessageInserted == nil {
				continue
			}
			if se.MessageInserted.Message != nil && se.MessageInserted.Message.Extra != nil {
				if v, ok := se.MessageInserted.Message.Extra["__agentsmd_content__"]; ok {
					if b, ok := v.(bool); ok && b {
						count++
					}
				}
			}
		}
		return count
	}
	require.Equal(t, 1, countAgentsmdInserts(), "first turn must insert exactly once")

	// Turn 2.
	runner2 := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, SessionID: sid, SessionService: store})
	for it := runner2.Query(ctx, "second"); ; {
		ev, ok := it.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	// Critical assertion: still exactly one — turn 2 must NOT have inserted
	// another agentsmd message because the marker is in the reconstructed history.
	assert.Equal(t, 1, countAgentsmdInserts(),
		"turn 2 must skip agentsmd re-insertion (prompt-cache prefix stability)")
}

// dummyDynamicTool is a no-op dynamic tool the toolsearch middleware can advertise.
type dummyDynamicTool struct {
	name string
	desc string
}

func (t *dummyDynamicTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: t.desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"q": {Type: schema.String, Desc: "q", Required: true},
		}),
	}, nil
}

func (t *dummyDynamicTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return `{"ok":true}`, nil
}

// TestToolSearchIntegration_PersistsMessageInserted is the toolsearch
// counterpart of the agentsmd integration test: a real ChatModelAgent + real
// toolsearch middleware + Runner + InMemoryStore. It verifies the toolsearch
// reminder is persisted as a MessageInserted event and survives across turns.
func TestToolSearchIntegration_PersistsMessageInserted(t *testing.T) {
	ctx := context.Background()

	mw, err := toolsearch.New(ctx, &toolsearch.Config{
		DynamicTools: []tool.BaseTool{
			&dummyDynamicTool{name: "weather", desc: "get weather for a city"},
			&dummyDynamicTool{name: "stocks", desc: "get a stock quote"},
		},
	})
	require.NoError(t, err)

	model := &stubChatModel{reply: "ok"}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "toolsearch-integration",
		Description: "toolsearch integration test agent",
		Instruction: "test agent",
		Model:       model,
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	store := session.NewInMemoryStore[*schema.Message](nil)
	sid := "toolsearch-test"
	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})

	for it := runner.Query(ctx, "anything"); ; {
		ev, ok := it.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	res, err := store.LoadEvents(ctx, sid, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)

	var sawInsertedReminder bool
	for _, se := range res.Events {
		if se.MessageInserted == nil {
			continue
		}
		ins := se.MessageInserted.Message
		if ins != nil && ins.Extra != nil {
			if v, ok := ins.Extra["__toolsearch_reminder__"]; ok {
				if b, ok := v.(bool); ok && b {
					sawInsertedReminder = true
				}
			}
		}
	}
	assert.True(t, sawInsertedReminder,
		"toolsearch middleware running through ChatModelAgent + Runner must persist a MessageInserted event with the reminder marker")
}

// TestPatchToolCallsIntegration_PersistsMessageInserted seeds the session event
// log with an assistant message that has a dangling tool call (no following
// tool result). On the next Run, the reconstructed history contains the
// dangling call; patchtoolcalls' BeforeModelRewriteState patches it by inserting
// a synthetic tool message and emitting a MessageInserted event. We verify the
// event reaches the persistent log.
func TestPatchToolCallsIntegration_PersistsMessageInserted(t *testing.T) {
	ctx := context.Background()

	store := session.NewInMemoryStore[*schema.Message](nil)
	sid := "patchtoolcalls-test"

	// Seed: an assistant message with a tool call but no corresponding tool result.
	dangling := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{
				ID:       "call-1",
				Type:     "function",
				Function: schema.FunctionCall{Name: "weather", Arguments: `{"city":"sf"}`},
			},
		},
		Extra: map[string]any{"_eino_msg_id": "dangling-msg-id"},
	}
	user := &schema.Message{
		Role:    schema.User,
		Content: "what's the weather?",
		Extra:   map[string]any{"_eino_msg_id": "user-msg-id"},
	}

	for _, m := range []*schema.Message{user, dangling} {
		se := &adk.SessionEvent[*schema.Message]{EventID: uuid.NewString(), Kind: adk.SessionEventMessage, Message: m}
		require.NoError(t, store.AppendEvents(ctx, sid, []*adk.SessionEvent[*schema.Message]{se}))
	}

	// Wire patchtoolcalls into a ChatModelAgent.
	mw, err := patchtoolcalls.New(ctx, nil)
	require.NoError(t, err)

	model := &stubChatModel{reply: "done"}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "patchtoolcalls-integration",
		Description: "patchtoolcalls integration test agent",
		Instruction: "test agent",
		Model:       model,
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})

	for it := runner.Query(ctx, "go"); ; {
		ev, ok := it.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	// Read events back; among the events appended on this turn there should be
	// a MessageInserted carrying a Tool-role synthetic message.
	res, err := store.LoadEvents(ctx, sid, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)
	var sawInsertedToolResult bool
	for _, se := range res.Events {
		if se.MessageInserted == nil {
			continue
		}
		ins := se.MessageInserted.Message
		if ins != nil && ins.Role == schema.Tool && ins.ToolCallID == "call-1" {
			sawInsertedToolResult = true
		}
	}
	assert.True(t, sawInsertedToolResult,
		"patchtoolcalls middleware must persist a MessageInserted event for the synthetic tool result")
}

// TestReductionIntegration_PersistsBothMessageUpdated seeds the session log
// with two rounds of (assistant tool call → tool result). With reduction's
// ClearRetentionSuffixLimit=1 (the framework default), the LAST round is
// retained and the FIRST round is cleared. Reduction emits MessageUpdated for
// both the assistant tool-call message (args replaced + cleared flag) and the
// tool-result message (content replaced). Both must reach the persistent log.
func TestReductionIntegration_PersistsBothMessageUpdated(t *testing.T) {
	ctx := context.Background()
	store := session.NewInMemoryStore[*schema.Message](nil)
	sid := "reduction-test"

	// Seed the session: user → assistant call A → tool result A → assistant call B → tool result B.
	// With ClearRetentionSuffixLimit=1, round B is retained; round A is cleared.
	user := &schema.Message{
		Role:    schema.User,
		Content: "do the thing",
		Extra:   map[string]any{"_eino_msg_id": "user-id"},
	}
	assistantA := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "tc-A", Type: "function", Function: schema.FunctionCall{Name: "noop", Arguments: `{"q":"A"}`}},
		},
		Extra: map[string]any{"_eino_msg_id": "assistant-A-id"},
	}
	toolResultA := &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "tc-A",
		ToolName:   "noop",
		Content:    "raw content A",
		Extra:      map[string]any{"_eino_msg_id": "tool-A-id"},
	}
	assistantB := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{ID: "tc-B", Type: "function", Function: schema.FunctionCall{Name: "noop", Arguments: `{"q":"B"}`}},
		},
		Extra: map[string]any{"_eino_msg_id": "assistant-B-id"},
	}
	toolResultB := &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "tc-B",
		ToolName:   "noop",
		Content:    "raw content B",
		Extra:      map[string]any{"_eino_msg_id": "tool-B-id"},
	}
	for _, m := range []*schema.Message{user, assistantA, toolResultA, assistantB, toolResultB} {
		se := &adk.SessionEvent[*schema.Message]{EventID: uuid.NewString(), Kind: adk.SessionEventMessage, Message: m}
		require.NoError(t, store.AppendEvents(ctx, sid, []*adk.SessionEvent[*schema.Message]{se}))
	}

	// Reduction config: token counter always exceeds threshold; clear handler always clears.
	mw, err := reduction.New(ctx, &reduction.Config{
		SkipTruncation: true,
		TokenCounter: func(_ context.Context, _ []*schema.Message, _ []*schema.ToolInfo) (int64, error) {
			return 1000000, nil
		},
		MaxTokensForClear: 1,
		// ClearRetentionSuffixLimit defaults to 1: round B is retained, round A is cleared.
		GenClearOffloadFilePath: func(_ context.Context, td *reduction.ToolDetail) (string, error) {
			return "/tmp/" + td.ToolContext.CallID, nil
		},
		ToolConfig: map[string]*reduction.ToolReductionConfig{
			"noop": {
				SkipClear: false,
				ClearHandler: func(_ context.Context, _ *reduction.ToolDetail) (*reduction.ClearResult, error) {
					return &reduction.ClearResult{
						NeedClear:    true,
						ToolArgument: &schema.ToolArgument{Text: `{"q":"[cleared]"}`},
						ToolResult:   &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "[cleared]"}}},
					}, nil
				},
			},
		},
	})
	require.NoError(t, err)

	model := &stubChatModel{reply: "ok"}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "reduction-integration",
		Description: "reduction integration test agent",
		Instruction: "test agent",
		Model:       model,
		Handlers:    []adk.ChatModelAgentMiddleware{mw},
	})
	require.NoError(t, err)

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:          agent,
		SessionID:      sid,
		SessionService: store,
	})

	for it := runner.Query(ctx, "go"); ; {
		ev, ok := it.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	res, err := store.LoadEvents(ctx, sid, &adk.LoadSessionEventsRequest{})
	require.NoError(t, err)

	var sawAssistantUpdated, sawToolUpdated bool
	for _, se := range res.Events {
		if se.MessageUpdated == nil {
			continue
		}
		switch se.MessageUpdated.MessageID {
		case "assistant-A-id":
			sawAssistantUpdated = true
		case "tool-A-id":
			sawToolUpdated = true
		}
	}
	assert.True(t, sawAssistantUpdated,
		"reduction must emit MessageUpdated for the cleared assistant tool-call message (round A)")
	assert.True(t, sawToolUpdated,
		"reduction must emit MessageUpdated for the cleared tool-result message (round A)")
}
