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
}

type RunnerConfig struct {
	Agent           Agent
	EnableStreaming bool

	CheckPointStore compose.CheckPointStore
}

func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return &Runner{
		enableStreaming: conf.EnableStreaming,
		a:               conf.Agent,
		store:           conf.CheckPointStore,
	}
}

func (r *Runner) Run(ctx context.Context, messages []Message,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	opts = unwrapOptions(r.a.Name(ctx), opts...)

	o := GetCommonOptions(&Options{
		checkPointStore: r.store,
		enableStreaming: &r.enableStreaming,
	}, opts...)

	// add CheckPointStore and EnableStreaming to options,
	// so that nested Runner can use these options
	newOpts := make([]AgentRunOption, len(opts)+2)
	copy(newOpts, opts)
	newOpts[len(opts)] = WithCheckPointStore(o.checkPointStore)
	newOpts[len(opts)+1] = WithEnableStreaming(*o.enableStreaming)

	fa := toFlowAgent(ctx, r.a)

	input := &AgentInput{
		Messages:        messages,
		EnableStreaming: *o.enableStreaming,
	}

	ctx = ctxWithNewRunCtx(ctx)

	iter := fa.Run(ctx, input, newOpts...)
	if o.checkPointStore == nil {
		return iter
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, iter, gen, o.checkPointID, o.checkPointStore)
	return niter
}

func getInterruptRunCtx(ctx context.Context) *runContext {
	cs := getInterruptRunContexts(ctx)
	if len(cs) == 0 {
		return nil
	}
	return cs[0] // assume that concurrency doesn't exist, so only one run ctx is in ctx
}

func (r *Runner) Query(ctx context.Context,
	query string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	return r.Run(ctx, []Message{schema.UserMessage(query)}, opts...)
}

func (r *Runner) Resume(ctx context.Context, checkPointID string, opts ...AgentRunOption) (*AsyncIterator[*AgentEvent], error) {
	opts = unwrapOptions(r.a.Name(ctx), opts...)

	o := GetCommonOptions(&Options{
		checkPointStore: r.store,
	}, opts...)

	if o.checkPointStore == nil {
		return nil, fmt.Errorf("failed to resume: store is nil")
	}

	newOpts := make([]AgentRunOption, len(opts)+2)
	copy(newOpts, opts)
	newOpts[len(opts)] = WithCheckPointStore(o.checkPointStore)
	newOpts[len(opts)+1] = WithCheckPointID(checkPointID)

	runCtx, info, existed, err := getCheckPoint(ctx, o.checkPointStore, checkPointID)
	if err != nil {
		return nil, fmt.Errorf("failed to get checkpoint: %w", err)
	}
	if !existed {
		return nil, fmt.Errorf("checkpoint[%s] is not existed", checkPointID)
	}

	ctx = setRunCtx(ctx, runCtx)
	aIter := toFlowAgent(ctx, r.a).Resume(ctx, info, newOpts...)
	if r.store == nil {
		return aIter, nil
	}

	niter, gen := NewAsyncIteratorPair[*AgentEvent]()

	go r.handleIter(ctx, aIter, gen, &checkPointID, o.checkPointStore)
	return niter, nil
}

func (r *Runner) handleIter(ctx context.Context, aIter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent], checkPointID *string, checkPointStore compose.CheckPointStore) {
	defer func() {
		panicErr := recover()
		if panicErr != nil {
			e := safe.NewPanicErr(panicErr, debug.Stack())
			gen.Send(&AgentEvent{Err: e})
		}

		gen.Close()
	}()
	for {
		event, ok := aIter.Next()
		if !ok {
			break
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			forUser, forStore := unwrapInterruptInfo(event.Action.Interrupted)
			event.Action.Interrupted = forUser
			if checkPointID != nil {
				err := saveCheckPoint(ctx, checkPointStore, *checkPointID, getInterruptRunCtx(ctx), forStore)
				if err != nil {
					gen.Send(&AgentEvent{Err: fmt.Errorf("failed to save checkpoint: %w", err)})
				}
			}
		}

		gen.Send(event)
	}
}

func unwrapInterruptInfo(info *InterruptInfo) (forUser *InterruptInfo, forStore *InterruptInfo) {
	if info == nil {
		return nil, nil
	}
	// from ChatModelAgent, tempInfo.data for saving and tempInfo.info for user
	if ti, ok := info.Data.(*tempInterruptInfo); ok {
		return &InterruptInfo{Data: ti.Info}, &InterruptInfo{Data: ti.Data}
	}

	// TODO: change this into an interface so developers can implement their own InterruptInfo containers
	if m, ok := info.Data.(*concurrentInterruptInfo); ok {
		forUserMap := map[string]*InterruptInfo{}
		forStoreMap := map[string]*InterruptInfo{}
		for k, v := range m.InterruptInfos {
			forUser, forStore := unwrapInterruptInfo(v)
			forUserMap[k] = forUser
			forStoreMap[k] = forStore
		}

		return &InterruptInfo{Data: forUserMap}, &InterruptInfo{Data: &concurrentInterruptInfo{
			InterruptInfos:  forStoreMap,
			TransferTargets: m.TransferTargets,
		}}
	}

	return info, info
}
