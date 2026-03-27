/*
 * Copyright 2026 CloudWeGo Authors
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

// team.go defines Config (public configuration), teamMiddleware (tool injection
// via BeforeAgent), and factory functions for leader/teammate middleware instances.
//
// teamMiddleware is intentionally thin: it holds only the agent identity
// (isLeader, agentName, teamNameVal) and delegates all infrastructure access
// to the embedded lifecycleManager. This keeps the middleware focused on its
// single responsibility — injecting tools into the agent run context.

package team

import (
	"context"
	"sync"
	"sync/atomic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
)

// Config is the configuration for the team middleware.
type Config struct {
	// Backend is the storage backend for team data.
	Backend Backend

	// BaseDir is the root directory for team data storage.
	// All team files (config, inboxes, tasks) are stored under this directory.
	BaseDir string

	// Logger is the logger used by the team middleware.
	// If nil, the standard log package is used.
	Logger Logger

	// state holds lazily-initialized internal fields. Separated from Config to
	// make it clear which fields are part of the public API vs internal bookkeeping.
	state    *configState
	initOnce sync.Once

	// Interval is the interval in assistant turns between task reminders.
	// Default is 10.
	// Set to 0 to disable task reminders.
	Interval int

	// OnReminder is called when a task reminder is triggered for an agent.
	// It receives the agent name and the reminder text.
	// Typically wired internally by NewRunner to push the reminder into the
	// agent's TurnLoop via the shared router.
	OnReminder func(ctx context.Context, agentName string, reminderText string)
}

// configState holds the lazily-initialized shared resources for a Config.
// Created once by ensureInit() and shared by all mailboxes and config stores.
type configState struct {
	locks    *namedLockManager // shared named lock manager for inbox file access
	cfgStore *teamConfigStore  // shared config store, created once and reused by all mailboxes
}

// ensureInit lazily initializes internal state (locks, cfgStore) if not already set.
// Thread-safe via sync.Once; called by NewRunner.
func (c *Config) ensureInit() {
	c.initOnce.Do(func() {
		locks := newNamedLockManager()
		// Config store uses a dedicated lock, separate from the namedLockManager
		// used for inbox files, to avoid namespace collisions if an agent happens
		// to have a name that matches the config lock key.
		cfgLock := &sync.RWMutex{}
		c.state = &configState{
			locks:    locks,
			cfgStore: newTeamConfigStore(c.Backend, c.BaseDir, cfgLock),
		}
	})
}

// logger returns the configured Logger, falling back to the standard log package.
func (c *Config) logger() Logger {
	if c.Logger != nil {
		return c.Logger
	}
	return defaultLogger{}
}

// configStore returns the shared teamConfigStore from Config.state.
// Config.ensureInit() must have been called before invoking this method.
func (c *Config) configStore() *teamConfigStore {
	return c.state.cfgStore
}

// removeLock releases the named lock for a resource (e.g. an inbox) to prevent
// memory accumulation over many create/destroy cycles.
func (c *Config) removeLock(name string) {
	if c.state != nil && c.state.locks != nil {
		c.state.locks.Remove(name)
	}
}

func newTeamLeadMiddleware(conf *RunnerConfig, router *sourceRouter, pumpMgr *pumpManager) *teamMiddleware {
	return newMiddleware(conf, true, LeaderAgentName, router, pumpMgr)
}

func newTeamTeammateMiddleware(conf *RunnerConfig, agentName, teamName string) *teamMiddleware {
	// Teammates do not manage sub-teammates, so router and pumpMgr are nil.
	// Teammate lifecycle operations (spawn/cleanup) are always performed by the
	// leader's lifecycleManager which holds the real router and pumpMgr.
	mw := newMiddleware(conf, false, agentName, nil, nil)
	mw.setTeamName(teamName)
	return mw
}

// newMiddleware creates a new team middleware.
func newMiddleware(conf *RunnerConfig, isLeader bool, agentName string, router *sourceRouter, pumpMgr *pumpManager) *teamMiddleware {
	return &teamMiddleware{
		isLeader:  isLeader,
		agentName: agentName,
		lifecycle: newLifecycleManager(conf.TeamConfig, conf, isLeader, router, pumpMgr),
	}
}

// teamMiddleware is the core middleware that injects team tools (TeamCreate,
// TeamDelete, Agent, SendMessage) into each agent run via BeforeAgent.
// Lifecycle management (teammate spawn/cleanup/termination) is delegated
// to the embedded lifecycleManager.
type teamMiddleware struct {
	*adk.BaseChatModelAgentMiddleware
	isLeader  bool
	agentName string

	teamNameVal atomic.Value // stores string; set at creation for teammates; set by TeamCreate for leader

	lifecycle *lifecycleManager // teammate lifecycle: registry, config, routing, plantask
}

// logger returns the configured Logger from the lifecycle manager.
func (mw *teamMiddleware) logger() Logger {
	return mw.lifecycle.logger
}

// getTeamName returns the current team name (thread-safe).
func (mw *teamMiddleware) getTeamName() string {
	if v := mw.teamNameVal.Load(); v != nil {
		return v.(string)
	}
	return ""
}

// setTeamName sets the team name (thread-safe).
func (mw *teamMiddleware) setTeamName(name string) {
	mw.teamNameVal.Store(name)
}

// BeforeAgent injects team tools before each agent run.
func (mw *teamMiddleware) BeforeAgent(ctx context.Context,
	runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {

	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	var tools []tool.BaseTool

	if mw.isLeader {
		tools = append(tools,
			newTeamCreateTool(mw),
			newTeamDeleteTool(mw),
			newAgentTool(mw),
		)
	}

	// SendMessage is available to both Leader and Teammate
	sendMsgTool, err := newSendMessageTool(mw, mw.agentName)
	if err != nil {
		return ctx, nil, err
	}
	tools = append(tools, sendMsgTool)

	nRunCtx.Tools = append(nRunCtx.Tools, tools...)
	return ctx, &nRunCtx, nil
}

// ShutdownAllTeammates cancels all active teammates and waits for their
// goroutines to exit. Each goroutine's deferred cleanupExitedTeammate handles
// unassigning tasks, removing from config, and deleting shadow tasks.
func (mw *teamMiddleware) ShutdownAllTeammates(ctx context.Context, teamName string) {
	mw.lifecycle.shutdownAll(mw.logger())
}
