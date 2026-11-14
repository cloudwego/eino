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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Keep Run and Resume on separate types so tests can observe dynamic resume support.
type executionTestAgent[M MessageType] struct {
	name string
	run  func(context.Context, *TypedAgentInput[M], ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]]
}

func (a *executionTestAgent[M]) Name(context.Context) string { return a.name }
func (a *executionTestAgent[M]) Description(context.Context) string {
	return "execution compatibility test"
}
func (a *executionTestAgent[M]) GetType() string { return "scripted" }
func (a *executionTestAgent[M]) Run(ctx context.Context, input *TypedAgentInput[M], opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	return a.run(ctx, input, opts...)
}

type executionTestResumable[M MessageType] struct {
	*executionTestAgent[M]
	resume func(context.Context, *ResumeInfo, ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]]
}

func (a *executionTestResumable[M]) Resume(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
	return a.resume(ctx, info, opts...)
}

func executionTestIter[M MessageType](events ...*TypedAgentEvent[M]) *AsyncIterator[*TypedAgentEvent[M]] {
	iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	for _, event := range events {
		gen.Send(event)
	}
	gen.Close()
	return iter
}

func executionTestCollect[M MessageType](t *testing.T, iter *AsyncIterator[*TypedAgentEvent[M]]) []*TypedAgentEvent[M] {
	t.Helper()
	done := make(chan []*TypedAgentEvent[M], 1)
	go func() {
		var events []*TypedAgentEvent[M]
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			events = append(events, event)
		}
		done <- events
	}()
	return executionTestReceive(t, done)
}

func executionTestReceive[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(10 * time.Second):
		t.Fatal("execution did not reach the expected barrier")
		var zero T
		return zero
	}
}

func executionTestCallbackIter[M MessageType](t *testing.T, out callbacks.CallbackOutput) *AsyncIterator[*TypedAgentEvent[M]] {
	t.Helper()
	if messageOut, ok := out.(*AgentCallbackOutput); ok {
		return any(messageOut.Events).(*AsyncIterator[*TypedAgentEvent[M]])
	}
	typedOut, ok := out.(*TypedAgentCallbackOutput[M])
	require.True(t, ok, "unexpected callback output %T", out)
	return typedOut.Events
}

func TestExecutionCompat_EntryAndErrors(t *testing.T) {
	t.Run("Message", func(t *testing.T) { executionTestEntryAndErrors[*schema.Message](t) })
	t.Run("Agentic", func(t *testing.T) { executionTestEntryAndErrors[*schema.AgenticMessage](t) })
}

func executionTestEntryAndErrors[M MessageType](t *testing.T) {
	for _, withStore := range []bool{false, true} {
		t.Run(fmt.Sprintf("store=%v", withStore), func(t *testing.T) {
			ctx := context.Background()
			sentinel := errors.New("ordinary error")
			entered := false
			agent := &executionTestAgent[M]{name: "A", run: func(context.Context, *TypedAgentInput[M], ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
				entered = true
				return executionTestIter(&TypedAgentEvent[M]{Err: sentinel}, &TypedAgentEvent[M]{})
			}}
			var store CheckPointStore
			if withStore {
				store = newCancelTestStore()
			}
			runner := NewTypedRunner(TypedRunnerConfig[M]{Agent: agent, CheckPointStore: store})
			iter := runner.Run(ctx, nil)
			require.True(t, entered, "Run must enter the business agent synchronously")
			events := executionTestCollect(t, iter)
			require.Len(t, events, 2)
			require.ErrorIs(t, events[0].Err, sentinel)
			require.NoError(t, events[1].Err)
			require.Equal(t, "A", events[1].AgentName)
			require.Len(t, events[1].RunPath, 1)
			agent.run = func(context.Context, *TypedAgentInput[M], ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
				panic(sentinel)
			}
			require.PanicsWithValue(t, sentinel, func() { runner.Run(ctx, nil) })
		})
	}
}

func TestExecutionCompat_MessageInputProjection(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("fail=%v", fail), func(t *testing.T) {
			ctx := context.Background()
			called := 0
			agent := &executionTestAgent[*schema.Message]{name: "A", run: func(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				called++
				require.Equal(t, "projected", input.Messages[0].Content)
				return executionTestIter(&AgentEvent{})
			}}
			wrapped := AgentWithOptions(ctx, agent, WithHistoryRewriter(func(_ context.Context, entries []*HistoryEntry) ([]Message, error) {
				require.Len(t, entries, 1)
				require.True(t, entries[0].IsUserInput)
				require.Equal(t, "original", entries[0].Message.Content)
				if fail {
					return nil, errors.New("projection failed")
				}
				return []Message{schema.UserMessage("projected")}, nil
			}))
			starts := 0
			handler := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
				starts++
				require.Equal(t, "projected", ConvAgentCallbackInput(input).Input.Messages[0].Content)
				return ctx
			}).Build()
			inheritedStarts := make(chan string, 1)
			inheritedEnds := make(chan *AsyncIterator[*AgentEvent], 1)
			inherited := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
				inheritedStarts <- info.Name + ":" + ConvAgentCallbackInput(input).Input.Messages[0].Content
				return ctx
			}).OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
				inheritedEnds <- ConvAgentCallbackOutput(output).Events
				return ctx
			}).Build()
			ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Name: "inherited", Component: ComponentOfAgent}, inherited)
			events := executionTestCollect(t, NewRunner(ctx, RunnerConfig{Agent: wrapped}).Query(ctx, "original", WithCallbacks(handler)))
			inheritedEvents := executionTestCollect(t, executionTestReceive(t, inheritedEnds))
			require.Len(t, inheritedEvents, 1)
			if fail {
				require.Zero(t, called)
				require.Zero(t, starts, "a failed projection must not initialize designated callbacks")
				require.EqualError(t, events[0].Err, "projection failed")
				require.Equal(t, "inherited:original", executionTestReceive(t, inheritedStarts))
				require.EqualError(t, inheritedEvents[0].Err, "projection failed")
			} else {
				require.Equal(t, 1, called)
				require.Equal(t, 1, starts)
				require.NoError(t, events[0].Err)
				require.Equal(t, "A:projected", executionTestReceive(t, inheritedStarts))
				require.NoError(t, inheritedEvents[0].Err)
			}
		})
	}
}

func TestExecutionCompat_TransferContinuation(t *testing.T) {
	for _, scenario := range []string{"clear", "missing", "transfer"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			bCalls := 0
			b := &executionTestAgent[*schema.Message]{name: "B", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				bCalls++
				return executionTestIter(EventFromMessage(schema.AssistantMessage("B", nil), nil, schema.Assistant, ""))
			}}
			a := &executionTestAgent[*schema.Message]{name: "A", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				dest := "B"
				if scenario == "missing" {
					dest = "missing"
				}
				events := []*AgentEvent{{Action: NewTransferToAgentAction(dest)}}
				if scenario == "clear" {
					events = append(events, &AgentEvent{})
				}
				return executionTestIter(events...)
			}}
			wrapped, err := SetSubAgents(ctx, a, []Agent{b})
			require.NoError(t, err)
			cb := make(chan *AsyncIterator[*AgentEvent], 1)
			handler := callbacks.NewHandlerBuilder().OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
				if info.Name == "A" {
					cb <- ConvAgentCallbackOutput(out).Events
				}
				return ctx
			}).Build()
			events := executionTestCollect(t, NewRunner(ctx, RunnerConfig{Agent: wrapped}).Query(ctx, "q", WithCallbacks(handler)))
			cbEvents := executionTestCollect(t, executionTestReceive(t, cb))
			for _, event := range cbEvents {
				require.Equal(t, "A", event.AgentName)
				require.NoError(t, event.Err, "routing errors belong to the caller, not the source callback")
			}
			switch scenario {
			case "clear":
				require.Zero(t, bCalls)
				require.Len(t, events, 2)
				require.Len(t, cbEvents, 2)
			case "missing":
				require.Zero(t, bCalls)
				require.Len(t, events, 2)
				require.ErrorContains(t, events[1].Err, "agent 'missing' not found")
				require.Len(t, cbEvents, 1)
			case "transfer":
				require.Equal(t, 1, bCalls)
				require.Len(t, events, 2)
				require.Equal(t, "B", events[1].AgentName)
				require.Len(t, cbEvents, 1)
			}
		})
	}
}

func TestExecutionCompat_Callbacks(t *testing.T) {
	t.Run("Message", func(t *testing.T) { executionTestCallbacks[*schema.Message](t, ComponentOfAgent) })
	t.Run("Agentic", func(t *testing.T) { executionTestCallbacks[*schema.AgenticMessage](t, ComponentOfAgenticAgent) })
	t.Run("normal transfer completion", func(t *testing.T) {
		ctx := context.Background()
		bStarted, releaseB, bFinished := make(chan struct{}), make(chan struct{}), make(chan struct{})
		a := &executionTestAgent[*schema.Message]{name: "A", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			event := EventFromMessage(schema.AssistantMessage("A-done", nil), nil, schema.Assistant, "")
			event.Action = NewTransferToAgentAction("B")
			return executionTestIter(event)
		}}
		b := &executionTestAgent[*schema.Message]{name: "B", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			close(bStarted)
			go func() {
				<-releaseB
				gen.Send(EventFromMessage(schema.AssistantMessage("B-done", nil), nil, schema.Assistant, ""))
				close(bFinished)
				gen.Close()
			}()
			return iter
		}}
		root, err := SetSubAgents(ctx, a, []Agent{b})
		require.NoError(t, err)
		type callbackResult struct {
			name           string
			contents       []string
			bFinishedAtEOF bool
		}
		results := make(chan callbackResult, 2)
		aObserved := make(chan struct{})
		bCallbackStarted := make(chan struct{})
		handler := callbacks.NewHandlerBuilder().OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			if info.Name == "B" {
				close(bCallbackStarted)
			}
			go func() {
				result := callbackResult{name: info.Name}
				iter := ConvAgentCallbackOutput(output).Events
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					result.contents = append(result.contents, event.Output.MessageOutput.Message.Content)
					if info.Name == "A" {
						close(aObserved)
					}
				}
				select {
				case <-bFinished:
					result.bFinishedAtEOF = true
				default:
				}
				results <- result
			}()
			return ctx
		}).Build()
		iter := NewRunner(ctx, RunnerConfig{Agent: root}).Query(ctx, "q", WithCallbacks(handler))
		executionTestReceive(t, aObserved)
		executionTestReceive(t, bStarted)
		executionTestReceive(t, bCallbackStarted)
		close(releaseB)
		require.Len(t, executionTestCollect(t, iter), 2)
		for i := 0; i < 2; i++ {
			result := executionTestReceive(t, results)
			require.True(t, result.bFinishedAtEOF, "%s callback reached EOF before B finished", result.name)
			require.Equal(t, []string{result.name + "-done"}, result.contents)
		}
	})
}

func executionTestCallbacks[M MessageType](t *testing.T, component components.Component) {
	ctx := context.Background()
	source, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
	defer gen.Close()
	agent := &executionTestAgent[M]{name: "A", run: func(context.Context, *TypedAgentInput[M], ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
		return source
	}}
	ends := make(chan *AsyncIterator[*TypedAgentEvent[M]], 2)
	endOrder := make(chan string, 2)
	var order []string
	handler := func(id string) callbacks.Handler {
		return callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			require.Equal(t, component, info.Component)
			require.Equal(t, "A", info.Name)
			require.Equal(t, "scripted", info.Type)
			if component == ComponentOfAgent {
				require.NotNil(t, ConvAgentCallbackInput(input))
			} else {
				require.NotNil(t, ConvTypedCallbackInput[M](input))
			}
			order = append(order, id)
			return ctx
		}).OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			require.Equal(t, component, info.Component)
			endOrder <- id
			ends <- executionTestCallbackIter[M](t, output)
			return ctx
		}).Build()
	}
	iter := NewTypedRunner(TypedRunnerConfig[M]{Agent: agent}).Run(ctx, nil, WithCallbacks(handler("first"), handler("second")))
	// OnEnd receives a live iterator while the source has not emitted any event.
	cb1 := executionTestReceive(t, ends)
	cb2 := executionTestReceive(t, ends)
	require.Equal(t, []string{"second", "first"}, order)
	require.Equal(t, "first", executionTestReceive(t, endOrder))
	require.Equal(t, "second", executionTestReceive(t, endOrder))
	gen.Send(&TypedAgentEvent[M]{})
	gen.Close()
	require.Len(t, executionTestCollect(t, iter), 1)
	require.Len(t, executionTestCollect(t, cb1), 1)
	require.Len(t, executionTestCollect(t, cb2), 1)
}

func TestExecutionCompat_StreamConsumers(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		executionTestStreamConsumers(t, []*schema.Message{schema.AssistantMessage("one", nil), schema.AssistantMessage("two", nil)})
	})
	t.Run("Agentic", func(t *testing.T) {
		executionTestStreamConsumers(t, []*schema.AgenticMessage{agenticMsg("one"), agenticMsg("two")})
	})
}

func executionTestStreamConsumers[M MessageType](t *testing.T, chunks []M) {
	ctx := context.Background()
	callbacksOut := make(chan *AsyncIterator[*TypedAgentEvent[M]], 2)
	handler := callbacks.NewHandlerBuilder().OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
		callbacksOut <- executionTestCallbackIter[M](t, out)
		return ctx
	}).Build()
	agent := &executionTestAgent[M]{name: "A", run: func(ctx context.Context, _ *TypedAgentInput[M], _ ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
		var zero M
		return executionTestIter(typedModelOutputEvent(zero, schema.StreamReaderFromArray(chunks)), TypedInterrupt[M](ctx, "consume history"))
	}}
	iter := NewTypedRunner(TypedRunnerConfig[M]{Agent: agent, CheckPointStore: newCancelTestStore()}).Run(ctx, nil, WithCallbacks(handler, handler), WithCheckPointID("streams"))
	// First exhaust caller events; only then consume callbacks and the retained stream.
	events := executionTestCollect(t, iter)
	require.Len(t, events, 2)
	require.NoError(t, events[0].Err)
	require.NotNil(t, events[1].Action.Interrupted)
	for i := 0; i < 2; i++ {
		cbEvents := executionTestCollect(t, executionTestReceive(t, callbacksOut))
		require.Len(t, cbEvents, 2)
		stream := cbEvents[0].Output.MessageOutput.MessageStream
		for _, chunk := range chunks {
			got, err := stream.Recv()
			require.NoError(t, err)
			require.Equal(t, chunk, got)
		}
		stream.Close()
	}
	stream := events[0].Output.MessageOutput.MessageStream
	defer stream.Close()
	for _, chunk := range chunks {
		got, err := stream.Recv()
		require.NoError(t, err)
		require.Equal(t, chunk, got)
	}
}

type executionTestStore struct {
	*cancelTestStore
	get func(context.Context, string) ([]byte, bool, error)
	set func(context.Context, string, []byte) error
}

func (s *executionTestStore) Get(ctx context.Context, key string) ([]byte, bool, error) {
	if s.get != nil {
		return s.get(ctx, key)
	}
	return s.cancelTestStore.Get(ctx, key)
}

func (s *executionTestStore) Set(ctx context.Context, key string, value []byte) error {
	if s.set != nil {
		return s.set(ctx, key, value)
	}
	return s.cancelTestStore.Set(ctx, key, value)
}

func TestExecutionCompat_ResumeAndCheckpoint(t *testing.T) {
	t.Run("Message", func(t *testing.T) { executionTestResumeAndCheckpoint[*schema.Message](t) })
	t.Run("Agentic", func(t *testing.T) { executionTestResumeAndCheckpoint[*schema.AgenticMessage](t) })
}

func executionTestResumeAndCheckpoint[M MessageType](t *testing.T) {
	ctx := context.Background()
	for _, failure := range []string{"get", "decode"} {
		t.Run(failure, func(t *testing.T) {
			store := &executionTestStore{get: func(context.Context, string) ([]byte, bool, error) {
				if failure == "get" {
					return nil, false, errors.New("store unavailable")
				}
				return []byte("invalid gob"), true, nil
			}}
			runner := NewTypedRunner(TypedRunnerConfig[M]{CheckPointStore: store})
			iter, err := runner.Resume(ctx, "cp")
			require.Nil(t, iter)
			require.ErrorContains(t, err, "failed to load from checkpoint")
		})
	}
	store := &executionTestStore{cancelTestStore: newCancelTestStore()}
	runs := 0
	agent := &executionTestAgent[M]{name: "A", run: func(ctx context.Context, _ *TypedAgentInput[M], _ ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
		runs++
		return executionTestIter(TypedStatefulInterrupt[M](ctx, "pause", "state"))
	}}
	runner := NewTypedRunner(TypedRunnerConfig[M]{Agent: agent, EnableStreaming: true, CheckPointStore: store})
	events := executionTestCollect(t, runner.Run(ctx, nil, WithCheckPointID("cp")))
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Action.Interrupted)
	iter, err := runner.Resume(ctx, "cp")
	require.NoError(t, err)
	events = executionTestCollect(t, iter)
	require.Len(t, events, 1)
	require.ErrorContains(t, events[0].Err, "not a ResumableAgent")
	require.Equal(t, 1, runs)
	resumes := 0
	resumable := &executionTestResumable[M]{executionTestAgent: agent, resume: func(ctx context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
		resumes++
		require.True(t, info.WasInterrupted)
		require.True(t, info.EnableStreaming, "checkpoint must override the new runner configuration")
		require.Equal(t, "state", info.InterruptState)
		require.Equal(t, "other", GetImplSpecificOptions(&myAgentOptions{}, opts...).value)
		return executionTestIter(TypedStatefulInterrupt[M](ctx, "pause again", "state"))
	}}
	runner = NewTypedRunner(TypedRunnerConfig[M]{Agent: resumable, EnableStreaming: false, CheckPointStore: store})
	for i := 0; i < 2; i++ {
		iter, err = runner.Resume(ctx, "cp", withValue("other").DesignateAgent("Other"))
		require.NoError(t, err)
		events = executionTestCollect(t, iter)
		require.Len(t, events, 1)
		require.NotNil(t, events[0].Action.Interrupted)
	}
	require.Equal(t, 2, resumes)

	t.Run("save failure precedes interrupt", func(t *testing.T) {
		store.set = func(context.Context, string, []byte) error { return errors.New("save failed") }
		events := executionTestCollect(t, runner.Run(ctx, nil, WithCheckPointID("failed")))
		require.Len(t, events, 2)
		require.ErrorContains(t, events[0].Err, "save failed")
		require.NotNil(t, events[1].Action.Interrupted)
	})
	t.Run("cancel acknowledgement precedes blocked save", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		store := &executionTestStore{cancelTestStore: newCancelTestStore(), set: func(context.Context, string, []byte) error {
			close(entered)
			<-release
			return errors.New("cancel save failed")
		}}
		source, gen := NewAsyncIteratorPair[*TypedAgentEvent[M]]()
		var runCtx context.Context
		agent := &executionTestAgent[M]{name: "A", run: func(ctx context.Context, _ *TypedAgentInput[M], _ ...AgentRunOption) *AsyncIterator[*TypedAgentEvent[M]] {
			runCtx = ctx
			return source
		}}
		cancelOpt, cancel := WithCancel()
		cb := make(chan *AsyncIterator[*TypedAgentEvent[M]], 1)
		handler := callbacks.NewHandlerBuilder().OnEndFn(func(ctx context.Context, _ *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			cb <- executionTestCallbackIter[M](t, output)
			return ctx
		}).Build()
		iter := NewTypedRunner(TypedRunnerConfig[M]{Agent: agent, CheckPointStore: store}).Run(ctx, nil, cancelOpt, WithCheckPointID("cancel-save"), WithCallbacks(handler))
		handle, requested := cancel()
		require.True(t, requested)
		gen.Send(TypedStatefulInterrupt[M](runCtx, "pause", "state"))
		gen.Close()
		executionTestReceive(t, entered)
		waited := make(chan error, 1)
		go func() { waited <- handle.Wait() }()
		require.NoError(t, executionTestReceive(t, waited), "Wait acknowledges cancellation, not persistence")
		close(release)
		events := executionTestCollect(t, iter)
		require.Len(t, events, 2)
		require.ErrorContains(t, events[0].Err, "failed to save checkpoint on cancel: cancel save failed")
		var cancelErr *CancelError
		require.ErrorAs(t, events[1].Err, &cancelErr)
		require.NotEmpty(t, cancelErr.InterruptContexts)
		cbEvents := executionTestCollect(t, executionTestReceive(t, cb))
		require.Len(t, cbEvents, 1)
		require.NoError(t, cbEvents[0].Err, "ordinary callbacks observe the interrupt before cancellation conversion")
		require.NotNil(t, cbEvents[0].Action.Interrupted)
	})
}

func TestExecutionCompat_WorkflowHistoryResume(t *testing.T) {
	ctx := context.Background()
	store := newCancelTestStore()
	inputs := make(chan []string, 4)
	output := func(content string) *AgentEvent {
		return EventFromMessage(schema.AssistantMessage(content, nil), nil, schema.Assistant, "")
	}
	capture := func(input *AgentInput) []string {
		var contents []string
		for _, message := range input.Messages {
			contents = append(contents, message.Content)
		}
		return contents
	}
	aCalls, bCalls, cCalls := 0, 0, 0
	a := &executionTestAgent[*schema.Message]{name: "A", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
		aCalls++
		return executionTestIter(output("a"))
	}}
	b := &executionTestResumable[*schema.Message]{executionTestAgent: &executionTestAgent[*schema.Message]{name: "B", run: func(ctx context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
		bCalls++
		inputs <- capture(input)
		return executionTestIter(output("b-before"), StatefulInterrupt(ctx, "pause", "B-state"))
	}}, resume: func(_ context.Context, info *ResumeInfo, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
		require.True(t, info.WasInterrupted)
		require.Equal(t, "B-state", info.InterruptState)
		return executionTestIter(output("b-after"))
	}}
	c := &executionTestAgent[*schema.Message]{name: "C", run: func(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
		cCalls++
		inputs <- capture(input)
		return executionTestIter(output("c"))
	}}
	finalInput := make(chan []string, 1)
	f := &executionTestAgent[*schema.Message]{name: "F", run: func(_ context.Context, input *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
		finalInput <- capture(input)
		return executionTestIter(output("f"))
	}}
	parallel, err := NewParallelAgent(ctx, &ParallelAgentConfig{Name: "P", SubAgents: []Agent{b, c}})
	require.NoError(t, err)
	sequence, err := NewSequentialAgent(ctx, &SequentialAgentConfig{Name: "S", SubAgents: []Agent{a, parallel, f}})
	require.NoError(t, err)
	runner := NewRunner(ctx, RunnerConfig{Agent: sequence, CheckPointStore: store})
	events := executionTestCollect(t, runner.Query(ctx, "q", WithCheckPointID("workflow")))
	require.NotNil(t, events[len(events)-1].Action.Interrupted)
	for i := 0; i < 2; i++ {
		require.Equal(t, []string{"q", "For context: [A] said: a."}, executionTestReceive(t, inputs))
	}
	iter, err := runner.Resume(ctx, "workflow")
	require.NoError(t, err)
	events = executionTestCollect(t, iter)
	var contents []string
	for _, event := range events {
		require.NoError(t, event.Err)
		if event.Output != nil {
			contents = append(contents, event.Output.MessageOutput.Message.Content)
		}
	}
	require.Equal(t, []string{"b-after", "f"}, contents, "resume must not replay completed branch outputs")
	require.Equal(t, 1, aCalls)
	require.Equal(t, 1, bCalls)
	require.Equal(t, 1, cCalls)
	require.ElementsMatch(t, []string{"q", "For context: [A] said: a.", "For context: [B] said: b-before.", "For context: [B] said: b-after.", "For context: [C] said: c."}, executionTestReceive(t, finalInput))
}

func TestExecutionCompat_PublicWrappersAndOptions(t *testing.T) {
	for _, direct := range []bool{false, true} {
		t.Run(fmt.Sprintf("direct=%v", direct), func(t *testing.T) {
			ctx := context.Background()
			calls, starts := 0, 0
			agent := &myAgent{name: "A", runFn: func(ctx context.Context, input *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				calls++
				require.Equal(t, "selected", GetImplSpecificOptions(&myAgentOptions{}, opts...).value)
				require.Equal(t, "callback-context", ctx.Value("execution-test"))
				return executionTestIter(&AgentEvent{})
			}, resumeFn: func(context.Context, *ResumeInfo, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				return executionTestIter(&AgentEvent{})
			}}
			wrapped := AgentWithOptions(ctx, agent)
			require.Implements(t, (*ResumableAgent)(nil), wrapped)
			handler := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
				starts++
				return context.WithValue(ctx, "execution-test", "callback-context")
			}).Build()
			opts := []AgentRunOption{withValue("selected").DesignateAgent("A"), withValue("other").DesignateAgent("Other"), WithCallbacks(handler)}
			var iter *AsyncIterator[*AgentEvent]
			if direct {
				iter = wrapped.Run(ctx, &AgentInput{Messages: []Message{schema.UserMessage("q")}}, opts...)
			} else {
				iter = NewRunner(ctx, RunnerConfig{Agent: wrapped}).Query(ctx, "q", opts...)
			}
			events := executionTestCollect(t, iter)
			require.Equal(t, 1, calls)
			require.Equal(t, 1, starts)
			require.Len(t, events, 1)
			require.Len(t, events[0].RunPath, 1)
		})
	}
	t.Run("direct and runner Resume preserve unfiltered options", func(t *testing.T) {
		ctx := context.Background()
		store := newCancelTestStore()
		projectionCalls, resumeCalls := 0, 0
		agent := &myAgent{name: "A", runFn: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			return executionTestIter(StatefulInterrupt(ctx, "pause", "state"))
		}, resumeFn: func(_ context.Context, info *ResumeInfo, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			resumeCalls++
			require.True(t, info.WasInterrupted)
			require.Equal(t, "other", GetImplSpecificOptions(&myAgentOptions{}, opts...).value)
			return executionTestIter(&AgentEvent{})
		}}
		wrapped := AgentWithOptions(ctx, agent, WithHistoryRewriter(func(_ context.Context, entries []*HistoryEntry) ([]Message, error) {
			projectionCalls++
			return []Message{entries[0].Message}, nil
		}))
		runner := NewRunner(ctx, RunnerConfig{Agent: wrapped, CheckPointStore: store})
		executionTestCollect(t, runner.Query(ctx, "q", WithCheckPointID("direct-resume")))
		opts := []AgentRunOption{withValue("other").DesignateAgent("Other")}
		iter, err := runner.Resume(ctx, "direct-resume", opts...)
		require.NoError(t, err)
		events := executionTestCollect(t, iter)
		require.Len(t, events, 1)
		require.NoError(t, events[0].Err)
		require.Len(t, events[0].RunPath, 1)
		// Set up the same persisted context a caller of the public wrapper receives.
		resumeCtx, runCtx, info, err := runnerLoadCheckPointImpl(store, ctx, "direct-resume")
		require.NoError(t, err)
		iter = wrapped.(ResumableAgent).Resume(setRunCtx(resumeCtx, runCtx), info, opts...)
		events = executionTestCollect(t, iter)
		require.Len(t, events, 1)
		require.NoError(t, events[0].Err)
		require.Len(t, events[0].RunPath, 1)
		require.Equal(t, 2, resumeCalls)
		require.Equal(t, 1, projectionCalls, "actual interrupt-point Resume must not project Run input again")
	})
	t.Run("option probes retain order", func(t *testing.T) {
		var trace []string
		probe := WrapImplSpecificOptFn(func(*options) { trace = append(trace, "probe") })
		handler := callbacks.NewHandlerBuilder().OnStartFn(func(ctx context.Context, _ *callbacks.RunInfo, _ callbacks.CallbackInput) context.Context {
			trace = append(trace, "start")
			return ctx
		}).Build()
		agent := &executionTestAgent[*schema.Message]{name: "A", run: func(_ context.Context, _ *AgentInput, opts ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			trace = append(trace, "run")
			getCommonOptions(nil, opts...)
			return executionTestIter(&AgentEvent{})
		}}
		iter := NewRunner(context.Background(), RunnerConfig{Agent: agent}).Run(context.Background(), nil, probe, WithCallbacks(handler))
		trace = append(trace, "returned")
		require.Equal(t, []string{"probe", "probe", "probe", "start", "run", "probe", "probe", "returned"}, trace)
		executionTestCollect(t, iter)
	})
}

func TestExecutionCompat_CancelObservationAndLifetime(t *testing.T) {
	for _, path := range []string{"ordinary", "workflow", "resume-transfer"} {
		t.Run(path, func(t *testing.T) {
			ctx := context.Background()
			store := newCancelTestStore()
			gate := make(chan struct{})
			started := make(chan struct{})
			blocked := func(ctx context.Context) *AsyncIterator[*AgentEvent] {
				iter, gen := NewAsyncIteratorPair[*AgentEvent]()
				close(started)
				go func() {
					defer gen.Close()
					<-gate
					gen.Send(StatefulInterrupt(ctx, "cancel point", "state"))
				}()
				return iter
			}
			rootName := "A"
			agent := &executionTestAgent[*schema.Message]{name: "A", run: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				return blocked(ctx)
			}}
			var root Agent = agent
			if path == "workflow" {
				rootName = "W"
				var err error
				root, err = NewSequentialAgent(ctx, &SequentialAgentConfig{Name: rootName, SubAgents: []Agent{agent}})
				require.NoError(t, err)
			}
			if path == "resume-transfer" {
				agent.run = func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
					return executionTestIter(&AgentEvent{Action: NewTransferToAgentAction("B")})
				}
				b := &executionTestResumable[*schema.Message]{executionTestAgent: &executionTestAgent[*schema.Message]{name: "B", run: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
					return executionTestIter(StatefulInterrupt(ctx, "initial", "state"))
				}}, resume: func(ctx context.Context, _ *ResumeInfo, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
					return blocked(ctx)
				}}
				var err error
				root, err = SetSubAgents(ctx, agent, []Agent{b})
				require.NoError(t, err)
			}
			runner := NewRunner(ctx, RunnerConfig{Agent: root, CheckPointStore: store})
			cb := make(chan *AsyncIterator[*AgentEvent], 1)
			handler := callbacks.NewHandlerBuilder().OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
				if info.Name == rootName {
					cb <- ConvAgentCallbackOutput(out).Events
				}
				return ctx
			}).Build()
			cancelOpt, cancel := WithCancel()
			var iter *AsyncIterator[*AgentEvent]
			if path == "resume-transfer" {
				executionTestCollect(t, runner.Query(ctx, "q", WithCheckPointID("cancel")))
				var err error
				iter, err = runner.Resume(ctx, "cancel", cancelOpt, WithCallbacks(handler))
				require.NoError(t, err)
			} else {
				iter = runner.Query(ctx, "q", cancelOpt, WithCheckPointID("cancel"), WithCallbacks(handler))
			}
			executionTestReceive(t, started)
			cbIter := executionTestReceive(t, cb)
			handle, requested := cancel(WithRecursive())
			require.True(t, requested)
			close(gate)
			events := executionTestCollect(t, iter)
			require.NoError(t, handle.Wait())
			require.NotEmpty(t, events)
			var cancelErr *CancelError
			require.ErrorAs(t, events[len(events)-1].Err, &cancelErr)
			cbEvents := executionTestCollect(t, cbIter)
			require.NotEmpty(t, cbEvents)
			last := cbEvents[len(cbEvents)-1]
			if path == "workflow" {
				require.ErrorAs(t, last.Err, &cancelErr)
			} else {
				require.NoError(t, last.Err)
				require.NotNil(t, last.Action.Interrupted)
			}
		})
	}
	t.Run("source EOF keeps continuation alive", func(t *testing.T) {
		ctx := context.Background()
		store := newCancelTestStore()
		started, gate := make(chan struct{}), make(chan struct{})
		a := &executionTestAgent[*schema.Message]{name: "A", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			return executionTestIter(&AgentEvent{Action: NewTransferToAgentAction("B")})
		}}
		b := &executionTestResumable[*schema.Message]{executionTestAgent: &executionTestAgent[*schema.Message]{name: "B", run: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			iter, gen := NewAsyncIteratorPair[*AgentEvent]()
			close(started)
			go func() { defer gen.Close(); <-gate; gen.Send(StatefulInterrupt(ctx, "B interrupted", "B-state")) }()
			return iter
		}}, resume: func(_ context.Context, info *ResumeInfo, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
			require.Equal(t, "B-state", info.InterruptState)
			return executionTestIter(EventFromMessage(schema.AssistantMessage("B resumed", nil), nil, schema.Assistant, ""))
		}}
		root, err := SetSubAgents(ctx, a, []Agent{b})
		require.NoError(t, err)
		cb := make(chan *AsyncIterator[*AgentEvent], 1)
		handler := callbacks.NewHandlerBuilder().OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, out callbacks.CallbackOutput) context.Context {
			if info.Name == "A" {
				cb <- ConvAgentCallbackOutput(out).Events
			}
			return ctx
		}).Build()
		cancelOpt, cancel := WithCancel()
		runner := NewRunner(ctx, RunnerConfig{Agent: root, CheckPointStore: store})
		iter := runner.Query(ctx, "q", cancelOpt, WithCheckPointID("continuation"), WithCallbacks(handler))
		cbIter := executionTestReceive(t, cb)
		executionTestReceive(t, started) // A is already at EOF and B has entered Run.
		first, ok := cbIter.Next()
		require.True(t, ok)
		require.Equal(t, "A", first.AgentName)
		cbDone := make(chan struct{})
		go func() { cbIter.Next(); close(cbDone) }()
		select {
		case <-cbDone:
			t.Fatal("source callback closed before continuation finished")
		default:
		}
		handle, requested := cancel(WithRecursive())
		require.True(t, requested, "the outer execution must still be alive after source EOF")
		close(gate)
		events := executionTestCollect(t, iter)
		require.NoError(t, handle.Wait())
		require.IsType(t, &CancelError{}, events[len(events)-1].Err)
		executionTestReceive(t, cbDone)
		resumed, err := runner.Resume(ctx, "continuation")
		require.NoError(t, err)
		events = executionTestCollect(t, resumed)
		require.Len(t, events, 1)
		require.Equal(t, "B", events[0].AgentName)
		require.Equal(t, "B resumed", events[0].Output.MessageOutput.Message.Content)
	})
}

func TestExecutionCompat_AgentToolBreakLoopPropagation(t *testing.T) {
	for _, scenario := range []struct {
		name         string
		emit, done   bool
		calls, after int
	}{{"emit", true, false, 1, 0}, {"hidden", false, false, 3, 3}, {"already-done", true, true, 3, 3}} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx := context.Background()
			innerCalls, parentCalls, afterCalls := 0, 0, 0
			inner := &executionTestAgent[*schema.Message]{name: "Inner", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				innerCalls++
				action := &AgentAction{BreakLoop: &BreakLoopAction{Done: scenario.done}}
				return executionTestIter(&AgentEvent{Action: action}, EventFromMessage(schema.AssistantMessage("inner-done", nil), nil, schema.Assistant, ""))
			}}
			innerTool := NewAgentTool(ctx, inner).(tool.InvokableTool)
			parent := &executionTestAgent[*schema.Message]{name: "Parent", run: func(ctx context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				parentCalls++
				iter, gen := NewAsyncIteratorPair[*AgentEvent]()
				go func() {
					defer gen.Close()
					var opts []tool.Option
					if scenario.emit {
						opts = append(opts, withAgentToolEventGenerator(gen))
					}
					_, err := innerTool.InvokableRun(ctx, `{"request":"q"}`, opts...)
					if err != nil {
						gen.Send(&AgentEvent{Err: err})
						return
					}
					gen.Send(EventFromMessage(schema.AssistantMessage("parent-done", nil), nil, schema.Assistant, ""))
				}()
				return iter
			}}
			after := &executionTestAgent[*schema.Message]{name: "After", run: func(context.Context, *AgentInput, ...AgentRunOption) *AsyncIterator[*AgentEvent] {
				afterCalls++
				return executionTestIter(&AgentEvent{})
			}}
			loop, err := NewLoopAgent(ctx, &LoopAgentConfig{Name: "Loop", SubAgents: []Agent{parent, after}, MaxIterations: 3})
			require.NoError(t, err)
			events := executionTestCollect(t, NewRunner(ctx, RunnerConfig{Agent: loop}).Query(ctx, "q"))
			require.Equal(t, scenario.calls, innerCalls)
			require.Equal(t, scenario.calls, parentCalls)
			require.Equal(t, scenario.after, afterCalls)
			var contents []string
			breaks := 0
			for _, event := range events {
				require.NoError(t, event.Err)
				if event.Output != nil {
					contents = append(contents, event.Output.MessageOutput.Message.Content)
				}
				if event.Action != nil && event.Action.BreakLoop != nil {
					breaks++
					require.True(t, event.Action.BreakLoop.Done)
					require.Zero(t, event.Action.BreakLoop.CurrentIterations)
				}
			}
			var expectedContents []string
			for i := 0; i < scenario.calls; i++ {
				if scenario.emit {
					expectedContents = append(expectedContents, "inner-done")
				}
				expectedContents = append(expectedContents, "parent-done")
			}
			require.ElementsMatch(t, expectedContents, contents)
			if scenario.emit {
				require.Equal(t, scenario.calls, breaks)
			} else {
				require.Zero(t, breaks)
			}
		})
	}
}
