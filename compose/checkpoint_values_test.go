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
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/serialization"
	"github.com/cloudwego/eino/schema"
)

func TestWalkAndTransformCheckpointValues(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	cp := &checkpoint{
		StateLayoutVersion: checkpointStateLayoutVersionV1,
		State:              "root-state",
		Inputs: map[string]any{
			"z": "input-z",
			"a": "input-a",
		},
		Channels: map[string]channel{
			"channel": &pregelChannel{Values: map[string]any{
				"z": "channel-z",
				"a": "channel-a",
			}},
		},
		InterruptID2State: map[string]core.InterruptState{
			"interrupt": {
				State:                "interrupt-state",
				LayerSpecificPayload: "layer-payload",
			},
			checkpointLayoutSentinelID: {
				State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
			},
		},
		SubGraphs: map[string]*checkpoint{
			"child": {
				StateLayoutVersion: checkpointStateLayoutVersionV1,
				State:              "child-state",
				InterruptID2State: map[string]core.InterruptState{
					checkpointLayoutSentinelID: {
						State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
					},
				},
			},
		},
	}
	data, err := serializer.Marshal(cp)
	require.NoError(t, err)

	var visited []string
	err = WalkCheckpointValues(data, serializer, func(path NodePath,
		location CheckpointValueLocation, value any) error {
		visited = append(visited, fmt.Sprintf("%v/%s/%s/%s=%v",
			path.GetPath(), location.Kind, location.Key, location.ValueKey, value))
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, []string{
		"[]/state//=root-state",
		"[]/interrupt_state/interrupt/=interrupt-state",
		"[]/interrupt_layer_payload/interrupt/=layer-payload",
		"[]/input/a/=input-a",
		"[]/input/z/=input-z",
		"[]/channel/channel/a=channel-a",
		"[]/channel/channel/z=channel-z",
		"[child]/state//=child-state",
	}, visited)

	unchanged, err := TransformCheckpointValues(data, serializer, func(_ NodePath,
		_ CheckpointValueLocation, value any) (any, bool, error) {
		return value, false, nil
	})
	require.NoError(t, err)
	require.Equal(t, data, unchanged)

	transformed, err := TransformCheckpointValues(data, serializer, func(_ NodePath,
		_ CheckpointValueLocation, value any) (any, bool, error) {
		text, ok := value.(string)
		if !ok {
			return value, false, nil
		}
		return text + "-projected", true, nil
	})
	require.NoError(t, err)
	require.NotEqual(t, data, transformed)

	var transformedValues []string
	require.NoError(t, WalkCheckpointValues(transformed, serializer, func(_ NodePath,
		_ CheckpointValueLocation, value any) error {
		if text, ok := value.(string); ok {
			transformedValues = append(transformedValues, text)
		}
		return nil
	}))
	require.Equal(t, []string{
		"root-state-projected",
		"interrupt-state-projected",
		"layer-payload-projected",
		"input-a-projected",
		"input-z-projected",
		"channel-a-projected",
		"channel-z-projected",
		"child-state-projected",
	}, transformedValues)

	var got checkpoint
	require.NoError(t, serializer.Unmarshal(transformed, &got))
	require.Contains(t, got.InterruptID2State, checkpointLayoutSentinelID)
}

func TestCheckpointValueTraversalErrors(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	require.ErrorContains(t, WalkCheckpointValues(nil, serializer, nil), "visitor is nil")
	_, err := TransformCheckpointValues(nil, serializer, nil)
	require.ErrorContains(t, err, "transformer is nil")
	require.ErrorContains(t, WalkCheckpointValues([]byte("invalid"), serializer,
		func(NodePath, CheckpointValueLocation, any) error { return nil }),
		"failed to decode checkpoint for inspection")
	require.ErrorContains(t, func() error {
		data, err := serializer.Marshal(&checkpoint{State: "state"})
		require.NoError(t, err)
		return WalkCheckpointValues(data, serializer,
			func(NodePath, CheckpointValueLocation, any) error { return errors.New("visit") })
	}(), "visit")
	require.ErrorContains(t, func() error {
		data, err := serializer.Marshal(&checkpoint{State: "state"})
		require.NoError(t, err)
		_, err = TransformCheckpointValues(data, serializer,
			func(NodePath, CheckpointValueLocation, any) (any, bool, error) {
				return nil, false, errors.New("transform")
			})
		return err
	}(), "transform")
}

func TestCheckpointValueCallbacksHydrateToolsNodeReferences(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	toolCalls := []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{"value":"original"}`,
		},
	}}
	digest, ok := checkpointToolCallsDigest(toolCalls)
	require.True(t, ok)
	newCheckpoint := func() []byte {
		cp := &checkpoint{
			State: &toolsNodeCheckpointState{Messages: []*schema.Message{
				schema.AssistantMessage("", toolCalls),
			}},
			InterruptID2State: map[string]core.InterruptState{
				"tool": {State: &toolsInterruptAndRerunStateV1{
					Version: toolsInterruptAndRerunStateVersionV1,
					Role:    schema.Assistant,
					ToolCallsSource: &toolsInterruptToolCallsSourceV1{
						MessageIndex: 0,
						Digest:       digest,
					},
				}},
			},
		}
		data, err := serializer.Marshal(cp)
		require.NoError(t, err)
		return data
	}

	t.Run("walk_observes_hydrated_state", func(t *testing.T) {
		var visited *toolsInterruptAndRerunStateV1
		err := WalkCheckpointValues(newCheckpoint(), serializer, func(_ NodePath,
			location CheckpointValueLocation, value any) error {
			if location.Kind == CheckpointValueInterruptState {
				visited, _ = value.(*toolsInterruptAndRerunStateV1)
			}
			return nil
		})
		require.NoError(t, err)
		require.NotNil(t, visited)
		require.Equal(t, toolCalls, visited.ToolCalls)
		require.Nil(t, visited.ToolCallsSource)
	})

	replacementCalls := []schema.ToolCall{{
		ID: "replacement",
		Function: schema.FunctionCall{
			Name:      "replacement",
			Arguments: `{}`,
		},
	}}
	assertReferenceRebound := func(t *testing.T, data []byte) {
		t.Helper()
		var cp checkpoint
		require.NoError(t, serializer.Unmarshal(data, &cp))
		state, ok := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.True(t, ok)
		require.Equal(t, toolCalls, state.ToolCalls)
		require.Nil(t, state.ToolCallsSource)
		require.NoError(t, hydrateCheckpointToolsNodeState(&cp))
	}

	t.Run("transform_rebinds_reference_after_state_change", func(t *testing.T) {
		data, err := TransformCheckpointValues(newCheckpoint(), serializer, func(_ NodePath,
			location CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind != CheckpointValueState {
				return value, false, nil
			}
			return &toolsNodeCheckpointState{Messages: []*schema.Message{
				schema.AssistantMessage("", replacementCalls),
			}}, true, nil
		})
		require.NoError(t, err)
		assertReferenceRebound(t, data)
	})

	t.Run("migration_rebinds_reference_after_state_change", func(t *testing.T) {
		data, err := MigrateCheckpointState(newCheckpoint(), serializer,
			func(any) (any, bool, error) {
				return &toolsNodeCheckpointState{Messages: []*schema.Message{
					schema.AssistantMessage("", replacementCalls),
				}}, true, nil
			})
		require.NoError(t, err)
		assertReferenceRebound(t, data)
	})
}

func TestAttack_TransformCheckpointRejectsNilSubgraph(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	data, err := serializer.Marshal(&checkpoint{
		State:     "old",
		SubGraphs: map[string]*checkpoint{"child": nil},
	})
	require.NoError(t, err)

	_, err = TransformCheckpointValues(data, serializer, func(_ NodePath,
		location CheckpointValueLocation, value any) (any, bool, error) {
		if location.Kind == CheckpointValueState {
			return "new", true, nil
		}
		return value, false, nil
	})
	require.ErrorContains(t, err, `subgraph checkpoint "child" is nil`)
}

func TestCheckpointValueAPIsRejectInvalidToolsNodeReference(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	newData := func() []byte {
		cp := &checkpoint{
			State: &toolsNodeCheckpointState{Messages: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{{ID: "call"}}),
			}},
			InterruptID2State: map[string]core.InterruptState{
				"tool": {State: &toolsInterruptAndRerunStateV1{
					Version: toolsInterruptAndRerunStateVersionV1,
					Role:    schema.Assistant,
					ToolCallsSource: &toolsInterruptToolCallsSourceV1{
						MessageIndex: 1,
						Digest:       "invalid",
					},
				}},
			},
		}
		data, err := serializer.Marshal(cp)
		require.NoError(t, err)
		return data
	}

	t.Run("walk", func(t *testing.T) {
		called := false
		err := WalkCheckpointValues(newData(), serializer,
			func(NodePath, CheckpointValueLocation, any) error {
				called = true
				return nil
			})
		require.ErrorContains(t, err, "failed to hydrate checkpoint tool state for inspection")
		require.False(t, called)
	})
	t.Run("transform", func(t *testing.T) {
		called := false
		_, err := TransformCheckpointValues(newData(), serializer,
			func(NodePath, CheckpointValueLocation, any) (any, bool, error) {
				called = true
				return nil, false, nil
			})
		require.ErrorContains(t, err, "failed to hydrate checkpoint tool state for transformation")
		require.False(t, called)
	})
	t.Run("migrate", func(t *testing.T) {
		called := false
		_, err := MigrateCheckpointState(newData(), serializer,
			func(value any) (any, bool, error) {
				called = true
				return value, false, nil
			})
		require.ErrorContains(t, err, "failed to hydrate checkpoint tool state for migration")
		require.False(t, called)
	})
}
