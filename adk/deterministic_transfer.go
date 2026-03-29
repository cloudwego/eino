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

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*deterministicTransferState]("_eino_adk_deterministic_transfer_state")
	schema.RegisterName[*typedDeterministicTransferState[*schema.AgenticMessage]]("_eino_adk_generic_deterministic_transfer_state_agentic")
}

type deterministicTransferState struct {
	EventList []*agentEventWrapper
}

type typedDeterministicTransferState[M MessageType] struct {
	EventList []*typedAgentEventWrapper[M]
}

// TypedAgentWithDeterministicTransferTo wraps a TypedAgent with deterministic transfer logic to specified agents.
func TypedAgentWithDeterministicTransferTo[M MessageType](_ context.Context, config *TypedDeterministicTransferConfig[M]) TypedAgent[M] {
	if ra, ok := config.Agent.(TypedResumableAgent[M]); ok {
		return &typedResumableAgentWithDeterministicTransferTo[M]{
			agent:        ra,
			toAgentNames: config.ToAgentNames,
		}
	}
	return &typedAgentWithDeterministicTransferTo[M]{
		agent:        config.Agent,
		toAgentNames: config.ToAgentNames,
	}
}

// AgentWithDeterministicTransferTo wraps an Agent with deterministic transfer logic to specified agents.
func AgentWithDeterministicTransferTo(_ context.Context, config *DeterministicTransferConfig) Agent {
	return TypedAgentWithDeterministicTransferTo[*schema.Message](context.Background(), config)
}

type typedAgentWithDeterministicTransferTo[M MessageType] struct {
	agent        TypedAgent[M]
	toAgentNames []string
}

func (a *typedAgentWithDeterministicTransferTo[M]) Description(ctx context.Context) string {
	return a.agent.Description(ctx)
}

func (a *typedAgentWithDeterministicTransferTo[M]) Name(ctx context.Context) string {
	return a.agent.Name(ctx)
}

func (a *typedAgentWithDeterministicTransferTo[M]) GetType() string {
	if typer, ok := a.agent.(components.Typer); ok {
		return typer.GetType()
	}
	return "DeterministicTransfer"
}

func (a *typedAgentWithDeterministicTransferTo[M]) Run(ctx context.Context,
	input *TypedAgentInput[M], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {

	if fa, ok := a.agent.(*typedFlowAgent[M]); ok {
		return typedRunFlowAgentWithIsolatedSession(ctx, fa, input, a.toAgentNames, options...)
	}

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedForwardEventsAndAppendTransfer(aIter, generator, a.toAgentNames)

	return iterator
}

type typedResumableAgentWithDeterministicTransferTo[M MessageType] struct {
	agent        TypedResumableAgent[M]
	toAgentNames []string
}

func (a *typedResumableAgentWithDeterministicTransferTo[M]) Description(ctx context.Context) string {
	return a.agent.Description(ctx)
}

func (a *typedResumableAgentWithDeterministicTransferTo[M]) Name(ctx context.Context) string {
	return a.agent.Name(ctx)
}

func (a *typedResumableAgentWithDeterministicTransferTo[M]) GetType() string {
	if typer, ok := a.agent.(components.Typer); ok {
		return typer.GetType()
	}
	return "DeterministicTransfer"
}

func (a *typedResumableAgentWithDeterministicTransferTo[M]) Run(ctx context.Context,
	input *TypedAgentInput[M], options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {

	if fa, ok := a.agent.(*typedFlowAgent[M]); ok {
		return typedRunFlowAgentWithIsolatedSession(ctx, fa, input, a.toAgentNames, options...)
	}

	aIter := a.agent.Run(ctx, input, options...)

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedForwardEventsAndAppendTransfer(aIter, generator, a.toAgentNames)

	return iterator
}

func (a *typedResumableAgentWithDeterministicTransferTo[M]) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	if fa, ok := a.agent.(*typedFlowAgent[M]); ok {
		return typedResumeFlowAgentWithIsolatedSession[M](ctx, fa, info, a.toAgentNames, opts...)
	}

	aIter := a.agent.Resume(ctx, info, opts...)

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedForwardEventsAndAppendTransfer(aIter, generator, a.toAgentNames)

	return iterator
}

func typedForwardEventsAndAppendTransfer[M MessageType](iter *AsyncIterator[*TypedAgentEvent[M]],
	generator *AsyncGenerator[*TypedAgentEvent[M]], toAgentNames []string) {

	defer func() {
		if panicErr := recover(); panicErr != nil {
			generator.Send(&TypedAgentEvent[M]{Err: safe.NewPanicErr(panicErr, debug.Stack())})
		}
		generator.Close()
	}()

	var lastEvent *TypedAgentEvent[M]
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

	typedSendTransferEvents(generator, toAgentNames)
}

func typedRunFlowAgentWithIsolatedSession[M MessageType](ctx context.Context, fa *typedFlowAgent[M], input *TypedAgentInput[M],
	toAgentNames []string, options ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {

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

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedHandleFlowAgentEvents(ctx, iter, generator, isolatedSession, parentSession, toAgentNames)

	return iterator
}

func typedResumeFlowAgentWithIsolatedSession[M MessageType](ctx context.Context, fa *typedFlowAgent[M], info *ResumeInfo,
	toAgentNames []string, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {

	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		state, ok := info.InterruptState.(*deterministicTransferState)
		if !ok || state == nil {
			return typedErrorIter[M](errors.New("invalid interrupt state for flowAgent resume in deterministic transfer"))
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

		iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
		go typedHandleFlowAgentEvents(ctx, iter, generator, isolatedSession, parentSession, toAgentNames)
		return iterator
	}

	state, ok := info.InterruptState.(*typedDeterministicTransferState[M])
	if !ok || state == nil {
		return typedErrorIter[M](errors.New("invalid interrupt state for flowAgent resume in deterministic transfer"))
	}

	parentSession := getSession(ctx)
	parentRunCtx := getRunCtx(ctx)

	eventsSlice := state.EventList
	isolatedSession := &runSession{
		Values:    parentSession.Values,
		valuesMtx: parentSession.valuesMtx,
	}
	s := make([]*typedAgentEventWrapper[M], len(eventsSlice))
	copy(s, eventsSlice)
	isolatedSession.TypedEvents = &s

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

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	go typedHandleFlowAgentEvents(ctx, iter, generator, isolatedSession, parentSession, toAgentNames)

	return iterator
}

func typedHandleFlowAgentEvents[M MessageType](ctx context.Context, iter *AsyncIterator[*TypedAgentEvent[M]],
	generator *AsyncGenerator[*TypedAgentEvent[M]], isolatedSession, parentSession *runSession, toAgentNames []string) {

	defer func() {
		if panicErr := recover(); panicErr != nil {
			generator.Send(&TypedAgentEvent[M]{Err: safe.NewPanicErr(panicErr, debug.Stack())})
		}
		generator.Close()
	}()

	var lastEvent *TypedAgentEvent[M]

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if parentSession != nil && (event.Action == nil || event.Action.Interrupted == nil) {
			copied := copyTypedAgentEvent(event)
			typedSetAutomaticClose(copied)
			typedSetAutomaticClose(event)
			addTypedEvent(parentSession, copied)
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
			var interruptState any
			var zero M
			if _, ok := any(zero).(*schema.Message); ok {
				events := isolatedSession.getEvents()
				interruptState = &deterministicTransferState{EventList: events}
			} else {
				events := getTypedEvents[M](isolatedSession)
				interruptState = &typedDeterministicTransferState[M]{EventList: events}
			}
			compositeEvent := TypedCompositeInterrupt[M](ctx, "deterministic transfer wrapper interrupted",
				interruptState, lastEvent.Action.internalInterrupted)
			generator.Send(compositeEvent)
			return
		}

		if lastEvent.Action.Exit {
			return
		}
	}

	typedSendTransferEvents(generator, toAgentNames)
}

func typedSendTransferEvents[M MessageType](generator *AsyncGenerator[*TypedAgentEvent[M]], toAgentNames []string) {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		for _, toAgentName := range toAgentNames {
			aMsg, tMsg := GenTransferMessages(context.Background(), toAgentName)

			aEvent := TypedEventFromMessage(aMsg, nil, schema.Assistant, "")
			generator.Send(any(aEvent).(*TypedAgentEvent[M]))

			tEvent := TypedEventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
			tEvent.Action = &AgentAction{
				TransferToAgent: &TransferToAgentAction{
					DestAgentName: toAgentName,
				},
			}
			generator.Send(any(tEvent).(*TypedAgentEvent[M]))
		}
		return
	}

	for _, toAgentName := range toAgentNames {
		aMsg, tMsg := genAgenticTransferMessages(toAgentName)

		aEvent := TypedEventFromMessage[M](any(aMsg).(M), nil, schema.Assistant, "")
		generator.Send(aEvent)

		tEvent := TypedEventFromMessage[M](any(tMsg).(M), nil, schema.Tool, TransferToAgentToolName)
		tEvent.Action = &AgentAction{
			TransferToAgent: &TransferToAgentAction{
				DestAgentName: toAgentName,
			},
		}
		generator.Send(tEvent)
	}
}
