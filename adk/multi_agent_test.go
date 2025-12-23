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

	"github.com/cloudwego/eino/schema"
)

func TestMultiAgent(t *testing.T) {
	ctx := context.Background()
	cnt := 0
	ma := &mockAgent{
		name:        "mock_name",
		description: "mock desc",
		responses: []*AgentEvent{
			{
				AgentName: "mock_name",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: false,
						Message:     schema.AssistantMessage("ok", nil),
						Role:        schema.Assistant,
					},
				},
			},
			{
				AgentName: "mock_name",
				Action: &AgentAction{
					Exit: true,
				},
			},
		},
	}
	mw := AgentMiddleware{
		Name: "test",
		BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
			assert.Equal(t, "mock_name", ac.AgentName())
			assert.Equal(t, EntranceTypeRun, ac.EntranceType())
			assert.Equal(t, "hello", ac.AgentInput.Messages[0].Content)

			ac.AgentInput.Messages[0].Content = "bye"
			ctx = context.WithValue(ctx, "mock_key", 1)
			return ctx, nil
		},
		OnEvents: NewSyncOnSingleEventHandler(func(ctx context.Context, ac *AgentContext, fromEvent *AgentEvent) (toEvent *AgentEvent) {
			assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
			assert.Equal(t, 1, ctx.Value("mock_key").(int))
			assert.Nil(t, fromEvent.Err)
			if cnt == 0 {
				assert.Equal(t, "ok", fromEvent.Output.MessageOutput.Message.Content)
				fromEvent.Output.MessageOutput.Message.Content = "okok"
			} else {
				assert.True(t, fromEvent.Action.Exit)
			}
			cnt++
			return fromEvent
		}),
	}

	a, err := NewMultiAgent(ctx, MultiAgentConfig{
		Agent:       ma,
		Middlewares: []AgentMiddleware{mw},
	})
	assert.NoError(t, err)
	assert.Equal(t, "mock_name", a.Name(ctx))
	assert.Equal(t, "mock desc", a.Description(ctx))

	r := NewRunner(ctx, RunnerConfig{Agent: a})
	iter := r.Run(ctx, []Message{schema.UserMessage("hello")})
	readCnt := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if readCnt == 0 {
			assert.Equal(t, "okok", event.Output.MessageOutput.Message.Content)
		} else {
			assert.True(t, event.Action.Exit)
		}
		readCnt++
	}

	assert.Equal(t, 2, cnt)
}
