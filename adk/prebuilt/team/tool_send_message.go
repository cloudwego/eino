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

// tool_send_message.go implements the SendMessage tool for DMs, broadcasts,
// shutdown requests/approvals, and plan approval responses.

package team

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type sendMessageTool struct {
	mw         *teamMiddleware
	senderName string
}

type sendMessageArgs struct {
	Type      string `json:"type"`
	Recipient string `json:"recipient,omitempty"`
	Content   string `json:"content,omitempty"`
	Summary   string `json:"summary,omitempty"`
	RequestID string `json:"request_id,omitempty"`
	Approve   *bool  `json:"approve,omitempty"`
}

func newSendMessageTool(mw *teamMiddleware, senderName string) (*sendMessageTool, error) {
	if senderName == "" {
		return nil, fmt.Errorf("senderName is required for SendMessage tool")
	}

	return &sendMessageTool{mw: mw, senderName: senderName}, nil
}

func (t *sendMessageTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: sendMessageToolName,
		Desc: selectToolDesc(sendMessageToolDesc, sendMessageToolDescChinese),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"type": {
				Type:     schema.String,
				Desc:     `Message type: "message" for DMs, "broadcast" to all teammates, "shutdown_request" to request shutdown, "shutdown_response" to respond to shutdown (approve or reject)`,
				Required: true,
				Enum:     []string{"message", "broadcast", "shutdown_request", "shutdown_response"},
			},
			"recipient": {
				Type: schema.String,
				Desc: `Agent name of the recipient (required for message, shutdown_request)`,
			},
			"content": {
				Type: schema.String,
				Desc: "Message text or reason",
			},
			"summary": {
				Type: schema.String,
				Desc: "A 5-10 word summary of the message, shown as a preview in the UI (required for message, broadcast)",
			},
			"request_id": {
				Type: schema.String,
				Desc: "Request ID to respond to (required for shutdown_response)",
			},
			"approve": {
				Type: schema.Boolean,
				Desc: "Whether to approve the request (required for shutdown_response)",
			},
		}),
	}, nil
}

func (t *sendMessageTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	// Hold the team-op read lock for the whole "read active team → validate
	// recipient → write inbox" sequence. This is a shared lock, so concurrent
	// SendMessage calls still run in parallel, but it excludes the exclusive
	// writers (TeamCreate/Agent/TeamDelete). Without it, TeamDelete could delete
	// the team directory and clear the team name in the window between this call
	// reading the active team (and passing membership validation) and actually
	// writing the inbox, producing a backend-dependent outcome (a failed write or
	// a recreated just-deleted path). The lock is held only on the leader's
	// middleware instance for any real exclusion; on a teammate's middleware it is
	// an uncontended RLock and thus effectively free.
	t.mw.teamOpLock.RLock()
	defer t.mw.teamOpLock.RUnlock()

	teamName := t.mw.getTeamName()
	if teamName == "" {
		return "", errTeamNotFound
	}

	var args sendMessageArgs
	if err := sonic.UnmarshalString(argumentsInJSON, &args); err != nil {
		return "", fmt.Errorf("parse SendMessage args: %w", err)
	}

	if args.Type == "" {
		return "", fmt.Errorf("'type' is required")
	}

	msgType, err := parseMessageType(args.Type)
	if err != nil {
		return "", err
	}

	if validateErr := t.validateArgs(msgType, &args); validateErr != nil {
		return "", validateErr
	}

	to, err := t.resolveRecipient(msgType, &args)
	if err != nil {
		return "", err
	}
	if validateErr := t.validateRecipient(ctx, teamName, msgType, to); validateErr != nil {
		return "", validateErr
	}

	approved := args.Approve != nil && *args.Approve

	msg, err := t.buildOutboxMessage(msgType, to, approved, &args)
	if err != nil {
		return "", err
	}

	mailbox := t.mw.lifecycle.mailbox(teamName, t.senderName)

	// Broadcast is best-effort and non-atomic: capture the per-member delivery
	// breakdown so the result can tell the model who actually received the message
	// rather than hiding partial failures behind a single aggregate error.
	if msgType == messageTypeBroadcast {
		bcast, bErr := mailbox.broadcast(ctx, msg)
		result := marshalToolResult(t.buildBroadcastResult(&args, bcast, bErr))
		return result, nil
	}

	if err := mailbox.Send(ctx, msg); err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}

	if msgType == messageTypeShutdownResponse && approved && t.senderName != LeaderAgentName {
		if err := adk.SendToolGenAction(ctx, sendMessageToolName, adk.NewExitAction()); err != nil {
			return "", fmt.Errorf("exit teammate after shutdown approval: %w", err)
		}
	}

	resultMap := t.buildResult(msgType, to, approved, msg, &args)
	// Membership validation only proves the recipient is listed in config.json;
	// it does not prove a runner is alive to consume the inbox. Surface a
	// non-fatal warning when the leader targets a teammate that has no running
	// goroutine (e.g. a residual member left by an incomplete cleanup) so the
	// model learns the message may sit unread instead of seeing a bare success.
	if warning := t.deliveryWarning(msgType, to); warning != "" {
		resultMap["delivery_warning"] = warning
	}

	return marshalToolResult(resultMap), nil
}

// deliveryWarning returns a human-readable warning when a point-to-point message
// is addressed to a recipient that is a config member but has no live runner to
// consume it. It returns "" when no warning applies.
//
// The check is intentionally limited to the leader: only the leader process owns
// the teammate registry, so only it can observe runtime liveness. A teammate's
// registry is always empty, and messages it sends to the leader are consumed by
// the leader's own pump (which is not tracked in the teammate registry), so a
// teammate could never make a reliable liveness judgement and must not warn.
// Broadcasts already report a per-member delivery breakdown and are skipped here.
func (t *sendMessageTool) deliveryWarning(msgType messageType, to string) string {
	if !t.mw.isLeader {
		return ""
	}
	if to == "" || to == broadcastTarget || to == LeaderAgentName {
		return ""
	}
	if msgType != messageTypeDM && msgType != messageTypeShutdownRequest {
		return ""
	}

	for _, name := range t.mw.lifecycle.activeTeammateNames() {
		if name == to {
			return ""
		}
	}
	return fmt.Sprintf(
		"recipient %q is a registered member but has no running goroutine; "+
			"the message was written to its inbox but will not be processed until "+
			"the teammate is (re)started", to,
	)
}

func (t *sendMessageTool) validateArgs(msgType messageType, args *sendMessageArgs) error {
	rule := sendMessageTypeRules[msgType]

	if rule.requiresRecipient && args.Recipient == "" {
		return fmt.Errorf("'recipient' is required for type %q", args.Type)
	}
	if rule.requiresContent && args.Content == "" {
		return fmt.Errorf("'content' is required for type %q", args.Type)
	}
	if rule.requiresSummary && args.Summary == "" {
		return fmt.Errorf("'summary' is required for type %q", args.Type)
	}
	if rule.requiresRequestID && args.RequestID == "" {
		return fmt.Errorf("'request_id' is required for type %q", args.Type)
	}
	if rule.requiresApprove && args.Approve == nil {
		return fmt.Errorf("'approve' is required for type %q", args.Type)
	}

	return nil
}

func (t *sendMessageTool) validateRecipient(ctx context.Context, teamName string, msgType messageType, to string) error {
	// Broadcast or empty recipient: no single-recipient validation needed.
	if to == "" || to == broadcastTarget {
		return nil
	}

	// All point-to-point message types require recipient membership validation.
	// Broadcast is already handled above (to == broadcastTarget).
	exists, err := t.mw.lifecycle.hasMember(ctx, teamName, to)
	if err != nil {
		return fmt.Errorf("check recipient %q: %w", to, err)
	}
	if !exists {
		return fmt.Errorf("recipient %q is not a member of team %q", to, teamName)
	}
	return nil
}

// resolveRecipient determines the message recipient based on message type.
func (t *sendMessageTool) resolveRecipient(msgType messageType, args *sendMessageArgs) (string, error) {
	switch msgType {
	case messageTypeBroadcast:
		return broadcastTarget, nil
	case messageTypeShutdownResponse:
		if args.Recipient == "" {
			return LeaderAgentName, nil
		}
		return args.Recipient, nil
	default:
		return args.Recipient, nil
	}
}

// buildOutboxMessage constructs the outbox message with properly formatted text.
func (t *sendMessageTool) buildOutboxMessage(msgType messageType, to string, approved bool, args *sendMessageArgs) (*outboxMessage, error) {
	msg := &outboxMessage{
		To:   to,
		Type: msgType,
	}

	switch msgType {
	case messageTypeDM, messageTypeBroadcast:
		msg.Text = args.Content
		msg.Summary = args.Summary

	case messageTypeShutdownRequest:
		msg.RequestID = t.shutdownRequestID(to)
		text, err := marshalShutdownRequest(t.senderName, msg.RequestID, args.Content)
		if err != nil {
			return nil, err
		}
		msg.Text = text

	case messageTypeShutdownResponse:
		text, err := marshalShutdownResponse(t.senderName, args.RequestID, approved, args.Content)
		if err != nil {
			return nil, err
		}
		msg.Text = text

	default:
		msg.Text = args.Content
	}

	return msg, nil
}

// shutdownRequestID generates a unique ID for a shutdown request. A UUID is used
// (rather than a timestamp) so two shutdown requests to the same target can never
// collide, even within the same nanosecond.
func (t *sendMessageTool) shutdownRequestID(to string) string {
	return fmt.Sprintf("shutdown-%s@%s", uuid.New().String(), to)
}

// buildResult constructs the response map for the tool invocation.
func (t *sendMessageTool) buildResult(msgType messageType, to string, approved bool, msg *outboxMessage, args *sendMessageArgs) map[string]any {
	result := map[string]any{"success": true}

	switch msgType {
	case messageTypeDM:
		result["message"] = fmt.Sprintf("Message sent to %s's inbox", to)
		result["routing"] = t.buildRoutingResult(to, args)
	case messageTypeShutdownRequest:
		result["message"] = fmt.Sprintf("Shutdown request sent to %s. Request ID: %s", to, msg.RequestID)
		result["request_id"] = msg.RequestID
		result["target"] = to
	case messageTypeShutdownResponse:
		result["message"] = buildApprovalResultMessage(msgType, to, approved)
	default:
		result["message"] = fmt.Sprintf("Message sent to %s", to)
	}

	return result
}

// buildBroadcastResult constructs the response map for a broadcast, exposing the
// per-member delivery breakdown. A broadcast is best-effort: success is true only
// when every teammate received the message; otherwise the failed recipients (and
// their errors) are reported so the model can decide whether to retry.
func (t *sendMessageTool) buildBroadcastResult(args *sendMessageArgs, bcast broadcastResult, bErr error) map[string]any {
	result := map[string]any{
		"success":   bErr == nil,
		"delivered": bcast.Delivered,
		"routing":   t.buildRoutingResult(broadcastTarget, args),
	}

	if bErr == nil {
		// A nil error with no recipients means the team has no other members, not
		// that the message reached the whole team. Use a distinct message so the
		// model does not read "0 delivered" as a successful fan-out.
		if len(bcast.Delivered) == 0 {
			result["message"] = "No other teammates to receive the broadcast"
			return result
		}
		result["message"] = fmt.Sprintf("Message broadcast to all teammates (%d delivered)", len(bcast.Delivered))
		return result
	}

	result["failed"] = bcast.Failed
	result["message"] = fmt.Sprintf(
		"Broadcast partially delivered: %d delivered, %d failed. Failed recipients still need the message.",
		len(bcast.Delivered), len(bcast.Failed),
	)
	return result
}

// buildApprovalResultMessage returns a human-readable result for approval-type messages.
func buildApprovalResultMessage(msgType messageType, to string, approved bool) string {
	switch msgType {
	case messageTypeShutdownResponse:
		if approved {
			return "Shutdown approved"
		}
		return "Shutdown rejected"
	default:
		return "OK"
	}
}

func (t *sendMessageTool) buildRoutingResult(target string, args *sendMessageArgs) map[string]any {
	if target != broadcastTarget {
		target = "@" + target
	}

	return map[string]any{
		"sender":  t.senderName,
		"target":  target,
		"summary": args.Summary,
		"content": args.Content,
	}
}
