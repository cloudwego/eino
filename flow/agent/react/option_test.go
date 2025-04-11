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

package react

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestWithMessageFuture(t *testing.T) {
	ctx := context.Background()

	// Test with Generate
	t.Run("test with Generate", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Mock model response
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("test response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			MaxStep:          1,
		})
		assert.Nil(t, err)

		// Generate response
		response, err := a.Generate(ctx, []*schema.Message{
			schema.UserMessage("test input"),
		}, option)
		assert.Nil(t, err)
		assert.Equal(t, "test response", response.Content)

		// Get messages from future
		iter := future.GetMessages()

		// Verify messages
		msg, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "test response", msg.Content)

		// Should be no more messages
		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	// Test with Stream
	t.Run("test with Stream", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Mock model stream response
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (
				*schema.StreamReader[*schema.Message], error) {
				sr, sw := schema.Pipe[*schema.Message](1)
				defer sw.Close()

				sw.Send(schema.AssistantMessage("test stream response", nil), nil)
				return sr, nil
			}).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			MaxStep:          1,
		})
		assert.Nil(t, err)

		// Stream response
		stream, err := a.Stream(ctx, []*schema.Message{
			schema.UserMessage("test input"),
		}, option)
		assert.Nil(t, err)

		// Read all messages from stream
		var messages []*schema.Message
		for {
			msg, err := stream.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatal(err)
			}
			messages = append(messages, msg)
		}
		assert.Equal(t, 1, len(messages))
		assert.Equal(t, "test stream response", messages[0].Content)

		// Get message streams from future
		streamIter := future.GetMessageStreams()

		// Verify message streams
		msgStream, hasNext, err := streamIter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)

		// Read from the message stream
		msg, err := msgStream.Recv()
		assert.Nil(t, err)
		assert.Equal(t, "test stream response", msg.Content)

		// Should be EOF after reading the message
		_, err = msgStream.Recv()
		assert.Equal(t, io.EOF, err)

		// Should be no more streams
		_, hasNext, err = streamIter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})

	// Test with tool calls
	t.Run("test with tool calls", func(t *testing.T) {
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)
		fakeTool := &fakeToolGreetForTest{}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Mock model response with tool call
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("",
				[]schema.ToolCall{
					{
						ID: "tool-call-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "test user"}`,
						},
					},
				}), nil).
			Times(1)
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			Return(schema.AssistantMessage("final response", nil), nil).
			Times(1)
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create agent with MessageFuture
		option, future := WithMessageFuture()
		a, err := NewAgent(ctx, &AgentConfig{
			ToolCallingModel: cm,
			ToolsConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			MaxStep: 3,
		})
		assert.Nil(t, err)

		// Generate response
		response, err := a.Generate(ctx, []*schema.Message{
			schema.UserMessage("use the greet tool"),
		}, option)
		assert.Nil(t, err)
		assert.Equal(t, "final response", response.Content)

		// Get messages from future
		iter := future.GetMessages()

		// First message should be the assistant message for tool calling
		msg1, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Assistant, msg1.Role)
		assert.Equal(t, 1, len(msg1.ToolCalls))

		// Second message should be the tool response
		msg2, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, schema.Tool, msg2.Role)

		// Third message should be the final response
		msg3, hasNext, err := iter.Next()
		assert.Nil(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, "final response", msg3.Content)

		// Should be no more messages
		_, hasNext, err = iter.Next()
		assert.Nil(t, err)
		assert.False(t, hasNext)
	})
}
