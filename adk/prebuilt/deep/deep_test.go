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
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestTaskTool(t *testing.T) {
	a1 := &myAgent{desc: "1"}
	a2 := &myAgent{desc: "2"}
	ctx := context.Background()
	tt, err := newTaskTool(
		ctx,
		&myChatModel{},
		nil,
		[]adk.Agent{a1, a2},
	)
	assert.NoError(t, err)
	result, err := tt.InvokableRun(ctx, `{"subagent_type":"1"}`)
	assert.NoError(t, err)
	assert.Equal(t, "1", result)
	result, err = tt.InvokableRun(ctx, `{"subagent_type":"2"}`)
	assert.NoError(t, err)
	assert.Equal(t, "2", result)
}

type myChatModel struct{}

func (m *myChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	panic("implement me")
}

func (m *myChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("implement me")
}

func (m *myChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	panic("implement me")
}

type myAgent struct {
	desc string
}

func (m *myAgent) Name(ctx context.Context) string {
	return m.desc
}

func (m *myAgent) Description(ctx context.Context) string {
	return ""
}

func (m *myAgent) Run(ctx context.Context, input *adk.AgentInput, options ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.UserMessage(m.desc), nil, schema.User, ""))
	gen.Close()
	return iter
}
