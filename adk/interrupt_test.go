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
	"fmt"
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

func TestInterruptFunctionsPopulateInterruptContextsImmediately(t *testing.T) {
	ctx := context.Background()
	ctx, _ = initRunCtx(ctx, "TestAgent", &AgentInput{Messages: []Message{}})
	ctx = AppendAddressSegment(ctx, AddressSegmentAgent, "TestAgent")

	t.Run("Interrupt populates InterruptContexts", func(t *testing.T) {
		event := Interrupt(ctx, "test info")
		assert.NotNil(t, event.Action)
		assert.NotNil(t, event.Action.Interrupted)
		assert.NotNil(t, event.Action.Interrupted.InterruptContexts)
		assert.Equal(t, 1, len(event.Action.Interrupted.InterruptContexts))
		assert.Equal(t, "test info", event.Action.Interrupted.InterruptContexts[0].Info)
		assert.True(t, event.Action.Interrupted.InterruptContexts[0].IsRootCause)
		assert.Equal(t, Address{
			{Type: AddressSegmentAgent, ID: "TestAgent"},
		}, event.Action.Interrupted.InterruptContexts[0].Address)
	})

	t.Run("StatefulInterrupt populates InterruptContexts", func(t *testing.T) {
		event := StatefulInterrupt(ctx, "stateful info", "my state")
		assert.NotNil(t, event.Action)
		assert.NotNil(t, event.Action.Interrupted)
		assert.NotNil(t, event.Action.Interrupted.InterruptContexts)
		assert.Equal(t, 1, len(event.Action.Interrupted.InterruptContexts))
		assert.Equal(t, "stateful info", event.Action.Interrupted.InterruptContexts[0].Info)
		assert.True(t, event.Action.Interrupted.InterruptContexts[0].IsRootCause)
	})

	t.Run("CompositeInterrupt populates InterruptContexts with filtered parent chain", func(t *testing.T) {
		subCtx := AppendAddressSegment(ctx, AddressSegmentAgent, "SubAgent")
		subEvent := Interrupt(subCtx, "sub info")
		event := CompositeInterrupt(ctx, "composite info", "composite state", subEvent.Action.internalInterrupted)
		assert.NotNil(t, event.Action)
		assert.NotNil(t, event.Action.Interrupted)
		assert.NotNil(t, event.Action.Interrupted.InterruptContexts)
		assert.Equal(t, 1, len(event.Action.Interrupted.InterruptContexts))

		rootCause := event.Action.Interrupted.InterruptContexts[0]
		assert.Equal(t, "sub info", rootCause.Info)
		assert.True(t, rootCause.IsRootCause)
		assert.Equal(t, Address{
			{Type: AddressSegmentAgent, ID: "TestAgent"},
			{Type: AddressSegmentAgent, ID: "SubAgent"},
		}, rootCause.Address)

		assert.NotNil(t, rootCause.Parent, "Parent should not be nil for composite interrupt")
		assert.Equal(t, "composite info", rootCause.Parent.Info)
		assert.Equal(t, Address{
			{Type: AddressSegmentAgent, ID: "TestAgent"},
		}, rootCause.Parent.Address)
	})

	t.Run("Address only contains agent/tool segments", func(t *testing.T) {
		event := Interrupt(ctx, "test info")
		addr := event.Action.Interrupted.InterruptContexts[0].Address
		for _, seg := range addr {
			assert.True(t, seg.Type == AddressSegmentAgent || seg.Type == AddressSegmentTool,
				"Address should only contain agent/tool segments, got: %s", seg.Type)
		}
	})
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
			intEvent := Interrupt(ctx, data)
			intEvent.Action.Interrupted.Data = data
			generator.Send(intEvent)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.True(t, info.WasInterrupted)
			assert.Nil(t, info.InterruptState)
			assert.True(t, info.EnableStreaming)
			assert.Equal(t, data, info.Data)

			assert.True(t, info.IsResumeTarget)
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
	_, ok := iter.Next()
	assert.True(t, ok)
	interruptEvent, ok := iter.Next()
	assert.True(t, ok)
	assert.Equal(t, data, interruptEvent.Action.Interrupted.Data)
	assert.NotEmpty(t, interruptEvent.Action.Interrupted.InterruptContexts[0].ID)
	assert.True(t, interruptEvent.Action.Interrupted.InterruptContexts[0].IsRootCause)
	assert.Equal(t, data, interruptEvent.Action.Interrupted.InterruptContexts[0].Info)
	assert.Equal(t, Address{{Type: AddressSegmentAgent, ID: "myAgent"}},
		interruptEvent.Action.Interrupted.InterruptContexts[0].Address)
	_, ok = iter.Next()
	assert.False(t, ok)

	iter, err := runner.ResumeWithParams(ctx, "1", &ResumeParams{
		Targets: map[string]any{
			interruptEvent.Action.Interrupted.InterruptContexts[0].ID: nil,
		},
	})
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
			intEvent := StatefulInterrupt(ctx, "hello world", "temp state")
			intEvent.Action.Interrupted.Data = "hello world"
			generator.Send(intEvent)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.NotNil(t, info)
			assert.Equal(t, info.Data, "hello world")

			assert.True(t, info.WasInterrupted)
			assert.NotNil(t, info.InterruptState)
			assert.Equal(t, "temp state", info.InterruptState)

			assert.True(t, info.IsResumeTarget)
			assert.NotNil(t, info.ResumeData)
			assert.Equal(t, "resume data", info.ResumeData)

			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "sa2",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: schema.UserMessage(info.ResumeData.(string))},
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
	assert.True(t, event.Action.Interrupted.InterruptContexts[0].IsRootCause)
	assert.Equal(t, Address{
		{Type: AddressSegmentAgent, ID: "sa1"},
		{Type: AddressSegmentAgent, ID: "sa2"},
	}, event.Action.Interrupted.InterruptContexts[0].Address)
	assert.NotEmpty(t, event.Action.Interrupted.InterruptContexts[0].ID)

	interruptID := event.Action.Interrupted.InterruptContexts[0].ID
	_, ok = iter.Next()
	assert.False(t, ok)

	iter, err = runner.ResumeWithParams(ctx, "1", &ResumeParams{
		Targets: map[string]any{
			interruptID: "resume data",
		},
	})
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "resume data")
	_, ok = iter.Next()
	assert.False(t, ok)
}

func TestWorkflowInterrupt(t *testing.T) {
	ctx := context.Background()
	sa1 := &myAgent{
		name: "sa1",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()

			intEvent := Interrupt(ctx, "sa1 interrupt data")
			intEvent.Action.Interrupted.Data = "sa1 interrupt data"
			generator.Send(intEvent)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, info.InterruptInfo.Data, "sa1 interrupt data")
			assert.True(t, info.WasInterrupted)
			assert.Nil(t, info.InterruptState)
			assert.True(t, info.IsResumeTarget)
			assert.Equal(t, "resume sa1", info.ResumeData)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Close()
			return iter
		},
	} // interrupt once
	sa2 := &myAgent{
		name: "sa2",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()

			intEvent := StatefulInterrupt(ctx, "sa2 interrupt data", "sa2 interrupt")
			intEvent.Action.Interrupted.Data = "sa2 interrupt data"
			generator.Send(intEvent)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.Equal(t, info.InterruptInfo.Data, "sa2 interrupt data")
			assert.True(t, info.WasInterrupted)
			assert.NotNil(t, info.InterruptState)
			assert.Equal(t, "sa2 interrupt", info.InterruptState)

			assert.True(t, info.IsResumeTarget)
			assert.NotNil(t, info.ResumeData)
			assert.Equal(t, "resume sa2", info.ResumeData)
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
					},
					LoopIterations: 0,
				},
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
						IsRootCause: true,
						Parent: &InterruptCtx{
							ID:   "agent:sequential",
							Info: "Sequential workflow interrupted",
							Address: Address{
								{
									ID:   "sequential",
									Type: AddressSegmentAgent,
								},
							},
						},
					},
				},
			},
		},
	}
	_ = firstInterruptEvent
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
					},
				},
				InterruptContexts: []*InterruptCtx{
					{
						ID:   "agent:sequential;agent:sa1;agent:sa2",
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
						IsRootCause: true,
						Parent: &InterruptCtx{
							ID:   "agent:sequential",
							Info: "Sequential workflow interrupted",
							Address: Address{
								{
									ID:   "sequential",
									Type: AddressSegmentAgent,
								},
							},
						},
					},
				},
			},
		},
	}
	_ = secondInterruptEvent
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
	_ = messageEvents

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
		assert.Equal(t, firstInterruptEvent.AgentName, events[0].AgentName)
		assert.Equal(t, firstInterruptEvent.RunPath, events[0].RunPath)
		assert.True(t, events[0].Action.Interrupted.InterruptContexts[0].EqualsWithoutID(firstInterruptEvent.Action.Interrupted.InterruptContexts[0]))
		interruptID1 := events[0].Action.Interrupted.InterruptContexts[0].ID
		events = []*AgentEvent{}

		// Resume after sa1 interrupt
		iter, err = runner.ResumeWithParams(ctx, "sequential-1", &ResumeParams{
			Targets: map[string]any{
				interruptID1: "resume sa1",
			},
		})
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		assert.Equal(t, 1, len(events))
		assert.Equal(t, secondInterruptEvent.AgentName, events[0].AgentName)
		assert.Equal(t, secondInterruptEvent.RunPath, events[0].RunPath)
		assert.True(t, events[0].Action.Interrupted.InterruptContexts[0].
			EqualsWithoutID(secondInterruptEvent.Action.Interrupted.InterruptContexts[0]))
		interruptID2 := events[0].Action.Interrupted.InterruptContexts[0].ID
		events = []*AgentEvent{}

		// Resume after sa2 interrupt
		iter, err = runner.ResumeWithParams(ctx, "sequential-1", &ResumeParams{
			Targets: map[string]any{
				interruptID2: "resume sa2",
			},
		})
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

		loopFirstInterruptEvent := &AgentEvent{
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
					InterruptContexts: []*InterruptCtx{
						{
							ID:   "agent:loop;agent:sa1",
							Info: "sa1 interrupt data",
							Address: Address{
								{
									ID:   "loop",
									Type: AddressSegmentAgent,
								},
								{
									ID:   "sa1",
									Type: AddressSegmentAgent,
								},
							},
							IsRootCause: true,
							Parent: &InterruptCtx{
								ID:   "agent:loop",
								Info: "Loop workflow interrupted",
								Address: Address{
									{
										ID:   "loop",
										Type: AddressSegmentAgent,
									},
								},
							},
						},
					},
				},
			},
		}
		assert.Equal(t, 1, len(events))
		assert.Equal(t, loopFirstInterruptEvent.AgentName, events[0].AgentName)
		assert.Equal(t, loopFirstInterruptEvent.RunPath, events[0].RunPath)
		assert.True(t, events[0].Action.Interrupted.InterruptContexts[0].EqualsWithoutID(loopFirstInterruptEvent.Action.Interrupted.InterruptContexts[0]))
		loopInterruptID1 := events[0].Action.Interrupted.InterruptContexts[0].ID
		events = []*AgentEvent{}

		// Resume after sa1 interrupt
		iter, err = runner.ResumeWithParams(ctx, "loop-1", &ResumeParams{
			Targets: map[string]any{
				loopInterruptID1: "resume sa1",
			},
		})
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		loopSecondInterruptEvent := &AgentEvent{
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
					InterruptContexts: []*InterruptCtx{
						{
							ID:   "agent:loop;agent:sa1;agent:sa2",
							Info: "sa2 interrupt data",
							Address: Address{
								{
									ID:   "loop",
									Type: AddressSegmentAgent,
								},
								{
									ID:   "sa2",
									Type: AddressSegmentAgent,
								},
							},
							IsRootCause: true,
							Parent: &InterruptCtx{
								ID:   "agent:loop",
								Info: "Loop workflow interrupted",
								Address: Address{
									{
										ID:   "loop",
										Type: AddressSegmentAgent,
									},
								},
							},
						},
					},
				},
			},
		}
		assert.Equal(t, 1, len(events))
		assert.Equal(t, loopSecondInterruptEvent.AgentName, events[0].AgentName)
		assert.Equal(t, loopSecondInterruptEvent.RunPath, events[0].RunPath)
		assert.True(t, events[0].Action.Interrupted.InterruptContexts[0].EqualsWithoutID(loopSecondInterruptEvent.Action.Interrupted.InterruptContexts[0]))
		loopInterruptID2 := events[0].Action.Interrupted.InterruptContexts[0].ID
		events = []*AgentEvent{}

		// Resume after sa2 interrupt
		iter, err = runner.ResumeWithParams(ctx, "loop-1", &ResumeParams{
			Targets: map[string]any{
				loopInterruptID2: "resume sa2",
			},
		})
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}

		loopThirdInterruptEvent := &AgentEvent{
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
					InterruptContexts: []*InterruptCtx{
						{
							ID:   "agent:loop;agent:sa1;agent:sa2;agent:sa3;agent:sa4;agent:sa1",
							Info: "sa1 interrupt data",
							Address: Address{
								{
									ID:   "loop",
									Type: AddressSegmentAgent,
								},
								{
									ID:   "sa1",
									Type: AddressSegmentAgent,
								},
							},
							IsRootCause: true,
							Parent: &InterruptCtx{
								ID:   "agent:loop",
								Info: "Loop workflow interrupted",
								Address: Address{
									{
										ID:   "loop",
										Type: AddressSegmentAgent,
									},
								},
							},
						},
					},
				},
			},
		}

		loopFourthInterruptEvent := &AgentEvent{
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
					InterruptContexts: []*InterruptCtx{
						{
							ID:   "agent:loop;agent:sa1;agent:sa2;agent:sa3;agent:sa4;agent:sa1;agent:sa2",
							Info: "sa2 interrupt data",
							Address: Address{
								{
									ID:   "loop",
									Type: AddressSegmentAgent,
								},
								{
									ID:   "sa2",
									Type: AddressSegmentAgent,
								},
							},
							IsRootCause: true,
							Parent: &InterruptCtx{
								ID:   "agent:loop",
								Info: "Loop workflow interrupted",
								Address: Address{
									{
										ID:   "loop",
										Type: AddressSegmentAgent,
									},
								},
							},
						},
					},
				},
			},
		}

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
		// Check the first two message events
		assert.Equal(t, loopMessageEvents[0].AgentName, events[0].AgentName)
		assert.Equal(t, loopMessageEvents[0].RunPath, events[0].RunPath)
		assert.Equal(t, loopMessageEvents[0].Output.MessageOutput.Message.Content, events[0].Output.MessageOutput.Message.Content)

		assert.Equal(t, loopMessageEvents[1].AgentName, events[1].AgentName)
		assert.Equal(t, loopMessageEvents[1].RunPath, events[1].RunPath)
		assert.Equal(t, loopMessageEvents[1].Output.MessageOutput.Message.Content, events[1].Output.MessageOutput.Message.Content)

		// Check the third interrupt event using EqualsWithoutID
		assert.Equal(t, loopMessageEvents[2].AgentName, events[2].AgentName)
		assert.Equal(t, loopMessageEvents[2].RunPath, events[2].RunPath)
		assert.True(t, events[2].Action.Interrupted.InterruptContexts[0].EqualsWithoutID(loopMessageEvents[2].Action.Interrupted.InterruptContexts[0]))
		loopInterruptID3 := events[2].Action.Interrupted.InterruptContexts[0].ID
		events = []*AgentEvent{}

		// Resume after third interrupt
		iter, err = runner.ResumeWithParams(ctx, "loop-1", &ResumeParams{
			Targets: map[string]any{
				loopInterruptID3: "resume sa1",
			},
		})
		assert.NoError(t, err)
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}
		assert.Equal(t, 1, len(events))
		assert.Equal(t, loopFourthInterruptEvent.AgentName, events[0].AgentName)
		assert.Equal(t, loopFourthInterruptEvent.RunPath, events[0].RunPath)
		assert.True(t, events[0].Action.Interrupted.InterruptContexts[0].EqualsWithoutID(loopFourthInterruptEvent.Action.Interrupted.InterruptContexts[0]))
		loopInterruptID4 := events[0].Action.Interrupted.InterruptContexts[0].ID
		events = []*AgentEvent{}

		// Resume after fourth interrupt
		iter, err = runner.ResumeWithParams(ctx, "loop-1", &ResumeParams{
			Targets: map[string]any{
				loopInterruptID4: "resume sa2",
			},
		})
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

		// Debug: Print actual events to see what we're getting
		for i, event := range events {
			t.Logf("Event %d: AgentName=%s, RunPath=%v, Output=%v", i, event.AgentName, event.RunPath, event.Output)
		}

		// Define parallel message events separately
		parallelMessageEvents := []*AgentEvent{
			{
				AgentName: "sa4",
				RunPath:   []RunStep{{"parallel agent"}, {"sa4"}},
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						Message: schema.UserMessage("sa4 completed"),
					},
				},
			},
			{
				AgentName: "sa3",
				RunPath:   []RunStep{{"parallel agent"}, {"sa3"}},
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{
						Message: schema.UserMessage("sa3 completed"),
					},
				},
			},
		}

		assert.Contains(t, events, parallelMessageEvents[0])
		assert.Contains(t, events, parallelMessageEvents[1])

		assert.NotNil(t, interruptEvent)
		assert.Equal(t, "parallel agent", interruptEvent.AgentName)
		assert.Equal(t, []RunStep{{"parallel agent"}}, interruptEvent.RunPath)
		assert.NotNil(t, interruptEvent.Action.Interrupted)
		wii, ok := interruptEvent.Action.Interrupted.Data.(*WorkflowInterruptInfo)
		assert.True(t, ok)
		assert.Equal(t, 2, len(wii.ParallelInterruptInfo))

		var sa1Found, sa2Found bool
		for _, info := range wii.ParallelInterruptInfo {
			switch info.Data {
			case "sa1 interrupt data":
				sa1Found = true
			case "sa2 interrupt data":
				sa2Found = true
			}
		}
		assert.True(t, sa1Found)
		assert.True(t, sa2Found)

		var sa1InfoFound, sa2InfoFound bool
		for _, ctx := range interruptEvent.Action.Interrupted.InterruptContexts {
			if ctx.Info == "sa1 interrupt data" {
				sa1InfoFound = true
			} else if ctx.Info == "sa2 interrupt data" {
				sa2InfoFound = true
			}
		}

		assert.Equal(t, 2, len(interruptEvent.Action.Interrupted.InterruptContexts))
		assert.True(t, sa1InfoFound)
		assert.True(t, sa2InfoFound)

		var parallelInterruptID1, parallelInterruptID2 string
		for _, ctx := range interruptEvent.Action.Interrupted.InterruptContexts {
			if ctx.Info == "sa1 interrupt data" {
				parallelInterruptID1 = ctx.ID
			} else if ctx.Info == "sa2 interrupt data" {
				parallelInterruptID2 = ctx.ID
			}
		}
		assert.NotEmpty(t, parallelInterruptID1)
		assert.NotEmpty(t, parallelInterruptID2)

		iter, err = runner.ResumeWithParams(ctx, "1", &ResumeParams{
			Targets: map[string]any{
				parallelInterruptID1: "resume sa1",
				parallelInterruptID2: "resume sa2",
			},
		})
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
	assert.Equal(t, 1, len(event.Action.Interrupted.InterruptContexts))
	assert.Equal(t, Address{
		{Type: AddressSegmentAgent, ID: "name"},
		{Type: AddressSegmentTool, ID: "tool1", SubID: "1"},
	}, event.Action.Interrupted.InterruptContexts[0].Address)

	var (
		chatModelAgentID string
		toolID           string
	)

	intCtx := event.Action.Interrupted.InterruptContexts[0]
	for intCtx != nil {
		if intCtx.Address[len(intCtx.Address)-1].Type == AddressSegmentTool {
			toolID = intCtx.ID
		} else if intCtx.Address[len(intCtx.Address)-1].Type == AddressSegmentAgent {
			chatModelAgentID = intCtx.ID
		}
		intCtx = intCtx.Parent
	}

	event, ok = iter.Next()
	assert.False(t, ok)

	iter, err = runner.ResumeWithParams(ctx, "1", &ResumeParams{
		Targets: map[string]any{
			chatModelAgentID: &ChatModelAgentResumeData{
				HistoryModifier: func(ctx context.Context, history []Message) []Message {
					history[2].Content = "new user message"
					return history
				},
			},
			toolID: "tool resume result",
		},
	})
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "tool resume result")
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "completed")
}

func TestChatModelAgentToolInterrupt(t *testing.T) {
	sa := &myAgent{
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			intAct := Interrupt(ctx, "hello world")
			intAct.Action.Interrupted.Data = "hello world"
			generator.Send(intAct)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.NotNil(t, info)
			assert.False(t, info.EnableStreaming)

			if !info.IsResumeTarget {
				iter, generator := NewAsyncIteratorPair[*AgentEvent]()
				intAct := Interrupt(ctx, "interrupt again")
				intAct.Action.Interrupted.Data = "interrupt again"
				generator.Send(intAct)
				generator.Close()
				return iter
			}

			assert.NotNil(t, info.ResumeData)
			assert.Equal(t, "resume sa", info.ResumeData)

			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.UserMessage(fmt.Sprintf("my agent completed with data %s", info.ResumeData))}}})
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

	iter, err = runner.Resume(ctx, "1")
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.NotNil(t, event.Action.Interrupted)
	assert.Equal(t, 1, len(event.Action.Interrupted.InterruptContexts))
	for _, ctx := range event.Action.Interrupted.InterruptContexts {
		if ctx.IsRootCause {
			assert.Equal(t, Address{
				{Type: AddressSegmentAgent, ID: "name"},
				{Type: AddressSegmentTool, ID: "myAgent", SubID: "1"},
				{Type: AddressSegmentAgent, ID: "myAgent"},
			}, ctx.Address)
			assert.Equal(t, "interrupt again", ctx.Info)
		}
	}

	var toolInterruptID string
	for _, ctx := range event.Action.Interrupted.InterruptContexts {
		if ctx.IsRootCause {
			toolInterruptID = ctx.ID
			break
		}
	}
	assert.NotEmpty(t, toolInterruptID)

	event, ok = iter.Next()
	assert.False(t, ok)

	iter, err = runner.ResumeWithParams(ctx, "1", &ResumeParams{
		Targets: map[string]any{
			toolInterruptID: "resume sa",
		},
	})
	assert.NoError(t, err)
	event, ok = iter.Next()
	assert.True(t, ok)
	assert.NoError(t, event.Err)
	assert.Equal(t, event.Output.MessageOutput.Message.Content, "my agent completed with data resume sa")
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

func (m *myStore) Set(_ context.Context, key string, value []byte) error {
	m.m[key] = value
	return nil
}

func (m *myStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	v, ok := m.m[key]
	return v, ok, nil
}

type myAgentOptions struct {
	interrupt bool

	value string
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

func (m *myAgent) Name(_ context.Context) string {
	if len(m.name) > 0 {
		return m.name
	}
	return "myAgent"
}

func (m *myAgent) Description(_ context.Context) string {
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

func (m *myModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
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

func (m *myModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("implement me")
}

func (m *myModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type myTool1 struct{}

func (m *myTool1) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "tool1",
		Desc: "desc",
	}, nil
}

func (m *myTool1) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	if wasInterrupted, _, _ := tool.GetInterruptState[any](ctx); !wasInterrupted {
		return "", tool.Interrupt(ctx, nil)
	}

	if isResumeFlow, hasResumeData, data := tool.GetResumeContext[string](ctx); !isResumeFlow {
		return "", tool.Interrupt(ctx, nil)
	} else if hasResumeData {
		return data, nil
	}

	return "result", nil
}

func TestCyclicalAgentInterrupt(t *testing.T) {
	ctx := context.Background()

	var agentA, agentB, agentC Agent

	// agentC interrupts
	agentC = &myAgent{
		name: "C",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			intAct := Interrupt(ctx, "interrupt from C")
			generator.Send(intAct)
			generator.Close()
			return iter
		},
		resumeFn: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			assert.True(t, info.IsResumeTarget)
			assert.NotNil(t, info.ResumeData)
			assert.Equal(t, "resume C", info.ResumeData)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "C",
				Output: &AgentOutput{
					MessageOutput: &MessageVariant{Message: schema.UserMessage("C completed")},
				},
			})
			generator.Close()
			return iter
		},
	}

	// agentB transfers back to its parent A
	agentB = &myAgent{
		name: "B",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()
			generator.Send(&AgentEvent{
				AgentName: "B",
				Action: &AgentAction{
					TransferToAgent: &TransferToAgentAction{
						DestAgentName: "A", // Transfer back to parent
					},
				},
			})
			generator.Close()
			return iter
		},
	}

	// agentA is the parent, orchestrating the A->B->A->C flow
	agentA = &myAgent{
		name: "A",
		runFn: func(ctx context.Context, input *AgentInput, options ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			runCtx := getRunCtx(ctx)
			iter, generator := NewAsyncIteratorPair[*AgentEvent]()

			// If the last agent was B, we are in the A->B->A path, so transfer to C.
			// Otherwise, it's the first run, transfer to B.
			dest := "B"
			if len(runCtx.RunPath) > 1 && runCtx.RunPath[len(runCtx.RunPath)-2].agentName == "B" {
				dest = "C"
			}

			generator.Send(&AgentEvent{
				AgentName: "A",
				Action: &AgentAction{
					TransferToAgent: &TransferToAgentAction{
						DestAgentName: dest,
					},
				},
			})
			generator.Close()
			return iter
		},
	}

	// Set up the hierarchy: A is parent of B and C.
	agentA, err := SetSubAgents(ctx, agentA, []Agent{agentB, agentC})
	assert.NoError(t, err)

	// Run the test
	runner := NewRunner(ctx, RunnerConfig{
		Agent:           agentA,
		CheckPointStore: newMyStore(),
	})
	iter := runner.Query(ctx, "start", WithCheckPointID("cyclical-1"))

	var events []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// We expect 3 transfer events (A->B, B->A, A->C) and 1 interrupt event from C.
	assert.Equal(t, 4, len(events))

	interruptEvent := events[3]
	assert.NotNil(t, interruptEvent.Action.Interrupted)
	assert.Equal(t, "C", interruptEvent.AgentName)

	// Check the interrupt context
	assert.Equal(t, 1, len(interruptEvent.Action.Interrupted.InterruptContexts))
	interruptCtx := interruptEvent.Action.Interrupted.InterruptContexts[0]
	assert.True(t, interruptCtx.IsRootCause)
	assert.Equal(t, "interrupt from C", interruptCtx.Info)

	expectedAddr := Address{
		{Type: AddressSegmentAgent, ID: "A"},
		{Type: AddressSegmentAgent, ID: "B"},
		{Type: AddressSegmentAgent, ID: "A"},
		{Type: AddressSegmentAgent, ID: "C"},
	}
	assert.Equal(t, expectedAddr, interruptCtx.Address)
	assert.NotEmpty(t, interruptCtx.ID)

	// Resume the execution
	iter, err = runner.ResumeWithParams(ctx, "cyclical-1", &ResumeParams{
		Targets: map[string]any{
			interruptCtx.ID: "resume C",
		},
	})
	assert.NoError(t, err)

	events = []*AgentEvent{}
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}

	// We expect one output event from C
	assert.Equal(t, 1, len(events))
	assert.Equal(t, "C completed", events[0].Output.MessageOutput.Message.Content)
}

// myStatefulTool is a tool that can interrupt and has internal state to track invocations.

type myStatefulTool struct {
	name string
	t    *testing.T
}

func (m *myStatefulTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: m.name,
		Desc: "desc",
	}, nil
}

type myStatefulToolState struct {
	InterruptCount int
}

func init() {
	schema.Register[myStatefulToolState]()
}

func (m *myStatefulTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	wasInterrupted, hasState, state := tool.GetInterruptState[myStatefulToolState](ctx)
	if !wasInterrupted {
		return "", tool.StatefulInterrupt(ctx, fmt.Sprintf("interrupt from %s", m.name), myStatefulToolState{InterruptCount: 1})
	}

	isResumeFlow, hasResumeData, data := tool.GetResumeContext[string](ctx)
	if !isResumeFlow || !hasResumeData {
		assert.True(m.t, hasState, "tool %s should have interrupt state on resume", m.name)
		return "", tool.StatefulInterrupt(ctx, fmt.Sprintf("interrupt from %s", m.name), myStatefulToolState{InterruptCount: state.InterruptCount + 1})
	}

	return data, nil
}

func TestChatModelParallelToolInterruptAndResume(t *testing.T) {
	ctx := context.Background()

	toolA := &myStatefulTool{name: "toolA", t: t}
	toolB := &myStatefulTool{name: "toolB", t: t}

	chatModel, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ParallelToolAgent",
		Description: "An agent that uses parallel tools",
		Model: &myModel{
			messages: []*schema.Message{
				// 1. First model response: call toolA and toolB in parallel
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "1", Function: schema.FunctionCall{Name: "toolA", Arguments: "{}"}},
					{ID: "2", Function: schema.FunctionCall{Name: "toolB", Arguments: "{}"}},
				}),
				// 2. Second model response (after tools are resumed): call them again to check state
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "3", Function: schema.FunctionCall{Name: "toolA", Arguments: "{}"}},
					{ID: "4", Function: schema.FunctionCall{Name: "toolB", Arguments: "{}"}},
				}),
				// 3. Final completion
				schema.AssistantMessage("all done", nil),
			},
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{toolA, toolB},
			},
		},
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           chatModel,
		CheckPointStore: newMyStore(),
	})

	// 1. Initial query -> parallel interrupt from toolA and toolB
	iter := runner.Query(ctx, "start", WithCheckPointID("parallel-tool-test-1"))
	normalEvents, interruptEvent := consumeUntilInterrupt(iter)

	assert.Equal(t, 1, len(normalEvents))
	assert.NotNil(t, interruptEvent)
	assert.Equal(t, 2, len(interruptEvent.Action.Interrupted.InterruptContexts),
		"should have 2 interrupts")

	var toolAInterruptID, toolBInterruptID string
	for _, info := range interruptEvent.Action.Interrupted.InterruptContexts {
		if info.Info == "interrupt from toolA" {
			toolAInterruptID = info.ID
			assert.True(t, info.IsRootCause)
		} else if info.Info == "interrupt from toolB" {
			toolBInterruptID = info.ID
			assert.True(t, info.IsRootCause)
		}
	}
	assert.NotEmpty(t, toolAInterruptID)
	assert.NotEmpty(t, toolBInterruptID)

	// 2. Resume, targeting only toolA. toolB should re-interrupt.
	iter, err = runner.ResumeWithParams(ctx, "parallel-tool-test-1", &ResumeParams{
		Targets: map[string]any{
			toolAInterruptID: "toolA resumed",
		},
	})
	assert.NoError(t, err)
	_, interruptEvent = consumeUntilInterrupt(iter)

	assert.NotNil(t, interruptEvent, "expected a re-interrupt from toolB")
	assert.Equal(t, 1, len(interruptEvent.Action.Interrupted.InterruptContexts),
		"should have 1 remaining interrupts")

	var rootCause *InterruptCtx
	for _, info := range interruptEvent.Action.Interrupted.InterruptContexts {
		if info.IsRootCause {
			rootCause = info
			break
		}
	}

	if rootCause == nil {
		t.Fatal("expected a root cause interrupt from toolB")
	}
	assert.Equal(t, "interrupt from toolB", rootCause.Info)
	toolBReInterruptID := rootCause.ID

	// 3. Resume the re-interrupted toolB. The agent should then call the tools again.
	iter, err = runner.ResumeWithParams(ctx, "parallel-tool-test-1", &ResumeParams{
		Targets: map[string]any{
			toolBReInterruptID: "toolB resumed",
		},
	})
	assert.NoError(t, err)

	// 4. Consume all final events. The internal assertions in the tools will check the wasInterrupted flag.
	// We expect to see the results of the second tool calls, and then the final agent completion.
	finalEvents, interruptEvent := consumeUntilInterrupt(iter)
	assert.Equal(t, 2, len(finalEvents))
	assert.NotNil(t, interruptEvent)
}

// TestNestedChatModelAgentWithAgentTool verifies that the shouldFire method correctly prevents
// duplicate event firing in nested ChatModelAgent scenarios (ChatModelAgent -> AgentTool -> ChatModelAgent).
// This ensures that only the inner agent's cbHandler fires, not the outer agent's.
func TestNestedChatModelAgentWithAgentTool(t *testing.T) {
	ctx := context.Background()

	// Create an interruptible tool for the inner agent
	innerTool := &myStatefulTool{name: "innerTool", t: t}

	// Create the inner ChatModelAgent that will be wrapped by AgentTool
	innerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "InnerAgent",
		Description: "Inner agent with interruptible tool",
		Model: &myModel{
			messages: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "1", Function: schema.FunctionCall{Name: "innerTool", Arguments: "{}"}},
				}),
				schema.AssistantMessage("inner agent completed", nil),
			},
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{innerTool},
			},
		},
	})
	assert.NoError(t, err)

	// Wrap the inner agent in an AgentTool
	agentTool := NewAgentTool(ctx, innerAgent)

	// Create the outer ChatModelAgent that uses the AgentTool
	outerAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "OuterAgent",
		Description: "Outer agent with AgentTool containing inner agent",
		Model: &myModel{
			messages: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{
					{ID: "1", Function: schema.FunctionCall{Name: "InnerAgent", Arguments: "{}"}},
				}),
				schema.AssistantMessage("outer agent completed", nil),
			},
		},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{agentTool},
			},
		},
	})
	assert.NoError(t, err)

	runner := NewRunner(ctx, RunnerConfig{
		Agent:           outerAgent,
		CheckPointStore: newMyStore(),
	})

	// Run the query - this should trigger the nested agent structure
	iter := runner.Query(ctx, "start", WithCheckPointID("nested-agent-test-1"))

	// Collect all events to verify no duplicates
	var allEvents []*AgentEvent
	var interruptEvent *AgentEvent

	for {
		event, ok := iter.Next()
		if !ok {
			break
		}

		if event.Action != nil && event.Action.Interrupted != nil {
			assert.Nil(t, interruptEvent)
			interruptEvent = event
		}

		allEvents = append(allEvents, event)
	}

	if interruptEvent == nil {
		t.Fatal("expected an interrupt event")
	}

	// Verify we got exactly one interrupt event (not duplicated)
	assert.NotNil(t, interruptEvent, "should have an interrupt event")
	assert.Equal(t, 1, len(interruptEvent.Action.Interrupted.InterruptContexts),
		"should have exactly one interrupt context")

	// Verify the interrupt comes from the inner tool, not duplicated
	interruptCtx := interruptEvent.Action.Interrupted.InterruptContexts[0]
	assert.True(t, interruptCtx.IsRootCause, "interrupt should be root cause")
	assert.Equal(t, "interrupt from innerTool", interruptCtx.Info)

	// Verify the address path shows the correct nested structure
	expectedAddress := Address{
		{Type: AddressSegmentAgent, ID: "OuterAgent"},
		{Type: AddressSegmentTool, ID: "InnerAgent", SubID: "1"},
		{Type: AddressSegmentAgent, ID: "InnerAgent"},
		{Type: AddressSegmentTool, ID: "innerTool", SubID: "1"},
	}
	assert.Equal(t, expectedAddress, interruptCtx.Address,
		"interrupt address should show correct nested structure")

	// Verify no duplicate events by checking agent names in events
	var agentNames []string
	for _, event := range allEvents {
		if event.AgentName != "" {
			agentNames = append(agentNames, event.AgentName)
		}
	}

	// Should only have events from the outer agent (the inner agent's events should be handled
	// by the AgentTool and not duplicated by the outer agent's cbHandler)
	for _, name := range agentNames {
		assert.Equal(t, "OuterAgent", name,
			"all events should come from OuterAgent, not duplicated from InnerAgent")
	}

	// Now resume the interrupt
	interruptID := interruptCtx.ID
	iter, err = runner.ResumeWithParams(ctx, "nested-agent-test-1", &ResumeParams{
		Targets: map[string]any{
			interruptID: "resume inner tool",
		},
	})
	assert.NoError(t, err)

	// Collect final events after resume
	var finalEvents []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		finalEvents = append(finalEvents, event)
	}

	// Verify completion events
	assert.Greater(t, len(finalEvents), 0, "should have completion events after resume")

	// Check that we get the expected completion messages
	var foundInnerCompletion, foundOuterCompletion bool
	for _, event := range finalEvents {
		if event.Output != nil && event.Output.MessageOutput != nil {
			if event.Output.MessageOutput.Message != nil {
				content := event.Output.MessageOutput.Message.Content
				if content == "inner agent completed" {
					foundInnerCompletion = true
				} else if content == "outer agent completed" {
					foundOuterCompletion = true
				}
			}
		}
	}

	assert.True(t, foundInnerCompletion, "should have inner agent completion")
	assert.True(t, foundOuterCompletion, "should have outer agent completion")
}

// consumeUntilInterrupt consumes events from the iterator until an interrupt is found or it's exhausted.
func consumeUntilInterrupt(iter *AsyncIterator[*AgentEvent]) (normalEvents []*AgentEvent, interruptEvent *AgentEvent) {
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			interruptEvent = event
			continue
		}
		normalEvents = append(normalEvents, event)
	}
	return
}
