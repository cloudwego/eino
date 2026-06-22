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
// mailboxMessageSource reads inbox messages, handles control-message filtering
// (shutdown response, teammate terminated), and builds TurnInput items.

package team

import (
	"context"
	"fmt"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// mailboxSourceConfig configures the mailboxMessageSource behavior.
type mailboxSourceConfig struct {
	// OwnerName is the name of the agent that owns this mailbox.
	// Used to set TargetAgent in TurnInput.
	OwnerName string

	// Role determines exit conditions.
	Role teamRole

	// OnShutdownResponse (Leader only) is called when a shutdown_response message is received.
	// It should handle: removing the member from team config, unassigning tasks, cancelling the teammate.
	// Returns the notification message text for the teammate_terminated system message.
	OnShutdownResponse func(ctx context.Context, fromName string) (string, error)

	// Logger for non-fatal warnings. If nil, a default logger is used so
	// best-effort I/O failures are still surfaced rather than silently dropped.
	Logger Logger
}

// mailboxMessageSource reads messages from a FileMailbox and produces TurnInput items.
type mailboxMessageSource struct {
	mailbox *mailbox
	conf    *mailboxSourceConfig

	processedCount         int
	lastIdleProcessedCount int
}

// newMailboxMessageSource creates a new mailboxMessageSource.
func newMailboxMessageSource(mailbox *mailbox, conf *mailboxSourceConfig) *mailboxMessageSource {
	return &mailboxMessageSource{
		mailbox: mailbox,
		conf:    conf,
	}
}

// logger returns the configured Logger, falling back to the standard log package
// so non-fatal warnings are never silently discarded when Logger is unset.
func (s *mailboxMessageSource) logger() Logger {
	if s.conf.Logger != nil {
		return s.conf.Logger
	}
	return defaultLogger{}
}

// tryReceive is a non-blocking read from the mailbox.
// Returns (item, true) if there are unread messages, or (empty, false) if none.
func (s *mailboxMessageSource) tryReceive(ctx context.Context, notifyIdle bool) (TurnInput, bool, error) {
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
			if err := sendIdleNotification(ctx, s.mailbox, s.conf.OwnerName, idleStatusAvailable); err != nil {
				// Best-effort: an idle notification is a hint to the leader, not a
				// correctness requirement, so log and continue rather than fail the read.
				s.logger().Printf("sendIdleNotification[%s]: %v", s.conf.OwnerName, err)
			}
		}
		return TurnInput{}, false, nil
	}

	return s.consumeMessages(ctx, msgs)
}

// waitForItem blocks until a message is available in the mailbox, then returns it.
func (s *mailboxMessageSource) waitForItem(ctx context.Context) (TurnInput, error) {
	empty := TurnInput{}

	if s.mailbox == nil {
		return empty, fmt.Errorf("mailbox is nil, cannot receive messages")
	}

	for {
		msgs, err := s.mailbox.waitForNewMessages(ctx)
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

func (s *mailboxMessageSource) consumeMessages(ctx context.Context, msgs []InboxMessage) (TurnInput, bool, error) {
	if len(msgs) == 0 {
		return TurnInput{}, false, nil
	}

	original := msgs

	// Mark the snapshot read BEFORE running control-message side effects.
	// handleLeaderControlMessages can trigger irreversible actions (e.g.
	// OnShutdownResponse → removeTeammate, which unassigns tasks and removes the
	// member from config). If MarkRead ran afterwards and failed, the same
	// shutdown_response would be observed again on the next poll and the side
	// effects would run a second time. Consuming the messages first makes a
	// failed control-message handler the only retry surface; the underlying
	// teardown is additionally guarded by idempotent firstStop checks.
	if err := s.mailbox.MarkRead(ctx, original); err != nil {
		return TurnInput{}, false, err
	}
	s.processedCount += len(original)

	msgs, err := s.handleLeaderControlMessages(ctx, msgs)
	if err != nil {
		return TurnInput{}, false, err
	}

	if len(msgs) == 0 {
		return TurnInput{}, false, nil
	}

	return s.buildTurnInput(msgs), true, nil
}

func (s *mailboxMessageSource) handleLeaderControlMessages(ctx context.Context, msgs []InboxMessage) ([]InboxMessage, error) {
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
				// The inbox snapshot was already consumed (MarkRead ran before this
				// handler so a successful side effect can never be replayed). A failure
				// here means graceful cleanup did not complete and will NOT be retried
				// from the mailbox, so surface it loudly instead of dropping it: log the
				// error and forward the original control message to the leader so a human
				// or the leader agent can react rather than losing the shutdown silently.
				s.logger().Printf("OnShutdownResponse[from=%s] failed, cleanup not retried: %v", fromName, err)
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
		From:      systemSender,
		Text:      text,
		Timestamp: utcNowMillis(),
	}, nil
}

func (s *mailboxMessageSource) buildTurnInput(msgs []InboxMessage) TurnInput {
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
