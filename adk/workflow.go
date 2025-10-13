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

type workflowAgent struct {
	name        string
	description string
	subAgents   []*flowAgent

	mode workflowAgentMode

	maxIterations int
}

func (a *workflowAgent) Name(_ context.Context) string {
	return a.name
}

func (a *workflowAgent) Description(_ context.Context) string {
	return a.description
}

func (a *workflowAgent) Run(ctx context.Context, _ *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go func() {

		var err error
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			} else if err != nil {
				generator.Send(&AgentEvent{Err: err})
			}

			generator.Close()
		}()

		// Different workflow execution based on mode
		switch a.mode {
		case workflowAgentModeSequential:
			a.runSequential(ctx, generator, nil, nil, 0, opts...)
		case workflowAgentModeLoop:
			a.runLoop(ctx, generator, nil, nil, opts...)
		case workflowAgentModeParallel:
			a.runParallel(ctx, generator, nil, nil, opts...)
		default:
			err = fmt.Errorf("unsupported workflow agent mode: %d", a.mode)
		}
	}()

	return iterator
}

type sequentialWorkflowState struct {
	InterruptIndex int
}

type parallelWorkflowState struct{}

type loopWorkflowState struct {
	LoopIterations int
	SubAgentIndex  int
}

func init() {
	schema.RegisterName[*sequentialWorkflowState]("eino_adk_sequential_workflow_state")
	schema.RegisterName[*parallelWorkflowState]("eino_adk_parallel_workflow_state")
	schema.RegisterName[*loopWorkflowState]("eino_adk_loop_workflow_state")
}

func (a *workflowAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go func() {
		var err error
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			} else if err != nil {
				generator.Send(&AgentEvent{Err: err})
			}

			generator.Close()
		}()

		// The flowAgent has already guaranteed we were interrupted.
		// We just need to get our state to know how to proceed.
		_, hasState, state := GetInterruptState[any](info, GetCurrentAddress(ctx))
		if !hasState {
			panic(fmt.Sprintf("workflowAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
		}

		// Different workflow execution based on the type of our restored state.
		switch s := state.(type) {
		case *sequentialWorkflowState:
			// A standalone sequential workflow was interrupted. LoopIterations is 0.
			a.runSequential(ctx, generator, s, info, 0, opts...)
		case *parallelWorkflowState:
			a.runParallel(ctx, generator, s, info, opts...)
		case *loopWorkflowState:
			a.runLoop(ctx, generator, s, info, opts...)
		default:
			err = fmt.Errorf("unsupported workflow agent state type: %T", s)
		}
	}()
	return iterator
}

type WorkflowInterruptInfo struct {
	OrigInput *AgentInput

	SequentialInterruptIndex int
	SequentialInterruptInfo  *InterruptInfo

	LoopIterations int

	ParallelInterruptInfo map[int] /*index*/ *InterruptInfo
}

func (a *workflowAgent) runSequential(ctx context.Context,
	generator *AsyncGenerator[*AgentEvent], seqState *sequentialWorkflowState, info *ResumeInfo,
	iterations int /*passed by loop agent*/, opts ...AgentRunOption) (exit, interrupted bool) {

	startIdx := 0
	var nextResumeInfo *ResumeInfo

	// seqCtx tracks the accumulated RunPath across the sequence.
	seqCtx := ctx

	// If we are resuming, find which sub-agent to start from and prepare its context.
	if seqState != nil {
		startIdx = seqState.InterruptIndex

		// Find the next point of resumption from the overall ResumeInfo.
		// The address for the sequential agent is our own address.
		nextPoints := info.getNextResumptionPoints(GetCurrentAddress(ctx))
		if len(nextPoints) > 1 {
			panic("runSequential received multiple resumption points, which is not possible in sequential execution")
		}
		// It's possible there are no next points if the sequential agent itself was the leaf interrupt.
		// In that case, we just restart the sub-agent from the saved index.
		if len(nextPoints) == 1 {
			for _, point := range nextPoints {
				nextResumeInfo = point
				// For backward compatibility, populate the deprecated InterruptInfo field.
				nextResumeInfo.InterruptInfo = info.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo
			}
		}

		// Rebuild the seqCtx to have the correct RunPath up to the point of resumption.
		// This is necessary so the resumed agent gets the correct full RunPath.
		var steps []string
		for i := 0; i < startIdx; i++ {
			steps = append(steps, a.subAgents[i].Name(seqCtx))
		}
		seqCtx = updateRunPathOnly(seqCtx, steps...)
	}

	for i := startIdx; i < len(a.subAgents); i++ {
		subAgent := a.subAgents[i]

		var subIterator *AsyncIterator[*AgentEvent]
		if nextResumeInfo != nil {
			// This is the agent we need to resume. `subAgent.Resume` will call initRunCtx internally.
			// It's critical that it receives the seqCtx with the accumulated RunPath.
			subIterator = subAgent.Resume(seqCtx, nextResumeInfo, opts...)
			nextResumeInfo = nil // Only resume the first time.
		} else {
			subIterator = subAgent.Run(seqCtx, nil, opts...)
		}

		var lastActionEvent *AgentEvent
		for {
			event, ok := subIterator.Next()
			if !ok {
				break
			}

			if event.Err != nil {
				// exit if report error
				generator.Send(event)
				return true, false
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

		// After the agent has finished, update the RunPath for the next iteration.
		// The Address of seqCtx remains that of the parent workflow agent.
		seqCtx = updateRunPathOnly(seqCtx, subAgent.Name(seqCtx))

		if lastActionEvent != nil {
			if lastActionEvent.Action.Interrupted != nil {
				// A sub-agent interrupted. Wrap it with our own state, including the index.
				state := &sequentialWorkflowState{
					InterruptIndex: i,
				}
				// Use CompositeInterrupt to funnel the sub-interrupt and add our own state.
				// The context for the composite interrupt must be the one from *before* the sub-agent ran.
				action := CompositeInterrupt(ctx, "Sequential workflow interrupted", state,
					lastActionEvent.Action.Interrupted)

				// For backward compatibility, populate the deprecated Data field.
				action.Interrupted.Data = &WorkflowInterruptInfo{
					OrigInput:                getRunCtx(ctx).RootInput,
					SequentialInterruptIndex: i,
					SequentialInterruptInfo:  lastActionEvent.Action.Interrupted,
					LoopIterations:           iterations,
				}

				generator.Send(&AgentEvent{
					AgentName: lastActionEvent.AgentName,
					RunPath:   lastActionEvent.RunPath,
					Action:    action,
				})
				return true, true
			}

			if lastActionEvent.Action.Exit {
				// Forward the event
				generator.Send(lastActionEvent)
				return true, false
			}

			if a.doBreakLoopIfNeeded(lastActionEvent.Action, iterations) {
				lastActionEvent.Action.BreakLoop.CurrentIterations = iterations
				generator.Send(lastActionEvent)
				return true, false
			}

			generator.Send(lastActionEvent)
		}
	}

	return false, false
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

func (a *workflowAgent) doBreakLoopIfNeeded(aa *AgentAction, iterations int) bool {
	if a.mode != workflowAgentModeLoop {
		return false
	}

	if aa != nil && aa.BreakLoop != nil && !aa.BreakLoop.Done {
		aa.BreakLoop.Done = true
		aa.BreakLoop.CurrentIterations = iterations
		return true
	}
	return false
}

func (a *workflowAgent) runLoop(ctx context.Context, generator *AsyncGenerator[*AgentEvent],
	loopState *loopWorkflowState, resumeInfo *ResumeInfo, opts ...AgentRunOption) {

	if len(a.subAgents) == 0 {
		return
	}

	startIter := 0
	startIdx := 0
	var nextResumeInfo *ResumeInfo

	// loopCtx tracks the accumulated RunPath across the full sequence within a single iteration.
	loopCtx := ctx

	if loopState != nil {
		// We are resuming.
		startIter = loopState.LoopIterations
		startIdx = loopState.SubAgentIndex

		// Find the next point of resumption from the overall ResumeInfo
		nextPoints := resumeInfo.getNextResumptionPoints(GetCurrentAddress(ctx))
		if len(nextPoints) > 1 {
			panic("runLoop received multiple resumption points, which is not possible")
		}
		if len(nextPoints) == 1 {
			for _, point := range nextPoints {
				nextResumeInfo = point
				// For backward compatibility, populate the deprecated InterruptInfo field.
				nextResumeInfo.InterruptInfo = resumeInfo.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo
			}
		}

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

			var subIterator *AsyncIterator[*AgentEvent]
			if nextResumeInfo != nil {
				// This is the agent we need to resume.
				subIterator = subAgent.Resume(loopCtx, nextResumeInfo, opts...)
				nextResumeInfo = nil // Only resume the first time.
			} else {
				// The input parameter is ignored as the sub-agent will generate its own input from the context.
				subIterator = subAgent.Run(loopCtx, nil, opts...)
			}

			var lastActionEvent *AgentEvent
			for {
				event, ok := subIterator.Next()
				if !ok {
					break
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

			// After the agent has finished, update the RunPath for the next agent in the sequence.
			loopCtx = updateRunPathOnly(loopCtx, subAgent.Name(loopCtx))

			if lastActionEvent != nil {
				if lastActionEvent.Action.Interrupted != nil {
					// A sub-agent interrupted. Wrap it with our own loop state.
					state := &loopWorkflowState{
						LoopIterations: i,
						SubAgentIndex:  j,
					}
					// Use CompositeInterrupt to funnel the sub-interrupt and add our own state.
					action := CompositeInterrupt(ctx, "Loop workflow interrupted", state,
						lastActionEvent.Action.Interrupted)

					// For backward compatibility, populate the deprecated Data field.
					action.Interrupted.Data = &WorkflowInterruptInfo{
						OrigInput:                getRunCtx(ctx).RootInput,
						LoopIterations:           i,
						SequentialInterruptIndex: j,
						SequentialInterruptInfo:  lastActionEvent.Action.Interrupted,
					}

					generator.Send(&AgentEvent{
						AgentName: lastActionEvent.AgentName,
						RunPath:   lastActionEvent.RunPath,
						Action:    action,
					})
					return // Exit the whole loop.
				}

				if lastActionEvent.Action.Exit {
					// Forward the event and exit the entire loop.
					generator.Send(lastActionEvent)
					return
				}
			}
		}

		// Reset the sub-agent index for the next iteration of the outer loop.
		startIdx = 0
	}
}

func (a *workflowAgent) runParallel(ctx context.Context, generator *AsyncGenerator[*AgentEvent],
	parState *parallelWorkflowState, resumeInfo *ResumeInfo, opts ...AgentRunOption) {

	if len(a.subAgents) == 0 {
		return
	}

	var wg sync.WaitGroup
	interruptMap := make(map[int]*InterruptInfo)
	var mu sync.Mutex

	// If resuming, get the scoped ResumeInfo for each child that needs to be resumed.
	nextPoints := make(map[string]*ResumeInfo)
	if parState != nil {
		nextPoints = resumeInfo.getNextResumptionPoints(GetCurrentAddress(ctx))
		// For backward compatibility, populate the deprecated InterruptInfo field.
		for subA, p := range nextPoints {
			var index int
			for i, a := range a.subAgents {
				if a.Name(ctx) == subA {
					index = i
					break
				}
			}
			p.InterruptInfo = resumeInfo.Data.(*WorkflowInterruptInfo).ParallelInterruptInfo[index]
		}
	}

	for i := range a.subAgents {
		wg.Add(1)
		go func(idx int, agent *flowAgent) {
			defer func() {
				panicErr := recover()
				if panicErr != nil {
					e := safe.NewPanicErr(panicErr, debug.Stack())
					generator.Send(&AgentEvent{Err: e})
				}
				wg.Done()
			}()

			var iterator *AsyncIterator[*AgentEvent]

			if nextInfo, ok := nextPoints[agent.Name(ctx)]; ok {
				// This branch was interrupted and needs to be resumed.
				iterator = agent.Resume(ctx, nextInfo, opts...)
			} else if parState != nil {
				// We are resuming, but this child is not in the next points map.
				// This means it finished successfully, so we don't run it.
				return
			} else {
				// This is a fresh run, so run all branches.
				// The input parameter is ignored as the sub-agent will generate its own input from the context.
				iterator = agent.Run(ctx, nil, opts...)
			}

			for {
				event, ok := iterator.Next()
				if !ok {
					break
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					mu.Lock()
					// We only need to store the raw interrupt info to pass to the parent.
					// The index is not part of the state.
					interruptMap[idx] = event.Action.Interrupted
					mu.Unlock()
					break // Stop processing this branch after an interrupt.
				}
				// Forward the event
				generator.Send(event)
			}
		}(i, a.subAgents[i])
	}

	wg.Wait()

	if len(interruptMap) > 0 {
		// One or more parallel branches interrupted. Create a composite interrupt.
		state := &parallelWorkflowState{}
		// We need to collect all the raw sub-interrupts to pass to CompositeInterrupt
		var allSubInterrupts []*InterruptInfo
		for _, subInterrupt := range interruptMap {
			allSubInterrupts = append(allSubInterrupts, subInterrupt)
		}
		action := CompositeInterrupt(ctx, "Parallel workflow interrupted", state, allSubInterrupts...)

		// For backward compatibility, populate the deprecated Data field.
		action.Interrupted.Data = &WorkflowInterruptInfo{
			OrigInput:             getRunCtx(ctx).RootInput,
			ParallelInterruptInfo: interruptMap,
		}

		generator.Send(&AgentEvent{
			AgentName: a.Name(ctx),
			RunPath:   getRunCtx(ctx).RunPath,
			Action:    action,
		})
	}
}

type SequentialAgentConfig struct {
	Name        string
	Description string
	SubAgents   []Agent
}

type ParallelAgentConfig struct {
	Name        string
	Description string
	SubAgents   []Agent
}

type LoopAgentConfig struct {
	Name        string
	Description string
	SubAgents   []Agent

	MaxIterations int
}

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

	wa.subAgents = fa.subAgents

	return fa, nil
}

func NewSequentialAgent(ctx context.Context, config *SequentialAgentConfig) (Agent, error) {
	return newWorkflowAgent(ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeSequential, 0)
}

func NewParallelAgent(ctx context.Context, config *ParallelAgentConfig) (Agent, error) {
	return newWorkflowAgent(ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeParallel, 0)
}

func NewLoopAgent(ctx context.Context, config *LoopAgentConfig) (Agent, error) {
	return newWorkflowAgent(ctx, config.Name, config.Description, config.SubAgents, workflowAgentModeLoop, config.MaxIterations)
}
