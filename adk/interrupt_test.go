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
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestSaveAgentEventWrapper(t *testing.T) {
	sr, sw := schema.Pipe[Message](1)
	sw.Send(schema.UserMessage("test"), nil)
	sw.Close()
	sr = sr.Copy(2)[1]

	w := &agentEventWrapper{
		AgentEvent: &AgentEvent{
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					IsStreaming:   true,
					MessageStream: sr,
				},
			},
			RunPath: []RunStep{
				{
					"a1",
				},
				{
					"a2",
				},
			},
		},
		mu:                  sync.Mutex{},
		concatenatedMessage: nil,
	}

	_, err := getMessageFromWrappedEvent(w)
	assert.NoError(t, err)

	buf, err := w.GobEncode()
	assert.NoError(t, err)
	assert.NoError(t, err)

	w1 := &agentEventWrapper{}
	err = w1.GobDecode(buf)
	assert.NoError(t, err)
}

func TestSimpleInterrupt(t *testing.T) {
	data := "hello world"
	agent := &myAgent{
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						IsStreaming: true,
						Message:     nil,
						MessageStream: schema.StreamReaderFromArray([]Message{
							schema.UserMessage("hello "),
							schema.UserMessage("world"),
						}),
					},
				},
			})
			generator.Send(&AgentEvent{
				Action: &AgentAction{Interrupted: &InterruptInfo{
					Data: data,
				}},
			})
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.NotNil(t, info)
			assert.True(t, info.EnableStreaming)
			assert.Equal(t, data, info.Data)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Close()
			return iter
		},
	}
	store := newMyStore()
	ctx := context.Background()
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: store,
	})
	iter := runner.Query(ctx, "hello world", WithCheckPointID("1"))
	event, ok := iter.Next()
	assert.True(t, ok)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.Equal(t, data, event.Action.Interrupted.Data)
	_, ok = iter.Next()
	assert.False(t, ok)

	iter, err := runner.Resume(ctx, "1")
	assert.NoError(t, err)
	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestMultiAgentInterrupt(t *testing.T) {
	ctx := context.Background()
	sa1 := &myAgent{
		name: "sa1",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa1",
				Action: &AgentAction{
					TransferToAgent: &TransferToAgentAction{
						DestAgentName: "sa2",
					},
				},
			})
			generator.Close()
			return iter
		},
	}
	sa2 := &myAgent{
		name: "sa2",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa2",
				Action: &AgentAction{
					Interrupted: &InterruptInfo{
						Data: "hello world",
					},
				},
			})
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.NotNil(t, info)
			assert.Equal(t, info.Data, "hello world")
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa2",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: schema.UserMessage("completed")},
				},
			})
			generator.Close()
			return iter
		},
	}
	a, err := SetSubAgents(ctx, sa1, []Agent{sa2})
	assert.NoError(t, err)
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           a,
		EnableStreaming: false,
		CheckPointStore: newMyStore(),
	})
	iter := runner.Query(ctx, "", WithCheckPointID("1"))
	event, ok := iter.Next()
	assert.True(t, ok)
	assert.NotNil(t, event.Action.TransferToAgent)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NotNil(t, event.Action.Interrupted)
	_, ok = iter.Next()
	assert.False(t, ok)
	iter, err = runner.Resume(ctx, "1")
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "completed")
	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestWorkflowInterrupt(t *testing.T) {
	ctx := context.Background()
	sa1 := &myAgent{
		name: "sa1",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa1",
				Action: &AgentAction{
					Interrupted: &InterruptInfo{
						Data: "sa1 interrupt data",
					},
				},
			})
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, info.Data, "sa1 interrupt data")
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Close()
			return iter
		},
	} // interrupt once
	sa2 := &myAgent{
		name: "sa2",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa2",
				Action: &AgentAction{
					Interrupted: &InterruptInfo{
						Data: "sa2 interrupt data",
					},
				},
			})
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, info.Data, "sa2 interrupt data")
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Close()
			return iter
		},
	} // interrupt once
	sa3 := &myAgent{
		name: "sa3",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa3",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						Message: schema.UserMessage("sa3 completed"),
					},
				},
			})
			generator.Close()
			return iter
		},
	} // won't interrupt
	sa4 := &myAgent{
		name: "sa4",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa4",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						Message: schema.UserMessage("sa4 completed"),
					},
				},
			})
			generator.Close()
			return iter
		},
	} // won't interrupt

	// sequential
	a, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "sequential",
		Description: "sequential agent",
		SubAgents:   []Agent{sa1, sa2, sa3, sa4},
	})
	assert.NoError(t, err)
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           a,
		CheckPointStore: newMyStore(),
	})
	var events []*AgentEvent
	iter := runner.Query(ctx, "hello world", WithCheckPointID("sequential-1"))
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	// Resume after sa1 interrupt
	iter, err = runner.Resume(ctx, "sequential-1")
	assert.NoError(t, err)
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	// Resume after sa2 interrupt
	iter, err = runner.Resume(ctx, "sequential-1")
	assert.NoError(t, err)
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	expectedSequentialEvents := []*AgentEvent{
		{
			AgentName: "sa1",
			RunPath:   []RunStep{{"sequential"}, {"sa1"}},
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput: &AgentInput{
							Messages: []Message{schema.UserMessage("hello world")},
						},
						SequentialInterruptIndex: 0,
						SequentialInterruptInfo: &InterruptInfo{
							Data: "sa1 interrupt data",
						},
						LoopIterations: 0,
					},
				},
			},
		},
		{
			AgentName: "sa2",
			RunPath:   []RunStep{{"sequential"}, {"sa1"}, {"sa2"}},
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput: &AgentInput{
							Messages: []Message{schema.UserMessage("hello world")},
						},
						SequentialInterruptIndex: 1,
						SequentialInterruptInfo: &InterruptInfo{
							Data: "sa2 interrupt data",
						},
						LoopIterations: 0,
					},
				},
			},
		},
		{
			AgentName: "sa3",
			RunPath:   []RunStep{{"sequential"}, {"sa1"}, {"sa2"}, {"sa3"}},
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.UserMessage("sa3 completed"),
				},
			},
		},
		{
			AgentName: "sa4",
			RunPath:   []RunStep{{"sequential"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}},
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.UserMessage("sa4 completed"),
				},
			},
		},
	}

	assert.Equal(t, 4, len(events))
	assert.Equal(t, expectedSequentialEvents, events)

	// loop
	a, err = NewLoopAgent(ctx, &LoopAgentConfig{
		Name:          "loop",
		SubAgents:     []Agent{sa1, sa2, sa3, sa4},
		MaxIterations: 2,
	})
	assert.NoError(t, err)
	runner = NewRunner(ctx, RunnerConfig{
		Agent:           a,
		CheckPointStore: newMyStore(),
	})
	events = []*AgentEvent{}
	iter = runner.Query(ctx, "hello world", WithCheckPointID("1"))
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	for i := 0; i < 4; i++ {
		iter, err = runner.Resume(ctx, "1")
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}
	}
	expectedEvents := []*AgentEvent{
		{
			AgentName: "sa1",
			RunPath:   []RunStep{{"loop"}, {"sa1"}},
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput: &AgentInput{
							Messages: []Message{schema.UserMessage("hello world")},
						},
						SequentialInterruptIndex: 0,
						SequentialInterruptInfo: &InterruptInfo{
							Data: "sa1 interrupt data",
						},
						LoopIterations: 0,
					},
				},
			},
		},
		{
			AgentName: "sa2",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}},
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput: &AgentInput{
							Messages: []Message{schema.UserMessage("hello world")},
						},
						SequentialInterruptIndex: 1,
						SequentialInterruptInfo: &InterruptInfo{
							Data: "sa2 interrupt data",
						},
						LoopIterations: 0,
					},
				},
			},
		},
		{
			AgentName: "sa3",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}},
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.UserMessage("sa3 completed"),
				},
			},
		},
		{
			AgentName: "sa4",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}},
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.UserMessage("sa4 completed"),
				},
			},
		},
		{
			AgentName: "sa1",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}, {"sa1"}},
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput: &AgentInput{
							Messages: []Message{schema.UserMessage("hello world")},
						},
						SequentialInterruptIndex: 0,
						SequentialInterruptInfo: &InterruptInfo{
							Data: "sa1 interrupt data",
						},
						LoopIterations: 1,
					},
				},
			},
		},
		{
			AgentName: "sa2",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}, {"sa1"}, {"sa2"}},
			Action: &AgentAction{
				Interrupted: &InterruptInfo{
					Data: &WorkflowInterruptInfo{
						OrigInput: &AgentInput{
							Messages: []Message{schema.UserMessage("hello world")},
						},
						SequentialInterruptIndex: 1,
						SequentialInterruptInfo: &InterruptInfo{
							Data: "sa2 interrupt data",
						},
						LoopIterations: 1,
					},
				},
			},
		},
		{
			AgentName: "sa3",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}, {"sa1"}, {"sa2"}, {"sa3"}},
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.UserMessage("sa3 completed"),
				},
			},
		},
		{
			AgentName: "sa4",
			RunPath:   []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}},
			Output: &AgentOutput{
				MessageOutput: &MessageVariant{
					Message: schema.UserMessage("sa4 completed"),
				},
			},
		},
	}

	assert.Equal(t, 8, len(events))
	assert.Equal(t, expectedEvents, events)

	// parallel
	a, err = NewParallelAgent(ctx, &ParallelAgentConfig{
		Name:      "parallel agent",
		SubAgents: []Agent{sa1, sa2, sa3, sa4},
	})
	assert.NoError(t, err)
	runner = NewRunner(ctx, RunnerConfig{
		Agent:           a,
		CheckPointStore: newMyStore(),
	})
	iter = runner.Query(ctx, "hello world", WithCheckPointID("1"))
	events = []*AgentEvent{}

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	assert.Equal(t, 3, len(events))

	iter, err = runner.Resume(ctx, "1")
	assert.NoError(t, err)
	_, ok := iter.Next()
	assert.False(t, ok)
}

func TestChatModelInterrupt(t *testing.T) {
	ctx := context.Background()
	a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "name",
		Description: "description",
		Instruction: "instruction",
		Model: &myModel{
			validator: func(i int, messages []*schema.Message) bool {
				if i > 0 && (len(messages) != 4 || messages[2].Content != "new user message") {
					return false
				}
				return true
			},
			messages: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID: "1",
						Function: schema.FunctionCall{
							Name:      "tool1",
							Arguments: "arguments",
						},
					},
				}),
				schema.AssistantMessage("completed", nil),
			},
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{&myTool1{}},
			},
		},
	})
	assert.NoError(t, err)
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           a,
		CheckPointStore: newMyStore(),
	})
	iter := runner.Query(ctx, "hello world", WithCheckPointID("1"))
	event, ok := iter.Next()
	assert.True(t, ok)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.NotNil(t, event.Action.Interrupted)
	event, ok = iter.Next()
	assert.False(t, ok)

	iter, err = runner.Resume(ctx, "1", WithHistoryModifier(func(ctx context.Context, messages []Message) []Message {
		messages[2].Content = "new user message"
		return messages
	}))
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "result")
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "completed")
}

func TestChatModelAgentToolInterrupt(t *testing.T) {
	sa := &myAgent{
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				Action: &AgentAction{Interrupted: &InterruptInfo{
					Data: "hello world",
				}},
			})
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.NotNil(t, info)
			assert.False(t, info.EnableStreaming)
			assert.Equal(t, "hello world", info.Data)

			o := GetImplSpecificOptions[myAgentOptions](nil, opts...)
			if o.interrupt {
				iter, generator := NewAsyncIteratorPair[*AgentEvent]()
				generator.Send(&AgentEvent{
					Action: &AgentAction{Interrupted: &InterruptInfo{
						Data: "hello world",
					}},
				})
				generator.Close()
				return iter
			}

			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.UserMessage("my agent completed")}}})
			generator.Close()
			return iter
		},
	}
	ctx := context.Background()
	a, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "name",
		Description: "description",
		Instruction: "instruction",
		Model: &myModel{
			messages: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{
						ID: "1",
						Function: schema.FunctionCall{
							Name:      "myAgent",
							Arguments: "{\"request\":\"123\"}",
						},
					},
				}),
				schema.AssistantMessage("completed", nil),
			},
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{NewAgentTool(ctx, sa)},
			},
		},
	})
	assert.NoError(t, err)
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           a,
		CheckPointStore: newMyStore(),
	})

	iter := runner.Query(ctx, "hello world", WithCheckPointID("1"))
	event, ok := iter.Next()
	assert.True(t, ok)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.NotNil(t, event.Action.Interrupted)
	event, ok = iter.Next()
	assert.False(t, ok)

	iter, err = runner.Resume(ctx, "1", WithAgentToolRunOptions(map[string][]AgentRunOption{
		"myAgent": {withResume()},
	}))
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.NotNil(t, event.Action.Interrupted)
	event, ok = iter.Next()
	assert.False(t, ok)
	iter, err = runner.Resume(ctx, "1")
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "my agent completed")
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "completed")
	_, ok = iter.Next()
	assert.False(t, ok)
}

func newMyStore() *myStore {
	return &myStore{
		m: map[string][]byte{},
	}
}

type myStore struct {
	m map[string][]byte
}

func (m *myStore) Set(ctx context.Context, key string, value []byte) error {
	m.m[key] = value
	return nil
}

func (m *myStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	v, ok := m.m[key]
	return v, ok, nil
}

type myAgentOptions struct {
	interrupt bool

	value string
}

func withResume() AgentRunOption {
	return WrapImplSpecificOptFn(func(t *myAgentOptions) {
		t.interrupt = true
	})
}

func withValue(value string) AgentRunOption {
	return WrapImplSpecificOptFn(func(t *myAgentOptions) {
		t.value = value
	})
}

type myAgent struct {
	name     string
	runFn    func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
	resumeFn func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent]
}

func (m *myAgent) Name(ctx context.Context) string {
	if len(m.name) > 0 {
		return m.name
	}
	return "myAgent"
}

func (m *myAgent) Description(ctx context.Context) string {
	return "myAgent description"
}

func (m *myAgent) Run(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return m.runFn(ctx, input, options...)
}

func (m *myAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return m.resumeFn(ctx, info, opts...)
}

type myModel struct {
	times     int
	messages  []*schema.Message
	validator func(int, []*schema.Message) bool
}

func (m *myModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if m.validator != nil && !m.validator(m.times, input) {
		return nil, errors.New("invalid input")
	}
	if m.times >= len(m.messages) {
		return nil, errors.New("exceeded max number of messages")
	}
	t := m.times
	m.times++
	return m.messages[t], nil
}

func (m *myModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("implement me")
}

func (m *myModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type myTool1 struct {
	times int
}

func (m *myTool1) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "tool1",
		Desc: "desc",
	}, nil
}

func (m *myTool1) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if m.times == 0 {
		m.times = 1
		return "", compose.InterruptAndRerun
	}
	return "result", nil
}

// Add this test case after the existing TestWorkflowInterrupt function
func TestWorkflowInterruptInvalidDataType(t *testing.T) {
	ctx := context.Background()

	// Create a simple workflow agent
	sa1 := &myAgent{
		name: "sa1",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa1",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						Message: schema.UserMessage("completed"),
					},
				},
			})
			generator.Close()
			return iter
		},
	}

	a, err := NewSequentialAgent(ctx, &SequentialAgentConfig{
		Name:        "sequential",
		Description: "sequential agent",
		SubAgents:   []Agent{sa1},
	})
	assert.NoError(t, err)

	// Cast to workflowAgent to access Resume method directly
	workflowAgent := a.(*flowAgent).Agent.(*workflowAgent)

	// Create ResumeInfo with invalid Data type (not *WorkflowInterruptInfo)
	resumeInfo := &ResumeInfo{
		EnableStreaming: false,
		InterruptInfo: &InterruptInfo{
			Data: "invalid data type", // This should be *WorkflowInterruptInfo but we pass string
		},
	}

	// Call Resume method directly to trigger the error path
	iter := workflowAgent.Resume(ctx, resumeInfo)

	// Verify that an error event is generated
	event, ok := iter.Next()
	assert.True(t, ok)
	assert.NotNil(t, event.Err)
	assert.Contains(t, event.Err.Error(), "type of InterruptInfo.Data is expected to")
	assert.Contains(t, event.Err.Error(), "actual: string")

	// Verify no more events
	_, ok = iter.Next()
	assert.False(t, ok)
}
