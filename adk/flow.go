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
	"sync"

	"github.com/cloudwego/eino/internal/core"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type HistoryEntry struct {
	IsUserInput bool
	AgentName   string
	Message     Message
}

type HistoryRewriter func(ctx context.Context, entries []*HistoryEntry) ([]Message, error)

type flowInterruptState struct {
	// Maps the destination agent name to the events generated in its lane before interruption.
	// This also serves as the source of truth for which lanes need to be resumed.
	LaneEvents map[string][]*agentEventWrapper
}

// collectLaneEvents collects events from child contexts following runParallel pattern
func (a *flowAgent) collectLaneEvents(childContexts []context.Context, agentNames []string) map[string][]*agentEventWrapper {
	laneEvents := make(map[string][]*agentEventWrapper)

	for i, childCtx := range childContexts {
		childRunCtx := getRunCtx(childCtx)
		if childRunCtx != nil && childRunCtx.Session != nil && childRunCtx.Session.LaneEvents != nil {
			// Use the provided agent names for reliable mapping
			agentName := agentNames[i]

			// COPY events before storing (streams can only be consumed once)
			laneEvents[agentName] = make([]*agentEventWrapper, len(childRunCtx.Session.LaneEvents.Events))
			for j, event := range childRunCtx.Session.LaneEvents.Events {
				copied := copyAgentEvent(event.AgentEvent)
				setAutomaticClose(copied)
				laneEvents[agentName][j] = &agentEventWrapper{
					AgentEvent: copied,
				}
			}
		}
	}

	return laneEvents
}

// createCompositeInterrupt creates a composite interrupt event with the collected state
func (a *flowAgent) createCompositeInterrupt(ctx context.Context, laneEvents map[string][]*agentEventWrapper, subInterruptSignals []*core.InterruptSignal) *AgentEvent {
	state := &flowInterruptState{
		LaneEvents: laneEvents,
	}

	event := CompositeInterrupt(ctx, "Concurrent transfer interrupted", state, subInterruptSignals...)

	// Set agent name and run path for proper identification
	event.AgentName = a.Name(ctx)
	event.RunPath = getRunCtx(ctx).RunPath

	return event
}

func init() {
	schema.RegisterName[*flowInterruptState]("eino_adk_dynamic_parallel_state")
}

type flowAgent struct {
	Agent

	subAgents   []*flowAgent
	parentAgent *flowAgent

	disallowTransferToParent bool
	historyRewriter          HistoryRewriter

	selfReturnAfterTransfer bool
}

func (a *flowAgent) deepCopy() *flowAgent {
	ret := &flowAgent{
		Agent:                    a.Agent,
		subAgents:                make([]*flowAgent, 0, len(a.subAgents)),
		parentAgent:              a.parentAgent,
		disallowTransferToParent: a.disallowTransferToParent,
		historyRewriter:          a.historyRewriter,
		selfReturnAfterTransfer:  a.selfReturnAfterTransfer,
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

// WithSelfReturnAfterTransfer returns an AgentOption that enables self-return behavior
// after a transfer operation completes. When this option is set, the agent will
// automatically return control to itself after all sub-agents have finished executing.
//
// This is particularly useful for supervisor agents that need to process the results
// of concurrent transfers or perform additional operations after sub-agent execution.
//
// Example usage:
//
//	agent := toFlowAgent(ctx, baseAgent, WithSelfReturnAfterTransfer())
//
// Without this option, the agent's execution ends after the transfer completes.
// With this option, the agent resumes execution to handle the aggregated results.
func WithSelfReturnAfterTransfer() AgentOption {
	return func(fa *flowAgent) {
		fa.selfReturnAfterTransfer = true
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
				sb.WriteString(fmt.Sprintf(" [%s] called tool: `%s` with arguments: %s.",
					agentName, f.Name, f.Arguments))
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
	if ai == nil {
		return nil
	}
	copied := &AgentInput{
		Messages:        make([]Message, len(ai.Messages)),
		EnableStreaming: ai.EnableStreaming,
	}

	copy(copied.Messages, ai.Messages)

	return copied
}

func (a *flowAgent) genAgentInput(ctx context.Context, runCtx *runContext, skipTransferMessages bool) (*AgentInput, error) {
	input := runCtx.RootInput.deepCopy()

	events := runCtx.Session.getEvents()
	historyEntries := make([]*HistoryEntry, 0)

	for _, m := range input.Messages {
		historyEntries = append(historyEntries, &HistoryEntry{
			IsUserInput: true,
			Message:     m,
		})
	}

	for _, event := range events {
		if skipTransferMessages && event.Action != nil && event.Action.TransferToAgent != nil {
			// If skipTransferMessages is true and the event contain transfer action, the message in this event won't be appended to history entries.
			if event.Output != nil &&
				event.Output.MessageOutput != nil &&
				event.Output.MessageOutput.Role == schema.Tool &&
				len(historyEntries) > 0 {
				// If the skipped message's role is Tool, remove the previous history entry as it's also a transfer message(from ChatModelAgent and GenTransferMessages).
				historyEntries = historyEntries[:len(historyEntries)-1]
			}
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
	input.Messages = messages

	return input, nil
}

func buildDefaultHistoryRewriter(agentName string) HistoryRewriter {
	return func(ctx context.Context, entries []*HistoryEntry) ([]Message, error) {
		messages := make([]Message, 0, len(entries))
		var err error
		for _, entry := range entries {
			msg := entry.Message
			if !entry.IsUserInput {
				msg, err = genMsg(entry, agentName)
				if err != nil {
					return nil, fmt.Errorf("gen agent input failed: %w", err)
				}
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

	var runCtx *runContext
	ctx, runCtx = initRunCtx(ctx, agentName, input)
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, agentName)

	o := getCommonOptions(nil, opts...)

	input, err := a.genAgentInput(ctx, runCtx, o.skipTransferMessages)
	if err != nil {
		return genErrorIter(err)
	}

	if wf, ok := a.Agent.(*workflowAgent); ok {
		return wf.Run(ctx, input, opts...)
	}

	aIter := a.Agent.Run(ctx, input, filterOptions(agentName, opts)...)

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

	go a.run(ctx, runCtx, aIter, generator, opts...)

	return iterator
}

func (a *flowAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	ctx, info = buildResumeInfo(ctx, a.Name(ctx), info)

	if info.WasInterrupted {
		// Check if we need to resume concurrent transfers
		if info.InterruptState != nil {
			state, ok := info.InterruptState.(*flowInterruptState)
			if ok {
				// Delegate to resumeConcurrentLanes which will handle the type assertion
				return a.resumeConcurrentLanes(ctx, state, info, opts...)
			}
		}

		ra, ok := a.Agent.(ResumableAgent)
		if !ok {
			return genErrorIter(fmt.Errorf("failed to resume agent: agent '%s' is an interrupt point "+
				"but is not a ResumableAgent", a.Name(ctx)))
		}

		iterator, generator := NewAsyncIteratorPair[*AgentEvent]()

		aIter := ra.Resume(ctx, info, opts...)
		if _, ok := ra.(*workflowAgent); ok {
			return aIter
		}
		go a.run(ctx, getRunCtx(ctx), aIter, generator, opts...)
		return iterator
	}

	nextAgentName, err := getNextResumeAgent(ctx, info)
	if err != nil {
		return genErrorIter(err)
	}

	subAgent := a.getAgent(ctx, nextAgentName)
	if subAgent == nil {
		return genErrorIter(fmt.Errorf("failed to resume agent: agent '%s' not found", nextAgentName))
	}

	return subAgent.Resume(ctx, info, opts...)
}

type DeterministicTransferConfig struct {
	Agent        Agent
	ToAgentNames []string
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

	// Collect all actions to apply precedence rules
	var interruptAction *AgentEvent
	var exitAction *AgentEvent
	var transferActions []*TransferToAgentAction

	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		event.AgentName = a.Name(ctx)
		event.RunPath = runCtx.RunPath

		// Apply Action Precedence: Interrupt > Exit > Transfer
		if event.Action != nil {
			if event.Action.Interrupted != nil {
				if interruptAction == nil {
					interruptAction = event
				}
			} else if event.Action.Exit {
				if interruptAction == nil {
					exitAction = event
				}
			} else if event.Action.TransferToAgent != nil {
				if interruptAction == nil && exitAction == nil {
					transferActions = append(transferActions, event.Action.TransferToAgent)
				}
			} else if event.Action.ConcurrentTransferToAgent != nil {
				if interruptAction == nil && exitAction == nil {
					// Convert concurrent transfer action to individual transfer actions
					for _, destName := range event.Action.ConcurrentTransferToAgent.DestAgentNames {
						transferActions = append(transferActions, &TransferToAgentAction{DestAgentName: destName})
					}
				}
			}
		}

		// Always send the event to the generator for immediate consumption
		// but only add non-interrupt events to the session
		if event.Action == nil || event.Action.Interrupted == nil {
			// copy the event so that the copied event's stream is exclusive for any potential consumer
			// copy before adding to session because once added to session it's stream could be consumed by genAgentInput at any time
			// interrupt action are not added to session, because ALL information contained in it
			// is either presented to end-user, or made available to agents through other means
			copied := copyAgentEvent(event)
			setAutomaticClose(copied)
			setAutomaticClose(event)
			runCtx.Session.addEvent(copied)
		}
		generator.Send(event)
	}

	if interruptAction != nil || exitAction != nil || len(transferActions) == 0 {
		return
	}

	// Handle transfers based on count
	if len(transferActions) == 1 {
		agentToRun, err := a.getAgentFromTransferAction(ctx, transferActions[0])
		if err != nil {
			generator.Send(&AgentEvent{Err: err})
			return
		}

		if a.selfReturnAfterTransfer {
			agentToRun = AgentWithDeterministicTransferTo(ctx, &DeterministicTransferConfig{
				Agent:        agentToRun,
				ToAgentNames: []string{a.Name(ctx)},
			}).(*flowAgent)
		}

		subAIter := agentToRun.Run(ctx, nil /*subagents get input from runCtx*/, opts...)
		generator.pipeAll(subAIter)

		return
	}

	// Multiple transfers - execute concurrently
	agents := make([]Agent, len(transferActions))
	for i, action := range transferActions {
		agentToRun, err := a.getAgentFromTransferAction(ctx, action)
		if err != nil {
			generator.Send(&AgentEvent{Err: err})
			return
		}
		agents[i] = agentToRun
	}
	iterator := a.runConcurrentLanes(ctx, agents, opts...)
	generator.pipeAll(iterator)
}

func (a *flowAgent) getAgentFromTransferAction(ctx context.Context, action *TransferToAgentAction) (*flowAgent, error) {
	sub := a.getAgent(ctx, action.DestAgentName)
	if sub == nil {
		return nil, fmt.Errorf("transfer failed: agent '%s' not found when transferring from '%s'",
			action.DestAgentName, a.Name(ctx))
	}
	return sub, nil
}

// runConcurrentLanes executes multiple agents concurrently using a fork-join model
func (a *flowAgent) runConcurrentLanes(
	ctx context.Context,
	agents []Agent,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	defer generator.Close()

	agentExecutors := make([]func(context.Context) *AsyncIterator[*AgentEvent], len(agents))
	childContexts := make([]context.Context, len(agents))
	agentNames := make([]string, len(agents))

	for i := range agents {
		subAgent := agents[i]
		agentNames[i] = subAgent.Name(ctx)
		childContexts[i] = forkRunCtx(ctx)
		agentExecutors[i] = func(ctx2 context.Context) *AsyncIterator[*AgentEvent] {
			return subAgent.Run(ctx2, nil, opts...)
		}
	}

	a.concurrentLaneExecution(ctx, agentExecutors, agentNames, childContexts, generator, opts...)
	return iterator
}

// resumeConcurrentLanes resumes execution after a concurrent transfer interruption
func (a *flowAgent) resumeConcurrentLanes(
	ctx context.Context,
	state *flowInterruptState,
	info *ResumeInfo,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	defer generator.Close()

	// Create child contexts for each lane (using LaneEvents as source of truth)
	// We need to preserve the order, so we'll create a slice of agent names
	agentNamesInOrder := make([]string, 0, len(state.LaneEvents))
	for destAgentName := range state.LaneEvents {
		agentNamesInOrder = append(agentNamesInOrder, destAgentName)
	}

	agents := make([]*flowAgent, len(agentNamesInOrder))
	for i, agentName := range agentNamesInOrder {
		agents[i] = a.getAgent(ctx, agentName)
		if agents[i] == nil {
			generator.Send(&AgentEvent{Err: fmt.Errorf("transfer failed: agent '%s' not found", agentNamesInOrder[i])})
			return iterator
		}
	}

	// Get the next resume agents from the interrupt context (following parallel workflow pattern)
	agentNames, err := getNextResumeAgents(ctx, info)
	if err != nil {
		generator.Send(&AgentEvent{Err: err})
		return iterator
	}

	childContexts := make([]context.Context, len(agentNamesInOrder))

	// Fork contexts for each lane (following parallel workflow pattern)
	for i, destAgentName := range agentNamesInOrder {
		childContexts[i] = forkRunCtx(ctx)

		// Add existing events to the child context
		if existingEvents, ok := state.LaneEvents[destAgentName]; ok {
			childRunCtx := getRunCtx(childContexts[i])
			if childRunCtx != nil && childRunCtx.Session != nil {
				if childRunCtx.Session.LaneEvents == nil {
					childRunCtx.Session.LaneEvents = &laneEvents{}
				}
				childRunCtx.Session.LaneEvents.Events = append(childRunCtx.Session.LaneEvents.Events, existingEvents...)
			}
		}
	}

	// Prepare agent executors for concurrent execution
	agentExecutors := make([]func(context.Context) *AsyncIterator[*AgentEvent], len(agentNamesInOrder))

	for i := range agentNamesInOrder {
		name := agentNamesInOrder[i]
		agentToRun := agents[i]
		agentExecutors[i] = func(ctx2 context.Context) *AsyncIterator[*AgentEvent] {
			// Check if this agent needs to be resumed (following parallel workflow pattern)
			if _, ok := agentNames[name]; ok {
				// This branch was interrupted and needs to be resumed
				return agentToRun.Resume(ctx2, &ResumeInfo{
					EnableStreaming: info.EnableStreaming,
					InterruptInfo:   info.InterruptInfo,
				}, opts...)
			} else {
				// We are resuming, but this child is not in the next points map.
				// This means it finished successfully, so we don't run it.
				return nil
			}
		}
	}

	// Execute all agents concurrently using shared logic, using pre-created child contexts
	a.concurrentLaneExecution(ctx, agentExecutors, agentNamesInOrder, childContexts, generator, opts...)

	return iterator
}

// concurrentLaneExecution handles the core logic for executing multiple agents concurrently
// This is shared between runConcurrentLanes and resumeConcurrentLanes
func (a *flowAgent) concurrentLaneExecution(
	ctx context.Context,
	agentExecutors []func(context.Context) *AsyncIterator[*AgentEvent],
	agentNames []string,
	childContexts []context.Context,
	generator *AsyncGenerator[*AgentEvent],
	opts ...AgentRunOption) {

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)

	subInterruptSignals := make([]*core.InterruptSignal, 0)

	// Launch concurrent execution for each agent
	for i := range agentExecutors {
		exec := agentExecutors[i]
		childRunCtx := childContexts[i]
		wg.Add(1)
		go func() {
			defer wg.Done()

			subAIter := exec(childRunCtx)
			if subAIter == nil {
				return
			}

			for {
				subEvent, ok := subAIter.Next()
				if !ok {
					break
				}

				// Check for interrupt action
				if subEvent.Action != nil && subEvent.Action.internalInterrupted != nil {
					mu.Lock()
					subInterruptSignals = append(subInterruptSignals, subEvent.Action.internalInterrupted)
					mu.Unlock()

					// Stop processing this lane when interrupted
					return
				}

				generator.Send(subEvent)
			}
		}()
	}

	// Wait for all concurrent lanes to complete
	wg.Wait()

	if len(subInterruptSignals) == 0 {
		joinRunCtxs(ctx, childContexts...)

		if a.selfReturnAfterTransfer {
			a.doSelfReturnAfterTransfer(ctx, generator, opts...)
		}
		return
	}

	// Collect events from child contexts if any lanes were interrupted
	var laneEvents map[string][]*agentEventWrapper
	if len(subInterruptSignals) > 0 {
		laneEvents = a.collectLaneEvents(childContexts, agentNames)
	}

	// Create composite interrupt with the collected state
	event := a.createCompositeInterrupt(ctx, laneEvents, subInterruptSignals)
	generator.Send(event)
}

func (a *flowAgent) doSelfReturnAfterTransfer(ctx context.Context,
	generator *AsyncGenerator[*AgentEvent], opts ...AgentRunOption) {
	target := a.Name(ctx)
	runCtx := getRunCtx(ctx)
	aMsg, tMsg := GenTransferMessages(ctx, target)
	aEvent := EventFromMessage(aMsg, nil, schema.Assistant, "")
	aEvent.AgentName = a.Name(ctx)
	aEvent.RunPath = runCtx.RunPath
	generator.Send(aEvent)
	tEvent := EventFromMessage(tMsg, nil, schema.Tool, tMsg.ToolName)
	tEvent.Action = &AgentAction{
		TransferToAgent: &TransferToAgentAction{
			DestAgentName: target,
		},
	}
	tEvent.AgentName = a.Name(ctx)
	tEvent.RunPath = runCtx.RunPath
	generator.Send(tEvent)
	iter := a.Run(ctx, nil, opts...)
	generator.pipeAll(iter)
}
