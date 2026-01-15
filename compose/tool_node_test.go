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

package compose

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

const (
	toolNameOfUserCompany = "user_company"
	toolIDOfUserCompany   = "call_TRZhlagwBS0LpWbWPeZOvIXc"

	toolNameOfUserSalary = "user_salary"
	toolIDOfUserSalary   = "call_AqfoRW6fuF98k0o7696k2nzm"
)

func TestToolsNode(t *testing.T) {
	var err error
	ctx := context.Background()

	userCompanyToolInfo := &schema.ToolInfo{
		Name: toolNameOfUserCompany,
		Desc: "Query user's company and position information based on user's name and email",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Type: "string",
					Desc: "User's name",
				},
				"email": {
					Type: "string",
					Desc: "User's email",
				},
			}),
	}

	userSalaryToolInfo := &schema.ToolInfo{
		Name: toolNameOfUserSalary,
		Desc: "Query user's salary information based on user's name and email",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Type: "string",
					Desc: "User's name",
				},
				"email": {
					Type: "string",
					Desc: "User's email",
				},
			}),
	}

	t.Run("success", func(t *testing.T) {
		const (
			nodeOfTools = "tools"
			nodeOfModel = "model"
		)
		g := NewGraph[[]*schema.Message, []*schema.Message]()

		err = g.AddChatModelNode(nodeOfModel, &mockIntentChatModel{})
		assert.NoError(t, err)

		ui := newTool(userCompanyToolInfo, queryUserCompany)
		us := newStreamableTool(userSalaryToolInfo, queryUserSalary)

		toolsNode, err := NewToolNode(ctx, &ToolsNodeConfig{
			Tools: []tool.BaseTool{ui, us},
		})
		assert.NoError(t, err)

		err = g.AddToolsNode(nodeOfTools, toolsNode)
		assert.NoError(t, err)

		err = g.AddEdge(START, nodeOfModel)
		assert.NoError(t, err)

		err = g.AddEdge(nodeOfModel, nodeOfTools)
		assert.NoError(t, err)

		err = g.AddEdge(nodeOfTools, END)
		assert.NoError(t, err)

		r, err := g.Compile(ctx)
		assert.NoError(t, err)

		out, err := r.Invoke(ctx, []*schema.Message{})
		assert.NoError(t, err)

		msg := findMsgByToolCallID(out, toolIDOfUserCompany)
		assert.Equal(t, toolIDOfUserCompany, msg.ToolCallID)
		assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","gender":"male","company":"bytedance","position":"CEO"}`,
			msg.Content)

		msg = findMsgByToolCallID(out, toolIDOfUserSalary)
		assert.Equal(t, toolIDOfUserSalary, msg.ToolCallID)
		assert.Contains(t, msg.Content,
			`{"user_id":"zhangsan-zhangsan@bytedance.com","salary":5000}{"user_id":"zhangsan-zhangsan@bytedance.com","salary":3000}{"user_id":"zhangsan-zhangsan@bytedance.com","salary":2000}`)

		// 测试流式调用
		reader, err := r.Stream(ctx, []*schema.Message{})
		assert.NoError(t, err)
		loops := 0
		userSalaryTimes := 0

		defer reader.Close()

		var arrMsgs [][]*schema.Message
		for ; loops < 10; loops++ {
			msgs, err := reader.Recv()
			if err == io.EOF {
				break
			}

			arrMsgs = append(arrMsgs, msgs)

			assert.NoError(t, err)

			assert.Len(t, msgs, 2)
			if msg := findMsgByToolCallID(out, toolIDOfUserCompany); msg != nil {
				assert.Equal(t, schema.Tool, msg.Role)
				assert.Equal(t, toolIDOfUserCompany, msg.ToolCallID)
				assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","gender":"male","company":"bytedance","position":"CEO"}`,
					msg.Content)
			} else if msg := findMsgByToolCallID(out, toolIDOfUserSalary); msg != nil {
				assert.Equal(t, schema.Tool, msg.Role)
				assert.Equal(t, toolIDOfUserSalary, msg.ToolCallID)

				switch userSalaryTimes {
				case 0:
					assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","salary":5000}`,
						msg.Content)
				case 1:
					assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","salary":3000}`,
						msg.Content)
				case 2:
					assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","salary":2000}`,
						msg.Content)
				}

				userSalaryTimes++
			} else {
				assert.Fail(t, "unexpected tool name")
			}
		}

		assert.Equal(t, 4, loops)

		msgs, err_ := schema.ConcatMessageArray(arrMsgs)
		assert.NoError(t, err_)
		msg = findMsgByToolCallID(msgs, toolIDOfUserCompany)
		msg = findMsgByToolCallID(msgs, toolIDOfUserSalary)

		sr, sw := schema.Pipe[[]*schema.Message](2)
		sw.Send([]*schema.Message{
			{
				Role:    schema.User,
				Content: `hi, how are you`,
			},
		}, nil)
		sw.Send([]*schema.Message{
			{
				Role:    schema.User,
				Content: `i'm fine'`,
			},
		}, nil)
		sw.Close()

		reader, err = r.Transform(ctx, sr)
		assert.NoError(t, err)

		defer reader.Close()

		loops = 0
		userSalaryTimes = 0

		for ; loops < 10; loops++ {
			msgs, err := reader.Recv()
			if err == io.EOF {
				break
			}

			assert.NoError(t, err)

			assert.Len(t, msgs, 2)
			if msg := findMsgByToolCallID(out, toolIDOfUserCompany); msg != nil {
				assert.Equal(t, schema.Tool, msg.Role)
				assert.Equal(t, toolIDOfUserCompany, msg.ToolCallID)
				assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","gender":"male","company":"bytedance","position":"CEO"}`,
					msg.Content)
			} else if msg := findMsgByToolCallID(out, toolIDOfUserSalary); msg != nil {
				assert.Equal(t, schema.Tool, msg.Role)
				assert.Equal(t, toolIDOfUserSalary, msg.ToolCallID)

				switch userSalaryTimes {
				case 0:
					assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","salary":5000}`,
						msg.Content)
				case 1:
					assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","salary":3000}`,
						msg.Content)
				case 2:
					assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","salary":2000}`,
						msg.Content)
				}

				userSalaryTimes++
			} else {
				assert.Fail(t, "unexpected tool name")
			}
		}

		assert.Equal(t, 4, loops)
	})

	t.Run("order_consistency", func(t *testing.T) {
		// Create a ToolsNode with multiple tools
		ui := newTool(userCompanyToolInfo, queryUserCompany)
		us := newTool(userSalaryToolInfo, queryUserSalary)

		toolsNode, err_ := NewToolNode(context.Background(), &ToolsNodeConfig{
			Tools: []tool.BaseTool{ui, us},
		})
		assert.NoError(t, err_)

		// Create an input message with multiple tool calls in a specific order
		input := &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: toolIDOfUserSalary,
					Function: schema.FunctionCall{
						Name:      toolNameOfUserSalary,
						Arguments: `{"name": "zhangsan", "email": "zhangsan@bytedance.com"}`,
					},
				},
				{
					ID: toolIDOfUserCompany,
					Function: schema.FunctionCall{
						Name:      toolNameOfUserCompany,
						Arguments: `{"name": "zhangsan", "email": "zhangsan@bytedance.com"}`,
					},
				},
			},
		}

		// Invoke the ToolsNode
		output, err_ := toolsNode.Invoke(context.Background(), input)
		assert.NoError(t, err_)

		// Verify the order of output messages matches the order of input tool calls
		assert.Equal(t, 2, len(output))
		assert.Equal(t, toolIDOfUserSalary, output[0].ToolCallID)
		assert.Equal(t, toolIDOfUserCompany, output[1].ToolCallID)

		// Test with Stream method as well
		streamer, err_ := toolsNode.Stream(context.Background(), input)
		assert.NoError(t, err_)
		defer streamer.Close()

		// Collect all stream outputs
		var streamOutputs [][]*schema.Message
		for {
			chunk, err__ := streamer.Recv()
			if err__ == io.EOF {
				break
			}
			assert.NoError(t, err__)
			streamOutputs = append(streamOutputs, chunk)
		}

		// Verify each chunk maintains the correct order
		for _, chunk := range streamOutputs {
			if chunk[0] != nil {
				assert.Equal(t, toolIDOfUserSalary, chunk[0].ToolCallID)
			}
			if chunk[1] != nil {
				assert.Equal(t, toolIDOfUserCompany, chunk[1].ToolCallID)
			}
		}

		// Concatenate all stream outputs and verify final result
		concatenated, err_ := schema.ConcatMessageArray(streamOutputs)
		assert.NoError(t, err_)
		assert.Equal(t, 2, len(concatenated))
		assert.Equal(t, toolIDOfUserSalary, concatenated[0].ToolCallID)
		assert.Equal(t, toolIDOfUserCompany, concatenated[1].ToolCallID)
	})
}

type userCompanyRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type userCompanyResponse struct {
	UserID   string `json:"user_id"`
	Gender   string `json:"gender"`
	Company  string `json:"company"`
	Position string `json:"position"`
}

func queryUserCompany(ctx context.Context, req *userCompanyRequest) (resp *userCompanyResponse, err error) {
	callID := GetToolCallID(ctx)
	if callID != toolIDOfUserCompany {
		return nil, fmt.Errorf("invalid tool call id= %s", callID)
	}

	return &userCompanyResponse{
		UserID:   fmt.Sprintf("%v-%v", req.Name, req.Email),
		Gender:   "male",
		Company:  "bytedance",
		Position: "CEO",
	}, nil
}

type userSalaryRequest struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type userSalaryResponse struct {
	UserID string `json:"user_id"`
	Salary int    `json:"salary"`
}

func queryUserSalary(ctx context.Context, req *userSalaryRequest) (resp *schema.StreamReader[*userSalaryResponse], err error) {
	callID := GetToolCallID(ctx)
	if callID != toolIDOfUserSalary {
		return nil, fmt.Errorf("invalid tool call id= %s", callID)
	}

	sr, sw := schema.Pipe[*userSalaryResponse](10)
	sw.Send(&userSalaryResponse{
		UserID: fmt.Sprintf("%v-%v", req.Name, req.Email),
		Salary: 5000,
	}, nil)

	sw.Send(&userSalaryResponse{
		UserID: fmt.Sprintf("%v-%v", req.Name, req.Email),
		Salary: 3000,
	}, nil)

	sw.Send(&userSalaryResponse{
		UserID: fmt.Sprintf("%v-%v", req.Name, req.Email),
		Salary: 2000,
	}, nil)
	sw.Close()
	return sr, nil
}

type mockIntentChatModel struct{}

func (m *mockIntentChatModel) BindTools(tools []*schema.ToolInfo) error {
	return nil
}

func (m *mockIntentChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{
		Role:    schema.Assistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{
				ID: toolIDOfUserCompany,
				Function: schema.FunctionCall{
					Name:      toolNameOfUserCompany,
					Arguments: `{"name": "zhangsan", "email": "zhangsan@bytedance.com"}`,
				},
			},
			{
				ID: toolIDOfUserSalary,
				Function: schema.FunctionCall{
					Name:      toolNameOfUserSalary,
					Arguments: `{"name": "zhangsan", "email": "zhangsan@bytedance.com"}`,
				},
			},
		},
	}, nil
}

func (m *mockIntentChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	sr, sw := schema.Pipe[*schema.Message](2)
	sw.Send(&schema.Message{
		Role:    schema.Assistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{
				ID: toolIDOfUserCompany,
				Function: schema.FunctionCall{
					Name:      toolNameOfUserCompany,
					Arguments: `{"name": "zhangsan", "email": "zhangsan@bytedance.com"}`,
				},
			},
		},
	}, nil)

	sw.Send(&schema.Message{
		Role:    schema.Assistant,
		Content: "",
		ToolCalls: []schema.ToolCall{
			{
				ID: toolIDOfUserSalary,
				Function: schema.FunctionCall{
					Name:      toolNameOfUserSalary,
					Arguments: `{"name": "zhangsan", "email": "zhangsan@bytedance.com"}`,
				},
			},
		},
	}, nil)

	sw.Close()

	return sr, nil
}

func TestToolsNodeOptions(t *testing.T) {
	ctx := context.Background()

	t.Run("tool_option", func(t *testing.T) {

		g := NewGraph[*schema.Message, []*schema.Message]()

		mt := &mockTool{}

		tn, err := NewToolNode(ctx, &ToolsNodeConfig{
			Tools: []tool.BaseTool{mt},
		})
		assert.NoError(t, err)

		err = g.AddToolsNode("tools", tn)
		assert.NoError(t, err)

		err = g.AddEdge(START, "tools")
		assert.NoError(t, err)
		err = g.AddEdge("tools", END)
		assert.NoError(t, err)

		r, err := g.Compile(ctx)
		assert.NoError(t, err)

		out, err := r.Invoke(ctx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: toolIDOfUserCompany,
					Function: schema.FunctionCall{
						Name:      "mock_tool",
						Arguments: `{"name": "jack"}`,
					},
				},
			},
		}, WithToolsNodeOption(WithToolOption(WithAge(10))))
		assert.NoError(t, err)
		assert.Len(t, out, 1)
		assert.JSONEq(t, `{"echo": "jack: 10"}`, out[0].Content)

		outMessages := make([][]*schema.Message, 0)
		outStream, err := r.Stream(ctx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: toolIDOfUserCompany,
					Function: schema.FunctionCall{
						Name:      "mock_tool",
						Arguments: `{"name": "jack"}`,
					},
				},
			},
		}, WithToolsNodeOption(WithToolOption(WithAge(10))))

		assert.NoError(t, err)

		for {
			msgs, err := outStream.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			outMessages = append(outMessages, msgs)
		}
		outStream.Close()

		msgs, err := internal.ConcatItems(outMessages)
		assert.NoError(t, err)

		assert.Len(t, msgs, 1)
		assert.JSONEq(t, `{"echo":"jack: 10"}`, msgs[0].Content)
	})
	t.Run("tool_list", func(t *testing.T) {

		g := NewGraph[*schema.Message, []*schema.Message]()

		mt := &mockTool{}

		tn, err := NewToolNode(ctx, &ToolsNodeConfig{
			Tools: []tool.BaseTool{},
		})
		assert.NoError(t, err)

		err = g.AddToolsNode("tools", tn)
		assert.NoError(t, err)

		err = g.AddEdge(START, "tools")
		assert.NoError(t, err)
		err = g.AddEdge("tools", END)
		assert.NoError(t, err)

		r, err := g.Compile(ctx)
		assert.NoError(t, err)

		out, err := r.Invoke(ctx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: toolIDOfUserCompany,
					Function: schema.FunctionCall{
						Name:      "mock_tool",
						Arguments: `{"name": "jack"}`,
					},
				},
			},
		}, WithToolsNodeOption(WithToolList(mt), WithToolOption(WithAge(10))))
		assert.NoError(t, err)
		assert.Len(t, out, 1)
		assert.JSONEq(t, `{"echo": "jack: 10"}`, out[0].Content)

		outMessages := make([][]*schema.Message, 0)
		outStream, err := r.Stream(ctx, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: toolIDOfUserCompany,
					Function: schema.FunctionCall{
						Name:      "mock_tool",
						Arguments: `{"name": "jack"}`,
					},
				},
			},
		}, WithToolsNodeOption(WithToolList(mt), WithToolOption(WithAge(10))))

		assert.NoError(t, err)

		for {
			msgs, err := outStream.Recv()
			if err == io.EOF {
				break
			}
			assert.NoError(t, err)
			outMessages = append(outMessages, msgs)
		}
		outStream.Close()

		msgs, err := internal.ConcatItems(outMessages)
		assert.NoError(t, err)

		assert.Len(t, msgs, 1)
		assert.JSONEq(t, `{"echo":"jack: 10"}`, msgs[0].Content)
	})

}

func findMsgByToolCallID(msgs []*schema.Message, toolCallID string) *schema.Message {
	for _, msg := range msgs {
		if msg.ToolCallID == toolCallID {
			return msg
		}
	}

	return nil
}

type mockToolOptions struct {
	Age int
}

func WithAge(age int) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *mockToolOptions) {
		o.Age = age
	})
}

type mockToolRequest struct {
	Name string `json:"name"`
}

type mockToolResponse struct {
	Echo string `json:"echo"`
}

type mockTool struct{}

func (m *mockTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "mock_tool",
		Desc: "mock tool",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Type:     "string",
					Desc:     "name",
					Required: true,
				},
			}),
	}, nil
}

func (m *mockTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	opt := tool.GetImplSpecificOptions(&mockToolOptions{}, opts...)

	req := &mockToolRequest{}

	if e := sonic.UnmarshalString(argumentsInJSON, req); e != nil {
		return "", e
	}

	resp := &mockToolResponse{
		Echo: fmt.Sprintf("%v: %v", req.Name, opt.Age),
	}

	return sonic.MarshalString(resp)
}

func (m *mockTool) StreamableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	sr, sw := schema.Pipe[string](1)
	go func() {
		defer sw.Close()

		opt := tool.GetImplSpecificOptions(&mockToolOptions{}, opts...)

		req := &mockToolRequest{}

		if e := sonic.UnmarshalString(argumentsInJSON, req); e != nil {
			sw.Send("", e)
			return
		}

		resp := mockToolResponse{
			Echo: fmt.Sprintf("%v: %v", req.Name, opt.Age),
		}

		output, err := sonic.MarshalString(resp)
		if err != nil {
			sw.Send("", err)
			return
		}

		for i := 0; i < len(output); i++ {
			sw.Send(string(output[i]), nil)
		}
	}()

	return sr, nil
}

func TestUnknownTool(t *testing.T) {
	ctx := context.Background()
	tn, err := NewToolNode(ctx, &ToolsNodeConfig{
		Tools: nil,
		UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
			return "unknown", nil
		},
	})
	assert.NoError(t, err)

	input := &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{
				ID: "1",
				Function: schema.FunctionCall{
					Name:      "unknown1",
					Arguments: `arg1`,
				},
			},
			{
				ID: "2",
				Function: schema.FunctionCall{
					Name:      "unknown2",
					Arguments: `arg2`,
				},
			},
		},
	}

	expected := []*schema.Message{
		{
			Role:       schema.Tool,
			Content:    "unknown",
			ToolCallID: "1",
			ToolName:   "unknown1",
		},
		{
			Role:       schema.Tool,
			Content:    "unknown",
			ToolCallID: "2",
			ToolName:   "unknown2",
		},
	}

	result, err := tn.Invoke(ctx, input)
	assert.NoError(t, err)
	assert.Equal(t, expected, result)

	streamResult, err := tn.Stream(ctx, input)
	assert.NoError(t, err)
	result = make([]*schema.Message, 2)
	for {
		chunk, err := streamResult.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		for i := range chunk {
			if chunk[i] != nil {
				result[i] = chunk[i]
			}
		}
	}
	assert.Equal(t, expected, result)
}

func TestToolRerun(t *testing.T) {
	type myToolRerunState struct {
		In *schema.Message
	}

	schema.Register[myToolRerunState]()

	tc := []schema.ToolCall{
		{
			ID: "3",
			Function: schema.FunctionCall{
				Name:      "tool3",
				Arguments: "input",
			},
		},
		{
			ID: "4",
			Function: schema.FunctionCall{
				Name:      "tool4",
				Arguments: "input",
			},
		},
		{
			ID: "1",
			Function: schema.FunctionCall{
				Name:      "tool1",
				Arguments: "input",
			},
		},
		{
			ID: "2",
			Function: schema.FunctionCall{
				Name:      "tool2",
				Arguments: "input",
			},
		},
	}
	g := NewGraph[*schema.Message, string](WithGenLocalState(func(ctx context.Context) (state *myToolRerunState) {
		return &myToolRerunState{In: &schema.Message{Role: schema.Assistant, ToolCalls: tc}}
	}))
	ctx := context.Background()
	tn, err := NewToolNode(ctx, &ToolsNodeConfig{
		Tools: []tool.BaseTool{&myTool1{}, &myTool2{}, &myTool3{t: t}, &myTool4{t: t}},
	})
	assert.NoError(t, err)
	assert.NoError(t, g.AddToolsNode("tool node", tn))
	assert.NoError(t, g.AddLambdaNode("lambda", InvokableLambda(func(ctx context.Context, input []*schema.Message) (output string, err error) {
		contents := make([]string, len(input))
		for _, m := range input {
			callID := m.ToolCallID
			callIDInt, err := strconv.Atoi(callID)
			if err != nil {
				return "", err
			}
			contents[callIDInt-1] = m.Content
		}
		sb := strings.Builder{}
		for _, m := range contents {
			sb.WriteString(m)
		}
		return sb.String(), nil
	})))
	assert.NoError(t, g.AddEdge(START, "tool node"))
	assert.NoError(t, g.AddEdge("tool node", "lambda"))
	assert.NoError(t, g.AddEdge("lambda", END))

	r, err := g.Compile(ctx, WithCheckPointStore(&inMemoryStore{m: map[string][]byte{}}))
	assert.NoError(t, err)

	_, err = r.Stream(ctx, &schema.Message{Role: schema.Assistant, ToolCalls: tc}, WithCheckPointID("1"))
	info, ok := ExtractInterruptInfo(err)
	assert.True(t, ok)
	assert.Equal(t, []string{"tool node"}, info.RerunNodes)
	assert.Equal(t, &ToolsInterruptAndRerunExtra{
		ToolCalls:     tc,
		RerunTools:    []string{"1", "2"},
		RerunExtraMap: map[string]any{"1": "tool1 rerun extra", "2": "tool2 rerun extra"},
		ExecutedTools: map[string]string{
			"3": "tool3 input: input",
			"4": "tool4 input: input",
		},
	}, info.RerunNodesExtra["tool node"])

	sr, err := r.Stream(ctx, nil, WithCheckPointID("1"))
	assert.NoError(t, err)
	result, err := concatStreamReader(sr)
	assert.NoError(t, err)
	assert.Equal(t, "tool1 input: inputtool2 input: inputtool3 input: inputtool4 input: input", result)
}

func TestToolMiddleware(t *testing.T) {
	ctx := context.Background()
	t3 := &myTool3{t: t}
	t4 := &myTool4{t: t}
	tn, err := NewToolNode(ctx, &ToolsNodeConfig{
		Tools: []tool.BaseTool{t3, t4},
		ToolCallMiddlewares: []ToolMiddleware{
			{
				Invokable: func(endpoint InvokableToolEndpoint) InvokableToolEndpoint {
					return func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
						_, err := endpoint(ctx, input)
						if err != nil {
							return nil, err
						}
						return &ToolOutput{Result: "middleware1"}, nil
					}
				},
			},
			{
				Streamable: func(endpoint StreamableToolEndpoint) StreamableToolEndpoint {
					return func(ctx context.Context, input *ToolInput) (*StreamToolOutput, error) {
						_, err := endpoint(ctx, input)
						if err != nil {
							return nil, err
						}
						return &StreamToolOutput{Result: schema.StreamReaderFromArray([]string{"middleware2"})}, nil
					}
				},
			},
		},
	})
	assert.NoError(t, err)

	messages, err := tn.Invoke(ctx, schema.AssistantMessage("", []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "tool3", Arguments: ""}},
		{ID: "2", Function: schema.FunctionCall{Name: "tool4", Arguments: ""}},
	}))
	assert.NoError(t, err)
	assert.Len(t, messages, 2)
	assert.Equal(t, "middleware1", messages[0].Content)
	assert.Equal(t, "middleware2", messages[1].Content)

	t3.times, t4.times = 0, 0 // reset t3 t4
	messageStreams, err := tn.Stream(ctx, schema.AssistantMessage("", []schema.ToolCall{
		{ID: "1", Function: schema.FunctionCall{Name: "tool3", Arguments: ""}},
		{ID: "2", Function: schema.FunctionCall{Name: "tool4", Arguments: ""}},
	}))
	assert.NoError(t, err)
	var messageArray [][]*schema.Message
	for {
		chunk, err := messageStreams.Recv()
		if err == io.EOF {
			break
		}
		assert.NoError(t, err)
		messageArray = append(messageArray, chunk)
	}
	messages, err = schema.ConcatMessageArray(messageArray)
	assert.Len(t, messages, 2)
	assert.Equal(t, "middleware1", messages[0].Content)
	assert.Equal(t, "middleware2", messages[1].Content)
}

type myTool1 struct {
	times uint
}

func (m *myTool1) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "tool1"}, nil
}

func (m *myTool1) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	if m.times == 0 {
		m.times++
		return "", tool.Interrupt(ctx, "tool1 rerun extra")
	}
	return "tool1 input: " + argumentsInJSON, nil
}

type myTool2 struct {
	times uint
}

func (m *myTool2) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "tool2"}, nil
}

func (m *myTool2) StreamableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	if m.times == 0 {
		m.times++
		return nil, tool.Interrupt(ctx, "tool2 rerun extra")
	}
	return schema.StreamReaderFromArray([]string{"tool2 input: ", argumentsInJSON}), nil
}

type myTool3 struct {
	t     *testing.T
	times int
}

func (m *myTool3) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "tool3"}, nil
}

func (m *myTool3) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	assert.Equal(m.t, 0, m.times)
	m.times++
	return "tool3 input: " + argumentsInJSON, nil
}

type myTool4 struct {
	t     *testing.T
	times int
}

func (m *myTool4) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "tool4"}, nil
}

func (m *myTool4) StreamableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	assert.Equal(m.t, 0, m.times)
	m.times++
	return schema.StreamReaderFromArray([]string{"tool4 input: ", argumentsInJSON}), nil
}

func newTool[I, O any](info *schema.ToolInfo, f func(ctx context.Context, in I) (O, error)) tool.InvokableTool {
	return &invokableTool[I, O]{
		info: info,
		fn:   f,
	}
}

type invokableTool[I, O any] struct {
	info *schema.ToolInfo
	fn   func(ctx context.Context, in I) (O, error)
}

func (f *invokableTool[I, O]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return f.info, nil
}

func (f *invokableTool[I, O]) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	t := generic.NewInstance[I]()
	err := sonic.UnmarshalString(argumentsInJSON, t)
	if err != nil {
		return "", err
	}
	o, err := f.fn(ctx, t)
	if err != nil {
		return "", err
	}
	return sonic.MarshalString(o)
}

func newStreamableTool[I, O any](info *schema.ToolInfo, f func(ctx context.Context, in I) (*schema.StreamReader[O], error)) tool.StreamableTool {
	return &streamableTool[I, O]{
		info: info,
		fn:   f,
	}
}

type streamableTool[I, O any] struct {
	info *schema.ToolInfo
	fn   func(ctx context.Context, in I) (*schema.StreamReader[O], error)
}

func (f *streamableTool[I, O]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return f.info, nil
}
func (f *streamableTool[I, O]) StreamableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (*schema.StreamReader[string], error) {
	t := generic.NewInstance[I]()
	err := sonic.UnmarshalString(argumentsInJSON, t)
	if err != nil {
		return nil, err
	}
	sr, err := f.fn(ctx, t)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderWithConvert(sr, func(o O) (string, error) {
		return sonic.MarshalString(o)
	}), nil
}
