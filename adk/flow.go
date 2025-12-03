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
	"log"
	"runtime/debug"
	"strings"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type HistoryEntry struct {
	IsUserInput bool
	AgentName   string
	Message     Message
}

type HistoryRewriter func(ctx context.Context, entries []*HistoryEntry) ([]Message, error)

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

// SetSubAgents sets sub-agents for the given agent and returns the updated agent.
func SetSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (ResumableAgent, error) {
	return setSubAgents(ctx, agent, subAgents)
}

type AgentOption func(options *flowAgent)

// WithDisallowTransferToParent prevents a sub-agent from transferring to its parent.
func WithDisallowTransferToParent() AgentOption {
	return func(fa *flowAgent) {
		fa.disallowTransferToParent = true
	}
}

// WithHistoryRewriter sets a rewriter to transform conversation history.
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

// AgentWithOptions wraps an agent with flow-specific options and returns it.
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
			var retryErr *WillRetryError
			if errors.As(err, &retryErr) {
				log.Printf("failed to get message from event, but will retry: %v", err)
				continue
			}
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

	nextAgentName, err := getNextResumeAgent(ctx, info)
	if err != nil {
		return genErrorIter(err)
	}

	subAgent := a.getAgent(ctx, nextAgentName)
	if subAgent == nil {
		// the inner agent wrapped by flowAgent may be ANY agent, including flowAgent,
		// AgentWithDeterministicTransferTo, or any other custom agent user defined,
		// or any combinations of the above in any order,
		// that ultimately wraps the flowAgent with sub-agents
		// We need to go through these wrappers to reach the flowAgent with sub-agents.
		if len(a.subAgents) == 0 {
			if ra, ok := a.Agent.(ResumableAgent); ok {
				return ra.Resume(ctx, info, opts...)
			}
		}
		return genErrorIter(fmt.Errorf("failed to resume agent: agent '%s' not found from flowAgent '%s'", nextAgentName, a.Name(ctx)))
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

	var lastAction *AgentAction
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		// RunPath ownership: the eino framework prepends parent context exactly once.
		// Custom agents should NOT include parent segments in event.RunPath.
		// Any event.RunPath provided by custom agents is treated as relative child provenance.
		// STRONG RECOMMENDATION: Do NOT set RunPath in custom agents unless you truly need to add
		//   relative child provenance; never add parent/current segments. Incorrect settings will
		//   duplicate head segments after merge and cause non-recording.
		// Here we merge: framework runCtx.RunPath + custom-provided event.RunPath.
		if len(event.RunPath) > 0 {
			rp := make([]RunStep, 0, len(runCtx.RunPath)+len(event.RunPath))
			rp = append(rp, runCtx.RunPath...)
			rp = append(rp, event.RunPath...)
			event.RunPath = rp
		} else {
			event.AgentName = a.Name(ctx)
			event.RunPath = runCtx.RunPath
		}
		// Recording policy: exact RunPath match (non-interrupt) indicates events belonging to this agent execution.
		// This prevents parent recording of child/tool-internal emissions.
		if (event.Action == nil || event.Action.Interrupted == nil) && exactRunPathMatch(runCtx.RunPath, event.RunPath) {
			// copy the event so that the copied event's stream is exclusive for any potential consumer
			// copy before adding to session because once added to session it's stream could be consumed by genAgentInput at any time
			// interrupt action are not added to session, because ALL information contained in it
			// is either presented to end-user, or made available to agents through other means
			copied := copyAgentEvent(event)
			setAutomaticClose(copied)
			setAutomaticClose(event)
			runCtx.Session.addEvent(copied)
		}
		// Action gating uses exact run-path match as well:
		// only actions originating from this agent execution (not child/tool runs)
		// should influence parent control flow (exit/transfer/interrupt).
		if exactRunPathMatch(runCtx.RunPath, event.RunPath) {
			lastAction = event.Action
		}
		generator.Send(event)
	}

	var destName string
	if lastAction != nil {
		if lastAction.Interrupted != nil {
			return
		}
		if lastAction.Exit {
			return
		}

		if lastAction.TransferToAgent != nil {
			destName = lastAction.TransferToAgent.DestAgentName
		}
	}

	// handle transferring to another agent
	if destName != "" {
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
	}
}

func exactRunPathMatch(aPath, bPath []RunStep) bool {
	if len(aPath) != len(bPath) {
		return false
	}
	for i := range aPath {
		if !aPath[i].Equals(bPath[i]) {
			return false
		}
	}
	return true
}
