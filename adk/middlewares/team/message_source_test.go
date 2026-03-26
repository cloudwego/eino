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
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
)

func TestNewMailboxMessageSource(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	conf := &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	}
	src := newMailboxMessageSource(mb, conf)

	assert.NotNil(t, src)
	assert.Same(t, mb, src.mailbox)
	assert.Same(t, conf, src.conf)
	assert.Equal(t, 0, src.processedCount)
	assert.Equal(t, 0, src.lastIdleProcessedCount)
}

func TestTryReceive_NilMailbox(t *testing.T) {
	src := newMailboxMessageSource(nil, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	item, ok, err := src.tryReceive(context.Background(), false)
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, TurnInput{}, item)
}

func TestTryReceive_NoMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	backend.files[inboxPath] = "[]"

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	item, ok, err := src.tryReceive(context.Background(), false)
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, TurnInput{}, item)
}

func TestTryReceive_WithMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	msgJSON, _ := sonic.MarshalString([]InboxMessage{
		{From: "sender", Text: "hello", Timestamp: utcNowMillis()},
	})
	backend.files[inboxPath] = msgJSON

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	item, ok, err := src.tryReceive(context.Background(), false)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "agent1", item.TargetAgent)
	assert.Len(t, item.Messages, 1)
	assert.Contains(t, item.Messages[0], "hello")
	assert.Contains(t, item.Messages[0], "sender")
}

func TestTryReceive_SendsIdleNotificationForTeammate(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	leaderInboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	backend.files[leaderInboxPath] = "[]"

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	ctx := context.Background()

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	ts := utcNowMillis()
	msgJSON, _ := sonic.MarshalString([]InboxMessage{
		{From: "sender", Text: "work", Timestamp: ts},
	})
	backend.files[inboxPath] = msgJSON

	_, _, err := src.consumeMessages(ctx, []InboxMessage{
		{From: "sender", Text: "work", Timestamp: ts},
	})
	assert.NoError(t, err)
	assert.Greater(t, src.processedCount, src.lastIdleProcessedCount)

	backend.files[inboxPath] = "[]"

	_, ok, err := src.tryReceive(ctx, true)
	assert.NoError(t, err)
	assert.False(t, ok)

	backend.mu.RLock()
	leaderInbox := backend.files[leaderInboxPath]
	backend.mu.RUnlock()

	var leaderMsgs []InboxMessage
	err = sonic.UnmarshalString(leaderInbox, &leaderMsgs)
	assert.NoError(t, err)
	assert.Len(t, leaderMsgs, 1)
	assert.Equal(t, "agent1", leaderMsgs[0].From)
	assert.Contains(t, leaderMsgs[0].Text, string(messageTypeIdleNotification))
}

func TestTryReceive_DoesNotSendIdleForLeader(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "team-lead",
		Role:      teamRoleLeader,
	})

	ctx := context.Background()

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	ts := utcNowMillis()
	msgJSON, _ := sonic.MarshalString([]InboxMessage{
		{From: "agent1", Text: "update", Timestamp: ts},
	})
	backend.files[inboxPath] = msgJSON

	_, _, err := src.consumeMessages(ctx, []InboxMessage{
		{From: "agent1", Text: "update", Timestamp: ts},
	})
	assert.NoError(t, err)
	assert.Greater(t, src.processedCount, src.lastIdleProcessedCount)

	backend.files[inboxPath] = "[]"

	agent1InboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	backend.files[agent1InboxPath] = "[]"

	_, ok, err := src.tryReceive(ctx, true)
	assert.NoError(t, err)
	assert.False(t, ok)

	backend.mu.RLock()
	agent1Inbox := backend.files[agent1InboxPath]
	backend.mu.RUnlock()
	assert.Equal(t, "[]", agent1Inbox)
}

func TestConsumeMessages_EmptyMsgs(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	item, ok, err := src.consumeMessages(context.Background(), []InboxMessage{})
	assert.NoError(t, err)
	assert.False(t, ok)
	assert.Equal(t, TurnInput{}, item)
}

func TestConsumeMessages_MarksMessagesAsRead(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	ts := utcNowMillis()
	msgs := []InboxMessage{
		{From: "sender", Text: "msg1", Timestamp: ts},
		{From: "sender2", Text: "msg2", Timestamp: ts},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	allMsgsJSON, _ := sonic.MarshalString(msgs)
	backend.files[inboxPath] = allMsgsJSON

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	ctx := context.Background()
	item, ok, err := src.consumeMessages(ctx, msgs)
	assert.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, "agent1", item.TargetAgent)

	// Messages are marked read immediately by consumeMessages.
	remaining, err := mb.readInbox(ctx, "agent1")
	assert.NoError(t, err)
	assert.Empty(t, remaining)
}

func TestHandleLeaderControlMessages_NonLeader(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	approvalJSON, _ := marshalShutdownResponse("agent1", "req-1", true, "done")
	msgs := []InboxMessage{
		{From: "agent1", Text: approvalJSON, Timestamp: utcNowMillis()},
	}

	result, err := src.handleLeaderControlMessages(context.Background(), msgs)
	assert.NoError(t, err)
	assert.Equal(t, msgs, result)
}

func TestHandleLeaderControlMessages_InterceptsShutdownResponse(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	var calledWith string
	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "team-lead",
		Role:      teamRoleLeader,
		OnShutdownResponse: func(ctx context.Context, fromName string) (string, error) {
			calledWith = fromName
			return fromName + " has shut down.", nil
		},
	})

	approvalJSON, _ := marshalShutdownResponse("agent1", "req-1", true, "done")
	msg := InboxMessage{From: "agent1", Text: approvalJSON, Timestamp: utcNowMillis()}

	result, err := src.handleLeaderControlMessages(context.Background(), []InboxMessage{msg})
	assert.NoError(t, err)
	assert.Equal(t, "agent1", calledWith)
	assert.Len(t, result, 1)
	assert.Equal(t, "system", result[0].From)
	assert.Contains(t, result[0].Text, string(messageTypeTeammateTerminated))
	assert.Contains(t, result[0].Text, "agent1 has shut down.")
}

func TestHandleLeaderControlMessages_ShutdownResponseFalseNotIntercepted(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	called := false
	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "team-lead",
		Role:      teamRoleLeader,
		OnShutdownResponse: func(ctx context.Context, fromName string) (string, error) {
			called = true
			return "", nil
		},
	})

	approvalJSON, _ := marshalShutdownResponse("agent1", "req-1", false, "not done yet")
	msg := InboxMessage{From: "agent1", Text: approvalJSON, Timestamp: utcNowMillis()}

	result, err := src.handleLeaderControlMessages(context.Background(), []InboxMessage{msg})
	assert.NoError(t, err)
	assert.False(t, called)
	assert.Len(t, result, 1)
	assert.Equal(t, "agent1", result[0].From)
}

func TestHandleLeaderControlMessages_NonShutdownPassesThrough(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	called := false
	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "team-lead",
		Role:      teamRoleLeader,
		OnShutdownResponse: func(ctx context.Context, fromName string) (string, error) {
			called = true
			return "", nil
		},
	})

	msgs := []InboxMessage{
		{From: "agent1", Text: "just a regular message", Timestamp: utcNowMillis()},
	}

	result, err := src.handleLeaderControlMessages(context.Background(), msgs)
	assert.NoError(t, err)
	assert.False(t, called)
	assert.Equal(t, msgs, result)
}

func TestHandleLeaderControlMessages_IdleNotificationPassedThrough(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "team-lead",
		Role:      teamRoleLeader,
	})

	idleJSON, _ := sonic.MarshalString(idleNotificationPayload{
		protocolHeader: newProtocolHeader(messageTypeIdleNotification, "agent1", ""),
		IdleReason:     "available",
	})
	msg := InboxMessage{From: "agent1", Text: idleJSON, Timestamp: utcNowMillis()}

	result, err := src.handleLeaderControlMessages(context.Background(), []InboxMessage{msg})
	assert.NoError(t, err)
	assert.Equal(t, []InboxMessage{msg}, result)
}

func TestBuildTeammateTerminatedSystemMessage(t *testing.T) {
	msg, err := buildTeammateTerminatedSystemMessage("agent1 has completed work")
	assert.NoError(t, err)
	assert.Equal(t, "system", msg.From)
	assert.NotEmpty(t, msg.Timestamp)

	var payload teammateTerminatedPayload
	err = sonic.UnmarshalString(msg.Text, &payload)
	assert.NoError(t, err)
	assert.Equal(t, string(messageTypeTeammateTerminated), payload.Type)
	assert.Equal(t, "agent1 has completed work", payload.Message)
}

func TestInboxMessagesToStrings_WithMessages(t *testing.T) {
	msgs := []InboxMessage{
		{From: "agent1", Text: "hello", Summary: "greeting"},
		{From: "agent2", Text: "", Summary: "empty"},
		{From: "agent3", Text: "world", Summary: ""},
	}

	result := inboxMessagesToStrings(msgs)
	assert.Len(t, result, 2)
	assert.Contains(t, result[0], "agent1")
	assert.Contains(t, result[0], "hello")
	assert.Contains(t, result[1], "agent3")
	assert.Contains(t, result[1], "world")
}

func TestInboxMessagesToStrings_EmptySlice(t *testing.T) {
	result := inboxMessagesToStrings([]InboxMessage{})
	assert.Empty(t, result)
}

func TestWaitForItem_NilMailbox(t *testing.T) {
	src := newMailboxMessageSource(nil, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	_, err := src.waitForItem(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "mailbox is nil")
}

func TestWaitForItem_LeaderExitWhenNoActiveTeammates(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead"}, nil
		},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	backend.files[inboxPath] = "[]"

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName:           "team-lead",
		Role:                teamRoleLeader,
		ExitWhenNoTeammates: true,
		HasActiveTeammates: func(ctx context.Context) (bool, error) {
			return false, nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := src.waitForItem(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no active teammates")
}

func TestWaitForItem_LeaderHasActiveTeammatesError(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead"}, nil
		},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	backend.files[inboxPath] = "[]"

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName:           "team-lead",
		Role:                teamRoleLeader,
		ExitWhenNoTeammates: true,
		HasActiveTeammates: func(ctx context.Context) (bool, error) {
			return false, fmt.Errorf("registry error")
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_, err := src.waitForItem(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "registry error")
}

func TestWaitForItem_LeaderWithActiveTeammatesReceivesMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "team-lead",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "worker"}, nil
		},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})

	go func() {
		time.Sleep(50 * time.Millisecond)
		msgs := []InboxMessage{{From: "worker", Text: "update", Timestamp: utcNowMillis()}}
		msgJSON, _ := sonic.MarshalString(msgs)
		_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})
	}()

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName:           "team-lead",
		Role:                teamRoleLeader,
		ExitWhenNoTeammates: true,
		HasActiveTeammates: func(ctx context.Context) (bool, error) {
			return true, nil
		},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	item, err := src.waitForItem(ctx)
	assert.NoError(t, err)
	assert.NotEmpty(t, item.Messages)
}

func TestWaitForItem_TeammateReceivesMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "worker",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "worker"}, nil
		},
	}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "worker.json")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})

	go func() {
		time.Sleep(50 * time.Millisecond)
		msgs := []InboxMessage{{From: "leader", Text: "do this", Timestamp: utcNowMillis()}}
		msgJSON, _ := sonic.MarshalString(msgs)
		_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})
	}()

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	item, err := src.waitForItem(ctx)
	assert.NoError(t, err)
	assert.NotEmpty(t, item.Messages)
	assert.Equal(t, "worker", item.TargetAgent)
}

func TestConsumeMessages_MarkReadError(t *testing.T) {
	eb := newErrBackend(errors.New("backend error"))
	locks := newNamedLockManager()
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      eb,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "agent1"}, nil
		},
	}

	src := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "agent1",
		Role:      teamRoleTeammate,
	})

	msgs := []InboxMessage{
		{From: "sender", Text: "hello", Timestamp: utcNowMillis()},
	}

	// consumeMessages should surface the MarkRead error directly.
	_, _, err := src.consumeMessages(context.Background(), msgs)
	assert.Error(t, err)
}

func TestBuildTeammateTerminatedSystemMessage_Valid(t *testing.T) {
	msg, err := buildTeammateTerminatedSystemMessage("worker has shut down.")
	assert.NoError(t, err)
	assert.Equal(t, "system", msg.From)
	assert.Contains(t, msg.Text, "teammate_terminated")
	assert.Contains(t, msg.Text, "worker has shut down.")
}
