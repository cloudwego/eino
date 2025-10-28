package core

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/cloudwego/eino/internal/generic"
)

// PathSegmentType defines the type of a segment in an execution path.
type PathSegmentType string

// ExecutionPath represents a full, hierarchical path to a point in the execution structure.
type ExecutionPath []PathSegment

// String converts an ExecutionPath into its unique string representation.
func (p ExecutionPath) String() string {
	if p == nil {
		return ""
	}
	var sb strings.Builder
	for i, s := range p {
		sb.WriteString(string(s.Type))
		sb.WriteString(":")
		sb.WriteString(s.ID)
		if i != len(p)-1 {
			sb.WriteString(";")
		}
	}
	return sb.String()
}

func (p ExecutionPath) Equals(other ExecutionPath) bool {
	if len(p) != len(other) {
		return false
	}
	for i := range p {
		if p[i].Type != other[i].Type || p[i].ID != other[i].ID {
			return false
		}
	}
	return true
}

// PathSegment represents a single segment in the hierarchical path of an execution point.
// A sequence of PathSegments uniquely identifies a location within a potentially nested structure.
type PathSegment struct {
	// ID is the unique identifier for this segment, e.g., the node's key or the tool call's ID.
	ID string
	// Type indicates whether this execution path segment is a graph node, a tool call, an agent, etc.
	Type PathSegmentType
}

type pathCtxKey struct{}

type pathCtx struct {
	path           ExecutionPath
	interruptState *InterruptState
	isResumeTarget bool
	resumeData     any
}

type globalResumeInfoKey struct{}

type globalResumeInfo struct {
	mu                sync.Mutex
	id2ResumeData     map[string]any
	id2ResumeDataUsed map[string]bool
	id2State          map[string]InterruptState
	id2StateUsed      map[string]bool
	id2Path           map[string]ExecutionPath
}

// GetCurrentExecutionPath returns the hierarchical execution path of the currently executing component.
// The execution path is a sequence of segments, each identifying a structural part of the execution
// like an agent, a graph node, or a tool call. This can be useful for logging or debugging.
func GetCurrentExecutionPath(ctx context.Context) ExecutionPath {
	if p, ok := ctx.Value(pathCtxKey{}).(*pathCtx); ok {
		return p.path
	}

	return nil
}

// AppendExecutionPathSegment creates a new execution context for a sub-component (e.g., a graph node or a tool call).
//
// It extends the current context's execution path with a new segment and populates the new context with the
// appropriate interrupt state and resume data for that specific sub-execution path.
//
//   - ctx: The parent context, typically the one passed into the component's Invoke/Stream method.
//   - segType: The type of the new execution path segment (e.g., "node", "tool").
//   - segID: The unique ID for the new execution path segment.
func AppendExecutionPathSegment(ctx context.Context, segType PathSegmentType, segID string) context.Context {
	// get current execution path
	currentPath := GetCurrentExecutionPath(ctx)
	if len(currentPath) == 0 {
		currentPath = []PathSegment{
			{
				Type: segType,
				ID:   segID,
			},
		}
	} else {
		newPath := make([]PathSegment, len(currentPath)+1)
		copy(newPath, currentPath)
		newPath[len(newPath)-1] = PathSegment{
			Type: segType,
			ID:   segID,
		}
		currentPath = newPath
	}

	runCtx := &pathCtx{
		path: currentPath,
	}

	rInfo, hasRInfo := getResumeInfo(ctx)
	if !hasRInfo {
		return context.WithValue(ctx, pathCtxKey{}, runCtx)
	}

	for id, addr := range rInfo.id2Path {
		if addr.Equals(currentPath) {
			if used, ok := rInfo.id2StateUsed[id]; !ok || !used {
				runCtx.interruptState = generic.PtrOf(rInfo.id2State[id])
				rInfo.id2StateUsed[id] = true
				break
			}
		}
	}

	// take from globalResumeInfo the data for the new path if there is any
	id := currentPath.String()
	rInfo.mu.Lock()
	defer rInfo.mu.Unlock()
	used := rInfo.id2ResumeDataUsed[id]
	if !used {
		rData, existed := rInfo.id2ResumeData[id]
		if existed {
			rInfo.id2ResumeDataUsed[id] = true
			runCtx.resumeData = rData
			runCtx.isResumeTarget = true
		}
	}

	return context.WithValue(ctx, pathCtxKey{}, runCtx)
}

// GetNextResumptionPoints finds the immediate child resumption points for a given parent execution path.
func GetNextResumptionPoints(ctx context.Context) (map[string]bool, error) {
	parentPath := GetCurrentExecutionPath(ctx)

	rInfo, exists := getResumeInfo(ctx)
	if !exists {
		return nil, fmt.Errorf("GetNextResumptionPoints: failed to get resume info from context")
	}

	nextPoints := make(map[string]bool)
	parentPathLen := len(parentPath)

	for _, addr := range rInfo.id2Path {
		// Check if path is a potential child (must be longer than parent)
		if len(addr) <= parentPathLen {
			continue
		}

		// Check if it has the parent execution path as a prefix
		var isPrefix bool
		if parentPathLen == 0 {
			isPrefix = true
		} else {
			isPrefix = addr[:parentPathLen].Equals(parentPath)
		}

		if !isPrefix {
			continue
		}

		// We are looking for immediate children.
		// The execution path of an immediate child should be one segment longer.
		childPath := addr[parentPathLen : parentPathLen+1]
		childID := childPath[0].ID

		// Avoid adding duplicates.
		if _, ok := nextPoints[childID]; !ok {
			nextPoints[childID] = true
		}
	}

	return nextPoints, nil
}

// BatchResumeWithData is the core function for preparing a resume context. It injects a map
// of resume targets and their corresponding data into the context.
//
// The `resumeData` map should contain the interrupt IDs (which are the string form of execution paths) of the
// components to be resumed as keys. The value can be the resume data for that component, or `nil`
// if no data is needed (equivalent to using `Resume`).
//
// This function is the foundation for the "Explicit Targeted Resume" strategy. Components whose interrupt IDs
// are present as keys in the map will receive `isResumeTarget = true` when they call `GetResumeContext`.
func BatchResumeWithData(ctx context.Context, resumeData map[string]any) context.Context {
	rInfo, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	if !ok {
		// Create a new globalResumeInfo and copy the map to prevent external mutation.
		newMap := make(map[string]any, len(resumeData))
		for k, v := range resumeData {
			newMap[k] = v
		}
		return context.WithValue(ctx, globalResumeInfoKey{}, &globalResumeInfo{
			id2ResumeData:     newMap,
			id2ResumeDataUsed: make(map[string]bool),
			id2StateUsed:      make(map[string]bool),
		})
	}

	rInfo.mu.Lock()
	defer rInfo.mu.Unlock()
	if rInfo.id2ResumeData == nil {
		rInfo.id2ResumeData = make(map[string]any)
	}
	for id, data := range resumeData {
		rInfo.id2ResumeData[id] = data
	}
	return ctx
}

func PopulateInterruptState(ctx context.Context, id2Path map[string]ExecutionPath,
	id2State map[string]InterruptState) context.Context {
	rInfo, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	if ok {
		if rInfo.id2Path == nil {
			rInfo.id2Path = make(map[string]ExecutionPath)
		}
		for id, path := range id2Path {
			rInfo.id2Path[id] = path
		}
		rInfo.id2State = id2State
	} else {
		rInfo = &globalResumeInfo{
			id2Path:           id2Path,
			id2State:          id2State,
			id2StateUsed:      make(map[string]bool),
			id2ResumeDataUsed: make(map[string]bool),
		}
		ctx = context.WithValue(ctx, globalResumeInfoKey{}, rInfo)
	}
	return ctx
}

func getResumeInfo(ctx context.Context) (*globalResumeInfo, bool) {
	info, ok := ctx.Value(globalResumeInfoKey{}).(*globalResumeInfo)
	return info, ok
}

type InterruptInfo struct {
	Info        any
	IsRootCause bool
}

func (i *InterruptInfo) String() string {
	if i == nil {
		return ""
	}
	return fmt.Sprintf("interrupt info: Info=%v, IsRootCause=%v", i.Info, i.IsRootCause)
}
