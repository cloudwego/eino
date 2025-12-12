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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestTaskTool(t *testing.T) {
	a1 := &myAgent{name: "1", desc: "desc of my agent 1"}
	a2 := &myAgent{name: "2", desc: "desc of my agent 2"}
	ctx := context.Background()
	tt, err := newTaskTool(
		ctx,
		nil,
		[]adk.Agent{a1, a2},
		true,
		nil,
		"",
		adk.ToolsConfig{},
		10,
		nil,
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

type myAgent struct {
	name string
	desc string
}

func (m *myAgent) Name(ctx context.Context) string {
	return m.name
}

func (m *myAgent) Description(ctx context.Context) string {
	return m.desc
}

func (m *myAgent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.UserMessage(m.desc), nil, schema.User, ""))
	gen.Close()
	return iter
}
