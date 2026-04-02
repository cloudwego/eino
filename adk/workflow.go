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
	"sync"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type workflowAgentMode int

const (
	workflowAgentModeUnknown workflowAgentMode = iota
	workflowAgentModeSequential
	workflowAgentModeLoop
	workflowAgentModeParallel
)

type typedWorkflowAgent[M MessageType] struct {
	name        string
	description string
	subAgents   []TypedAgent[M]

	mode workflowAgentMode

	maxIterations int
}

type workflowAgent = typedWorkflowAgent[*schema.Message]

func (a *typedWorkflowAgent[M]) Name(_ context.Context) string {
	return a.name
}

func (a *typedWorkflowAgent[M]) Description(_ context.Context) string {
	return a.description
}

func (a *typedWorkflowAgent[M]) GetType() string {
	switch a.mode {
	case workflowAgentModeSequential:
		return "Sequential"
	case workflowAgentModeParallel:
		return "Parallel"
	case workflowAgentModeLoop:
		return "Loop"
	default:
		return "WorkflowAgent"
	}
}

func (a *typedWorkflowAgent[M]) Run(ctx context.Context, _ *TypedAgentInput[M], opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	go func() {

		var err error
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&TypedAgentEvent[M]{Err: e})
			} else if err != nil {
				generator.Send(&TypedAgentEvent[M]{Err: err})
			}

			generator.Close()
		}()

		switch a.mode {
		case workflowAgentModeSequential:
			err = a.runSequential(ctx, generator, nil, nil, opts...)
		case workflowAgentModeLoop:
			err = a.runLoop(ctx, generator, nil, nil, opts...)
		case workflowAgentModeParallel:
			err = a.runParallel(ctx, generator, nil, nil, opts...)
		default:
			err = fmt.Errorf("unsupported workflow agent mode: %d", a.mode)
		}
	}()

	return iterator
}

type sequentialWorkflowState struct {
	InterruptIndex int
}

type parallelWorkflowState struct {
	SubAgentEvents      map[int][]*agentEventWrapper
	TypedSubAgentEvents any
}

type loopWorkflowState struct {
	LoopIterations int
	SubAgentIndex  int
}

func init() {
	schema.RegisterName[*sequentialWorkflowState]("eino_adk_sequential_workflow_state")
	schema.RegisterName[*parallelWorkflowState]("eino_adk_parallel_workflow_state")
	schema.RegisterName[*loopWorkflowState]("eino_adk_loop_workflow_state")
}

func (a *typedWorkflowAgent[M]) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	go func() {
		var err error
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&TypedAgentEvent[M]{Err: e})
			} else if err != nil {
				generator.Send(&TypedAgentEvent[M]{Err: err})
			}

			generator.Close()
		}()

		state := info.InterruptState
		if state == nil {
			panic(fmt.Sprintf("workflowAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
		}

		switch s := state.(type) {
		case *sequentialWorkflowState:
			err = a.runSequential(ctx, generator, s, info, opts...)
		case *parallelWorkflowState:
			err = a.runParallel(ctx, generator, s, info, opts...)
		case *loopWorkflowState:
			err = a.runLoop(ctx, generator, s, info, opts...)
		default:
			err = fmt.Errorf("unsupported workflow agent state type: %T", s)
		}
	}()
	return iterator
}

// WorkflowInterruptInfo CheckpointSchema: persisted via InterruptInfo.Data (gob).
type WorkflowInterruptInfo struct {
	OrigInput      *AgentInput
	TypedOrigInput any

	SequentialInterruptIndex int
	SequentialInterruptInfo  *InterruptInfo

	LoopIterations int

	ParallelInterruptInfo map[int] /*index*/ *InterruptInfo
}

func (a *typedWorkflowAgent[M]) runSequential(ctx context.Context,
	generator *AsyncGenerator[*TypedAgentEvent[M]], seqState *sequentialWorkflowState, info *ResumeInfo,
	opts ...AgentRunOption) (err error) {

	startIdx := 0

	seqCtx := ctx

	// If we are resuming, find which sub-agent to start from and prepare its context.
	if seqState != nil {
		startIdx = seqState.InterruptIndex

		var steps []string
		for i := 0; i < startIdx; i++ {
			steps = append(steps, a.subAgents[i].Name(seqCtx))
		}

		seqCtx = updateRunPathOnly(seqCtx, steps...)
	}

	for i := startIdx; i < len(a.subAgents); i++ {
		subAgent := a.subAgents[i]

		// Cancel check at transition boundary between sub-agents.
		// Transition boundaries are always safe to cancel at — no sub-agent
		// work is in progress, so any cancel mode is honoured.
		if cancelCtx := getCancelContext(ctx); cancelCtx != nil && cancelCtx.shouldCancel() {
			state := &sequentialWorkflowState{InterruptIndex: i}
			event := typedCancelAtTransition[M](ctx, "Sequential workflow cancel at transition", state)
			generator.Send(event)
			return nil
		}

		var subIterator *AsyncIterator[*TypedAgentEvent[M]]
		if seqState != nil {
			wfInfo, _ := info.Data.(*WorkflowInterruptInfo)
			if wfInfo != nil && wfInfo.SequentialInterruptInfo != nil {
				if ra, ok := subAgent.(TypedResumableAgent[M]); ok {
					subIterator = ra.Resume(seqCtx, &ResumeInfo{
						EnableStreaming: info.EnableStreaming,
						InterruptInfo:   wfInfo.SequentialInterruptInfo,
					}, opts...)
				} else {
					subIterator = subAgent.Run(seqCtx, nil, opts...)
				}
			} else {
				subIterator = subAgent.Run(seqCtx, nil, opts...)
			}
			seqState = nil
		} else {
			subIterator = subAgent.Run(seqCtx, nil, opts...)
		}

		seqCtx = updateRunPathOnly(seqCtx, subAgent.Name(seqCtx))

		var lastActionEvent *TypedAgentEvent[M]
		for {
			event, ok := subIterator.Next()
			if !ok {
				break
			}

			if event.Err != nil {
				// exit if report error
				generator.Send(event)
				return nil
			}

			if lastActionEvent != nil {
				generator.Send(lastActionEvent)
				lastActionEvent = nil
			}

			if event.Action != nil {
				lastActionEvent = event
				continue
			}
			generator.Send(event)
		}

		if lastActionEvent != nil {
			if lastActionEvent.Action.internalInterrupted != nil {
				// A sub-agent interrupted. Wrap it with our own state, including the index.
				state := &sequentialWorkflowState{
					InterruptIndex: i,
				}
				// Use TypedCompositeInterrupt to funnel the sub-interrupt and add our own state.
				// The context for the composite interrupt must be the one from *before* the sub-agent ran.
				event := TypedCompositeInterrupt[M](ctx, "Sequential workflow interrupted", state,
					lastActionEvent.Action.internalInterrupted)

				runCtxHere := getRunCtx(ctx)
				event.Action.Interrupted.Data = &WorkflowInterruptInfo{
					OrigInput:                runCtxHere.RootInput,
					TypedOrigInput:           runCtxHere.TypedRootInput,
					SequentialInterruptIndex: i,
					SequentialInterruptInfo:  lastActionEvent.Action.Interrupted,
				}
				event.AgentName = lastActionEvent.AgentName
				event.RunPath = lastActionEvent.RunPath

				generator.Send(event)
				return nil
			}

			if lastActionEvent.Action.Exit {
				// Forward the event
				generator.Send(lastActionEvent)
				return nil
			}

			generator.Send(lastActionEvent)
		}
	}

	return nil
}

// BreakLoopAction is a programmatic-only agent action used to prematurely
// terminate the execution of a loop workflow agent.
// When a loop workflow agent receives this action from a sub-agent, it will stop its
// current iteration and will not proceed to the next one.
// It will mark the BreakLoopAction as Done, signalling to any 'upper level' loop agent
// that this action has been processed and should be ignored further up.
// This action is not intended to be used by LLMs.
type BreakLoopAction struct {
	// From records the name of the agent that initiated the break loop action.
	From string
	// Done is a state flag that can be used by the framework to mark when the
	// action has been handled.
	Done bool
	// CurrentIterations is populated by the framework to record at which
	// iteration the loop was broken.
	CurrentIterations int
}

// NewBreakLoopAction creates a new BreakLoopAction, signaling a request
// to terminate the current loop.
func NewBreakLoopAction(agentName string) *AgentAction {
	return &AgentAction{BreakLoop: &BreakLoopAction{
		From: agentName,
	}}
}

func (a *typedWorkflowAgent[M]) runLoop(ctx context.Context, generator *AsyncGenerator[*TypedAgentEvent[M]],
	loopState *loopWorkflowState, resumeInfo *ResumeInfo, opts ...AgentRunOption) (err error) {

	if len(a.subAgents) == 0 {
		return nil
	}

	startIter := 0
	startIdx := 0

	loopCtx := ctx

	if loopState != nil {
		// We are resuming.
		startIter = loopState.LoopIterations
		startIdx = loopState.SubAgentIndex

		// Rebuild the loopCtx to have the correct RunPath up to the point of resumption.
		var steps []string
		for i := 0; i < startIter; i++ {
			for _, subAgent := range a.subAgents {
				steps = append(steps, subAgent.Name(loopCtx))
			}
		}
		for i := 0; i < startIdx; i++ {
			steps = append(steps, a.subAgents[i].Name(loopCtx))
		}
		loopCtx = updateRunPathOnly(loopCtx, steps...)
	}

	for i := startIter; i < a.maxIterations || a.maxIterations == 0; i++ {
		for j := startIdx; j < len(a.subAgents); j++ {
			subAgent := a.subAgents[j]

			if cancelCtx := getCancelContext(ctx); cancelCtx != nil && cancelCtx.shouldCancel() {
				state := &loopWorkflowState{LoopIterations: i, SubAgentIndex: j}
				event := typedCancelAtTransition[M](ctx, "Loop workflow cancel at transition", state)
				generator.Send(event)
				return nil
			}

			var subIterator *AsyncIterator[*TypedAgentEvent[M]]
			if loopState != nil {
				wfInfo, _ := resumeInfo.Data.(*WorkflowInterruptInfo)
				if wfInfo != nil && wfInfo.SequentialInterruptInfo != nil {
					if ra, ok := subAgent.(TypedResumableAgent[M]); ok {
						subIterator = ra.Resume(loopCtx, &ResumeInfo{
							EnableStreaming: resumeInfo.EnableStreaming,
							InterruptInfo:   wfInfo.SequentialInterruptInfo,
						}, opts...)
					} else {
						subIterator = subAgent.Run(loopCtx, nil, opts...)
					}
				} else {
					subIterator = subAgent.Run(loopCtx, nil, opts...)
				}
				loopState = nil
			} else {
				subIterator = subAgent.Run(loopCtx, nil, opts...)
			}

			loopCtx = updateRunPathOnly(loopCtx, subAgent.Name(loopCtx))

			var lastActionEvent *TypedAgentEvent[M]
			var breakLoopEvent *TypedAgentEvent[M]
			for {
				event, ok := subIterator.Next()
				if !ok {
					break
				}

				if event.Err != nil {
					generator.Send(event)
					return nil
				}

				if lastActionEvent != nil {
					if lastActionEvent.Action.BreakLoop != nil && !lastActionEvent.Action.BreakLoop.Done {
						lastActionEvent.Action.BreakLoop.Done = true
						lastActionEvent.Action.BreakLoop.CurrentIterations = i
						breakLoopEvent = lastActionEvent
					}
					generator.Send(lastActionEvent)
					lastActionEvent = nil
				}

				if event.Action != nil {
					lastActionEvent = event
					continue
				}
				generator.Send(event)
			}

			if lastActionEvent != nil {
				if lastActionEvent.Action.BreakLoop != nil && !lastActionEvent.Action.BreakLoop.Done {
					lastActionEvent.Action.BreakLoop.Done = true
					lastActionEvent.Action.BreakLoop.CurrentIterations = i
					breakLoopEvent = lastActionEvent
				}

				if lastActionEvent.Action.internalInterrupted != nil {
					state := &loopWorkflowState{
						LoopIterations: i,
						SubAgentIndex:  j,
					}
					event := TypedCompositeInterrupt[M](ctx, "Loop workflow interrupted", state,
						lastActionEvent.Action.internalInterrupted)

					runCtxHere := getRunCtx(ctx)
					event.Action.Interrupted.Data = &WorkflowInterruptInfo{
						OrigInput:                runCtxHere.RootInput,
						TypedOrigInput:           runCtxHere.TypedRootInput,
						LoopIterations:           i,
						SequentialInterruptIndex: j,
						SequentialInterruptInfo:  lastActionEvent.Action.Interrupted,
					}
					event.AgentName = lastActionEvent.AgentName
					event.RunPath = lastActionEvent.RunPath

					generator.Send(event)
					return
				}

				if lastActionEvent.Action.Exit {
					generator.Send(lastActionEvent)
					return
				}

				generator.Send(lastActionEvent)
			}

			if breakLoopEvent != nil {
				return
			}
		}

		// Reset the sub-agent index for the next iteration of the outer loop.
		startIdx = 0
	}

	return nil
}

func (a *typedWorkflowAgent[M]) runParallel(ctx context.Context, generator *AsyncGenerator[*TypedAgentEvent[M]], //nolint:cyclop
	parState *parallelWorkflowState, resumeInfo *ResumeInfo, opts ...AgentRunOption) error {

	if len(a.subAgents) == 0 {
		return nil
	}

	var (
		wg                  sync.WaitGroup
		subInterruptSignals []*core.InterruptSignal
		dataMap             = make(map[int]*InterruptInfo)
		mu                  sync.Mutex
		agentNames          map[string]bool
		err                 error
		childContexts       = make([]context.Context, len(a.subAgents))
	)

	if parState != nil {
		agentNames, err = getNextResumeAgents(ctx, resumeInfo)
		if err != nil {
			return err
		}
	}

	for i := range a.subAgents {
		childContexts[i] = forkTypedRunCtx[M](ctx)

		if parState != nil && parState.SubAgentEvents != nil {
			if existingEvents, ok := parState.SubAgentEvents[i]; ok {
				childRunCtx := getRunCtx(childContexts[i])
				if childRunCtx != nil && childRunCtx.Session != nil {
					if childRunCtx.Session.LaneEvents == nil {
						childRunCtx.Session.LaneEvents = &laneEvents{}
					}
					childRunCtx.Session.LaneEvents.Events = append(childRunCtx.Session.LaneEvents.Events, existingEvents...)
				}
			}
		}

		var zero M
		if _, ok := any(zero).(*schema.Message); !ok {
			if parState != nil && parState.TypedSubAgentEvents != nil {
				if gEvents, ok := parState.TypedSubAgentEvents.(map[int][]*typedAgentEventWrapper[M]); ok {
					if events, ok := gEvents[i]; ok {
						childRunCtx := getRunCtx(childContexts[i])
						if childRunCtx != nil && childRunCtx.Session != nil {
							if gl, ok := childRunCtx.Session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok && gl != nil {
								gl.Events = append(gl.Events, events...)
							}
						}
					}
				}
			}
		}
	}

	if cancelCtx := getCancelContext(ctx); cancelCtx != nil && cancelCtx.shouldCancel() {
		state := &parallelWorkflowState{}
		event := typedCancelAtTransition[M](ctx, "Parallel workflow cancel before spawn", state)
		generator.Send(event)
		return nil
	}

	for i := range a.subAgents {
		wg.Add(1)
		go func(idx int, agent TypedAgent[M]) {
			defer func() {
				panicErr := recover()
				if panicErr != nil {
					e := safe.NewPanicErr(panicErr, debug.Stack())
					generator.Send(&TypedAgentEvent[M]{Err: e})
				}
				wg.Done()
			}()

			var iterator *AsyncIterator[*TypedAgentEvent[M]]

			if _, ok := agentNames[agent.Name(ctx)]; ok {
				childResumeInfo := &ResumeInfo{
					EnableStreaming: resumeInfo.EnableStreaming,
				}
				if wfInfo, ok := resumeInfo.Data.(*WorkflowInterruptInfo); ok && wfInfo != nil {
					childResumeInfo.InterruptInfo = wfInfo.ParallelInterruptInfo[idx]
				}
				if ra, ok := agent.(TypedResumableAgent[M]); ok {
					iterator = ra.Resume(childContexts[idx], childResumeInfo, opts...)
				} else {
					iterator = agent.Run(childContexts[idx], nil, opts...)
				}
			} else if parState != nil {
				return
			} else {
				iterator = agent.Run(childContexts[idx], nil, opts...)
			}

			for {
				event, ok := iterator.Next()
				if !ok {
					break
				}
				if event.Action != nil && event.Action.internalInterrupted != nil {
					mu.Lock()
					subInterruptSignals = append(subInterruptSignals, event.Action.internalInterrupted)
					dataMap[idx] = event.Action.Interrupted
					mu.Unlock()
					break
				}
				generator.Send(event)
			}
		}(i, a.subAgents[i])
	}

	wg.Wait()

	if len(subInterruptSignals) == 0 {
		joinTypedRunCtxs[M](ctx, childContexts...)
		return nil
	}

	if len(subInterruptSignals) > 0 {
		subAgentEvents := make(map[int][]*agentEventWrapper)
		for i, childCtx := range childContexts {
			childRunCtx := getRunCtx(childCtx)
			if childRunCtx != nil && childRunCtx.Session != nil && childRunCtx.Session.LaneEvents != nil {
				subAgentEvents[i] = childRunCtx.Session.LaneEvents.Events
			}
		}

		var TypedSubAgentEvents any
		var zero M
		if _, ok := any(zero).(*schema.Message); !ok {
			gEvents := make(map[int][]*typedAgentEventWrapper[M])
			for i, childCtx := range childContexts {
				childRunCtx := getRunCtx(childCtx)
				if childRunCtx != nil && childRunCtx.Session != nil {
					if gl, ok := childRunCtx.Session.TypedLaneEvents.(*typedLaneEventsOf[M]); ok && gl != nil {
						gEvents[i] = gl.Events
					}
				}
			}
			TypedSubAgentEvents = gEvents
		}

		state := &parallelWorkflowState{
			SubAgentEvents:      subAgentEvents,
			TypedSubAgentEvents: TypedSubAgentEvents,
		}
		event := TypedCompositeInterrupt[M](ctx, "Parallel workflow interrupted", state, subInterruptSignals...)

		runCtxHere := getRunCtx(ctx)
		event.Action.Interrupted.Data = &WorkflowInterruptInfo{
			OrigInput:             runCtxHere.RootInput,
			TypedOrigInput:        runCtxHere.TypedRootInput,
			ParallelInterruptInfo: dataMap,
		}
		event.AgentName = a.Name(ctx)
		event.RunPath = runCtxHere.RunPath

		generator.Send(event)
	}

	return nil
}

func typedCancelAtTransition[M MessageType](ctx context.Context, info string, state any) *TypedAgentEvent[M] {
	is, err := core.Interrupt(ctx, info, state, nil,
		core.WithLayerPayload(getRunCtx(ctx).RunPath))
	if err != nil {
		return &TypedAgentEvent[M]{Err: err}
	}

	contexts := core.ToInterruptContexts(is, allowedAddressSegmentTypes)

	return &TypedAgentEvent[M]{
		Action: &AgentAction{
			Interrupted: &InterruptInfo{
				InterruptContexts: contexts,
			},
			internalInterrupted: is,
		},
	}
}

type TypedSequentialAgentConfig[M MessageType] struct {
	Name        string
	Description string
	SubAgents   []TypedAgent[M]
}

type SequentialAgentConfig = TypedSequentialAgentConfig[*schema.Message]

type TypedParallelAgentConfig[M MessageType] struct {
	Name        string
	Description string
	SubAgents   []TypedAgent[M]
}

type ParallelAgentConfig = TypedParallelAgentConfig[*schema.Message]

type TypedLoopAgentConfig[M MessageType] struct {
	Name        string
	Description string
	SubAgents   []TypedAgent[M]

	MaxIterations int
}

type LoopAgentConfig = TypedLoopAgentConfig[*schema.Message]

func newWorkflowAgent(ctx context.Context, name, desc string,
	subAgents []Agent, mode workflowAgentMode, maxIterations int) (*flowAgent, error) {

	wa := &workflowAgent{
		name:        name,
		description: desc,
		mode:        mode,

		maxIterations: maxIterations,
	}

	fas := make([]Agent, len(subAgents))
	for i, subAgent := range subAgents {
		fas[i] = toFlowAgent(ctx, subAgent, WithDisallowTransferToParent())
	}

	fa, err := setSubAgents(ctx, wa, fas)
	if err != nil {
		return nil, err
	}

	waSubAgents := make([]Agent, len(fa.subAgents))
	for i, sa := range fa.subAgents {
		waSubAgents[i] = sa
	}
	wa.subAgents = waSubAgents

	return fa, nil
}

func newTypedWorkflowAgent[M MessageType](ctx context.Context, name, desc string,
	subAgents []TypedAgent[M], mode workflowAgentMode, maxIterations int) (*typedFlowAgent[M], error) {

	wa := &typedWorkflowAgent[M]{
		name:          name,
		description:   desc,
		mode:          mode,
		maxIterations: maxIterations,
	}

	wrappedSubAgents := make([]TypedAgent[M], len(subAgents))
	for i, subAgent := range subAgents {
		wrappedSubAgents[i] = toTypedFlowAgent(ctx, subAgent, TypedWithDisallowTransferToParent[M]())
	}

	fa, err := typedSetSubAgents(ctx, TypedAgent[M](wa), wrappedSubAgents)
	if err != nil {
		return nil, err
	}

	waSubAgents := make([]TypedAgent[M], len(fa.subAgents))
	for i, sa := range fa.subAgents {
		waSubAgents[i] = sa
	}
	wa.subAgents = waSubAgents

	return fa, nil
}

// NewSequentialAgent creates a new sequential workflow agent with the given config.
func NewSequentialAgent(ctx context.Context, config *SequentialAgentConfig) (ResumableAgent, error) {
	return newWorkflowAgent(ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeSequential, 0)
}

// NewTypedSequentialAgent creates a new TypedSequentialAgent that runs sub-agents in sequence.
func NewTypedSequentialAgent[M MessageType](ctx context.Context, config *TypedSequentialAgentConfig[M]) (TypedResumableAgent[M], error) {
	return newTypedWorkflowAgent[M](ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeSequential, 0)
}

// NewParallelAgent creates a new parallel workflow agent with the given config.
func NewParallelAgent(ctx context.Context, config *ParallelAgentConfig) (ResumableAgent, error) {
	return newWorkflowAgent(ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeParallel, 0)
}

// NewTypedParallelAgent creates a new TypedParallelAgent that runs sub-agents in parallel.
func NewTypedParallelAgent[M MessageType](ctx context.Context, config *TypedParallelAgentConfig[M]) (TypedResumableAgent[M], error) {
	return newTypedWorkflowAgent[M](ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeParallel, 0)
}

// NewLoopAgent creates a new loop workflow agent with the given config.
func NewLoopAgent(ctx context.Context, config *LoopAgentConfig) (ResumableAgent, error) {
	return newWorkflowAgent(ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeLoop, config.MaxIterations)
}

// NewTypedLoopAgent creates a new TypedLoopAgent that runs sub-agents in a loop.
func NewTypedLoopAgent[M MessageType](ctx context.Context, config *TypedLoopAgentConfig[M]) (TypedResumableAgent[M], error) {
	return newTypedWorkflowAgent[M](ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeLoop, config.MaxIterations)
}
