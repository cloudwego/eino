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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestTaskTool(t *testing.T) {
	a1 := &myAgent{name: "1", desc: "desc of my agent 1"}
	a2 := &myAgent{name: "2", desc: "desc of my agent 2"}
	ctx := context.Background()
	tt, err := typedNewTaskTool(
		ctx,
		nil,
		[]adk.Agent{a1, a2},
		true,
		nil,
		"",
		adk.ToolsConfig{},
		10,
		nil,
		nil,
		nil,
		0,
	)
	assert.NoError(t, err)

	info, err := tt.Info(ctx)
	assert.NoError(t, err)
	assert.Contains(t, info.Desc, "desc of my agent 1")

	result, err := tt.InvokableRun(ctx, `{"subagent_type":"1"}`)
	assert.NoError(t, err)
	assert.Equal(t, "desc of my agent 1", result)
	result, err = tt.InvokableRun(ctx, `{"subagent_type":"2"}`)
	assert.NoError(t, err)
	assert.Equal(t, "desc of my agent 2", result)
}

func TestTaskToolTimeout(t *testing.T) {
	ctx := context.Background()
	agent := &blockingAgent{name: "blocking", desc: "blocking agent", canceled: make(chan struct{})}
	taskTool, err := typedNewTaskTool(
		ctx,
		nil,
		[]adk.Agent{agent},
		true,
		nil,
		"",
		adk.ToolsConfig{},
		10,
		nil,
		nil,
		nil,
		20*time.Millisecond,
	)
	assert.NoError(t, err)

	result, err := taskTool.InvokableRun(ctx, `{"subagent_type":"blocking","description":"wait"}`)
	assert.Empty(t, result)
	assert.True(t, errors.Is(err, context.DeadlineExceeded), "expected deadline exceeded, got %v", err)

	select {
	case <-agent.canceled:
	case <-time.After(time.Second):
		t.Fatal("sub-agent did not receive timeout cancellation")
	}
}

type myAgent struct {
	name string
	desc string
}

type blockingAgent struct {
	name     string
	desc     string
	canceled chan struct{}
}

func (a *blockingAgent) Name(context.Context) string        { return a.name }
func (a *blockingAgent) Description(context.Context) string { return a.desc }
func (a *blockingAgent) Run(ctx context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("done", nil), nil, schema.Assistant, ""))
	go func() { <-ctx.Done(); close(a.canceled) }()
	return it
}

func (m *myAgent) Name(_ context.Context) string {
	return m.name
}

func (m *myAgent) Description(_ context.Context) string {
	return m.desc
}

func (m *myAgent) Run(_ context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.UserMessage(m.desc), nil, schema.User, ""))
	gen.Close()
	return iter
}
