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

	"github.com/cloudwego/eino/schema"
)

type Runner struct {
	enableStreaming bool
}

type RunnerConfig struct {
	EnableStreaming bool
}

func NewRunner(_ context.Context, conf RunnerConfig) *Runner {
	return &Runner{enableStreaming: conf.EnableStreaming}
}

func (r *Runner) Run(ctx context.Context, agent Agent, msgs []Message,
	opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	fa := toFlowAgent(agent)

	input := &AgentInput{
		Msgs:            msgs,
		EnableStreaming: r.enableStreaming,
	}

	ctx = ctxWithNewRunCtx(ctx)

	return fa.Run(ctx, input, opts...)
}

func (r *Runner) Query(ctx context.Context, agent Agent,
	query string, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {

	return r.Run(ctx, agent, []Message{schema.UserMessage(query)}, opts...)
}
