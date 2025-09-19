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

type ResumeInfo struct {
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

func SetFullResumeInfo(ctx context.Context, info *ResumeInfo) context.Context {
	return context.WithValue(ctx, interruptCtxKey{}, info)
}

func SetResumeInfo(ctx context.Context, id string, info any) context.Context {
	rInfo, ok := ctx.Value(interruptCtxKey{}).(*ResumeInfo)
	if !ok {
		return context.WithValue(ctx, interruptCtxKey{}, &ResumeInfo{
			interruptID2Info: map[string]any{
				id: info,
			},
			interruptID2Used: make(map[string]bool),
		})
	}

	rInfo.mu.Lock()
	rInfo.interruptID2Info[id] = info
	rInfo.mu.Unlock()
	return ctx
}

func getResumeInfoForID(ctx context.Context, id string) (any, bool) {
	info, ok := ctx.Value(interruptCtxKey{}).(*ResumeInfo)
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
	rInfo, ok := ctx.Value(interruptCtxKey{}).(*ResumeInfo)
	if !ok {
		return nil
	}

	return rInfo.cp
}

func GetRunCtx(ctx context.Context) (*RunCtx, bool) {
	rCtx, ok := ctx.Value(runCtxKey{}).(*RunCtx)
	return rCtx, ok
}
