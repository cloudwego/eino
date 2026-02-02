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
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*deterministicTransferState]("_eino_adk_deterministic_transfer_state")
}

type deterministicTransferState struct {
	EventList []*agentEventWrapper
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
		return runFlowAgentWithIsolatedSession(ctx, fa, input, a.toAgentNames, options...)
	}

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go forwardEventsAndAppendTransfer(aIter, generator, a.toAgentNames)

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
		return runFlowAgentWithIsolatedSession(ctx, fa, input, a.toAgentNames, options...)
	}

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go forwardEventsAndAppendTransfer(aIter, generator, a.toAgentNames)

	return iterator
}

func (a *resumableAgentWithDeterministicTransferTo) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	if fa, ok := a.agent.(*flowAgent); ok {
		return resumeFlowAgentWithIsolatedSession(ctx, fa, info, a.toAgentNames, opts...)
	}

	aIter := a.agent.Resume(ctx, info, opts...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go forwardEventsAndAppendTransfer(aIter, generator, a.toAgentNames)

	return iterator
}

func forwardEventsAndAppendTransfer(iter *AsyncIterator[*AgentEvent],
	generator *AsyncGenerator[*AgentEvent], toAgentNames []string) {

	defer func() {
		if panicErr := recover(); panicErr != nil {
			generator.Send(&AgentEvent{Err: safe.NewPanicErr(panicErr, debug.Stack())})
		}
		generator.Close()
	}()

	var lastEvent *AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		generator.Send(event)
		lastEvent = event
	}

	if lastEvent != nil && lastEvent.Action != nil && (lastEvent.Action.Interrupted != nil || lastEvent.Action.Exit) {
		return
	}

	sendTransferEvents(generator, toAgentNames)
}

func runFlowAgentWithIsolatedSession(ctx context.Context, fa *flowAgent, input *AgentInput,
	toAgentNames []string, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	parentSession := getSession(ctx)
	parentRunCtx := getRunCtx(ctx)

	isolatedSession := &runSession{
		Values:    parentSession.Values,
		valuesMtx: parentSession.valuesMtx,
	}
	if isolatedSession.valuesMtx == nil {
		isolatedSession.valuesMtx = &sync.Mutex{}
	}
	if isolatedSession.Values == nil {
		isolatedSession.Values = make(map[string]any)
	}

	ctx = setRunCtx(ctx, &runContext{
		Session:   isolatedSession,
		RootInput: parentRunCtx.RootInput,
		RunPath:   parentRunCtx.RunPath,
	})

	iter := fa.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go handleFlowAgentEvents(ctx, iter, generator, isolatedSession, parentSession, toAgentNames)

	return iterator
}

func resumeFlowAgentWithIsolatedSession(ctx context.Context, fa *flowAgent, info *ResumeInfo,
	toAgentNames []string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	state, ok := info.InterruptState.(*deterministicTransferState)
	if !ok || state == nil {
		return genErrorIter(errors.New("invalid interrupt state for flowAgent resume in deterministic transfer"))
	}

	parentSession := getSession(ctx)
	parentRunCtx := getRunCtx(ctx)

	isolatedSession := &runSession{
		Values:    parentSession.Values,
		valuesMtx: parentSession.valuesMtx,
		Events:    state.EventList,
	}
	if isolatedSession.valuesMtx == nil {
		isolatedSession.valuesMtx = &sync.Mutex{}
	}
	if isolatedSession.Values == nil {
		isolatedSession.Values = make(map[string]any)
	}

	ctx = setRunCtx(ctx, &runContext{
		Session:   isolatedSession,
		RootInput: parentRunCtx.RootInput,
		RunPath:   parentRunCtx.RunPath,
	})

	iter := fa.Resume(ctx, info, opts...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	go handleFlowAgentEvents(ctx, iter, generator, isolatedSession, parentSession, toAgentNames)

	return iterator
}

func handleFlowAgentEvents(ctx context.Context, iter *AsyncIterator[*AgentEvent],
	generator *AsyncGenerator[*AgentEvent], isolatedSession, parentSession *runSession, toAgentNames []string) {

	defer func() {
		if panicErr := recover(); panicErr != nil {
			generator.Send(&AgentEvent{Err: safe.NewPanicErr(panicErr, debug.Stack())})
		}
		generator.Close()
	}()

	var lastEvent *AgentEvent

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if parentSession != nil && (event.Action == nil || event.Action.Interrupted == nil) {
			copied := copyAgentEvent(event)
			setAutomaticClose(copied)
			setAutomaticClose(event)
			parentSession.addEvent(copied)
		}

		if event.Action != nil && event.Action.internalInterrupted != nil {
			lastEvent = event
			continue
		}

		generator.Send(event)
		lastEvent = event
	}

	if lastEvent != nil && lastEvent.Action != nil {
		if lastEvent.Action.internalInterrupted != nil {
			events := isolatedSession.getEvents()
			state := &deterministicTransferState{EventList: events}
			compositeEvent := CompositeInterrupt(ctx, "deterministic transfer wrapper interrupted",
				state, lastEvent.Action.internalInterrupted)
			generator.Send(compositeEvent)
			return
		}

		if lastEvent.Action.Exit {
			return
		}
	}

	sendTransferEvents(generator, toAgentNames)
}

func sendTransferEvents(generator *AsyncGenerator[*AgentEvent], toAgentNames []string) {
	for _, toAgentName := range toAgentNames {
		aMsg, tMsg := GenTransferMessages(context.Background(), toAgentName)

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
