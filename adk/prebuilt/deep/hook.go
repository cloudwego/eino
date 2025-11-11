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

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

func buildBuiltinAgentMiddlewares(withoutWriteTodos bool) ([]adk.AgentMiddleware, error) {
	var ms []adk.AgentMiddleware
	if !withoutWriteTodos {
		t, err := newWriteTodos()
		if err != nil {
			return nil, err
		}
		ms = append(ms, t)
	}

	return ms, nil
}

type TODO struct {
	Content string `json:"content"`
	Status  string `json:"status" jsonschema:"enum=pending,enum=in_progress,enum=completed"`
}

type writeTodosArguments struct {
	Todos []TODO `json:"todos"`
}

func newWriteTodos() (adk.AgentMiddleware, error) {
	t, err := utils.InferTool("write_todos", writeTodosToolDescription, func(ctx context.Context, input writeTodosArguments) (output string, err error) {
		adk.AddSessionValue(ctx, SessionKeyTodos, input.Todos)
		todos, err := sonic.MarshalString(input.Todos)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated todo list to %s", todos), nil
	})
	if err != nil {
		return adk.AgentMiddleware{}, err
	}

	return adk.AgentMiddleware{
		AdditionalInstruction: writeTodosPrompt,
		AdditionalTools:       []tool.BaseTool{t},
	}, nil
}
