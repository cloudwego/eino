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

func TestTaskUpdateTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	taskData := &task{
		ID:          "1",
		Subject:     "Original Subject",
		Description: "Original description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	info, err := tool.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, TaskUpdateToolName, info.Name)
	assert.Equal(t, taskUpdateToolDesc, info.Desc)

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "in_progress"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Updated task #1")
	assert.Contains(t, result, "status")

	content, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	var updated task
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, taskStatusInProgress, updated.Status)

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "subject": "New Subject", "description": "New description"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "subject")
	assert.Contains(t, result, "description")

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, "New Subject", updated.Subject)
	assert.Equal(t, "New description", updated.Description)
}

func TestTaskUpdateToolOwnerAndMetadata(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	taskData := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "agent1"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "owner")

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, "agent1", updated.Owner)

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "metadata": {"key1": "value1", "key2": "value2"}}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "metadata")

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, "value1", updated.Metadata["key1"])
	assert.Equal(t, "value2", updated.Metadata["key2"])

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "metadata": {"key1": null, "key3": "value3"}}`)
	assert.NoError(t, err)

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated2 task
	_ = sonic.UnmarshalString(content, &updated2)
	_, key1Exists := updated2.Metadata["key1"]
	assert.False(t, key1Exists)
	assert.Equal(t, "value2", updated2.Metadata["key2"])
	assert.Equal(t, "value3", updated2.Metadata["key3"])
}

func TestTaskUpdateToolBlocks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	task4 := &task{
		ID:          "4",
		Subject:     "Task 4",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task4JSON, _ := sonic.MarshalString(task4)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "4.json"), Content: task4JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "3"]}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "blocks")

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, []string{"2", "3"}, updated.Blocks)

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["4"]}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "blockedBy")

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, []string{"4"}, updated.BlockedBy)
}

func TestTaskUpdateToolDelete(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	taskData := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "deleted"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "deleted")

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)
}

func TestTaskUpdateToolInvalidTaskID(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "../../../etc/passwd", "status": "in_progress"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate task ID failed")

	_, err = tool.InvokableRun(ctx, `{"taskId": "abc", "status": "in_progress"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate task ID failed")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1.5", "status": "in_progress"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate task ID failed")

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["invalid"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate blocked task ID failed")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["invalid"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate blocking task ID failed")
}

func TestTaskUpdateToolBlocksDeduplication(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{"1"},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{"1"},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	task4 := &task{
		ID:          "4",
		Subject:     "Task 4",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task4JSON, _ := sonic.MarshalString(task4)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "4.json"), Content: task4JSON})

	task5 := &task{
		ID:          "5",
		Subject:     "Task 5",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task5JSON, _ := sonic.MarshalString(task5)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "5.json"), Content: task5JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "4", "4"]}`)
	assert.NoError(t, err)

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, []string{"2", "4"}, updated.Blocks)

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["3", "5", "5"]}`)
	assert.NoError(t, err)

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content, &updated)
	assert.Equal(t, []string{"3", "5"}, updated.BlockedBy)
}

func TestTaskUpdateToolBidirectionalBlocks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Third task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "3"]}`)
	assert.NoError(t, err)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1, &updatedTask1)
	assert.Equal(t, []string{"2", "3"}, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2, &updatedTask2)
	assert.Empty(t, updatedTask2.Blocks)
	assert.Equal(t, []string{"1"}, updatedTask2.BlockedBy)

	content3, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"1"}, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolBidirectionalBlockedBy(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Third task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "3", "addBlockedBy": ["1", "2"]}`)
	assert.NoError(t, err)

	content3, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"1", "2"}, updatedTask3.BlockedBy)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1, &updatedTask1)
	assert.Equal(t, []string{"3"}, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2, &updatedTask2)
	assert.Equal(t, []string{"3"}, updatedTask2.Blocks)
	assert.Empty(t, updatedTask2.BlockedBy)
}

func TestTaskUpdateToolBidirectionalWithNonExistentTask(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["999"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "update Task #1 blocks failed")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["999"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "update Task #1 blockedBy failed")
}

func TestTaskUpdateToolCyclicDependencyDetection(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Third task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["1"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic dependency")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["1"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic dependency")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2"]}`)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"taskId": "2", "addBlocks": ["1"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic dependency")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["2"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic dependency")

	_, err = tool.InvokableRun(ctx, `{"taskId": "2", "addBlocks": ["3"]}`)
	assert.NoError(t, err)

	_, err = tool.InvokableRun(ctx, `{"taskId": "3", "addBlocks": ["1"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic dependency")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["3"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cyclic dependency")

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1, &updatedTask1)
	assert.Equal(t, []string{"2"}, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2, &updatedTask2)
	assert.Equal(t, []string{"3"}, updatedTask2.Blocks)
	assert.Equal(t, []string{"1"}, updatedTask2.BlockedBy)

	content3, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"2"}, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolDeleteCleansDependencies(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{"2", "3"},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusPending,
		Blocks:      []string{"3"},
		BlockedBy:   []string{"1"},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Third task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{"1", "2"},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "deleted"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "deleted")

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)

	content2, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2, &updatedTask2)
	assert.Equal(t, []string{"3"}, updatedTask2.Blocks)
	assert.Empty(t, updatedTask2.BlockedBy)

	content3, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	assert.NoError(t, err)
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"2"}, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolAutoDeleteAllTasksWhenAllCompleted(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusCompleted,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusCompleted,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Third task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "3", "status": "completed"}`)
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)
	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.Error(t, err)
	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	assert.Error(t, err)
}

func TestTaskUpdateToolNoDeleteWhenNotAllCompleted(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	tool := newTaskUpdateTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "completed"}`)
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1, &updatedTask1)
	assert.Equal(t, taskStatusCompleted, updatedTask1.Status)
}
