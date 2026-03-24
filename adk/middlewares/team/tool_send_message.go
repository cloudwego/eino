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

package team

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const sendMessageToolName = "SendMessage"
const sendMessageToolDesc = `Send messages to agent teammates and handle protocol requests/responses in a team.

## Message Types

### type: "message" - Send a Direct Message

Send a message to a single specific teammate. You MUST specify the recipient.

**IMPORTANT for teammates**: Your plain text output is NOT visible to the team lead or other teammates. To communicate with anyone on your team, you MUST use this tool. Just typing a response or acknowledgment in text is not enough.

### type: "broadcast" - Send Message to ALL Teammates (USE SPARINGLY)

Send the same message to everyone on the team at once.

**WARNING: Broadcasting is expensive.** Each broadcast sends a separate message to every teammate, which means:
- N teammates = N separate message deliveries
- Each delivery consumes API resources
- Costs scale linearly with team size

**CRITICAL: Use broadcast only when absolutely necessary.** Valid use cases:
- Critical issues requiring immediate team-wide attention (e.g., "stop all work, blocking bug found")
- Major announcements that genuinely affect every teammate equally

**Default to "message" instead of "broadcast".** Use "message" for:
- Responding to a single teammate
- Normal back-and-forth communication
- Following up on a task with one person
- Sharing findings relevant to only some teammates
- Any message that doesn't require everyone's attention

### type: "shutdown_request" - Request a Teammate to Shut Down

Use this to ask a teammate to gracefully shut down. The teammate will receive a shutdown request and can either approve (exit) or reject (continue working).

### type: "shutdown_approved" - Respond to a Shutdown Request

When you receive a shutdown request as a JSON message with type: "shutdown_request", you MUST respond to approve or reject it. Do NOT just acknowledge the request in text - you must actually call this tool.

**IMPORTANT**: Extract the sender and requestId from the JSON message. Pass the sender as recipient and the requestId as request_id. Simply saying "I'll shut down" is not enough - you must call the tool.

### type: "plan_approval_response" - Approve or Reject a Teammate's Plan

When a teammate with plan_mode_required calls ExitPlanMode, they send you a plan approval request as a JSON message with type: "plan_approval_request". Use this to approve or reject their plan.

## Important Notes

- Messages from teammates are automatically delivered to you. You do NOT need to manually check your inbox.
- **IMPORTANT**: Always refer to teammates by their NAME (e.g., "team-lead", "researcher", "tester"), never by UUID.
- Do NOT send structured JSON status messages. Use TaskUpdate to mark tasks completed and the system will automatically send idle notifications when you stop.`

const sendMessageToolDescChinese = `向团队中的代理队友发送消息并处理协议请求/响应。

## 消息类型

### type: "message" - 发送直接消息

向单个特定队友发送消息。你必须指定收件人。

**队友须知**：你的纯文本输出对团队领导或其他队友不可见。要与团队中的任何人通信，你必须使用此工具。仅输入回复或确认文本是不够的。

### type: "broadcast" - 向所有队友发送消息（谨慎使用）

向团队中的每个人同时发送相同的消息。

**警告：广播开销很大。** 每次广播都会向每个队友发送单独的消息，这意味着：
- N 个队友 = N 次单独的消息投递
- 每次投递消耗 API 资源
- 成本随团队规模线性增长

**关键：仅在绝对必要时使用广播。** 有效的使用场景：
- 需要团队立即关注的关键问题（例如 "停止所有工作，发现阻塞性 bug"）
- 真正影响每个队友的重大公告

**默认使用 "message" 而非 "broadcast"。** 以下情况使用 "message"：
- 回复单个队友
- 正常的来回沟通
- 跟进某人的任务
- 分享仅与部分队友相关的发现
- 任何不需要所有人关注的消息

### type: "shutdown_request" - 请求队友关闭

请求队友优雅地关闭。队友会收到关闭请求，可以批准（退出）或拒绝（继续工作）。

### type: "shutdown_approved" - 响应关闭请求

当你收到 type 为 "shutdown_request" 的 JSON 消息时，你必须响应以批准或拒绝。不要只是在文本中确认请求 - 你必须实际调用此工具。

**重要**：从 JSON 消息中提取发送者和 requestId。将发送者作为 recipient，将 requestId 作为 request_id 传递给工具。仅说 "我将关闭" 是不够的 - 你必须调用工具。

### type: "plan_approval_response" - 批准或拒绝队友的计划

当具有 plan_mode_required 的队友调用 ExitPlanMode 时，他们会向你发送 type 为 "plan_approval_request" 的计划审批请求。使用此类型来批准或拒绝他们的计划。

## 重要说明

- 来自队友的消息会自动投递给你。你不需要手动检查收件箱。
- **重要**：始终通过名称引用队友（例如 "team-lead"、"researcher"、"tester"），而不是 UUID。
- 不要发送结构化的 JSON 状态消息。使用 TaskUpdate 标记任务完成，系统会在你停止时自动发送空闲通知。`

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

type sendMessageTypeRule struct {
	requiresRecipient bool
	requiresContent   bool
	requiresSummary   bool
	requiresRequestID bool
	requiresApprove   bool
	defaultRecipient  string
}

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
	messageTypeShutdownApproved: {
		requiresRecipient: true,
		requiresRequestID: true,
		requiresApprove:   true,
	},
	messageTypePlanApprovalResponse: {
		requiresRecipient: true,
		requiresRequestID: true,
		requiresApprove:   true,
	},
}

func newSendMessageTool(mw *teamMiddleware, senderName string) (*sendMessageTool, error) {
	if senderName == "" {
		return nil, fmt.Errorf("senderName is required for SendMessage tool")
	}

	return &sendMessageTool{mw: mw, senderName: senderName}, nil
}

func (t *sendMessageTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: sendMessageToolDesc,
		Chinese: sendMessageToolDescChinese,
	})

	return &schema.ToolInfo{
		Name: sendMessageToolName,
		Desc: desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"type": {
				Type:     schema.String,
				Desc:     `Message type: "message" for DMs, "broadcast" to all teammates, "shutdown_request" to request shutdown, "shutdown_approved" to respond to shutdown, "plan_approval_response" to approve/reject plans`,
				Required: true,
				Enum:     []string{"message", "broadcast", "shutdown_request", "shutdown_approved", "plan_approval_response"},
			},
			"recipient": {
				Type: schema.String,
				Desc: `Agent name of the recipient (required for message, shutdown_request, shutdown_approved, plan_approval_response)`,
			},
			"content": {
				Type: schema.String,
				Desc: "Message text, reason, or feedback",
			},
			"summary": {
				Type: schema.String,
				Desc: "A 5-10 word summary of the message, shown as a preview in the UI (required for message, broadcast)",
			},
			"request_id": {
				Type: schema.String,
				Desc: "Request ID to respond to (required for shutdown_response, plan_approval_response)",
			},
			"approve": {
				Type: schema.Boolean,
				Desc: "Whether to approve the request (required for shutdown_response, plan_approval_response)",
			},
		}),
	}, nil
}

func (t *sendMessageTool) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
	teamName := t.mw.teamName
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

	if args.Summary == "" && (msgType == messageTypeDM || msgType == messageTypeBroadcast) {
		args.Summary = buildSummary(args.Content)
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

	msg, err := t.buildOutboxMessage(msgType, to, &args)
	if err != nil {
		return "", err
	}

	mailbox := t.mw.mailbox(teamName, t.senderName)
	defer mailbox.Close()

	if err := mailbox.Send(ctx, msg); err != nil {
		return "", fmt.Errorf("send message: %w", err)
	}

	if msgType == messageTypeShutdownApproved && args.Approve != nil && *args.Approve && t.senderName != LeaderAgentName {
		if err := adk.SendToolGenAction(ctx, sendMessageToolName, adk.NewExitAction()); err != nil {
			return "", fmt.Errorf("exit teammate after shutdown approval: %w", err)
		}
	}

	result, _ := sonic.MarshalString(t.buildResult(msgType, to, msg, &args))

	return result, nil
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
	// to == "" is defensive: validateArgs already rejects empty recipient for types that require it,
	// but guard here in case new message types are added without requiresRecipient.
	// to == "*" is broadcast, no single-recipient validation needed.
	if to == "" || to == "*" {
		return nil
	}

	// Only point-to-point message types need recipient membership validation.
	switch msgType {
	case messageTypeDM, messageTypeShutdownRequest, messageTypeShutdownApproved, messageTypePlanApprovalResponse:
		// these require recipient validation, continue to HasMember check below
	default:
		// Currently unreachable: parseMessageType only allows types registered in sendMessageTypeRules
		// (DM, Broadcast, ShutdownRequest, ShutdownApproved, PlanApprovalResponse).
		// Other internal types (TaskAssignment, IdleNotification, TeammateTerminated, PlanApprovalRequest)
		// are sent by system code directly, never through this tool.
		// Among the allowed types, broadcast is already handled by the to == "*" check above,
		// and the rest are matched in the case above. Kept as a safe fallback for future types.
		return nil
	}

	// Verify the recipient is a registered member of the team.
	exists, err := t.mw.configStore().HasMember(ctx, teamName, to)
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
	rule := sendMessageTypeRules[msgType]
	if rule.defaultRecipient != "" {
		return rule.defaultRecipient, nil
	}

	switch msgType {
	case messageTypeBroadcast:
		return "*", nil
	default:
		return args.Recipient, nil
	}
}

// buildOutboxMessage constructs the outbox message with properly formatted text.
func (t *sendMessageTool) buildOutboxMessage(msgType messageType, to string, args *sendMessageArgs) (*outboxMessage, error) {
	msg := &outboxMessage{
		To:   to,
		Type: msgType,
	}

	approved := args.Approve != nil && *args.Approve

	switch msgType {
	case messageTypeDM, messageTypeBroadcast:
		msg.Text = args.Content
		msg.Summary = args.Summary

	case messageTypeShutdownRequest:
		msg.RequestID = t.shutdownRequestID(to)
		text, err := marshalShutdownRequest(t.senderName, msg.RequestID, args.Content)
		if err != nil {
			return nil, fmt.Errorf("marshal shutdown_request: %w", err)
		}
		msg.Text = text

	case messageTypeShutdownApproved:
		text, err := marshalShutdownApproval(t.senderName, args.RequestID, approved, args.Content)
		if err != nil {
			return nil, fmt.Errorf("marshal shutdown_approved: %w", err)
		}
		msg.Text = text

	case messageTypePlanApprovalResponse:
		text, err := marshalPlanApprovalResponse(t.senderName, args.RequestID, approved, args.Content)
		if err != nil {
			return nil, fmt.Errorf("marshal plan_approval_response: %w", err)
		}
		msg.Text = text

	default:
		msg.Text = args.Content
	}

	return msg, nil
}

// shutdownRequestID generates a unique ID for a shutdown request.
func (t *sendMessageTool) shutdownRequestID(to string) string {
	return fmt.Sprintf("shutdown-%d@%s", time.Now().UnixMilli(), to)
}

// buildResult constructs the response map for the tool invocation.
func (t *sendMessageTool) buildResult(msgType messageType, to string, msg *outboxMessage, args *sendMessageArgs) map[string]any {
	result := map[string]any{"success": true}
	approved := args.Approve != nil && *args.Approve

	switch msgType {
	case messageTypeDM:
		result["message"] = fmt.Sprintf("Message sent to %s's inbox", to)
		result["routing"] = t.buildRoutingResult(to, args)
	case messageTypeBroadcast:
		result["message"] = "Message broadcast to all teammates"
		result["routing"] = t.buildRoutingResult("*", args)
	case messageTypeShutdownRequest:
		result["message"] = fmt.Sprintf("Shutdown request sent to %s. Request ID: %s", to, msg.RequestID)
		result["request_id"] = msg.RequestID
		result["target"] = to
	case messageTypeShutdownApproved:
		if approved {
			result["message"] = "Shutdown approved"
		} else {
			result["message"] = "Shutdown rejected"
		}
	case messageTypePlanApprovalResponse:
		if approved {
			result["message"] = fmt.Sprintf("Plan approved for %s", to)
		} else {
			result["message"] = fmt.Sprintf("Plan rejected for %s", to)
		}
	default:
		result["message"] = fmt.Sprintf("Message sent to %s", to)
	}

	return result
}

func (t *sendMessageTool) buildRoutingResult(target string, args *sendMessageArgs) map[string]any {
	if target != "*" {
		target = "@" + target
	}

	return map[string]any{
		"sender":  t.senderName,
		"target":  target,
		"summary": args.Summary,
		"content": args.Content,
	}
}

func buildSummary(content string) string {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return ""
	}

	fields := strings.Fields(trimmed)
	if len(fields) > 10 {
		fields = fields[:10]
	}
	return truncateRunes(strings.Join(fields, " "), 80)
}

func truncateRunes(value string, limit int) string {
	if limit <= 0 || value == "" {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
