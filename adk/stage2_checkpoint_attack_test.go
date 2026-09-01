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
	"encoding/json"
	"os"
	"path/filepath"
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

func TestAttack_ProjectionRejectsNilToolCallReferenceWithoutPanic(t *testing.T) {
	// Attack: mark a tool-call projection reference as nil.
	// Impact: corrupt checkpoint bytes can panic the checkpoint loader.
	// Expected: malformed tool-call references return an error without panicking.
	info := &compose.InterruptInfo{
		RerunNodesExtra: map[string]any{
			"tools": &compose.ToolsInterruptAndRerunExtra{},
		},
	}
	ref := infoMessageProjectionV1{
		Target:        infoTargetRerunToolCalls,
		ContextIndex:  -1,
		ParentDepth:   0,
		RerunExtraKey: "tools",
		MessageIndex:  -1,
		IsNil:         true,
	}
	index := &checkpointProjectionIndex{byID: make(map[string][]canonicalCheckpointMessage)}

	var err error
	require.NotPanics(t, func() {
		err = hydrateComposeInterruptInfoRefs(info, []infoMessageProjectionV1{ref}, index)
	})
	require.Error(t, err)
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

func TestAttack_FrozenGobFixturesResumeWithoutMutation(t *testing.T) {
	// Attack: decode and resume immutable main-produced Invoke and Stream fixtures.
	// Impact: field/type changes in gob schemas can make deployed checkpoints unreadable.
	// Expected: fixture bytes retain their frozen digest and both modes resume successfully.
	manifestData, err := os.ReadFile(filepath.Join(checkpointCompatDir, "manifest.json"))
	require.NoError(t, err)
	var manifest checkpointCompatManifest
	require.NoError(t, json.Unmarshal(manifestData, &manifest))

	wanted := map[string]bool{"single_invoke": true, "single_stream": true}
	for _, fixture := range manifest.Fixtures {
		if !wanted[fixture.Name] {
			continue
		}
		t.Run(fixture.Name, func(t *testing.T) {
			raw := readCheckpointCompatFixture(t, filepath.Join(checkpointCompatDir, fixture.File))
			before := append([]byte(nil), raw...)
			resumeCheckpointCompatCandidate(t, fixture, raw, fixture.InterruptIDs,
				len(fixture.InterruptIDs), 0)
			require.Equal(t, before, raw, "compatibility fixture bytes were mutated during resume")
		})
		delete(wanted, fixture.Name)
	}
	require.Empty(t, wanted)
}
