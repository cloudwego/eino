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

package deepagents

import (
	"context"
	"testing"

	. "github.com/bytedance/mockey"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// mockBaseTool is a simple tool for testing
type mockBaseTool struct {
	info *schema.ToolInfo
}

func (m *mockBaseTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return m.info, nil
}

// mockAgent is a simple agent for testing
type mockAgent struct {
	name        string
	description string
}

func (m *mockAgent) Name(_ context.Context) string {
	return m.name
}

func (m *mockAgent) Description(_ context.Context) string {
	if m.description != "" {
		return m.description
	}
	return "mock agent for testing"
}

func (m *mockAgent) Run(ctx context.Context, input *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iterator, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer generator.Close()
		// Just close immediately for testing
	}()
	return iterator
}

func TestDeepAgentsNew(t *testing.T) {
	PatchConvey("TestDeepAgentsNew", t, func() {
		ctx := context.Background()

		// Create a real mock model using an anonymous struct that implements the interface
		mockModel := &struct {
			model.ToolCallingChatModel
		}{}

		storage := NewMockStorage()

		// Test with nil config
		_, err := New(ctx, nil)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "config is required")

		// Test with nil model
		_, err = New(ctx, &Config{Storage: storage})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "model is required")

		// Test with nil storage
		_, err = New(ctx, &Config{Model: mockModel})
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "storage is required")

		// Test basic creation
		Convey("Test basic agent creation", func() {
			agent, err := New(ctx, &Config{
				Model:   mockModel,
				Storage: storage,
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)
			So(agent.Name(ctx), ShouldEqual, "DeepAgents")
		})

		// Test with MemoryStore
		Convey("Test with MemoryStore", func() {
			memoryStore := NewMockMemoryStore()
			agent, err := New(ctx, &Config{
				Model:       mockModel,
				Storage:     storage,
				MemoryStore: memoryStore,
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)

			// Verify memory tools are included by checking agent description
			// (The actual tool verification would require accessing internal state)
			So(agent.Description(ctx), ShouldContainSubstring, "memory")
		})

		// Test with custom tools
		Convey("Test with custom tools", func() {
			customTool := &mockBaseTool{
				info: &schema.ToolInfo{
					Name: "custom_tool",
					Desc: "A custom tool for testing",
				},
			}

			agent, err := New(ctx, &Config{
				Model:   mockModel,
				Storage: storage,
				ToolsConfig: adk.ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{customTool},
					},
				},
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)
		})

		// Test with custom instruction
		Convey("Test with custom instruction", func() {
			customInstruction := "You are a specialized test agent."
			agent, err := New(ctx, &Config{
				Model:       mockModel,
				Storage:     storage,
				Instruction: customInstruction,
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)
		})

		// Test with MaxIterations
		Convey("Test with MaxIterations", func() {
			agent, err := New(ctx, &Config{
				Model:         mockModel,
				Storage:       storage,
				MaxIterations: 10,
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)
		})

		// Test with all options
		Convey("Test with all options", func() {
			memoryStore := NewMockMemoryStore()
			customTool := &mockBaseTool{
				info: &schema.ToolInfo{
					Name: "custom_tool",
					Desc: "A custom tool",
				},
			}

			agent, err := New(ctx, &Config{
				Model:         mockModel,
				Storage:       storage,
				MemoryStore:   memoryStore,
				MaxIterations: 15,
				Instruction:   "Custom instruction",
				ToolsConfig: adk.ToolsConfig{
					ToolsNodeConfig: compose.ToolsNodeConfig{
						Tools: []tool.BaseTool{customTool},
					},
				},
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)
			So(agent.Name(ctx), ShouldEqual, "DeepAgents")
		})

		// Test default instruction when not provided
		Convey("Test default instruction", func() {
			agent, err := New(ctx, &Config{
				Model:   mockModel,
				Storage: storage,
			})
			So(err, ShouldBeNil)
			So(agent, ShouldNotBeNil)
			// Default instruction should be used (we can't directly verify it,
			// but if it wasn't set, the agent creation would fail or behave differently)
		})
	})
}
