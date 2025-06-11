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

package adk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestReact tests the newReact function with different scenarios
func TestReact(t *testing.T) {
	// Basic test for newReact function
	t.Run("Invoke", func(t *testing.T) {
		ctx := context.Background()

		// Create a fake tool for testing
		fakeTool := &fakeToolForTest{
			tarCount: 3,
		}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		times := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []Message, opts ...model.Option) (Message, error) {
				times++
				if times <= 2 {
					return schema.AssistantMessage("hello test",
							[]schema.ToolCall{
								{
									ID: randStrForTest(),
									Function: schema.FunctionCall{
										Name:      info.Name,
										Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStrForTest()),
									},
								},
							}),
						nil
				}

				return schema.AssistantMessage("bye", nil), nil
			}).AnyTimes()
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create a reactConfig
		config := &reactConfig{
			model: cm,
			toolsConfig: &compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			toolsReturnDirectly: map[string]bool{},
		}

		graph, err := newReact(ctx, config)
		assert.NoError(t, err)
		assert.NotNil(t, graph)

		compiled, err := graph.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, compiled)

		// Test with a user message
		result, err := compiled.Invoke(ctx, []Message{
			{
				Role:    schema.User,
				Content: "Use the test tool to say hello",
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)
	})

	// Test with toolsReturnDirectly
	t.Run("ToolsReturnDirectly", func(t *testing.T) {
		ctx := context.Background()

		// Create a fake tool for testing
		fakeTool := &fakeToolForTest{
			tarCount: 3,
		}

		info, err := fakeTool.Info(ctx)
		assert.NoError(t, err)

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		times := 0
		cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []Message, opts ...model.Option) (Message, error) {
				times++
				if times <= 2 {
					return schema.AssistantMessage("hello test",
							[]schema.ToolCall{
								{
									ID: randStrForTest(),
									Function: schema.FunctionCall{
										Name:      info.Name,
										Arguments: fmt.Sprintf(`{"name": "%s", "hh": "123"}`, randStrForTest()),
									},
								},
							}),
						nil
				}

				return schema.AssistantMessage("bye", nil), nil
			}).AnyTimes()
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create a reactConfig with toolsReturnDirectly
		config := &reactConfig{
			model: cm,
			toolsConfig: &compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
			toolsReturnDirectly: map[string]bool{info.Name: true},
		}

		graph, err := newReact(ctx, config)
		assert.NoError(t, err)
		assert.NotNil(t, graph)

		compiled, err := graph.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, compiled)

		// Test with a user message when tool returns directly
		result, err := compiled.Invoke(ctx, []Message{
			{
				Role:    schema.User,
				Content: "Use the test tool to say hello",
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, result)

		assert.Equal(t, result.Role, schema.Tool)
	})

	// Test streaming functionality
	t.Run("Stream", func(t *testing.T) {
		ctx := context.Background()

		// Create a fake tool for testing
		fakeTool := &fakeToolForTest{
			tarCount: 3,
		}

		fakeStreamTool := &fakeStreamToolForTest{
			tarCount: 3,
		}

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		times := 0
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []Message, opts ...model.Option) (
				MessageStream, error) {
				sr, sw := schema.Pipe[Message](1)
				defer sw.Close()

				info, _ := fakeTool.Info(ctx)
				streamInfo, _ := fakeStreamTool.Info(ctx)

				times++
				if times <= 1 {
					sw.Send(schema.AssistantMessage("hello test",
						[]schema.ToolCall{
							{
								ID: randStrForTest(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "tool"}`, randStrForTest()),
								},
							},
						}),
						nil)
					return sr, nil
				} else if times == 2 {
					sw.Send(schema.AssistantMessage("hello stream",
						[]schema.ToolCall{
							{
								ID: randStrForTest(),
								Function: schema.FunctionCall{
									Name:      streamInfo.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "stream tool"}`, randStrForTest()),
								},
							},
						}),
						nil)
					return sr, nil
				}

				sw.Send(schema.AssistantMessage("bye", nil), nil)
				return sr, nil
			}).AnyTimes()
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		// Create a reactConfig
		config := &reactConfig{
			model: cm,
			toolsConfig: &compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool, fakeStreamTool},
			},
			toolsReturnDirectly: map[string]bool{},
		}

		graph, err := newReact(ctx, config)
		assert.NoError(t, err)
		assert.NotNil(t, graph)

		compiled, err := graph.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, compiled)

		// Test streaming with a user message
		outStream, err := compiled.Stream(ctx, []Message{
			{
				Role:    schema.User,
				Content: "Use the test tool to say hello",
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, outStream)

		defer outStream.Close()

		msgs := make([]Message, 0)
		for {
			msg, err_ := outStream.Recv()
			if err_ != nil {
				if errors.Is(err_, io.EOF) {
					break
				}
				t.Fatal(err_)
			}

			msgs = append(msgs, msg)
		}

		assert.NotEmpty(t, msgs)
	})

	// Test streaming with toolsReturnDirectly
	t.Run("StreamWithToolsReturnDirectly", func(t *testing.T) {
		ctx := context.Background()

		// Create a fake tool for testing
		fakeTool := &fakeToolForTest{
			tarCount: 3,
		}

		fakeStreamTool := &fakeStreamToolForTest{
			tarCount: 3,
		}

		// Create a mock chat model
		ctrl := gomock.NewController(t)
		cm := mockModel.NewMockToolCallingChatModel(ctrl)

		// Set up expectations for the mock model
		times := 0
		cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
			DoAndReturn(func(ctx context.Context, input []Message, opts ...model.Option) (
				MessageStream, error) {
				sr, sw := schema.Pipe[Message](1)
				defer sw.Close()

				info, _ := fakeTool.Info(ctx)
				streamInfo, _ := fakeStreamTool.Info(ctx)

				times++
				if times <= 1 {
					sw.Send(schema.AssistantMessage("hello test",
						[]schema.ToolCall{
							{
								ID: randStrForTest(),
								Function: schema.FunctionCall{
									Name:      info.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "tool"}`, randStrForTest()),
								},
							},
						}),
						nil)
					return sr, nil
				} else if times == 2 {
					sw.Send(schema.AssistantMessage("hello stream",
						[]schema.ToolCall{
							{
								ID: randStrForTest(),
								Function: schema.FunctionCall{
									Name:      streamInfo.Name,
									Arguments: fmt.Sprintf(`{"name": "%s", "hh": "stream tool"}`, randStrForTest()),
								},
							},
						}),
						nil)
					return sr, nil
				}

				sw.Send(schema.AssistantMessage("bye", nil), nil)
				return sr, nil
			}).AnyTimes()
		cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

		streamInfo, err := fakeStreamTool.Info(ctx)
		assert.NoError(t, err)

		// Create a reactConfig with toolsReturnDirectly
		config := &reactConfig{
			model: cm,
			toolsConfig: &compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool, fakeStreamTool},
			},
			toolsReturnDirectly: map[string]bool{streamInfo.Name: true},
		}

		graph, err := newReact(ctx, config)
		assert.NoError(t, err)
		assert.NotNil(t, graph)

		compiled, err := graph.Compile(ctx)
		assert.NoError(t, err)
		assert.NotNil(t, compiled)

		// Reset times counter
		times = 0

		// Test streaming with a user message when tool returns directly
		outStream, err := compiled.Stream(ctx, []Message{
			{
				Role:    schema.User,
				Content: "Use the test tool to say hello",
			},
		})
		assert.NoError(t, err)
		assert.NotNil(t, outStream)

		msgs := make([]Message, 0)
		for {
			msg, err_ := outStream.Recv()
			if err_ != nil {
				if errors.Is(err_, io.EOF) {
					break
				}
				t.Fatal(err)
			}

			assert.Equal(t, msg.Role, schema.Tool)

			msgs = append(msgs, msg)
		}

		outStream.Close()

		assert.NotEmpty(t, msgs)
	})
}

// Helper types and functions for testing

type fakeStreamToolForTest struct {
	tarCount int
	curCount int
}

func (t *fakeStreamToolForTest) StreamableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (
	*schema.StreamReader[string], error) {
	p := &fakeToolInputForTest{}
	err := sonic.UnmarshalString(argumentsInJSON, p)
	if err != nil {
		return nil, err
	}

	if t.curCount >= t.tarCount {
		s := schema.StreamReaderFromArray([]string{`{"say": "bye"}`})
		return s, nil
	}
	t.curCount++
	s := schema.StreamReaderFromArray([]string{fmt.Sprintf(`{"say": "hello %v"}`, p.Name)})
	return s, nil
}

type fakeToolForTest struct {
	tarCount int
	curCount int
}

func (t *fakeToolForTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "test_tool",
		Desc: "test tool for unit testing",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Desc:     "user name for testing",
					Required: true,
					Type:     schema.String,
				},
			}),
	}, nil
}

func (t *fakeStreamToolForTest) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "test_stream_tool",
		Desc: "test stream tool for unit testing",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Desc:     "user name for testing",
					Required: true,
					Type:     schema.String,
				},
			}),
	}, nil
}

func (t *fakeToolForTest) InvokableRun(_ context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	p := &fakeToolInputForTest{}
	err := sonic.UnmarshalString(argumentsInJSON, p)
	if err != nil {
		return "", err
	}

	if t.curCount >= t.tarCount {
		return `{"say": "bye"}`, nil
	}

	t.curCount++
	return fmt.Sprintf(`{"say": "hello %v"}`, p.Name), nil
}

type fakeToolInputForTest struct {
	Name string `json:"name"`
}

func randStrForTest() string {
	seeds := []rune("test seed")
	b := make([]rune, 8)
	for i := range b {
		b[i] = seeds[rand.Intn(len(seeds))]
	}
	return string(b)
}
