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
)

func TestTaskTool(t *testing.T) {
	PatchConvey("TestTaskTool", t, func() {
		ctx := context.Background()

		// Create a mock model
		mockModel := &struct {
			model.ToolCallingChatModel
		}{}

		baseTool := NewTaskTool(mockModel)

		// Test tool info
		Convey("Test tool info", func() {
			info, err := baseTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "task")
			So(info.Desc, ShouldContainSubstring, "Create a specialized sub-agent")
		})

		// Verify the tool is invokable
		type invokable interface {
			InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
		}

		invTool, ok := baseTool.(invokable)
		So(ok, ShouldBeTrue)

		// Test with all parameters
		Convey("Test with all parameters", func() {
			Mock(adk.GetSessionValues).Return(map[string]interface{}{}).Build()

			args := `{"task_description": "Write unit tests", "agent_name": "test_agent", "instruction": "You are a test writer"}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully created sub-agent")
			So(result, ShouldContainSubstring, "test_agent")
			So(result, ShouldContainSubstring, "Write unit tests")
		})

		// Test with default agent_name
		Convey("Test with default agent_name", func() {
			Mock(adk.GetSessionValues).Return(map[string]interface{}{}).Build()

			args := `{"task_description": "Write documentation"}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully created sub-agent")
			So(result, ShouldContainSubstring, "sub_agent")
		})

		// Test with default instruction
		Convey("Test with default instruction", func() {
			Mock(adk.GetSessionValues).Return(map[string]interface{}{}).Build()

			args := `{"task_description": "Review code", "agent_name": "reviewer"}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully created sub-agent")
			So(result, ShouldContainSubstring, "Review code")
		})

		// Test with custom instruction
		Convey("Test with custom instruction", func() {
			Mock(adk.GetSessionValues).Return(map[string]interface{}{}).Build()

			args := `{"task_description": "Fix bugs", "agent_name": "bug_fixer", "instruction": "You are an expert bug fixer"}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully created sub-agent")
			So(result, ShouldContainSubstring, "bug_fixer")
		})

		// Test invalid JSON
		Convey("Test invalid JSON", func() {
			args := `{"task_description": invalid}`
			_, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to parse arguments")
		})

		// Test missing required parameter
		Convey("Test missing required parameter", func() {
			args := `{"agent_name": "test"}`
			_, err := invTool.InvokableRun(ctx, args)
			// The tool should still work but with empty task_description
			// Let's check if it handles it gracefully
			if err != nil {
				So(err.Error(), ShouldContainSubstring, "failed")
			}
		})
	})
}

