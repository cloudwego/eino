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

func TestCheckpointValueAPIsUseDefaultSerializer(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	data, err := serializer.Marshal(&checkpoint{State: "before"})
	require.NoError(t, err)

	var visited any
	err = WalkCheckpointValues(data, nil, func(_ NodePath,
		location CheckpointValueLocation, value any) error {
		if location.Kind == CheckpointValueState {
			visited = value
		}
		return nil
	})
	require.NoError(t, err)
	require.Equal(t, "before", visited)

	transformed, err := TransformCheckpointValues(data, nil, func(_ NodePath,
		location CheckpointValueLocation, value any) (any, bool, error) {
		if location.Kind == CheckpointValueState {
			return "after", true, nil
		}
		return value, false, nil
	})
	require.NoError(t, err)
	require.NotEqual(t, data, transformed)

	var got checkpoint
	require.NoError(t, serializer.Unmarshal(transformed, &got))
	require.Equal(t, "after", got.State)
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

func TestTransformCheckpointValues_MarshalError(t *testing.T) {
	marshalErr := errors.New("marshal failed")
	codec := stubSerializer{
		unmarshal: func(_ []byte, value any) error {
			*(value.(*checkpoint)) = checkpoint{State: "before"}
			return nil
		},
		marshal: func(value any) ([]byte, error) {
			require.Equal(t, "after", value.(*checkpoint).State)
			return nil, marshalErr
		},
	}

	transformed, err := TransformCheckpointValues([]byte("checkpoint"), codec,
		func(_ NodePath, location CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind == CheckpointValueState {
				return "after", true, nil
			}
			return value, false, nil
		})
	require.Nil(t, transformed)
	require.EqualError(t, err, "failed to encode transformed checkpoint: marshal failed")
}

func TestCheckpointValueAPIsValidateFormatBeforeCallbacks(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	marshal := func(t *testing.T, cp *checkpoint) []byte {
		t.Helper()
		data, err := serializer.Marshal(cp)
		require.NoError(t, err)
		return data
	}
	apis := []struct {
		name string
		run  func([]byte, *bool) ([]byte, error)
	}{
		{
			name: "walk",
			run: func(data []byte, called *bool) ([]byte, error) {
				err := WalkCheckpointValues(data, serializer,
					func(NodePath, CheckpointValueLocation, any) error {
						*called = true
						return nil
					})
				return data, err
			},
		},
		{
			name: "transform",
			run: func(data []byte, called *bool) ([]byte, error) {
				return TransformCheckpointValues(data, serializer,
					func(_ NodePath, _ CheckpointValueLocation, value any) (any, bool, error) {
						*called = true
						return value, false, nil
					})
			},
		},
		{
			name: "migrate",
			run: func(data []byte, called *bool) ([]byte, error) {
				return MigrateCheckpointState(data, serializer, func(value any) (any, bool, error) {
					*called = true
					return value, false, nil
				})
			},
		},
	}

	invalid := []struct {
		name string
		cp   *checkpoint
		want string
	}{
		{
			name: "unsupported",
			cp: &checkpoint{
				StateLayoutVersion: checkpointStateLayoutVersionV1 + 1,
				State:              "root",
			},
			want: "unsupported state layout version 2",
		},
		{
			name: "missing_sentinel",
			cp: &checkpoint{
				StateLayoutVersion: checkpointStateLayoutVersionV1,
				State:              "root",
			},
			want: "checkpoint state layout sentinel is missing",
		},
		{
			name: "mixed",
			cp: &checkpoint{
				State: "root",
				SubGraphs: map[string]*checkpoint{
					"child": {
						StateLayoutVersion: checkpointStateLayoutVersionV1,
						State:              "child",
						InterruptID2State: map[string]core.InterruptState{
							checkpointLayoutSentinelID: {
								State: &checkpointLayoutSentinelV1{
									Version: checkpointStateLayoutVersionV1,
								},
							},
						},
					},
				},
			},
			want: "mixed checkpoint state layout",
		},
	}
	for _, format := range invalid {
		t.Run(format.name, func(t *testing.T) {
			data := marshal(t, format.cp)
			for _, api := range apis {
				t.Run(api.name, func(t *testing.T) {
					called := false
					_, err := api.run(data, &called)
					require.ErrorContains(t, err, format.want)
					require.False(t, called)
				})
			}
		})
	}

	valid := []struct {
		name string
		cp   *checkpoint
	}{
		{
			name: "legacy_v0",
			cp: &checkpoint{
				State: "root",
				SubGraphs: map[string]*checkpoint{
					"child": {State: "child"},
				},
			},
		},
		{
			name: "v1",
			cp: &checkpoint{
				StateLayoutVersion: checkpointStateLayoutVersionV1,
				State:              "root",
				InterruptID2State: map[string]core.InterruptState{
					checkpointLayoutSentinelID: {
						State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
					},
				},
				SubGraphs: map[string]*checkpoint{
					"child": {
						StateLayoutVersion: checkpointStateLayoutVersionV1,
						State:              "child",
						InterruptID2State: map[string]core.InterruptState{
							checkpointLayoutSentinelID: {
								State: &checkpointLayoutSentinelV1{
									Version: checkpointStateLayoutVersionV1,
								},
							},
						},
					},
				},
			},
		},
	}
	for _, format := range valid {
		t.Run(format.name, func(t *testing.T) {
			data := marshal(t, format.cp)
			for _, api := range apis {
				t.Run(api.name, func(t *testing.T) {
					called := false
					got, err := api.run(data, &called)
					require.NoError(t, err)
					require.True(t, called)
					require.Equal(t, data, got)
				})
			}
		})
	}
}

func TestAttack_CheckpointValueAPIsRejectForwardToolsNodeVersion(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	toolCalls := []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{}`,
		},
	}}
	digest, ok := checkpointToolCallsDigest(toolCalls)
	require.True(t, ok)

	newV1Checkpoint := func(state any, compact, nested bool) *checkpoint {
		t.Helper()
		interruptState := core.InterruptState{State: state}
		graphState := any("state")
		if compact {
			graphState = &toolsNodeCheckpointState{Messages: []*schema.Message{
				schema.AssistantMessage("", toolCalls),
			}}
		}
		owner := &checkpoint{
			StateLayoutVersion: checkpointStateLayoutVersionV1,
			State:              graphState,
			InterruptID2State: map[string]core.InterruptState{
				checkpointLayoutSentinelID: {
					State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
				},
				"tool": interruptState,
			},
		}
		if !nested {
			return owner
		}
		return &checkpoint{
			StateLayoutVersion: checkpointStateLayoutVersionV1,
			State:              "root",
			InterruptID2State: map[string]core.InterruptState{
				checkpointLayoutSentinelID: {
					State: &checkpointLayoutSentinelV1{Version: checkpointStateLayoutVersionV1},
				},
			},
			SubGraphs: map[string]*checkpoint{"child": owner},
		}
	}
	newToolsState := func(version int, compact bool) *toolsInterruptAndRerunStateV1 {
		state := &toolsInterruptAndRerunStateV1{
			Version: version,
			Role:    schema.Assistant,
		}
		if compact {
			state.ToolCallsSource = &toolsInterruptToolCallsSourceV1{
				MessageIndex: 0,
				Digest:       digest,
			}
		} else {
			state.ToolCalls = toolCalls
		}
		return state
	}
	marshal := func(t *testing.T, cp *checkpoint) []byte {
		t.Helper()
		data, err := serializer.Marshal(cp)
		require.NoError(t, err)
		return data
	}
	apis := []struct {
		name string
		run  func([]byte, *bool) error
	}{
		{
			name: "walk",
			run: func(data []byte, called *bool) error {
				return WalkCheckpointValues(data, serializer,
					func(NodePath, CheckpointValueLocation, any) error {
						*called = true
						return nil
					})
			},
		},
		{
			name: "transform",
			run: func(data []byte, called *bool) error {
				_, err := TransformCheckpointValues(data, serializer,
					func(_ NodePath, _ CheckpointValueLocation, value any) (any, bool, error) {
						*called = true
						return value, false, nil
					})
				return err
			},
		},
		{
			name: "migrate",
			run: func(data []byte, called *bool) error {
				_, err := MigrateCheckpointState(data, serializer, func(value any) (any, bool, error) {
					*called = true
					return value, false, nil
				})
				return err
			},
		},
	}

	invalid := []struct {
		name string
		cp   *checkpoint
		want string
	}{
		{
			name: "inline_root",
			cp: newV1Checkpoint(
				newToolsState(toolsInterruptAndRerunStateVersionV1+1, false), false, false),
			want: `tools node interrupt state "tool" has unsupported version 2`,
		},
		{
			name: "hydrated_nested",
			cp: newV1Checkpoint(
				newToolsState(toolsInterruptAndRerunStateVersionV1+1, true), true, true),
			want: `tools node interrupt state "tool" has unsupported version 2`,
		},
		{
			name: "typed_nil",
			cp:   newV1Checkpoint((*toolsInterruptAndRerunStateV1)(nil), false, false),
			want: `tools node interrupt state "tool" is nil`,
		},
	}
	for _, wireState := range invalid {
		t.Run(wireState.name, func(t *testing.T) {
			data := marshal(t, wireState.cp)
			for _, api := range apis {
				t.Run(api.name, func(t *testing.T) {
					called := false
					err := api.run(data, &called)
					require.ErrorContains(t, err, wireState.want)
					require.False(t, called)
				})
			}
		})
	}

	valid := []struct {
		name string
		cp   *checkpoint
	}{
		{
			name: "legacy",
			cp: &checkpoint{
				State: "root",
				InterruptID2State: map[string]core.InterruptState{
					"tool": {State: &toolsInterruptAndRerunState{
						Input: schema.AssistantMessage("", toolCalls),
					}},
				},
			},
		},
		{
			name: "v1_inline",
			cp:   newV1Checkpoint(newToolsState(toolsInterruptAndRerunStateVersionV1, false), false, false),
		},
		{
			name: "v1_hydrated_nested",
			cp:   newV1Checkpoint(newToolsState(toolsInterruptAndRerunStateVersionV1, true), true, true),
		},
	}
	for _, wireState := range valid {
		t.Run(wireState.name, func(t *testing.T) {
			data := marshal(t, wireState.cp)
			for _, api := range apis {
				t.Run(api.name, func(t *testing.T) {
					called := false
					require.NoError(t, api.run(data, &called))
					require.True(t, called)
				})
			}
		})
	}
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
			}, Unrelated: "before"},
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
	assertInlineFallback := func(t *testing.T, data []byte) {
		t.Helper()
		var cp checkpoint
		require.NoError(t, serializer.Unmarshal(data, &cp))
		state, ok := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.True(t, ok)
		require.Equal(t, toolCalls, state.ToolCalls)
		require.Nil(t, state.ToolCallsSource)
		require.Equal(t, "after", cp.State.(*toolsNodeCheckpointState).Unrelated)
		require.NoError(t, hydrateCheckpointToolsNodeState(&cp))
	}
	assertReferenceRebound := func(t *testing.T, data []byte) {
		t.Helper()
		var cp checkpoint
		require.NoError(t, serializer.Unmarshal(data, &cp))
		state, ok := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.True(t, ok)
		require.Nil(t, state.ToolCalls)
		require.NotNil(t, state.ToolCallsSource)
		require.Equal(t, 0, state.ToolCallsSource.MessageIndex)
		require.Equal(t, digest, state.ToolCallsSource.Digest)
		require.Equal(t, "after", cp.State.(*toolsNodeCheckpointState).Unrelated)
		require.NoError(t, hydrateCheckpointToolsNodeState(&cp))
		hydrated := cp.InterruptID2State["tool"].State.(*toolsInterruptAndRerunStateV1)
		require.Equal(t, toolCalls, hydrated.ToolCalls)
		require.Nil(t, hydrated.ToolCallsSource)
	}

	t.Run("transform_falls_back_inline_when_source_calls_change", func(t *testing.T) {
		data, err := TransformCheckpointValues(newCheckpoint(), serializer, func(_ NodePath,
			location CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind != CheckpointValueState {
				return value, false, nil
			}
			return &toolsNodeCheckpointState{Messages: []*schema.Message{
				schema.AssistantMessage("", replacementCalls),
			}, Unrelated: "after"}, true, nil
		})
		require.NoError(t, err)
		assertInlineFallback(t, data)
	})

	t.Run("migration_falls_back_inline_when_source_calls_change", func(t *testing.T) {
		data, err := MigrateCheckpointState(newCheckpoint(), serializer,
			func(any) (any, bool, error) {
				return &toolsNodeCheckpointState{Messages: []*schema.Message{
					schema.AssistantMessage("", replacementCalls),
				}, Unrelated: "after"}, true, nil
			})
		require.NoError(t, err)
		assertInlineFallback(t, data)
	})

	t.Run("transform_rebinds_reference_after_unrelated_state_change", func(t *testing.T) {
		data, err := TransformCheckpointValues(newCheckpoint(), serializer, func(_ NodePath,
			location CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind != CheckpointValueState {
				return value, false, nil
			}
			state := value.(*toolsNodeCheckpointState)
			return &toolsNodeCheckpointState{
				Messages:  append([]*schema.Message(nil), state.Messages...),
				Unrelated: "after",
			}, true, nil
		})
		require.NoError(t, err)
		assertReferenceRebound(t, data)
	})

	t.Run("migration_rebinds_reference_after_unrelated_state_change", func(t *testing.T) {
		data, err := MigrateCheckpointState(newCheckpoint(), serializer,
			func(value any) (any, bool, error) {
				state := value.(*toolsNodeCheckpointState)
				return &toolsNodeCheckpointState{
					Messages:  append([]*schema.Message(nil), state.Messages...),
					Unrelated: "after",
				}, true, nil
			})
		require.NoError(t, err)
		assertReferenceRebound(t, data)
	})
}

func TestCheckpointValueAPIsRejectNilSubgraph(t *testing.T) {
	serializer := &serialization.InternalSerializer{}
	data, err := serializer.Marshal(&checkpoint{
		State:     "old",
		SubGraphs: map[string]*checkpoint{"child": nil},
	})
	require.NoError(t, err)

	tests := []struct {
		name string
		run  func([]byte) error
	}{
		{
			name: "walk",
			run: func(data []byte) error {
				return WalkCheckpointValues(data, serializer,
					func(NodePath, CheckpointValueLocation, any) error { return nil })
			},
		},
		{
			name: "transform",
			run: func(data []byte) error {
				_, transformErr := TransformCheckpointValues(data, serializer,
					func(_ NodePath, _ CheckpointValueLocation, value any) (any, bool, error) {
						return value, false, nil
					})
				return transformErr
			},
		},
		{
			name: "migrate",
			run: func(data []byte) error {
				_, migrateErr := MigrateCheckpointState(data, serializer,
					func(value any) (any, bool, error) {
						return value, false, nil
					})
				return migrateErr
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.ErrorContains(t, tt.run(data), `subgraph checkpoint "child" is nil`)
		})
	}
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
