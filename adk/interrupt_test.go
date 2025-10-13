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
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
			intAct := Interrupt(ctx, data)
			intAct.Interrupted.Data = data
			generator.Send(&AgentEvent{
				Action: intAct,
			})
			generator.Close()
			return iter
		},
		resumer: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			wasInterrupted, hasState, _ := GetInterruptState[string](info, GetCurrentAddress(ctx))
			assert.True(t, wasInterrupted)
			assert.False(t, hasState)
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
	assert.Equal(t, "agent:myAgent", event.Action.Interrupted.InterruptContexts[0].ID)
	assert.True(t, event.Action.Interrupted.InterruptContexts[0].IsCause)
	assert.Equal(t, data, event.Action.Interrupted.InterruptContexts[0].Info)
	assert.Equal(t, Address{{Type: AddressSegmentAgent, ID: "myAgent"}},
		event.Action.Interrupted.InterruptContexts[0].Address)
	event, ok = iter.Next()
	assert.False(t, ok)

	_, err := runner.Resume(ctx, "1")
	assert.NoError(t, err)
}

func TestMultiAgentInterrupt(t *testing.T) {
	ctx := context.Background()
	sa1 := &myAgent{
		name: "sa1",
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			intAct := Interrupt(ctx, "hello world")
			intAct.Interrupted.Data = "hello world"
			generator.Send(&AgentEvent{
				AgentName: "sa2",
				Action:    intAct,
			})
			generator.Close()
			return iter
		},
		resumer: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.NotNil(t, info)
			assert.Equal(t, info.Data, "hello world")

			wasInterrupted, _, _ := GetInterruptState[any](info, GetCurrentAddress(ctx))
			assert.True(t, wasInterrupted)

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
	assert.Equal(t, 1, len(event.Action.Interrupted.InterruptContexts))
	assert.Equal(t, "hello world", event.Action.Interrupted.InterruptContexts[0].Info)
	assert.True(t, event.Action.Interrupted.InterruptContexts[0].IsCause)
	assert.Equal(t, Address{
		{Type: AddressSegmentAgent, ID: "sa1"},
		{Type: AddressSegmentAgent, ID: "sa2"},
	}, event.Action.Interrupted.InterruptContexts[0].Address)
	assert.Equal(t, "agent:sa1;agent:sa2", event.Action.Interrupted.InterruptContexts[0].ID)

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
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()

			intAct := Interrupt(ctx, "sa1 interrupt data")
			intAct.Interrupted.Data = "sa1 interrupt data"
			generator.Send(&AgentEvent{
				AgentName: "sa1",
				Action:    intAct,
			})
			generator.Close()
			return iter
		},
		resumer: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, info.Data, "sa1 interrupt data")
			wasInterrupted, _, _ := GetInterruptState[any](info, GetCurrentAddress(ctx))
			assert.True(t, wasInterrupted)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Close()
			return iter
		},
	} // interrupt once
	sa2 := &myAgent{
		name: "sa2",
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()

			intAct := Interrupt(ctx, "sa2 interrupt data")
			intAct.Interrupted.Data = "sa2 interrupt data"
			generator.Send(&AgentEvent{
				AgentName: "sa2",
				Action:    intAct,
			})
			generator.Close()
			return iter
		},
		resumer: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, info.Data, "sa2 interrupt data")
			wasInterrupted, _, _ := GetInterruptState[any](info, GetCurrentAddress(ctx))
			assert.True(t, wasInterrupted)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Close()
			return iter
		},
	} // interrupt once
	sa3 := &myAgent{
		name: "sa3",
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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

	firstInterruptEvent := &AgentEvent{
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
						InterruptContexts: []*InterruptCtx{
							{
								ID:   "agent:sequential;agent:sa1",
								Info: "sa1 interrupt data",
								Address: Address{
									{
										ID:   "sequential",
										Type: AddressSegmentAgent,
									},
									{
										ID:   "sa1",
										Type: AddressSegmentAgent,
									},
								},
								IsCause: true,
							},
						},
						interruptStates: []*interruptState{
							{
								Addr: Address{
									{
										ID:   "sequential",
										Type: AddressSegmentAgent,
									},
									{
										ID:   "sa1",
										Type: AddressSegmentAgent,
									},
								},
							},
						},
					},
					LoopIterations: 0,
				},
				InterruptContexts: []*InterruptCtx{
					{
						ID:   "agent:sequential",
						Info: "Sequential workflow interrupted",
						Address: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
						},
					},
					{
						ID:   "agent:sequential;agent:sa1",
						Info: "sa1 interrupt data",
						Address: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
							{
								ID:   "sa1",
								Type: AddressSegmentAgent,
							},
						},
						IsCause: true,
					},
				},
				interruptStates: []*interruptState{
					{
						Addr: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
							{
								ID:   "sa1",
								Type: AddressSegmentAgent,
							},
						},
					},
					{
						Addr: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
						},
						State: &sequentialWorkflowState{
							InterruptIndex: 0,
						},
					},
				},
			},
		},
	}
	secondInterruptEvent := &AgentEvent{
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
						InterruptContexts: []*InterruptCtx{
							{
								ID:   "agent:sequential;agent:sa2",
								Info: "sa2 interrupt data",
								Address: Address{
									{
										ID:   "sequential",
										Type: AddressSegmentAgent,
									},
									{
										ID:   "sa2",
										Type: AddressSegmentAgent,
									},
								},
								IsCause: true,
							},
						},
						interruptStates: []*interruptState{
							{
								Addr: Address{
									{
										ID:   "sequential",
										Type: AddressSegmentAgent,
									},
									{
										ID:   "sa2",
										Type: AddressSegmentAgent,
									},
								},
							},
						},
					},
					LoopIterations: 0,
				},
				InterruptContexts: []*InterruptCtx{
					{
						ID:   "agent:sequential",
						Info: "Sequential workflow interrupted",
						Address: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
						},
					},
					{
						ID:   "agent:sequential;agent:sa2",
						Info: "sa2 interrupt data",
						Address: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
							{
								ID:   "sa2",
								Type: AddressSegmentAgent,
							},
						},
						IsCause: true,
					},
				},
				interruptStates: []*interruptState{
					{
						Addr: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
							{
								ID:   "sa2",
								Type: AddressSegmentAgent,
							},
						},
					},
					{
						Addr: Address{
							{
								ID:   "sequential",
								Type: AddressSegmentAgent,
							},
						},
						State: &sequentialWorkflowState{
							InterruptIndex: 1,
						},
					},
				},
			},
		},
	}
	messageEvents := []*AgentEvent{
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

	t.Run("test sequential workflow agent", func(t *testing.T) {

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

		assert.Equal(t, 1, len(events))
		assert.Equal(t, firstInterruptEvent, events[0])
		events = []*AgentEvent{}

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

		assert.Equal(t, 1, len(events))
		assert.Equal(t, secondInterruptEvent, events[0])
		events = []*AgentEvent{}

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

		assert.Equal(t, 2, len(events))
		assert.Equal(t, messageEvents, events)
	})

	t.Run("test loop workflow agent", func(t *testing.T) {
		// loop
		a, err := NewLoopAgent(ctx, &LoopAgentConfig{
			Name:          "loop",
			SubAgents:     []Agent{sa1, sa2, sa3, sa4},
			MaxIterations: 2,
		})
		assert.NoError(t, err)
		runner := NewRunner(ctx, RunnerConfig{
			Agent:           a,
			CheckPointStore: newMyStore(),
		})
		var events []*AgentEvent
		iter := runner.Query(ctx, "hello world", WithCheckPointID("loop-1"))
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		loopFirstInterruptEvent := copyAgentEvent(firstInterruptEvent)
		loopFirstInterruptEvent.AgentName = "sa1"
		loopFirstInterruptEvent.RunPath = []RunStep{{"loop"}, {"sa1"}}
		loopFirstInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).LoopIterations = 0
		loopFirstInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].ID = "agent:loop;agent:sa1"
		loopFirstInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopFirstInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopFirstInterruptEvent.Action.Interrupted.InterruptContexts[0].ID = "agent:loop"
		loopFirstInterruptEvent.Action.Interrupted.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopFirstInterruptEvent.Action.Interrupted.InterruptContexts[0].Info = "Loop workflow interrupted"
		loopFirstInterruptEvent.Action.Interrupted.InterruptContexts[1].ID = "agent:loop;agent:sa1"
		loopFirstInterruptEvent.Action.Interrupted.InterruptContexts[1].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopFirstInterruptEvent.Action.Interrupted.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopFirstInterruptEvent.Action.Interrupted.interruptStates[1].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopFirstInterruptEvent.Action.Interrupted.interruptStates[1].State = &loopWorkflowState{LoopIterations: 0, SubAgentIndex: 0}
		assert.Equal(t, 1, len(events))
		assert.Equal(t, loopFirstInterruptEvent, events[0])
		events = []*AgentEvent{}

		// Resume after sa1 interrupt
		iter, err = runner.Resume(ctx, "loop-1")
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		loopSecondInterruptEvent := copyAgentEvent(secondInterruptEvent)
		loopSecondInterruptEvent.AgentName = "sa2"
		loopSecondInterruptEvent.RunPath = []RunStep{{"loop"}, {"sa1"}, {"sa2"}}
		loopSecondInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).LoopIterations = 0
		loopSecondInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].ID = "agent:loop;agent:sa2"
		loopSecondInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopSecondInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopSecondInterruptEvent.Action.Interrupted.InterruptContexts[0].ID = "agent:loop"
		loopSecondInterruptEvent.Action.Interrupted.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopSecondInterruptEvent.Action.Interrupted.InterruptContexts[0].Info = "Loop workflow interrupted"
		loopSecondInterruptEvent.Action.Interrupted.InterruptContexts[1].ID = "agent:loop;agent:sa2"
		loopSecondInterruptEvent.Action.Interrupted.InterruptContexts[1].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopSecondInterruptEvent.Action.Interrupted.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopSecondInterruptEvent.Action.Interrupted.interruptStates[1].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopSecondInterruptEvent.Action.Interrupted.interruptStates[1].State = &loopWorkflowState{LoopIterations: 0, SubAgentIndex: 1}
		assert.Equal(t, 1, len(events))
		assert.Equal(t, loopSecondInterruptEvent, events[0])
		events = []*AgentEvent{}

		// Resume after sa2 interrupt
		iter, err = runner.Resume(ctx, "loop-1")
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		loopThirdInterruptEvent := copyAgentEvent(firstInterruptEvent)
		loopThirdInterruptEvent.AgentName = "sa1"
		loopThirdInterruptEvent.RunPath = []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}, {"sa1"}}
		loopThirdInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).LoopIterations = 1
		loopThirdInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].ID = "agent:loop;agent:sa1"
		loopThirdInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopThirdInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopThirdInterruptEvent.Action.Interrupted.InterruptContexts[0].ID = "agent:loop"
		loopThirdInterruptEvent.Action.Interrupted.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopThirdInterruptEvent.Action.Interrupted.InterruptContexts[0].Info = "Loop workflow interrupted"
		loopThirdInterruptEvent.Action.Interrupted.InterruptContexts[1].ID = "agent:loop;agent:sa1"
		loopThirdInterruptEvent.Action.Interrupted.InterruptContexts[1].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopThirdInterruptEvent.Action.Interrupted.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa1", Type: AddressSegmentAgent}}
		loopThirdInterruptEvent.Action.Interrupted.interruptStates[1].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopThirdInterruptEvent.Action.Interrupted.interruptStates[1].State = &loopWorkflowState{LoopIterations: 1, SubAgentIndex: 0}

		loopFourthInterruptEvent := copyAgentEvent(secondInterruptEvent)
		loopFourthInterruptEvent.AgentName = "sa2"
		loopFourthInterruptEvent.RunPath = []RunStep{{"loop"}, {"sa1"}, {"sa2"}, {"sa3"}, {"sa4"}, {"sa1"}, {"sa2"}}
		loopFourthInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).LoopIterations = 1
		loopFourthInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].ID = "agent:loop;agent:sa2"
		loopFourthInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopFourthInterruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo).SequentialInterruptInfo.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopFourthInterruptEvent.Action.Interrupted.InterruptContexts[0].ID = "agent:loop"
		loopFourthInterruptEvent.Action.Interrupted.InterruptContexts[0].Address = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopFourthInterruptEvent.Action.Interrupted.InterruptContexts[0].Info = "Loop workflow interrupted"
		loopFourthInterruptEvent.Action.Interrupted.InterruptContexts[1].ID = "agent:loop;agent:sa2"
		loopFourthInterruptEvent.Action.Interrupted.InterruptContexts[1].Address = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopFourthInterruptEvent.Action.Interrupted.interruptStates[0].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}, {ID: "sa2", Type: AddressSegmentAgent}}
		loopFourthInterruptEvent.Action.Interrupted.interruptStates[1].Addr = Address{{ID: "loop", Type: AddressSegmentAgent}}
		loopFourthInterruptEvent.Action.Interrupted.interruptStates[1].State = &loopWorkflowState{LoopIterations: 1, SubAgentIndex: 1}

		loopMessageEvents := []*AgentEvent{
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
			loopThirdInterruptEvent,
		}
		assert.Equal(t, 3, len(events))
		assert.Equal(t, loopMessageEvents, events)
		events = []*AgentEvent{}

		// Resume after third interrupt
		iter, err = runner.Resume(ctx, "loop-1")
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}
		assert.Equal(t, 1, len(events))
		assert.Equal(t, loopFourthInterruptEvent, events[0])
		events = []*AgentEvent{}

		// Resume after fourth interrupt
		iter, err = runner.Resume(ctx, "loop-1")
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}
		loopFinalMessageEvents := []*AgentEvent{
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
		assert.Equal(t, 2, len(events))
		assert.Equal(t, loopFinalMessageEvents, events)
	})

	t.Run("test parallel workflow agent", func(t *testing.T) {
		// parallel
		a, err := NewParallelAgent(ctx, &ParallelAgentConfig{
			Name:      "parallel agent",
			SubAgents: []Agent{sa1, sa2, sa3, sa4},
		})
		assert.NoError(t, err)
		runner := NewRunner(ctx, RunnerConfig{
			Agent:           a,
			CheckPointStore: newMyStore(),
		})
		iter := runner.Query(ctx, "hello world", WithCheckPointID("1"))
		var (
			events         []*AgentEvent
			interruptEvent *AgentEvent
		)

		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			if event.Action != nil && event.Action.Interrupted != nil {
				interruptEvent = event
				continue
			}
			events = append(events, event)
		}
		assert.Equal(t, 2, len(events))

		normalEvents := make([]*AgentEvent, 0, len(messageEvents))
		for _, event := range messageEvents {
			copied := copyAgentEvent(event)
			copied.RunPath = []RunStep{{"parallel agent"}, {copied.AgentName}}
			normalEvents = append(normalEvents, copied)
		}

		assert.Contains(t, events, normalEvents[0])
		assert.Contains(t, events, normalEvents[1])

		assert.NotNil(t, interruptEvent)
		assert.Equal(t, "parallel agent", interruptEvent.AgentName)
		assert.Equal(t, []RunStep{{"parallel agent"}}, interruptEvent.RunPath)
		assert.NotNil(t, interruptEvent.Action.Interrupted)
		wii, ok := interruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo)
		assert.True(t, ok)
		assert.Equal(t, 2, len(wii.ParallelInterruptInfo))

		var sa1Found, sa2Found bool
		for _, info := range wii.ParallelInterruptInfo {
			if info.InterruptContexts[0].Info == "sa1 interrupt data" {
				sa1Found = true
			} else if info.InterruptContexts[0].Info == "sa2 interrupt data" {
				sa2Found = true
			}
		}
		assert.True(t, sa1Found)
		assert.True(t, sa2Found)

		assert.Equal(t, 3, len(interruptEvent.Action.Interrupted.InterruptContexts))
		assert.Equal(t, "Parallel workflow interrupted", interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
		assert.Equal(t, 3, len(interruptEvent.Action.Interrupted.interruptStates))

		iter, err = runner.Resume(ctx, "1")
		assert.NoError(t, err)
		_, ok = iter.Next()
		assert.False(t, ok)
	})
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
	assert.Equal(t, 4, len(event.Action.Interrupted.InterruptContexts))

	var hasRootCause bool
	for _, ctx := range event.Action.Interrupted.InterruptContexts {
		if ctx.IsCause {
			hasRootCause = true
			assert.Equal(t, Address{
				{Type: AddressSegmentAgent, ID: "name"},
				{Type: compose.AddressSegmentRunnable, ID: "React"},
				{Type: compose.AddressSegmentNode, ID: "ToolNode"},
				{Type: compose.AddressSegmentTool, ID: "1"},
			}, ctx.Address)
		}
	}
	assert.True(t, hasRootCause)
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
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				Action: &AgentAction{Interrupted: &InterruptInfo{
					Data: "hello world",
				}},
			})
			generator.Close()
			return iter
		},
		resumer: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
}

func withResume() AgentRunOption {
	return WrapImplSpecificOptFn(func(t *myAgentOptions) {
		t.interrupt = true
	})
}

type myAgent struct {
	name    string
	runner  func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent]
	resumer func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent]
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
	return m.runner(ctx, input, options...)
}

func (m *myAgent) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	return m.resumer(ctx, info, opts...)
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

type myTool1 struct{}

func (m *myTool1) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "tool1",
		Desc: "desc",
	}, nil
}

func (m *myTool1) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if wasInterrupted, _, _ := compose.GetInterruptState[any](ctx); !wasInterrupted {
		return "", compose.Interrupt(ctx, nil)
	}

	return "result", nil
}

// Add this test case after the existing TestWorkflowInterrupt function
func TestWorkflowInterruptInvalidDataType(t *testing.T) {
	ctx := context.Background()

	// Create a simple workflow agent
	sa1 := &myAgent{
		name: "sa1",
		runner: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
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
