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
//   - state (T): The typed state object, if it was provided and matches type `T`.
//   - hasState (bool): True if state was provided during the original interrupt and successfully cast to type `T`.
//   - wasInterrupted (bool): True if the node was part of a previous interruption, regardless of whether state was provided.
func GetInterruptState[T any](ctx context.Context) (state T, hasState bool, wasInterrupted bool) {
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
//   - data (T): The typed resume data. This is only valid if `hasData` is true.
//   - hasData (bool): True if resume data was provided via `ResumeWithData` and successfully cast to type `T`.
//     It is important to check this flag rather than checking `data == nil`, as the provided data could itself be nil
//     or a non-nil zero value (like 0 or "").
//   - isResumeFlow (bool): True if the current node was the specific target of a `Resume` or `ResumeWithData` call.
func GetResumeContext[T any](ctx context.Context) (data T, hasData bool, isResumeFlow bool) {
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

// GetCurrentPath returns the hierarchical path of the currently executing component.
// The path is a sequence of segments, each identifying a node or a tool call.
// This can be useful for logging or
func GetCurrentPath(ctx context.Context) (Path, bool) {
	if p, ok := ctx.Value(runCtxKey{}).(*runCtx); ok {
		return p.ps, true
	}

	return nil, false
}

// Resume marks a specific interrupt point for resumption without passing any data.
// When the graph is resumed, the component that was interrupted at the given `id` will be re-executed,
// but its `runCtx.resumeData` field will be nil.
//
// - ctx: The parent context.
// - id: The unique ID of the interrupt point, obtained from `InterruptCtx.ID`.
func Resume(ctx context.Context, id string) context.Context {
	return ResumeWithData(ctx, id, nil)
}

// ResumeWithData attaches data to a specific interrupt point for resumption.
// When the graph is resumed, the component that was interrupted at the given `id` will receive this data.
// The component should then use the generic `GetResumeData[T]` function to retrieve this data in a type-safe manner.
//
//   - ctx: The parent context.
//   - id: The unique ID of the interrupt point, obtained from `InterruptCtx.ID`.
//   - data: The data to be passed to the interrupted component. The type of this data must match what the
//     target component expects to receive via `GetResumeData[T]`.
func ResumeWithData(ctx context.Context, id string, data any) context.Context {
	rInfo, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	if !ok {
		return context.WithValue(ctx, interruptCtxKey{}, &resumeInfo{
			interruptID2ResumeData: map[string]any{
				id: data,
			},
			interruptID2ResumeDataUsed: make(map[string]bool),
		})
	}

	rInfo.mu.Lock()
	rInfo.interruptID2ResumeData[id] = data
	rInfo.mu.Unlock()
	return ctx
}

type runCtxKey struct{}

type interruptState struct {
	Interrupted bool
	State       any
}

type interruptStateForPath struct {
	P    Path
	S    *interruptState
	Used bool
}

type resumeInfo struct {
	mu                         sync.Mutex
	interruptID2ResumeData     map[string]any
	interruptID2ResumeDataUsed map[string]bool
	interruptPoints            []*interruptStateForPath
}

type interruptCtxKey struct{}

type runCtx struct {
	ps            Path
	interruptData *interruptState
	resumeData    any
	isResumeFlow  bool
}

func getNodePath(ctx context.Context) (*NodePath, bool) {
	currentPaths, existed := GetCurrentPath(ctx)
	if !existed {
		return nil, false
	}

	nodePath := make([]string, 0, len(currentPaths))
	for _, p := range currentPaths {
		if p.Type == PathStepRunnable {
			nodePath = []string{}
			continue
		}

		nodePath = append(nodePath, p.ID)
	}

	return NewNodePath(nodePath...), len(nodePath) > 0
}

// AppendPathStep creates a new execution context for a sub-component within a custom composite node.
// This is an advanced feature for developers building custom nodes that contain their own interruptible
// sub-processes (e.g., a node that runs multiple sub-tasks in parallel).
//
// It extends the current context's path with a new segment and populates the new context with the
// appropriate interrupt state and resume data for that specific sub-path.
//
//   - ctx: The parent context, typically the one passed into the composite node's Invoke/Stream method.
//   - pathType: The type of the new path segment (e.g., "process", "tool").
//   - pathID: The unique ID for the new path segment.
func AppendPathStep(ctx context.Context, stepType PathStepType, pathID string) context.Context {
	// get current path
	currentPaths, existed := GetCurrentPath(ctx)
	if !existed {
		currentPaths = []PathStep{
			{
				Type: stepType,
				ID:   pathID,
			},
		}
	} else {
		newPaths := make([]PathStep, len(currentPaths)+1)
		copy(newPaths, currentPaths)
		newPaths[len(newPaths)-1] = PathStep{
			Type: stepType,
			ID:   pathID,
		}
		currentPaths = newPaths
	}

	runCtx := &runCtx{
		ps: currentPaths,
	}

	rInfo, hasRInfo := getResumeInfo(ctx)
	if !hasRInfo {
		return context.WithValue(ctx, runCtxKey{}, runCtx)
	}

	for _, ip := range rInfo.interruptPoints {
		if ip.P.Equals(currentPaths) {
			if !ip.Used {
				runCtx.interruptData = ip.S
				ip.Used = true
				break
			}
		}
	}

	// take from resumeInfo the data for the new path if there is any
	id := currentPaths.String()
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

func getResumeInfo(ctx context.Context) (*resumeInfo, bool) {
	info, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	return info, ok
}

func getRunCtx(ctx context.Context) (*runCtx, bool) {
	rCtx, ok := ctx.Value(runCtxKey{}).(*runCtx)
	return rCtx, ok
}
