/*
 * Copyright 2025 CloudWeGo Authors
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
	"errors"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

// ResumeInfo holds all the information necessary to resume an interrupted agent execution.
// It is created by the framework and passed to an agent's Resume method.
type ResumeInfo struct {
	// EnableStreaming indicates whether the original execution was in streaming mode.
	EnableStreaming bool

	// Deprecated: use InterruptContexts from the embedded InterruptInfo for user-facing details,
	// and GetInterruptState for internal state retrieval.
	*InterruptInfo

	WasInterrupted bool
	InterruptState any
	IsResumeTarget bool
	ResumeData     any
}

// InterruptInfo contains all the information about an interruption event.
// It is created by the framework when an agent returns an interrupt action.
type InterruptInfo struct {
	Data any

	// InterruptContexts provides a structured, user-facing view of the interrupt chain.
	// Each context represents a step in the agent hierarchy that was interrupted.
	InterruptContexts []*InterruptCtx
}

// TypedInterrupt creates a typed interrupt event that pauses execution to request external input.
func TypedInterrupt[M MessageType](ctx context.Context, info any) *TypedAgentEvent[M] {
	var rp []RunStep
	rCtx := getRunCtx(ctx)
	if rCtx != nil {
		rp = rCtx.RunPath
	}

	is, err := core.Interrupt(ctx, info, nil, nil,
		core.WithLayerPayload(rp))
	if err != nil {
		return &TypedAgentEvent[M]{Err: err}
	}

	contexts := core.ToInterruptContexts(is, allowedAddressSegmentTypes)

	return &TypedAgentEvent[M]{
		Action: &AgentAction{
			Interrupted: &InterruptInfo{
				InterruptContexts: contexts,
			},
			internalInterrupted: is,
		},
	}
}

// Interrupt creates a basic interrupt action.
// This is used when an agent needs to pause its execution to request external input or intervention,
// but does not need to save any internal state to be restored upon resumption.
// The `info` parameter is user-facing data that describes the reason for the interrupt.
func Interrupt(ctx context.Context, info any) *AgentEvent {
	return TypedInterrupt[*schema.Message](ctx, info)
}

// TypedStatefulInterrupt creates a typed interrupt event that also saves the agent's internal state.
func TypedStatefulInterrupt[M MessageType](ctx context.Context, info any, state any) *TypedAgentEvent[M] {
	var rp []RunStep
	rCtx := getRunCtx(ctx)
	if rCtx != nil {
		rp = rCtx.RunPath
	}

	is, err := core.Interrupt(ctx, info, state, nil,
		core.WithLayerPayload(rp))
	if err != nil {
		return &TypedAgentEvent[M]{Err: err}
	}

	contexts := core.ToInterruptContexts(is, allowedAddressSegmentTypes)

	return &TypedAgentEvent[M]{
		Action: &AgentAction{
			Interrupted: &InterruptInfo{
				InterruptContexts: contexts,
			},
			internalInterrupted: is,
		},
	}
}

// StatefulInterrupt creates an interrupt action that also saves the agent's internal state.
// This is used when an agent has internal state that must be restored for it to continue correctly.
// The `info` parameter is user-facing data describing the interrupt.
// The `state` parameter is the agent's internal state object, which will be serialized and stored.
func StatefulInterrupt(ctx context.Context, info any, state any) *AgentEvent {
	return TypedStatefulInterrupt[*schema.Message](ctx, info, state)
}

// TypedCompositeInterrupt creates a typed interrupt event that aggregates sub-interrupt signals.
func TypedCompositeInterrupt[M MessageType](ctx context.Context, info any, state any,
	subInterruptSignals ...*InterruptSignal) *TypedAgentEvent[M] {
	var rp []RunStep
	rCtx := getRunCtx(ctx)
	if rCtx != nil {
		rp = rCtx.RunPath
	}

	is, err := core.Interrupt(ctx, info, state, subInterruptSignals,
		core.WithLayerPayload(rp))
	if err != nil {
		return &TypedAgentEvent[M]{Err: err}
	}

	contexts := core.ToInterruptContexts(is, allowedAddressSegmentTypes)

	return &TypedAgentEvent[M]{
		Action: &AgentAction{
			Interrupted: &InterruptInfo{
				InterruptContexts: contexts,
			},
			internalInterrupted: is,
		},
	}
}

// CompositeInterrupt creates an interrupt event that aggregates sub-interrupt signals.
func CompositeInterrupt(ctx context.Context, info any, state any,
	subInterruptSignals ...*InterruptSignal) *AgentEvent {
	return TypedCompositeInterrupt[*schema.Message](ctx, info, state, subInterruptSignals...)
}

// Address represents the unique, hierarchical address of a component within an execution.
// It is a slice of AddressSegments, where each segment represents one level of nesting.
// This is a type alias for core.Address. See the core package for more details.
type Address = core.Address
type AddressSegment = core.AddressSegment
type AddressSegmentType = core.AddressSegmentType

const (
	AddressSegmentAgent AddressSegmentType = "agent"
	AddressSegmentTool  AddressSegmentType = "tool"
)

var allowedAddressSegmentTypes = []AddressSegmentType{AddressSegmentAgent, AddressSegmentTool}

// AppendAddressSegment adds an address segment for the current execution context.
func AppendAddressSegment(ctx context.Context, segType AddressSegmentType, segID string) context.Context {
	return core.AppendAddressSegment(ctx, segType, segID, "")
}

// InterruptCtx provides a structured, user-facing view of a single point of interruption.
// It contains the ID and Address of the interrupted component, as well as user-defined info.
// This is a type alias for core.InterruptCtx. See the core package for more details.
type InterruptCtx = core.InterruptCtx
type InterruptSignal = core.InterruptSignal

// FromInterruptContexts converts user-facing interrupt contexts to an interrupt signal.
func FromInterruptContexts(contexts []*InterruptCtx) *InterruptSignal {
	return core.FromInterruptContexts(contexts)
}

// WithCheckPointID sets the checkpoint ID used for interruption persistence.
func WithCheckPointID(id string) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *options) {
		t.checkPointID = &id
	})
}

func init() {
	schema.RegisterName[*serialization]("_eino_adk_serialization")
	schema.RegisterName[*WorkflowInterruptInfo]("_eino_adk_workflow_interrupt_info")
	// Register []byte for gob: the cancel refactor routes bridge store checkpoint
	// bytes ([]byte) through InterruptState.State (type any) inside the outer
	// serialization struct. Gob requires concrete types behind interface fields
	// to be registered.
	gob.Register([]byte{})
}

// serialization CheckpointSchema: root checkpoint payload (gob).
// Any type tagged with `CheckpointSchema:` is persisted and must remain backward compatible.
type serialization struct {
	RunCtx *runContext
	// deprecated: still keep it here for backward compatibility
	Info                *InterruptInfo
	EnableStreaming     bool
	InterruptID2Address map[string]Address
	InterruptID2State   map[string]core.InterruptState
}

func runnerLoadCheckPointImpl(store CheckPointStore, ctx context.Context, checkpointID string) (
	context.Context, *runContext, *ResumeInfo, error) {
	data, existed, err := store.Get(ctx, checkpointID)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to get checkpoint from store: %w", err)
	}
	if !existed {
		return nil, nil, nil, fmt.Errorf("checkpoint[%s] not exist", checkpointID)
	}

	data = preprocessADKCheckpoint(data)

	s := &serialization{}
	err = gob.NewDecoder(bytes.NewReader(data)).Decode(s)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("failed to decode checkpoint: %w", err)
	}
	ctx = core.PopulateInterruptState(ctx, s.InterruptID2Address, s.InterruptID2State)

	return ctx, s.RunCtx, &ResumeInfo{
		EnableStreaming: s.EnableStreaming,
		InterruptInfo:   s.Info,
	}, nil
}

// preprocessADKCheckpoint fixes a gob incompatibility when resuming old ChatModelAgent/DeepAgents checkpoints.
//
// Background
//   - ADK checkpoints are gob-encoded.
//   - Some values inside checkpoints are stored as `any`, so gob includes a concrete type name
//     string in the wire format and uses that name to pick the local Go type to decode into.
//
// Problem (v0.8.0-v0.8.3 checkpoints)
//   - In v0.8.0-v0.8.3, *State was registered under the name "_eino_adk_react_state" AND
//     implemented GobEncode/GobDecode, so the wire format for that name is "GobEncoder payload"
//     (opaque bytes).
//   - In v0.7.*, the same name "_eino_adk_react_state" was used but encoded as a normal struct
//     (no GobEncode). Gob treats these two wire formats as incompatible.
//   - Gob only allows one local Go type per name. Today we register "_eino_adk_react_state" to
//     a v0.7-compatible struct decoder (stateV07). If we try to decode a v0.8.0-v0.8.3
//     checkpoint under that same name, gob fails with a "want struct; got non-struct" mismatch.
//
// Solution
//   - We keep "_eino_adk_react_state" mapped to the v0.7 decoder.
//   - For v0.8.0-v0.8.3 checkpoints only, we rewrite the on-wire name to a same-length alias
//     "_eino_adk_state_v080_", which is registered to a GobDecoder-compatible type (stateV080).
//   - The alias is the same length as the original, so we can safely replace the length-prefixed
//     bytes without re-encoding the whole stream.
func preprocessADKCheckpoint(data []byte) []byte {
	const (
		lenPrefixedReactStateName         = "\x15" + stateGobNameV07
		lenPrefixedCompatName             = "\x15" + stateGobNameV080
		lenPrefixedStateSerializationName = "\x12stateSerialization"
	)

	// the following line checks whether the checkpoint is persisted through v0.8.0-v0.8.3
	if !bytes.Contains(data, []byte(lenPrefixedReactStateName)) || !bytes.Contains(data, []byte(lenPrefixedStateSerializationName)) {
		return data
	}
	return bytes.ReplaceAll(data,
		[]byte(lenPrefixedReactStateName),
		[]byte(lenPrefixedCompatName))
}

func runnerSaveCheckPointImpl(
	enableStreaming bool,
	store CheckPointStore,
	ctx context.Context,
	key string,
	info *InterruptInfo,
	is *core.InterruptSignal,
) error {
	if store == nil {
		return nil
	}

	runCtx := getRunCtx(ctx)

	id2Addr, id2State := core.SignalToPersistenceMaps(is)

	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(&serialization{
		RunCtx:              runCtx,
		Info:                info,
		InterruptID2Address: id2Addr,
		InterruptID2State:   id2State,
		EnableStreaming:     enableStreaming,
	})
	if err != nil {
		return fmt.Errorf("failed to encode checkpoint: %w", err)
	}
	return store.Set(ctx, key, buf.Bytes())
}

const bridgeCheckpointID = "adk_react_mock_key"

func newBridgeStore() *bridgeStore {
	return &bridgeStore{data: make(map[string][]byte)}
}

func newResumeBridgeStore(checkPointID string, data []byte) *bridgeStore {
	return &bridgeStore{
		data: map[string][]byte{checkPointID: data},
	}
}

type bridgeStore struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (m *bridgeStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.data[key]; ok {
		return v, true, nil
	}
	return nil, false, nil
}

func (m *bridgeStore) Set(_ context.Context, key string, checkPoint []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.data == nil {
		m.data = make(map[string][]byte)
	}
	m.data[key] = checkPoint
	return nil
}

func getNextResumeAgent(ctx context.Context, info *ResumeInfo) (string, error) {
	nextAgents, err := core.GetNextResumptionPoints(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get next agent leading to interruption: %w", err)
	}

	if len(nextAgents) == 0 {
		return "", errors.New("no child agents leading to interrupted agent were found")
	}

	if len(nextAgents) > 1 {
		return "", errors.New("agent has multiple child agents leading to interruption, " +
			"but concurrent transfer is not supported")
	}

	// get the single next agent to delegate to.
	var nextAgentID string
	for id := range nextAgents {
		nextAgentID = id
		break
	}

	return nextAgentID, nil
}

func getNextResumeAgents(ctx context.Context, info *ResumeInfo) (map[string]bool, error) {
	nextAgents, err := core.GetNextResumptionPoints(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get next agents leading to interruption: %w", err)
	}

	if len(nextAgents) == 0 {
		return nil, errors.New("no child agents leading to interrupted agent were found")
	}

	return nextAgents, nil
}

func buildResumeInfo(ctx context.Context, nextAgentID string, info *ResumeInfo) (
	context.Context, *ResumeInfo) {
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, nextAgentID)
	nextResumeInfo := &ResumeInfo{
		EnableStreaming: info.EnableStreaming,
		InterruptInfo:   info.InterruptInfo,
	}

	wasInterrupted, hasState, state := core.GetInterruptState[any](ctx)
	nextResumeInfo.WasInterrupted = wasInterrupted
	if hasState {
		nextResumeInfo.InterruptState = state
	}

	if wasInterrupted {
		isResumeTarget, hasData, data := core.GetResumeContext[any](ctx)
		nextResumeInfo.IsResumeTarget = isResumeTarget
		if hasData {
			nextResumeInfo.ResumeData = data
		}
	}

	ctx = updateRunPathOnly(ctx, nextAgentID)

	return ctx, nextResumeInfo
}
