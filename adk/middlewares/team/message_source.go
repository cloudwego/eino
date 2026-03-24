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
	"encoding/xml"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
)

// MailboxSourceConfig configures the MailboxMessageSource behavior.
type MailboxSourceConfig struct {
	// OwnerName is the name of the agent that owns this mailbox.
	// Used to set TargetAgent in TurnInput.
	OwnerName string

	// Role determines exit conditions.
	Role teamRole

	// ExitWhenNoTeammates (Leader only): exit when no active teammates remain.
	ExitWhenNoTeammates bool

	// OnShutdownApproval (Leader only) is called when a shutdown_approved message is received.
	// It should handle: removing the member from team config, unassigning tasks, cancelling the teammate.
	// Returns the notification message text for the teammate_terminated system message.
	OnShutdownApproval func(ctx context.Context, fromName string) (string, error)

	// IdleNotifyInterval controls how often idle notifications are sent when no messages arrive.
	// Default is 2s; set negative to disable idle notifications.
	IdleNotifyInterval time.Duration
}

// MailboxMessageSource adapts a FileMailbox to TurnLoop's MessageSource[TurnInput].
type MailboxMessageSource struct {
	mailbox      *mailbox
	conf         *MailboxSourceConfig
	idleMu       sync.Mutex
	lastIdleSent time.Time
}

// NewMailboxMessageSource creates a new MailboxMessageSource.
func NewMailboxMessageSource(mailbox *mailbox, conf *MailboxSourceConfig) *MailboxMessageSource {
	if conf != nil && conf.IdleNotifyInterval == 0 {
		conf.IdleNotifyInterval = 2 * time.Second
	}
	return &MailboxMessageSource{
		mailbox: mailbox,
		conf:    conf,
	}
}

// Receive implements MessageSource[TurnInput].Receive.
func (s *MailboxMessageSource) Receive(ctx context.Context,
	_ adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {

	empty := TurnInput{}

	// Leader exit condition: no active teammates
	if s.conf.Role == teamRoleLeader && s.conf.ExitWhenNoTeammates && s.mailbox != nil {
		active, err := s.mailbox.HasActiveTeammates(ctx)
		if err != nil {
			return ctx, empty, nil, err
		}
		if !active {
			return ctx, empty, nil, adk.ErrLoopExit
		}
	}

	if s.mailbox == nil {
		return ctx, empty, nil, fmt.Errorf("mailbox is nil, cannot receive messages")
	}

	for {
		msgs, err := s.mailbox.WaitForMessages(ctx)
		if err != nil {
			return ctx, empty, nil, err
		}

		item, ok, err := s.consumeMessages(ctx, msgs)
		if err != nil {
			return ctx, empty, nil, err
		}
		if ok {
			return ctx, item, nil, nil
		}
	}
}

// Front implements MessageSource[TurnInput].Front.
func (s *MailboxMessageSource) Front(ctx context.Context,
	cfg adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	return s.Receive(ctx, cfg)
}

// TryReceive is a non-blocking read from the mailbox.
// Returns (item, true) if there are unread messages, or (empty, false) if none.
// Also handles shutdown_request detection for teammates.
func (s *MailboxMessageSource) TryReceive(ctx context.Context) (TurnInput, bool, error) {
	return s.tryReceive(ctx, true)
}

func (s *MailboxMessageSource) tryReceive(ctx context.Context, notifyIdle bool) (TurnInput, bool, error) {
	if s.mailbox == nil {
		return TurnInput{}, false, nil
	}

	msgs, err := s.mailbox.ReadUnread(ctx)
	if err != nil {
		return TurnInput{}, false, err
	}
	if len(msgs) == 0 {
		// Teammate: send idle_notification to leader when no messages
		if notifyIdle && s.conf.Role == teamRoleTeammate && s.shouldNotifyIdle() {
			_ = SendIdleNotification(ctx, s.mailbox, &idleInfo{
				AgentName: s.conf.OwnerName,
				Status:    "available",
			})
		}
		return TurnInput{}, false, nil
	}

	return s.consumeMessages(ctx, msgs)
}

func (s *MailboxMessageSource) shouldNotifyIdle() bool {
	if s.conf == nil {
		return false
	}
	if s.conf.IdleNotifyInterval < 0 {
		return false
	}

	interval := s.conf.IdleNotifyInterval
	if interval == 0 {
		interval = 2 * time.Second
	}

	now := time.Now()
	s.idleMu.Lock()
	defer s.idleMu.Unlock()
	if !s.lastIdleSent.IsZero() && now.Sub(s.lastIdleSent) < interval {
		return false
	}
	s.lastIdleSent = now
	return true
}

func (s *MailboxMessageSource) consumeMessages(ctx context.Context, msgs []InboxMessage) (TurnInput, bool, error) {
	if len(msgs) == 0 {
		return TurnInput{}, false, nil
	}

	var (
		err        error
		remaining  = msgs
		systemMsgs []InboxMessage
		toMark     []InboxMessage
	)

	if s.conf.Role == teamRoleLeader && s.conf.OnShutdownApproval != nil {
		remaining, systemMsgs, toMark, err = s.handleLeaderControlMessages(ctx, msgs)
		if err != nil {
			return TurnInput{}, false, err
		}
	} else {
		toMark = msgs
	}

	if len(toMark) > 0 {
		if err := s.mailbox.MarkRead(ctx, toMark); err != nil {
			return TurnInput{}, false, err
		}
	}

	if len(systemMsgs) > 0 {
		remaining = append(systemMsgs, remaining...)
	}
	if len(remaining) == 0 {
		return TurnInput{}, false, nil
	}

	return s.buildTurnInput(remaining), true, nil
}

func (s *MailboxMessageSource) handleLeaderControlMessages(ctx context.Context, msgs []InboxMessage) ([]InboxMessage, []InboxMessage, []InboxMessage, error) {
	if s.conf.Role != teamRoleLeader || s.conf.OnShutdownApproval == nil {
		return msgs, nil, msgs, nil
	}

	var remaining []InboxMessage
	var systemMsgs []InboxMessage
	var toMark []InboxMessage
	for _, m := range msgs {
		payload, err := decodeShutdownApproval(m.Text)
		if err != nil || payload.Type != string(messageTypeShutdownApproved) {
			remaining = append(remaining, m)
			toMark = append(toMark, m)
			continue
		}

		fromName := m.From
		if fromName == "" {
			fromName = payload.From
		}
		if fromName == "" || !payload.Approve {
			remaining = append(remaining, m)
			toMark = append(toMark, m)
			continue
		}

		notifyMsg, err := s.conf.OnShutdownApproval(ctx, fromName)
		if err != nil {
			systemMsg, buildErr := buildShutdownFailedSystemMessage(fromName, err)
			if buildErr != nil {
				return nil, nil, nil, buildErr
			}
			systemMsgs = append(systemMsgs, systemMsg)
			toMark = append(toMark, m)
			continue
		}

		systemMsg, err := buildTeammateTerminatedSystemMessage(notifyMsg)
		if err != nil {
			return nil, nil, nil, err
		}
		systemMsgs = append(systemMsgs, systemMsg)
		toMark = append(toMark, m)
	}

	return remaining, systemMsgs, toMark, nil
}

func buildTeammateTerminatedSystemMessage(notifyMsg string) (InboxMessage, error) {
	terminatedPayload := teammateTerminatedPayload{
		Type:    string(messageTypeTeammateTerminated),
		Message: notifyMsg,
	}
	text, err := sonic.MarshalString(terminatedPayload)
	if err != nil {
		return InboxMessage{}, err
	}
	return InboxMessage{
		From:      "system",
		Text:      text,
		Timestamp: utcNowMillis(),
	}, nil
}

func buildShutdownFailedSystemMessage(fromName string, cause error) (InboxMessage, error) {
	if fromName == "" {
		fromName = "unknown"
	}
	text, err := sonic.MarshalString(map[string]any{
		"type":  "shutdown_failed",
		"from":  fromName,
		"error": cause.Error(),
	})
	if err != nil {
		return InboxMessage{}, err
	}
	return InboxMessage{
		From:      "system",
		Text:      text,
		Timestamp: utcNowMillis(),
	}, nil
}

func (s *MailboxMessageSource) buildTurnInput(msgs []InboxMessage) TurnInput {
	targetAgent := s.conf.OwnerName
	if msgs[0].To != "" {
		targetAgent = msgs[0].To
	}

	return TurnInput{
		TargetAgent: targetAgent,
		Messages:    inboxMessagesToStrings(msgs),
	}
}

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

// inboxMessagesToStrings converts InboxMessages to teammate-message XML format.
func inboxMessagesToStrings(msgs []InboxMessage) []string {
	result := make([]string, 0, len(msgs))
	for _, m := range msgs {
		if m.Text == "" {
			continue
		}
		result = append(result, formatTeammateMessageEnvelope(m.From, m.Text, m.Summary))
	}
	return result
}

// <teammate-message teammate_id="system">
// {"type":"teammate_terminated","message":"security-reviewer has shut down."}
// </teammate-message>

// <teammate-message teammate_id="system">
// {"type":"teammate_terminated","message":"perf-reviewer has shut down."}
// </teammate-message>

// <teammate-message teammate_id="security-reviewer" color="blue">
// {"type":"shutdown_approved","requestId":"shutdown-1773283386010@security-reviewer","from":"security-reviewer","timestamp":"2026-03-12T02:43:10.260Z","paneId":"in-process","backendType":"in-process"}
// </teammate-message>

// <teammate-message teammate_id="perf-reviewer" color="green">
// {"type":"shutdown_approved","requestId":"shutdown-1773283386032@perf-reviewer","from":"perf-reviewer","timestamp":"2026-03-12T02:43:10.286Z","paneId":"in-process","backendType":"in-process"}
// </teammate-message>

// <teammate-message teammate_id="test-reviewer" color="yellow">
// {"type":"idle_notification","from":"test-reviewer","timestamp":"2026-03-12T02:43:12.117Z","idleReason":"available"}
// </teammate-message>

// <teammate-message teammate_id="system">
// {"type":"teammate_terminated","message":"test-reviewer has shut down."}
// </teammate-message>

// <teammate-message teammate_id="test-reviewer" color="yellow">
// {"type":"shutdown_approved","requestId":"shutdown-1773283386035@test-reviewer","from":"test-reviewer","timestamp":"2026-03-12T02:43:16.496Z","paneId":"in-process","backendType":"in-process"}
// </teammate-message>
