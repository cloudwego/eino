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
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

type checkpointProjectionEnhancedTool struct {
	name  string
	mu    sync.Mutex
	calls int
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
	require.Equal(t, checkpointProjectionVersionV1, persisted.ProjectionV1.Version)
	require.Contains(t, persisted.InterruptID2State, runnerProjectionSentinelID)
	require.NotContains(t, persisted.InterruptID2Address, runnerProjectionSentinelID)
	require.NotEmpty(t, persisted.ProjectionV1.RunCtxRefs)
	require.NotEmpty(t, persisted.ProjectionV1.InfoRefs)

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

	persistedChatModelInfo, ok := persisted.Info.Data.(*ChatModelAgentInterruptInfo)
	require.True(t, ok)
	require.Nil(t, persistedChatModelInfo.Data)
	require.NotEmpty(t, originalChatModelInfo.Data,
		"checkpoint projection must not mutate the live interrupt event")
}

func TestRunnerCheckpointProjectionMetadataValidation(t *testing.T) {
	valid := func() *serialization {
		return &serialization{
			ProjectionV1: &checkpointProjectionV1{
				Version:           checkpointProjectionVersionV1,
				SourceInterruptID: "source",
			},
			InterruptID2Address: map[string]Address{},
			InterruptID2State: map[string]core.InterruptState{
				"source": {State: []byte("not a checkpoint")},
				runnerProjectionSentinelID: {
					State: &runnerProjectionSentinelV1{Version: checkpointProjectionVersionV1},
				},
			},
		}
	}

	t.Run("legacy", func(t *testing.T) {
		require.NoError(t, validateRunnerProjectionMetadata(&serialization{}))
	})
	t.Run("sentinel_without_metadata", func(t *testing.T) {
		state := valid()
		state.ProjectionV1 = nil
		require.ErrorContains(t, validateRunnerProjectionMetadata(state), "metadata is missing")
	})
	t.Run("metadata_without_sentinel", func(t *testing.T) {
		state := valid()
		delete(state.InterruptID2State, runnerProjectionSentinelID)
		require.ErrorContains(t, validateRunnerProjectionMetadata(state), "sentinel is missing")
	})
	t.Run("unsupported_version", func(t *testing.T) {
		state := valid()
		state.ProjectionV1.Version++
		require.ErrorContains(t, validateRunnerProjectionMetadata(state), "requires a newer Eino version")
	})
	t.Run("sentinel_with_address", func(t *testing.T) {
		state := valid()
		state.InterruptID2Address[runnerProjectionSentinelID] = Address{}
		require.ErrorContains(t, validateRunnerProjectionMetadata(state), "must not have a routing address")
	})
	t.Run("invalid_sentinel", func(t *testing.T) {
		state := valid()
		state.InterruptID2State[runnerProjectionSentinelID] = core.InterruptState{State: "invalid"}
		require.ErrorContains(t, validateRunnerProjectionMetadata(state), "invalid sentinel")
	})
	t.Run("missing_source", func(t *testing.T) {
		state := valid()
		delete(state.InterruptID2State, "source")
		require.ErrorContains(t, restoreRunnerCheckpointProjection(state), "source interrupt state")
	})
	t.Run("invalid_source_type", func(t *testing.T) {
		state := valid()
		state.InterruptID2State["source"] = core.InterruptState{State: "invalid"}
		require.ErrorContains(t, restoreRunnerCheckpointProjection(state), "invalid type")
	})
	t.Run("malformed_source_bytes", func(t *testing.T) {
		require.ErrorContains(t, restoreRunnerCheckpointProjection(valid()),
			"failed to decode checkpoint projection source")
	})
	t.Run("reserved_interrupt_id", func(t *testing.T) {
		require.ErrorContains(t, validateRunnerProjectionReservedIDs(
			map[string]Address{"_eino_user": {}}, nil), "reserved checkpoint metadata prefix")
		require.ErrorContains(t, validateRunnerProjectionReservedIDs(nil,
			map[string]core.InterruptState{"_eino_user": {}}), "reserved checkpoint metadata prefix")
	})
	t.Run("reserved_interrupt_id_error_is_deterministic", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			require.EqualError(t, validateRunnerProjectionReservedIDs(
				map[string]Address{"_eino_z": {}, "_eino_a": {}}, nil),
				`interrupt ID "_eino_a" uses reserved checkpoint metadata prefix`)
			require.EqualError(t, validateRunnerProjectionReservedIDs(nil,
				map[string]core.InterruptState{"_eino_z": {}, "_eino_a": {}}),
				`interrupt ID "_eino_a" uses reserved checkpoint metadata prefix`)
		}
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
	source.GraphPath = []string{}
	restoredMessage, err := index.schemaMessage(source)
	require.NoError(t, err)
	require.Equal(t, message, restoredMessage)

	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic-root")
	index.addAgenticMessage(nil, 0, agenticMessage)
	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)
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
}

func TestRunnerCheckpointProjectionReferenceValidation(t *testing.T) {
	t.Run("run_context_targets_must_be_unique", func(t *testing.T) {
		ref := runCtxMessageProjectionV1{Target: runCtxTargetEvent, Index: 0}
		require.ErrorContains(t, validateRunCtxProjectionRefs(
			[]runCtxMessageProjectionV1{ref, ref}, 2), "duplicate")
	})
	t.Run("info_slice_must_be_complete", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetStateMessage,
			ContextIndex: -1,
			MessageIndex: 0,
			TargetLength: 2,
			Inline:       schema.UserMessage("first"),
		}}
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "incomplete")
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
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "parent depth")
	})
	t.Run("run_context_invalid_coordinates", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetRootInput,
			Index:        -1,
			TargetLength: 1,
		}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "coordinates")
	})
	t.Run("run_context_invalid_length", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target: runCtxTargetRootInput,
			Index:  0,
		}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "invalid length")
	})
	t.Run("run_context_index_exceeds_length", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetRootInput,
			Index:        1,
			TargetLength: 1,
		}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "exceeds length")
	})
	t.Run("run_context_inconsistent_lengths", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{
			{Target: runCtxTargetRootInput, Index: 0, TargetLength: 2},
			{Target: runCtxTargetRootInput, Index: 1, TargetLength: 3},
		}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 2), "inconsistent lengths")
	})
	t.Run("run_context_scalar_has_slice_metadata", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:    runCtxTargetEvent,
			Index:     0,
			LaneDepth: 1,
		}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "invalid lane depth")
	})
	t.Run("run_context_lane_has_slice_metadata", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetLaneEvent,
			Index:        0,
			TargetLength: 1,
		}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "unexpected slice length")
	})
	t.Run("run_context_unsupported_target", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{Target: "unknown"}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "unsupported")
	})
	t.Run("info_count_mismatch", func(t *testing.T) {
		require.ErrorContains(t, validateInfoProjectionRefs(nil, 1), "reference count mismatch")
	})
	t.Run("info_state_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetStateMessage,
			ContextIndex: 0,
			MessageIndex: 0,
			TargetLength: 1,
		}}
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "invalid interrupt state coordinates")
	})
	t.Run("info_context_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetContextStateMessage,
			ContextIndex: -1,
			MessageIndex: 0,
			TargetLength: 1,
		}}
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "invalid context state coordinates")
	})
	t.Run("info_rerun_tool_calls_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:        infoTargetRerunToolCalls,
			ContextIndex:  0,
			MessageIndex:  -1,
			RerunExtraKey: "tools",
		}}
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "invalid rerun tool calls coordinates")
	})
	t.Run("info_context_tool_calls_invalid_coordinates", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{
			Target:       infoTargetContextToolCalls,
			ContextIndex: -1,
			MessageIndex: -1,
		}}
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "invalid context tool calls coordinates")
	})
	t.Run("info_unsupported_target", func(t *testing.T) {
		refs := []infoMessageProjectionV1{{Target: "unknown"}}
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 1), "unsupported")
	})
	t.Run("info_duplicate_target", func(t *testing.T) {
		ref := infoMessageProjectionV1{
			Target:        infoTargetRerunToolCalls,
			ContextIndex:  -1,
			MessageIndex:  -1,
			RerunExtraKey: "tools",
		}
		require.ErrorContains(t, validateInfoProjectionRefs(
			[]infoMessageProjectionV1{ref, ref}, 2), "duplicate")
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
		require.ErrorContains(t, validateInfoProjectionRefs(refs, 2), "inconsistent lengths")
	})
}

func TestProjectedCheckpointMessages(t *testing.T) {
	schemaMessage := schema.UserMessage("schema")
	typedSetMessageID(schemaMessage, "schema-message")
	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
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
		require.ErrorContains(t, err, "nil message has payload")
	})
	t.Run("schema_inline_missing", func(t *testing.T) {
		_, err := projectedSchemaMessage(checkpointMessageSourceV1{}, nil, false, index)
		require.ErrorContains(t, err, "inline message is missing")
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
		require.ErrorContains(t, err, "both inline data and a source reference")
	})
	t.Run("schema_source_mismatch", func(t *testing.T) {
		corrupt := schemaSource
		corrupt.Digest = "corrupt"
		_, err := projectedSchemaMessage(corrupt, nil, false, index)
		require.ErrorContains(t, err, "does not match metadata")
	})
	t.Run("agentic_nil", func(t *testing.T) {
		message, err := projectedAgenticMessage(checkpointMessageSourceV1{}, nil, true, index)
		require.NoError(t, err)
		require.Nil(t, message)
	})
	t.Run("agentic_nil_with_payload", func(t *testing.T) {
		_, err := projectedAgenticMessage(agenticSource, nil, true, index)
		require.ErrorContains(t, err, "nil agentic message has payload")
	})
	t.Run("agentic_inline_missing", func(t *testing.T) {
		_, err := projectedAgenticMessage(checkpointMessageSourceV1{}, nil, false, index)
		require.ErrorContains(t, err, "inline agentic message is missing")
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
		require.ErrorContains(t, err, "both inline data and a source reference")
	})
	t.Run("agentic_source_mismatch", func(t *testing.T) {
		corrupt := agenticSource
		corrupt.Digest = "corrupt"
		_, err := projectedAgenticMessage(corrupt, nil, false, index)
		require.ErrorContains(t, err, "does not match metadata")
	})
	t.Run("unsupported_digest_value", func(t *testing.T) {
		_, ok := projectionMessageDigest(make(chan int))
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

func TestHydrateRunContextMessageTargets(t *testing.T) {
	schemaMessage := schema.UserMessage("schema")
	typedSetMessageID(schemaMessage, "schema-message")
	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
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
		require.ErrorContains(t, err, "invalid root input target")
	})
	t.Run("root_input_occupied", func(t *testing.T) {
		runCtx := &runContext{RootInput: &AgentInput{
			Messages: []*schema.Message{schema.UserMessage("occupied")},
		}}
		err := hydrateRunCtxRootInput(runCtx, runCtxMessageProjectionV1{
			Inline: schemaMessage,
			Index:  0,
		}, 1, index)
		require.ErrorContains(t, err, "invalid root input target")
	})
	t.Run("root_input_source_error", func(t *testing.T) {
		source := schemaSource
		source.Digest = "corrupt"
		err := hydrateRunCtxRootInput(&runContext{RootInput: &AgentInput{}},
			runCtxMessageProjectionV1{Source: source}, 1, index)
		require.ErrorContains(t, err, "does not match metadata")
	})
	t.Run("event_missing", func(t *testing.T) {
		err := hydrateRunCtxEvent(nil, runCtxMessageProjectionV1{
			Inline: schemaMessage,
			Index:  0,
		}, index)
		require.ErrorContains(t, err, "invalid event target")
	})
	t.Run("event_target_occupied", func(t *testing.T) {
		event := &agentEventWrapper{
			AgentEvent: EventFromMessage(schemaMessage, nil, schema.User, ""),
		}
		err := hydrateAgentEventMessage(event, schemaMessage, false)
		require.ErrorContains(t, err, "invalid event message target")
	})
	t.Run("lane_session_missing", func(t *testing.T) {
		err := hydrateRunCtxLaneEvent(nil, runCtxMessageProjectionV1{
			Inline: schemaMessage,
		}, index)
		require.ErrorContains(t, err, "lane event session is missing")
	})
	t.Run("lane_depth_missing", func(t *testing.T) {
		runCtx := &runContext{Session: &runSession{LaneEvents: &laneEvents{}}}
		err := hydrateRunCtxLaneEvent(runCtx, runCtxMessageProjectionV1{
			Inline:    schemaMessage,
			LaneDepth: 1,
		}, index)
		require.ErrorContains(t, err, "invalid lane event target")
	})
	t.Run("agentic_root_input_missing", func(t *testing.T) {
		err := hydrateRunCtxAgenticRootInput(nil, runCtxMessageProjectionV1{
			AgenticInline: agenticMessage,
		}, 1, index)
		require.ErrorContains(t, err, "invalid agentic root input target")
	})
	t.Run("agentic_root_input_wrong_type", func(t *testing.T) {
		err := hydrateRunCtxAgenticRootInput(&runContext{AgenticRootInput: "invalid"},
			runCtxMessageProjectionV1{AgenticInline: agenticMessage}, 1, index)
		require.ErrorContains(t, err, "invalid agentic root input target")
	})
	t.Run("agentic_root_input_occupied", func(t *testing.T) {
		runCtx := &runContext{AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{agenticMessage},
		}}
		err := hydrateRunCtxAgenticRootInput(runCtx,
			runCtxMessageProjectionV1{AgenticInline: agenticMessage}, 1, index)
		require.ErrorContains(t, err, "invalid agentic root input target")
	})
	t.Run("agentic_root_input_source_error", func(t *testing.T) {
		source := agenticSource
		source.Digest = "corrupt"
		runCtx := &runContext{AgenticRootInput: &TypedAgentInput[*schema.AgenticMessage]{}}
		err := hydrateRunCtxAgenticRootInput(runCtx,
			runCtxMessageProjectionV1{Source: source}, 1, index)
		require.ErrorContains(t, err, "does not match metadata")
	})
	t.Run("typed_event_session_missing", func(t *testing.T) {
		err := hydrateRunCtxTypedEvent(nil, runCtxMessageProjectionV1{
			AgenticInline: agenticMessage,
		}, index)
		require.ErrorContains(t, err, "typed event session is missing")
	})
	t.Run("typed_event_collection_invalid", func(t *testing.T) {
		runCtx := &runContext{Session: &runSession{TypedEvents: "invalid"}}
		err := hydrateRunCtxTypedEvent(runCtx, runCtxMessageProjectionV1{
			AgenticInline: agenticMessage,
		}, index)
		require.ErrorContains(t, err, "invalid typed event target")
	})
	t.Run("typed_event_target_occupied", func(t *testing.T) {
		event := &typedAgentEventWrapper[*schema.AgenticMessage]{
			event: EventFromAgenticMessage(agenticMessage, nil, schema.AgenticRoleTypeUser),
		}
		err := hydrateTypedAgentEventMessage(event, agenticMessage, false)
		require.ErrorContains(t, err, "invalid typed event message target")
	})
}

func TestHydrateInterruptInfoMessageTargets(t *testing.T) {
	message := schema.AssistantMessage("schema", []schema.ToolCall{{ID: "tool"}})
	typedSetMessageID(message, "schema-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
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
		require.ErrorContains(t, err, "reference count mismatch")
		require.NoError(t, hydrateInterruptInfoMessages(nil, nil, 0, index))
		err = hydrateInterruptInfoMessages(nil, []infoMessageProjectionV1{stateRef}, 1, index)
		require.ErrorContains(t, err, "interrupt info is missing")
		err = hydrateInterruptInfoMessages(&InterruptInfo{Data: "invalid"},
			[]infoMessageProjectionV1{stateRef}, 1, index)
		require.ErrorContains(t, err, "invalid type")
	})
	t.Run("missing_subgraph_path", func(t *testing.T) {
		info := &compose.InterruptInfo{SubGraphs: map[string]*compose.InterruptInfo{}}
		ref := stateRef
		ref.SubGraphPath = []string{"missing"}
		err := hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
		require.ErrorContains(t, err, "path")
	})
	t.Run("missing_context", func(t *testing.T) {
		err := hydrateComposeInterruptInfoRefs(&compose.InterruptInfo{},
			[]infoMessageProjectionV1{contextRef}, index)
		require.ErrorContains(t, err, "interrupt context index")
	})
	t.Run("context_tool_calls_nil_source", func(t *testing.T) {
		info := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{Info: &compose.ToolsInterruptAndRerunExtra{}}}}
		ref := contextToolCallsRef
		ref.Inline = nil
		ref.IsNil = true
		err := hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
		require.ErrorContains(t, err, "source is nil")
	})
	t.Run("context_tool_calls_invalid_target", func(t *testing.T) {
		info := &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{Info: "invalid"}}}
		err := hydrateComposeInterruptInfoRefs(info,
			[]infoMessageProjectionV1{contextToolCallsRef}, index)
		require.ErrorContains(t, err, "invalid context tool calls target")
	})
	t.Run("rerun_tool_calls_nil_source", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{
			"tools": &compose.ToolsInterruptAndRerunExtra{},
		}}
		ref := rerunToolCallsRef
		ref.Inline = nil
		ref.IsNil = true
		err := hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
		require.ErrorContains(t, err, "source is nil")
	})
	t.Run("rerun_tool_calls_invalid_target", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{"tools": "invalid"}}
		err := hydrateComposeInterruptInfoRefs(info,
			[]infoMessageProjectionV1{rerunToolCallsRef}, index)
		require.ErrorContains(t, err, "invalid rerun tool calls target")
	})
	t.Run("unsupported_target", func(t *testing.T) {
		ref := stateRef
		ref.Target = "unknown"
		err := hydrateComposeInterruptInfoRefs(&compose.InterruptInfo{},
			[]infoMessageProjectionV1{ref}, index)
		require.ErrorContains(t, err, "unsupported")
	})
	t.Run("state_message_source_error", func(t *testing.T) {
		ref := stateRef
		ref.Inline = nil
		ref.Source = checkpointMessageSourceV1{MessageID: "missing"}
		err := hydrateInfoStateMessage(&State{}, ref, 1, index)
		require.ErrorContains(t, err, "does not match metadata")
	})
	t.Run("state_message_nil_target", func(t *testing.T) {
		var state *State
		err := hydrateInfoStateMessage(state, stateRef, 1, index)
		require.ErrorContains(t, err, "invalid state message target")
	})
	t.Run("state_message_occupied", func(t *testing.T) {
		state := &State{Messages: []*schema.Message{schema.UserMessage("occupied")}}
		err := hydrateInfoStateMessage(state, stateRef, 1, index)
		require.ErrorContains(t, err, "invalid state message target")
	})
	t.Run("agentic_state_message_nil_target", func(t *testing.T) {
		var state *agenticState
		ref := stateRef
		ref.Inline = nil
		ref.AgenticInline = schema.UserAgenticMessage("agentic")
		err := hydrateInfoStateMessage(state, ref, 1, index)
		require.ErrorContains(t, err, "invalid agentic state message target")
	})
	t.Run("invalid_state_type", func(t *testing.T) {
		err := hydrateInfoStateMessage("invalid", stateRef, 1, index)
		require.ErrorContains(t, err, "invalid state message target type")
	})
	t.Run("nested_placeholder_validation", func(t *testing.T) {
		value, err := hydrateProjectionInfoValue(
			(*checkpointInterruptInfoPlaceholderV1)(nil), index)
		require.ErrorContains(t, err, "nil interrupt info reference")
		require.Nil(t, value)

		value, err = hydrateProjectionInfoValue("inline", index)
		require.NoError(t, err)
		require.Equal(t, "inline", value)

		placeholder := &checkpointInterruptInfoPlaceholderV1{
			Info:     &compose.InterruptInfo{},
			RefCount: 1,
		}
		_, err = hydrateProjectionInfoValue(placeholder, index)
		require.ErrorContains(t, err, "reference count mismatch")
	})
	t.Run("nested_info_paths", func(t *testing.T) {
		require.NoError(t, hydrateNestedInterruptInfoPlaceholders(nil, index))
		_, err := composeInterruptInfoAtPath(nil, nil)
		require.ErrorContains(t, err, "path")
		_, err = composeInterruptInfoAtPath(
			&compose.InterruptInfo{SubGraphs: map[string]*compose.InterruptInfo{}},
			[]string{"missing"})
		require.ErrorContains(t, err, "path")
		_, err = interruptContextAt(&compose.InterruptInfo{}, 0, 0)
		require.ErrorContains(t, err, "index")
		_, err = interruptContextAt(&compose.InterruptInfo{
			InterruptContexts: []*InterruptCtx{{}},
		}, 0, 1)
		require.ErrorContains(t, err, "parent depth")
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
	t.Run("outer_validation", func(t *testing.T) {
		err := hydrateInterruptInfoToolResults(nil, []infoToolResultProjectionV1{rerunRef},
			0, index)
		require.ErrorContains(t, err, "reference count mismatch")
		require.NoError(t, hydrateInterruptInfoToolResults(nil, nil, 0, index))
		err = hydrateInterruptInfoToolResults(nil, []infoToolResultProjectionV1{rerunRef},
			1, index)
		require.ErrorContains(t, err, "interrupt info is missing")
		err = hydrateInterruptInfoToolResults(&InterruptInfo{Data: "invalid"},
			[]infoToolResultProjectionV1{rerunRef}, 1, index)
		require.ErrorContains(t, err, "invalid type")
	})
	t.Run("compose_reference_validation", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{
			"tools": &compose.ToolsInterruptAndRerunExtra{},
		}}
		err := hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{rerunRef}, 0, index)
		require.ErrorContains(t, err, "reference count mismatch")

		invalidCoordinates := rerunRef
		invalidCoordinates.ParentDepth = -1
		err = hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{invalidCoordinates}, 1, index)
		require.ErrorContains(t, err, "invalid tool result coordinates")

		err = hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{rerunRef, rerunRef}, 2, index)
		require.ErrorContains(t, err, "duplicate tool result target")
	})
	t.Run("missing_subgraph_path", func(t *testing.T) {
		ref := rerunRef
		ref.SubGraphPath = []string{"missing"}
		err := hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.ErrorContains(t, err, "path")
	})
	t.Run("invalid_target_coordinates", func(t *testing.T) {
		ref := rerunRef
		ref.ContextIndex = 0
		err := hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.ErrorContains(t, err, "invalid rerun tool result target")

		ref.Target = infoTargetContextToolResult
		ref.ContextIndex = -1
		err = hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.ErrorContains(t, err, "invalid context tool result target")

		ref.ContextIndex = 0
		err = hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.ErrorContains(t, err, "interrupt context index")
	})
	t.Run("unsupported_target", func(t *testing.T) {
		ref := rerunRef
		ref.Target = "unknown"
		err := hydrateComposeInterruptInfoToolResults(&compose.InterruptInfo{},
			[]infoToolResultProjectionV1{ref}, 1, index)
		require.ErrorContains(t, err, "unsupported tool result target")
	})
	t.Run("invalid_target_type", func(t *testing.T) {
		info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{"tools": "invalid"}}
		err := hydrateComposeInterruptInfoToolResults(info,
			[]infoToolResultProjectionV1{rerunRef}, 1, index)
		require.ErrorContains(t, err, "invalid type")
	})
	t.Run("standard_result", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{}
		require.NoError(t, hydrateInfoToolResult(extra, rerunRef, index))
		require.Equal(t, "result", extra.ExecutedTools[stringSource.ToolCallID])
		require.ErrorContains(t, hydrateInfoToolResult(extra, rerunRef, index),
			"already populated")
	})
	t.Run("enhanced_result", func(t *testing.T) {
		ref := rerunRef
		ref.ToolCallID = enhancedSource.ToolCallID
		ref.Source = enhancedSource
		extra := &compose.ToolsInterruptAndRerunExtra{}
		require.NoError(t, hydrateInfoToolResult(extra, ref, index))
		require.Equal(t, enhancedResult, extra.ExecutedEnhancedTools[enhancedSource.ToolCallID])
		require.NotSame(t, enhancedResult, extra.ExecutedEnhancedTools[enhancedSource.ToolCallID])
		require.ErrorContains(t, hydrateInfoToolResult(extra, ref, index), "already populated")
	})
	t.Run("source_and_kind_errors", func(t *testing.T) {
		ref := rerunRef
		ref.Source.Digest = "missing"
		require.ErrorContains(t, hydrateInfoToolResult(
			&compose.ToolsInterruptAndRerunExtra{}, ref, index), "does not match metadata")

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
		require.ErrorContains(t, hydrateInfoToolResult(
			&compose.ToolsInterruptAndRerunExtra{}, ref, index), "unsupported tool result kind")
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
	events := []*typedAgentEventWrapper[*schema.AgenticMessage]{{event: event}}
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
	require.Len(t, projection.RunCtxRefs, 2)
	rootInput := cloned.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage])
	require.Nil(t, rootInput.Messages)
	typedEvents := cloned.Session.TypedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage])
	require.Nil(t, (*typedEvents)[0].event.Output.MessageOutput.Message)

	require.NoError(t, hydrateRunContextMessages(cloned, projection.RunCtxRefs,
		projection.RunCtxRefCount, index))
	require.Equal(t, []*schema.AgenticMessage{message}, rootInput.Messages)
	require.Equal(t, message, (*typedEvents)[0].event.Output.MessageOutput.Message)
	require.Equal(t, "value", cloned.Session.Values["preserved"])
	require.Equal(t, "agent", cloned.RunPath[0].String())
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
	sourceState := persisted.InterruptID2State[persisted.ProjectionV1.SourceInterruptID]
	sourceData, ok := sourceState.State.([]byte)
	require.True(t, ok)
	var projectedInputs int
	require.NoError(t, compose.WalkCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if location.Kind != compose.CheckpointValueInput {
				return nil
			}
			if _, projected := value.(*checkpointMessagePlaceholderV1); projected {
				projectedInputs++
			}
			if _, projected := value.(*checkpointMessageSlicePlaceholderV1); projected {
				projectedInputs++
			}
			return nil
		}))
	require.Zero(t, projectedInputs,
		"gob re-encoding cannot project this input while preserving byte-identical ResumeInfo.Data")

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
	sourceID := outer.ProjectionV1.SourceInterruptID
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))
	source := outer.InterruptID2State[sourceID]
	sourceData, ok := source.State.([]byte)
	require.True(t, ok)

	canonical := schema.UserAgenticMessage("canonical")
	typedSetMessageID(canonical, "agentic-canonical")
	inline := schema.UserAgenticMessage("inline")
	prepared, err := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind == compose.CheckpointValueState {
				return &agenticState{Messages: []*schema.AgenticMessage{canonical}}, true, nil
			}
			return value, false, nil
		})
	require.NoError(t, err)

	index, err := buildCheckpointProjectionIndex(prepared)
	require.NoError(t, err)
	var projected bool
	var valueLocation compose.CheckpointValueLocation
	projectedData, err := compose.TransformCheckpointValues(prepared, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) (any, bool, error) {
			if projected || (location.Kind != compose.CheckpointValueInput &&
				location.Kind != compose.CheckpointValueChannel) {
				return value, false, nil
			}
			entries, hasProjection := index.projectAgenticMessages(
				[]*schema.AgenticMessage{inline, canonical})
			require.True(t, hasProjection)
			valueLocation = location
			projected = true
			return &checkpointAgenticMessageSlicePlaceholderV1{Entries: entries}, true, nil
		})
	require.NoError(t, err)
	require.True(t, projected)

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
	restored, ok := outer.State.(*compose.InterruptInfo)
	require.True(t, ok)
	state, ok := restored.State.(*State)
	require.True(t, ok)
	require.Equal(t, "inline", state.Messages[0].Content)
	require.Equal(t, canonical, state.Messages[1])
	require.NotSame(t, canonical, state.Messages[1])
}
