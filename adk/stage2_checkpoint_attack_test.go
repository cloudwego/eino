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
	require.ErrorContains(t, err, "invalid run context coordinates",
		"negative lane depth must not select the root lane")
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
	require.Error(t, err, "a missing projection entry silently truncated the root input")
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
	require.Error(t, err, "an implicit nil placeholder entry was accepted as valid checkpoint data")
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
	require.Error(t, err, "a scalar event reference accepted an impossible nil projection")
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
	sourceData := outer.InterruptID2State[outer.ProjectionV1.SourceInterruptID].State.([]byte)

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
	require.ErrorContains(t, err, "tool call ID")
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
		require.ErrorContains(t, hydrateInfoToolResult(extra, ref, index),
			"already populated")
	})

	t.Run("enhanced_source_with_standard_target", func(t *testing.T) {
		extra := &compose.ToolsInterruptAndRerunExtra{
			ExecutedTools: map[string]string{"call": "standard"},
		}
		ref := infoToolResultProjectionV1{
			ToolCallID: "call",
			Source:     enhancedSource,
		}
		require.ErrorContains(t, hydrateInfoToolResult(extra, ref, index),
			"already populated")
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
	require.ErrorContains(t, err, "does not match metadata")

	agenticSource, ok := index.sourceForAgenticMessage(agenticMessage)
	require.True(t, ok)
	agenticSource.Kind = projectionMessageKindSchema
	_, err = index.agenticMessage(agenticSource)
	require.ErrorContains(t, err, "does not match metadata")
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
