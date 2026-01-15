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

	"github.com/google/uuid"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/schema"
)

// WithInterruptBeforeNodes instructs to interrupt before the given nodes.
func WithInterruptBeforeNodes(nodes []string) GraphCompileOption {
	return func(options *graphCompileOptions) {
		options.interruptBeforeNodes = nodes
	}
}

// WithInterruptAfterNodes instructs to interrupt after the given nodes.
func WithInterruptAfterNodes(nodes []string) GraphCompileOption {
	return func(options *graphCompileOptions) {
		options.interruptAfterNodes = nodes
	}
}

// Deprecated: prefer Interrupt/StatefulInterrupt and CompositeInterrupt.
// If you need to pass the legacy error into CompositeInterrupt, wrap it using WrapInterruptAndRerunIfNeeded first.
var InterruptAndRerun = deprecatedInterruptAndRerun
var deprecatedInterruptAndRerun = errors.New("interrupt and rerun")

// NewInterruptAndRerunErr creates a legacy interrupt-and-rerun error.
// Deprecated: prefer Interrupt(ctx, info) or StatefulInterrupt(ctx, info, state).
// If passing into CompositeInterrupt, wrap using WrapInterruptAndRerunIfNeeded first.
func NewInterruptAndRerunErr(extra any) error {
	return deprecatedInterruptAndRerunErr(extra)
}
func deprecatedInterruptAndRerunErr(extra any) error {
	return &core.InterruptSignal{InterruptInfo: core.InterruptInfo{
		Info:        extra,
		IsRootCause: true,
	}}
}

type wrappedInterruptAndRerun struct {
	ps    Address
	inner error
}

func (w *wrappedInterruptAndRerun) Error() string {
	return fmt.Sprintf("interrupt and rerun at address %s: %s", w.ps.String(), w.inner.Error())
}

func (w *wrappedInterruptAndRerun) Unwrap() error {
	return w.inner
}

// WrapInterruptAndRerunIfNeeded wraps the deprecated old interrupt errors, with the current execution address.
// If the error is returned by either Interrupt, StatefulInterrupt or CompositeInterrupt,
// it will be returned as-is without wrapping
func WrapInterruptAndRerunIfNeeded(ctx context.Context, step AddressSegment, err error) error {
	addr := GetCurrentAddress(ctx)
	newAddr := append(append([]AddressSegment{}, addr...), step)
	if errors.Is(err, deprecatedInterruptAndRerun) {
		return &wrappedInterruptAndRerun{
			ps:    newAddr,
			inner: err,
		}
	}

	ire := &core.InterruptSignal{}
	if errors.As(err, &ire) {
		if ire.Address == nil {
			return &wrappedInterruptAndRerun{
				ps:    newAddr,
				inner: err,
			}
		}
		return ire
	}

	return fmt.Errorf("failed to wrap error as addressed InterruptAndRerun: %w", err)
}

// Interrupt creates a special error that signals the execution engine to interrupt
// the current run at the component's specific address and save a checkpoint.
//
// This is the standard way for a single, non-composite component to signal a resumable interruption.
//
//   - ctx: The context of the running component, used to retrieve the current execution address.
//   - info: User-facing information about the interrupt. This is not persisted but is exposed to the
//     calling application via the InterruptCtx to provide context (e.g., a reason for the pause).
func Interrupt(ctx context.Context, info any) error {
	is, err := core.Interrupt(ctx, info, nil, nil)
	if err != nil {
		return err
	}

	return is
}

// StatefulInterrupt creates a special error that signals the execution engine to interrupt
// the current run at the component's specific address and save a checkpoint.
//
// This is the standard way for a single, non-composite component to signal a resumable interruption.
//
//   - ctx: The context of the running component, used to retrieve the current execution address.
//   - info: User-facing information about the interrupt. This is not persisted but is exposed to the
//     calling application via the InterruptCtx to provide context (e.g., a reason for the pause).
//   - state: The internal state that the interrupting component needs to persist to be able to resume
//     its work later. This state is saved in the checkpoint and will be provided back to the component
//     upon resumption via GetInterruptState.
func StatefulInterrupt(ctx context.Context, info any, state any) error {
	is, err := core.Interrupt(ctx, info, state, nil)
	if err != nil {
		return err
	}

	return is
}

// CompositeInterrupt creates a special error that signals a composite interruption.
// It is designed for "composite" nodes (like ToolsNode) that manage multiple, independent,
// interruptible sub-processes. It bundles multiple sub-interrupt errors into a single error
// that the engine can deconstruct into a flat list of resumable points.
//
// This function is robust and can handle several types of errors from sub-processes:
//
//   - A `Interrupt` or `StatefulInterrupt` error from a simple component.
//
//   - A nested `CompositeInterrupt` error from another composite component.
//
//   - An error containing `InterruptInfo` returned by a `Runnable` (e.g., a Graph within a lambda node).
//
//   - An error returned by \'WrapInterruptAndRerunIfNeeded\' for the legacy old interrupt and rerun error,
//     and for the error returned by the deprecated old interrupt errors.
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
// NOTE: if the error you passed in is the deprecated old interrupt and rerun err, or an error returned by
// the deprecated old interrupt function, you must wrap it using WrapInterruptAndRerunIfNeeded first
// before passing them into this function.
func CompositeInterrupt(ctx context.Context, info any, state any, errs ...error) error {
	if len(errs) == 0 {
		return StatefulInterrupt(ctx, info, state)
	}

	var cErrs []*core.InterruptSignal
	for _, err := range errs {
		wrapped := &wrappedInterruptAndRerun{}
		if errors.As(err, &wrapped) {
			inner := wrapped.Unwrap()
			if errors.Is(inner, deprecatedInterruptAndRerun) {
				id := uuid.NewString()
				cErrs = append(cErrs, &core.InterruptSignal{
					ID:      id,
					Address: wrapped.ps,
					InterruptInfo: core.InterruptInfo{
						Info:        nil,
						IsRootCause: true,
					},
				})
				continue
			}

			ire := &core.InterruptSignal{}
			if errors.As(err, &ire) {
				id := uuid.NewString()
				cErrs = append(cErrs, &core.InterruptSignal{
					ID:      id,
					Address: wrapped.ps,
					InterruptInfo: core.InterruptInfo{
						Info:        ire.InterruptInfo.Info,
						IsRootCause: ire.InterruptInfo.IsRootCause,
					},
					InterruptState: core.InterruptState{
						State: ire.InterruptState.State,
					},
				})
			}

			continue
		}

		ire := &core.InterruptSignal{}
		if errors.As(err, &ire) {
			cErrs = append(cErrs, ire)
			continue
		}

		ie := &interruptError{}
		if errors.As(err, &ie) {
			is := core.FromInterruptContexts(ie.Info.InterruptContexts)
			cErrs = append(cErrs, is)
			continue
		}

		return fmt.Errorf("composite interrupt but one of the sub error is not interrupt and rerun error: %w", err)
	}

	is, err := core.Interrupt(ctx, info, state, cErrs)
	if err != nil {
		return err
	}
	return is
}

// IsInterruptRerunError reports whether the error represents an interrupt-and-rerun
// and returns any attached info.
func IsInterruptRerunError(err error) (any, bool) {
	info, _, ok := isInterruptRerunError(err)
	return info, ok
}

func isInterruptRerunError(err error) (info any, state any, ok bool) {
	if errors.Is(err, deprecatedInterruptAndRerun) {
		return nil, nil, true
	}
	ire := &core.InterruptSignal{}
	if errors.As(err, &ire) {
		return ire.Info, ire.State, true
	}
	return nil, nil, false
}

// InterruptInfo aggregates interrupt metadata for composite or nested runs.
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
	schema.RegisterName[*InterruptInfo]("_eino_compose_interrupt_info")
}

// AddressSegmentType defines the type of a segment in an execution address.
type AddressSegmentType = core.AddressSegmentType

const (
	// AddressSegmentNode represents a segment of an address that corresponds to a graph node.
	AddressSegmentNode AddressSegmentType = "node"
	// AddressSegmentTool represents a segment of an address that corresponds to a specific tool call within a ToolsNode.
	AddressSegmentTool AddressSegmentType = "tool"
	// AddressSegmentRunnable represents a segment of an address that corresponds to an instance of the Runnable interface.
	// Currently the possible Runnable types are: Graph, Workflow and Chain.
	// Note that for sub-graphs added through AddGraphNode to another graph is not a Runnable.
	// So a AddressSegmentRunnable indicates a standalone Root level Graph,
	// or a Root level Graph inside a node such as Lambda node.
	AddressSegmentRunnable AddressSegmentType = "runnable"
)

// Address represents a full, hierarchical address to a point in the execution structure.
type Address = core.Address

// AddressSegment represents a single segment in the hierarchical address of an execution point.
// A sequence of AddressSegments uniquely identifies a location within a potentially nested structure.
type AddressSegment = core.AddressSegment

// InterruptCtx provides a complete, user-facing context for a single, resumable interrupt point.
type InterruptCtx = core.InterruptCtx

// ExtractInterruptInfo extracts InterruptInfo from an error if present.
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

func (e *interruptError) GetInterruptContexts() []*InterruptCtx {
	if e.Info == nil {
		return nil
	}
	return e.Info.InterruptContexts
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

	signal *core.InterruptSignal
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
