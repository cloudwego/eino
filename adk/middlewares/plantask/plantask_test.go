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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

func TestNew(t *testing.T) {
	ctx := context.Background()

	_, err := New(ctx, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config is required")

	_, err = New(ctx, &Config{})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "backend is required")

	_, err = New(ctx, &Config{Backend: newInMemoryBackend()})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "baseDir is required")

	m, err := New(ctx, &Config{Backend: newInMemoryBackend(), BaseDir: "/tmp/tasks"})
	assert.NoError(t, err)
	assert.NotNil(t, m)
}

func TestMiddlewareBeforeAgent(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	m, err := New(ctx, &Config{Backend: backend, BaseDir: baseDir})
	assert.NoError(t, err)

	mw := m.(*middleware)

	ctx, runCtx, err := mw.BeforeAgent(ctx, nil)
	assert.NoError(t, err)
	assert.Nil(t, runCtx)

	runCtx = &adk.ChatModelAgentContext{
		Tools: []tool.BaseTool{},
	}
	ctx, newRunCtx, err := mw.BeforeAgent(ctx, runCtx)
	assert.NoError(t, err)
	assert.NotNil(t, newRunCtx)
	assert.Len(t, newRunCtx.Tools, 4)

	toolNames := make([]string, 0, 4)
	for _, t := range newRunCtx.Tools {
		info, _ := t.Info(ctx)
		toolNames = append(toolNames, info.Name)
	}
	assert.Contains(t, toolNames, "TaskCreate")
	assert.Contains(t, toolNames, "TaskGet")
	assert.Contains(t, toolNames, "TaskUpdate")
	assert.Contains(t, toolNames, "TaskList")
}

func TestMiddlewareAfterAgent(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	m, err := New(ctx, &Config{Backend: backend, BaseDir: baseDir})
	assert.NoError(t, err)

	mw := m.(*middleware)

	ctx, runCtx, err := mw.AfterAgent(ctx, nil)
	assert.NoError(t, err)
	assert.Nil(t, runCtx)

	task1 := &task{ID: "1", Subject: "Task 1", Status: "completed"}
	task1JSON, _ := sonic.MarshalString(task1)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: task1JSON})

	task2 := &task{ID: "2", Subject: "Task 2", Status: "pending"}
	task2JSON, _ := sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	runCtx = &adk.ChatModelAgentContext{}
	ctx, _, err = mw.AfterAgent(ctx, runCtx)
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.NoError(t, err)

	task2 = &task{ID: "2", Subject: "Task 2", Status: "completed"}
	task2JSON, _ = sonic.MarshalString(task2)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "2.json"), Content: task2JSON})

	ctx, _, err = mw.AfterAgent(ctx, runCtx)
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)
	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	assert.Error(t, err)
}

func TestIntegration(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	createTool := newTaskCreateTool(backend, baseDir, lock)
	getTool := newTaskGetTool(backend, baseDir, lock)
	updateTool := newTaskUpdateTool(backend, baseDir, lock)
	listTool := newTaskListTool(backend, baseDir, lock)

	result, err := createTool.InvokableRun(ctx, `{"subject": "Task 1", "description": "First task"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Task #1")

	result, err = createTool.InvokableRun(ctx, `{"subject": "Task 2", "description": "Second task"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Task #2")

	_, err = updateTool.InvokableRun(ctx, `{"taskId": "2", "addBlockedBy": ["1"]}`)
	assert.NoError(t, err)

	result, err = listTool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#1 [pending] Task 1")
	assert.Contains(t, result, "#2 [pending] Task 2")
	assert.Contains(t, result, "[blocked by #1]")

	_, err = updateTool.InvokableRun(ctx, `{"taskId": "1", "status": "in_progress"}`)
	assert.NoError(t, err)

	result, err = getTool.InvokableRun(ctx, `{"taskId": "1"}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "Status: in_progress")

	_, err = updateTool.InvokableRun(ctx, `{"taskId": "1", "status": "completed"}`)
	assert.NoError(t, err)

	result, err = listTool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)
	assert.Contains(t, result, "#1 [completed] Task 1")
}
