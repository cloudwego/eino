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
	"fmt"
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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, taskStatusInProgress, updated.Status)

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "subject": "New Subject", "description": "New description"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "subject")
	assert.Contains(t, result, "description")

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "New Subject", updated.Subject)
	assert.Equal(t, "New description", updated.Description)
}

func TestTaskUpdateToolOwnerAndMetadata(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "agent1"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "owner")

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "agent1", updated.Owner)

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "metadata": {"key1": "value1", "key2": "value2"}}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "metadata")

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "value1", updated.Metadata["key1"])
	assert.Equal(t, "value2", updated.Metadata["key2"])

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "metadata": {"key1": null, "key3": "value3"}}`)
	assert.NoError(t, err)

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated2 task
	_ = sonic.UnmarshalString(content.Content, &updated2)
	_, key1Exists := updated2.Metadata["key1"]
	assert.False(t, key1Exists)
	assert.Equal(t, "value2", updated2.Metadata["key2"])
	assert.Equal(t, "value3", updated2.Metadata["key3"])
}

func TestTaskUpdateToolAutoOwnerInTeamMode(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	teamMW := &middleware{
		backend:             backend,
		baseDir:             baseDir,
		taskBaseDirResolver: func(ctx context.Context) string { return baseDir },
		agentNameResolver:   func(ctx context.Context) string { return "agent-a" },
	}

	t.Run("auto-set owner when marking in_progress without explicit owner", func(t *testing.T) {
		taskData := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
		taskJSON, _ := sonic.MarshalString(taskData)
		_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

		tool := newTaskUpdateTool(teamMW, &sync.RWMutex{})
		result, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "in_progress"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "owner")

		content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
		var updated task
		_ = sonic.UnmarshalString(content.Content, &updated)
		assert.Equal(t, "agent-a", updated.Owner)
	})

	t.Run("do not override explicit owner", func(t *testing.T) {
		taskData := &task{ID: "2", Subject: "Task 2", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
		taskJSON, _ := sonic.MarshalString(taskData)
		_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: taskJSON})

		tool := newTaskUpdateTool(teamMW, &sync.RWMutex{})
		result, err := tool.InvokableRun(ctx, `{"taskId": "2", "status": "in_progress", "owner": "agent-b"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "owner")

		content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
		var updated task
		_ = sonic.UnmarshalString(content.Content, &updated)
		assert.Equal(t, "agent-b", updated.Owner)
	})

	t.Run("do not auto-set if task already has owner", func(t *testing.T) {
		taskData := &task{ID: "3", Subject: "Task 3", Status: taskStatusPending, Owner: "existing-owner", Blocks: []string{}, BlockedBy: []string{}}
		taskJSON, _ := sonic.MarshalString(taskData)
		_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: taskJSON})

		tool := newTaskUpdateTool(teamMW, &sync.RWMutex{})
		result, err := tool.InvokableRun(ctx, `{"taskId": "3", "status": "in_progress"}`)
		assert.NoError(t, err)
		assert.NotContains(t, result, "owner")

		content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
		var updated task
		_ = sonic.UnmarshalString(content.Content, &updated)
		assert.Equal(t, "existing-owner", updated.Owner)
	})

	t.Run("no auto-set in non-team mode", func(t *testing.T) {
		singleMW := testMiddleware(backend, baseDir)

		taskData := &task{ID: "4", Subject: "Task 4", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
		taskJSON, _ := sonic.MarshalString(taskData)
		_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "4.json"), Content: taskJSON})

		tool := newTaskUpdateTool(singleMW, &sync.RWMutex{})
		result, err := tool.InvokableRun(ctx, `{"taskId": "4", "status": "in_progress"}`)
		assert.NoError(t, err)
		assert.NotContains(t, result, "owner")

		content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "4.json")})
		var updated task
		_ = sonic.UnmarshalString(content.Content, &updated)
		assert.Empty(t, updated.Owner)
	})
}

func TestTaskUpdateToolBlocks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "3"]}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "blocks")

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, []string{"2", "3"}, updated.Blocks)

	result, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["4"]}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "blockedBy")

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, []string{"4"}, updated.BlockedBy)
}

func TestTaskUpdateToolDelete(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	taskData := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "4", "4"]}`)
	assert.NoError(t, err)

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, []string{"2", "4"}, updated.Blocks)

	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlockedBy": ["3", "5", "5"]}`)
	assert.NoError(t, err)

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, []string{"3", "5"}, updated.BlockedBy)
}

func TestTaskUpdateToolBidirectionalBlocks(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "3"]}`)
	assert.NoError(t, err)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1.Content, &updatedTask1)
	assert.Equal(t, []string{"2", "3"}, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2.Content, &updatedTask2)
	assert.Empty(t, updatedTask2.Blocks)
	assert.Equal(t, []string{"1"}, updatedTask2.BlockedBy)

	content3, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3.Content, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"1"}, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolBidirectionalBlockedBy(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "3", "addBlockedBy": ["1", "2"]}`)
	assert.NoError(t, err)

	content3, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3.Content, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"1", "2"}, updatedTask3.BlockedBy)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1.Content, &updatedTask1)
	assert.Equal(t, []string{"3"}, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2.Content, &updatedTask2)
	assert.Equal(t, []string{"3"}, updatedTask2.Blocks)
	assert.Empty(t, updatedTask2.BlockedBy)
}

func TestTaskUpdateToolBidirectionalWithNonExistentTask(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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
	_ = sonic.UnmarshalString(content1.Content, &updatedTask1)
	assert.Equal(t, []string{"2"}, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2.Content, &updatedTask2)
	assert.Equal(t, []string{"3"}, updatedTask2.Blocks)
	assert.Equal(t, []string{"1"}, updatedTask2.BlockedBy)

	content3, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3.Content, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"2"}, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolDeleteCleansDependencies(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "deleted"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "deleted")

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)

	content2, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2.Content, &updatedTask2)
	assert.Equal(t, []string{"3"}, updatedTask2.Blocks)
	assert.Empty(t, updatedTask2.BlockedBy)

	content3, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	assert.NoError(t, err)
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3.Content, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Equal(t, []string{"2"}, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolCompletedCleansDependencies(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{"2"},
		BlockedBy:   []string{"3"},
	}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{
		ID:          "2",
		Subject:     "Task 2",
		Description: "Second task",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{"1"},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	task3 := &task{
		ID:          "3",
		Subject:     "Task 3",
		Description: "Third task",
		Status:      taskStatusPending,
		Blocks:      []string{"1"},
		BlockedBy:   []string{},
	}
	task3JSON, _ := sonic.MarshalString(task3)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "3.json"), Content: task3JSON})

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "completed"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "status")
	assert.Contains(t, result, "blocks")
	assert.Contains(t, result, "blockedBy")

	content1, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1.Content, &updatedTask1)
	assert.Equal(t, taskStatusCompleted, updatedTask1.Status)
	assert.Empty(t, updatedTask1.Blocks)
	assert.Empty(t, updatedTask1.BlockedBy)

	content2, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)
	var updatedTask2 task
	_ = sonic.UnmarshalString(content2.Content, &updatedTask2)
	assert.Empty(t, updatedTask2.Blocks)
	assert.Empty(t, updatedTask2.BlockedBy)

	content3, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	assert.NoError(t, err)
	var updatedTask3 task
	_ = sonic.UnmarshalString(content3.Content, &updatedTask3)
	assert.Empty(t, updatedTask3.Blocks)
	assert.Empty(t, updatedTask3.BlockedBy)
}

func TestTaskUpdateToolAutoDeleteAllTasksWhenAllCompleted(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "completed"}`)
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updatedTask1 task
	_ = sonic.UnmarshalString(content1.Content, &updatedTask1)
	assert.Equal(t, taskStatusCompleted, updatedTask1.Status)
}

func TestTaskUpdateToolInvalidJSON(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{invalid`)
	assert.Error(t, err)
}

func TestTaskUpdateToolInvalidStatus(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	taskData := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "unknown"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid task status")
}

func TestTaskUpdateToolActiveForm(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "activeForm": "Running tests"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "activeForm")

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "Running tests", updated.ActiveForm)
}

func TestTaskUpdateToolWithAssignedHook_IgnoredOutsideSharedTaskMode(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	var hookCalled bool

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		onTaskAssigned: func(ctx context.Context, assignment TaskAssignment) error {
			hookCalled = true
			return nil
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Hook Task",
		Description: "Task for hook test",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "agent1"}`)
	assert.NoError(t, err)
	assert.False(t, hookCalled)
}

func TestTaskUpdateToolWithAgentNameResolver_IgnoredOutsideSharedTaskMode(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	var receivedAssignment TaskAssignment

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		onTaskAssigned: func(ctx context.Context, assignment TaskAssignment) error {
			receivedAssignment = assignment
			return nil
		},
		agentNameResolver: func(ctx context.Context) string {
			return "leader-agent"
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Resolver Task",
		Description: "Task for resolver test",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "worker-agent"}`)
	assert.NoError(t, err)
	assert.Equal(t, TaskAssignment{}, receivedAssignment)
}

func TestTaskUpdateToolWithAssignedHookAndAgentNameResolver_InSharedTaskMode(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	var hookCalled bool
	var receivedAssignment TaskAssignment

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		taskBaseDirResolver: func(ctx context.Context) string {
			return baseDir
		},
		agentNameResolver: func(ctx context.Context) string {
			return "leader-agent"
		},
		onTaskAssigned: func(ctx context.Context, assignment TaskAssignment) error {
			hookCalled = true
			receivedAssignment = assignment
			return nil
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Hook Task",
		Description: "Task for hook test",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "worker-agent"}`)
	assert.NoError(t, err)
	assert.True(t, hookCalled)
	assert.Equal(t, "1", receivedAssignment.TaskID)
	assert.Equal(t, "worker-agent", receivedAssignment.Owner)
	assert.Equal(t, "Hook Task", receivedAssignment.Subject)
	assert.Equal(t, "Task for hook test", receivedAssignment.Description)
	assert.Equal(t, "leader-agent", receivedAssignment.AssignedBy)
}

// TestTaskUpdateToolWithAssignedHook_NotificationFailureSurfaced verifies that
// when the owner is persisted but the assignment notification fails, the tool
// still succeeds (the owner write committed) yet surfaces the delivery failure in
// the result so the model can re-send the message rather than assuming the
// assignee was told.
func TestTaskUpdateToolWithAssignedHook_NotificationFailureSurfaced(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		taskBaseDirResolver: func(ctx context.Context) string {
			return baseDir
		},
		agentNameResolver: func(ctx context.Context) string {
			return "leader-agent"
		},
		onTaskAssigned: func(ctx context.Context, assignment TaskAssignment) error {
			return fmt.Errorf("mailbox unavailable")
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Hook Task",
		Description: "Task for hook test",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "worker-agent"}`)
	assert.NoError(t, err)

	var out taskOut
	assert.NoError(t, sonic.UnmarshalString(result, &out))
	assert.Contains(t, out.Result, "owner")
	assert.NotEmpty(t, out.NotificationWarning, "notification failure must be surfaced")
	assert.Contains(t, out.NotificationWarning, "worker-agent")
	assert.Contains(t, out.NotificationWarning, "mailbox unavailable")

	// The owner must still have been persisted despite the notification failure.
	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted task
	_ = sonic.UnmarshalString(content.Content, &persisted)
	assert.Equal(t, "worker-agent", persisted.Owner)
}

func TestTaskUpdateToolWithAssignedHook_DoesNotNotifyWhenOwnerUnchanged(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	var hookCalled bool

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		taskBaseDirResolver: func(ctx context.Context) string {
			return baseDir
		},
		agentNameResolver: func(ctx context.Context) string {
			return "leader-agent"
		},
		onTaskAssigned: func(ctx context.Context, assignment TaskAssignment) error {
			hookCalled = true
			return nil
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Hook Task",
		Description: "Task for hook test",
		Status:      taskStatusPending,
		Owner:       "worker-agent",
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "worker-agent"}`)
	assert.NoError(t, err)
	assert.False(t, hookCalled)
	assert.NotContains(t, result, "owner")

	content, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "worker-agent", updated.Owner)
}

func TestTaskUpdateToolWithOwnerValidator_RejectsUnknownOwner(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	var hookCalled bool

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		taskBaseDirResolver: func(ctx context.Context) string {
			return baseDir
		},
		ownerValidator: func(ctx context.Context, owner string) error {
			if owner != "known-agent" {
				return fmt.Errorf("owner %q is not a member", owner)
			}
			return nil
		},
		onTaskAssigned: func(ctx context.Context, assignment TaskAssignment) error {
			hookCalled = true
			return nil
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Validated Task",
		Description: "Task for owner validation",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "ghost-agent"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ghost-agent")
	assert.False(t, hookCalled, "assignment hook must not fire on rejected owner")

	// The task must not have been mutated/persisted with the invalid owner.
	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted task
	_ = sonic.UnmarshalString(content.Content, &persisted)
	assert.Empty(t, persisted.Owner, "rejected owner must not be persisted")
}

func TestTaskUpdateToolWithOwnerValidator_AllowsKnownOwner(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		taskBaseDirResolver: func(ctx context.Context) string {
			return baseDir
		},
		ownerValidator: func(ctx context.Context, owner string) error {
			if owner != "known-agent" {
				return fmt.Errorf("owner %q is not a member", owner)
			}
			return nil
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Validated Task",
		Description: "Task for owner validation",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "owner": "known-agent"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "owner")

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted task
	_ = sonic.UnmarshalString(content.Content, &persisted)
	assert.Equal(t, "known-agent", persisted.Owner)
}

func TestTaskUpdateToolWithOwnerValidator_SkipsImplicitSelfAssignment(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	validatorCalled := false

	mw := &middleware{
		backend: backend,
		baseDir: baseDir,
		taskBaseDirResolver: func(ctx context.Context) string {
			return baseDir
		},
		agentNameResolver: func(ctx context.Context) string {
			return "self-agent"
		},
		ownerValidator: func(ctx context.Context, owner string) error {
			validatorCalled = true
			return fmt.Errorf("should not be consulted for implicit self-assignment")
		},
	}

	taskData := &task{
		ID:          "1",
		Subject:     "Self Task",
		Description: "Implicit self assignment",
		Status:      taskStatusPending,
		Blocks:      []string{},
		BlockedBy:   []string{},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskUpdateTool(mw, &sync.RWMutex{})

	// No explicit owner; marking in_progress triggers implicit self-assignment,
	// which must not consult the validator.
	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "status": "in_progress"}`)
	assert.NoError(t, err)
	assert.False(t, validatorCalled)

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted task
	_ = sonic.UnmarshalString(content.Content, &persisted)
	assert.Equal(t, "self-agent", persisted.Owner)
}

func TestTaskUpdateToolCompletedWithDependencyUpdates(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusInProgress,
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

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	result, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2"], "status": "completed"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "status")
	assert.Contains(t, result, "blocks")

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated1 task
	_ = sonic.UnmarshalString(content1.Content, &updated1)
	assert.Equal(t, taskStatusCompleted, updated1.Status)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var updated2 task
	_ = sonic.UnmarshalString(content2.Content, &updated2)
	assert.Empty(t, updated2.BlockedBy)
}

func TestDeleteTaskPublicAPI(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{
		ID:          "1",
		Subject:     "Task 1",
		Description: "First task",
		Status:      taskStatusPending,
		Blocks:      []string{"2"},
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
		BlockedBy:   []string{"1"},
	}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	err := DeleteTask(ctx, backend, baseDir, "1")
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)

	content2, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)
	var updated2 task
	_ = sonic.UnmarshalString(content2.Content, &updated2)
	assert.Empty(t, updated2.BlockedBy)
}

func TestDeleteTaskInvalidID(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	err := DeleteTask(ctx, backend, baseDir, "invalid")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "DeleteTask invalid task ID")
}

// writeFailBackend wraps an inMemoryBackend and fails Write for a configured set
// of file paths, letting tests simulate a backend that errors part-way through a
// multi-task graph write.
type writeFailBackend struct {
	*inMemoryBackend
	failPaths map[string]struct{}
}

func (b *writeFailBackend) Write(ctx context.Context, req *WriteRequest) error {
	if _, fail := b.failPaths[req.FilePath]; fail {
		return fmt.Errorf("simulated write failure for %s", req.FilePath)
	}
	return b.inMemoryBackend.Write(ctx, req)
}

// TestTaskUpdateToolDependencyWriteFailsBeforePartialEdge verifies that when the
// batched graph flush fails on the first task it writes, nothing is persisted —
// so a failed dependency update never leaves a one-sided edge. persistGraph
// writes in ascending ID order, so failing the current task (#1, written first)
// aborts before the counterpart (#2) is touched.
func TestTaskUpdateToolDependencyWriteFailsBeforePartialEdge(t *testing.T) {
	ctx := context.Background()
	mem := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = mem.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = mem.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	backend := &writeFailBackend{
		inMemoryBackend: mem,
		failPaths:       map[string]struct{}{filepath.Join(baseDir, "1.json"): {}},
	}

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "persist task graph failed")

	// Neither side should carry an edge: the flush aborted on #1 before touching #2.
	content1, _ := mem.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted1 task
	_ = sonic.UnmarshalString(content1.Content, &persisted1)
	assert.Empty(t, persisted1.Blocks)

	content2, _ := mem.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var persisted2 task
	_ = sonic.UnmarshalString(content2.Content, &persisted2)
	assert.Empty(t, persisted2.BlockedBy)
}

// TestTaskUpdateToolDependencyValidationFailsPersistsNothing verifies that a
// validation failure (here a non-existent target) aborts before any backend
// write, because all mutation now happens in memory ahead of the batched flush.
func TestTaskUpdateToolDependencyValidationFailsPersistsNothing(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	// #2 exists, #999 does not: the whole update must be rejected with no partial
	// edge written for the valid target.
	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2", "999"]}`)
	assert.Error(t, err)

	content1, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted1 task
	_ = sonic.UnmarshalString(content1.Content, &persisted1)
	assert.Empty(t, persisted1.Blocks)

	content2, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var persisted2 task
	_ = sonic.UnmarshalString(content2.Content, &persisted2)
	assert.Empty(t, persisted2.BlockedBy)
}

// TestTaskUpdateToolDependencyRetryAfterWriteFailure verifies that even when a
// flush fails partway (leaving at most a recoverable one-sided edge), re-running
// the same idempotent TaskUpdate reconciles the graph to a fully consistent
// bidirectional edge. Here the counterpart (#2) write is failed first — which can
// leave #1.blocks written but #2.blockedBy missing — then the fault is cleared
// and the retry repairs it.
func TestTaskUpdateToolDependencyRetryAfterWriteFailure(t *testing.T) {
	ctx := context.Background()
	mem := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	task1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = mem.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusPending, Blocks: []string{}, BlockedBy: []string{}}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = mem.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	backend := &writeFailBackend{
		inMemoryBackend: mem,
		failPaths:       map[string]struct{}{filepath.Join(baseDir, "2.json"): {}},
	}
	tool := newTaskUpdateTool(testMiddleware(backend, baseDir), &sync.RWMutex{})

	_, err := tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2"]}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "persist task graph failed")

	// Clear the fault and retry the identical update; idempotent mutations repair
	// any one-sided edge left behind.
	backend.failPaths = map[string]struct{}{}
	_, err = tool.InvokableRun(ctx, `{"taskId": "1", "addBlocks": ["2"]}`)
	assert.NoError(t, err)

	content1, _ := mem.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var persisted1 task
	_ = sonic.UnmarshalString(content1.Content, &persisted1)
	assert.Equal(t, []string{"2"}, persisted1.Blocks)

	content2, _ := mem.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	var persisted2 task
	_ = sonic.UnmarshalString(content2.Content, &persisted2)
	assert.Equal(t, []string{"1"}, persisted2.BlockedBy)
}
