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

package compose

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type toolsNodeCheckpointState struct {
	Messages []*schema.Message
}

func init() {
	schema.RegisterName[*toolsNodeCheckpointState]("_eino_test_tools_node_checkpoint_state")
}

func toolsNodeCheckpointContext(state any) context.Context {
	address := Address{{Type: AddressSegmentNode, ID: "tools"}}
	ctx := core.PopulateInterruptState(context.Background(),
		map[string]Address{"interrupt": address},
		map[string]core.InterruptState{"interrupt": {State: state}})
	return AppendAddressSegment(ctx, AddressSegmentNode, "tools")
}

func TestRestoreToolsInterruptState(t *testing.T) {
	t.Run("legacy", func(t *testing.T) {
		input := schema.AssistantMessage("", []schema.ToolCall{{ID: "legacy"}})
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunState{
			Input:         input,
			ExecutedTools: map[string]string{"done": "result"},
		})
		got, executed, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.NoError(t, err)
		require.Same(t, input, got)
		require.Equal(t, map[string]string{"done": "result"}, executed)
	})

	t.Run("v1", func(t *testing.T) {
		toolCalls := []schema.ToolCall{{ID: "rewritten"}}
		enhanced := map[string]*schema.ToolResult{"enhanced": {}}
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunStateV1{
			Version:               toolsInterruptAndRerunStateVersionV1,
			Role:                  schema.Assistant,
			ToolCalls:             toolCalls,
			ExecutedEnhancedTools: enhanced,
		})
		got, _, gotEnhanced, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, schema.Assistant, got.Role)
		require.Equal(t, toolCalls, got.ToolCalls)
		require.Empty(t, got.Content)
		require.Equal(t, enhanced, gotEnhanced)
	})

	t.Run("nil_legacy_input", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunState{})
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, "nil input")
	})

	t.Run("unsupported_version", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunStateV1{
			Version: toolsInterruptAndRerunStateVersionV1 + 1,
			Role:    schema.Assistant,
		})
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, "unsupported version")
	})

	t.Run("typed_nil_v1", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext((*toolsInterruptAndRerunStateV1)(nil))
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, "unsupported version")
	})

	t.Run("invalid_role", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunStateV1{
			Version: toolsInterruptAndRerunStateVersionV1,
			Role:    schema.User,
		})
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, "invalid role")
	})

	t.Run("invalid_type", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext("invalid")
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, "invalid interrupt state type")
	})
}

func TestToolsNodeWritesV1InterruptState(t *testing.T) {
	const toolName = "interrupting"
	interruptingTool := newCheckpointTestTool(&schema.ToolInfo{Name: toolName},
		func(ctx context.Context, _ *longRunningToolInput) (string, error) {
			return "", StatefulInterrupt(ctx, "interrupt", "state")
		})
	node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools: []componenttool.BaseTool{interruptingTool},
	})
	require.NoError(t, err)
	input := schema.AssistantMessage("large content must not be persisted in ToolsNode state",
		[]schema.ToolCall{{
			ID: "call",
			Function: schema.FunctionCall{
				Name:      toolName,
				Arguments: `{}`,
			},
		}})

	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "invoke",
			run: func() error {
				_, invokeErr := node.Invoke(context.Background(), input)
				return invokeErr
			},
		},
		{
			name: "stream",
			run: func() error {
				_, streamErr := node.Stream(context.Background(), input)
				return streamErr
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			var signal *core.InterruptSignal
			require.ErrorAs(t, err, &signal)
			state, ok := signal.State.(*toolsInterruptAndRerunStateV1)
			require.True(t, ok)
			require.Equal(t, toolsInterruptAndRerunStateVersionV1, state.Version)
			require.Equal(t, schema.Assistant, state.Role)
			require.Equal(t, input.ToolCalls, state.ToolCalls)
		})
	}
}

func TestToolsNodeV1ResumeUsesPrehandledToolCalls(t *testing.T) {
	const toolName = "rewritten"
	var preHandlerCalls int
	interruptingTool := newCheckpointTestTool(&schema.ToolInfo{Name: toolName},
		func(ctx context.Context, _ *longRunningToolInput) (string, error) {
			wasInterrupted, hasState, state := GetInterruptState[string](ctx)
			if !wasInterrupted {
				return "", StatefulInterrupt(ctx, "interrupt", "saved")
			}
			if !hasState || state != "saved" {
				return "", errors.New("tools node lost persisted tool state")
			}
			return "completed", nil
		})
	node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools: []componenttool.BaseTool{interruptingTool},
	})
	require.NoError(t, err)

	graph := NewGraph[*schema.Message, []*schema.Message](WithGenLocalState(
		func(context.Context) *testStruct { return &testStruct{} }))
	require.NoError(t, graph.AddToolsNode("tools", node, WithStatePreHandler(
		func(_ context.Context, input *schema.Message, _ *testStruct) (*schema.Message, error) {
			preHandlerCalls++
			if input == nil || len(input.ToolCalls) == 0 {
				return input, nil
			}
			copied := *input
			copied.ToolCalls = append([]schema.ToolCall(nil), input.ToolCalls...)
			copied.ToolCalls[0].Function.Name = toolName
			return &copied, nil
		})))
	require.NoError(t, graph.AddEdge(START, "tools"))
	require.NoError(t, graph.AddEdge("tools", END))
	store := newInMemoryStore()
	runnable, err := graph.Compile(context.Background(), WithCheckPointStore(store))
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "before-pre-handler",
			Arguments: `{}`,
		},
	}})
	_, err = runnable.Invoke(context.Background(), input, WithCheckPointID("tools-v1"))
	require.Error(t, err)
	require.Equal(t, 1, preHandlerCalls)

	output, err := runnable.Invoke(context.Background(), &schema.Message{},
		WithCheckPointID("tools-v1"))
	require.NoError(t, err)
	require.Equal(t, 2, preHandlerCalls, "resume preserves the existing pre-handler lifecycle")
	require.Len(t, output, 1)
	require.Equal(t, `"completed"`, output[0].Content)
	require.Equal(t, toolName, output[0].ToolName)
}

func TestCompactCheckpointToolsNodeState(t *testing.T) {
	message := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{"payload":"large"}`,
		},
	}})
	state := &toolsInterruptAndRerunStateV1{
		Version:   toolsInterruptAndRerunStateVersionV1,
		Role:      schema.Assistant,
		ToolCalls: append([]schema.ToolCall(nil), message.ToolCalls...),
	}
	cp := &checkpoint{
		State: struct {
			Messages []*schema.Message
		}{Messages: []*schema.Message{schema.UserMessage("request"), message}},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {State: state},
		},
	}

	compactCheckpointToolsNodeState(cp)
	compacted := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, compacted.ToolCalls)
	require.NotNil(t, compacted.ToolCallsSource)
	require.Equal(t, 1, compacted.ToolCallsSource.MessageIndex)

	require.NoError(t, hydrateCheckpointToolsNodeState(cp))
	hydrated := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Equal(t, message.ToolCalls, hydrated.ToolCalls)
	require.Nil(t, hydrated.ToolCallsSource)

	t.Run("mismatch_remains_inline", func(t *testing.T) {
		mismatch := *state
		mismatch.ToolCalls = append([]schema.ToolCall(nil), state.ToolCalls...)
		mismatch.ToolCalls[0].Function.Arguments = `{"different":true}`
		mismatchCP := &checkpoint{
			State: cp.State,
			InterruptID2State: map[string]core.InterruptState{
				"tool": {State: &mismatch},
			},
		}
		compactCheckpointToolsNodeState(mismatchCP)
		got := mismatchCP.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.NotEmpty(t, got.ToolCalls)
		require.Nil(t, got.ToolCallsSource)
	})

	t.Run("duplicate_source_remains_inline", func(t *testing.T) {
		duplicateCP := &checkpoint{
			State: struct {
				Messages []*schema.Message
			}{Messages: []*schema.Message{message, message}},
			InterruptID2State: map[string]core.InterruptState{
				"tool": {State: state},
			},
		}
		compactCheckpointToolsNodeState(duplicateCP)
		got := duplicateCP.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.NotEmpty(t, got.ToolCalls)
		require.Nil(t, got.ToolCallsSource)
	})

	t.Run("corrupt_source_fails", func(t *testing.T) {
		corrupt := *compacted
		corrupt.ToolCallsSource = &toolsInterruptToolCallsSourceV1{
			MessageIndex: 99,
			Digest:       compacted.ToolCallsSource.Digest,
		}
		corruptCP := &checkpoint{
			State: cp.State,
			InterruptID2State: map[string]core.InterruptState{
				"tool": {State: &corrupt},
			},
		}
		require.ErrorContains(t, hydrateCheckpointToolsNodeState(corruptCP), "invalid tool calls source")

		corrupt.ToolCallsSource = &toolsInterruptToolCallsSourceV1{
			MessageIndex: 1,
			Digest:       "corrupt",
		}
		corruptCP.InterruptID2State["tool"] = core.InterruptState{State: &corrupt}
		require.ErrorContains(t, hydrateCheckpointToolsNodeState(corruptCP), "do not match metadata")

		corrupt.Role = schema.User
		corrupt.ToolCallsSource = compacted.ToolCallsSource
		corruptCP.InterruptID2State["tool"] = core.InterruptState{State: &corrupt}
		require.ErrorContains(t, hydrateCheckpointToolsNodeState(corruptCP), "source role")
	})
}

func TestToolsNodeV1EnhancedSiblingResume(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "invoke"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			const (
				enhancedName  = "enhanced"
				interruptName = "interrupt"
			)
			enhancedCalls := 0
			enhanced := &enhancedInvokableTool{
				info: &schema.ToolInfo{Name: enhancedName},
				fn: func(context.Context, *schema.ToolArgument) (*schema.ToolResult, error) {
					enhancedCalls++
					return &schema.ToolResult{Parts: []schema.ToolOutputPart{{
						Type: schema.ToolPartTypeText,
						Text: strings.Repeat("result", 128),
					}}}, nil
				},
			}
			interrupting := newCheckpointTestTool(&schema.ToolInfo{Name: interruptName},
				func(ctx context.Context, _ *longRunningToolInput) (string, error) {
					wasInterrupted, hasState, state := GetInterruptState[string](ctx)
					if !wasInterrupted {
						return "", StatefulInterrupt(ctx, "interrupt", "saved")
					}
					if !hasState || state != "saved" {
						return "", errors.New("interrupt state was not restored")
					}
					return "completed", nil
				})
			node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
				Tools: []componenttool.BaseTool{enhanced, interrupting},
			})
			require.NoError(t, err)

			graph := NewGraph[*schema.Message, []*schema.Message](WithGenLocalState(
				func(context.Context) *toolsNodeCheckpointState {
					return &toolsNodeCheckpointState{}
				}))
			require.NoError(t, graph.AddToolsNode("tools", node, WithStatePreHandler(
				func(_ context.Context, input *schema.Message,
					state *toolsNodeCheckpointState) (*schema.Message, error) {
					if input != nil && len(input.ToolCalls) > 0 {
						state.Messages = []*schema.Message{input}
					}
					return state.Messages[len(state.Messages)-1], nil
				})))
			require.NoError(t, graph.AddEdge(START, "tools"))
			require.NoError(t, graph.AddEdge("tools", END))
			store := newInMemoryStore()
			runnable, err := graph.Compile(context.Background(), WithCheckPointStore(store))
			require.NoError(t, err)

			input := schema.AssistantMessage("", []schema.ToolCall{
				{
					ID: "enhanced-call",
					Function: schema.FunctionCall{
						Name:      enhancedName,
						Arguments: "input",
					},
				},
				{
					ID: "interrupt-call",
					Function: schema.FunctionCall{
						Name:      interruptName,
						Arguments: `{}`,
					},
				},
			})
			if streaming {
				_, invokeErr := runnable.Stream(context.Background(), input,
					WithCheckPointID("enhanced-v1"))
				require.Error(t, invokeErr)
			} else {
				_, err = runnable.Invoke(context.Background(), input,
					WithCheckPointID("enhanced-v1"))
				require.Error(t, err)
			}
			require.Equal(t, 1, enhancedCalls)

			if streaming {
				stream, err := runnable.Stream(context.Background(), &schema.Message{},
					WithCheckPointID("enhanced-v1"))
				require.NoError(t, err)
				var output []*schema.Message
				for {
					chunk, receiveErr := stream.Recv()
					if receiveErr == io.EOF {
						break
					}
					require.NoError(t, receiveErr)
					for _, message := range chunk {
						if message != nil {
							output = append(output, message)
						}
					}
				}
				require.Len(t, output, 2)
			} else {
				output, invokeErr := runnable.Invoke(context.Background(), &schema.Message{},
					WithCheckPointID("enhanced-v1"))
				require.NoError(t, invokeErr)
				require.Len(t, output, 2)
			}
			require.Equal(t, 1, enhancedCalls, "successful enhanced sibling must be reused")
		})
	}
}

func TestAttack_ToolsNodeV1RejectsInlineAndReference(t *testing.T) {
	sourceCalls := []schema.ToolCall{{
		ID: "source",
		Function: schema.FunctionCall{
			Name:      "source",
			Arguments: `{}`,
		},
	}}
	inlineCalls := []schema.ToolCall{{
		ID: "inline",
		Function: schema.FunctionCall{
			Name:      "inline",
			Arguments: `{}`,
		},
	}}
	digest, ok := checkpointToolCallsDigest(sourceCalls)
	require.True(t, ok)
	cp := &checkpoint{
		State: &toolsNodeCheckpointState{Messages: []*schema.Message{
			schema.AssistantMessage("", sourceCalls),
		}},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {State: &toolsInterruptAndRerunStateV1{
				Version:   toolsInterruptAndRerunStateVersionV1,
				Role:      schema.Assistant,
				ToolCalls: inlineCalls,
				ToolCallsSource: &toolsInterruptToolCallsSourceV1{
					MessageIndex: 0,
					Digest:       digest,
				},
			}},
		},
	}

	require.ErrorContains(t, hydrateCheckpointToolsNodeState(cp),
		"both inline tool calls and a source reference")
}

func TestAttack_ToolsNodeRejectsDuplicateToolCallIDs(t *testing.T) {
	input := schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:       "duplicate",
			Function: schema.FunctionCall{Name: "standard"},
		},
		{
			ID:       "duplicate",
			Function: schema.FunctionCall{Name: "enhanced"},
		},
	})

	_, err := (&ToolsNode{}).genToolCallTasks(context.Background(), &toolsTuple{}, input,
		map[string]string{"duplicate": "completed"}, nil, false)
	require.ErrorContains(t, err, `duplicate tool call ID "duplicate"`)
}

func TestAttack_ToolsNodeV1RejectsConflictingResultState(t *testing.T) {
	t.Run("duplicate_tool_calls", func(t *testing.T) {
		state := &toolsInterruptAndRerunStateV1{
			Version: toolsInterruptAndRerunStateVersionV1,
			Role:    schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{ID: "duplicate"},
				{ID: "duplicate"},
			},
		}
		ctx := toolsNodeCheckpointContext(state)
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, `duplicate tool call ID "duplicate"`)
	})

	t.Run("standard_and_enhanced", func(t *testing.T) {
		state := &toolsInterruptAndRerunStateV1{
			Version:               toolsInterruptAndRerunStateVersionV1,
			Role:                  schema.Assistant,
			ToolCalls:             []schema.ToolCall{{ID: "duplicate"}},
			ExecutedTools:         map[string]string{"duplicate": "result"},
			ExecutedEnhancedTools: map[string]*schema.ToolResult{"duplicate": {}},
		}
		ctx := toolsNodeCheckpointContext(state)
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, `duplicate executed tool call ID "duplicate"`)
	})

	t.Run("standard_and_enhanced_error_is_deterministic", func(t *testing.T) {
		state := &toolsInterruptAndRerunStateV1{
			Version:   toolsInterruptAndRerunStateVersionV1,
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{{ID: "a"}, {ID: "z"}},
			ExecutedTools: map[string]string{
				"z": "standard-z",
				"a": "standard-a",
			},
			ExecutedEnhancedTools: map[string]*schema.ToolResult{
				"z": {},
				"a": {},
			},
		}
		for i := 0; i < 100; i++ {
			require.EqualError(t, validateToolsInterruptAndRerunStateV1(state),
				`tools node interrupt state has duplicate executed tool call ID "a"`)
		}
	})

	t.Run("executed_and_rerun", func(t *testing.T) {
		state := &toolsInterruptAndRerunStateV1{
			Version:       toolsInterruptAndRerunStateVersionV1,
			Role:          schema.Assistant,
			ToolCalls:     []schema.ToolCall{{ID: "duplicate"}},
			ExecutedTools: map[string]string{"duplicate": "result"},
			RerunTools:    []string{"duplicate"},
		}
		ctx := toolsNodeCheckpointContext(state)
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, `both executed and pending rerun`)
	})

	t.Run("duplicate_rerun", func(t *testing.T) {
		state := &toolsInterruptAndRerunStateV1{
			Version:   toolsInterruptAndRerunStateVersionV1,
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{{ID: "duplicate"}},
			RerunTools: []string{
				"duplicate",
				"duplicate",
			},
		}
		ctx := toolsNodeCheckpointContext(state)
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.ErrorContains(t, err, `duplicate rerun tool call ID "duplicate"`)
	})
}
