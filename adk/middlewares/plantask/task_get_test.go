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

func TestTaskGetTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	taskData := &task{
		ID:          "1",
		Subject:     "Test Task",
		Description: "Test description",
		Status:      taskStatusPending,
		Blocks:      []string{"2", "3"},
		BlockedBy:   []string{"4"},
	}
	taskJSON, _ := sonic.MarshalString(taskData)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: taskJSON})

	tool := newTaskGetTool(backend, baseDir, lock)

	info, err := tool.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, TaskGetToolName, info.Name)
	assert.Equal(t, taskGetToolDesc, info.Desc)

	result, err := tool.InvokableRun(ctx, `{"taskId": "1"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Task #1: Test Task")
	assert.Contains(t, result, "Status: "+taskStatusPending)
	assert.Contains(t, result, "Description: Test description")
	assert.Contains(t, result, "Blocked by: #4")
	assert.Contains(t, result, "Blocks: #2, #3")

	_, err = tool.InvokableRun(ctx, `{"taskId": "999"}`)
	assert.Error(t, err)
}

func TestTaskGetToolInvalidTaskID(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	tool := newTaskGetTool(backend, baseDir, lock)

	_, err := tool.InvokableRun(ctx, `{"taskId": "../../../etc/passwd"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate task ID failed")

	_, err = tool.InvokableRun(ctx, `{"taskId": "abc"}`)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "validate task ID failed")
}
