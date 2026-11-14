/*
 * Copyright 2026 CloudWeGo Authors
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

	"github.com/cloudwego/eino/schema"
)

type executionSourceKind uint8

const (
	executionSourceOrdinary executionSourceKind = iota
	executionSourceWorkflow
	executionSourceResumeRelay
)

// executionSource describes only the three existing event delivery paths.
type executionSource[M MessageType] struct {
	kind          executionSourceKind
	ctx           context.Context
	iter          *AsyncIterator[*TypedAgentEvent[M]]
	name          func(context.Context) string
	continueAtEOF func(*AgentAction, *AsyncGenerator[*TypedAgentEvent[M]])
	err           error
	markDone      bool
}

// executionBinding adapts existing business agents without adding a visible agent
// layer. Only Message bindings participate in history projection and transfers.
type executionBinding[M MessageType] struct {
	agent TypedAgent[M]
	flow  *flowAgent
}

func bindExecution[M MessageType](ctx context.Context, agent TypedAgent[M], store CheckPointStore) executionBinding[M] {
	var zero M
	if _, ok := any(zero).(*schema.Message); ok {
		fa := toFlowAgent(ctx, any(agent).(Agent))
		if store != nil {
			fa.checkPointStore = store
		}
		return executionBinding[M]{agent: any(fa.Agent).(TypedAgent[M]), flow: fa}
	}
	return executionBinding[M]{agent: agent}
}

// Direct public-wrapper calls already have their binding; do not deep-copy again.
func bindFlowExecution(agent *flowAgent) executionBinding[*schema.Message] {
	return executionBinding[*schema.Message]{agent: agent.Agent, flow: agent}
}

func (b executionBinding[M]) prepareInput(ctx context.Context, runCtx *runContext, input *TypedAgentInput[M], skipTransferMessages bool) (*TypedAgentInput[M], error) {
	if b.flow == nil {
		return input, nil
	}
	processed, err := b.flow.genAgentInput(ctx, runCtx, skipTransferMessages)
	if err != nil {
		return nil, err
	}
	return any(processed).(*TypedAgentInput[M]), nil
}

func (b executionBinding[M]) ordinarySource(frame executionFrame[M], iter *AsyncIterator[*TypedAgentEvent[M]], opts []AgentRunOption) executionSource[M] {
	source := executionSource[M]{kind: executionSourceOrdinary, iter: iter, name: b.agent.Name}
	// Preserve option closure probing even for Agentic, whose ordinary source has
	// no continuation. Filtering must still happen synchronously after Run/Resume.
	filteredOpts := filterCancelOption(opts)
	if b.flow != nil {
		ctx := withCancelContext(frame.ctxForSubAgents, frame.cancelCtx)
		source.continueAtEOF = func(action *AgentAction, gen *AsyncGenerator[*TypedAgentEvent[M]]) {
			b.flow.continueAtEOF(ctx, action, any(gen).(*AsyncGenerator[*AgentEvent]), filteredOpts...)
		}
	}
	return source
}

func (b executionBinding[M]) runSource(frame executionFrame[M], input *TypedAgentInput[M], opts ...AgentRunOption) executionSource[M] {
	ctx := withCancelContext(frame.ctx, frame.cancelCtx)
	if wf, ok := any(b.agent).(*workflowAgent); ok {
		filteredOpts := filterCancelOption(filterCallbackHandlersForNestedAgents(frame.agentName, opts))
		iter := wf.Run(ctx, any(input).(*AgentInput), filteredOpts...)
		return executionSource[M]{kind: executionSourceWorkflow, ctx: ctx, iter: any(iter).(*AsyncIterator[*TypedAgentEvent[M]])}
	}
	iter := b.agent.Run(ctx, input, filterOptions(frame.agentName, opts)...)
	return b.ordinarySource(frame, iter, opts)
}

func (b executionBinding[M]) resumeSource(frame executionFrame[M], agentName string, info *ResumeInfo, opts ...AgentRunOption) executionSource[M] {
	ctx := withCancelContext(frame.ctx, frame.cancelCtx)
	if info.WasInterrupted {
		if ra, ok := b.agent.(TypedResumableAgent[M]); ok {
			if _, workflow := any(ra).(*workflowAgent); workflow {
				filteredOpts := filterCancelOption(filterCallbackHandlersForNestedAgents(agentName, opts))
				return executionSource[M]{kind: executionSourceWorkflow, ctx: ctx, iter: ra.Resume(ctx, info, filteredOpts...)}
			}
			return b.ordinarySource(frame, ra.Resume(ctx, info, opts...), opts)
		}
		return executionSource[M]{err: fmt.Errorf("failed to resume agent: agent '%s' is an interrupt point "+
			"but is not a ResumableAgent", agentName), markDone: true}
	}

	nextAgentName, err := getNextResumeAgent(frame.ctx, info)
	if err != nil {
		return executionSource[M]{err: err, markDone: true}
	}
	if b.flow != nil {
		subAgent := b.flow.getAgent(frame.ctxForSubAgents, nextAgentName)
		if subAgent != nil {
			childCtx := withCancelContext(frame.ctxForSubAgents, frame.cancelCtx)
			filteredOpts := filterCancelOption(opts)
			childCancelCtx := deriveCheckpointAwareSubAgentCancelContext(childCtx, filteredOpts)
			childOpts := appendCancelContextOption(filteredOpts, childCancelCtx)
			iter := subAgent.Resume(childCtx, info, childOpts...)
			return executionSource[M]{kind: executionSourceResumeRelay, ctx: frame.ctx, iter: any(iter).(*AsyncIterator[*TypedAgentEvent[M]])}
		}
		if len(b.flow.subAgents) != 0 {
			return executionSource[M]{err: fmt.Errorf("failed to resume agent: sub-agent '%s' not found in agent '%s'", nextAgentName, agentName), markDone: true}
		}
	}
	if ra, ok := b.agent.(TypedResumableAgent[M]); ok {
		return executionSource[M]{kind: executionSourceResumeRelay, ctx: ctx, iter: ra.Resume(ctx, info, filterCancelOption(opts)...)}
	}
	if b.flow != nil {
		return executionSource[M]{err: fmt.Errorf(
			"failed to resume agent: agent '%s' (type %T) has no sub-agents and does not implement ResumableAgent interface. "+
				"To support resume, your custom agent wrapper must implement the ResumableAgent interface", agentName, b.agent)}
	}
	return executionSource[M]{err: fmt.Errorf(
		"failed to resume agent: agent '%s' (type %T) does not implement ResumableAgent interface. "+
			"To support resume, your custom agent wrapper must implement the ResumableAgent interface", agentName, b.agent)}
}
