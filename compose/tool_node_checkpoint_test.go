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
	"testing"

	"github.com/stretchr/testify/require"

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
