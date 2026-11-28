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

package automemory

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	asyncReplayToolName = "async_memory_replay_tool"
	asyncReplayCallID   = "async-memory-replay-call"
)

type gatedTopicSelectionModel struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (m *gatedTopicSelectionModel) Generate(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.once.Do(func() {
		close(m.started)
	})
	select {
	case <-m.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return schema.AssistantMessage(`{"selected_memories":["topic.md"]}`, nil), nil
}

func (m *gatedTopicSelectionModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

type asyncReplayChatModel struct {
	selectionStarted chan struct{}
	releaseSelection chan struct{}
	releaseOnce      sync.Once
	inputs           [][]*schema.Message
}

func (m *asyncReplayChatModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	if len(m.inputs) == 1 {
		select {
		case <-m.selectionStarted:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m.releaseOnce.Do(func() {
			close(m.releaseSelection)
		})
		return schema.AssistantMessage("", []schema.ToolCall{{
			ID:   asyncReplayCallID,
			Type: "function",
			Function: schema.FunctionCall{
				Name:      asyncReplayToolName,
				Arguments: `{}`,
			},
		}}), nil
	}
	return schema.AssistantMessage("done", nil), nil
}

func (m *asyncReplayChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *asyncReplayChatModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type asyncReplayTool struct{}

func (asyncReplayTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: asyncReplayToolName,
		Desc: "return a deterministic tool result",
	}, nil
}

func (asyncReplayTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "tool result", nil
}

// asyncSelectionBarrier delegates to the real middleware after making the
// asynchronous selection boundary deterministic for the second model call.
type asyncSelectionBarrier struct {
	adk.ChatModelAgentMiddleware
	beforeModelCalls int
}

func (m *asyncSelectionBarrier) BeforeModelRewriteState(
	ctx context.Context,
	state *adk.ChatModelAgentState,
	modelCtx *adk.ModelContext,
) (context.Context, *adk.ChatModelAgentState, error) {
	m.beforeModelCalls++
	if m.beforeModelCalls == 2 {
		future, ok := ctx.Value(ctxKeySelectionFuture{}).(*selectionFuture)
		if !ok || future == nil {
			return ctx, state, fmt.Errorf("automemory selection future missing")
		}
		select {
		case <-future.done:
		case <-ctx.Done():
			return ctx, state, ctx.Err()
		}
	}
	return m.ChatModelAgentMiddleware.BeforeModelRewriteState(ctx, state, modelCtx)
}

func asyncReplayRelevantOrder(messages []*schema.Message) []string {
	order := make([]string, 0, 3)
	for _, message := range messages {
		switch {
		case message == nil:
			continue
		case len(message.ToolCalls) == 1 && message.ToolCalls[0].ID == asyncReplayCallID:
			order = append(order, "assistant tool-call")
		case message.Role == schema.Tool && message.ToolCallID == asyncReplayCallID:
			order = append(order, "tool result")
		case strings.Contains(message.Content, "<!-- automemory -->"):
			order = append(order, "topic memory")
		}
	}
	return order
}

func TestAutomemoryIntegration_AsyncReplayPreservesReadyBoundaryOrder(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	backend := NewInMemoryBackend()
	require.NoError(t, backend.Write(ctx, &WriteRequest{
		FilePath: "/mem/topic.md",
		Content:  "facts selected for the current request",
	}))

	selectionStarted := make(chan struct{})
	releaseSelection := make(chan struct{})
	memoryMiddleware, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   backend,
		Read: &ReadConfig[*schema.Message]{
			Mode: ReadModeAsync,
			Model: &gatedTopicSelectionModel{
				started: selectionStarted,
				release: releaseSelection,
			},
		},
	})
	require.NoError(t, err)

	barrier := &asyncSelectionBarrier{ChatModelAgentMiddleware: memoryMiddleware}
	chatModel := &asyncReplayChatModel{
		selectionStarted: selectionStarted,
		releaseSelection: releaseSelection,
	}
	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "automemory-async-replay",
		Description: "verify async memory ordering survives session replay",
		Instruction: "test agent",
		Model:       chatModel,
		Handlers:    []adk.ChatModelAgentMiddleware{barrier},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{asyncReplayTool{}},
			},
		},
	})
	require.NoError(t, err)

	store := session.NewInMemoryStore[*schema.Message](nil)
	const sessionID = "automemory-async-replay-order"
	run := func(query string) {
		runner := adk.NewRunner(ctx, adk.RunnerConfig{
			Agent:        agent,
			SessionID:    sessionID,
			SessionStore: store,
		})
		for iter := runner.Query(ctx, query); ; {
			event, ok := iter.Next()
			if !ok {
				return
			}
			require.NoError(t, event.Err)
		}
	}

	run("first turn")
	require.Len(t, chatModel.inputs, 2)
	require.Equal(t,
		[]string{"assistant tool-call", "tool result", "topic memory"},
		asyncReplayRelevantOrder(chatModel.inputs[1]),
		"live async injection must append memory after the tool result",
	)

	run("second turn")
	require.Len(t, chatModel.inputs, 3)
	require.Equal(t,
		[]string{"assistant tool-call", "tool result", "topic memory"},
		asyncReplayRelevantOrder(chatModel.inputs[2]),
		"session replay must preserve the live ready-boundary order",
	)
}
