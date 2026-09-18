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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/serialization"
	"github.com/cloudwego/eino/schema"
)

type toolsNodeCheckpointState struct {
	Messages  []*schema.Message
	Unrelated string
}

type toolsNodeCheckpointEmbeddedMessages struct {
	Messages []*schema.Message
}

type toolsNodeCheckpointNilEmbeddedState struct {
	*toolsNodeCheckpointEmbeddedMessages
}

type toolsNodeCheckpointJSONExtra struct {
	Behavior string
	Value    string
}

var toolsNodeCheckpointJSONCalls uint32

func (v *toolsNodeCheckpointJSONExtra) MarshalJSON() ([]byte, error) {
	call := atomic.AddUint32(&toolsNodeCheckpointJSONCalls, 1)
	if v.Behavior == "panic" {
		panic("ToolCall.Extra JSON marshaler must not be called")
	}
	return []byte(fmt.Sprintf(`{"call":%d}`, call)), nil
}

type toolsNodeCheckpointGobExtra struct {
	Value    string
	Behavior string
	cached   []byte
}

var toolsNodeCheckpointGobCalls uint32

func (v *toolsNodeCheckpointGobExtra) GobEncode() ([]byte, error) {
	atomic.AddUint32(&toolsNodeCheckpointGobCalls, 1)
	if v.Behavior == "panic" {
		panic("ToolsNode GobEncoder must not be called while digesting")
	}
	if v.Behavior == "lazy" && v.cached == nil {
		v.cached = []byte(v.Value)
	}
	if v.cached == nil {
		return []byte(v.Value), nil
	}
	return append([]byte(nil), v.cached...), nil
}

func (v *toolsNodeCheckpointGobExtra) GobDecode(data []byte) error {
	v.Value = string(data)
	v.cached = append([]byte(nil), data...)
	return nil
}

type toolsNodeCheckpointBinaryExtra struct {
	Value    string
	Behavior string
	cached   []byte
}

var toolsNodeCheckpointBinaryCalls uint32

func (v *toolsNodeCheckpointBinaryExtra) MarshalBinary() ([]byte, error) {
	atomic.AddUint32(&toolsNodeCheckpointBinaryCalls, 1)
	if v.Behavior == "panic" {
		panic("ToolsNode BinaryMarshaler must not be called while digesting")
	}
	if v.Behavior == "lazy" && v.cached == nil {
		v.cached = []byte(v.Value)
	}
	if v.cached == nil {
		return []byte(v.Value), nil
	}
	return append([]byte(nil), v.cached...), nil
}

type toolsNodeCheckpointCountingTool struct {
	info      *schema.ToolInfo
	infoCalls *int32
	runCalls  *int32
	result    string
}

func (t *toolsNodeCheckpointCountingTool) Info(context.Context) (*schema.ToolInfo, error) {
	atomic.AddInt32(t.infoCalls, 1)
	return t.info, nil
}

func (t *toolsNodeCheckpointCountingTool) InvokableRun(context.Context, string,
	...componenttool.Option,
) (string, error) {
	atomic.AddInt32(t.runCalls, 1)
	return t.result, nil
}

func init() {
	schema.RegisterName[*toolsNodeCheckpointState]("_eino_test_tools_node_checkpoint_state")
	schema.RegisterName[*toolsNodeCheckpointJSONExtra](
		"_eino_test_tools_node_checkpoint_json_extra")
	schema.RegisterName[*toolsNodeCheckpointGobExtra](
		"_eino_test_tools_node_checkpoint_gob_extra")
	schema.RegisterName[*schema.ToolInfo]("_eino_test_tools_node_checkpoint_tool_info")
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
			ExecutedTools: map[string]string{"legacy": "result"},
		})
		got, executed, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.NoError(t, err)
		require.Same(t, input, got)
		require.Equal(t, map[string]string{"legacy": "result"}, executed)
	})

	t.Run("v1", func(t *testing.T) {
		toolCalls := []schema.ToolCall{
			{ID: "standard"},
			{ID: "enhanced"},
			{ID: "rerun"},
		}
		enhanced := map[string]*schema.ToolResult{"enhanced": {}}
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunStateV1{
			Version:               toolsInterruptAndRerunStateVersionV1,
			Role:                  schema.Assistant,
			ToolCalls:             toolCalls,
			ExecutedTools:         map[string]string{"standard": "result"},
			ExecutedEnhancedTools: enhanced,
			RerunTools:            []string{"rerun"},
		})
		got, gotStandard, gotEnhanced, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.NoError(t, err)
		require.Equal(t, schema.Assistant, got.Role)
		require.Equal(t, toolCalls, got.ToolCalls)
		require.Empty(t, got.Content)
		require.Equal(t, map[string]string{"standard": "result"}, gotStandard)
		require.Equal(t, enhanced, gotEnhanced)
	})

	t.Run("nil_legacy_input", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunState{})
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.EqualError(t, err, "tools node legacy interrupt state has nil input")
	})

	t.Run("unsupported_version", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunStateV1{
			Version: toolsInterruptAndRerunStateVersionV1 + 1,
			Role:    schema.Assistant,
		})
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.EqualError(t, err, "tools node interrupt state has unsupported version")
	})

	t.Run("typed_nil_v1", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext((*toolsInterruptAndRerunStateV1)(nil))
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.EqualError(t, err, "tools node interrupt state has unsupported version")
	})

	t.Run("invalid_role", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext(&toolsInterruptAndRerunStateV1{
			Version: toolsInterruptAndRerunStateVersionV1,
			Role:    schema.User,
		})
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.EqualError(t, err, `tools node interrupt state has invalid role "user"`)
	})

	t.Run("invalid_type", func(t *testing.T) {
		ctx := toolsNodeCheckpointContext("invalid")
		_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
		require.EqualError(t, err, "tools node has invalid interrupt state type string")
	})
}

func TestToolsNodeRejectsMissingInterruptState(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "invoke"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			var toolCalls int32
			node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
				Tools: []componenttool.BaseTool{newCheckpointTestTool(
					&schema.ToolInfo{Name: "tool"},
					func(context.Context, *longRunningToolInput) (string, error) {
						atomic.AddInt32(&toolCalls, 1)
						return "unexpected", nil
					},
				)},
			})
			require.NoError(t, err)
			input := schema.AssistantMessage("", []schema.ToolCall{{
				ID: "call",
				Function: schema.FunctionCall{
					Name:      "tool",
					Arguments: `{}`,
				},
			}})

			ctx := toolsNodeCheckpointContext(nil)
			if streaming {
				_, err = node.Stream(ctx, input)
			} else {
				_, err = node.Invoke(ctx, input)
			}

			require.Equal(t, int32(0), atomic.LoadInt32(&toolCalls))
			require.EqualError(t, err, "tools node interrupt state is missing")
		})
	}
}

func TestToolsNodeResumeRestoresStateBeforeRuntimeOverrides(t *testing.T) {
	tests := []struct {
		name  string
		state any
		want  string
	}{
		{
			name: "missing",
			want: "tools node interrupt state is missing",
		},
		{
			name:  "invalid",
			state: "invalid",
			want:  "tools node has invalid interrupt state type string",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "invoke"
				if streaming {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					var overrideInfoCalls int32
					var overrideRunCalls int32
					node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
						Tools: []componenttool.BaseTool{newCheckpointTestTool(
							&schema.ToolInfo{Name: "base"},
							func(context.Context, *longRunningToolInput) (string, error) {
								return "base", nil
							},
						)},
					})
					require.NoError(t, err)
					overrideTool := &toolsNodeCheckpointCountingTool{
						info:      &schema.ToolInfo{Name: "override"},
						infoCalls: &overrideInfoCalls,
						runCalls:  &overrideRunCalls,
					}
					opts := []ToolsNodeOption{
						WithToolList(overrideTool),
						WithToolAliases(map[string]ToolAliasConfig{
							"override": {NameAliases: []string{"alias"}},
						}),
					}

					ctx := toolsNodeCheckpointContext(tt.state)
					if streaming {
						_, err = node.Stream(ctx, nil, opts...)
					} else {
						_, err = node.Invoke(ctx, nil, opts...)
					}

					require.EqualError(t, err, tt.want)
					require.Equal(t, []int32{0, 0}, []int32{
						atomic.LoadInt32(&overrideInfoCalls),
						atomic.LoadInt32(&overrideRunCalls),
					}, "override Info and tool execution counters")
				})
			}
		})
	}
}

func TestToolsNodeResumeHonorsRuntimeToolAndAliasOverrides(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "invoke"
		if streaming {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			var overrideInfoCalls int32
			var overrideRunCalls int32
			node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
				Tools: []componenttool.BaseTool{newCheckpointTestTool(
					&schema.ToolInfo{Name: "base"},
					func(context.Context, *longRunningToolInput) (string, error) {
						return "base", nil
					},
				)},
			})
			require.NoError(t, err)
			overrideTool := &toolsNodeCheckpointCountingTool{
				info:      &schema.ToolInfo{Name: "override"},
				infoCalls: &overrideInfoCalls,
				runCalls:  &overrideRunCalls,
				result:    "override result",
			}
			state := &toolsInterruptAndRerunState{
				Input: schema.AssistantMessage("", []schema.ToolCall{{
					ID: "call",
					Function: schema.FunctionCall{
						Name:      "alias",
						Arguments: `{}`,
					},
				}}),
			}
			opts := []ToolsNodeOption{
				WithToolList(overrideTool),
				WithToolAliases(map[string]ToolAliasConfig{
					"override": {NameAliases: []string{"alias"}},
				}),
			}

			var output []*schema.Message
			ctx := toolsNodeCheckpointContext(state)
			if streaming {
				reader, streamErr := node.Stream(ctx, nil, opts...)
				require.NoError(t, streamErr)
				defer reader.Close()
				for {
					chunk, receiveErr := reader.Recv()
					if receiveErr == io.EOF {
						break
					}
					require.NoError(t, receiveErr)
					output = append(output, chunk...)
				}
			} else {
				output, err = node.Invoke(ctx, nil, opts...)
				require.NoError(t, err)
			}

			require.Equal(t, []int32{1, 1}, []int32{
				atomic.LoadInt32(&overrideInfoCalls),
				atomic.LoadInt32(&overrideRunCalls),
			}, "override Info and tool execution counters")
			require.Len(t, output, 1)
			require.Equal(t, "alias", output[0].ToolName)
			require.Equal(t, "override result", output[0].Content)
		})
	}
}

func TestToolsNodeCheckpointResumeHonorsRuntimeToolAndAliasOverrides(t *testing.T) {
	for _, nested := range []bool{false, true} {
		scope := "root"
		if nested {
			scope = "nested_input_only"
		}
		for _, streaming := range []bool{false, true} {
			mode := "invoke"
			if streaming {
				mode = "stream"
			}
			t.Run(scope+"/"+mode, func(t *testing.T) {
				var overrideInfoCalls int32
				var overrideRunCalls int32
				node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
					Tools: []componenttool.BaseTool{newCheckpointTestTool(
						&schema.ToolInfo{Name: "base"},
						func(context.Context, *longRunningToolInput) (string, error) {
							return "base", nil
						},
					)},
				})
				require.NoError(t, err)
				overrideTool := &toolsNodeCheckpointCountingTool{
					info:      &schema.ToolInfo{Name: "override"},
					infoCalls: &overrideInfoCalls,
					runCalls:  &overrideRunCalls,
					result:    "override result",
				}

				graph := NewGraph[*schema.Message, []*schema.Message]()
				route := Address{{Type: AddressSegmentRunnable, ID: "root"}}
				inputKey := "tools"
				optionPath := NewNodePath("tools")
				if nested {
					child := NewGraph[*schema.Message, []*schema.Message]()
					require.NoError(t, child.AddToolsNode("tools", node))
					require.NoError(t, child.AddEdge(START, "tools"))
					require.NoError(t, child.AddEdge("tools", END))
					require.NoError(t, graph.AddGraphNode("subgraph", child))
					require.NoError(t, graph.AddEdge(START, "subgraph"))
					require.NoError(t, graph.AddEdge("subgraph", END))
					route = append(route,
						AddressSegment{Type: AddressSegmentNode, ID: "subgraph"})
					inputKey = "subgraph"
					optionPath = NewNodePath("subgraph", "tools")
				} else {
					require.NoError(t, graph.AddToolsNode("tools", node))
					require.NoError(t, graph.AddEdge(START, "tools"))
					require.NoError(t, graph.AddEdge("tools", END))
				}
				route = append(route, AddressSegment{Type: AddressSegmentNode, ID: "tools"})

				store := newInMemoryStore()
				runnable, err := graph.Compile(context.Background(),
					WithGraphName("root"),
					WithCheckPointStore(store))
				require.NoError(t, err)
				resumedInput := schema.AssistantMessage("", []schema.ToolCall{{
					ID: "call",
					Function: schema.FunctionCall{
						Name:      "alias",
						Arguments: `{}`,
					},
				}})
				digest, ok := checkpointToolCallsDigest(resumedInput.ToolCalls)
				require.True(t, ok)
				cp := &checkpoint{
					StateLayoutVersion: checkpointStateLayoutVersionV1,
					Inputs:             map[string]any{inputKey: resumedInput},
					State:              &toolsNodeCheckpointState{Messages: []*schema.Message{resumedInput}},
					InterruptID2Addr: map[string]Address{
						"interrupt": route,
					},
					InterruptID2State: map[string]core.InterruptState{
						"interrupt": {
							State: &toolsInterruptAndRerunStateV1{
								Version:    toolsInterruptAndRerunStateVersionV1,
								Role:       schema.Assistant,
								RerunTools: []string{"call"},
								ToolCallsSource: &toolsInterruptToolCallsSourceV1{
									MessageIndex: 0,
									Digest:       digest,
								},
							},
						},
						checkpointLayoutSentinelID: {
							State: &checkpointLayoutSentinelV1{
								Version: checkpointStateLayoutVersionV1,
							},
						},
					},
				}
				data, err := (&serialization.InternalSerializer{}).Marshal(cp)
				require.NoError(t, err)
				require.NoError(t, store.Set(context.Background(), "valid-tools-state", data))
				opts := []Option{
					WithCheckPointID("valid-tools-state"),
					WithToolsNodeOption(
						WithToolList(overrideTool),
						WithToolAliases(map[string]ToolAliasConfig{
							"override": {NameAliases: []string{"alias"}},
						}),
					).DesignateNodeWithPath(optionPath),
				}

				var output []*schema.Message
				if streaming {
					reader, streamErr := runnable.Stream(context.Background(), nil, opts...)
					require.NoError(t, streamErr)
					defer reader.Close()
					for {
						chunk, receiveErr := reader.Recv()
						if receiveErr == io.EOF {
							break
						}
						require.NoError(t, receiveErr)
						output = append(output, chunk...)
					}
				} else {
					output, err = runnable.Invoke(context.Background(), nil, opts...)
					require.NoError(t, err)
				}

				require.Equal(t, []int32{1, 1}, []int32{
					atomic.LoadInt32(&overrideInfoCalls),
					atomic.LoadInt32(&overrideRunCalls),
				}, "override Info and tool execution counters")
				require.Len(t, output, 1)
				require.Equal(t, "alias", output[0].ToolName)
				require.Equal(t, "override result", output[0].Content)
			})
		}
	}
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

func TestAttack_ToolsNodeV1RoundTripsSingleEmptyToolCallID(t *testing.T) {
	const toolName = "interrupting"
	interruptingTool := newCheckpointTestTool(&schema.ToolInfo{Name: toolName},
		func(ctx context.Context, _ *longRunningToolInput) (string, error) {
			return "", StatefulInterrupt(ctx, "interrupt", "state")
		})
	node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools: []componenttool.BaseTool{interruptingTool},
	})
	require.NoError(t, err)
	input := schema.AssistantMessage("", []schema.ToolCall{{
		Function: schema.FunctionCall{
			Name:      toolName,
			Arguments: `{}`,
		},
	}})

	tests := []struct {
		name string
		run  func(context.Context, *schema.Message) error
	}{
		{
			name: "invoke",
			run: func(ctx context.Context, input *schema.Message) error {
				_, invokeErr := node.Invoke(ctx, input)
				return invokeErr
			},
		},
		{
			name: "stream",
			run: func(ctx context.Context, input *schema.Message) error {
				_, streamErr := node.Stream(ctx, input)
				return streamErr
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(context.Background(), input)
			var signal *core.InterruptSignal
			require.ErrorAs(t, err, &signal)
			state, ok := signal.State.(*toolsInterruptAndRerunStateV1)
			require.True(t, ok)
			require.Equal(t, []schema.ToolCall(input.ToolCalls), state.ToolCalls)
			require.Equal(t, []string{""}, state.RerunTools)

			restored, executed, enhanced, err := restoreToolsInterruptState(
				toolsNodeCheckpointContext(state), nil, nil, nil)
			require.NoError(t, err)
			require.Equal(t, input.ToolCalls, restored.ToolCalls)
			require.Empty(t, executed)
			require.Empty(t, enhanced)
		})
	}
}

func TestAttack_ToolsNodeV1RejectsAllExecutedState(t *testing.T) {
	const toolName = "already-executed"
	resultForms := []struct {
		name     string
		enhanced bool
	}{
		{name: "standard"},
		{name: "enhanced", enhanced: true},
	}
	runModes := []struct {
		name   string
		stream bool
	}{
		{name: "invoke"},
		{name: "stream", stream: true},
	}

	for _, resultForm := range resultForms {
		t.Run(resultForm.name, func(t *testing.T) {
			for _, runMode := range runModes {
				t.Run(runMode.name, func(t *testing.T) {
					state := &toolsInterruptAndRerunStateV1{
						Version: toolsInterruptAndRerunStateVersionV1,
						Role:    schema.Assistant,
						ToolCalls: []schema.ToolCall{{
							Function: schema.FunctionCall{
								Name:      toolName,
								Arguments: `{}`,
							},
						}},
					}
					if resultForm.enhanced {
						state.ExecutedEnhancedTools = map[string]*schema.ToolResult{"": {}}
					} else {
						state.ExecutedTools = map[string]string{"": "result"}
					}

					ctx := toolsNodeCheckpointContext(state)
					node := &ToolsNode{}
					var err error
					if runMode.stream {
						_, err = node.Stream(ctx, nil)
					} else {
						_, err = node.Invoke(ctx, nil)
					}
					require.EqualError(t, err,
						"tools node interrupt state has no pending rerun tools")
				})
			}
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
	var interruptErr *interruptError
	require.ErrorAs(t, err, &interruptErr)
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
		require.EqualError(t, hydrateCheckpointToolsNodeState(corruptCP),
			`tools node interrupt state "tool" has invalid tool calls source index 99`)

		corrupt.ToolCallsSource = &toolsInterruptToolCallsSourceV1{
			MessageIndex: 1,
			Digest:       "corrupt",
		}
		corruptCP.InterruptID2State["tool"] = core.InterruptState{State: &corrupt}
		require.EqualError(t, hydrateCheckpointToolsNodeState(corruptCP),
			`tools node interrupt state "tool" source tool calls do not match metadata`)

		corrupt.Role = schema.User
		corrupt.ToolCallsSource = compacted.ToolCallsSource
		corruptCP.InterruptID2State["tool"] = core.InterruptState{State: &corrupt}
		require.EqualError(t, hydrateCheckpointToolsNodeState(corruptCP),
			`tools node interrupt state "tool" source role "assistant" does not match "user"`)
	})

	t.Run("clone_failure_is_reported", func(t *testing.T) {
		unregistered := struct {
			Value string `json:"value"`
		}{Value: "unsupported"}
		unclonableMessage := schema.AssistantMessage("", []schema.ToolCall{{
			ID:    "unclonable",
			Extra: map[string]any{"unregistered": unregistered},
		}})
		unclonableCP := &checkpoint{
			State: &toolsNodeCheckpointState{
				Messages: []*schema.Message{unclonableMessage},
			},
			InterruptID2State: map[string]core.InterruptState{
				"tool": {
					State: &toolsInterruptAndRerunStateV1{
						Version:   toolsInterruptAndRerunStateVersionV1,
						Role:      schema.Assistant,
						ToolCalls: unclonableMessage.ToolCalls,
					},
				},
			},
		}
		compactCheckpointToolsNodeState(unclonableCP)
		got := unclonableCP.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.NotNil(t, got.ToolCallsSource)

		err := hydrateCheckpointToolsNodeState(unclonableCP)
		require.ErrorContains(t, err,
			`tools node interrupt state "tool" failed to clone source tool calls: failed to marshal tool calls`)
	})

	t.Run("nil_embedded_messages_stays_inline", func(t *testing.T) {
		inline := &toolsInterruptAndRerunStateV1{
			Version:   toolsInterruptAndRerunStateVersionV1,
			Role:      schema.Assistant,
			ToolCalls: append([]schema.ToolCall(nil), message.ToolCalls...),
		}
		nilEmbeddedCP := &checkpoint{
			State: &toolsNodeCheckpointNilEmbeddedState{},
			InterruptID2State: map[string]core.InterruptState{
				"tool": {State: inline},
			},
		}

		require.NotPanics(t, func() {
			compactCheckpointToolsNodeState(nilEmbeddedCP)
		})
		got := nilEmbeddedCP.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.Equal(t, message.ToolCalls, got.ToolCalls)
		require.Nil(t, got.ToolCallsSource)
	})
}

func TestToolsNodeCheckpointSignedZeroSourceMismatchStaysInlineAcrossGob(t *testing.T) {
	negativeFloat32 := math.Float32frombits(1 << 31)
	negativeFloat64 := math.Float64frombits(1 << 63)
	newToolCalls := func(float32Value float32, float64Value float64) []schema.ToolCall {
		return []schema.ToolCall{{
			ID: "call",
			Extra: map[string]any{
				"float32":    float32Value,
				"float64":    float64Value,
				"complex64":  complex(float32Value, float32Value),
				"complex128": complex(float64Value, float64Value),
			},
		}}
	}
	sourceToolCalls := newToolCalls(0, 0)
	targetToolCalls := newToolCalls(negativeFloat32, negativeFloat64)
	cp := &checkpoint{
		State: &toolsNodeCheckpointState{Messages: []*schema.Message{
			schema.AssistantMessage("", sourceToolCalls),
		}},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {State: &toolsInterruptAndRerunStateV1{
				Version:    toolsInterruptAndRerunStateVersionV1,
				Role:       schema.Assistant,
				ToolCalls:  targetToolCalls,
				RerunTools: []string{"call"},
			}},
		},
	}

	compactCheckpointToolsNodeState(cp)
	compacted := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Equal(t, targetToolCalls, compacted.ToolCalls)
	require.Nil(t, compacted.ToolCallsSource)

	store := newInMemoryStore()
	pointer := newCheckPointer(nil, nil, store, &checkpointGobSerializer{})
	require.NoError(t, pointer.set(context.Background(), "signed-zero", cp))
	restored, exists, err := pointer.get(context.Background(), "signed-zero")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, hydrateCheckpointToolsNodeState(restored))

	hydrated := restored.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, hydrated.ToolCallsSource)
	require.Len(t, hydrated.ToolCalls, 1)
	extra := hydrated.ToolCalls[0].Extra
	require.True(t, math.Signbit(float64(extra["float32"].(float32))))
	require.True(t, math.Signbit(extra["float64"].(float64)))
	complex64Value := extra["complex64"].(complex64)
	require.True(t, math.Signbit(float64(real(complex64Value))))
	require.True(t, math.Signbit(float64(imag(complex64Value))))
	complex128Value := extra["complex128"].(complex128)
	require.True(t, math.Signbit(real(complex128Value)))
	require.True(t, math.Signbit(imag(complex128Value)))
}

func TestToolsNodeToolCallsDigestIgnoresUserJSONDuringCheckpointRoundTrip(t *testing.T) {
	for _, behavior := range []string{"panic", "nondeterministic"} {
		t.Run(behavior, func(t *testing.T) {
			atomic.StoreUint32(&toolsNodeCheckpointJSONCalls, 0)
			toolCalls := []schema.ToolCall{{
				ID:   "call",
				Type: "function",
				Function: schema.FunctionCall{
					Name:      "tool",
					Arguments: `{"value":"persisted"}`,
				},
				Extra: map[string]any{
					"unsafe-json": &toolsNodeCheckpointJSONExtra{
						Behavior: behavior,
						Value:    "gob-visible",
					},
				},
			}}
			cp := &checkpoint{
				State: &toolsNodeCheckpointState{Messages: []*schema.Message{
					schema.AssistantMessage("", toolCalls),
				}},
				InterruptID2State: map[string]core.InterruptState{
					"tool": {State: &toolsInterruptAndRerunStateV1{
						Version:    toolsInterruptAndRerunStateVersionV1,
						Role:       schema.Assistant,
						ToolCalls:  toolCalls,
						RerunTools: []string{"call"},
					}},
				},
			}

			compactCheckpointToolsNodeState(cp)
			compacted := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
			require.Nil(t, compacted.ToolCalls)
			require.NotNil(t, compacted.ToolCallsSource)

			store := newInMemoryStore()
			pointer := newCheckPointer(nil, nil, store, &serialization.InternalSerializer{})
			require.NotPanics(t, func() {
				require.NoError(t, pointer.set(context.Background(), "unsafe-json", cp))
			})
			restored, exists, err := pointer.get(context.Background(), "unsafe-json")
			require.NoError(t, err)
			require.True(t, exists)
			require.NotPanics(t, func() {
				require.NoError(t, hydrateCheckpointToolsNodeState(restored))
			})

			hydrated := restored.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
			require.Nil(t, hydrated.ToolCallsSource)
			require.Equal(t, toolCalls, hydrated.ToolCalls)
			require.Equal(t, uint32(0), atomic.LoadUint32(&toolsNodeCheckpointJSONCalls))
		})
	}
}

func TestToolsNodeGobCheckpointRejectsAdjacentLargeIntegerTampering(t *testing.T) {
	for _, tt := range []struct {
		name     string
		original string
		tampered string
	}{
		{
			name:     "integer",
			original: "9007199254740993",
			tampered: "9007199254740992",
		},
		{
			name:     "fraction",
			original: "9007199254740993.0",
			tampered: "9007199254740992.0",
		},
		{
			name:     "exponent",
			original: "9007199254740993e0",
			tampered: "9007199254740992e0",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			testToolsNodeGobCheckpointRejectsLargeIntegerTampering(
				t, tt.original, tt.tampered)
		})
	}
}

func testToolsNodeGobCheckpointRejectsLargeIntegerTampering(
	t *testing.T, original, tampered string,
) {
	t.Helper()
	toolInfo := &schema.ToolInfo{
		Name: "large-integer",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Maximum: json.Number(original),
		}),
	}
	toolCalls := []schema.ToolCall{{
		ID: "call",
		Extra: map[string]any{
			"tool_info": toolInfo,
		},
	}}
	cp := &checkpoint{
		State: &toolsNodeCheckpointState{Messages: []*schema.Message{
			schema.AssistantMessage("", toolCalls),
		}},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {State: &toolsInterruptAndRerunStateV1{
				Version:    toolsInterruptAndRerunStateVersionV1,
				Role:       schema.Assistant,
				ToolCalls:  toolCalls,
				RerunTools: []string{"call"},
			}},
		},
	}
	compactCheckpointToolsNodeState(cp)
	compacted := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, compacted.ToolCalls)
	require.NotNil(t, compacted.ToolCallsSource)

	store := newInMemoryStore()
	pointer := newCheckPointer(nil, nil, store, &checkpointGobSerializer{})
	require.NoError(t, pointer.set(context.Background(), "original", cp))
	loaded, exists, err := pointer.get(context.Background(), "original")
	require.NoError(t, err)
	require.True(t, exists)

	loadedState := loaded.State.(*toolsNodeCheckpointState)
	loadedInfo := loadedState.Messages[0].ToolCalls[0].Extra["tool_info"].(*schema.ToolInfo)
	loadedSchema, err := loadedInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	require.Equal(t, json.Number(original), loadedSchema.Maximum)
	loadedSchema.Maximum = json.Number(tampered)

	require.NoError(t, pointer.set(context.Background(), "tampered", loaded))
	restored, exists, err := pointer.get(context.Background(), "tampered")
	require.NoError(t, err)
	require.True(t, exists)
	restoredInfo := restored.State.(*toolsNodeCheckpointState).
		Messages[0].ToolCalls[0].Extra["tool_info"].(*schema.ToolInfo)
	restoredSchema, err := restoredInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	require.Equal(t, json.Number(tampered), restoredSchema.Maximum)

	require.EqualError(t, hydrateCheckpointToolsNodeState(restored),
		`tools node interrupt state "tool" source tool calls do not match metadata`)
}

func TestToolsNodeJSONStringOptionStaysInlineAcrossGobCheckpoint(t *testing.T) {
	testToolsNodeUnsupportedJSONStructTagStaysInlineAcrossGobCheckpoint(
		t,
		struct {
			Count int `json:"count,string"`
		}{Count: 1},
		map[string]any{"count": "1"},
	)
}

func TestToolsNodeInvalidJSONFieldNameStaysInlineAcrossGobCheckpoint(t *testing.T) {
	value := reflect.New(reflect.StructOf([]reflect.StructField{{
		Name: "Count",
		Type: reflect.TypeOf(0),
		Tag:  `json:"bad\\name"`,
	}})).Elem()
	value.Field(0).SetInt(1)
	testToolsNodeUnsupportedJSONStructTagStaysInlineAcrossGobCheckpoint(
		t, value.Interface(), map[string]any{"Count": float64(1)})
}

func testToolsNodeUnsupportedJSONStructTagStaysInlineAcrossGobCheckpoint(
	t *testing.T, value any, want map[string]any,
) {
	t.Helper()
	toolCalls := []schema.ToolCall{{
		ID: "call",
		Extra: map[string]any{
			"tool_info": &schema.ToolInfo{
				Name: "string-option",
				ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
					Default: value,
				}),
			},
		},
	}}
	cp := &checkpoint{
		State: &toolsNodeCheckpointState{Messages: []*schema.Message{
			schema.AssistantMessage("", toolCalls),
		}},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {State: &toolsInterruptAndRerunStateV1{
				Version:    toolsInterruptAndRerunStateVersionV1,
				Role:       schema.Assistant,
				ToolCalls:  toolCalls,
				RerunTools: []string{"call"},
			}},
		},
	}

	compactCheckpointToolsNodeState(cp)
	compacted := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Equal(t, toolCalls, compacted.ToolCalls)
	require.Nil(t, compacted.ToolCallsSource)

	store := newInMemoryStore()
	pointer := newCheckPointer(nil, nil, store, &checkpointGobSerializer{})
	require.NoError(t, pointer.set(context.Background(), "string-option", cp))
	restored, exists, err := pointer.get(context.Background(), "string-option")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, hydrateCheckpointToolsNodeState(restored))

	restoredState := restored.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, restoredState.ToolCallsSource)
	require.Len(t, restoredState.ToolCalls, 1)
	restoredInfo := restoredState.ToolCalls[0].Extra["tool_info"].(*schema.ToolInfo)
	restoredSchema, err := restoredInfo.ParamsOneOf.ToJSONSchema()
	require.NoError(t, err)
	require.Equal(t, want, restoredSchema.Default)
}

func TestToolsNodeToolCallsWithExternalMarshalersStayInlineWithoutCalling(t *testing.T) {
	tests := []struct {
		name  string
		value any
		calls *uint32
	}{
		{name: "gob_stable", value: &toolsNodeCheckpointGobExtra{
			Value: "stable",
		}, calls: &toolsNodeCheckpointGobCalls},
		{name: "gob_panic", value: &toolsNodeCheckpointGobExtra{
			Behavior: "panic",
		}, calls: &toolsNodeCheckpointGobCalls},
		{name: "gob_lazy", value: &toolsNodeCheckpointGobExtra{
			Value: "lazy", Behavior: "lazy",
		}, calls: &toolsNodeCheckpointGobCalls},
		{name: "binary_stable", value: &toolsNodeCheckpointBinaryExtra{
			Value: "stable",
		}, calls: &toolsNodeCheckpointBinaryCalls},
		{name: "binary_panic", value: &toolsNodeCheckpointBinaryExtra{
			Behavior: "panic",
		}, calls: &toolsNodeCheckpointBinaryCalls},
		{name: "binary_lazy", value: &toolsNodeCheckpointBinaryExtra{
			Value: "lazy", Behavior: "lazy",
		}, calls: &toolsNodeCheckpointBinaryCalls},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreUint32(tt.calls, 0)
			toolCalls := []schema.ToolCall{{
				ID:    "call",
				Extra: map[string]any{"external": tt.value},
			}}
			cp := &checkpoint{
				State: &toolsNodeCheckpointState{Messages: []*schema.Message{
					schema.AssistantMessage("", toolCalls),
				}},
				InterruptID2State: map[string]core.InterruptState{
					"tool": {State: &toolsInterruptAndRerunStateV1{
						Version:    toolsInterruptAndRerunStateVersionV1,
						Role:       schema.Assistant,
						ToolCalls:  toolCalls,
						RerunTools: []string{"call"},
					}},
				},
			}

			require.NotPanics(t, func() {
				compactCheckpointToolsNodeState(cp)
			})
			got := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
			require.Equal(t, toolCalls, got.ToolCalls)
			require.Nil(t, got.ToolCallsSource)
			require.Zero(t, atomic.LoadUint32(tt.calls))
		})
	}
}

func TestToolsNodeInlineLazyGobMarshalerSaveLoad(t *testing.T) {
	atomic.StoreUint32(&toolsNodeCheckpointGobCalls, 0)
	extra := &toolsNodeCheckpointGobExtra{Value: "persisted", Behavior: "lazy"}
	toolCalls := []schema.ToolCall{{
		ID:    "call",
		Extra: map[string]any{"external": extra},
	}}
	cp := &checkpoint{
		State: &toolsNodeCheckpointState{Messages: []*schema.Message{
			schema.AssistantMessage("", toolCalls),
		}},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {State: &toolsInterruptAndRerunStateV1{
				Version:    toolsInterruptAndRerunStateVersionV1,
				Role:       schema.Assistant,
				ToolCalls:  toolCalls,
				RerunTools: []string{"call"},
			}},
		},
	}

	compactCheckpointToolsNodeState(cp)
	require.Zero(t, atomic.LoadUint32(&toolsNodeCheckpointGobCalls))
	require.Nil(t, extra.cached)
	inline := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.NotEmpty(t, inline.ToolCalls)
	require.Nil(t, inline.ToolCallsSource)

	store := newInMemoryStore()
	pointer := newCheckPointer(nil, nil, store, &serialization.InternalSerializer{})
	require.NoError(t, pointer.set(context.Background(), "inline-lazy-gob", cp))
	require.Zero(t, atomic.LoadUint32(&toolsNodeCheckpointGobCalls))
	restored, exists, err := pointer.get(context.Background(), "inline-lazy-gob")
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, hydrateCheckpointToolsNodeState(restored))
	restoredState := restored.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, restoredState.ToolCallsSource)
	require.Len(t, restoredState.ToolCalls, 1)
	restoredExtra, ok := restoredState.ToolCalls[0].Extra["external"].(*toolsNodeCheckpointGobExtra)
	require.True(t, ok)
	require.Equal(t, "persisted", restoredExtra.Value)
	require.Nil(t, restoredExtra.cached)
	require.Zero(t, atomic.LoadUint32(&toolsNodeCheckpointGobCalls))
}

func TestAttack_HydratedToolsNodeToolCallsDoNotAliasGraphState(t *testing.T) {
	index := 1
	message := schema.AssistantMessage("", []schema.ToolCall{{
		Index: &index,
		ID:    "call",
		Extra: map[string]any{
			"top": "source",
			"nested_map": map[string]any{
				"value": "source",
			},
			"nested_slice": []any{"source"},
		},
	}})
	cp := &checkpoint{
		State: &toolsNodeCheckpointState{
			Messages: []*schema.Message{message},
		},
		InterruptID2State: map[string]core.InterruptState{
			"tool": {
				State: &toolsInterruptAndRerunStateV1{
					Version:   toolsInterruptAndRerunStateVersionV1,
					Role:      schema.Assistant,
					ToolCalls: append([]schema.ToolCall(nil), message.ToolCalls...),
				},
			},
		},
	}

	compactCheckpointToolsNodeState(cp)
	compacted := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, compacted.ToolCalls)
	require.NotNil(t, compacted.ToolCallsSource)

	serializer := &serialization.InternalSerializer{}
	data, err := serializer.Marshal(cp)
	require.NoError(t, err)
	var decoded checkpoint
	require.NoError(t, serializer.Unmarshal(data, &decoded))
	decodedCompacted := decoded.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, decodedCompacted.ToolCalls)
	require.NotNil(t, decodedCompacted.ToolCallsSource)
	require.NoError(t, hydrateCheckpointToolsNodeState(&decoded))

	source := decoded.State.(*toolsNodeCheckpointState).Messages[0].ToolCalls[0]
	hydrated := decoded.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
	require.Nil(t, hydrated.ToolCallsSource)
	require.Len(t, hydrated.ToolCalls, 1)

	*hydrated.ToolCalls[0].Index = 2
	hydrated.ToolCalls[0].Extra["top"] = "hydrated"
	hydrated.ToolCalls[0].Extra["nested_map"].(map[string]any)["value"] = "hydrated"
	hydrated.ToolCalls[0].Extra["nested_slice"].([]any)[0] = "hydrated"

	require.Equal(t, 1, *source.Index)
	require.Equal(t, "source", source.Extra["top"])
	require.Equal(t, "source", source.Extra["nested_map"].(map[string]any)["value"])
	require.Equal(t, "source", source.Extra["nested_slice"].([]any)[0])
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
			enhancedResult := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
				Type: schema.ToolPartTypeText,
				Text: strings.Repeat("result", 128),
			}}}
			wantEnhancedParts, err := enhancedResult.ToMessageInputParts()
			require.NoError(t, err)
			enhanced := &enhancedInvokableTool{
				info: &schema.ToolInfo{Name: enhancedName},
				fn: func(context.Context, *schema.ToolArgument) (*schema.ToolResult, error) {
					enhancedCalls++
					return enhancedResult, nil
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
			var setupErr error
			if streaming {
				_, setupErr = runnable.Stream(context.Background(), input,
					WithCheckPointID("enhanced-v1"))
			} else {
				_, setupErr = runnable.Invoke(context.Background(), input,
					WithCheckPointID("enhanced-v1"))
			}
			var interruptErr *interruptError
			require.ErrorAs(t, setupErr, &interruptErr)
			require.Equal(t, 1, enhancedCalls)

			var output []*schema.Message
			if streaming {
				stream, err := runnable.Stream(context.Background(), &schema.Message{},
					WithCheckPointID("enhanced-v1"))
				require.NoError(t, err)
				defer stream.Close()
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
			} else {
				output, err = runnable.Invoke(context.Background(), &schema.Message{},
					WithCheckPointID("enhanced-v1"))
				require.NoError(t, err)
			}
			require.Len(t, output, 2)
			require.Equal(t, 1, enhancedCalls, "successful enhanced sibling must be reused")

			byCallID := make(map[string]*schema.Message, len(output))
			for _, message := range output {
				require.NotNil(t, message)
				byCallID[message.ToolCallID] = message
			}
			enhancedOutput, ok := byCallID["enhanced-call"]
			require.True(t, ok)
			require.Equal(t, wantEnhancedParts, enhancedOutput.UserInputMultiContent)
			interruptOutput, ok := byCallID["interrupt-call"]
			require.True(t, ok)
			require.Equal(t, `"completed"`, interruptOutput.Content)
		})
	}
}

func TestAttack_ToolsNodeCheckpointRejectedBeforeResumeSideEffects(t *testing.T) {
	tests := []struct {
		name  string
		state func(string) any
		want  string
	}{
		{
			name: "unsupported_version",
			state: func(digest string) any {
				return &toolsInterruptAndRerunStateV1{
					Version:    toolsInterruptAndRerunStateVersionV1 + 1,
					Role:       schema.Assistant,
					RerunTools: []string{"call"},
					ToolCallsSource: &toolsInterruptToolCallsSourceV1{
						MessageIndex: 0,
						Digest:       digest,
					},
				}
			},
			want: `tools node interrupt state "interrupt" has unsupported version 2`,
		},
		{
			name: "malformed_v1",
			state: func(digest string) any {
				return &toolsInterruptAndRerunStateV1{
					Version: toolsInterruptAndRerunStateVersionV1,
					Role:    schema.Assistant,
					ToolCallsSource: &toolsInterruptToolCallsSourceV1{
						MessageIndex: 0,
						Digest:       digest,
					},
				}
			},
			want: `tools node interrupt state tool call ID "call" has neither an executed result nor a rerun marker`,
		},
		{
			name: "empty_v1",
			state: func(string) any {
				return &toolsInterruptAndRerunStateV1{
					Version: toolsInterruptAndRerunStateVersionV1,
					Role:    schema.Assistant,
				}
			},
			want: "tools node interrupt state has no tool calls",
		},
		{
			name: "all_executed_v1",
			state: func(digest string) any {
				return &toolsInterruptAndRerunStateV1{
					Version:       toolsInterruptAndRerunStateVersionV1,
					Role:          schema.Assistant,
					ExecutedTools: map[string]string{"call": "result"},
					ToolCallsSource: &toolsInterruptToolCallsSourceV1{
						MessageIndex: 0,
						Digest:       digest,
					},
				}
			},
			want: "tools node interrupt state has no pending rerun tools",
		},
		{
			name:  "wrong_type",
			state: func(string) any { return "custom state" },
			want:  `tools node interrupt state "interrupt" has invalid type string`,
		},
		{
			name:  "missing",
			state: func(string) any { return nil },
			want:  `tools node interrupt state "interrupt" is missing`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, nested := range []bool{false, true} {
				scope := "root"
				if nested {
					scope = "subgraph"
				}
				for _, streaming := range []bool{false, true} {
					mode := "invoke"
					if streaming {
						mode = "stream"
					}
					t.Run(scope+"/"+mode, func(t *testing.T) {
						testToolsNodeCheckpointRejectedBeforeResumeSideEffects(
							t, nested, streaming, tt.state, tt.want)
					})
				}
			}
		})
	}

	t.Run("before_hydration", func(t *testing.T) {
		cp := &checkpoint{
			InterruptID2State: map[string]core.InterruptState{
				"interrupt": {
					State: &toolsInterruptAndRerunStateV1{
						Version: toolsInterruptAndRerunStateVersionV1 + 1,
						Role:    schema.Assistant,
						ToolCallsSource: &toolsInterruptToolCallsSourceV1{
							MessageIndex: -1,
						},
					},
				},
			},
		}

		err := (&runner{}).prepareCheckpointForResume(cp, nil)
		require.EqualError(t, err,
			`invalid checkpoint: tools node interrupt state "interrupt" has unsupported version 2`)
	})
}

func testToolsNodeCheckpointRejectedBeforeResumeSideEffects(t *testing.T, nested, streaming bool,
	newState func(string) any, want string,
) {
	t.Helper()
	counters := &toolsNodeSideEffectCounters{}

	toolNode, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools: []componenttool.BaseTool{newCheckpointTestTool(
			&schema.ToolInfo{Name: "tool"},
			func(context.Context, *longRunningToolInput) (string, error) {
				atomic.AddInt32(&counters.tool, 1)
				return "unexpected", nil
			},
		)},
	})
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{}`,
		},
	}})
	sibling := InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
		atomic.AddInt32(&counters.sibling, 1)
		return nil, nil
	})
	fresh := InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
		atomic.AddInt32(&counters.fresh, 1)
		return nil, nil
	})

	graph := NewGraph[*schema.Message, []*schema.Message](WithGenLocalState(
		func(context.Context) *toolsNodeCheckpointState {
			return &toolsNodeCheckpointState{}
		}))
	if nested {
		child := NewGraph[*schema.Message, []*schema.Message]()
		require.NoError(t, child.AddToolsNode("tools", toolNode))
		require.NoError(t, child.AddEdge(START, "tools"))
		require.NoError(t, child.AddEdge("tools", END))
		require.NoError(t, graph.AddGraphNode("subgraph", child))
		require.NoError(t, graph.AddEdge(START, "subgraph"))
		require.NoError(t, graph.AddEdge("subgraph", END))
	} else {
		require.NoError(t, graph.AddToolsNode("tools", toolNode))
		require.NoError(t, graph.AddEdge(START, "tools"))
		require.NoError(t, graph.AddEdge("tools", END))
	}
	require.NoError(t, graph.AddLambdaNode("sibling", sibling))
	require.NoError(t, graph.AddEdge(START, "sibling"))
	require.NoError(t, graph.AddEdge("sibling", END))
	require.NoError(t, graph.AddLambdaNode("fresh", fresh))
	require.NoError(t, graph.AddEdge(START, "fresh"))
	require.NoError(t, graph.AddEdge("fresh", END))

	store := newInMemoryStore()
	runnable, err := graph.Compile(context.Background(),
		WithGraphName("root"),
		WithCheckPointStore(store))
	require.NoError(t, err)

	digest, ok := checkpointToolCallsDigest(input.ToolCalls)
	require.True(t, ok)
	resumeState := newState(digest)
	interruptAddress := Address{
		{Type: AddressSegmentRunnable, ID: "root"},
	}
	rootState := map[string]core.InterruptState{
		checkpointLayoutSentinelID: {
			State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
		},
	}
	cp := &checkpoint{
		StateLayoutVersion: checkpointStateLayoutVersionV1,
		Inputs: map[string]any{
			"sibling": input,
			"fresh":   input,
		},
		State: &toolsNodeCheckpointState{},
		InterruptID2Addr: map[string]Address{
			"interrupt": interruptAddress,
		},
		InterruptID2State: rootState,
	}
	if nested {
		interruptAddress = append(interruptAddress,
			AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
			AddressSegment{Type: AddressSegmentNode, ID: "tools"})
		cp.Inputs["subgraph"] = input
		cp.SubGraphs = map[string]*checkpoint{
			"subgraph": {
				StateLayoutVersion: checkpointStateLayoutVersionV1,
				Inputs: map[string]any{
					"tools": input,
				},
				State: &toolsNodeCheckpointState{Messages: []*schema.Message{input}},
				InterruptID2Addr: map[string]Address{
					"interrupt": interruptAddress,
				},
				InterruptID2State: map[string]core.InterruptState{
					"interrupt": {State: resumeState},
					checkpointLayoutSentinelID: {
						State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
					},
				},
			},
		}
	} else {
		interruptAddress = append(interruptAddress,
			AddressSegment{Type: AddressSegmentNode, ID: "tools"})
		cp.Inputs["tools"] = input
		cp.State = &toolsNodeCheckpointState{Messages: []*schema.Message{input}}
		cp.InterruptID2State["interrupt"] = core.InterruptState{State: resumeState}
	}
	cp.InterruptID2Addr["interrupt"] = interruptAddress

	data, err := (&serialization.InternalSerializer{}).Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, store.Set(context.Background(), "invalid-tools-state", data))

	toolsPath := NewNodePath("tools")
	if nested {
		toolsPath = NewNodePath("subgraph", "tools")
	}
	opts := newToolsNodePreflightOptions("invalid-tools-state", counters, toolsPath)
	if streaming {
		_, err = runnable.Stream(context.Background(), nil, opts...)
	} else {
		_, err = runnable.Invoke(context.Background(), nil, opts...)
	}

	requireNoToolsNodeSideEffects(t, counters)
	require.ErrorContains(t, err, want)
}

func TestClassifyCheckpointToolsNodeRoute(t *testing.T) {
	baseAddress := Address{{Type: AddressSegmentRunnable, ID: "root"}}
	address := func(segments ...AddressSegment) Address {
		return append(append(Address(nil), baseAddress...), segments...)
	}
	node := func(componentType component, child *runner) *chanCall {
		return &chanCall{action: &composableRunnable{
			meta:        &executorMeta{component: componentType},
			graphRunner: child,
		}}
	}
	newRunner := func() *runner {
		leaf := &runner{chanSubscribeTo: map[string]*chanCall{
			"tools":         node(ComponentOfToolsNode, nil),
			"agentic_tools": node(ComponentOfAgenticToolsNode, nil),
			"lambda":        node(ComponentOfLambda, nil),
		}}
		current := &runner{chanSubscribeTo: map[string]*chanCall{
			"nested":     node(ComponentOfGraph, leaf),
			"not_graph":  node(ComponentOfLambda, leaf),
			"nil_runner": node(ComponentOfGraph, nil),
			"nil_call":   nil,
			"nil_action": {},
			"nil_meta":   {action: &composableRunnable{}},
		}}
		return &runner{chanSubscribeTo: map[string]*chanCall{
			"subgraph": node(ComponentOfGraph, current),
		}}
	}

	tests := []struct {
		name    string
		address Address
		want    checkpointToolsNodeRouteMatch
	}{
		{
			name: "multi_level_tools_node",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
			want: checkpointRouteToolsNodeExact,
		},
		{
			name: "multi_level_agentic_tools_node",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "agentic_tools"}),
			want: checkpointRouteToolsNodeExact,
		},
		{
			name: "terminal_non_tools_node",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "lambda"}),
		},
		{
			name: "route_ends_at_inherited_path",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"}),
		},
		{
			name: "base_address_id_mismatch",
			address: Address{
				{Type: AddressSegmentRunnable, ID: "other"},
				{Type: AddressSegmentNode, ID: "subgraph"},
				{Type: AddressSegmentNode, ID: "nested"},
				{Type: AddressSegmentNode, ID: "tools"},
			},
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "base_address_type_mismatch",
			address: Address{
				{Type: AddressSegmentNode, ID: "root"},
				{Type: AddressSegmentNode, ID: "subgraph"},
				{Type: AddressSegmentNode, ID: "nested"},
				{Type: AddressSegmentNode, ID: "tools"},
			},
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "base_address_sub_id_mismatch",
			address: Address{
				{Type: AddressSegmentRunnable, ID: "root", SubID: "unexpected"},
				{Type: AddressSegmentNode, ID: "subgraph"},
				{Type: AddressSegmentNode, ID: "nested"},
				{Type: AddressSegmentNode, ID: "tools"},
			},
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "inherited_path_type_mismatch",
			address: address(
				AddressSegment{Type: AddressSegmentTool, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "inherited_path_id_mismatch",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "other"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "inherited_path_sub_id_mismatch",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph", SubID: "unexpected"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "intermediate_segment_type_mismatch",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentTool, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "intermediate_segment_sub_id_mismatch",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested", SubID: "unexpected"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "terminal_segment_sub_id_mismatch",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools", SubID: "unexpected"}),
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "tools_node_illegal_descendant",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"},
				AddressSegment{Type: AddressSegmentNode, ID: "descendant"}),
			want: checkpointRouteToolsNodeAddressMismatch,
		},
		{
			name: "tools_node_tool_call_descendant",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"},
				AddressSegment{Type: AddressSegmentTool, ID: "tool", SubID: "call"}),
		},
		{
			name: "tools_node_composite_tool_descendant",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nested"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"},
				AddressSegment{Type: AddressSegmentTool, ID: "tool", SubID: "call"},
				AddressSegment{Type: AddressSegmentRunnable, ID: "inner"},
				AddressSegment{Type: AddressSegmentNode, ID: "inner_node"}),
		},
		{
			name: "missing_intermediate_call",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "missing"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "nil_intermediate_call",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nil_call"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "nil_intermediate_action",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nil_action"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "nil_intermediate_metadata",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nil_meta"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "intermediate_non_subgraph",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "not_graph"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
		{
			name: "intermediate_subgraph_without_runner",
			address: address(
				AddressSegment{Type: AddressSegmentNode, ID: "subgraph"},
				AddressSegment{Type: AddressSegmentNode, ID: "nil_runner"},
				AddressSegment{Type: AddressSegmentNode, ID: "tools"}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want,
				newRunner().classifyCheckpointToolsNodeRoute(tt.address, baseAddress))
		})
	}
}

func TestCheckpointPreflightAllowsCustomComponentInterruptState(t *testing.T) {
	address := Address{
		{Type: AddressSegmentRunnable, ID: "root"},
		{Type: AddressSegmentNode, ID: "custom"},
	}
	cp := &checkpoint{
		StateLayoutVersion: checkpointStateLayoutVersionV1,
		InterruptID2Addr: map[string]Address{
			"interrupt-a": address,
			"interrupt-b": address,
		},
		InterruptID2State: map[string]core.InterruptState{
			"interrupt-a": {State: "custom state a"},
			"interrupt-b": {State: "custom state b"},
			checkpointLayoutSentinelID: {
				State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
			},
		},
	}
	r := &runner{chanSubscribeTo: map[string]*chanCall{
		"custom": {
			action: &composableRunnable{
				meta: &executorMeta{component: ComponentOfLambda},
			},
		},
	}}

	require.NoError(t, r.prepareCheckpointForResume(cp, Address{
		{Type: AddressSegmentRunnable, ID: "root"},
	}))
	require.Equal(t, "custom state a", cp.InterruptID2State["interrupt-a"].State)
	require.Equal(t, "custom state b", cp.InterruptID2State["interrupt-b"].State)
}

func TestCheckpointPreflightAllowsV0CustomComponentStateWithoutRoute(t *testing.T) {
	cp := &checkpoint{
		InterruptID2State: map[string]core.InterruptState{
			"interrupt": {State: "custom state"},
		},
	}

	require.NoError(t, (&runner{}).prepareCheckpointForResume(cp, nil))
	require.Equal(t, "custom state", cp.InterruptID2State["interrupt"].State)
}

func TestCheckpointPreflightAllowsInputOnlyNestedCustomComponentState(t *testing.T) {
	child := NewGraph[string, string]()
	require.NoError(t, child.AddLambdaNode("custom",
		InvokableLambda(func(ctx context.Context, input string) (string, error) {
			wasInterrupted, hasState, state := GetInterruptState[string](ctx)
			require.True(t, wasInterrupted)
			require.True(t, hasState)
			require.Equal(t, "custom state", state)
			return input + " resumed", nil
		})))
	require.NoError(t, child.AddEdge(START, "custom"))
	require.NoError(t, child.AddEdge("custom", END))
	root := NewGraph[string, string]()
	require.NoError(t, root.AddGraphNode("subgraph", child))
	require.NoError(t, root.AddEdge(START, "subgraph"))
	require.NoError(t, root.AddEdge("subgraph", END))

	store := newInMemoryStore()
	runnable, err := root.Compile(context.Background(),
		WithGraphName("root"),
		WithCheckPointStore(store))
	require.NoError(t, err)
	cp := &checkpoint{
		Inputs: map[string]any{"subgraph": "input"},
		InterruptID2Addr: map[string]Address{
			"interrupt": {
				{Type: AddressSegmentRunnable, ID: "root"},
				{Type: AddressSegmentNode, ID: "subgraph"},
				{Type: AddressSegmentNode, ID: "custom"},
			},
		},
		InterruptID2State: map[string]core.InterruptState{
			"interrupt": {State: "custom state"},
		},
	}
	data, err := (&serialization.InternalSerializer{}).Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, store.Set(context.Background(), "custom-state", data))

	output, err := runnable.Invoke(context.Background(), "", WithCheckPointID("custom-state"))
	require.NoError(t, err)
	require.Equal(t, "input resumed", output)
}

func TestCheckpointPreflightAllowsChildOwnedToolsNodeState(t *testing.T) {
	input := schema.AssistantMessage("", []schema.ToolCall{{ID: "call"}})
	address := Address{
		{Type: AddressSegmentRunnable, ID: "root"},
		{Type: AddressSegmentNode, ID: "child"},
		{Type: AddressSegmentNode, ID: "tools"},
	}
	toolsCall := &chanCall{action: &composableRunnable{
		meta: &executorMeta{component: ComponentOfToolsNode},
	}}
	childRunner := &runner{chanSubscribeTo: map[string]*chanCall{"tools": toolsCall}}
	rootRunner := &runner{chanSubscribeTo: map[string]*chanCall{
		"child": {action: &composableRunnable{
			meta:        &executorMeta{component: ComponentOfGraph},
			graphRunner: childRunner,
		}},
	}}

	for _, version := range []int{0, checkpointStateLayoutVersionV1} {
		name := "v0"
		if version == checkpointStateLayoutVersionV1 {
			name = "v1"
		}
		t.Run(name, func(t *testing.T) {
			cp := &checkpoint{
				StateLayoutVersion: version,
				InterruptID2Addr:   map[string]Address{"interrupt": address},
				InterruptID2State:  map[string]core.InterruptState{},
				SubGraphs: map[string]*checkpoint{
					"child": {
						StateLayoutVersion: version,
						InterruptID2Addr:   map[string]Address{"interrupt": address},
						InterruptID2State: map[string]core.InterruptState{
							"interrupt": {
								State: toolsNodeCheckpointStateForVersion(version, input),
							},
						},
					},
				},
			}
			if version == checkpointStateLayoutVersionV1 {
				cp.InterruptID2State[checkpointLayoutSentinelID] = core.InterruptState{
					State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
				}
				cp.SubGraphs["child"].InterruptID2State[checkpointLayoutSentinelID] = core.InterruptState{
					State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
				}
			}

			require.NoError(t, rootRunner.prepareCheckpointForResume(cp, address[:1]))
		})
	}
}

func TestAttack_ToolsNodeV0StateWithoutRouteRejectedBeforeSideEffects(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "invoke"
		if streaming {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			counters, err := runToolsNodeV0StateWithoutRoute(t, streaming)

			requireNoToolsNodeSideEffects(t, counters)
			require.EqualError(t, err,
				`[GraphRunError] invalid checkpoint tools state: `+
					`tools node interrupt state "interrupt" at owner path [] has no routing address`)
		})
	}
}

func runToolsNodeV0StateWithoutRoute(t *testing.T, streaming bool) (*toolsNodeSideEffectCounters, error) {
	t.Helper()
	counters := &toolsNodeSideEffectCounters{}
	toolsNode, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools: []componenttool.BaseTool{newCheckpointTestTool(
			&schema.ToolInfo{Name: "tool"},
			func(context.Context, *longRunningToolInput) (string, error) {
				atomic.AddInt32(&counters.tool, 1)
				return "unexpected", nil
			},
		)},
	})
	require.NoError(t, err)

	graph := NewGraph[*schema.Message, []*schema.Message](WithGenLocalState(
		func(context.Context) *toolsNodeCheckpointState {
			return &toolsNodeCheckpointState{}
		}))
	require.NoError(t, graph.AddToolsNode("tools", toolsNode))
	require.NoError(t, graph.AddLambdaNode("sibling",
		InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
			atomic.AddInt32(&counters.sibling, 1)
			return nil, nil
		})))
	require.NoError(t, graph.AddLambdaNode("fresh",
		InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
			atomic.AddInt32(&counters.fresh, 1)
			return nil, nil
		})))
	require.NoError(t, graph.AddEdge(START, "tools"))
	require.NoError(t, graph.AddEdge("tools", END))
	require.NoError(t, graph.AddEdge(START, "sibling"))
	require.NoError(t, graph.AddEdge("sibling", END))
	require.NoError(t, graph.AddEdge(START, "fresh"))
	require.NoError(t, graph.AddEdge("fresh", END))

	store := newInMemoryStore()
	runnable, err := graph.Compile(context.Background(),
		WithGraphName("root"),
		WithCheckPointStore(store))
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{}`,
		},
	}})
	cp := &checkpoint{
		Inputs: map[string]any{
			"tools":   input,
			"sibling": input,
			"fresh":   input,
		},
		State: &toolsNodeCheckpointState{},
		InterruptID2State: map[string]core.InterruptState{
			"interrupt": {
				State: &toolsInterruptAndRerunState{Input: input},
			},
		},
	}
	data, err := (&serialization.InternalSerializer{}).Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, store.Set(context.Background(), "v0-tools-state-without-route", data))

	opts := newToolsNodePreflightOptions(
		"v0-tools-state-without-route", counters, NewNodePath("tools"))
	if !streaming {
		_, err = runnable.Invoke(context.Background(), nil, opts...)
		return counters, err
	}
	stream, err := runnable.Stream(context.Background(), nil, opts...)
	if err != nil {
		return counters, err
	}
	defer stream.Close()
	for {
		_, receiveErr := stream.Recv()
		if receiveErr == io.EOF {
			return counters, nil
		}
		if receiveErr != nil {
			return counters, receiveErr
		}
	}
}

func TestAttack_ToolsNodeChildOwnedWrongChildRouteRejectedBeforeSideEffects(t *testing.T) {
	for _, compileWrongChild := range []bool{false, true} {
		childCase := "nonexistent_child"
		if compileWrongChild {
			childCase = "different_compiled_child"
		}
		for _, streaming := range []bool{false, true} {
			mode := "invoke"
			if streaming {
				mode = "stream"
			}
			t.Run(childCase+"/"+mode, func(t *testing.T) {
				counters, err := runChildOwnedWrongChildToolsNodeCheckpoint(
					t, compileWrongChild, streaming)

				require.EqualError(t, err,
					`[GraphRunError] invalid checkpoint tools state: subgraph checkpoint "child" `+
						`has invalid tools node state: tools node interrupt state "interrupt" at owner path [child] `+
						`has route "runnable:root;node:wrong-child;node:tools" `+
						`that does not target a compiled ToolsNode`)
				requireNoToolsNodeSideEffects(t, counters)
			})
		}
	}
}

func TestAttack_ToolsNodeRootRouteClassificationPrecedesSparseOwnerAssociation(t *testing.T) {
	for _, version := range []int{0, checkpointStateLayoutVersionV1} {
		versionName := "v0"
		if version == checkpointStateLayoutVersionV1 {
			versionName = "v1"
		}
		for _, streaming := range []bool{false, true} {
			mode := "invoke"
			if streaming {
				mode = "stream"
			}
			t.Run(versionName+"/"+mode, func(t *testing.T) {
				counters, err := runChildOwnedToolsNodeCheckpoint(
					t, version, true, streaming, func(*schema.Message) any {
						return "wrong state"
					})

				require.EqualError(t, err,
					`[GraphRunError] invalid checkpoint tools state: subgraph checkpoint "child" `+
						`has invalid tools node state: tools node interrupt state "interrupt" `+
						`has invalid type string`)
				requireNoToolsNodeSideEffects(t, counters)
			})
		}
	}
}

type toolsNodeSideEffectCounters struct {
	modifier int32
	callback int32
	tool     int32
	sibling  int32
	override int32
	fresh    int32
}

func requireNoToolsNodeSideEffects(t *testing.T, counters *toolsNodeSideEffectCounters) {
	t.Helper()
	require.Equal(t, toolsNodeSideEffectCounters{}, toolsNodeSideEffectCounters{
		modifier: atomic.LoadInt32(&counters.modifier),
		callback: atomic.LoadInt32(&counters.callback),
		tool:     atomic.LoadInt32(&counters.tool),
		sibling:  atomic.LoadInt32(&counters.sibling),
		override: atomic.LoadInt32(&counters.override),
		fresh:    atomic.LoadInt32(&counters.fresh),
	}, "modifier, callback, tool, sibling, override, and fresh-execution counters")
}

func newToolsNodePreflightOptions(checkpointID string, counters *toolsNodeSideEffectCounters,
	toolsPath *NodePath,
) []Option {
	cb := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo,
			_ callbacks.CallbackInput) context.Context {
			atomic.AddInt32(&counters.callback, 1)
			return ctx
		}).
		OnStartWithStreamInputFn(func(ctx context.Context, _ *callbacks.RunInfo,
			input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
			atomic.AddInt32(&counters.callback, 1)
			input.Close()
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, _ *callbacks.RunInfo, _ error) context.Context {
			atomic.AddInt32(&counters.callback, 1)
			return ctx
		}).
		Build()
	opts := []Option{
		WithCheckPointID(checkpointID),
		WithStateModifier(func(context.Context, NodePath, any) error {
			atomic.AddInt32(&counters.modifier, 1)
			return nil
		}),
		WithCallbacks(cb),
	}
	if toolsPath == nil {
		return opts
	}
	return append(opts, WithToolsNodeOption(WithToolList(&toolsNodeCheckpointCountingTool{
		info:      &schema.ToolInfo{Name: "tool"},
		infoCalls: &counters.override,
		runCalls:  &counters.tool,
	})).DesignateNodeWithPath(toolsPath))
}

func runChildOwnedWrongChildToolsNodeCheckpoint(t *testing.T, compileWrongChild, streaming bool) (
	*toolsNodeSideEffectCounters, error,
) {
	return runChildOwnedToolsNodeCheckpoint(
		t, checkpointStateLayoutVersionV1, compileWrongChild, streaming,
		func(input *schema.Message) any {
			return &toolsInterruptAndRerunStateV1{
				Version:    toolsInterruptAndRerunStateVersionV1,
				Role:       schema.Assistant,
				ToolCalls:  append([]schema.ToolCall(nil), input.ToolCalls...),
				RerunTools: []string{"call"},
			}
		})
}

func runChildOwnedToolsNodeCheckpoint(t *testing.T, version int, compileWrongChild, streaming bool,
	newState func(*schema.Message) any,
) (*toolsNodeSideEffectCounters, error) {
	t.Helper()
	counters := &toolsNodeSideEffectCounters{}
	newToolsNode := func() *ToolsNode {
		node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
			Tools: []componenttool.BaseTool{newCheckpointTestTool(
				&schema.ToolInfo{Name: "tool"},
				func(context.Context, *longRunningToolInput) (string, error) {
					atomic.AddInt32(&counters.tool, 1)
					return "unexpected", nil
				},
			)},
		})
		require.NoError(t, err)
		return node
	}

	child := NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, child.AddToolsNode("tools", newToolsNode()))
	require.NoError(t, child.AddLambdaNode("fresh",
		InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
			atomic.AddInt32(&counters.fresh, 1)
			return nil, nil
		})))
	require.NoError(t, child.AddEdge(START, "tools"))
	require.NoError(t, child.AddEdge("tools", END))
	require.NoError(t, child.AddEdge(START, "fresh"))
	require.NoError(t, child.AddEdge("fresh", END))

	root := NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, root.AddGraphNode("child", child))
	require.NoError(t, root.AddLambdaNode("sibling",
		InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
			atomic.AddInt32(&counters.sibling, 1)
			return nil, nil
		})))
	require.NoError(t, root.AddEdge(START, "child"))
	require.NoError(t, root.AddEdge("child", END))
	require.NoError(t, root.AddEdge(START, "sibling"))
	require.NoError(t, root.AddEdge("sibling", END))
	if compileWrongChild {
		wrongChild := NewGraph[*schema.Message, []*schema.Message]()
		require.NoError(t, wrongChild.AddToolsNode("tools", newToolsNode()))
		require.NoError(t, wrongChild.AddEdge(START, "tools"))
		require.NoError(t, wrongChild.AddEdge("tools", END))
		require.NoError(t, root.AddGraphNode("wrong-child", wrongChild))
		require.NoError(t, root.AddEdge(START, "wrong-child"))
		require.NoError(t, root.AddEdge("wrong-child", END))
	}

	store := newInMemoryStore()
	runnable, err := root.Compile(context.Background(),
		WithGraphName("root"),
		WithCheckPointStore(store))
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{}`,
		},
	}})
	address := Address{
		{Type: AddressSegmentRunnable, ID: "root"},
		{Type: AddressSegmentNode, ID: "wrong-child"},
		{Type: AddressSegmentNode, ID: "tools"},
	}
	cp := &checkpoint{
		StateLayoutVersion: version,
		Inputs: map[string]any{
			"child":   input,
			"sibling": input,
		},
		SubGraphs: map[string]*checkpoint{
			"child": {
				StateLayoutVersion: version,
				Inputs: map[string]any{
					"tools": input,
					"fresh": input,
				},
				InterruptID2Addr: map[string]Address{
					"interrupt": address,
				},
				InterruptID2State: map[string]core.InterruptState{
					"interrupt": {
						State: newState(input),
					},
				},
			},
		},
		InterruptID2Addr: map[string]Address{
			"interrupt": address,
		},
		InterruptID2State: map[string]core.InterruptState{},
	}
	if version == checkpointStateLayoutVersionV1 {
		cp.InterruptID2State[checkpointLayoutSentinelID] = core.InterruptState{
			State: &checkpointLayoutSentinelV1{
				Version: checkpointStateLayoutVersionV1,
			},
		}
		cp.SubGraphs["child"].InterruptID2State[checkpointLayoutSentinelID] = core.InterruptState{
			State: &checkpointLayoutSentinelV1{
				Version: checkpointStateLayoutVersionV1,
			},
		}
	}
	data, err := (&serialization.InternalSerializer{}).Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, store.Set(context.Background(), "wrong-child-route", data))

	opts := newToolsNodePreflightOptions(
		"wrong-child-route", counters, NewNodePath("child", "tools"))
	if streaming {
		_, err = runnable.Stream(context.Background(), nil, opts...)
	} else {
		_, err = runnable.Invoke(context.Background(), nil, opts...)
	}
	return counters, err
}

func TestToolsNodeLegacyAncestorOwnedNestedResume(t *testing.T) {
	tests := []struct {
		name          string
		state         func(*schema.Message) any
		wantToolCalls int32
		wantContent   string
	}{
		{
			name: "pending",
			state: func(input *schema.Message) any {
				return &toolsInterruptAndRerunState{Input: input}
			},
			wantToolCalls: 1,
			wantContent:   `"completed"`,
		},
		{
			name: "all_executed",
			state: func(input *schema.Message) any {
				return &toolsInterruptAndRerunState{
					Input:         input,
					ExecutedTools: map[string]string{"call": "persisted"},
				}
			},
			wantContent: "persisted",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "invoke"
				if streaming {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					output, counters, err := runMultiLevelToolsNodeCheckpoint(
						t, streaming, false, true, 0, tt.state, nil)
					require.NoError(t, err)
					require.Equal(t, tt.wantToolCalls, atomic.LoadInt32(&counters.tool))
					require.Len(t, output, 1)
					require.Equal(t, tt.wantContent, output[0].Content)
				})
			}
		})
	}
}

func TestToolsNodeInputOnlyNestedResumePreflight(t *testing.T) {
	tests := []struct {
		name  string
		state func(*schema.Message) any
		want  string
	}{
		{
			name:  "missing",
			state: func(*schema.Message) any { return nil },
			want:  `tools node interrupt state "interrupt" is missing`,
		},
		{
			name:  "invalid",
			state: func(*schema.Message) any { return "invalid" },
			want:  `tools node interrupt state "interrupt" has invalid type string`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "invoke"
				if streaming {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					_, counters, err := runMultiLevelToolsNodeCheckpoint(
						t, streaming, true, false, 0, tt.state, nil)
					require.ErrorContains(t, err, tt.want)
					requireNoToolsNodeSideEffects(t, counters)
				})
			}
		})
	}

	t.Run("valid", func(t *testing.T) {
		for _, streaming := range []bool{false, true} {
			mode := "invoke"
			if streaming {
				mode = "stream"
			}
			t.Run(mode, func(t *testing.T) {
				output, counters, err := runMultiLevelToolsNodeCheckpoint(
					t, streaming, false, false, 0, func(input *schema.Message) any {
						return &toolsInterruptAndRerunState{Input: input}
					}, nil)
				require.NoError(t, err)
				require.Equal(t, int32(1), atomic.LoadInt32(&counters.tool))
				require.Len(t, output, 1)
				require.Equal(t, `"completed"`, output[0].Content)
			})
		}
	})
}

func TestAttack_ToolsNodeMalformedLegacyAncestorStateRejectedBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name  string
		state func(*schema.Message) any
		want  string
	}{
		{
			name:  "missing",
			state: func(*schema.Message) any { return nil },
			want:  `tools node interrupt state "interrupt" is missing`,
		},
		{
			name:  "wrong_type",
			state: func(*schema.Message) any { return "invalid" },
			want:  `tools node interrupt state "interrupt" has invalid type string`,
		},
		{
			name: "nil_input",
			state: func(*schema.Message) any {
				return &toolsInterruptAndRerunState{}
			},
			want: "tools node legacy interrupt state has nil input",
		},
		{
			name: "invalid_role",
			state: func(input *schema.Message) any {
				copied := *input
				copied.Role = schema.User
				return &toolsInterruptAndRerunState{Input: &copied}
			},
			want: `tools node legacy interrupt state has invalid role "user"`,
		},
		{
			name: "empty_tool_calls",
			state: func(input *schema.Message) any {
				copied := *input
				copied.ToolCalls = nil
				return &toolsInterruptAndRerunState{Input: &copied}
			},
			want: "tools node legacy interrupt state has no tool calls",
		},
		{
			name: "duplicate_tool_call_id",
			state: func(input *schema.Message) any {
				copied := *input
				copied.ToolCalls = append(copied.ToolCalls, copied.ToolCalls[0])
				return &toolsInterruptAndRerunState{Input: &copied}
			},
			want: `tools node legacy interrupt state has duplicate tool call ID "call"`,
		},
		{
			name: "duplicate_executed_result",
			state: func(input *schema.Message) any {
				return &toolsInterruptAndRerunState{
					Input:                 input,
					ExecutedTools:         map[string]string{"call": "result"},
					ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": {}},
				}
			},
			want: `tools node legacy interrupt state has duplicate executed tool call ID "call"`,
		},
		{
			name: "unknown_result",
			state: func(input *schema.Message) any {
				return &toolsInterruptAndRerunState{
					Input:         input,
					ExecutedTools: map[string]string{"unknown": "result"},
				}
			},
			want: `tools node legacy interrupt state has result for unknown tool call ID "unknown"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, streaming := range []bool{false, true} {
				mode := "invoke"
				if streaming {
					mode = "stream"
				}
				t.Run(mode, func(t *testing.T) {
					_, counters, err := runMultiLevelToolsNodeCheckpoint(
						t, streaming, true, true, 0, tt.state, nil)
					require.ErrorContains(t, err, tt.want)
					requireNoToolsNodeSideEffects(t, counters)
				})
			}
		})
	}
}

func TestAttack_ToolsNodeV1AncestorOwnedMultiLevelResumeRejectedBeforeSideEffects(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		mode := "invoke"
		if streaming {
			mode = "stream"
		}
		t.Run(mode, func(t *testing.T) {
			_, counters, err := runMultiLevelToolsNodeCheckpoint(
				t, streaming, true, true, checkpointStateLayoutVersionV1,
				func(input *schema.Message) any {
					return &toolsInterruptAndRerunStateV1{
						Version:    toolsInterruptAndRerunStateVersionV1,
						Role:       schema.Assistant,
						ToolCalls:  append([]schema.ToolCall(nil), input.ToolCalls...),
						RerunTools: []string{"call"},
					}
				}, nil)
			require.ErrorContains(t, err,
				`interrupt ID "interrupt" state owner path [] does not match routing owner path [subgraph nested]`)
			requireNoToolsNodeSideEffects(t, counters)
		})
	}
}

func runMultiLevelToolsNodeCheckpoint(t *testing.T, streaming, withSibling bool,
	withChildCheckpoint bool, stateLayoutVersion int,
	newState func(*schema.Message) any,
	mutateCheckpoint func(*checkpoint, Address, *schema.Message),
) ([]*schema.Message, *toolsNodeSideEffectCounters, error) {
	t.Helper()
	counters := &toolsNodeSideEffectCounters{}
	toolNode, err := NewToolNode(context.Background(), &ToolsNodeConfig{
		Tools: []componenttool.BaseTool{newCheckpointTestTool(
			&schema.ToolInfo{Name: "tool"},
			func(context.Context, *longRunningToolInput) (string, error) {
				atomic.AddInt32(&counters.tool, 1)
				return "completed", nil
			},
		)},
	})
	require.NoError(t, err)

	nested := NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, nested.AddToolsNode("tools", toolNode))
	require.NoError(t, nested.AddEdge(START, "tools"))
	require.NoError(t, nested.AddEdge("tools", END))
	child := NewGraph[*schema.Message, []*schema.Message]()
	require.NoError(t, child.AddGraphNode("nested", nested))
	require.NoError(t, child.AddEdge(START, "nested"))
	require.NoError(t, child.AddEdge("nested", END))
	root := NewGraph[*schema.Message, []*schema.Message](WithGenLocalState(
		func(context.Context) *toolsNodeCheckpointState {
			return &toolsNodeCheckpointState{}
		}))
	require.NoError(t, root.AddGraphNode("subgraph", child))
	require.NoError(t, root.AddEdge(START, "subgraph"))
	require.NoError(t, root.AddEdge("subgraph", END))
	if withSibling {
		sibling := InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
			atomic.AddInt32(&counters.sibling, 1)
			return nil, nil
		})
		require.NoError(t, root.AddLambdaNode("sibling", sibling))
		require.NoError(t, root.AddEdge(START, "sibling"))
		require.NoError(t, root.AddEdge("sibling", END))
		fresh := InvokableLambda(func(context.Context, *schema.Message) ([]*schema.Message, error) {
			atomic.AddInt32(&counters.fresh, 1)
			return nil, nil
		})
		require.NoError(t, root.AddLambdaNode("fresh", fresh))
		require.NoError(t, root.AddEdge(START, "fresh"))
		require.NoError(t, root.AddEdge("fresh", END))
	}

	store := newInMemoryStore()
	runnable, err := root.Compile(context.Background(),
		WithGraphName("root"),
		WithCheckPointStore(store))
	require.NoError(t, err)

	input := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{}`,
		},
	}})
	address := Address{
		{Type: AddressSegmentRunnable, ID: "root"},
		{Type: AddressSegmentNode, ID: "subgraph"},
		{Type: AddressSegmentNode, ID: "nested"},
		{Type: AddressSegmentNode, ID: "tools"},
	}
	inputs := map[string]any{"subgraph": input}
	if withSibling {
		inputs["sibling"] = input
		inputs["fresh"] = input
	}
	cp := &checkpoint{
		StateLayoutVersion: stateLayoutVersion,
		Inputs:             inputs,
		State:              &toolsNodeCheckpointState{},
		InterruptID2Addr: map[string]Address{
			"interrupt": address,
		},
		InterruptID2State: map[string]core.InterruptState{
			"interrupt": {State: newState(input)},
		},
	}
	if withChildCheckpoint {
		cp.SubGraphs = map[string]*checkpoint{
			"subgraph": {
				StateLayoutVersion: stateLayoutVersion,
				Inputs:             map[string]any{"nested": input},
				SubGraphs: map[string]*checkpoint{
					"nested": {
						StateLayoutVersion: stateLayoutVersion,
						Inputs:             map[string]any{"tools": input},
						State:              &toolsNodeCheckpointState{Messages: []*schema.Message{input}},
					},
				},
			},
		}
	}
	if stateLayoutVersion == checkpointStateLayoutVersionV1 {
		cp.InterruptID2State[checkpointLayoutSentinelID] = core.InterruptState{
			State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
		}
		if withChildCheckpoint {
			childCheckpoint := cp.SubGraphs["subgraph"]
			nestedCheckpoint := childCheckpoint.SubGraphs["nested"]
			childCheckpoint.InterruptID2Addr = map[string]Address{"interrupt": address}
			childCheckpoint.InterruptID2State = map[string]core.InterruptState{
				checkpointLayoutSentinelID: {
					State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
				},
			}
			nestedCheckpoint.InterruptID2Addr = map[string]Address{"interrupt": address}
			nestedCheckpoint.InterruptID2State = map[string]core.InterruptState{
				checkpointLayoutSentinelID: {
					State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
				},
			}
		}
	}
	if mutateCheckpoint != nil {
		mutateCheckpoint(cp, address, input)
	}
	data, err := (&serialization.InternalSerializer{}).Marshal(cp)
	require.NoError(t, err)
	require.NoError(t, store.Set(context.Background(), "multi-level-tools-state", data))

	var toolsPath *NodePath
	if withSibling {
		toolsPath = NewNodePath("subgraph", "nested", "tools")
	}
	opts := newToolsNodePreflightOptions("multi-level-tools-state", counters, toolsPath)
	if !streaming {
		output, invokeErr := runnable.Invoke(context.Background(), nil, opts...)
		return output, counters, invokeErr
	}
	stream, streamErr := runnable.Stream(context.Background(), nil, opts...)
	if streamErr != nil {
		return nil, counters, streamErr
	}
	defer stream.Close()
	var output []*schema.Message
	for {
		chunk, receiveErr := stream.Recv()
		if receiveErr == io.EOF {
			return output, counters, nil
		}
		if receiveErr != nil {
			return nil, counters, receiveErr
		}
		output = append(output, chunk...)
	}
}

func TestToolsNodeRoutePreflight(t *testing.T) {
	t.Run("distinct_structural_routes_with_same_display", func(t *testing.T) {
		input := schema.AssistantMessage("", []schema.ToolCall{{
			ID:       "call",
			Function: schema.FunctionCall{Name: "tool", Arguments: `{}`},
		}})
		directRoute := Address{
			{Type: AddressSegmentRunnable, ID: "root"},
			{Type: AddressSegmentNode, ID: "parent;node:tools"},
		}
		nestedRoute := Address{
			{Type: AddressSegmentRunnable, ID: "root"},
			{Type: AddressSegmentNode, ID: "parent"},
			{Type: AddressSegmentNode, ID: "tools"},
		}
		require.Equal(t, directRoute.String(), nestedRoute.String())
		require.False(t, directRoute.Equals(nestedRoute))

		toolsCall := func(child *runner) *chanCall {
			componentType := ComponentOfToolsNode
			if child != nil {
				componentType = ComponentOfGraph
			}
			return &chanCall{action: &composableRunnable{
				meta:        &executorMeta{component: componentType},
				graphRunner: child,
			}}
		}
		child := &runner{chanSubscribeTo: map[string]*chanCall{
			"tools": toolsCall(nil),
		}}
		r := &runner{chanSubscribeTo: map[string]*chanCall{
			"parent;node:tools": toolsCall(nil),
			"parent":            toolsCall(child),
		}}
		cp := &checkpoint{
			InterruptID2Addr: map[string]Address{
				"direct": directRoute,
				"nested": nestedRoute,
			},
			InterruptID2State: map[string]core.InterruptState{
				"direct": {State: &toolsInterruptAndRerunState{Input: input}},
				"nested": {State: &toolsInterruptAndRerunState{Input: input}},
			},
		}

		require.NoError(t, r.validateCheckpointToolsNodeStates(cp, directRoute[:1]))
	})

	type routeMutation struct {
		name   string
		mutate func(*checkpoint, Address, *schema.Message)
		error  string
	}
	mutations := []routeMutation{
		{
			name: "duplicate",
			mutate: func(cp *checkpoint, address Address, input *schema.Message) {
				cp.InterruptID2Addr["interrupt-b"] = append(Address(nil), address...)
				cp.InterruptID2State["interrupt-b"] = core.InterruptState{
					State: toolsNodeCheckpointStateForVersion(cp.StateLayoutVersion, input),
				}
			},
			error: `[GraphRunError] invalid checkpoint tools state: tools node interrupt states ` +
				`"interrupt" and "interrupt-b" share route ` +
				`"runnable:root;node:subgraph;node:nested;node:tools"`,
		},
		{
			name: "wrong_state_type",
			mutate: func(cp *checkpoint, _ Address, _ *schema.Message) {
				cp.InterruptID2State["interrupt"] = core.InterruptState{State: "wrong state"}
			},
			error: `[GraphRunError] invalid checkpoint tools state: ` +
				`tools node interrupt state "interrupt" has invalid type string`,
		},
	}
	for _, baseMismatch := range []struct {
		name   string
		mutate func(Address)
	}{
		{
			name: "base_type_mismatch_with_wrong_state_type",
			mutate: func(address Address) {
				address[0].Type = AddressSegmentNode
			},
		},
		{
			name: "base_id_mismatch_with_wrong_state_type",
			mutate: func(address Address) {
				address[0].ID = "other"
			},
		},
	} {
		baseMismatch := baseMismatch
		expectedAddress := Address{
			{Type: AddressSegmentRunnable, ID: "root"},
			{Type: AddressSegmentNode, ID: "subgraph"},
			{Type: AddressSegmentNode, ID: "nested"},
			{Type: AddressSegmentNode, ID: "tools"},
		}
		baseMismatch.mutate(expectedAddress)
		mutations = append(mutations, routeMutation{
			name: baseMismatch.name,
			mutate: func(cp *checkpoint, address Address, _ *schema.Message) {
				mutated := append(Address(nil), address...)
				baseMismatch.mutate(mutated)
				cp.InterruptID2Addr["interrupt"] = mutated
				cp.InterruptID2State["interrupt"] = core.InterruptState{State: "wrong state"}
			},
			error: fmt.Sprintf(`[GraphRunError] invalid checkpoint tools state: tools node interrupt state `+
				`"interrupt" at owner path [] has route %q `+
				`with an incompatible complete address for a compiled ToolsNode`,
				expectedAddress.String()),
		})
	}
	illegalDescendantAddress := Address{
		{Type: AddressSegmentRunnable, ID: "root"},
		{Type: AddressSegmentNode, ID: "subgraph"},
		{Type: AddressSegmentNode, ID: "nested"},
		{Type: AddressSegmentNode, ID: "tools"},
		{Type: AddressSegmentNode, ID: "descendant"},
	}
	mutations = append(mutations, routeMutation{
		name: "tools_node_illegal_descendant",
		mutate: func(cp *checkpoint, address Address, _ *schema.Message) {
			cp.InterruptID2Addr["interrupt"] = append(
				append(Address(nil), address...),
				AddressSegment{Type: AddressSegmentNode, ID: "descendant"},
			)
		},
		error: fmt.Sprintf(`[GraphRunError] invalid checkpoint tools state: tools node interrupt state `+
			`"interrupt" at owner path [] has route %q `+
			`with an incompatible complete address for a compiled ToolsNode`,
			illegalDescendantAddress.String()),
	})
	for routeIndex, name := range []string{"inherited", "intermediate", "terminal"} {
		index := routeIndex + 1
		expectedAddress := Address{
			{Type: AddressSegmentRunnable, ID: "root"},
			{Type: AddressSegmentNode, ID: "subgraph"},
			{Type: AddressSegmentNode, ID: "nested"},
			{Type: AddressSegmentNode, ID: "tools"},
		}
		expectedAddress[index].SubID = "unexpected"
		mutations = append(mutations, routeMutation{
			name: name + "_sub_id_mismatch",
			mutate: func(cp *checkpoint, address Address, _ *schema.Message) {
				mutated := append(Address(nil), address...)
				mutated[index].SubID = "unexpected"
				cp.InterruptID2Addr["interrupt"] = mutated
			},
			error: fmt.Sprintf(`[GraphRunError] invalid checkpoint tools state: tools node interrupt state `+
				`"interrupt" at owner path [] has route %q `+
				`with an incompatible complete address for a compiled ToolsNode`,
				expectedAddress.String()),
		})
		if name != "inherited" {
			mutations = append(mutations, routeMutation{
				name: name + "_sub_id_mismatch_with_wrong_state_type",
				mutate: func(cp *checkpoint, address Address, _ *schema.Message) {
					mutated := append(Address(nil), address...)
					mutated[index].SubID = "unexpected"
					cp.InterruptID2Addr["interrupt"] = mutated
					cp.InterruptID2State["interrupt"] = core.InterruptState{State: "wrong state"}
				},
				error: fmt.Sprintf(`[GraphRunError] invalid checkpoint tools state: tools node interrupt state `+
					`"interrupt" at owner path [] has route %q `+
					`with an incompatible complete address for a compiled ToolsNode`,
					expectedAddress.String()),
			})
		}
	}

	for _, version := range []int{0, checkpointStateLayoutVersionV1} {
		versionName := "v0"
		if version == checkpointStateLayoutVersionV1 {
			versionName = "v1"
		}
		for _, mutation := range mutations {
			for _, streaming := range []bool{false, true} {
				mode := "invoke"
				if streaming {
					mode = "stream"
				}
				t.Run(versionName+"/"+mutation.name+"/"+mode, func(t *testing.T) {
					_, counters, err := runMultiLevelToolsNodeCheckpoint(
						t, streaming, true, false, version,
						func(input *schema.Message) any {
							return toolsNodeCheckpointStateForVersion(version, input)
						}, mutation.mutate)

					require.EqualError(t, err, mutation.error)
					requireNoToolsNodeSideEffects(t, counters)
				})
			}
		}
	}
}

func toolsNodeCheckpointStateForVersion(version int, input *schema.Message) any {
	if version == checkpointStateLayoutVersionV1 {
		return &toolsInterruptAndRerunStateV1{
			Version:    toolsInterruptAndRerunStateVersionV1,
			Role:       schema.Assistant,
			ToolCalls:  append([]schema.ToolCall(nil), input.ToolCalls...),
			RerunTools: []string{"call"},
		}
	}
	return &toolsInterruptAndRerunState{Input: input}
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

	require.EqualError(t, hydrateCheckpointToolsNodeState(cp),
		`tools node interrupt state "tool" has both inline tool calls and a source reference`)
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
	require.EqualError(t, err, `duplicate tool call ID "duplicate"`)
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
		require.EqualError(t, err,
			`tools node interrupt state has duplicate tool call ID "duplicate"`)
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
		require.EqualError(t, err,
			`tools node interrupt state has duplicate executed tool call ID "duplicate"`)
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
		require.EqualError(t, err,
			`tools node interrupt state tool call ID "duplicate" is both executed and pending rerun`)
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
		require.EqualError(t, err,
			`tools node interrupt state has duplicate rerun tool call ID "duplicate"`)
	})
}

func TestAttack_ToolsNodeV1RequiresExactResultPartition(t *testing.T) {
	tests := []struct {
		name    string
		state   *toolsInterruptAndRerunStateV1
		wantErr string
	}{
		{
			name: "unknown_standard_result",
			state: &toolsInterruptAndRerunStateV1{
				ToolCalls:     []schema.ToolCall{{ID: "known"}},
				ExecutedTools: map[string]string{"unknown": "result"},
				RerunTools:    []string{"known"},
			},
			wantErr: `tools node interrupt state has result for unknown tool call ID "unknown"`,
		},
		{
			name: "unknown_enhanced_result",
			state: &toolsInterruptAndRerunStateV1{
				ToolCalls:             []schema.ToolCall{{ID: "known"}},
				ExecutedEnhancedTools: map[string]*schema.ToolResult{"unknown": {}},
				RerunTools:            []string{"known"},
			},
			wantErr: `tools node interrupt state has result for unknown tool call ID "unknown"`,
		},
		{
			name: "unknown_rerun_marker",
			state: &toolsInterruptAndRerunStateV1{
				ToolCalls:  []schema.ToolCall{{ID: "known"}},
				RerunTools: []string{"known", "unknown"},
			},
			wantErr: `tools node interrupt state has rerun marker for unknown tool call ID "unknown"`,
		},
		{
			name: "missing_classification",
			state: &toolsInterruptAndRerunStateV1{
				ToolCalls:     []schema.ToolCall{{ID: "executed"}, {ID: "missing"}},
				ExecutedTools: map[string]string{"executed": "result"},
			},
			wantErr: `tools node interrupt state tool call ID "missing" has neither an executed result nor a rerun marker`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.state.Version = toolsInterruptAndRerunStateVersionV1
			tt.state.Role = schema.Assistant
			ctx := toolsNodeCheckpointContext(tt.state)
			_, _, _, err := restoreToolsInterruptState(ctx, nil, nil, nil)
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestAttack_ToolsNodeV1PartitionValidationInvokeStreamParity(t *testing.T) {
	state := &toolsInterruptAndRerunStateV1{
		Version:   toolsInterruptAndRerunStateVersionV1,
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{{ID: "missing"}},
	}
	ctx := toolsNodeCheckpointContext(state)
	node := &ToolsNode{}

	_, invokeErr := node.Invoke(ctx, nil)
	_, streamErr := node.Stream(ctx, nil)
	require.EqualError(t, invokeErr,
		`tools node interrupt state tool call ID "missing" has neither an executed result nor a rerun marker`)
	require.EqualError(t, streamErr, invokeErr.Error())
}
