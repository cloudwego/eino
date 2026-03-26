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
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
)

func TestTaskListTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	tool := newTaskListTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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

func TestTaskListToolFiltersInternalTasks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{ID: "1", Subject: "Visible Task", Status: taskStatusPending}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Internal Task", Status: taskStatusInProgress, Metadata: map[string]any{"_internal": true}}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	tool := newTaskListTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Visible Task")
	assert.NotContains(t, result, "Internal Task")
}

func TestTaskListToolSortsByID(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task3 := &task{ID: "3", Subject: "Task 3", Status: taskStatusPending}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	task1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusPending}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	tool := newTaskListTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#1 [pending] Task 1")
	assert.Contains(t, result, "#2 [pending] Task 2")
	assert.Contains(t, result, "#3 [pending] Task 3")
}

func TestTaskListToolFiltersCompletedBlockers(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	// task1 is blocked by task2 and task3
	task1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, BlockedBy: []string{"2", "3"}}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	// task2 is completed, so it should be filtered out from task1's blockedBy
	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusCompleted}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	// task3 is still in_progress, so it should remain in task1's blockedBy
	task3 := &task{ID: "3", Subject: "Task 3", Status: taskStatusInProgress}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskListTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	// task1 should only show task3 as blocker, not task2
	assert.Contains(t, result, "[blocked by #3]")
	assert.NotContains(t, result, "#2]")

	// When all blockers are completed, blocked by should not appear at all
	task3.Status = taskStatusCompleted
	task3JSON, _ = sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	result, err = tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.NotContains(t, result, "blocked by")
}

func TestListTasksSkipsInvalidFiles(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "readme.txt"), Content: "not a task"})
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "abc.json"), Content: `{"id":"abc"}`})

	task1 := &task{ID: "1", Subject: "Valid Task", Status: taskStatusPending}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	tasks, err := listTasks(ctx, backend, baseDir)
	assert.NoError(t, err)
	assert.Len(t, tasks, 1)
	assert.Equal(t, "1", tasks[0].ID)
}
