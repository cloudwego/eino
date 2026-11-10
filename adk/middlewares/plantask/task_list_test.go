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
	"strings"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk/middlewares/filesystem"
)

func TestTaskListTool(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	lock := &sync.Mutex{}

	tool := newTaskListTool(backend, baseDir, lock)

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

// pathFreeBackend is a Backend stub that serves file entries exactly as they
// were registered, without path normalization, so tests stay independent of
// platform path separators.
type pathFreeBackend struct {
	entries  []FileInfo
	contents map[string]string
}

func (b *pathFreeBackend) LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error) {
	return b.entries, nil
}

func (b *pathFreeBackend) Read(ctx context.Context, req *ReadRequest) (*filesystem.FileContent, error) {
	content, ok := b.contents[req.FilePath]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", req.FilePath)
	}
	return &filesystem.FileContent{Content: content}, nil
}

func (b *pathFreeBackend) Write(ctx context.Context, req *WriteRequest) error {
	b.contents[req.FilePath] = req.Content
	return nil
}

func (b *pathFreeBackend) Delete(ctx context.Context, req *DeleteRequest) error {
	delete(b.contents, req.FilePath)
	return nil
}

func TestTaskListToolSortsTasksByNumericID(t *testing.T) {
	ctx := context.Background()
	baseDir := "/tmp/tasks"

	backend := &pathFreeBackend{contents: make(map[string]string)}
	for _, id := range []string{"10", "2", "1", "11"} {
		taskData := &task{ID: id, Subject: "Task " + id, Status: taskStatusPending}
		taskJSON, err := sonic.MarshalString(taskData)
		assert.NoError(t, err)
		filePath := baseDir + "/" + id + ".json"
		backend.entries = append(backend.entries, FileInfo{Path: filePath})
		backend.contents[filePath] = taskJSON
	}

	tool := newTaskListTool(backend, baseDir, &sync.Mutex{})
	result, err := tool.InvokableRun(ctx, `{}`)
	assert.NoError(t, err)

	lastPos := -1
	for _, prefix := range []string{"#1 [", "#2 [", "#10 [", "#11 ["} {
		pos := strings.Index(result, prefix)
		assert.GreaterOrEqual(t, pos, 0, "missing %s in result: %s", prefix, result)
		if lastPos >= 0 {
			assert.Greater(t, pos, lastPos, "tasks not in numeric ID order, got: %s", result)
		}
		lastPos = pos
	}
}
