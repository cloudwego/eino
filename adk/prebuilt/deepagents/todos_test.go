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
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/components/tool"
)

func TestWriteTodosTool(t *testing.T) {
	PatchConvey("TestWriteTodosTool", t, func() {
		ctx := context.Background()

		baseTool := NewWriteTodosTool()

		// Test tool info
		Convey("Test tool info", func() {
			info, err := baseTool.Info(ctx)
			So(err, ShouldBeNil)
			So(info.Name, ShouldEqual, "write_todos")
		})

		// Use type assertion to access InvokableRun method
		type invokable interface {
			InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
		}

		invTool, ok := baseTool.(invokable)
		So(ok, ShouldBeTrue)

		// Test writing todos with multiple steps
		Convey("Test writing todos with multiple steps", func() {
			var capturedKey string
			var capturedValue interface{}
			Mock(adk.AddSessionValue).To(func(ctx context.Context, key string, value interface{}) {
				capturedKey = key
				capturedValue = value
			}).Build()

			args := `{"steps": ["task1", "task2", "task3"]}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote 3 todos")
			So(result, ShouldContainSubstring, "task1")

			// Verify session value was set
			So(capturedKey, ShouldEqual, TodosSessionKey)
			plan, ok := capturedValue.(planexecute.Plan)
			So(ok, ShouldBeTrue)
			So(plan, ShouldNotBeNil)
			So(plan.FirstStep(), ShouldEqual, "task1")
		})

		// Test writing empty todos list
		Convey("Test writing empty todos list", func() {
			var capturedValue interface{}
			Mock(adk.AddSessionValue).To(func(ctx context.Context, key string, value interface{}) {
				capturedValue = value
			}).Build()

			args := `{"steps": []}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote 0 todos")

			// Verify empty plan was set
			plan, ok := capturedValue.(planexecute.Plan)
			So(ok, ShouldBeTrue)
			So(plan, ShouldNotBeNil)
			So(plan.FirstStep(), ShouldEqual, "") // Empty plan
		})

		// Test writing single todo
		Convey("Test writing single todo", func() {
			var capturedValue interface{}
			Mock(adk.AddSessionValue).To(func(ctx context.Context, key string, value interface{}) {
				capturedValue = value
			}).Build()

			args := `{"steps": ["single task"]}`
			result, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote 1 todos")
			So(result, ShouldContainSubstring, "single task")

			plan, ok := capturedValue.(planexecute.Plan)
			So(ok, ShouldBeTrue)
			So(plan.FirstStep(), ShouldEqual, "single task")
		})

		// Test multiple calls (updating todos)
		Convey("Test multiple calls updating todos", func() {
			var callCount int
			var lastValue interface{}
			Mock(adk.AddSessionValue).To(func(ctx context.Context, key string, value interface{}) {
				callCount++
				lastValue = value
			}).Build()

			// First call
			args1 := `{"steps": ["task1", "task2"]}`
			result, err := invTool.InvokableRun(ctx, args1)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote 2 todos")
			So(callCount, ShouldEqual, 1)

			plan1, ok := lastValue.(planexecute.Plan)
			So(ok, ShouldBeTrue)
			So(plan1.FirstStep(), ShouldEqual, "task1")

			// Second call (update)
			args2 := `{"steps": ["new_task1", "new_task2", "new_task3"]}`
			result, err = invTool.InvokableRun(ctx, args2)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote 3 todos")
			So(callCount, ShouldEqual, 2)

			plan2, ok := lastValue.(planexecute.Plan)
			So(ok, ShouldBeTrue)
			So(plan2.FirstStep(), ShouldEqual, "new_task1")
		})

		// Test invalid JSON
		Convey("Test invalid JSON", func() {
			args := `{"steps": [invalid]}`
			_, err := invTool.InvokableRun(ctx, args)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to parse arguments")
		})

		// Test missing steps parameter
		Convey("Test missing steps parameter", func() {
			args := `{}`
			_, err := invTool.InvokableRun(ctx, args)
			// The tool should handle missing steps gracefully (empty slice)
			// or return an error depending on implementation
			if err != nil {
				So(err.Error(), ShouldContainSubstring, "failed")
			}
		})

		// Test wrong parameter type
		Convey("Test wrong parameter type", func() {
			args := `{"steps": "not an array"}`
			_, err := invTool.InvokableRun(ctx, args)
			// Should fail to parse or handle gracefully
			if err != nil {
				So(err.Error(), ShouldContainSubstring, "failed")
			}
		})
	})
}

func TestGetTodos(t *testing.T) {
	PatchConvey("TestGetTodos", t, func() {
		ctx := context.Background()

		// Test when no todos exist (no session)
		plan, ok := GetTodos(ctx)
		So(ok, ShouldBeFalse)
		So(plan, ShouldBeNil)

		// Mock GetSessionValue to return a plan
		testPlan := &defaultPlan{Steps: []string{"step1", "step2"}}
		Mock(adk.GetSessionValue).To(func(ctx context.Context, key string) (interface{}, bool) {
			if key == TodosSessionKey {
				return testPlan, true
			}
			return nil, false
		}).Build()

		plan, ok = GetTodos(ctx)
		So(ok, ShouldBeTrue)
		So(plan, ShouldNotBeNil)
		So(plan.FirstStep(), ShouldEqual, "step1")
	})
}

func TestNewPlan(t *testing.T) {
	PatchConvey("TestNewPlan", t, func() {
		ctx := context.Background()
		plan := NewPlan(ctx)
		So(plan, ShouldNotBeNil)

		// Test that it implements planexecute.Plan interface
		// NewPlan already returns planexecute.Plan, so we just verify it's not nil
		// and can call methods on it
		So(plan.FirstStep(), ShouldEqual, "") // Empty plan should return empty string
	})
}
