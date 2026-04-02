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
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

func errorIterator[M MessageType](err error) *AsyncIterator[*TypedAgentEvent[M]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	gen.Send(&TypedAgentEvent[M]{Err: err})
	gen.Close()
	return iter
}

func newUserMessage[M MessageType](query string) (M, error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(query)).(M), nil
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(query)).(M), nil
	default:
		return zero, fmt.Errorf("unsupported message type %T", zero)
	}
}

// TypedRunner is the primary entry point for executing an Agent.
// It manages the agent's lifecycle, including starting, resuming, and checkpointing.
//
// For M = *schema.Message, execution goes through the full flowAgent pipeline
// (multi-agent orchestration, message rewriting, etc.).
// For other M types (e.g. *schema.AgenticMessage), the agent is called directly
// without flowAgent wrapping.
type TypedRunner[M MessageType] struct {
	a               TypedAgent[M]
	enableStreaming bool
	store           CheckPointStore
}

// Runner is the default runner type using *schema.Message.
type Runner = TypedRunner[*schema.Message]

type CheckPointStore = core.CheckPointStore

type CheckPointDeleter = core.CheckPointDeleter

type TypedRunnerConfig[M MessageType] struct {
	Agent           TypedAgent[M]
	EnableStreaming bool

	CheckPointStore CheckPointStore
}

// RunnerConfig is the default runner config type using *schema.Message.
type RunnerConfig = TypedRunnerConfig[*schema.Message]

// ResumeParams contains all parameters needed to resume an execution.
// This struct provides an extensible way to pass resume parameters without
// requiring breaking changes to method signatures.
type ResumeParams struct {
	// Targets contains the addresses of components to be resumed as keys,
	// with their corresponding resume data as values
	Targets map[string]any
	// Future extensible fields can be added here without breaking changes
}

// NewRunner creates a new Runner with the given config.
func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return NewTypedRunner[*schema.Message](conf)
}

// NewTypedRunner creates a new TypedRunner with the given config.
func NewTypedRunner[M MessageType](conf TypedRunnerConfig[M]) *TypedRunner[M] {
	return &TypedRunner[M]{
		enableStreaming: conf.EnableStreaming,
		a:               conf.Agent,
		store:           conf.CheckPointStore,
	}
}

func (r *TypedRunner[M]) Run(ctx context.Context, messages []M,
	opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	return typedRunnerRunImpl(r.a, r.enableStreaming, r.store, ctx, messages, opts...)
}

// Query is a convenience method that starts a new execution with a single user query string.
func (r *TypedRunner[M]) Query(ctx context.Context,
	query string, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	msgs, err := newUserMessage[M](query)
	if err != nil {
		return errorIterator[M](err)
	}
	return r.Run(ctx, []M{msgs}, opts...)
}

// Resume continues an interrupted execution from a checkpoint, using an "Implicit Resume All" strategy.
// This method is best for simpler use cases where the act of resuming implies that all previously
// interrupted points should proceed without specific data.
//
// When using this method, all interrupted agents will receive `isResumeFlow = false` when they
// call `GetResumeContext`, as no specific agent was targeted. This is suitable for the "Simple Confirmation"
// pattern where an agent only needs to know `wasInterrupted` is true to continue.
func (r *TypedRunner[M]) Resume(ctx context.Context, checkPointID string, opts ...AgentRunOption) (
	*AsyncIterator[*TypedAgentEvent[M]], error) {
	return r.resumeInternal(ctx, checkPointID, nil, opts...)
}

// ResumeWithParams continues an interrupted execution from a checkpoint with specific parameters.
// This is the most common and powerful way to resume, allowing you to target specific interrupt points
// (identified by their address/ID) and provide them with data.
//
// The params.Targets map should contain the addresses of the components to be resumed as keys. These addresses
// can point to any interruptible component in the entire execution graph, including ADK agents, compose
// graph nodes, or tools. The value can be the resume data for that component, or `nil` if no data is needed.
//
// When using this method:
//   - Components whose addresses are in the params.Targets map will receive `isResumeFlow = true` when they
//     call `GetResumeContext`.
//   - Interrupted components whose addresses are NOT in the params.Targets map must decide how to proceed:
//     -- "Leaf" components (the actual root causes of the original interrupt) MUST re-interrupt themselves
//     to preserve their state.
//     -- "Composite" agents (like SequentialAgent or ChatModelAgent) should generally proceed with their
//     execution. They act as conduits, allowing the resume signal to flow to their children. They will
//     naturally re-interrupt if one of their interrupted children re-interrupts, as they receive the
//     new `CompositeInterrupt` signal from them.
func (r *TypedRunner[M]) ResumeWithParams(ctx context.Context, checkPointID string, params *ResumeParams, opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	return r.resumeInternal(ctx, checkPointID, params.Targets, opts...)
}

func (r *TypedRunner[M]) resumeInternal(ctx context.Context, checkPointID string, resumeData map[string]any,
	opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	return typedRunnerResumeInternalImpl(r.a, r.enableStreaming, r.store, ctx, checkPointID, resumeData, opts...)
}

func typedRunnerRunImpl[M MessageType](a TypedAgent[M], enableStreaming bool, store CheckPointStore, ctx context.Context, messages []M, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	o := getCommonOptions(nil, opts...)

	fa := toTypedFlowAgent(ctx, a)
	if store != nil {
		fa.checkPointStore = store
	}

	input := &TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: enableStreaming,
	}

	ctx = ctxWithNewTypedRunCtx(ctx, input, o.sharedParentSession)

	AddSessionValues(ctx, o.sessionValues)

	iter := fa.Run(ctx, input, opts...)

	if store == nil && o.cancelCtx == nil {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	go typedRunnerHandleIterImpl(enableStreaming, store, ctx, iter, gen, o.checkPointID, o.cancelCtx)
	return niter
}

func typedRunnerResumeInternalImpl[M MessageType](a TypedAgent[M], enableStreaming bool, store CheckPointStore, ctx context.Context, checkPointID string, resumeData map[string]any, //nolint:revive // argument-limit
	opts ...AgentRunOption) (*AsyncIterator[*TypedAgentEvent[M]], error) {
	if store == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	fa := toTypedFlowAgent(ctx, a)

	ra, ok := TypedAgent[M](fa).(TypedResumableAgent[M])
	if !ok {
		return nil, fmt.Errorf("agent %T does not support resume", a)
	}

	ctx, runCtx, resumeInfo, err := runnerLoadCheckPointImpl(store, ctx, checkPointID)
	if err != nil {
		return nil, fmt.Errorf("failed to load from checkpoint: %w", err)
	}

	o := getCommonOptions(nil, opts...)
	if o.sharedParentSession {
		parentSession := getSession(ctx)
		if parentSession != nil {
			runCtx.Session.Values = parentSession.Values
			runCtx.Session.valuesMtx = parentSession.valuesMtx
		}
	}
	if runCtx.Session.valuesMtx == nil {
		runCtx.Session.valuesMtx = &sync.Mutex{}
	}
	if runCtx.Session.Values == nil {
		runCtx.Session.Values = make(map[string]any)
	}

	ctx = setRunCtx(ctx, runCtx)

	AddSessionValues(ctx, o.sessionValues)

	if len(resumeData) > 0 {
		ctx = core.BatchResumeWithData(ctx, resumeData)
	}

	aIter := ra.Resume(ctx, resumeInfo, opts...)

	niter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	go typedRunnerHandleIterImpl(enableStreaming, store, ctx, aIter, gen, &checkPointID, o.cancelCtx)
	return niter, nil
}

func typedRunnerHandleIterImpl[M MessageType](enableStreaming bool, store CheckPointStore, ctx context.Context, aIter *AsyncIterator[*TypedAgentEvent[M]], //nolint:revive // argument-limit
	gen *AsyncGenerator[*TypedAgentEvent[M]], checkPointID *string, cancelCtx *cancelContext) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&TypedAgentEvent[M]{Err: e})
		}

		gen.Close()
	}()
	var (
		interruptSignal *core.InterruptSignal
		legacyData      any
	)
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		if event.Err != nil {
			var cancelErr *CancelError
			if errors.As(event.Err, &cancelErr) {
				if cancelCtx != nil && cancelCtx.isRoot() && cancelCtx.shouldCancel() {
					cancelCtx.markCancelHandled()
				}
				if cancelErr.interruptSignal != nil && checkPointID != nil {
					cancelErr.CheckPointID = *checkPointID
					cancelErr.InterruptContexts = core.ToInterruptContexts(cancelErr.interruptSignal, allowedAddressSegmentTypes)
					err := runnerSaveCheckPointImpl(enableStreaming, store, ctx, *checkPointID, &InterruptInfo{}, cancelErr.interruptSignal)
					if err != nil {
						gen.Send(&TypedAgentEvent[M]{Err: fmt.Errorf("failed to save checkpoint on cancel: %w", err)})
					}
				}
				gen.Send(event)
				break
			}
		}

		if event.Action != nil && event.Action.internalInterrupted != nil {
			if interruptSignal != nil {
				panic("multiple interrupt actions should not happen in Runner")
			}
			interruptSignal = event.Action.internalInterrupted
			interruptContexts := core.ToInterruptContexts(interruptSignal, allowedAddressSegmentTypes)
			event = &TypedAgentEvent[M]{
				AgentName: event.AgentName,
				RunPath:   event.RunPath,
				Output:    event.Output,
				Action: &AgentAction{
					Interrupted: &InterruptInfo{
						Data:              event.Action.Interrupted.Data,
						InterruptContexts: interruptContexts,
					},
					internalInterrupted: interruptSignal,
				},
			}
			legacyData = event.Action.Interrupted.Data

			if checkPointID != nil {
				err := runnerSaveCheckPointImpl(enableStreaming, store, ctx, *checkPointID, &InterruptInfo{
					Data: legacyData,
				}, interruptSignal)
				if err != nil {
					gen.Send(&TypedAgentEvent[M]{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
				}
			}
		}

		gen.Send(event)
	}
}
