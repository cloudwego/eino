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

func (a *workflowAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
			a.runSequential(ctx, input, generator)
		case workflowAgentModeLoop:
			a.runLoop(ctx, input, generator)
		case workflowAgentModeParallel:
			a.runParallel(ctx, input, generator)
		default:
			err = errors.New(fmt.Sprintf("unsupported workflow agent mode: %d", a.mode))
		}
	}()

	return iterator
}

func (a *workflowAgent) runSequential(ctx context.Context, input *AgentInput,
	generator *AsyncGenerator[*AgentEvent]) (exit bool) {

	for _, subAgent := range a.subAgents {
		subIterator := subAgent.Run(ctx, input)
		for {
			event, ok := subIterator.Next()
			if !ok {
				break
			}

			// Forward the event
			generator.Send(event)

			if event.Err != nil {
				return true
			}

			if event.Action != nil {
				if event.Action.Exit {
					return true
				}
			}
		}
	}

	return false
}

func (a *workflowAgent) runLoop(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent]) {
	if len(a.subAgents) == 0 {
		return
	}

	var iterations int
	for iterations < a.maxIterations || a.maxIterations == 0 {
		iterations++
		exit := a.runSequential(ctx, input, generator)
		if exit {
			return
		}
	}
}

func (a *workflowAgent) runParallel(ctx context.Context, input *AgentInput, generator *AsyncGenerator[*AgentEvent]) {
	if len(a.subAgents) == 0 {
		return
	}

	var wg sync.WaitGroup
	if len(a.subAgents) > 1 {
		for i := 1; i < len(a.subAgents); i++ {
			wg.Add(1)
			go func(agent *flowAgent) {
				defer func() {
					panicErr := recover()
					if panicErr != nil {
						e := safe.NewPanicErr(panicErr, debug.Stack())
						generator.Send(&AgentEvent{Err: e})
					}
					wg.Done()
				}()

				iterator := agent.Run(ctx, input)
				for {
					event, ok := iterator.Next()
					if !ok {
						break
					}
					// Forward the event
					generator.Send(event)
				}
			}(a.subAgents[i])
		}
	}

	agent := a.subAgents[0]
	iterator := agent.Run(ctx, input)
	for {
		event, ok := iterator.Next()
		if !ok {
			break
		}
		// Forward the event
		generator.Send(event)
	}

	if len(a.subAgents) > 1 {
		wg.Wait()
	}
}

type workflowAgentConfig struct {
	Name        string
	Description string
	SubAgents   []Agent
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
		fas[i] = agentWithOptions(ctx, subAgent, WithDisallowTransferToParent())
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
