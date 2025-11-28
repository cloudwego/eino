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

// Runner orchestrates agent execution, session, and checkpoints.
//
// Behavior summary:
// - Run: loads session history (if SessionService provided) and saves new input messages;
//   outputs are persisted via SessionService while iterating.
// - Resume: resumes from checkpoints only; session history is not loaded during resume,
//   but outputs will still be persisted if SessionService is provided.
//
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
	// sessionService manages session persistence and hooks.
	// If nil, session persistence is disabled.
	sessionService *SessionService
}

type RunnerConfig struct {
	Agent           Agent
	EnableStreaming bool

	CheckPointStore compose.CheckPointStore
	// SessionService is optional. If provided, enables session persistence.
	SessionService *SessionService
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
	return &Runner{
		enableStreaming: conf.EnableStreaming,
		a:               conf.Agent,
		store:           conf.CheckPointStore,
		sessionService:  conf.SessionService,
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

	ctx = ctxWithNewRunCtx(ctx, input)

	AddSessionValues(ctx, o.sessionValues)

	if r.sessionService != nil && o.sessionID != nil {
		// load history from session
		history, err := r.sessionService.load(ctx, *o.sessionID, o.sessionOptions...)
		if err != nil {
			niter, gen := NewAsyncIteratorPair[*AgentEvent]()
			gen.Send(&AgentEvent{Err: fmt.Errorf("failed to load session history: %w", err)})
			gen.Close()
			return niter
		}
		// Add history to session context (not input messages)
		session := getSession(ctx)
		for _, event := range history {
			session.addEvent(event)
		}

		// Save new input messages to session
		if err := r.sessionService.saveInput(ctx, *o.sessionID, messages, o.sessionOptions...); err != nil {
			niter, gen := NewAsyncIteratorPair[*AgentEvent]()
			gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save input to session: %w", err)})
			gen.Close()
			return niter
		}
	}

	iter := fa.Run(ctx, input, opts...)
	if r.store == nil && r.sessionService == nil {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, iter, gen, o.checkPointID, o.sessionID, o.sessionOptions)
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

	o := getCommonOptions(nil, opts...)
	AddSessionValues(ctx, o.sessionValues)

	if len(resumeData) > 0 {
		ctx = core.BatchResumeWithData(ctx, resumeData)
	}

	fa := toFlowAgent(ctx, r.a)
	aIter := fa.Resume(ctx, resumeInfo, opts...)
	if r.store == nil && r.sessionService == nil {
		return aIter, nil
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, aIter, gen, &checkPointID, o.sessionID, o.sessionOptions)
	return niter, nil
}

func (r *Runner) handleIter(ctx context.Context, aIter *AsyncIterator[*AgentEvent],
	gen *AsyncGenerator[*AgentEvent], checkPointID *string, sessionID *string, sessionOptions []StoreOption) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&AgentEvent{Err: e})
		}

		gen.Close()
	}()

	// Wrap iterator with checkpoint handling if needed
	processedIter := r.wrapWithCheckpoint(ctx, aIter, checkPointID)

	// Handle session or just forward events
	if r.sessionService != nil && sessionID != nil {
		// Delegate to SessionService for session handling
		r.sessionService.saveOutput(ctx, *sessionID, processedIter, gen, sessionOptions...)
	} else {
		// Just forward all events
		for {
			item, ok := processedIter.Next()
			if !ok {
				break
			}
			gen.Send(item)
		}
	}
}

// wrapWithCheckpoint wraps an iterator to process checkpoints for interrupt events.
// Returns the original iterator if checkPointID is nil.
func (r *Runner) wrapWithCheckpoint(ctx context.Context, aIter *AsyncIterator[*AgentEvent], checkPointID *string) *AsyncIterator[*AgentEvent] {
	if checkPointID == nil {
		return aIter // No checkpoint, return as-is
	}

	// Create intermediate iterator for checkpoint processing
	niter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		r.processCheckpoints(ctx, aIter, gen, *checkPointID)
	}()
	return niter
}

// processCheckpoints handles checkpoint save logic for interrupt events.
func (r *Runner) processCheckpoints(ctx context.Context, aIter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent], checkPointID string) {
	var (
		interruptSignal *core.InterruptSignal
		legacyData      any
	)

	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		// Handle checkpoint for interrupts
		if event.Action != nil && event.Action.internalInterrupted != nil {
			// Even if multiple interrupt happens, they should be merged into one
			// action by CompositeInterrupt, so here in Runner we must assume at most
			// one interrupt action happens
			if interruptSignal != nil {
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

			err := r.saveCheckPoint(ctx, checkPointID, &InterruptInfo{
				Data: legacyData,
			}, interruptSignal)
			if err != nil {
				gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
			}
		}

		gen.Send(event)
	}
}
