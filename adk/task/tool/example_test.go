/*
 * Copyright 2026 CloudWeGo Authors
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

package tool_test

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/adk/task/background"
	backgroundtool "github.com/cloudwego/eino/adk/task/tool"
	componenttool "github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type exampleTool struct{}

func (exampleTool) ValidateArguments(arguments string) error {
	var input map[string]any
	return json.Unmarshal([]byte(arguments), &input)
}
func (exampleTool) Start(
	context.Context,
	*backgroundtool.StartRequest,
) (*backgroundtool.StartResult, error) {
	return &backgroundtool.StartResult{Run: exampleRun{}}, nil
}

type exampleRun struct{}

func (exampleRun) Wait(context.Context) (*backgroundtool.Outcome, error) {
	return &backgroundtool.Outcome{
		Status: background.StatusCompleted, Data: []byte("video ready"),
	}, nil
}
func (exampleRun) Stop(context.Context) error { return nil }

func ExampleNewManagedTool() {
	manager, err := background.New(context.Background(), &background.Config{
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
		IDGen: func(context.Context, *background.AllocateTaskIDRequest) (string, error) {
			return "task_video", nil
		},
	})
	if err != nil {
		panic(err)
	}
	registry := backgroundtool.NewRegistry()
	_ = registry.Register(&backgroundtool.Registration{
		Info: &schema.ToolInfo{
			Name: "generate_video", Desc: "Generate a product video",
			ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
				"prompt": {Type: schema.String, Required: true},
			}),
		},
		Tool: exampleTool{},
	})
	wrapped, _ := backgroundtool.NewManagedTool(context.Background(), &backgroundtool.ManagedToolConfig{
		Manager: manager, Registry: registry, ToolName: "generate_video",
		SessionID: func(context.Context) (string, error) { return "session", nil },
	})
	result, _ := wrapped.(componenttool.EnhancedInvokableTool).InvokableRun(
		context.Background(), &schema.ToolArgument{Text: `{"prompt":"launch"}`},
	)
	var event backgroundtool.ManagedToolResponseEvent
	_ = json.Unmarshal([]byte(result.Parts[0].Text), &event)
	fmt.Println(event.TaskID, event.Status, event.Output)
	// Output:  completed video ready
}
