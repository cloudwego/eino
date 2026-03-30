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

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/compose"
	icb "github.com/cloudwego/eino/internal/callbacks"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type TypedHistoryEntry[M MessageType] struct {
	IsUserInput bool
	AgentName   string
	Message     M
}

type HistoryEntry = TypedHistoryEntry[*schema.Message]

type TypedHistoryRewriter[M MessageType] func(ctx context.Context, entries []*TypedHistoryEntry[M]) ([]M, error)

type HistoryRewriter = TypedHistoryRewriter[*schema.Message]

type typedFlowAgent[M MessageType] struct {
	TypedAgent[M]

	subAgents   []*typedFlowAgent[M]
	parentAgent *typedFlowAgent[M]

	disallowTransferToParent bool
	historyRewriter          TypedHistoryRewriter[M]

	checkPointStore compose.CheckPointStore
}

type flowAgent = typedFlowAgent[*schema.Message]

func (a *typedFlowAgent[M]) deepCopy() *typedFlowAgent[M] {
	ret := &typedFlowAgent[M]{
		TypedAgent:               a.TypedAgent,
		subAgents:                make([]*typedFlowAgent[M], 0, len(a.subAgents)),
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

type TypedAgentOption[M MessageType] func(options *typedFlowAgent[M])

type AgentOption = TypedAgentOption[*schema.Message]

// WithDisallowTransferToParent prevents a sub-agent from transferring to its parent.
func WithDisallowTransferToParent() AgentOption {
	return TypedWithDisallowTransferToParent[*schema.Message]()
}

// TypedWithDisallowTransferToParent prevents a typed sub-agent from transferring to its parent.
func TypedWithDisallowTransferToParent[M MessageType]() TypedAgentOption[M] {
	return func(fa *typedFlowAgent[M]) {
		fa.disallowTransferToParent = true
	}
}

// WithHistoryRewriter sets a rewriter to transform conversation history.
func WithHistoryRewriter(h HistoryRewriter) AgentOption {
	return TypedWithHistoryRewriter[*schema.Message](h)
}

// TypedWithHistoryRewriter sets a typed rewriter to transform conversation history.
func TypedWithHistoryRewriter[M MessageType](h TypedHistoryRewriter[M]) TypedAgentOption[M] {
	return func(fa *typedFlowAgent[M]) {
		fa.historyRewriter = h
	}
}

func toTypedFlowAgent[M MessageType](ctx context.Context, agent TypedAgent[M], opts ...TypedAgentOption[M]) *typedFlowAgent[M] {
	var fa *typedFlowAgent[M]
	var ok bool
	if fa, ok = agent.(*typedFlowAgent[M]); !ok {
		fa = &typedFlowAgent[M]{TypedAgent: agent}
	} else {
		fa = fa.deepCopy()
	}
	for _, opt := range opts {
		opt(fa)
	}

	if fa.historyRewriter == nil {
		fa.historyRewriter = buildTypedDefaultHistoryRewriter[M](agent.Name(ctx))
	}

	return fa
}

func toFlowAgent(ctx context.Context, agent Agent, opts ...AgentOption) *flowAgent {
	return toTypedFlowAgent(ctx, agent, opts...)
}

// AgentWithOptions wraps an agent with flow-specific options and returns it.
func AgentWithOptions(ctx context.Context, agent Agent, opts ...AgentOption) Agent {
	return toFlowAgent(ctx, agent, opts...)
}

// TypedSetSubAgents sets the sub-agents for a typed agent and returns the resulting TypedResumableAgent.
func TypedSetSubAgents[M MessageType](ctx context.Context, agent TypedAgent[M], subAgents []TypedAgent[M]) (TypedResumableAgent[M], error) {
	return typedSetSubAgents(ctx, agent, subAgents)
}

func typedSetSubAgents[M MessageType](ctx context.Context, agent TypedAgent[M], subAgents []TypedAgent[M]) (*typedFlowAgent[M], error) {
	fa := toTypedFlowAgent(ctx, agent)

	if len(fa.subAgents) > 0 {
		return nil, errors.New("agent's sub-agents has already been set")
	}

	if onAgent, ok_ := fa.TypedAgent.(TypedOnSubAgents[M]); ok_ {
		err := onAgent.OnSetSubAgents(ctx, subAgents)
		if err != nil {
			return nil, err
		}
	}

	for _, s := range subAgents {
		fsa := toTypedFlowAgent(ctx, s)

		if fsa.parentAgent != nil {
			return nil, errors.New("agent has already been set as a sub-agent of another agent")
		}

		fsa.parentAgent = fa
		if onAgent, ok__ := fsa.TypedAgent.(TypedOnSubAgents[M]); ok__ {
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

func setSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (*flowAgent, error) {
	return typedSetSubAgents(ctx, agent, subAgents)
}

func (a *typedFlowAgent[M]) getAgent(ctx context.Context, name string) *typedFlowAgent[M] {
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
		rewritten.MultiContent = append([]schema.ChatMessagePart{}, msg.MultiContent...)
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

func rewriteAgenticMessage(msg *schema.AgenticMessage, agentName string) *schema.AgenticMessage {
	var sb strings.Builder
	sb.WriteString("For context:")

	if msg.Role == schema.AgenticRoleTypeAssistant {
		for _, block := range msg.ContentBlocks {
			if block == nil {
				continue
			}
			if block.Type == schema.ContentBlockTypeAssistantGenText && block.AssistantGenText != nil {
				sb.WriteString(fmt.Sprintf(" [%s] said: %s.", agentName, block.AssistantGenText.Text))
			}
			if block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
				sb.WriteString(fmt.Sprintf(" [%s] called tool: `%s` with arguments: %s.",
					agentName, block.FunctionToolCall.Name, block.FunctionToolCall.Arguments))
			}
		}
	} else if msg.Role == schema.AgenticRoleTypeUser {
		for _, block := range msg.ContentBlocks {
			if block == nil {
				continue
			}
			if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
				sb.WriteString(fmt.Sprintf(" [%s] `%s` tool returned result: %s.",
					agentName, block.FunctionToolResult.Name, block.FunctionToolResult.Result))
			}
		}
	}

	rewritten := schema.UserAgenticMessage(sb.String())

	for _, block := range msg.ContentBlocks {
		if block == nil {
			continue
		}
		switch block.Type {
		case schema.ContentBlockTypeUserInputText,
			schema.ContentBlockTypeUserInputImage,
			schema.ContentBlockTypeUserInputAudio,
			schema.ContentBlockTypeUserInputVideo,
			schema.ContentBlockTypeUserInputFile:
			copied := *block
			rewritten.ContentBlocks = append(rewritten.ContentBlocks, &copied)
		}
	}

	return rewritten
}

func typedRewriteMessage[M MessageType](msg M, agentName string) M {
	switch v := any(msg).(type) {
	case *schema.Message:
		return any(rewriteMessage(v, agentName)).(M)
	case *schema.AgenticMessage:
		return any(rewriteAgenticMessage(v, agentName)).(M)
	default:
		return msg
	}
}

func typedGenMsg[M MessageType](entry *TypedHistoryEntry[M], agentName string) (M, error) {
	msg := entry.Message
	if entry.AgentName != agentName {
		msg = typedRewriteMessage(msg, entry.AgentName)
	}
	return msg, nil
}

func typedDeepCopyAgentInput[M MessageType](ai *TypedAgentInput[M]) *TypedAgentInput[M] {
	copied := &TypedAgentInput[M]{
		Messages:        make([]M, len(ai.Messages)),
		EnableStreaming: ai.EnableStreaming,
	}
	copy(copied.Messages, ai.Messages)
	return copied
}

func isToolResultEvent[M MessageType](event *typedAgentEventWrapper[M]) bool {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		e := any(event.event).(*AgentEvent)
		return e.Output != nil &&
			e.Output.MessageOutput != nil &&
			e.Output.MessageOutput.Role == schema.Tool
	case *schema.AgenticMessage:
		e := any(event.event).(*TypedAgentEvent[*schema.AgenticMessage])
		if e.Output == nil || e.Output.MessageOutput == nil {
			return false
		}
		mv := e.Output.MessageOutput
		var msg *schema.AgenticMessage
		if mv.IsStreaming {
			return false
		}
		msg = mv.Message
		if msg == nil {
			return false
		}
		for _, block := range msg.ContentBlocks {
			if block != nil && block.Type == schema.ContentBlockTypeFunctionToolResult {
				return true
			}
		}
		return false
	default:
		return false
	}
}

func (a *typedFlowAgent[M]) genAgentInput(ctx context.Context, runCtx *runContext, skipTransferMessages bool) (*TypedAgentInput[M], error) {
	var input *TypedAgentInput[M]
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		input = typedDeepCopyAgentInput(any(runCtx.RootInput).(*TypedAgentInput[M]))
	} else {
		if ri, ok := runCtx.TypedRootInput.(*TypedAgentInput[M]); ok {
			input = typedDeepCopyAgentInput(ri)
		} else {
			input = &TypedAgentInput[M]{}
		}
	}

	events := getTypedEvents[M](runCtx.Session)
	historyEntries := make([]*TypedHistoryEntry[M], 0)

	for _, m := range input.Messages {
		historyEntries = append(historyEntries, &TypedHistoryEntry[M]{
			IsUserInput: true,
			Message:     m,
		})
	}

	for _, event := range events {
		if skipTransferMessages && event.event.Action != nil && event.event.Action.TransferToAgent != nil {
			if isToolResultEvent(event) && len(historyEntries) > 0 {
				historyEntries = historyEntries[:len(historyEntries)-1]
			}
			continue
		}

		msg, err := getMessageFromTypedWrappedEvent(event)
		if err != nil {
			var retryErr *WillRetryError
			if errors.As(err, &retryErr) {
				log.Printf("failed to get message from event, but will retry: %v", err)
				continue
			}
			return nil, err
		}

		if any(msg) == any(zero) {
			continue
		}

		historyEntries = append(historyEntries, &TypedHistoryEntry[M]{
			AgentName: event.event.AgentName,
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

func buildTypedDefaultHistoryRewriter[M MessageType](agentName string) TypedHistoryRewriter[M] {
	return func(ctx context.Context, entries []*TypedHistoryEntry[M]) ([]M, error) {
		messages := make([]M, 0, len(entries))
		var err error
		for _, entry := range entries {
			msg := entry.Message
			if !entry.IsUserInput {
				msg, err = typedGenMsg(entry, agentName)
				if err != nil {
					return nil, fmt.Errorf("gen agent input failed: %w", err)
				}
			}

			var zero M
			if any(msg) != any(zero) {
				messages = append(messages, msg)
			}
		}

		return messages, nil
	}
}

func getTypedAgentType[M MessageType](agent TypedAgent[M]) string {
	if msgAgent, ok := any(agent).(Agent); ok {
		return getAgentType(msgAgent)
	}
	if typer, ok := any(agent).(interface{ GetType() string }); ok {
		return typer.GetType()
	}
	return ""
}

func (a *typedFlowAgent[M]) Run(ctx context.Context, input *TypedAgentInput[M], opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	agentName := a.Name(ctx)

	var runCtx *runContext
	ctx, runCtx = initTypedRunCtx(ctx, agentName, input)
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, agentName)

	o := getCommonOptions(nil, opts...)
	cancelCtx := o.cancelCtx

	processedInput, err := a.genAgentInput(ctx, runCtx, o.skipTransferMessages)
	if err != nil {
		if cancelCtx != nil {
			cancelCtx.markDone()
		}
		var zero M
		if _, ok := any(zero).(*schema.Message); ok {
			cbInput := &AgentCallbackInput{Input: any(input).(*AgentInput)}
			ctx = callbacks.OnStart(ctx, cbInput)
			return any(wrapIterWithOnEnd(ctx, genErrorIter(err))).(*AsyncIterator[*TypedAgentEvent[M]])
		}
		cbInput := &AgenticAgentCallbackInput{Input: any(input).(*TypedAgentInput[*schema.AgenticMessage])}
		ctx = callbacks.OnStart(ctx, cbInput)
		return any(wrapAgenticIterWithOnEnd(ctx, genAgenticErrorIter(err))).(*AsyncIterator[*TypedAgentEvent[M]])
	}

	ctxForSubAgents := ctx

	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		agentType := getTypedAgentType(a.TypedAgent)
		ctx = initAgentCallbacks(ctx, agentName, agentType, filterOptions(agentName, opts)...)
		cbInput := &AgentCallbackInput{Input: any(processedInput).(*AgentInput)}
		ctx = callbacks.OnStart(ctx, cbInput)
	} else {
		agentType := getTypedAgentType(a.TypedAgent)
		ctx = initAgenticAgentCallbacks(ctx, agentName, agentType, filterOptions(agentName, opts)...)
		cbInput := &AgenticAgentCallbackInput{Input: any(processedInput).(*TypedAgentInput[*schema.AgenticMessage])}
		ctx = callbacks.OnStart(ctx, cbInput)
	}

	input = processedInput

	if wf, ok := a.TypedAgent.(*typedWorkflowAgent[M]); ok {
		ctx = withCancelContext(ctx, cancelCtx)
		filteredOpts := filterCancelOption(filterCallbackHandlersForNestedAgents(agentName, opts))
		iter := wf.Run(ctx, input, filteredOpts...)
		iter = wrapIterWithCancelCtx(iter, cancelCtx)
		return typedWrapIterWithOnEnd(ctx, iter)
	}

	aIter := a.TypedAgent.Run(withCancelContext(ctx, cancelCtx), input, filterOptions(agentName, opts)...)

	iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()

	go a.run(withCancelContext(ctx, cancelCtx), withCancelContext(ctxForSubAgents, cancelCtx), runCtx, aIter, generator, filterCancelOption(opts)...)

	return wrapIterWithCancelCtx(iterator, cancelCtx)
}

func (a *typedFlowAgent[M]) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	agentName := a.Name(ctx)

	ctx, info = buildResumeInfo(ctx, agentName, info)

	ctxForSubAgents := ctx

	o := getCommonOptions(nil, opts...)
	cancelCtx := o.cancelCtx

	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		agentType := getTypedAgentType(a.TypedAgent)
		ctx = initAgentCallbacks(ctx, agentName, agentType, filterOptions(agentName, opts)...)
		cbInput := &AgentCallbackInput{ResumeInfo: info}
		ctx = callbacks.OnStart(ctx, cbInput)
	} else {
		agentType := getTypedAgentType(a.TypedAgent)
		ctx = initAgenticAgentCallbacks(ctx, agentName, agentType, filterOptions(agentName, opts)...)
		cbInput := &AgenticAgentCallbackInput{ResumeInfo: info}
		ctx = callbacks.OnStart(ctx, cbInput)
	}

	if info.WasInterrupted {
		if ra, ok := a.TypedAgent.(TypedResumableAgent[M]); ok {
			if _, ok := ra.(*typedWorkflowAgent[M]); ok {
				ctx = withCancelContext(ctx, cancelCtx)
				filteredOpts := filterCancelOption(filterCallbackHandlersForNestedAgents(agentName, opts))
				aIter := ra.Resume(ctx, info, filteredOpts...)
				aIter = wrapIterWithCancelCtx(aIter, cancelCtx)
				return typedWrapIterWithOnEnd(ctx, aIter)
			}

			aIter := ra.Resume(withCancelContext(ctx, cancelCtx), info, opts...)

			iterator, generator := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
			go a.run(withCancelContext(ctx, cancelCtx), withCancelContext(ctxForSubAgents, cancelCtx), getRunCtx(ctxForSubAgents), aIter, generator, filterCancelOption(opts)...)
			return wrapIterWithCancelCtx(iterator, cancelCtx)
		}

		if cancelCtx != nil {
			cancelCtx.markDone()
		}
		return typedWrapIterWithOnEnd(ctx, typedErrorIter[M](fmt.Errorf("failed to resume agent: agent '%s' is an interrupt point "+
			"but is not a ResumableAgent", agentName)))
	}

	nextAgentName, err := getNextResumeAgent(ctx, info)
	if err != nil {
		if cancelCtx != nil {
			cancelCtx.markDone()
		}
		return typedWrapIterWithOnEnd(ctx, typedErrorIter[M](err))
	}

	subAgent := a.getAgent(ctxForSubAgents, nextAgentName)
	if subAgent == nil {
		if len(a.subAgents) == 0 {
			if ra, ok := a.TypedAgent.(TypedResumableAgent[M]); ok {
				ctx = withCancelContext(ctx, cancelCtx)
				innerIter := ra.Resume(ctx, info, filterCancelOption(opts)...)
				return wrapIterWithCancelCtx(typedWrapIterWithOnEnd(ctx, innerIter), cancelCtx)
			}
		}
		if cancelCtx != nil {
			cancelCtx.markDone()
		}
		return typedWrapIterWithOnEnd(ctx, typedErrorIter[M](fmt.Errorf("failed to resume agent: agent '%s' not found from flowAgent '%s'", nextAgentName, agentName)))
	}

	ctxForSubAgents = withCancelContext(ctxForSubAgents, cancelCtx)
	innerIter := subAgent.Resume(ctxForSubAgents, info, filterCancelOption(opts)...)
	return wrapIterWithCancelCtx(typedWrapIterWithOnEnd(ctx, innerIter), cancelCtx)
}

type TypedDeterministicTransferConfig[M MessageType] struct {
	Agent        TypedAgent[M]
	ToAgentNames []string
}

type DeterministicTransferConfig = TypedDeterministicTransferConfig[*schema.Message]

func (a *typedFlowAgent[M]) run(
	ctx context.Context,
	ctxForSubAgents context.Context,
	runCtx *runContext,
	aIter *AsyncIterator[*TypedAgentEvent[M]],
	generator *AsyncGenerator[*TypedAgentEvent[M]],
	opts ...AgentRunOption) {

	var zero M
	isMessageType := false
	if _, ok := any(zero).(*schema.Message); ok {
		isMessageType = true
	}

	var cbGen *AsyncGenerator[*AgentEvent]
	var agenticCbGen *AsyncGenerator[*TypedAgentEvent[*schema.AgenticMessage]]
	if isMessageType {
		var cbIter *AsyncIterator[*AgentEvent]
		cbIter, cbGen = NewAsyncIteratorPair[*AgentEvent]()
		cbOutput := &AgentCallbackOutput{Events: cbIter}
		icb.On(ctx, cbOutput, icb.BuildOnEndHandleWithCopy(copyAgentCallbackOutput), callbacks.TimingOnEnd, false)
	} else {
		var agenticCbIter *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]
		agenticCbIter, agenticCbGen = NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
		cbOutput := &AgenticAgentCallbackOutput{Events: agenticCbIter}
		icb.On(ctx, cbOutput, icb.BuildOnEndHandleWithCopy(copyAgenticAgentCallbackOutput), callbacks.TimingOnEnd, false)
	}

	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			generator.Send(&TypedAgentEvent[M]{Err: e})
		}

		if cbGen != nil {
			cbGen.Close()
		}
		if agenticCbGen != nil {
			agenticCbGen.Close()
		}
		generator.Close()
	}()

	var lastAction *AgentAction
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		if len(event.RunPath) == 0 {
			event.AgentName = a.Name(ctx)
			event.RunPath = runCtx.RunPath
		}
		if (event.Action == nil || event.Action.Interrupted == nil) && exactRunPathMatch(runCtx.RunPath, event.RunPath) {
			copied := copyTypedAgentEvent(event)
			typedSetAutomaticClose(copied)
			typedSetAutomaticClose(event)
			addTypedEvent(runCtx.Session, copied)
		}
		if exactRunPathMatch(runCtx.RunPath, event.RunPath) {
			lastAction = event.Action
		}
		if isMessageType && cbGen != nil {
			msgCopied := copyTypedAgentEvent(event)
			typedSetAutomaticClose(msgCopied)
			typedSetAutomaticClose(event)
			cbGen.Send(any(msgCopied).(*AgentEvent))
			generator.Send(event)
		} else if agenticCbGen != nil {
			agenticCopied := copyTypedAgentEvent(event)
			typedSetAutomaticClose(agenticCopied)
			typedSetAutomaticClose(event)
			agenticCbGen.Send(any(agenticCopied).(*TypedAgentEvent[*schema.AgenticMessage]))
			generator.Send(event)
		} else {
			typedSetAutomaticClose(event)
			generator.Send(event)
		}
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

	if destName != "" {
		agentToRun := a.getAgent(ctxForSubAgents, destName)
		if agentToRun == nil {
			e := fmt.Errorf("transfer failed: agent '%s' not found when transferring from '%s'",
				destName, a.Name(ctxForSubAgents))
			generator.Send(&TypedAgentEvent[M]{Err: e})
			return
		}

		subAIter := agentToRun.Run(ctxForSubAgents, nil, opts...)
		for {
			subEvent, ok_ := subAIter.Next()
			if !ok_ {
				break
			}

			typedSetAutomaticClose(subEvent)
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

func wrapIterWithOnEnd(ctx context.Context, iter *AsyncIterator[*AgentEvent]) *AsyncIterator[*AgentEvent] {
	cbIter, cbGen := NewAsyncIteratorPair[*AgentEvent]()
	cbOutput := &AgentCallbackOutput{Events: cbIter}
	icb.On(ctx, cbOutput, icb.BuildOnEndHandleWithCopy(copyAgentCallbackOutput), callbacks.TimingOnEnd, false)

	outIter, outGen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer func() {
			cbGen.Close()
			outGen.Close()
		}()
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			copied := copyAgentEvent(event)
			cbGen.Send(copied)
			outGen.Send(event)
		}
	}()
	return outIter
}

func typedWrapIterWithOnEnd[M MessageType](ctx context.Context, iter *AsyncIterator[*TypedAgentEvent[M]]) *AsyncIterator[*TypedAgentEvent[M]] {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		msgIter := any(iter).(*AsyncIterator[*AgentEvent])
		return any(wrapIterWithOnEnd(ctx, msgIter)).(*AsyncIterator[*TypedAgentEvent[M]])
	}
	agenticIter := any(iter).(*AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]])
	return any(wrapAgenticIterWithOnEnd(ctx, agenticIter)).(*AsyncIterator[*TypedAgentEvent[M]])
}

func wrapAgenticIterWithOnEnd(ctx context.Context, iter *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]]) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	cbIter, cbGen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	cbOutput := &AgenticAgentCallbackOutput{Events: cbIter}
	icb.On(ctx, cbOutput, icb.BuildOnEndHandleWithCopy(copyAgenticAgentCallbackOutput), callbacks.TimingOnEnd, false)

	outIter, outGen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	go func() {
		defer func() {
			cbGen.Close()
			outGen.Close()
		}()
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			copied := copyAgenticAgentEvent(event)
			cbGen.Send(copied)
			outGen.Send(event)
		}
	}()
	return outIter
}

func genAgenticErrorIter(err error) *AsyncIterator[*TypedAgentEvent[*schema.AgenticMessage]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.AgenticMessage]]()
	gen.Send(&TypedAgentEvent[*schema.AgenticMessage]{Err: err})
	gen.Close()
	return iter
}
