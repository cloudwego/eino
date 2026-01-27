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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
)

func TestCopyEventIterator(t *testing.T) {
	t.Run("n=0 returns nil", func(t *testing.T) {
		iter, gen := NewAsyncIteratorPair[*AgentEvent]()
		go func() {
			gen.Send(&AgentEvent{AgentName: "test"})
			gen.Close()
		}()

		result := copyEventIterator(iter, 0)
		assert.Nil(t, result)
	})

	t.Run("n=1 returns original iterator", func(t *testing.T) {
		iter, gen := NewAsyncIteratorPair[*AgentEvent]()
		go func() {
			gen.Send(&AgentEvent{AgentName: "test"})
			gen.Close()
		}()

		result := copyEventIterator(iter, 1)
		assert.Len(t, result, 1)
		assert.Equal(t, iter, result[0])
	})

	t.Run("n>1 creates n independent copies", func(t *testing.T) {
		iter, gen := NewAsyncIteratorPair[*AgentEvent]()
		events := []*AgentEvent{
			{AgentName: "agent1", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("msg1", nil)}}},
			{AgentName: "agent2", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("msg2", nil)}}},
		}

		go func() {
			for _, e := range events {
				gen.Send(e)
			}
			gen.Close()
		}()

		n := 3
		copies := copyEventIterator(iter, n)
		assert.Len(t, copies, n)

		var wg sync.WaitGroup
		receivedEvents := make([][]*AgentEvent, n)

		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				for {
					event, ok := copies[idx].Next()
					if !ok {
						break
					}
					receivedEvents[idx] = append(receivedEvents[idx], event)
				}
			}(i)
		}

		wg.Wait()

		for i := 0; i < n; i++ {
			assert.Len(t, receivedEvents[i], len(events), "iterator %d should receive all events", i)
			for j, e := range receivedEvents[i] {
				assert.Equal(t, events[j].AgentName, e.AgentName)
			}
		}
	})
}

func TestCopyAgentCallbackOutput(t *testing.T) {
	t.Run("nil output", func(t *testing.T) {
		result := copyAgentCallbackOutput(nil, 3)
		assert.Len(t, result, 3)
		for _, r := range result {
			assert.Nil(t, r)
		}
	})

	t.Run("output with nil Events", func(t *testing.T) {
		out := &AgentCallbackOutput{Events: nil}
		result := copyAgentCallbackOutput(out, 3)
		assert.Len(t, result, 3)
		for _, r := range result {
			assert.Equal(t, out, r)
		}
	})

	t.Run("valid output with events", func(t *testing.T) {
		iter, gen := NewAsyncIteratorPair[*AgentEvent]()
		go func() {
			gen.Send(&AgentEvent{AgentName: "test"})
			gen.Close()
		}()

		out := &AgentCallbackOutput{Events: iter}
		result := copyAgentCallbackOutput(out, 2)
		assert.Len(t, result, 2)

		for i, r := range result {
			assert.NotNil(t, r, "result[%d] should not be nil", i)
			assert.NotNil(t, r.Events, "result[%d].Events should not be nil", i)
		}
	})
}

func TestConvAgentCallbackInput(t *testing.T) {
	t.Run("valid AgentCallbackInput", func(t *testing.T) {
		input := &AgentCallbackInput{
			Input: &AgentInput{Messages: []Message{schema.UserMessage("test")}},
		}
		result := ConvAgentCallbackInput(input)
		assert.Equal(t, input, result)
	})

	t.Run("invalid type returns nil", func(t *testing.T) {
		result := ConvAgentCallbackInput("invalid")
		assert.Nil(t, result)
	})

	t.Run("nil returns nil", func(t *testing.T) {
		result := ConvAgentCallbackInput(nil)
		assert.Nil(t, result)
	})
}

func TestConvAgentCallbackOutput(t *testing.T) {
	t.Run("valid AgentCallbackOutput", func(t *testing.T) {
		iter, _ := NewAsyncIteratorPair[*AgentEvent]()
		output := &AgentCallbackOutput{Events: iter}
		result := ConvAgentCallbackOutput(output)
		assert.Equal(t, output, result)
	})

	t.Run("invalid type returns nil", func(t *testing.T) {
		result := ConvAgentCallbackOutput("invalid")
		assert.Nil(t, result)
	})

	t.Run("nil returns nil", func(t *testing.T) {
		result := ConvAgentCallbackOutput(nil)
		assert.Nil(t, result)
	})
}

type mockTyperAgent struct {
	name      string
	agentType string
}

func (a *mockTyperAgent) Name(_ context.Context) string        { return a.name }
func (a *mockTyperAgent) Description(_ context.Context) string { return "mock agent" }
func (a *mockTyperAgent) GetType() string                      { return a.agentType }
func (a *mockTyperAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	gen.Close()
	return iter
}

type mockNonTyperAgent struct {
	name string
}

func (a *mockNonTyperAgent) Name(_ context.Context) string        { return a.name }
func (a *mockNonTyperAgent) Description(_ context.Context) string { return "mock agent" }
func (a *mockNonTyperAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	gen.Close()
	return iter
}

func TestGetAgentType(t *testing.T) {
	t.Run("agent implementing Typer", func(t *testing.T) {
		agent := &mockTyperAgent{name: "test", agentType: "CustomType"}
		result := getAgentType(agent)
		assert.Equal(t, "CustomType", result)
	})

	t.Run("agent not implementing Typer", func(t *testing.T) {
		agent := &mockNonTyperAgent{name: "test"}
		result := getAgentType(agent)
		assert.Equal(t, "", result)
	})
}

func TestWithCallbacksOption(t *testing.T) {
	handler := callbacks.NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			return ctx
		}).
		Build()

	opt := WithCallbacks(handler)
	opts := getCommonOptions(nil, opt)

	assert.Len(t, opts.handlers, 1)
}

func TestWithMultipleCallbacksOption(t *testing.T) {
	handler1 := callbacks.NewHandlerBuilder().Build()
	handler2 := callbacks.NewHandlerBuilder().Build()
	opt := WithCallbacks(handler1, handler2)

	opts := getCommonOptions(nil, opt)

	assert.Len(t, opts.handlers, 2)
}
