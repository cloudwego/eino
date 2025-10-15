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
	// parentAddr is the address of the parent component, used for creating a nested address hierarchy.
	// This is for internal framework use.
	parentAddr *Address
}

type RunnerConfig struct {
	Agent           Agent
	EnableStreaming bool

	CheckPointStore compose.CheckPointStore
}

func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return &Runner{
		enableStreaming: conf.EnableStreaming,
		a:               conf.Agent,
		store:           conf.CheckPointStore,
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

	input := &AgentInput{
		Messages:        messages,
		EnableStreaming: r.enableStreaming,
	}

	ctx = ctxWithNewRunCtx(ctx, r.parentAddr)

	AddSessionValues(ctx, o.sessionValues)

	iter := fa.Run(ctx, input, opts...)
	if r.store == nil {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	addr := GetCurrentAddress(ctx)

	go r.handleIter(ctx, iter, gen, input, addr, getSession(ctx), o.checkPointID)
	return niter
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

// TargetedResume continues an interrupted execution from a checkpoint, using an "Explicit Targeted Resume" strategy.
// This is the most common and powerful way to resume, allowing you to target specific interrupt points
// (identified by their address/ID) and provide them with data.
//
// The `targets` map should contain the addresses of the components to be resumed as keys. These addresses
// can point to any interruptible component in the entire execution graph, including ADK agents, compose
// graph nodes, or tools. The value can be the resume data for that component, or `nil` if no data is needed.
//
// When using this method:
//   - Components whose addresses are in the `targets` map will receive `isResumeFlow = true` when they
//     call `GetResumeContext`.
//   - Interrupted components whose addresses are NOT in the `targets` map must decide how to proceed:
//     -- "Leaf" components (the actual root causes of the original interrupt) MUST re-interrupt themselves
//     to preserve their state.
//     -- "Composite" agents (like SequentialAgent or ChatModelAgent) should generally proceed with their
//     execution. They act as conduits, allowing the resume signal to flow to their children. They will
//     naturally re-interrupt if one of their interrupted children re-interrupts, as they receive the
//     new `CompositeInterrupt` signal from them.
func (r *Runner) TargetedResume(ctx context.Context, checkPointID string, targets map[string]any,
	opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error) {
	return r.resume(ctx, checkPointID, targets, opts...)
}

// resume is the internal implementation for both Resume and TargetedResume.
func (r *Runner) resume(ctx context.Context, checkPointID string, resumeData map[string]any,
	opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error) {
	if r.store == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	runCtx, info, existed, err := getCheckPoint(ctx, r.store, checkPointID)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}
	if !existed {
		return nil, fmt.Errorf("checkpoint[%s] not exist", checkPointID)
	}

	ctx = setRunCtx(ctx, runCtx)

	o := getCommonOptions(nil, opts...)
	AddSessionValues(ctx, o.sessionValues)

	info.ResumeData = resumeData

	aIter := toFlowAgent(ctx, r.a).Resume(ctx, info, opts...)
	if r.store == nil {
		return aIter, nil
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	addr := GetCurrentAddress(ctx)

	go r.handleIter(ctx, aIter, gen, runCtx.RootInput, addr, getSession(ctx), &checkPointID)
	return niter, nil
}

func (r *Runner) handleIter(ctx context.Context, aIter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent],
	rootInput *AgentInput, addr Address, session *runSession, checkPointID *string) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&AgentEvent{Err: e})
		}

		gen.Close()
	}()
	var interruptedInfo *InterruptInfo
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			if interruptedInfo != nil {
				// even if multiple interrupt happens, they should be merged into one
				// action by CompositeInterrupt, so here in Runner we must assume at most
				// one interrupt action happens
				panic("multiple interrupt actions should not happen in Runner")
			}
			interruptedInfo = event.Action.Interrupted
		} else {
			interruptedInfo = nil
		}

		gen.Send(event)
	}

	if interruptedInfo != nil && checkPointID != nil {
		err := r.saveCheckPoint(ctx, r.store, *checkPointID, interruptedInfo, rootInput,
			session, addr)
		if err != nil {
			gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
		}
	}
}
