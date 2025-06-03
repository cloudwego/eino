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

	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type runCtxKey struct{}

type flowAgent struct {
	Agent

	subAgents   []*flowAgent
	parentAgent *flowAgent

	disallowTransferToParent bool
}

func toFlowAgent(agent Agent) *flowAgent {
	if fa, ok := agent.(*flowAgent); ok {
		return fa
	}
	return &flowAgent{Agent: agent}
}

func SetSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (Agent, error) {
	return setSubAgents(ctx, agent, subAgents)
}

type AgentOptions struct {
	disallowTransferToParent bool
}

type AgentOption func(options *AgentOptions)

func WithDisallowTransferToParent() AgentOption {
	return func(options *AgentOptions) {
		options.disallowTransferToParent = true
	}
}

func agentWithOptions(_ context.Context, agent Agent, opts ...AgentOption) *flowAgent {
	fa := toFlowAgent(agent)
	var options AgentOptions
	for _, opt := range opts {
		opt(&options)
	}

	fa.disallowTransferToParent = options.disallowTransferToParent

	return fa
}

func AgentWithOptions(ctx context.Context, agent Agent, opts ...AgentOption) Agent {
	return agentWithOptions(ctx, agent, opts...)
}

type runSession struct {
	events []*AgentEvent
	values map[string]any

	mtx sync.Mutex
}

func newRunSession() *runSession {
	return &runSession{
		values: make(map[string]any),
	}
}

func GetSessionValues(ctx context.Context) map[string]any {
	session := getSession(ctx)
	if session == nil {
		return map[string]any{}
	}

	return session.getValues()
}

func SetSessionValue(ctx context.Context, key string, value any) {
	session := getSession(ctx)
	if session == nil {
		return
	}

	session.setValue(key, value)
}

func GetSessionValue(ctx context.Context, key string) (any, bool) {
	session := getSession(ctx)
	if session == nil {
		return nil, false
	}

	return session.getValue(key)
}

func (rs *runSession) addEvent(event *AgentEvent) {
	rs.mtx.Lock()
	rs.events = append(rs.events, event)
	rs.mtx.Unlock()
}

func (rs *runSession) getEvents() []*AgentEvent {
	rs.mtx.Lock()
	events := rs.events
	rs.mtx.Unlock()

	return events
}

func (rs *runSession) getValues() map[string]any {
	rs.mtx.Lock()
	values := make(map[string]any, len(rs.values))
	for k, v := range rs.values {
		values[k] = v
	}
	rs.mtx.Unlock()

	return values
}

func (rs *runSession) setValue(key string, value any) {
	rs.mtx.Lock()
	rs.values[key] = value
	rs.mtx.Unlock()
}

func (rs *runSession) getValue(key string) (any, bool) {
	rs.mtx.Lock()
	value, ok := rs.values[key]
	rs.mtx.Unlock()

	return value, ok
}

type runContext struct {
	rootInput *AgentInput
	runPath   []string

	session *runSession
}

func (rc *runContext) isRoot() bool {
	return len(rc.runPath) == 1
}

func (rc *runContext) deepCopy() *runContext {
	copied := &runContext{
		rootInput: rc.rootInput,
		runPath:   make([]string, len(rc.runPath)),
		session:   rc.session,
	}

	copy(copied.runPath, rc.runPath)

	return copied
}

func setSubAgents(ctx context.Context, agent Agent, subAgents []Agent) (*flowAgent, error) {
	fa := toFlowAgent(agent)

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
		fsa := toFlowAgent(s)

		if fsa.parentAgent != nil {
			return nil, errors.New("agent has already been set as a sub-agent of another agent")
		}

		fsa.parentAgent = fa
		if onAgent, ok__ := fsa.Agent.(OnSubAgents); ok__ {
			err := onAgent.OnSetAsSubAgent(ctx, agent)
			if err != nil {
				return nil, err
			}

			if fa.disallowTransferToParent {
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

func belongToRunPath(eventRunPath []string, runPath []string) bool {
	if len(runPath) < len(eventRunPath) {
		return false
	}

	for i, name := range eventRunPath {
		if runPath[i] != name {
			return false
		}
	}

	return true
}

func rewriteMsg(msg Message, agentName string) Message {
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

func genMsg(event *AgentEvent, agentName string) (Message, error) {
	var msg Message
	var err error
	if modelOutput := event.GetModelOutput(); modelOutput != nil {
		msg, err = modelOutput.Response.GetMessage()
		if err != nil {
			return nil, err
		}
	}

	if toolCallOutput := event.GetToolCallOutput(); toolCallOutput != nil {
		msg, err = toolCallOutput.Response.GetMessage()
		if err != nil {
			return nil, err
		}
	}

	if msg == nil {
		return nil, nil
	}

	if event.AgentName != agentName {
		msg = rewriteMsg(msg, event.AgentName)
	}

	return msg, nil
}

func initRunCtx(ctx context.Context, agentName string, input *AgentInput) (context.Context, *runContext) {
	v := ctx.Value(runCtxKey{})
	var runCtx *runContext
	if v != nil {
		runCtx = v.(*runContext).deepCopy()
	}

	if runCtx == nil {
		runCtx = &runContext{session: newRunSession()}
	}

	runCtx.runPath = append(runCtx.runPath, agentName)
	if runCtx.isRoot() {
		runCtx.rootInput = input
	}

	return context.WithValue(ctx, runCtxKey{}, runCtx), runCtx
}

func getSession(ctx context.Context) *runSession {
	v := ctx.Value(runCtxKey{})

	if v != nil {
		runCtx := v.(*runContext)
		return runCtx.session
	}

	return nil
}

func (ai *AgentInput) deepCopy() *AgentInput {
	copied := &AgentInput{
		Msgs:            make([]Message, len(ai.Msgs)),
		EnableStreaming: ai.EnableStreaming,
	}

	copy(copied.Msgs, ai.Msgs)

	return copied
}

func genAgentInput(runCtx *runContext, agentName string) (*AgentInput, error) {
	if runCtx.isRoot() {
		return runCtx.rootInput, nil
	}

	input := runCtx.rootInput.deepCopy()
	runPath := runCtx.runPath

	events := runCtx.session.getEvents()

	for _, event := range events {
		if !belongToRunPath(event.RunPath, runPath) {
			continue
		}

		msg, err := genMsg(event, agentName)
		if err != nil {
			return nil, fmt.Errorf("gen agent input failed: %w", err)
		}

		if msg != nil {
			input.Msgs = append(input.Msgs, msg)
		}
	}

	return input, nil
}

func (a *flowAgent) Run(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	agentName := a.Name(ctx)

	ctx, runCtx := initRunCtx(ctx, agentName, input)

	input, err := genAgentInput(runCtx, agentName)
	if err != nil {
		iterator, generator := NewAsyncIteratorPair[*AgentEvent]()
		generator.Send(&AgentEvent{Err: err})
		generator.Close()

		return iterator
	}

	if wf, ok := a.Agent.(*workflowAgent); ok {
		return wf.Run(ctx, input)
	}

	aIter := a.Agent.Run(ctx, input)

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

		var destName string
		for {
			event, ok := aIter.Next()
			if !ok {
				break
			}

			event.RunPath = runCtx.runPath
			runCtx.session.addEvent(event)

			generator.Send(event)

			if event.Action != nil && event.Action.Exit {
				return
			}

			if event.Action != nil && event.Action.TransferToAgent != nil {
				destName = event.Action.TransferToAgent.DestAgentName
			}
		}

		// handle transferring to another agent
		if destName != "" {
			agentToRun := a.getAgent(ctx, destName)
			if agentToRun == nil {
				e := errors.New(fmt.Sprintf(
					"transfer failed: agent '%s' not found when transferring from '%s'",
					destName, agentName))
				generator.Send(&AgentEvent{Err: e})
				return
			}

			subAIter := agentToRun.Run(ctx, input)
			for {
				subEvent, ok_ := subAIter.Next()
				if !ok_ {
					break
				}

				generator.Send(subEvent)
			}
		}
	}()

	return iterator
}
