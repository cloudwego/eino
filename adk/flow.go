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
	"strings"

	"github.com/cloudwego/eino/compose"
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
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
func SetSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (ResumableAgent, error) {
	return setSubAgents(ctx, agent, subAgents)
}

type AgentOption func(options *flowAgent)

// WithDisallowTransferToParent prevents a sub-agent from transferring to its parent.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
func WithDisallowTransferToParent() AgentOption {
	return func(fa *flowAgent) {
		fa.disallowTransferToParent = true
	}
}

// WithHistoryRewriter sets a rewriter to transform conversation history
// during agent transfers.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
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
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
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

	rewritten := schema.UserMessage(sb.String())
	if msg.MultiContent != nil {
		rewritten.MultiContent = append([]schema.ChatMessagePart{}, msg.MultiContent...) //nolint:staticcheck // backward compat with deprecated MultiContent field
	}
	if msg.UserInputMultiContent != nil {
		rewritten.UserInputMultiContent = append([]schema.MessageInputPart{}, msg.UserInputMultiContent...)
	}

	// Convert AssistantGenMultiContent to UserInputMultiContent, since the role changes to User.
	// Reasoning parts have no user input equivalent and are dropped.
	for _, part := range msg.AssistantGenMultiContent {
		switch part.Type {
		case schema.ChatMessagePartTypeText:
			rewritten.UserInputMultiContent = append(rewritten.UserInputMultiContent, schema.MessageInputPart{
				Type:  part.Type,
				Text:  part.Text,
				Extra: part.Extra,
			})
		case schema.ChatMessagePartTypeImageURL:
			if part.Image != nil {
				rewritten.UserInputMultiContent = append(rewritten.UserInputMultiContent, schema.MessageInputPart{
					Type:  part.Type,
					Image: &schema.MessageInputImage{MessagePartCommon: part.Image.MessagePartCommon},
					Extra: part.Extra,
				})
			}
		case schema.ChatMessagePartTypeAudioURL:
			if part.Audio != nil {
				rewritten.UserInputMultiContent = append(rewritten.UserInputMultiContent, schema.MessageInputPart{
					Type:  part.Type,
					Audio: &schema.MessageInputAudio{MessagePartCommon: part.Audio.MessagePartCommon},
					Extra: part.Extra,
				})
			}
		case schema.ChatMessagePartTypeVideoURL:
			if part.Video != nil {
				rewritten.UserInputMultiContent = append(rewritten.UserInputMultiContent, schema.MessageInputPart{
					Type:  part.Type,
					Video: &schema.MessageInputVideo{MessagePartCommon: part.Video.MessagePartCommon},
					Extra: part.Extra,
				})
			}
		}
	}

	return rewritten
}

func genMsg(entry *HistoryEntry, agentName string) (Message, error) {
	msg := entry.Message
	if entry.AgentName != agentName {
		msg = rewriteMessage(msg, entry.AgentName)
	}

	return msg, nil
}

func deepCopyAgentInput(ai *AgentInput) *AgentInput {
	copied := &AgentInput{
		Messages:        make([]Message, len(ai.Messages)),
		EnableStreaming: ai.EnableStreaming,
	}

	copy(copied.Messages, ai.Messages)

	return copied
}

func (a *flowAgent) genAgentInput(ctx context.Context, runCtx *runContext, skipTransferMessages bool) (*AgentInput, error) {
	input := deepCopyAgentInput(runCtx.RootInput)

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
	return runExecution(ctx, bindFlowExecution(a), input, opts...)
}

func (a *flowAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return resumeExecution(ctx, bindFlowExecution(a), info, opts...)
}

// DeterministicTransferConfig is the configuration for AgentWithDeterministicTransferTo.
//
// NOT RECOMMENDED: Agent transfer with full context sharing between agents has not proven
// to be more effective empirically. Consider using ChatModelAgent with AgentTool
// or DeepAgent instead for most multi-agent scenarios.
type DeterministicTransferConfig struct {
	Agent        Agent
	ToAgentNames []string
}

// continueAtEOF interprets only the source's final owned action. Successor events
// bypass the source callback and history recorder, while its callback stays live.
func (a *flowAgent) continueAtEOF(ctxForSubAgents context.Context, lastAction *AgentAction,
	generator *AsyncGenerator[*AgentEvent], opts ...AgentRunOption) {
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
		agentToRun := a.getAgent(ctxForSubAgents, destName)
		if agentToRun == nil {
			e := fmt.Errorf("transfer failed: agent '%s' not found when transferring from '%s'",
				destName, a.Name(ctxForSubAgents))
			generator.Send(&AgentEvent{Err: e})
			return
		}

		childCancelCtx := deriveCheckpointAwareSubAgentCancelContext(ctxForSubAgents, opts)
		childOpts := appendCancelContextOption(opts, childCancelCtx)
		subAIter := agentToRun.Run(ctxForSubAgents, nil /*subagents get input from runCtx*/, childOpts...)
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
