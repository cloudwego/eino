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
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for the team middleware.
//
// A Config must not be copied after it is first passed to NewRunner: it carries
// a sync.Once and lazily-initialized shared state, and copying it by value would
// duplicate that state and break the once-only initialization guarantee. Pass it
// by pointer instead.
type Config struct {
	// Backend is the storage backend for team data. Required.
	Backend Backend

	// BaseDir is the root directory for team data storage.
	// All team files (config, inboxes, tasks) are stored under this directory.
	// Required.
	BaseDir string

	// Interval is the interval in assistant turns between task reminders.
	// The zero value (i.e. leaving this field unset) selects the default of 10.
	// Set to a negative value to disable task reminders entirely.
	Interval int

	// PollInterval is how often the mailbox poller checks an inbox for new
	// messages. The zero value selects the default of 500ms. Lowering it reduces
	// message-delivery latency at the cost of more frequent backend reads; raising
	// it does the opposite.
	PollInterval time.Duration

	// state holds lazily-initialized internal fields. Separated from the public
	// fields above to make it clear which fields are part of the public API vs
	// internal bookkeeping.
	state    *configState
	initOnce sync.Once
}

func (c *Config) validate() error {
	if c == nil {
		return fmt.Errorf("TeamConfig is required")
	}
	if c.Backend == nil {
		return fmt.Errorf("TeamConfig.Backend is required")
	}
	if strings.TrimSpace(c.BaseDir) == "" {
		return fmt.Errorf("TeamConfig.BaseDir is required")
	}
	return nil
}

// configState holds the lazily-initialized shared resources for a Config.
// Created once by ensureInit() and shared by all mailboxes.
type configState struct {
	locks    *namedLockManager // shared named lock manager for inbox file access
	cfgLock  *sync.RWMutex     // dedicated lock for config.json read/write
	taskLock *sync.RWMutex     // shared task lock for cross-agent serialization in plantask
}

// ensureInit lazily initializes internal state (locks, cfgLock) if not already set.
// Thread-safe via sync.Once; called by NewRunner.
func (c *Config) ensureInit() {
	c.initOnce.Do(func() {
		locks := newNamedLockManager()
		// Config lock is a dedicated RWMutex, separate from the namedLockManager
		// used for inbox files, to avoid namespace collisions if an agent happens
		// to have a name that matches the config lock key.
		c.state = &configState{
			locks:    locks,
			cfgLock:  &sync.RWMutex{},
			taskLock: &sync.RWMutex{},
		}
	})
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

	// teamOpLock serializes team-lifecycle transitions that span multiple,
	// individually non-atomic steps. TeamCreate's "no active team → create dir →
	// setup leader mailbox → setTeamName" sequence and the Agent tool's
	// "read active team name → register/spawn teammate" sequence both read and
	// then mutate active-team state. Tool calls within a single assistant turn
	// may run in parallel (see compose tool_node parallelRunToolCall), so without
	// this lock two concurrent TeamCreate calls could both observe an empty team
	// name and each create a team, leaving an orphaned team directory and the
	// leader pump bound to the losing team. Held only by the leader.
	teamOpLock sync.Mutex

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
	runCtx *adk.ChatModelAgentContext[*schema.Message]) (context.Context, *adk.ChatModelAgentContext[*schema.Message], error) {

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
// unassigning tasks, removing the member from config, and deleting its inbox
// file. The wait honors ctx so callers can bound teardown to an external
// deadline; it is also capped at defaultShutdownTimeout internally.
func (mw *teamMiddleware) ShutdownAllTeammates(ctx context.Context) {
	mw.lifecycle.shutdownAll(ctx, mw.logger())
}
