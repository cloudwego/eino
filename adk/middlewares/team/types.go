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

// Package team provides Agent Teams middleware for coordinating multiple agents
// via mailbox-based message passing and shared task lists.
//
// # Architecture
//
// The package is organised into the following layers. Tool implementations
// access infrastructure exclusively through the lifecycleManager facade,
// never through direct field access to router/pumpMgr/configStore.
//
//	┌─────────────────────────────────────────────────────────────┐
//	│  Runner (team_runner.go)                                    │
//	│    Entry point: creates TurnLoop, leader middleware, agent.  │
//	├─────────────────────────────────────────────────────────────┤
//	│  teamMiddleware (team.go)                                   │
//	│    Injects tool instances (Agent, SendMessage, TeamCreate,  │
//	│    TeamDelete) into each agent run via BeforeAgent.          │
//	│    Has no config/infra fields — delegates to lifecycle.      │
//	├─────────────────────────────────────────────────────────────┤
//	│  lifecycleManager (lifecycle.go)  ← central facade          │
//	│    Teammate spawn/cleanup/termination. Owns registry,       │
//	│    config store, router, pump manager, plantask, and        │
//	│    RunnerConfig. Exposes semantic methods to tool layer.     │
//	├─────────────────────────────────────────────────────────────┤
//	│  Messaging layer                                            │
//	│    sourceRouter    - routes TurnInput to agent TurnLoops     │
//	│    pumpManager     - per-agent mailbox→TurnLoop goroutines   │
//	│    MailboxMsgSrc   - control-message filtering & TurnInput   │
//	│    mailbox         - file-backed inbox read/write/poll       │
//	│                      (uses memberLister callback, not        │
//	│                       Config directly)                      │
//	├─────────────────────────────────────────────────────────────┤
//	│  Protocol (protocol.go)                                     │
//	│    Message types, serialisation, XML envelope formatting.    │
//	├─────────────────────────────────────────────────────────────┤
//	│  Storage (backend.go, team_config.go)                       │
//	│    Backend interface, path layout, config.json CRUD.         │
//	└─────────────────────────────────────────────────────────────┘
//
// # Message flow
//
// SendMessage tool → mailbox.Send → target inbox file → pumpManager reads →
// sourceRouter.Push → target TurnLoop → agent processes messages.
package team

import (
	"errors"
	"log"
	"time"
)

// ─── Constants ───────────────────────────────────────────────────────────────

const (
	// LeaderAgentName is the fixed agent name for the team leader.
	LeaderAgentName = "team-lead"

	// generalAgentName is the default agent type when none is specified.
	generalAgentName = "general-purpose"

	// defaultShutdownTimeout is the maximum time to wait for teammates to exit.
	defaultShutdownTimeout = 30 * time.Second

	// defaultPollInterval is the fallback polling interval for mailbox reads.
	defaultPollInterval = 500 * time.Millisecond
)

// ─── Errors ──────────────────────────────────────────────────────────────────

// errTeamNotFound is returned when no active team exists.
var errTeamNotFound = errors.New("no active team, create a team first with TeamCreate")

// Logger is the logging interface used by the team middleware.
// Implementations must be safe for concurrent use.
type Logger interface {
	Printf(format string, args ...any)
}

// defaultLogger wraps the standard log package.
type defaultLogger struct{}

func (defaultLogger) Printf(format string, args ...any) { log.Printf(format, args...) }

// nopLogger discards all log output.
type nopLogger struct{}

func (nopLogger) Printf(string, ...any) {}

// InboxMessage
// Each message is stored as an element in a JSON array file per agent.
type InboxMessage struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Text      string `json:"text"`
	Summary   string `json:"summary,omitempty"`
	Timestamp string `json:"timestamp"`
	Read      bool   `json:"read"`
}

// TurnInput carries routing information along with messages for multi-agent dispatch.
type TurnInput struct {
	// TargetAgent is the name of the agent that should handle this input.
	// Empty string means the team leader (main agent).
	TargetAgent string
	// Messages contains the actual messages for this turn.
	Messages []string
}
