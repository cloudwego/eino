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
	"sync"

	"github.com/cloudwego/eino/adk"
)

// Config is the core configuration for the plantask middleware.
// Team-specific extensions are injected via Option functions.
type Config struct {
	// Backend is the storage backend for reading and writing task files.
	Backend Backend
	// BaseDir is the root directory where task files are stored.
	BaseDir string
}

// Option configures optional behavior on the plantask middleware.
type Option func(*middleware)

// WithTaskBaseDirResolver enables team mode with a custom task directory resolver.
// When set, resolveBaseDir calls this resolver instead of using baseDir directly.
// The resolver should return the full path to the task storage directory.
// When nil or returning "", single-agent baseDir is used as fallback.
func WithTaskBaseDirResolver(resolver func(ctx context.Context) string) Option {
	return func(m *middleware) {
		m.taskBaseDirResolver = resolver
	}
}

// WithAgentNameResolver sets the resolver for the current agent name.
// Used to fill TaskAssignment.AssignedBy when a task's owner changes.
func WithAgentNameResolver(resolver func(ctx context.Context) string) Option {
	return func(m *middleware) {
		m.agentNameResolver = resolver
	}
}

// WithTaskAssignedHook registers a callback invoked when TaskUpdate sets a
// task's owner field. The team middleware uses this to send task_assignment
// messages to the assignee's mailbox.
func WithTaskAssignedHook(hook func(ctx context.Context, assignment TaskAssignment) error) Option {
	return func(m *middleware) {
		m.onTaskAssigned = hook
	}
}

// WithReminder configures task reminder injection. The interval specifies how
// many assistant turns without TaskCreate/TaskUpdate before a reminder is
// injected. Set to negative to disable. Default is 10.
// The optional callback is invoked when a reminder message is injected.
func WithReminder(interval int, onInjected func(ctx context.Context, msg adk.Message)) Option {
	return func(m *middleware) {
		m.reminderInterval = interval
		m.onReminderInjected = onInjected
	}
}

// TaskAssignment contains information about a task ownership change.
type TaskAssignment struct {
	TaskID      string
	Subject     string
	Description string
	Owner       string // new owner (assignee)
	AssignedBy  string // who set the owner (from context)
}

// Middleware is a marker interface for identifying plantask middleware instances.
// Used by team.NewRunner to detect if a plantask middleware is already present
// in user-provided handlers to avoid duplicate injection.
type Middleware interface {
	isPlantaskMiddleware()

	// CreateTask creates a task programmatically with proper locking.
	// This is the recommended way to create tasks from outside the tool call path
	// (e.g., when spawning teammates), as it shares the same lock with the tool
	// implementations to prevent concurrent read-then-write races on the highwatermark.
	CreateTask(ctx context.Context, input *TaskInput) (string, error)

	// DeleteTask deletes a task with proper locking.
	DeleteTask(ctx context.Context, taskID string) error
}

// isPlantaskMiddleware implements the Middleware marker interface.
func (m *middleware) isPlantaskMiddleware() {}

// CreateTask creates a task with proper locking. It resolves the baseDir from
// the context (team mode) or falls back to the configured baseDir.
func (m *middleware) CreateTask(ctx context.Context, input *TaskInput) (string, error) {
	m.taskLock.Lock()
	defer m.taskLock.Unlock()

	return createTaskLocked(ctx, m.backend, m.resolveBaseDir(ctx), input)
}

// DeleteTask deletes a task with proper locking.
func (m *middleware) DeleteTask(ctx context.Context, taskID string) error {
	m.taskLock.Lock()
	defer m.taskLock.Unlock()

	return deleteTaskLocked(ctx, m.backend, m.resolveBaseDir(ctx), taskID)
}

// New creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
//
// Use Option functions to enable team-specific extensions:
//
//	plantask.New(ctx, config,
//	    plantask.WithTaskBaseDirResolver(resolver),
//	    plantask.WithTaskAssignedHook(hook),
//	    plantask.WithReminder(interval, callback))
func New(ctx context.Context, config *Config, opts ...Option) (adk.ChatModelAgentMiddleware, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	m := &middleware{
		backend:          config.Backend,
		baseDir:          config.BaseDir,
		reminderInterval: defaultReminderInterval,
	}

	for _, opt := range opts {
		opt(m)
	}

	return m, nil
}

type middleware struct {
	adk.BaseChatModelAgentMiddleware
	backend  Backend
	baseDir  string
	taskLock sync.Mutex // protects all task read/write operations within this middleware instance

	// Task reminder config (set via WithReminder)
	reminderInterval   int
	onReminderInjected func(ctx context.Context, msg adk.Message)

	// Task assignment notification (set via WithTaskAssignedHook)
	onTaskAssigned func(ctx context.Context, assignment TaskAssignment) error

	// Context resolvers (set via WithTaskBaseDirResolver / WithAgentNameResolver, nil in single-agent mode)
	taskBaseDirResolver func(ctx context.Context) string
	agentNameResolver   func(ctx context.Context) string
}

// resolveBaseDir returns the task storage directory at call time.
// In team mode, the taskBaseDirResolver provides the full path.
func (m *middleware) resolveBaseDir(ctx context.Context) string {
	if m.taskBaseDirResolver != nil {
		if dir := m.taskBaseDirResolver(ctx); dir != "" {
			return dir
		}
	}
	return m.baseDir
}

// isTeamMode returns true if team mode is enabled.
func (m *middleware) isTeamMode() bool {
	return m.taskBaseDirResolver != nil
}

// getAgentName returns the current agent name, or empty if not set.
func (m *middleware) getAgentName(ctx context.Context) string {
	if m.agentNameResolver != nil {
		return m.agentNameResolver(ctx)
	}
	return ""
}

func (m *middleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	if !m.isTeamMode() {
		m.taskLock = sync.Mutex{}
	}
	nRunCtx := *runCtx
	// All tools share the same m.taskLock via the middleware reference
	nRunCtx.Tools = append(nRunCtx.Tools,
		newTaskCreateTool(m),
		newTaskGetTool(m),
		newTaskUpdateTool(m),
		newTaskListTool(m),
	)

	return ctx, &nRunCtx, nil
}
