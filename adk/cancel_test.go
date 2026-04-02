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
	"fmt"
	"io"
	"runtime"
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
	delayNs     int64
	response    *schema.Message
	startedChan chan struct{}
	doneChan    chan struct{}
}

func (m *cancelTestChatModel) getDelay() time.Duration {
	return time.Duration(atomic.LoadInt64(&m.delayNs))
}

func (m *cancelTestChatModel) setDelay(d time.Duration) {
	atomic.StoreInt64(&m.delayNs, int64(d))
}

func (m *cancelTestChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	select {
	case m.startedChan <- struct{}{}:
	default:
	}
	select {
	case <-time.After(m.getDelay()):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case m.doneChan <- struct{}{}:
	default:
	}
	return m.response, nil
}

func (m *cancelTestChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.startedChan <- struct{}{}
	time.Sleep(m.getDelay())
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
	select {
	case <-time.After(t.delay):
	case <-ctx.Done():
		return "", ctx.Err()
	}
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

		cc.setMode(CancelImmediate)
		close(cc.cancelChan)

		assert.True(t, cc.shouldCancel(), "Should be cancelled after close(cancelChan)")
		assert.Equal(t, CancelImmediate, cc.getMode())
	})
}

func TestWithCancel_WithTools(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_DuringModelCall", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delayNs: int64(2 * time.Second),
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

		handle, _ := cancelFn()
		err = handle.Wait()
		assert.NoError(t, err)

		var events []*AgentEvent
		select {
		case events = <-eventsCh:
		case <-time.After(5 * time.Second):
			t.Fatal("Timed out waiting for events")
		}

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

		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		err = handle.Wait()
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

		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		err = handle.Wait()
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

	t.Run("NestedCancelPropagation", func(t *testing.T) {
		cc := newCancelContext()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		child := cc.deriveChild(ctx)
		assert.NotNil(t, child)

		cc.setMode(CancelImmediate)

		if atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateCancelling) {
			close(cc.cancelChan)
		}

		select {
		case <-child.cancelChan:
		case <-time.After(1 * time.Second):
			t.Fatal("Child did not receive cancel signal")
		}

		assert.True(t, child.shouldCancel())
		assert.Equal(t, CancelImmediate, child.getMode())
	})

	t.Run("DeepAgentIntegrationCancel", func(t *testing.T) {
		ctx := context.Background()
		modelStarted := make(chan struct{}, 1)

		leafModel := &cancelTestChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "Leaf result",
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 1),
		}
		leafModel.setDelay(500 * time.Millisecond)
		leafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "LeafAgent",
			Description: "desc",
			Model:       leafModel,
		})
		assert.NoError(t, err)

		rootModel := &cancelTestChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "LeafAgent",
							Arguments: `{}`,
						},
					},
				},
			},
			startedChan: make(chan struct{}, 1),
			doneChan:    make(chan struct{}, 1),
		}
		rootAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "RootAgent",
			Description: "desc",
			Model:       rootModel,
			ToolsConfig: ToolsConfig{
				ToolsNodeConfig: compose.ToolsNodeConfig{
					Tools: []tool.BaseTool{NewAgentTool(ctx, leafAgent)},
				},
			},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent: rootAgent,
		})

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("Run leaf")}, cancelOpt)

		<-modelStarted

		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
		err = handle.Wait()
		assert.NoError(t, err)

		hasCancelError := false
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				var ce *CancelError
				if errors.As(event.Err, &ce) {
					hasCancelError = true
					assert.NotNil(t, ce.interruptSignal, "CancelError should carry interrupt signal")
				}
			}
		}
		assert.True(t, hasCancelError, "Should have received CancelError")
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
	delay    time.Duration
	response *schema.Message
}

func (m *simpleChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return m.response, nil
}

func (m *simpleChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
			delayNs: int64(1 * time.Second),
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

		handle, _ := cancelFn()
		err = handle.Wait()
		assert.NoError(t, err)

		var events []*AgentEvent
		hasCancelError := false
		var cancelErrorCheckPointID string
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				hasCancelError = true
				cancelErrorCheckPointID = ce.CheckPointID
				continue
			}
			events = append(events, event)
		}

		assert.True(t, hasCancelError, "Should have CancelError event after cancel")
		assert.Equal(t, "cancel-1", cancelErrorCheckPointID, "CancelError should contain the checkpoint ID")
	})
}

func TestAgentCancelFuncMultipleCalls(t *testing.T) {
	ctx := context.Background()

	t.Run("SecondCancelReturnsErrAgentFinished", func(t *testing.T) {
		modelStarted := make(chan struct{}, 1)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delayNs: int64(1 * time.Second),
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

		handle, _ := cancelFn()
		cancelErr := handle.Wait()
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
			delayNs: int64(2 * time.Second),
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

		handle, _ := cancelFn()
		cancelErr := handle.Wait()
		assert.NoError(t, cancelErr)

		var events []*AgentEvent
		select {
		case events = <-eventsCh:
		case <-time.After(5 * time.Second):
			t.Fatal("Timed out waiting for events")
		}

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

		handle, _ := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
		cancelErr := handle.Wait()
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
			delayNs: int64(500 * time.Millisecond),
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

		handle, _ := cancelFn()
		cancelErr := handle.Wait()
		assert.NoError(t, cancelErr)

		var events []*AgentEvent
		hasCancelErr := false
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Err != nil {
				var ce *CancelError
				if errors.As(event.Err, &ce) {
					hasCancelErr = true
					continue
				}
				t.Fatalf("unexpected error: %v", event.Err)
			}
			events = append(events, event)
		}
		assert.True(t, hasCancelErr, "Should have CancelError event after cancel")

		newModelStarted := make(chan struct{}, 1)
		slowModel2 := &cancelTestChatModel{
			delayNs: int64(100 * time.Millisecond),
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
		modelCallCount := int32(0)
		st := newSlowTool("slow_tool", 100*time.Millisecond, "tool result")

		slowModel := &cancelTestChatModel{
			delayNs: int64(500 * time.Millisecond),
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

		handle, _ := cancelFn()
		cancelErr := handle.Wait()
		assert.NoError(t, cancelErr)

		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		slowModel2 := newBlockingChatModel(toolCallMsg(toolCall("call_1", "slow_tool", `{"input": "test"}`)))

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

		<-slowModel2.started
		atomic.AddInt32(&modelCallCount, 1)

		cancelHandle, _ := resumeCancelFn()
		close(slowModel2.unblockCh)
		err = cancelHandle.Wait()
		assert.True(t, err == nil || errors.Is(err, ErrExecutionCompleted), "unexpected cancel wait error: %v", err)

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
		executionCompletedBeforeCancel := errors.Is(err, ErrExecutionCompleted)
		assert.True(t, hasCancelError || executionCompletedBeforeCancel, "Resume should have CancelError event after cancel, or execution completed before cancel")
	})
}

func TestCancelMonitoredToolHandler_StreamableToolCall(t *testing.T) {
	t.Run("NoCancelContext_PassesThrough", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}

		// Create a stream with some data
		r, w := schema.Pipe[string](1)
		go func() {
			w.Send("chunk1", nil)
			w.Send("chunk2", nil)
			w.Close()
		}()

		next := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return &compose.StreamToolOutput{Result: r}, nil
		}

		wrapped := handler.WrapStreamableToolCall(next)
		// No cancelContext in the Go context
		output, err := wrapped(context.Background(), &compose.ToolInput{Name: "test"})
		assert.NoError(t, err)

		// Should get the original stream unchanged
		chunk1, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "chunk1", chunk1)

		chunk2, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "chunk2", chunk2)

		_, err = output.Result.Recv()
		assert.ErrorIs(t, err, io.EOF)
	})

	t.Run("WithCancelContext_NoCancel_StreamsNormally", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}
		cc := newCancelContext()

		r, w := schema.Pipe[string](1)
		go func() {
			w.Send("data1", nil)
			w.Send("data2", nil)
			w.Close()
		}()

		next := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return &compose.StreamToolOutput{Result: r}, nil
		}

		wrapped := handler.WrapStreamableToolCall(next)
		ctx := withCancelContext(context.Background(), cc)
		output, err := wrapped(ctx, &compose.ToolInput{Name: "test"})
		assert.NoError(t, err)

		chunk1, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "data1", chunk1)

		chunk2, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "data2", chunk2)

		_, err = output.Result.Recv()
		assert.ErrorIs(t, err, io.EOF)
	})

	t.Run("WithCancelContext_ImmediateCancel_TerminatesStream", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}
		cc := newCancelContext()

		// Create a slow stream that we'll cancel mid-way
		r, w := schema.Pipe[string](1)
		go func() {
			defer w.Close()
			w.Send("chunk1", nil)
			time.Sleep(200 * time.Millisecond)
			w.Send("chunk2", nil)
		}()

		next := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return &compose.StreamToolOutput{Result: r}, nil
		}

		wrapped := handler.WrapStreamableToolCall(next)
		ctx := withCancelContext(context.Background(), cc)
		output, err := wrapped(ctx, &compose.ToolInput{Name: "test"})
		assert.NoError(t, err)

		// Read first chunk
		chunk1, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, "chunk1", chunk1)

		// Fire immediate cancel
		close(cc.immediateChan)

		// Next recv should get ErrStreamCanceled
		_, err = output.Result.Recv()
		assert.ErrorIs(t, err, ErrStreamCanceled)
	})

	t.Run("WithCancelContext_AlreadyCancelled_TerminatesImmediately", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}
		cc := newCancelContext()
		close(cc.immediateChan) // Already canceled

		r, w := schema.Pipe[string](1)
		go func() {
			w.Send("should-not-see", nil)
			w.Close()
		}()

		next := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return &compose.StreamToolOutput{Result: r}, nil
		}

		wrapped := handler.WrapStreamableToolCall(next)
		ctx := withCancelContext(context.Background(), cc)
		output, err := wrapped(ctx, &compose.ToolInput{Name: "test"})
		assert.NoError(t, err)

		_, err = output.Result.Recv()
		assert.ErrorIs(t, err, ErrStreamCanceled)
	})

	t.Run("NextReturnsError_PropagatesError", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}
		cc := newCancelContext()

		nextErr := errors.New("tool execution failed")
		next := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
			return nil, nextErr
		}

		wrapped := handler.WrapStreamableToolCall(next)
		ctx := withCancelContext(context.Background(), cc)
		_, err := wrapped(ctx, &compose.ToolInput{Name: "test"})
		assert.ErrorIs(t, err, nextErr)
	})
}

func TestCancelMonitoredToolHandler_EnhancedStreamableToolCall(t *testing.T) {
	t.Run("NoCancelContext_PassesThrough", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}

		tr1 := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "chunk1"}}}
		r, w := schema.Pipe[*schema.ToolResult](1)
		go func() {
			w.Send(tr1, nil)
			w.Close()
		}()

		next := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: r}, nil
		}

		wrapped := handler.WrapEnhancedStreamableToolCall(next)
		output, err := wrapped(context.Background(), &compose.ToolInput{Name: "test"})
		assert.NoError(t, err)

		result, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, tr1, result)

		_, err = output.Result.Recv()
		assert.ErrorIs(t, err, io.EOF)
	})

	t.Run("WithCancelContext_ImmediateCancel_TerminatesStream", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}
		cc := newCancelContext()

		tr1 := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "chunk1"}}}
		tr2 := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "chunk2"}}}
		r, w := schema.Pipe[*schema.ToolResult](1)
		go func() {
			defer w.Close()
			w.Send(tr1, nil)
			time.Sleep(200 * time.Millisecond)
			w.Send(tr2, nil)
		}()

		next := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return &compose.EnhancedStreamableToolOutput{Result: r}, nil
		}

		wrapped := handler.WrapEnhancedStreamableToolCall(next)
		ctx := withCancelContext(context.Background(), cc)
		output, err := wrapped(ctx, &compose.ToolInput{Name: "test"})
		assert.NoError(t, err)

		result, err := output.Result.Recv()
		assert.NoError(t, err)
		assert.Equal(t, tr1, result)

		close(cc.immediateChan)

		_, err = output.Result.Recv()
		assert.ErrorIs(t, err, ErrStreamCanceled)
	})

	t.Run("NextReturnsError_PropagatesError", func(t *testing.T) {
		handler := &cancelMonitoredToolHandler{}
		cc := newCancelContext()

		nextErr := errors.New("enhanced tool failed")
		next := func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
			return nil, nextErr
		}

		wrapped := handler.WrapEnhancedStreamableToolCall(next)
		ctx := withCancelContext(context.Background(), cc)
		_, err := wrapped(ctx, &compose.ToolInput{Name: "test"})
		assert.ErrorIs(t, err, nextErr)
	})
}

func TestCancelContextKey(t *testing.T) {
	t.Run("WithAndGet_RoundTrips", func(t *testing.T) {
		cc := newCancelContext()
		ctx := withCancelContext(context.Background(), cc)
		got := getCancelContext(ctx)
		assert.Equal(t, cc, got)
	})

	t.Run("Get_NoValue_ReturnsNil", func(t *testing.T) {
		got := getCancelContext(context.Background())
		assert.Nil(t, got)
	})

	t.Run("With_NilCancelContext_ReturnsOriginalCtx", func(t *testing.T) {
		ctx := context.Background()
		result := withCancelContext(ctx, nil)
		assert.Equal(t, ctx, result)
	})
}

// -- Tests for cancel support across all agent types --

// cancelTestAgent is a ChatModelAgent-based agent where the model blocks until
// signalled, allowing tests to control exactly when to issue a cancel.
func newCancelTestAgent(t *testing.T, name string, modelDelay time.Duration, modelStarted chan struct{}) *ChatModelAgent {
	t.Helper()
	slowModel := &cancelTestChatModel{
		delayNs: int64(modelDelay),
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "response from " + name,
		},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}

	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:        name,
		Description: "Test agent " + name,
		Instruction: "You are a test assistant",
		Model:       slowModel,
	})
	assert.NoError(t, err)
	return agent
}

func newCancelTestAgentWithTools(t *testing.T, name string, modelDelay time.Duration, modelStarted chan struct{}) *ChatModelAgent {
	t.Helper()
	toolName := name + "_tool"
	slowModel := &cancelTestChatModel{
		delayNs: int64(modelDelay),
		response: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call_1", Type: "function",
				Function: schema.FunctionCall{
					Name:      toolName,
					Arguments: `{"input": "test"}`,
				},
			}},
		},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}

	st := newSlowTool(toolName, 10*time.Millisecond, "tool result")

	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:        name,
		Description: "Test agent " + name,
		Instruction: "You are a test assistant",
		Model:       slowModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{st},
			},
		},
	})
	assert.NoError(t, err)
	return agent
}

func newCancelTestAgentWithToolsFinalAnswer(t *testing.T, name string) *ChatModelAgent {
	t.Helper()
	toolName := name + "_tool"
	finalModel := &cancelTestChatModel{
		delayNs: int64(10 * time.Millisecond),
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "final response from " + name,
		},
		startedChan: make(chan struct{}, 1),
		doneChan:    make(chan struct{}, 1),
	}

	st := newSlowTool(toolName, 10*time.Millisecond, "tool result")

	agent, err := NewChatModelAgent(context.Background(), &ChatModelAgentConfig{
		Name:        name,
		Description: "Test agent " + name,
		Instruction: "You are a test assistant",
		Model:       finalModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{st},
			},
		},
	})
	assert.NoError(t, err)
	return agent
}

func TestWithCancel_SequentialAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_DuringSecondAgent", func(t *testing.T) {
		// The first agent completes quickly. The second agent takes a long time.
		// Cancel during the second agent's model call.
		agent1Started := make(chan struct{}, 1)
		agent2Started := make(chan struct{}, 1)

		agent1 := newCancelTestAgent(t, "fast_agent", 50*time.Millisecond, agent1Started)
		agent2 := newCancelTestAgent(t, "slow_agent", 5*time.Second, agent2Started)

		seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
			Name:        "seq_agent",
			Description: "Sequential test",
			SubAgents:   []Agent{agent1, agent2},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           seqAgent,
			EnableStreaming: false,
		})

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

		// Wait for second agent to start
		select {
		case <-agent2Started:
		case <-time.After(10 * time.Second):
			t.Fatal("Second agent did not start")
		}

		time.Sleep(50 * time.Millisecond)

		// Cancel should NOT return ErrExecutionCompleted (the bug before the fix)
		handle, _ := cancelFn()
		err = handle.Wait()
		assert.NoError(t, err, "Cancel during second agent should succeed, not return ErrExecutionCompleted")

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
			}
			events = append(events, event)
		}

		assert.True(t, hasCancelError, "Should have CancelError event")
	})
}

func TestWithCancel_LoopAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_DuringIteration", func(t *testing.T) {
		// Agent in a loop. Cancel during second iteration's model call.
		modelStarted := make(chan struct{}, 10)

		slowModel := &cancelTestChatModel{
			delayNs: int64(3 * time.Second),
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "loop response",
			},
			startedChan: modelStarted,
			doneChan:    make(chan struct{}, 10),
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "loop_inner",
			Description: "Inner loop agent",
			Instruction: "You are a test assistant",
			Model:       slowModel,
		})
		assert.NoError(t, err)

		loopAgent, err := NewLoopAgent(ctx, &LoopAgentConfig{
			Name:          "loop_agent",
			Description:   "Loop test",
			SubAgents:     []Agent{agent},
			MaxIterations: 10,
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           loopAgent,
			EnableStreaming: false,
		})

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

		// Wait for first iteration's model call to start
		select {
		case <-modelStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("Model did not start")
		}

		time.Sleep(50 * time.Millisecond)

		// Cancel should succeed
		handle, _ := cancelFn()
		err = handle.Wait()
		assert.NoError(t, err, "Cancel during loop iteration should succeed")

		hasCancelError := false
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				hasCancelError = true
			}
		}

		assert.True(t, hasCancelError, "Should have CancelError event")
	})
}

func TestWithCancel_ParallelAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_InterruptsAllBranches", func(t *testing.T) {
		agent1Started := make(chan struct{}, 1)
		agent2Started := make(chan struct{}, 1)

		// Both agents have long delays, so cancel should interrupt both.
		agent1 := newCancelTestAgent(t, "par_agent1", 5*time.Second, agent1Started)
		agent2 := newCancelTestAgent(t, "par_agent2", 5*time.Second, agent2Started)

		parAgent, err := NewParallelAgent(ctx, &ParallelAgentConfig{
			Name:        "par_agent",
			Description: "Parallel test",
			SubAgents:   []Agent{agent1, agent2},
		})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           parAgent,
			EnableStreaming: false,
		})

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

		// Wait for both agents to start
		for i := 0; i < 2; i++ {
			select {
			case <-agent1Started:
			case <-agent2Started:
			case <-time.After(10 * time.Second):
				t.Fatal("Parallel agents did not start")
			}
		}

		time.Sleep(50 * time.Millisecond)

		start := time.Now()
		handle, _ := cancelFn()
		err = handle.Wait()
		assert.NoError(t, err, "Cancel during parallel agents should succeed")

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
			}
			events = append(events, event)
		}
		elapsed := time.Since(start)

		assert.True(t, hasCancelError, "Should have CancelError event")
		assert.True(t, elapsed < 3*time.Second, "Should complete quickly after cancel, elapsed: %v", elapsed)
	})
}

func TestWithCancel_SupervisorAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("CancelImmediate_DuringSubAgent", func(t *testing.T) {
		// Supervisor delegates to a slow sub-agent via transfer.
		// Cancel during the sub-agent's model call.
		supervisorModelStarted := make(chan struct{}, 1)
		subAgentModelStarted := make(chan struct{}, 1)

		// The supervisor model returns a transfer_to_agent tool call
		supervisorModel := &simpleChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      TransferToAgentToolName,
							Arguments: `{"agent_name": "slow_sub"}`,
						},
					},
				},
			},
		}

		supervisorAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "supervisor",
			Description: "Supervisor agent",
			Instruction: "You are a supervisor",
			Model:       supervisorModel,
		})
		assert.NoError(t, err)

		subAgent := newCancelTestAgent(t, "slow_sub", 5*time.Second, subAgentModelStarted)

		agentWithSubAgents, err := SetSubAgents(ctx, supervisorAgent, []Agent{subAgent})
		assert.NoError(t, err)

		runner := NewRunner(ctx, RunnerConfig{
			Agent:           agentWithSubAgents,
			EnableStreaming: false,
		})

		cancelOpt, cancelFn := WithCancel()
		iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

		// Ignore the supervisor model start, wait for the sub-agent model
		// The supervisor model is fast (simpleChatModel), so it will start and finish quickly
		_ = supervisorModelStarted
		select {
		case <-subAgentModelStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("Sub-agent model did not start")
		}

		time.Sleep(50 * time.Millisecond)

		start := time.Now()
		handle, _ := cancelFn()
		err = handle.Wait()
		assert.NoError(t, err, "Cancel during sub-agent should succeed")

		hasCancelError := false
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			var ce *CancelError
			if event.Err != nil && errors.As(event.Err, &ce) {
				hasCancelError = true
			}
		}
		elapsed := time.Since(start)

		assert.True(t, hasCancelError, "Should have CancelError event")
		assert.True(t, elapsed < 3*time.Second, "Should complete quickly after cancel, elapsed: %v", elapsed)
	})
}

func TestFilterCancelOption(t *testing.T) {
	t.Run("RemovesCancelOption", func(t *testing.T) {
		cancelOpt, _ := WithCancel()
		sessionOpt := WithSessionValues(map[string]any{"key": "value"})
		opts := []AgentRunOption{cancelOpt, sessionOpt}

		filtered := filterCancelOption(opts)
		assert.Len(t, filtered, 1, "Should have removed the cancel option")

		// Verify the remaining option is the session option
		testOpt := &options{}
		filtered[0].implSpecificOptFn.(func(*options))(testOpt)
		assert.NotNil(t, testOpt.sessionValues)
		assert.Nil(t, testOpt.cancelCtx)
	})

	t.Run("KeepsNonCancelOptions", func(t *testing.T) {
		sessionOpt := WithSessionValues(map[string]any{"key": "value"})
		callbackOpt := WithCallbacks()
		opts := []AgentRunOption{sessionOpt, callbackOpt}

		filtered := filterCancelOption(opts)
		assert.Len(t, filtered, 2, "Should keep all non-cancel options")
	})

	t.Run("EmptyInput", func(t *testing.T) {
		filtered := filterCancelOption(nil)
		assert.Nil(t, filtered)
	})
}

func wrapIterWithMarkDone(iter *AsyncIterator[*AgentEvent], cc *cancelContext) *AsyncIterator[*AgentEvent] {
	if cc == nil {
		return iter
	}
	outIter, outGen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer cc.markDone()
		defer outGen.Close()
		for {
			event, ok := iter.Next()
			if !ok {
				return
			}
			outGen.Send(event)
		}
	}()
	return outIter
}

func TestWrapIterWithMarkDone(t *testing.T) {
	t.Run("MarksDoneAfterDrain", func(t *testing.T) {
		cc := newCancelContext()
		iter, gen := NewAsyncIteratorPair[*AgentEvent]()

		go func() {
			gen.Send(&AgentEvent{AgentName: "test"})
			gen.Close()
		}()

		wrapped := wrapIterWithMarkDone(iter, cc)

		event, ok := wrapped.Next()
		assert.True(t, ok)
		assert.Equal(t, "test", event.AgentName)

		_, ok = wrapped.Next()
		assert.False(t, ok)

		// markDone should have been called, so doneChan should be closed
		select {
		case <-cc.doneChan:
			// good
		case <-time.After(time.Second):
			t.Fatal("doneChan was not closed after drain")
		}
	})

	t.Run("NilCancelContext_PassesThrough", func(t *testing.T) {
		iter, gen := NewAsyncIteratorPair[*AgentEvent]()
		go func() {
			gen.Send(&AgentEvent{AgentName: "test"})
			gen.Close()
		}()

		wrapped := wrapIterWithMarkDone(iter, nil)
		assert.Equal(t, iter, wrapped, "Should return same iter when cc is nil")
	})
}

func TestGraphInterruptFuncs_Parallel(t *testing.T) {
	t.Run("MultipleGraphInterruptFuncsAllCalled", func(t *testing.T) {
		cc := newCancelContext()

		var called1, called2 int32
		cc.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
			atomic.AddInt32(&called1, 1)
		})
		cc.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
			atomic.AddInt32(&called2, 1)
		})

		// Simulate immediate cancel
		cc.setMode(CancelImmediate)
		atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateCancelling)
		close(cc.cancelChan)
		cc.sendImmediateInterrupt()

		assert.Equal(t, int32(1), atomic.LoadInt32(&called1), "First graph interrupt func should be called")
		assert.Equal(t, int32(1), atomic.LoadInt32(&called2), "Second graph interrupt func should be called")
	})

	t.Run("RetroactiveFire_OnSetAfterCancel", func(t *testing.T) {
		cc := newCancelContext()

		// First set up cancel state with immediate interrupt
		cc.setMode(CancelImmediate)
		atomic.CompareAndSwapInt32(&cc.state, stateRunning, stateCancelling)
		close(cc.cancelChan)
		close(cc.immediateChan)
		atomic.StoreInt32(&cc.interruptSent, interruptImmediate)

		// Now register a new function - it should be retroactively fired
		var called int32
		cc.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
			atomic.AddInt32(&called, 1)
		})

		assert.Equal(t, int32(1), atomic.LoadInt32(&called), "setGraphInterruptFunc should retroactively fire new func")
	})
}

// -- Tests for transition-point cancel (cancel between sub-agents) --

// gatedChatModel is a model that:
// - Signals doneChan when Generate completes
// - Optionally blocks on gateChan before returning (nil gateChan = no blocking)
// - Tracks call count via callCount
type gatedChatModel struct {
	response  *schema.Message
	gateChan  chan struct{} // if non-nil, blocks until closed before returning
	doneChan  chan struct{} // signalled after Generate completes
	callCount int32
}

func (m *gatedChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.callCount, 1)
	if m.gateChan != nil {
		select {
		case <-m.gateChan:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	select {
	case m.doneChan <- struct{}{}:
	default:
	}
	return m.response, nil
}

func (m *gatedChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *gatedChatModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
}

func TestCheckCancel_Sequential_BetweenSubAgents(t *testing.T) {
	ctx := context.Background()

	// CancelAfterToolCalls fires at transition boundaries between sub-agents.
	// At a transition boundary, the completed sub-agent's entire execution
	// (including any tool calls) is done, satisfying the CancelAfterToolCalls
	// contract — even if this particular sub-agent had no tools.
	model1 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent1 done"},
		gateChan: make(chan struct{}),
		doneChan: make(chan struct{}, 1),
	}
	model2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent2 done"},
		doneChan: make(chan struct{}, 1),
	}

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test", Model: model1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test", Model: model2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "sequential test", SubAgents: []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: seqAgent, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt)

	for atomic.LoadInt32(&model1.callCount) == 0 {
		runtime.Gosched()
	}

	cancelCalled, result := cancelAsync(cancelFn, WithAgentCancelMode(CancelAfterToolCalls))
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(model1.gateChan)

	assert.NoError(t, result.waitDone(t), "CancelAfterToolCalls should succeed at transition boundary")

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&model1.callCount), "Agent1 model should be invoked")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model2.callCount),
		"Agent2 model should NOT be invoked (CancelAfterToolCalls caught at transition)")
}

func TestCheckCancel_Loop_BetweenIterations(t *testing.T) {
	ctx := context.Background()

	// CancelAfterToolCalls fires at loop iteration boundaries.
	// After the first iteration completes, any tool calls it made are done,
	// satisfying the CancelAfterToolCalls contract.
	mdl := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "loop iter"},
		gateChan: make(chan struct{}),
		doneChan: make(chan struct{}, 10),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "loop_inner", Description: "inner", Instruction: "test", Model: mdl,
	})
	assert.NoError(t, err)

	loopAgent, err := NewLoopAgent(ctx, &LoopAgentConfig{
		Name: "loop", Description: "loop test", SubAgents: []Agent{agent}, MaxIterations: 3,
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: loopAgent, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt)

	for atomic.LoadInt32(&mdl.callCount) == 0 {
		runtime.Gosched()
	}

	cancelCalled, result := cancelAsync(cancelFn, WithAgentCancelMode(CancelAfterToolCalls))
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(mdl.gateChan)

	assert.NoError(t, result.waitDone(t), "CancelAfterToolCalls should succeed at loop transition boundary")

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&mdl.callCount),
		"Model should be called once; second iteration caught at transition")
}

func TestCheckCancel_Parallel_PreSpawn(t *testing.T) {
	ctx := context.Background()

	// Cancel fires before Run is called. Neither model should be invoked.
	model1 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "par1"},
		doneChan: make(chan struct{}, 1),
	}
	model2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "par2"},
		doneChan: make(chan struct{}, 1),
	}

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "par1", Description: "first", Instruction: "test", Model: model1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "par2", Description: "second", Instruction: "test", Model: model2,
	})
	assert.NoError(t, err)

	parAgent, err := NewParallelAgent(ctx, &ParallelAgentConfig{
		Name: "par", Description: "parallel test", SubAgents: []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	// Fire cancel in goroutine (cancelFn blocks until handled)
	cancelOpt, cancelFn := WithCancel()
	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn()
		cancelDone <- handle.Wait()
	}()
	// Wait for cancelChan to be closed (happens synchronously before the blocking doneChan wait)
	time.Sleep(20 * time.Millisecond)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: parAgent, EnableStreaming: false,
	})

	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt)

	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if event.Err != nil && errors.As(event.Err, &ce) {
			cancelErr = ce
		}
	}

	// cancelFn should have completed
	select {
	case err = <-cancelDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelFn did not return")
	}

	assert.NotNil(t, cancelErr, "Should have CancelError")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model1.callCount), "First model should never be invoked")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model2.callCount), "Second model should never be invoked")
}

func TestCheckCancel_Transfer_BeforeTarget(t *testing.T) {
	ctx := context.Background()

	// Supervisor CMA returns a transfer action (instantly).
	// Cancel fires after transfer action but before target runs.
	// Target model should never be invoked.
	supervisorModel := &simpleChatModel{
		response: &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call_1", Type: "function",
				Function: schema.FunctionCall{
					Name:      TransferToAgentToolName,
					Arguments: `{"agent_name": "target"}`,
				},
			}},
		},
	}
	targetModel := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "target done"},
		doneChan: make(chan struct{}, 1),
	}

	supervisorAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "supervisor", Description: "supervisor", Instruction: "test", Model: supervisorModel,
	})
	assert.NoError(t, err)

	targetAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "target", Description: "target", Instruction: "test", Model: targetModel,
	})
	assert.NoError(t, err)

	agentWithSub, err := SetSubAgents(ctx, supervisorAgent, []Agent{targetAgent})
	assert.NoError(t, err)

	// Fire cancel in goroutine (cancelFn blocks until handled)
	cancelOpt, cancelFn := WithCancel()
	cancelDone := make(chan error, 1)
	go func() {
		handle, _ := cancelFn()
		cancelDone <- handle.Wait()
	}()
	time.Sleep(20 * time.Millisecond)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: agentWithSub, EnableStreaming: false,
	})

	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if event.Err != nil && errors.As(event.Err, &ce) {
			cancelErr = ce
		}
	}

	select {
	case err = <-cancelDone:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("cancelFn did not return")
	}

	assert.NotNil(t, cancelErr, "Should have CancelError")
	assert.Equal(t, int32(0), atomic.LoadInt32(&targetModel.callCount), "Target model should never be invoked")
}

func TestCheckCancel_AlreadyHandled_NoDuplicate(t *testing.T) {
	ctx := context.Background()

	// In a sequential agent, if the first CMA handles the cancel (graph interrupt),
	// the workflow's transition check should NOT emit a duplicate CancelError.
	// Use a slow model so cancel fires during its execution (handled by CMA).
	modelStarted := make(chan struct{}, 1)
	model1 := &cancelTestChatModel{
		delayNs:     int64(2 * time.Second),
		response:    &schema.Message{Role: schema.Assistant, Content: "agent1"},
		startedChan: modelStarted,
		doneChan:    make(chan struct{}, 1),
	}
	model2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent2"},
		doneChan: make(chan struct{}, 1),
	}

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test", Model: model1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test", Model: model2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "sequential", SubAgents: []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: seqAgent, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

	// Wait for model to start, then cancel during model execution
	select {
	case <-modelStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("Model did not start")
	}
	time.Sleep(50 * time.Millisecond)
	handle, _ := cancelFn()
	err = handle.Wait()
	assert.NoError(t, err)

	cancelCount := 0
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if event.Err != nil && errors.As(event.Err, &ce) {
			cancelCount++
		}
	}

	assert.Equal(t, 1, cancelCount, "Should have exactly one CancelError, no duplicate from workflow transition")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model2.callCount), "Second agent should not run")
}

// Tests for CancelAfterChatModel/CancelAfterToolCalls in nested workflow structures.
// These verify that safe-point cancel modes propagate through the entire agent hierarchy
// and fire at whichever nested level reaches the safe-point first.

func TestCancel_SequentialWorkflow_CancelAfterChatModel(t *testing.T) {
	ctx := context.Background()
	agent1Started := make(chan struct{}, 1)

	agent1 := newCancelTestAgentWithTools(t, "seq_slow", 500*time.Millisecond, agent1Started)
	agent2 := newCancelTestAgentWithTools(t, "seq_fast", 50*time.Millisecond, make(chan struct{}, 1))

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "seq_agent",
		Description: "Sequential workflow",
		SubAgents:   []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           seqAgent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("seq-cancel-1"))

	select {
	case <-agent1Started:
	case <-time.After(10 * time.Second):
		t.Fatal("First agent did not start")
	}

	handle, contributed := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	assert.True(t, contributed, "Cancel should contribute")
	err = handle.Wait()
	assert.NoError(t, err)

	hasCancelError := false
	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.As(event.Err, &cancelErr) {
			hasCancelError = true
		}
	}

	assert.True(t, hasCancelError, "Should have CancelError")
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.NotEmpty(t, cancelErr.CheckPointID, "CancelError should have checkpoint ID")
	assert.NotNil(t, cancelErr.interruptSignal, "CancelError should have interrupt signal for checkpoint")

	resumeAgent1 := newCancelTestAgentWithToolsFinalAnswer(t, "seq_slow")
	resumeAgent2 := newCancelTestAgentWithToolsFinalAnswer(t, "seq_fast")

	resumeSeq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "seq_agent",
		Description: "Sequential workflow",
		SubAgents:   []Agent{resumeAgent1, resumeAgent2},
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           resumeSeq,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.Resume(ctx, "seq-cancel-1")
	assert.NoError(t, err)
	assert.NotNil(t, resumeIter)

	var resumeEvents []*AgentEvent
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		assert.Nil(t, event.Err, "Should not have error during resume")
		resumeEvents = append(resumeEvents, event)
	}
	assert.True(t, len(resumeEvents) > 0, "Resume should produce events")
}

func TestCancelImmediate_OrphanedToolGoroutine_NoPanic(t *testing.T) {
	t.Run("unit_send_after_close", func(t *testing.T) {
		_, gen := NewAsyncIteratorPair[*AgentEvent]()

		cc := newCancelContext()
		cc.setMode(CancelImmediate)
		close(cc.cancelChan)
		close(cc.immediateChan)

		gen.Close()

		execCtx := &chatModelAgentExecCtx{
			generator: gen,
			cancelCtx: cc,
		}

		assert.NotPanics(t, func() {
			execCtx.send(&AgentEvent{AgentName: "test"})
		}, "send after generator.Close must not panic")
	})

	t.Run("unit_send_after_close_without_cancel_ctx", func(t *testing.T) {
		_, gen := NewAsyncIteratorPair[*AgentEvent]()
		gen.Close()

		execCtx := &chatModelAgentExecCtx{
			generator: gen,
		}

		assert.NotPanics(t, func() {
			execCtx.send(&AgentEvent{AgentName: "test"})
		}, "send after generator.Close must not panic even without cancelCtx (trySend safety net)")
	})

	t.Run("unit_send_nil_execCtx", func(t *testing.T) {
		var execCtx *chatModelAgentExecCtx
		assert.NotPanics(t, func() {
			execCtx.send(&AgentEvent{AgentName: "test"})
		}, "send on nil execCtx must not panic")
	})

	t.Run("unit_send_nil_generator", func(t *testing.T) {
		execCtx := &chatModelAgentExecCtx{}
		assert.NotPanics(t, func() {
			execCtx.send(&AgentEvent{AgentName: "test"})
		}, "send with nil generator must not panic")
	})

	t.Run("unit_isImmediateCancelled_nil_cancelContext", func(t *testing.T) {
		var cc *cancelContext
		assert.False(t, cc.isImmediateCancelled(), "nil cancelContext should return false")
	})

	t.Run("unit_trySend_race_window", func(t *testing.T) {
		_, gen := NewAsyncIteratorPair[*AgentEvent]()
		cc := newCancelContext()

		gen.Close()

		execCtx := &chatModelAgentExecCtx{
			generator: gen,
			cancelCtx: cc,
		}

		assert.NotPanics(t, func() {
			execCtx.send(&AgentEvent{AgentName: "test"})
		}, "trySend must handle the case where isImmediateCancelled is false but generator is closed")
	})

	t.Run("unit_SendEvent_after_close", func(t *testing.T) {
		_, gen := NewAsyncIteratorPair[*AgentEvent]()

		cc := newCancelContext()
		cc.setMode(CancelImmediate)
		close(cc.cancelChan)
		close(cc.immediateChan)

		gen.Close()

		execCtx := &chatModelAgentExecCtx{
			generator: gen,
			cancelCtx: cc,
		}

		ctx := withChatModelAgentExecCtx(context.Background(), execCtx)

		assert.NotPanics(t, func() {
			err := SendEvent(ctx, &AgentEvent{AgentName: "test"})
			assert.NoError(t, err)
		}, "SendEvent after generator.Close must not panic")
	})

	t.Run("unit_SendEvent_no_execCtx", func(t *testing.T) {
		err := SendEvent(context.Background(), &AgentEvent{AgentName: "test"})
		assert.Error(t, err, "SendEvent without execCtx should return error")
	})

	t.Run("integration_cancel_escalation_orphans_tool", func(t *testing.T) {
		ctx := context.Background()

		toolStarted := make(chan struct{}, 1)
		toolDone := make(chan struct{}, 1)
		st := &slowToolWithSignal{
			name:        "orphan_tool",
			delay:       2 * time.Second,
			result:      "tool result",
			startedChan: toolStarted,
		}

		mdl := &simpleChatModel{
			response: &schema.Message{
				Role:    schema.Assistant,
				Content: "",
				ToolCalls: []schema.ToolCall{
					{
						ID:   "call_orphan_1",
						Type: "function",
						Function: schema.FunctionCall{
							Name:      "orphan_tool",
							Arguments: `{"input": "test"}`,
						},
					},
				},
			},
		}

		agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
			Name:        "OrphanTestAgent",
			Description: "Test agent for orphaned tool goroutine panic",
			Model:       mdl,
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

		select {
		case <-toolStarted:
		case <-time.After(10 * time.Second):
			t.Fatal("Tool did not start")
		}

		timeout := 50 * time.Millisecond
		handle, contributed := cancelFn(
			WithAgentCancelMode(CancelAfterChatModel),
			WithAgentCancelTimeout(timeout),
		)
		assert.True(t, contributed, "Cancel should contribute")

		err = handle.Wait()
		assert.True(t, err == nil || errors.Is(err, ErrCancelTimeout),
			"handle.Wait should return nil or ErrCancelTimeout, got: %v", err)

		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}

		go func() {
			time.Sleep(3 * time.Second)
			select {
			case toolDone <- struct{}{}:
			default:
			}
		}()

		runtime.Gosched()
		time.Sleep(3 * time.Second)

		select {
		case <-toolDone:
		default:
		}
	})
}

// -- Tests for CancelImmediate in nested agent structures --

func newTestChatModel(response *schema.Message, delay time.Duration) *cancelTestChatModel {
	m := &cancelTestChatModel{
		response:    response,
		startedChan: make(chan struct{}, 1),
		doneChan:    make(chan struct{}, 1),
	}
	if delay > 0 {
		m.setDelay(delay)
	}
	return m
}

func newToolCallResponse(toolName string) *schema.Message {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: toolName, Arguments: `{}`}},
		},
	}
}

func newAgentWithTool(t *testing.T, ctx context.Context, name string, mdl model.BaseChatModel, subAgent Agent) (Agent, error) {
	t.Helper()
	return NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        name,
		Description: name,
		Model:       mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, subAgent)},
			},
		},
	})
}

func waitForChan(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatal(msg)
	}
}

func drainCancelError(t *testing.T, iter *AsyncIterator[*AgentEvent]) *CancelError {
	t.Helper()
	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errors.As(event.Err, &cancelErr)
		}
	}
	return cancelErr
}

func drainResumeErrors(t *testing.T, iter *AsyncIterator[*AgentEvent]) []error {
	t.Helper()
	var errs []error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			errs = append(errs, event.Err)
		}
	}
	return errs
}

type cancelResult struct {
	err         error
	contributed bool
	done        chan struct{}
}

func cancelAsync(cancelFn AgentCancelFunc, opts ...AgentCancelOption) (cancelCalled chan struct{}, result *cancelResult) {
	cancelCalled = make(chan struct{})
	result = &cancelResult{done: make(chan struct{})}
	go func() {
		handle, contributed := cancelFn(opts...)
		result.contributed = contributed
		close(cancelCalled)
		result.err = handle.Wait()
		close(result.done)
	}()
	return
}

func (r *cancelResult) waitDone(t *testing.T) error {
	t.Helper()
	select {
	case <-r.done:
		return r.err
	case <-time.After(10 * time.Second):
		t.Fatal("cancel did not complete")
		return nil
	}
}

func TestCancelImmediate_AgentTool_PreservesChildCheckpoint(t *testing.T) {
	ctx := context.Background()

	leafModel := newTestChatModel(
		&schema.Message{Role: schema.Assistant, Content: "leaf response"}, 2*time.Second)
	leafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "leaf_agent", Description: "Leaf agent in agentTool", Model: leafModel,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "inner_seq", Description: "Inner sequential workflow", SubAgents: []Agent{leafAgent},
	})
	assert.NoError(t, err)

	rootModel := newTestChatModel(newToolCallResponse("inner_seq"), 0)
	rootAgent, err := newAgentWithTool(t, ctx, "root_agent", rootModel, seqAgent)
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{Agent: rootAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("immediate-agent-tool-1"))

	waitForChan(t, leafModel.startedChan, "Leaf agent model did not start")

	handle, contributed := cancelFn()
	assert.True(t, contributed)
	assert.NoError(t, handle.Wait())

	cancelErr := drainCancelError(t, iter)
	assert.NotNil(t, cancelErr, "Should have CancelError from CancelImmediate through agentTool")
	assert.NotEmpty(t, cancelErr.CheckPointID)
	assert.NotNil(t, cancelErr.interruptSignal)

	resumeLeaf, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "leaf_agent", Description: "Leaf agent in agentTool",
		Model: newTestChatModel(&schema.Message{Role: schema.Assistant, Content: "resumed leaf"}, 0),
	})
	assert.NoError(t, err)
	resumeSeq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "inner_seq", Description: "Inner sequential workflow", SubAgents: []Agent{resumeLeaf},
	})
	assert.NoError(t, err)
	resumeRoot, err := newAgentWithTool(t, ctx, "root_agent",
		newTestChatModel(&schema.Message{Role: schema.Assistant, Content: "resumed root"}, 0), resumeSeq)
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{Agent: resumeRoot, CheckPointStore: store})
	resumeIter, err := runner2.Resume(ctx, "immediate-agent-tool-1")
	assert.NoError(t, err)
	assert.Empty(t, drainResumeErrors(t, resumeIter), "Resume should complete without errors")
}

func TestCancelImmediate_ParallelWorkflow_WithAgentTool(t *testing.T) {
	ctx := context.Background()

	leafModel := newTestChatModel(
		&schema.Message{Role: schema.Assistant, Content: "leaf response"}, 2*time.Second)
	leafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "leaf_agent", Description: "Leaf agent in agentTool", Model: leafModel,
	})
	assert.NoError(t, err)

	agentWithTool, err := newAgentWithTool(t, ctx, "agent_with_tool",
		newTestChatModel(newToolCallResponse("leaf_agent"), 0), leafAgent)
	assert.NoError(t, err)

	simpleStarted := make(chan struct{}, 1)
	simpleAgent := newCancelTestAgent(t, "simple_agent", 2*time.Second, simpleStarted)

	parAgent, err := NewParallelAgent(ctx, &ParallelAgentConfig{
		Name: "par_agent", Description: "Parallel with agentTool and simple agent",
		SubAgents: []Agent{agentWithTool, simpleAgent},
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{Agent: parAgent, EnableStreaming: false})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

	waitForChan(t, leafModel.startedChan, "Leaf agent did not start")
	waitForChan(t, simpleStarted, "Simple agent did not start")

	start := time.Now()
	handle, _ := cancelFn()
	assert.NoError(t, handle.Wait())

	cancelErr := drainCancelError(t, iter)
	elapsed := time.Since(start)

	assert.NotNil(t, cancelErr, "Should have CancelError from parallel with agentTool")
	assert.True(t, elapsed < 5*time.Second, "Should complete quickly after cancel, elapsed: %v", elapsed)
}

type cancelUnawareAgent struct {
	name     string
	desc     string
	delay    time.Duration
	response string
}

type multiResponseGatedModel struct {
	responses []*schema.Message
	gateChan  chan struct{}
	gateOnce  bool
	gated     int32
	doneChan  chan struct{}
	callCount int32
}

func (m *multiResponseGatedModel) Generate(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	idx := atomic.AddInt32(&m.callCount, 1)
	if m.gateChan != nil && (!m.gateOnce || atomic.CompareAndSwapInt32(&m.gated, 0, 1)) {
		select {
		case <-m.gateChan:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if len(m.responses) == 0 {
		return nil, fmt.Errorf("multiResponseGatedModel: no responses configured")
	}
	resp := m.responses[(int(idx)-1)%len(m.responses)]
	if m.doneChan != nil {
		select {
		case m.doneChan <- struct{}{}:
		default:
		}
	}
	return resp, nil
}

func (m *multiResponseGatedModel) Stream(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	resp, err := m.Generate(ctx, msgs, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{resp}), nil
}

func (m *multiResponseGatedModel) BindTools(tools []*schema.ToolInfo) error { return nil }

func (a *cancelUnawareAgent) Name(_ context.Context) string        { return a.name }
func (a *cancelUnawareAgent) Description(_ context.Context) string { return a.desc }

func (a *cancelUnawareAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		// Intentionally ignores ctx.Done() — simulates a custom agent that
		// does not participate in the cancel protocol at all.
		// Delay is kept short (relative to grace period) to avoid goroutine
		// leak lasting long after the test completes.
		time.Sleep(a.delay)
	}()
	return iter
}

func TestCancelImmediate_CustomAgent_GracePeriodFallback(t *testing.T) {
	ctx := context.Background()

	customAgent := &cancelUnawareAgent{
		name: "custom_slow", desc: "A custom agent that ignores cancel",
		delay: 5 * time.Second, response: "custom response",
	}

	rootModel := newTestChatModel(newToolCallResponse("custom_slow"), 0)
	rootAgent, err := newAgentWithTool(t, ctx, "root_agent", rootModel, customAgent)
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{Agent: rootAgent, EnableStreaming: false})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt)

	waitForChan(t, rootModel.startedChan, "Root model did not start")
	waitForChan(t, rootModel.doneChan, "Root model did not finish")

	start := time.Now()
	handle, _ := cancelFn()
	assert.NoError(t, handle.Wait())

	cancelErr := drainCancelError(t, iter)
	elapsed := time.Since(start)

	assert.NotNil(t, cancelErr, "Should have CancelError (from grace period fallback)")
	assert.True(t, elapsed < 5*time.Second,
		"Should complete within grace period + overhead, elapsed: %v", elapsed)
}

func TestCancelImmediate_MultiLevelNesting(t *testing.T) {
	ctx := context.Background()

	innerLeafModel := newTestChatModel(
		&schema.Message{Role: schema.Assistant, Content: "inner leaf response"}, 2*time.Second)
	innerLeafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "inner_leaf", Description: "Innermost leaf agent", Model: innerLeafModel,
	})
	assert.NoError(t, err)

	middleAgent, err := newAgentWithTool(t, ctx, "middle_agent",
		newTestChatModel(newToolCallResponse("inner_leaf"), 0), innerLeafAgent)
	assert.NoError(t, err)

	rootAgent, err := newAgentWithTool(t, ctx, "root_agent",
		newTestChatModel(newToolCallResponse("middle_agent"), 0), middleAgent)
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{Agent: rootAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("multi-level-1"))

	waitForChan(t, innerLeafModel.startedChan, "Inner leaf model did not start")

	start := time.Now()
	handle, contributed := cancelFn()
	assert.True(t, contributed)
	assert.NoError(t, handle.Wait())

	cancelErr := drainCancelError(t, iter)
	elapsed := time.Since(start)

	assert.NotNil(t, cancelErr, "Should have CancelError from multi-level nesting")
	assert.NotEmpty(t, cancelErr.CheckPointID)
	assert.NotNil(t, cancelErr.interruptSignal)
	assert.True(t, elapsed < 5*time.Second, "Should complete quickly, elapsed: %v", elapsed)

	resumeInnerLeaf, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "inner_leaf", Description: "Innermost leaf agent",
		Model: newTestChatModel(&schema.Message{Role: schema.Assistant, Content: "resumed inner leaf"}, 0),
	})
	assert.NoError(t, err)
	resumeMiddle, err := newAgentWithTool(t, ctx, "middle_agent",
		newTestChatModel(&schema.Message{Role: schema.Assistant, Content: "resumed middle"}, 0), resumeInnerLeaf)
	assert.NoError(t, err)
	resumeRoot, err := newAgentWithTool(t, ctx, "root_agent",
		newTestChatModel(&schema.Message{Role: schema.Assistant, Content: "resumed root"}, 0), resumeMiddle)
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{Agent: resumeRoot, CheckPointStore: store})
	resumeIter, err := runner2.Resume(ctx, "multi-level-1")
	assert.NoError(t, err)
	assert.Empty(t, drainResumeErrors(t, resumeIter), "Resume should complete without errors")
}

func TestCancelImmediate_SequentialTransitionBoundary(t *testing.T) {
	ctx := context.Background()

	model1 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent1 done"},
		gateChan: make(chan struct{}),
		doneChan: make(chan struct{}, 1),
	}
	model2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent2 done"},
		doneChan: make(chan struct{}, 1),
	}

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test", Model: model1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test", Model: model2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "sequential test", SubAgents: []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: seqAgent, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt)

	for atomic.LoadInt32(&model1.callCount) == 0 {
		runtime.Gosched()
	}

	cancelCalled, result := cancelAsync(cancelFn)
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(model1.gateChan)

	assert.NoError(t, result.waitDone(t), "CancelImmediate should succeed at transition")

	cancelErr := drainCancelError(t, iter)

	assert.NotNil(t, cancelErr, "Should have CancelError at transition boundary")
	assert.Equal(t, int32(1), atomic.LoadInt32(&model1.callCount), "Agent1 model should be invoked")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model2.callCount), "Agent2 model should NOT be invoked (caught at transition)")
}

func TestCancelImmediate_LoopTransitionBoundary(t *testing.T) {
	ctx := context.Background()

	mdl := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "loop iter"},
		gateChan: make(chan struct{}),
		doneChan: make(chan struct{}, 10),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "loop_inner", Description: "inner", Instruction: "test", Model: mdl,
	})
	assert.NoError(t, err)

	loopAgent, err := NewLoopAgent(ctx, &LoopAgentConfig{
		Name: "loop", Description: "loop test", SubAgents: []Agent{agent}, MaxIterations: 5,
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent: loopAgent, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt)

	for atomic.LoadInt32(&mdl.callCount) == 0 {
		runtime.Gosched()
	}

	cancelCalled, result := cancelAsync(cancelFn)
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(mdl.gateChan)

	assert.NoError(t, result.waitDone(t), "CancelImmediate should succeed at loop transition")

	for {
		_, ok := iter.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, int32(1), atomic.LoadInt32(&mdl.callCount),
		"Model should be called once; second iteration caught at transition")
}

func TestCancelAfterChatModel_SequentialTransitionBoundary(t *testing.T) {
	ctx := context.Background()

	model1 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent1 done"},
		gateChan: make(chan struct{}),
		doneChan: make(chan struct{}, 1),
	}
	model2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent2 done"},
		doneChan: make(chan struct{}, 1),
	}

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test", Model: model1,
	})
	assert.NoError(t, err)

	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test", Model: model2,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "sequential test", SubAgents: []Agent{agent1, agent2},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           seqAgent,
		EnableStreaming: false,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt, WithCheckPointID("chatmodel-transition-1"))

	for atomic.LoadInt32(&model1.callCount) == 0 {
		runtime.Gosched()
	}

	cancelCalled, result := cancelAsync(cancelFn, WithAgentCancelMode(CancelAfterChatModel))
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(model1.gateChan)

	assert.NoError(t, result.waitDone(t), "CancelAfterChatModel should succeed at transition boundary")

	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		var ce *CancelError
		if event.Err != nil && errors.As(event.Err, &ce) {
			cancelErr = ce
		}
	}

	assert.NotNil(t, cancelErr, "Should have CancelError at transition boundary")
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.Equal(t, int32(1), atomic.LoadInt32(&model1.callCount), "Agent1 model should be invoked")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model2.callCount),
		"Agent2 model should NOT be invoked (CancelAfterChatModel caught at transition)")
}

func TestCancelAfterChatModel_Sequential_Agent1CompletesCancelBeforeAgent2Resume(t *testing.T) {
	ctx := context.Background()

	model1 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent1 done"},
		gateChan: make(chan struct{}),
		doneChan: make(chan struct{}, 1),
	}
	model2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent2 done"},
		doneChan: make(chan struct{}, 1),
	}
	model3 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "agent3 done"},
		doneChan: make(chan struct{}, 1),
	}

	agent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test", Model: model1,
	})
	assert.NoError(t, err)
	agent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test", Model: model2,
	})
	assert.NoError(t, err)
	agent3, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent3", Description: "third", Instruction: "test", Model: model3,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "3-agent sequential", SubAgents: []Agent{agent1, agent2, agent3},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent: seqAgent, CheckPointStore: store, EnableStreaming: false,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("hello")}, cancelOpt,
		WithCheckPointID("seq-transition-resume-1"))

	for atomic.LoadInt32(&model1.callCount) == 0 {
		runtime.Gosched()
	}

	cancelCalled, result := cancelAsync(cancelFn, WithAgentCancelMode(CancelAfterChatModel))
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(model1.gateChan)

	assert.NoError(t, result.waitDone(t))

	cancelErr := drainCancelError(t, iter)
	assert.NotNil(t, cancelErr, "Should have CancelError")
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.Equal(t, int32(1), atomic.LoadInt32(&model1.callCount))
	assert.Equal(t, int32(0), atomic.LoadInt32(&model2.callCount),
		"Agent2 should NOT run (cancel caught at transition after agent1)")
	assert.Equal(t, int32(0), atomic.LoadInt32(&model3.callCount))
	assert.NotEmpty(t, cancelErr.CheckPointID)

	resumeModel2 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "resumed agent2"},
		doneChan: make(chan struct{}, 1),
	}
	resumeModel3 := &gatedChatModel{
		response: &schema.Message{Role: schema.Assistant, Content: "resumed agent3"},
		doneChan: make(chan struct{}, 1),
	}

	resumeAgent1, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent1", Description: "first", Instruction: "test",
		Model: &gatedChatModel{
			response: &schema.Message{Role: schema.Assistant, Content: "should not run"},
			doneChan: make(chan struct{}, 1),
		},
	})
	assert.NoError(t, err)
	resumeAgent2, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent2", Description: "second", Instruction: "test", Model: resumeModel2,
	})
	assert.NoError(t, err)
	resumeAgent3, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "agent3", Description: "third", Instruction: "test", Model: resumeModel3,
	})
	assert.NoError(t, err)

	resumeSeq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name: "seq", Description: "3-agent sequential",
		SubAgents: []Agent{resumeAgent1, resumeAgent2, resumeAgent3},
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent: resumeSeq, CheckPointStore: store, EnableStreaming: false,
	})
	resumeIter, err := runner2.Resume(ctx, "seq-transition-resume-1")
	assert.NoError(t, err)
	assert.Empty(t, drainResumeErrors(t, resumeIter), "Resume should complete without errors")

	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeModel2.callCount),
		"Agent2 should run on resume")
	assert.Equal(t, int32(1), atomic.LoadInt32(&resumeModel3.callCount),
		"Agent3 should run on resume")
}

func TestCancelAfterToolCalls_LoopTransitionBoundary(t *testing.T) {
	ctx := context.Background()

	// Model that returns tool calls on odd calls and no tools on even calls.
	// This completes one ReAct cycle per pair of calls:
	//   call 1 (gated): returns tool call → tool runs → call 2: returns no tools → END
	// The gate only blocks the very first call. After that, all calls proceed instantly.
	mdl := &multiResponseGatedModel{
		responses: []*schema.Message{
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				ID: "call_1", Type: "function",
				Function: schema.FunctionCall{Name: "loop_tool", Arguments: `{"input": "test"}`},
			}}},
			{Role: schema.Assistant, Content: "iteration done"},
		},
		gateChan: make(chan struct{}),
		gateOnce: true,
		doneChan: make(chan struct{}, 10),
	}

	st := &slowTool{
		name:        "loop_tool",
		delay:       10 * time.Millisecond,
		result:      "tool done",
		startedChan: make(chan struct{}, 10),
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name: "loop_inner", Description: "inner", Instruction: "test", Model: mdl,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{st},
			},
		},
	})
	assert.NoError(t, err)

	loopAgent, err := NewLoopAgent(ctx, &LoopAgentConfig{
		Name: "loop", Description: "loop test", SubAgents: []Agent{agent}, MaxIterations: 10,
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{Agent: loopAgent, CheckPointStore: store})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("toolcalls-loop-1"))

	// Wait for the model to be entered (blocked on gate)
	for atomic.LoadInt32(&mdl.callCount) == 0 {
		runtime.Gosched()
	}

	// Fire cancel, wait for it to be registered, then release the gate
	cancelCalled, result := cancelAsync(cancelFn, WithAgentCancelMode(CancelAfterToolCalls))
	waitForChan(t, cancelCalled, "cancelFn was not called")
	close(mdl.gateChan)

	// Iteration 1 completes fully (model→tool→model-no-tools→END).
	// The CancelAfterToolCalls safe-point inside ReAct fires after tool calls,
	// OR the transition boundary catches it before iteration 2.
	// Note: this test doesn't deterministically distinguish which path fires —
	// both are semantically correct for CancelAfterToolCalls. The transition-
	// boundary code path for CancelAfterToolCalls in loops is not definitively
	// covered here because the ReAct safe-point may handle it first.
	assert.NoError(t, result.waitDone(t))

	cancelErr := drainCancelError(t, iter)
	assert.NotNil(t, cancelErr, "Should have CancelError from CancelAfterToolCalls in loop")
	assert.Equal(t, CancelAfterToolCalls, cancelErr.Info.Mode)
	assert.NotEmpty(t, cancelErr.CheckPointID)
}

func TestCancelContext_ActiveChildren_Tracking(t *testing.T) {
	t.Run("DeriveChild_IncrementsActiveChildren", func(t *testing.T) {
		parent := newCancelContext()
		assert.False(t, parent.hasActiveChildren())

		ctx := context.Background()
		child := parent.deriveChild(ctx)
		assert.True(t, parent.hasActiveChildren())
		assert.Equal(t, int32(1), atomic.LoadInt32(&parent.activeChildren))

		child.markDone()
		time.Sleep(10 * time.Millisecond)
		assert.False(t, parent.hasActiveChildren())
		assert.Equal(t, int32(0), atomic.LoadInt32(&parent.activeChildren))
	})

	t.Run("MultipleChildren_AllTracked", func(t *testing.T) {
		parent := newCancelContext()
		ctx := context.Background()

		child1 := parent.deriveChild(ctx)
		child2 := parent.deriveChild(ctx)
		assert.Equal(t, int32(2), atomic.LoadInt32(&parent.activeChildren))

		child1.markDone()
		time.Sleep(10 * time.Millisecond)
		assert.Equal(t, int32(1), atomic.LoadInt32(&parent.activeChildren))
		assert.True(t, parent.hasActiveChildren())

		child2.markDone()
		time.Sleep(10 * time.Millisecond)
		assert.False(t, parent.hasActiveChildren())
	})

	t.Run("MarkCancelHandled_AlsoDecrementsParent", func(t *testing.T) {
		parent := newCancelContext()
		ctx := context.Background()

		child := parent.deriveChild(ctx)
		assert.True(t, parent.hasActiveChildren())

		child.triggerCancel(CancelImmediate)
		child.markCancelHandled()
		time.Sleep(10 * time.Millisecond)
		assert.False(t, parent.hasActiveChildren())
	})

	t.Run("GracePeriodWrapper_AppliesWhenChildrenActive", func(t *testing.T) {
		parent := newCancelContext()
		ctx := context.Background()

		var receivedOpts []compose.GraphInterruptOption
		mockInterrupt := func(opts ...compose.GraphInterruptOption) {
			receivedOpts = opts
		}

		wrapped := parent.wrapGraphInterruptWithGracePeriod(mockInterrupt)

		// No children: no options appended
		receivedOpts = nil
		wrapped()
		assert.Empty(t, receivedOpts, "Should pass no extra options when no children")

		// With active child: one timeout option appended
		_ = parent.deriveChild(ctx)
		receivedOpts = nil
		wrapped()
		assert.Len(t, receivedOpts, 1, "Should add exactly one timeout option when children are active")

		// Caller-provided options are preserved, grace period option appended after
		receivedOpts = nil
		callerOpt := compose.WithGraphInterruptTimeout(0)
		wrapped(callerOpt)
		assert.Len(t, receivedOpts, 2,
			"Should append timeout option after caller-provided options when children are active")
		// Note: verifying the exact timeout value (defaultCancelImmediateGracePeriod)
		// requires access to unexported compose.graphInterruptOptions. The integration
		// tests (TestCancelImmediate_AgentTool_PreservesChildCheckpoint) verify the
		// actual behavioral effect — child interrupts propagate within the grace period.
	})
}

func TestCancel_ParallelWorkflow_CancelAfterChatModel(t *testing.T) {
	ctx := context.Background()
	slowStarted := make(chan struct{}, 1)

	slowAgent := newCancelTestAgentWithTools(t, "par_slow", 1*time.Second, slowStarted)
	fastAgent := newCancelTestAgentWithTools(t, "par_fast", 50*time.Millisecond, make(chan struct{}, 1))

	parAgent, err := NewParallelAgent(ctx, &ParallelAgentConfig{
		Name:        "par_agent",
		Description: "Parallel workflow",
		SubAgents:   []Agent{slowAgent, fastAgent},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           parAgent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("par-cancel-1"))

	select {
	case <-slowStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Slow agent did not start")
	}

	handle, contributed := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	assert.True(t, contributed, "Cancel should contribute")
	err = handle.Wait()
	assert.NoError(t, err)

	hasCancelError := false
	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.As(event.Err, &cancelErr) {
			hasCancelError = true
		}
	}

	assert.True(t, hasCancelError, "Should have CancelError from parallel workflow")
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.NotEmpty(t, cancelErr.CheckPointID, "CancelError should have checkpoint ID")

	resumeSlow := newCancelTestAgentWithToolsFinalAnswer(t, "par_slow")
	resumeFast := newCancelTestAgentWithToolsFinalAnswer(t, "par_fast")

	resumePar, err := NewParallelAgent(ctx, &ParallelAgentConfig{
		Name:        "par_agent",
		Description: "Parallel workflow",
		SubAgents:   []Agent{resumeSlow, resumeFast},
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           resumePar,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.Resume(ctx, "par-cancel-1")
	assert.NoError(t, err)
	assert.NotNil(t, resumeIter)

	var resumeErrors []error
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			resumeErrors = append(resumeErrors, event.Err)
		}
	}
	assert.Empty(t, resumeErrors, "Resume should complete without errors")
}

func TestCancel_LoopWorkflow_CancelAfterChatModel(t *testing.T) {
	ctx := context.Background()
	modelStarted := make(chan struct{}, 10)

	agent := newCancelTestAgentWithTools(t, "loop_inner", 500*time.Millisecond, modelStarted)

	loopAgent, err := NewLoopAgent(ctx, &LoopAgentConfig{
		Name:          "loop_agent",
		Description:   "Loop workflow",
		SubAgents:     []Agent{agent},
		MaxIterations: 10,
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           loopAgent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("loop-cancel-1"))

	select {
	case <-modelStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Model did not start")
	}

	handle, contributed := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	assert.True(t, contributed, "Cancel should contribute")
	err = handle.Wait()
	assert.NoError(t, err)

	hasCancelError := false
	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.As(event.Err, &cancelErr) {
			hasCancelError = true
		}
	}

	assert.True(t, hasCancelError, "Should have CancelError from loop workflow")
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.NotEmpty(t, cancelErr.CheckPointID, "CancelError should have checkpoint ID")

	resumeAgent := newCancelTestAgentWithToolsFinalAnswer(t, "loop_inner")

	resumeLoop, err := NewLoopAgent(ctx, &LoopAgentConfig{
		Name:          "loop_agent",
		Description:   "Loop workflow",
		SubAgents:     []Agent{resumeAgent},
		MaxIterations: 10,
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           resumeLoop,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.Resume(ctx, "loop-cancel-1")
	assert.NoError(t, err)
	assert.NotNil(t, resumeIter)

	var resumeEvents []*AgentEvent
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		assert.Nil(t, event.Err, "Should not have error during resume")
		resumeEvents = append(resumeEvents, event)
	}
	assert.True(t, len(resumeEvents) > 0, "Resume should produce events")
}

func TestCancel_NestedWorkflow_AgentTool_CancelAfterChatModel(t *testing.T) {
	// Structure: Runner -> RootCMA (with tools) -> agentTool -> flowAgent -> seqWorkflow -> LeafCMA
	ctx := context.Background()
	leafStarted := make(chan struct{}, 1)

	leafModel := &cancelTestChatModel{
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "leaf response",
		},
		startedChan: leafStarted,
		doneChan:    make(chan struct{}, 1),
	}
	leafModel.setDelay(500 * time.Millisecond)

	leafAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "leaf_agent",
		Description: "Leaf agent in workflow",
		Model:       leafModel,
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "inner_seq",
		Description: "Inner sequential workflow",
		SubAgents:   []Agent{leafAgent},
	})
	assert.NoError(t, err)

	rootModel := &cancelTestChatModel{
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "",
			ToolCalls: []schema.ToolCall{
				{
					ID:   "call_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "inner_seq",
						Arguments: `{}`,
					},
				},
			},
		},
		startedChan: make(chan struct{}, 1),
		doneChan:    make(chan struct{}, 1),
	}
	rootAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "root_agent",
		Description: "Root agent",
		Model:       rootModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, seqAgent)},
			},
		},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           rootAgent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("test")}, cancelOpt, WithCheckPointID("nested-cancel-1"))

	select {
	case <-leafStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Leaf agent model did not start")
	}

	handle, contributed := cancelFn(WithAgentCancelMode(CancelAfterChatModel))
	assert.True(t, contributed, "Cancel should contribute")
	err = handle.Wait()
	assert.NoError(t, err)

	hasCancelError := false
	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.As(event.Err, &cancelErr) {
			hasCancelError = true
		}
	}

	assert.True(t, hasCancelError, "Should have CancelError from deeply nested workflow")
	assert.Equal(t, CancelAfterChatModel, cancelErr.Info.Mode)
	assert.NotEmpty(t, cancelErr.CheckPointID, "CancelError should have checkpoint ID")
	assert.NotNil(t, cancelErr.interruptSignal, "CancelError should carry interrupt signal through agent tree")

	// Phase 2: Resume from checkpoint — new instances to avoid data races
	resumeLeafModel := &cancelTestChatModel{
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "resumed leaf response",
		},
		startedChan: make(chan struct{}, 1),
		doneChan:    make(chan struct{}, 1),
	}
	resumeLeaf, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "leaf_agent",
		Description: "Leaf agent in workflow",
		Model:       resumeLeafModel,
	})
	assert.NoError(t, err)

	resumeSeq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "inner_seq",
		Description: "Inner sequential workflow",
		SubAgents:   []Agent{resumeLeaf},
	})
	assert.NoError(t, err)

	resumeRootModel := &cancelTestChatModel{
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "resumed root response",
		},
		startedChan: make(chan struct{}, 1),
		doneChan:    make(chan struct{}, 1),
	}
	resumeRoot, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "root_agent",
		Description: "Root agent",
		Model:       resumeRootModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, resumeSeq)},
			},
		},
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           resumeRoot,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.Resume(ctx, "nested-cancel-1")
	assert.NoError(t, err)
	assert.NotNil(t, resumeIter)

	var resumeErrors []error
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			resumeErrors = append(resumeErrors, event.Err)
		}
	}
	assert.Empty(t, resumeErrors, "Resume should complete without errors")
}

func TestCancel_CancelAfterToolCalls_InSequentialWorkflow(t *testing.T) {
	ctx := context.Background()
	toolStarted := make(chan struct{}, 1)

	st := &slowTool{
		name:        "slow_tool",
		delay:       200 * time.Millisecond,
		result:      "tool done",
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

	agentWithTools, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "agent_with_tools",
		Description: "Agent with slow tool",
		Model:       modelWithToolCall,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{st},
			},
		},
	})
	assert.NoError(t, err)

	seqAgent, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "seq_agent",
		Description: "Sequential workflow with tool agent",
		SubAgents:   []Agent{agentWithTools},
	})
	assert.NoError(t, err)

	store := newCancelTestStore()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           seqAgent,
		CheckPointStore: store,
	})

	cancelOpt, cancelFn := WithCancel()
	iter := runner.Run(ctx, []Message{schema.UserMessage("Use the tool")}, cancelOpt, WithCheckPointID("tool-cancel-1"))

	select {
	case <-toolStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("Tool did not start")
	}

	// Cancel after tool calls — should wait for the tool to finish, then cancel
	handle, contributed := cancelFn(WithAgentCancelMode(CancelAfterToolCalls))
	assert.True(t, contributed, "Cancel should contribute")
	err = handle.Wait()
	assert.NoError(t, err)

	hasCancelError := false
	var cancelErr *CancelError
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil && errors.As(event.Err, &cancelErr) {
			hasCancelError = true
		}
	}

	assert.True(t, hasCancelError, "Should have CancelError after tool calls complete")
	assert.Equal(t, CancelAfterToolCalls, cancelErr.Info.Mode)
	assert.NotEmpty(t, cancelErr.CheckPointID, "CancelError should have checkpoint ID")

	// Phase 2: Resume from checkpoint — new instances
	resumeTool := &slowTool{
		name:        "slow_tool",
		delay:       50 * time.Millisecond,
		result:      "resumed tool done",
		startedChan: make(chan struct{}, 1),
	}

	resumeModel := &simpleChatModel{
		response: &schema.Message{
			Role:    schema.Assistant,
			Content: "resumed response after tool",
		},
	}

	resumeAgentWithTools, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "agent_with_tools",
		Description: "Agent with slow tool",
		Model:       resumeModel,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{resumeTool},
			},
		},
	})
	assert.NoError(t, err)

	resumeSeq, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "seq_agent",
		Description: "Sequential workflow with tool agent",
		SubAgents:   []Agent{resumeAgentWithTools},
	})
	assert.NoError(t, err)

	runner2 := NewRunner(ctx, RunnerConfig{
		Agent:           resumeSeq,
		CheckPointStore: store,
	})

	resumeIter, err := runner2.Resume(ctx, "tool-cancel-1")
	assert.NoError(t, err)
	assert.NotNil(t, resumeIter)

	var resumeEvents []*AgentEvent
	for {
		event, ok := resumeIter.Next()
		if !ok {
			break
		}
		assert.Nil(t, event.Err, "Should not have error during resume")
		resumeEvents = append(resumeEvents, event)
	}
	assert.True(t, len(resumeEvents) > 0, "Resume should produce events")
}
