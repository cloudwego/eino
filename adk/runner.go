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
	"fmt"
	"runtime/debug"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type Runner struct {
	a               Agent
	enableStreaming bool
	store           compose.CheckPointStore
	middlewares     []RunnerMiddleware
}

type RunnerConfig struct {
	Agent           Agent
	EnableStreaming bool

	CheckPointStore compose.CheckPointStore
	Middlewares     []RunnerMiddleware
}

// RunnerMiddleware defines the middleware type for Runner.
// It takes a RunnerEndpoint as input and returns a new RunnerEndpoint.
// This allows chaining multiple middlewares that wrap the final business logic,
// forming an "onion model" where each middleware can process input/output and control flow.
type RunnerMiddleware func(RunnerEndpoint) RunnerEndpoint

// RunnerEndpoint represents the execution endpoint function type for Runner.
// Parameters:
// - ctx: the context for controlling cancellation and passing values.
// - messages: the input message slice for the run operation，nil when using Resume.
// - resp: the response AsyncIterator pointer, usually nil on initial call.
// - opts: optional run parameters for flexible configuration.
//
// Returns:
// - An AsyncIterator that yields AgentEvent asynchronously, representing the event stream of the run.
//
// This function signature allows middlewares to intercept and modify the input/output,
type RunnerEndpoint func(ctx context.Context, messages []Message, resp *AsyncIterator[*AgentEvent], opts ...AgentRunOption) *AsyncIterator[*AgentEvent]

func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return &Runner{
		enableStreaming: conf.EnableStreaming,
		a:               conf.Agent,
		store:           conf.CheckPointStore,
		middlewares:     conf.Middlewares,
	}
}

func (r *Runner) Run(ctx context.Context, messages []Message,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	runChain := runnerChain(r.middlewares...)(r.runEndpoint)

	return runChain(ctx, messages, nil, opts...)
}

func (r *Runner) runEndpoint(ctx context.Context, messages []Message, _ *AsyncIterator[*AgentEvent],
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	o := getCommonOptions(nil, opts...)

	fa := toFlowAgent(ctx, r.a)

	input := &AgentInput{
		Messages:        messages,
		EnableStreaming: r.enableStreaming,
	}

	ctx = ctxWithNewRunCtx(ctx)

	AddSessionValues(ctx, o.sessionValues)

	iter := fa.Run(ctx, input, opts...)
	if r.store == nil {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, iter, gen, o.checkPointID)
	return niter
}

func getInterruptRunCtx(ctx context.Context) *runContext {
	cs := getInterruptRunCtxs(ctx)
	if len(cs) == 0 {
		return nil
	}
	return cs[0] // assume that concurrency isn't existed, so only one run ctx is in ctx
}

func (r *Runner) Query(ctx context.Context,
	query string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	return r.Run(ctx, []Message{schema.UserMessage(query)}, opts...)
}

func (r *Runner) Resume(ctx context.Context, checkPointID string, opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error) {
	if r.store == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	runCtx, info, existed, err := getCheckPoint(ctx, r.store, checkPointID)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}
	if !existed {
		return nil, fmt.Errorf("checkpoint[%s] is not existed", checkPointID)
	}

	chain := runnerChain(r.middlewares...)(r.buildResumeEndpoint(checkPointID, runCtx, info))

	return chain(ctx, nil, nil, opts...), nil
}

func (r *Runner) buildResumeEndpoint(checkPointID string, runCtx *runContext, info *ResumeInfo) RunnerEndpoint {
	return func(ctx context.Context, _ []Message, _ *AsyncIterator[*AgentEvent], opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
		ctx = setRunCtx(ctx, runCtx)

		o := getCommonOptions(nil, opts...)
		AddSessionValues(ctx, o.sessionValues)

		aIter := toFlowAgent(ctx, r.a).Resume(ctx, info, opts...)

		niter, gen := NewAsyncIteratorPair[*AgentEvent]()

		go r.handleIter(ctx, aIter, gen, &checkPointID)
		return niter
	}
}

func (r *Runner) handleIter(ctx context.Context, aIter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent], checkPointID *string) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&AgentEvent{Err: e})
		}

		gen.Close()
	}()
	var interruptedInfo *InterruptInfo
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			interruptedInfo = event.Action.Interrupted
		} else {
			interruptedInfo = nil
		}

		gen.Send(event)
	}

	if interruptedInfo != nil && checkPointID != nil {
		err := saveCheckPoint(ctx, r.store, *checkPointID, getInterruptRunCtx(ctx), interruptedInfo)
		if err != nil {
			gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
		}
	}
}

func runnerChain(mws ...RunnerMiddleware) RunnerMiddleware {
	return func(endpoint RunnerEndpoint) RunnerEndpoint {
		for i := len(mws) - 1; i >= 0; i-- {
			endpoint = mws[i](endpoint)
		}
		return endpoint
	}
}
