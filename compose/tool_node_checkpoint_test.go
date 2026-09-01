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
	"testing"

	"github.com/stretchr/testify/require"

	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

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
