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
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func TestTaskTool(t *testing.T) {
	a1 := &myAgent{desc: "1"}
	a2 := &myAgent{desc: "2"}
	ctx := context.Background()
	tt, err := newTaskTool(
		ctx,
		nil,
		[]adk.Agent{a1, a2},
		false,
		AgentSetup{},
		nil,
	)
	assert.NoError(t, err)
	result, err := tt.InvokableRun(ctx, `{"subagent_type":"1"}`)
	assert.NoError(t, err)
	assert.Equal(t, "1", result)
	result, err = tt.InvokableRun(ctx, `{"subagent_type":"2"}`)
	assert.NoError(t, err)
	assert.Equal(t, "2", result)
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

func TestLoadAgentSetup(t *testing.T) {
	ctx := context.Background()
	cm := &mockToolCallingChatModel{}

	testTool, err := utils.InferTool("test tool", "", func(ctx context.Context, input string) (output string, err error) {
		return input, nil
	})
	assert.NoError(t, err)

	a, err := loadAgentSetup(
		ctx,
		AgentSetup{
			Model:        nil,
			Instruction:  "",
			ToolsConfig:  adk.ToolsConfig{},
			MaxIteration: 0,
		},
		"test name",
		"test desc",
		[]SetupHook{
			func(setup *AgentSetup) {
				setup.Model = cm
				setup.ToolsConfig.Tools = append(setup.ToolsConfig.Tools, testTool)
			},
		},
	)
	assert.NoError(t, err)
	_ = a.Run(ctx, &adk.AgentInput{})

	assert.Equal(t, 1, len(cm.tools))
	assert.Equal(t, "test name", a.Name(ctx))
	assert.Equal(t, "test desc", a.Description(ctx))
}

type mockToolCallingChatModel struct {
	tools []*schema.ToolInfo
}

func (m *mockToolCallingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("success", nil), nil
}

func (m *mockToolCallingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("success", nil)}), nil
}

func (m *mockToolCallingChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	cm := *m
	m.tools = append(m.tools, tools...)
	cm.tools = tools
	return &cm, nil
}
