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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

func TestAttack_ProjectionHydrationDoesNotCreateMessageAliases(t *testing.T) {
	// Attack: hydrate two logically independent checkpoint fields from one canonical message.
	// Impact: mutating resume input must not silently mutate interrupt info or graph state.
	// Expected: hydrated fields are value-equal but independently mutable.
	canonical := schema.AssistantMessage("original", nil)
	canonical.Extra = map[string]any{"owner": "canonical"}
	typedSetMessageID(canonical, "shared-message")

	independent := *canonical
	independent.Extra = make(map[string]any, len(canonical.Extra))
	for key, value := range canonical.Extra {
		independent.Extra[key] = value
	}
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage([]string{"graph"}, 0, canonical)

	runCtx := &runContext{RootInput: &AgentInput{Messages: []*schema.Message{&independent}}}
	projected := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(projected, index, projection)
	require.Len(t, projection.RunCtxRefs, 1)
	require.NoError(t, hydrateRunContextMessages(projected, projection.RunCtxRefs,
		projection.RunCtxRefCount, index))

	hydrated := projected.RootInput.Messages[0]
	hydrated.Content = "mutated"
	hydrated.Extra["owner"] = "mutated"
	require.Equal(t, "original", canonical.Content,
		"hydrating a reference created a pointer alias to the canonical checkpoint message")
	require.Equal(t, "canonical", canonical.Extra["owner"],
		"hydrating a reference shared nested mutable message data")
}

func TestAttack_ProjectionRejectsNegativeLaneDepth(t *testing.T) {
	// Attack: corrupt a lane-event reference with a negative depth.
	// Impact: accepting it redirects the reference to lane zero and silently hydrates the wrong event.
	// Expected: malformed projection coordinates fail checkpoint loading.
	message := schema.AssistantMessage("lane", nil)
	typedSetMessageID(message, "lane-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, message)

	runCtx := &runContext{Session: &runSession{
		Values:    map[string]any{},
		valuesMtx: nil,
		LaneEvents: &laneEvents{Events: []*agentEventWrapper{{
			AgentEvent: EventFromMessage(message, nil, schema.Assistant, ""),
		}}},
	}}
	projected := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(projected, index, projection)
	require.Len(t, projection.RunCtxRefs, 1)
	require.Equal(t, runCtxTargetLaneEvent, projection.RunCtxRefs[0].Target)
	projection.RunCtxRefs[0].LaneDepth = -1

	err := hydrateRunContextMessages(projected, projection.RunCtxRefs,
		projection.RunCtxRefCount, index)
	require.EqualError(t, err, "checkpoint projection has invalid run context coordinates -1/0",
		"negative lane depth must not select the root lane")
}

func TestAttack_ProjectionRejectsIgnoredMessageTargetCoordinates(t *testing.T) {
	tests := []struct {
		name string
		ref  runCtxMessageProjectionV1
		err  string
	}{
		{
			name: "schema_root_input_lane_depth",
			ref: runCtxMessageProjectionV1{
				Target:       runCtxTargetRootInput,
				Index:        0,
				LaneDepth:    1,
				TargetLength: 1,
			},
			err: `checkpoint projection target "root_input" has invalid lane depth 1`,
		},
		{
			name: "agentic_root_input_lane_depth",
			ref: runCtxMessageProjectionV1{
				Target:       runCtxTargetAgenticRootInput,
				Index:        0,
				LaneDepth:    1,
				TargetLength: 1,
			},
			err: `checkpoint projection target "agentic_root_input" has invalid lane depth 1`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, validateRunCtxProjectionRefs(
				[]runCtxMessageProjectionV1{tt.ref}, 1), tt.err)
		})
	}

	infoTests := []struct {
		name string
		ref  infoMessageProjectionV1
		err  string
	}{
		{
			name: "state_parent_depth",
			ref: infoMessageProjectionV1{
				Target:       infoTargetStateMessage,
				ContextIndex: -1,
				ParentDepth:  1,
				MessageIndex: 0,
				TargetLength: 1,
			},
			err: "checkpoint projection has invalid interrupt state coordinates",
		},
		{
			name: "state_rerun_extra_key",
			ref: infoMessageProjectionV1{
				Target:        infoTargetStateMessage,
				ContextIndex:  -1,
				RerunExtraKey: "ignored",
				MessageIndex:  0,
				TargetLength:  1,
			},
			err: "checkpoint projection has invalid interrupt state coordinates",
		},
		{
			name: "context_state_rerun_extra_key",
			ref: infoMessageProjectionV1{
				Target:        infoTargetContextStateMessage,
				ContextIndex:  0,
				RerunExtraKey: "ignored",
				MessageIndex:  0,
				TargetLength:  1,
			},
			err: "checkpoint projection has invalid context state coordinates",
		},
		{
			name: "rerun_tool_calls_parent_depth",
			ref: infoMessageProjectionV1{
				Target:        infoTargetRerunToolCalls,
				ContextIndex:  -1,
				ParentDepth:   1,
				RerunExtraKey: "tools",
				MessageIndex:  -1,
			},
			err: "checkpoint projection has invalid rerun tool calls coordinates",
		},
		{
			name: "context_tool_calls_rerun_extra_key",
			ref: infoMessageProjectionV1{
				Target:        infoTargetContextToolCalls,
				ContextIndex:  0,
				RerunExtraKey: "ignored",
				MessageIndex:  -1,
			},
			err: "checkpoint projection has invalid context tool calls coordinates",
		},
	}
	for _, tt := range infoTests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, validateInfoProjectionRefs(
				[]infoMessageProjectionV1{tt.ref}, 1), tt.err)
		})
	}
}

func TestAttack_ProjectionRejectsConflictingAndIgnoredPayloadFields(t *testing.T) {
	schemaCanonical := schema.UserMessage("schema canonical")
	typedSetMessageID(schemaCanonical, "schema-canonical")
	agenticCanonical := schema.UserAgenticMessage("agentic canonical")
	typedSetMessageID(agenticCanonical, "agentic-canonical")
	schemaInline := schema.UserMessage("schema inline")
	agenticInline := schema.UserAgenticMessage("agentic inline")

	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, schemaCanonical)
	index.addAgenticMessage(nil, 0, agenticCanonical)
	schemaSource, ok := index.sourceForSchemaMessage(schemaCanonical)
	require.True(t, ok)
	agenticSource, ok := index.sourceForAgenticMessage(agenticCanonical)
	require.True(t, ok)

	t.Run("valid_run_context_forms", func(t *testing.T) {
		tests := []struct {
			name    string
			agentic bool
			ref     runCtxMessageProjectionV1
		}{
			{
				name: "schema_source",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1, Source: schemaSource,
				},
			},
			{
				name: "schema_inline",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1, Inline: schemaInline,
				},
			},
			{
				name: "schema_explicit_nil",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1, IsNil: true,
				},
			},
			{
				name:    "agentic_source",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1, Source: agenticSource,
				},
			},
			{
				name:    "agentic_inline",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1, AgenticInline: agenticInline,
				},
			},
			{
				name:    "agentic_explicit_nil",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1, IsNil: true,
				},
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				runCtx := &runContext{RootInput: &AgentInput{}}
				if tt.agentic {
					runCtx.AgenticRootInput = &TypedAgentInput[*schema.AgenticMessage]{}
				}
				require.NoError(t, hydrateRunContextMessages(
					runCtx, []runCtxMessageProjectionV1{tt.ref}, 1, index))
			})
		}
	})

	t.Run("invalid_run_context_forms", func(t *testing.T) {
		const schemaPayloadError = "checkpoint projection schema message payload must contain exactly one of source, inline, or explicit nil"
		const agenticPayloadError = "checkpoint projection agentic message payload must contain exactly one of source, inline, or explicit nil"
		tests := []struct {
			name    string
			agentic bool
			ref     runCtxMessageProjectionV1
			err     string
		}{
			{
				name: "schema_matching_and_opposite_inline",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1,
					Inline: schemaInline, AgenticInline: agenticInline,
				},
				err: schemaPayloadError,
			},
			{
				name: "schema_source_and_opposite_inline",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1,
					Source: schemaSource, AgenticInline: agenticInline,
				},
				err: schemaPayloadError,
			},
			{
				name: "schema_nil_and_matching_inline",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1,
					Inline: schemaInline, IsNil: true,
				},
				err: schemaPayloadError,
			},
			{
				name: "schema_nil_and_opposite_inline",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1,
					AgenticInline: agenticInline, IsNil: true,
				},
				err: schemaPayloadError,
			},
			{
				name: "schema_partial_source",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1,
					Source: checkpointMessageSourceV1{Kind: projectionMessageKindSchema},
					Inline: schemaInline,
				},
				err: schemaPayloadError,
			},
			{
				name: "schema_ignored_streaming",
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetRootInput, TargetLength: 1,
					Inline: schemaInline, WasStreaming: true,
				},
				err: `checkpoint projection target "root_input" has unexpected streaming state`,
			},
			{
				name:    "agentic_matching_and_opposite_inline",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1,
					Inline: schemaInline, AgenticInline: agenticInline,
				},
				err: agenticPayloadError,
			},
			{
				name:    "agentic_source_and_opposite_inline",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1,
					Source: agenticSource, Inline: schemaInline,
				},
				err: agenticPayloadError,
			},
			{
				name:    "agentic_nil_and_matching_inline",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1,
					AgenticInline: agenticInline, IsNil: true,
				},
				err: agenticPayloadError,
			},
			{
				name:    "agentic_nil_and_opposite_inline",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1,
					Inline: schemaInline, IsNil: true,
				},
				err: agenticPayloadError,
			},
			{
				name:    "agentic_partial_source",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1,
					Source:        checkpointMessageSourceV1{Digest: "partial"},
					AgenticInline: agenticInline,
				},
				err: agenticPayloadError,
			},
			{
				name:    "agentic_ignored_streaming",
				agentic: true,
				ref: runCtxMessageProjectionV1{
					Target: runCtxTargetAgenticRootInput, TargetLength: 1,
					AgenticInline: agenticInline, WasStreaming: true,
				},
				err: `checkpoint projection target "agentic_root_input" has unexpected streaming state`,
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				runCtx := &runContext{RootInput: &AgentInput{}}
				if tt.agentic {
					runCtx.AgenticRootInput = &TypedAgentInput[*schema.AgenticMessage]{}
				}
				require.EqualError(t, hydrateRunContextMessages(
					runCtx, []runCtxMessageProjectionV1{tt.ref}, 1, index), tt.err)
				require.Nil(t, runCtx.RootInput.Messages)
				if tt.agentic {
					require.Nil(t, runCtx.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage]).Messages)
				}
			})
		}
	})

	t.Run("interrupt_state_opposite_inline", func(t *testing.T) {
		schemaState := &State{}
		schemaRef := infoMessageProjectionV1{
			Target: infoTargetStateMessage, ContextIndex: -1,
			MessageIndex: 0, TargetLength: 1,
			Inline: schemaInline, AgenticInline: agenticInline,
		}
		require.EqualError(t, hydrateComposeInterruptInfoRefs(
			&compose.InterruptInfo{State: schemaState},
			[]infoMessageProjectionV1{schemaRef}, index),
			"checkpoint projection schema message payload must contain exactly one of source, inline, or explicit nil")
		require.Nil(t, schemaState.Messages)

		agenticState := &agenticState{}
		agenticRef := infoMessageProjectionV1{
			Target: infoTargetStateMessage, ContextIndex: -1,
			MessageIndex: 0, TargetLength: 1,
			Inline: schemaInline, AgenticInline: agenticInline,
		}
		require.EqualError(t, hydrateComposeInterruptInfoRefs(
			&compose.InterruptInfo{State: agenticState},
			[]infoMessageProjectionV1{agenticRef}, index),
			"checkpoint projection agentic message payload must contain exactly one of source, inline, or explicit nil")
		require.Nil(t, agenticState.Messages)
	})
}

func TestAttack_ProjectionRejectsMissingSliceReference(t *testing.T) {
	// Attack: remove the final reference from a projected two-message root input.
	// Impact: hydration can silently truncate chat history without any decode error.
	// Expected: projection metadata carries enough shape information to reject a missing entry.
	canonical := schema.AssistantMessage("canonical", nil)
	typedSetMessageID(canonical, "canonical-message")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, canonical)

	first := *canonical
	second := *canonical
	runCtx := &runContext{RootInput: &AgentInput{
		Messages: []*schema.Message{&first, &second},
	}}
	projected := cloneRunContextForCheckpointProjection(runCtx)
	projection := &checkpointProjectionV1{}
	projectRunContextMessages(projected, index, projection)
	require.Len(t, projection.RunCtxRefs, 2)

	projection.RunCtxRefCount--
	err := hydrateRunContextMessages(projected, projection.RunCtxRefs[:1],
		projection.RunCtxRefCount, index)
	require.EqualError(t, err, `checkpoint projection has incomplete run context slice "root_input/0"`)
}

func TestAttack_ProjectionRejectsImplicitNilSliceEntry(t *testing.T) {
	// Attack: replace a checkpoint value with a slice placeholder whose entry is neither
	// explicitly nil, inline, nor a source reference.
	// Impact: accepting it silently turns a persisted message into nil.
	// Expected: malformed placeholder entries fail hydration.
	spec := checkpointCompatFixture{
		Name:         "attack-corrupt-placeholder",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  1024,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)

	source := outer.InterruptID2State[outer.ProjectionV1.SourceInterruptID]
	sourceData, ok := source.State.([]byte)
	require.True(t, ok)
	index, err := buildCheckpointProjectionIndex(sourceData)
	require.NoError(t, err)

	replaced := false
	corrupt, err := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) (any, bool, error) {
			if replaced || location.Kind == compose.CheckpointValueState {
				return value, false, nil
			}
			replaced = true
			return &checkpointMessageSlicePlaceholderV1{
				Entries: []checkpointMessageSliceEntryV1{{}},
			}, true, nil
		})
	require.NoError(t, err)
	require.True(t, replaced)

	_, err = hydrateComposeCheckpointValues(corrupt, index)
	require.EqualError(t, err, "checkpoint projection inline message is missing")

	replaced = false
	corrupt, err = compose.TransformCheckpointValues(sourceData, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) (any, bool, error) {
			if replaced || location.Kind == compose.CheckpointValueState {
				return value, false, nil
			}
			replaced = true
			return &checkpointAgenticMessageSlicePlaceholderV1{
				Entries: []checkpointAgenticMessageSliceEntryV1{{}},
			}, true, nil
		})
	require.NoError(t, err)
	require.True(t, replaced)

	_, err = hydrateComposeCheckpointValues(corrupt, index)
	require.EqualError(t, err, "checkpoint projection inline agentic message is missing")
}

func TestAttack_ProjectionRejectsConflictingComposeSliceEntryPayloads(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:         "attack-conflicting-compose-slice-entry",
		Depth:        1,
		PayloadField: "content",
		PayloadSize:  1024,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)

	sourceData, ok := outer.InterruptID2State[outer.ProjectionV1.SourceInterruptID].State.([]byte)
	require.True(t, ok)
	index, err := buildCheckpointProjectionIndex(sourceData)
	require.NoError(t, err)

	schemaMessage := schema.UserMessage("schema")
	typedSetMessageID(schemaMessage, "conflicting-schema-message")
	index.addSchemaMessage(nil, 0, schemaMessage)
	schemaSource, ok := index.sourceForSchemaMessage(schemaMessage)
	require.True(t, ok)

	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "conflicting-agentic-message")
	index.addAgenticMessage(nil, 0, agenticMessage)
	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)

	tests := []struct {
		name        string
		replacement any
		wantErr     string
	}{
		{
			name: "schema",
			replacement: &checkpointMessageSlicePlaceholderV1{
				Entries: []checkpointMessageSliceEntryV1{{
					Source: &schemaSource,
					Inline: schemaMessage,
				}},
			},
			wantErr: "checkpoint projection message has both inline data and a source reference",
		},
		{
			name: "agentic",
			replacement: &checkpointAgenticMessageSlicePlaceholderV1{
				Entries: []checkpointAgenticMessageSliceEntryV1{{
					Source: &agenticSource,
					Inline: agenticMessage,
				}},
			},
			wantErr: "checkpoint projection agentic message has both inline data and a source reference",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			replaced := false
			corrupt, transformErr := compose.TransformCheckpointValues(sourceData, &gobSerializer{},
				func(_ compose.NodePath, location compose.CheckpointValueLocation,
					value any) (any, bool, error) {
					if replaced || location.Kind == compose.CheckpointValueState {
						return value, false, nil
					}
					replaced = true
					return tt.replacement, true, nil
				})
			require.NoError(t, transformErr)
			require.True(t, replaced)

			_, hydrateErr := hydrateComposeCheckpointValues(corrupt, index)
			require.EqualError(t, hydrateErr, tt.wantErr,
				"a compose slice entry with both source and inline payload was accepted")
		})
	}
}

func TestAttack_ProjectionKeepsNilAgenticComposeSliceInline(t *testing.T) {
	spec := checkpointCompatFixture{
		Name:         "attack-nil-agentic-compose-slice",
		Cancel:       true,
		PayloadField: "content",
		PayloadSize:  1024,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)
	sourceID := outer.ProjectionV1.SourceInterruptID
	require.NoError(t, restoreRunnerCheckpointProjection(&outer))
	sourceData, ok := outer.InterruptID2State[sourceID].State.([]byte)
	require.True(t, ok)

	canonical := schema.UserAgenticMessage("canonical")
	typedSetMessageID(canonical, "nil-agentic-canonical")
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
	messages := []*schema.AgenticMessage{nil, canonical}
	entries, projected := index.projectComposeAgenticMessages(messages)
	require.False(t, projected,
		"a compose slice containing nil must remain inline instead of producing an unhydratable projection")
	require.Nil(t, entries)
	require.Len(t, messages, 2)
	require.Nil(t, messages[0])
	require.Equal(t, canonical, messages[1])
}

func TestAttack_ProjectionRejectsNilEventReference(t *testing.T) {
	// Attack: mark a scalar event projection reference as nil.
	// Impact: malformed metadata can erase a persisted event while still loading successfully.
	// Expected: nil is accepted only for slice entries where it is an explicit value.
	runCtx := &runContext{Session: &runSession{
		Events: []*agentEventWrapper{{
			AgentEvent: EventFromMessage(nil, nil, schema.Assistant, ""),
		}},
	}}
	ref := runCtxMessageProjectionV1{
		Target: runCtxTargetEvent,
		Index:  0,
		IsNil:  true,
	}
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}

	err := hydrateRunContextMessages(runCtx, []runCtxMessageProjectionV1{ref}, 1, index)
	require.EqualError(t, err,
		`checkpoint projection target "event" has invalid lane depth 0`)
}

func TestAttack_ProjectionSourceSelectionIsDeterministic(t *testing.T) {
	// Attack: provide equivalent candidate sources through a map with unstable iteration order.
	// Impact: nondeterministic source selection changes checkpoint layout and corruption diagnostics.
	// Expected: the lexically first valid interrupt ID is always selected.
	spec := checkpointCompatFixture{
		Name:         "attack-source-order",
		PayloadField: "content",
		PayloadSize:  1024,
	}
	raw, _, _ := captureCheckpointCompatFixture(t, spec)
	var outer serialization
	require.NoError(t, gob.NewDecoder(bytes.NewReader(raw)).Decode(&outer))
	require.NotNil(t, outer.ProjectionV1)
	sourceID := outer.ProjectionV1.SourceInterruptID
	source, exists := outer.InterruptID2State[sourceID]
	require.True(t, exists)
	sourceData, ok := source.State.([]byte)
	require.True(t, ok)

	states := map[string]core.InterruptState{
		"z-source": {State: sourceData},
		"a-source": {State: sourceData},
	}
	for i := 0; i < 100; i++ {
		id, _, index, err := findProjectionSource("", states)
		require.NoError(t, err)
		require.NotNil(t, index)
		require.Equal(t, "a-source", id)
	}
}

func TestAttack_ProjectionRejectsToolResultCallIDRelabel(t *testing.T) {
	// Attack: point a target call ID at a different source call ID.
	// Impact: one tool's output can be restored under another tool call.
	// Expected: source and target call IDs must match.
	source := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "call-a",
		Digest:      "digest",
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"call-a": {{source: source, text: "result-a"}},
	}}
	ref := infoToolResultProjectionV1{
		ToolCallID: "call-b",
		Source:     source,
	}

	err := hydrateInfoToolResult(&compose.ToolsInterruptAndRerunExtra{}, ref, index)
	require.EqualError(t, err, `checkpoint projection tool call ID "call-b" does not match source "call-a"`)
}

func TestAttack_ToolResultProjectionCoordinatesDoNotAlias(t *testing.T) {
	firstSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "b/c",
		Digest:      "first-digest",
	}
	secondSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "c",
		Digest:      "second-digest",
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"b/c": {{source: firstSource, text: "first-result"}},
		"c":   {{source: secondSource, text: "second-result"}},
	}}
	firstTarget := &compose.ToolsInterruptAndRerunExtra{}
	secondTarget := &compose.ToolsInterruptAndRerunExtra{}
	info := &compose.InterruptInfo{RerunNodesExtra: map[string]any{
		"a":   firstTarget,
		"a/b": secondTarget,
	}}
	refs := []infoToolResultProjectionV1{
		{
			Target:        infoTargetRerunToolResult,
			ContextIndex:  -1,
			RerunExtraKey: "a",
			ToolCallID:    "b/c",
			Source:        firstSource,
		},
		{
			Target:        infoTargetRerunToolResult,
			ContextIndex:  -1,
			RerunExtraKey: "a/b",
			ToolCallID:    "c",
			Source:        secondSource,
		},
	}

	require.NoError(t, hydrateComposeInterruptInfoToolResults(info, refs, len(refs), index))
	require.Equal(t, map[string]string{"b/c": "first-result"}, firstTarget.ExecutedTools)
	require.Equal(t, map[string]string{"c": "second-result"}, secondTarget.ExecutedTools)
}

func TestAttack_ProjectionRejectsIgnoredToolResultTargetCoordinates(t *testing.T) {
	source := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      "digest",
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"call": {{source: source, text: "result"}},
	}}

	tests := []struct {
		name string
		ref  infoToolResultProjectionV1
		info *compose.InterruptInfo
		err  string
	}{
		{
			name: "rerun_parent_depth",
			ref: infoToolResultProjectionV1{
				Target:        infoTargetRerunToolResult,
				ContextIndex:  -1,
				ParentDepth:   1,
				RerunExtraKey: "tools",
				ToolCallID:    "call",
				Source:        source,
			},
			info: &compose.InterruptInfo{RerunNodesExtra: map[string]any{
				"tools": &compose.ToolsInterruptAndRerunExtra{},
			}},
			err: "checkpoint projection has invalid rerun tool result target",
		},
		{
			name: "context_rerun_extra_key",
			ref: infoToolResultProjectionV1{
				Target:        infoTargetContextToolResult,
				ContextIndex:  0,
				RerunExtraKey: "ignored",
				ToolCallID:    "call",
				Source:        source,
			},
			info: &compose.InterruptInfo{InterruptContexts: []*InterruptCtx{{
				Info: &compose.ToolsInterruptAndRerunExtra{},
			}}},
			err: "checkpoint projection has invalid context tool result target",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.EqualError(t, hydrateComposeInterruptInfoToolResults(
				tt.info, []infoToolResultProjectionV1{tt.ref}, 1, index), tt.err)
		})
	}
}

func TestAttack_ProjectionRejectsCrossKindToolResultConflict(t *testing.T) {
	standardSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      "standard-digest",
	}
	enhancedResult := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: "enhanced",
	}}}
	enhancedSource := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindEnhanced,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      "enhanced-digest",
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"call": {
			{source: standardSource, text: "standard"},
			{source: enhancedSource, enhanced: enhancedResult},
		},
	}}

	t.Run("standard_source_with_enhanced_target", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{
			ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": enhancedResult},
		}
		ref := infoToolResultProjectionV1{
			ToolCallID: "call",
			Source:     standardSource,
		}
		require.EqualError(t, hydrateInfoToolResult(extra, ref, index),
			`checkpoint projection tool result target "call" is already populated`)
	})

	t.Run("enhanced_source_with_standard_target", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{
			ExecutedTools: map[string]string{"call": "standard"},
		}
		ref := infoToolResultProjectionV1{
			ToolCallID: "call",
			Source:     enhancedSource,
		}
		require.EqualError(t, hydrateInfoToolResult(extra, ref, index),
			`checkpoint projection tool result target "call" is already populated`)
	})

	t.Run("writer_keeps_conflict_inline", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{
			ExecutedTools:         map[string]string{"call": "standard"},
			ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": enhancedResult},
		}
		projection := &checkpointProjectionV1{}
		projectInfoToolResults(extra, infoProjectionTarget{
			kind:         infoTargetRerunToolCalls,
			contextIndex: -1,
			rerunKey:     "tools",
		}, index, projection)
		require.Empty(t, projection.ToolResultRefs)
		require.Equal(t, map[string]string{"call": "standard"}, extra.ExecutedTools)
		require.Equal(t, map[string]*schema.ToolResult{"call": enhancedResult},
			extra.ExecutedEnhancedTools)
	})
}

func TestAttack_ProjectionDoesNotEmitEmptyToolCallIDReference(t *testing.T) {
	// Attack: present a successful tool result under an empty call ID.
	// Impact: the writer can emit metadata that its own reader rejects.
	// Expected: an invalid result remains inline.
	source := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "",
		Digest:      "digest",
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"": {{source: source, text: "result"}},
	}}
	extra := &compose.ToolsInterruptAndRerunExtra{
		ExecutedTools: map[string]string{"": "result"},
	}
	projection := &checkpointProjectionV1{}

	projectInfoToolResults(extra, infoProjectionTarget{
		kind:         infoTargetRerunToolCalls,
		contextIndex: -1,
		rerunKey:     "tools",
	}, index, projection)
	require.Empty(t, projection.ToolResultRefs)
	require.Equal(t, map[string]string{"": "result"}, extra.ExecutedTools)
}

func TestAttack_StateScopedToolResultStaysInline(t *testing.T) {
	// Attack: place ToolsNode metadata directly in InterruptInfo.State.
	// Impact: projecting it as context data creates impossible coordinates.
	// Expected: unsupported locations remain inline.
	source := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindString,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      "digest",
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"call": {{source: source, text: "result"}},
	}}
	extra := &compose.ToolsInterruptAndRerunExtra{
		ExecutedTools: map[string]string{"call": "result"},
	}
	projection := &checkpointProjectionV1{}

	var value any = extra
	projectInfoValueMessages(&value, infoProjectionTarget{
		kind:         infoTargetStateMessage,
		contextIndex: -1,
	}, index, projection)
	require.Empty(t, projection.ToolResultRefs)
	require.Equal(t, map[string]string{"call": "result"}, extra.ExecutedTools)
}

func TestAttack_ProjectionRejectsMismatchedMessageSourceKind(t *testing.T) {
	// Attack: change only the source kind while retaining valid path, ID, index, and digest.
	// Impact: corrupted metadata can cross schema and agentic wire domains undetected.
	// Expected: source kind is part of the reference identity.
	schemaMessage := schema.UserMessage("schema")
	typedSetMessageID(schemaMessage, "schema")
	agenticMessage := schema.UserAgenticMessage("agentic")
	typedSetMessageID(agenticMessage, "agentic")
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}
	index.addSchemaMessage(nil, 0, schemaMessage)
	index.addAgenticMessage(nil, 0, agenticMessage)

	schemaSource, ok := index.sourceForSchemaMessage(schemaMessage)
	require.True(t, ok)
	schemaSource.Kind = projectionMessageKindAgentic
	_, err := index.schemaMessage(schemaSource)
	require.EqualError(t, err, `checkpoint projection source message "schema" does not match metadata`)

	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)
	agenticSource.Kind = projectionMessageKindSchema
	_, err = index.agenticMessage(agenticSource)
	require.EqualError(t, err, `checkpoint projection source agentic message "agentic" does not match metadata`)
}

func TestAttack_NestedToolResultOnlyProjectionRoundTrip(t *testing.T) {
	// Attack: nest an InterruptInfo whose only projected value is a tool result.
	// Impact: top-level message reference counts remain zero and can bypass hydration.
	// Expected: recursive hydration restores the nested result without rerunning the tool.
	result := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: "canonical",
	}}}
	digest, ok := projectionMessageDigest(result)
	require.True(t, ok)
	source := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindEnhanced,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      digest,
	}
	index := &checkpointProjectionIndex{
		byID: make(map[string][]canonicalCheckpointMessage),
		toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
			"call": {{source: source, enhanced: result}},
		},
	}
	extra := &compose.ToolsInterruptAndRerunExtra{
		ExecutedEnhancedTools: map[string]*schema.ToolResult{"call": result},
	}
	nested := &compose.InterruptInfo{RerunNodesExtra: map[string]any{"tools": extra}}
	outer := &compose.InterruptInfo{State: nested}
	projection := &checkpointProjectionV1{}
	projectComposeInterruptInfoMessages(outer, nil, index, projection)
	require.Empty(t, projection.InfoRefs)
	require.Empty(t, projection.ToolResultRefs)
	require.IsType(t, &checkpointInterruptInfoPlaceholderV1{}, outer.State)
	require.Empty(t, extra.ExecutedEnhancedTools)

	info := &InterruptInfo{Data: &ChatModelAgentInterruptInfo{Info: outer}}
	require.NoError(t, hydrateInterruptInfoMessages(info, nil, 0, index))
	restoredNested, ok := outer.State.(*compose.InterruptInfo)
	require.True(t, ok)
	restoredExtra, ok := restoredNested.RerunNodesExtra["tools"].(*compose.ToolsInterruptAndRerunExtra)
	require.True(t, ok)
	require.Equal(t, result, restoredExtra.ExecutedEnhancedTools["call"])
	require.NotSame(t, result, restoredExtra.ExecutedEnhancedTools["call"])
}

func TestAttack_EnhancedToolResultHydrationHasNoNestedAliases(t *testing.T) {
	// Attack: mutate a nested part after restoring a projected enhanced result.
	// Impact: a shallow copy would corrupt the canonical compose checkpoint value.
	// Expected: the hydrated result and all nested parts are independent.
	result := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeText,
		Text: "canonical",
	}}}
	digest, ok := projectionMessageDigest(result)
	require.True(t, ok)
	source := checkpointToolResultSourceV1{
		Kind:        projectionToolResultKindEnhanced,
		InterruptID: "interrupt",
		ToolCallID:  "call",
		Digest:      digest,
	}
	index := &checkpointProjectionIndex{toolResultsByCallID: map[string][]canonicalCheckpointToolResult{
		"call": {{source: source, enhanced: result}},
	}}
	extra := &compose.ToolsInterruptAndRerunExtra{}
	ref := infoToolResultProjectionV1{
		ToolCallID: "call",
		Source:     source,
	}

	require.NoError(t, hydrateInfoToolResult(extra, ref, index))
	extra.ExecutedEnhancedTools["call"].Parts[0].Text = "mutated"
	require.Equal(t, "canonical", result.Parts[0].Text)
}

func TestAttack_ProjectionValidationErrorIsDeterministic(t *testing.T) {
	// Attack: corrupt two independently projected slices in one checkpoint.
	// Impact: map iteration can select a different first error for identical bytes.
	// Expected: validation always reports the lexical target first.
	runCtxRefs := []runCtxMessageProjectionV1{
		{
			Target:       runCtxTargetRootInput,
			Index:        0,
			TargetLength: 2,
		},
		{
			Target:       runCtxTargetAgenticRootInput,
			Index:        0,
			TargetLength: 2,
		},
	}
	infoRefs := []infoMessageProjectionV1{
		{
			Target:       infoTargetStateMessage,
			ContextIndex: -1,
			MessageIndex: 0,
			TargetLength: 2,
		},
		{
			Target:       infoTargetContextStateMessage,
			ContextIndex: 0,
			MessageIndex: 0,
			TargetLength: 2,
		},
	}

	for i := 0; i < 100; i++ {
		require.EqualError(t, validateRunCtxProjectionRefs(runCtxRefs, 2),
			`checkpoint projection has incomplete run context slice "agentic_root_input/0"`)
		require.EqualError(t, validateInfoProjectionRefs(infoRefs, 2),
			`checkpoint projection has incomplete interrupt info slice "context_state_message/[]/0/0/"`)
	}
}

func TestAttack_NestedProjectionValidationErrorIsDeterministic(t *testing.T) {
	info := &compose.InterruptInfo{
		RerunNodesExtra: map[string]any{
			"z": (*checkpointInterruptInfoPlaceholderV1)(nil),
			"a": &checkpointInterruptInfoPlaceholderV1{
				Info:     &compose.InterruptInfo{},
				RefCount: 1,
			},
		},
	}
	index := &checkpointProjectionIndex{
		byID: make(map[string][]canonicalCheckpointMessage),
	}

	for i := 0; i < 100; i++ {
		require.EqualError(t, hydrateNestedInterruptInfoPlaceholders(info, index),
			"checkpoint projection interrupt info reference count mismatch: got 0, want 1")
	}
}

func TestAttack_NestedParallelTargetedResumeInvokeStreamParity(t *testing.T) {
	// Attack: checkpoint four parallel AgentTools, then resume only one target in both modes.
	// Impact: sparse ownership or stream divergence can consume sibling state or lose interrupts.
	// Expected: exactly three untouched branches remain interrupted in Invoke and Stream.
	for _, streaming := range []bool{false, true} {
		name := "invoke"
		if streaming {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			spec := checkpointCompatFixture{
				Name:             "attack-parallel-" + name,
				ParallelChildren: 4,
				Streaming:        streaming,
				PayloadField:     "content",
				PayloadSize:      1024,
			}
			raw, interruptIDs, interruptAddresses := captureCheckpointCompatFixture(t, spec)
			require.Len(t, interruptIDs, 4)
			resumeCheckpointCompatCandidate(t, spec, raw, interruptIDs, 1, 3, interruptAddresses)
		})
	}
}
