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

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/core"
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

type interruptState struct {
	Addr    Address
	State   any
	RunPath []RunStep
}

// Interrupt creates a basic interrupt action.
// This is used when an agent needs to pause its execution to request external input or intervention,
// but does not need to save any internal state to be restored upon resumption.
// The `info` parameter is user-facing data that describes the reason for the interrupt.
func Interrupt(ctx context.Context, info any) *AgentEvent {
	is, err := core.Interrupt(ctx, info, nil, nil,
		core.WithLayerPayload(getRunCtx(ctx).RunPath))
	if err != nil {
		return &AgentEvent{Err: err}
	}

	return &AgentEvent{
		Action: &AgentAction{
			Interrupted:         &InterruptInfo{},
			internalInterrupted: is,
		},
	}
}

// StatefulInterrupt creates an interrupt action that also saves the agent's internal state.
// This is used when an agent has internal state that must be restored for it to continue correctly.
// The `info` parameter is user-facing data describing the interrupt.
// The `state` parameter is the agent's internal state object, which will be serialized and stored.
func StatefulInterrupt(ctx context.Context, info any, state any) *AgentEvent {
	is, err := core.Interrupt(ctx, info, state, nil,
		core.WithLayerPayload(getRunCtx(ctx).RunPath))
	if err != nil {
		return &AgentEvent{Err: err}
	}

	return &AgentEvent{
		Action: &AgentAction{
			Interrupted:         &InterruptInfo{},
			internalInterrupted: is,
		},
	}
}

// CompositeInterrupt creates an interrupt action for a workflow agent.
// It combines the interrupts from one or more of its sub-agents into a single, cohesive interrupt.
// This is used by workflow agents (like Sequential, Parallel, or Loop) to propagate interrupts from their children.
// The `info` parameter is user-facing data describing the workflow's own reason for interrupting.
// The `state` parameter is the workflow agent's own state (e.g., the index of the sub-agent that was interrupted).
// The `subInterruptSignals` is a variadic list of the InterruptSignal objects from the interrupted sub-agents.
func CompositeInterrupt(ctx context.Context, info any, state any,
	subInterruptSignals ...*core.InterruptSignal) *AgentEvent {
	is, err := core.Interrupt(ctx, info, state, subInterruptSignals,
		core.WithLayerPayload(getRunCtx(ctx).RunPath))
	if err != nil {
		return &AgentEvent{Err: err}
	}

	return &AgentEvent{
		Action: &AgentAction{
			Interrupted:         &InterruptInfo{},
			internalInterrupted: is,
		},
	}
}

// Address represents the unique, hierarchical address of a component within an execution.
// It is a slice of AddressSegments, where each segment represents one level of nesting.
// This is a type alias for core.Address. See the core package for more details.
type Address = core.Address
type AddressSegment = core.AddressSegment
type AddressSegmentType = core.AddressSegmentType

const (
	AddressSegmentAgent AddressSegmentType = "agent"
)

// InterruptCtx provides a structured, user-facing view of a single point of interruption.
// It contains the ID and Address of the interrupted component, as well as user-defined info.
// This is a type alias for core.InterruptCtx. See the core package for more details.
type InterruptCtx = core.InterruptCtx

func WithCheckPointID(id string) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *options) {
		t.checkPointID = &id
	})
}

func init() {
	schema.RegisterName[*serialization]("_eino_adk_serialization")
	schema.RegisterName[*WorkflowInterruptInfo]("_eino_adk_workflow_interrupt_info")
	schema.RegisterName[*State]("_eino_adk_react_state")

	schema.Register[*interruptState]()
}

type serialization struct {
	RunCtx *runContext
	// deprecated: still keep it here for backward compatibility
	Info                *InterruptInfo
	EnableStreaming     bool
	Addr                Address
	InterruptID2Address map[string]Address
	InterruptID2State   map[string]core.InterruptState
}

func loadCheckPoint(ctx context.Context, store compose.CheckPointStore, checkpointID string) (
	context.Context, *ResumeInfo, error) {
	data, existed, err := store.Get(ctx, checkpointID)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get checkpoint from store: %w", err)
	}
	if !existed {
		return nil, nil, fmt.Errorf("checkpoint[%s] not exist", checkpointID)
	}

	s := &serialization{}
	err = gob.NewDecoder(bytes.NewReader(data)).Decode(s)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to decode checkpoint: %w", err)
	}
	ctx = core.PopulateInterruptState(ctx, s.InterruptID2Address, s.InterruptID2State)
	ctx = setRunCtx(ctx, s.RunCtx)
	if len(s.Addr) > 0 {
		ctx = core.SetParentAddress(ctx, s.Addr)
	}

	return ctx, &ResumeInfo{
		EnableStreaming: s.EnableStreaming,
		InterruptInfo:   s.Info,
	}, nil
}

func (r *Runner) saveCheckPoint(
	ctx context.Context,
	store compose.CheckPointStore,
	key string,
	info *InterruptInfo,
	is *core.InterruptSignal,
) error {
	runCtx := getRunCtx(ctx)

	id2Addr, id2State := core.SignalToPersistenceMaps(is)

	var addr Address
	if r.parentAddr != nil {
		addr = *r.parentAddr
	}

	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(&serialization{
		RunCtx:              runCtx,
		Info:                info,
		Addr:                addr,
		InterruptID2Address: id2Addr,
		InterruptID2State:   id2State,
		EnableStreaming:     r.enableStreaming,
	})
	if err != nil {
		return fmt.Errorf("failed to encode checkpoint: %w", err)
	}
	return store.Set(ctx, key, buf.Bytes())
}

const mockCheckPointID = "adk_react_mock_key"

func newEmptyStore() *mockStore {
	return &mockStore{}
}

func newResumeStore(data []byte) *mockStore {
	return &mockStore{
		Data:  data,
		Valid: true,
	}
}

type mockStore struct {
	Data  []byte
	Valid bool
}

func (m *mockStore) Get(_ context.Context, _ string) ([]byte, bool, error) {
	if m.Valid {
		return m.Data, true, nil
	}
	return nil, false, nil
}

func (m *mockStore) Set(_ context.Context, _ string, checkPoint []byte) error {
	m.Data = checkPoint
	m.Valid = true
	return nil
}

func getNextResumeAgent(ctx context.Context, info *ResumeInfo) (context.Context,
	string, *ResumeInfo, error) {
	nextAgents, err := core.GetNextResumptionPoints(ctx)
	if err != nil {
		return ctx, "", nil, fmt.Errorf("failed to get next agent leading to interruption: %w", err)
	}

	if len(nextAgents) == 0 {
		return ctx, "", nil, errors.New("no child agents leading to interrupted agent were found")
	}

	if len(nextAgents) > 1 {
		return ctx, "", nil, errors.New("agent has multiple child agents leading to interruption, " +
			"but concurrent transfer is not supported")
	}

	// get the single next agent to delegate to.
	var nextAgentID string
	for id := range nextAgents {
		nextAgentID = id
		break
	}

	ctx, nextResumeInfo := buildResumeInfo(ctx, nextAgentID, info)

	return ctx, nextAgentID, nextResumeInfo, nil
}

func getNextResumeAgents(ctx context.Context, info *ResumeInfo) (map[string]context.Context,
	map[string]*ResumeInfo, error) {
	nextAgents, err := core.GetNextResumptionPoints(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get next agents leading to interruption: %w", err)
	}

	if len(nextAgents) == 0 {
		return nil, nil, errors.New("no child agents leading to interrupted agent were found")
	}

	agentID2Ctx := make(map[string]context.Context)
	agentID2ResumeInfo := make(map[string]*ResumeInfo)
	for id := range nextAgents {
		subCtx, subResumeInfo := buildResumeInfo(ctx, id, info)

		agentID2Ctx[id] = subCtx
		agentID2ResumeInfo[id] = subResumeInfo
	}

	return agentID2Ctx, agentID2ResumeInfo, nil
}

func buildResumeInfo(ctx context.Context, nextAgentID string, info *ResumeInfo) (
	context.Context, *ResumeInfo) {
	ctx = core.AppendAddressSegment(ctx, AddressSegmentAgent, nextAgentID)
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

	return ctx, nextResumeInfo
}
