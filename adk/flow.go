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
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type HistoryEntry struct {
	AgentName string
	Message   Message
}

type HistoryRewriter func(ctx context.Context, entries []*HistoryEntry) ([]Message, error)

type flowAgent struct {
	Agent

	subAgents   []*flowAgent
	parentAgent *flowAgent

	disallowTransferToParent bool
	historyRewriter          HistoryRewriter

	checkPointStore compose.CheckPointStore

	noExecuteTransfer bool // if true, only emits transfer event, will not execute transfer
}

func (a *flowAgent) deepCopy() *flowAgent {
	ret := &flowAgent{
		Agent:                    a.Agent,
		subAgents:                make([]*flowAgent, 0, len(a.subAgents)),
		parentAgent:              a.parentAgent,
		disallowTransferToParent: a.disallowTransferToParent,
		historyRewriter:          a.historyRewriter,
		checkPointStore:          a.checkPointStore,
	}

	for _, sa := range a.subAgents {
		ret.subAgents = append(ret.subAgents, sa.deepCopy())
	}
	return ret
}

func SetSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (Agent, error) {
	return setSubAgents(ctx, agent, subAgents)
}

type AgentOption func(options *flowAgent)

func WithDisallowTransferToParent() AgentOption {
	return func(fa *flowAgent) {
		fa.disallowTransferToParent = true
	}
}

func WithHistoryRewriter(h HistoryRewriter) AgentOption {
	return func(fa *flowAgent) {
		fa.historyRewriter = h
	}
}

func withNoExecuteTransfer() AgentOption {
	return func(fa *flowAgent) {
		fa.noExecuteTransfer = true
	}
}

func toFlowAgent(ctx context.Context, agent Agent, opts ...AgentOption) *flowAgent {
	var fa *flowAgent
	var ok bool
	if fa, ok = agent.(*flowAgent); !ok {
		fa = &flowAgent{Agent: agent}
	} else {
		fa = fa.deepCopy()
	}
	for _, opt := range opts {
		opt(fa)
	}

	if fa.historyRewriter == nil {
		fa.historyRewriter = buildDefaultHistoryRewriter(agent.Name(ctx))
	}

	return fa
}

func AgentWithOptions(ctx context.Context, agent Agent, opts ...AgentOption) Agent {
	return toFlowAgent(ctx, agent, opts...)
}

func setSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (*flowAgent, error) {
	fa := toFlowAgent(ctx, agent)

	if len(fa.subAgents) > 0 {
		return nil, errors.New("agent's sub-agents has already been set")
	}

	if onAgent, ok_ := fa.Agent.(OnSubAgents); ok_ {
		err := onAgent.OnSetSubAgents(ctx, subAgents)
		if err != nil {
			return nil, err
		}
	}

	for _, s := range subAgents {
		fsa := toFlowAgent(ctx, s)

		if fsa.parentAgent != nil {
			return nil, errors.New("agent has already been set as a sub-agent of another agent")
		}

		fsa.parentAgent = fa
		if onAgent, ok__ := fsa.Agent.(OnSubAgents); ok__ {
			err := onAgent.OnSetAsSubAgent(ctx, agent)
			if err != nil {
				return nil, err
			}

			if fsa.disallowTransferToParent {
				err = onAgent.OnDisallowTransferToParent(ctx)
				if err != nil {
					return nil, err
				}
			}
		}

		fa.subAgents = append(fa.subAgents, fsa)
	}

	return fa, nil
}

func (a *flowAgent) getAgent(ctx context.Context, name string) *flowAgent {
	for _, subAgent := range a.subAgents {
		if subAgent.Name(ctx) == name {
			return subAgent
		}
	}

	if a.parentAgent != nil && a.parentAgent.Name(ctx) == name {
		return a.parentAgent
	}

	return nil
}

// belongToRunPath tests whether eventRunPath is a 'prefix' of runPath.
func belongToRunPath(eventRunPath []ExecutionStep, runPath []ExecutionStep) bool {
	if len(runPath) < len(eventRunPath) {
		return false
	}

	for i, eventStep := range eventRunPath {
		runStep := runPath[i]
		if eventStep.AgentName != runStep.AgentName {
			if len(runStep.Concurrent) == 0 {
				return false
			}

			var partOfConcurrent bool
			for _, concurrent := range runStep.Concurrent {
				if concurrent == eventStep.AgentName {
					partOfConcurrent = true
					break
				}
			}

			if !partOfConcurrent {
				return false
			}
		}
	}

	return true
}

func rewriteMessage(msg Message, agentName string) Message {
	var sb strings.Builder
	sb.WriteString("For context:")
	if msg.Role == schema.Assistant {
		if msg.Content != "" {
			sb.WriteString(fmt.Sprintf(" [%s] said: %s.", agentName, msg.Content))
		}
		if len(msg.ToolCalls) > 0 {
			for i := range msg.ToolCalls {
				f := msg.ToolCalls[i].Function
				sb.WriteString(fmt.Sprintf(" [%s] called tool: `%s`", agentName, f.Name))
				if f.Arguments != "" {
					sb.WriteString(fmt.Sprintf(" with arguments: %s", f.Arguments))
				}
			}
		}
	} else if msg.Role == schema.Tool && msg.Content != "" {
		sb.WriteString(fmt.Sprintf(" [%s] `%s` tool returned result: %s.",
			agentName, msg.ToolName, msg.Content))
	}

	return schema.UserMessage(sb.String())
}

func genMsg(entry *HistoryEntry, agentName string) (Message, error) {
	msg := entry.Message
	if entry.AgentName != agentName {
		msg = rewriteMessage(msg, entry.AgentName)
	}

	return msg, nil
}

func (ai *AgentInput) deepCopy() *AgentInput {
	copied := &AgentInput{
		Messages:        make([]Message, len(ai.Messages)),
		EnableStreaming: ai.EnableStreaming,
	}

	copy(copied.Messages, ai.Messages)

	return copied
}

func (a *flowAgent) genAgentInput(ctx context.Context, runCtx *runContext) (*AgentInput, error) {
	if runCtx.isRoot() {
		return runCtx.RootInput, nil
	}

	input := runCtx.RootInput.deepCopy()
	runPath := runCtx.RunPath

	events := runCtx.Session.getEvents()
	historyEntries := make([]*HistoryEntry, 0)

	for _, event := range events {
		if !belongToRunPath(event.RunPath, runPath) {
			continue
		}

		msg, err := getMessageFromWrappedEvent(event)
		if err != nil {
			return nil, err
		}

		if msg == nil {
			continue
		}

		historyEntries = append(historyEntries, &HistoryEntry{
			AgentName: event.AgentName,
			Message:   msg,
		})
	}

	messages, err := a.historyRewriter(ctx, historyEntries)
	if err != nil {
		return nil, err
	}
	input.Messages = append(input.Messages, messages...)

	return input, nil
}

func buildDefaultHistoryRewriter(agentName string) HistoryRewriter {
	return func(ctx context.Context, entries []*HistoryEntry) ([]Message, error) {
		messages := make([]Message, 0, len(entries))
		for _, entry := range entries {
			msg, err := genMsg(entry, agentName)
			if err != nil {
				return nil, fmt.Errorf("gen agent input failed: %w", err)
			}

			if msg != nil {
				messages = append(messages, msg)
			}
		}

		return messages, nil
	}
}

func (a *flowAgent) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	agentName := a.Name(ctx)

	ctx, runCtx := initRunCtx(ctx, agentName, input)

	input, err := a.genAgentInput(ctx, runCtx)
	if err != nil {
		iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
		generator.Send(&AgentEvent{Err: err})
		generator.Close()

		return iterator
	}

	if wf, ok := a.Agent.(*workflowAgent); ok {
		return wf.Run(ctx, input, filterOptions(agentName, opts)...)
	}

	aIter := a.Agent.Run(ctx, input, filterOptions(agentName, opts)...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go a.run(ctx, runCtx, aIter, generator, opts...)

	return iterator
}

func (a *flowAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	runCtx := getRunCtx(ctx)
	if len(runCtx.RunPath) == 0 {
		iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
		generator.Send(&AgentEvent{Err: errors.New("resume flowAgent, but RunPath empty")})
		generator.Close()
		return iterator
	}

	targetStep := runCtx.RunPath[len(runCtx.RunPath)-1]
	agentName := a.Name(ctx)
	if agentName != targetStep.AgentName {
		// go to target flow agent
		targetAgent, err := getResumeTargetAgent(ctx, a, targetStep, runCtx.RunPath)
		if err != nil {
			iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{Err: err})
			generator.Close()
			return iterator
		}
		return targetAgent.Resume(ctx, info, opts...)
	}
	if wf, ok := a.Agent.(*workflowAgent); ok {
		return wf.Resume(ctx, info, opts...)
	}

	// resume current agent
	ra, ok := a.Agent.(ResumableAgent)
	if !ok {
		iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
		generator.Send(&AgentEvent{Err: fmt.Errorf("failed to resume agent: target agent[%s] isn't resumable", agentName)})
		generator.Close()

		return iterator
	}
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	filteredOpts := filterOptions(agentName, opts)

	aIter := ra.Resume(ctx, info, filteredOpts...)

	go a.run(ctx, runCtx, aIter, generator, filteredOpts...)

	return iterator
}

func (a *flowAgent) run(
	ctx context.Context,
	runCtx *runContext,
	aIter *AsyncIterator[*AgentEvent],
	generator *AsyncGenerator[*AgentEvent],
	opts ...AgentRunOption) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			generator.Send(&AgentEvent{Err: e})
		}

		generator.Close()
	}()

	var (
		lastEvent *AgentEvent
		destNames []string
	)

	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}
		if lastEvent != nil {
			generator.Send(lastEvent)
			if lastEvent.Err != nil {
				break
			}
		}

		event.AgentName = a.Name(ctx)
		event.RunPath = runCtx.RunPath
		// copy the event so that the copied event's stream is exclusive for any potential consumer
		// copy before adding to session because once added to session it's stream could be consumed by genAgentInput at any time
		copied := copyAgentEvent(event)
		setAutomaticClose(copied)
		setAutomaticClose(event)
		runCtx.Session.addEvent(copied)
		lastEvent = event

		if event.Action != nil && event.Action.TransferToAgent != nil {
			destNames = append(destNames, event.Action.TransferToAgent.DestAgentName)
		}
	}

	if lastEvent != nil {
		if lastEvent.Action != nil {
			action := lastEvent.Action
			if action.Interrupted != nil {
				appendInterruptRunCtx(ctx, runCtx)
				generator.Send(lastEvent)
				return
			}
			if action.Exit {
				generator.Send(lastEvent)
				return
			}
		} else if lastEvent.Err != nil {
			generator.Send(lastEvent)
			return
		}
	}

	if lastEvent != nil {
		generator.Send(lastEvent)
	}

	if len(destNames) == 0 {
		return
	}

	if a.noExecuteTransfer {
		return
	}

	// handle transferring to another agent
	if len(destNames) == 1 {
		destName := destNames[0]
		agentToRun := a.getAgent(ctx, destName)
		if agentToRun == nil {
			e := errors.New(fmt.Sprintf(
				"transfer failed: agent '%s' not found when transferring from '%s'",
				destName, a.Name(ctx)))
			generator.Send(&AgentEvent{Err: e})
			return
		}

		subAIter := agentToRun.Run(ctx, nil /*subagents get input from runCtx*/, opts...)
		for {
			subEvent, ok_ := subAIter.Next()
			if !ok_ {
				break
			}

			setAutomaticClose(subEvent)
			generator.Send(subEvent)
		}

		return
	}

	subAgents := make([]Agent, len(destNames))
	for i, name := range destNames {
		subA := a.getAgent(ctx, name)
		if subA == nil {
			e := errors.New(fmt.Sprintf(
				"transfer failed: agent '%s' not found when transferring from '%s'",
				name, a.Name(ctx)))
			generator.Send(&AgentEvent{Err: e})
			return
		}
		subAgents[i] = subA
	}

	cw := NewConcurrentWrapper(ctx, subAgents)

	cwIter := cw.Run(ctx, nil, opts...)
	for {
		subEvent, ok_ := cwIter.Next()
		if !ok_ {
			break
		}

		setAutomaticClose(subEvent)
		generator.Send(subEvent)
	}
}

func getResumeTargetAgent(ctx context.Context, current *flowAgent, target ExecutionStep, runPath []ExecutionStep) (
	ResumableAgent, error) {
	name := current.Name(ctx)

	if name == target.AgentName {
		return current, nil
	}

	if len(runPath) == 0 {
		return nil, fmt.Errorf("can't find resume target agent [%s] along RunPath", target.AgentName)
	}

	first := runPath[0]
	if len(first.Concurrent) == 0 {
		if name == first.AgentName { // this can only happen if current is ROOT. Kick start the search process.
			return getResumeTargetAgent(ctx, current, target, runPath[1:])
		}

		for _, sa := range current.subAgents {
			subName := sa.Name(ctx)
			if subName == first.AgentName {
				return getResumeTargetAgent(ctx, sa, target, runPath[1:])
			}
		}

		if current.parentAgent != nil {
			pName := current.parentAgent.Name(ctx)
			if pName == first.AgentName {
				return getResumeTargetAgent(ctx, current.parentAgent, target, runPath[1:])
			}
		}

		return nil, fmt.Errorf("can't find transfer target agent %s from %s", first.AgentName, name)
	}

	if len(runPath) == 1 {
		// reached last step, attempt to assemble the concurrentWrapper
		cAgents := make([]Agent, 0, len(first.Concurrent))
	loop:
		for _, subN := range first.Concurrent {
			for _, sa := range current.subAgents {
				if sa.Name(ctx) == subN {
					cAgents = append(cAgents, sa)
					continue loop
				}
			}

			if current.parentAgent != nil {
				pName := current.parentAgent.Name(ctx)
				if pName == subN {
					cAgents = append(cAgents, current.parentAgent)
					continue loop
				}
			}

			return nil, fmt.Errorf("can't find agent [%s] within concurrent wrapper [%v], from %s",
				subN, first.Concurrent, name)
		}

		cw := NewConcurrentWrapper(ctx, cAgents)
		return cw, nil
	}

	// intermediate concurrent step, pick any one of the concurrent agent, should all be able to reach target
	one := first.Concurrent[0]
	for _, sa := range current.subAgents {
		subName := sa.Name(ctx)
		if subName == one {
			return getResumeTargetAgent(ctx, sa, target, runPath[1:])
		}
	}

	if current.parentAgent != nil {
		pName := current.parentAgent.Name(ctx)
		if pName == one {
			return getResumeTargetAgent(ctx, current.parentAgent, target, runPath[1:])
		}
	}

	return nil, fmt.Errorf("can't find transfer target agent %s from %s", one, name)
}
