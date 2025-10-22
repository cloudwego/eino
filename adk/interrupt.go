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
	"fmt"
	"strings"

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

	interruptStates map[string]*interruptState
	// ResumeData holds data provided by the user for targeted resumption of specific agents.
	// The map keys are the string addresses of the target agents.
	ResumeData map[string]any
}

// GetResumeContext retrieves targeted data for the current agent during a resume operation.
//
// This function is typically called *after* an agent has already determined it is in a
// resumed state by calling GetInterruptState.
//
// It returns three values:
//   - isResumeFlow: A boolean that is true if the current agent's address was explicitly targeted
//     by the `TargetedResume` method.
//   - hasData: A boolean that is true if data was provided for this agent (i.e., not nil).
//   - data: The typed data provided by the user.
//
// ### How to Use This Function: A Decision Framework
//
// The correct usage pattern depends on the application's desired resume strategy.
//
// #### Strategy 1: Implicit "Resume All"
// In some use cases, any resume operation implies that *all* interrupted agents should proceed.
// For example, if an application's UI only provides a single "Continue" button. In this model,
// an agent can often just use `GetInterruptState` to see if `wasInterrupted` is true and then
// proceed with its logic, as it can assume it is an intended target. It may still call
// `GetResumeContext` to check for optional data, but the `isResumeFlow` flag is less critical.
//
// #### Strategy 2: Explicit "Targeted Resume" (Most Common)
// For applications with multiple, distinct interrupt points that must be resumed independently, it is
// crucial to differentiate which point is being resumed. This is the primary use case for the `isResumeFlow` flag.
//   - If `isResumeFlow` is `true`: Your agent is the explicit target. You should consume
//     the `data` (if any) and complete your work.
//   - If `isResumeFlow` is `false`: Another agent is the target. You MUST re-interrupt
//     (e.g., by returning `StatefulInterrupt(...)`) to preserve your state and allow the
//     resume signal to propagate.
//
// ### Guidance for Composite Agents
//
// Composite agents (like `SequentialAgent` or `ChatModelAgent`) have a dual role:
//  1. Check for Self-Targeting: A composite agent can itself be the target of a resume
//     operation, for instance, to modify its internal state (e.g., providing a new history
//     to a `ChatModelAgent`). It may call `GetResumeContext` to check for data targeted at its own address.
//  2. Act as a Conduit: After checking for itself, its primary role is to re-execute its children,
//     allowing the `ResumeInfo` to flow down to them. It must not consume a resume signal
//     intended for one of its descendants.
func GetResumeContext[T any](ctx context.Context, info *ResumeInfo) (isResumeFlow bool, hasData bool, data T) {
	var t T
	if info == nil || info.ResumeData == nil {
		return false, false, t
	}

	addr := GetCurrentAddress(ctx)
	val, exists := info.ResumeData[addr.String()]
	if !exists {
		return false, false, t
	}

	isResumeFlow = true
	if val == nil {
		return true, false, t
	}

	data, hasData = val.(T)
	return
}

// newResumeInfo creates a new ResumeInfo object.
// This is intended for internal framework use.
func newResumeInfo(states []*interruptState, enableStreaming bool) *ResumeInfo {
	stateMap := make(map[string]*interruptState, len(states))
	for _, is := range states {
		stateMap[is.Addr.String()] = is
	}

	return &ResumeInfo{
		EnableStreaming: enableStreaming,
		interruptStates: stateMap,
	}
}

// GetInterruptState checks if the current agent was a direct interrupt point and returns its state.
func GetInterruptState[T any](ctx context.Context, info *ResumeInfo) (wasInterrupted bool, hasState bool, state T) {
	if info == nil {
		return false, false, *new(T)
	}
	addr := GetCurrentAddress(ctx)
	is, ok := info.interruptStates[addr.String()]
	if !ok {
		return false, false, *new(T)
	}
	wasInterrupted = true
	if is.State != nil {
		s, ok := is.State.(T)
		if ok {
			hasState = true
			state = s
		}
	}
	return
}

// getNextResumptionPoints finds all descendant interrupt points
// and groups them by the ID of the next segment in the address.
// It returns a map of new, scoped ResumeInfo objects for each child component that needs to be resumed.
// It is the unexported implementation detail.
func (ri *ResumeInfo) getNextResumptionPoints(parentAddr Address) map[string]*ResumeInfo {
	nextPoints := make(map[string][]*interruptState)
	parentAddrLen := len(parentAddr)

	for _, is := range ri.interruptStates {
		addr := is.Addr
		// Check if the interrupt state's address is a descendant of the parent
		if len(addr) > parentAddrLen && addr.HasPrefix(parentAddr) {
			// The next segment in the path determines which child component is responsible.
			nextSegment := addr[parentAddrLen]
			childID := nextSegment.ID
			if nextSegment.Type != AddressSegmentAgent {
				panic(fmt.Sprintf("getNextResumptionPoint for parentAddr %s returns childID %s, "+
					"which is not an agent", parentAddr, childID))
			}
			nextPoints[childID] = append(nextPoints[childID], is)
		}
	}

	if len(nextPoints) == 0 {
		return nil
	}

	result := make(map[string]*ResumeInfo, len(nextPoints))
	for childID, states := range nextPoints {
		// Create a new, scoped ResumeInfo for the child.
		childRI := newResumeInfo(states, ri.EnableStreaming)

		// Filter the ResumeData map to only include entries relevant to the child's address space.
		if ri.ResumeData != nil {
			childAddrPrefix := append(parentAddr.DeepCopy(),
				AddressSegment{Type: AddressSegmentAgent, ID: childID}).String()
			childResumeData := make(map[string]any)
			for addr, data := range ri.ResumeData {
				if strings.HasPrefix(addr, childAddrPrefix) {
					childResumeData[addr] = data
				}
			}
			if len(childResumeData) > 0 {
				childRI.ResumeData = childResumeData
			}
		}

		result[childID] = childRI
	}
	return result
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
	Info            *InterruptInfo
	InterruptStates []*interruptState
	EnableStreaming bool
}

// getCheckPoint get checkpoint from store.
// What we want to retrieve is the full *ResumeInfo, as well as the runCtx from the previous Runner.Run.
func getCheckPoint(
	ctx context.Context,
	store compose.CheckPointStore,
	key string,
) (*runContext, *ResumeInfo, bool, error) {
	data, existed, err := store.Get(ctx, key)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to get checkpoint from store: %w", err)
	}
	if !existed {
		return nil, nil, false, nil
	}
	s := &serialization{}
	err = gob.NewDecoder(bytes.NewReader(data)).Decode(s)
	if err != nil {
		return nil, nil, false, fmt.Errorf("failed to decode checkpoint: %w", err)
	}

	id2State := make(map[string]*interruptState, len(s.InterruptStates))
	for _, state := range s.InterruptStates {
		id2State[state.Addr.String()] = state
	}

	return s.RunCtx, &ResumeInfo{
		EnableStreaming: s.EnableStreaming,
		InterruptInfo:   s.Info,
		interruptStates: id2State,
	}, true, nil
}

func (r *Runner) saveCheckPoint(
	ctx context.Context,
	store compose.CheckPointStore,
	key string,
	info *InterruptInfo,
	rootInput *AgentInput,
	session *runSession,
	parentAddr Address,
) error {
	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(&serialization{
		RunCtx: &runContext{
			RootInput: rootInput,
			Addr:      parentAddr,
			Session:   session,
		},
		Info:            info,
		InterruptStates: info.interruptStates,
		EnableStreaming: r.enableStreaming,
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
