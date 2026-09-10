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
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type dynamicDirectTool struct {
	run func(context.Context, string) (string, error)
}

func (*dynamicDirectTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "dynamic", Desc: "optionally return directly"}, nil
}
func (d *dynamicDirectTool) InvokableRun(ctx context.Context, args string, _ ...tool.Option) (string, error) {
	if d.run != nil {
		return d.run(ctx, args)
	}
	if args == `"direct"` {
		if err := SetToolReturnDirectly(ctx); err != nil {
			return "", err
		}
	}
	return "result:" + args, nil
}
func (d *dynamicDirectTool) StreamableRun(ctx context.Context, args string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	result, err := d.InvokableRun(ctx, args, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]string{result}), nil
}

type dynamicDirectModel[M MessageType] struct {
	responses []M
	calls     int
}

func (m *dynamicDirectModel[M]) Generate(context.Context, []M, ...model.Option) (M, error) {
	m.calls++
	if m.calls > len(m.responses) {
		var zero M
		return zero, fmt.Errorf("unexpected model call")
	}
	return m.responses[m.calls-1], nil
}
func (m *dynamicDirectModel[M]) Stream(ctx context.Context, in []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	msg, err := m.Generate(ctx, in, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]M{msg}), nil
}
func TestSetToolReturnDirectly(t *testing.T) {
	require.Error(t, SetToolReturnDirectly(context.Background()))
	t.Run("Message", func(t *testing.T) {
		testDynamicDirect(t, func(args string) *schema.Message {
			return schema.AssistantMessage("", []schema.ToolCall{{ID: "dynamic-call", Function: schema.FunctionCall{Name: "dynamic", Arguments: args}}})
		}, schema.AssistantMessage("continued", nil), func(m *schema.Message) string { return m.ToolCallID })
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		testDynamicDirect(t, func(args string) *schema.AgenticMessage { return agenticToolCallMsg("dynamic", "dynamic-call", args) }, &schema.AgenticMessage{Role: schema.AgenticRoleTypeAssistant}, func(m *schema.AgenticMessage) string { _, id := extractToolIdentifiers(m); return id })
	})
}
func testDynamicDirect[M MessageType](t *testing.T, call func(string) M, final M, callID func(M) string) {
	for _, stream := range []bool{false, true} {
		for _, direct := range []bool{false, true} {
			t.Run(fmt.Sprintf("stream=%v/direct=%v", stream, direct), func(t *testing.T) {
				args := `"continue"`
				if direct {
					args = `"direct"`
				}
				mdl := &dynamicDirectModel[M]{responses: []M{call(args), final}}
				agent, err := NewTypedChatModelAgent(context.Background(), &TypedChatModelAgentConfig[M]{Name: "test", Description: "test", Model: mdl, ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{&dynamicDirectTool{}}}}})
				require.NoError(t, err)
				// Reuse the agent to check that the request is scoped to a run.
				for i := 0; i < 2; i++ {
					mdl.calls = 0
					// The second run must continue even after a direct first run.
					if i == 1 {
						mdl.responses[0] = call(`"continue"`)
					}
					iter := agent.Run(context.Background(), &TypedAgentInput[M]{EnableStreaming: stream})
					var last M
					toolEvents := 0
					for {
						event, ok := iter.Next()
						if !ok {
							break
						}
						require.NoError(t, event.Err)
						if event.Output == nil || event.Output.MessageOutput == nil {
							continue
						}
						out := event.Output.MessageOutput
						if out.IsStreaming {
							for {
								chunk, err := out.MessageStream.Recv()
								if err == io.EOF {
									break
								}
								require.NoError(t, err)
								last = chunk
							}
							out.MessageStream.Close()
						} else {
							last = out.Message
						}
						if callID(last) == "dynamic-call" {
							toolEvents++
						}
					}
					require.Equal(t, 1, toolEvents)
					if direct && i == 0 {
						require.Equal(t, 1, mdl.calls)
						require.Equal(t, "dynamic-call", callID(last))
					} else {
						require.Equal(t, 2, mdl.calls)
						require.Empty(t, callID(last))
					}
				}
			})
		}
	}
}

func TestSetToolReturnDirectlyParallel(t *testing.T) {
	for _, static := range []bool{false, true} {
		t.Run(fmt.Sprintf("static=%v", static), func(t *testing.T) {
			firstSelected := make(chan struct{})
			d := &dynamicDirectTool{run: func(ctx context.Context, args string) (string, error) {
				if args == `"first"` {
					err := SetToolReturnDirectly(ctx)
					close(firstSelected)
					return args, err
				}
				<-firstSelected
				return args, SetToolReturnDirectly(ctx)
			}}
			mdl := &dynamicDirectModel[*schema.Message]{responses: []*schema.Message{schema.AssistantMessage("", []schema.ToolCall{
				{ID: "first", Function: schema.FunctionCall{Name: "dynamic", Arguments: `"first"`}},
				{ID: "second", Function: schema.FunctionCall{Name: "dynamic", Arguments: `"second"`}},
			})}}
			conf := ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{d}}}
			expected := "first"
			if static {
				conf.ReturnDirectly = map[string]bool{"dynamic": true}
				expected = "second"
			}
			agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{Name: "parallel", Description: "test", Model: mdl, ToolsConfig: conf})
			require.NoError(t, err)
			iter := agent.Run(context.Background(), &AgentInput{})
			var ids []string
			for {
				ev, ok := iter.Next()
				if !ok {
					break
				}
				require.NoError(t, ev.Err)
				if ev.Output != nil && ev.Output.MessageOutput != nil && ev.Output.MessageOutput.Message.ToolCallID != "" {
					ids = append(ids, ev.Output.MessageOutput.Message.ToolCallID)
				}
			}
			require.Len(t, ids, 2)
			require.ElementsMatch(t, []string{"first", "second"}, ids)
			require.Equal(t, expected, ids[len(ids)-1])
			require.Equal(t, 1, mdl.calls)
		})
	}
}

func TestSetToolReturnDirectlyToolError(t *testing.T) {
	failure := errors.New("tool failed after requesting direct return")
	d := &dynamicDirectTool{run: func(ctx context.Context, _ string) (string, error) {
		if err := SetToolReturnDirectly(ctx); err != nil {
			return "", err
		}
		return "", failure
	}}
	mdl := &dynamicDirectModel[*schema.Message]{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{ID: "call", Function: schema.FunctionCall{Name: "dynamic"}}}),
	}}
	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{Name: "error", Description: "test", Model: mdl, ToolsConfig: ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{d}}}})
	require.NoError(t, err)
	iter := agent.Run(context.Background(), &AgentInput{})
	var finalErr error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			finalErr = ev.Err
		}
	}
	require.ErrorIs(t, finalErr, failure)
	require.Equal(t, 1, mdl.calls)
}
