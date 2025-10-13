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

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type ResumeInfo struct {
	EnableStreaming bool
	// Deprecated: use GetInterruptState[T] to fetch state for agent
	*InterruptInfo

	interruptStates map[string]*interruptState
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

// GetInterruptState checks if the given address is a direct interrupt point and returns its state.
func GetInterruptState[T any](info *ResumeInfo, addr Address) (wasInterrupted bool, hasState bool, state T) {
	if info == nil {
		return false, false, *new(T)
	}
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

// getNextResumptionPoints finds all descendant interrupt points and groups them by the ID of the next segment in the address.
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
		// The resumeData is passed down unmodified, as the child will perform its own address-based lookups.
		result[childID] = newResumeInfo(states, ri.EnableStreaming)
	}
	return result
}

type InterruptInfo struct {
	// Deprecated: use InterruptContexts for user-facing info,
	// and interruptStates for internal state persistence
	Data any

	InterruptContexts []*InterruptCtx

	interruptStates []*interruptState
}

type interruptState struct {
	Addr  Address
	State any
}

func Interrupt(ctx context.Context, info any) *AgentAction {
	runCtx := getRunCtx(ctx)
	addr := runCtx.Addr.DeepCopy()
	interruptID := addr.String()
	return &AgentAction{
		Interrupted: &InterruptInfo{
			InterruptContexts: []*InterruptCtx{
				{
					ID:      interruptID,
					Info:    info,
					Address: addr,
					IsCause: true,
				},
			},
			interruptStates: []*interruptState{
				{
					Addr: addr,
				},
			},
		},
	}
}

func StatefulInterrupt(ctx context.Context, info any, state any) *AgentAction {
	runCtx := getRunCtx(ctx)
	addr := runCtx.Addr.DeepCopy()
	interruptID := addr.String()
	return &AgentAction{
		Interrupted: &InterruptInfo{
			InterruptContexts: []*InterruptCtx{
				{
					ID:      interruptID,
					Info:    info,
					Address: addr,
					IsCause: true,
				},
			},
			interruptStates: []*interruptState{
				{
					Addr:  addr,
					State: state,
				},
			},
		},
	}
}

func CompositeInterrupt(ctx context.Context, info any, state any, subInterruptInfos ...*InterruptInfo) *AgentAction {
	runCtx := getRunCtx(ctx)
	addr := runCtx.Addr.DeepCopy()
	interruptID := addr.String()

	interruptContexts := []*InterruptCtx{
		{
			ID:      interruptID,
			Info:    info,
			Address: addr,
		},
	}
	for _, subInfo := range subInterruptInfos {
		interruptContexts = append(interruptContexts, subInfo.InterruptContexts...)
	}

	var interruptStates []*interruptState
	for _, subInfo := range subInterruptInfos {
		interruptStates = append(interruptStates, subInfo.interruptStates...)
	}

	return &AgentAction{
		Interrupted: &InterruptInfo{
			InterruptContexts: interruptContexts,
			interruptStates: append(interruptStates, &interruptState{
				Addr:  addr,
				State: state,
			}),
		},
	}
}

type Address = compose.Address
type AddressSegment = compose.AddressSegment
type AddressSegmentType = compose.AddressSegmentType

const (
	AddressSegmentAgent AddressSegmentType = "agent"
)

type InterruptCtx = compose.InterruptCtx

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

func (m *mockStore) Get(ctx context.Context, checkPointID string) ([]byte, bool, error) {
	if m.Valid {
		return m.Data, true, nil
	}
	return nil, false, nil
}

func (m *mockStore) Set(ctx context.Context, checkPointID string, checkPoint []byte) error {
	m.Data = checkPoint
	m.Valid = true
	return nil
}
