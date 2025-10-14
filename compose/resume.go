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

package compose

import (
	"context"
	"sync"
)

// GetInterruptState provides a type-safe way to check for and retrieve the persisted state from a previous interruption.
// It is the primary function a component should use to understand its past state.
//
// It returns three values:
//   - wasInterrupted (bool): True if the node was part of a previous interruption, regardless of whether state was provided.
//   - state (T): The typed state object, if it was provided and matches type `T`.
//   - hasState (bool): True if state was provided during the original interrupt and successfully cast to type `T`.
func GetInterruptState[T any](ctx context.Context) (wasInterrupted bool, hasState bool, state T) {
	rCtx, ok := getRunCtx(ctx)
	if !ok || rCtx.interruptData == nil || !rCtx.interruptData.Interrupted {
		return
	}

	wasInterrupted = true
	if rCtx.interruptData.State == nil {
		return
	}

	state, hasState = rCtx.interruptData.State.(T)
	return
}

// GetResumeContext checks if the current node is being resumed and retrieves any associated resume data in a type-safe way.
// This is the primary function a component should use to understand the user's intent for the current run.
//
// It returns three values:
//   - isResumeFlow (bool): True if the current node was the specific target of a `Resume` or `ResumeWithData` call.
//   - data (T): The typed resume data. This is only valid if `hasData` is true.
//   - hasData (bool): True if resume data was provided via `ResumeWithData` and successfully cast to type `T`.
//     It is important to check this flag rather than checking `data == nil`, as the provided data could itself be nil
//     or a non-nil zero value (like 0 or "").
func GetResumeContext[T any](ctx context.Context) (isResumeFlow bool, hasData bool, data T) {
	rCtx, ok := getRunCtx(ctx)
	if !ok {
		return
	}

	isResumeFlow = rCtx.isResumeFlow
	if !isResumeFlow {
		return
	}

	// It is a resume flow, now check for data
	if rCtx.resumeData == nil {
		return // hasData is false
	}

	data, hasData = rCtx.resumeData.(T)
	return
}

// GetAllResumeData retrieves all resume data from the context that has not yet been claimed and used
// by a resumed component.
//
// This function is primarily intended for advanced use cases involving "bridge" components that
// contain nested, independent execution environments (e.g., an adk.AgentTool running an
// adk.Agent which in turn contains a compose.Graph).
//
// The typical pattern for such a bridge component during a resume operation is:
// 1. Call GetAllResumeData to get the complete map of unused data.
// 2. Identify and consume the data intended for the bridge component itself, often by deleting it from the map.
// 3. Pass the remaining data down to the nested environment using its own BatchResumeWithData function.
// This ensures that resume data is correctly routed across different framework layers.
func GetAllResumeData(ctx context.Context) map[string]any {
	rInfo, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	if !ok || rInfo == nil {
		return nil
	}

	result := make(map[string]any, len(rInfo.interruptID2ResumeData))
	// Copy all resume data to the result map
	for id, data := range rInfo.interruptID2ResumeData {
		result[id] = data
	}

	for id := range rInfo.interruptID2ResumeDataUsed {
		delete(result, id)
	}

	return result
}

// GetCurrentAddress returns the hierarchical address of the currently executing component.
// The address is a sequence of segments, each identifying a structural part of the execution
// like an agent, a graph node, or a tool call. This can be useful for logging or debugging.
func GetCurrentAddress(ctx context.Context) (Address, bool) {
	if p, ok := ctx.Value(runCtxKey{}).(*runCtx); ok {
		return p.addr, true
	}

	return nil, false
}

// Resume marks a specific interrupt point for resumption without passing any data.
// When the graph is resumed, the component that was interrupted at the given `address` will be re-executed,
// but its `runCtx.resumeData` field will be nil.
//
// - ctx: The parent context.
//   - addresses: A variadic list of unique interrupt point addresses, obtained from `InterruptCtx.ID`.
func Resume(ctx context.Context, addresses ...string) context.Context {
	resumeData := make(map[string]any, len(addresses))
	for _, addr := range addresses {
		resumeData[addr] = nil
	}
	// An empty map signals a "resume all" to the framework.
	return BatchResumeWithData(ctx, resumeData)
}

// ResumeWithData provides a convenient way to resume a single interrupt point with data.
//
//   - ctx: The parent context.
//   - address: The unique address of the interrupt point, obtained from `InterruptCtx.ID`.
//   - data: The data to be passed to the interrupted component.
func ResumeWithData(ctx context.Context, address string, data any) context.Context {
	return BatchResumeWithData(ctx, map[string]any{address: data})
}

// BatchResumeWithData attaches data to one or more interrupt points for resumption in a single batch operation.
// This is the underlying function used by `Resume` and `ResumeWithData`.
//
//   - ctx: The parent context.
//   - resumeData: A map where keys are the unique interrupt point addresses and values are the data
//     to be passed to the corresponding component.
func BatchResumeWithData(ctx context.Context, resumeData map[string]any) context.Context {
	rInfo, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	if !ok {
		// Create a new resumeInfo and copy the map to prevent external mutation.
		newMap := make(map[string]any, len(resumeData))
		for k, v := range resumeData {
			newMap[k] = v
		}
		return context.WithValue(ctx, interruptCtxKey{}, &resumeInfo{
			interruptID2ResumeData:     newMap,
			interruptID2ResumeDataUsed: make(map[string]bool),
		})
	}

	rInfo.mu.Lock()
	defer rInfo.mu.Unlock()
	if rInfo.interruptID2ResumeData == nil {
		rInfo.interruptID2ResumeData = make(map[string]any)
	}
	for id, data := range resumeData {
		rInfo.interruptID2ResumeData[id] = data
	}
	return ctx
}

type runCtxKey struct{}

type interruptState struct {
	Interrupted bool
	State       any
}

type interruptStateForAddress struct {
	Addr Address
	S    *interruptState
	Used bool
}

type resumeInfo struct {
	mu                         sync.Mutex
	interruptID2ResumeData     map[string]any
	interruptID2ResumeDataUsed map[string]bool
	interruptPoints            []*interruptStateForAddress
}

type interruptCtxKey struct{}

type runCtx struct {
	addr          Address
	interruptData *interruptState
	resumeData    any
	isResumeFlow  bool
}

func getNodePath(ctx context.Context) (*NodePath, bool) {
	currentAddress, existed := GetCurrentAddress(ctx)
	if !existed {
		return nil, false
	}

	nodePath := make([]string, 0, len(currentAddress))
	for _, p := range currentAddress {
		if p.Type == AddressSegmentRunnable {
			nodePath = []string{}
			continue
		}

		nodePath = append(nodePath, p.ID)
	}

	return NewNodePath(nodePath...), len(nodePath) > 0
}

// AppendAddressSegment creates a new execution context for a sub-component (e.g., a graph node or a tool call).
//
// It extends the current context's address with a new segment and populates the new context with the
// appropriate interrupt state and resume data for that specific sub-address.
//
//   - ctx: The parent context, typically the one passed into the component's Invoke/Stream method.
//   - segType: The type of the new address segment (e.g., "node", "tool").
//   - segID: The unique ID for the new address segment.
func AppendAddressSegment(ctx context.Context, segType AddressSegmentType, segID string) context.Context {
	// get current address
	currentAddress, existed := GetCurrentAddress(ctx)
	if !existed {
		currentAddress = []AddressSegment{
			{
				Type: segType,
				ID:   segID,
			},
		}
	} else {
		newAddress := make([]AddressSegment, len(currentAddress)+1)
		copy(newAddress, currentAddress)
		newAddress[len(newAddress)-1] = AddressSegment{
			Type: segType,
			ID:   segID,
		}
		currentAddress = newAddress
	}

	runCtx := &runCtx{
		addr: currentAddress,
	}

	rInfo, hasRInfo := getResumeInfo(ctx)
	if !hasRInfo {
		return context.WithValue(ctx, runCtxKey{}, runCtx)
	}

	for _, ip := range rInfo.interruptPoints {
		if ip.Addr.Equals(currentAddress) {
			if !ip.Used {
				runCtx.interruptData = ip.S
				ip.Used = true
				break
			}
		}
	}

	// take from resumeInfo the data for the new address if there is any
	id := currentAddress.String()
	rInfo.mu.Lock()
	defer rInfo.mu.Unlock()
	used := rInfo.interruptID2ResumeDataUsed[id]
	if !used {
		rData, existed := rInfo.interruptID2ResumeData[id]
		if existed {
			rInfo.interruptID2ResumeDataUsed[id] = true
			runCtx.resumeData = rData
			runCtx.isResumeFlow = true
		}
	}

	return context.WithValue(ctx, runCtxKey{}, runCtx)
}

// SetParentAddress returns a new context that contains the given parent address.
// This is used to bridge the address hierarchy between different execution layers,
// such as between a parent package (such as adk package) and the compose package.
// It's important to note that this function will overwrite any previous address
// that may exist in the context.
func SetParentAddress(ctx context.Context, addr Address) context.Context {
	if addr == nil {
		return ctx
	}

	runCtx := &runCtx{
		addr: addr.DeepCopy(),
	}

	return context.WithValue(ctx, runCtxKey{}, runCtx)
}

func getResumeInfo(ctx context.Context) (*resumeInfo, bool) {
	info, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	return info, ok
}

func getRunCtx(ctx context.Context) (*runCtx, bool) {
	rCtx, ok := ctx.Value(runCtxKey{}).(*runCtx)
	return rCtx, ok
}
