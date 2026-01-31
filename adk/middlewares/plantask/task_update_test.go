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
	assert.Equal(t, taskUpdateToolName, info.Name)
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
	assert.Contains(t, err.Error(), "invalid task ID format")

	_, err = tool.InvokableRun(ctx, `{"taskId": "abc", "status": "in_progress"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid task ID format")

	_, err = tool.InvokableRun(ctx, `{"taskId": "1.5", "status": "in_progress"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid task ID format")
}

func TestTaskUpdateToolBlocksDeduplication(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	taskData := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{"2"},
		BlockedBy:   []string{"3"},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

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
