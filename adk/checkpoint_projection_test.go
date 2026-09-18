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
	"context"
	"encoding/gob"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/require"

	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	checkpointinternal "github.com/cloudwego/eino/internal/checkpoint"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type checkpointProjectionEnhancedTool struct {
	name  string
	mu    sync.Mutex
	calls int
}

type checkpointProjectionPointerKey struct {
	ID string
}

type checkpointProjectionPointerMapState struct {
	Values map[*checkpointProjectionPointerKey]string
}

type checkpointProjectionNaNMapKey struct {
	ID      int
	Float   float64
	Complex complex128
	Nested  [1]float32
}

type checkpointProjectionNaNMapState struct {
	Values map[checkpointProjectionNaNMapKey]int
}

type checkpointProjectionGobNormalizedTailInfo struct {
	Marker       string
	State        State
	NegativeZero float64
	ZeroPointer  *int
	OmittedMap   checkpointProjectionGobNormalizedMap
	InterfaceMap any
	SliceMaps    []checkpointProjectionGobNormalizedMap
	ArrayMaps    [1]checkpointProjectionGobNormalizedMap
	MapValues    map[string]checkpointProjectionGobNormalizedMap
}

type checkpointProjectionGobNormalizedMap map[string]string

type checkpointProjectionGobInfo struct {
	Visible  string
	Behavior string
	cached   []byte
}

var checkpointProjectionGobCalls uint32

func (i *checkpointProjectionGobInfo) GobEncode() ([]byte, error) {
	atomic.AddUint32(&checkpointProjectionGobCalls, 1)
	if i.Behavior == "panic" {
		panic("checkpoint projection GobEncoder must not be called while digesting")
	}
	if i.Behavior == "lazy" && i.cached == nil {
		i.cached = []byte(i.Visible)
	}
	if i.cached == nil {
		return []byte(i.Visible), nil
	}
	return cloneSlice(i.cached), nil
}

func (i *checkpointProjectionGobInfo) GobDecode(data []byte) error {
	i.Visible = string(data)
	i.cached = cloneSlice(data)
	return nil
}

type checkpointProjectionJSONBehavior string

const (
	checkpointProjectionJSONPanic            checkpointProjectionJSONBehavior = "panic"
	checkpointProjectionJSONNondeterministic checkpointProjectionJSONBehavior = "nondeterministic"
)

type checkpointProjectionJSONValue struct {
	Behavior checkpointProjectionJSONBehavior
	Value    string
}

var checkpointProjectionJSONCalls uint32

func (v *checkpointProjectionJSONValue) MarshalJSON() ([]byte, error) {
	call := atomic.AddUint32(&checkpointProjectionJSONCalls, 1)
	switch v.Behavior {
	case checkpointProjectionJSONPanic:
		panic("checkpoint digest JSON panic")
	case checkpointProjectionJSONNondeterministic:
		return []byte(fmt.Sprintf(`{"call":%d}`, call)), nil
	default:
		return []byte(`{"value":"stable"}`), nil
	}
}

type checkpointProjectionBinaryInfo struct {
	Visible  string
	Behavior string
	cached   []byte
}

var checkpointProjectionBinaryCalls uint32

func (i *checkpointProjectionBinaryInfo) MarshalBinary() ([]byte, error) {
	atomic.AddUint32(&checkpointProjectionBinaryCalls, 1)
	if i.Behavior == "panic" {
		panic("checkpoint projection BinaryMarshaler must not be called while digesting")
	}
	if i.Behavior == "lazy" && i.cached == nil {
		i.cached = []byte(i.Visible)
	}
	if i.cached == nil {
		return []byte(i.Visible), nil
	}
	return cloneSlice(i.cached), nil
}

func (i *checkpointProjectionBinaryInfo) UnmarshalBinary(data []byte) error {
	i.Visible = string(data)
	i.cached = cloneSlice(data)
	return nil
}

type checkpointProjectionTextInfo struct {
	Visible string
	hidden  string
}

func (i *checkpointProjectionTextInfo) MarshalText() ([]byte, error) {
	return []byte(i.hidden), nil
}

func init() {
	schema.RegisterName[*checkpointProjectionPointerMapState](
		"_eino_adk_test_checkpoint_projection_pointer_map_state")
	schema.RegisterName[*checkpointProjectionNaNMapState](
		"_eino_adk_test_checkpoint_projection_nan_map_state")
	schema.RegisterName[*checkpointProjectionGobNormalizedTailInfo](
		"_eino_adk_test_checkpoint_projection_gob_normalized_tail_info")
	schema.RegisterName[checkpointProjectionGobNormalizedMap](
		"_eino_adk_test_checkpoint_projection_gob_normalized_map")
	schema.RegisterName[*checkpointProjectionGobInfo](
		"_eino_adk_test_checkpoint_projection_gob_info")
	schema.RegisterName[*checkpointProjectionBinaryInfo](
		"_eino_adk_test_checkpoint_projection_binary_info")
	schema.RegisterName[*checkpointProjectionJSONValue](
		"_eino_adk_test_checkpoint_projection_json_value")
	schema.RegisterName[*checkpointProjectionTextInfo](
		"_eino_adk_test_checkpoint_projection_text_info")
	schema.RegisterName[*schema.ToolInfo](
		"_eino_adk_test_checkpoint_projection_tool_info")
}

func (t *checkpointProjectionEnhancedTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name}, nil
}

func (t *checkpointProjectionEnhancedTool) InvokableRun(context.Context,
	*schema.ToolArgument, ...componenttool.Option) (*schema.ToolResult, error) {
	t.mu.Lock()
	t.calls++
	t.mu.Unlock()
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: strings.Repeat("result", 1024),
	}}}, nil
}

func (t *checkpointProjectionEnhancedTool) callCount() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.calls
}

func TestRunnerCheckpointProjectionRoundTrip(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:         "projection-round-trip",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  320 << 10,
	}
	store := newCheckpointCompatStore()
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent: newCheckpointCompatAgent(t, spec.Depth, spec.ParallelChildren,
			spec.PayloadField, spec.PayloadSize),
		CheckPointStore: store,
	})
	iter := runner.Query(context.Background(), "start", WithCheckPointID(spec.Name))
	var original *InterruptInfo
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			original = event.Action.Interrupted
		}
	}
	require.NotNil(t, original)
	originalChatModelInfo, ok := original.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.NotEmpty(t, originalChatModelInfo.Data)
	require.NotEmpty(t, originalChatModelInfo.Info.InterruptContexts)

	raw, exists, err := store.Get(context.Background(), spec.Name)
	require.NoError(t, err)
	require.True(t, exists)
	var persisted serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))
	require.NotNil(t, persisted.ProjectionV1)
	require.Equal(t, checkpointProjectionVersionV2, persisted.ProjectionV1.Version)
	require.Contains(t, persisted.InterruptID2State, runnerProjectionSentinelID)
	require.NotContains(t, persisted.InterruptID2Address, runnerProjectionSentinelID)
	require.NotEmpty(t, persisted.ProjectionV1.RunCtxRefs)
	require.NotEmpty(t, persisted.ProjectionV1.InfoRefs)
	require.Equal(t, 1, persisted.ProjectionV1.InterruptCtxRefCount)
	persistedChatModelInfo, ok := persisted.Info.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, persistedChatModelInfo.Info.InterruptContexts, 1)
	require.Equal(t, persisted.ProjectionV1.InterruptCtxRefCount,
		countInterruptContextRefs(persisted.Info))

	_, runCtx, resumeInfo, err := runnerLoadCheckPointImpl(store, context.Background(), spec.Name)
	require.NoError(t, err)
	require.NotNil(t, runCtx)
	require.NotNil(t, resumeInfo)
	require.Equal(t, original.Data, resumeInfo.InterruptInfo.Data)

	restoredChatModelInfo, ok := resumeInfo.InterruptInfo.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, restoredChatModelInfo.Info.InterruptContexts,
		len(originalChatModelInfo.Info.InterruptContexts))
	for i := range originalChatModelInfo.Info.InterruptContexts {
		want := originalChatModelInfo.Info.InterruptContexts[i]
		got := restoredChatModelInfo.Info.InterruptContexts[i]
		require.Equal(t, want.ID, got.ID)
		require.True(t, want.EqualsWithoutID(got))
	}

	require.Nil(t, persistedChatModelInfo.Data)
	require.NotEmpty(t, originalChatModelInfo.Data,
		"checkpoint projection must not mutate the live interrupt event")
}

func TestRunnerCheckpointProjectionGobNormalizedInlineTailRoundTrip(t *testing.T) {
	const checkpointID = "gob-normalized-inline-tail"
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         checkpointID + "-source",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  128 << 10,
	})
	var source serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&source))
	require.NoError(t, restoreRunnerCheckpointProjection(&source))
	require.NoError(t, restoreRunnerCheckpointInfoData(&source))
	sourceState, exists := source.InterruptID2State[source.InfoDataSourceInterruptID]
	require.True(t, exists)
	sourceData, ok := sourceState.State.([]byte)
	require.True(t, ok)

	index, err := buildCheckpointProjectionIndex(sourceData)
	require.NoError(t, err)
	var target *InterruptCtx
	for _, candidate := range index.allInterruptInfos() {
		for _, interruptCtx := range candidate.contexts {
			if interruptCtx != nil && interruptCtx.Parent != nil {
				target = cloneInterruptContextForProjection(interruptCtx)
				break
			}
		}
		if target != nil {
			break
		}
	}
	require.NotNil(t, target)

	zero := 0
	var nilMap checkpointProjectionGobNormalizedMap
	tailInfo := &checkpointProjectionGobNormalizedTailInfo{
		Marker: "preserved",
		State: State{
			Messages:  make([]*schema.Message, 0),
			AgentName: "tail-agent",
		},
		NegativeZero: math.Copysign(0, -1),
		ZeroPointer:  &zero,
		InterfaceMap: any(nilMap),
		SliceMaps:    []checkpointProjectionGobNormalizedMap{nil},
		ArrayMaps:    [1]checkpointProjectionGobNormalizedMap{nil},
		MapValues: map[string]checkpointProjectionGobNormalizedMap{
			"nil": nil,
		},
	}
	require.NotNil(t, tailInfo.State.Messages)
	require.True(t, math.Signbit(tailInfo.NegativeZero))
	require.NotNil(t, tailInfo.ZeroPointer)
	tail := target
	for tail.Parent != nil {
		tail = tail.Parent
	}
	tail.Parent = &InterruptCtx{
		ID:      "normalized-tail",
		Address: Address{{Type: AddressSegmentAgent, ID: "normalized-tail"}},
		Info:    tailInfo,
	}

	info := &InterruptInfo{
		Data: &ChatModelAgentInterruptInfo{
			Data: sourceData,
			Info: &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{target}},
		},
	}
	ctx := setRunCtx(context.Background(), source.RunCtx)
	signal := &core.InterruptSignal{
		ID:      "source",
		Address: Address{{Type: AddressSegmentAgent, ID: "source"}},
		InterruptInfo: core.InterruptInfo{
			IsRootCause: true,
		},
		InterruptState: core.InterruptState{State: sourceData},
	}
	store := newCheckpointCompatStore()
	require.NoError(t, runnerSaveCheckPointImpl(false, store, ctx, checkpointID, info, signal))

	persistedRaw, exists, err := store.Get(context.Background(), checkpointID)
	require.NoError(t, err)
	require.True(t, exists)
	var persisted serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(persistedRaw)).Decode(&persisted))
	require.NotNil(t, persisted.ProjectionV1)
	require.Equal(t, checkpointProjectionVersionV2, persisted.ProjectionV1.Version)
	require.Equal(t, 1, persisted.ProjectionV1.InterruptCtxRefCount)
	persistedChatModelInfo, ok := persisted.Info.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, persistedChatModelInfo.Info.InterruptContexts, 1)
	projected := persistedChatModelInfo.Info.InterruptContexts[0]
	require.IsType(t, &checkpointInterruptContextPlaceholderV1{}, projected.Info)
	require.NotNil(t, projected.Parent)
	persistedTail := projected.Parent
	for persistedTail.Parent != nil {
		persistedTail = persistedTail.Parent
	}
	persistedTailInfo, ok := persistedTail.Info.(*checkpointProjectionGobNormalizedTailInfo)
	require.True(t, ok)
	require.Equal(t, "preserved", persistedTailInfo.Marker)
	require.Nil(t, persistedTailInfo.State.Messages)
	require.Equal(t, "tail-agent", persistedTailInfo.State.AgentName)
	require.Zero(t, persistedTailInfo.NegativeZero)
	require.False(t, math.Signbit(persistedTailInfo.NegativeZero))
	require.Nil(t, persistedTailInfo.ZeroPointer)
	require.Nil(t, persistedTailInfo.OmittedMap)
	require.NotNil(t, persistedTailInfo.InterfaceMap.(checkpointProjectionGobNormalizedMap))
	require.NotNil(t, persistedTailInfo.SliceMaps[0])
	require.NotNil(t, persistedTailInfo.ArrayMaps[0])
	require.NotNil(t, persistedTailInfo.MapValues["nil"])
	ref := projected.Info.(*checkpointInterruptContextPlaceholderV1)
	require.NotEmpty(t, ref.IntegrityDigest)

	loadedCtx, _, resumeInfo, err := runnerLoadCheckPointImpl(
		store, context.Background(), checkpointID)
	require.NoError(t, err)
	resumedCtx := AppendAddressSegment(loadedCtx, AddressSegmentAgent, "source")
	wasInterrupted, hasState, restoredData := compose.GetInterruptState[[]byte](resumedCtx)
	require.True(t, wasInterrupted)
	require.True(t, hasState)
	require.Equal(t, sourceData, restoredData)

	restoredChatModelInfo, ok := resumeInfo.InterruptInfo.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, restoredChatModelInfo.Info.InterruptContexts, 1)
	restoredTail := restoredChatModelInfo.Info.InterruptContexts[0]
	for restoredTail.Parent != nil {
		restoredTail = restoredTail.Parent
	}
	require.Equal(t, "normalized-tail", restoredTail.ID)
	restoredTailInfo, ok := restoredTail.Info.(*checkpointProjectionGobNormalizedTailInfo)
	require.True(t, ok)
	require.Equal(t, "preserved", restoredTailInfo.Marker)
	require.Nil(t, restoredTailInfo.State.Messages)
	require.Equal(t, "tail-agent", restoredTailInfo.State.AgentName)
	require.Zero(t, restoredTailInfo.NegativeZero)
	require.False(t, math.Signbit(restoredTailInfo.NegativeZero))
	require.Nil(t, restoredTailInfo.ZeroPointer)
	require.Nil(t, restoredTailInfo.OmittedMap)
	require.NotNil(t, restoredTailInfo.InterfaceMap.(checkpointProjectionGobNormalizedMap))
	require.NotNil(t, restoredTailInfo.SliceMaps[0])
	require.NotNil(t, restoredTailInfo.ArrayMaps[0])
	require.NotNil(t, restoredTailInfo.MapValues["nil"])
	requireNoCheckpointProjectionPlaceholders(t, restoredChatModelInfo.Info)
}

func TestRunnerCheckpointProjectionCustomInterruptIDRoundTrip(t *testing.T) {
	const (
		checkpointID = "custom-interrupt-id"
		interruptID  = "_eino_custom"
	)
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         checkpointID + "-source",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  128 << 10,
	})
	var source serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&source))
	require.NoError(t, restoreRunnerCheckpointProjection(&source))
	require.NoError(t, restoreRunnerCheckpointInfoData(&source))
	sourceState, exists := source.InterruptID2State[source.InfoDataSourceInterruptID]
	require.True(t, exists)
	sourceData, ok := sourceState.State.([]byte)
	require.True(t, ok)

	address := Address{{Type: AddressSegmentAgent, ID: "custom"}}
	signal := &core.InterruptSignal{
		ID:      interruptID,
		Address: address,
		InterruptInfo: core.InterruptInfo{
			IsRootCause: true,
		},
		InterruptState: core.InterruptState{State: sourceData},
	}
	ctx := setRunCtx(context.Background(), source.RunCtx)
	store := newCheckpointCompatStore()
	require.NoError(t, runnerSaveCheckPointImpl(false, store, ctx, checkpointID, source.Info, signal))

	persistedRaw, exists, err := store.Get(context.Background(), checkpointID)
	require.NoError(t, err)
	require.True(t, exists)
	var persisted serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(persistedRaw)).Decode(&persisted))
	require.NotNil(t, persisted.ProjectionV1)
	require.Equal(t, interruptID, persisted.ProjectionV1.SourceInterruptID)
	require.Contains(t, persisted.InterruptID2State, interruptID)
	require.Contains(t, persisted.InterruptID2State, runnerProjectionSentinelID)

	loadedCtx, _, _, err := runnerLoadCheckPointImpl(store, context.Background(), checkpointID)
	require.NoError(t, err)
	resumedCtx := AppendAddressSegment(loadedCtx, AddressSegmentAgent, "custom")
	wasInterrupted, hasState, restoredData := compose.GetInterruptState[[]byte](resumedCtx)
	require.True(t, wasInterrupted)
	require.True(t, hasState)
	require.Equal(t, sourceData, restoredData)
}

func TestRunnerCheckpointProjectionKeepsStateScopedToolCallsInline(t *testing.T) {
	const checkpointID = "state-scoped-tool-calls"
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         checkpointID + "-source",
		PayloadField: "tool_arguments",
		PayloadSize:  128 << 10,
	})
	var source serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&source))
	require.NotNil(t, source.ProjectionV1)
	sourceID := source.ProjectionV1.SourceInterruptID
	require.NoError(t, restoreRunnerCheckpointProjection(&source))
	sourceData, ok := source.InterruptID2State[sourceID].State.([]byte)
	require.True(t, ok)

	var canonical *schema.Message
	require.NoError(t, compose.WalkCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if location.Kind != compose.CheckpointValueState {
				return nil
			}
			state, stateOK := value.(*State)
			if !stateOK {
				return nil
			}
			for _, message := range state.Messages {
				if message != nil && len(message.ToolCalls) > 0 {
					canonical = message
					return nil
				}
			}
			return nil
		}))
	require.NotNil(t, canonical)
	require.NotEmpty(t, GetMessageID(canonical))

	stateExtra := &compose.ToolsInterruptAndRerunExtra{
		ToolCalls:             append([]schema.ToolCall(nil), canonical.ToolCalls...),
		ExecutedTools:         map[string]string{},
		ExecutedEnhancedTools: map[string]*schema.ToolResult{},
		RerunExtraMap:         map[string]any{},
	}
	ctx := setRunCtx(context.Background(), &runContext{
		RootInput: &AgentInput{Messages: []*schema.Message{canonical}},
	})
	signal := StatefulInterrupt(ctx, "source", sourceData).Action.internalInterrupted
	store := newCheckpointCompatStore()
	require.NoError(t, runnerSaveCheckPointImpl(false, store, ctx, checkpointID, &InterruptInfo{
		Data: &ChatModelAgentInterruptInfo{
			Data: sourceData,
			Info: &compose.InterruptInfo{State: stateExtra},
		},
	}, signal))

	persistedRaw, exists, err := store.Get(context.Background(), checkpointID)
	require.NoError(t, err)
	require.True(t, exists)
	var persisted serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(persistedRaw)).Decode(&persisted))
	require.NotNil(t, persisted.ProjectionV1)
	require.NotEmpty(t, persisted.ProjectionV1.RunCtxRefs)
	require.Empty(t, persisted.ProjectionV1.InfoRefs)
	require.NoError(t, validateInfoProjectionRefs(
		persisted.ProjectionV1.InfoRefs, persisted.ProjectionV1.InfoRefCount))

	_, restoredRunCtx, resumeInfo, err := runnerLoadCheckPointImpl(
		store, context.Background(), checkpointID)
	require.NoError(t, err)
	require.Equal(t, []*schema.Message{canonical}, restoredRunCtx.RootInput.Messages)
	restoredChatModelInfo, ok := resumeInfo.InterruptInfo.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	restoredExtra, ok := restoredChatModelInfo.Info.State.(*compose.ToolsInterruptAndRerunExtra)
	require.True(t, ok)
	require.Equal(t, stateExtra, restoredExtra)
}

func TestRunnerCheckpointProjectionProfitability(t *testing.T) {
	tests := []struct {
		name           string
		payloadSize    int
		wantProjection bool
	}{
		{name: "small", payloadSize: 64},
		{name: "large", payloadSize: 320 << 10, wantProjection: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := checkpointCompatFixture{
				Name:         "projection-profitability-" + tt.name,
				Depth:        1,
				PayloadField: "content",
				PayloadSize:  tt.payloadSize,
			}
			raw, interruptIDs, _ := captureCheckpointCompatFixture(t, spec)
			var persisted serialization
			require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))

			var projectedData, unprojectedData []byte
			projectionPersisted := persisted.ProjectionV1 != nil
			if tt.wantProjection {
				require.True(t, projectionPersisted)
			}
			if projectionPersisted {
				require.Contains(t, persisted.InterruptID2State, runnerProjectionSentinelID)
				projectedData = raw

				require.NoError(t, restoreRunnerCheckpointProjection(&persisted))
				persisted.ProjectionV1 = nil
				var err error
				unprojectedData, err = encodeRunnerCheckpoint(&persisted)
				require.NoError(t, err)

				require.Less(t, len(projectedData), len(unprojectedData))
				require.Less(t, len(projectedData), 1<<20,
					"large payload checkpoint must retain its linear size bound")
			} else {
				require.Nil(t, persisted.ProjectionV1)
				require.NotContains(t, persisted.InterruptID2State, runnerProjectionSentinelID)
				unprojectedData = raw

				projectedRunCtx, projectedInfo, projectedStates, projection, err :=
					projectRunnerCheckpoint(persisted.RunCtx, persisted.Info,
						persisted.InfoDataSourceInterruptID, persisted.InterruptID2State)
				require.NoError(t, err)
				require.NotNil(t, projection, "small fixture must exercise the profitability gate")
				projectedData, err = encodeRunnerCheckpoint(&serialization{
					RunCtx:                    projectedRunCtx,
					Info:                      projectedInfo,
					InfoDataSourceInterruptID: persisted.InfoDataSourceInterruptID,
					ProjectionV1:              projection,
					EnableStreaming:           persisted.EnableStreaming,
					InterruptID2Address:       persisted.InterruptID2Address,
					InterruptID2State:         projectedStates,
				})
				require.NoError(t, err)
				require.LessOrEqual(t, len(unprojectedData), len(projectedData),
					"projection must not enlarge the persisted checkpoint")
			}
			t.Logf("payload=%d unprojected=%d projected=%d projection_persisted=%t",
				tt.payloadSize, len(unprojectedData), len(projectedData), projectionPersisted)

			projectedMessages := resumeCheckpointProjectionFixture(
				t, spec, projectedData, interruptIDs)
			unprojectedMessages := resumeCheckpointProjectionFixture(
				t, spec, unprojectedData, interruptIDs)
			require.Equal(t, unprojectedMessages, projectedMessages)
			require.NotEmpty(t, projectedMessages)
			require.Equal(t, "completed", projectedMessages[len(projectedMessages)-1].Content)
		})
	}
}

func resumeCheckpointProjectionFixture(t *testing.T, spec checkpointCompatFixture,
	raw []byte, interruptIDs []string) []*schema.Message {
	t.Helper()
	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent: newCheckpointCompatAgent(t, spec.Depth, spec.ParallelChildren,
			spec.PayloadField, spec.PayloadSize),
		CheckPointStore: store,
	})
	targets := make(map[string]any, len(interruptIDs))
	for _, id := range interruptIDs {
		targets[id] = "resumed"
	}
	iter, err := runner.ResumeWithParams(context.Background(), spec.Name,
		&ResumeParams{Targets: targets})
	require.NoError(t, err)

	var messages []*schema.Message
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		require.True(t, event.Action == nil || event.Action.Interrupted == nil,
			"fully resumed checkpoint must not interrupt again")
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, messageErr := event.Output.MessageOutput.GetMessage()
		require.NoError(t, messageErr)
		if message != nil {
			typedSetMessageID(message, "")
			messages = append(messages, message)
		}
	}
	return messages
}

func TestRunnerCheckpointProjectionMetadataValidation(t *testing.T) {
	valid := func() *serialization {
		return &serialization{
			ProjectionV1: &checkpointProjectionV1{
				Version:           checkpointProjectionVersion,
				SourceInterruptID: "source",
			},
			InterruptID2Address: map[string]Address{},
			InterruptID2State: map[string]core.InterruptState{
				"source": {State: []byte("not a checkpoint")},
				runnerProjectionSentinelID: {
					State: &runnerProjectionSentinelV1{Version: checkpointProjectionVersion},
				},
			},
		}
	}

	t.Run("legacy", func(t *testing.T) {
		require.NoError(t, validateRunnerProjectionMetadata(&serialization{}))
	})
	t.Run("current", func(t *testing.T) {
		require.NoError(t, validateRunnerProjectionMetadata(valid()))
	})
	t.Run("v1", func(t *testing.T) {
		state := valid()
		state.ProjectionV1.Version = checkpointProjectionVersionV1
		state.InterruptID2State[runnerProjectionSentinelID] = core.InterruptState{
			State: &runnerProjectionSentinelV1{Version: checkpointProjectionVersionV1},
		}
		require.NoError(t, validateRunnerProjectionMetadata(state))
	})
	t.Run("sentinel_without_metadata", func(t *testing.T) {
		state := valid()
		state.ProjectionV1 = nil
		require.EqualError(t, validateRunnerProjectionMetadata(state),
			"failed to decode checkpoint projection: metadata is missing")
	})
	t.Run("metadata_without_sentinel", func(t *testing.T) {
		state := valid()
		delete(state.InterruptID2State, runnerProjectionSentinelID)
		require.EqualError(t, validateRunnerProjectionMetadata(state),
			"failed to decode checkpoint projection: sentinel is missing")
	})
	t.Run("unsupported_version", func(t *testing.T) {
		state := valid()
		state.ProjectionV1.Version++
		require.EqualError(t, validateRunnerProjectionMetadata(state),
			"checkpoint requires a newer Eino version: unsupported projection version 3")
	})
	t.Run("sentinel_structure", func(t *testing.T) {
		tests := []struct {
			name   string
			mutate func(*serialization)
			want   string
		}{
			{
				name: "address_with_projection",
				mutate: func(state *serialization) {
					state.InterruptID2Address[runnerProjectionSentinelID] = Address{}
				},
				want: "failed to decode checkpoint projection: sentinel must not have a routing address",
			},
			{
				name: "address_without_projection",
				mutate: func(state *serialization) {
					state.ProjectionV1 = nil
					delete(state.InterruptID2State, runnerProjectionSentinelID)
					state.InterruptID2Address[runnerProjectionSentinelID] = Address{}
				},
				want: "failed to decode checkpoint projection: sentinel must not have a routing address",
			},
			{
				name: "payload_with_projection",
				mutate: func(state *serialization) {
					sentinel := state.InterruptID2State[runnerProjectionSentinelID]
					sentinel.LayerSpecificPayload = "payload"
					state.InterruptID2State[runnerProjectionSentinelID] = sentinel
				},
				want: "failed to decode checkpoint projection: sentinel must not have a layer-specific payload",
			},
			{
				name: "payload_without_projection",
				mutate: func(state *serialization) {
					state.ProjectionV1 = nil
					sentinel := state.InterruptID2State[runnerProjectionSentinelID]
					sentinel.LayerSpecificPayload = "payload"
					state.InterruptID2State[runnerProjectionSentinelID] = sentinel
				},
				want: "failed to decode checkpoint projection: sentinel must not have a layer-specific payload",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				state := valid()
				tt.mutate(state)
				require.EqualError(t, validateRunnerProjectionMetadata(state), tt.want)
			})
		}
	})
	t.Run("invalid_sentinel", func(t *testing.T) {
		state := valid()
		state.InterruptID2State[runnerProjectionSentinelID] = core.InterruptState{State: "invalid"}
		require.EqualError(t, validateRunnerProjectionMetadata(state),
			"failed to decode checkpoint projection: invalid sentinel string")
	})
	t.Run("missing_source", func(t *testing.T) {
		state := valid()
		delete(state.InterruptID2State, "source")
		require.EqualError(t, restoreRunnerCheckpointProjection(state),
			`failed to decode checkpoint projection: source interrupt state "source" is missing`)
	})
	t.Run("invalid_source_type", func(t *testing.T) {
		state := valid()
		state.InterruptID2State["source"] = core.InterruptState{State: "invalid"}
		require.EqualError(t, restoreRunnerCheckpointProjection(state),
			`failed to decode checkpoint projection: source interrupt state "source" has invalid type string`)
	})
	t.Run("malformed_source_bytes", func(t *testing.T) {
		require.ErrorContains(t, restoreRunnerCheckpointProjection(valid()),
			"failed to decode checkpoint projection source")
	})
	t.Run("interrupt_context_reference_count", func(t *testing.T) {
		state := valid()
		state.ProjectionV1.InterruptCtxRefCount = 1
		require.EqualError(t, restoreRunnerCheckpointProjection(state),
			"failed to decode checkpoint projection: reference count mismatch")
	})
	t.Run("custom_prefixed_interrupt_id", func(t *testing.T) {
		require.NoError(t, validateRunnerProjectionReservedIDs(
			map[string]Address{"_eino_custom": {}},
			map[string]core.InterruptState{"_eino_custom": {}}))
	})
	t.Run("projection_sentinel_interrupt_id", func(t *testing.T) {
		require.EqualError(t, validateRunnerProjectionReservedIDs(
			map[string]Address{
				"_eino_custom":             {},
				runnerProjectionSentinelID: {},
			}, nil),
			`interrupt ID "_eino_runner_projection" is reserved for checkpoint metadata`)
		require.EqualError(t, validateRunnerProjectionReservedIDs(nil,
			map[string]core.InterruptState{
				"_eino_custom":             {},
				runnerProjectionSentinelID: {},
			}),
			`interrupt ID "_eino_runner_projection" is reserved for checkpoint metadata`)
	})
}

func TestInterruptContextProjectionValidation(t *testing.T) {
	sourceTail := &InterruptCtx{
		ID:      "source-tail",
		Address: Address{{Type: AddressSegmentAgent, ID: "source"}},
		Info:    "source-tail",
	}
	source := &InterruptCtx{
		ID:          "source-root",
		Address:     Address{{Type: AddressSegmentTool, ID: "tool"}},
		Info:        "source-root",
		IsRootCause: true,
		Parent:      sourceTail,
	}
	index := &checkpointProjectionIndex{
		version: checkpointProjectionVersionV1,
		interruptInfos: []canonicalCheckpointInterruptInfo{{
			path:     []string{"child"},
			contexts: []*InterruptCtx{source},
		}},
	}
	reference := func() *InterruptCtx {
		return &InterruptCtx{Info: &checkpointInterruptContextPlaceholderV1{
			RunnerPath:   []string{"child"},
			ContextIndex: 0,
			PrefixLength: 2,
		}}
	}

	t.Run("legacy_v1_digest_free", func(t *testing.T) {
		restored, err := hydrateInterruptContextPrefix(reference(), index)
		require.NoError(t, err)
		require.Equal(t, source, restored)
		require.NotSame(t, source, restored)
		require.NotSame(t, source.Parent, restored.Parent)
		require.Nil(t, restored.Parent.Parent)
	})
	t.Run("v2_ordinal", func(t *testing.T) {
		candidate := canonicalCheckpointInterruptInfo{
			sourceOrdinal: 7,
			sourceID:      "source",
			contexts:      []*InterruptCtx{source},
		}
		sourceID, digest, ok := checkpointInterruptContextSourceMetadata(candidate, 0)
		require.True(t, ok)
		v2Index := &checkpointProjectionIndex{
			version: checkpointProjectionVersionV2,
			interruptInfosByOrdinal: map[int]canonicalCheckpointInterruptInfo{
				7: candidate,
			},
		}
		ref := reference()
		placeholder := ref.Info.(*checkpointInterruptContextPlaceholderV1)
		placeholder.SourceOrdinal = 7
		placeholder.RunnerPath = nil
		placeholder.SourceID = sourceID
		placeholder.Digest = digest
		sealInterruptContextReference(t, placeholder, ref.Parent)
		restored, err := hydrateInterruptContextPrefix(ref, v2Index)
		require.NoError(t, err)
		require.Equal(t, source, restored)
		require.NotSame(t, source, restored)
	})
	t.Run("v2_ordinal_cannot_be_cross_wired", func(t *testing.T) {
		other := cloneInterruptContextForProjection(source)
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "other",
			contexts: []*InterruptCtx{other},
		})
		projected, ok := v2Index.projectInterruptContextPrefix(source)
		require.True(t, ok)
		placeholder := projected.Info.(*checkpointInterruptContextPlaceholderV1)
		require.Equal(t, 1, placeholder.SourceOrdinal)
		sealInterruptContextReference(t, placeholder, projected.Parent)
		placeholder.SourceOrdinal = 2

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context reference does not match integrity metadata")
	})
	t.Run("v2_digest_cannot_be_changed", func(t *testing.T) {
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		projected, ok := v2Index.projectInterruptContextPrefix(source)
		require.True(t, ok)
		sealInterruptContextReference(t,
			projected.Info.(*checkpointInterruptContextPlaceholderV1), projected.Parent)
		projected.Info.(*checkpointInterruptContextPlaceholderV1).Digest = "corrupt"

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context reference does not match integrity metadata")
	})
	t.Run("v2_binding_cannot_hide_wrong_source", func(t *testing.T) {
		other := cloneInterruptContextForProjection(source)
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "other",
			contexts: []*InterruptCtx{other},
		})
		projected, ok := v2Index.projectInterruptContextPrefix(source)
		require.True(t, ok)
		placeholder := projected.Info.(*checkpointInterruptContextPlaceholderV1)
		placeholder.SourceOrdinal = 2
		sealInterruptContextReference(t, placeholder, projected.Parent)

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source does not match metadata")
	})
	t.Run("v2_reference_tail_link_deletion_fails_closed", func(t *testing.T) {
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		target := prependInterruptContextAddresses([]*InterruptCtx{source}, Address{{
			Type:  AddressSegmentTool,
			ID:    "SameChild",
			SubID: "call-a",
		}})[0]
		target.Parent.Parent = &InterruptCtx{
			ID:      "tail",
			Address: Address{{Type: AddressSegmentAgent, ID: "tail"}},
			Info:    "tail",
		}
		projected, ok := v2Index.projectInterruptContextPrefix(target)
		require.True(t, ok)
		require.NotNil(t, projected.Parent)
		sealInterruptContextReference(t,
			projected.Info.(*checkpointInterruptContextPlaceholderV1), projected.Parent)
		projected.Parent = nil

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context reference does not match integrity metadata")
	})
	t.Run("v2_source_prefix_link_deletion_fails_closed", func(t *testing.T) {
		sourceCopy := cloneInterruptContextForProjection(source)
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{sourceCopy},
		})
		target := prependInterruptContextAddresses([]*InterruptCtx{sourceCopy}, Address{{
			Type:  AddressSegmentTool,
			ID:    "SameChild",
			SubID: "call-a",
		}})[0]
		projected, ok := v2Index.projectInterruptContextPrefix(target)
		require.True(t, ok)
		sealInterruptContextReference(t,
			projected.Info.(*checkpointInterruptContextPlaceholderV1), projected.Parent)
		sourceCopy.Parent = nil

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source does not match metadata")
	})
	t.Run("v2_complete_address_substitution_fails_closed", func(t *testing.T) {
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		target := prependInterruptContextAddresses([]*InterruptCtx{source}, Address{{
			Type:  AddressSegmentTool,
			ID:    "SameChild",
			SubID: "call-a",
		}})[0]
		projected, ok := v2Index.projectInterruptContextPrefix(target)
		require.True(t, ok)
		placeholder := projected.Info.(*checkpointInterruptContextPlaceholderV1)
		sealInterruptContextReference(t, placeholder, projected.Parent)
		require.Equal(t, "call-a", placeholder.AddressPrefix[0].SubID)
		placeholder.AddressPrefix[0].SubID = "call-b"

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context reference does not match integrity metadata")
	})
	t.Run("tail_info", func(t *testing.T) {
		for _, mutate := range []struct {
			name string
			node func(*InterruptCtx) *InterruptCtx
		}{
			{name: "first", node: func(tail *InterruptCtx) *InterruptCtx { return tail }},
			{name: "second", node: func(tail *InterruptCtx) *InterruptCtx { return tail.Parent }},
		} {
			t.Run(mutate.name, func(t *testing.T) {
				v2Index := newCheckpointProjectionIndex(
					checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
				v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
					sourceID: "source",
					contexts: []*InterruptCtx{source},
				})
				target := cloneInterruptContextForProjection(source)
				target.Parent.Parent = &InterruptCtx{
					ID:      "tail-a",
					Address: Address{{Type: AddressSegmentAgent, ID: "tail-a"}},
					Info:    map[string]any{"value": "a"},
					Parent: &InterruptCtx{
						ID:      "tail-b",
						Address: Address{{Type: AddressSegmentAgent, ID: "tail-b"}},
						Info:    map[string]any{"value": "b"},
					},
				}
				projected, ok := v2Index.projectInterruptContextPrefix(target)
				require.True(t, ok)
				placeholder := projected.Info.(*checkpointInterruptContextPlaceholderV1)
				sealInterruptContextReference(t, placeholder, projected.Parent)

				mutate.node(projected.Parent).Info.(map[string]any)["value"] = "tampered"

				_, err := hydrateInterruptContextPrefix(projected, v2Index)
				require.EqualError(t, err,
					"checkpoint projection interrupt context reference does not match integrity metadata")
			})
		}
	})
	t.Run("unsummarizable_tail_info_stays_inline", func(t *testing.T) {
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		target := cloneInterruptContextForProjection(source)
		target.Parent.Parent = &InterruptCtx{
			ID:      "tail",
			Address: Address{{Type: AddressSegmentAgent, ID: "tail"}},
			Info:    make(chan int),
		}

		projected, ok := v2Index.projectInterruptContextPrefix(target)
		require.False(t, ok)
		require.Same(t, target, projected)
		require.IsType(t, make(chan int), projected.Parent.Parent.Info)
	})
	t.Run("v2_reconstruction_metadata_is_bound", func(t *testing.T) {
		mutations := []struct {
			name   string
			mutate func(*checkpointInterruptContextPlaceholderV1)
		}{
			{
				name: "context_index",
				mutate: func(ref *checkpointInterruptContextPlaceholderV1) {
					ref.ContextIndex++
				},
			},
			{
				name: "prefix_length",
				mutate: func(ref *checkpointInterruptContextPlaceholderV1) {
					ref.PrefixLength--
				},
			},
			{
				name: "address_type",
				mutate: func(ref *checkpointInterruptContextPlaceholderV1) {
					ref.AddressPrefix[0].Type = AddressSegmentAgent
				},
			},
			{
				name: "address_id",
				mutate: func(ref *checkpointInterruptContextPlaceholderV1) {
					ref.AddressPrefix[0].ID = "OtherChild"
				},
			},
			{
				name: "address_sub_id",
				mutate: func(ref *checkpointInterruptContextPlaceholderV1) {
					ref.AddressPrefix[0].SubID = "call-b"
				},
			},
		}
		for _, tt := range mutations {
			t.Run(tt.name, func(t *testing.T) {
				v2Index := newCheckpointProjectionIndex(
					checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
				v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
					sourceID: "source",
					contexts: []*InterruptCtx{source},
				})
				target := prependInterruptContextAddresses([]*InterruptCtx{source}, Address{{
					Type:  AddressSegmentTool,
					ID:    "SameChild",
					SubID: "call-a",
				}})[0]
				projected, ok := v2Index.projectInterruptContextPrefix(target)
				require.True(t, ok)
				placeholder := projected.Info.(*checkpointInterruptContextPlaceholderV1)
				sealInterruptContextReference(t, placeholder, projected.Parent)
				tt.mutate(placeholder)

				_, err := hydrateInterruptContextPrefix(projected, v2Index)
				require.EqualError(t, err,
					"checkpoint projection interrupt context reference does not match integrity metadata")
			})
		}
	})
	t.Run("v2_requires_integrity_metadata", func(t *testing.T) {
		v2Index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		v2Index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		projected, ok := v2Index.projectInterruptContextPrefix(source)
		require.True(t, ok)
		projected.Info.(*checkpointInterruptContextPlaceholderV1).IntegrityDigest = ""

		_, err := hydrateInterruptContextPrefix(projected, v2Index)
		require.EqualError(t, err,
			"checkpoint projection V2 interrupt context integrity metadata is incomplete")
	})
	t.Run("v1_source_metadata", func(t *testing.T) {
		other := cloneInterruptContextForProjection(source)
		candidate := canonicalCheckpointInterruptInfo{
			sourceID: "source",
			path:     []string{"child"},
			contexts: []*InterruptCtx{source},
		}
		sourceID, digest, ok := checkpointInterruptContextSourceMetadata(candidate, 0)
		require.True(t, ok)
		v1Index := &checkpointProjectionIndex{
			version: checkpointProjectionVersionV1,
			interruptInfos: []canonicalCheckpointInterruptInfo{
				candidate,
				{sourceID: "other", path: []string{"other"}, contexts: []*InterruptCtx{other}},
			},
		}
		ref := reference()
		placeholder := ref.Info.(*checkpointInterruptContextPlaceholderV1)
		placeholder.SourceID = sourceID
		placeholder.Digest = digest
		restored, err := hydrateInterruptContextPrefix(ref, v1Index)
		require.NoError(t, err)
		require.Equal(t, source, restored)

		placeholder.RunnerPath = []string{"other"}
		_, err = hydrateInterruptContextPrefix(ref, v1Index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source does not match metadata")
	})
	t.Run("source_metadata_must_be_complete", func(t *testing.T) {
		for _, field := range []string{"source_id", "digest"} {
			t.Run(field, func(t *testing.T) {
				ref := reference()
				placeholder := ref.Info.(*checkpointInterruptContextPlaceholderV1)
				if field == "source_id" {
					placeholder.SourceID = "source"
				} else {
					placeholder.Digest = "digest"
				}
				_, err := hydrateInterruptContextPrefix(ref, index)
				require.EqualError(t, err,
					"checkpoint projection interrupt context source metadata is incomplete")
			})
		}
	})
	t.Run("nil_and_inline", func(t *testing.T) {
		restored, err := hydrateInterruptContextPrefix(nil, index)
		require.NoError(t, err)
		require.Nil(t, restored)

		inline := &InterruptCtx{ID: "inline"}
		restored, err = hydrateInterruptContextPrefix(inline, index)
		require.NoError(t, err)
		require.Same(t, inline, restored)
	})
	t.Run("invalid_reference", func(t *testing.T) {
		_, err := hydrateInterruptContextPrefix(&InterruptCtx{
			Info: &checkpointInterruptContextPlaceholderV1{},
		}, index)
		require.EqualError(t, err,
			"checkpoint projection has invalid interrupt context reference")
	})
	t.Run("inline_data", func(t *testing.T) {
		ref := reference()
		ref.ID = "unexpected"
		_, err := hydrateInterruptContextPrefix(ref, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context reference has inline data")
	})
	t.Run("missing_source", func(t *testing.T) {
		ref := reference()
		ref.Info.(*checkpointInterruptContextPlaceholderV1).RunnerPath = []string{"missing"}
		_, err := hydrateInterruptContextPrefix(ref, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source path [missing] is missing")
	})
	t.Run("invalid_source_index", func(t *testing.T) {
		ref := reference()
		ref.Info.(*checkpointInterruptContextPlaceholderV1).ContextIndex = 1
		_, err := hydrateInterruptContextPrefix(ref, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source index 1 is invalid")
	})
	t.Run("source_too_short", func(t *testing.T) {
		ref := reference()
		ref.Info.(*checkpointInterruptContextPlaceholderV1).PrefixLength = 3
		_, err := hydrateInterruptContextPrefix(ref, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source is shorter than its prefix")
	})
	t.Run("ambiguous_legacy_path", func(t *testing.T) {
		ambiguous := *index
		ambiguous.interruptInfos = append(
			cloneSlice(index.interruptInfos), index.interruptInfos[0])
		_, err := hydrateInterruptContextPrefix(reference(), &ambiguous)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source path [child] is ambiguous")
	})
	t.Run("multiple_coordinates", func(t *testing.T) {
		ref := reference()
		ref.Info.(*checkpointInterruptContextPlaceholderV1).SourceOrdinal = 1
		_, err := hydrateInterruptContextPrefix(ref, index)
		require.EqualError(t, err,
			"checkpoint projection V1 interrupt context source uses an ordinal")
	})
	t.Run("negative_ordinal", func(t *testing.T) {
		ref := reference()
		ref.Info.(*checkpointInterruptContextPlaceholderV1).SourceOrdinal = -1
		_, err := hydrateInterruptContextPrefix(ref, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context source ordinal is negative")
	})
	t.Run("v2_requires_ordinal_without_path", func(t *testing.T) {
		v2Index := &checkpointProjectionIndex{version: checkpointProjectionVersionV2}
		ref := reference()
		_, err := hydrateInterruptContextPrefix(ref, v2Index)
		require.EqualError(t, err,
			"checkpoint projection V2 interrupt context source contains V1 metadata")

		placeholder := ref.Info.(*checkpointInterruptContextPlaceholderV1)
		placeholder.RunnerPath = nil
		_, err = hydrateInterruptContextPrefix(ref, v2Index)
		require.EqualError(t, err,
			"checkpoint projection V2 interrupt context source metadata is incomplete")
	})
	t.Run("non_head_reference", func(t *testing.T) {
		info := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{
			ID: "head",
			Parent: &InterruptCtx{Info: &checkpointInterruptContextPlaceholderV1{
				RunnerPath:   []string{"child"},
				ContextIndex: 0,
				PrefixLength: 1,
			}},
		}}}
		require.EqualError(t, hydrateNestedInterruptInfoPlaceholders(info, index),
			"checkpoint projection interrupt context reference must be at the chain head")
	})
}

func TestHydrateInterruptInfoReferencesUsesCompactCoordinates(t *testing.T) {
	message := schema.AssistantMessage("", []schema.ToolCall{{
		ID: "call",
		Function: schema.FunctionCall{
			Name:      "tool",
			Arguments: `{}`,
		},
	}})
	typedSetMessageID(message, "message")
	index := newCheckpointProjectionIndex(
		checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
	index.addSchemaMessage(nil, 0, message)
	messageSource, ok := index.sourceForSchemaMessage(message)
	require.True(t, ok)
	index.addCheckpointToolResults(nil, "interrupt",
		&checkpointinternal.ToolsNodeInterruptStateV1{
			ExecutedTools: map[string]string{"call": "result"},
		})
	resultSource, ok := index.sourceForStandardToolResult("call", "result")
	require.True(t, ok)
	sourceContext := &InterruptCtx{
		ID:      "source-head",
		Address: Address{{Type: AddressSegmentTool, ID: "source-head"}},
		Parent: &InterruptCtx{
			ID:      "source-parent",
			Address: Address{{Type: AddressSegmentAgent, ID: "source-parent"}},
		},
	}
	index.addInterruptInfo(canonicalCheckpointInterruptInfo{
		contexts: []*InterruptCtx{sourceContext},
	})
	contextSource := index.interruptInfosByOrdinal[index.nextSourceOrdinal]
	contextSourceID, contextDigest, ok := checkpointInterruptContextSourceMetadata(
		contextSource, 0)
	require.True(t, ok)

	extra := &compose.ToolsInterruptAndRerunExtra{}
	compact := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{
		Info: &checkpointInterruptContextPlaceholderV1{
			SourceOrdinal: index.nextSourceOrdinal,
			SourceID:      contextSourceID,
			Digest:        contextDigest,
			ContextIndex:  0,
			PrefixLength:  2,
		},
		Parent: &InterruptCtx{
			ID:      "tail",
			Address: Address{{Type: AddressSegmentAgent, ID: "tail"}},
			Info:    extra,
		},
	}}}
	sealInterruptContextReference(t,
		compact.InterruptContexts[0].Info.(*checkpointInterruptContextPlaceholderV1),
		compact.InterruptContexts[0].Parent)
	info := &InterruptInfo{Data: &ChatModelAgentInterruptInfo{Info: compact}}
	messageRefs := []infoMessageProjectionV1{{
		Target:       infoTargetContextToolCalls,
		ContextIndex: 0,
		ParentDepth:  1,
		MessageIndex: -1,
		Source:       messageSource,
	}}
	resultRefs := []infoToolResultProjectionV1{{
		Target:       infoTargetContextToolResult,
		ContextIndex: 0,
		ParentDepth:  1,
		ToolCallID:   "call",
		Source:       resultSource,
	}}

	require.NoError(t, validateInterruptInfoContextReferences(info, index))
	require.NoError(t, hydrateInterruptInfoMessages(info, messageRefs, 1, index))
	require.NoError(t, hydrateInterruptInfoToolResults(info, resultRefs, 1, index))
	require.NoError(t, hydrateInterruptInfoContextPrefixesAfterValidation(info, index))

	tail, err := interruptContextAt(compact, 0, 2)
	require.NoError(t, err)
	restored, ok := tail.Info.(*compose.ToolsInterruptAndRerunExtra)
	require.True(t, ok)
	require.Equal(t, message.ToolCalls, restored.ToolCalls)
	require.Equal(t, "result", restored.ExecutedTools["call"])
}

func TestCheckpointProjectionIndexLazyLookup(t *testing.T) {
	const depth = 8
	traversal := newCheckpointProjectionTraversal()
	leaf := newCheckpointProjectionIndex(checkpointProjectionVersionV2, traversal)

	first := schema.UserMessage("first")
	typedSetMessageID(first, "first")
	leaf.addSchemaMessage([]string{"leaf"}, 0, first)
	leaf.addCheckpointToolResults([]string{"leaf"}, "interrupt",
		&checkpointinternal.ToolsNodeInterruptStateV1{
			ExecutedTools: map[string]string{"call": "result"},
		})
	leaf.addInterruptInfo(canonicalCheckpointInterruptInfo{
		sourceID: "context",
		path:     []string{"leaf"},
		contexts: []*InterruptCtx{{ID: "context"}},
	})
	leaf.nextOrdinal()
	last := schema.UserMessage("last")
	typedSetMessageID(last, "last")
	leaf.addSchemaMessage([]string{"leaf"}, 1, last)

	index := leaf
	expectedPath := []string{"leaf"}
	for level := depth - 1; level >= 0; level-- {
		parent := newCheckpointProjectionIndex(checkpointProjectionVersionV2, traversal)
		parent.nextOrdinal()
		prefix := []string{fmt.Sprintf("@interrupt:%d", level)}
		parent.importIndex(index, prefix)
		index = parent
		expectedPath = append(prefix, expectedPath...)
	}
	require.Empty(t, index.byID)
	require.Empty(t, index.toolResultsByCallID)
	require.True(t, index.hasMessagesOrToolResults())

	requireMessage := func(ordinal int, want *schema.Message) {
		t.Helper()
		candidate, ok := index.messageByOrdinal(ordinal)
		require.True(t, ok)
		require.Equal(t, want, candidate.message)
		require.Equal(t, ordinal, candidate.source.SourceOrdinal)
		require.Equal(t, depth, candidate.source.AgentToolDepth)
		require.Equal(t, expectedPath, candidate.source.GraphPath)
	}

	t.Run("imported_range_first_and_last", func(t *testing.T) {
		requireMessage(depth+1, first)
		requireMessage(depth+5, last)
	})
	t.Run("holes_and_out_of_range", func(t *testing.T) {
		for _, ordinal := range []int{depth + 2, depth + 3, depth + 4} {
			_, ok := index.messageByOrdinal(ordinal)
			require.False(t, ok)
		}
		for _, ordinal := range []int{-1, 0, index.nextSourceOrdinal + 1} {
			_, ok := index.messageByOrdinal(ordinal)
			require.False(t, ok)
		}
	})
	t.Run("candidate_lookup_preserves_imported_paths", func(t *testing.T) {
		candidates := index.messageCandidates("first")
		require.Len(t, candidates, 1)
		require.Equal(t, expectedPath, candidates[0].source.GraphPath)

		all := index.allMessages()
		require.Len(t, all, 2)
		for _, candidate := range all {
			require.Equal(t, expectedPath, candidate.source.GraphPath)
		}
	})
	t.Run("tool_result_and_context_ranges", func(t *testing.T) {
		result, ok := index.toolResultByOrdinal(depth + 2)
		require.True(t, ok)
		require.Equal(t, "result", result.text)
		require.Equal(t, expectedPath, result.source.GraphPath)

		info, ok := index.interruptInfoByOrdinal(depth + 3)
		require.True(t, ok)
		require.Equal(t, expectedPath, info.path)
	})
	t.Run("tool_result_negative_ordinal", func(t *testing.T) {
		_, ok := index.toolResultByOrdinal(-1)
		require.False(t, ok)

		source := checkpointToolResultSourceV1{
			SourceOrdinal: -1,
			Kind:          projectionToolResultKindString,
			InterruptID:   "interrupt",
			ToolCallID:    "call",
			Digest:        "digest",
		}
		require.EqualError(t, validateCheckpointToolResultSource(
			source, checkpointProjectionVersionV2),
			"checkpoint projection tool result source metadata is incomplete")
	})
	t.Run("imported_only_empty_index", func(t *testing.T) {
		emptyLeaf := newCheckpointProjectionIndex(checkpointProjectionVersionV2, nil)
		emptyRoot := newCheckpointProjectionIndex(checkpointProjectionVersionV2, nil)
		emptyLeaf.nextOrdinal()
		emptyRoot.importIndex(emptyLeaf, []string{"empty"})
		require.False(t, emptyRoot.hasMessagesOrToolResults())
	})
}

func TestCheckpointProjectionRootPathMetadata(t *testing.T) {
	index := &checkpointProjectionIndex{
		byID:                make(map[string][]canonicalCheckpointMessage),
		toolResultsByCallID: make(map[string][]canonicalCheckpointToolResult),
	}

	message := schema.UserMessage("schema")
	typedSetMessageID(message, "schema-root")
	index.addSchemaMessage(nil, 0, message)
	source, ok := index.sourceForSchemaMessage(message)
	require.True(t, ok)
	source.SourceOrdinal = 0
	source.AgentToolDepth = 0
	source.GraphPath = []string{}
	index.version = checkpointProjectionVersionV1
	restoredMessage, err := index.schemaMessage(source)
	require.NoError(t, err)
	require.Equal(t, message, restoredMessage)

	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic-root")
	index.addAgenticMessage(nil, 0, agenticMessage)
	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)
	agenticSource.SourceOrdinal = 0
	agenticSource.AgentToolDepth = 0
	agenticSource.GraphPath = []string{}
	restoredAgenticMessage, err := index.agenticMessage(agenticSource)
	require.NoError(t, err)
	require.Equal(t, agenticMessage, restoredAgenticMessage)

	toolSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "tool",
		Digest:      "digest",
	}
	index.toolResultsByCallID[toolSource.ToolCallID] = []canonicalCheckpointToolResult{{
		source: toolSource,
		text:   "result",
	}}
	toolSource.GraphPath = []string{}
	restoredToolResult, err := index.toolResult(toolSource)
	require.NoError(t, err)
	require.Equal(t, "result", restoredToolResult.text)

	index.version = checkpointProjectionVersionV2
	_, err = index.schemaMessage(source)
	require.EqualError(t, err,
		"checkpoint projection V2 message source metadata is incomplete")
	_, err = index.agenticMessage(agenticSource)
	require.EqualError(t, err,
		"checkpoint projection V2 agentic message source metadata is incomplete")
	_, err = index.toolResult(toolSource)
	require.EqualError(t, err,
		"checkpoint projection V2 tool result source metadata is incomplete")
}

func TestRunnerCheckpointProjectionReferenceValidation(t *testing.T) {
	t.Run("inline_rejects_all_source_metadata", func(t *testing.T) {
		tests := []struct {
			name   string
			source checkpointMessageSourceV1
		}{
			{name: "source_ordinal", source: checkpointMessageSourceV1{SourceOrdinal: 1}},
			{name: "agent_tool_depth", source: checkpointMessageSourceV1{AgentToolDepth: 1}},
			{name: "kind", source: checkpointMessageSourceV1{Kind: projectionMessageKindSchema}},
			{name: "graph_path", source: checkpointMessageSourceV1{GraphPath: []string{"graph"}}},
			{name: "index", source: checkpointMessageSourceV1{Index: 1}},
			{name: "message_id", source: checkpointMessageSourceV1{MessageID: "message"}},
			{name: "digest", source: checkpointMessageSourceV1{Digest: "digest"}},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ref := runCtxMessageProjectionV1{
					Target:       runCtxTargetRootInput,
					TargetLength: 1,
					Inline:       schema.UserMessage("inline"),
					Source:       tt.source,
				}
				require.EqualError(t, validateRunCtxProjectionRefs(
					[]runCtxMessageProjectionV1{ref}, 1),
					"checkpoint projection schema message payload must contain exactly one of source, inline, or explicit nil")
			})
		}
	})
	t.Run("source_versions_are_complete_and_exclusive", func(t *testing.T) {
		v1 := checkpointMessageSourceV1{
			Kind:      projectionMessageKindSchema,
			GraphPath: []string{"graph"},
			MessageID: "message",
			Digest:    "digest",
		}
		v2 := checkpointMessageSourceV1{
			SourceOrdinal:  1,
			AgentToolDepth: 2,
			Kind:           projectionMessageKindSchema,
			MessageID:      "message",
			Digest:         "digest",
		}
		require.NoError(t, validateCheckpointMessageSource(
			v1, projectionMessageKindSchema, checkpointProjectionVersionV1))
		require.NoError(t, validateCheckpointMessageSource(
			v2, projectionMessageKindSchema, checkpointProjectionVersionV2))

		tests := []struct {
			name    string
			source  checkpointMessageSourceV1
			version int
			want    string
		}{
			{
				name: "v1_with_ordinal", source: func() checkpointMessageSourceV1 {
					source := v1
					source.SourceOrdinal = 1
					return source
				}(), version: checkpointProjectionVersionV1,
				want: "checkpoint projection V1 message source contains V2 metadata",
			},
			{
				name: "v1_with_depth", source: func() checkpointMessageSourceV1 {
					source := v1
					source.AgentToolDepth = 1
					return source
				}(), version: checkpointProjectionVersionV1,
				want: "checkpoint projection V1 message source contains V2 metadata",
			},
			{
				name: "v2_without_ordinal", source: func() checkpointMessageSourceV1 {
					source := v2
					source.SourceOrdinal = 0
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection V2 message source metadata is incomplete",
			},
			{
				name: "v2_with_path", source: func() checkpointMessageSourceV1 {
					source := v2
					source.GraphPath = []string{"legacy"}
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection V2 message source contains V1 metadata",
			},
			{
				name: "negative_ordinal", source: func() checkpointMessageSourceV1 {
					source := v2
					source.SourceOrdinal = -1
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection message source metadata is incomplete",
			},
			{
				name: "negative_depth", source: func() checkpointMessageSourceV1 {
					source := v2
					source.AgentToolDepth = -1
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection message source metadata is incomplete",
			},
			{
				name: "negative_index", source: func() checkpointMessageSourceV1 {
					source := v2
					source.Index = -1
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection message source metadata is incomplete",
			},
			{
				name: "missing_kind", source: func() checkpointMessageSourceV1 {
					source := v2
					source.Kind = ""
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection message source metadata is incomplete",
			},
			{
				name: "missing_message_id", source: func() checkpointMessageSourceV1 {
					source := v2
					source.MessageID = ""
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection message source metadata is incomplete",
			},
			{
				name: "missing_digest", source: func() checkpointMessageSourceV1 {
					source := v2
					source.Digest = ""
					return source
				}(), version: checkpointProjectionVersionV2,
				want: "checkpoint projection message source metadata is incomplete",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				require.EqualError(t, validateCheckpointMessageSource(
					tt.source, projectionMessageKindSchema, tt.version), tt.want)
			})
		}
	})
	t.Run("run_context_targets_must_be_unique", func(t *testing.T) {
		ref := runCtxMessageProjectionV1{Target: runCtxTargetEvent, Index: 0}
		require.EqualError(t, validateRunCtxProjectionRefs(
			[]runCtxMessageProjectionV1{ref, ref}, 2),
			`checkpoint projection has duplicate run context target "event/0/0"`)
	})
	t.Run("info_slice_must_be_complete", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetStateMessage,
			ContextIndex: -1,
			MessageIndex: 0,
			TargetLength: 2,
			Inline:       schema.UserMessage("first"),
		}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			`checkpoint projection has incomplete interrupt info slice "state_message/[]/-1/0/"`)
	})
	t.Run("negative_parent_depth", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetContextStateMessage,
			ContextIndex: 0,
			ParentDepth:  -1,
			MessageIndex: 0,
			TargetLength: 1,
			Inline:       schema.UserMessage("first"),
		}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			"checkpoint projection has invalid parent depth -1")
	})
	t.Run("run_context_invalid_coordinates", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetRootInput,
			Index:        -1,
			TargetLength: 1,
		}}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 1),
			"checkpoint projection has invalid run context coordinates 0/-1")
	})
	t.Run("run_context_invalid_length", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target: runCtxTargetRootInput,
			Index:  0,
		}}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 1),
			`checkpoint projection target "root_input" has invalid length 0`)
	})
	t.Run("run_context_index_exceeds_length", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetRootInput,
			Index:        1,
			TargetLength: 1,
		}}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 1),
			`checkpoint projection target "root_input" index 1 exceeds length 1`)
	})
	t.Run("run_context_inconsistent_lengths", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{
			{Target: runCtxTargetRootInput, Index: 0, TargetLength: 2},
			{Target: runCtxTargetRootInput, Index: 1, TargetLength: 3},
		}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 2),
			`checkpoint projection target "root_input/0" has inconsistent lengths`)
	})
	t.Run("run_context_scalar_has_slice_metadata", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:    runCtxTargetEvent,
			Index:     0,
			LaneDepth: 1,
		}}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 1),
			`checkpoint projection target "event" has invalid lane depth 1`)
	})
	t.Run("run_context_lane_has_slice_metadata", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetLaneEvent,
			Index:        0,
			TargetLength: 1,
		}}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 1),
			`checkpoint projection target "lane_event" has unexpected slice length`)
	})
	t.Run("run_context_unsupported_target", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{Target: "unknown"}}
		require.EqualError(t, validateRunCtxProjectionRefs(refs, 1),
			`checkpoint projection has unsupported run context target "unknown"`)
	})
	t.Run("info_count_mismatch", func(t *testing.T) {
		require.EqualError(t, validateInfoProjectionRefs(nil, 1),
			"checkpoint projection interrupt info reference count mismatch: got 0, want 1")
	})
	t.Run("info_state_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetStateMessage,
			ContextIndex: 0,
			MessageIndex: 0,
			TargetLength: 1,
		}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			"checkpoint projection has invalid interrupt state coordinates")
	})
	t.Run("info_context_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetContextStateMessage,
			ContextIndex: -1,
			MessageIndex: 0,
			TargetLength: 1,
		}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			"checkpoint projection has invalid context state coordinates")
	})
	t.Run("info_rerun_tool_calls_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:        infoTargetRerunToolCalls,
			ContextIndex:  0,
			MessageIndex:  -1,
			RerunExtraKey: "tools",
		}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			"checkpoint projection has invalid rerun tool calls coordinates")
	})
	t.Run("info_context_tool_calls_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetContextToolCalls,
			ContextIndex: -1,
			MessageIndex: -1,
		}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			"checkpoint projection has invalid context tool calls coordinates")
	})
	t.Run("info_unsupported_target", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{Target: "unknown"}}
		require.EqualError(t, validateInfoProjectionRefs(refs, 1),
			`checkpoint projection has unsupported interrupt info target "unknown"`)
	})
	t.Run("info_duplicate_target", func(t *testing.T) {
		ref := infoMessageProjectionV1{
			Target:        infoTargetRerunToolCalls,
			ContextIndex:  -1,
			MessageIndex:  -1,
			RerunExtraKey: "tools",
		}
		require.EqualError(t, validateInfoProjectionRefs(
			[]infoMessageProjectionV1{ref, ref}, 2),
			`checkpoint projection has duplicate interrupt info target "rerun_tool_calls/[]/-1/0/tools/-1"`)
	})
	t.Run("info_inconsistent_lengths", func(t *testing.T) {
		refs := []infoMessageProjectionV1{
			{
				Target:       infoTargetStateMessage,
				ContextIndex: -1,
				MessageIndex: 0,
				TargetLength: 2,
			},
			{
				Target:       infoTargetStateMessage,
				ContextIndex: -1,
				MessageIndex: 1,
				TargetLength: 3,
			},
		}
		require.EqualError(t, validateInfoProjectionRefs(refs, 2),
			`checkpoint projection target "state_message/[]/-1/0/" has inconsistent lengths`)
	})
}

func TestProjectedCheckpointMessages(t *testing.T) {
	schemaMessage := schema.UserMessage("schema")
	typedSetMessageID(schemaMessage, "schema-message")
	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic-message")
	index := &checkpointProjectionIndex{
		byID:    make(map[string][]canonicalCheckpointMessage),
		version: checkpointProjectionVersionV2,
	}
	index.addSchemaMessage(nil, 0, schemaMessage)
	index.addAgenticMessage(nil, 0, agenticMessage)
	schemaSource, ok := index.sourceForSchemaMessage(schemaMessage)
	require.True(t, ok)
	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)

	t.Run("schema_nil", func(t *testing.T) {
		message, err := projectedSchemaMessage(checkpointMessageSourceV1{}, nil, true, index)
		require.NoError(t, err)
		require.Nil(t, message)
	})
	t.Run("schema_nil_with_payload", func(t *testing.T) {
		_, err := projectedSchemaMessage(schemaSource, nil, true, index)
		require.EqualError(t, err, "checkpoint projection nil message has payload")
	})
	t.Run("schema_inline_missing", func(t *testing.T) {
		_, err := projectedSchemaMessage(checkpointMessageSourceV1{}, nil, false, index)
		require.EqualError(t, err, "checkpoint projection inline message is missing")
	})
	t.Run("schema_inline", func(t *testing.T) {
		message, err := projectedSchemaMessage(checkpointMessageSourceV1{},
			schemaMessage, false, index)
		require.NoError(t, err)
		require.Equal(t, schemaMessage, message)
		require.NotSame(t, schemaMessage, message)
	})
	t.Run("schema_inline_and_source", func(t *testing.T) {
		_, err := projectedSchemaMessage(schemaSource, schemaMessage, false, index)
		require.EqualError(t, err,
			"checkpoint projection message has both inline data and a source reference")
	})
	t.Run("schema_source_mismatch", func(t *testing.T) {
		corrupt := schemaSource
		corrupt.Digest = "corrupt"
		_, err := projectedSchemaMessage(corrupt, nil, false, index)
		require.EqualError(t, err,
			`checkpoint projection source message "schema-message" does not match metadata`)
	})
	t.Run("agentic_nil", func(t *testing.T) {
		message, err := projectedAgenticMessage(checkpointMessageSourceV1{}, nil, true, index)
		require.NoError(t, err)
		require.Nil(t, message)
	})
	t.Run("agentic_nil_with_payload", func(t *testing.T) {
		_, err := projectedAgenticMessage(agenticSource, nil, true, index)
		require.EqualError(t, err, "checkpoint projection nil agentic message has payload")
	})
	t.Run("agentic_inline_missing", func(t *testing.T) {
		_, err := projectedAgenticMessage(checkpointMessageSourceV1{}, nil, false, index)
		require.EqualError(t, err, "checkpoint projection inline agentic message is missing")
	})
	t.Run("agentic_inline", func(t *testing.T) {
		message, err := projectedAgenticMessage(checkpointMessageSourceV1{},
			agenticMessage, false, index)
		require.NoError(t, err)
		require.Equal(t, agenticMessage, message)
		require.NotSame(t, agenticMessage, message)
	})
	t.Run("agentic_inline_and_source", func(t *testing.T) {
		_, err := projectedAgenticMessage(agenticSource, agenticMessage, false, index)
		require.EqualError(t, err,
			"checkpoint projection agentic message has both inline data and a source reference")
	})
	t.Run("agentic_source_mismatch", func(t *testing.T) {
		corrupt := agenticSource
		corrupt.Digest = "corrupt"
		_, err := projectedAgenticMessage(corrupt, nil, false, index)
		require.EqualError(t, err,
			`checkpoint projection source agentic message "agentic-message" does not match metadata`)
	})
	t.Run("unsupported_digest_value", func(t *testing.T) {
		_, ok := checkpointProjectionValueDigest(make(chan int))
		require.False(t, ok)
	})
	t.Run("clone_nil", func(t *testing.T) {
		schemaClone, err := cloneSchemaMessageForProjection(nil)
		require.NoError(t, err)
		require.Nil(t, schemaClone)
		agenticClone, err := cloneAgenticMessageForProjection(nil)
		require.NoError(t, err)
		require.Nil(t, agenticClone)
	})
	t.Run("clone_rejects_unencodable_extra", func(t *testing.T) {
		invalidSchema := schema.UserMessage("invalid")
		invalidSchema.Extra = map[string]any{"channel": make(chan int)}
		_, err := cloneSchemaMessageForProjection(invalidSchema)
		require.ErrorContains(t, err, "failed to clone checkpoint message")

		invalidAgentic := schema.UserAgenticMessage("invalid")
		invalidAgentic.Extra = map[string]any{"channel": make(chan int)}
		_, err = cloneAgenticMessageForProjection(invalidAgentic)
		require.ErrorContains(t, err, "failed to clone checkpoint agentic message")
	})
	t.Run("path_comparison", func(t *testing.T) {
		require.False(t, checkpointProjectionPathEqual([]string{"a"}, nil))
		require.False(t, checkpointProjectionPathEqual([]string{"a"}, []string{"b"}))
		require.True(t, checkpointProjectionPathEqual(nil, []string{}))
	})
}

func TestProjectionReferenceDigestsIgnoreUnsafeJSON(t *testing.T) {
	for _, behavior := range []checkpointProjectionJSONBehavior{
		checkpointProjectionJSONPanic,
		checkpointProjectionJSONNondeterministic,
	} {
		t.Run(string(behavior), func(t *testing.T) {
			t.Run("message", func(t *testing.T) {
				atomic.StoreUint32(&checkpointProjectionJSONCalls, 0)
				message := schema.UserMessage("schema")
				message.Extra = map[string]any{"value": &checkpointProjectionJSONValue{
					Behavior: behavior,
					Value:    "persisted",
				}}
				typedSetMessageID(message, "unsafe-json-schema")

				index := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
				require.NotPanics(t, func() {
					index.addSchemaMessage(nil, 0, message)
				})
				source, ok := index.sourceForSchemaMessage(message)
				require.True(t, ok)

				persisted, err := cloneSchemaMessageForProjection(message)
				require.NoError(t, err)
				restoredIndex := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
				restoredIndex.addSchemaMessage(nil, 0, persisted)
				restored, err := restoredIndex.schemaMessage(source)
				require.NoError(t, err)
				require.Equal(t, message, restored)
				require.Zero(t, atomic.LoadUint32(&checkpointProjectionJSONCalls))
			})

			t.Run("agentic_message", func(t *testing.T) {
				atomic.StoreUint32(&checkpointProjectionJSONCalls, 0)
				message := schema.UserAgenticMessage("agentic")
				message.Extra = map[string]any{"value": &checkpointProjectionJSONValue{
					Behavior: behavior,
					Value:    "persisted",
				}}
				typedSetMessageID(message, "unsafe-json-agentic")

				index := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
				require.NotPanics(t, func() {
					index.addAgenticMessage(nil, 0, message)
				})
				source, ok := index.sourceForAgenticMessage(message)
				require.True(t, ok)

				persisted, err := cloneAgenticMessageForProjection(message)
				require.NoError(t, err)
				restoredIndex := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
				restoredIndex.addAgenticMessage(nil, 0, persisted)
				restored, err := restoredIndex.agenticMessage(source)
				require.NoError(t, err)
				require.Equal(t, message, restored)
				require.Zero(t, atomic.LoadUint32(&checkpointProjectionJSONCalls))
			})

			t.Run("enhanced_tool_result", func(t *testing.T) {
				atomic.StoreUint32(&checkpointProjectionJSONCalls, 0)
				result := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
					Type: schema.ToolPartTypeText,
					Text: "result",
					Extra: map[string]any{"value": &checkpointProjectionJSONValue{
						Behavior: behavior,
						Value:    "persisted",
					}},
				}}}
				index := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
				require.NotPanics(t, func() {
					index.addCheckpointToolResults(nil, "interrupt",
						&checkpointinternal.ToolsNodeInterruptStateV1{
							ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": result},
						})
				})
				source, ok := index.sourceForEnhancedToolResult("call", result)
				require.True(t, ok)

				persisted, err := cloneToolResultForProjection(result)
				require.NoError(t, err)
				restoredIndex := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
				restoredIndex.addCheckpointToolResults(nil, "interrupt",
					&checkpointinternal.ToolsNodeInterruptStateV1{
						ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": persisted},
					})
				extra := &compose.ToolsInterruptAndRerunExtra{}
				require.NoError(t, hydrateInfoToolResult(extra, infoToolResultProjectionV1{
					ToolCallID: "call",
					Source:     source,
				}, restoredIndex))
				require.Equal(t, result, extra.ExecutedEnhancedTools["call"])
				require.Zero(t, atomic.LoadUint32(&checkpointProjectionJSONCalls))
			})
		})
	}
}

func TestProjectionReferenceDigestExternalMarshalersStayInline(t *testing.T) {
	tests := []struct {
		name  string
		value any
		calls *uint32
	}{
		{name: "gob_stable", value: &checkpointProjectionGobInfo{
			Visible: "stable",
		}, calls: &checkpointProjectionGobCalls},
		{name: "gob_panic", value: &checkpointProjectionGobInfo{
			Behavior: "panic",
		}, calls: &checkpointProjectionGobCalls},
		{name: "gob_lazy", value: &checkpointProjectionGobInfo{
			Visible: "lazy", Behavior: "lazy",
		}, calls: &checkpointProjectionGobCalls},
		{name: "binary_stable", value: &checkpointProjectionBinaryInfo{
			Visible: "stable",
		}, calls: &checkpointProjectionBinaryCalls},
		{name: "binary_panic", value: &checkpointProjectionBinaryInfo{
			Behavior: "panic",
		}, calls: &checkpointProjectionBinaryCalls},
		{name: "binary_lazy", value: &checkpointProjectionBinaryInfo{
			Visible: "lazy", Behavior: "lazy",
		}, calls: &checkpointProjectionBinaryCalls},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreUint32(tt.calls, 0)
			message := schema.UserMessage("inline")
			message.Extra = map[string]any{"value": tt.value}
			typedSetMessageID(message, "external-marshaler-message")
			index := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)

			require.NotPanics(t, func() {
				index.addSchemaMessage(nil, 0, message)
			})
			require.Empty(t, index.messageCandidates("external-marshaler-message"))

			entries, projected := index.projectSchemaMessages([]*schema.Message{message})
			require.False(t, projected)
			require.Len(t, entries, 1)
			require.Same(t, message, entries[0].Inline)
			require.Nil(t, entries[0].Source)

			agenticMessage := schema.UserAgenticMessage("inline")
			agenticMessage.Extra = map[string]any{"value": tt.value}
			typedSetMessageID(agenticMessage, "external-marshaler-agentic-message")
			index.addAgenticMessage(nil, 0, agenticMessage)
			require.Empty(t, index.messageCandidates("external-marshaler-agentic-message"))
			agenticEntries, agenticProjected := index.projectAgenticMessages(
				[]*schema.AgenticMessage{agenticMessage})
			require.False(t, agenticProjected)
			require.Len(t, agenticEntries, 1)
			require.Same(t, agenticMessage, agenticEntries[0].Inline)
			require.Nil(t, agenticEntries[0].Source)

			result := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
				Type:  schema.ToolPartTypeText,
				Text:  "inline",
				Extra: map[string]any{"value": tt.value},
			}}}
			index.addCheckpointToolResults(nil, "interrupt",
				&checkpointinternal.ToolsNodeInterruptStateV1{
					ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": result},
				})
			require.Empty(t, index.toolResultCandidates("call"))
			extra := &compose.ToolsInterruptAndRerunExtra{
				ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": result},
			}
			projection := &checkpointProjectionV1{}
			projectInfoToolResults(extra, infoProjectionTarget{
				kind: infoTargetRerunToolCalls,
			}, index, projection)
			require.Same(t, result, extra.ExecutedEnhancedTools["call"])
			require.Empty(t, projection.ToolResultRefs)
			require.Zero(t, atomic.LoadUint32(tt.calls))
		})
	}
}

func TestProjectionSourceSelectionPreservesNegativeZero(t *testing.T) {
	negativeFloat32 := math.Float32frombits(1 << 31)
	negativeFloat64 := math.Float64frombits(1 << 63)
	tests := []struct {
		name     string
		positive any
		negative any
	}{
		{name: "float32", positive: float32(0), negative: negativeFloat32},
		{name: "float64", positive: float64(0), negative: negativeFloat64},
		{
			name:     "complex64_real",
			positive: complex(float32(0), float32(1)),
			negative: complex(negativeFloat32, float32(1)),
		},
		{
			name:     "complex64_imaginary",
			positive: complex(float32(1), float32(0)),
			negative: complex(float32(1), negativeFloat32),
		},
		{
			name:     "complex128_real",
			positive: complex(float64(0), float64(1)),
			negative: complex(negativeFloat64, float64(1)),
		},
		{
			name:     "complex128_imaginary",
			positive: complex(float64(1), float64(0)),
			negative: complex(float64(1), negativeFloat64),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.False(t, gobSemanticEqual(tt.positive, tt.negative))

			schemaCanonical := schema.UserMessage("schema")
			schemaCanonical.Extra = map[string]any{"number": tt.positive}
			typedSetMessageID(schemaCanonical, "signed-zero-schema-"+tt.name)
			schemaTarget := schema.UserMessage("schema")
			schemaTarget.Extra = map[string]any{"number": tt.negative}
			typedSetMessageID(schemaTarget, "signed-zero-schema-"+tt.name)

			agenticCanonical := schema.UserAgenticMessage("agentic")
			agenticCanonical.Extra = map[string]any{"number": tt.positive}
			typedSetMessageID(agenticCanonical, "signed-zero-agentic-"+tt.name)
			agenticTarget := schema.UserAgenticMessage("agentic")
			agenticTarget.Extra = map[string]any{"number": tt.negative}
			typedSetMessageID(agenticTarget, "signed-zero-agentic-"+tt.name)

			canonicalCalls := []schema.ToolCall{{ID: "call", Extra: map[string]any{
				"number": tt.positive,
			}}}
			targetCalls := []schema.ToolCall{{ID: "call", Extra: map[string]any{
				"number": tt.negative,
			}}}
			toolCallMessage := schema.AssistantMessage("", canonicalCalls)
			typedSetMessageID(toolCallMessage, "signed-zero-tool-calls-"+tt.name)

			canonicalResult := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
				Type:  schema.ToolPartTypeText,
				Text:  "result",
				Extra: map[string]any{"number": tt.positive},
			}}}
			targetResult := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
				Type:  schema.ToolPartTypeText,
				Text:  "result",
				Extra: map[string]any{"number": tt.negative},
			}}}

			index := newCheckpointProjectionIndex(checkpointProjectionVersion, nil)
			index.addSchemaMessage(nil, 0, schemaCanonical)
			index.addSchemaMessage(nil, 0, toolCallMessage)
			index.addAgenticMessage(nil, 0, agenticCanonical)
			index.addCheckpointToolResults(nil, "interrupt",
				&checkpointinternal.ToolsNodeInterruptStateV1{
					ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": canonicalResult},
				})

			_, ok := index.sourceForSchemaMessage(schemaTarget)
			require.False(t, ok)
			_, ok = index.sourceForAgenticMessage(agenticTarget)
			require.False(t, ok)
			_, ok = index.sourceForToolCalls(targetCalls)
			require.False(t, ok)
			_, ok = index.sourceForEnhancedToolResult("call", targetResult)
			require.False(t, ok)

			extra := &compose.ToolsInterruptAndRerunExtra{
				ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": targetResult},
			}
			projection := &checkpointProjectionV1{}
			projectInfoToolResults(extra, infoProjectionTarget{
				kind: infoTargetRerunToolCalls,
			}, index, projection)
			require.Same(t, targetResult, extra.ExecutedEnhancedTools["call"])
			require.Empty(t, projection.ToolResultRefs)

			restored, err := cloneToolResultForProjection(targetResult)
			require.NoError(t, err)
			require.True(t, gobSemanticEqual(
				tt.negative, restored.Parts[0].Extra["number"]))
		})
	}
}

func TestHydrateRunContextMessageTargets(t *testing.T) {
	schemaMessage := schema.UserMessage("schema")
	typedSetMessageID(schemaMessage, "schema-message")
	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic-message")
	index := &checkpointProjectionIndex{
		byID:    make(map[string][]canonicalCheckpointMessage),
		version: checkpointProjectionVersionV2,
	}
	index.addSchemaMessage(nil, 0, schemaMessage)
	index.addAgenticMessage(nil, 0, agenticMessage)
	schemaSource, ok := index.sourceForSchemaMessage(schemaMessage)
	require.True(t, ok)
	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)

	t.Run("root_input_missing", func(t *testing.T) {
		err := hydrateRunCtxRootInput(nil, runCtxMessageProjectionV1{
			Inline: schemaMessage,
			Index:  0,
		}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid root input target 0")
	})
	t.Run("root_input_occupied", func(t *testing.T) {
		runCtx := &runContext{RootInput: &AgentInput{
			Messages: []*schema.Message{schema.UserMessage("occupied")},
		}}
		err := hydrateRunCtxRootInput(runCtx, runCtxMessageProjectionV1{
			Inline: schemaMessage,
			Index:  0,
		}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid root input target 0")
	})
	t.Run("root_input_source_error", func(t *testing.T) {
		source := schemaSource
		source.Digest = "corrupt"
		err := hydrateRunCtxRootInput(&runContext{RootInput: &AgentInput{}},
			runCtxMessageProjectionV1{Source: source}, 1, index)
		require.EqualError(t, err,
			`checkpoint projection source message "schema-message" does not match metadata`)
	})
	t.Run("event_missing", func(t *testing.T) {
		err := hydrateRunCtxEvent(nil, runCtxMessageProjectionV1{
			Inline: schemaMessage,
			Index:  0,
		}, index)
		require.EqualError(t, err, "checkpoint projection has invalid event target 0")
	})
	t.Run("event_target_occupied", func(t *testing.T) {
		event := &agentEventWrapper{
			AgentEvent: EventFromMessage(schemaMessage, nil, schema.User, ""),
		}
		err := hydrateAgentEventMessage(event, schemaMessage, false)
		require.EqualError(t, err, "checkpoint projection has invalid event message target")
	})
	t.Run("lane_session_missing", func(t *testing.T) {
		err := hydrateRunCtxLaneEvent(nil, runCtxMessageProjectionV1{
			Inline: schemaMessage,
		}, index)
		require.EqualError(t, err, "checkpoint projection lane event session is missing")
	})
	t.Run("lane_depth_missing", func(t *testing.T) {
		runCtx := &runContext{Session: &runSession{LaneEvents: &laneEvents{}}}
		err := hydrateRunCtxLaneEvent(runCtx, runCtxMessageProjectionV1{
			Inline:    schemaMessage,
			LaneDepth: 1,
		}, index)
		require.EqualError(t, err, "checkpoint projection has invalid lane event target 1/0")
	})
	t.Run("agentic_root_input_missing", func(t *testing.T) {
		err := hydrateRunCtxAgenticRootInput(nil, runCtxMessageProjectionV1{
			AgenticInline: agenticMessage,
		}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid agentic root input target 0")
	})
	t.Run("agentic_root_input_wrong_type", func(t *testing.T) {
		err := hydrateRunCtxAgenticRootInput(&runContext{AgenticRootInput: "invalid"},
			runCtxMessageProjectionV1{AgenticInline: agenticMessage}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid agentic root input target 0")
	})
	t.Run("agentic_root_input_occupied", func(t *testing.T) {
		runCtx := &runContext{AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{agenticMessage},
		}}
		err := hydrateRunCtxAgenticRootInput(runCtx,
			runCtxMessageProjectionV1{AgenticInline: agenticMessage}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid agentic root input target 0")
	})
	t.Run("agentic_root_input_source_error", func(t *testing.T) {
		source := agenticSource
		source.Digest = "corrupt"
		runCtx := &runContext{AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{}}
		err := hydrateRunCtxAgenticRootInput(runCtx,
			runCtxMessageProjectionV1{Source: source}, 1, index)
		require.EqualError(t, err,
			`checkpoint projection source agentic message "agentic-message" does not match metadata`)
	})
	t.Run("typed_event_session_missing", func(t *testing.T) {
		err := hydrateRunCtxTypedEvent(nil, runCtxMessageProjectionV1{
			AgenticInline: agenticMessage,
		}, index)
		require.EqualError(t, err, "checkpoint projection typed event session is missing")
	})
	t.Run("typed_event_collection_invalid", func(t *testing.T) {
		runCtx := &runContext{Session: &runSession{TypedEvents: "invalid"}}
		err := hydrateRunCtxTypedEvent(runCtx, runCtxMessageProjectionV1{
			AgenticInline: agenticMessage,
		}, index)
		require.EqualError(t, err, "checkpoint projection has invalid typed event target 0")
	})
	t.Run("typed_event_target_occupied", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: EventFromAgenticMessage(agenticMessage, nil, schema.AgenticRoleTypeUser),
		}
		err := hydrateTypedAgentEventMessage(event, agenticMessage, false)
		require.EqualError(t, err, "checkpoint projection has invalid typed event message target")
	})
}

func TestHydrateInterruptInfoMessageTargets(t *testing.T) {
	message := schema.AssistantMessage("schema", []schema.ToolCall{{ID: "tool"}})
	typedSetMessageID(message, "schema-message")
	index := &checkpointProjectionIndex{
		byID:    make(map[string][]canonicalCheckpointMessage),
		version: checkpointProjectionVersionV2,
	}
	index.addSchemaMessage(nil, 0, message)

	stateRef := infoMessageProjectionV1{
		Target:       infoTargetStateMessage,
		ContextIndex: -1,
		MessageIndex: 0,
		TargetLength: 1,
		Inline:       message,
	}
	contextRef := infoMessageProjectionV1{
		Target:       infoTargetContextStateMessage,
		ContextIndex: 0,
		MessageIndex: 0,
		TargetLength: 1,
		Inline:       message,
	}
	contextToolCallsRef := infoMessageProjectionV1{
		Target:       infoTargetContextToolCalls,
		ContextIndex: 0,
		MessageIndex: -1,
		Inline:       message,
	}
	rerunToolCallsRef := infoMessageProjectionV1{
		Target:        infoTargetRerunToolCalls,
		ContextIndex:  -1,
		MessageIndex:  -1,
		RerunExtraKey: "tools",
		Inline:        message,
	}

	t.Run("outer_reference_validation", func(t *testing.T) {
		err := hydrateInterruptInfoMessages(nil, []infoMessageProjectionV1{stateRef}, 0, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt info reference count mismatch: got 1, want 0")
		require.NoError(t, hydrateInterruptInfoMessages(nil, nil, 0, index))
		err = hydrateInterruptInfoMessages(nil, []infoMessageProjectionV1{stateRef}, 1, index)
		require.EqualError(t, err, "checkpoint projection interrupt info is missing")
		err = hydrateInterruptInfoMessages(&InterruptInfo{Data: "invalid"},
			[]infoMessageProjectionV1{stateRef}, 1, index)
		require.EqualError(t, err, "checkpoint projection interrupt info has invalid type string")
	})
	t.Run("missing_subgraph_path", func(t *testing.T) {
		info := &compose.InterruptInfo{SubGraphs: map[string]*compose.InterruptInfo{}}
		ref := stateRef
		ref.SubGraphPath = []string{"missing"}
		err := hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
		require.EqualError(t, err, "checkpoint projection interrupt info path [missing] is missing")
	})
	t.Run("missing_context", func(t *testing.T) {
		err := hydrateComposeInterruptInfoRefs(&compose.InterruptInfo{},
			[]infoMessageProjectionV1{contextRef}, index)
		require.EqualError(t, err, "checkpoint projection interrupt context index 0 is invalid")
	})
	t.Run("context_tool_calls_nil_source", func(t *testing.T) {
		info := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{Info: &compose.ToolsInterruptAndRerunExtra{}}}}
		ref := contextToolCallsRef
		ref.Inline = nil
		ref.IsNil = true
		err := hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
		require.EqualError(t, err, "checkpoint projection context tool calls source is nil")
	})
	t.Run("context_tool_calls_invalid_target", func(t *testing.T) {
		info := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{Info: "invalid"}}}
		err := hydrateComposeInterruptInfoRefs(info,
			[]infoMessageProjectionV1{contextToolCallsRef}, index)
		require.EqualError(t, err, "checkpoint projection has invalid context tool calls target")
	})
	t.Run("rerun_tool_calls_nil_source", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{
			"tools": &compose.ToolsInterruptAndRerunExtra{},
		}}
		ref := rerunToolCallsRef
		ref.Inline = nil
		ref.IsNil = true
		err := hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
		require.EqualError(t, err, "checkpoint projection rerun tool calls source is nil")
	})
	t.Run("rerun_tool_calls_invalid_target", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{"tools": "invalid"}}
		err := hydrateComposeInterruptInfoRefs(info,
			[]infoMessageProjectionV1{rerunToolCallsRef}, index)
		require.EqualError(t, err, "checkpoint projection has invalid rerun tool calls target")
	})
	t.Run("unsupported_target", func(t *testing.T) {
		ref := stateRef
		ref.Target = "unknown"
		err := hydrateComposeInterruptInfoRefs(&compose.InterruptInfo{},
			[]infoMessageProjectionV1{ref}, index)
		require.EqualError(t, err,
			`checkpoint projection has unsupported interrupt info target "unknown"`)
	})
	t.Run("state_message_source_error", func(t *testing.T) {
		ref := stateRef
		ref.Inline = nil
		ref.Source = checkpointMessageSourceV1{
			SourceOrdinal: 99,
			Kind:          projectionMessageKindSchema,
			MessageID:     "missing",
			Digest:        "digest",
		}
		err := hydrateInfoStateMessage(&State{}, ref, 1, index)
		require.EqualError(t, err,
			`checkpoint projection source message "missing" does not match metadata`)
	})
	t.Run("state_message_nil_target", func(t *testing.T) {
		var state *State
		err := hydrateInfoStateMessage(state, stateRef, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid state message target")
	})
	t.Run("state_message_occupied", func(t *testing.T) {
		state := &State{Messages: []*schema.Message{schema.UserMessage("occupied")}}
		err := hydrateInfoStateMessage(state, stateRef, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid state message target")
	})
	t.Run("agentic_state_message_nil_target", func(t *testing.T) {
		var state *agenticState
		ref := stateRef
		ref.Inline = nil
		ref.AgenticInline = schema.UserAgenticMessage("agentic")
		err := hydrateInfoStateMessage(state, ref, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid agentic state message target")
	})
	t.Run("invalid_state_type", func(t *testing.T) {
		err := hydrateInfoStateMessage("invalid", stateRef, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid state message target type string")
	})
	t.Run("nested_placeholder_validation", func(t *testing.T) {
		value, err := hydrateProjectionInfoValue(
			(*checkpointInterruptInfoPlaceholderV1)(nil), index)
		require.EqualError(t, err, "checkpoint projection contains a nil interrupt info reference")
		require.Nil(t, value)

		value, err = hydrateProjectionInfoValue("inline", index)
		require.NoError(t, err)
		require.Equal(t, "inline", value)

		placeholder := &checkpointInterruptInfoPlaceholderV1{
			Info:     &compose.InterruptInfo{},
			RefCount: 1,
		}
		_, err = hydrateProjectionInfoValue(placeholder, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt info reference count mismatch: got 0, want 1")
	})
	t.Run("nested_info_paths", func(t *testing.T) {
		require.NoError(t, hydrateNestedInterruptInfoPlaceholders(nil, index))
		_, err := composeInterruptInfoAtPath(nil, nil)
		require.EqualError(t, err, "checkpoint projection interrupt info path [] is missing")
		_, err = composeInterruptInfoAtPath(
			&compose.InterruptInfo{SubGraphs: map[string]*compose.InterruptInfo{}},
			[]string{"missing"})
		require.EqualError(t, err, "checkpoint projection interrupt info path [missing] is missing")
		_, err = interruptContextAt(&compose.InterruptInfo{}, 0, 0)
		require.EqualError(t, err, "checkpoint projection interrupt context index 0 is invalid")
		_, err = interruptContextAt(&compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{{}},
		}, 0, 1)
		require.EqualError(t, err, "checkpoint projection interrupt context parent depth 1 is invalid")
	})
}

func TestCheckpointToolResultProjectionValidation(t *testing.T) {
	stringSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      "string-digest",
	}
	enhancedSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindEnhanced,
		InterruptID: "interrupt",
		ToolCallID:  "enhanced-call",
		Digest:      "enhanced-digest",
	}
	enhancedResult := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: "enhanced",
	}}}
	index := &checkpointProjectionIndex{
		version: checkpointProjectionVersionV1,
		toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
			stringSource.ToolCallID: {{
				source: stringSource,
				text:   "result",
			}},
			enhancedSource.ToolCallID: {{
				source:   enhancedSource,
				enhanced: enhancedResult,
			}},
		},
	}
	rerunRef := infoToolResultProjectionV1{
		Target:        infoTargetRerunToolResult,
		ContextIndex:  -1,
		RerunExtraKey: "tools",
		ToolCallID:    stringSource.ToolCallID,
		Source:        stringSource,
	}

	t.Run("reflection_rejects_invalid_values", func(t *testing.T) {
		var nilPointer *struct{}
		_, _, ok := checkpointToolExecutionMaps(nilPointer)
		require.False(t, ok)
		_, _, ok = checkpointToolExecutionMaps("invalid")
		require.False(t, ok)
	})
	t.Run("projection_keeps_unmatched_results", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{
			ExecutedTools: map[string]string{"missing": "value"},
			ExecutedEnhancedTools: map[string]*schema.ToolResult{
				"missing": enhancedResult,
			},
		}
		projection := &checkpointProjectionV1{}
		projectInfoToolResults(nil, infoProjectionTarget{}, index, projection)
		projectInfoToolResults(extra, infoProjectionTarget{}, index, projection)
		require.Empty(t, projection.ToolResultRefs)
		require.Contains(t, extra.ExecutedTools, "missing")
		require.Contains(t, extra.ExecutedEnhancedTools, "missing")
	})
	t.Run("source_lookup_and_sorting", func(t *testing.T) {
		_, ok := index.sourceForStandardToolResult("missing", "result")
		require.False(t, ok)
		_, ok = index.sourceForEnhancedToolResult("missing", enhancedResult)
		require.False(t, ok)

		later := stringSource
		later.GraphPath = []string{"z"}
		earlier := stringSource
		earlier.GraphPath = []string{"a"}
		index.toolResultsByCallID["sorted"] = []canonicalCheckpointToolResult{
			{source: later}, {source: earlier},
		}
		candidates := index.sortedToolResultCandidates("sorted")
		require.Equal(t, []string{"a"}, candidates[0].source.GraphPath)
		require.Equal(t, []string{"z"}, candidates[1].source.GraphPath)
	})
	t.Run("source_versions_are_complete_and_exclusive", func(t *testing.T) {
		v2 := stringSource
		v2.SourceOrdinal = 1
		require.NoError(t, validateCheckpointToolResultSource(
			stringSource, checkpointProjectionVersionV1))
		require.NoError(t, validateCheckpointToolResultSource(
			v2, checkpointProjectionVersionV2))

		mixedV1 := stringSource
		mixedV1.SourceOrdinal = 1
		require.EqualError(t, validateCheckpointToolResultSource(
			mixedV1, checkpointProjectionVersionV1),
			"checkpoint projection V1 tool result source contains V2 metadata")

		incompleteV2 := v2
		incompleteV2.SourceOrdinal = 0
		require.EqualError(t, validateCheckpointToolResultSource(
			incompleteV2, checkpointProjectionVersionV2),
			"checkpoint projection V2 tool result source metadata is incomplete")

		mixedV2 := v2
		mixedV2.GraphPath = []string{"legacy"}
		require.EqualError(t, validateCheckpointToolResultSource(
			mixedV2, checkpointProjectionVersionV2),
			"checkpoint projection V2 tool result source contains V1 metadata")

		incomplete := v2
		incomplete.Digest = ""
		require.EqualError(t, validateCheckpointToolResultSource(
			incomplete, checkpointProjectionVersionV2),
			"checkpoint projection tool result source metadata is incomplete")
	})
	t.Run("nested_import_assigns_local_ordinals", func(t *testing.T) {
		enhanced := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
			Type: schema.ToolPartTypeText,
			Text: "enhanced",
		}}}
		source := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		source.addCheckpointToolResults([]string{"child"}, "interrupt",
			&checkpointinternal.ToolsNodeInterruptStateV1{
				ExecutedTools:         map[string]string{"standard": "result"},
				ExecutedEnhancedTools: map[string]*schema.ToolResult{"enhanced": enhanced},
			})
		require.Equal(t, 2, source.nextSourceOrdinal)

		destination := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		destination.importIndex(source, []string{"outer"})
		require.Equal(t, 2, destination.nextSourceOrdinal)

		standardSource, ok := destination.sourceForStandardToolResult("standard", "result")
		require.True(t, ok)
		require.Equal(t, 1, standardSource.SourceOrdinal)
		require.Empty(t, standardSource.GraphPath)
		standard, err := destination.toolResult(standardSource)
		require.NoError(t, err)
		require.Equal(t, "result", standard.text)
		require.Equal(t, []string{"outer", "child"}, standard.source.GraphPath)

		enhancedSource, ok := destination.sourceForEnhancedToolResult("enhanced", enhanced)
		require.True(t, ok)
		require.Equal(t, 2, enhancedSource.SourceOrdinal)
		require.Empty(t, enhancedSource.GraphPath)
		restoredEnhanced, err := destination.toolResult(enhancedSource)
		require.NoError(t, err)
		require.Equal(t, enhanced, restoredEnhanced.enhanced)
		require.Equal(t, []string{"outer", "child"}, restoredEnhanced.source.GraphPath)
	})
	t.Run("outer_validation", func(t *testing.T) {
		err := hydrateInterruptInfoToolResults(nil, []infoToolResultProjectionV1{rerunRef},
			0, index)
		require.EqualError(t, err,
			"checkpoint projection tool result reference count mismatch: got 1, want 0")
		require.NoError(t, hydrateInterruptInfoToolResults(nil, nil, 0, index))
		err = hydrateInterruptInfoToolResults(nil, []infoToolResultProjectionV1{rerunRef},
			1, index)
		require.EqualError(t, err, "checkpoint projection tool result interrupt info is missing")
		err = hydrateInterruptInfoToolResults(&InterruptInfo{Data: "invalid"},
			[]infoToolResultProjectionV1{rerunRef}, 1, index)
		require.EqualError(t, err,
			"checkpoint projection tool result interrupt info has invalid type string")
	})
	t.Run("compose_reference_validation", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{
			"tools": &compose.ToolsInterruptAndRerunExtra{},
		}}
		err := hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{rerunRef}, 0, index)
		require.EqualError(t, err,
			"checkpoint projection tool result reference count mismatch: got 1, want 0")

		invalidCoordinates := rerunRef
		invalidCoordinates.ParentDepth = -1
		err = hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{invalidCoordinates}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid tool result coordinates")

		err = hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{rerunRef, rerunRef}, 2, index)
		require.EqualError(t, err, `checkpoint projection has duplicate tool result target "call"`)
	})
	t.Run("missing_subgraph_path", func(t *testing.T) {
		ref := rerunRef
		ref.SubGraphPath = []string{"missing"}
		err := hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.EqualError(t, err, "checkpoint projection interrupt info path [missing] is missing")
	})
	t.Run("invalid_target_coordinates", func(t *testing.T) {
		ref := rerunRef
		ref.ContextIndex = 0
		err := hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid rerun tool result target")

		ref.Target = infoTargetContextToolResult
		ref.ContextIndex = -1
		ref.RerunExtraKey = ""
		err = hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.EqualError(t, err, "checkpoint projection has invalid context tool result target")

		ref.ContextIndex = 0
		err = hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.EqualError(t, err, "checkpoint projection interrupt context index 0 is invalid")
	})
	t.Run("unsupported_target", func(t *testing.T) {
		ref := rerunRef
		ref.Target = "unknown"
		err := hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.EqualError(t, err,
			`checkpoint projection has unsupported tool result target "unknown"`)
	})
	t.Run("invalid_target_type", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{"tools": "invalid"}}
		err := hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{rerunRef}, 1, index)
		require.EqualError(t, err,
			"checkpoint projection tool result target has invalid type string")
	})
	t.Run("standard_result", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{}
		require.NoError(t, hydrateInfoToolResult(extra, rerunRef, index))
		require.Equal(t, "result", extra.ExecutedTools[stringSource.ToolCallID])
		require.EqualError(t, hydrateInfoToolResult(extra, rerunRef, index),
			`checkpoint projection tool result target "call" is already populated`)
	})
	t.Run("enhanced_result", func(t *testing.T) {
		ref := rerunRef
		ref.ToolCallID = enhancedSource.ToolCallID
		ref.Source = enhancedSource
		extra := &compose.ToolsInterruptAndRerunExtra{}
		require.NoError(t, hydrateInfoToolResult(extra, ref, index))
		require.Equal(t, enhancedResult, extra.ExecutedEnhancedTools[enhancedSource.ToolCallID])
		require.NotSame(t, enhancedResult, extra.ExecutedEnhancedTools[enhancedSource.ToolCallID])
		require.EqualError(t, hydrateInfoToolResult(extra, ref, index),
			`checkpoint projection tool result target "enhanced-call" is already populated`)
	})
	t.Run("source_and_kind_errors", func(t *testing.T) {
		ref := rerunRef
		ref.Source.Digest = "missing"
		require.EqualError(t, hydrateInfoToolResult(
			&compose.ToolsInterruptAndRerunExtra{}, ref, index),
			`checkpoint projection tool result "call" does not match metadata`)

		unsupportedSource := checkpointToolResultSourceV1{
			Kind:        "unknown",
			InterruptID: "interrupt",
			ToolCallID:  "unknown-call",
			Digest:      "unknown-digest",
		}
		index.toolResultsByCallID[unsupportedSource.ToolCallID] =
			[]canonicalCheckpointToolResult{{source: unsupportedSource}}
		ref.ToolCallID = unsupportedSource.ToolCallID
		ref.Source = unsupportedSource
		require.EqualError(t, hydrateInfoToolResult(
			&compose.ToolsInterruptAndRerunExtra{}, ref, index),
			`checkpoint projection has unsupported tool result kind "unknown"`)
	})
	t.Run("clone_nil", func(t *testing.T) {
		result, err := cloneToolResultForProjection(nil)
		require.NoError(t, err)
		require.Nil(t, result)
	})
}

func TestRunnerProjectionKeepsAmbiguousMessagesInline(t *testing.T) {
	message := schema.AssistantMessage("same", nil)
	message.Extra = map[string]any{"_eino_msg_id": "duplicate"}
	different := *message
	different.Content = "different"
	index := &checkpointProjectionIndex{
		byID: map[string][]canonicalCheckpointMessage{
			"duplicate": {
				{source: checkpointMessageSourceV1{Kind: projectionMessageKindSchema}, message: message},
				{source: checkpointMessageSourceV1{Kind: projectionMessageKindSchema}, message: &different},
			},
		},
	}
	_, ok := index.sourceForSchemaMessage(message)
	require.False(t, ok)

	missingID := schema.AssistantMessage("no id", nil)
	_, ok = index.sourceForSchemaMessage(missingID)
	require.False(t, ok)
}

func TestRunnerProjectionSentinelFailsLoudlyInLegacyReader(t *testing.T) {
	assertCheckpointCompatLegacyReaderRejectsValue(t, buildCheckpointCompatLegacyReader(t),
		&runnerProjectionSentinelV1{Version: checkpointProjectionVersionV1},
		"_eino_adk_runner_projection_v1")
}

func TestRunnerCheckpointProjectionRejectsCorruptReference(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:         "projection-corrupt-ref",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  32 << 10,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	var persisted serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))
	require.NotNil(t, persisted.ProjectionV1)
	require.NotEmpty(t, persisted.ProjectionV1.InfoRefs)
	persisted.ProjectionV1.InfoRefs[0].Source.Digest = "corrupt"

	var buf bytes.Buffer
	require.NoError(t, gob.NewEncoder(&buf).Encode(&persisted))
	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, buf.Bytes()))
	_, _, _, err := runnerLoadCheckPointImpl(store, context.Background(), spec.Name)
	require.ErrorContains(t, err, "does not match metadata")
}

func TestRunnerCheckpointProjectionAgenticMessages(t *testing.T) {
	message := schema.UserAgenticMessage("projected")
	typedSetMessageID(message, "agentic-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addAgenticMessage([]string{"graph"}, 0, message)

	event := EventFromAgenticMessage(message, nil, schema.AgenticRoleTypeUser)
	streamEvent := EventFromAgenticMessage(nil,
		schema.StreamReaderFromArray([]*schema.AgenticMessage{message}),
		schema.AgenticRoleTypeUser)
	events := []*typedAgentEventWrapper[*schema.AgenticMessage]{
		{event: event},
		{event: streamEvent},
	}
	runCtx := &runContext{
		AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{message},
		},
		Session: &runSession{
			Values:      map[string]any{"preserved": "value"},
			valuesMtx:   &sync.Mutex{},
			TypedEvents: &events,
		},
		RunPath: []RunStep{{agentName: "agent"}},
	}
	cloned := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(cloned, index, projection)
	require.Len(t, projection.RunCtxRefs, 3)
	rootInput := cloned.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage])
	require.Nil(t, rootInput.Messages)
	typedEvents := cloned.Session.TypedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage])
	require.Nil(t, (*typedEvents)[0].event.Output.MessageOutput.Message)
	require.False(t, (*typedEvents)[1].event.Output.MessageOutput.IsStreaming)
	require.Nil(t, (*typedEvents)[1].event.Output.MessageOutput.MessageStream)

	require.NoError(t, hydrateRunContextMessages(cloned, projection.RunCtxRefs,
		projection.RunCtxRefCount, index))
	require.Equal(t, []*schema.AgenticMessage{message}, rootInput.Messages)
	require.Equal(t, message, (*typedEvents)[0].event.Output.MessageOutput.Message)
	require.True(t, (*typedEvents)[1].event.Output.MessageOutput.IsStreaming)
	restoredStreamMessage, err := (*typedEvents)[1].event.Output.MessageOutput.GetMessage()
	require.NoError(t, err)
	require.Equal(t, message, restoredStreamMessage)
	require.Equal(t, "value", cloned.Session.Values["preserved"])
	require.Equal(t, "agent", cloned.RunPath[0].String())

	liveTypedEvents := runCtx.Session.TypedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage])
	liveStreamMessage, err := (*liveTypedEvents)[1].event.Output.MessageOutput.GetMessage()
	require.NoError(t, err)
	require.Equal(t, message, liveStreamMessage,
		"projection must leave an independently readable stream on the live Agentic event")
}

func TestRunnerCheckpointProjectionAgenticInterruptState(t *testing.T) {
	canonical := schema.UserAgenticMessage("canonical")
	typedSetMessageID(canonical, "agentic-state-message")
	inline := schema.UserAgenticMessage("inline")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addAgenticMessage([]string{"graph"}, 0, canonical)

	state := &agenticState{Messages: []*schema.AgenticMessage{inline, canonical}}
	info := &compose.InterruptInfo{State: state}
	projection := &checkpointProjectionV1{}
	projectComposeInterruptInfoMessages(info, nil, index, projection)
	require.Len(t, projection.InfoRefs, 2)
	require.Nil(t, state.Messages)

	require.NoError(t, hydrateComposeInterruptInfoRefs(info, projection.InfoRefs, index))
	require.Equal(t, []*schema.AgenticMessage{inline, canonical}, state.Messages)
	require.NotSame(t, inline, state.Messages[0])
	require.NotSame(t, canonical, state.Messages[1])
}

func TestRunnerCheckpointProjectionReusesEnhancedToolResult(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "invoke"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			enhanced := &checkpointProjectionEnhancedTool{name: "enhanced"}
			interrupting := &checkpointCompatInterruptTool{name: "interrupt"}
			agent := newCheckpointCompatChatModelAgent(t, "projection-enhanced",
				[]string{"enhanced", "interrupt"},
				[]componenttool.BaseTool{enhanced, interrupting}, "", "")
			store := newCheckpointCompatStore()
			runner := NewRunner(context.Background(), RunnerConfig{
				Agent:           agent,
				EnableStreaming: streaming,
				CheckPointStore: store,
			})
			iter := runner.Query(context.Background(), "start", WithCheckPointID(name))
			var interruptIDs []string
			var liveInterruptInfo *InterruptInfo
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				require.NoError(t, event.Err)
				if event.Action != nil && event.Action.Interrupted != nil {
					liveInterruptInfo = event.Action.Interrupted
					for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
						interruptIDs = append(interruptIDs, interruptCtx.ID)
					}
				}
			}
			require.Len(t, interruptIDs, 1)
			require.Equal(t, 1, enhanced.callCount())
			liveExtra := findCheckpointProjectionToolsExtra(t, liveInterruptInfo)
			require.Contains(t, liveExtra.ExecutedEnhancedTools, "call-0",
				"checkpoint projection mutated the live interrupt event")

			raw, exists, err := store.Get(context.Background(), name)
			require.NoError(t, err)
			require.True(t, exists)
			var persisted serialization
			require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))
			require.NotNil(t, persisted.ProjectionV1)
			require.NotEmpty(t, persisted.ProjectionV1.ToolResultRefs)
			_, _, restoredInfo, err := runnerLoadCheckPointImpl(store, context.Background(), name)
			require.NoError(t, err)
			restoredExtra := findCheckpointProjectionToolsExtra(t, restoredInfo.InterruptInfo)
			require.Equal(t, liveExtra.ExecutedEnhancedTools,
				restoredExtra.ExecutedEnhancedTools)

			if !streaming {
				sourceID := persisted.ProjectionV1.SourceInterruptID
				require.NoError(t, restoreRunnerCheckpointProjection(&persisted))
				sourceState := persisted.InterruptID2State[sourceID]
				sourceData, ok := sourceState.State.([]byte)
				require.True(t, ok)
				toolOnlyData, transformErr := compose.TransformCheckpointValues(sourceData,
					&gobSerializer{}, func(_ compose.NodePath,
						location compose.CheckpointValueLocation, value any) (any, bool, error) {
						if location.Kind == compose.CheckpointValueState {
							return nil, true, nil
						}
						return value, false, nil
					})
				require.NoError(t, transformErr)
				sourceState.State = toolOnlyData
				states := map[string]core.InterruptState{sourceID: sourceState}
				toolOnlyExtra := &compose.ToolsInterruptAndRerunExtra{
					ExecutedEnhancedTools: map[string]*schema.ToolResult{
						"call-0": restoredExtra.ExecutedEnhancedTools["call-0"],
					},
				}
				toolOnlyInfo := &InterruptInfo{Data: &ChatModelAgentInterruptInfo{
					Info: &compose.InterruptInfo{RerunNodesExtra: map[string]any{
						"tools": toolOnlyExtra,
					}},
				}}
				_, projectedInfo, _, toolOnlyProjection, projectionErr :=
					projectRunnerCheckpoint(nil, toolOnlyInfo, sourceID, states)
				require.NoError(t, projectionErr)
				require.NotNil(t, toolOnlyProjection)
				require.Empty(t, toolOnlyProjection.InfoRefs)
				require.Len(t, toolOnlyProjection.ToolResultRefs, 1)
				projectedExtra := projectedInfo.Data.(*ChatModelAgentInterruptInfo).
					Info.RerunNodesExtra["tools"].(*compose.ToolsInterruptAndRerunExtra)
				require.NotContains(t, projectedExtra.ExecutedEnhancedTools, "call-0")
			}

			iter, err = runner.ResumeWithParams(context.Background(), name, &ResumeParams{
				Targets: map[string]any{interruptIDs[0]: "resumed"},
			})
			require.NoError(t, err)
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				require.NoError(t, event.Err)
			}
			require.Equal(t, 1, enhanced.callCount(),
				"successful enhanced sibling must be restored instead of executed again")
		})
	}
}

func findCheckpointProjectionToolsExtra(t *testing.T,
	info *InterruptInfo) *compose.ToolsInterruptAndRerunExtra {
	t.Helper()
	require.NotNil(t, info)
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.NotNil(t, chatModelInfo)
	require.NotNil(t, chatModelInfo.Info)
	for _, interruptCtx := range chatModelInfo.Info.InterruptContexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			if extra, ok := current.Info.(*compose.ToolsInterruptAndRerunExtra); ok {
				return extra
			}
		}
	}
	t.Fatal("ToolsInterruptAndRerunExtra not found")
	return nil
}

func TestRunnerCheckpointProjectionPreservesUnmatchedMessagesInline(t *testing.T) {
	canonical := schema.AssistantMessage("canonical", nil)
	typedSetMessageID(canonical, "message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, canonical)

	inline := schema.UserMessage("inline")
	runCtx := &runContext{RootInput: &AgentInput{
		Messages: []*schema.Message{nil, inline, canonical},
	}}
	cloned := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(cloned, index, projection)
	require.Nil(t, cloned.RootInput.Messages)
	require.Len(t, projection.RunCtxRefs, 3)
	require.True(t, projection.RunCtxRefs[0].IsNil)
	require.Same(t, inline, projection.RunCtxRefs[1].Inline)
	require.Empty(t, projection.RunCtxRefs[1].Source.MessageID)
	require.Nil(t, projection.RunCtxRefs[2].Inline)
	require.Equal(t, "message", projection.RunCtxRefs[2].Source.MessageID)

	require.NoError(t, hydrateRunContextMessages(cloned, projection.RunCtxRefs,
		projection.RunCtxRefCount, index))
	require.Equal(t, []*schema.Message{nil, inline, canonical}, cloned.RootInput.Messages)
}

func TestCheckpointProjectionCloneContainerSemantics(t *testing.T) {
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         "projection-container-semantics",
		Cancel:       true,
		PayloadField: "content",
		PayloadSize:  32 << 10,
	})
	var source serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&source))
	require.NotNil(t, source.ProjectionV1)
	sourceID := source.ProjectionV1.SourceInterruptID
	require.NoError(t, restoreRunnerCheckpointProjection(&source))
	sourceState, exists := source.InterruptID2State[sourceID]
	require.True(t, exists)
	sourceData, ok := sourceState.State.([]byte)
	require.True(t, ok)

	build := func(empty bool) (*runContext, *compose.InterruptInfo) {
		var (
			runPath           []RunStep
			rootMessages      []*schema.Message
			agenticMessages   []*schema.AgenticMessage
			values            map[string]any
			events            []*agentEventWrapper
			laneEventSlice    []*agentEventWrapper
			typedEvents       []*typedAgentEventWrapper[*schema.AgenticMessage]
			beforeNodes       []string
			afterNodes        []string
			rerunNodes        []string
			rerunNodesExtra   map[string]any
			subGraphs         map[string]*compose.InterruptInfo
			interruptContexts []*InterruptCtx
			toolCalls         []schema.ToolCall
			executedTools     map[string]string
			enhancedTools     map[string]*schema.ToolResult
			rerunTools        []string
			rerunExtra        map[string]any
		)
		if empty {
			runPath = []RunStep{}
			rootMessages = []*schema.Message{}
			agenticMessages = []*schema.AgenticMessage{}
			values = map[string]any{}
			events = []*agentEventWrapper{}
			laneEventSlice = []*agentEventWrapper{}
			typedEvents = []*typedAgentEventWrapper[*schema.AgenticMessage]{}
			beforeNodes = []string{}
			afterNodes = []string{}
			rerunNodes = []string{}
			rerunNodesExtra = map[string]any{}
			subGraphs = map[string]*compose.InterruptInfo{}
			interruptContexts = []*InterruptCtx{}
			toolCalls = []schema.ToolCall{}
			executedTools = map[string]string{}
			enhancedTools = map[string]*schema.ToolResult{}
			rerunTools = []string{}
			rerunExtra = map[string]any{}
		}
		extra := &compose.ToolsInterruptAndRerunExtra{
			ToolCalls:             toolCalls,
			ExecutedTools:         executedTools,
			ExecutedEnhancedTools: enhancedTools,
			RerunTools:            rerunTools,
			RerunExtraMap:         rerunExtra,
		}
		info := &compose.InterruptInfo{
			State:             extra,
			BeforeNodes:       beforeNodes,
			AfterNodes:        afterNodes,
			RerunNodes:        rerunNodes,
			RerunNodesExtra:   rerunNodesExtra,
			SubGraphs:         subGraphs,
			InterruptContexts: interruptContexts,
		}
		runCtx := &runContext{
			RootInput: &AgentInput{Messages: rootMessages},
			RunPath:   runPath,
			AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{
				Messages: agenticMessages,
			},
			Session: &runSession{
				Values:      values,
				valuesMtx:   &sync.Mutex{},
				Events:      events,
				LaneEvents:  &laneEvents{Events: laneEventSlice},
				TypedEvents: &typedEvents,
			},
		}
		return runCtx, info
	}

	for _, empty := range []bool{false, true} {
		name := "nil"
		if empty {
			name = "empty"
		}
		t.Run(name, func(t *testing.T) {
			runCtx, info := build(empty)
			var outerInterruptContexts []*InterruptCtx
			if empty {
				outerInterruptContexts = []*InterruptCtx{}
			}
			projectedRunCtx, projectedInfo, projectedStates, projection, err :=
				projectRunnerCheckpoint(runCtx, &InterruptInfo{
					Data: &ChatModelAgentInterruptInfo{
						Info: info,
						Data: sourceData,
					},
					InterruptContexts: outerInterruptContexts,
				}, sourceID, map[string]core.InterruptState{
					sourceID: {State: sourceData},
				})
			require.NoError(t, err)
			require.NotNil(t, projection)
			roundTrip := &serialization{
				RunCtx:            projectedRunCtx,
				Info:              projectedInfo,
				ProjectionV1:      projection,
				InterruptID2State: projectedStates,
			}
			require.NoError(t, restoreRunnerCheckpointProjection(roundTrip))

			clonedRunCtx := roundTrip.RunCtx
			require.Equal(t, empty, roundTrip.Info.InterruptContexts != nil)
			clonedInfo := roundTrip.Info.Data.(*ChatModelAgentInterruptInfo).Info
			clonedExtra := clonedInfo.State.(*compose.ToolsInterruptAndRerunExtra)
			clonedTypedEvents := clonedRunCtx.Session.TypedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage])

			require.Equal(t, empty, clonedRunCtx.RunPath != nil)
			require.Equal(t, empty, clonedRunCtx.RootInput.Messages != nil)
			require.Equal(t, empty,
				clonedRunCtx.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage]).Messages != nil)
			require.Equal(t, empty, clonedRunCtx.Session.Values != nil)
			require.Equal(t, empty, clonedRunCtx.Session.Events != nil)
			require.Equal(t, empty, clonedRunCtx.Session.LaneEvents.Events != nil)
			require.Equal(t, empty, *clonedTypedEvents != nil)
			require.Equal(t, empty, clonedInfo.BeforeNodes != nil)
			require.Equal(t, empty, clonedInfo.AfterNodes != nil)
			require.Equal(t, empty, clonedInfo.RerunNodes != nil)
			require.Equal(t, empty, clonedInfo.RerunNodesExtra != nil)
			require.Equal(t, empty, clonedInfo.SubGraphs != nil)
			require.Equal(t, empty, clonedInfo.InterruptContexts != nil)
			require.Equal(t, empty, clonedExtra.ToolCalls != nil)
			require.Equal(t, empty, clonedExtra.ExecutedTools != nil)
			require.Equal(t, empty, clonedExtra.ExecutedEnhancedTools != nil)
			require.Equal(t, empty, clonedExtra.RerunTools != nil)
			require.Equal(t, empty, clonedExtra.RerunExtraMap != nil)

			clonedStates := cloneInterruptStateMap(nil)
			if empty {
				clonedStates = cloneInterruptStateMap(map[string]core.InterruptState{})
			}
			require.Equal(t, empty, clonedStates != nil)
		})
	}
}

func TestCheckpointProjectionEventWrapperClones(t *testing.T) {
	t.Run("schema_message", func(t *testing.T) {
		require.Nil(t, cloneAgentEventWrapperForProjection(nil))
		require.Nil(t, cloneAgentEventWrapperForProjection(&agentEventWrapper{}))
	})
	t.Run("agentic_message", func(t *testing.T) {
		require.Nil(t, cloneTypedAgentEventWrapperForProjection(nil))
		require.Nil(t, cloneTypedAgentEventWrapperForProjection(
			&typedAgentEventWrapper[*schema.AgenticMessage]{}))
	})
}

func TestRunnerCheckpointProjectionAgenticRootInputExplicitNilRoundTrip(t *testing.T) {
	canonical := schema.UserAgenticMessage("canonical")
	typedSetMessageID(canonical, "agentic-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addAgenticMessage(nil, 0, canonical)

	inline := schema.UserAgenticMessage("inline")
	runCtx := &runContext{AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{nil, inline, canonical},
	}}
	cloned := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(cloned, index, projection)
	rootInput := cloned.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage])
	require.Nil(t, rootInput.Messages)
	require.Len(t, projection.RunCtxRefs, 3)
	require.True(t, projection.RunCtxRefs[0].IsNil)
	require.Same(t, inline, projection.RunCtxRefs[1].AgenticInline)
	require.Empty(t, projection.RunCtxRefs[1].Source.MessageID)
	require.Nil(t, projection.RunCtxRefs[2].AgenticInline)
	require.Equal(t, "agentic-message", projection.RunCtxRefs[2].Source.MessageID)

	var encoded bytes.Buffer
	require.NoError(t, gob.NewEncoder(&encoded).Encode(projection))
	var persisted checkpointProjectionV1
	require.NoError(t, gob.NewDecoder(&encoded).Decode(&persisted))
	require.True(t, persisted.RunCtxRefs[0].IsNil)

	require.NoError(t, hydrateRunContextMessages(cloned, persisted.RunCtxRefs,
		persisted.RunCtxRefCount, index))
	require.Equal(t, []*schema.AgenticMessage{nil, inline, canonical}, rootInput.Messages)
}

func TestRunnerCheckpointProjectionRestoresCancelInput(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:         "projection-cancel-input",
		Cancel:       true,
		PayloadField: "content",
		PayloadSize:  320 << 10,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	t.Logf("cancel checkpoint bytes: %d", len(raw))
	require.Less(t, len(raw), 2<<20)
	var persisted serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))
	require.NotNil(t, persisted.ProjectionV1)
	sourceID := persisted.ProjectionV1.SourceInterruptID
	sourceState, exists := persisted.InterruptID2State[sourceID]
	require.True(t, exists)
	sourceData, ok := sourceState.State.([]byte)
	require.True(t, ok)
	countInputs := func(data []byte) (visited, projected int) {
		require.NoError(t, compose.WalkCheckpointValues(data, &gobSerializer{},
			func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
				if location.Kind != compose.CheckpointValueInput {
					return nil
				}
				visited++
				if _, ok := value.(*checkpointMessagePlaceholderV1); ok {
					projected++
				}
				if _, ok := value.(*checkpointMessageSlicePlaceholderV1); ok {
					projected++
				}
				return nil
			}))
		return visited, projected
	}
	visitedInputs, projectedInputs := countInputs(sourceData)
	require.Positive(t, visitedInputs, "fixture must contain a persisted input")
	require.Positive(t, projectedInputs,
		"eligible cancel input must be projected after logical restoration succeeds")

	require.NoError(t, restoreRunnerCheckpointProjection(&persisted))
	sourceState, exists = persisted.InterruptID2State[sourceID]
	require.True(t, exists)
	sourceData, ok = sourceState.State.([]byte)
	require.True(t, ok)
	visitedInputs, projectedInputs = countInputs(sourceData)
	require.Positive(t, visitedInputs, "restored checkpoint must contain a persisted input")
	require.Zero(t, projectedInputs, "restored checkpoint input must not retain projection placeholders")

	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           newCheckpointCompatCancelResumeAgent(t),
		CheckPointStore: store,
	})
	iter, err := runner.Resume(context.Background(), spec.Name)
	require.NoError(t, err)
	var completedEvents int
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, event.Err)
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, messageErr := event.Output.MessageOutput.GetMessage()
		require.NoError(t, messageErr)
		if message != nil && message.Role == schema.Assistant && message.Content == "completed" {
			completedEvents++
		}
	}
	require.Equal(t, 1, completedEvents)
}

func TestRunnerCheckpointProjectionRestoresEventsAndLanes(t *testing.T) {
	message := schema.AssistantMessage("projected", nil)
	typedSetMessageID(message, "event-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, message)

	regular := &agentEventWrapper{AgentEvent: EventFromMessage(message, nil, schema.Assistant, "")}
	streamingEvent := EventFromMessage(nil,
		schema.StreamReaderFromArray([]*schema.Message{message}), schema.Assistant, "")
	streaming := &agentEventWrapper{AgentEvent: streamingEvent}
	parentLane := &laneEvents{Events: []*agentEventWrapper{
		{AgentEvent: EventFromMessage(message, nil, schema.Assistant, "")},
	}}
	runCtx := &runContext{
		Session: &runSession{
			Values:    map[string]any{},
			valuesMtx: &sync.Mutex{},
			Events:    []*agentEventWrapper{regular},
			LaneEvents: &laneEvents{
				Events: []*agentEventWrapper{streaming},
				Parent: parentLane,
			},
		},
	}
	cloned := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(cloned, index, projection)
	require.Len(t, projection.RunCtxRefs, 3)

	require.NoError(t, hydrateRunContextMessages(cloned, projection.RunCtxRefs,
		projection.RunCtxRefCount, index))
	require.Equal(t, message, cloned.Session.Events[0].Output.MessageOutput.Message)
	laneMessage, err := cloned.Session.LaneEvents.Events[0].Output.MessageOutput.GetMessage()
	require.NoError(t, err)
	require.Equal(t, message, laneMessage)
	require.Equal(t, message,
		cloned.Session.LaneEvents.Parent.Events[0].Output.MessageOutput.Message)

	liveMessage, err := runCtx.Session.LaneEvents.Events[0].Output.MessageOutput.GetMessage()
	require.NoError(t, err)
	require.Equal(t, message, liveMessage,
		"projection must leave an independently readable stream on the live event")
}

func TestRunnerCheckpointProjectionAgenticComposeValues(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:         "projection-agentic-compose-values",
		Cancel:       true,
		PayloadField: "content",
		PayloadSize:  1024,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)
	sourceID := outer.ProjectionV1.SourceInterruptID
	_, exists := outer.InterruptID2State[sourceID]
	require.True(t, exists)
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))
	source, exists := outer.InterruptID2State[sourceID]
	require.True(t, exists)
	sourceData, ok := source.State.([]byte)
	require.True(t, ok)

	canonical := schema.UserAgenticMessage("canonical")
	typedSetMessageID(canonical, "agentic-canonical")
	inline := schema.UserAgenticMessage("inline")
	var fixture struct {
		Inputs map[string]any
		State  any
	}
	require.NoError(t, gob.NewDecoder(bytes.NewReader(sourceData)).Decode(&fixture))
	fixture.Inputs = map[string]any{
		"agentic-input": []*schema.AgenticMessage{inline, canonical},
	}
	fixture.State = &agenticState{Messages: []*schema.AgenticMessage{canonical}}
	var fixtureData bytes.Buffer
	require.NoError(t, gob.NewEncoder(&fixtureData).Encode(&fixture))

	var preparedInputs int
	var valueLocation compose.CheckpointValueLocation
	prepared, err := compose.TransformCheckpointValues(fixtureData.Bytes(), &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind != compose.CheckpointValueInput {
				return value, false, nil
			}
			valueLocation = location
			preparedInputs++
			return value, true, nil
		})
	require.NoError(t, err)
	require.Equal(t, 1, preparedInputs)

	index, err := buildCheckpointProjectionIndex(prepared)
	require.NoError(t, err)

	projectedData, changed, err := projectComposeCheckpointValues(prepared, index)
	require.NoError(t, err)
	require.True(t, changed)
	var projectedValue any
	require.NoError(t, compose.WalkCheckpointValues(projectedData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if reflect.DeepEqual(location, valueLocation) {
				projectedValue = value
			}
			return nil
		}))
	placeholder, ok := projectedValue.(*checkpointAgenticMessageSlicePlaceholderV1)
	require.True(t, ok)
	require.Len(t, placeholder.Entries, 2)
	require.Equal(t, inline, placeholder.Entries[0].Inline)
	require.NotNil(t, placeholder.Entries[1].Source)
	require.Nil(t, placeholder.Entries[1].Inline)

	hydrated, err := hydrateComposeCheckpointValues(projectedData, index)
	require.NoError(t, err)
	var restored []*schema.AgenticMessage
	require.NoError(t, compose.WalkCheckpointValues(hydrated, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if reflect.DeepEqual(location, valueLocation) {
				restored, _ = value.([]*schema.AgenticMessage)
			}
			return nil
		}))
	require.Equal(t, []*schema.AgenticMessage{inline, canonical}, restored)
	require.NotSame(t, canonical, restored[1])
}

func TestComposeCheckpointValuesEquivalent(t *testing.T) {
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         "logical-compose-equivalence",
		Cancel:       true,
		PayloadField: "content",
		PayloadSize:  1024,
	})
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)
	sourceID := outer.ProjectionV1.SourceInterruptID
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))
	sourceData, ok := outer.InterruptID2State[sourceID].State.([]byte)
	require.True(t, ok)

	reencoded, err := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation,
			value any) (any, bool, error) {
			return value, location.Kind == compose.CheckpointValueInput, nil
		})
	require.NoError(t, err)
	equivalent, err := composeCheckpointValuesEquivalent(sourceData, reencoded)
	require.NoError(t, err)
	require.True(t, equivalent)

	different, err := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation,
			value any) (any, bool, error) {
			if location.Kind == compose.CheckpointValueInput {
				return schema.UserMessage("different"), true, nil
			}
			return value, false, nil
		})
	require.NoError(t, err)
	equivalent, err = composeCheckpointValuesEquivalent(sourceData, different)
	require.NoError(t, err)
	require.False(t, equivalent)

	_, err = composeCheckpointValuesEquivalent(sourceData, []byte("invalid"))
	require.ErrorContains(t, err, "failed to decode checkpoint for inspection")

	var canonical *schema.Message
	require.NoError(t, compose.WalkCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if location.Kind != compose.CheckpointValueState {
				return nil
			}
			state, ok := value.(*State)
			if ok && len(state.Messages) > 0 {
				canonical = state.Messages[len(state.Messages)-1]
			}
			return nil
		}))
	require.NotNil(t, canonical)
	typedSetMessageID(canonical, "pointer-map-message")
	canonical.Content = strings.Repeat("pointer-map-payload", 32<<10)
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, canonical)

	var replacedInput bool
	pointerMapData, err := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation,
			value any) (any, bool, error) {
			switch {
			case location.Kind == compose.CheckpointValueState:
				return &checkpointProjectionPointerMapState{
					Values: map[*checkpointProjectionPointerKey]string{
						{ID: "key"}: "value",
					},
				}, true, nil
			case location.Kind == compose.CheckpointValueInput && !replacedInput:
				replacedInput = true
				return canonical, true, nil
			default:
				return value, false, nil
			}
		})
	require.NoError(t, err)
	require.True(t, replacedInput)

	for i := 0; i < 20; i++ {
		projected, changed, projectErr := projectComposeCheckpointValues(pointerMapData, index)
		require.NoError(t, projectErr)
		require.True(t, changed, "iteration %d must retain projection eligibility", i)
		require.Less(t, len(projected), len(pointerMapData)-len(canonical.Content)/2,
			"iteration %d must retain the projection size benefit", i)

		restored, restoreErr := hydrateComposeCheckpointValues(projected, index)
		require.NoError(t, restoreErr)
		equivalent, compareErr := composeCheckpointValuesEquivalent(pointerMapData, restored)
		require.NoError(t, compareErr)
		require.True(t, equivalent)
	}
}

func TestComposeCheckpointProjectionRetainsNaNMapState(t *testing.T) {
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         "projection-nan-map-state",
		Cancel:       true,
		PayloadField: "content",
		PayloadSize:  1024,
	})
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)
	sourceID := outer.ProjectionV1.SourceInterruptID
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))
	sourceData, ok := outer.InterruptID2State[sourceID].State.([]byte)
	require.True(t, ok)

	var canonical *schema.Message
	require.NoError(t, compose.WalkCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if location.Kind != compose.CheckpointValueState {
				return nil
			}
			state, stateOK := value.(*State)
			if stateOK && len(state.Messages) > 0 {
				canonical = state.Messages[len(state.Messages)-1]
			}
			return nil
		}))
	require.NotNil(t, canonical)
	typedSetMessageID(canonical, "nan-map-message")
	canonical.Content = strings.Repeat("nan-map-payload", 32<<10)

	nan64 := math.Float64frombits(0x7ff8000000000001)
	nan32 := math.Float32frombits(0x7fc00001)
	expectedState := &checkpointProjectionNaNMapState{
		Values: make(map[checkpointProjectionNaNMapKey]int, 257),
	}
	for i := 0; i < 257; i++ {
		expectedState.Values[checkpointProjectionNaNMapKey{
			ID:      i,
			Float:   nan64,
			Complex: complex(float64(i), nan64),
			Nested:  [1]float32{nan32},
		}] = i * 2
	}

	replacedInput := false
	nanMapData, err := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation,
			value any) (any, bool, error) {
			switch {
			case location.Kind == compose.CheckpointValueState:
				return expectedState, true, nil
			case location.Kind == compose.CheckpointValueInput && !replacedInput:
				replacedInput = true
				return canonical, true, nil
			default:
				return value, false, nil
			}
		})
	require.NoError(t, err)
	require.True(t, replacedInput)

	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, canonical)
	for i := 0; i < 10; i++ {
		projected, changed, projectErr := projectComposeCheckpointValues(nanMapData, index)
		require.NoError(t, projectErr)
		require.True(t, changed, "iteration %d must retain projection eligibility", i)
		require.Less(t, len(projected), len(nanMapData)-len(canonical.Content)/2,
			"iteration %d must retain the projection size benefit", i)

		hydrated, hydrateErr := hydrateComposeCheckpointValues(projected, index)
		require.NoError(t, hydrateErr)
		equivalent, compareErr := composeCheckpointValuesEquivalent(nanMapData, hydrated)
		require.NoError(t, compareErr)
		require.True(t, equivalent)

		var restoredState *checkpointProjectionNaNMapState
		require.NoError(t, compose.WalkCheckpointValues(hydrated, &gobSerializer{},
			func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
				if location.Kind == compose.CheckpointValueState {
					restoredState, _ = value.(*checkpointProjectionNaNMapState)
				}
				return nil
			}))
		require.NotNil(t, restoredState)
		require.Len(t, restoredState.Values, len(expectedState.Values))
		require.True(t, gobSemanticEqual(expectedState.Values, restoredState.Values))
	}
}

func TestGobSemanticEqual(t *testing.T) {
	t.Run("direct_key_types", func(t *testing.T) {
		type nestedScalar struct {
			Enabled bool
			Code    int32
		}
		type scalarComposite struct {
			ID     uint64
			Labels [2]string
			Nested nestedScalar
		}
		tests := []struct {
			name  string
			left  any
			right any
		}{
			{name: "bool", left: map[bool]int{false: 0, true: 1},
				right: map[bool]int{true: 1, false: 0}},
			{name: "signed", left: map[int64]int{-1: 1, 2: 2},
				right: map[int64]int{2: 2, -1: 1}},
			{name: "unsigned", left: map[uint64]int{1: 1, 2: 2},
				right: map[uint64]int{2: 2, 1: 1}},
			{name: "string", left: map[string]int{"a": 1, "b": 2},
				right: map[string]int{"b": 2, "a": 1}},
			{name: "array", left: map[[2]int]int{{1, 2}: 1, {3, 4}: 2},
				right: map[[2]int]int{{3, 4}: 2, {1, 2}: 1}},
			{
				name: "struct",
				left: map[scalarComposite]int{
					{ID: 1, Labels: [2]string{"a", "b"},
						Nested: nestedScalar{Enabled: true, Code: 12}}: 1,
					{ID: 2, Labels: [2]string{"c", "d"}}: 2,
				},
				right: map[scalarComposite]int{
					{ID: 2, Labels: [2]string{"c", "d"}}: 2,
					{ID: 1, Labels: [2]string{"a", "b"},
						Nested: nestedScalar{Enabled: true, Code: 12}}: 1,
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				require.True(t, gobSemanticEqual(tt.left, tt.right))
			})
		}
	})

	t.Run("nan_keys_match_by_bits", func(t *testing.T) {
		nan32 := math.Float32frombits(0x7fc00001)
		otherNaN32 := math.Float32frombits(0x7fc00002)
		nan64 := math.Float64frombits(0x7ff8000000000001)
		otherNaN64 := math.Float64frombits(0x7ff8000000000002)

		require.True(t, gobSemanticEqual(
			map[float32]string{nan32: "value"},
			map[float32]string{nan32: "value"}))
		require.False(t, gobSemanticEqual(
			map[float32]string{nan32: "value"},
			map[float32]string{otherNaN32: "value"}))

		floatLeft := make(map[float64]string)
		floatLeft[nan64] = "first"
		floatLeft[nan64] = "second"
		floatRight := make(map[float64]string)
		floatRight[nan64] = "second"
		floatRight[nan64] = "first"
		require.True(t, gobSemanticEqual(floatLeft, floatRight))
		require.Len(t, floatLeft, 2)
		require.Len(t, floatRight, 2)

		mismatchedValues := make(map[float64]string)
		mismatchedValues[nan64] = "first"
		mismatchedValues[nan64] = "third"
		require.False(t, gobSemanticEqual(floatLeft, mismatchedValues))

		differentBits := map[float64]string{otherNaN64: "first"}
		require.False(t, gobSemanticEqual(
			map[float64]string{nan64: "first"}, differentBits))

		require.False(t, gobSemanticEqual(
			map[float64]string{1.5: "value"},
			map[float64]string{nan64: "value"}))

		complexNaN32 := complex(nan32, float32(7))
		otherComplexNaN32 := complex(otherNaN32, float32(7))
		require.True(t, gobSemanticEqual(
			map[complex64]string{complexNaN32: "value"},
			map[complex64]string{complexNaN32: "value"}))
		require.False(t, gobSemanticEqual(
			map[complex64]string{complexNaN32: "value"},
			map[complex64]string{otherComplexNaN32: "value"}))

		complexNaN := complex(nan64, 7)
		require.True(t, gobSemanticEqual(
			map[complex128]string{complexNaN: "value"},
			map[complex128]string{complexNaN: "value"}))

		type compositeKey struct {
			Complex complex128
			Nested  [1]float32
		}
		composite := compositeKey{
			Complex: complex(3, nan64),
			Nested:  [1]float32{nan32},
		}
		require.True(t, gobSemanticEqual(
			map[compositeKey]string{composite: "value"},
			map[compositeKey]string{composite: "value"}))
		require.True(t, gobSemanticEqual(
			map[[1]float64]string{{nan64}: "value"},
			map[[1]float64]string{{nan64}: "value"}))
		require.True(t, gobSemanticEqual(
			map[any]string{nan64: "value"},
			map[any]string{nan64: "value"}))

		negativeZero := math.Copysign(0, -1)
		require.True(t, gobSemanticEqual(
			map[float64]string{0: "value"},
			map[float64]string{negativeZero: "value"}))
		require.True(t, gobSemanticEqual(
			struct{ Value float64 }{Value: nan64},
			struct{ Value float64 }{Value: nan64}))
		require.False(t, gobSemanticEqual(
			struct{ Value float64 }{Value: nan64},
			struct{ Value float64 }{Value: otherNaN64}))
	})

	t.Run("nan_keyed_maps_preserve_every_entry", func(t *testing.T) {
		nan64 := math.Float64frombits(0x7ff8000000000001)
		nan32 := math.Float32frombits(0x7fc00001)
		for _, cardinality := range []int{1, 17, 257} {
			left := make(map[checkpointProjectionNaNMapKey]int, cardinality)
			right := make(map[checkpointProjectionNaNMapKey]int, cardinality)
			for i := 0; i < cardinality; i++ {
				key := checkpointProjectionNaNMapKey{
					ID:      i,
					Float:   nan64,
					Complex: complex(float64(i), nan64),
					Nested:  [1]float32{nan32},
				}
				left[key] = i
				right[key] = i
			}

			require.True(t, gobSemanticEqual(left, right))
			right[checkpointProjectionNaNMapKey{
				ID:      cardinality,
				Float:   nan64,
				Complex: complex(float64(cardinality), nan64),
				Nested:  [1]float32{nan32},
			}] = cardinality
			require.False(t, gobSemanticEqual(left, right))
		}
	})

	t.Run("large_scalar_map_preserves_entries", func(t *testing.T) {
		for _, cardinality := range []int{1, 17, 257, 4096} {
			left := make(map[int]int, cardinality)
			right := make(map[int]int, cardinality)
			for i := 0; i < cardinality; i++ {
				left[i] = i * 2
				right[i] = i * 2
			}

			require.True(t, gobSemanticEqual(left, right))
		}

		left := make(map[int]int, 4096)
		right := make(map[int]int, 4096)
		for i := 0; i < 4096; i++ {
			left[i] = i
			right[i] = i
		}
		right[2048] = -1
		require.False(t, gobSemanticEqual(left, right))
		delete(right, 2048)
		right[4096] = 4096
		require.False(t, gobSemanticEqual(left, right))
	})

	t.Run("large_finite_numeric_maps_preserve_entries", func(t *testing.T) {
		type finiteComposite struct {
			ID      int
			Float   float64
			Complex complex64
			Nested  [2]float32
		}
		tests := []struct {
			name  string
			build func(int) (any, any)
		}{
			{
				name: "float32",
				build: func(cardinality int) (any, any) {
					left := make(map[float32]int, cardinality)
					right := make(map[float32]int, cardinality)
					for i := 0; i < cardinality; i++ {
						key := float32(i) + 0.25
						left[key], right[key] = i, i
					}
					return left, right
				},
			},
			{
				name: "float64",
				build: func(cardinality int) (any, any) {
					left := make(map[float64]int, cardinality)
					right := make(map[float64]int, cardinality)
					for i := 0; i < cardinality; i++ {
						key := float64(i) + 0.25
						left[key], right[key] = i, i
					}
					return left, right
				},
			},
			{
				name: "complex64",
				build: func(cardinality int) (any, any) {
					left := make(map[complex64]int, cardinality)
					right := make(map[complex64]int, cardinality)
					for i := 0; i < cardinality; i++ {
						key := complex(float32(i)+0.25, float32(i)+0.5)
						left[key], right[key] = i, i
					}
					return left, right
				},
			},
			{
				name: "complex128",
				build: func(cardinality int) (any, any) {
					left := make(map[complex128]int, cardinality)
					right := make(map[complex128]int, cardinality)
					for i := 0; i < cardinality; i++ {
						key := complex(float64(i)+0.25, float64(i)+0.5)
						left[key], right[key] = i, i
					}
					return left, right
				},
			},
			{
				name: "composite",
				build: func(cardinality int) (any, any) {
					left := make(map[finiteComposite]int, cardinality)
					right := make(map[finiteComposite]int, cardinality)
					for i := 0; i < cardinality; i++ {
						key := finiteComposite{
							ID:      i,
							Float:   float64(i) + 0.25,
							Complex: complex(float32(i)+0.5, float32(i)+0.75),
							Nested:  [2]float32{float32(i) + 1.25, float32(i) + 1.5},
						}
						left[key], right[key] = i, i
					}
					return left, right
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				for _, cardinality := range []int{1, 17, 257, 4096} {
					left, right := tt.build(cardinality)
					require.True(t, gobSemanticEqual(left, right),
						"cardinality %d", cardinality)
				}
			})
		}
	})

	t.Run("pointer_and_interface_keys_use_semantic_matching", func(t *testing.T) {
		leftKey := &checkpointProjectionPointerKey{ID: "key"}
		rightKey := &checkpointProjectionPointerKey{ID: "key"}
		require.True(t, gobSemanticEqual(
			map[*checkpointProjectionPointerKey]string{leftKey: "value"},
			map[*checkpointProjectionPointerKey]string{rightKey: "value"}))

		require.False(t, gobSemanticEqual(
			map[*checkpointProjectionPointerKey]string{leftKey: "value"},
			map[*checkpointProjectionPointerKey]string{rightKey: "different"}))

		type interfaceKey struct {
			Value any
		}
		require.True(t, gobSemanticEqual(
			map[interfaceKey]string{{Value: leftKey}: "value"},
			map[interfaceKey]string{{Value: rightKey}: "value"}))

		type pointerCompositeKey struct {
			Values [1]*checkpointProjectionPointerKey
		}
		require.True(t, gobSemanticEqual(
			map[pointerCompositeKey]string{
				{Values: [1]*checkpointProjectionPointerKey{leftKey}}: "value",
			},
			map[pointerCompositeKey]string{
				{Values: [1]*checkpointProjectionPointerKey{rightKey}}: "value",
			}))

		require.False(t, gobSemanticEqual(
			map[any]string{int(1): "value"},
			map[any]string{int64(1): "value"}))

		require.True(t, gobSemanticEqual(
			map[*checkpointProjectionPointerKey]string{
				{ID: "duplicate"}: "first",
				{ID: "duplicate"}: "second",
			},
			map[*checkpointProjectionPointerKey]string{
				{ID: "duplicate"}: "second",
				{ID: "duplicate"}: "first",
			}))
	})

	t.Run("semantic_pointer_map_signed_zero_scaling", func(t *testing.T) {
		signedZeroValue := func(index int) [12]float64 {
			var value [12]float64
			for bit := range value {
				if index&(1<<bit) != 0 {
					value[bit] = math.Copysign(0, -1)
				}
			}
			return value
		}
		candidateBounds := make(map[int]int)
		for _, size := range []int{1_000, 4_000} {
			left := make(map[*checkpointProjectionPointerKey][12]float64, size)
			right := make(map[*checkpointProjectionPointerKey][12]float64, size)
			for i := 0; i < size; i++ {
				value := signedZeroValue(i)
				left[&checkpointProjectionPointerKey{ID: "duplicate"}] = value
				right[&checkpointProjectionPointerKey{ID: "duplicate"}] = value
			}
			leftEntries := gobSemanticMapEntries(reflect.ValueOf(left))
			buckets, ok := gobSemanticMapEntryBuckets(
				gobSemanticMapEntries(reflect.ValueOf(right)))
			require.True(t, ok)
			require.Len(t, buckets, size)
			for _, candidates := range buckets {
				require.Len(t, candidates, 1)
			}
			candidateBounds[size], ok = gobSemanticMapCandidateBound(leftEntries, buckets)
			require.True(t, ok)
			require.Equal(t, size, candidateBounds[size])
			require.True(t, gobSemanticEqual(left, right))
		}
		require.NoError(t, validateGobSemanticMapCandidateGrowth(
			candidateBounds[1_000], candidateBounds[4_000], 8))
		require.EqualError(t, validateGobSemanticMapCandidateGrowth(
			1_000*1_000, 4_000*4_000, 8),
			"semantic map candidate bound 16000000 is not below 8x baseline 1000000")
	})

	t.Run("value_kinds_preserve_semantics", func(t *testing.T) {
		require.True(t, gobSemanticEqual(nil, nil))
		require.False(t, gobSemanticEqual(nil, "value"))
		require.True(t, gobSemanticEqual(true, true))
		require.True(t, gobSemanticEqual(int64(-1), int64(-1)))
		require.True(t, gobSemanticEqual(uint64(1), uint64(1)))
		require.True(t, gobSemanticEqual(float64(1.5), float64(1.5)))
		require.True(t, gobSemanticEqual(complex128(1+2i), complex128(1+2i)))
		require.False(t, gobSemanticEqual(float64(0), math.Copysign(0, -1)))
		require.False(t, gobSemanticEqual(
			complex(float64(0), float64(0)),
			complex(math.Copysign(0, -1), float64(0))))
		require.True(t, gobSemanticEqual("value", "value"))

		channel := make(chan int)
		otherChannel := make(chan int)
		require.True(t, gobSemanticEqual(channel, channel))
		require.False(t, gobSemanticEqual(channel, otherChannel))

		type interfaceValue struct {
			Value any
		}
		require.True(t, gobSemanticEqual(
			interfaceValue{Value: "value"}, interfaceValue{Value: "value"}))
		require.True(t, gobSemanticEqual(interfaceValue{}, interfaceValue{}))
		require.False(t, gobSemanticEqual(
			interfaceValue{}, interfaceValue{Value: "value"}))

		require.True(t, gobSemanticEqual([2]int{1, 2}, [2]int{1, 2}))
		require.False(t, gobSemanticEqual([2]int{1, 2}, [2]int{1, 3}))
		require.True(t, gobSemanticEqual([]int{1, 2}, []int{1, 2}))
		require.False(t, gobSemanticEqual([]int{1, 2}, []int{1}))
		require.False(t, gobSemanticEqual([]int{1, 2}, []int{1, 3}))
	})

	t.Run("nil_and_concrete_types_remain_distinct", func(t *testing.T) {
		require.False(t, gobSemanticEqual([]string(nil), []string{}))
		require.False(t, gobSemanticEqual(map[string]string(nil), map[string]string{}))
		require.False(t, gobSemanticEqual(any(int(1)), any(int64(1))))

		var nilPointer *checkpointProjectionPointerKey
		require.True(t, gobSemanticEqual(nilPointer, nilPointer))
		require.False(t, gobSemanticEqual(
			nilPointer, &checkpointProjectionPointerKey{}))
		require.True(t, gobSemanticEqual(
			map[string]string(nil), map[string]string(nil)))
		require.False(t, gobSemanticEqual(
			map[string]string{"key": "value"}, map[string]string{}))
	})

	t.Run("cycles_terminate", func(t *testing.T) {
		type cyclicValue struct {
			Value string
			Next  *cyclicValue
		}
		leftCycle := &cyclicValue{Value: "same"}
		leftCycle.Next = leftCycle
		rightCycle := &cyclicValue{Value: "same"}
		rightCycle.Next = rightCycle
		require.True(t, gobSemanticEqual(leftCycle, rightCycle))
		require.True(t, gobSemanticEqual(
			map[*cyclicValue]string{leftCycle: "value"},
			map[*cyclicValue]string{rightCycle: "value"}))
		rightCycle.Value = "different"
		require.False(t, gobSemanticEqual(leftCycle, rightCycle))
		require.False(t, gobSemanticEqual(
			map[*cyclicValue]string{leftCycle: "value"},
			map[*cyclicValue]string{rightCycle: "value"}))

		leftSliceCycle := make([]any, 1)
		leftSliceCycle[0] = leftSliceCycle
		rightSliceCycle := make([]any, 1)
		rightSliceCycle[0] = rightSliceCycle
		require.True(t, gobSemanticEqual(leftSliceCycle, rightSliceCycle))

		leftMapCycle := make(map[string]any)
		leftMapCycle["self"] = leftMapCycle
		rightMapCycle := make(map[string]any)
		rightMapCycle["self"] = rightMapCycle
		require.True(t, gobSemanticEqual(leftMapCycle, rightMapCycle))
	})

	t.Run("unsupported_values_fail_closed", func(t *testing.T) {
		left := func() {}
		right := left
		require.False(t, gobSemanticEqual(left, right))
		var leftNil, rightNil func()
		require.True(t, gobSemanticEqual(leftNil, rightNil))
	})
}

func gobSemanticMapCandidateBound(entries []gobSemanticMapEntry,
	buckets map[string][]gobSemanticMapEntry) (int, bool) {
	bound := 0
	for _, entry := range entries {
		digest, ok := gobSemanticMapEntryDigest(entry)
		if !ok {
			return 0, false
		}
		candidates, exists := buckets[digest]
		if !exists {
			return 0, false
		}
		bound += len(candidates)
	}
	return bound, true
}

func validateGobSemanticMapCandidateGrowth(small, large, limit int) error {
	if small <= 0 || large <= 0 || limit <= 1 {
		return fmt.Errorf("semantic map candidate bound inputs must be positive with limit above one")
	}
	if large >= small*limit {
		return fmt.Errorf("semantic map candidate bound %d is not below %dx baseline %d",
			large, limit, small)
	}
	return nil
}

func BenchmarkGobSemanticEqualPointerMap(b *testing.B) {
	for _, size := range []int{1_000, 4_000} {
		left := make(map[*checkpointProjectionPointerKey]int, size)
		right := make(map[*checkpointProjectionPointerKey]int, size)
		for i := 0; i < size; i++ {
			id := strconv.Itoa(i)
			left[&checkpointProjectionPointerKey{ID: id}] = i
			right[&checkpointProjectionPointerKey{ID: id}] = i
		}
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				if !gobSemanticEqual(left, right) {
					b.Fatal("semantically equal pointer maps did not match")
				}
			}
		})
	}
}

func TestGobSemanticMapKeyDigest(t *testing.T) {
	type interfaceHolder struct {
		Value any
	}
	type key struct {
		Bool       bool
		Signed     int64
		Unsigned   uint64
		Float32    float32
		Float64    float64
		Complex64  complex64
		Complex128 complex128
		String     string
		Array      [1]int
		Slice      []string
	}
	value := key{
		Bool:       true,
		Signed:     -1,
		Unsigned:   2,
		Float32:    math.Float32frombits(0x7fc00001),
		Float64:    math.Float64frombits(0x7ff8000000000001),
		Complex64:  complex(math.Float32frombits(0x7fc00001), 3),
		Complex128: complex(4, math.Float64frombits(0x7ff8000000000001)),
		String:     "value",
		Array:      [1]int{5},
		Slice:      []string{"item"},
	}
	digest, ok := gobSemanticMapKeyDigest(reflect.ValueOf(value))
	require.True(t, ok)
	require.NotEmpty(t, digest)

	positiveZero, ok := gobSemanticMapKeyDigest(reflect.ValueOf(float64(0)))
	require.True(t, ok)
	negativeZero, ok := gobSemanticMapKeyDigest(
		reflect.ValueOf(math.Copysign(0, -1)))
	require.True(t, ok)
	require.Equal(t, positiveZero, negativeZero)

	float32PositiveZero, ok := gobSemanticMapKeyDigest(reflect.ValueOf(float32(0)))
	require.True(t, ok)
	float32NegativeZero, ok := gobSemanticMapKeyDigest(
		reflect.ValueOf(math.Float32frombits(1 << 31)))
	require.True(t, ok)
	require.Equal(t, float32PositiveZero, float32NegativeZero)

	negativeFloat32 := math.Float32frombits(1 << 31)
	negativeFloat64 := math.Float64frombits(1 << 63)
	signedZeroValues := []struct {
		name     string
		positive any
		negative any
	}{
		{name: "float32", positive: float32(0), negative: negativeFloat32},
		{name: "float64", positive: float64(0), negative: negativeFloat64},
		{
			name:     "complex64_real",
			positive: complex(float32(0), float32(1)),
			negative: complex(negativeFloat32, float32(1)),
		},
		{
			name:     "complex64_imaginary",
			positive: complex(float32(1), float32(0)),
			negative: complex(float32(1), negativeFloat32),
		},
		{
			name:     "complex128_real",
			positive: complex(float64(0), float64(1)),
			negative: complex(negativeFloat64, float64(1)),
		},
		{
			name:     "complex128_imaginary",
			positive: complex(float64(1), float64(0)),
			negative: complex(float64(1), negativeFloat64),
		},
	}
	for _, tt := range signedZeroValues {
		t.Run(tt.name, func(t *testing.T) {
			positiveKey, keyOK := gobSemanticMapKeyDigest(reflect.ValueOf(tt.positive))
			require.True(t, keyOK)
			negativeKey, keyOK := gobSemanticMapKeyDigest(reflect.ValueOf(tt.negative))
			require.True(t, keyOK)
			require.Equal(t, positiveKey, negativeKey)

			positiveValue, valueOK := gobSemanticMapValueDigest(reflect.ValueOf(tt.positive))
			require.True(t, valueOK)
			negativeValue, valueOK := gobSemanticMapValueDigest(reflect.ValueOf(tt.negative))
			require.True(t, valueOK)
			require.NotEqual(t, positiveValue, negativeValue)
		})
	}

	type nestedSignedZero struct {
		Interface any
		Pointer   *float32
	}
	positiveFloat32 := float32(0)
	negativeFloat32 = math.Float32frombits(1 << 31)
	positiveComposite := nestedSignedZero{
		Interface: [1]complex128{complex(float64(0), float64(1))},
		Pointer:   &positiveFloat32,
	}
	negativeComposite := nestedSignedZero{
		Interface: [1]complex128{complex(negativeFloat64, float64(1))},
		Pointer:   &negativeFloat32,
	}
	positiveCompositeKey, ok := gobSemanticMapKeyDigest(
		reflect.ValueOf(positiveComposite))
	require.True(t, ok)
	negativeCompositeKey, ok := gobSemanticMapKeyDigest(
		reflect.ValueOf(negativeComposite))
	require.True(t, ok)
	require.Equal(t, positiveCompositeKey, negativeCompositeKey)
	positiveCompositeValue, ok := gobSemanticMapValueDigest(
		reflect.ValueOf(positiveComposite))
	require.True(t, ok)
	negativeCompositeValue, ok := gobSemanticMapValueDigest(
		reflect.ValueOf(negativeComposite))
	require.True(t, ok)
	require.NotEqual(t, positiveCompositeValue, negativeCompositeValue)

	var nilPointer *checkpointProjectionPointerKey
	_, ok = gobSemanticMapKeyDigest(reflect.ValueOf(nilPointer))
	require.True(t, ok)
	nilInterface := reflect.ValueOf(interfaceHolder{}).Field(0)
	_, ok = gobSemanticMapKeyDigest(nilInterface)
	require.True(t, ok)
	concreteInterface := reflect.ValueOf(interfaceHolder{Value: "value"}).Field(0)
	_, ok = gobSemanticMapKeyDigest(concreteInterface)
	require.True(t, ok)

	leftCycle := &struct{ Next any }{}
	leftCycle.Next = leftCycle
	rightCycle := &struct{ Next any }{}
	rightCycle.Next = rightCycle
	leftDigest, ok := gobSemanticMapKeyDigest(reflect.ValueOf(leftCycle))
	require.True(t, ok)
	rightDigest, ok := gobSemanticMapKeyDigest(reflect.ValueOf(rightCycle))
	require.True(t, ok)
	require.Equal(t, leftDigest, rightDigest)
	leftValueDigest, ok := gobSemanticMapValueDigest(reflect.ValueOf(leftCycle))
	require.True(t, ok)
	rightValueDigest, ok := gobSemanticMapValueDigest(reflect.ValueOf(rightCycle))
	require.True(t, ok)
	require.Equal(t, leftValueDigest, rightValueDigest)

	_, ok = gobSemanticMapKeyDigest(reflect.ValueOf([]string(nil)))
	require.True(t, ok)
	sliceCycle := make([]any, 1)
	sliceCycle[0] = sliceCycle
	_, ok = gobSemanticMapKeyDigest(reflect.ValueOf(sliceCycle))
	require.True(t, ok)

	_, ok = gobSemanticMapKeyDigest(reflect.Value{})
	require.True(t, ok)
	_, ok = gobSemanticMapKeyDigest(reflect.ValueOf(map[string]int{"key": 1}))
	require.False(t, ok)
	_, ok = gobSemanticMapValueDigest(reflect.ValueOf(map[string]int{"key": 1}))
	require.False(t, ok)
}

func TestRunnerCheckpointProjectionRestoresNestedInterruptInfoState(t *testing.T) {
	canonical := schema.AssistantMessage("canonical", nil)
	typedSetMessageID(canonical, "nested-info-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage([]string{"graph"}, 0, canonical)

	nested := &compose.InterruptInfo{State: &State{
		Messages: []*schema.Message{schema.UserMessage("inline"), canonical},
	}}
	outer := &compose.InterruptInfo{State: nested}
	projection := &checkpointProjectionV1{}
	projectComposeInterruptInfoMessages(outer, nil, index, projection)
	require.Empty(t, projection.InfoRefs)
	require.IsType(t, &checkpointInterruptInfoPlaceholderV1{}, outer.State)

	info := &InterruptInfo{Data: &ChatModelAgentInterruptInfo{Info: outer}}
	require.NoError(t, hydrateInterruptInfoMessages(info, projection.InfoRefs,
		projection.InfoRefCount, index))
	require.NoError(t, hydrateInterruptInfoContextPrefixes(info, index))
	restored, ok := outer.State.(*compose.InterruptInfo)
	require.True(t, ok)
	state, ok := restored.State.(*State)
	require.True(t, ok)
	require.Equal(t, "inline", state.Messages[0].Content)
	require.Equal(t, canonical, state.Messages[1])
	require.NotSame(t, canonical, state.Messages[1])
}

func TestRunnerCheckpointProjectionRestoresContextOnlyNestedInterruptInfo(t *testing.T) {
	source := &InterruptCtx{
		ID:          "leaf",
		Address:     Address{{Type: AddressSegmentTool, ID: "leaf"}},
		Info:        "leaf-info",
		IsRootCause: true,
		Parent: &InterruptCtx{
			ID:      "parent",
			Address: Address{{Type: AddressSegmentAgent, ID: "child"}},
			Info:    "parent-info",
		},
	}
	index := newCheckpointProjectionIndex(
		checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
	index.addInterruptInfo(canonicalCheckpointInterruptInfo{
		contexts: []*InterruptCtx{source},
	})
	prefixed := func() *InterruptCtx {
		return prependInterruptContextAddresses([]*InterruptCtx{source}, Address{{
			Type: AddressSegmentAgent,
			ID:   "outer",
		}})[0]
	}
	nested := func() *compose.InterruptInfo {
		return &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{prefixed()}}
	}
	outer := &compose.InterruptInfo{
		State: nested(),
		RerunNodesExtra: map[string]any{
			"nested": nested(),
		},
		InterruptContexts: []*InterruptCtx{{
			ID:      "outer-context",
			Address: Address{{Type: AddressSegmentAgent, ID: "outer"}},
			Info:    nested(),
		}},
	}
	want := cloneComposeInterruptInfoForProjection(outer)

	projectComposeInterruptContextPrefixes(outer, index)
	require.Equal(t, 3, countComposeInterruptContextRefs(outer))
	require.IsType(t, &compose.InterruptInfo{}, outer.State)
	require.IsType(t, &compose.InterruptInfo{}, outer.RerunNodesExtra["nested"])
	require.IsType(t, &compose.InterruptInfo{}, outer.InterruptContexts[0].Info)
	require.NoError(t, sealComposeInterruptContextReferences(outer))

	require.NoError(t, hydrateNestedInterruptInfoPlaceholders(outer, index))
	require.Equal(t, want, outer)
	requireNoCheckpointProjectionPlaceholders(t, outer)
}

func TestProjectInfoValueInterruptContextPrefixes(t *testing.T) {
	source := &InterruptCtx{
		ID:          "leaf",
		Address:     Address{{Type: AddressSegmentTool, ID: "leaf"}},
		Info:        "leaf-info",
		IsRootCause: true,
		Parent: &InterruptCtx{
			ID:      "parent",
			Address: Address{{Type: AddressSegmentAgent, ID: "child"}},
			Info:    "parent-info",
		},
	}
	index := newCheckpointProjectionIndex(
		checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
	index.addInterruptInfo(canonicalCheckpointInterruptInfo{
		contexts: []*InterruptCtx{source},
	})
	prefixed := prependInterruptContextAddresses([]*InterruptCtx{source}, Address{{
		Type: AddressSegmentAgent,
		ID:   "outer",
	}})[0]
	placeholder := &checkpointInterruptInfoPlaceholderV1{
		Info: &compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{prefixed},
		},
	}
	want := cloneComposeInterruptInfoForProjection(placeholder.Info)

	projectInfoValueInterruptContextPrefixes(placeholder, index)
	require.Equal(t, 1, countComposeInterruptContextRefs(placeholder.Info))
	require.IsType(t, &checkpointInterruptContextPlaceholderV1{},
		placeholder.Info.InterruptContexts[0].Info)
	ref := placeholder.Info.InterruptContexts[0].Info.(*checkpointInterruptContextPlaceholderV1)
	ref.IntegrityDigest = ""
	require.NoError(t, sealInfoValueInterruptContextReferences(placeholder))
	require.NotEmpty(t, ref.IntegrityDigest)
	require.NoError(t, sealInfoValueInterruptContextReferences(
		(*checkpointInterruptInfoPlaceholderV1)(nil)))

	hydrated, err := hydrateProjectionInfoValue(placeholder, index)
	require.NoError(t, err)
	require.Equal(t, want, hydrated)
	requireNoCheckpointProjectionPlaceholders(t, hydrated.(*compose.InterruptInfo))
}

func TestInterruptContextReferenceSealsFinalProjectedTailInfo(t *testing.T) {
	canonical := schema.AssistantMessage("canonical", nil)
	typedSetMessageID(canonical, "tail-info-message")
	source := &InterruptCtx{
		ID:      "leaf",
		Address: Address{{Type: AddressSegmentTool, ID: "leaf"}},
		Info:    "leaf-info",
		Parent: &InterruptCtx{
			ID:      "parent",
			Address: Address{{Type: AddressSegmentAgent, ID: "child"}},
			Info:    "parent-info",
		},
	}

	build := func(t *testing.T) (*compose.InterruptInfo, *checkpointProjectionV1,
		*checkpointProjectionIndex, *checkpointInterruptContextPlaceholderV1, string) {
		t.Helper()
		index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		index.addSchemaMessage([]string{"graph"}, 0, canonical)
		index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		target := cloneInterruptContextForProjection(source)
		target.Parent.Parent = &InterruptCtx{
			ID:      "tail",
			Address: Address{{Type: AddressSegmentAgent, ID: "tail"}},
			Info: &State{
				Messages:  []*schema.Message{canonical},
				AgentName: "tail-agent",
			},
		}
		info := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{target}}
		projectComposeInterruptContextPrefixes(info, index)
		require.Equal(t, 1, countComposeInterruptContextRefs(info))
		ref := info.InterruptContexts[0].Info.(*checkpointInterruptContextPlaceholderV1)
		require.Empty(t, ref.IntegrityDigest)
		beforeNestedProjection, ok := checkpointInterruptContextReferenceIntegrity(
			ref, info.InterruptContexts[0].Parent)
		require.True(t, ok)

		projection := &checkpointProjectionV1{}
		projectComposeInterruptInfoMessages(info, nil, index, projection)
		require.Len(t, projection.InfoRefs, 1)
		tailState := info.InterruptContexts[0].Parent.Info.(*State)
		require.Nil(t, tailState.Messages)
		require.NoError(t, sealComposeInterruptContextReferences(info))
		require.NotEmpty(t, ref.IntegrityDigest)
		require.NotEqual(t, beforeNestedProjection, ref.IntegrityDigest)
		finalDigest, ok := checkpointInterruptContextReferenceIntegrity(
			ref, info.InterruptContexts[0].Parent)
		require.True(t, ok)
		require.Equal(t, finalDigest, ref.IntegrityDigest)
		return info, projection, index, ref, beforeNestedProjection
	}

	t.Run("legal_nested_info_recovers", func(t *testing.T) {
		info, projection, index, _, _ := build(t)
		wrapped := &InterruptInfo{Data: &ChatModelAgentInterruptInfo{Info: info}}
		require.NoError(t, validateInterruptInfoContextReferences(wrapped, index))
		require.NoError(t, hydrateInterruptInfoMessages(
			wrapped, projection.InfoRefs, len(projection.InfoRefs), index))
		require.NoError(t, hydrateInterruptInfoContextPrefixesAfterValidation(wrapped, index))

		restored := info.InterruptContexts[0].Parent.Parent.Info.(*State)
		require.Equal(t, []*schema.Message{canonical}, restored.Messages)
		require.Equal(t, "tail-agent", restored.AgentName)
		requireNoCheckpointProjectionPlaceholders(t, info)
	})

	t.Run("tampering_fails_before_hydration", func(t *testing.T) {
		info, projection, index, _, _ := build(t)
		tailState := info.InterruptContexts[0].Parent.Info.(*State)
		tailState.AgentName = "tampered"
		wrapped := &InterruptInfo{Data: &ChatModelAgentInterruptInfo{Info: info}}

		err := validateInterruptInfoContextReferences(wrapped, index)
		require.EqualError(t, err,
			"checkpoint projection interrupt context reference does not match integrity metadata")
		require.Nil(t, tailState.Messages)
		require.Len(t, projection.InfoRefs, 1)
	})

	t.Run("nested_tail_self_validates", func(t *testing.T) {
		index := newCheckpointProjectionIndex(
			checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
		index.addInterruptInfo(canonicalCheckpointInterruptInfo{
			sourceID: "source",
			contexts: []*InterruptCtx{source},
		})
		nestedTarget := prependInterruptContextAddresses([]*InterruptCtx{source}, Address{{
			Type: AddressSegmentAgent,
			ID:   "nested",
		}})[0]
		nestedInfo := &compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{nestedTarget},
		}
		outerTarget := cloneInterruptContextForProjection(source)
		outerTarget.Parent.Parent = &InterruptCtx{
			ID:      "tail",
			Address: Address{{Type: AddressSegmentAgent, ID: "tail"}},
			Info:    nestedInfo,
		}
		info := &compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{outerTarget},
		}

		projectComposeInterruptContextPrefixes(info, index)
		require.Equal(t, 2, countComposeInterruptContextRefs(info))
		require.NoError(t, sealComposeInterruptContextReferences(info))

		outerReference := info.InterruptContexts[0]
		nestedReference := outerReference.Parent.Info.(*compose.InterruptInfo).
			InterruptContexts[0]
		require.NoError(t, validateInterruptContextReference(nestedReference, index))
		require.NoError(t, validateNestedInterruptInfoContextReferences(info, index))
	})
}

func TestInterruptContextExternalMarshalersStayInlineWithoutCalling(t *testing.T) {
	tests := []struct {
		name     string
		newValue func() any
		calls    *uint32
	}{
		{name: "gob_stable", newValue: func() any {
			return &checkpointProjectionGobInfo{Visible: "stable"}
		}, calls: &checkpointProjectionGobCalls},
		{name: "gob_panic", newValue: func() any {
			return &checkpointProjectionGobInfo{Behavior: "panic"}
		}, calls: &checkpointProjectionGobCalls},
		{name: "gob_lazy", newValue: func() any {
			return &checkpointProjectionGobInfo{Visible: "lazy", Behavior: "lazy"}
		}, calls: &checkpointProjectionGobCalls},
		{name: "binary_stable", newValue: func() any {
			return &checkpointProjectionBinaryInfo{Visible: "stable"}
		}, calls: &checkpointProjectionBinaryCalls},
		{name: "binary_panic", newValue: func() any {
			return &checkpointProjectionBinaryInfo{Behavior: "panic"}
		}, calls: &checkpointProjectionBinaryCalls},
		{name: "binary_lazy", newValue: func() any {
			return &checkpointProjectionBinaryInfo{Visible: "lazy", Behavior: "lazy"}
		}, calls: &checkpointProjectionBinaryCalls},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreUint32(tt.calls, 0)
			source := checkpointProjectionCanonicalContext(tt.newValue())
			target := checkpointProjectionCanonicalContext(tt.newValue())

			var projected *InterruptCtx
			var ok bool
			require.NotPanics(t, func() {
				projected, ok = checkpointProjectionCanonicalContextIndex(source).
					projectInterruptContextPrefix(target)
			})
			require.False(t, ok)
			require.Same(t, target, projected)
			require.Zero(t, atomic.LoadUint32(tt.calls))
			switch info := target.Info.(type) {
			case *checkpointProjectionGobInfo:
				require.Nil(t, info.cached)
			case *checkpointProjectionBinaryInfo:
				require.Nil(t, info.cached)
			}
		})
	}
}

func TestRunnerCheckpointToolInfoUnsafeDefaultStaysInline(t *testing.T) {
	const checkpointID = "tool-info-unsafe-default"
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:         "tool-info-source",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  1024,
	})
	var sourceCheckpoint serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&sourceCheckpoint))
	require.NotNil(t, sourceCheckpoint.ProjectionV1)
	sourceID := sourceCheckpoint.ProjectionV1.SourceInterruptID
	sourceState, ok := sourceCheckpoint.InterruptID2State[sourceID]
	require.True(t, ok)
	sourceData, ok := sourceState.State.([]byte)
	require.True(t, ok)
	index, err := buildCheckpointProjectionIndex(sourceData)
	require.NoError(t, err)
	sources := index.allInterruptInfos()
	require.NotEmpty(t, sources)
	require.NotEmpty(t, sources[0].contexts)

	target := prependInterruptContextAddresses(
		[]*InterruptCtx{sources[0].contexts[0]},
		Address{{Type: AddressSegmentAgent, ID: "outer"}},
	)[0]
	tail := target
	for tail.Parent != nil {
		tail = tail.Parent
	}
	tail.Parent = &InterruptCtx{
		ID:      "tool-info-tail",
		Address: Address{{Type: AddressSegmentAgent, ID: "tool-info-tail"}},
		Info: &schema.ToolInfo{
			Name: "unsafe-default",
			ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
				Type: "object",
				Default: map[string]any{
					"nested": []any{&checkpointProjectionJSONValue{
						Behavior: checkpointProjectionJSONNondeterministic,
					}},
				},
			}),
		},
	}
	info := &InterruptInfo{
		Data: &ChatModelAgentInterruptInfo{
			Info: &compose.InterruptInfo{
				InterruptContexts: []*InterruptCtx{target},
			},
			Data: sourceData,
		},
	}
	ctx := setRunCtx(context.Background(), &runContext{Session: &runSession{}})
	signalCtx := AppendAddressSegment(ctx, AddressSegmentAgent, "tool-info-resume")
	signal := StatefulInterrupt(signalCtx, "source", sourceData).Action.internalInterrupted

	atomic.StoreUint32(&checkpointProjectionJSONCalls, 0)
	require.Zero(t, countInterruptContextRefs(info))
	_, _, digestOK := checkpointinternal.SemanticDigest(tail.Parent.Info)
	require.False(t, digestOK)
	require.Zero(t, atomic.LoadUint32(&checkpointProjectionJSONCalls))
	_, projectedInfo, _, _, err := projectRunnerCheckpoint(
		getRunCtx(ctx), info, signal.ID, map[string]core.InterruptState{
			signal.ID: {State: sourceData},
		})
	require.NoError(t, err)
	require.Zero(t, atomic.LoadUint32(&checkpointProjectionJSONCalls))
	projectedChatModelInfo, ok := projectedInfo.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, projectedChatModelInfo.Info.InterruptContexts, 1)
	_, isReference := projectedChatModelInfo.Info.InterruptContexts[0].Info.(*checkpointInterruptContextPlaceholderV1)
	require.False(t, isReference)

	store := newCheckpointCompatStore()
	require.NoError(t, runnerSaveCheckPointImpl(
		false, store, ctx, checkpointID, info, signal))
	persisted, exists, err := store.Get(context.Background(), checkpointID)
	require.NoError(t, err)
	require.True(t, exists)
	var encoded serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(persisted)).Decode(&encoded))
	require.Contains(t, encoded.InterruptID2Address, signal.ID)
	require.Equal(t, Address{{Type: AddressSegmentAgent, ID: "tool-info-resume"}},
		encoded.InterruptID2Address[signal.ID])
	encodedChatModelInfo, ok := encoded.Info.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, encodedChatModelInfo.Info.InterruptContexts, 1)
	_, isReference = encodedChatModelInfo.Info.InterruptContexts[0].Info.(*checkpointInterruptContextPlaceholderV1)
	require.False(t, isReference)

	_, _, resumeInfo, err := runnerLoadCheckPointImpl(
		store, context.Background(), checkpointID)
	require.NoError(t, err)
	require.NotNil(t, resumeInfo)
	loadedChatModelInfo, ok := resumeInfo.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Len(t, loadedChatModelInfo.Info.InterruptContexts, 1)

	var resumed int32
	agent := &myAgent{
		name: "tool-info-resume",
		resumeFn: func(_ context.Context, got *ResumeInfo,
			_ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			atomic.AddInt32(&resumed, 1)
			require.NotNil(t, got)
			gotChatModelInfo, typeOK := got.Data.(*ChatModelAgentInterruptInfo)
			require.True(t, typeOK)
			require.Len(t, gotChatModelInfo.Info.InterruptContexts, 1)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(EventFromMessage(
				schema.AssistantMessage("resumed", nil), nil, schema.Assistant, ""))
			generator.Close()
			return iter
		},
	}
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           agent,
		CheckPointStore: store,
	})
	iter, err := runner.ResumeWithParams(context.Background(), checkpointID, &ResumeParams{
		Targets: map[string]any{signal.ID: "approved"},
	})
	require.NoError(t, err)
	var output string
	for {
		event, more := iter.Next()
		if !more {
			break
		}
		require.NoError(t, event.Err)
		if event.Output != nil && event.Output.MessageOutput != nil &&
			event.Output.MessageOutput.Message != nil {
			output = event.Output.MessageOutput.Message.Content
		}
	}
	require.Equal(t, "resumed", output)
	require.Equal(t, int32(1), atomic.LoadInt32(&resumed))
}

func TestCheckpointInlineExternalMarshalerSaveLoad(t *testing.T) {
	tests := []struct {
		name     string
		newValue func() any
		calls    *uint32
		assert   func(*testing.T, any)
	}{
		{
			name: "gob_lazy",
			newValue: func() any {
				return &checkpointProjectionGobInfo{Visible: "persisted", Behavior: "lazy"}
			},
			calls: &checkpointProjectionGobCalls,
			assert: func(t *testing.T, value any) {
				restored, ok := value.(*checkpointProjectionGobInfo)
				require.True(t, ok)
				require.Equal(t, "persisted", restored.Visible)
				require.Equal(t, []byte("persisted"), restored.cached)
			},
		},
		{
			name: "binary_lazy",
			newValue: func() any {
				return &checkpointProjectionBinaryInfo{Visible: "persisted", Behavior: "lazy"}
			},
			calls: &checkpointProjectionBinaryCalls,
			assert: func(t *testing.T, value any) {
				restored, ok := value.(*checkpointProjectionBinaryInfo)
				require.True(t, ok)
				require.Equal(t, "persisted", restored.Visible)
				require.Equal(t, []byte("persisted"), restored.cached)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			atomic.StoreUint32(tt.calls, 0)
			source := checkpointProjectionCanonicalContext(tt.newValue())
			target := checkpointProjectionCanonicalContext(tt.newValue())
			projected, ok := checkpointProjectionCanonicalContextIndex(source).
				projectInterruptContextPrefix(target)
			require.False(t, ok)
			require.Same(t, target, projected)
			require.Zero(t, atomic.LoadUint32(tt.calls))

			store := newCheckpointCompatStore()
			ctx := setRunCtx(context.Background(), &runContext{})
			require.NoError(t, runnerSaveCheckPointImpl(false, store, ctx, tt.name,
				&InterruptInfo{InterruptContexts: []*InterruptCtx{projected}}, nil))
			require.Positive(t, atomic.LoadUint32(tt.calls))

			_, _, resumeInfo, err := runnerLoadCheckPointImpl(
				store, context.Background(), tt.name)
			require.NoError(t, err)
			require.NotNil(t, resumeInfo)
			require.NotNil(t, resumeInfo.InterruptInfo)
			require.Len(t, resumeInfo.InterruptContexts, 1)
			tt.assert(t, resumeInfo.InterruptContexts[0].Info)
		})
	}
}

func TestCheckpointTextOnlyInfoGobRoundTrip(t *testing.T) {
	const checkpointID = "text-only-info"
	original := &checkpointProjectionTextInfo{
		Visible: "persisted",
		hidden:  "marshal-text-output-must-not-be-used",
	}

	data, err := encodeRunnerCheckpoint(&serialization{
		Info: &InterruptInfo{
			InterruptContexts: []*InterruptCtx{{
				ID:      "text-only",
				Address: Address{{Type: AddressSegmentTool, ID: "text-only"}},
				Info:    original,
			}},
		},
		InterruptID2Address: map[string]Address{},
		InterruptID2State:   map[string]core.InterruptState{},
	})
	require.NoError(t, err)
	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), checkpointID, data))

	_, _, resumeInfo, err := runnerLoadCheckPointImpl(
		store, context.Background(), checkpointID)
	require.NoError(t, err)
	require.NotNil(t, resumeInfo)
	require.NotNil(t, resumeInfo.InterruptInfo)
	require.Len(t, resumeInfo.InterruptContexts, 1)
	restored, ok := resumeInfo.InterruptContexts[0].Info.(*checkpointProjectionTextInfo)
	require.True(t, ok)
	require.Equal(t, original.Visible, restored.Visible)
	require.Empty(t, restored.hidden)
}

func TestSealComposeInterruptContextReferencesErrorPropagation(t *testing.T) {
	const want = "failed to bind checkpoint projection interrupt context reference"
	invalidChain := func() *InterruptCtx {
		return &InterruptCtx{
			Info: &checkpointInterruptContextPlaceholderV1{},
			Parent: &InterruptCtx{
				ID:      "unsummarizable-tail",
				Address: Address{{Type: AddressSegmentAgent, ID: "tail"}},
				Info:    make(chan int),
			},
		}
	}
	invalidInfo := func() *compose.InterruptInfo {
		return &compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{invalidChain()},
		}
	}
	tests := []struct {
		name string
		info *compose.InterruptInfo
	}{
		{
			name: "state",
			info: &compose.InterruptInfo{State: invalidInfo()},
		},
		{
			name: "rerun",
			info: &compose.InterruptInfo{
				RerunNodesExtra: map[string]any{"rerun": invalidInfo()},
			},
		},
		{
			name: "context_chain",
			info: &compose.InterruptInfo{
				InterruptContexts: []*InterruptCtx{invalidChain()},
			},
		},
		{
			name: "subgraph",
			info: &compose.InterruptInfo{
				SubGraphs: map[string]*compose.InterruptInfo{"subgraph": invalidInfo()},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, sealComposeInterruptContextReferences(tt.info), want)
		})
	}
	require.NoError(t, sealComposeInterruptContextReferences(nil))
}

func checkpointProjectionCanonicalContext(info any) *InterruptCtx {
	return &InterruptCtx{
		ID:      "leaf",
		Address: Address{{Type: AddressSegmentTool, ID: "leaf"}},
		Info:    info,
		Parent: &InterruptCtx{
			ID:      "parent",
			Address: Address{{Type: AddressSegmentAgent, ID: "parent"}},
			Info:    "parent",
		},
	}
}

func checkpointProjectionCanonicalContextIndex(
	source *InterruptCtx) *checkpointProjectionIndex {
	index := newCheckpointProjectionIndex(
		checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
	index.addInterruptInfo(canonicalCheckpointInterruptInfo{
		sourceID: "canonical-source",
		contexts: []*InterruptCtx{source},
	})
	return index
}

func TestValidateInfoValueContextReferences(t *testing.T) {
	index := newCheckpointProjectionIndex(
		checkpointProjectionVersionV2, newCheckpointProjectionTraversal())
	invalidReference := func() *InterruptCtx {
		return &InterruptCtx{
			Info: &checkpointInterruptContextPlaceholderV1{},
		}
	}

	t.Run("raw_interrupt_info", func(t *testing.T) {
		value := &compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{invalidReference()},
		}
		require.EqualError(t, validateInfoValueContextReferences(value, index),
			"checkpoint projection has invalid interrupt context reference")
	})

	t.Run("non_nil_placeholder", func(t *testing.T) {
		value := &checkpointInterruptInfoPlaceholderV1{
			Info: &compose.InterruptInfo{
				InterruptContexts: []*InterruptCtx{invalidReference()},
			},
		}
		require.EqualError(t, validateInfoValueContextReferences(value, index),
			"checkpoint projection has invalid interrupt context reference")
	})
}

func TestCheckpointProjectionV2PersistsOnlyLocalCoordinates(t *testing.T) {
	const depth = 8
	raw, _, _ := captureCheckpointCompatFixture(t, checkpointCompatFixture{
		Name:             "projection-v2-local-coordinates",
		Depth:            depth,
		PayloadField:     "content",
		PayloadSize:      32 << 10,
		StableDepthNames: true,
	})
	require.Equal(t, depth+1, requireLocalProjectionCoordinates(t, raw))
}

func requireLocalProjectionCoordinates(t *testing.T, raw []byte) int {
	t.Helper()
	var checkpoint serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&checkpoint))
	require.NotNil(t, checkpoint.ProjectionV1)
	require.Equal(t, checkpointProjectionVersionV2, checkpoint.ProjectionV1.Version)
	require.Equal(t, checkpointProjectionVersionV2,
		checkpoint.InterruptID2State[runnerProjectionSentinelID].State.(*runnerProjectionSentinelV1).Version)
	checkMessageSource := func(source checkpointMessageSourceV1) {
		t.Helper()
		if source.MessageID == "" {
			return
		}
		require.Positive(t, source.SourceOrdinal)
		require.Empty(t, source.GraphPath)
	}
	for _, ref := range checkpoint.ProjectionV1.RunCtxRefs {
		checkMessageSource(ref.Source)
	}
	for _, ref := range checkpoint.ProjectionV1.InfoRefs {
		checkMessageSource(ref.Source)
	}
	for _, ref := range checkpoint.ProjectionV1.ToolResultRefs {
		require.Positive(t, ref.Source.SourceOrdinal)
		require.Empty(t, ref.Source.GraphPath)
	}
	requireLocalInterruptContextCoordinates(t, checkpoint.Info)

	count := 1
	for _, state := range checkpoint.InterruptID2State {
		data, ok := state.State.([]byte)
		if !ok {
			continue
		}
		require.NoError(t, compose.WalkCheckpointValues(data, &gobSerializer{},
			func(_ compose.NodePath, _ compose.CheckpointValueLocation, value any) error {
				switch value := value.(type) {
				case *checkpointMessagePlaceholderV1:
					checkMessageSource(value.Source)
				case *checkpointMessageSlicePlaceholderV1:
					for _, entry := range value.Entries {
						if entry.Source != nil {
							checkMessageSource(*entry.Source)
						}
					}
				case *checkpointAgenticMessagePlaceholderV1:
					checkMessageSource(value.Source)
				case *checkpointAgenticMessageSlicePlaceholderV1:
					for _, entry := range value.Entries {
						if entry.Source != nil {
							checkMessageSource(*entry.Source)
						}
					}
				case *agentToolInterruptStateV1:
					t.Fatal("new checkpoint persisted legacy AgentTool V1 state")
				case *agentToolInterruptStateV2:
					require.Equal(t, agentToolInterruptStateVersionV2, value.Version)
					count += requireLocalProjectionCoordinates(t, value.BridgeCheckpoint)
				}
				return nil
			}))
	}
	return count
}

func requireLocalInterruptContextCoordinates(t *testing.T, info *InterruptInfo) {
	t.Helper()
	if info == nil {
		return
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil {
		return
	}
	var inspectComposeInfo func(*compose.InterruptInfo)
	inspectValue := func(any) {}
	inspectComposeInfo = func(current *compose.InterruptInfo) {
		if current == nil {
			return
		}
		inspectValue(current.State)
		for _, value := range current.RerunNodesExtra {
			inspectValue(value)
		}
		for _, interruptCtx := range current.InterruptContexts {
			for node := interruptCtx; node != nil; node = node.Parent {
				if ref, ok := node.Info.(*checkpointInterruptContextPlaceholderV1); ok {
					require.Positive(t, ref.SourceOrdinal)
					require.Empty(t, ref.RunnerPath)
					require.NotEmpty(t, ref.SourceID)
					require.NotEmpty(t, ref.Digest)
					require.NotEmpty(t, ref.IntegrityDigest)
				}
				inspectValue(node.Info)
			}
		}
		for _, subGraph := range current.SubGraphs {
			inspectComposeInfo(subGraph)
		}
	}
	inspectValue = func(value any) {
		switch value := value.(type) {
		case *compose.InterruptInfo:
			inspectComposeInfo(value)
		case *checkpointInterruptInfoPlaceholderV1:
			if value != nil {
				inspectComposeInfo(value.Info)
			}
		}
	}
	inspectComposeInfo(chatModelInfo.Info)
}

func sealInterruptContextReference(t *testing.T,
	ref *checkpointInterruptContextPlaceholderV1, tail *InterruptCtx) {
	t.Helper()
	digest, ok := checkpointInterruptContextReferenceIntegrity(ref, tail)
	require.True(t, ok)
	ref.IntegrityDigest = digest
}

func requireNoCheckpointProjectionPlaceholders(t *testing.T, info *compose.InterruptInfo) {
	t.Helper()
	if info == nil {
		return
	}
	checkValue := func(value any) {
		t.Helper()
		switch value := value.(type) {
		case *checkpointInterruptInfoPlaceholderV1:
			t.Fatalf("interrupt info placeholder remains after hydration: %#v", value)
		case *checkpointInterruptContextPlaceholderV1:
			t.Fatalf("interrupt context placeholder remains after hydration: %#v", value)
		case *compose.InterruptInfo:
			requireNoCheckpointProjectionPlaceholders(t, value)
		}
	}
	checkValue(info.State)
	for _, value := range info.RerunNodesExtra {
		checkValue(value)
	}
	for _, interruptCtx := range info.InterruptContexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			checkValue(current.Info)
		}
	}
	for _, subGraph := range info.SubGraphs {
		requireNoCheckpointProjectionPlaceholders(t, subGraph)
	}
}
