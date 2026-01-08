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
	"runtime/debug"

	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

// AgentWithDeterministicTransferTo wraps an agent to transfer to given agents deterministically.
func AgentWithDeterministicTransferTo(_ context.Context, config *DeterministicTransferConfig) Agent {
	if ra, ok := config.Agent.(ResumableAgent); ok {
		return &resumableAgentWithDeterministicTransferTo{
			agent:        ra,
			toAgentNames: config.ToAgentNames,
		}
	}
	return &agentWithDeterministicTransferTo{
		agent:        config.Agent,
		toAgentNames: config.ToAgentNames,
	}
}

type agentWithDeterministicTransferTo struct {
	agent        Agent
	toAgentNames []string
}

func (a *agentWithDeterministicTransferTo) Description(ctx context.Context) string {
	return a.agent.Description(ctx)
}

func (a *agentWithDeterministicTransferTo) Name(ctx context.Context) string {
	return a.agent.Name(ctx)
}

func (a *agentWithDeterministicTransferTo) Run(ctx context.Context,
	input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	/*if _, ok := a.agent.(*flowAgent); ok {
		ctx = ClearRunCtx(ctx)
	}*/

	// TODO: ClearRunCtx is too much for this.
	// What we actually want is just to clear the AgentEvent list from runSession,
	// while keeping the RunPath and SessionValues from runSession intact.

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go appendTransferAction(ctx, aIter, generator, a.toAgentNames)

	return iterator
}

type resumableAgentWithDeterministicTransferTo struct {
	agent        ResumableAgent
	toAgentNames []string
}

func (a *resumableAgentWithDeterministicTransferTo) Description(ctx context.Context) string {
	return a.agent.Description(ctx)
}

func (a *resumableAgentWithDeterministicTransferTo) Name(ctx context.Context) string {
	return a.agent.Name(ctx)
}

func (a *resumableAgentWithDeterministicTransferTo) Run(ctx context.Context,
	input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	/*if _, ok := a.agent.(*flowAgent); ok {
		ctx = ClearRunCtx(ctx)
	}*/

	// TODO: ClearRunCtx is too much for this.
	// What we actually want is just to clear the AgentEvent list from runSession,
	// while keeping the RunPath and SessionValues from runSession intact.

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go appendTransferAction(ctx, aIter, generator, a.toAgentNames)

	return iterator
}

func (a *resumableAgentWithDeterministicTransferTo) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	aIter := a.agent.Resume(ctx, info, opts...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go appendTransferAction(ctx, aIter, generator, a.toAgentNames)

	return iterator
}

func appendTransferAction(ctx context.Context, aIter *AsyncIterator[*AgentEvent], generator *AsyncGenerator[*AgentEvent], toAgentNames []string) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			generator.Send(&AgentEvent{Err: e})
		}

		generator.Close()
	}()

	var (
		interrupted       bool
		exit              bool
		currentRunnerStep *string
	)

	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		generator.Send(event)

		if event.Action != nil && event.Action.Interrupted != nil {
			interrupted = true
		} else {
			interrupted = false
		}

		// if this event is Exit, it could either be:
		// 1. an exit event from current Runner scope: should be honored and skip the deterministic transfer
		// 2. an exit event from nested Runner scope: should be ignored and continue the deterministic transfer
		// How to distinguish the two cases:
		// 1. calculate current Runner scope: the last runner step from current RunPath
		// 2. calculate event Runner scope: the last runner step from event.RunPath, maybe nil
		// 3. check whether they are the same.
		if event.Action != nil && event.Action.Exit {
			var eventRunnerStep string
			if len(event.RunPath) > 0 {
				for i := len(event.RunPath) - 1; i >= 0; i-- {
					if event.RunPath[i].runnerName != "" {
						eventRunnerStep = event.RunPath[i].runnerName
						break
					}
				}
			}

			if eventRunnerStep == "" {
				exit = true
				continue
			}

			if currentRunnerStep == nil {
				runCtx := getRunCtx(ctx)
				for i := len(runCtx.RunPath) - 1; i >= 0; i-- {
					if runCtx.RunPath[i].runnerName != "" {
						currentRunnerStep = &runCtx.RunPath[i].runnerName
						break
					}
				}
			}

			if currentRunnerStep != nil && *currentRunnerStep == eventRunnerStep {
				exit = true
			}
		}
	}

	if interrupted || exit {
		return
	}

	for _, toAgentName := range toAgentNames {
		aMsg, tMsg := GenTransferMessages(ctx, toAgentName)
		aEvent := EventFromMessage(aMsg, nil, schema.Assistant, "")
		generator.Send(aEvent)
		tEvent := EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
		tEvent.Action = &AgentAction{
			TransferToAgent: &TransferToAgentAction{
				DestAgentName: toAgentName,
			},
		}
		generator.Send(tEvent)
	}
}
