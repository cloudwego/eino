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
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/internal"
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
		Desc: "根据用户的姓名和邮箱，查询用户的公司和职位信息",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Type: "string",
					Desc: "用户的姓名",
				},
				"email": {
					Type: "string",
					Desc: "用户的邮箱",
				},
			}),
	}

	userSalaryToolInfo := &schema.ToolInfo{
		Name: toolNameOfUserSalary,
		Desc: "根据用户的姓名和邮箱，查询用户的薪酬信息",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"name": {
					Type: "string",
					Desc: "用户的姓名",
				},
				"email": {
					Type: "string",
					Desc: "用户的邮箱",
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

		ui := utils.NewTool(userCompanyToolInfo, queryUserCompany)
		us := utils.NewStreamTool(userSalaryToolInfo, queryUserSalary)

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

		assert.Equal(t, toolIDOfUserCompany, findMsgByToolCallID(out, toolIDOfUserCompany).ToolCallID)
		assert.JSONEq(t, `{"user_id":"zhangsan-zhangsan@bytedance.com","gender":"male","company":"bytedance","position":"CEO"}`,
			findMsgByToolCallID(out, toolIDOfUserCompany).Content)

		assert.Equal(t, toolIDOfUserSalary, findMsgByToolCallID(out, toolIDOfUserSalary).ToolCallID)
		assert.Contains(t, findMsgByToolCallID(out, toolIDOfUserSalary).Content,
			`{"user_id":"zhangsan-zhangsan@bytedance.com","salary":5000}{"user_id":"zhangsan-zhangsan@bytedance.com","salary":3000}{"user_id":"zhangsan-zhangsan@bytedance.com","salary":2000}`)

		// 测试流式调用
		reader, err := r.Stream(ctx, []*schema.Message{})
		assert.NoError(t, err)
		loops := 0
		userSalaryTimes := 0

		defer reader.Close()

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
