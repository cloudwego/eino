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
	"log"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Config is the core configuration for the plantask middleware.
// Team-specific extensions are injected via Option functions.
type Config struct {
	// Backend is the storage backend for reading and writing task files.
	Backend Backend
	// BaseDir is the root directory where task files are stored.
	BaseDir string
}

// Logger is the logging interface used by the plantask middleware for
// best-effort, non-fatal diagnostics (e.g. an undeliverable assignment
// notification or an unparsable task file). Implementations must be safe for
// concurrent use. Inject one via WithLogger so these messages flow through the
// host's structured logger instead of the standard log package.
type Logger interface {
	Printf(format string, args ...any)
}

// stdLogger is the default Logger, used when WithLogger is not supplied. It
// preserves the previous behavior of writing to the standard log package.
type stdLogger struct{}

func (stdLogger) Printf(format string, args ...any) { log.Printf(format, args...) }

// Option configures optional behavior on the plantask middleware.
type Option func(*middleware)

// WithTaskBaseDirResolver enables the shared-task mode used by team integration.
// When set, resolveBaseDir calls this resolver instead of using baseDir directly.
// The resolver should return the full path to the task storage directory.
// When nil or returning "", single-agent baseDir is used as fallback.
func WithTaskBaseDirResolver(resolver func(ctx context.Context) string) Option {
	return func(m *middleware) {
		m.taskBaseDirResolver = resolver
	}
}

// WithAgentNameResolver sets the resolver for the current agent name.
// This is only consulted in shared-task mode (enabled by WithTaskBaseDirResolver),
// where it is used to auto-fill task ownership metadata such as
// TaskAssignment.AssignedBy and the implicit owner for in_progress tasks.
func WithAgentNameResolver(resolver func(ctx context.Context) string) Option {
	return func(m *middleware) {
		m.agentNameResolver = resolver
	}
}

// WithTaskAssignedHook registers a callback invoked when TaskUpdate changes a
// task's owner in shared-task mode (enabled by WithTaskBaseDirResolver).
// The team middleware uses this to send task_assignment messages to the
// assignee's mailbox.
func WithTaskAssignedHook(hook func(ctx context.Context, assignment TaskAssignment) error) Option {
	return func(m *middleware) {
		m.onTaskAssigned = hook
	}
}

// WithSharedTaskLock injects an external lock that replaces the per-instance
// taskLock for all task operations. This is used by team integration so that
// all agents in the same team serialize against a single shared lock.
func WithSharedTaskLock(lock *sync.RWMutex) Option {
	return func(m *middleware) {
		m.sharedTaskLock = lock
	}
}

// WithOwnerValidator registers a validator invoked when TaskUpdate sets a
// non-empty task owner in shared-task mode (enabled by WithTaskBaseDirResolver).
// It lets the embedding layer (e.g. team) reject assignments to identities that
// are not real members before the change is persisted and before any assignment
// notification is sent, preventing orphaned tasks owned by non-existent agents.
//
// The validator is only consulted for explicit owner changes; the implicit
// self-assignment performed when marking a task in_progress is trusted because
// it always uses the current agent's own name.
func WithOwnerValidator(validator func(ctx context.Context, owner string) error) Option {
	return func(m *middleware) {
		m.ownerValidator = validator
	}
}

// WithTaskGuard registers a guard consulted at the start of every task tool
// (TaskCreate / TaskUpdate / TaskGet / TaskList) before any storage access. When
// it returns a non-nil error, the tool call fails with that error instead of
// touching the task directory.
//
// The team integration uses this to reject task operations issued before a team
// has been created: until TeamCreate runs, the resolved task directory is not yet
// team-scoped, so tasks would otherwise be written to the wrong location.
func WithTaskGuard(guard func(ctx context.Context) error) Option {
	return func(m *middleware) {
		m.taskGuard = guard
	}
}

// WithReminder configures task reminder injection. The interval specifies how
// many assistant turns without TaskCreate/TaskUpdate before a reminder is
// injected. Set to negative to disable. Default is 10.
// When onReminder is non-nil, BeforeModelRewriteState calls onReminder with
// the reminder text and leaves the current state untouched, instead of
// injecting the reminder directly into state.Messages. Throttling is tracked
// via an internal assistant-turn counter so repeated reminders are still
// suppressed correctly.
func WithReminder(interval int, onReminder func(ctx context.Context, reminderText string)) Option {
	return func(m *middleware) {
		m.reminderInterval = interval
		m.onReminder = onReminder
	}
}

// WithLogger injects the Logger used for best-effort, non-fatal diagnostics.
// When unset, plantask falls back to the standard log package. Embedding layers
// such as the team middleware pass their own Logger here so plantask warnings
// (undeliverable assignment notifications, unparsable task files, best-effort
// cleanup failures) share the host's structured logging instead of bypassing it.
func WithLogger(logger Logger) Option {
	return func(m *middleware) {
		m.logger = logger
	}
}

// TaskAssignment contains information about a task ownership change emitted by
// the shared-task/team workflow.
type TaskAssignment struct {
	TaskID      string
	Subject     string
	Description string
	Owner       string // new owner (assignee)
	AssignedBy  string // who set the owner (from context)
}

// Middleware is the programmatic interface for driving plantask state outside of
// model tool calls. team.NewRunner uses it as a marker (via isPlanTaskMiddleware)
// to detect an already-present plantask middleware and avoid duplicate injection;
// it also exposes the concurrency-safe task operations that the package-level
// CreateTask/DeleteTask functions document as the preferred entry points.
type Middleware interface {
	isPlanTaskMiddleware()

	// CreateTask creates a task with proper locking and returns its ID. Prefer
	// this over the package-level CreateTask when a Middleware is available: it
	// shares the middleware's task lock (and the team lock in team mode) and honors
	// the configured task guard, so it is safe to call concurrently with tool calls.
	CreateTask(ctx context.Context, input *TaskInput) (string, error)

	// DeleteTask deletes a task with proper locking. Prefer this over the
	// package-level DeleteTask when a Middleware is available, for the same locking
	// and guard guarantees as CreateTask.
	DeleteTask(ctx context.Context, taskID string) error

	// UnassignOwnerTasks finds all tasks owned by the given owner, clears their
	// owner, reverts in_progress tasks to pending, and returns the unassigned task IDs.
	// This is used by the team layer when a teammate exits to release their tasks.
	UnassignOwnerTasks(ctx context.Context, owner string) ([]string, error)
}

// isPlanTaskMiddleware implements the Middleware marker interface.
func (m *middleware) isPlanTaskMiddleware() {}

// rwLock returns the effective read-write lock: the shared team lock when set,
// otherwise the per-instance lock.
func (m *middleware) rwLock() *sync.RWMutex {
	if m.sharedTaskLock != nil {
		return m.sharedTaskLock
	}
	return &m.taskLock
}

// CreateTask creates a task with proper locking. It resolves the baseDir from
// the context (team mode) or falls back to the configured baseDir. The configured
// task guard is consulted first so a programmatic create cannot bypass the team
// directory constraint that the TaskCreate tool enforces.
func (m *middleware) CreateTask(ctx context.Context, input *TaskInput) (string, error) {
	if err := m.checkGuard(ctx); err != nil {
		return "", err
	}

	lock := m.rwLock()
	lock.Lock()
	defer lock.Unlock()

	return createTaskLocked(ctx, m.backend, m.resolveBaseDir(ctx), input)
}

// DeleteTask deletes a task with proper locking. The configured task guard is
// consulted first so a programmatic delete cannot bypass the team directory
// constraint that the TaskUpdate/Delete tools enforce.
func (m *middleware) DeleteTask(ctx context.Context, taskID string) error {
	if err := m.checkGuard(ctx); err != nil {
		return err
	}

	lock := m.rwLock()
	lock.Lock()
	defer lock.Unlock()

	return deleteTaskLocked(ctx, m.backend, m.resolveBaseDir(ctx), taskID)
}

// UnassignOwnerTasks finds all tasks owned by the given owner, clears their owner,
// reverts in_progress tasks to pending, and returns the unassigned task IDs.
func (m *middleware) UnassignOwnerTasks(ctx context.Context, owner string) ([]string, error) {
	lock := m.rwLock()
	lock.Lock()
	defer lock.Unlock()

	baseDir := m.resolveBaseDir(ctx)
	tasks, err := listTasks(ctx, m.backend, baseDir, m.logger)
	if err != nil {
		return nil, fmt.Errorf("list tasks for unassign: %w", err)
	}

	var unassigned []string
	for _, t := range tasks {
		if t.Owner != owner {
			continue
		}
		t.Owner = ""
		if t.Status == taskStatusInProgress {
			t.Status = taskStatusPending
		}
		if err := writeTask(ctx, m.backend, baseDir, t); err != nil {
			return nil, fmt.Errorf("unassign task #%s: %w", t.ID, err)
		}
		unassigned = append(unassigned, t.ID)
	}

	return unassigned, nil
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
	return NewTyped[*schema.Message](ctx, config, opts...)
}

// NewTyped creates a new plantask middleware that provides task management tools for agents.
// It adds TaskCreate, TaskGet, TaskUpdate, and TaskList tools to the agent's tool set,
// allowing agents to create and manage structured task lists during coding sessions.
//
// This is the generic constructor that supports both *schema.Message and *schema.AgenticMessage.
// Use Option functions to enable team-specific extensions:
//
//	plantask.NewTyped[*schema.Message](ctx, config,
//	    plantask.WithTaskBaseDirResolver(resolver),
//	    plantask.WithTaskAssignedHook(hook),
//	    plantask.WithReminder(interval, callback))
func NewTyped[M adk.MessageType](_ context.Context, config *Config, opts ...Option) (adk.TypedChatModelAgentMiddleware[M], error) {
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
		reminderInterval: DefaultReminderInterval,
	}

	for _, opt := range opts {
		opt(m)
	}

	return &typedMiddleware[M]{middleware: m}, nil
}

// typedMiddleware is the generic adapter that exposes the message-type-agnostic
// middleware core as a TypedChatModelAgentMiddleware[M]. The embedded base
// provides default no-op hooks; BeforeAgent and BeforeModelRewriteState are
// overridden below.
type typedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	*middleware
}

// middleware holds the message-type-agnostic task state and helpers shared by
// the task tools and the typed adapter.
type middleware struct {
	backend        Backend
	baseDir        string
	taskLock       sync.RWMutex  // protects all task read/write operations within this middleware instance
	sharedTaskLock *sync.RWMutex // when non-nil, used instead of taskLock (team mode cross-agent lock)

	// Task reminder config (set via WithReminder) , 0 means disable
	reminderInterval int
	onReminder       func(ctx context.Context, reminderText string)

	// lastCallbackReminderAssistantCount stores the total number of assistant
	// messages in state.Messages at the time onReminder was last invoked.
	// Used to throttle subsequent reminders when onReminder is set, since the
	// callback path does not inject a _task_reminder marker into messages.
	lastCallbackReminderAssistantCount int

	// Task assignment notification (set via WithTaskAssignedHook)
	onTaskAssigned func(ctx context.Context, assignment TaskAssignment) error

	// Owner validation (set via WithOwnerValidator). When non-nil, an explicit
	// non-empty owner on TaskUpdate must pass this check before being persisted.
	ownerValidator func(ctx context.Context, owner string) error

	// taskGuard (set via WithTaskGuard). When non-nil, every task tool consults
	// it before any storage access and fails the call if it returns an error.
	taskGuard func(ctx context.Context) error

	// Context resolvers (set via WithTaskBaseDirResolver / WithAgentNameResolver, nil in single-agent mode)
	taskBaseDirResolver func(ctx context.Context) string
	agentNameResolver   func(ctx context.Context) string

	// logger (set via WithLogger) receives best-effort, non-fatal diagnostics.
	// nil means "use the standard log package"; access it through logger().
	logger Logger
}

// logger returns the configured Logger, falling back to the standard log package
// so non-fatal diagnostics are never silently discarded when none was injected.
func (m *middleware) effectiveLogger() Logger {
	if m.logger != nil {
		return m.logger
	}
	return stdLogger{}
}

// resolveBaseDir returns the task storage directory at call time.
// In shared-task mode, the taskBaseDirResolver provides the full path.
func (m *middleware) resolveBaseDir(ctx context.Context) string {
	if m.taskBaseDirResolver != nil {
		if dir := m.taskBaseDirResolver(ctx); dir != "" {
			return dir
		}
	}
	return m.baseDir
}

// checkGuard consults the optional taskGuard before a task tool touches storage.
// It returns nil when no guard is configured or the guard permits the operation.
func (m *middleware) checkGuard(ctx context.Context) error {
	if m.taskGuard == nil {
		return nil
	}
	return m.taskGuard(ctx)
}

// usesSharedTaskMode returns true when task storage is resolved dynamically
// from context and task operations should use the middleware-wide lock.
// This is the mode used by team integration.
func (m *middleware) usesSharedTaskMode() bool {
	return m.taskBaseDirResolver != nil
}

// getAgentName returns the current agent name, or empty if not set.
func (m *middleware) getAgentName(ctx context.Context) string {
	if m.agentNameResolver != nil {
		return m.agentNameResolver(ctx)
	}
	return ""
}

func (m *middleware) getLock(turnLock *sync.RWMutex) *sync.RWMutex {
	if m.usesSharedTaskMode() {
		if m.sharedTaskLock != nil {
			return m.sharedTaskLock
		}
		return &m.taskLock
	}
	return turnLock
}

func (m *typedMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	turnLock := &sync.RWMutex{}
	nRunCtx := *runCtx
	// In shared-task mode, tools share m.sharedTaskLock (or m.taskLock as fallback); otherwise they share the per-turn lock.
	nRunCtx.Tools = append(nRunCtx.Tools,
		newTaskCreateTool(m.middleware, turnLock),
		newTaskGetTool(m.middleware, turnLock),
		newTaskUpdateTool(m.middleware, turnLock),
		newTaskListTool(m.middleware, turnLock),
	)

	return ctx, &nRunCtx, nil
}

// BeforeModelRewriteState injects task reminders for *schema.Message agents.
// Task reminders are only active in shared-task (team) mode, which always uses
// *schema.Message; for any other message type this is a no-op.
func (m *typedMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], mc *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	msgState, ok := any(state).(*adk.ChatModelAgentState)
	if !ok {
		return ctx, state, nil
	}
	msgMC, ok := any(mc).(*adk.ModelContext)
	if !ok {
		return ctx, state, nil
	}

	ctx, nState, err := m.injectTaskReminder(ctx, msgState, msgMC)
	if err != nil {
		return ctx, nil, err
	}

	return ctx, any(nState).(*adk.TypedChatModelAgentState[M]), nil
}
