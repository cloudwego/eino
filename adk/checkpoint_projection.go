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
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

const (
	checkpointProjectionVersionV1 = 1
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

type runnerProjectionSentinelV1 struct {
	Version int
}

// checkpointMessageSourceV1 identifies one canonical message by kind, graph
// path, state index, stable message ID, and content digest.
type checkpointMessageSourceV1 struct {
	Kind      string
	GraphPath []string
	Index     int
	MessageID string
	Digest    string
}

// runCtxMessageProjectionV1 stores either Source, Inline, or an explicit nil.
// TargetLength applies only to root-input slices; LaneDepth applies only to
// lane events.
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

// infoMessageProjectionV1 stores either Source, an inline value, or an
// explicit nil. The target determines which coordinate fields are applicable.
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

// checkpointProjectionV1 is persisted in serialization. RefCount fields make
// truncation detectable before hydration mutates any logical checkpoint owner.
type checkpointProjectionV1 struct {
	Version            int
	SourceInterruptID  string
	RunCtxRefCount     int
	InfoRefCount       int
	ToolResultRefCount int
	RunCtxRefs         []runCtxMessageProjectionV1
	InfoRefs           []infoMessageProjectionV1
	ToolResultRefs     []infoToolResultProjectionV1
}

type checkpointMessagePlaceholderV1 struct {
	Source checkpointMessageSourceV1
}

type checkpointMessageSliceEntryV1 struct {
	Inline *schema.Message
	Source *checkpointMessageSourceV1
	IsNil  bool
}

type checkpointMessageSlicePlaceholderV1 struct {
	Entries []checkpointMessageSliceEntryV1
}

type checkpointAgenticMessagePlaceholderV1 struct {
	Source checkpointMessageSourceV1
}

type checkpointAgenticMessageSliceEntryV1 struct {
	Inline *schema.AgenticMessage
	Source *checkpointMessageSourceV1
	IsNil  bool
}

type checkpointAgenticMessageSlicePlaceholderV1 struct {
	Entries []checkpointAgenticMessageSliceEntryV1
}

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

type checkpointProjectionIndex struct {
	byID                map[string][]canonicalCheckpointMessage
	toolResultsByCallID map[string][]canonicalCheckpointToolResult
}

func init() {
	schema.RegisterName[*checkpointProjectionV1]("_eino_adk_checkpoint_projection_v1")
	schema.RegisterName[*runnerProjectionSentinelV1]("_eino_adk_runner_projection_v1")
	schema.RegisterName[*checkpointMessagePlaceholderV1]("_eino_adk_checkpoint_message_ref_v1")
	schema.RegisterName[*checkpointMessageSlicePlaceholderV1]("_eino_adk_checkpoint_message_slice_ref_v1")
	schema.RegisterName[*checkpointAgenticMessagePlaceholderV1]("_eino_adk_checkpoint_agentic_message_ref_v1")
	schema.RegisterName[*checkpointAgenticMessageSlicePlaceholderV1]("_eino_adk_checkpoint_agentic_message_slice_ref_v1")
	schema.RegisterName[*checkpointInterruptInfoPlaceholderV1]("_eino_adk_checkpoint_interrupt_info_ref_v1")
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
		Version:           checkpointProjectionVersionV1,
		SourceInterruptID: sourceID,
	}
	projectRunContextMessages(projectedRunCtx, index, projection)
	projectInterruptInfoMessages(projectedInfo, index, projection)
	projection.RunCtxRefCount = len(projection.RunCtxRefs)
	projection.InfoRefCount = len(projection.InfoRefs)
	projection.ToolResultRefCount = len(projection.ToolResultRefs)

	projectedCompose, composeChanged, err := projectComposeCheckpointValues(sourceData, index)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if len(projection.RunCtxRefs) == 0 && len(projection.InfoRefs) == 0 &&
		len(projection.ToolResultRefs) == 0 && !composeChanged {
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
		State: &runnerProjectionSentinelV1{Version: checkpointProjectionVersionV1},
	}
	return projectedRunCtx, projectedInfo, projectedStates, projection, nil
}

func restoreRunnerCheckpointProjection(s *serialization) error {
	if err := validateRunnerProjectionMetadata(s); err != nil {
		return err
	}
	if s.ProjectionV1 == nil {
		return nil
	}

	projection := s.ProjectionV1
	if projection.RunCtxRefCount != len(projection.RunCtxRefs) ||
		projection.InfoRefCount != len(projection.InfoRefs) ||
		projection.ToolResultRefCount != len(projection.ToolResultRefs) {
		return errors.New("failed to decode checkpoint projection: reference count mismatch")
	}
	sourceState, ok := s.InterruptID2State[projection.SourceInterruptID]
	if !ok {
		return fmt.Errorf("failed to decode checkpoint projection: source interrupt state %q is missing",
			projection.SourceInterruptID)
	}
	sourceData, ok := sourceState.State.([]byte)
	if !ok {
		return fmt.Errorf("failed to decode checkpoint projection: source interrupt state %q has invalid type %T",
			projection.SourceInterruptID, sourceState.State)
	}
	index, err := buildCheckpointProjectionIndex(sourceData)
	if err != nil {
		return fmt.Errorf("failed to decode checkpoint projection source: %w", err)
	}
	restoredCompose, err := hydrateComposeCheckpointValues(sourceData, index)
	if err != nil {
		return err
	}
	sourceState.State = restoredCompose
	s.InterruptID2State[projection.SourceInterruptID] = sourceState

	if err = hydrateRunContextMessages(s.RunCtx, projection.RunCtxRefs,
		projection.RunCtxRefCount, index); err != nil {
		return err
	}
	if err = hydrateInterruptInfoMessages(s.Info, projection.InfoRefs,
		projection.InfoRefCount, index); err != nil {
		return err
	}
	if err = hydrateInterruptInfoToolResults(s.Info, projection.ToolResultRefs,
		projection.ToolResultRefCount, index); err != nil {
		return err
	}
	delete(s.InterruptID2State, runnerProjectionSentinelID)
	return nil
}

func validateRunnerProjectionMetadata(s *serialization) error {
	if s == nil {
		return nil
	}
	sentinelState, hasSentinel := s.InterruptID2State[runnerProjectionSentinelID]
	if s.ProjectionV1 == nil {
		if hasSentinel {
			return errors.New("failed to decode checkpoint projection: metadata is missing")
		}
		return nil
	}
	if s.ProjectionV1.Version != checkpointProjectionVersionV1 {
		return fmt.Errorf("checkpoint requires a newer Eino version: unsupported projection version %d",
			s.ProjectionV1.Version)
	}
	if !hasSentinel {
		return errors.New("failed to decode checkpoint projection: sentinel is missing")
	}
	if _, exists := s.InterruptID2Address[runnerProjectionSentinelID]; exists {
		return errors.New("failed to decode checkpoint projection: sentinel must not have a routing address")
	}
	sentinel, ok := sentinelState.State.(*runnerProjectionSentinelV1)
	if !ok || sentinel == nil || sentinel.Version != checkpointProjectionVersionV1 {
		return fmt.Errorf("failed to decode checkpoint projection: invalid sentinel %T", sentinelState.State)
	}
	return nil
}

func validateRunnerProjectionReservedIDs(id2Address map[string]Address,
	id2State map[string]core.InterruptState) error {
	for _, id := range sortedStringKeys(id2Address) {
		if strings.HasPrefix(id, "_eino_") {
			return fmt.Errorf("interrupt ID %q uses reserved checkpoint metadata prefix", id)
		}
	}
	for _, id := range sortedStringKeys(id2State) {
		if strings.HasPrefix(id, "_eino_") {
			return fmt.Errorf("interrupt ID %q uses reserved checkpoint metadata prefix", id)
		}
	}
	return nil
}

func findProjectionSource(preferredID string, id2State map[string]core.InterruptState) (
	string, []byte, *checkpointProjectionIndex, error) {
	otherIDs := make([]string, 0, len(id2State))
	for id := range id2State {
		if id != preferredID && !strings.HasPrefix(id, "_eino_") {
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
		if len(index.byID) > 0 || len(index.toolResultsByCallID) > 0 {
			return id, data, index, nil
		}
	}
	return "", nil, nil, nil
}

func buildCheckpointProjectionIndex(data []byte) (*checkpointProjectionIndex, error) {
	index := &checkpointProjectionIndex{
		byID:                make(map[string][]canonicalCheckpointMessage),
		toolResultsByCallID: make(map[string][]canonicalCheckpointToolResult),
	}
	if err := index.addComposeCheckpoint(data, nil); err != nil {
		return nil, err
	}
	return index, nil
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
				if state, ok := value.(*agentToolInterruptStateV1); ok && state != nil {
					childPrefix := append(append([]string(nil), fullPath...), "@interrupt:"+location.Key)
					if err := i.addRunnerCheckpoint(state.BridgeCheckpoint, childPrefix); err != nil {
						return err
					}
				}
			}
			return nil
		})
}

func (i *checkpointProjectionIndex) addRunnerCheckpoint(data []byte, prefix []string) error {
	var runnerCheckpoint serialization
	if err := gob.NewDecoder(bytes.NewReader(data)).Decode(&runnerCheckpoint); err != nil {
		return fmt.Errorf("failed to decode child runner checkpoint: %w", err)
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
		if err := i.addComposeCheckpoint(composeData,
			append(append([]string(nil), prefix...), "@runner:"+id)); err != nil {
			return err
		}
	}
	return nil
}

func (i *checkpointProjectionIndex) addSchemaMessage(path []string, index int, message *schema.Message) {
	if message == nil {
		return
	}
	id := GetMessageID(message)
	digest, ok := projectionMessageDigest(message)
	if id == "" || !ok {
		return
	}
	i.byID[id] = append(i.byID[id], canonicalCheckpointMessage{
		source: checkpointMessageSourceV1{
			Kind:      projectionMessageKindSchema,
			GraphPath: append([]string(nil), path...),
			Index:     index,
			MessageID: id,
			Digest:    digest,
		},
		message: message,
	})
}

func (i *checkpointProjectionIndex) addAgenticMessage(path []string, index int,
	message *schema.AgenticMessage) {
	if message == nil {
		return
	}
	id := GetMessageID(message)
	digest, ok := projectionMessageDigest(message)
	if id == "" || !ok {
		return
	}
	i.byID[id] = append(i.byID[id], canonicalCheckpointMessage{
		source: checkpointMessageSourceV1{
			Kind:      projectionMessageKindAgentic,
			GraphPath: append([]string(nil), path...),
			Index:     index,
			MessageID: id,
			Digest:    digest,
		},
		agenticMessage: message,
	})
}

func projectionMessageDigest(message any) (string, bool) {
	data, err := json.Marshal(message)
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), true
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
	candidates := i.byID[id]
	if id == "" || len(candidates) == 0 {
		return checkpointMessageSourceV1{}, false
	}
	// A duplicate ID is usable only when every candidate is the same logical
	// message. Otherwise keeping the value inline avoids an ambiguous reference.
	for _, candidate := range candidates {
		if candidate.source.Kind != projectionMessageKindSchema ||
			!reflect.DeepEqual(candidate.message, message) {
			return checkpointMessageSourceV1{}, false
		}
	}
	return candidates[0].source, true
}

func (i *checkpointProjectionIndex) schemaMessage(
	source checkpointMessageSourceV1) (*schema.Message, error) {
	candidates := i.byID[source.MessageID]
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
	candidates := i.byID[id]
	if id == "" || len(candidates) == 0 {
		return checkpointMessageSourceV1{}, false
	}
	for _, candidate := range candidates {
		if candidate.source.Kind != projectionMessageKindAgentic ||
			!reflect.DeepEqual(candidate.agenticMessage, message) {
			return checkpointMessageSourceV1{}, false
		}
	}
	return candidates[0].source, true
}

func (i *checkpointProjectionIndex) agenticMessage(
	source checkpointMessageSourceV1) (*schema.AgenticMessage, error) {
	candidates := i.byID[source.MessageID]
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

func cloneInterruptStateMap(source map[string]core.InterruptState) map[string]core.InterruptState {
	cloned := make(map[string]core.InterruptState, len(source)+1)
	for id, state := range source {
		cloned[id] = state
	}
	return cloned
}

func cloneRunContextForCheckpointProjection(runCtx *runContext) *runContext {
	if runCtx == nil {
		return nil
	}
	cloned := &runContext{
		RunPath: append([]RunStep(nil), runCtx.RunPath...),
		Session: cloneRunSessionForCheckpointProjection(runCtx.Session),
	}
	if runCtx.RootInput != nil {
		rootInput := *runCtx.RootInput
		rootInput.Messages = append([]*schema.Message(nil), runCtx.RootInput.Messages...)
		cloned.RootInput = &rootInput
	}
	if input, ok := runCtx.AgenticRootInput.(*TypedAgentInput[*schema.AgenticMessage]); ok && input != nil {
		rootInput := *input
		rootInput.Messages = append([]*schema.AgenticMessage(nil), input.Messages...)
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
		Values:    make(map[string]any),
		valuesMtx: &sync.Mutex{},
	}
	if session.valuesMtx != nil {
		session.valuesMtx.Lock()
		for key, value := range session.Values {
			cloned.Values[key] = value
		}
		session.valuesMtx.Unlock()
	} else {
		for key, value := range session.Values {
			cloned.Values[key] = value
		}
	}

	session.mtx.Lock()
	events := append([]*agentEventWrapper(nil), session.Events...)
	typedEvents := session.TypedEvents
	session.mtx.Unlock()
	for _, event := range events {
		cloned.Events = append(cloned.Events, cloneAgentEventWrapperForProjection(event))
	}
	cloned.LaneEvents = cloneLaneEventsForProjection(session.LaneEvents)
	if typed, ok := typedEvents.(*[]*typedAgentEventWrapper[*schema.AgenticMessage]); ok && typed != nil {
		copied := make([]*typedAgentEventWrapper[*schema.AgenticMessage], 0, len(*typed))
		for _, event := range *typed {
			copied = append(copied, cloneTypedAgentEventWrapperForProjection(event))
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
	for _, event := range lane.Events {
		cloned.Events = append(cloned.Events, cloneAgentEventWrapperForProjection(event))
	}
	return cloned
}

func cloneInterruptInfoForCheckpointProjection(info *InterruptInfo) *InterruptInfo {
	if info == nil {
		return nil
	}
	cloned := *info
	chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
	if !ok || chatModelInfo == nil {
		return &cloned
	}
	clonedChatModelInfo := *chatModelInfo
	clonedChatModelInfo.Data = append([]byte(nil), chatModelInfo.Data...)
	clonedChatModelInfo.Info = cloneComposeInterruptInfoForProjection(chatModelInfo.Info)
	cloned.Data = &clonedChatModelInfo
	return &cloned
}

func cloneComposeInterruptInfoForProjection(info *compose.InterruptInfo) *compose.InterruptInfo {
	if info == nil {
		return nil
	}
	cloned := *info
	cloned.BeforeNodes = append([]string(nil), info.BeforeNodes...)
	cloned.AfterNodes = append([]string(nil), info.AfterNodes...)
	cloned.RerunNodes = append([]string(nil), info.RerunNodes...)
	cloned.State = cloneProjectionInfoValue(info.State)
	cloned.RerunNodesExtra = make(map[string]any, len(info.RerunNodesExtra))
	for key, value := range info.RerunNodesExtra {
		cloned.RerunNodesExtra[key] = cloneProjectionInfoValue(value)
	}
	cloned.SubGraphs = make(map[string]*compose.InterruptInfo, len(info.SubGraphs))
	for key, sub := range info.SubGraphs {
		cloned.SubGraphs[key] = cloneComposeInterruptInfoForProjection(sub)
	}
	cloned.InterruptContexts = make([]*InterruptCtx, len(info.InterruptContexts))
	for i, interruptCtx := range info.InterruptContexts {
		cloned.InterruptContexts[i] = cloneInterruptContextForProjection(interruptCtx)
	}
	return &cloned
}

func cloneInterruptContextForProjection(interruptCtx *InterruptCtx) *InterruptCtx {
	if interruptCtx == nil {
		return nil
	}
	cloned := *interruptCtx
	cloned.Address = append(Address(nil), interruptCtx.Address...)
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
		cloned.Messages = append([]*schema.Message(nil), value.Messages...)
		return &cloned
	case *agenticState:
		if value == nil {
			return value
		}
		cloned := *value
		cloned.Messages = append([]*schema.AgenticMessage(nil), value.Messages...)
		return &cloned
	case *compose.ToolsInterruptAndRerunExtra:
		if value == nil {
			return value
		}
		cloned := *value
		cloned.ToolCalls = append([]schema.ToolCall(nil), value.ToolCalls...)
		cloned.ExecutedTools = make(map[string]string, len(value.ExecutedTools))
		for callID, result := range value.ExecutedTools {
			cloned.ExecutedTools[callID] = result
		}
		cloned.ExecutedEnhancedTools = make(map[string]*schema.ToolResult, len(value.ExecutedEnhancedTools))
		for callID, result := range value.ExecutedEnhancedTools {
			cloned.ExecutedEnhancedTools[callID] = result
		}
		cloned.RerunExtraMap = make(map[string]any, len(value.RerunExtraMap))
		for callID, extra := range value.RerunExtraMap {
			cloned.RerunExtraMap[callID] = extra
		}
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
	ids := make([]string, 0, len(i.byID))
	for id := range i.byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		candidates := i.byID[id]
		for _, candidate := range candidates {
			if candidate.message != nil && reflect.DeepEqual(candidate.message.ToolCalls, toolCalls) {
				return candidate.source, true
			}
		}
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
				entries, projected := index.projectAgenticMessages(value)
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
	// Projection is accepted only if hydrating it reproduces the exact original
	// compose bytes. This keeps optimization failures from changing resume data.
	restored, err := hydrateComposeCheckpointValues(transformed, index)
	if err != nil {
		return nil, false, err
	}
	if !bytes.Equal(restored, data) {
		return data, false, nil
	}
	return transformed, true, nil
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
						messages[i] = entry.Inline
						continue
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
						messages[i] = entry.Inline
						continue
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
		switch ref.Target {
		case runCtxTargetRootInput, runCtxTargetAgenticRootInput:
			key := runCtxProjectionTargetKey(ref.Target, ref.LaneDepth)
			if ref.TargetLength <= 0 {
				return fmt.Errorf("checkpoint projection target %q has invalid length %d",
					ref.Target, ref.TargetLength)
			}
			if ref.Index >= ref.TargetLength {
				return fmt.Errorf("checkpoint projection target %q index %d exceeds length %d",
					ref.Target, ref.Index, ref.TargetLength)
			}
			if length, exists := sliceLengths[key]; exists && length != ref.TargetLength {
				return fmt.Errorf("checkpoint projection target %q has inconsistent lengths", key)
			}
			sliceLengths[key] = ref.TargetLength
			sliceCounts[key]++
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
		switch ref.Target {
		case infoTargetStateMessage:
			if ref.ContextIndex != -1 || ref.MessageIndex < 0 ||
				ref.TargetLength <= 0 || ref.MessageIndex >= ref.TargetLength {
				return errors.New("checkpoint projection has invalid interrupt state coordinates")
			}
		case infoTargetContextStateMessage:
			if ref.ContextIndex < 0 || ref.MessageIndex < 0 ||
				ref.TargetLength <= 0 || ref.MessageIndex >= ref.TargetLength {
				return errors.New("checkpoint projection has invalid context state coordinates")
			}
		case infoTargetRerunToolCalls:
			if ref.ContextIndex != -1 || ref.MessageIndex != -1 ||
				ref.RerunExtraKey == "" || ref.TargetLength != 0 || ref.IsNil {
				return errors.New("checkpoint projection has invalid rerun tool calls coordinates")
			}
		case infoTargetContextToolCalls:
			if ref.ContextIndex < 0 || ref.MessageIndex != -1 ||
				ref.TargetLength != 0 || ref.IsNil {
				return errors.New("checkpoint projection has invalid context tool calls coordinates")
			}
		default:
			return fmt.Errorf("checkpoint projection has unsupported interrupt info target %q", ref.Target)
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
		if info == nil {
			return nil
		}
		chatModelInfo, ok := info.Data.(*ChatModelAgentInterruptInfo)
		if !ok || chatModelInfo == nil || chatModelInfo.Info == nil {
			return nil
		}
		return hydrateNestedInterruptInfoPlaceholders(chatModelInfo.Info, index)
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
	return hydrateNestedInterruptInfoPlaceholders(chatModelInfo.Info, index)
}

func hydrateComposeInterruptInfoRefs(info *compose.InterruptInfo, refs []infoMessageProjectionV1,
	index *checkpointProjectionIndex) error {
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

func hydrateInfoStateMessage(target any, ref infoMessageProjectionV1,
	targetLength int, index *checkpointProjectionIndex) error {
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
	if info == nil {
		return nil
	}
	hydratedState, err := hydrateProjectionInfoValue(info.State, index)
	if err != nil {
		return err
	}
	info.State = hydratedState
	for _, key := range sortedStringKeys(info.RerunNodesExtra) {
		value := info.RerunNodesExtra[key]
		hydrated, err := hydrateProjectionInfoValue(value, index)
		if err != nil {
			return err
		}
		info.RerunNodesExtra[key] = hydrated
	}
	for _, interruptCtx := range info.InterruptContexts {
		for current := interruptCtx; current != nil; current = current.Parent {
			hydrated, err := hydrateProjectionInfoValue(current.Info, index)
			if err != nil {
				return err
			}
			current.Info = hydrated
		}
	}
	for _, key := range sortedStringKeys(info.SubGraphs) {
		if err := hydrateNestedInterruptInfoPlaceholders(info.SubGraphs[key], index); err != nil {
			return err
		}
	}
	return nil
}

func hydrateProjectionInfoValue(value any, index *checkpointProjectionIndex) (any, error) {
	placeholder, ok := value.(*checkpointInterruptInfoPlaceholderV1)
	if !ok {
		return value, nil
	}
	if placeholder == nil || placeholder.Info == nil {
		return nil, errors.New("checkpoint projection contains a nil interrupt info reference")
	}
	if err := validateInfoProjectionRefs(placeholder.Refs, placeholder.RefCount); err != nil {
		return nil, err
	}
	if err := hydrateComposeInterruptInfoRefs(placeholder.Info, placeholder.Refs, index); err != nil {
		return nil, err
	}
	if err := hydrateComposeInterruptInfoToolResults(placeholder.Info,
		placeholder.ToolResultRefs, placeholder.ToolResultRefCount, index); err != nil {
		return nil, err
	}
	if err := hydrateNestedInterruptInfoPlaceholders(placeholder.Info, index); err != nil {
		return nil, err
	}
	return placeholder.Info, nil
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
