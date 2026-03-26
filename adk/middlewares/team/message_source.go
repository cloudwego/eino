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

// message_source.go adapts the mailbox into a TurnInput producer.
// MailboxMessageSource reads inbox messages, handles control-message filtering
// (shutdown response, teammate terminated), and builds TurnInput items.

package team

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
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

	// HasActiveTeammates (Leader only) checks if there are active teammates.
	// Required when ExitWhenNoTeammates is true.
	HasActiveTeammates func(ctx context.Context) (bool, error)

	// OnShutdownResponse (Leader only) is called when a shutdown_response message is received.
	// It should handle: removing the member from team config, unassigning tasks, cancelling the teammate.
	// Returns the notification message text for the teammate_terminated system message.
	OnShutdownResponse func(ctx context.Context, fromName string) (string, error)

	// Logger for non-fatal warnings. If nil, errors are silently ignored.
	Logger Logger
}

// MailboxMessageSource reads messages from a FileMailbox and produces TurnInput items.
type MailboxMessageSource struct {
	mailbox *mailbox
	conf    *MailboxSourceConfig

	processedCount         int
	lastIdleProcessedCount int
}

// newMailboxMessageSource creates a new MailboxMessageSource.
func newMailboxMessageSource(mailbox *mailbox, conf *MailboxSourceConfig) *MailboxMessageSource {
	return &MailboxMessageSource{
		mailbox: mailbox,
		conf:    conf,
	}
}

// tryReceive is a non-blocking read from the mailbox.
// Returns (item, true) if there are unread messages, or (empty, false) if none.
func (s *MailboxMessageSource) tryReceive(ctx context.Context, notifyIdle bool) (TurnInput, bool, error) {
	if s.mailbox == nil {
		return TurnInput{}, false, nil
	}

	msgs, err := s.mailbox.ReadUnread(ctx)
	if err != nil {
		return TurnInput{}, false, err
	}
	if len(msgs) == 0 {
		if notifyIdle && s.conf.Role == teamRoleTeammate && s.processedCount > s.lastIdleProcessedCount {
			s.lastIdleProcessedCount = s.processedCount
			if err := sendIdleNotification(ctx, s.mailbox, s.conf.OwnerName, "available"); err != nil && s.conf.Logger != nil {
				s.conf.Logger.Printf("sendIdleNotification[%s]: %v", s.conf.OwnerName, err)
			}
		}
		return TurnInput{}, false, nil
	}

	return s.consumeMessages(ctx, msgs)
}

// waitForItem blocks until a message is available in the mailbox, then returns it.
func (s *MailboxMessageSource) waitForItem(ctx context.Context) (TurnInput, error) {
	empty := TurnInput{}

	if s.mailbox == nil {
		return empty, fmt.Errorf("mailbox is nil, cannot receive messages")
	}

	// Build an optional per-tick check so the leader can exit promptly when
	// the last teammate shuts down, even if no new inbox messages arrive.
	// The check runs inside the polling loop of waitForNewMessagesWithCheck,
	// so it is evaluated on every 500ms tick — not only when a message appears.
	var tickCheck func(ctx context.Context) error
	if s.conf.Role == teamRoleLeader && s.conf.ExitWhenNoTeammates && s.conf.HasActiveTeammates != nil {
		tickCheck = func(ctx context.Context) error {
			active, err := s.conf.HasActiveTeammates(ctx)
			if err != nil {
				return err
			}
			if !active {
				return fmt.Errorf("no active teammates")
			}
			return nil
		}
	}

	for {
		msgs, err := s.mailbox.waitForNewMessagesWithCheck(ctx, tickCheck)
		if err != nil {
			return empty, err
		}

		item, ok, err := s.consumeMessages(ctx, msgs)
		if err != nil {
			return empty, err
		}
		if ok {
			return item, nil
		}
	}
}

func (s *MailboxMessageSource) consumeMessages(ctx context.Context, msgs []InboxMessage) (TurnInput, bool, error) {
	if len(msgs) == 0 {
		return TurnInput{}, false, nil
	}

	original := msgs
	var err error
	msgs, err = s.handleLeaderControlMessages(ctx, msgs)
	if err != nil {
		return TurnInput{}, false, err
	}

	if err := s.mailbox.MarkRead(ctx, original); err != nil {
		return TurnInput{}, false, err
	}
	s.processedCount += len(original)

	if len(msgs) == 0 {
		return TurnInput{}, false, nil
	}

	return s.buildTurnInput(msgs), true, nil
}

func (s *MailboxMessageSource) handleLeaderControlMessages(ctx context.Context, msgs []InboxMessage) ([]InboxMessage, error) {
	if s.conf.Role != teamRoleLeader {
		return msgs, nil
	}

	var remaining []InboxMessage
	var systemMsgs []InboxMessage
	for _, m := range msgs {
		var header protocolHeader
		if err := sonic.UnmarshalString(m.Text, &header); err != nil {
			remaining = append(remaining, m)
			continue
		}
		switch messageType(header.Type) {
		case messageTypeShutdownResponse:
			if s.conf.OnShutdownResponse == nil {
				remaining = append(remaining, m)
				continue
			}
			payload, err := decodeShutdownResponse(m.Text)
			if err != nil {
				remaining = append(remaining, m)
				continue
			}

			fromName := m.From
			if fromName == "" {
				fromName = payload.From
			}
			if fromName == "" || !payload.Approve {
				remaining = append(remaining, m)
				continue
			}

			notifyMsg, err := s.conf.OnShutdownResponse(ctx, fromName)
			if err != nil {
				remaining = append(remaining, m)
				continue
			}
			if notifyMsg == "" {
				continue
			}

			systemMsg, err := buildTeammateTerminatedSystemMessage(notifyMsg)
			if err != nil {
				return nil, err
			}
			systemMsgs = append(systemMsgs, systemMsg)
		case messageTypeIdleNotification:
			remaining = append(remaining, m)
		default:
			remaining = append(remaining, m)
		}
	}

	return append(systemMsgs, remaining...), nil
}

func buildTeammateTerminatedSystemMessage(notifyMsg string) (InboxMessage, error) {
	terminatedPayload := teammateTerminatedPayload{
		protocolHeader: newProtocolHeader(messageTypeTeammateTerminated, "", ""),
		Message:        notifyMsg,
	}
	text, err := sonic.MarshalString(terminatedPayload)
	if err != nil {
		return InboxMessage{}, err
	}
	return InboxMessage{
		ID:        uuid.New().String(),
		From:      "system",
		Text:      text,
		Timestamp: utcNowMillis(),
	}, nil
}

func (s *MailboxMessageSource) buildTurnInput(msgs []InboxMessage) TurnInput {
	return TurnInput{
		TargetAgent: s.conf.OwnerName,
		Messages:    inboxMessagesToStrings(msgs),
	}
}

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
