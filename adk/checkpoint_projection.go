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
	"crypto/sha256"
	"encoding/binary"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sort"
	"strconv"
	"sync"

	"github.com/cloudwego/eino/compose"
	checkpointinternal "github.com/cloudwego/eino/internal/checkpoint"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

const (
	checkpointProjectionVersionV1 = 1
	checkpointProjectionVersionV2 = 2
	checkpointProjectionVersion   = checkpointProjectionVersionV2
	runnerProjectionSentinelID    = "_eino_runner_projection"

	projectionMessageKindSchema   = "schema"
	projectionMessageKindAgentic  = "agentic"
	runCtxTargetRootInput         = "root_input"
	runCtxTargetEvent             = "event"
	runCtxTargetLaneEvent         = "lane_event"
	runCtxTargetAgenticRootInput  = "agentic_root_input"
	runCtxTargetTypedEvent        = "typed_event"
	infoTargetStateMessage        = "state_message"
	infoTargetContextStateMessage = "context_state_message"
	infoTargetRerunToolCalls      = "rerun_tool_calls"
	infoTargetContextToolCalls    = "context_tool_calls"
)

// The V1 suffixes on projection types below are stable Gob wire identities, not
// version limits. Projection V1 and V2 share these registered types; Version in
// the envelope and sentinel selects their semantics. Fields added for V2 remain
// optional on the wire because Gob decodes fields absent from V1 as zero values.
//
// runnerProjectionSentinelV1 CheckpointSchema: stable versioned Runner
// projection sentinel persisted via Gob. Keep existing fields compatible; add
// optional fields only.
type runnerProjectionSentinelV1 struct {
	Version int
}

// checkpointMessageSourceV1 CheckpointSchema: stable nested Runner projection
// source identifying a canonical message in V1 or V2.
type checkpointMessageSourceV1 struct {
	// SourceOrdinal is a V2-only compact coordinate. A legacy V1 payload omits
	// it, so Gob decodes zero and V1 uses GraphPath instead.
	SourceOrdinal int
	// AgentToolDepth is V2-only disambiguation metadata. A legacy V1 payload
	// omits it, so Gob decodes zero; zero is also the valid V2 root depth.
	AgentToolDepth int
	Kind           string
	GraphPath      []string
	Index          int
	MessageID      string
	Digest         string
}

// runCtxMessageProjectionV1 CheckpointSchema: stable nested Runner projection
// metadata used by V1 and V2. It stores Source, Inline, or an explicit nil.
// TargetLength applies to root-input slices; LaneDepth applies to lane events.
type runCtxMessageProjectionV1 struct {
	Target        string
	Index         int
	LaneDepth     int
	TargetLength  int
	Source        checkpointMessageSourceV1
	Inline        *schema.Message
	AgenticInline *schema.AgenticMessage
	IsNil         bool
	WasStreaming  bool
}

// infoMessageProjectionV1 CheckpointSchema: stable nested Runner projection
// metadata used by V1 and V2. It stores Source, an inline value, or an explicit
// nil. Target selects the applicable coordinate fields.
type infoMessageProjectionV1 struct {
	Target        string
	SubGraphPath  []string
	ContextIndex  int
	ParentDepth   int
	RerunExtraKey string
	MessageIndex  int
	TargetLength  int
	Source        checkpointMessageSourceV1
	Inline        *schema.Message
	AgenticInline *schema.AgenticMessage
	IsNil         bool
}

type infoProjectionTarget struct {
	kind         string
	path         []string
	contextIndex int
	parentDepth  int
	rerunKey     string
}

// checkpointProjectionV1 CheckpointSchema: stable Runner projection envelope
// persisted in serialization. Despite its legacy name, it carries V1 or V2 as
// selected by Version. RefCount fields detect truncation before hydration.
type checkpointProjectionV1 struct {
	Version            int
	SourceInterruptID  string
	RunCtxRefCount     int
	InfoRefCount       int
	ToolResultRefCount int
	// InterruptCtxRefCount is V2-only. It is absent from V1 payloads, which
	// decode it as zero and contain no V2 interrupt-context references.
	InterruptCtxRefCount int
	RunCtxRefs           []runCtxMessageProjectionV1
	InfoRefs             []infoMessageProjectionV1
	ToolResultRefs       []infoToolResultProjectionV1
}

// checkpointMessagePlaceholderV1 CheckpointSchema: stable persisted
// schema-message placeholder used by Runner projection V1 and V2.
type checkpointMessagePlaceholderV1 struct {
	Source checkpointMessageSourceV1
}

// checkpointMessageSliceEntryV1 CheckpointSchema: stable nested persisted entry
// in a schema-message slice placeholder used by projection V1 and V2.
type checkpointMessageSliceEntryV1 struct {
	Inline *schema.Message
	Source *checkpointMessageSourceV1
	IsNil  bool
}

// checkpointMessageSlicePlaceholderV1 CheckpointSchema: stable persisted
// schema-message slice placeholder used by Runner projection V1 and V2.
type checkpointMessageSlicePlaceholderV1 struct {
	Entries []checkpointMessageSliceEntryV1
}

// checkpointAgenticMessagePlaceholderV1 CheckpointSchema: stable persisted
// agentic-message placeholder used by Runner projection V1 and V2.
type checkpointAgenticMessagePlaceholderV1 struct {
	Source checkpointMessageSourceV1
}

// checkpointAgenticMessageSliceEntryV1 CheckpointSchema: stable nested
// persisted entry in an agentic-message slice placeholder used by projection
// V1 and V2.
type checkpointAgenticMessageSliceEntryV1 struct {
	Inline *schema.AgenticMessage
	Source *checkpointMessageSourceV1
	IsNil  bool
}

// checkpointAgenticMessageSlicePlaceholderV1 CheckpointSchema: stable
// persisted agentic-message slice placeholder used by Runner projection V1 and
// V2.
type checkpointAgenticMessageSlicePlaceholderV1 struct {
	Entries []checkpointAgenticMessageSliceEntryV1
}

// checkpointInterruptContextPlaceholderV1 is the stable V1/V2 wire type for a
// prefix already persisted authoritatively by a nested AgentTool runner
// checkpoint. The synthetic containing InterruptCtx stores the parent-specific
// tail.
type checkpointInterruptContextPlaceholderV1 struct {
	// SourceOrdinal is a V2-only compact coordinate. A legacy V1 payload omits
	// it, so Gob decodes zero and V1 uses RunnerPath instead.
	SourceOrdinal   int
	RunnerPath      []string
	SourceID        string
	Digest          string
	ContextIndex    int
	PrefixLength    int
	AddressPrefix   Address
	IntegrityDigest string
}

// checkpointInterruptInfoPlaceholderV1 CheckpointSchema: stable persisted
// interrupt-info placeholder used by Runner projection V1 and V2.
type checkpointInterruptInfoPlaceholderV1 struct {
	Info               *compose.InterruptInfo
	RefCount           int
	ToolResultRefCount int
	Refs               []infoMessageProjectionV1
	ToolResultRefs     []infoToolResultProjectionV1
}

type canonicalCheckpointMessage struct {
	source         checkpointMessageSourceV1
	message        *schema.Message
	agenticMessage *schema.AgenticMessage
}

type canonicalCheckpointInterruptInfo struct {
	sourceOrdinal int
	sourceID      string
	path          []string
	contexts      []*InterruptCtx
}

type checkpointProjectionIndex struct {
	byID                    map[string][]canonicalCheckpointMessage
	messagesByOrdinal       map[int]canonicalCheckpointMessage
	toolResultsByCallID     map[string][]canonicalCheckpointToolResult
	toolResultsByOrdinal    map[int]canonicalCheckpointToolResult
	interruptInfos          []canonicalCheckpointInterruptInfo
	interruptInfosByOrdinal map[int]canonicalCheckpointInterruptInfo
	imports                 []checkpointProjectionIndexImport
	nextSourceOrdinal       int
	version                 int
	traversal               *checkpointProjectionTraversal
}

type checkpointProjectionIndexImport struct {
	index         *checkpointProjectionIndex
	prefix        []string
	ordinalOffset int
}

type checkpointProjectionLookup struct {
	parent          *checkpointProjectionLookup
	prefix          []string
	ordinalOffset   int
	agentToolDepth  int
	graphPathLength int
}

type checkpointProjectionRunnerSnapshot struct {
	index *checkpointProjectionIndex
}

type checkpointProjectionTraversal struct {
	runners map[[sha256.Size]byte]*checkpointProjectionRunnerSnapshot
	loading map[[sha256.Size]byte]struct{}
}

type composeCheckpointLogicalValue struct {
	path     []string
	location compose.CheckpointValueLocation
	value    any
}

func init() {
	schema.RegisterName[*checkpointProjectionV1]("_eino_adk_checkpoint_projection_v1")
	schema.RegisterName[*runnerProjectionSentinelV1]("_eino_adk_runner_projection_v1")
	schema.RegisterName[*checkpointMessagePlaceholderV1]("_eino_adk_checkpoint_message_ref_v1")
	schema.RegisterName[*checkpointMessageSlicePlaceholderV1]("_eino_adk_checkpoint_message_slice_ref_v1")
	schema.RegisterName[*checkpointAgenticMessagePlaceholderV1]("_eino_adk_checkpoint_agentic_message_ref_v1")
	schema.RegisterName[*checkpointAgenticMessageSlicePlaceholderV1]("_eino_adk_checkpoint_agentic_message_slice_ref_v1")
	schema.RegisterName[*checkpointInterruptInfoPlaceholderV1]("_eino_adk_checkpoint_interrupt_info_ref_v1")
	schema.RegisterName[*checkpointInterruptContextPlaceholderV1]("_eino_adk_checkpoint_interrupt_context_ref_v1")
}

func projectRunnerCheckpoint(runCtx *runContext, info *InterruptInfo, infoDataStateID string,
	id2State map[string]core.InterruptState) (*runContext, *InterruptInfo,
	map[string]core.InterruptState, *checkpointProjectionV1, error) {
	sourceID, sourceData, index, err := findProjectionSource(infoDataStateID, id2State)
	if err != nil || index == nil {
		return runCtx, info, id2State, nil, err
	}

	projectedRunCtx := cloneRunContextForCheckpointProjection(runCtx)
	projectedInfo := cloneInterruptInfoForCheckpointProjection(info)
	projection := &checkpointProjectionV1{
		Version:           checkpointProjectionVersion,
		SourceInterruptID: sourceID,
	}
	projectRunContextMessages(projectedRunCtx, index, projection)
	projectInterruptContextPrefixes(projectedInfo, index)
	projectInterruptInfoMessages(projectedInfo, index, projection)
	if err = sealInterruptContextReferences(projectedInfo); err != nil {
		return nil, nil, nil, nil, err
	}
	projection.RunCtxRefCount = len(projection.RunCtxRefs)
	projection.InfoRefCount = len(projection.InfoRefs)
	projection.ToolResultRefCount = len(projection.ToolResultRefs)
	projection.InterruptCtxRefCount = countInterruptContextRefs(projectedInfo)

	projectedCompose, composeChanged, err := projectComposeCheckpointValues(sourceData, index)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(projection.RunCtxRefs) == 0 && len(projection.InfoRefs) == 0 &&
		len(projection.ToolResultRefs) == 0 && projection.InterruptCtxRefCount == 0 &&
		!composeChanged {
		return runCtx, info, id2State, nil, nil
	}

	projectedStates := cloneInterruptStateMap(id2State)
	sourceState := projectedStates[sourceID]
	sourceState.State = projectedCompose
	projectedStates[sourceID] = sourceState
	if err = validateRunnerProjectionReservedIDs(nil, projectedStates); err != nil {
		return nil, nil, nil, nil, err
	}
	projectedStates[runnerProjectionSentinelID] = core.InterruptState{
		State: &runnerProjectionSentinelV1{Version: checkpointProjectionVersion},
	}
	return projectedRunCtx, projectedInfo, projectedStates, projection, nil
}

func restoreRunnerCheckpointProjection(s *serialization) error {
	_, err := restoreRunnerCheckpointProjectionWithTraversal(
		s, newCheckpointProjectionTraversal())
	return err
}

func restoreRunnerCheckpointProjectionWithTraversal(s *serialization,
	traversal *checkpointProjectionTraversal) (*checkpointProjectionIndex, error) {
	if err := validateRunnerProjectionMetadata(s); err != nil {
		return nil, err
	}
	if s.ProjectionV1 == nil {
		return nil, nil
	}

	projection := s.ProjectionV1
	if projection.RunCtxRefCount != len(projection.RunCtxRefs) ||
		projection.InfoRefCount != len(projection.InfoRefs) ||
		projection.ToolResultRefCount != len(projection.ToolResultRefs) ||
		projection.InterruptCtxRefCount != countInterruptContextRefs(s.Info) {
		return nil, errors.New("failed to decode checkpoint projection: reference count mismatch")
	}
	sourceState, ok := s.InterruptID2State[projection.SourceInterruptID]
	if !ok {
		return nil, fmt.Errorf("failed to decode checkpoint projection: source interrupt state %q is missing",
			projection.SourceInterruptID)
	}
	sourceData, ok := sourceState.State.([]byte)
	if !ok {
		return nil, fmt.Errorf("failed to decode checkpoint projection: source interrupt state %q has invalid type %T",
			projection.SourceInterruptID, sourceState.State)
	}
	index, err := buildCheckpointProjectionIndexWithTraversal(
		sourceData, projection.Version, traversal)
	if err != nil {
		return nil, fmt.Errorf("failed to decode checkpoint projection source: %w", err)
	}
	if err = validateInterruptInfoContextReferences(s.Info, index); err != nil {
		return nil, err
	}
	restoredCompose, err := hydrateComposeCheckpointValues(sourceData, index)
	if err != nil {
		return nil, err
	}
	sourceState.State = restoredCompose
	s.InterruptID2State[projection.SourceInterruptID] = sourceState

	if err = hydrateRunContextMessages(s.RunCtx, projection.RunCtxRefs,
		projection.RunCtxRefCount, index); err != nil {
		return nil, err
	}
	if err = hydrateInterruptInfoMessages(s.Info, projection.InfoRefs,
		projection.InfoRefCount, index); err != nil {
		return nil, err
	}
	if err = hydrateInterruptInfoToolResults(s.Info, projection.ToolResultRefs,
		projection.ToolResultRefCount, index); err != nil {
		return nil, err
	}
	if err = hydrateInterruptInfoContextPrefixesAfterValidation(s.Info, index); err != nil {
		return nil, err
	}
	delete(s.InterruptID2State, runnerProjectionSentinelID)
	return index, nil
}

func validateRunnerProjectionMetadata(s *serialization) error {
	if s == nil {
		return nil
	}
	sentinelState, hasSentinel := s.InterruptID2State[runnerProjectionSentinelID]
	if _, exists := s.InterruptID2Address[runnerProjectionSentinelID]; exists {
		return errors.New("failed to decode checkpoint projection: sentinel must not have a routing address")
	}
	if hasSentinel && sentinelState.LayerSpecificPayload != nil {
		return errors.New("failed to decode checkpoint projection: sentinel must not have a layer-specific payload")
	}
	if s.ProjectionV1 == nil {
		if hasSentinel {
			return errors.New("failed to decode checkpoint projection: metadata is missing")
		}
		return nil
	}
	if s.ProjectionV1.Version != checkpointProjectionVersionV1 &&
		s.ProjectionV1.Version != checkpointProjectionVersionV2 {
		return fmt.Errorf("checkpoint requires a newer Eino version: unsupported projection version %d",
			s.ProjectionV1.Version)
	}
	if !hasSentinel {
		return errors.New("failed to decode checkpoint projection: sentinel is missing")
	}
	sentinel, ok := sentinelState.State.(*runnerProjectionSentinelV1)
	if !ok || sentinel == nil || sentinel.Version != s.ProjectionV1.Version {
		return fmt.Errorf("failed to decode checkpoint projection: invalid sentinel %T", sentinelState.State)
	}
	return nil
}

func validateRunnerProjectionReservedIDs(id2Address map[string]Address,
	id2State map[string]core.InterruptState) error {
	for _, id := range sortedStringKeys(id2Address) {
		if isRunnerProjectionMetadataID(id) {
			return fmt.Errorf("interrupt ID %q is reserved for checkpoint metadata", id)
		}
	}
	for _, id := range sortedStringKeys(id2State) {
		if isRunnerProjectionMetadataID(id) {
			return fmt.Errorf("interrupt ID %q is reserved for checkpoint metadata", id)
		}
	}
	return nil
}

func isRunnerProjectionMetadataID(id string) bool {
	return id == runnerProjectionSentinelID
}

func findProjectionSource(preferredID string, id2State map[string]core.InterruptState) (
	string, []byte, *checkpointProjectionIndex, error) {
	otherIDs := make([]string, 0, len(id2State))
	for id := range id2State {
		if id != preferredID && !isRunnerProjectionMetadataID(id) {
			otherIDs = append(otherIDs, id)
		}
	}
	sort.Strings(otherIDs)
	ids := otherIDs
	if preferredID != "" {
		ids = append([]string{preferredID}, otherIDs...)
	}
	for _, id := range ids {
		data, ok := id2State[id].State.([]byte)
		if !ok {
			continue
		}
		index, err := buildCheckpointProjectionIndex(data)
		if err != nil {
			continue
		}
		if index.hasMessagesOrToolResults() {
			return id, data, index, nil
		}
	}
	return "", nil, nil, nil
}

func (i *checkpointProjectionIndex) hasMessagesOrToolResults() bool {
	if len(i.byID) > 0 || len(i.toolResultsByCallID) > 0 {
		return true
	}
	for _, entry := range i.imports {
		if entry.index.hasMessagesOrToolResults() {
			return true
		}
	}
	return false
}

func buildCheckpointProjectionIndex(data []byte) (*checkpointProjectionIndex, error) {
	return buildCheckpointProjectionIndexForVersion(data, checkpointProjectionVersion)
}

func buildCheckpointProjectionIndexForVersion(data []byte,
	version int) (*checkpointProjectionIndex, error) {
	return buildCheckpointProjectionIndexWithTraversal(
		data, version, newCheckpointProjectionTraversal())
}

func newCheckpointProjectionTraversal() *checkpointProjectionTraversal {
	return &checkpointProjectionTraversal{
		runners: make(map[[sha256.Size]byte]*checkpointProjectionRunnerSnapshot),
		loading: make(map[[sha256.Size]byte]struct{}),
	}
}

func buildCheckpointProjectionIndexWithTraversal(data []byte, version int,
	traversal *checkpointProjectionTraversal) (*checkpointProjectionIndex, error) {
	if traversal == nil {
		traversal = newCheckpointProjectionTraversal()
	}
	index := &checkpointProjectionIndex{
		byID:                    make(map[string][]canonicalCheckpointMessage),
		messagesByOrdinal:       make(map[int]canonicalCheckpointMessage),
		toolResultsByCallID:     make(map[string][]canonicalCheckpointToolResult),
		toolResultsByOrdinal:    make(map[int]canonicalCheckpointToolResult),
		interruptInfosByOrdinal: make(map[int]canonicalCheckpointInterruptInfo),
		version:                 version,
		traversal:               traversal,
	}
	if err := index.addComposeCheckpoint(data, nil); err != nil {
		return nil, err
	}
	return index, nil
}

func (i *checkpointProjectionIndex) nextOrdinal() int {
	i.nextSourceOrdinal++
	return i.nextSourceOrdinal
}

func checkpointAgentToolStateData(value any) ([]byte, bool) {
	switch state := value.(type) {
	case *agentToolInterruptStateV1:
		if state != nil && state.Version == agentToolInterruptStateVersionV1 {
			return state.BridgeCheckpoint, true
		}
	case *agentToolInterruptStateV2:
		if state != nil && state.Version == agentToolInterruptStateVersionV2 {
			return state.BridgeCheckpoint, true
		}
	}
	return nil, false
}

func compactRunnerCheckpointNestedInterrupts(sourceID string, id2Address map[string]Address,
	id2State map[string]core.InterruptState) {
	if sourceID == "" {
		return
	}
	source, ok := id2State[sourceID].State.([]byte)
	if !ok {
		return
	}

	nestedIDs := make(map[string]struct{})
	if err := collectComposeCheckpointInterruptIDs(source, nestedIDs); err != nil {
		return
	}
	for id := range nestedIDs {
		if id == sourceID {
			continue
		}
		delete(id2Address, id)
		delete(id2State, id)
	}
}

func collectComposeCheckpointInterruptIDs(data []byte, ids map[string]struct{}) error {
	return compose.WalkCheckpointValues(data, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			if location.Kind != compose.CheckpointValueInterruptState {
				return nil
			}
			ids[location.Key] = struct{}{}
			bridgeCheckpoint, ok := checkpointAgentToolStateData(value)
			if !ok {
				return nil
			}
			var child serialization
			if err := gob.NewDecoder(bytes.NewReader(bridgeCheckpoint)).Decode(&child); err != nil {
				return fmt.Errorf("failed to decode nested AgentTool checkpoint: %w", err)
			}
			for id := range child.InterruptID2State {
				if !isRunnerProjectionMetadataID(id) {
					ids[id] = struct{}{}
				}
			}
			for id := range child.InterruptID2Address {
				if !isRunnerProjectionMetadataID(id) {
					ids[id] = struct{}{}
				}
			}
			childSourceID := child.InfoDataSourceInterruptID
			if child.ProjectionV1 != nil {
				childSourceID = child.ProjectionV1.SourceInterruptID
			}
			if childData, childOK := child.InterruptID2State[childSourceID].State.([]byte); childOK {
				if err := collectComposeCheckpointInterruptIDs(childData, ids); err != nil {
					return err
				}
			}
			return nil
		})
}

func (i *checkpointProjectionIndex) addComposeCheckpoint(data []byte, prefix []string) error {
	return compose.WalkCheckpointValues(data, &gobSerializer{},
		func(path compose.NodePath, location compose.CheckpointValueLocation, value any) error {
			fullPath := append(append([]string(nil), prefix...), path.GetPath()...)
			if location.Kind == compose.CheckpointValueState {
				switch state := value.(type) {
				case *State:
					for index, message := range state.Messages {
						i.addSchemaMessage(fullPath, index, message)
					}
				case *agenticState:
					for index, message := range state.Messages {
						i.addAgenticMessage(fullPath, index, message)
					}
				}
				return nil
			}
			if location.Kind == compose.CheckpointValueInterruptState {
				i.addCheckpointToolResults(fullPath, location.Key, value)
				if bridgeCheckpoint, ok := checkpointAgentToolStateData(value); ok {
					childPrefix := append(append([]string(nil), fullPath...), "@interrupt:"+location.Key)
					if err := i.addRunnerCheckpoint(bridgeCheckpoint, childPrefix); err != nil {
						return err
					}
				}
			}
			return nil
		})
}

func (i *checkpointProjectionIndex) addRunnerCheckpoint(data []byte, prefix []string) error {
	snapshot, err := i.traversal.runnerCheckpoint(data)
	if err != nil {
		return err
	}
	i.importIndex(snapshot.index, prefix)
	return nil
}

func (t *checkpointProjectionTraversal) runnerCheckpoint(
	data []byte) (*checkpointProjectionRunnerSnapshot, error) {
	key := sha256.Sum256(data)
	if snapshot, ok := t.runners[key]; ok {
		return snapshot, nil
	}
	if _, ok := t.loading[key]; ok {
		return nil, errors.New("nested AgentTool checkpoint cycle detected")
	}
	t.loading[key] = struct{}{}
	defer delete(t.loading, key)

	var runnerCheckpoint serialization
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&runnerCheckpoint); err != nil {
		return nil, fmt.Errorf("failed to decode child runner checkpoint: %w", err)
	}
	restoredSourceIndex, err := restoreRunnerCheckpointProjectionWithTraversal(&runnerCheckpoint, t)
	if err != nil {
		return nil, fmt.Errorf("failed to restore child runner checkpoint projection: %w", err)
	}
	index := newCheckpointProjectionIndex(checkpointProjectionVersion, t)
	if runnerCheckpoint.Info != nil {
		if len(runnerCheckpoint.Info.InterruptContexts) > 0 {
			index.addInterruptInfo(canonicalCheckpointInterruptInfo{
				sourceID: "runner-info",
				contexts: runnerCheckpoint.Info.InterruptContexts,
			})
		}
		if chatModelInfo, ok := runnerCheckpoint.Info.Data.(*ChatModelAgentInterruptInfo); ok && chatModelInfo != nil && chatModelInfo.Info != nil {
			sourceID := runnerCheckpoint.InfoDataSourceInterruptID
			if runnerCheckpoint.ProjectionV1 != nil {
				sourceID = runnerCheckpoint.ProjectionV1.SourceInterruptID
			}
			if address, exists := runnerCheckpoint.InterruptID2Address[sourceID]; exists {
				signal := &core.InterruptSignal{
					ID:      sourceID,
					Address: address,
					InterruptInfo: core.InterruptInfo{
						Info: chatModelInfo.Info,
					},
				}
				if child := FromInterruptContexts(chatModelInfo.Info.InterruptContexts); child != nil {
					signal.Subs = []*core.InterruptSignal{child}
				}
				index.addInterruptInfo(canonicalCheckpointInterruptInfo{
					sourceID: "signal:" + sourceID,
					contexts: core.ToInterruptContexts(signal, allowedAddressSegmentTypes),
				})
			}
			index.addInterruptInfo(canonicalCheckpointInterruptInfo{
				sourceID: "compose-info",
				path:     []string{"@compose-info"},
				contexts: chatModelInfo.Info.InterruptContexts,
			})
		}
	}
	ids := make([]string, 0, len(runnerCheckpoint.InterruptID2State))
	for id := range runnerCheckpoint.InterruptID2State {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		composeData, ok := runnerCheckpoint.InterruptID2State[id].State.([]byte)
		if !ok {
			continue
		}
		prefix := []string{"@runner:" + id}
		if runnerCheckpoint.ProjectionV1 != nil &&
			id == runnerCheckpoint.ProjectionV1.SourceInterruptID &&
			restoredSourceIndex != nil {
			index.importIndex(restoredSourceIndex, prefix)
			continue
		}
		stateIndex, err := buildCheckpointProjectionIndexWithTraversal(
			composeData, checkpointProjectionVersion, t)
		if err != nil {
			return nil, err
		}
		index.importIndex(stateIndex, prefix)
	}
	snapshot := &checkpointProjectionRunnerSnapshot{
		index: index,
	}
	t.runners[key] = snapshot
	return snapshot, nil
}

func newCheckpointProjectionIndex(version int,
	traversal *checkpointProjectionTraversal) *checkpointProjectionIndex {
	return &checkpointProjectionIndex{
		byID:                    make(map[string][]canonicalCheckpointMessage),
		messagesByOrdinal:       make(map[int]canonicalCheckpointMessage),
		toolResultsByCallID:     make(map[string][]canonicalCheckpointToolResult),
		toolResultsByOrdinal:    make(map[int]canonicalCheckpointToolResult),
		interruptInfosByOrdinal: make(map[int]canonicalCheckpointInterruptInfo),
		version:                 version,
		traversal:               traversal,
	}
}

func (i *checkpointProjectionIndex) importIndex(source *checkpointProjectionIndex,
	prefix []string) {
	if source == nil || source.nextSourceOrdinal == 0 {
		return
	}
	i.imports = append(i.imports, checkpointProjectionIndexImport{
		index:         source,
		prefix:        cloneSlice(prefix),
		ordinalOffset: i.nextSourceOrdinal,
	})
	i.nextSourceOrdinal += source.nextSourceOrdinal
}

func (l *checkpointProjectionLookup) importEntry(
	entry checkpointProjectionIndexImport) checkpointProjectionLookup {
	lookup := checkpointProjectionLookup{
		parent:          l,
		prefix:          entry.prefix,
		ordinalOffset:   entry.ordinalOffset,
		agentToolDepth:  checkpointProjectionAgentToolDepth(entry.prefix),
		graphPathLength: len(entry.prefix),
	}
	if l != nil {
		lookup.ordinalOffset += l.ordinalOffset
		lookup.agentToolDepth += l.agentToolDepth
		lookup.graphPathLength += l.graphPathLength
	}
	return lookup
}

func (l *checkpointProjectionLookup) graphPath(local []string) []string {
	if l == nil {
		return local
	}
	path := make([]string, l.graphPathLength+len(local))
	offset := len(path) - len(local)
	copy(path[offset:], local)
	for current := l; current != nil; current = current.parent {
		offset -= len(current.prefix)
		copy(path[offset:], current.prefix)
	}
	return path
}

func importCheckpointMessage(candidate canonicalCheckpointMessage,
	lookup *checkpointProjectionLookup) canonicalCheckpointMessage {
	if lookup == nil {
		return candidate
	}
	candidate.source.SourceOrdinal += lookup.ordinalOffset
	candidate.source.AgentToolDepth += lookup.agentToolDepth
	candidate.source.GraphPath = lookup.graphPath(candidate.source.GraphPath)
	return candidate
}

func (i *checkpointProjectionIndex) messageCandidates(
	id string) []canonicalCheckpointMessage {
	return i.messageCandidatesAt(id, nil)
}

func (i *checkpointProjectionIndex) messageCandidatesAt(
	id string, lookup *checkpointProjectionLookup) []canonicalCheckpointMessage {
	local := i.byID[id]
	candidates := make([]canonicalCheckpointMessage, 0, len(local))
	for _, candidate := range local {
		candidates = append(candidates, importCheckpointMessage(candidate, lookup))
	}
	for _, entry := range i.imports {
		childLookup := lookup.importEntry(entry)
		candidates = append(candidates,
			entry.index.messageCandidatesAt(id, &childLookup)...)
	}
	return candidates
}

func (i *checkpointProjectionIndex) allMessages() []canonicalCheckpointMessage {
	return i.allMessagesAt(nil)
}

func (i *checkpointProjectionIndex) allMessagesAt(
	lookup *checkpointProjectionLookup) []canonicalCheckpointMessage {
	var messages []canonicalCheckpointMessage
	for _, candidates := range i.byID {
		for _, candidate := range candidates {
			messages = append(messages, importCheckpointMessage(candidate, lookup))
		}
	}
	for _, entry := range i.imports {
		childLookup := lookup.importEntry(entry)
		messages = append(messages, entry.index.allMessagesAt(&childLookup)...)
	}
	return messages
}

func (i *checkpointProjectionIndex) messageByOrdinal(
	ordinal int) (canonicalCheckpointMessage, bool) {
	return i.messageByOrdinalAt(ordinal, nil)
}

func (i *checkpointProjectionIndex) messageByOrdinalAt(
	ordinal int, lookup *checkpointProjectionLookup) (canonicalCheckpointMessage, bool) {
	if candidate, ok := i.messagesByOrdinal[ordinal]; ok {
		return importCheckpointMessage(candidate, lookup), true
	}
	for _, entry := range i.imports {
		sourceOrdinal := ordinal - entry.ordinalOffset
		if sourceOrdinal <= 0 || sourceOrdinal > entry.index.nextSourceOrdinal {
			continue
		}
		childLookup := lookup.importEntry(entry)
		candidate, ok := entry.index.messageByOrdinalAt(sourceOrdinal, &childLookup)
		if !ok {
			return canonicalCheckpointMessage{}, false
		}
		return candidate, true
	}
	return canonicalCheckpointMessage{}, false
}

func importCheckpointToolResult(candidate canonicalCheckpointToolResult,
	lookup *checkpointProjectionLookup) canonicalCheckpointToolResult {
	if lookup == nil {
		return candidate
	}
	candidate.source.SourceOrdinal += lookup.ordinalOffset
	candidate.source.GraphPath = lookup.graphPath(candidate.source.GraphPath)
	return candidate
}

func (i *checkpointProjectionIndex) toolResultCandidates(
	callID string) []canonicalCheckpointToolResult {
	return i.toolResultCandidatesAt(callID, nil)
}

func (i *checkpointProjectionIndex) toolResultCandidatesAt(
	callID string, lookup *checkpointProjectionLookup) []canonicalCheckpointToolResult {
	local := i.toolResultsByCallID[callID]
	candidates := make([]canonicalCheckpointToolResult, 0, len(local))
	for _, candidate := range local {
		candidates = append(candidates, importCheckpointToolResult(candidate, lookup))
	}
	for _, entry := range i.imports {
		childLookup := lookup.importEntry(entry)
		candidates = append(candidates,
			entry.index.toolResultCandidatesAt(callID, &childLookup)...)
	}
	return candidates
}

func (i *checkpointProjectionIndex) toolResultByOrdinal(
	ordinal int) (canonicalCheckpointToolResult, bool) {
	return i.toolResultByOrdinalAt(ordinal, nil)
}

func (i *checkpointProjectionIndex) toolResultByOrdinalAt(
	ordinal int, lookup *checkpointProjectionLookup) (canonicalCheckpointToolResult, bool) {
	if candidate, ok := i.toolResultsByOrdinal[ordinal]; ok {
		return importCheckpointToolResult(candidate, lookup), true
	}
	for _, entry := range i.imports {
		sourceOrdinal := ordinal - entry.ordinalOffset
		if sourceOrdinal <= 0 || sourceOrdinal > entry.index.nextSourceOrdinal {
			continue
		}
		childLookup := lookup.importEntry(entry)
		candidate, ok := entry.index.toolResultByOrdinalAt(sourceOrdinal, &childLookup)
		if !ok {
			return canonicalCheckpointToolResult{}, false
		}
		return candidate, true
	}
	return canonicalCheckpointToolResult{}, false
}

func importCheckpointInterruptInfo(candidate canonicalCheckpointInterruptInfo,
	lookup *checkpointProjectionLookup) canonicalCheckpointInterruptInfo {
	if lookup == nil {
		return candidate
	}
	candidate.sourceOrdinal += lookup.ordinalOffset
	candidate.path = lookup.graphPath(candidate.path)
	return candidate
}

func (i *checkpointProjectionIndex) allInterruptInfos() []canonicalCheckpointInterruptInfo {
	return i.allInterruptInfosAt(nil)
}

func (i *checkpointProjectionIndex) allInterruptInfosAt(
	lookup *checkpointProjectionLookup) []canonicalCheckpointInterruptInfo {
	infos := make([]canonicalCheckpointInterruptInfo, 0, len(i.interruptInfos))
	for _, candidate := range i.interruptInfos {
		infos = append(infos, importCheckpointInterruptInfo(candidate, lookup))
	}
	for _, entry := range i.imports {
		childLookup := lookup.importEntry(entry)
		infos = append(infos, entry.index.allInterruptInfosAt(&childLookup)...)
	}
	return infos
}

func (i *checkpointProjectionIndex) interruptInfoByOrdinal(
	ordinal int) (canonicalCheckpointInterruptInfo, bool) {
	return i.interruptInfoByOrdinalAt(ordinal, nil)
}

func (i *checkpointProjectionIndex) interruptInfoByOrdinalAt(
	ordinal int, lookup *checkpointProjectionLookup) (canonicalCheckpointInterruptInfo, bool) {
	if candidate, ok := i.interruptInfosByOrdinal[ordinal]; ok {
		return importCheckpointInterruptInfo(candidate, lookup), true
	}
	for _, entry := range i.imports {
		sourceOrdinal := ordinal - entry.ordinalOffset
		if sourceOrdinal <= 0 || sourceOrdinal > entry.index.nextSourceOrdinal {
			continue
		}
		childLookup := lookup.importEntry(entry)
		candidate, ok := entry.index.interruptInfoByOrdinalAt(sourceOrdinal, &childLookup)
		if !ok {
			return canonicalCheckpointInterruptInfo{}, false
		}
		return candidate, true
	}
	return canonicalCheckpointInterruptInfo{}, false
}

func (i *checkpointProjectionIndex) addInterruptInfo(info canonicalCheckpointInterruptInfo) {
	info.sourceOrdinal = i.nextOrdinal()
	i.interruptInfos = append(i.interruptInfos, info)
	if i.interruptInfosByOrdinal == nil {
		i.interruptInfosByOrdinal = make(map[int]canonicalCheckpointInterruptInfo)
	}
	i.interruptInfosByOrdinal[info.sourceOrdinal] = info
}

func (i *checkpointProjectionIndex) addSchemaMessage(path []string, index int, message *schema.Message) {
	if message == nil {
		return
	}
	id := GetMessageID(message)
	digest, ok := checkpointProjectionValueDigest(message)
	if id == "" || !ok {
		return
	}
	canonical := canonicalCheckpointMessage{
		source: checkpointMessageSourceV1{
			SourceOrdinal:  i.nextOrdinal(),
			AgentToolDepth: checkpointProjectionAgentToolDepth(path),
			Kind:           projectionMessageKindSchema,
			GraphPath:      append([]string(nil), path...),
			Index:          index,
			MessageID:      id,
			Digest:         digest,
		},
		message: message,
	}
	i.byID[id] = append(i.byID[id], canonical)
	if i.messagesByOrdinal == nil {
		i.messagesByOrdinal = make(map[int]canonicalCheckpointMessage)
	}
	i.messagesByOrdinal[canonical.source.SourceOrdinal] = canonical
}

func (i *checkpointProjectionIndex) addAgenticMessage(path []string, index int,
	message *schema.AgenticMessage) {
	if message == nil {
		return
	}
	id := GetMessageID(message)
	digest, ok := checkpointProjectionValueDigest(message)
	if id == "" || !ok {
		return
	}
	canonical := canonicalCheckpointMessage{
		source: checkpointMessageSourceV1{
			SourceOrdinal:  i.nextOrdinal(),
			AgentToolDepth: checkpointProjectionAgentToolDepth(path),
			Kind:           projectionMessageKindAgentic,
			GraphPath:      append([]string(nil), path...),
			Index:          index,
			MessageID:      id,
			Digest:         digest,
		},
		agenticMessage: message,
	}
	i.byID[id] = append(i.byID[id], canonical)
	if i.messagesByOrdinal == nil {
		i.messagesByOrdinal = make(map[int]canonicalCheckpointMessage)
	}
	i.messagesByOrdinal[canonical.source.SourceOrdinal] = canonical
}

func checkpointProjectionAgentToolDepth(path []string) int {
	depth := 0
	for _, segment := range path {
		if len(segment) >= len("@interrupt:") && segment[:len("@interrupt:")] == "@interrupt:" {
			depth++
		}
	}
	return depth
}

func checkpointProjectionValueDigest(value any) (string, bool) {
	_, digest, ok := checkpointinternal.SemanticDigest(value)
	return digest, ok
}

func checkpointProjectionPathEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (i *checkpointProjectionIndex) sourceForSchemaMessage(
	message *schema.Message) (checkpointMessageSourceV1, bool) {
	if message == nil {
		return checkpointMessageSourceV1{}, false
	}
	id := GetMessageID(message)
	candidates := i.messageCandidates(id)
	if id == "" || len(candidates) == 0 {
		return checkpointMessageSourceV1{}, false
	}
	digest, ok := checkpointProjectionValueDigest(message)
	if !ok {
		return checkpointMessageSourceV1{}, false
	}
	// A duplicate ID is usable only when every candidate is the same logical
	// message. Otherwise keeping the value inline avoids an ambiguous reference.
	for _, candidate := range candidates {
		if candidate.source.Kind != projectionMessageKindSchema ||
			candidate.source.Digest != digest ||
			!gobSemanticEqual(candidate.message, message) {
			return checkpointMessageSourceV1{}, false
		}
	}
	return compactCheckpointMessageSource(candidates[0].source), true
}

func (i *checkpointProjectionIndex) schemaMessage(
	source checkpointMessageSourceV1) (*schema.Message, error) {
	if err := validateCheckpointMessageSource(
		source, projectionMessageKindSchema, i.version); err != nil {
		return nil, err
	}
	if source.SourceOrdinal > 0 {
		candidate, ok := i.messageByOrdinal(source.SourceOrdinal)
		if ok && checkpointMessageSourceMatches(candidate.source, source,
			projectionMessageKindSchema) {
			return cloneSchemaMessageForProjection(candidate.message)
		}
		return nil, fmt.Errorf("checkpoint projection source message %q does not match metadata",
			source.MessageID)
	}
	candidates := i.messageCandidates(source.MessageID)
	for _, candidate := range candidates {
		if source.Kind == projectionMessageKindSchema &&
			candidate.source.Kind == source.Kind &&
			candidate.source.Index == source.Index &&
			checkpointProjectionPathEqual(candidate.source.GraphPath, source.GraphPath) &&
			candidate.source.Digest == source.Digest {
			return cloneSchemaMessageForProjection(candidate.message)
		}
	}
	return nil, fmt.Errorf("checkpoint projection source message %q does not match metadata", source.MessageID)
}

func (i *checkpointProjectionIndex) sourceForAgenticMessage(
	message *schema.AgenticMessage) (checkpointMessageSourceV1, bool) {
	if message == nil {
		return checkpointMessageSourceV1{}, false
	}
	id := GetMessageID(message)
	candidates := i.messageCandidates(id)
	if id == "" || len(candidates) == 0 {
		return checkpointMessageSourceV1{}, false
	}
	digest, ok := checkpointProjectionValueDigest(message)
	if !ok {
		return checkpointMessageSourceV1{}, false
	}
	for _, candidate := range candidates {
		if candidate.source.Kind != projectionMessageKindAgentic ||
			candidate.source.Digest != digest ||
			!gobSemanticEqual(candidate.agenticMessage, message) {
			return checkpointMessageSourceV1{}, false
		}
	}
	return compactCheckpointMessageSource(candidates[0].source), true
}

func (i *checkpointProjectionIndex) agenticMessage(
	source checkpointMessageSourceV1) (*schema.AgenticMessage, error) {
	if err := validateCheckpointMessageSource(
		source, projectionMessageKindAgentic, i.version); err != nil {
		return nil, err
	}
	if source.SourceOrdinal > 0 {
		candidate, ok := i.messageByOrdinal(source.SourceOrdinal)
		if ok && checkpointMessageSourceMatches(candidate.source, source,
			projectionMessageKindAgentic) {
			return cloneAgenticMessageForProjection(candidate.agenticMessage)
		}
		return nil, fmt.Errorf("checkpoint projection source agentic message %q does not match metadata",
			source.MessageID)
	}
	candidates := i.messageCandidates(source.MessageID)
	for _, candidate := range candidates {
		if source.Kind == projectionMessageKindAgentic &&
			candidate.source.Kind == source.Kind &&
			candidate.source.Index == source.Index &&
			checkpointProjectionPathEqual(candidate.source.GraphPath, source.GraphPath) &&
			candidate.source.Digest == source.Digest {
			return cloneAgenticMessageForProjection(candidate.agenticMessage)
		}
	}
	return nil, fmt.Errorf("checkpoint projection source agentic message %q does not match metadata",
		source.MessageID)
}

func validateCheckpointMessageSource(source checkpointMessageSourceV1, kind string,
	version int) error {
	label := "message"
	if kind == projectionMessageKindAgentic {
		label = "agentic message"
	}
	if source.Kind == "" || source.MessageID == "" || source.Digest == "" ||
		source.Index < 0 || source.AgentToolDepth < 0 || source.SourceOrdinal < 0 {
		return fmt.Errorf("checkpoint projection %s source metadata is incomplete", label)
	}
	if version == checkpointProjectionVersionV1 {
		if source.SourceOrdinal != 0 || source.AgentToolDepth != 0 {
			return fmt.Errorf("checkpoint projection V1 %s source contains V2 metadata", label)
		}
		return nil
	}
	if source.SourceOrdinal == 0 {
		return fmt.Errorf("checkpoint projection V2 %s source metadata is incomplete", label)
	}
	if len(source.GraphPath) != 0 {
		return fmt.Errorf("checkpoint projection V2 %s source contains V1 metadata", label)
	}
	return nil
}

func compactCheckpointMessageSource(source checkpointMessageSourceV1) checkpointMessageSourceV1 {
	if source.SourceOrdinal > 0 {
		source.GraphPath = nil
	}
	return source
}

func checkpointMessageSourceMatches(candidate, source checkpointMessageSourceV1,
	kind string) bool {
	return source.Kind == kind &&
		candidate.SourceOrdinal == source.SourceOrdinal &&
		candidate.AgentToolDepth == source.AgentToolDepth &&
		candidate.Kind == source.Kind &&
		candidate.Index == source.Index &&
		candidate.MessageID == source.MessageID &&
		candidate.Digest == source.Digest
}

func cloneInterruptStateMap(source map[string]core.InterruptState) map[string]core.InterruptState {
	return cloneProjectionMap(source)
}

func cloneProjectionMap[K comparable, V any](source map[K]V) map[K]V {
	if source == nil {
		return nil
	}
	cloned := make(map[K]V, len(source))
	for key, value := range source {
		cloned[key] = value
	}
	return cloned
}

func cloneRunContextForCheckpointProjection(runCtx *runContext) *runContext {
	if runCtx == nil {
		return nil
	}
	cloned := &runContext{
		RunPath: cloneSlice(runCtx.RunPath),
		Session: cloneRunSessionForCheckpointProjection(runCtx.Session),
	}
	if runCtx.RootInput != nil {
		rootInput := *runCtx.RootInput
		rootInput.Messages = cloneSlice(runCtx.RootInput.Messages)
		cloned.RootInput = &rootInput
	}
	if input, ok := runCtx.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage]); ok && input != nil {
		rootInput := *input
		rootInput.Messages = cloneSlice(input.Messages)
		cloned.AgenticRootInput = &rootInput
	} else {
		cloned.AgenticRootInput = runCtx.AgenticRootInput
	}
	return cloned
}

func cloneRunSessionForCheckpointProjection(session *runSession) *runSession {
	if session == nil {
		return nil
	}
	cloned := &runSession{
		valuesMtx: &sync.Mutex{},
	}
	if session.valuesMtx != nil {
		session.valuesMtx.Lock()
		cloned.Values = cloneProjectionMap(session.Values)
		session.valuesMtx.Unlock()
	} else {
		cloned.Values = cloneProjectionMap(session.Values)
	}

	session.mtx.Lock()
	events := cloneSlice(session.Events)
	typedEvents := session.TypedEvents
	session.mtx.Unlock()
	if events != nil {
		cloned.Events = make([]*agentEventWrapper, len(events))
		for i, event := range events {
			cloned.Events[i] = cloneAgentEventWrapperForProjection(event)
		}
	}
	cloned.LaneEvents = cloneLaneEventsForProjection(session.LaneEvents)
	if typed, ok := typedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage]); ok && typed != nil {
		var copied []*typedAgentEventWrapper[*schema.AgenticMessage]
		if *typed != nil {
			copied = make([]*typedAgentEventWrapper[*schema.AgenticMessage], len(*typed))
			for i, event := range *typed {
				copied[i] = cloneTypedAgentEventWrapperForProjection(event)
			}
		}
		cloned.TypedEvents = &copied
	} else {
		cloned.TypedEvents = typedEvents
	}
	return cloned
}

func cloneAgentEventWrapperForProjection(event *agentEventWrapper) *agentEventWrapper {
	if event == nil || event.AgentEvent == nil {
		return nil
	}
	return &agentEventWrapper{
		AgentEvent:          copyTypedAgentEvent(event.AgentEvent),
		concatenatedMessage: event.concatenatedMessage,
		TS:                  event.TS,
		StreamErr:           event.StreamErr,
	}
}

func cloneTypedAgentEventWrapperForProjection(
	event *typedAgentEventWrapper[*schema.AgenticMessage],
) *typedAgentEventWrapper[*schema.AgenticMessage] {
	if event == nil || event.event == nil {
		return nil
	}
	return &typedAgentEventWrapper[*schema.AgenticMessage]{
		event:               copyTypedAgentEvent(event.event),
		concatenatedMessage: event.concatenatedMessage,
		TS:                  event.TS,
		StreamErr:           event.StreamErr,
	}
}

func cloneLaneEventsForProjection(lane *laneEvents) *laneEvents {
	if lane == nil {
		return nil
	}
	cloned := &laneEvents{Parent: cloneLaneEventsForProjection(lane.Parent)}
	if lane.Events != nil {
		cloned.Events = make([]*agentEventWrapper, len(lane.Events))
		for i, event := range lane.Events {
			cloned.Events[i] = cloneAgentEventWrapperForProjection(event)
		}
	}
	return cloned
}

func cloneInterruptInfoForCheckpointProjection(info *InterruptInfo) *InterruptInfo {
	if info == nil {
		return nil
	}
	cloned := *info
	if info.InterruptContexts != nil {
		cloned.InterruptContexts = make([]*InterruptCtx, len(info.InterruptContexts))
		for i, interruptCtx := range info.InterruptContexts {
			cloned.InterruptContexts[i] = cloneInterruptContextForProjection(interruptCtx)
		}
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil {
		return &cloned
	}
	clonedChatModelInfo := *chatModelInfo
	clonedChatModelInfo.Data = cloneSlice(chatModelInfo.Data)
	clonedChatModelInfo.Info = cloneComposeInterruptInfoForProjection(chatModelInfo.Info)
	cloned.Data = &clonedChatModelInfo
	return &cloned
}

func cloneComposeInterruptInfoForProjection(info *compose.InterruptInfo) *compose.InterruptInfo {
	if info == nil {
		return nil
	}
	cloned := *info
	cloned.BeforeNodes = cloneSlice(info.BeforeNodes)
	cloned.AfterNodes = cloneSlice(info.AfterNodes)
	cloned.RerunNodes = cloneSlice(info.RerunNodes)
	cloned.State = cloneProjectionInfoValue(info.State)
	if info.RerunNodesExtra != nil {
		cloned.RerunNodesExtra = make(map[string]any, len(info.RerunNodesExtra))
		for key, value := range info.RerunNodesExtra {
			cloned.RerunNodesExtra[key] = cloneProjectionInfoValue(value)
		}
	}
	if info.SubGraphs != nil {
		cloned.SubGraphs = make(map[string]*compose.InterruptInfo, len(info.SubGraphs))
		for key, sub := range info.SubGraphs {
			cloned.SubGraphs[key] = cloneComposeInterruptInfoForProjection(sub)
		}
	}
	if info.InterruptContexts != nil {
		cloned.InterruptContexts = make([]*InterruptCtx, len(info.InterruptContexts))
		for i, interruptCtx := range info.InterruptContexts {
			cloned.InterruptContexts[i] = cloneInterruptContextForProjection(interruptCtx)
		}
	}
	return &cloned
}

func cloneInterruptContextForProjection(interruptCtx *InterruptCtx) *InterruptCtx {
	if interruptCtx == nil {
		return nil
	}
	cloned := *interruptCtx
	cloned.Address = cloneSlice(interruptCtx.Address)
	cloned.Info = cloneProjectionInfoValue(interruptCtx.Info)
	cloned.Parent = cloneInterruptContextForProjection(interruptCtx.Parent)
	return &cloned
}

func cloneProjectionInfoValue(value any) any {
	switch value := value.(type) {
	case *State:
		if value == nil {
			return value
		}
		cloned := *value
		cloned.Messages = cloneSlice(value.Messages)
		return &cloned
	case *agenticState:
		if value == nil {
			return value
		}
		cloned := *value
		cloned.Messages = cloneSlice(value.Messages)
		return &cloned
	case *compose.ToolsInterruptAndRerunExtra:
		if value == nil {
			return value
		}
		cloned := *value
		cloned.ToolCalls = cloneSlice(value.ToolCalls)
		cloned.ExecutedTools = cloneProjectionMap(value.ExecutedTools)
		cloned.ExecutedEnhancedTools = cloneProjectionMap(value.ExecutedEnhancedTools)
		cloned.RerunTools = cloneSlice(value.RerunTools)
		cloned.RerunExtraMap = cloneProjectionMap(value.RerunExtraMap)
		return &cloned
	case *compose.InterruptInfo:
		return cloneComposeInterruptInfoForProjection(value)
	default:
		return value
	}
}

func projectRunContextMessages(runCtx *runContext, index *checkpointProjectionIndex,
	projection *checkpointProjectionV1) {
	defer func() {
		projection.RunCtxRefCount = len(projection.RunCtxRefs)
	}()
	if runCtx == nil {
		return
	}
	if runCtx.RootInput != nil {
		entries, projected := index.projectSchemaMessages(runCtx.RootInput.Messages)
		if projected {
			runCtx.RootInput.Messages = nil
			for i, entry := range entries {
				projection.RunCtxRefs = append(projection.RunCtxRefs, runCtxMessageProjectionV1{
					Target:       runCtxTargetRootInput,
					Index:        i,
					TargetLength: len(entries),
					Source:       checkpointMessageEntrySource(entry),
					Inline:       entry.Inline,
					IsNil:        entry.IsNil,
				})
			}
		}
	}
	if rootInput, ok := runCtx.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage]); ok &&
		rootInput != nil {
		entries, projected := index.projectAgenticMessages(rootInput.Messages)
		if projected {
			rootInput.Messages = nil
			for i, entry := range entries {
				projection.RunCtxRefs = append(projection.RunCtxRefs, runCtxMessageProjectionV1{
					Target:        runCtxTargetAgenticRootInput,
					Index:         i,
					TargetLength:  len(entries),
					Source:        checkpointAgenticMessageEntrySource(entry),
					AgenticInline: entry.Inline,
					IsNil:         entry.IsNil,
				})
			}
		}
	}
	if runCtx.Session == nil {
		return
	}
	for i, event := range runCtx.Session.Events {
		projectAgentEventMessage(event, runCtxTargetEvent, i, 0, index, projection)
	}
	for depth, lane := 0, runCtx.Session.LaneEvents; lane != nil; depth, lane = depth+1, lane.Parent {
		for i, event := range lane.Events {
			projectAgentEventMessage(event, runCtxTargetLaneEvent, i, depth, index, projection)
		}
	}
	if typed, ok := runCtx.Session.TypedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage]); ok && typed != nil {
		for i, event := range *typed {
			projectTypedAgentEventMessage(event, i, index, projection)
		}
	}
}

func projectAgentEventMessage(event *agentEventWrapper, target string, indexValue, laneDepth int,
	index *checkpointProjectionIndex, projection *checkpointProjectionV1) {
	if event == nil || event.AgentEvent == nil || event.Output == nil || event.Output.MessageOutput == nil {
		return
	}
	var message *schema.Message
	wasStreaming := event.Output.MessageOutput.IsStreaming
	if wasStreaming {
		event.consumeStream()
		if event.StreamErr != nil {
			return
		}
		message = event.concatenatedMessage
	} else {
		message = event.Output.MessageOutput.Message
	}
	source, ok := index.sourceForSchemaMessage(message)
	if !ok {
		return
	}
	event.Output.MessageOutput.IsStreaming = false
	event.Output.MessageOutput.Message = nil
	event.Output.MessageOutput.MessageStream = nil
	event.concatenatedMessage = nil
	projection.RunCtxRefs = append(projection.RunCtxRefs, runCtxMessageProjectionV1{
		Target:       target,
		Index:        indexValue,
		LaneDepth:    laneDepth,
		Source:       source,
		WasStreaming: wasStreaming,
	})
}

func projectTypedAgentEventMessage(event *typedAgentEventWrapper[*schema.AgenticMessage],
	indexValue int, index *checkpointProjectionIndex, projection *checkpointProjectionV1) {
	if event == nil || event.event == nil || event.event.Output == nil ||
		event.event.Output.MessageOutput == nil {
		return
	}
	var message *schema.AgenticMessage
	wasStreaming := event.event.Output.MessageOutput.IsStreaming
	if wasStreaming {
		event.consumeStream()
		if event.StreamErr != nil {
			return
		}
		message = event.concatenatedMessage
	} else {
		message = event.event.Output.MessageOutput.Message
	}
	source, ok := index.sourceForAgenticMessage(message)
	if !ok {
		return
	}
	event.event.Output.MessageOutput.IsStreaming = false
	event.event.Output.MessageOutput.Message = nil
	event.event.Output.MessageOutput.MessageStream = nil
	event.concatenatedMessage = nil
	projection.RunCtxRefs = append(projection.RunCtxRefs, runCtxMessageProjectionV1{
		Target:       runCtxTargetTypedEvent,
		Index:        indexValue,
		Source:       source,
		WasStreaming: wasStreaming,
	})
}

func projectInterruptInfoMessages(info *InterruptInfo, index *checkpointProjectionIndex,
	projection *checkpointProjectionV1) {
	defer func() {
		projection.InfoRefCount = len(projection.InfoRefs)
		projection.ToolResultRefCount = len(projection.ToolResultRefs)
	}()
	if info == nil {
		return
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil || chatModelInfo.Info == nil {
		return
	}
	projectComposeInterruptInfoMessages(chatModelInfo.Info, nil, index, projection)
}

func projectInterruptContextPrefixes(info *InterruptInfo, index *checkpointProjectionIndex) {
	if info == nil {
		return
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil {
		return
	}
	projectComposeInterruptContextPrefixes(chatModelInfo.Info, index)
}

func projectComposeInterruptContextPrefixes(info *compose.InterruptInfo,
	index *checkpointProjectionIndex) {
	if info == nil {
		return
	}
	for i, interruptCtx := range info.InterruptContexts {
		if projected, ok := index.projectInterruptContextPrefix(interruptCtx); ok {
			info.InterruptContexts[i] = projected
		}
		for current := info.InterruptContexts[i]; current != nil; current = current.Parent {
			projectInfoValueInterruptContextPrefixes(current.Info, index)
		}
	}
	projectInfoValueInterruptContextPrefixes(info.State, index)
	for _, value := range info.RerunNodesExtra {
		projectInfoValueInterruptContextPrefixes(value, index)
	}
	for _, subGraph := range info.SubGraphs {
		projectComposeInterruptContextPrefixes(subGraph, index)
	}
}

func projectInfoValueInterruptContextPrefixes(value any, index *checkpointProjectionIndex) {
	switch value := value.(type) {
	case *compose.InterruptInfo:
		projectComposeInterruptContextPrefixes(value, index)
	case *checkpointInterruptInfoPlaceholderV1:
		if value != nil {
			projectComposeInterruptContextPrefixes(value.Info, index)
		}
	}
}

func sealInterruptContextReferences(info *InterruptInfo) error {
	if info == nil {
		return nil
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil {
		return nil
	}
	return sealComposeInterruptContextReferences(chatModelInfo.Info)
}

func sealComposeInterruptContextReferences(info *compose.InterruptInfo) error {
	if info == nil {
		return nil
	}
	if err := sealInfoValueInterruptContextReferences(info.State); err != nil {
		return err
	}
	for _, key := range sortedStringKeys(info.RerunNodesExtra) {
		if err := sealInfoValueInterruptContextReferences(info.RerunNodesExtra[key]); err != nil {
			return err
		}
	}
	for _, interruptCtx := range info.InterruptContexts {
		if err := sealInterruptContextChain(interruptCtx); err != nil {
			return err
		}
	}
	for _, key := range sortedStringKeys(info.SubGraphs) {
		if err := sealComposeInterruptContextReferences(info.SubGraphs[key]); err != nil {
			return err
		}
	}
	return nil
}

func sealInterruptContextChain(interruptCtx *InterruptCtx) error {
	if interruptCtx == nil {
		return nil
	}
	if err := sealInterruptContextChain(interruptCtx.Parent); err != nil {
		return err
	}
	if err := sealInfoValueInterruptContextReferences(interruptCtx.Info); err != nil {
		return err
	}
	if ref, ok := interruptCtx.Info.(*checkpointInterruptContextPlaceholderV1); ok {
		digest, valid := checkpointInterruptContextReferenceIntegrity(ref, interruptCtx.Parent)
		if !valid {
			return errors.New(
				"failed to bind checkpoint projection interrupt context reference")
		}
		ref.IntegrityDigest = digest
	}
	return nil
}

func sealInfoValueInterruptContextReferences(value any) error {
	switch value := value.(type) {
	case *compose.InterruptInfo:
		return sealComposeInterruptContextReferences(value)
	case *checkpointInterruptInfoPlaceholderV1:
		if value != nil {
			return sealComposeInterruptContextReferences(value.Info)
		}
	}
	return nil
}

func (i *checkpointProjectionIndex) projectInterruptContextPrefix(
	interruptCtx *InterruptCtx) (*InterruptCtx, bool) {
	var (
		bestSource checkpointInterruptContextPlaceholderV1
		bestLength int
	)
	for _, candidate := range i.allInterruptInfos() {
		for contextIndex, source := range candidate.contexts {
			commonLength, addressPrefix := commonInterruptContextPrefix(interruptCtx, source)
			if commonLength <= bestLength {
				continue
			}
			sourceID, digest, ok := checkpointInterruptContextSourceMetadata(
				candidate, contextIndex)
			if !ok {
				continue
			}
			bestLength = commonLength
			bestSource = checkpointInterruptContextPlaceholderV1{
				SourceOrdinal: candidate.sourceOrdinal,
				SourceID:      sourceID,
				Digest:        digest,
				ContextIndex:  contextIndex,
				PrefixLength:  commonLength,
				AddressPrefix: addressPrefix,
			}
		}
	}
	if bestLength < 2 {
		return interruptCtx, false
	}
	tail := interruptCtx
	for step := 0; step < bestLength; step++ {
		tail = tail.Parent
	}
	if _, ok := checkpointInterruptContextTailDigest(tail); !ok {
		return interruptCtx, false
	}
	return &InterruptCtx{
		Info:   &bestSource,
		Parent: tail,
	}, true
}

func checkpointInterruptContextReferenceIntegrity(
	ref *checkpointInterruptContextPlaceholderV1, tail *InterruptCtx) (string, bool) {
	if ref == nil {
		return "", false
	}
	tailDigest, ok := checkpointInterruptContextTailDigest(tail)
	if !ok {
		return "", false
	}
	data, err := json.Marshal(struct {
		Version       int
		SourceOrdinal int
		RunnerPath    []string
		SourceID      string
		SourceDigest  string
		ContextIndex  int
		PrefixLength  int
		AddressPrefix Address
		TailDigest    string
	}{
		Version:       checkpointProjectionVersionV2,
		SourceOrdinal: ref.SourceOrdinal,
		RunnerPath:    ref.RunnerPath,
		SourceID:      ref.SourceID,
		SourceDigest:  ref.Digest,
		ContextIndex:  ref.ContextIndex,
		PrefixLength:  ref.PrefixLength,
		AddressPrefix: canonicalCheckpointAddress(ref.AddressPrefix),
		TailDigest:    tailDigest,
	})
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), true
}

func checkpointInterruptContextTailDigest(tail *InterruptCtx) (string, bool) {
	type tailNode struct {
		ID          string
		Address     Address
		InfoType    string
		InfoDigest  string
		IsRootCause bool
	}
	nodes := make([]tailNode, 0)
	visited := make(map[*InterruptCtx]struct{})
	for current := tail; current != nil; current = current.Parent {
		if _, exists := visited[current]; exists {
			return "", false
		}
		visited[current] = struct{}{}
		infoType, infoDigest, ok := checkpointinternal.SemanticDigest(current.Info)
		if !ok {
			return "", false
		}
		nodes = append(nodes, tailNode{
			ID:          current.ID,
			Address:     canonicalCheckpointAddress(current.Address),
			InfoType:    infoType,
			InfoDigest:  infoDigest,
			IsRootCause: current.IsRootCause,
		})
	}
	data, err := json.Marshal(struct {
		IsNil bool
		Nodes []tailNode
	}{
		IsNil: tail == nil,
		Nodes: nodes,
	})
	if err != nil {
		return "", false
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), true
}

// Semantic digest semantics are centralized in internal/checkpoint; ADK only consumes the stable digest API.
func canonicalCheckpointAddress(address Address) Address {
	canonical := make(Address, len(address))
	copy(canonical, address)
	return canonical
}

func checkpointInterruptContextSourceMetadata(candidate canonicalCheckpointInterruptInfo,
	contextIndex int) (string, string, bool) {
	if contextIndex < 0 || contextIndex >= len(candidate.contexts) ||
		candidate.contexts[contextIndex] == nil {
		return "", "", false
	}
	identityData, err := json.Marshal(struct {
		Path         []string
		SourceID     string
		ContextIndex int
	}{
		Path:         candidate.path,
		SourceID:     candidate.sourceID,
		ContextIndex: contextIndex,
	})
	if err != nil {
		return "", "", false
	}
	identity := sha256.Sum256(identityData)
	_, digest, ok := checkpointinternal.SemanticDigest(candidate.contexts[contextIndex])
	if !ok {
		return "", "", false
	}
	return hex.EncodeToString(identity[:]), digest, true
}

func commonInterruptContextPrefix(left, right *InterruptCtx) (int, Address) {
	addressPrefix, ok := interruptContextAddressPrefix(left, right)
	if !ok {
		return 0, nil
	}
	length := 0
	for left != nil && right != nil &&
		interruptContextNodeEqual(left, right, addressPrefix) {
		length++
		left = left.Parent
		right = right.Parent
	}
	return length, addressPrefix
}

func interruptContextAddressPrefix(left, right *InterruptCtx) (Address, bool) {
	if left == nil || right == nil || len(left.Address) < len(right.Address) {
		return nil, left == nil && right == nil
	}
	prefixLength := len(left.Address) - len(right.Address)
	if !Address(left.Address[prefixLength:]).Equals(right.Address) {
		return nil, false
	}
	return cloneSlice(left.Address[:prefixLength]), true
}

func interruptContextNodeEqual(left, right *InterruptCtx, addressPrefix Address) bool {
	if left == nil || right == nil {
		return left == right
	}
	expectedAddress := make(Address, 0, len(addressPrefix)+len(right.Address))
	expectedAddress = append(expectedAddress, addressPrefix...)
	expectedAddress = append(expectedAddress, right.Address...)
	return left.ID == right.ID &&
		left.Address.Equals(expectedAddress) &&
		reflect.DeepEqual(left.Info, right.Info) &&
		left.IsRootCause == right.IsRootCause
}

func projectComposeInterruptInfoMessages(info *compose.InterruptInfo, path []string,
	index *checkpointProjectionIndex, projection *checkpointProjectionV1) {
	if info == nil {
		return
	}
	projectInfoValueMessages(&info.State, infoProjectionTarget{
		kind:         infoTargetStateMessage,
		path:         path,
		contextIndex: -1,
	}, index, projection)
	keys := make([]string, 0, len(info.RerunNodesExtra))
	for key := range info.RerunNodesExtra {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := info.RerunNodesExtra[key]
		projectInfoValueMessages(&value, infoProjectionTarget{
			kind:         infoTargetRerunToolCalls,
			path:         path,
			contextIndex: -1,
			rerunKey:     key,
		}, index, projection)
		info.RerunNodesExtra[key] = value
	}
	for i, interruptCtx := range info.InterruptContexts {
		for depth, current := 0, interruptCtx; current != nil; depth, current = depth+1, current.Parent {
			projectInfoValueMessages(&current.Info, infoProjectionTarget{
				kind:         infoTargetContextStateMessage,
				path:         path,
				contextIndex: i,
				parentDepth:  depth,
			}, index, projection)
		}
	}
	subGraphKeys := make([]string, 0, len(info.SubGraphs))
	for key := range info.SubGraphs {
		subGraphKeys = append(subGraphKeys, key)
	}
	sort.Strings(subGraphKeys)
	for _, key := range subGraphKeys {
		projectComposeInterruptInfoMessages(info.SubGraphs[key],
			append(append([]string(nil), path...), key), index, projection)
	}
}

func projectInfoValueMessages(target *any, targetInfo infoProjectionTarget,
	index *checkpointProjectionIndex, projection *checkpointProjectionV1) {
	switch value := (*target).(type) {
	case *State:
		if value == nil {
			return
		}
		entries, projected := index.projectSchemaMessages(value.Messages)
		if projected {
			value.Messages = nil
			for i, entry := range entries {
				projection.InfoRefs = append(projection.InfoRefs, infoMessageProjectionV1{
					Target:        targetInfo.kind,
					SubGraphPath:  append([]string(nil), targetInfo.path...),
					ContextIndex:  targetInfo.contextIndex,
					ParentDepth:   targetInfo.parentDepth,
					RerunExtraKey: targetInfo.rerunKey,
					MessageIndex:  i,
					TargetLength:  len(entries),
					Source:        checkpointMessageEntrySource(entry),
					Inline:        entry.Inline,
					IsNil:         entry.IsNil,
				})
			}
		}
	case *agenticState:
		if value == nil {
			return
		}
		entries, projected := index.projectAgenticMessages(value.Messages)
		if projected {
			value.Messages = nil
			for i, entry := range entries {
				projection.InfoRefs = append(projection.InfoRefs, infoMessageProjectionV1{
					Target:        targetInfo.kind,
					SubGraphPath:  append([]string(nil), targetInfo.path...),
					ContextIndex:  targetInfo.contextIndex,
					ParentDepth:   targetInfo.parentDepth,
					RerunExtraKey: targetInfo.rerunKey,
					MessageIndex:  i,
					TargetLength:  len(entries),
					Source:        checkpointAgenticMessageEntrySource(entry),
					AgenticInline: entry.Inline,
					IsNil:         entry.IsNil,
				})
			}
		}
	case *compose.ToolsInterruptAndRerunExtra:
		projectInfoToolResults(value, targetInfo, index, projection)
		if targetInfo.kind != infoTargetRerunToolCalls &&
			targetInfo.kind != infoTargetContextStateMessage {
			return
		}
		source, ok := index.sourceForToolCalls(value.ToolCalls)
		if !ok {
			return
		}
		value.ToolCalls = nil
		projection.InfoRefs = append(projection.InfoRefs, infoMessageProjectionV1{
			Target:        normalizeToolCallsTarget(targetInfo.kind),
			SubGraphPath:  append([]string(nil), targetInfo.path...),
			ContextIndex:  targetInfo.contextIndex,
			ParentDepth:   targetInfo.parentDepth,
			RerunExtraKey: targetInfo.rerunKey,
			MessageIndex:  -1,
			Source:        source,
		})
	case *compose.InterruptInfo:
		nestedProjection := &checkpointProjectionV1{}
		projectComposeInterruptInfoMessages(value, nil, index, nestedProjection)
		if len(nestedProjection.InfoRefs) > 0 || len(nestedProjection.ToolResultRefs) > 0 {
			*target = &checkpointInterruptInfoPlaceholderV1{
				Info:               value,
				RefCount:           len(nestedProjection.InfoRefs),
				ToolResultRefCount: len(nestedProjection.ToolResultRefs),
				Refs:               nestedProjection.InfoRefs,
				ToolResultRefs:     nestedProjection.ToolResultRefs,
			}
		}
	}
}

func normalizeToolCallsTarget(target string) string {
	if target == infoTargetRerunToolCalls {
		return target
	}
	return infoTargetContextToolCalls
}

func (i *checkpointProjectionIndex) sourceForToolCalls(
	toolCalls []schema.ToolCall) (checkpointMessageSourceV1, bool) {
	if len(toolCalls) == 0 {
		return checkpointMessageSourceV1{}, false
	}
	digest, ok := checkpointProjectionValueDigest(toolCalls)
	if !ok {
		return checkpointMessageSourceV1{}, false
	}
	var matched *canonicalCheckpointMessage
	for _, candidate := range i.allMessages() {
		if candidate.message == nil {
			continue
		}
		candidateDigest, ok := checkpointProjectionValueDigest(candidate.message.ToolCalls)
		if !ok || candidateDigest != digest ||
			!gobSemanticEqual(candidate.message.ToolCalls, toolCalls) {
			continue
		}
		if matched == nil || candidate.source.SourceOrdinal < matched.source.SourceOrdinal {
			candidateCopy := candidate
			matched = &candidateCopy
		}
	}
	if matched != nil {
		return compactCheckpointMessageSource(matched.source), true
	}
	return checkpointMessageSourceV1{}, false
}

func (i *checkpointProjectionIndex) projectSchemaMessages(
	messages []*schema.Message) ([]checkpointMessageSliceEntryV1, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	entries := make([]checkpointMessageSliceEntryV1, len(messages))
	projected := false
	for messageIndex, message := range messages {
		if message == nil {
			entries[messageIndex].IsNil = true
			continue
		}
		source, ok := i.sourceForSchemaMessage(message)
		if ok {
			sourceCopy := source
			entries[messageIndex].Source = &sourceCopy
			projected = true
		} else {
			entries[messageIndex].Inline = message
		}
	}
	return entries, projected
}

func checkpointMessageEntrySource(entry checkpointMessageSliceEntryV1) checkpointMessageSourceV1 {
	if entry.Source == nil {
		return checkpointMessageSourceV1{}
	}
	return *entry.Source
}

func (i *checkpointProjectionIndex) projectAgenticMessages(
	messages []*schema.AgenticMessage) ([]checkpointAgenticMessageSliceEntryV1, bool) {
	if len(messages) == 0 {
		return nil, false
	}
	entries := make([]checkpointAgenticMessageSliceEntryV1, len(messages))
	projected := false
	for messageIndex, message := range messages {
		if message == nil {
			entries[messageIndex].IsNil = true
			continue
		}
		source, ok := i.sourceForAgenticMessage(message)
		if ok {
			sourceCopy := source
			entries[messageIndex].Source = &sourceCopy
			projected = true
		} else {
			entries[messageIndex].Inline = message
		}
	}
	return entries, projected
}

func (i *checkpointProjectionIndex) projectComposeAgenticMessages(
	messages []*schema.AgenticMessage) ([]checkpointAgenticMessageSliceEntryV1, bool) {
	// Gob cannot re-encode nil pointer elements after compose-value hydration.
	for _, message := range messages {
		if message == nil {
			return nil, false
		}
	}
	return i.projectAgenticMessages(messages)
}

func checkpointAgenticMessageEntrySource(
	entry checkpointAgenticMessageSliceEntryV1) checkpointMessageSourceV1 {
	if entry.Source == nil {
		return checkpointMessageSourceV1{}
	}
	return *entry.Source
}

func projectComposeCheckpointValues(data []byte, index *checkpointProjectionIndex) ([]byte, bool, error) {
	changed := false
	transformed, err := compose.TransformCheckpointValues(data, &gobSerializer{},
		func(_ compose.NodePath, location compose.CheckpointValueLocation, value any) (any, bool, error) {
			if location.Kind == compose.CheckpointValueState {
				return value, false, nil
			}
			switch value := value.(type) {
			case *schema.Message:
				source, ok := index.sourceForSchemaMessage(value)
				if !ok {
					return value, false, nil
				}
				changed = true
				return &checkpointMessagePlaceholderV1{Source: source}, true, nil
			case []*schema.Message:
				entries := make([]checkpointMessageSliceEntryV1, len(value))
				projected := false
				for i, message := range value {
					if source, ok := index.sourceForSchemaMessage(message); ok {
						sourceCopy := source
						entries[i].Source = &sourceCopy
						projected = true
					} else {
						entries[i].Inline = message
					}
				}
				if !projected {
					return value, false, nil
				}
				changed = true
				return &checkpointMessageSlicePlaceholderV1{Entries: entries}, true, nil
			case *schema.AgenticMessage:
				source, ok := index.sourceForAgenticMessage(value)
				if !ok {
					return value, false, nil
				}
				changed = true
				return &checkpointAgenticMessagePlaceholderV1{Source: source}, true, nil
			case []*schema.AgenticMessage:
				entries, projected := index.projectComposeAgenticMessages(value)
				if !projected {
					return value, false, nil
				}
				changed = true
				return &checkpointAgenticMessageSlicePlaceholderV1{Entries: entries}, true, nil
			default:
				return value, false, nil
			}
		})
	if err != nil || !changed {
		return transformed, changed, err
	}
	// Projection is accepted only if hydrating it reproduces the original
	// logical values. Gob bytes are not stable for map-bearing values.
	restored, err := hydrateComposeCheckpointValues(transformed, index)
	if err != nil {
		return nil, false, err
	}
	equivalent, err := composeCheckpointValuesEquivalent(data, restored)
	if err != nil {
		return nil, false, fmt.Errorf("failed to compare restored checkpoint projection: %w", err)
	}
	if !equivalent {
		return data, false, nil
	}
	return transformed, true, nil
}

func composeCheckpointValuesEquivalent(left, right []byte) (bool, error) {
	collect := func(data []byte) ([]composeCheckpointLogicalValue, error) {
		var values []composeCheckpointLogicalValue
		err := compose.WalkCheckpointValues(data, &gobSerializer{},
			func(path compose.NodePath, location compose.CheckpointValueLocation, value any) error {
				values = append(values, composeCheckpointLogicalValue{
					path:     cloneSlice(path.GetPath()),
					location: location,
					value:    value,
				})
				return nil
			})
		return values, err
	}

	leftValues, err := collect(left)
	if err != nil {
		return false, err
	}
	rightValues, err := collect(right)
	if err != nil {
		return false, err
	}
	return gobSemanticEqual(leftValues, rightValues), nil
}

type gobSemanticVisit struct {
	typ         reflect.Type
	left, right uintptr
}

type gobSemanticMapEntry struct {
	key   reflect.Value
	value reflect.Value
}

type gobSemanticFingerprintVisit struct {
	typ     reflect.Type
	pointer uintptr
}

type gobSemanticFingerprintContext uint8

const (
	gobSemanticValueContext gobSemanticFingerprintContext = iota
	gobSemanticMapKeyContext
)

type gobSemanticComparator struct {
	visiting map[gobSemanticVisit]struct{}
}

func gobSemanticEqual(left, right any) bool {
	comparator := gobSemanticComparator{
		visiting: make(map[gobSemanticVisit]struct{}),
	}
	return comparator.valueEqual(reflect.ValueOf(left), reflect.ValueOf(right))
}

func (c *gobSemanticComparator) valueEqual(left, right reflect.Value) bool {
	if !left.IsValid() || !right.IsValid() {
		return left.IsValid() == right.IsValid()
	}
	if left.Type() != right.Type() {
		return false
	}

	switch left.Kind() {
	case reflect.Bool:
		return left.Bool() == right.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return left.Int() == right.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return left.Uint() == right.Uint()
	case reflect.Float32:
		return math.Float32bits(float32(left.Float())) ==
			math.Float32bits(float32(right.Float()))
	case reflect.Float64:
		return math.Float64bits(left.Float()) == math.Float64bits(right.Float())
	case reflect.Complex64:
		leftValue, rightValue := complex64(left.Complex()), complex64(right.Complex())
		return math.Float32bits(real(leftValue)) == math.Float32bits(real(rightValue)) &&
			math.Float32bits(imag(leftValue)) == math.Float32bits(imag(rightValue))
	case reflect.Complex128:
		leftValue, rightValue := left.Complex(), right.Complex()
		return math.Float64bits(real(leftValue)) == math.Float64bits(real(rightValue)) &&
			math.Float64bits(imag(leftValue)) == math.Float64bits(imag(rightValue))
	case reflect.String:
		return left.String() == right.String()
	case reflect.Chan, reflect.UnsafePointer:
		return left.Pointer() == right.Pointer()
	case reflect.Func:
		return left.IsNil() && right.IsNil()
	case reflect.Interface:
		return c.interfaceValueEqual(left, right)
	case reflect.Pointer:
		return c.pointerValueEqual(left, right)
	case reflect.Slice:
		return c.sliceValueEqual(left, right)
	case reflect.Array:
		return c.arrayValueEqual(left, right)
	case reflect.Map:
		return c.mapValueEqual(left, right)
	case reflect.Struct:
		return c.structValueEqual(left, right)
	default:
		return false
	}
}

func (c *gobSemanticComparator) interfaceValueEqual(left, right reflect.Value) bool {
	if left.IsNil() || right.IsNil() {
		return left.IsNil() == right.IsNil()
	}
	return c.valueEqual(left.Elem(), right.Elem())
}

func (c *gobSemanticComparator) pointerValueEqual(left, right reflect.Value) bool {
	if left.IsNil() || right.IsNil() {
		return left.IsNil() == right.IsNil()
	}
	if c.alreadyVisiting(left, right) {
		return true
	}
	defer c.leave(left, right)
	return c.valueEqual(left.Elem(), right.Elem())
}

func (c *gobSemanticComparator) sliceValueEqual(left, right reflect.Value) bool {
	if left.IsNil() || right.IsNil() {
		return left.IsNil() == right.IsNil()
	}
	if left.Len() != right.Len() {
		return false
	}
	if c.alreadyVisiting(left, right) {
		return true
	}
	defer c.leave(left, right)
	for i := 0; i < left.Len(); i++ {
		if !c.valueEqual(left.Index(i), right.Index(i)) {
			return false
		}
	}
	return true
}

func (c *gobSemanticComparator) arrayValueEqual(left, right reflect.Value) bool {
	for i := 0; i < left.Len(); i++ {
		if !c.valueEqual(left.Index(i), right.Index(i)) {
			return false
		}
	}
	return true
}

func (c *gobSemanticComparator) mapValueEqual(left, right reflect.Value) bool {
	if left.IsNil() || right.IsNil() {
		return left.IsNil() == right.IsNil()
	}
	if left.Len() != right.Len() {
		return false
	}
	if c.alreadyVisiting(left, right) {
		return true
	}
	defer c.leave(left, right)

	if c.mapSupportsDirectLookup(left, right) {
		return c.directMapValueEqual(left, right)
	}
	return c.semanticMapValueEqual(left, right)
}

func (c *gobSemanticComparator) mapSupportsDirectLookup(left, right reflect.Value) bool {
	keyType := left.Type().Key()
	if !gobSemanticMapKeySupportsDirectLookup(keyType) {
		return false
	}
	if !gobSemanticMapKeyCanContainNaN(keyType) {
		return true
	}
	return c.mapKeysReflexive(left) && c.mapKeysReflexive(right)
}

func (c *gobSemanticComparator) mapKeysReflexive(value reflect.Value) bool {
	iterator := value.MapRange()
	for iterator.Next() {
		if !gobSemanticMapKeyReflexive(iterator.Key()) {
			return false
		}
	}
	return true
}

func (c *gobSemanticComparator) directMapValueEqual(left, right reflect.Value) bool {
	iterator := left.MapRange()
	for iterator.Next() {
		rightValue := right.MapIndex(iterator.Key())
		if !rightValue.IsValid() || !c.valueEqual(iterator.Value(), rightValue) {
			return false
		}
	}
	return true
}

func (c *gobSemanticComparator) semanticMapValueEqual(left, right reflect.Value) bool {
	// Pair keys with values while iterating so non-reflexive keys are never
	// passed to MapIndex. Semantic digests narrow matching to equivalent-entry
	// candidates while the full comparison remains authoritative.
	leftEntries := gobSemanticMapEntries(left)
	rightEntries := gobSemanticMapEntries(right)
	rightByDigest, ok := gobSemanticMapEntryBuckets(rightEntries)
	if !ok {
		return false
	}
	for _, leftEntry := range leftEntries {
		digest, ok := gobSemanticMapEntryDigest(leftEntry)
		if !ok {
			return false
		}
		candidates := rightByDigest[digest]
		found := false
		for i, rightEntry := range candidates {
			if !c.mapKeyEqual(leftEntry.key, rightEntry.key) ||
				!c.valueEqual(leftEntry.value, rightEntry.value) {
				continue
			}
			candidates[i] = candidates[len(candidates)-1]
			rightByDigest[digest] = candidates[:len(candidates)-1]
			found = true
			break
		}
		if !found {
			return false
		}
	}
	return true
}

func gobSemanticMapEntryBuckets(entries []gobSemanticMapEntry) (
	map[string][]gobSemanticMapEntry, bool,
) {
	byDigest := make(map[string][]gobSemanticMapEntry, len(entries))
	for _, entry := range entries {
		digest, ok := gobSemanticMapEntryDigest(entry)
		if !ok {
			return nil, false
		}
		byDigest[digest] = append(byDigest[digest], entry)
	}
	return byDigest, true
}

func gobSemanticMapEntryDigest(entry gobSemanticMapEntry) (string, bool) {
	keyDigest, ok := gobSemanticMapKeyDigest(entry.key)
	if !ok {
		return "", false
	}
	valueDigest, ok := gobSemanticMapValueDigest(entry.value)
	if !ok {
		return "", false
	}
	return keyDigest + "\x00" + valueDigest, true
}

func gobSemanticMapKeyDigest(value reflect.Value) (string, bool) {
	return gobSemanticMapDigest(value, gobSemanticMapKeyContext)
}

func gobSemanticMapValueDigest(value reflect.Value) (string, bool) {
	return gobSemanticMapDigest(value, gobSemanticValueContext)
}

func gobSemanticMapDigest(value reflect.Value,
	context gobSemanticFingerprintContext) (string, bool) {
	data, ok := appendGobSemanticMapDigest(
		nil, value, context, make(map[gobSemanticFingerprintVisit]int))
	if !ok {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
}

func appendGobSemanticMapDigest(data []byte, value reflect.Value,
	context gobSemanticFingerprintContext,
	visiting map[gobSemanticFingerprintVisit]int) ([]byte, bool) {
	if !value.IsValid() {
		return append(data, "invalid;"...), true
	}
	data = appendFramedString(data, value.Type().PkgPath()+"\x00"+value.Type().String())
	data = append(data, byte(value.Kind()))
	switch value.Kind() {
	case reflect.Bool:
		if value.Bool() {
			return appendFramedString(data, "1"), true
		}
		return appendFramedString(data, "0"), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return appendFramedString(data, strconv.FormatInt(value.Int(), 10)), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return appendFramedString(data, strconv.FormatUint(value.Uint(), 10)), true
	case reflect.Float32:
		return appendGobSemanticFloat32(
			data, float32(value.Float()), context), true
	case reflect.Float64:
		return appendGobSemanticFloat64(data, value.Float(), context), true
	case reflect.Complex64:
		number := complex64(value.Complex())
		data = appendGobSemanticFloat32(data, real(number), context)
		return appendGobSemanticFloat32(data, imag(number), context), true
	case reflect.Complex128:
		number := value.Complex()
		data = appendGobSemanticFloat64(data, real(number), context)
		return appendGobSemanticFloat64(data, imag(number), context), true
	case reflect.String:
		return appendFramedString(data, value.String()), true
	case reflect.Interface:
		if value.IsNil() {
			return append(data, "nil;"...), true
		}
		return appendGobSemanticMapDigest(data, value.Elem(), context, visiting)
	case reflect.Pointer:
		if value.IsNil() {
			return append(data, "nil;"...), true
		}
		visit := gobSemanticFingerprintVisit{typ: value.Type(), pointer: value.Pointer()}
		if reference, exists := visiting[visit]; exists {
			return appendFramedString(append(data, "ref:"...), strconv.Itoa(reference)), true
		}
		visiting[visit] = len(visiting) + 1
		result, ok := appendGobSemanticMapDigest(
			data, value.Elem(), context, visiting)
		delete(visiting, visit)
		return result, ok
	case reflect.Slice:
		if value.IsNil() {
			return append(data, "nil;"...), true
		}
		visit := gobSemanticFingerprintVisit{typ: value.Type(), pointer: value.Pointer()}
		if reference, exists := visiting[visit]; exists {
			return appendFramedString(append(data, "ref:"...), strconv.Itoa(reference)), true
		}
		visiting[visit] = len(visiting) + 1
		for i := 0; i < value.Len(); i++ {
			var ok bool
			data, ok = appendGobSemanticMapDigest(
				data, value.Index(i), context, visiting)
			if !ok {
				delete(visiting, visit)
				return nil, false
			}
		}
		delete(visiting, visit)
		return data, true
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			var ok bool
			data, ok = appendGobSemanticMapDigest(
				data, value.Index(i), context, visiting)
			if !ok {
				return nil, false
			}
		}
		return data, true
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			var ok bool
			data, ok = appendGobSemanticMapDigest(
				data, value.Field(i), context, visiting)
			if !ok {
				return nil, false
			}
		}
		return data, true
	default:
		return nil, false
	}
}

func appendGobSemanticFloat32(data []byte, value float32,
	context gobSemanticFingerprintContext) []byte {
	if context == gobSemanticMapKeyContext && value == 0 {
		value = 0
	}
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], math.Float32bits(value))
	return append(data, encoded[:]...)
}

func appendGobSemanticFloat64(data []byte, value float64,
	context gobSemanticFingerprintContext) []byte {
	if context == gobSemanticMapKeyContext && value == 0 {
		value = 0
	}
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], math.Float64bits(value))
	return append(data, encoded[:]...)
}

func appendFramedString(data []byte, value string) []byte {
	data = strconv.AppendInt(data, int64(len(value)), 10)
	data = append(data, ':')
	return append(data, value...)
}

func (c *gobSemanticComparator) structValueEqual(left, right reflect.Value) bool {
	for i := 0; i < left.NumField(); i++ {
		if !c.valueEqual(left.Field(i), right.Field(i)) {
			return false
		}
	}
	return true
}

func gobSemanticMapEntries(value reflect.Value) []gobSemanticMapEntry {
	entries := make([]gobSemanticMapEntry, 0, value.Len())
	iterator := value.MapRange()
	for iterator.Next() {
		entries = append(entries, gobSemanticMapEntry{
			key:   iterator.Key(),
			value: iterator.Value(),
		})
	}
	return entries
}

func (c *gobSemanticComparator) mapKeyEqual(left, right reflect.Value) bool {
	if !left.IsValid() || !right.IsValid() {
		return left.IsValid() == right.IsValid()
	}
	if left.Type() != right.Type() {
		return false
	}

	switch left.Kind() {
	case reflect.Float32, reflect.Float64:
		return gobSemanticFloatKeyEqual(left, right)
	case reflect.Complex64, reflect.Complex128:
		return gobSemanticComplexKeyEqual(left, right)
	case reflect.Interface:
		if left.IsNil() || right.IsNil() {
			return left.IsNil() == right.IsNil()
		}
		return c.mapKeyEqual(left.Elem(), right.Elem())
	case reflect.Pointer:
		if left.IsNil() || right.IsNil() {
			return left.IsNil() == right.IsNil()
		}
		if c.alreadyVisiting(left, right) {
			return true
		}
		defer c.leave(left, right)
		return c.mapKeyEqual(left.Elem(), right.Elem())
	case reflect.Array:
		for i := 0; i < left.Len(); i++ {
			if !c.mapKeyEqual(left.Index(i), right.Index(i)) {
				return false
			}
		}
		return true
	case reflect.Struct:
		for i := 0; i < left.NumField(); i++ {
			if !c.mapKeyEqual(left.Field(i), right.Field(i)) {
				return false
			}
		}
		return true
	default:
		return c.valueEqual(left, right)
	}
}

func gobSemanticFloatKeyEqual(left, right reflect.Value) bool {
	if left.Kind() == reflect.Float32 {
		return gobSemanticFloat32KeyEqual(float32(left.Float()), float32(right.Float()))
	}
	return gobSemanticFloat64KeyEqual(left.Float(), right.Float())
}

func gobSemanticComplexKeyEqual(left, right reflect.Value) bool {
	leftComplex, rightComplex := left.Complex(), right.Complex()
	if leftComplex == rightComplex {
		return true
	}
	if left.Kind() == reflect.Complex64 {
		left64, right64 := complex64(leftComplex), complex64(rightComplex)
		return gobSemanticFloat32KeyEqual(real(left64), real(right64)) &&
			gobSemanticFloat32KeyEqual(imag(left64), imag(right64))
	}
	return gobSemanticFloat64KeyEqual(real(leftComplex), real(rightComplex)) &&
		gobSemanticFloat64KeyEqual(imag(leftComplex), imag(rightComplex))
}

func gobSemanticFloat32KeyEqual(left, right float32) bool {
	return left == right ||
		(math.IsNaN(float64(left)) && math.IsNaN(float64(right)) &&
			math.Float32bits(left) == math.Float32bits(right))
}

func gobSemanticFloat64KeyEqual(left, right float64) bool {
	return left == right ||
		(math.IsNaN(left) && math.IsNaN(right) &&
			math.Float64bits(left) == math.Float64bits(right))
}

// Comparable pointer and interface values can change identity across Gob
// decoding, so only recursively identity-stable key types support direct
// lookup. NaN-capable types require an additional runtime reflexivity check.
func gobSemanticMapKeySupportsDirectLookup(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.String:
		return true
	case reflect.Array:
		return gobSemanticMapKeySupportsDirectLookup(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if !gobSemanticMapKeySupportsDirectLookup(typ.Field(i).Type) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

func gobSemanticMapKeyCanContainNaN(typ reflect.Type) bool {
	switch typ.Kind() {
	case reflect.Float32, reflect.Float64, reflect.Complex64, reflect.Complex128:
		return true
	case reflect.Array:
		return gobSemanticMapKeyCanContainNaN(typ.Elem())
	case reflect.Struct:
		for i := 0; i < typ.NumField(); i++ {
			if gobSemanticMapKeyCanContainNaN(typ.Field(i).Type) {
				return true
			}
		}
	}
	return false
}

func gobSemanticMapKeyReflexive(value reflect.Value) bool {
	switch value.Kind() {
	case reflect.Float32, reflect.Float64:
		number := value.Float()
		return number == number
	case reflect.Complex64, reflect.Complex128:
		number := value.Complex()
		return number == number
	case reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if !gobSemanticMapKeyReflexive(value.Index(i)) {
				return false
			}
		}
	case reflect.Struct:
		for i := 0; i < value.NumField(); i++ {
			if !gobSemanticMapKeyReflexive(value.Field(i)) {
				return false
			}
		}
	}
	return true
}

func (c *gobSemanticComparator) alreadyVisiting(left, right reflect.Value) bool {
	visit := gobSemanticVisit{
		typ:   left.Type(),
		left:  left.Pointer(),
		right: right.Pointer(),
	}
	if _, ok := c.visiting[visit]; ok {
		return true
	}
	c.visiting[visit] = struct{}{}
	return false
}

func (c *gobSemanticComparator) leave(left, right reflect.Value) {
	delete(c.visiting, gobSemanticVisit{
		typ:   left.Type(),
		left:  left.Pointer(),
		right: right.Pointer(),
	})
}

func hydrateComposeCheckpointValues(data []byte, index *checkpointProjectionIndex) ([]byte, error) {
	return compose.TransformCheckpointValues(data, &gobSerializer{},
		func(_ compose.NodePath, _ compose.CheckpointValueLocation, value any) (any, bool, error) {
			switch value := value.(type) {
			case *checkpointMessagePlaceholderV1:
				if value == nil {
					return nil, false, errors.New("checkpoint projection contains a nil message reference")
				}
				message, err := index.schemaMessage(value.Source)
				return message, err == nil, err
			case *checkpointMessageSlicePlaceholderV1:
				if value == nil {
					return nil, false, errors.New("checkpoint projection contains a nil message-slice reference")
				}
				messages := make([]*schema.Message, len(value.Entries))
				for i, entry := range value.Entries {
					if entry.IsNil {
						return nil, false, errors.New(
							"checkpoint projection cannot restore a nil message into a compose value")
					}
					if entry.Source == nil {
						if entry.Inline == nil {
							return nil, false, errors.New(
								"checkpoint projection inline message is missing")
						}
						messages[i] = entry.Inline
						continue
					}
					if entry.Inline != nil {
						return nil, false, errors.New(
							"checkpoint projection message has both inline data and a source reference")
					}
					message, err := index.schemaMessage(*entry.Source)
					if err != nil {
						return nil, false, err
					}
					messages[i] = message
				}
				return messages, true, nil
			case *checkpointAgenticMessagePlaceholderV1:
				if value == nil {
					return nil, false, errors.New("checkpoint projection contains a nil agentic message reference")
				}
				message, err := index.agenticMessage(value.Source)
				return message, err == nil, err
			case *checkpointAgenticMessageSlicePlaceholderV1:
				if value == nil {
					return nil, false, errors.New("checkpoint projection contains a nil agentic message-slice reference")
				}
				messages := make([]*schema.AgenticMessage, len(value.Entries))
				for i, entry := range value.Entries {
					if entry.IsNil {
						return nil, false, errors.New(
							"checkpoint projection cannot restore a nil agentic message into a compose value")
					}
					if entry.Source == nil {
						if entry.Inline == nil {
							return nil, false, errors.New(
								"checkpoint projection inline agentic message is missing")
						}
						messages[i] = entry.Inline
						continue
					}
					if entry.Inline != nil {
						return nil, false, errors.New(
							"checkpoint projection agentic message has both inline data and a source reference")
					}
					message, err := index.agenticMessage(*entry.Source)
					if err != nil {
						return nil, false, err
					}
					messages[i] = message
				}
				return messages, true, nil
			default:
				return value, false, nil
			}
		})
}

func validateRunCtxProjectionRefs(refs []runCtxMessageProjectionV1, expectedCount int) error {
	if len(refs) != expectedCount {
		return fmt.Errorf("checkpoint projection run context reference count mismatch: got %d, want %d",
			len(refs), expectedCount)
	}
	seen := make(map[string]struct{}, len(refs))
	sliceCounts := make(map[string]int)
	sliceLengths := make(map[string]int)
	for _, ref := range refs {
		if ref.Index < 0 || ref.LaneDepth < 0 {
			return fmt.Errorf("checkpoint projection has invalid run context coordinates %d/%d",
				ref.LaneDepth, ref.Index)
		}
		if err := validateRunCtxProjectionTarget(ref); err != nil {
			return err
		}
		if ref.Target == runCtxTargetRootInput || ref.Target == runCtxTargetAgenticRootInput {
			key := runCtxProjectionTargetKey(ref.Target, ref.LaneDepth)
			if length, exists := sliceLengths[key]; exists && length != ref.TargetLength {
				return fmt.Errorf("checkpoint projection target %q has inconsistent lengths", key)
			}
			sliceLengths[key] = ref.TargetLength
			sliceCounts[key]++
		}
		key := fmt.Sprintf("%s/%d/%d", ref.Target, ref.LaneDepth, ref.Index)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("checkpoint projection has duplicate run context target %q", key)
		}
		seen[key] = struct{}{}
	}
	for _, key := range sortedStringKeys(sliceCounts) {
		count := sliceCounts[key]
		if count != sliceLengths[key] {
			return fmt.Errorf("checkpoint projection has incomplete run context slice %q", key)
		}
	}
	for _, ref := range refs {
		kind := projectionMessageKindSchema
		if ref.Target == runCtxTargetAgenticRootInput || ref.Target == runCtxTargetTypedEvent {
			kind = projectionMessageKindAgentic
		}
		if err := validateProjectionMessagePayload(
			ref.Source, ref.Inline, ref.AgenticInline, ref.IsNil, kind); err != nil {
			return err
		}
	}
	return nil
}

func validateRunCtxProjectionTarget(ref runCtxMessageProjectionV1) error {
	switch ref.Target {
	case runCtxTargetRootInput, runCtxTargetAgenticRootInput:
		if ref.LaneDepth != 0 {
			return fmt.Errorf("checkpoint projection target %q has invalid lane depth %d",
				ref.Target, ref.LaneDepth)
		}
		if ref.TargetLength <= 0 {
			return fmt.Errorf("checkpoint projection target %q has invalid length %d",
				ref.Target, ref.TargetLength)
		}
		if ref.Index >= ref.TargetLength {
			return fmt.Errorf("checkpoint projection target %q index %d exceeds length %d",
				ref.Target, ref.Index, ref.TargetLength)
		}
		if ref.WasStreaming {
			return fmt.Errorf("checkpoint projection target %q has unexpected streaming state",
				ref.Target)
		}
	case runCtxTargetEvent, runCtxTargetTypedEvent:
		if ref.LaneDepth != 0 || ref.TargetLength != 0 || ref.IsNil {
			return fmt.Errorf("checkpoint projection target %q has invalid lane depth %d",
				ref.Target, ref.LaneDepth)
		}
	case runCtxTargetLaneEvent:
		if ref.TargetLength != 0 || ref.IsNil {
			return fmt.Errorf("checkpoint projection target %q has unexpected slice length",
				ref.Target)
		}
	default:
		return fmt.Errorf("checkpoint projection has unsupported run context target %q", ref.Target)
	}
	return nil
}

func validateProjectionMessagePayload(source checkpointMessageSourceV1, inline *schema.Message,
	agenticInline *schema.AgenticMessage, isNil bool, kind string) error {
	sourceActive := source.MessageID != ""
	sourceHasMetadata := sourceActive || source.SourceOrdinal != 0 ||
		source.AgentToolDepth != 0 || source.Kind != "" || len(source.GraphPath) != 0 ||
		source.Index != 0 || source.Digest != ""
	formCount := 0
	if sourceActive {
		formCount++
	}
	if inline != nil {
		formCount++
	}
	if agenticInline != nil {
		formCount++
	}
	if isNil {
		formCount++
	}

	matchingInline := inline != nil
	if kind == projectionMessageKindAgentic {
		matchingInline = agenticInline != nil
	}
	if !sourceHasMetadata && !matchingInline && !isNil && formCount == 0 {
		if kind == projectionMessageKindAgentic {
			return errors.New("checkpoint projection inline agentic message is missing")
		}
		return errors.New("checkpoint projection inline message is missing")
	}
	if sourceHasMetadata != sourceActive || formCount != 1 ||
		(kind == projectionMessageKindSchema && agenticInline != nil) ||
		(kind == projectionMessageKindAgentic && inline != nil) {
		return fmt.Errorf("checkpoint projection %s message payload must contain exactly one of source, inline, or explicit nil",
			kind)
	}
	return nil
}

func validateInfoProjectionRefs(refs []infoMessageProjectionV1, expectedCount int) error {
	if len(refs) != expectedCount {
		return fmt.Errorf("checkpoint projection interrupt info reference count mismatch: got %d, want %d",
			len(refs), expectedCount)
	}
	seen := make(map[string]struct{}, len(refs))
	sliceCounts := make(map[string]int)
	sliceLengths := make(map[string]int)
	for _, ref := range refs {
		if ref.ParentDepth < 0 {
			return fmt.Errorf("checkpoint projection has invalid parent depth %d", ref.ParentDepth)
		}
		if err := validateInfoProjectionTarget(ref); err != nil {
			return err
		}
		key := fmt.Sprintf("%s/%q/%d/%d/%s/%d", ref.Target, ref.SubGraphPath,
			ref.ContextIndex, ref.ParentDepth, ref.RerunExtraKey, ref.MessageIndex)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("checkpoint projection has duplicate interrupt info target %q", key)
		}
		seen[key] = struct{}{}
		if ref.Target == infoTargetStateMessage || ref.Target == infoTargetContextStateMessage {
			targetKey := infoProjectionTargetKey(ref)
			if length, exists := sliceLengths[targetKey]; exists && length != ref.TargetLength {
				return fmt.Errorf("checkpoint projection target %q has inconsistent lengths", targetKey)
			}
			sliceLengths[targetKey] = ref.TargetLength
			sliceCounts[targetKey]++
		}
	}
	for _, key := range sortedStringKeys(sliceCounts) {
		count := sliceCounts[key]
		if count != sliceLengths[key] {
			return fmt.Errorf("checkpoint projection has incomplete interrupt info slice %q", key)
		}
	}
	return nil
}

func validateInfoProjectionTarget(ref infoMessageProjectionV1) error {
	switch ref.Target {
	case infoTargetStateMessage:
		if ref.ContextIndex != -1 || ref.ParentDepth != 0 || ref.RerunExtraKey != "" ||
			ref.MessageIndex < 0 || ref.TargetLength <= 0 || ref.MessageIndex >= ref.TargetLength {
			return errors.New("checkpoint projection has invalid interrupt state coordinates")
		}
	case infoTargetContextStateMessage:
		if ref.ContextIndex < 0 || ref.RerunExtraKey != "" || ref.MessageIndex < 0 ||
			ref.TargetLength <= 0 || ref.MessageIndex >= ref.TargetLength {
			return errors.New("checkpoint projection has invalid context state coordinates")
		}
	case infoTargetRerunToolCalls:
		if ref.ContextIndex != -1 || ref.ParentDepth != 0 || ref.MessageIndex != -1 ||
			ref.RerunExtraKey == "" || ref.TargetLength != 0 || ref.IsNil {
			return errors.New("checkpoint projection has invalid rerun tool calls coordinates")
		}
	case infoTargetContextToolCalls:
		if ref.ContextIndex < 0 || ref.RerunExtraKey != "" || ref.MessageIndex != -1 ||
			ref.TargetLength != 0 || ref.IsNil {
			return errors.New("checkpoint projection has invalid context tool calls coordinates")
		}
	default:
		return fmt.Errorf("checkpoint projection has unsupported interrupt info target %q", ref.Target)
	}
	return nil
}

func hydrateRunContextMessages(runCtx *runContext, refs []runCtxMessageProjectionV1,
	expectedCount int, index *checkpointProjectionIndex) error {
	if err := validateRunCtxProjectionRefs(refs, expectedCount); err != nil {
		return err
	}
	for _, ref := range refs {
		var err error
		switch ref.Target {
		case runCtxTargetRootInput:
			err = hydrateRunCtxRootInput(runCtx, ref, ref.TargetLength, index)
		case runCtxTargetEvent:
			err = hydrateRunCtxEvent(runCtx, ref, index)
		case runCtxTargetLaneEvent:
			err = hydrateRunCtxLaneEvent(runCtx, ref, index)
		case runCtxTargetAgenticRootInput:
			err = hydrateRunCtxAgenticRootInput(runCtx, ref, ref.TargetLength, index)
		case runCtxTargetTypedEvent:
			err = hydrateRunCtxTypedEvent(runCtx, ref, index)
		default:
			return fmt.Errorf("checkpoint projection has unsupported run context target %q", ref.Target)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func hydrateRunCtxRootInput(runCtx *runContext, ref runCtxMessageProjectionV1,
	targetLength int, index *checkpointProjectionIndex) error {
	message, err := projectedSchemaMessage(ref.Source, ref.Inline, ref.IsNil, index)
	if err != nil {
		return err
	}
	if runCtx == nil || runCtx.RootInput == nil || ref.Index < 0 {
		return fmt.Errorf("checkpoint projection has invalid root input target %d", ref.Index)
	}
	if runCtx.RootInput.Messages == nil {
		runCtx.RootInput.Messages = make([]*schema.Message, targetLength)
	}
	if ref.Index >= len(runCtx.RootInput.Messages) || runCtx.RootInput.Messages[ref.Index] != nil {
		return fmt.Errorf("checkpoint projection has invalid root input target %d", ref.Index)
	}
	runCtx.RootInput.Messages[ref.Index] = message
	return nil
}

func hydrateRunCtxEvent(runCtx *runContext, ref runCtxMessageProjectionV1,
	index *checkpointProjectionIndex) error {
	message, err := projectedSchemaMessage(ref.Source, ref.Inline, ref.IsNil, index)
	if err != nil {
		return err
	}
	if runCtx == nil || runCtx.Session == nil || ref.Index < 0 ||
		ref.Index >= len(runCtx.Session.Events) {
		return fmt.Errorf("checkpoint projection has invalid event target %d", ref.Index)
	}
	return hydrateAgentEventMessage(runCtx.Session.Events[ref.Index], message, ref.WasStreaming)
}

func hydrateRunCtxLaneEvent(runCtx *runContext, ref runCtxMessageProjectionV1,
	index *checkpointProjectionIndex) error {
	message, err := projectedSchemaMessage(ref.Source, ref.Inline, ref.IsNil, index)
	if err != nil {
		return err
	}
	if runCtx == nil || runCtx.Session == nil {
		return errors.New("checkpoint projection lane event session is missing")
	}
	lane := runCtx.Session.LaneEvents
	for depth := 0; depth < ref.LaneDepth && lane != nil; depth++ {
		lane = lane.Parent
	}
	if lane == nil || ref.Index < 0 || ref.Index >= len(lane.Events) {
		return fmt.Errorf("checkpoint projection has invalid lane event target %d/%d",
			ref.LaneDepth, ref.Index)
	}
	return hydrateAgentEventMessage(lane.Events[ref.Index], message, ref.WasStreaming)
}

func hydrateRunCtxAgenticRootInput(runCtx *runContext, ref runCtxMessageProjectionV1,
	targetLength int, index *checkpointProjectionIndex) error {
	message, err := projectedAgenticMessage(ref.Source, ref.AgenticInline, ref.IsNil, index)
	if err != nil {
		return err
	}
	if runCtx == nil {
		return fmt.Errorf("checkpoint projection has invalid agentic root input target %d", ref.Index)
	}
	rootInput, ok := runCtx.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage])
	if !ok || rootInput == nil || ref.Index < 0 {
		return fmt.Errorf("checkpoint projection has invalid agentic root input target %d", ref.Index)
	}
	if rootInput.Messages == nil {
		rootInput.Messages = make([]*schema.AgenticMessage, targetLength)
	}
	if ref.Index >= len(rootInput.Messages) || rootInput.Messages[ref.Index] != nil {
		return fmt.Errorf("checkpoint projection has invalid agentic root input target %d", ref.Index)
	}
	rootInput.Messages[ref.Index] = message
	return nil
}

func hydrateRunCtxTypedEvent(runCtx *runContext, ref runCtxMessageProjectionV1,
	index *checkpointProjectionIndex) error {
	message, err := projectedAgenticMessage(ref.Source, ref.AgenticInline, ref.IsNil, index)
	if err != nil {
		return err
	}
	if runCtx == nil || runCtx.Session == nil {
		return errors.New("checkpoint projection typed event session is missing")
	}
	events, ok := runCtx.Session.TypedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage])
	if !ok || events == nil || ref.Index < 0 || ref.Index >= len(*events) {
		return fmt.Errorf("checkpoint projection has invalid typed event target %d", ref.Index)
	}
	return hydrateTypedAgentEventMessage((*events)[ref.Index], message, ref.WasStreaming)
}

func hydrateTypedAgentEventMessage(event *typedAgentEventWrapper[*schema.AgenticMessage],
	message *schema.AgenticMessage, wasStreaming bool) error {
	if event == nil || event.event == nil || event.event.Output == nil ||
		event.event.Output.MessageOutput == nil ||
		event.event.Output.MessageOutput.Message != nil ||
		event.event.Output.MessageOutput.MessageStream != nil {
		return errors.New("checkpoint projection has invalid typed event message target")
	}
	if wasStreaming {
		event.event.Output.MessageOutput.IsStreaming = true
		event.event.Output.MessageOutput.MessageStream =
			schema.StreamReaderFromArray([]*schema.AgenticMessage{message})
		event.concatenatedMessage = message
		return nil
	}
	event.event.Output.MessageOutput.Message = message
	return nil
}

func hydrateAgentEventMessage(event *agentEventWrapper, message *schema.Message, wasStreaming bool) error {
	if event == nil || event.AgentEvent == nil || event.Output == nil || event.Output.MessageOutput == nil ||
		event.Output.MessageOutput.Message != nil || event.Output.MessageOutput.MessageStream != nil {
		return errors.New("checkpoint projection has invalid event message target")
	}
	if wasStreaming {
		event.Output.MessageOutput.IsStreaming = true
		event.Output.MessageOutput.MessageStream = schema.StreamReaderFromArray([]*schema.Message{message})
		event.concatenatedMessage = message
		return nil
	}
	event.Output.MessageOutput.Message = message
	return nil
}

func hydrateInterruptInfoMessages(info *InterruptInfo, refs []infoMessageProjectionV1,
	expectedCount int, index *checkpointProjectionIndex) error {
	if err := validateInfoProjectionRefs(refs, expectedCount); err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}
	if info == nil {
		return errors.New("checkpoint projection interrupt info is missing")
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil || chatModelInfo.Info == nil {
		return fmt.Errorf("checkpoint projection interrupt info has invalid type %T", info.Data)
	}
	if err := hydrateComposeInterruptInfoRefs(chatModelInfo.Info, refs, index); err != nil {
		return err
	}
	return nil
}

func hydrateInterruptInfoContextPrefixes(info *InterruptInfo,
	index *checkpointProjectionIndex) error {
	return hydrateInterruptInfoContextPrefixesWithValidation(info, index, true)
}

func validateInterruptInfoContextReferences(info *InterruptInfo,
	index *checkpointProjectionIndex) error {
	if info == nil {
		return nil
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil || chatModelInfo.Info == nil {
		return nil
	}
	return validateNestedInterruptInfoContextReferences(chatModelInfo.Info, index)
}

func validateNestedInterruptInfoContextReferences(info *compose.InterruptInfo,
	index *checkpointProjectionIndex) error {
	if info == nil {
		return nil
	}
	if err := validateInfoValueContextReferences(info.State, index); err != nil {
		return err
	}
	for _, key := range sortedStringKeys(info.RerunNodesExtra) {
		if err := validateInfoValueContextReferences(info.RerunNodesExtra[key], index); err != nil {
			return err
		}
	}
	for _, interruptCtx := range info.InterruptContexts {
		for depth, current := 0, interruptCtx; current != nil; depth, current = depth+1, current.Parent {
			if _, ok := current.Info.(*checkpointInterruptContextPlaceholderV1); ok {
				if depth > 0 {
					return errors.New(
						"checkpoint projection interrupt context reference must be at the chain head")
				}
				if err := validateInterruptContextReference(current, index); err != nil {
					return err
				}
			}
			if err := validateInfoValueContextReferences(current.Info, index); err != nil {
				return err
			}
		}
	}
	for _, key := range sortedStringKeys(info.SubGraphs) {
		if err := validateNestedInterruptInfoContextReferences(info.SubGraphs[key], index); err != nil {
			return err
		}
	}
	return nil
}

func validateInfoValueContextReferences(value any, index *checkpointProjectionIndex) error {
	switch value := value.(type) {
	case *compose.InterruptInfo:
		return validateNestedInterruptInfoContextReferences(value, index)
	case *checkpointInterruptInfoPlaceholderV1:
		if value != nil {
			return validateNestedInterruptInfoContextReferences(value.Info, index)
		}
	}
	return nil
}

func hydrateInterruptInfoContextPrefixesAfterValidation(info *InterruptInfo,
	index *checkpointProjectionIndex) error {
	return hydrateInterruptInfoContextPrefixesWithValidation(info, index, false)
}

func hydrateInterruptInfoContextPrefixesWithValidation(info *InterruptInfo,
	index *checkpointProjectionIndex, validateIntegrity bool) error {
	if info == nil {
		return nil
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil || chatModelInfo.Info == nil {
		return nil
	}
	return hydrateNestedInterruptInfoPlaceholdersWithValidation(
		chatModelInfo.Info, index, validateIntegrity)
}

func hydrateComposeInterruptInfoRefs(info *compose.InterruptInfo, refs []infoMessageProjectionV1,
	index *checkpointProjectionIndex) error {
	for _, ref := range refs {
		if err := validateComposeInterruptInfoRefPayload(info, ref); err != nil {
			return err
		}
	}
	for _, ref := range refs {
		targetInfo, err := composeInterruptInfoAtPath(info, ref.SubGraphPath)
		if err != nil {
			return err
		}
		switch ref.Target {
		case infoTargetStateMessage:
			if err = hydrateInfoStateMessage(targetInfo.State, ref, ref.TargetLength, index); err != nil {
				return err
			}
		case infoTargetContextStateMessage, infoTargetContextToolCalls:
			contextInfo, err := interruptContextAt(targetInfo, ref.ContextIndex, ref.ParentDepth)
			if err != nil {
				return err
			}
			if ref.Target == infoTargetContextStateMessage {
				if err = hydrateInfoStateMessage(contextInfo.Info, ref, ref.TargetLength, index); err != nil {
					return err
				}
			} else {
				message, err := projectedSchemaMessage(ref.Source, ref.Inline, ref.IsNil, index)
				if err != nil {
					return err
				}
				if message == nil {
					return errors.New("checkpoint projection context tool calls source is nil")
				}
				extra, ok := contextInfo.Info.(*compose.ToolsInterruptAndRerunExtra)
				if !ok || extra == nil || extra.ToolCalls != nil {
					return errors.New("checkpoint projection has invalid context tool calls target")
				}
				extra.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
			}
		case infoTargetRerunToolCalls:
			message, err := projectedSchemaMessage(ref.Source, ref.Inline, ref.IsNil, index)
			if err != nil {
				return err
			}
			if message == nil {
				return errors.New("checkpoint projection rerun tool calls source is nil")
			}
			value, ok := targetInfo.RerunNodesExtra[ref.RerunExtraKey]
			extra, typeOK := value.(*compose.ToolsInterruptAndRerunExtra)
			if !ok || !typeOK || extra == nil || extra.ToolCalls != nil {
				return errors.New("checkpoint projection has invalid rerun tool calls target")
			}
			extra.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
		default:
			return fmt.Errorf("checkpoint projection has unsupported interrupt info target %q", ref.Target)
		}
	}
	return nil
}

func validateComposeInterruptInfoRefPayload(info *compose.InterruptInfo,
	ref infoMessageProjectionV1) error {
	targetInfo, err := composeInterruptInfoAtPath(info, ref.SubGraphPath)
	if err != nil {
		return err
	}
	switch ref.Target {
	case infoTargetStateMessage:
		return validateInfoStateMessagePayload(targetInfo.State, ref)
	case infoTargetContextStateMessage, infoTargetContextToolCalls:
		contextInfo, err := interruptContextAt(targetInfo, ref.ContextIndex, ref.ParentDepth)
		if err != nil {
			return err
		}
		if ref.Target == infoTargetContextStateMessage {
			return validateInfoStateMessagePayload(contextInfo.Info, ref)
		}
		return validateProjectionMessagePayload(
			ref.Source, ref.Inline, ref.AgenticInline, ref.IsNil, projectionMessageKindSchema)
	case infoTargetRerunToolCalls:
		return validateProjectionMessagePayload(
			ref.Source, ref.Inline, ref.AgenticInline, ref.IsNil, projectionMessageKindSchema)
	default:
		return fmt.Errorf("checkpoint projection has unsupported interrupt info target %q", ref.Target)
	}
}

func validateInfoStateMessagePayload(target any, ref infoMessageProjectionV1) error {
	switch target.(type) {
	case *State:
		return validateProjectionMessagePayload(
			ref.Source, ref.Inline, ref.AgenticInline, ref.IsNil, projectionMessageKindSchema)
	case *agenticState:
		return validateProjectionMessagePayload(
			ref.Source, ref.Inline, ref.AgenticInline, ref.IsNil, projectionMessageKindAgentic)
	default:
		return fmt.Errorf("checkpoint projection has invalid state message target type %T", target)
	}
}

func hydrateInfoStateMessage(target any, ref infoMessageProjectionV1,
	targetLength int, index *checkpointProjectionIndex) error {
	if err := validateInfoStateMessagePayload(target, ref); err != nil {
		return err
	}
	switch state := target.(type) {
	case *State:
		message, err := projectedSchemaMessage(ref.Source, ref.Inline, ref.IsNil, index)
		if err != nil {
			return err
		}
		if state == nil || ref.MessageIndex < 0 {
			return errors.New("checkpoint projection has invalid state message target")
		}
		if state.Messages == nil {
			state.Messages = make([]*schema.Message, targetLength)
		}
		if ref.MessageIndex >= len(state.Messages) || state.Messages[ref.MessageIndex] != nil {
			return errors.New("checkpoint projection has invalid state message target")
		}
		state.Messages[ref.MessageIndex] = message
		return nil
	case *agenticState:
		message, err := projectedAgenticMessage(ref.Source, ref.AgenticInline, ref.IsNil, index)
		if err != nil {
			return err
		}
		if state == nil || ref.MessageIndex < 0 {
			return errors.New("checkpoint projection has invalid agentic state message target")
		}
		if state.Messages == nil {
			state.Messages = make([]*schema.AgenticMessage, targetLength)
		}
		if ref.MessageIndex >= len(state.Messages) || state.Messages[ref.MessageIndex] != nil {
			return errors.New("checkpoint projection has invalid agentic state message target")
		}
		state.Messages[ref.MessageIndex] = message
		return nil
	default:
		return fmt.Errorf("checkpoint projection has invalid state message target type %T", target)
	}
}

func projectedSchemaMessage(source checkpointMessageSourceV1, inline *schema.Message, isNil bool,
	index *checkpointProjectionIndex) (*schema.Message, error) {
	if isNil {
		if source.MessageID != "" || inline != nil {
			return nil, errors.New("checkpoint projection nil message has payload")
		}
		return nil, nil
	}
	if source.MessageID == "" {
		if inline == nil {
			return nil, errors.New("checkpoint projection inline message is missing")
		}
		return cloneSchemaMessageForProjection(inline)
	}
	if inline != nil {
		return nil, errors.New("checkpoint projection message has both inline data and a source reference")
	}
	return index.schemaMessage(source)
}

func projectedAgenticMessage(source checkpointMessageSourceV1, inline *schema.AgenticMessage, isNil bool,
	index *checkpointProjectionIndex) (*schema.AgenticMessage, error) {
	if isNil {
		if source.MessageID != "" || inline != nil {
			return nil, errors.New("checkpoint projection nil agentic message has payload")
		}
		return nil, nil
	}
	if source.MessageID == "" {
		if inline == nil {
			return nil, errors.New("checkpoint projection inline agentic message is missing")
		}
		return cloneAgenticMessageForProjection(inline)
	}
	if inline != nil {
		return nil, errors.New("checkpoint projection agentic message has both inline data and a source reference")
	}
	return index.agenticMessage(source)
}

func cloneSchemaMessageForProjection(message *schema.Message) (*schema.Message, error) {
	if message == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(message); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint message: %w", err)
	}
	var cloned schema.Message
	if err := gob.NewDecoder(&buf).Decode(&cloned); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint message: %w", err)
	}
	return &cloned, nil
}

func cloneAgenticMessageForProjection(message *schema.AgenticMessage) (*schema.AgenticMessage, error) {
	if message == nil {
		return nil, nil
	}
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(message); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint agentic message: %w", err)
	}
	var cloned schema.AgenticMessage
	if err := gob.NewDecoder(&buf).Decode(&cloned); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint agentic message: %w", err)
	}
	return &cloned, nil
}

func hydrateNestedInterruptInfoPlaceholders(info *compose.InterruptInfo,
	index *checkpointProjectionIndex) error {
	return hydrateNestedInterruptInfoPlaceholdersWithValidation(info, index, true)
}

func hydrateNestedInterruptInfoPlaceholdersWithValidation(info *compose.InterruptInfo,
	index *checkpointProjectionIndex, validateIntegrity bool) error {
	if info == nil {
		return nil
	}
	hydratedState, err := hydrateProjectionInfoValueWithValidation(
		info.State, index, validateIntegrity)
	if err != nil {
		return err
	}
	info.State = hydratedState
	for _, key := range sortedStringKeys(info.RerunNodesExtra) {
		value := info.RerunNodesExtra[key]
		hydrated, err := hydrateProjectionInfoValueWithValidation(
			value, index, validateIntegrity)
		if err != nil {
			return err
		}
		info.RerunNodesExtra[key] = hydrated
	}
	for i, interruptCtx := range info.InterruptContexts {
		hydrated, err := hydrateInterruptContextPrefixWithValidation(
			interruptCtx, index, validateIntegrity)
		if err != nil {
			return err
		}
		info.InterruptContexts[i] = hydrated
		for depth, current := 0, hydrated; current != nil; depth, current = depth+1, current.Parent {
			if depth > 0 {
				if _, ok := current.Info.(*checkpointInterruptContextPlaceholderV1); ok {
					return errors.New(
						"checkpoint projection interrupt context reference must be at the chain head")
				}
			}
			hydrated, err := hydrateProjectionInfoValueWithValidation(
				current.Info, index, validateIntegrity)
			if err != nil {
				return err
			}
			current.Info = hydrated
		}
	}
	for _, key := range sortedStringKeys(info.SubGraphs) {
		if err := hydrateNestedInterruptInfoPlaceholdersWithValidation(
			info.SubGraphs[key], index, validateIntegrity); err != nil {
			return err
		}
	}
	return nil
}

func hydrateProjectionInfoValue(value any, index *checkpointProjectionIndex) (any, error) {
	return hydrateProjectionInfoValueWithValidation(value, index, true)
}

func hydrateProjectionInfoValueWithValidation(value any, index *checkpointProjectionIndex,
	validateIntegrity bool) (any, error) {
	switch value := value.(type) {
	case *compose.InterruptInfo:
		if err := hydrateNestedInterruptInfoPlaceholdersWithValidation(
			value, index, validateIntegrity); err != nil {
			return nil, err
		}
		return value, nil
	case *checkpointInterruptInfoPlaceholderV1:
		if value == nil || value.Info == nil {
			return nil, errors.New("checkpoint projection contains a nil interrupt info reference")
		}
		if err := validateInfoProjectionRefs(value.Refs, value.RefCount); err != nil {
			return nil, err
		}
		if err := hydrateComposeInterruptInfoRefs(value.Info, value.Refs, index); err != nil {
			return nil, err
		}
		if err := hydrateComposeInterruptInfoToolResults(value.Info,
			value.ToolResultRefs, value.ToolResultRefCount, index); err != nil {
			return nil, err
		}
		if err := hydrateNestedInterruptInfoPlaceholdersWithValidation(
			value.Info, index, validateIntegrity); err != nil {
			return nil, err
		}
		return value.Info, nil
	case *checkpointInterruptContextPlaceholderV1:
		return nil, errors.New(
			"checkpoint projection interrupt context reference is outside a context chain")
	default:
		return value, nil
	}
}

func hydrateInterruptContextPrefix(interruptCtx *InterruptCtx,
	index *checkpointProjectionIndex) (*InterruptCtx, error) {
	return hydrateInterruptContextPrefixWithValidation(interruptCtx, index, true)
}

func hydrateInterruptContextPrefixWithValidation(interruptCtx *InterruptCtx,
	index *checkpointProjectionIndex, validateIntegrity bool) (*InterruptCtx, error) {
	if interruptCtx == nil {
		return nil, nil
	}
	placeholder, ok := interruptCtx.Info.(*checkpointInterruptContextPlaceholderV1)
	if !ok {
		return interruptCtx, nil
	}
	if placeholder == nil || placeholder.ContextIndex < 0 || placeholder.PrefixLength <= 0 {
		return nil, errors.New("checkpoint projection has invalid interrupt context reference")
	}
	if interruptCtx.ID != "" || interruptCtx.Address != nil || interruptCtx.IsRootCause {
		return nil, errors.New("checkpoint projection interrupt context reference has inline data")
	}
	if err := index.validateInterruptContextReferenceMetadata(placeholder); err != nil {
		return nil, err
	}
	if validateIntegrity {
		if err := validateInterruptContextReference(interruptCtx, index); err != nil {
			return nil, err
		}
	}
	source, err := index.interruptContext(placeholder)
	if err != nil {
		return nil, err
	}
	return cloneInterruptContextPrefix(
		source, placeholder.PrefixLength, placeholder.AddressPrefix, interruptCtx.Parent)
}

func validateInterruptContextReference(interruptCtx *InterruptCtx,
	index *checkpointProjectionIndex) error {
	if interruptCtx == nil {
		return errors.New("checkpoint projection has invalid interrupt context reference")
	}
	placeholder, ok := interruptCtx.Info.(*checkpointInterruptContextPlaceholderV1)
	if !ok || placeholder == nil || placeholder.ContextIndex < 0 ||
		placeholder.PrefixLength <= 0 {
		return errors.New("checkpoint projection has invalid interrupt context reference")
	}
	if interruptCtx.ID != "" || interruptCtx.Address != nil || interruptCtx.IsRootCause {
		return errors.New("checkpoint projection interrupt context reference has inline data")
	}
	if err := index.validateInterruptContextReferenceMetadata(placeholder); err != nil {
		return err
	}
	if index.version == checkpointProjectionVersionV2 {
		if placeholder.IntegrityDigest == "" {
			return errors.New(
				"checkpoint projection V2 interrupt context integrity metadata is incomplete")
		}
		integrityDigest, ok := checkpointInterruptContextReferenceIntegrity(
			placeholder, interruptCtx.Parent)
		if !ok || integrityDigest != placeholder.IntegrityDigest {
			return errors.New(
				"checkpoint projection interrupt context reference does not match integrity metadata")
		}
	}
	_, err := index.interruptContext(placeholder)
	return err
}

func (i *checkpointProjectionIndex) interruptContext(
	ref *checkpointInterruptContextPlaceholderV1) (*InterruptCtx, error) {
	if ref == nil {
		return nil, errors.New("checkpoint projection has invalid interrupt context reference")
	}
	if err := i.validateInterruptContextReferenceMetadata(ref); err != nil {
		return nil, err
	}
	hasSourceID := ref.SourceID != ""
	if i.version == checkpointProjectionVersionV1 {
		var matched *canonicalCheckpointInterruptInfo
		for _, candidate := range i.allInterruptInfos() {
			if !checkpointProjectionPathEqual(candidate.path, ref.RunnerPath) {
				continue
			}
			if matched != nil {
				return nil, fmt.Errorf(
					"checkpoint projection interrupt context source path %v is ambiguous",
					ref.RunnerPath)
			}
			candidateCopy := candidate
			matched = &candidateCopy
		}
		if matched == nil {
			return nil, fmt.Errorf(
				"checkpoint projection interrupt context source path %v is missing",
				ref.RunnerPath)
		}
		return checkpointInterruptContextSource(*matched, ref, hasSourceID)
	}
	candidate, ok := i.interruptInfoByOrdinal(ref.SourceOrdinal)
	if !ok {
		return nil, fmt.Errorf(
			"checkpoint projection interrupt context source ordinal %d is missing",
			ref.SourceOrdinal)
	}
	return checkpointInterruptContextSource(candidate, ref, true)
}

func (i *checkpointProjectionIndex) validateInterruptContextReferenceMetadata(
	ref *checkpointInterruptContextPlaceholderV1) error {
	if ref.SourceOrdinal < 0 {
		return errors.New("checkpoint projection interrupt context source ordinal is negative")
	}
	hasSourceID := ref.SourceID != ""
	hasDigest := ref.Digest != ""
	if hasSourceID != hasDigest {
		return errors.New(
			"checkpoint projection interrupt context source metadata is incomplete")
	}
	if i.version == checkpointProjectionVersionV1 {
		if ref.SourceOrdinal != 0 {
			return errors.New(
				"checkpoint projection V1 interrupt context source uses an ordinal")
		}
		if len(ref.RunnerPath) == 0 {
			return errors.New(
				"checkpoint projection V1 interrupt context source metadata is incomplete")
		}
		return nil
	}
	if ref.SourceOrdinal == 0 {
		if len(ref.RunnerPath) != 0 {
			return errors.New(
				"checkpoint projection V2 interrupt context source contains V1 metadata")
		}
		return errors.New(
			"checkpoint projection V2 interrupt context source metadata is incomplete")
	}
	if !hasSourceID {
		return errors.New(
			"checkpoint projection V2 interrupt context source metadata is incomplete")
	}
	if len(ref.RunnerPath) != 0 {
		return errors.New(
			"checkpoint projection V2 interrupt context source contains V1 metadata")
	}
	return nil
}

func checkpointInterruptContextSource(candidate canonicalCheckpointInterruptInfo,
	ref *checkpointInterruptContextPlaceholderV1, validateMetadata bool) (*InterruptCtx, error) {
	source, err := interruptContextSourceAt(candidate, ref.ContextIndex)
	if err != nil {
		return nil, err
	}
	if !validateMetadata {
		return source, nil
	}
	sourceID, digest, ok := checkpointInterruptContextSourceMetadata(candidate, ref.ContextIndex)
	if !ok || sourceID != ref.SourceID || digest != ref.Digest {
		return nil, errors.New(
			"checkpoint projection interrupt context source does not match metadata")
	}
	return source, nil
}

func interruptContextSourceAt(candidate canonicalCheckpointInterruptInfo,
	contextIndex int) (*InterruptCtx, error) {
	if contextIndex < 0 || contextIndex >= len(candidate.contexts) {
		return nil, fmt.Errorf(
			"checkpoint projection interrupt context source index %d is invalid", contextIndex)
	}
	source := candidate.contexts[contextIndex]
	if source == nil {
		return nil, errors.New("checkpoint projection interrupt context source is nil")
	}
	return source, nil
}

func cloneInterruptContextPrefix(source *InterruptCtx, prefixLength int, addressPrefix Address,
	tail *InterruptCtx) (*InterruptCtx, error) {
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(source); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint interrupt context: %w", err)
	}
	var cloned InterruptCtx
	if err := gob.NewDecoder(&buf).Decode(&cloned); err != nil {
		return nil, fmt.Errorf("failed to clone checkpoint interrupt context: %w", err)
	}
	current := &cloned
	for step := 0; step < prefixLength; step++ {
		current.Address = append(cloneSlice(addressPrefix), current.Address...)
		if step == prefixLength-1 {
			break
		}
		if current.Parent == nil {
			return nil, errors.New("checkpoint projection interrupt context source is shorter than its prefix")
		}
		current = current.Parent
	}
	current.Parent = tail
	return &cloned, nil
}

func countInterruptContextRefs(info *InterruptInfo) int {
	if info == nil {
		return 0
	}
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil {
		return 0
	}
	return countComposeInterruptContextRefs(chatModelInfo.Info)
}

func countComposeInterruptContextRefs(info *compose.InterruptInfo) int {
	if info == nil {
		return 0
	}
	count := 0
	for _, interruptCtx := range info.InterruptContexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			if _, ok := current.Info.(*checkpointInterruptContextPlaceholderV1); ok {
				count++
			}
			count += countProjectionInfoValueContextRefs(current.Info)
		}
	}
	count += countProjectionInfoValueContextRefs(info.State)
	for _, value := range info.RerunNodesExtra {
		count += countProjectionInfoValueContextRefs(value)
	}
	for _, subGraph := range info.SubGraphs {
		count += countComposeInterruptContextRefs(subGraph)
	}
	return count
}

func countProjectionInfoValueContextRefs(value any) int {
	switch value := value.(type) {
	case *checkpointInterruptInfoPlaceholderV1:
		if value == nil {
			return 0
		}
		return countComposeInterruptContextRefs(value.Info)
	case *compose.InterruptInfo:
		return countComposeInterruptContextRefs(value)
	default:
		return 0
	}
}

func runCtxProjectionTargetKey(target string, laneDepth int) string {
	return fmt.Sprintf("%s/%d", target, laneDepth)
}

func infoProjectionTargetKey(target infoMessageProjectionV1) string {
	return fmt.Sprintf("%s/%q/%d/%d/%s", target.Target, target.SubGraphPath,
		target.ContextIndex, target.ParentDepth, target.RerunExtraKey)
}

func composeInterruptInfoAtPath(info *compose.InterruptInfo,
	path []string) (*compose.InterruptInfo, error) {
	current := info
	for _, key := range path {
		if current == nil {
			return nil, fmt.Errorf("checkpoint projection interrupt info path %v is missing", path)
		}
		current = current.SubGraphs[key]
	}
	if current == nil {
		return nil, fmt.Errorf("checkpoint projection interrupt info path %v is missing", path)
	}
	return current, nil
}

func interruptContextAt(info *compose.InterruptInfo, index, parentDepth int) (*InterruptCtx, error) {
	if index < 0 || index >= len(info.InterruptContexts) {
		return nil, fmt.Errorf("checkpoint projection interrupt context index %d is invalid", index)
	}
	current := info.InterruptContexts[index]
	for depth := 0; depth < parentDepth && current != nil; depth++ {
		current = current.Parent
	}
	if current == nil {
		return nil, fmt.Errorf("checkpoint projection interrupt context parent depth %d is invalid", parentDepth)
	}
	return current, nil
}
