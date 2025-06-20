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

	"github.com/cloudwego/eino/schema"
)

type Runner struct {
	enableStreaming bool
	store           CheckPointStore
}

type RunnerConfig struct {
	EnableStreaming bool
}

func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return &Runner{enableStreaming: conf.EnableStreaming}
}

func (r *Runner) Run(ctx context.Context, agent Agent, messages []Message,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	fa := toFlowAgent(ctx, agent)

	input := &AgentInput{
		Messages:        messages,
		EnableStreaming: r.enableStreaming,
	}

	ctx = ctxWithNewRunCtx(ctx)

	iter := fa.Run(ctx, input, opts...)
	if r.store == nil {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go func() {
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			gen.Send(event)

			if event.Action != nil && event.Action.Interrupted != nil {
				err := saveCheckPoint(ctx, r.store, "" /*todo*/, getInterruptRunCtx(ctx), event.Action.Interrupted)
				if err != nil {
					gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
				}
				return
			}
		}
	}()
	return niter
}

func getInterruptRunCtx(ctx context.Context) *runContext {
	cs := getInterruptRunCtxs(ctx)
	if len(cs) == 0 {
		return nil
	}
	return cs[0] // assume that concurrency isn't existed, so only one run ctx is in ctx
}

func (r *Runner) Query(ctx context.Context, agent Agent,
	query string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	return r.Run(ctx, agent, []Message{schema.UserMessage(query)}, opts...)
}

func (r *Runner) Resume(ctx context.Context, agent Agent, key string) (*AsyncIterator[*AgentEvent], error) {
	if r.store == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	runCtx, info, existed, err := getCheckPoint(ctx, r.store, key)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}
	if !existed {
		return nil, fmt.Errorf("checkpoint[%s] is not existed", key)
	}

	aIter := toFlowAgent(ctx, agent).Resume(setRunCtx(ctx, runCtx), info)
	if r.store == nil {
		return aIter, nil
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go func() {
		for {
			event, ok := aIter.Next()
			if !ok {
				break
			}
			gen.Send(event)

			if event.Action != nil && event.Action.Interrupted != nil {
				err := saveCheckPoint(ctx, r.store, "" /*todo*/, runCtx, event.Action.Interrupted)
				if err != nil {
					gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
				}
				return
			}
		}
	}()
	return niter, nil
}
