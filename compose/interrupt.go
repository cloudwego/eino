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
	"context"
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

// InterruptAndRerun throws a simple stateless and info-less interrupt error.
// Deprecated: use Interrupt(ctx context.Context, info any) error instead.
// If you really needs to use this error as a sub-error for a CompositeInterrupt call,
// wrap it using WrapInterruptAndRerunIfNeeded first.
var InterruptAndRerun = errors.New("interrupt and rerun")

// NewInterruptAndRerunErr creates a stateless interrupt error with info.
// Deprecated: use Interrupt(ctx context.Context, info any) error instead.
// If you really needs to use this error as a sub-error for a CompositeInterrupt call,
// wrap it using WrapInterruptAndRerunIfNeeded first.
func NewInterruptAndRerunErr(extra any) error {
	return &interruptAndRerun{info: extra}
}

type wrappedInterruptAndRerun struct {
	ps    Path
	inner error
}

func (w *wrappedInterruptAndRerun) Error() string {
	return fmt.Sprintf("interrupt and rerun at path %s: %s", w.ps.String(), w.inner.Error())
}

func (w *wrappedInterruptAndRerun) Unwrap() error {
	return w.inner
}

// WrapInterruptAndRerunIfNeeded wraps the InterruptAndRerun error, or the error returned by
// NewInterruptAndRerunErr, with the current path.
// If the error is returned by either Interrupt, StatefulInterrupt or CompositeInterrupt,
// it will be returned as-is without wrapping
func WrapInterruptAndRerunIfNeeded(ctx context.Context, step PathStep, err error) error {
	path, _ := GetCurrentPath(ctx)
	newPath := append(append([]PathStep{}, path...), step)
	if errors.Is(err, InterruptAndRerun) {
		return &wrappedInterruptAndRerun{
			ps:    newPath,
			inner: err,
		}
	}

	ire := &interruptAndRerun{}
	if errors.As(err, &ire) {
		if ire.path == nil {
			return &wrappedInterruptAndRerun{
				ps:    newPath,
				inner: err,
			}
		}
		return ire
	}

	ie := &interruptError{}
	if errors.As(err, &ie) {
		return ie
	}

	return fmt.Errorf("failed to wrap error as pathed InterruptAndRerun: %w", err)
}

// Interrupt creates a special error that signals the graph execution engine to interrupt
// the current run at the component's specific path and save a checkpoint.
//
// This is the standard way for a single, non-composite component to signal a resumable interruption.
//
//   - ctx: The context of the running component, used to retrieve the current execution path.
//   - info: User-facing information about the interrupt. This is not persisted but is exposed to the
//     calling application via the InterruptCtx to provide context (e.g., a reason for the pause).
func Interrupt(ctx context.Context, info any) error {
	var interruptID string
	path, pathExist := GetCurrentPath(ctx)
	if pathExist {
		interruptID = path.String()
	}

	return &interruptAndRerun{info: info, interruptID: &interruptID, path: path}
}

// StatefulInterrupt creates a special error that signals the graph execution engine to interrupt
// the current run at the component's specific path and save a checkpoint.
//
// This is the standard way for a single, non-composite component to signal a resumable interruption.
//
//   - ctx: The context of the running component, used to retrieve the current execution path.
//   - info: User-facing information about the interrupt. This is not persisted but is exposed to the
//     calling application via the InterruptCtx to provide context (e.g., a reason for the pause).
//   - state: The internal state that the interrupting component needs to persist to be able to resume
//     its work later. This state is saved in the checkpoint and will be provided back to the component
//     upon resumption via GetInterruptState.
func StatefulInterrupt(ctx context.Context, info any, state any) error {
	var interruptID string
	path, pathExist := GetCurrentPath(ctx)
	if pathExist {
		interruptID = path.String()
	}

	return &interruptAndRerun{info: info, state: state, interruptID: &interruptID, path: path}
}

type interruptAndRerun struct {
	info        any // for end-user, probably the human-being
	state       any // for persistence, when resuming, use GetInterruptState to fetch it at the interrupt location
	interruptID *string
	path        Path
	errs        []*interruptAndRerun
}

// CompositeInterrupt creates a special error that signals a composite interruption.
// It is designed for "composite" nodes (like ToolsNode) that manage multiple, independent,
// interruptible sub-processes. It bundles multiple sub-interrupt errors into a single error
// that the graph engine can deconstruct into a flat list of resumable points.
//
// This function is robust and can handle several types of errors from sub-processes:
//
//   - A `Interrupt` or `StatefulInterrupt` error from a simple component.
//
//   - A nested `CompositeInterrupt` error from another composite component.
//
//   - An error containing `InterruptInfo` returned by a `Runnable` (e.g., a Graph within a lambda node).
//
//   - An error returned by 'WrapInterruptAndRerunIfNeeded' for the legacy InterruptAndRerun error,
//     and for the error returned by the deprecated NewInterruptAndRerunErr.
//
// Parameters:
//
//   - ctx: The context of the running composite node.
//
//   - info: User-facing information for the composite node itself. Can be nil.
//     This info will be attached to InterruptInfo.RerunNodeExtra.
//     Provided mainly for compatibility purpose as the composite node itself
//     is not an interrupt point with interrupt ID,
//     which means it lacks enough reason to give a user-facing info.
//
//   - state: The state for the composite node itself. Can be nil.
//     This could be useful when the composite node needs to restore state,
//     such as its input (e.g. ToolsNode).
//
//   - errs: a list of errors emitted by sub-processes.
//
// NOTE: if the error you passed in is the deprecated InterruptAndRerun, or an error returned by
// the deprecated NewInterruptAndRerunErr function, you must wrap it using WrapInterruptAndRerunIfNeeded first
// before passing them into this function.
func CompositeInterrupt(ctx context.Context, info any, state any, errs ...error) error {
	path, _ := GetCurrentPath(ctx)
	var cErrs []*interruptAndRerun
	for _, err := range errs {
		wrapped := &wrappedInterruptAndRerun{}
		if errors.As(err, &wrapped) {
			inner := wrapped.Unwrap()
			if errors.Is(inner, InterruptAndRerun) {
				id := wrapped.ps.String()
				cErrs = append(cErrs, &interruptAndRerun{
					path:        wrapped.ps,
					interruptID: &id,
				})
				continue
			}

			ire := &interruptAndRerun{}
			if errors.As(err, &ire) {
				id := wrapped.ps.String()
				cErrs = append(cErrs, &interruptAndRerun{
					path:        wrapped.ps,
					interruptID: &id,
					info:        ire.info,
					state:       ire.state,
				})
			}

			continue
		}

		ire := &interruptAndRerun{}
		if errors.As(err, &ire) {
			cErrs = append(cErrs, ire)
			continue
		}

		ie := &interruptError{}
		if errors.As(err, &ie) {
			for _, subInterruptCtx := range ie.Info.InterruptContexts {
				subIRE := &interruptAndRerun{
					info:        subInterruptCtx.Info,
					interruptID: &subInterruptCtx.ID,
					path:        subInterruptCtx.Path,
				}
				cErrs = append(cErrs, subIRE)
			}
			continue
		}

		return fmt.Errorf("composite interrupt but one of the sub error is not interrupt and rerun error: %w", err)
	}
	return &interruptAndRerun{errs: cErrs, path: path, state: state, info: info}
}

func (i *interruptAndRerun) Error() string {
	return fmt.Sprintf("interrupt and rerun: %v for path: %s", i.info, i.path.String())
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
		return ire.info, ire.state, true
	}
	return nil, nil, false
}

type InterruptInfo struct {
	State             any
	BeforeNodes       []string
	AfterNodes        []string
	RerunNodes        []string
	RerunNodesExtra   map[string]any
	SubGraphs         map[string]*InterruptInfo
	InterruptContexts []*InterruptCtx
}

func init() {
	schema.RegisterName[*InterruptInfo]("_eino_compose_interrupt_info") // TODO: check if this is really needed when refactoring adk resume
}

// PathStepType defines the type of a segment in an interrupt path.
type PathStepType string

const (
	// PathStepNode represents a segment of a path that corresponds to a graph node.
	PathStepNode PathStepType = "node"
	// PathStepTool represents a segment of a path that corresponds to a specific tool call within a ToolsNode.
	PathStepTool PathStepType = "tool"
	// PathStepRunnable represents a segment of a path that corresponds to an instance of the Runnable interface.
	// Currently the possible Runnable types are: Graph, Workflow and Chain.
	// Note that for sub-graphs added through AddGraphNode to another graph is not a Runnable.
	// So a PathStepRunnable indicates a standalone Root level Graph,
	// or a Root level Graph inside a node such as Lambda node.
	PathStepRunnable PathStepType = "runnable"
)

// Path represents a full, hierarchical path to an interrupt point.
type Path []PathStep

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

func (p Path) Equals(other Path) bool {
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

// PathStep represents a single segment in the hierarchical path to an interrupt point.
// A sequence of PathSegments uniquely identifies a location within a potentially nested graph structure.
type PathStep struct {
	// Type indicates whether this path segment is a graph node or a tool call.
	Type PathStepType
	// ID is the unique identifier for this segment, e.g., the node's key or the tool call's ID.
	ID string
}

// InterruptCtx provides a complete, user-facing context for a single, resumable interrupt point.
type InterruptCtx struct {
	// ID is the unique, fully-qualified path to the interrupt point.
	// It is constructed by joining the individual Path segments, e.g., "node:graph_a;node:tools;tool:tool_call_123".
	// This ID should be used when providing resume data via ResumeWithData.
	ID string
	// Path is the structured sequence of PathStep segments that leads to the interrupt point.
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

	InterruptPoints   []*interruptStateForPath
	InterruptContexts []*InterruptCtx
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
