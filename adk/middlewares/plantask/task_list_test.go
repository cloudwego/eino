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

package plantask

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
)

func TestTaskListTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	tool := newTaskListTool(testMiddleware(backend, baseDir))

	info, err := tool.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, TaskListToolName, info.Name)
	assert.Equal(t, taskListToolDesc, info.Desc)

	result, err := tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Equal(t, `{"result":"No tasks found."}`, result)

	task1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, BlockedBy: []string{"2"}}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusInProgress, Owner: "agent1"}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	result, err = tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#1 ["+taskStatusPending+"] Task 1")
	assert.Contains(t, result, "[blocked by #2]")
	assert.Contains(t, result, "#2 ["+taskStatusInProgress+"] Task 2")
	assert.Contains(t, result, "[owner: agent1]")
}

func TestTaskListToolSortsNumericallyAndHidesInternalTasks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	tool := newTaskListTool(testMiddleware(backend, baseDir))

	task10 := &task{ID: "10", Subject: "Task 10", Status: taskStatusPending}
	task10JSON, _ := sonic.MarshalString(task10)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "10.json"), Content: task10JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusPending}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	internalTask := &task{
		ID:      "3",
		Subject: "Internal Task",
		Status:  taskStatusInProgress,
		Metadata: map[string]any{
			MetadataKeyInternal: true,
		},
	}
	internalTaskJSON, _ := sonic.MarshalString(internalTask)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: internalTaskJSON})

	result, err := tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#2 ["+taskStatusPending+"] Task 2")
	assert.Contains(t, result, "#10 ["+taskStatusPending+"] Task 10")
	assert.NotContains(t, result, "Internal Task")
	assert.Less(t, strings.Index(result, "#2 ["), strings.Index(result, "#10 ["))
}
