/*
 * Copyright 2024 CloudWeGo Authors
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

package host

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/flow/agent"
	"github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/eino/utils/generic"
)

func TestHostMultiAgent(t *testing.T) {
	ctrl := gomock.NewController(t)
	mockHostLLM := model.NewMockChatModel(ctrl)
	mockSpecialistLLM1 := model.NewMockChatModel(ctrl)

	specialist1 := &Specialist{
		ChatModel:    mockSpecialistLLM1,
		SystemPrompt: "You are a helpful assistant.",
		AgentMeta: AgentMeta{
			Name:        "specialist 1",
			IntendedUse: "do stuff that works",
		},
	}

	specialist2 := &Specialist{
		Invokable: func(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (*schema.Message, error) {
			return &schema.Message{
				Role:    schema.Assistant,
				Content: "specialist2 invoke answer",
			}, nil
		},
		Streamable: func(ctx context.Context, input []*schema.Message, opts ...agent.AgentOption) (*schema.StreamReader[*schema.Message], error) {
			sr, sw := schema.Pipe[*schema.Message](0)
			go func() {
				sw.Send(&schema.Message{
					Role:    schema.Assistant,
					Content: "specialist2 stream answer",
				}, nil)
				sw.Close()
			}()
			return sr, nil
		},
		AgentMeta: AgentMeta{
			Name:        "specialist 2",
			IntendedUse: "do stuff that works too",
		},
	}

	ctx := context.Background()

	mockHostLLM.EXPECT().BindTools(gomock.Any()).Return(nil).AnyTimes()

	hostMA, err := NewMultiAgent(ctx, &MultiAgentConfig{
		Host: Host{
			ChatModel: mockHostLLM,
		},
		Specialists: []*Specialist{
			specialist1,
			specialist2,
		},
	})

	assert.NoError(t, err)

	t.Run("generate direct answer from host", func(t *testing.T) {
		directAnswerMsg := &schema.Message{
			Role:    schema.Assistant,
			Content: "direct answer",
		}

		mockHostLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(directAnswerMsg, nil).Times(1)

		mockCallback := &mockAgentCallback{}

		out, err := hostMA.Generate(ctx, nil, WithAgentCallbacks(mockCallback))
		assert.NoError(t, err)
		assert.Equal(t, "direct answer", out.Content)
		assert.Empty(t, mockCallback.infos)
	})

	t.Run("stream direct answer from host", func(t *testing.T) {
		directAnswerMsg1 := &schema.Message{
			Role:    schema.Assistant,
			Content: "direct ",
		}

		directAnswerMsg2 := &schema.Message{
			Role:    schema.Assistant,
			Content: "answer",
		}

		sr, sw := schema.Pipe[*schema.Message](0)
		go func() {
			sw.Send(directAnswerMsg1, nil)
			sw.Send(directAnswerMsg2, nil)
			sw.Close()
		}()

		mockHostLLM.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)

		mockCallback := &mockAgentCallback{}
		outStream, err := hostMA.Stream(ctx, nil, WithAgentCallbacks(mockCallback))
		assert.NoError(t, err)
		assert.Empty(t, mockCallback.infos)

		var msgs []*schema.Message
		for {
			msg, err := outStream.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			msgs = append(msgs, msg)
		}

		outStream.Close()

		msg, err := schema.ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, "direct answer", msg.Content)
	})

	t.Run("generate hand off", func(t *testing.T) {
		handOffMsg := &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					Index: generic.PtrOf(0),
					Function: schema.FunctionCall{
						Name:      specialist1.Name,
						Arguments: `{"reason": "specialist 1 is the best"}`,
					},
				},
			},
		}

		specialistMsg := &schema.Message{
			Role:    schema.Assistant,
			Content: "specialist 1 answer",
		}

		mockHostLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(handOffMsg, nil).Times(1)
		mockSpecialistLLM1.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(specialistMsg, nil).Times(1)

		mockCallback := &mockAgentCallback{}

		out, err := hostMA.Generate(ctx, nil, WithAgentCallbacks(mockCallback))
		assert.NoError(t, err)
		assert.Equal(t, "specialist 1 answer", out.Content)
		assert.Equal(t, []*HandOffInfo{
			{
				ToAgentName: specialist1.Name,
				Argument:    `{"reason": "specialist 1 is the best"}`,
			},
		}, mockCallback.infos)

		handOffMsg.ToolCalls[0].Function.Name = specialist2.Name
		handOffMsg.ToolCalls[0].Function.Arguments = `{"reason": "specialist 2 is even better"}`
		mockHostLLM.EXPECT().Generate(gomock.Any(), gomock.Any()).Return(handOffMsg, nil).Times(1)

		mockCallback = &mockAgentCallback{}

		out, err = hostMA.Generate(ctx, nil, WithAgentCallbacks(mockCallback))
		assert.NoError(t, err)
		assert.Equal(t, "specialist2 invoke answer", out.Content)
		assert.Equal(t, []*HandOffInfo{
			{
				ToAgentName: specialist2.Name,
				Argument:    `{"reason": "specialist 2 is even better"}`,
			},
		}, mockCallback.infos)
	})

	t.Run("stream hand off to chat model", func(t *testing.T) {
		handOffMsg1 := &schema.Message{
			Role: schema.Assistant,
		}

		handOffMsg2 := &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					Index: generic.PtrOf(0),
				},
			},
		}

		handOffMsg3 := &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					Index:    generic.PtrOf(0),
					Function: schema.FunctionCall{},
				},
			},
		}

		handOffMsg4 := &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					Index: generic.PtrOf(0),
					Function: schema.FunctionCall{
						Name:      specialist1.Name,
						Arguments: `{"reason": "specialist 1 is the best"}`,
					},
				},
			},
		}

		sr, sw := schema.Pipe[*schema.Message](0)
		go func() {
			sw.Send(handOffMsg1, nil)
			sw.Send(handOffMsg2, nil)
			sw.Send(handOffMsg3, nil)
			sw.Send(handOffMsg4, nil)
			sw.Close()
		}()

		specialistMsg1 := &schema.Message{
			Role:    schema.Assistant,
			Content: "specialist ",
		}

		specialistMsg2 := &schema.Message{
			Role:    schema.Assistant,
			Content: "1 answer",
		}

		sr1, sw1 := schema.Pipe[*schema.Message](0)
		go func() {
			sw1.Send(specialistMsg1, nil)
			sw1.Send(specialistMsg2, nil)
			sw1.Close()
		}()

		mockHostLLM.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)
		mockSpecialistLLM1.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(sr1, nil).Times(1)

		mockCallback := &mockAgentCallback{}
		outStream, err := hostMA.Stream(ctx, nil, WithAgentCallbacks(mockCallback))
		assert.NoError(t, err)

		var msgs []*schema.Message
		for {
			msg, err := outStream.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			msgs = append(msgs, msg)
		}

		outStream.Close()

		msg, err := schema.ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, "specialist 1 answer", msg.Content)

		assert.Equal(t, []*HandOffInfo{
			{
				ToAgentName: specialist1.Name,
				Argument:    `{"reason": "specialist 1 is the best"}`,
			},
		}, mockCallback.infos)

		handOffMsg4.ToolCalls[0].Function.Name = specialist2.Name
		handOffMsg4.ToolCalls[0].Function.Arguments = `{"reason": "specialist 2 is even better"}`
		sr, sw = schema.Pipe[*schema.Message](0)
		go func() {
			sw.Send(handOffMsg1, nil)
			sw.Send(handOffMsg2, nil)
			sw.Send(handOffMsg3, nil)
			sw.Send(handOffMsg4, nil)
			sw.Close()
		}()

		mockHostLLM.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(sr, nil).Times(1)

		mockCallback = &mockAgentCallback{}
		outStream, err = hostMA.Stream(ctx, nil, WithAgentCallbacks(mockCallback))
		assert.NoError(t, err)

		msgs = nil
		for {
			msg, err := outStream.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			msgs = append(msgs, msg)
		}

		outStream.Close()

		msg, err = schema.ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, "specialist2 stream answer", msg.Content)

		assert.Equal(t, []*HandOffInfo{
			{
				ToAgentName: specialist2.Name,
				Argument:    `{"reason": "specialist 2 is even better"}`,
			},
		}, mockCallback.infos)
	})
}

type mockAgentCallback struct {
	infos []*HandOffInfo
}

func (m *mockAgentCallback) OnHandOff(ctx context.Context, info *HandOffInfo) context.Context {
	m.infos = append(m.infos, info)
	return ctx
}