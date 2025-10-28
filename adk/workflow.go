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

	"github.com/cloudwego/eino/core"
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

		state := info.InterruptState
		if state == nil {
			panic(fmt.Sprintf("workflowAgent.Resume: agent '%s' was asked to resume but has no state", a.Name(ctx)))
		}

		// Different workflow execution based on the type of our restored state.
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

type WorkflowInterruptInfo struct {
	OrigInput *AgentInput

	SequentialInterruptIndex int
	SequentialInterruptInfo  *InterruptInfo

	LoopIterations int

	ParallelInterruptInfo map[int] /*index*/ *InterruptInfo
}

func (a *workflowAgent) runSequential(ctx context.Context,
	generator *AsyncGenerator[*AgentEvent], seqState *sequentialWorkflowState, info *ResumeInfo,
	opts ...AgentRunOption) (err error) {

	startIdx := 0
	var nextResumeInfo *ResumeInfo

	// seqCtx tracks the accumulated RunPath across the sequence.
	seqCtx := ctx

	// If we are resuming, find which sub-agent to start from and prepare its context.
	if seqState != nil {
		startIdx = seqState.InterruptIndex

		var steps []string
		for i := 0; i <= startIdx; i++ {
			steps = append(steps, a.subAgents[i].Name(seqCtx))
			seqCtx, _, nextResumeInfo, err = getNextResumeAgent(seqCtx, info)
			if err != nil {
				return err
			}
		}

		nextResumeInfo.InterruptInfo = info.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo
		seqCtx = updateRunPathOnly(seqCtx, steps...)
	}

	for i := startIdx; i < len(a.subAgents); i++ {
		subAgent := a.subAgents[i]

		var subIterator *AsyncIterator[*AgentEvent]
		if nextResumeInfo != nil {
			subIterator = subAgent.Resume(seqCtx, nextResumeInfo, opts...)
			nextResumeInfo = nil // Only resume the first time.
		} else {
			seqCtx = updateRunPathOnly(seqCtx, subAgent.Name(seqCtx))
			seqCtx = core.AppendAddressSegment(seqCtx, AddressSegmentAgent, subAgent.Name(seqCtx))
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
				// Use CompositeInterrupt to funnel the sub-interrupt and add our own state.
				// The context for the composite interrupt must be the one from *before* the sub-agent ran.
				event := CompositeInterrupt(ctx, "Sequential workflow interrupted", state,
					lastActionEvent.Action.internalInterrupted)

				// For backward compatibility, populate the deprecated Data field.
				event.Action.Interrupted.Data = &WorkflowInterruptInfo{
					OrigInput:                getRunCtx(ctx).RootInput,
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
	loopState *loopWorkflowState, resumeInfo *ResumeInfo, opts ...AgentRunOption) (err error) {

	if len(a.subAgents) == 0 {
		return nil
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

		// Rebuild the loopCtx to have the correct RunPath up to the point of resumption.
		var steps []string
		for i := 0; i < startIter; i++ {
			for _, subAgent := range a.subAgents {
				steps = append(steps, subAgent.Name(loopCtx))
				loopCtx, _, nextResumeInfo, err = getNextResumeAgent(loopCtx, resumeInfo)
				if err != nil {
					return err
				}
			}
		}
		for i := 0; i <= startIdx; i++ {
			steps = append(steps, a.subAgents[i].Name(loopCtx))
			loopCtx, _, nextResumeInfo, err = getNextResumeAgent(loopCtx, resumeInfo)
			if err != nil {
				return err
			}
		}
		nextResumeInfo.InterruptInfo = resumeInfo.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo
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
				loopCtx = updateRunPathOnly(loopCtx, subAgent.Name(loopCtx))
				loopCtx = core.AppendAddressSegment(loopCtx, AddressSegmentAgent, subAgent.Name(loopCtx))
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

			if lastActionEvent != nil {
				if lastActionEvent.Action.internalInterrupted != nil {
					// A sub-agent interrupted. Wrap it with our own loop state.
					state := &loopWorkflowState{
						LoopIterations: i,
						SubAgentIndex:  j,
					}
					// Use CompositeInterrupt to funnel the sub-interrupt and add our own state.
					event := CompositeInterrupt(ctx, "Loop workflow interrupted", state,
						lastActionEvent.Action.internalInterrupted)

					// For backward compatibility, populate the deprecated Data field.
					event.Action.Interrupted.Data = &WorkflowInterruptInfo{
						OrigInput:                getRunCtx(ctx).RootInput,
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

				if a.doBreakLoopIfNeeded(lastActionEvent.Action, i) {
					generator.Send(lastActionEvent)
					return
				}

				generator.Send(lastActionEvent)
			}
		}

		// Reset the sub-agent index for the next iteration of the outer loop.
		startIdx = 0
	}

	return nil
}

func (a *workflowAgent) runParallel(ctx context.Context, generator *AsyncGenerator[*AgentEvent],
	parState *parallelWorkflowState, resumeInfo *ResumeInfo, opts ...AgentRunOption) error {

	if len(a.subAgents) == 0 {
		return nil
	}

	var (
		wg                   sync.WaitGroup
		subInterruptSignals  []*core.InterruptSignal
		dataMap              = make(map[int]*InterruptInfo)
		mu                   sync.Mutex
		agentName2ResumeInfo map[string]*ResumeInfo
		agentName2Ctx        map[string]context.Context
		err                  error
	)

	// If resuming, get the scoped ResumeInfo for each child that needs to be resumed.
	if parState != nil {
		agentName2Ctx, agentName2ResumeInfo, err = getNextResumeAgents(ctx, resumeInfo)
		if err != nil {
			return err
		}
		// For backward compatibility, populate the deprecated InterruptInfo field.
		for subA, p := range agentName2ResumeInfo {
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

			if nextInfo, ok := agentName2ResumeInfo[agent.Name(ctx)]; ok {
				// This branch was interrupted and needs to be resumed.
				subCtx := agentName2Ctx[agent.Name(ctx)]
				iterator = agent.Resume(subCtx, nextInfo, opts...)
			} else if parState != nil {
				// We are resuming, but this child is not in the next points map.
				// This means it finished successfully, so we don't run it.
				return
			} else {
				subCtx := core.AppendAddressSegment(ctx, AddressSegmentAgent, agent.Name(ctx))
				subCtx = updateRunPathOnly(subCtx, agent.Name(ctx))
				iterator = agent.Run(subCtx, nil, opts...)
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

	if len(subInterruptSignals) > 0 {
		state := &parallelWorkflowState{}
		event := CompositeInterrupt(ctx, "Parallel workflow interrupted", state, subInterruptSignals...)

		// For backward compatibility, populate the deprecated Data field.
		event.Action.Interrupted.Data = &WorkflowInterruptInfo{
			OrigInput:             getRunCtx(ctx).RootInput,
			ParallelInterruptInfo: dataMap,
		}
		event.AgentName = a.Name(ctx)
		event.RunPath = getRunCtx(ctx).RunPath

		generator.Send(event)
	}

	return nil
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
