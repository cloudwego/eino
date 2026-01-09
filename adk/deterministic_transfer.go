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

func init() {
	schema.RegisterName[*deterministicTransferState]("_eino_adk_deterministic_transfer_state")
}

type deterministicTransferState struct {
	CheckpointData []byte
}

func stripCurrentRunnerScope(eventRunPath []RunStep) []RunStep {
	if len(eventRunPath) == 0 {
		return nil
	}

	for i := 1; i < len(eventRunPath); i++ {
		if eventRunPath[i].runnerName != "" {
			return eventRunPath[i:]
		}
	}

	return nil
}

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

	if fa, ok := a.agent.(*flowAgent); ok {
		return runFlowAgentWithRunner(ctx, fa, input, a.toAgentNames, options...)
	}

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

	if fa, ok := a.agent.(*flowAgent); ok {
		return runFlowAgentWithRunner(ctx, fa, input, a.toAgentNames, options...)
	}

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go appendTransferAction(ctx, aIter, generator, a.toAgentNames)

	return iterator
}

func (a *resumableAgentWithDeterministicTransferTo) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if fa, ok := a.agent.(*flowAgent); ok {
		return resumeFlowAgentWithRunner(ctx, fa, info, a.toAgentNames, opts...)
	}

	aIter := a.agent.Resume(ctx, info, opts...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go appendTransferAction(ctx, aIter, generator, a.toAgentNames)

	return iterator
}

func runFlowAgentWithRunner(ctx context.Context, fa *flowAgent, input *AgentInput,
	toAgentNames []string, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	o := getCommonOptions(nil, options...)
	enableStreaming := false
	if o.checkPointID != nil {
		enableStreaming = true
	}

	ms := newBridgeStore()
	runner := &Runner{
		a:               fa,
		enableStreaming: enableStreaming,
		store:           ms,
	}

	var messages []Message
	if input != nil {
		messages = input.Messages
	}

	iter := runner.Run(ctx, messages,
		append(options, WithCheckPointID(bridgeCheckpointID))...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go handleFlowAgentEvents(ctx, iter, generator, ms, toAgentNames)

	return iterator
}

func resumeFlowAgentWithRunner(ctx context.Context, fa *flowAgent, info *ResumeInfo,
	toAgentNames []string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	state, ok := info.InterruptState.(*deterministicTransferState)
	if !ok || state == nil {
		return genErrorIter(fmt.Errorf("invalid interrupt state for flowAgent resume in deterministic transfer"))
	}

	ms := newResumeBridgeStore(state.CheckpointData)
	runner := &Runner{
		a:               fa,
		enableStreaming: info.EnableStreaming,
		store:           ms,
	}

	iter, err := runner.Resume(ctx, bridgeCheckpointID, opts...)
	if err != nil {
		return genErrorIter(err)
	}

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go handleFlowAgentEvents(ctx, iter, generator, ms, toAgentNames)

	return iterator
}

func handleFlowAgentEvents(ctx context.Context, iter *AsyncIterator[*AgentEvent],
	generator *AsyncGenerator[*AgentEvent], ms compose.CheckPointStore, toAgentNames []string) {

	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			generator.Send(&AgentEvent{Err: e})
		}

		generator.Close()
	}()

	var (
		interruptEvent    *AgentEvent
		exit              bool
		currentRunnerStep *string
	)

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		event.RunPath = stripCurrentRunnerScope(event.RunPath)

		if event.Action != nil && event.Action.internalInterrupted != nil {
			interruptEvent = event
			continue
		}

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
				generator.Send(event)
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

		generator.Send(event)
	}

	if interruptEvent != nil && interruptEvent.Action != nil && interruptEvent.Action.internalInterrupted != nil {
		data, _, _ := ms.Get(ctx, bridgeCheckpointID)
		state := &deterministicTransferState{CheckpointData: data}
		compositeEvent := CompositeInterrupt(ctx, "deterministic transfer wrapper interrupted",
			state, interruptEvent.Action.internalInterrupted)
		generator.Send(compositeEvent)
		return
	}

	if exit {
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

		var eventRunnerStep string
		if event.Action != nil && event.Action.Exit && len(event.RunPath) > 0 {
			for i := len(event.RunPath) - 1; i >= 0; i-- {
				if event.RunPath[i].runnerName != "" {
					eventRunnerStep = event.RunPath[i].runnerName
					break
				}
			}
		}

		generator.Send(event)

		if event.Action != nil && event.Action.Interrupted != nil {
			interrupted = true
		} else {
			interrupted = false
		}

		if event.Action != nil && event.Action.Exit {
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
