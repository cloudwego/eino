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

package core

import (
	"context"
	"fmt"
	"reflect"

	"github.com/google/uuid"
)

type CheckPointStore interface {
	Get(ctx context.Context, checkPointID string) ([]byte, bool, error)
	Set(ctx context.Context, checkPointID string, checkPoint []byte) error
}

type InterruptSignal struct {
	ID string
	Address
	InterruptInfo
	InterruptState
	Subs []*InterruptSignal
}

func (is *InterruptSignal) Error() string {
	return fmt.Sprintf("interrupt signal: ID=%s, Addr=%s, Info=%s, State=%s, SubsLen=%d",
		is.ID, is.Address.String(), is.InterruptInfo.String(), is.InterruptState.String(), len(is.Subs))
}

type InterruptState struct {
	State                any
	LayerSpecificPayload any
}

func (is *InterruptState) String() string {
	if is == nil {
		return ""
	}
	return fmt.Sprintf("interrupt state: State=%v, LayerSpecificPayload=%v", is.State, is.LayerSpecificPayload)
}

// InterruptConfig holds optional parameters for creating an interrupt.
type InterruptConfig struct {
	LayerPayload any
}

// InterruptOption is a function that configures an InterruptConfig.
type InterruptOption func(*InterruptConfig)

// WithLayerPayload creates an option to attach layer-specific metadata
// to the interrupt's state.
func WithLayerPayload(payload any) InterruptOption {
	return func(c *InterruptConfig) {
		c.LayerPayload = payload
	}
}

func Interrupt(ctx context.Context, info any, state any, subContexts []*InterruptSignal, opts ...InterruptOption) (
	*InterruptSignal, error) {
	addr := GetCurrentAddress(ctx)

	// Apply options to get config
	config := &InterruptConfig{}
	for _, opt := range opts {
		opt(config)
	}

	myPoint := InterruptInfo{
		Info: info,
	}

	if len(subContexts) == 0 {
		myPoint.IsRootCause = true
		return &InterruptSignal{
			ID:            uuid.NewString(),
			Address:       addr,
			InterruptInfo: myPoint,
			InterruptState: InterruptState{
				State:                state,
				LayerSpecificPayload: config.LayerPayload,
			},
		}, nil
	}

	return &InterruptSignal{
		ID:            uuid.NewString(),
		Address:       addr,
		InterruptInfo: myPoint,
		InterruptState: InterruptState{
			State:                state,
			LayerSpecificPayload: config.LayerPayload,
		},
		Subs: subContexts,
	}, nil
}

// InterruptCtx provides a complete, user-facing context for a single, resumable interrupt point.
type InterruptCtx struct {
	// ID is the unique, fully-qualified address of the interrupt point.
	// It is constructed by joining the individual Address segments, e.g., "agent:A;node:graph_a;tool:tool_call_123".
	// This ID should be used when providing resume data via ResumeWithData.
	ID string
	// Address is the structured sequence of AddressSegment segments that leads to the interrupt point.
	Address Address
	// Info is the user-facing information associated with the interrupt, provided by the component that triggered it.
	Info any
	// IsRootCause indicates whether the interrupt point is the exact root cause for an interruption.
	IsRootCause bool
	// Parent points to the context of the parent component in the interrupt chain.
	// It is nil for the top-level interrupt.
	Parent *InterruptCtx
}

func (ic *InterruptCtx) EqualsWithoutID(other *InterruptCtx) bool {
	if ic == nil && other == nil {
		return true
	}

	if ic == nil || other == nil {
		return false
	}

	if !ic.Address.Equals(other.Address) {
		return false
	}

	if ic.IsRootCause != other.IsRootCause {
		return false
	}

	if ic.Info != nil || other.Info != nil {
		if ic.Info == nil || other.Info == nil {
			return false
		}

		if !reflect.DeepEqual(ic.Info, other.Info) {
			return false
		}
	}

	if ic.Parent != nil || other.Parent != nil {
		if ic.Parent == nil || other.Parent == nil {
			return false
		}

		if !ic.Parent.EqualsWithoutID(other.Parent) {
			return false
		}
	}

	return true
}

// InterruptContextsProvider is an interface for errors that contain interrupt contexts.
// This allows different packages to check for and extract interrupt contexts from errors
// without needing to know the concrete error type.
type InterruptContextsProvider interface {
	GetInterruptContexts() []*InterruptCtx
}

// FromInterruptContexts converts a list of user-facing InterruptCtx objects into an
// internal InterruptSignal tree. It correctly handles common ancestors and ensures
// that the resulting tree is consistent with the original interrupt chain.
//
// This method is primarily used by components that bridge different execution environments.
// For example, an `adk.AgentTool` might catch an `adk.InterruptInfo`, extract the
// `adk.InterruptCtx` objects from it, and then call this method on each one. The resulting
// error signals are then typically aggregated into a single error using `compose.CompositeInterrupt`
// to be returned from the tool's `InvokableRun` method.
// FromInterruptContexts reconstructs a single InterruptSignal tree from a list of
// user-facing InterruptCtx objects. It correctly merges common ancestors.
func FromInterruptContexts(contexts []*InterruptCtx) *InterruptSignal {
	if len(contexts) == 0 {
		return nil
	}

	signalMap := make(map[string]*InterruptSignal)
	var rootSignal *InterruptSignal

	// getOrCreateSignal is a recursive helper that builds the tree bottom-up.
	var getOrCreateSignal func(*InterruptCtx) *InterruptSignal
	getOrCreateSignal = func(ctx *InterruptCtx) *InterruptSignal {
		if ctx == nil {
			return nil
		}
		// If we've already created a signal for this context, return it.
		if signal, exists := signalMap[ctx.ID]; exists {
			return signal
		}

		// Create the signal for the current context.
		newSignal := &InterruptSignal{
			ID:      ctx.ID,
			Address: ctx.Address,
			InterruptInfo: InterruptInfo{
				Info:        ctx.Info,
				IsRootCause: ctx.IsRootCause,
			},
		}
		signalMap[ctx.ID] = newSignal // Cache it immediately.

		// Recursively ensure the parent exists. If it doesn't, this is the root.
		if parentSignal := getOrCreateSignal(ctx.Parent); parentSignal != nil {
			parentSignal.Subs = append(parentSignal.Subs, newSignal)
		} else {
			rootSignal = newSignal
		}
		return newSignal
	}

	// Process all contexts to ensure all branches of the tree are built.
	for _, ctx := range contexts {
		_ = getOrCreateSignal(ctx)
	}

	return rootSignal
}

// ToInterruptContexts converts the internal InterruptSignal tree into a list of
// user-facing InterruptCtx objects for the root causes of the interruption.
// Each returned context has its Parent field populated (if it has a parent),
// allowing traversal up the interrupt chain.
//
// If allowedSegmentTypes is nil, all segment types are kept and addresses are unchanged.
// If allowedSegmentTypes is provided, it:
//  1. Filters the parent chain to only keep contexts whose leaf segment type is allowed
//  2. Strips non-allowed segment types from all addresses
func ToInterruptContexts(is *InterruptSignal, allowedSegmentTypes []AddressSegmentType) []*InterruptCtx {
	if is == nil {
		return nil
	}
	var rootCauseContexts []*InterruptCtx

	var buildContexts func(*InterruptSignal, *InterruptCtx)
	buildContexts = func(signal *InterruptSignal, parentCtx *InterruptCtx) {
		currentCtx := &InterruptCtx{
			ID:          signal.ID,
			Address:     signal.Address,
			Info:        signal.InterruptInfo.Info,
			IsRootCause: signal.InterruptInfo.IsRootCause,
			Parent:      parentCtx,
		}

		if currentCtx.IsRootCause {
			rootCauseContexts = append(rootCauseContexts, currentCtx)
		}

		for _, subSignal := range signal.Subs {
			buildContexts(subSignal, currentCtx)
		}
	}

	buildContexts(is, nil)

	if len(allowedSegmentTypes) > 0 {
		allowedSet := make(map[AddressSegmentType]bool, len(allowedSegmentTypes))
		for _, t := range allowedSegmentTypes {
			allowedSet[t] = true
		}

		for _, ctx := range rootCauseContexts {
			filterParentChain(ctx, allowedSet)
			encapsulateContextAddresses(ctx, allowedSet)
		}
	}

	return rootCauseContexts
}

func filterParentChain(ctx *InterruptCtx, allowedSet map[AddressSegmentType]bool) {
	if ctx == nil {
		return
	}

	parent := ctx.Parent
	for parent != nil {
		if len(parent.Address) > 0 && allowedSet[parent.Address[len(parent.Address)-1].Type] {
			break
		}
		parent = parent.Parent
	}

	ctx.Parent = parent

	filterParentChain(parent, allowedSet)
}

func encapsulateContextAddresses(ctx *InterruptCtx, allowedSet map[AddressSegmentType]bool) {
	for c := ctx; c != nil; c = c.Parent {
		newAddr := make(Address, 0, len(c.Address))
		for _, seg := range c.Address {
			if allowedSet[seg.Type] {
				newAddr = append(newAddr, seg)
			}
		}
		c.Address = newAddr
	}
}

// SignalToPersistenceMaps flattens an InterruptSignal tree into two maps suitable for persistence in a checkpoint.
func SignalToPersistenceMaps(is *InterruptSignal) (map[string]Address, map[string]InterruptState) {
	id2addr := make(map[string]Address)
	id2state := make(map[string]InterruptState)

	if is == nil {
		return id2addr, id2state
	}

	var traverse func(*InterruptSignal)
	traverse = func(signal *InterruptSignal) {
		// Add current signal's data to the maps.
		id2addr[signal.ID] = signal.Address
		id2state[signal.ID] = signal.InterruptState // The embedded struct

		// Recurse into children.
		for _, sub := range signal.Subs {
			traverse(sub)
		}
	}

	traverse(is)
	return id2addr, id2state
}
