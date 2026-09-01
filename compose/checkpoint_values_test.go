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
