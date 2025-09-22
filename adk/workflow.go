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
	"reflect"
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/safe"
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

func (a *workflowAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
			a.runSequential(ctx, input, generator, nil, 0, opts...)
		case workflowAgentModeLoop:
			a.runLoop(ctx, input, generator, nil, opts...)
		case workflowAgentModeParallel:
			a.runParallel(ctx, input, generator, nil, opts...)
		default:
			err = errors.New(fmt.Sprintf("unsupported workflow agent mode: %d", a.mode))
		}
	}()

	return iterator
}

func (a *workflowAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	wi, ok := info.Data.(*WorkflowInterruptInfo)
	if !ok {
		// unreachable
		iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
		generator.Send(&AgentEvent{Err: fmt.Errorf("type of InterruptInfo.Data is expected to %s, actual: %T", reflect.TypeOf((*WorkflowInterruptInfo)(nil)).String(), info.Data)})
		generator.Close()

		return iterator
	}

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
			a.runSequential(ctx, wi.OrigInput, generator, wi, 0, opts...)
		case workflowAgentModeLoop:
			a.runLoop(ctx, wi.OrigInput, generator, wi, opts...)
		case workflowAgentModeParallel:
			a.runParallel(ctx, wi.OrigInput, generator, wi, opts...)
		default:
			err = errors.New(fmt.Sprintf("unsupported workflow agent mode: %d", a.mode))
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

func (a *workflowAgent) runSequential(ctx context.Context, input *AgentInput,
	generator *AsyncGenerator[*AgentEvent], intInfo *WorkflowInterruptInfo, iterations int /*passed by loop agent*/, opts ...AgentRunOption) (exit, interrupted bool) {
	var runPath []RunStep // reconstruct RunPath each loop
	if iterations > 0 {
		runPath = make([]RunStep, 0, (iterations+1)*len(a.subAgents))
		for iter := 0; iter < iterations; iter++ {
			for j := 0; j < len(a.subAgents); j++ {
				runPath = append(runPath, RunStep{
					agentName: a.subAgents[j].Name(ctx),
				})
			}
		}
	}

	i := 0
	if intInfo != nil { // restore previous RunPath
		i = intInfo.SequentialInterruptIndex

		for j := 0; j < i; j++ {
			runPath = append(runPath, RunStep{
				agentName: a.subAgents[j].Name(ctx),
			})
		}
	}

	runCtx := getRunCtx(ctx)
	nRunCtx := runCtx.deepCopy()
	nRunCtx.RunPath = append(nRunCtx.RunPath, runPath...)
	nCtx := setRunCtx(ctx, nRunCtx)

	for ; i < len(a.subAgents); i++ {
		subAgent := a.subAgents[i]

		var subIterator *AsyncIterator[*AgentEvent]
		if intInfo != nil && i == intInfo.SequentialInterruptIndex {
			nCtx, nRunCtx = initRunCtx(nCtx, subAgent.Name(nCtx), nRunCtx.RootInput)
			enableStreaming := false
			if runCtx.RootInput != nil {
				enableStreaming = runCtx.RootInput.EnableStreaming
			}
			subIterator = subAgent.Resume(nCtx, &ResumeInfo{
				EnableStreaming: enableStreaming,
				InterruptInfo:   intInfo.SequentialInterruptInfo,
			}, opts...)
		} else {
			subIterator = subAgent.Run(nCtx, input, opts...)
			nCtx, _ = initRunCtx(nCtx, subAgent.Name(nCtx), input)
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
			if lastActionEvent.Action.Interrupted != nil {
				newEvent := wrapWorkflowInterrupt(lastActionEvent, input, i, iterations)

				// Reset run ctx,
				// because the control should be transferred to the workflow agent, not the interrupted agent
				replaceInterruptRunCtx(nCtx, runCtx)

				// Forward the event
				generator.Send(newEvent)
				return true, true
			}

			if lastActionEvent.Action.Exit {
				// Forward the event
				generator.Send(lastActionEvent)
				return true, false
			}
		}
	}

	return false, false
}

func wrapWorkflowInterrupt(e *AgentEvent, origInput *AgentInput, seqIdx int, iterations int) *AgentEvent {
	newEvent := &AgentEvent{
		AgentName: e.AgentName,
		RunPath:   e.RunPath,
		Output:    e.Output,
		Action: &AgentAction{
			Exit:             e.Action.Exit,
			Interrupted:      &InterruptInfo{Data: e.Action.Interrupted.Data},
			TransferToAgent:  e.Action.TransferToAgent,
			CustomizedAction: e.Action.CustomizedAction,
		},
		Err: e.Err,
	}
	newEvent.Action.Interrupted.Data = &WorkflowInterruptInfo{
		OrigInput:                origInput,
		SequentialInterruptIndex: seqIdx,
		SequentialInterruptInfo:  e.Action.Interrupted,
		LoopIterations:           iterations,
	}
	return newEvent
}

func (a *workflowAgent) runLoop(ctx context.Context, input *AgentInput,
	generator *AsyncGenerator[*AgentEvent], intInfo *WorkflowInterruptInfo, opts ...AgentRunOption) {

	if len(a.subAgents) == 0 {
		return
	}
	var iterations int
	if intInfo != nil {
		iterations = intInfo.LoopIterations
	}
	for iterations < a.maxIterations || a.maxIterations == 0 {
		exit, interrupted := a.runSequential(ctx, input, generator, intInfo, iterations, opts...)
		if interrupted {
			return
		}
		if exit {
			return
		}
		intInfo = nil // only effect once
		iterations++
	}
}

func (a *workflowAgent) runParallel(ctx context.Context, input *AgentInput,
	generator *AsyncGenerator[*AgentEvent], intInfo *WorkflowInterruptInfo, opts ...AgentRunOption) {

	if len(a.subAgents) == 0 {
		return
	}

	runners := getRunners(a.subAgents, input, intInfo, opts...)
	var wg sync.WaitGroup
	interruptMap := make(map[int]*InterruptInfo)
	var mu sync.Mutex
	if len(runners) > 1 {
		for i := 1; i < len(runners); i++ {
			wg.Add(1)
			go func(idx int, runner func(ctx context.Context) *AsyncIterator[*AgentEvent]) {
				defer func() {
					panicErr := recover()
					if panicErr != nil {
						e := safe.NewPanicErr(panicErr, debug.Stack())
						generator.Send(&AgentEvent{Err: e})
					}
					wg.Done()
				}()

				iterator := runner(ctx)
				for {
					event, ok := iterator.Next()
					if !ok {
						break
					}
					if event.Action != nil && event.Action.Interrupted != nil {
						mu.Lock()
						interruptMap[idx] = event.Action.Interrupted
						mu.Unlock()
						break
					}
					// Forward the event
					generator.Send(event)
				}
			}(i, runners[i])
		}
	}

	runner := runners[0]
	iterator := runner(ctx)
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			mu.Lock()
			interruptMap[0] = event.Action.Interrupted
			mu.Unlock()
			break
		}
		// Forward the event
		generator.Send(event)
	}

	if len(a.subAgents) > 1 {
		wg.Wait()
	}

	if len(interruptMap) > 0 {
		replaceInterruptRunCtx(ctx, getRunCtx(ctx))
		generator.Send(&AgentEvent{
			AgentName: a.Name(ctx),
			RunPath:   getRunCtx(ctx).RunPath,
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput:             input,
						ParallelInterruptInfo: interruptMap,
					},
				},
			},
		})
	}
}

func getRunners(subAgents []*flowAgent, input *AgentInput, intInfo *WorkflowInterruptInfo, opts ...AgentRunOption) []func(ctx context.Context) *AsyncIterator[*AgentEvent] {
	ret := make([]func(ctx context.Context) *AsyncIterator[*AgentEvent], 0, len(subAgents))
	if intInfo == nil {
		// init run
		for _, subAgent := range subAgents {
			sa := subAgent
			ret = append(ret, func(ctx context.Context) *AsyncIterator[*AgentEvent] {
				return sa.Run(ctx, input, opts...)
			})
		}
		return ret
	}
	// resume
	for i, subAgent := range subAgents {
		sa := subAgent
		info, ok := intInfo.ParallelInterruptInfo[i]
		if !ok {
			// have executed
			continue
		}
		ret = append(ret, func(ctx context.Context) *AsyncIterator[*AgentEvent] {
			nCtx, runCtx := initRunCtx(ctx, sa.Name(ctx), input)
			enableStreaming := false
			if runCtx.RootInput != nil {
				enableStreaming = runCtx.RootInput.EnableStreaming
			}
			return sa.Resume(nCtx, &ResumeInfo{
				EnableStreaming: enableStreaming,
				InterruptInfo:   info,
			}, opts...)
		})
	}
	return ret
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
