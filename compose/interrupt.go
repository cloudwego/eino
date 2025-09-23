/*
 * Copyright 2024 CloudWeGo Authors
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
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/schema"
)

func WithInterruptBeforeNodes(nodes []string) GraphCompileOption {
	return func(options *graphCompileOptions) {
		options.interruptBeforeNodes = nodes
	}
}

func WithInterruptAfterNodes(nodes []string) GraphCompileOption {
	return func(options *graphCompileOptions) {
		options.interruptAfterNodes = nodes
	}
}

var InterruptAndRerun = errors.New("interrupt and rerun")

func NewInterruptAndRerunErr(extra any) error {
	return &interruptAndRerun{Extra: extra}
}

// NewStatefulInterruptAndRerunErr creates a special error that signals the graph execution engine
// to interrupt the current run and save a checkpoint. It allows a component to provide both user-facing
// context and internal, persistable state.
//
//   - info: User-facing information about the interrupt. This is not persisted in the checkpoint for resumption
//     but is exposed to the calling application via the InterruptCtx to provide context (e.g., a reason for the pause).
//     If the info object implements the CompositeInterruptInfo interface, the graph will use it to generate
//     detailed contexts for multiple sub-interrupts.
//
//   - state: The internal state that the interrupting component needs to persist to be able to resume its work later.
//     This state is saved in the checkpoint and will be provided back to the component upon resumption via runCtx.interruptData.State.
//     If the state object implements the CompositeInterruptState interface, the graph's resume mechanism can
//     query the state of specific internal sub-paths.
func NewStatefulInterruptAndRerunErr(info any, state any) error {
	return &interruptAndRerun{Extra: info, State: state}
}

type interruptAndRerun struct {
	Extra any // for end-user, probably the human-being
	State any // for persistence, when resuming, use GetInterruptState to fetch it at the interrupt location
}

// CompositeInterruptState is an interface for state objects that represent a collection of multiple,
// independent interrupt points within a single component (e.g., a `ToolsNode` where several tools can be interrupted).
// Its methods are used by the graph's internal resume mechanism to query the state of a specific internal sub-path.
// This object is persisted in a checkpoint and passed back to the component upon resumption.
type CompositeInterruptState interface {
	GetSubStateForPath(p PathSegment) (any, bool)
	IsPathInterrupted(p PathSegment) bool
}

// CompositeInterruptInfo is an interface for info objects that can provide structured details about
// multiple internal interrupt points. Its primary role is to allow a complex component (like a `ToolsNode`)
// to generate a detailed list of `InterruptCtx` objects. This list is then presented to the end-user to
// allow for targeted resumption of a specific sub-operation. This object is exposed to the user, not persisted.
type CompositeInterruptInfo interface {
	GetSubInterruptContexts() []*InterruptCtx
}

func (i *interruptAndRerun) Error() string {
	return fmt.Sprintf("interrupt and rerun: %v", i.Extra)
}

func IsInterruptRerunError(err error) (any, bool) {
	info, _, ok := isInterruptRerunError(err)
	return info, ok
}

func isInterruptRerunError(err error) (info any, state any, ok bool) {
	if errors.Is(err, InterruptAndRerun) {
		return nil, nil, true
	}
	ire := &interruptAndRerun{}
	if errors.As(err, &ire) {
		return ire.Extra, ire.State, true
	}
	return nil, nil, false
}

type InterruptInfo struct {
	State           any
	BeforeNodes     []string
	AfterNodes      []string
	RerunNodes      []string
	RerunNodesExtra map[string]any
	SubGraphs       map[string]*InterruptInfo
}

func init() {
	schema.RegisterName[*InterruptInfo]("_eino_compose_interrupt_info") // TODO: check if this is really needed when refactoring adk resume
}

// GetInterruptContexts recursively traverses the InterruptInfo structure to generate a flat list of all
// distinct, resumable interrupt points. Each returned InterruptCtx contains a unique, stable path ID
// that can be used to provide targeted resume data.
//
// This method handles three types of interrupts:
//  1. Sub-graph interrupts: It recursively calls GetInterruptContexts on any sub-graphs and prepends the
//     sub-graph's node key to the path of each context, ensuring a unique, fully-qualified path.
//  2. Simple node interrupts: For a standard node that has been interrupted, it creates a simple context
//     containing just that node's path and its associated user-facing info.
//  3. Composite node interrupts: If a node's "extra" data implements the CompositeInterruptInfo interface
//     (e.g., a ToolsNode), it calls GetSubInterruptContexts on that object to get the list of internal
//     interrupts (e.g., individual tools). It then prepends the composite node's key to each of those
//     paths to create the final, fully-qualified paths.
func (ii *InterruptInfo) GetInterruptContexts() []*InterruptCtx {
	var ctxs []*InterruptCtx
	for subKey, subInfo := range ii.SubGraphs {
		subInterruptCtxs := subInfo.GetInterruptContexts()
		for i := range subInterruptCtxs {
			c := subInterruptCtxs[i]
			c.Path = append([]PathSegment{{Type: PathSegmentNode, ID: subKey}}, c.Path...)
			c.ID = c.Path.String()
			ctxs = append(ctxs, c)
		}
	}

	for _, n := range ii.RerunNodes {
		if _, ok := ii.SubGraphs[n]; ok {
			continue
		}

		extra, ok := ii.RerunNodesExtra[n]
		if !ok {
			ctxs = append(ctxs, &InterruptCtx{
				Path: []PathSegment{},
			})
			continue
		}

		// composite node
		if c, ok := extra.(CompositeInterruptInfo); ok {
			subContexts := c.GetSubInterruptContexts()
			for i := range subContexts {
				c := subContexts[i]
				c.Path = append([]PathSegment{{Type: PathSegmentNode, ID: n}}, c.Path...)
				c.ID = c.Path.String()
				ctxs = append(ctxs, c)
			}
			continue
		}

		// simple node
		ps := Path{
			{
				Type: PathSegmentNode,
				ID:   n,
			},
		}
		ctxs = append(ctxs, &InterruptCtx{
			ID:   ps.String(),
			Path: ps,
			Info: extra,
		})
	}
	return ctxs
}

// PathSegmentType defines the type of a segment in an interrupt path.
type PathSegmentType string

const (
	// PathSegmentNode represents a segment of a path that corresponds to a graph node.
	PathSegmentNode PathSegmentType = "node"
	// PathSegmentTool represents a segment of a path that corresponds to a specific tool call within a ToolsNode.
	PathSegmentTool PathSegmentType = "tool"
)

// Path represents a full, hierarchical path to an interrupt point.
type Path []PathSegment

// String converts a Path into its unique string representation.
func (p Path) String() string {
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

// PathSegment represents a single segment in the hierarchical path to an interrupt point.
// A sequence of PathSegments uniquely identifies a location within a potentially nested graph structure.
type PathSegment struct {
	// Type indicates whether this path segment is a graph node or a tool call.
	Type PathSegmentType
	// ID is the unique identifier for this segment, e.g., the node's key or the tool call's ID.
	ID string
}

// InterruptCtx provides a complete, user-facing context for a single, resumable interrupt point.
type InterruptCtx struct {
	// ID is the unique, fully-qualified path to the interrupt point.
	// It is constructed by joining the individual Path segments, e.g., "node:graph_a;node:tools;tool:tool_call_123".
	// This ID should be used when providing resume data via ResumeWithData.
	ID string
	// Path is the structured sequence of PathSegment segments that leads to the interrupt point.
	Path Path
	// Info is the user-facing information associated with the interrupt, provided by the component that triggered it.
	Info any
}

func ExtractInterruptInfo(err error) (info *InterruptInfo, existed bool) {
	if err == nil {
		return nil, false
	}
	var iE *interruptError
	if errors.As(err, &iE) {
		return iE.Info, true
	}
	var sIE *subGraphInterruptError
	if errors.As(err, &sIE) {
		return sIE.Info, true
	}
	return nil, false
}

type interruptError struct {
	Info *InterruptInfo
}

func (e *interruptError) Error() string {
	return fmt.Sprintf("interrupt happened, info: %+v", e.Info)
}

func isSubGraphInterrupt(err error) *subGraphInterruptError {
	if err == nil {
		return nil
	}
	var iE *subGraphInterruptError
	if errors.As(err, &iE) {
		return iE
	}
	return nil
}

type subGraphInterruptError struct {
	Info       *InterruptInfo
	CheckPoint *checkpoint
}

func (e *subGraphInterruptError) Error() string {
	return fmt.Sprintf("interrupt happened, info: %+v", e.Info)
}

func isInterruptError(err error) bool {
	if _, ok := ExtractInterruptInfo(err); ok {
		return true
	}
	if info := isSubGraphInterrupt(err); info != nil {
		return true
	}
	if _, ok := IsInterruptRerunError(err); ok {
		return true
	}

	return false
}
