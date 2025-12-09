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
	"fmt"
	"runtime/debug"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

// Runner is the primary entry point for executing an Agent.
// It manages the agent's lifecycle, including starting, resuming, and checkpointing.
type Runner struct {
	// a is the agent to be executed.
	a Agent
	// enableStreaming dictates whether the execution should be in streaming mode.
	enableStreaming bool
	// store is the checkpoint store used to persist agent state upon interruption.
	// If nil, checkpointing is disabled.
	store compose.CheckPointStore

	runnerMWHelper         runnerMWHelper
	globalAgentMiddlewares []AgentMiddleware
}

type RunnerConfig struct {
	// Agent is the agent to be executed.
	Agent Agent

	// EnableStreaming dictates whether the execution should be in streaming mode.
	EnableStreaming bool

	// CheckPointStore is the checkpoint store used to persist agent state upon interruption.
	// If nil, checkpointing is disabled.
	CheckPointStore compose.CheckPointStore

	// RunnerMiddlewares provides hooks to customize runner and agent behavior at various stages of execution.
	RunnerMiddlewares []RunnerMiddleware
}

// ResumeParams contains all parameters needed to resume an execution.
// This struct provides an extensible way to pass resume parameters without
// requiring breaking changes to method signatures.
type ResumeParams struct {
	// Targets contains the addresses of components to be resumed as keys,
	// with their corresponding resume data as values
	Targets map[string]any
	// Future extensible fields can be added here without breaking changes
}

func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	var (
		dedup    = map[string]struct{}{}
		mwHelper = runnerMWHelper{}
		agentMWs []AgentMiddleware
	)
	for _, m := range conf.RunnerMiddlewares {
		if _, found := dedup[m.Name]; m.Name != "" && found {
			continue
		}
		dedup[m.Name] = struct{}{}
		mwHelper.beforeRunnerFns = append(mwHelper.beforeRunnerFns, m.BeforeRunner)
		mwHelper.onEventsFns = append(mwHelper.onEventsFns, m.OnEvents)
		if m.GlobalAgentMiddleware != nil {
			agentMWs = append(agentMWs, *m.GlobalAgentMiddleware)
		}
	}
	return &Runner{
		enableStreaming:        conf.EnableStreaming,
		a:                      conf.Agent,
		store:                  conf.CheckPointStore,
		runnerMWHelper:         mwHelper,
		globalAgentMiddlewares: agentMWs,
	}
}

// Run starts a new execution of the agent with a given set of messages.
// It returns an iterator that yields agent events as they occur.
// If the Runner was configured with a CheckPointStore, it will automatically save the agent's state
// upon interruption.
func (r *Runner) Run(ctx context.Context, messages []Message,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	o := getCommonOptions(nil, opts...)

	fa := toFlowAgent(ctx, r.a)

	rc := &RunnerContext{
		Messages:        messages,
		AgentRunOptions: opts,
		agentName:       r.a.Name(ctx),
		entrance:        EntranceTypeRun,
		checkpointID:    o.checkPointID,
	}

	var termIter *AsyncIterator[*AgentEvent]
	ctx, termIter = r.runnerMWHelper.execBeforeRunner(ctx, rc)
	if termIter != nil {
		return termIter
	}

	input := &AgentInput{
		Messages:        rc.Messages,
		EnableStreaming: r.enableStreaming,
	}
	ctx = ctxWithNewRunCtx(ctx, input)
	ctx = r.initAgentMWCtx(ctx, r.globalAgentMiddlewares)

	AddSessionValues(ctx, o.sessionValues)

	iter := fa.Run(ctx, input, rc.AgentRunOptions...)
	if r.store == nil {
		return r.runnerMWHelper.execOnEvents(ctx, rc, iter)
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, iter, gen, o.checkPointID)

	return r.runnerMWHelper.execOnEvents(ctx, rc, niter)
}

// Query is a convenience method that starts a new execution with a single user query string.
func (r *Runner) Query(ctx context.Context,
	query string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	return r.Run(ctx, []Message{schema.UserMessage(query)}, opts...)
}

// Resume continues an interrupted execution from a checkpoint, using an "Implicit Resume All" strategy.
// This method is best for simpler use cases where the act of resuming implies that all previously
// interrupted points should proceed without specific data.
//
// When using this method, all interrupted agents will receive `isResumeFlow = false` when they
// call `GetResumeContext`, as no specific agent was targeted. This is suitable for the "Simple Confirmation"
// pattern where an agent only needs to know `wasInterrupted` is true to continue.
func (r *Runner) Resume(ctx context.Context, checkPointID string, opts ...AgentRunOption) (
	*AsyncIterator[*AgentEvent], error) {
	return r.resume(ctx, checkPointID, nil, opts...)
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
func (r *Runner) ResumeWithParams(ctx context.Context, checkPointID string, params *ResumeParams, opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error) {
	return r.resume(ctx, checkPointID, params.Targets, opts...)
}

// resume is the internal implementation for both Resume and ResumeWithParams.
func (r *Runner) resume(ctx context.Context, checkPointID string, resumeData map[string]any,
	opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error) {
	if r.store == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	ctx, resumeInfo, err := r.loadCheckPoint(ctx, checkPointID)
	if err != nil {
		return nil, fmt.Errorf("failed to load from checkpoint: %w", err)
	}

	rc := &RunnerContext{
		AgentRunOptions: opts,
		agentName:       r.a.Name(ctx),
		entrance:        EntranceTypeResume,
		checkpointID:    &checkPointID,
	}
	var termIter *AsyncIterator[*AgentEvent]
	ctx, termIter = r.runnerMWHelper.execBeforeRunner(ctx, rc)
	if termIter != nil {
		return termIter, nil
	}

	ctx = r.initAgentMWCtx(ctx, r.globalAgentMiddlewares)
	o := getCommonOptions(nil, rc.AgentRunOptions...)
	AddSessionValues(ctx, o.sessionValues)

	if len(resumeData) > 0 {
		ctx = core.BatchResumeWithData(ctx, resumeData)
	}

	fa := toFlowAgent(ctx, r.a)
	aIter := fa.Resume(ctx, resumeInfo, rc.AgentRunOptions...)
	if r.store == nil {
		return r.runnerMWHelper.execOnEvents(ctx, rc, aIter), nil
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, aIter, gen, &checkPointID)

	return r.runnerMWHelper.execOnEvents(ctx, rc, niter), nil
}

func (r *Runner) handleIter(ctx context.Context, aIter *AsyncIterator[*AgentEvent],
	gen *AsyncGenerator[*AgentEvent], checkPointID *string) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&AgentEvent{Err: e})
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

		if event.Action != nil && event.Action.internalInterrupted != nil {
			if interruptSignal != nil {
				// even if multiple interrupt happens, they should be merged into one
				// action by CompositeInterrupt, so here in Runner we must assume at most
				// one interrupt action happens
				panic("multiple interrupt actions should not happen in Runner")
			}
			interruptSignal = event.Action.internalInterrupted
			interruptContexts := core.ToInterruptContexts(interruptSignal, encapsulateAddress)
			event = &AgentEvent{
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
				// save checkpoint first before sending interrupt event,
				// so when end-user receives interrupt event, they can resume from this checkpoint
				err := r.saveCheckPoint(ctx, *checkPointID, &InterruptInfo{
					Data: legacyData,
				}, interruptSignal)
				if err != nil {
					gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
				}
			}
		}

		gen.Send(event)
	}
}

func (r *Runner) initAgentMWCtx(ctx context.Context, agentMiddlewares []AgentMiddleware) context.Context {
	if len(agentMiddlewares) == 0 {
		return ctx
	}
	if v, ok := ctx.Value(globalAgentMiddlewareCtxKey{}).([]AgentMiddleware); ok && v != nil {
		// has been set, skip
		return ctx
	}
	return context.WithValue(ctx, globalAgentMiddlewareCtxKey{}, agentMiddlewares)
}
