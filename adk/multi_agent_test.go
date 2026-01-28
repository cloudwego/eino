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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

type simpleTestAgent struct {
	name        string
	description string
	events      []*AgentEvent
}

func (a *simpleTestAgent) Name(_ context.Context) string        { return a.name }
func (a *simpleTestAgent) Description(_ context.Context) string { return a.description }
func (a *simpleTestAgent) Run(_ context.Context, _ *AgentInput, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		for _, e := range a.events {
			gen.Send(e)
		}
	}()
	return iter
}

type resumableTestAgent struct {
	simpleTestAgent
	resumeEvents []*AgentEvent
}

func (a *resumableTestAgent) Resume(_ context.Context, _ *ResumeInfo, _ ...AgentRunOption) *AsyncIterator[*AgentEvent] {
	iter, gen := NewAsyncIteratorPair[*AgentEvent]()
	go func() {
		defer gen.Close()
		for _, e := range a.resumeEvents {
			gen.Send(e)
		}
	}()
	return iter
}

func TestNewMultiAgent(t *testing.T) {
	ctx := context.Background()

	t.Run("wraps non-flowAgent in flowAgent", func(t *testing.T) {
		agent := &simpleTestAgent{name: "test", description: "test agent"}
		ma := NewMultiAgent(ctx, "multi", "multi agent", agent)

		assert.NotNil(t, ma)
		assert.Equal(t, "multi", ma.Name(context.Background()))
		assert.Equal(t, "multi agent", ma.Description(context.Background()))

		maImpl := ma.(*multiAgent)
		_, isFlowAgent := maImpl.agent.(*flowAgent)
		assert.True(t, isFlowAgent, "inner agent should be wrapped in flowAgent")
	})

	t.Run("creates new flowAgent from existing flowAgent", func(t *testing.T) {
		innerAgent := &simpleTestAgent{name: "inner", description: "inner agent"}
		fa := toFlowAgent(ctx, innerAgent)
		ma := NewMultiAgent(ctx, "multi", "multi agent", fa)

		assert.NotNil(t, ma)
		maImpl := ma.(*multiAgent)
		_, isFlowAgent := maImpl.agent.(*flowAgent)
		assert.True(t, isFlowAgent, "inner agent should be flowAgent")
	})
}

func TestMultiAgentGetType(t *testing.T) {
	ctx := context.Background()
	agent := &simpleTestAgent{name: "test", description: "test agent"}
	ma := NewMultiAgent(ctx, "multi", "multi agent", agent)

	maImpl := ma.(*multiAgent)
	assert.Equal(t, "MultiAgent", maImpl.GetType())
}

func TestMultiAgentNameAndDescription(t *testing.T) {
	ctx := context.Background()
	agent := &simpleTestAgent{name: "inner", description: "inner desc"}
	ma := NewMultiAgent(ctx, "outer", "outer desc", agent)

	assert.Equal(t, "outer", ma.Name(ctx))
	assert.Equal(t, "outer desc", ma.Description(ctx))
}

func TestMultiAgentRun(t *testing.T) {
	ctx := context.Background()
	events := []*AgentEvent{
		{AgentName: "test", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("hello", nil)}}},
	}
	agent := &simpleTestAgent{name: "test", description: "test agent", events: events}
	ma := NewMultiAgent(ctx, "multi", "multi agent", agent)

	input := &AgentInput{Messages: []Message{schema.UserMessage("test")}}

	iter := ma.Run(ctx, input)
	var receivedEvents []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		receivedEvents = append(receivedEvents, event)
	}

	assert.NotEmpty(t, receivedEvents)
}

func TestMultiAgentResume(t *testing.T) {
	t.Run("with resumable inner agent", func(t *testing.T) {
		ctx := context.Background()
		resumeEvents := []*AgentEvent{
			{AgentName: "test", Output: &AgentOutput{MessageOutput: &MessageVariant{Message: schema.AssistantMessage("resumed", nil)}}},
		}
		agent := &resumableTestAgent{
			simpleTestAgent: simpleTestAgent{name: "test", description: "test agent"},
			resumeEvents:    resumeEvents,
		}
		fa := toFlowAgent(ctx, agent)
		ma := NewMultiAgent(ctx, "multi", "multi agent", fa)

		info := &ResumeInfo{}

		iter := ma.Resume(ctx, info)
		var receivedEvents []*AgentEvent
		for {
			event, ok := iter.Next()
			if !ok {
				break
			}
			receivedEvents = append(receivedEvents, event)
		}

		assert.NotEmpty(t, receivedEvents)
	})

	t.Run("with non-resumable inner agent returns error", func(t *testing.T) {
		agent := &simpleTestAgent{name: "test", description: "test agent"}
		maImpl := &multiAgent{name: "multi", description: "multi agent", agent: agent}

		ctx := context.Background()
		info := &ResumeInfo{}

		iter := maImpl.Resume(ctx, info)
		event, ok := iter.Next()
		assert.True(t, ok)
		assert.NotNil(t, event.Err)
		assert.Contains(t, event.Err.Error(), "not resumable")
	})
}
