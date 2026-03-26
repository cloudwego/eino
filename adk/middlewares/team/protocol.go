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

// protocol.go defines the wire-level message types, serialisation helpers,
// and envelope formatting used by the mailbox system (shutdown, idle,
// plan-approval, teammate-message XML envelopes, etc.).

package team

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"
)

// teamRole identifies the role of an agent in a team.
type teamRole string

const (
	// teamRoleLeader is the team lead that coordinates teammates.
	teamRoleLeader teamRole = "leader"
	// teamRoleTeammate is a teammate that works on assigned tasks.
	teamRoleTeammate teamRole = "teammate"
)

// messageType identifies the type of a message in the mailbox system.
type messageType string

const (
	messageTypeDM                 messageType = "message"
	messageTypeBroadcast          messageType = "broadcast"
	messageTypeShutdownRequest    messageType = "shutdown_request"
	messageTypeShutdownResponse   messageType = "shutdown_response"
	messageTypeTaskAssignment     messageType = "task_assignment"
	messageTypeIdleNotification   messageType = "idle_notification"
	messageTypeTeammateTerminated messageType = "teammate_terminated"
)

// protocolHeader contains the common fields shared by all protocol payloads.
type protocolHeader struct {
	Type      string `json:"type"`
	From      string `json:"from,omitempty"`
	Timestamp string `json:"timestamp,omitempty"`
	RequestID string `json:"requestId,omitempty"`
}

// sendMessageTypeRule defines validation requirements for each message type.
type sendMessageTypeRule struct {
	requiresRecipient bool
	requiresContent   bool
	requiresSummary   bool
	requiresRequestID bool
	requiresApprove   bool
}

// sendMessageTypeRules maps each supported message type to its validation rule.
var sendMessageTypeRules = map[messageType]sendMessageTypeRule{
	messageTypeDM: {
		requiresRecipient: true,
		requiresContent:   true,
		requiresSummary:   true,
	},
	messageTypeBroadcast: {
		requiresContent: true,
		requiresSummary: true,
	},
	messageTypeShutdownRequest: {
		requiresRecipient: true,
	},
	messageTypeShutdownResponse: {
		requiresRequestID: true,
		requiresApprove:   true,
	},
}

func parseMessageType(typeStr string) (messageType, error) {
	mt := messageType(typeStr)
	if _, ok := sendMessageTypeRules[mt]; ok {
		return mt, nil
	}
	return "", fmt.Errorf("unsupported message type %q", typeStr)
}

type shutdownRequestPayload struct {
	protocolHeader
	Reason string `json:"reason,omitempty"`
}

type shutdownResponsePayload struct {
	protocolHeader
	Approve bool   `json:"approve"`
	Reason  string `json:"reason,omitempty"`
}

// teammateTerminatedPayload is the system message injected when a teammate shuts down.
type teammateTerminatedPayload struct {
	protocolHeader
	Message string `json:"message"`
}

// outboxMessage is used internally to route and send messages.
type outboxMessage struct {
	To        string      // recipient agent name or "*" for broadcast
	Type      messageType // for routing: broadcast vs DM
	Text      string      // the text field content
	Summary   string      // optional summary for DMs
	RequestID string      // request ID for shutdown requests
}

// newProtocolHeader constructs a protocolHeader with the given type and from,
// automatically populating the timestamp. requestID is optional (pass "" to omit).
func newProtocolHeader(msgType messageType, from, requestID string) protocolHeader {
	return protocolHeader{
		Type:      string(msgType),
		From:      from,
		RequestID: requestID,
		Timestamp: utcNowMillis(),
	}
}

func marshalShutdownRequest(fromName, requestID, reason string) (string, error) {
	return sonic.MarshalString(shutdownRequestPayload{
		protocolHeader: newProtocolHeader(messageTypeShutdownRequest, fromName, requestID),
		Reason:         reason,
	})
}

func marshalShutdownResponse(fromName, requestID string, approve bool, reason string) (string, error) {
	return sonic.MarshalString(shutdownResponsePayload{
		protocolHeader: newProtocolHeader(messageTypeShutdownResponse, fromName, requestID),
		Approve:        approve,
		Reason:         reason,
	})
}

func decodeShutdownResponse(text string) (shutdownResponsePayload, error) {
	var p shutdownResponsePayload
	if err := sonic.UnmarshalString(text, &p); err != nil {
		return shutdownResponsePayload{}, err
	}
	return p, nil
}

func utcNowMillis() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

// formatTeammateMessageEnvelope wraps a message in an XML envelope for display
// in the agent's conversation context.
func formatTeammateMessageEnvelope(teammateID, text, summary string) string {
	var sb strings.Builder
	sb.WriteString(`<teammate-message teammate_id="`)
	xml.EscapeText(&sb, []byte(teammateID))
	sb.WriteString(`"`)
	if summary != "" {
		sb.WriteString(` summary="`)
		xml.EscapeText(&sb, []byte(summary))
		sb.WriteString(`"`)
	}
	sb.WriteString(">\n")
	sb.WriteString(sanitizeEnvelopeText(text))
	sb.WriteString("\n</teammate-message>")
	return sb.String()
}

func sanitizeEnvelopeText(text string) string {
	return strings.ReplaceAll(text, "</teammate-message>", "&lt;/teammate-message&gt;")
}

// ─── Idle notification ───────────────────────────────────────────────────────

// idleNotificationPayload is the typed payload for idle notifications.
type idleNotificationPayload struct {
	protocolHeader
	IdleReason string `json:"idleReason"`
}

// sendIdleNotification sends an idle notification from a teammate to the leader.
func sendIdleNotification(ctx context.Context, mb *mailbox, agentName, status string) error {
	text, err := sonic.MarshalString(idleNotificationPayload{
		protocolHeader: newProtocolHeader(messageTypeIdleNotification, agentName, ""),
		IdleReason:     status,
	})
	if err != nil {
		return fmt.Errorf("marshal idle info: %w", err)
	}
	return mb.Send(ctx, &outboxMessage{
		To:   LeaderAgentName,
		Type: messageTypeIdleNotification,
		Text: text,
	})
}
