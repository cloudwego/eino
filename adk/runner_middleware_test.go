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
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestRunnerMiddlewares(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	t.Run("run", func(t *testing.T) {
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", nil), nil).
			Times(1)
		rm := RunnerMiddleware{
			Name: "test_runner_middleware",
			BeforeRunner: func(ctx context.Context, rc *RunnerContext) (nextContext context.Context, err error) {
				assert.Equal(t, "a", rc.AgentName())
				assert.Equal(t, EntranceTypeRun, rc.EntranceType())
				assert.Equal(t, "1", *rc.CheckpointID())
				rc.Messages[0].Content = "modified_by_runner_middleware"
				rc.AgentRunOptions = append(rc.AgentRunOptions, WithSessionValues(map[string]any{"1": 1}))
				return context.WithValue(ctx, "mock_ctx_key", 1), nil
			},
			OnEvents: func(ctx context.Context, rc *RunnerContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				defer gen.Close()
				var lastEvent *AgentEvent
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					lastEvent = event
					gen.Send(event)
				}
				assert.Equal(t, "bye from agent OnEvents", lastEvent.Output.MessageOutput.Message.Content)
				gen.Send(&AgentEvent{
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.AssistantMessage("bye from runner OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
			},
			GlobalAgentMiddleware: &AgentMiddleware{
				OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
					assert.Equal(t, 1, ctx.Value("mock_ctx_key").(int))
					assert.Equal(t, "modified_by_runner_middleware", ac.AgentInput.Messages[0].Content)
					assert.Equal(t, 2, len(ac.AgentRunOptions))
					defer gen.Close()
					for {
						event, ok := iter.Next()
						if !ok {
							break
						}
						gen.Send(event)
					}
					gen.Send(&AgentEvent{
						Output: &AgentOutput{
							MessageOutput: &MessageVariant{
								IsStreaming: false,
								Message:     schema.AssistantMessage("bye from agent OnEvents", nil),
								Role:        schema.Assistant,
							},
						},
					})
				},
			},
		}

		rmDedup := RunnerMiddleware{
			Name: "test_runner_middleware",
			BeforeRunner: func(ctx context.Context, rc *RunnerContext) (nextContext context.Context, err error) {
				assert.Fail(t, "should not be called")
				return ctx, nil
			},
		}

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:             a,
			EnableStreaming:   false,
			CheckPointStore:   nil,
			RunnerMiddlewares: []RunnerMiddleware{rm, rmDedup},
		})

		iter := runner.Run(ctx, []Message{{Content: "Hi"}}, WithCheckPointID("1"))
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "Hello", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "bye from agent OnEvents", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "bye from runner OnEvents", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})

	t.Run("run with terminate iterator", func(t *testing.T) {
		rm1 := RunnerMiddleware{
			Name: "test_runner_middleware_1",
			BeforeRunner: func(ctx context.Context, rc *RunnerContext) (nextContext context.Context, err error) {
				assert.Equal(t, "a", rc.AgentName())
				assert.Equal(t, EntranceTypeRun, rc.EntranceType())
				assert.Equal(t, "1", *rc.CheckpointID())
				rc.Messages[0].Content = "modified_by_runner_middleware"
				rc.AgentRunOptions = append(rc.AgentRunOptions, WithSessionValues(map[string]any{"1": 1}))
				return context.WithValue(ctx, "mock_ctx_key", 1), nil
			},
			OnEvents: func(ctx context.Context, rc *RunnerContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				gen.Send(&AgentEvent{
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.AssistantMessage("bye from OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
				BypassIterator(iter, gen)
			},
			GlobalAgentMiddleware: &AgentMiddleware{
				OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
					assert.Fail(t, "should not be called")
				},
			},
		}

		rm2 := RunnerMiddleware{
			Name: "test_runner_middleware_2",
			BeforeRunner: func(ctx context.Context, rc *RunnerContext) (nextContext context.Context, err error) {
				assert.Equal(t, 1, ctx.Value("mock_ctx_key").(int))
				assert.Equal(t, "modified_by_runner_middleware", rc.Messages[0].Content)
				assert.Equal(t, 2, len(rc.AgentRunOptions))
				return ctx, fmt.Errorf("mock err")
			},
			OnEvents: func(ctx context.Context, rc *RunnerContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				assert.Fail(t, "should not be called")
			},
		}

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:             a,
			EnableStreaming:   false,
			CheckPointStore:   nil,
			RunnerMiddlewares: []RunnerMiddleware{rm1, rm2},
		})

		iter := runner.Run(ctx, []Message{{Content: "Hi"}}, WithCheckPointID("1"))
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "bye from OnEvents", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Error(t, fmt.Errorf("mock err"), event.Err)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})

	t.Run("resume", func(t *testing.T) {
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).Times(2)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", []schema.ToolCall{
				{
					ID:   "1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "test_tool",
						Arguments: "{\"input\":123}",
					},
					Extra: nil,
				},
			}), nil).
			Times(1)

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("ok", nil), nil).
			Times(1)

		type toolInput struct {
			Input int `json:"input"`
		}
		type toolOutput struct {
			Output string `json:"output"`
		}
		type interruptOptions struct {
			NewInput *string
		}
		withOptions := func(newInput string) tool.Option {
			return tool.WrapImplSpecificOptFn(func(t *interruptOptions) {
				t.NewInput = &newInput
			})
		}
		mockTool, err := utils.InferOptionableTool(
			"test_tool",
			"test",
			func(ctx context.Context, input *toolInput, opts ...tool.Option) (output *toolOutput, err error) {
				o := tool.GetImplSpecificOptions[interruptOptions](nil, opts...)
				if o.NewInput == nil {
					return nil, compose.Interrupt(ctx, input.Input)
				}
				return &toolOutput{Output: "from interrupt:" + *o.NewInput}, nil
			},
		)
		assert.NoError(t, err)

		rm1 := RunnerMiddleware{
			OnEvents: func(ctx context.Context, rc *RunnerContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				BypassIterator(iter, gen)
			},
		}
		rm2 := RunnerMiddleware{
			BeforeRunner: func(ctx context.Context, rc *RunnerContext) (nextContext context.Context, err error) {
				if rc.EntranceType() == EntranceTypeRun {
					assert.Equal(t, "Hi", rc.Messages[0].Content)
				} else if rc.EntranceType() == EntranceTypeResume {
					assert.NotNil(t, rc.CheckpointID())
					assert.Equal(t, "1", *rc.CheckpointID())
				} else {
					assert.Fail(t, "invalid entrance")
				}
				return ctx, nil
			},
		}

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{mockTool},
				},
			},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:             a,
			EnableStreaming:   false,
			CheckPointStore:   newMyStore(),
			RunnerMiddlewares: []RunnerMiddleware{rm1, rm2},
		})

		iter := runner.Run(ctx, []Message{{Content: "Hi"}}, WithCheckPointID("1"))
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "Hello", event.Output.MessageOutput.Message.Content)
		assert.NotNil(t, event.Output.MessageOutput.Message.ToolCalls[0])
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.NotNil(t, event.Action.Interrupted)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)

		iter, err = runner.Resume(ctx, "1", WithToolOptions([]tool.Option{withOptions("resume_input")}))
		assert.NoError(t, err)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "{\"output\":\"from interrupt:resume_input\"}", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "ok", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})
}
