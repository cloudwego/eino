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

	"github.com/cloudwego/eino/compose"
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

type dynamicParallelState struct {
	// Maps the destination agent name to the events generated in its lane before interruption.
	// This also serves as the source of truth for which lanes need to be resumed.
	LaneEvents map[string][]*agentEventWrapper 
}

func init() {
	schema.RegisterName[*dynamicParallelState]("eino_adk_dynamic_parallel_state")
}

type flowAgent struct {
	Agent

	subAgents   []*flowAgent
	parentAgent *flowAgent

	disallowTransferToParent bool
	historyRewriter          HistoryRewriter

	checkPointStore compose.CheckPointStore
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

	// Check if we need to resume concurrent transfers
	if info.InterruptState != nil {
		// Delegate to resumeConcurrentLanes which will handle the type assertion
		return a.resumeConcurrentLanes(ctx, info, opts...)
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

// resumeConcurrentLanes resumes execution after a concurrent transfer interruption
func (a *flowAgent) resumeConcurrentLanes(
	ctx context.Context,
	info *ResumeInfo,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	
	// Type assertion for dynamicParallelState
	state, ok := info.InterruptState.(*dynamicParallelState)
	if !ok {
		return genErrorIter(fmt.Errorf("resumeConcurrentLanes: expected dynamicParallelState, got %T", info.InterruptState))
	}
	
	iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
	
	go func() {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				e := safe.NewPanicErr(panicErr, debug.Stack())
				generator.Send(&AgentEvent{Err: e})
			}
			generator.Close()
		}()
		
		runCtx := getRunCtx(ctx)
		
		// Get the next resume agents from the interrupt context (following parallel workflow pattern)
		agentNames, err := getNextResumeAgents(ctx, info)
		if err != nil {
			generator.Send(&AgentEvent{Err: err})
			return
		}
		
		var (
			wg sync.WaitGroup
			mu sync.Mutex
		)
		
		// Create child contexts for each lane (using LaneEvents as source of truth)
		childContexts := make([]context.Context, len(state.LaneEvents))
		subInterruptSignals := make([]*core.InterruptSignal, 0)
		
		// Fork contexts for each lane (following parallel workflow pattern)
		i := 0
		for destAgentName := range state.LaneEvents {
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
			i++
		}
		
		// Launch concurrent execution for each lane (following parallel workflow pattern)
		i = 0
		for destAgentName := range state.LaneEvents {
			wg.Add(1)
			go func(idx int, agentName string) {
				defer wg.Done()
				
				agentToRun := a.getAgent(ctx, agentName)
				if agentToRun == nil {
					// Agent not found - send error and continue
					e := fmt.Errorf("transfer failed: agent '%s' not found", agentName)
					generator.Send(&AgentEvent{Err: e})
					return
				}
				
				var subAIter *AsyncIterator[*AgentEvent]
				
				// Check if this agent needs to be resumed (following parallel workflow pattern)
				if _, ok := agentNames[agentToRun.Name(ctx)]; ok {
					// This branch was interrupted and needs to be resumed
					subAIter = agentToRun.Resume(childContexts[idx], &ResumeInfo{
						EnableStreaming: info.EnableStreaming,
						InterruptInfo:   info.InterruptInfo,
					}, opts...)
				} else if state != nil {
					// We are resuming, but this child is not in the next points map.
					// This means it finished successfully, so we don't run it.
					return
				} else {
					// Not resuming - run normally
					subAIter = agentToRun.Run(childContexts[idx], nil, opts...)
				}
				
				// Collect events for this lane
				var laneEventsForAgent []*agentEventWrapper
				
				for {
					subEvent, ok := subAIter.Next()
					if !ok {
						break
					}
					
					generator.Send(subEvent)
					
					// Check for interrupt action
					if subEvent.Action != nil && subEvent.Action.internalInterrupted != nil {
						mu.Lock()
						subInterruptSignals = append(subInterruptSignals, subEvent.Action.internalInterrupted)
						mu.Unlock()
						
						// Stop processing this lane when interrupted
						return
					}
					
					// Store event for potential resume
					if subEvent.Action == nil || subEvent.Action.Interrupted == nil {
						// COPY the event before storing (streams can only be consumed once)
						copied := copyAgentEvent(subEvent)
						setAutomaticClose(copied)
						laneEventsForAgent = append(laneEventsForAgent, &agentEventWrapper{
							AgentEvent: copied,
						})
					}
				}
			}(i, destAgentName)
			i++
		}
		
		wg.Wait()
		
		if len(subInterruptSignals) == 0 {
			// Join all child contexts back to the parent
			joinRunCtxs(ctx, childContexts...)
			return
		}
		
		if len(subInterruptSignals) > 0 {
			// Create composite interrupt with the collected state
			newState := &dynamicParallelState{
				LaneEvents: state.LaneEvents, // Preserve existing events
			}
			
			event := CompositeInterrupt(ctx, "Concurrent transfer interrupted", newState, subInterruptSignals...)
			
			// Set agent name and run path for proper identification
			event.AgentName = a.Name(ctx)
			event.RunPath = runCtx.RunPath
			generator.Send(event)
		}
	}()
	
	return iterator
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
				// Interrupt has highest precedence - handle immediately
				if interruptAction == nil {
					interruptAction = event
				}
				// Continue to collect all actions for proper precedence handling
			} else if event.Action.Exit {
				// Exit has second precedence - only store if no interrupt found
				if interruptAction == nil {
					exitAction = event
				}
			} else if event.Action.TransferToAgent != nil {
				// Transfer has lowest precedence - only store if no interrupt or exit found
				if interruptAction == nil && exitAction == nil {
					transferActions = append(transferActions, event.Action.TransferToAgent)
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

	// Apply Action Precedence decision
	if interruptAction != nil {
		// Interrupt has highest precedence - just return, the interrupt event was already sent
		return
	}

	if exitAction != nil {
		// Exit has second precedence - just return, the exit event was already sent
		return
	}

	// Handle transfers based on count
	switch len(transferActions) {
	case 0:
		// No transfers - this branch is complete
		return
	case 1:
		// Single transfer - handle sequentially
		destName := transferActions[0].DestAgentName
		agentToRun := a.getAgent(ctx, destName)
		if agentToRun == nil {
			e := fmt.Errorf("transfer failed: agent '%s' not found when transferring from '%s'",
				destName, a.Name(ctx))
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
	default:
		// Multiple transfers - execute concurrently using fork-join model
		a.runConcurrentLanes(ctx, runCtx, transferActions, generator, opts...)
	}
}

// runConcurrentLanes executes multiple transfers concurrently using a fork-join model
func (a *flowAgent) runConcurrentLanes(
	ctx context.Context,
	runCtx *runContext,
	transferActions []*TransferToAgentAction,
	generator *AsyncGenerator[*AgentEvent],
	opts ...AgentRunOption) {
	
	if len(transferActions) == 0 {
		return
	}

	var (
		wg sync.WaitGroup
		mu sync.Mutex
	)
	
	childContexts := make([]context.Context, len(transferActions))
	subInterruptSignals := make([]*core.InterruptSignal, 0)
	laneEvents := make(map[string][]*agentEventWrapper)

	// Fork contexts for each transfer
	for i := range transferActions {
		childContexts[i] = forkRunCtx(ctx)
	}

	// Launch concurrent execution for each transfer
	for i, transfer := range transferActions {
		wg.Add(1)
		go func(idx int, transferAction *TransferToAgentAction) {
			defer wg.Done()

			agentToRun := a.getAgent(ctx, transferAction.DestAgentName)
			if agentToRun == nil {
				// Agent not found - send error and continue
				e := fmt.Errorf("transfer failed: agent '%s' not found", transferAction.DestAgentName)
				generator.Send(&AgentEvent{Err: e})
				return
			}

			// Execute the agent in its own lane
			subAIter := agentToRun.Run(childContexts[idx], nil, opts...)
			
			// Collect events for this lane
			var laneEventsForAgent []*agentEventWrapper
			
			for {
				subEvent, ok := subAIter.Next()
				if !ok {
					break
				}

				generator.Send(subEvent)
				
				// Check for interrupt action
				if subEvent.Action != nil && subEvent.Action.internalInterrupted != nil {
					mu.Lock()
					subInterruptSignals = append(subInterruptSignals, subEvent.Action.internalInterrupted)
					
					// Store events collected so far for this lane
					laneEvents[transferAction.DestAgentName] = laneEventsForAgent
					mu.Unlock()
					
					// Stop processing this lane when interrupted
					return
				}
				
				// Store event for potential resume
				if subEvent.Action == nil || subEvent.Action.Interrupted == nil {
					// COPY the event before storing (streams can only be consumed once)
					copied := copyAgentEvent(subEvent)
					setAutomaticClose(copied)
					laneEventsForAgent = append(laneEventsForAgent, &agentEventWrapper{
						AgentEvent: copied,
					})
				}
			}
			
			// Store completed lane events
			mu.Lock()
			laneEvents[transferAction.DestAgentName] = laneEventsForAgent
			mu.Unlock()
		}(i, transfer)
	}

	// Wait for all concurrent lanes to complete
	wg.Wait()

	// Check if any lane was interrupted
	if len(subInterruptSignals) > 0 {
		// Create composite interrupt with the collected state
		state := &dynamicParallelState{
			LaneEvents: laneEvents,
		}
		
		event := CompositeInterrupt(ctx, "Concurrent transfer interrupted", state, subInterruptSignals...)
		
		// Set agent name and run path for proper identification
		event.AgentName = a.Name(ctx)
		event.RunPath = getRunCtx(ctx).RunPath
		generator.Send(event)
		return
	}

	// Join all child contexts back to the parent
	joinRunCtxs(ctx, childContexts...)
}
