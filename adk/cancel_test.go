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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type cancelTestChatModel struct {
	delay       time.Duration
	response    *schema.Message
	startedChan chan struct{}
	doneChan    chan struct{}
}

func (m *cancelTestChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	select {
	case m.startedChan <- struct{}{}:
	default:
	}
	time.Sleep(m.delay)
	select {
	case m.doneChan <- struct{}{}:
	default:
	}
	return m.response, nil
}

func (m *cancelTestChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.startedChan <- struct{}{}
	time.Sleep(m.delay)
	m.doneChan <- struct{}{}
	return schema.StreamReaderFromArray([]*schema.Message{m.response}), nil
}

func (m *cancelTestChatModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
}

type slowTool struct {
	name        string
	delay       time.Duration
	result      string
	callCount   int32
	startedChan chan struct{}
}

func newSlowTool(name string, delay time.Duration, result string) *slowTool {
	return &slowTool{
		name:        name,
		delay:       delay,
		result:      result,
		startedChan: make(chan struct{}, 10),
	}
}

func (t *slowTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "A slow tool for testing",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string", Desc: "Input parameter"},
		}),
	}, nil
}

func (t *slowTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	atomic.AddInt32(&t.callCount, 1)
	select {
	case t.startedChan <- struct{}{}:
	default:
	}
	time.Sleep(t.delay)
	return t.result, nil
}

type cancelTestStore struct {
	m  map[string][]byte
	mu sync.Mutex
}

func newCancelTestStore() *cancelTestStore {
	return &cancelTestStore{m: make(map[string][]byte)}
}

func (s *cancelTestStore) Set(_ context.Context, key string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[key] = value
	return nil
}

func (s *cancelTestStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[key]
	return v, ok, nil
}

func TestCancelSig(t *testing.T) {
	t.Run("BasicCancelSignal", func(t *testing.T) {
		cs := newCancelSig()

		cfg := checkCancelSig(cs)
		assert.Nil(t, cfg, "Should not be cancelled initially")

		cs.cancel(&cancelConfig{Mode: CancelImmediate})

		cfg = checkCancelSig(cs)
		assert.NotNil(t, cfg, "Should be cancelled after cancel()")
		assert.Equal(t, CancelImmediate, cfg.Mode)
	})
}

func TestRunWithCancel_WithTools(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_DuringModelCall", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delay: 2 * time.Second,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")})
		assert.NoError(t, err)
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		eventsCh := make(chan []*AgentEvent, 1)
		go func() {
			var events []*AgentEvent
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				events = append(events, event)
			}
			eventsCh <- events
		}()

		select {
		case <-modelStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("Model did not start within 5 seconds")
		}

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(ctx)
		assert.NoError(t, err)

		start := time.Now()
		events := <-eventsCh
		elapsed := time.Since(start)

		assert.True(t, elapsed < 1*time.Second, "Should return quickly after cancel, elapsed: %v", elapsed)
		assert.True(t, len(events) > 0)

		hasInterrupted := false
		for _, e := range events {
			assert.Nil(t, e.Err, "Should not have error event after cancel")
			if e.Action != nil && e.Action.Interrupted != nil {
				hasInterrupted = true
			}
		}
		assert.True(t, hasInterrupted, "Should have interrupted event after cancel")
	})

	t.Run("CancelAfterChatModel_DuringToolCall", func(t *testing.T) {
		toolStarted := make(chan struct{}, 1)
		st := &slowToolWithSignal{
			name:        "slow_tool",
			delay:       2 * time.Second,
			result:      "tool result",
			startedChan: toolStarted,
		}

		modelWithToolCall := &simpleChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       modelWithToolCall,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		iter, cancelFn := agent.RunWithCancel(ctx, &AgentInput{
			Messages: []Message{schema.UserMessage("Use the tool")},
		})
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		<-toolStarted

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(ctx, WithCancelMode(CancelAfterChatModel))
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			assert.Nil(t, event.Err, "Should not have error event after cancel")
			events = append(events, event)
		}

		assert.True(t, len(events) > 0)
		assert.True(t, atomic.LoadInt32(&st.callCount) >= 1, "Tool should have been called")
	})

	t.Run("CancelAfterToolCall_CompletesToolExecution", func(t *testing.T) {
		toolStarted := make(chan struct{}, 1)
		st := &slowToolWithSignal{
			name:        "slow_tool",
			delay:       500 * time.Millisecond,
			result:      "tool result",
			startedChan: toolStarted,
		}

		modelWithToolCall := &simpleChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       modelWithToolCall,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		iter, cancelFn := agent.RunWithCancel(ctx, &AgentInput{
			Messages: []Message{schema.UserMessage("Use the tool")},
		})
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		<-toolStarted

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(ctx, WithCancelMode(CancelAfterToolCall))
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			assert.Nil(t, event.Err, "Should not have error event after cancel")
			events = append(events, event)
		}

		assert.True(t, len(events) > 0)
		assert.True(t, atomic.LoadInt32(&st.callCount) >= 1, "Tool should have been called")
	})
}

type slowToolWithSignal struct {
	name        string
	delay       time.Duration
	result      string
	callCount   int32
	startedChan chan struct{}
}

func (t *slowToolWithSignal) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: t.name,
		Desc: "A slow tool for testing",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"input": {Type: "string", Desc: "Input parameter"},
		}),
	}, nil
}

func (t *slowToolWithSignal) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	atomic.AddInt32(&t.callCount, 1)
	t.startedChan <- struct{}{}
	time.Sleep(t.delay)
	return t.result, nil
}

type simpleChatModel struct {
	response *schema.Message
}

func (m *simpleChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return m.response, nil
}

func (m *simpleChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{m.response}), nil
}

func (m *simpleChatModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
}

func TestRunWithCancel_WithCheckpoint(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelWithCheckpoint", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delay: 500 * time.Millisecond,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		store := newCancelTestStore()
		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
			CheckPointStore: store,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")}, WithCheckPointID("cancel-1"))
		assert.NoError(t, err)

		<-modelStarted

		err = cancelFn(ctx)
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			assert.Nil(t, event.Err, "Should not have error event after cancel")
			events = append(events, event)
		}

		assert.True(t, len(events) > 0)
	})
}

func TestCancelFuncMultipleCalls(t *testing.T) {
	ctx := context.Background()

	t.Run("SecondCancelReturnsErrAgentFinished", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delay: 1 * time.Second,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")})
		assert.NoError(t, err)

		<-modelStarted

		err = cancelFn(ctx)
		assert.NoError(t, err)

		err = cancelFn(ctx)
		assert.ErrorIs(t, err, ErrAgentFinished)

		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}
	})
}

func TestAgentNotCancellable(t *testing.T) {
	ctx := context.Background()

	nonCancellableAgent := &nonCancellableTestAgent{
		name: "NonCancellable",
	}

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           nonCancellableAgent,
		EnableStreaming: false,
	})

	t.Run("RunWithCancelReturnsError", func(t *testing.T) {
		_, _, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Hello")})
		assert.ErrorIs(t, err, ErrAgentNotCancellable)
	})
}

type nonCancellableTestAgent struct {
	name string
}

func (a *nonCancellableTestAgent) Name(_ context.Context) string {
	return a.name
}

func (a *nonCancellableTestAgent) Description(_ context.Context) string {
	return "A non-cancellable agent"
}

func (a *nonCancellableTestAgent) Run(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	gen.Send(&AgentEvent{
		Output: &AgentOutput{
			MessageOutput: &MessageVariant{
				Message: schema.AssistantMessage("Response", nil),
			},
		},
	})
	gen.Close()
	return iter
}

func TestRunWithCancel_Streaming(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_DuringModelStream", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delay: 2 * time.Second,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: true,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")})
		assert.NoError(t, err)
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		eventsCh := make(chan []*AgentEvent, 1)
		go func() {
			var events []*AgentEvent
			for {
				event, ok := iter.Next()
				if !ok {
					break
				}
				events = append(events, event)
			}
			eventsCh <- events
		}()

		select {
		case <-modelStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("Model did not start within 5 seconds")
		}

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(ctx)
		assert.NoError(t, err)

		start := time.Now()
		events := <-eventsCh
		elapsed := time.Since(start)

		assert.True(t, elapsed < 1*time.Second, "Should return quickly after cancel, elapsed: %v", elapsed)
		assert.True(t, len(events) > 0)

		hasInterrupted := false
		for _, e := range events {
			assert.Nil(t, e.Err, "Should not have error event after cancel")
			if e.Action != nil && e.Action.Interrupted != nil {
				hasInterrupted = true
			}
		}
		assert.True(t, hasInterrupted, "Should have interrupted event after cancel")
	})

	t.Run("CancelAfterToolCall_Streaming", func(t *testing.T) {
		toolStarted := make(chan struct{}, 1)
		st := &slowToolWithSignal{
			name:        "slow_tool",
			delay:       500 * time.Millisecond,
			result:      "tool result",
			startedChan: toolStarted,
		}

		modelWithToolCall := &simpleChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       modelWithToolCall,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: true,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")})
		assert.NoError(t, err)
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		<-toolStarted

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(ctx, WithCancelMode(CancelAfterToolCall))
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			assert.Nil(t, event.Err, "Should not have error event after cancel")
			events = append(events, event)
		}

		assert.True(t, len(events) > 0)
		assert.True(t, atomic.LoadInt32(&st.callCount) >= 1, "Tool should have been called")
	})
}

func TestResumeWithCancel(t *testing.T) {
	ctx := context.Background()

	t.Run("RunWithCancel_ThenResumeWithCancel", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		modelCallCount := int32(0)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delay: 500 * time.Millisecond,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		store := newCancelTestStore()
		checkpointID := "resume-cancel-test-1"
		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
			CheckPointStore: store,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")}, WithCheckPointID(checkpointID))
		assert.NoError(t, err)

		<-modelStarted
		atomic.AddInt32(&modelCallCount, 1)

		err = cancelFn(ctx)
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			assert.Nil(t, event.Err, "Should not have error event after cancel")
			events = append(events, event)
		}
		assert.True(t, len(events) > 0)

		hasInterrupted := false
		for _, e := range events {
			if e.Action != nil && e.Action.Interrupted != nil {
				hasInterrupted = true
				break
			}
		}
		assert.True(t, hasInterrupted, "First run should have interrupted event")

		slowModel.delay = 100 * time.Millisecond
		slowModel.response = &schema.Message{
			Role:    schema.Assistant,
			Content: "Final response after resume",
		}

		resumeIter, resumeCancelFn, err := runner.ResumeWithCancel(ctx, checkpointID)
		assert.NoError(t, err)
		assert.NotNil(t, resumeIter)
		assert.NotNil(t, resumeCancelFn)

		var resumeEvents []*AgentEvent
		for {
			event, ok := resumeIter.Next()
			if !ok {
				break
			}
			assert.Nil(t, event.Err, "Should not have error event during resume")
			resumeEvents = append(resumeEvents, event)
		}

		assert.True(t, len(resumeEvents) > 0, "Resume should produce events")
	})

	t.Run("ResumeWithCancel_ThenCancel", func(t *testing.T) {
		firstModelStarted := make(chan struct{}, 1)
		resumeModelStarted := make(chan struct{}, 1)
		modelCallCount := int32(0)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delay: 500 * time.Millisecond,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "slow_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
			startedChan: firstModelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		store := newCancelTestStore()
		checkpointID := "resume-then-cancel-test-1"
		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agent,
			EnableStreaming: false,
			CheckPointStore: store,
		})

		iter, cancelFn, err := runner.RunWithCancel(ctx, []Message{schema.UserMessage("Use the tool")}, WithCheckPointID(checkpointID))
		assert.NoError(t, err)

		<-firstModelStarted
		atomic.AddInt32(&modelCallCount, 1)

		err = cancelFn(ctx)
		assert.NoError(t, err)

		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		slowModel.delay = 2 * time.Second
		slowModel.startedChan = resumeModelStarted

		resumeIter, resumeCancelFn, err := runner.ResumeWithCancel(ctx, checkpointID)
		assert.NoError(t, err)

		resumeEventsCh := make(chan []*AgentEvent, 1)
		go func() {
			var events []*AgentEvent
			for {
				event, ok := resumeIter.Next()
				if !ok {
					break
				}
				events = append(events, event)
			}
			resumeEventsCh <- events
		}()

		<-resumeModelStarted
		atomic.AddInt32(&modelCallCount, 1)

		time.Sleep(100 * time.Millisecond)

		err = resumeCancelFn(ctx)
		assert.NoError(t, err)

		start := time.Now()
		resumeEvents := <-resumeEventsCh
		elapsed := time.Since(start)

		assert.True(t, elapsed < 1*time.Second, "Resume should return quickly after cancel, elapsed: %v", elapsed)
		assert.True(t, len(resumeEvents) > 0)

		hasInterrupted := false
		for _, e := range resumeEvents {
			assert.Nil(t, e.Err, "Should not have error event after resume cancel")
			if e.Action != nil && e.Action.Interrupted != nil {
				hasInterrupted = true
			}
		}
		assert.True(t, hasInterrupted, "Resume should have interrupted event after cancel")
	})
}
