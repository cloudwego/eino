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
	"strings"
	"sync"
)

type runCtxKey struct{}

type interruptState struct {
	Interrupted bool
	State       any
}

type resumeInfo struct {
	mu               sync.Mutex
	interruptID2Info map[string]any
	interruptID2Used map[string]bool
	cp               *checkpoint
}

type interruptCtxKey struct{}

type RunCtx struct {
	Paths         []Path
	InterruptData *interruptState
	ResumeData    any
}

type Paths []Path

func (ps Paths) String() string {
	var sb strings.Builder
	for i, p := range ps {
		sb.WriteString(string(p.Type))
		sb.WriteString(":")
		sb.WriteString(p.ID)
		if i != len(ps)-1 {
			sb.WriteString(";")
		}
	}

	return sb.String()
}

func getPath(ctx context.Context) (Paths, bool) {
	if p, ok := ctx.Value(runCtxKey{}).(*RunCtx); ok {
		return p.Paths, true
	}

	return nil, false
}

func getNodePath(ctx context.Context) (*NodePath, bool) {
	paths, existed := getPath(ctx)
	if !existed {
		return nil, false
	}

	nodePaths := make([]string, 0, len(paths))
	for _, p := range paths {
		if p.Type != PathTypeNode {
			break
		}

		nodePaths = append(nodePaths, p.ID)
	}

	return NewNodePath(nodePaths...), true
}

func setRunCtx(ctx context.Context, pathType PathType, pathID string) context.Context {
	// get current path
	paths, existed := getPath(ctx)
	if !existed {
		paths = []Path{
			{
				Type: pathType,
				ID:   pathID,
			},
		}
	} else {
		newPaths := make([]Path, len(paths)+1)
		copy(newPaths, paths)
		newPaths[len(newPaths)-1] = Path{
			Type: pathType,
			ID:   pathID,
		}
		paths = newPaths
	}

	runCtx := &RunCtx{
		Paths: paths,
	}

	// take from checkpoint the state for the new path if there is any
	cp := getCheckpointFromResumeInfo(ctx)
	if cp != nil {
		is, hasIs := cp.buildInterruptStateForPaths(paths)
		if hasIs {
			runCtx.InterruptData = is
		}
	}

	// take from resumeInfo the state for the new path if there is any
	id := paths.String()
	if info, existed := getResumeInfoForID(ctx, id); existed {
		runCtx.ResumeData = info
	}

	return context.WithValue(ctx, runCtxKey{}, runCtx)
}

func clearRunCtx(ctx context.Context) context.Context {
	return context.WithValue(ctx, runCtxKey{}, nil)
}

// Resume marks a specific interrupt point for resumption without passing any data.
// When the graph is resumed, the component that was interrupted at the given `id` will be re-executed,
// but its `RunCtx.ResumeData` field will be nil.
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
			interruptID2Info: map[string]any{
				id: data,
			},
			interruptID2Used: make(map[string]bool),
		})
	}

	rInfo.mu.Lock()
	rInfo.interruptID2Info[id] = data
	rInfo.mu.Unlock()
	return ctx
}

func getResumeInfoForID(ctx context.Context, id string) (any, bool) {
	info, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	if !ok {
		return nil, false
	}

	info.mu.Lock()
	defer info.mu.Unlock()
	used := info.interruptID2Used[id]
	if used {
		return nil, false
	}

	rInfo, existed := info.interruptID2Info[id]
	if existed {
		info.interruptID2Used[id] = true
	}
	return rInfo, existed
}

func getCheckpointFromResumeInfo(ctx context.Context) *checkpoint {
	rInfo, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	if !ok {
		return nil
	}

	return rInfo.cp
}

func GetRunCtx(ctx context.Context) (*RunCtx, bool) {
	rCtx, ok := ctx.Value(runCtxKey{}).(*RunCtx)
	return rCtx, ok
}

// GetInterruptState provides a type-safe way to check for and retrieve the persisted state from a previous interruption.
// It is the primary function a component should use to understand its past state.
//
// It returns three values:
//   - state (T): The typed state object, if it was provided and matches type `T`.
//   - hasState (bool): True if state was provided during the original interrupt and successfully cast to type `T`.
//   - wasInterrupted (bool): True if the node was part of a previous interruption, regardless of whether state was provided.
func GetInterruptState[T any](ctx context.Context) (state T, hasState bool, wasInterrupted bool) {
	rCtx, ok := GetRunCtx(ctx)
	if !ok || rCtx.InterruptData == nil || !rCtx.InterruptData.Interrupted {
		return
	}

	wasInterrupted = true
	if rCtx.InterruptData.State == nil {
		return
	}

	state, hasState = rCtx.InterruptData.State.(T)
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
	rCtx, ok := GetRunCtx(ctx)
	if !ok {
		return
	}

	// Check if this is a resume flow for the current node
	resumeInfo, ok := ctx.Value(interruptCtxKey{}).(*resumeInfo)
	if !ok || resumeInfo == nil {
		return
	}

	currentID := Paths(rCtx.Paths).String()
	resumeInfo.mu.Lock()
	defer resumeInfo.mu.Unlock()

	resumeVal, isResumeFlow := resumeInfo.interruptID2Info[currentID]
	if !isResumeFlow {
		return
	}

	// It is a resume flow, now check for data
	if resumeVal == nil {
		return // hasData is false
	}

	data, hasData = resumeVal.(T)
	return
}
