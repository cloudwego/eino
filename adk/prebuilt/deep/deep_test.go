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

package deep

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestGenModelInput(t *testing.T) {
	ctx := context.Background()

	t.Run("WithInstruction", func(t *testing.T) {
		input := &adk.AgentInput{
			Messages: []*schema.Message{
				schema.UserMessage("hello"),
			},
		}

		msgs, err := genModelInput(ctx, "You are a helpful assistant", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 2)
		assert.Equal(t, schema.System, msgs[0].Role)
		assert.Equal(t, "You are a helpful assistant", msgs[0].Content)
		assert.Equal(t, schema.User, msgs[1].Role)
		assert.Equal(t, "hello", msgs[1].Content)
	})

	t.Run("WithoutInstruction", func(t *testing.T) {
		input := &adk.AgentInput{
			Messages: []*schema.Message{
				schema.UserMessage("hello"),
			},
		}

		msgs, err := genModelInput(ctx, "", input)
		assert.NoError(t, err)
		assert.Len(t, msgs, 1)
		assert.Equal(t, schema.User, msgs[0].Role)
		assert.Equal(t, "hello", msgs[0].Content)
	})
}

func TestWriteTodos(t *testing.T) {
	m, err := buildBuiltinAgentMiddlewares(false)
	assert.NoError(t, err)

	wt := m[0].AdditionalTools[0].(tool.InvokableTool)

	todos := `[{"content":"content1","activeForm":"","status":"pending"},{"content":"content2","activeForm":"","status":"pending"}]`
	args := fmt.Sprintf(`{"todos": %s}`, todos)

	result, err := wt.InvokableRun(context.Background(), args)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Updated todo list to %s", todos), result)
}

func TestDeepSubAgentSharesSessionValues(t *testing.T) {
	ctx := context.Background()
	spy := &spySubAgent{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	calls := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			calls++
			if calls == 1 {
				c := schema.ToolCall{ID: "id-1", Type: "function"}
				c.Function.Name = taskToolName
				c.Function.Arguments = fmt.Sprintf(`{"subagent_type":"%s","description":"from_parent"}`, spy.Name(ctx))
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()

	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              cm,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{spy},
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           2,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")}, adk.WithSessionValues(map[string]any{"parent_key": "parent_val"}))
	for {
		if _, ok := it.Next(); !ok {
			break
		}
	}

	assert.Equal(t, "parent_val", spy.seenParentValue)
}

func TestDeepSubAgentFollowsStreamingMode(t *testing.T) {
	ctx := context.Background()
	spy := &spyStreamingSubAgent{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	subName := spy.Name(ctx)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:   "id-1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      taskToolName,
						Arguments: fmt.Sprintf(`{"subagent_type":"%s","description":"from_parent"}`, subName),
					},
				},
			}),
		}), nil).
		Times(1)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("done", nil),
		}), nil).
		Times(1)

	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              cm,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{spy},
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           2,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent, EnableStreaming: true})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
	for {
		if _, ok := it.Next(); !ok {
			break
		}
	}

	assert.True(t, spy.seenEnableStreaming)
}

type spySubAgent struct {
	seenParentValue any
}

func (s *spySubAgent) Name(context.Context) string        { return "spy-subagent" }
func (s *spySubAgent) Description(context.Context) string { return "spy" }
func (s *spySubAgent) Run(ctx context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	s.seenParentValue, _ = adk.GetSessionValue(ctx, "parent_key")
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("ok", nil), nil, schema.Assistant, ""))
	gen.Close()
	return it
}

type spyStreamingSubAgent struct {
	seenEnableStreaming bool
}

func (s *spyStreamingSubAgent) Name(context.Context) string        { return "spy-streaming-subagent" }
func (s *spyStreamingSubAgent) Description(context.Context) string { return "spy" }
func (s *spyStreamingSubAgent) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	if input != nil {
		s.seenEnableStreaming = input.EnableStreaming
	}
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("ok", nil), nil, schema.Assistant, ""))
	gen.Close()
	return it
}

func TestDeepAgentWithPlanExecuteSubAgent_InternalEventsEmitted(t *testing.T) {
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	deepModel := mockModel.NewMockToolCallingChatModel(ctrl)
	plannerModel := mockModel.NewMockToolCallingChatModel(ctrl)
	executorModel := mockModel.NewMockToolCallingChatModel(ctrl)
	replannerModel := mockModel.NewMockToolCallingChatModel(ctrl)

	deepModel.EXPECT().WithTools(gomock.Any()).Return(deepModel, nil).AnyTimes()
	plannerModel.EXPECT().WithTools(gomock.Any()).Return(plannerModel, nil).AnyTimes()
	executorModel.EXPECT().WithTools(gomock.Any()).Return(executorModel, nil).AnyTimes()
	replannerModel.EXPECT().WithTools(gomock.Any()).Return(replannerModel, nil).AnyTimes()

	plannerModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input []*schema.Message, opts ...interface{}) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				planJSON := `{"steps":["step1"]}`
				msg := schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:   "plan_call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "plan",
							Arguments: planJSON,
						},
					},
				})
				sw.Send(msg, nil)
			}()
			return sr, nil
		},
	).Times(1)

	executorModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			return schema.AssistantMessage("executed step1", nil), nil
		},
	).Times(1)

	replannerModel.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
		func(ctx context.Context, input []*schema.Message, opts ...interface{}) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](1)
			go func() {
				defer sw.Close()
				responseJSON := `{"response":"final response"}`
				msg := schema.AssistantMessage("", []schema.ToolCall{
					{
						ID:   "respond_call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "respond",
							Arguments: responseJSON,
						},
					},
				})
				sw.Send(msg, nil)
			}()
			return sr, nil
		},
	).Times(1)

	planner, err := planexecute.NewPlanner(ctx, &planexecute.PlannerConfig{
		ToolCallingChatModel: plannerModel,
	})
	assert.NoError(t, err)

	executor, err := planexecute.NewExecutor(ctx, &planexecute.ExecutorConfig{
		Model: executorModel,
	})
	assert.NoError(t, err)

	replanner, err := planexecute.NewReplanner(ctx, &planexecute.ReplannerConfig{
		ChatModel: replannerModel,
	})
	assert.NoError(t, err)

	planExecuteAgent, err := planexecute.New(ctx, &planexecute.Config{
		Planner:   planner,
		Executor:  executor,
		Replanner: replanner,
	})
	assert.NoError(t, err)

	namedPlanExecuteAgent := &namedPlanExecuteAgent{
		ResumableAgent: planExecuteAgent,
		name:           "plan_execute_subagent",
		description:    "a plan execute subagent",
	}

	deepModelCalls := 0
	deepModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			deepModelCalls++
			if deepModelCalls == 1 {
				c := schema.ToolCall{ID: "id-1", Type: "function"}
				c.Function.Name = taskToolName
				c.Function.Arguments = fmt.Sprintf(`{"subagent_type":"%s","description":"execute the plan"}`, namedPlanExecuteAgent.name)
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()

	deepAgent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              deepModel,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{namedPlanExecuteAgent},
		ToolsConfig:            adk.ToolsConfig{EmitInternalEvents: true},
		MaxIteration:           5,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: deepAgent})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})

	var events []*adk.AgentEvent
	for {
		event, ok := it.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	assert.Greater(t, len(events), 0, "should have at least one event")

	var deepAgentEvents []*adk.AgentEvent
	var plannerEvents []*adk.AgentEvent
	var executorEvents []*adk.AgentEvent
	var replannerEvents []*adk.AgentEvent
	var planExecuteEvents []*adk.AgentEvent

	for _, event := range events {
		switch event.AgentName {
		case "deep":
			deepAgentEvents = append(deepAgentEvents, event)
		case "planner":
			plannerEvents = append(plannerEvents, event)
		case "executor":
			executorEvents = append(executorEvents, event)
		case "replanner":
			replannerEvents = append(replannerEvents, event)
		case "plan_execute_replan", "execute_replan":
			planExecuteEvents = append(planExecuteEvents, event)
		}
	}

	assert.Greater(t, len(deepAgentEvents), 0, "should have events from deep agent")

	assert.Greater(t, len(plannerEvents), 0, "planner internal events should be emitted when EmitInternalEvents is true")
	assert.Greater(t, len(executorEvents), 0, "executor internal events should be emitted when EmitInternalEvents is true")
	assert.Greater(t, len(replannerEvents), 0, "replanner internal events should be emitted when EmitInternalEvents is true")

	t.Logf("Total events: %d", len(events))
	t.Logf("Deep agent events: %d", len(deepAgentEvents))
	t.Logf("Planner events: %d", len(plannerEvents))
	t.Logf("Executor events: %d", len(executorEvents))
	t.Logf("Replanner events: %d", len(replannerEvents))
	t.Logf("PlanExecute events: %d", len(planExecuteEvents))
}

type namedPlanExecuteAgent struct {
	adk.ResumableAgent
	name        string
	description string
}

func (n *namedPlanExecuteAgent) Name(_ context.Context) string {
	return n.name
}

func (n *namedPlanExecuteAgent) Description(_ context.Context) string {
	return n.description
}

func TestDeepAgentOutputKey(t *testing.T) {
	t.Run("OutputKeyStoresInSession", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello from DeepAgent", nil), nil).
			Times(1)

		agent, err := New(ctx, &Config{
			Name:                   "deep",
			Description:            "deep agent",
			ChatModel:              cm,
			Instruction:            "you are deep agent",
			MaxIteration:           2,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
			OutputKey:              "deep_output",
		})
		assert.NoError(t, err)

		var capturedSessionValues map[string]any
		wrappedAgent := &sessionCaptureAgent{
			Agent:          agent,
			captureSession: func(values map[string]any) { capturedSessionValues = values },
		}

		r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: wrappedAgent})
		it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
		for {
			if _, ok := it.Next(); !ok {
				break
			}
		}

		assert.Contains(t, capturedSessionValues, "deep_output")
		assert.Equal(t, "Hello from DeepAgent", capturedSessionValues["deep_output"])
	})

	t.Run("OutputKeyWithStreamingStoresInSession", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("Hello", nil),
				schema.AssistantMessage(" from", nil),
				schema.AssistantMessage(" DeepAgent", nil),
			}), nil).
			Times(1)

		agent, err := New(ctx, &Config{
			Name:                   "deep",
			Description:            "deep agent",
			ChatModel:              cm,
			Instruction:            "you are deep agent",
			MaxIteration:           2,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
			OutputKey:              "deep_output",
		})
		assert.NoError(t, err)

		var capturedSessionValues map[string]any
		wrappedAgent := &sessionCaptureAgent{
			Agent:          agent,
			captureSession: func(values map[string]any) { capturedSessionValues = values },
		}

		r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: wrappedAgent, EnableStreaming: true})
		it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
		for {
			if _, ok := it.Next(); !ok {
				break
			}
		}

		assert.Contains(t, capturedSessionValues, "deep_output")
		assert.Equal(t, "Hello from DeepAgent", capturedSessionValues["deep_output"])
	})

	t.Run("OutputKeyNotSetWhenEmpty", func(t *testing.T) {
		ctx := context.Background()

		ctrl := gomock.NewController(t)
		defer ctrl.Finish()

		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("Hello from DeepAgent", nil), nil).
			Times(1)

		agent, err := New(ctx, &Config{
			Name:                   "deep",
			Description:            "deep agent",
			ChatModel:              cm,
			Instruction:            "you are deep agent",
			MaxIteration:           2,
			WithoutWriteTodos:      true,
			WithoutGeneralSubAgent: true,
		})
		assert.NoError(t, err)

		var capturedSessionValues map[string]any
		wrappedAgent := &sessionCaptureAgent{
			Agent:          agent,
			captureSession: func(values map[string]any) { capturedSessionValues = values },
		}

		r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: wrappedAgent})
		it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")})
		for {
			if _, ok := it.Next(); !ok {
				break
			}
		}

		assert.NotContains(t, capturedSessionValues, "deep_output")
	})
}

type sessionCaptureAgent struct {
	adk.Agent
	captureSession func(map[string]any)
}

func (s *sessionCaptureAgent) Run(ctx context.Context, input *adk.AgentInput, opts ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	innerIt := s.Agent.Run(ctx, input, opts...)
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		for {
			event, ok := innerIt.Next()
			if !ok {
				break
			}
			gen.Send(event)
		}
		s.captureSession(adk.GetSessionValues(ctx))
	}()
	return it
}
