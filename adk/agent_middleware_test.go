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
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestChatModelAgentWithMWCallbacks(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	t.Run("run", func(t *testing.T) {
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", nil), nil).
			Times(1)
		mw := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				ac.AgentInput.Messages[0].Content = "bye"
				ac.AgentRunOptions = append(ac.AgentRunOptions, WithSessionValues(map[string]any{"sv": 1}))
				return context.WithValue(ctx, "mw1", 1), nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
				assert.Equal(t, 1, len(ac.AgentRunOptions))
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
							Message:     schema.AssistantMessage("bye from OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
			},
		}

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
			Middlewares: []AgentMiddleware{mw},
		})
		assert.NoError(t, err)
		iter := a.Run(ctx, &AgentInput{Messages: []Message{{Content: "Hi"}}})
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "Hello", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "bye from OnEvents", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})

	t.Run("run with partly callbacks", func(t *testing.T) {
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", nil), nil).
			Times(1)
		mw1 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				ac.AgentInput.Messages[0].Content = "bye"
				ac.AgentRunOptions = append(ac.AgentRunOptions, WithSessionValues(map[string]any{"sv": 1}))
				return context.WithValue(ctx, "mw1", 1), nil
			},
		}
		mw2 := AgentMiddleware{
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
				assert.Equal(t, 1, len(ac.AgentRunOptions))
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
							Message:     schema.AssistantMessage("bye from OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
			},
		}
		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
			Middlewares: []AgentMiddleware{mw1, mw2},
		})
		assert.NoError(t, err)
		iter := a.Run(ctx, &AgentInput{Messages: []Message{{Content: "Hi"}}})
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "Hello", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "bye from OnEvents", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})

	t.Run("run with BeforeAgent interrupt", func(t *testing.T) {
		mw1 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				ac.AgentInput.Messages[0].Content = "bye"
				ac.AgentRunOptions = append(ac.AgentRunOptions, WithSessionValues(map[string]any{"sv": 1}))
				return context.WithValue(ctx, "mw1", 1), nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
				assert.Equal(t, 1, len(ac.AgentRunOptions))
				defer gen.Close()
				gen.Send(&AgentEvent{
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.AssistantMessage("bye from OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					gen.Send(event)
				}
			},
		}
		mw2 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				return ctx, fmt.Errorf("mock err")
			},
		}
		mw3 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				ac.AgentInput.Messages[0].Content = "invalid change"
				return ctx, nil
			},
		}

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
			Middlewares: []AgentMiddleware{mw1, mw2, mw3},
		})
		assert.NoError(t, err)
		iter := a.Run(ctx, &AgentInput{Messages: []Message{{Content: "Hi"}}})
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
}

func TestRunnerWithMWCallbacks(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	t.Run("run with ChatModelAgent", func(t *testing.T) {
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", nil), nil).
			Times(1)
		mw1 := AgentMiddleware{
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
				assert.Equal(t, 1, len(ac.AgentRunOptions))
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
							Message:     schema.AssistantMessage("bye from OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
			},
		}
		mw2 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				ac.AgentInput.Messages[0].Content = "bye"
				ac.AgentRunOptions = append(ac.AgentRunOptions, WithSessionValues(map[string]any{"sv": 1}))
				return context.WithValue(ctx, "mw1", 1), nil
			},
		}

		globalAgentMiddlewares = []AgentMiddleware{mw2}
		defer func() {
			globalAgentMiddlewares = nil
		}()

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
			Middlewares: []AgentMiddleware{mw1},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           a,
			EnableStreaming: false,
			CheckPointStore: nil,
		})

		iter := runner.Run(ctx, []Message{{Content: "Hi"}})
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "Hello", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.True(t, ok)
		assert.Equal(t, "bye from OnEvents", event.Output.MessageOutput.Message.Content)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})

	t.Run("run with ChatModelAgent BeforeAgent interrupt", func(t *testing.T) {
		mw1 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				ac.AgentInput.Messages[0].Content = "bye"
				ac.AgentRunOptions = append(ac.AgentRunOptions, WithSessionValues(map[string]any{"sv": 1}))
				return context.WithValue(ctx, "mw1", 1), nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
				assert.Equal(t, 1, len(ac.AgentRunOptions))
				defer gen.Close()
				gen.Send(&AgentEvent{
					Output: &AgentOutput{
						MessageOutput: &MessageVariant{
							IsStreaming: false,
							Message:     schema.AssistantMessage("bye from OnEvents", nil),
							Role:        schema.Assistant,
						},
					},
				})
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					gen.Send(event)
				}
			},
		}
		mw2 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				return ctx, fmt.Errorf("mock err")
			},
		}

		a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "a",
			Description: "b",
			Model:       cm,
			Middlewares: []AgentMiddleware{mw1},
		})
		assert.NoError(t, err)

		globalAgentMiddlewares = []AgentMiddleware{mw2}
		defer func() {
			globalAgentMiddlewares = nil
		}()

		runner := NewRunner(ctx, RunnerConfig{
			Agent: a,
		})

		iter := runner.Run(ctx, []Message{{Content: "Hi"}})
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.Error(t, fmt.Errorf("mock err"), event.Err)
		event, ok = iter.Next()
		assert.False(t, ok)
		assert.Nil(t, event)
	})

	t.Run("run with Workflow Agents", func(t *testing.T) {
		var (
			cmBeforeAgentCounter, cmOnEventsCounter         int32
			runnerBeforeAgentCounter, runnerOnEventsCounter int32
		)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", nil), nil).
			Times(5)

		mwChatModelAgent := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				atomic.AddInt32(&cmBeforeAgentCounter, 1)
				return ctx, nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				atomic.AddInt32(&cmOnEventsCounter, 1)
				defer gen.Close()
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					gen.Send(event)
				}
			},
		}

		mwRunner := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				atomic.AddInt32(&runnerBeforeAgentCounter, 1)
				return ctx, nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				atomic.AddInt32(&runnerOnEventsCounter, 1)
				BypassIterator(iter, gen)
			},
		}

		newChatModelAgent := func() func() Agent {
			nameCounter := 0
			return func() Agent {
				a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
					Name:        fmt.Sprintf("chat_model_agent_%d", nameCounter),
					Description: "b",
					Model:       cm,
					Middlewares: []AgentMiddleware{mwChatModelAgent},
				})
				assert.NoError(t, err)
				nameCounter++
				return a
			}
		}()

		loop, err := NewLoopAgent(ctx, &LoopAgentConfig{
			Name:          "loop",
			SubAgents:     []Agent{newChatModelAgent(), newChatModelAgent()},
			MaxIterations: 1,
		})
		assert.NoError(t, err)

		parallel, err := NewParallelAgent(ctx, &ParallelAgentConfig{
			Name:      "parallel",
			SubAgents: []Agent{newChatModelAgent(), newChatModelAgent()},
		})
		assert.NoError(t, err)

		seq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
			Name:      "seq",
			SubAgents: []Agent{newChatModelAgent(), loop, parallel},
		})
		assert.NoError(t, err)

		globalAgentMiddlewares = []AgentMiddleware{mwRunner}
		defer func() {
			globalAgentMiddlewares = nil
		}()

		runner := NewRunner(ctx, RunnerConfig{
			Agent: seq,
		})

		iter := runner.Run(ctx, []Message{{Content: "work work"}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}
		assert.Equal(t, int32(5), cmBeforeAgentCounter)
		assert.Equal(t, int32(5), cmOnEventsCounter)
		assert.Equal(t, int32(8), runnerBeforeAgentCounter)
		assert.Equal(t, int32(8), runnerOnEventsCounter)
	})

	t.Run("run with Workflow Agents BeforeAgent interrupt", func(t *testing.T) {
		var (
			cmBeforeAgentCounter, cmOnEventsCounter         int32
			runnerBeforeAgentCounter, runnerOnEventsCounter int32
		)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello", nil), nil).
			Times(1)

		mwChatModelAgent := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				atomic.AddInt32(&cmBeforeAgentCounter, 1)
				ac.AgentInput.Messages[0].Content = "bye"
				return ctx, nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				atomic.AddInt32(&cmOnEventsCounter, 1)
				assert.Equal(t, "bye", ac.AgentInput.Messages[0].Content)
				defer gen.Close()
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					gen.Send(event)
				}
			},
		}

		mwRunner := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				v := atomic.AddInt32(&runnerBeforeAgentCounter, 1)
				if v == 3 {
					return ctx, fmt.Errorf("interrupt")
				}
				return ctx, nil
			},
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				atomic.AddInt32(&runnerOnEventsCounter, 1)
				BypassIterator(iter, gen)
			},
		}

		newChatModelAgent := func() func() Agent {
			nameCounter := 0
			return func() Agent {
				a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
					Name:        fmt.Sprintf("chat_model_agent_%d", nameCounter),
					Description: "b",
					Model:       cm,
					Middlewares: []AgentMiddleware{mwChatModelAgent},
				})
				assert.NoError(t, err)
				nameCounter++
				return a
			}
		}()

		loop, err := NewLoopAgent(ctx, &LoopAgentConfig{
			Name:          "loop",
			SubAgents:     []Agent{newChatModelAgent(), newChatModelAgent()},
			MaxIterations: 1,
		})
		assert.NoError(t, err)

		parallel, err := NewParallelAgent(ctx, &ParallelAgentConfig{
			Name:      "parallel",
			SubAgents: []Agent{newChatModelAgent(), newChatModelAgent()},
		})
		assert.NoError(t, err)

		seq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
			Name:      "seq",
			SubAgents: []Agent{newChatModelAgent(), loop, parallel},
		})
		assert.NoError(t, err)

		globalAgentMiddlewares = []AgentMiddleware{mwRunner}
		defer func() {
			globalAgentMiddlewares = nil
		}()

		runner := NewRunner(ctx, RunnerConfig{
			Agent: seq,
		})

		iter := runner.Run(ctx, []Message{{Content: "work work"}})
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}
		assert.Equal(t, int32(1), cmBeforeAgentCounter)
		assert.Equal(t, int32(1), cmOnEventsCounter)
		assert.Equal(t, int32(3), runnerBeforeAgentCounter)
		assert.Equal(t, int32(2), runnerOnEventsCounter)
	})

	t.Run("resume with ChatModelAgent", func(t *testing.T) {
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).Times(1)
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

		mw1 := AgentMiddleware{
			OnEvents: func(ctx context.Context, ac *AgentContext, iter *AsyncIterator[*AgentEvent], gen *AsyncGenerator[*AgentEvent]) {
				BypassIterator(iter, gen)
			},
		}
		mw2 := AgentMiddleware{
			BeforeAgent: func(ctx context.Context, ac *AgentContext) (nextContext context.Context, err error) {
				if ac.InvocationType() == InvocationTypeRun {
					assert.Equal(t, "Hi", ac.AgentInput.Messages[0].Content)
				} else if ac.InvocationType() == InvocationTypeResume {
					assert.NotNil(t, ac.ResumeInfo)
				} else {
					assert.Fail(t, "invalid invocationType")
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
			Middlewares: []AgentMiddleware{mw1},
		})
		assert.NoError(t, err)

		globalAgentMiddlewares = []AgentMiddleware{mw2}
		defer func() {
			globalAgentMiddlewares = nil
		}()

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           a,
			EnableStreaming: false,
			CheckPointStore: newMyStore(),
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
