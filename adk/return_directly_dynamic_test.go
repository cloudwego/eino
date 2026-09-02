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

package adk

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// conditionalReturnTool is a tool whose body decides at runtime whether the
// ReAct loop should stop, by calling SetReturnDirectly.
type conditionalReturnTool struct {
	name string
	run  func(ctx context.Context, argumentsInJSON string) (string, error)
}

func (t *conditionalReturnTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "decides return-directly at runtime"}, nil
}

func (t *conditionalReturnTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	return t.run(ctx, argumentsInJSON)
}

// TestSetReturnDirectly_AllowRuntimeOnly covers the core new capability: a tool
// stops the loop from its own result with nothing listed in
// ToolsConfig.ReturnDirectly, enabled purely by AllowRuntimeReturnDirectly.
func TestSetReturnDirectly_AllowRuntimeOnly(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
	// Exactly one model call. A second call would mean the loop did not stop.
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("calling tool", []schema.ToolCall{
			{ID: "call-1", Function: schema.FunctionCall{Name: "dyn"}},
		}), nil).
		Times(1)

	var setErr error
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "agent whose tool stops the loop at runtime",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&conditionalReturnTool{
					name: "dyn",
					run: func(ctx context.Context, _ string) (string, error) {
						setErr = SetReturnDirectly(ctx)
						return "final from tool", nil
					},
				}},
			},
			// No ReturnDirectly entries: the switch alone must be enough.
			AllowRuntimeReturnDirectly: true,
		},
	})
	require.NoError(t, err)

	events, _ := drainEvents(agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("go")},
	}))
	require.NoError(t, setErr)
	require.NotEmpty(t, events)

	for _, ev := range events {
		require.NoError(t, ev.Err)
	}

	last := events[len(events)-1]
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)
	msg := last.Output.MessageOutput.Message
	require.NotNil(t, msg)
	assert.Equal(t, schema.Tool, msg.Role)
	assert.Equal(t, "dyn", msg.ToolName)
	assert.Equal(t, "final from tool", msg.Content)
}

// TestSetReturnDirectly_NotAllowed pins the guard: when the agent has no
// direct-return path at all, SetReturnDirectly reports an error instead of
// silently doing nothing, and the loop keeps running.
func TestSetReturnDirectly_NotAllowed(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("calling tool", []schema.ToolCall{
			{ID: "call-1", Function: schema.FunctionCall{Name: "dyn"}},
		}), nil).
		Times(1)
	// The loop must continue, so the model is asked for a final answer.
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("final from model", nil), nil).
		Times(1)

	var setErr error
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "agent that does not allow returning directly",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&conditionalReturnTool{
					name: "dyn",
					run: func(ctx context.Context, _ string) (string, error) {
						setErr = SetReturnDirectly(ctx)
						return "intermediate", nil
					},
				}},
			},
			// Neither ReturnDirectly nor AllowRuntimeReturnDirectly.
		},
	})
	require.NoError(t, err)

	events, _ := drainEvents(agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("go")},
	}))
	require.ErrorIs(t, setErr, errReturnDirectlyNotAllowed)
	require.NotEmpty(t, events)

	for _, ev := range events {
		require.NoError(t, ev.Err)
	}

	last := events[len(events)-1]
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)
	msg := last.Output.MessageOutput.Message
	require.NotNil(t, msg)
	assert.Equal(t, schema.Assistant, msg.Role)
	assert.Equal(t, "final from model", msg.Content)
}

// TestSetReturnDirectly_OverridesStaticSelection verifies the runtime decision
// wins over the static one: "cfg" is the configured return-directly tool, but
// "dyn" claims the direct return at runtime, so "dyn"'s result is returned.
func TestSetReturnDirectly_OverridesStaticSelection(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("calling tools", []schema.ToolCall{
			{ID: "call-cfg", Function: schema.FunctionCall{Name: "cfg"}},
			{ID: "call-dyn", Function: schema.FunctionCall{Name: "dyn"}},
		}), nil).
		Times(1)

	var setErr error
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "runtime decision overrides the configured one",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{
					&conditionalReturnTool{
						name: "cfg",
						run: func(_ context.Context, _ string) (string, error) {
							return "from cfg", nil
						},
					},
					&conditionalReturnTool{
						name: "dyn",
						run: func(ctx context.Context, _ string) (string, error) {
							setErr = SetReturnDirectly(ctx)
							return "from dyn", nil
						},
					},
				},
			},
			ReturnDirectly: map[string]bool{"cfg": true},
		},
	})
	require.NoError(t, err)

	events, _ := drainEvents(agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("go")},
	}))
	require.NoError(t, setErr)
	require.NotEmpty(t, events)

	for _, ev := range events {
		require.NoError(t, ev.Err)
	}

	last := events[len(events)-1]
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)
	msg := last.Output.MessageOutput.Message
	require.NotNil(t, msg)
	assert.Equal(t, "dyn", msg.ToolName)
	assert.Equal(t, "from dyn", msg.Content)
}

// streamingReturnTool requests a direct return from a StreamableRun tool, which
// the event-sender wrapper handles on a separate code path from InvokableRun.
type streamingReturnTool struct {
	name   string
	setErr func(error)
	chunks []string
}

func (t *streamingReturnTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "streams and stops the loop"}, nil
}

func (t *streamingReturnTool) StreamableRun(ctx context.Context, _ string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	// Must be called synchronously, before returning the reader: the wrapper
	// inspects state right after this function returns.
	t.setErr(SetReturnDirectly(ctx))

	sr, sw := schema.Pipe[string](len(t.chunks))
	go func() {
		defer sw.Close()
		for _, c := range t.chunks {
			sw.Send(c, nil)
		}
	}()
	return sr, nil
}

func TestSetReturnDirectly_StreamableTool(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ context.Context, _ []*schema.Message, _ ...any) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				sw.Send(schema.AssistantMessage("calling tool", []schema.ToolCall{
					{ID: "call-1", Function: schema.FunctionCall{Name: "dyn_stream"}},
				}), nil)
			}()
			return sr, nil
		}).
		Times(1)

	var setErr error
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAgent",
		Description: "streaming tool that stops the loop at runtime",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&streamingReturnTool{
					name:   "dyn_stream",
					chunks: []string{"fi", "nal"},
					setErr: func(e error) { setErr = e },
				}},
			},
			AllowRuntimeReturnDirectly: true,
		},
	})
	require.NoError(t, err)

	events, _ := drainEvents(agent.Run(ctx, &AgentInput{
		Messages:        []Message{schema.UserMessage("go")},
		EnableStreaming: true,
	}))
	require.NoError(t, setErr)
	require.NotEmpty(t, events)

	for _, ev := range events {
		require.NoError(t, ev.Err)
	}

	last := events[len(events)-1]
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)

	mo := last.Output.MessageOutput
	content := ""
	toolName := ""
	if mo.IsStreaming {
		for {
			chunk, recvErr := mo.MessageStream.Recv()
			if recvErr != nil {
				break
			}
			content += chunk.Content
			if chunk.ToolName != "" {
				toolName = chunk.ToolName
			}
		}
	} else {
		require.NotNil(t, mo.Message)
		content = mo.Message.Content
		toolName = mo.Message.ToolName
	}

	assert.Equal(t, "dyn_stream", toolName)
	assert.Equal(t, "final", content)
}

// TestSetReturnDirectly_AgenticPath covers the same runtime decision on the
// *schema.AgenticMessage path, which builds its graph separately.
func TestSetReturnDirectly_AgenticPath(t *testing.T) {
	ctx := context.Background()

	mdl := &sequentialAgenticModel{
		responses: []*schema.AgenticMessage{
			agenticToolCallMsg("dyn", "call-1", `"args"`),
		},
	}

	var setErr error
	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        t.Name(),
		Description: "agentic agent whose tool stops the loop at runtime",
		Model:       mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&conditionalReturnTool{
					name: "dyn",
					run: func(ctx context.Context, _ string) (string, error) {
						setErr = SetReturnDirectly(ctx)
						return "final from tool", nil
					},
				}},
			},
			AllowRuntimeReturnDirectly: true,
		},
	})
	require.NoError(t, err)

	runner := NewTypedRunner(TypedRunnerConfig[*schema.AgenticMessage]{
		Agent: agent, EnableStreaming: false,
	})
	events := drainAgenticEvents(runner.Query(ctx, "go"))
	require.NoError(t, setErr)
	require.NoError(t, firstAgenticEventError(events))

	// The model must not be called a second time.
	assert.Equal(t, int32(1), atomic.LoadInt32(&mdl.callCount))

	last := lastAgenticEvent(events)
	require.NotNil(t, last)
	require.NotNil(t, last.Output)
	require.NotNil(t, last.Output.MessageOutput)
	msg := last.Output.MessageOutput.Message
	require.NotNil(t, msg)
	require.GreaterOrEqual(t, len(msg.ContentBlocks), 1)
	ftr := msg.ContentBlocks[0].FunctionToolResult
	require.NotNil(t, ftr, "expected FunctionToolResult, got type=%v", msg.ContentBlocks[0].Type)
	assert.Equal(t, "call-1", ftr.CallID)
}

func TestSetReturnDirectly_OutsideToolCall(t *testing.T) {
	require.ErrorContains(t, SetReturnDirectly(context.Background()),
		"SetReturnDirectly must be called within a tool call")
}

// TestReturnDirectlyReachable pins when the direct-return path is built, which is
// what keeps agents that use neither mechanism on their original topology.
func TestReturnDirectlyReachable(t *testing.T) {
	t.Run("NeitherConfigured", func(t *testing.T) {
		c := &reactConfig{}
		assert.False(t, c.returnDirectlyReachable())
	})

	t.Run("StaticOnly", func(t *testing.T) {
		c := &reactConfig{toolsReturnDirectly: map[string]bool{"a": true}}
		assert.True(t, c.returnDirectlyReachable())
	})

	t.Run("RuntimeOnly", func(t *testing.T) {
		c := &reactConfig{allowRuntimeReturnDirectly: true}
		assert.True(t, c.returnDirectlyReachable())
	})
}
