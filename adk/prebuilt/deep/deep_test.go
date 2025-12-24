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
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWriteTodos(t *testing.T) {
	m, err := buildBuiltinAgentMiddlewares(false)
	assert.NoError(t, err)

	wt := m[0].AdditionalTools[0].(tool.InvokableTool)

	todos := `[{"content":"content1","status":"pending"},{"content":"content2","status":"pending"}]`
	args := fmt.Sprintf(`{"todos": %s}`, todos)

	result, err := wt.InvokableRun(context.Background(), args)
	assert.NoError(t, err)
	assert.Equal(t, fmt.Sprintf("Updated todo list to %s", todos), result)
}

func TestDeepSubAgentSharesSessionValues(t *testing.T) {
	ctx := context.Background()
	spy := &spySubAgent{}

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	calls := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			calls++
			if calls == 1 {
				c := schema.ToolCall{ID: "id-1", Type: "function"}
				c.Function.Name = taskToolName
				c.Function.Arguments = fmt.Sprintf(`{"subagent_type":"%s","description":"from_parent"}`, spy.Name(ctx))
				return schema.AssistantMessage("", []schema.ToolCall{c}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()

	agent, err := New(ctx, &Config{
		Name:                   "deep",
		Description:            "deep agent",
		ChatModel:              cm,
		Instruction:            "you are deep agent",
		SubAgents:              []adk.Agent{spy},
		ToolsConfig:            adk.ToolsConfig{},
		MaxIteration:           2,
		WithoutWriteTodos:      true,
		WithoutGeneralSubAgent: true,
	})
	assert.NoError(t, err)

	r := adk.NewRunner(ctx, adk.RunnerConfig{Agent: agent})
	it := r.Run(ctx, []adk.Message{schema.UserMessage("hi")}, adk.WithSessionValues(map[string]any{"parent_key": "parent_val"}))
	for {
		if _, ok := it.Next(); !ok {
			break
		}
	}

	assert.Equal(t, "parent_val", spy.seenParentValue)
}

type spySubAgent struct {
	seenParentValue any
}

func (s *spySubAgent) Name(context.Context) string        { return "spy-subagent" }
func (s *spySubAgent) Description(context.Context) string { return "spy" }
func (s *spySubAgent) Run(ctx context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	s.seenParentValue, _ = adk.GetSessionValue(ctx, "parent_key")
	it, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	gen.Send(adk.EventFromMessage(schema.AssistantMessage("ok", nil), nil, schema.Assistant, ""))
	gen.Close()
	return it
}
