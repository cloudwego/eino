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

package adk

import (
	"bytes"
	"encoding/gob"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
)

type composeCheckpointSizeMirror struct {
	State             any
	Inputs            map[string]any
	SubGraphs         map[string]*composeCheckpointSizeMirror
	InterruptID2State map[string]core.InterruptState
}

type checkpointSizeBreakdown struct {
	RunnerRawBytes             int
	RunCtxBytes                int
	LegacyInfoBytes            int
	OuterInterruptStateBytes   int
	ComposeStateBytes          int
	ComposeInputsBytes         int
	ComposeSubGraphsBytes      int
	ComposeInterruptStateBytes int
	AgentToolChildRunnerBytes  int
	RunCtxProjectionRefs       int
	InfoProjectionRefs         int
	InfoStateBytes             int
	InfoSubGraphsBytes         int
	InfoInterruptContextsBytes int
	InfoRerunExtraBytes        int
}

func gobEncodedSize(t *testing.T, value any) int {
	t.Helper()
	if value == nil {
		return 0
	}
	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(value))
	return buf.Len()
}

func measureCheckpointSizeBreakdown(t *testing.T, raw []byte) checkpointSizeBreakdown {
	t.Helper()
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))

	breakdown := checkpointSizeBreakdown{
		RunnerRawBytes:           len(raw),
		RunCtxBytes:              gobEncodedSize(t, outer.RunCtx),
		LegacyInfoBytes:          gobEncodedSize(t, outer.Info),
		OuterInterruptStateBytes: gobEncodedSize(t, outer.InterruptID2State),
	}
	if outer.ProjectionV1 != nil {
		breakdown.RunCtxProjectionRefs = len(outer.ProjectionV1.RunCtxRefs)
		breakdown.InfoProjectionRefs = len(outer.ProjectionV1.InfoRefs)
	}
	if chatModelInfo, ok := outer.Info.Data.(*ChatModelAgentInterruptInfo); ok && chatModelInfo != nil {
		measureInterruptInfoBreakdown(t, chatModelInfo.Info, &breakdown)
	}
	for _, state := range outer.InterruptID2State {
		measureCheckpointStateValue(t, state.State, &breakdown)
	}
	return breakdown
}

func measureInterruptInfoBreakdown(t *testing.T, info *compose.InterruptInfo,
	breakdown *checkpointSizeBreakdown) {
	t.Helper()
	if info == nil {
		return
	}
	breakdown.InfoStateBytes += gobEncodedSize(t, info.State)
	breakdown.InfoSubGraphsBytes += gobEncodedSize(t, info.SubGraphs)
	breakdown.InfoInterruptContextsBytes += gobEncodedSize(t, info.InterruptContexts)
	breakdown.InfoRerunExtraBytes += gobEncodedSize(t, info.RerunNodesExtra)
	for _, interruptCtx := range info.InterruptContexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			if state, ok := current.Info.(*State); ok && state != nil {
				breakdown.InfoStateBytes += gobEncodedSize(t, state)
			}
			if projected, ok := current.Info.(*checkpointInterruptInfoPlaceholderV1); ok && projected != nil {
				measureInterruptInfoBreakdown(t, projected.Info, breakdown)
			}
		}
	}
	for _, sub := range info.SubGraphs {
		measureInterruptInfoBreakdown(t, sub, breakdown)
	}
}

func measureCheckpointStateValue(t *testing.T, value any, breakdown *checkpointSizeBreakdown) {
	t.Helper()
	switch state := value.(type) {
	case []byte:
		measureComposeCheckpointBytes(t, state, breakdown)
	case *agentToolInterruptStateV1:
		if state == nil {
			return
		}
		breakdown.AgentToolChildRunnerBytes += len(state.BridgeCheckpoint)
		var child serialization
		require.NoError(t, gob.NewDecoder(bytes.NewReader(state.BridgeCheckpoint)).Decode(&child))
		for _, childState := range child.InterruptID2State {
			measureCheckpointStateValue(t, childState.State, breakdown)
		}
	}
}

func measureComposeCheckpointBytes(t *testing.T, data []byte, breakdown *checkpointSizeBreakdown) {
	t.Helper()
	var cp composeCheckpointSizeMirror
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&cp); err != nil {
		return
	}
	measureComposeCheckpoint(t, &cp, breakdown)
}

func measureComposeCheckpoint(t *testing.T, cp *composeCheckpointSizeMirror,
	breakdown *checkpointSizeBreakdown) {
	t.Helper()
	if cp == nil {
		return
	}
	breakdown.ComposeStateBytes += gobEncodedSize(t, cp.State)
	breakdown.ComposeInputsBytes += gobEncodedSize(t, cp.Inputs)
	breakdown.ComposeSubGraphsBytes += gobEncodedSize(t, cp.SubGraphs)
	breakdown.ComposeInterruptStateBytes += gobEncodedSize(t, cp.InterruptID2State)
	for _, state := range cp.InterruptID2State {
		measureCheckpointStateValue(t, state.State, breakdown)
	}
	for _, sub := range cp.SubGraphs {
		measureComposeCheckpoint(t, sub, breakdown)
	}
}

func TestCheckpointSizeBreakdown(t *testing.T) {
	tests := []checkpointCompatFixture{
		{Name: "single_invoke", PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "single_stream", Streaming: true, PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "agent_tool_invoke", Depth: 1, PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "agent_tool_stream", Depth: 1, Streaming: true, PayloadField: "content", PayloadSize: 32 << 10},
		{Name: "agent_tool_320k", Depth: 1, PayloadField: "content", PayloadSize: 320 << 10},
		{Name: "parallel_6", ParallelChildren: 6, PayloadField: "content", PayloadSize: 32 << 10},
	}
	for _, spec := range tests {
		t.Run(spec.Name, func(t *testing.T) {
			raw, _, _ := captureCheckpointCompatFixture(t, spec)
			breakdown := measureCheckpointSizeBreakdown(t, raw)
			t.Logf("checkpoint size breakdown: %+v", breakdown)
			require.Equal(t, len(raw), breakdown.RunnerRawBytes)
			require.Positive(t, breakdown.RunCtxBytes)
			require.Positive(t, breakdown.LegacyInfoBytes)
			require.Positive(t, breakdown.OuterInterruptStateBytes)
			require.Positive(t, breakdown.ComposeStateBytes)
			require.Positive(t, breakdown.ComposeInterruptStateBytes)
			if spec.Depth > 0 || spec.ParallelChildren > 0 {
				require.Positive(t, breakdown.AgentToolChildRunnerBytes)
			}
		})
	}
}

func TestCheckpointSizeScalesLinearlyWithDepthAndWidth(t *testing.T) {
	const payloadSize = 320 << 10

	depthSizes := make([]int, 4)
	for depth := 0; depth < len(depthSizes); depth++ {
		spec := checkpointCompatFixture{
			Name:         "depth",
			Depth:        depth,
			PayloadField: "content",
			PayloadSize:  payloadSize,
		}
		raw, _, _ := captureCheckpointCompatFixture(t, spec)
		depthSizes[depth] = len(raw)
		t.Logf("depth=%d checkpoint bytes=%d", depth, len(raw))
	}
	for depth := 2; depth < len(depthSizes); depth++ {
		increment := depthSizes[depth] - depthSizes[depth-1]
		require.Less(t, increment, payloadSize/2,
			"an added AgentTool layer must not duplicate the large payload")
	}

	widths := []int{1, 2, 6}
	widthSizes := make([]int, len(widths))
	for i, width := range widths {
		spec := checkpointCompatFixture{
			Name:             "width",
			ParallelChildren: width,
			PayloadField:     "content",
			PayloadSize:      payloadSize,
		}
		raw, _, _ := captureCheckpointCompatFixture(t, spec)
		widthSizes[i] = len(raw)
		t.Logf("width=%d checkpoint bytes=%d", width, len(raw))
	}
	require.Less(t, widthSizes[1], 2*widthSizes[0]+payloadSize,
		"two children must scale by their unique state, not duplicate the whole checkpoint")
	require.Less(t, widthSizes[2], 3*widthSizes[1]+payloadSize,
		"six children must scale approximately linearly from two children")
}

func TestCheckpointSizeMatrix(t *testing.T) {
	t.Run("small_payload", func(t *testing.T) {
		single, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
			Name: "small-single", PayloadField: "content",
		})
		nested, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
			Name: "small-nested", Depth: 1, PayloadField: "content",
		})
		require.Less(t, len(single), 30_000)
		require.Less(t, len(nested), 120_000)
	})

	for _, size := range []int{0, 256 << 10, 320 << 10, 1 << 20} {
		t.Run(fmt.Sprintf("content_size_%d", size), func(t *testing.T) {
			raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
				Name:         "content-size",
				Depth:        1,
				PayloadField: "content",
				PayloadSize:  size,
			})
			t.Logf("payload=%d checkpoint=%d", size, len(raw))
			if size == 320<<10 {
				require.Less(t, len(raw), 1<<20)
			}
			if size == 1<<20 {
				require.Less(t, len(raw), 5<<18)
			}
		})
	}

	for _, field := range []string{
		"user_query",
		"content",
		"reasoning",
		"tool_arguments",
		"extra",
		"multimodal",
	} {
		for _, streaming := range []bool{false, true} {
			name := fmt.Sprintf("%s_stream_%t", field, streaming)
			t.Run(name, func(t *testing.T) {
				raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
					Name:         name,
					Depth:        1,
					Streaming:    streaming,
					PayloadField: field,
					PayloadSize:  320 << 10,
				})
				t.Logf("field=%s streaming=%t checkpoint=%d", field, streaming, len(raw))
				require.Less(t, len(raw), 2<<20)
			})
		}
	}
}
