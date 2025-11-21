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
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/prebuilt/planexecute"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Todos Management:
//
// DeepAgents uses Session (adk.Session) to store todos during agent execution.
// The todos are stored as a planexecute.Plan, which provides a structured way
// to represent and serialize task lists.
//
// This approach is consistent with other ADK prebuilt agents like planexecute,
// which also use Session for state management during execution.
//
// Lifecycle: Todos persist for the duration of a single agent run and are
// automatically managed by the ADK Session mechanism.

const (
	// TodosSessionKey is the session key for storing the todos (plan).
	TodosSessionKey = "_deepagents_todos"
)

// writeTodosTool is a tool that allows the agent to write and manage todos.
// It reuses the planexecute.Plan interface for serialization and storage.
type writeTodosTool struct{}

// Info returns the tool information for write_todos.
func (w *writeTodosTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "write_todos",
		Desc: "Write a list of todos (tasks) to track progress. The todos are stored as a plan with steps. You can update the todos by calling this tool again with a new list.",
		ParamsOneOf: schema.NewParamsOneOfByParams(
			map[string]*schema.ParameterInfo{
				"steps": {
					Type:     schema.Array,
					ElemInfo: &schema.ParameterInfo{Type: schema.String},
					Desc:     "List of todo items (tasks) to be completed. Each step should be clear and actionable.",
					Required: true,
				},
			},
		),
	}, nil
}

// InvokableRun executes the write_todos tool.
func (w *writeTodosTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	// Parse the arguments
	type writeTodosParams struct {
		Steps []string `json:"steps"`
	}

	var params writeTodosParams
	err := sonic.UnmarshalString(argumentsInJSON, &params)
	if err != nil {
		return "", fmt.Errorf("failed to parse arguments: %w", err)
	}

	// Create a plan using the default plan implementation
	// We need to create a plan that implements planexecute.Plan interface
	plan := newDefaultPlan()
	plan.Steps = params.Steps

	// Store the plan in session
	adk.AddSessionValue(ctx, TodosSessionKey, plan)

	// Return success message
	return fmt.Sprintf("Successfully wrote %d todos. First todo: %s", len(params.Steps), plan.FirstStep()), nil
}

// NewWriteTodosTool creates a new write_todos tool.
func NewWriteTodosTool() tool.BaseTool {
	return &writeTodosTool{}
}

// GetTodos retrieves the current todos from the session.
// Returns nil if no todos are stored.
func GetTodos(ctx context.Context) (planexecute.Plan, bool) {
	value, ok := adk.GetSessionValue(ctx, TodosSessionKey)
	if !ok {
		return nil, false
	}

	plan, ok := value.(planexecute.Plan)
	if !ok {
		return nil, false
	}

	return plan, true
}

// defaultPlan is a local implementation of planexecute.Plan interface.
// It reuses the same structure as planexecute's defaultPlan.
type defaultPlan struct {
	Steps []string `json:"steps"`
}

// FirstStep returns the first step in the plan or an empty string if no steps exist.
func (p *defaultPlan) FirstStep() string {
	if len(p.Steps) == 0 {
		return ""
	}
	return p.Steps[0]
}

// MarshalJSON implements json.Marshaler.
func (p *defaultPlan) MarshalJSON() ([]byte, error) {
	type planTyp defaultPlan
	return sonic.Marshal((*planTyp)(p))
}

// UnmarshalJSON implements json.Unmarshaler.
func (p *defaultPlan) UnmarshalJSON(bytes []byte) error {
	type planTyp defaultPlan
	return sonic.Unmarshal(bytes, (*planTyp)(p))
}

// newDefaultPlan creates a new default plan instance.
func newDefaultPlan() *defaultPlan {
	return &defaultPlan{}
}

// NewPlan creates a new plan instance compatible with planexecute.Plan interface.
func NewPlan(ctx context.Context) planexecute.Plan {
	return newDefaultPlan()
}
