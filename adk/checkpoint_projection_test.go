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
	require.True(t, reflect.DeepEqual(original.Data, resumeInfo.InterruptInfo.Data))

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
	t.Run("reserved_interrupt_id", func(t *testing.T) {
		require.ErrorContains(t, validateRunnerProjectionReservedIDs(
			map[string]Address{"_eino_user": {}}, nil), "reserved checkpoint metadata prefix")
		require.ErrorContains(t, validateRunnerProjectionReservedIDs(nil,
			map[string]core.InterruptState{"_eino_user": {}}), "reserved checkpoint metadata prefix")
	})
}

func TestRunnerCheckpointProjectionReferenceValidation(t *testing.T) {
	t.Run("run_context_slice_must_be_complete", func(t *testing.T) {
		refs := []runCtxMessageProjectionV1{{
			Target:       runCtxTargetRootInput,
			Index:        0,
			TargetLength: 2,
			Inline:       schema.UserMessage("first"),
		}}
		require.ErrorContains(t, validateRunCtxProjectionRefs(refs, 1), "incomplete")
	})
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
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				require.NoError(t, event.Err)
				if event.Action != nil && event.Action.Interrupted != nil {
					for _, interruptCtx := range event.Action.Interrupted.InterruptContexts {
						interruptIDs = append(interruptIDs, interruptCtx.ID)
					}
				}
			}
			require.Len(t, interruptIDs, 1)
			require.Equal(t, 1, enhanced.callCount())

			raw, exists, err := store.Get(context.Background(), name)
			require.NoError(t, err)
			require.True(t, exists)
			var persisted serialization
			require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&persisted))
			require.NotNil(t, persisted.ProjectionV1)
			require.NotEmpty(t, persisted.ProjectionV1.ToolResultRefs)

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
	var projectedInput bool
	require.NoError(t, compose.WalkCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if location.Kind != compose.CheckpointValueInput {
				return nil
			}
			_, projectedInput = value.(*checkpointMessagePlaceholderV1)
			if !projectedInput {
				_, projectedInput = value.(*checkpointMessageSlicePlaceholderV1)
			}
			return nil
		}))
	require.False(t, projectedInput,
		"gob re-encoding cannot project this input while preserving byte-identical ResumeInfo.Data")

	store := newCheckpointCompatStore()
	require.NoError(t, store.Set(context.Background(), spec.Name, raw))
	runner := NewRunner(context.Background(), RunnerConfig{
		Agent:           newCheckpointCompatCancelResumeAgent(t),
		CheckPointStore: store,
	})
	iter, err := runner.Resume(context.Background(), spec.Name)
	require.NoError(t, err)
	var completed bool
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
			completed = true
		}
	}
	require.True(t, completed)
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
	source := outer.InterruptID2State[outer.ProjectionV1.SourceInterruptID]
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

	require.NoError(t, hydrateNestedInterruptInfoPlaceholders(outer, index))
	restored, ok := outer.State.(*compose.InterruptInfo)
	require.True(t, ok)
	state, ok := restored.State.(*State)
	require.True(t, ok)
	require.Equal(t, "inline", state.Messages[0].Content)
	require.Equal(t, canonical, state.Messages[1])
	require.NotSame(t, canonical, state.Messages[1])
}
