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
	"runtime/debug"

	"github.com/cloudwego/eino/internal/safe"
)

// executionFrame lasts until both the source and its EOF continuation finish.
// It is local to an invocation and never becomes part of persisted run state.
type executionFrame[M MessageType] struct {
	agentName       string
	ctx             context.Context
	ctxForSubAgents context.Context
	runCtx          *runContext
	cancelCtx       *cancelContext
}

func runExecution[M MessageType](ctx context.Context, binding executionBinding[M], input *TypedAgentInput[M], opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	agentName := binding.agent.Name(ctx)
	ctx, runCtx := initTypedRunCtx(ctx, agentName, input)
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, agentName)
	o := getCommonOptions(nil, opts...)
	frame := executionFrame[M]{agentName: agentName, ctx: ctx, runCtx: runCtx, cancelCtx: o.cancelCtx}

	processedInput, err := binding.prepareInput(ctx, runCtx, input, o.skipTransferMessages)
	cb := executionCallbacks[M]{}
	if err != nil {
		if frame.cancelCtx != nil {
			frame.cancelCtx.markDone()
		}
		// A failed projection uses the existing callback context; designated local
		// handlers have not been initialized yet.
		frame.ctx = cb.onStart(ctx, input, nil)
		return wrapExecutionRelay(frame.ctx, typedErrorIter[M](err), cb)
	}

	frame.ctxForSubAgents = ctx
	agentType := getTypedAgentType(binding.agent)
	ctx = cb.init(ctx, agentName, agentType, filterOptions(agentName, opts)...)
	frame.ctx = cb.onStart(ctx, processedInput, nil)
	return frame.start(binding.runSource(frame, processedInput, opts...))
}

func resumeExecution[M MessageType](ctx context.Context, binding executionBinding[M], info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	agentName := binding.agent.Name(ctx)
	ctx, info = buildResumeInfo(ctx, agentName, info)
	ctxForSubAgents := ctx
	o := getCommonOptions(nil, opts...)
	cb := executionCallbacks[M]{}
	agentType := getTypedAgentType(binding.agent)
	ctx = cb.init(ctx, agentName, agentType, filterOptions(agentName, opts)...)
	ctx = cb.onStart(ctx, nil, info)
	frame := executionFrame[M]{agentName: agentName, ctx: ctx, ctxForSubAgents: ctxForSubAgents, runCtx: getRunCtx(ctxForSubAgents), cancelCtx: o.cancelCtx}
	return frame.start(binding.resumeSource(frame, agentName, info, opts...))
}

func (f executionFrame[M]) start(source executionSource[M]) *AsyncIterator[*TypedAgentEvent[M]] {
	cb := executionCallbacks[M]{}
	if source.err != nil {
		if source.markDone && f.cancelCtx != nil {
			f.cancelCtx.markDone()
		}
		return wrapExecutionRelay(f.ctx, typedErrorIter[M](source.err), cb)
	}
	switch source.kind {
	case executionSourceWorkflow:
		// Container callbacks observe cancellation after its conversion.
		return wrapExecutionRelay(source.ctx, wrapIterWithCancelCtx(source.iter, f.cancelCtx), cb)
	case executionSourceResumeRelay:
		// Ancestor callbacks retain the downstream event's original ownership.
		return wrapIterWithCancelCtx(wrapExecutionRelay(source.ctx, source.iter, cb), f.cancelCtx)
	default:
		iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
		f.ctx = withCancelContext(f.ctx, f.cancelCtx)
		go f.process(source, gen)
		return wrapIterWithCancelCtx(iter, f.cancelCtx)
	}
}

func (f executionFrame[M]) process(source executionSource[M], gen *AsyncGenerator[*TypedAgentEvent[M]]) {
	cbIter, cbGen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	// Keep this before the recovery boundary, matching the existing ordinary path.
	executionCallbacks[M]{}.onEnd(f.ctx, cbIter)
	defer func() {
		if panicErr := recover(); panicErr != nil {
			gen.Send(&TypedAgentEvent[M]{Err: safe.NewPanicErr(panicErr, debug.Stack())})
		}
		cbGen.Close()
		gen.Close()
	}()

	var lastAction *AgentAction
	for {
		event, ok := source.iter.Next()
		if !ok {
			break
		}
		// Already attributed child/tool events retain their names and paths.
		if len(event.RunPath) == 0 {
			event.AgentName = source.name(f.ctx)
			event.RunPath = f.runCtx.RunPath
		}
		owned := exactRunPathMatch(f.runCtx.RunPath, event.RunPath)
		if (event.Action == nil || event.Action.Interrupted == nil) && owned {
			// Copy before publishing to history, which may consume streams immediately.
			copied := copyTypedAgentEvent(event)
			typedSetAutomaticClose(copied)
			typedSetAutomaticClose(event)
			addTypedEvent(f.runCtx.Session, copied)
		}
		if owned {
			// A nil action also replaces the previous action.
			lastAction = event.Action
		}
		copied := copyTypedAgentEvent(event)
		typedSetAutomaticClose(copied)
		typedSetAutomaticClose(event)
		cbGen.Send(copied)
		gen.Send(event)
	}
	if source.continueAtEOF != nil {
		// Continuation events are neither recorded nor observed again by the source.
		source.continueAtEOF(lastAction, gen)
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

// Relay callbacks receive a live iterator synchronously, before the wrapper returns.
func wrapExecutionRelay[M MessageType](ctx context.Context, iter *AsyncIterator[*TypedAgentEvent[M]], cb executionCallbacks[M]) *AsyncIterator[*TypedAgentEvent[M]] {
	cbIter, cbGen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	cb.onEnd(ctx, cbIter)
	outIter, outGen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
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
			cbGen.Send(copyTypedAgentEvent(event))
			outGen.Send(event)
		}
	}()
	return outIter
}
