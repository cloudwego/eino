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
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	fspkg "github.com/cloudwego/eino/adk/filesystem"
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

func testMiddleware(backend Backend, baseDir string) *middleware {
	return &middleware{backend: backend, baseDir: baseDir}
}

func TestIntegration(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)
	turnLock := &sync.RWMutex{}

	createTool := newTaskCreateTool(mw, turnLock)
	getTool := newTaskGetTool(mw, turnLock)
	updateTool := newTaskUpdateTool(mw, turnLock)
	listTool := newTaskListTool(mw, turnLock)

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

type errBackend struct {
	lsInfoErr error
	readErr   error
	writeErr  error
	deleteErr error
	real      *inMemoryBackend
}

func (b *errBackend) LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error) {
	if b.lsInfoErr != nil {
		return nil, b.lsInfoErr
	}
	return b.real.LsInfo(ctx, req)
}

func (b *errBackend) Read(ctx context.Context, req *ReadRequest) (*fspkg.FileContent, error) {
	if b.readErr != nil {
		return nil, b.readErr
	}
	return b.real.Read(ctx, req)
}

func (b *errBackend) Write(ctx context.Context, req *WriteRequest) error {
	if b.writeErr != nil {
		return b.writeErr
	}
	return b.real.Write(ctx, req)
}

func (b *errBackend) Delete(ctx context.Context, req *DeleteRequest) error {
	if b.deleteErr != nil {
		return b.deleteErr
	}
	return b.real.Delete(ctx, req)
}

func TestWithTaskBaseDirResolver(t *testing.T) {
	resolver := func(ctx context.Context) string {
		return "/resolved/tasks"
	}
	opt := WithTaskBaseDirResolver(resolver)
	m := &middleware{}
	opt(m)
	assert.NotNil(t, m.taskBaseDirResolver)
	assert.Equal(t, "/resolved/tasks", m.taskBaseDirResolver(context.Background()))
}

func TestWithAgentNameResolver(t *testing.T) {
	resolver := func(ctx context.Context) string {
		return "agent-1"
	}
	opt := WithAgentNameResolver(resolver)
	m := &middleware{}
	opt(m)
	assert.NotNil(t, m.agentNameResolver)
	assert.Equal(t, "agent-1", m.agentNameResolver(context.Background()))
}

func TestWithTaskAssignedHook(t *testing.T) {
	called := false
	hook := func(ctx context.Context, assignment TaskAssignment) error {
		called = true
		return nil
	}
	opt := WithTaskAssignedHook(hook)
	m := &middleware{}
	opt(m)
	assert.NotNil(t, m.onTaskAssigned)
	_ = m.onTaskAssigned(context.Background(), TaskAssignment{})
	assert.True(t, called)
}

func TestWithReminder(t *testing.T) {
	called := false
	onReminder := func(ctx context.Context, reminderText string) {
		called = true
	}
	opt := WithReminder(5, onReminder)
	m := &middleware{}
	opt(m)
	assert.Equal(t, 5, m.reminderInterval)
	assert.NotNil(t, m.onReminder)
	m.onReminder(context.Background(), "test")
	assert.True(t, called)
}

func TestWithReminderNilCallback(t *testing.T) {
	opt := WithReminder(20, nil)
	m := &middleware{}
	opt(m)
	assert.Equal(t, 20, m.reminderInterval)
	assert.Nil(t, m.onReminder)
}

func TestMiddlewareCreateTask(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	taskID, err := mw.CreateTask(ctx, &TaskInput{Subject: "Test", Description: "Desc"})
	assert.NoError(t, err)
	assert.Equal(t, "1", taskID)

	taskID2, err := mw.CreateTask(ctx, &TaskInput{Subject: "Test 2", Description: "Desc 2"})
	assert.NoError(t, err)
	assert.Equal(t, "2", taskID2)

	content, err := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.NoError(t, err)
	var td task
	_ = sonic.UnmarshalString(content.Content, &td)
	assert.Equal(t, "Test", td.Subject)
	assert.Equal(t, taskStatusPending, td.Status)
}

func TestMiddlewareDeleteTask(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	_, err := mw.CreateTask(ctx, &TaskInput{Subject: "To delete", Description: "Desc"})
	assert.NoError(t, err)

	err = mw.DeleteTask(ctx, "1")
	assert.NoError(t, err)

	_, err = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	assert.Error(t, err)
}

func TestMiddlewareDeleteTaskInvalidID(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	err := mw.DeleteTask(ctx, "abc")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid task ID")
}

func TestMiddlewareDeleteTaskMissingTaskIsNoOp(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	err := mw.DeleteTask(ctx, "1")
	assert.NoError(t, err)
}

func TestUnassignOwnerTasksSuccess(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	t1 := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Owner: "alice", Blocks: []string{}, BlockedBy: []string{}}
	t2 := &task{ID: "2", Subject: "Task 2", Status: taskStatusInProgress, Owner: "alice", Blocks: []string{}, BlockedBy: []string{}}
	t3 := &task{ID: "3", Subject: "Task 3", Status: taskStatusPending, Owner: "bob", Blocks: []string{}, BlockedBy: []string{}}

	for _, td := range []*task{t1, t2, t3} {
		data, _ := sonic.MarshalString(td)
		_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, td.ID+".json"), Content: data})
	}

	unassigned, err := mw.UnassignOwnerTasks(ctx, "alice")
	assert.NoError(t, err)
	assert.Equal(t, []string{"1", "2"}, unassigned)

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "", updated.Owner)
	assert.Equal(t, taskStatusPending, updated.Status)

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "2.json")})
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "", updated.Owner)
	assert.Equal(t, taskStatusPending, updated.Status)

	content, _ = backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "3.json")})
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, "bob", updated.Owner)
}

func TestUnassignOwnerTasksNoMatch(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	td := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Owner: "bob", Blocks: []string{}, BlockedBy: []string{}}
	data, _ := sonic.MarshalString(td)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: data})

	unassigned, err := mw.UnassignOwnerTasks(ctx, "alice")
	assert.NoError(t, err)
	assert.Nil(t, unassigned)
}

func TestUnassignOwnerTasksListError(t *testing.T) {
	ctx := context.Background()
	real := newInMemoryBackend()
	backend := &errBackend{real: real, lsInfoErr: errors.New("ls failed")}
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	_, err := mw.UnassignOwnerTasks(ctx, "alice")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "list tasks for unassign")
}

func TestUnassignOwnerTasksWriteError(t *testing.T) {
	ctx := context.Background()
	real := newInMemoryBackend()
	baseDir := "/tmp/tasks"

	td := &task{ID: "1", Subject: "Task 1", Status: taskStatusPending, Owner: "alice", Blocks: []string{}, BlockedBy: []string{}}
	data, _ := sonic.MarshalString(td)
	_ = real.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: data})

	backend := &errBackend{real: real, writeErr: errors.New("write failed")}
	mw := testMiddleware(backend, baseDir)

	_, err := mw.UnassignOwnerTasks(ctx, "alice")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unassign task #1")
}

func TestUnassignOwnerTasksInProgressRevertedToPending(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()
	baseDir := "/tmp/tasks"
	mw := testMiddleware(backend, baseDir)

	td := &task{ID: "1", Subject: "Task 1", Status: taskStatusInProgress, Owner: "alice", Blocks: []string{}, BlockedBy: []string{}}
	data, _ := sonic.MarshalString(td)
	_ = backend.Write(ctx, &WriteRequest{FilePath: filepath.Join(baseDir, "1.json"), Content: data})

	unassigned, err := mw.UnassignOwnerTasks(ctx, "alice")
	assert.NoError(t, err)
	assert.Equal(t, []string{"1"}, unassigned)

	content, _ := backend.Read(ctx, &ReadRequest{FilePath: filepath.Join(baseDir, "1.json")})
	var updated task
	_ = sonic.UnmarshalString(content.Content, &updated)
	assert.Equal(t, taskStatusPending, updated.Status)
	assert.Equal(t, "", updated.Owner)
}

func TestResolveBaseDirWithResolver(t *testing.T) {
	ctx := context.Background()
	mw := &middleware{
		baseDir:             "/fallback",
		taskBaseDirResolver: func(ctx context.Context) string { return "/resolved" },
	}
	assert.Equal(t, "/resolved", mw.resolveBaseDir(ctx))
}

func TestResolveBaseDirResolverReturnsEmpty(t *testing.T) {
	ctx := context.Background()
	mw := &middleware{
		baseDir:             "/fallback",
		taskBaseDirResolver: func(ctx context.Context) string { return "" },
	}
	assert.Equal(t, "/fallback", mw.resolveBaseDir(ctx))
}

func TestResolveBaseDirWithoutResolver(t *testing.T) {
	ctx := context.Background()
	mw := &middleware{baseDir: "/fallback"}
	assert.Equal(t, "/fallback", mw.resolveBaseDir(ctx))
}

func TestUsesSharedTaskMode(t *testing.T) {
	mw := &middleware{}
	assert.False(t, mw.usesSharedTaskMode())

	mw.taskBaseDirResolver = func(ctx context.Context) string { return "/team" }
	assert.True(t, mw.usesSharedTaskMode())
}

func TestGetAgentNameWithResolver(t *testing.T) {
	ctx := context.Background()
	mw := &middleware{
		agentNameResolver: func(ctx context.Context) string { return "agent-x" },
	}
	assert.Equal(t, "agent-x", mw.getAgentName(ctx))
}

func TestGetAgentNameWithoutResolver(t *testing.T) {
	ctx := context.Background()
	mw := &middleware{}
	assert.Equal(t, "", mw.getAgentName(ctx))
}

func TestGetLockTeamMode(t *testing.T) {
	turnLock := &sync.RWMutex{}
	mw := &middleware{
		taskBaseDirResolver: func(ctx context.Context) string { return "/team" },
	}
	lock := mw.getLock(turnLock)
	assert.True(t, lock == &mw.taskLock)
	assert.True(t, lock != turnLock)
}

func TestGetLockNonTeamMode(t *testing.T) {
	turnLock := &sync.RWMutex{}
	mw := &middleware{}
	lock := mw.getLock(turnLock)
	assert.Equal(t, turnLock, lock)
}

func TestIsPlanTaskMiddleware(t *testing.T) {
	mw := &middleware{}
	mw.isPlanTaskMiddleware()

	var m Middleware = mw
	m.isPlanTaskMiddleware()
}

func TestNewWithAllOptions(t *testing.T) {
	ctx := context.Background()
	backend := newInMemoryBackend()

	hookCalled := false
	reminderCalled := false

	m, err := New(ctx, &Config{Backend: backend, BaseDir: "/tmp/tasks"},
		WithTaskBaseDirResolver(func(ctx context.Context) string { return "/custom/dir" }),
		WithAgentNameResolver(func(ctx context.Context) string { return "my-agent" }),
		WithTaskAssignedHook(func(ctx context.Context, assignment TaskAssignment) error {
			hookCalled = true
			return nil
		}),
		WithReminder(15, func(ctx context.Context, reminderText string) {
			reminderCalled = true
		}),
	)
	assert.NoError(t, err)
	assert.NotNil(t, m)

	mw := m.(*middleware)
	assert.Equal(t, "/tmp/tasks", mw.baseDir)
	assert.True(t, mw.usesSharedTaskMode())
	assert.Equal(t, "/custom/dir", mw.resolveBaseDir(ctx))
	assert.Equal(t, "my-agent", mw.getAgentName(ctx))
	assert.Equal(t, 15, mw.reminderInterval)

	_ = mw.onTaskAssigned(ctx, TaskAssignment{})
	assert.True(t, hookCalled)

	mw.onReminder(ctx, "test")
	assert.True(t, reminderCalled)
}
