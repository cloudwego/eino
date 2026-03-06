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

func TestTaskCreateTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	tool := newTaskCreateTool(backend, baseDir, lock)

	info, err := tool.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, TaskCreateToolName, info.Name)
	assert.Equal(t, taskCreateToolDesc, info.Desc)

	result, err := tool.InvokableRun(ctx, `{"subject": "Test Task", "description": "Test description", "activeForm": "Testing"}`)
	assert.NoError(t, err)
	assert.Equal(t, `{"result":"Task #1 created successfully: Test Task"}`, result)

	content, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)

	var taskData task
	err = sonic.UnmarshalString(content, &taskData)
	assert.NoError(t, err)
	assert.Equal(t, "1", taskData.ID)
	assert.Equal(t, "Test Task", taskData.Subject)
	assert.Equal(t, "Test description", taskData.Description)
	assert.Equal(t, taskStatusPending, taskData.Status)
	assert.Equal(t, "Testing", taskData.ActiveForm)

	hwContent, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, highWatermarkFileName)})
	assert.NoError(t, err)
	assert.Equal(t, "1", hwContent)

	result, err = tool.InvokableRun(ctx, `{"subject": "Second Task", "description": "Second description"}`)
	assert.NoError(t, err)
	assert.Equal(t, `{"result":"Task #2 created successfully: Second Task"}`, result)

	hwContent, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, highWatermarkFileName)})
	assert.NoError(t, err)
	assert.Equal(t, "2", hwContent)
}

func TestTaskCreateToolWithMetadata(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	tool := newTaskCreateTool(backend, baseDir, lock)

	result, err := tool.InvokableRun(ctx, `{"subject": "Task with metadata", "description": "Has metadata", "metadata": {"key1": "value1", "key2": "value2"}}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Task #1 created successfully")

	content, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)

	var taskData task
	err = sonic.UnmarshalString(content, &taskData)
	assert.NoError(t, err)
	assert.Equal(t, "value1", taskData.Metadata["key1"])
	assert.Equal(t, "value2", taskData.Metadata["key2"])
}
