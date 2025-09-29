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
	*InterruptInfo
}

type InterruptInfo struct {
	// Deprecated: use Info for user-facing info, and state for internal state persistence
	Data any

	InterruptContexts []*InterruptCtx

	interruptStates []*interruptState
}

type interruptState struct {
	state  any
	runCtx *runContext
}

func StatefulInterrupt(ctx context.Context, info any, state any) *AgentAction {
	runCtx := getRunCtx(ctx)
	addr := runCtx.Addr
	interruptID := addr.String()
	return &AgentAction{
		Interrupted: &InterruptInfo{
			InterruptContexts: []*InterruptCtx{
				{
					ID:      interruptID,
					Info:    info,
					Address: addr.DeepCopy(),
				},
			},
			interruptStates: []*interruptState{
				{
					state:  state,
					runCtx: runCtx.deepCopy(),
				},
			},
		},
	}
}

func CompositeInterrupt(ctx context.Context, state any, subInterruptInfos ...*InterruptInfo) *AgentAction {
	runCtx := getRunCtx(ctx)

	var interruptContexts []*InterruptCtx
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
				state:  state,
				runCtx: runCtx.deepCopy(),
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
}

type serialization struct {
	RunCtx *runContext
	Info   *InterruptInfo
}

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
	enableStreaming := false
	if s.RunCtx.RootInput != nil {
		enableStreaming = s.RunCtx.RootInput.EnableStreaming
	}
	return s.RunCtx, &ResumeInfo{
		EnableStreaming: enableStreaming,
		InterruptInfo:   s.Info,
	}, true, nil
}

func saveCheckPoint(
	ctx context.Context,
	store compose.CheckPointStore,
	key string,
	runCtx *runContext,
	info *InterruptInfo,
) error {
	buf := &bytes.Buffer{}
	err := gob.NewEncoder(buf).Encode(&serialization{
		RunCtx: runCtx,
		Info:   info,
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
