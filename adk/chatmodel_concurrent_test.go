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

package adk

import (
	"context"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// concurrentStubTC is a ToolCallingChatModel that returns one assistant message.
type concurrentStubTC struct{}

func (concurrentStubTC) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return concurrentStubTC{}, nil
}
func (concurrentStubTC) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("ok", nil), nil
}
func (concurrentStubTC) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("ok", nil)}), nil
}

// concurrentNoopTool forces the ReAct (tools) run path.
type concurrentNoopTool struct{}

func (concurrentNoopTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "ping", Desc: "noop"}, nil
}
func (concurrentNoopTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return "pong", nil
}

func TestConcurrentRunSharedAgentWithTools(t *testing.T) {
	ctx := context.Background()
	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "shared",
		Description: "race repro",
		Instruction: "hi",
		Model:       concurrentStubTC{},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{concurrentNoopTool{}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			iter := agent.Run(context.Background(), &AgentInput{
				Messages: []*schema.Message{schema.UserMessage("q")},
			})
			for {
				ev, ok := iter.Next()
				if !ok {
					return
				}
				if ev != nil && ev.Err != nil {
					t.Errorf("run err: %v", ev.Err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestConcurrentRunSharedAgenticModelWithTools(t *testing.T) {
	ctx := context.Background()
	agent, err := NewTypedChatModelAgent(ctx, &TypedChatModelAgentConfig[*schema.AgenticMessage]{
		Name:        "shared",
		Description: "race repro",
		Instruction: "hi",
		Model: &mockAgenticModel{generateFn: func(context.Context, []*schema.AgenticMessage, ...model.Option) (*schema.AgenticMessage, error) {
			return agenticMsg("ok"), nil
		}},
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{concurrentNoopTool{}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			iter := agent.Run(context.Background(), &TypedAgentInput[*schema.AgenticMessage]{
				Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("q")},
			})
			for {
				ev, ok := iter.Next()
				if !ok {
					return
				}
				if ev != nil && ev.Err != nil {
					t.Errorf("run err: %v", ev.Err)
				}
			}
		}()
	}
	wg.Wait()
}
