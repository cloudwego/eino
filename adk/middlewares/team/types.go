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

// Package team provides Agent Teams middleware for coordinating multiple agents.
package team

import (
	"context"
	"sync"
	"time"
)

// teamRole identifies the role of an agent in a team.
type teamRole string

const (
	// teamRoleLeader is the team lead that coordinates teammates.
	teamRoleLeader teamRole = "leader"
	// teamRoleTeammate is a teammate that works on assigned tasks.
	teamRoleTeammate teamRole = "teammate"
)

// LeaderAgentName is the fixed agent name for the team leader.
const LeaderAgentName = "team-lead"

// messageType identifies the type of a message in the mailbox system.
type messageType string

const (
	messageTypeDM                   messageType = "message"
	messageTypeBroadcast            messageType = "broadcast"
	messageTypeShutdownRequest      messageType = "shutdown_request"
	messageTypeShutdownApproved     messageType = "shutdown_approved"
	messageTypePlanApprovalRequest  messageType = "plan_approval_request"
	messageTypePlanApprovalResponse messageType = "plan_approval_response"
	messageTypeTaskAssignment       messageType = "task_assignment"
	messageTypeIdleNotification     messageType = "idle_notification"
	messageTypeTeammateTerminated   messageType = "teammate_terminated"
)

// InboxMessage matches Claude Code's inbox JSON format.
// Each message is stored as an element in a JSON array file per agent.
type InboxMessage struct {
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

// outboxMessage is used internally to route and send messages.
type outboxMessage struct {
	To        string      // recipient agent name or "*" for broadcast
	Type      messageType // for routing: broadcast vs DM
	Text      string      // the text field content
	Summary   string      // optional summary for DMs
	RequestID string      // request ID for shutdown requests
}

// teamConfig represents the team configuration stored in config.json.
type teamConfig struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	LeadAgentID string       `json:"leadAgentId,omitempty"`
	Members     []teamMember `json:"members"`
	CreatedAt   time.Time    `json:"createdAt"`
}

// teamMember represents a member in the team configuration.
type teamMember struct {
	Name      string    `json:"name"`
	AgentID   string    `json:"agentId,omitempty"`
	AgentType string    `json:"agentType,omitempty"`
	Prompt    string    `json:"prompt,omitempty"`
	JoinedAt  time.Time `json:"joinedAt"`
}

// spawnResult contains the result of spawning a teammate.
type spawnResult struct {
	Cancel context.CancelFunc
	// LocalTaskID is the ID of the _internal shadow task created for this teammate.
	// Used to delete the task when the teammate is shut down.
	LocalTaskID string
}

// idleInfo contains information about an idle agent.
type idleInfo struct {
	AgentName string   `json:"agentName"`
	TeamName  string   `json:"teamName"`
	Role      teamRole `json:"role"`
	TurnCount int      `json:"turnCount"`
	Status    string   `json:"status"`
}

// shutdownRequest is the structured payload for a shutdown request message.
type shutdownRequest struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId,omitempty"`
}

// shutdownResponse is the structured payload for a shutdown response message.
type shutdownResponse struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Approve   bool   `json:"approve"`
	Timestamp string `json:"timestamp,omitempty"`
}

// shutdownApprovedPayload is the parsed payload from a shutdown_approved inbox message.
type shutdownApprovedPayload struct {
	Type      string `json:"type"`
	RequestID string `json:"request_id,omitempty"`
	Approve   bool   `json:"approve"`
	From      string `json:"from,omitempty"`
}

// teammateTerminatedPayload is the system message injected when a teammate shuts down.
type teammateTerminatedPayload struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// inboxLockManager provides a shared per-inbox-file lock so that all mailbox
// instances writing to the same agent's inbox use the same mutex.
// This prevents lost updates when multiple agents send messages concurrently.
type inboxLockManager struct {
	mu    sync.Mutex
	locks map[string]*sync.RWMutex // key: inbox file owner name
}

func newInboxLockManager() *inboxLockManager {
	return &inboxLockManager{locks: make(map[string]*sync.RWMutex)}
}

// ForInbox returns the shared RWMutex for the given inbox owner.
// It lazily creates a new one if none exists yet.
func (m *inboxLockManager) ForInbox(owner string) *sync.RWMutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	if lk, ok := m.locks[owner]; ok {
		return lk
	}
	lk := &sync.RWMutex{}
	m.locks[owner] = lk
	return lk
}

// makeAgentID returns the agent ID in the format "name@team".
func makeAgentID(name, teamName string) string {
	return name + "@" + teamName
}
