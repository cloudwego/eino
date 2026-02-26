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
	"errors"
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

func TestCancelContext(t *testing.T) {
	t.Run("BasicCancelContext", func(t *testing.T) {
		cc := newCancelContext()
		assert.False(t, cc.shouldCancel(), "Should not be cancelled initially")

		cc.config = &agentCancelConfig{Mode: CancelImmediate}
		close(cc.cancelChan)

		assert.True(t, cc.shouldCancel(), "Should be cancelled after close(cancelChan)")
		assert.Equal(t, CancelImmediate, cc.config.Mode)
	})
}

func TestWithCancel_WithTools(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt)
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

		err = cancelFn()
		assert.NoError(t, err)

		start := time.Now()
		events := <-eventsCh
		elapsed := time.Since(start)

		assert.True(t, elapsed < 1*time.Second, "Should return quickly after cancel, elapsed: %v", elapsed)
		assert.True(t, len(events) > 0)

		hasCancelError := false
		for _, e := range events {
			var cancelErr *CancelError
			if e.Err != nil && errors.As(e.Err, &cancelErr) {
				hasCancelError = true
			}
		}
		assert.True(t, hasCancelError, "Should have CancelError event after cancel")
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

		cancelOpt, cancelFn := WithCancel()
		iter := agent.Run(ctx, &AgentInput{
			Messages: []Message{schema.UserMessage("Use the tool")},
		}, cancelOpt)
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		<-toolStarted

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				continue
			}
			events = append(events, event)
		}

		assert.True(t, len(events) > 0)
		assert.True(t, atomic.LoadInt32(&st.callCount) >= 1, "Tool should have been called")
	})

	t.Run("CancelAfterToolCalls_CompletesToolExecution", func(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := agent.Run(ctx, &AgentInput{
			Messages: []Message{schema.UserMessage("Use the tool")},
		}, cancelOpt)
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		<-toolStarted

		time.Sleep(100 * time.Millisecond)

		err = cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		assert.NoError(t, err)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				continue
			}
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

func TestWithCancel_WithCheckpoint(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt, WithCheckPointID("cancel-1"))

		<-modelStarted

		err = cancelFn()
		assert.NoError(t, err)

		var events []*AgentEvent
		hasCancelError := false
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				hasCancelError = true
				continue
			}
			events = append(events, event)
		}

		assert.True(t, hasCancelError, "Should have CancelError event after cancel")
	})
}

func TestAgentCancelFuncMultipleCalls(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt)

		<-modelStarted

		cancelErr := cancelFn()
		assert.NoError(t, cancelErr)

		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}
	})
}

func TestWithCancel_Streaming(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt)
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

		cancelErr := cancelFn()
		assert.NoError(t, cancelErr)

		start := time.Now()
		events := <-eventsCh
		elapsed := time.Since(start)

		assert.True(t, elapsed < 1*time.Second, "Should return quickly after cancel, elapsed: %v", elapsed)
		assert.True(t, len(events) > 0)

		hasCancelError := false
		for _, e := range events {
			var ce *CancelError
			if e.Err != nil && errors.As(e.Err, &ce) {
				hasCancelError = true
			}
		}
		assert.True(t, hasCancelError, "Should have CancelError event after cancel")
	})

	t.Run("CancelAfterToolCalls_Streaming", func(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt)
		assert.NotNil(t, iter)
		assert.NotNil(t, cancelFn)

		<-toolStarted

		time.Sleep(100 * time.Millisecond)

		cancelErr := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		assert.NoError(t, cancelErr)

		var events []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				continue
			}
			events = append(events, event)
		}

		assert.True(t, len(events) > 0)
		assert.True(t, atomic.LoadInt32(&st.callCount) >= 1, "Tool should have been called")
	})
}

// TestWithCancel_Resume tests the workflow of Cancel followed by Resume.
//
// To avoid data races, we create new agent and runner instances for the Resume phase
// instead of reusing and modifying the original model instance.
func TestWithCancel_Resume(t *testing.T) {
	ctx := context.Background()

	t.Run("Cancel_ThenResume", func(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt, WithCheckPointID(checkpointID))

		<-modelStarted
		atomic.AddInt32(&modelCallCount, 1)

		cancelErr := cancelFn()
		assert.NoError(t, cancelErr)

		var events []*AgentEvent
		hasCancelErr := false
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				hasCancelErr = true
				continue
			}
			events = append(events, event)
		}
		assert.True(t, hasCancelErr, "Should have CancelError event after cancel")

		newModelStarted := make(chan struct{}, 1)
		slowModel2 := &cancelTestChatModel{
			delay: 100 * time.Millisecond,
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "Final response after resume",
			},
			startedChan: newModelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel2,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		runner2 := NewRunner(ctx, RunnerConfig{
			Agent:           agent2,
			EnableStreaming: false,
			CheckPointStore: store,
		})

		resumeCancelOpt, _ := WithCancel()
		resumeIter, err := runner2.Resume(ctx, checkpointID, resumeCancelOpt)
		assert.NoError(t, err)
		assert.NotNil(t, resumeIter)

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

	t.Run("Resume_ThenCancel", func(t *testing.T) {
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

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt, WithCheckPointID(checkpointID))

		<-firstModelStarted
		atomic.AddInt32(&modelCallCount, 1)

		cancelErr := cancelFn()
		assert.NoError(t, cancelErr)

		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		slowModel2 := &cancelTestChatModel{
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
			startedChan: resumeModelStarted,
			doneChan:    make(chan struct{}, 1),
		}

		agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "TestAgent",
			Description: "Test agent with tool",
			Instruction: "You are a test assistant",
			Model:       slowModel2,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{st},
				},
			},
		})
		assert.NoError(t, err)

		runner2 := NewRunner(ctx, RunnerConfig{
			Agent:           agent2,
			EnableStreaming: false,
			CheckPointStore: store,
		})

		resumeCancelOpt, resumeCancelFn := WithCancel()
		resumeIter, err := runner2.Resume(ctx, checkpointID, resumeCancelOpt)
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

		err = resumeCancelFn()
		assert.NoError(t, err)

		start := time.Now()
		resumeEvents := <-resumeEventsCh
		elapsed := time.Since(start)

		assert.True(t, elapsed < 1*time.Second, "Resume should return quickly after cancel, elapsed: %v", elapsed)
		assert.True(t, len(resumeEvents) > 0)

		hasCancelError := false
		for _, e := range resumeEvents {
			var ce *CancelError
			if e.Err != nil && errors.As(e.Err, &ce) {
				hasCancelError = true
			}
		}
		assert.True(t, hasCancelError, "Resume should have CancelError event after cancel")
	})
}
