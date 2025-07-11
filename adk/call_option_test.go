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
	"testing"

	"github.com/stretchr/testify/assert"

	cb "github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/callbacks"
)

func TestCallOption(t *testing.T) {
	ctx := context.Background()

	t.Run("adk options without impl specific options", func(t *testing.T) {
		agent := &mockAgentForOption{}

		rn := NewRunner(ctx, RunnerConfig{
			EnableStreaming: false,
		})

		cb1 := callbacks.NewHandlerHelper().Handler()
		cb2 := callbacks.NewHandlerHelper().Handler()
		cb3 := callbacks.NewHandlerHelper().Handler()

		_ = rn.Run(ctx, agent, []Message{schema.UserMessage("test")}, WithCallbacks(cb1),
			WithCallbacks(cb2).DesignateAgent("agent_1"),
			WithCallbacks(cb3).DesignateAgent("agent_2"))

		assert.Len(t, agent.opts, 2)
		assert.Equal(t, agent.options, &options{
			Callbacks: []cb.Handler{cb1, cb2},
		})
	})
}

type mockAgentForOption struct {
	opts []AgentRunOption

	options *options
}

func (m *mockAgentForOption) Name(ctx context.Context) string {
	return "agent_1"
}

func (m *mockAgentForOption) Description(ctx context.Context) string {
	return ""
}

func (m *mockAgentForOption) Run(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	m.opts = opts
	m.options = getCommonOptions(&options{}, opts...)

	return nil
}
