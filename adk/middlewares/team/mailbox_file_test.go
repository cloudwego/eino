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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func newTestMailbox(backend Backend, baseDir, teamName, ownerName string, members []string) *mailbox {
	return &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      baseDir,
			TeamName:     teamName,
			OwnerName:    ownerName,
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: newNamedLockManager(),
		listMembers: func(ctx context.Context) ([]string, error) {
			return members, nil
		},
	}
}

func TestInitInboxFile_CreatesFileWithEmptyArray(t *testing.T) {
	backend := newInMemoryBackend()
	ctx := context.Background()
	inboxPath := "/tmp/test/teams/myteam/inboxes/agent1.json"

	err := initInboxFile(ctx, backend, inboxPath)
	assert.NoError(t, err)

	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()
	assert.Equal(t, "[]", content)
}

func TestInitInboxFile_Idempotent(t *testing.T) {
	backend := newInMemoryBackend()
	ctx := context.Background()
	inboxPath := "/tmp/test/teams/myteam/inboxes/agent1.json"

	err := initInboxFile(ctx, backend, inboxPath)
	assert.NoError(t, err)

	backend.mu.Lock()
	backend.files[inboxPath] = `[{"from":"x","text":"existing"}]`
	backend.mu.Unlock()

	err = initInboxFile(ctx, backend, inboxPath)
	assert.NoError(t, err)

	backend.mu.RLock()
	content := backend.files[inboxPath]
	backend.mu.RUnlock()
	assert.Equal(t, `[{"from":"x","text":"existing"}]`, content)
}

func TestInboxFilePathForOwner(t *testing.T) {
	mb := newTestMailbox(newInMemoryBackend(), "/data", "alpha-team", "leader", nil)

	path := mb.inboxFilePathForOwner("worker-1")
	expected := filepath.Join("/data", "teams", "alpha-team", "inboxes", "worker-1.json")
	assert.Equal(t, expected, path)
}

func TestReadInbox_NonExistentFile_ReturnsNil(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)

	msgs, err := mb.readInbox(context.Background(), "agent1")
	assert.NoError(t, err)
	assert.Nil(t, msgs)
}

func TestReadInbox_EmptyFileContent_ReturnsNil(t *testing.T) {
	backend := newInMemoryBackend()
	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	backend.mu.Lock()
	backend.files[inboxPath] = ""
	backend.mu.Unlock()

	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)

	msgs, err := mb.readInbox(context.Background(), "agent1")
	assert.NoError(t, err)
	assert.Nil(t, msgs)
}

func TestReadInbox_ValidJSON(t *testing.T) {
	backend := newInMemoryBackend()
	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	backend.mu.Lock()
	backend.files[inboxPath] = `[{"from":"leader","to":"agent1","text":"hello","timestamp":"2025-01-01T00:00:00.000Z","read":false}]`
	backend.mu.Unlock()

	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)

	msgs, err := mb.readInbox(context.Background(), "agent1")
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "leader", msgs[0].From)
	assert.Equal(t, "agent1", msgs[0].To)
	assert.Equal(t, "hello", msgs[0].Text)
	assert.Equal(t, false, msgs[0].Read)
}

func TestWriteInbox_WriteAndReadBack(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)
	ctx := context.Background()

	msgs := []InboxMessage{
		{From: "leader", To: "agent1", Text: "task1", Timestamp: "2025-01-01T00:00:00.000Z", Read: false},
		{From: "agent2", To: "agent1", Text: "update", Timestamp: "2025-01-01T00:00:01.000Z", Read: true},
	}

	err := mb.writeInbox(ctx, "agent1", msgs)
	assert.NoError(t, err)

	readMsgs, err := mb.readInbox(ctx, "agent1")
	assert.NoError(t, err)
	assert.Len(t, readMsgs, 2)
	assert.Equal(t, "task1", readMsgs[0].Text)
	assert.Equal(t, "update", readMsgs[1].Text)
	assert.Equal(t, false, readMsgs[0].Read)
	assert.Equal(t, true, readMsgs[1].Read)
}

func TestSend_DM(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "leader", []string{"leader", "agent1", "agent2"})
	ctx := context.Background()

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	assert.NoError(t, initInboxFile(ctx, backend, inboxPath))

	err := mb.Send(ctx, &outboxMessage{
		To:      "agent1",
		Type:    messageTypeDM,
		Text:    "do this task",
		Summary: "task assignment",
	})
	assert.NoError(t, err)

	msgs, err := mb.readInbox(ctx, "agent1")
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "leader", msgs[0].From)
	assert.Equal(t, "agent1", msgs[0].To)
	assert.Equal(t, "do this task", msgs[0].Text)
	assert.Equal(t, "task assignment", msgs[0].Summary)
	assert.Equal(t, false, msgs[0].Read)
	assert.NotEmpty(t, msgs[0].Timestamp)
}

func TestSend_Broadcast(t *testing.T) {
	backend := newInMemoryBackend()
	members := []string{"team-lead", "agent1", "agent2"}
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "team-lead", members)
	ctx := context.Background()

	for _, name := range members {
		inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", name+".json")
		assert.NoError(t, initInboxFile(ctx, backend, inboxPath))
	}

	err := mb.Send(ctx, &outboxMessage{
		To:      "*",
		Type:    messageTypeBroadcast,
		Text:    "broadcast msg",
		Summary: "important",
	})
	assert.NoError(t, err)

	agent1Msgs, err := mb.readInbox(ctx, "agent1")
	assert.NoError(t, err)
	assert.Len(t, agent1Msgs, 1)
	assert.Equal(t, "broadcast msg", agent1Msgs[0].Text)
	assert.Equal(t, "team-lead", agent1Msgs[0].From)

	agent2Msgs, err := mb.readInbox(ctx, "agent2")
	assert.NoError(t, err)
	assert.Len(t, agent2Msgs, 1)
	assert.Equal(t, "broadcast msg", agent2Msgs[0].Text)

	leaderMsgs, err := mb.readInbox(ctx, "team-lead")
	assert.NoError(t, err)
	assert.Len(t, leaderMsgs, 0)
}

func TestReadUnread_ReturnsOnlyUnread(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)
	ctx := context.Background()

	msgs := []InboxMessage{
		{From: "leader", To: "agent1", Text: "read msg", Timestamp: "t1", Read: true},
		{From: "leader", To: "agent1", Text: "unread msg1", Timestamp: "t2", Read: false},
		{From: "agent2", To: "agent1", Text: "unread msg2", Timestamp: "t3", Read: false},
	}
	assert.NoError(t, mb.writeInbox(ctx, "agent1", msgs))

	unread, err := mb.ReadUnread(ctx)
	assert.NoError(t, err)
	assert.Len(t, unread, 2)
	assert.Equal(t, "unread msg1", unread[0].Text)
	assert.Equal(t, "unread msg2", unread[1].Text)
}

func TestMarkRead_RemovesSpecifiedMessages(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)
	ctx := context.Background()

	msgs := []InboxMessage{
		{ID: "id-1", From: "leader", To: "agent1", Text: "msg1", Summary: "s1", Timestamp: "t1", Read: false},
		{ID: "id-2", From: "leader", To: "agent1", Text: "msg2", Summary: "s2", Timestamp: "t2", Read: false},
		{ID: "id-3", From: "agent2", To: "agent1", Text: "msg3", Summary: "s3", Timestamp: "t3", Read: false},
	}
	assert.NoError(t, mb.writeInbox(ctx, "agent1", msgs))

	err := mb.MarkRead(ctx, []InboxMessage{msgs[0], msgs[2]})
	assert.NoError(t, err)

	remaining, err := mb.readInbox(ctx, "agent1")
	assert.NoError(t, err)
	assert.Len(t, remaining, 1)
	assert.Equal(t, "msg2", remaining[0].Text)
}

func TestMarkRead_EmptySlice_NoOp(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)
	ctx := context.Background()

	msgs := []InboxMessage{
		{From: "leader", To: "agent1", Text: "msg1", Timestamp: "t1", Read: false},
	}
	assert.NoError(t, mb.writeInbox(ctx, "agent1", msgs))

	err := mb.MarkRead(ctx, []InboxMessage{})
	assert.NoError(t, err)

	remaining, err := mb.readInbox(ctx, "agent1")
	assert.NoError(t, err)
	assert.Len(t, remaining, 1)
	assert.Equal(t, "msg1", remaining[0].Text)
}

func TestWaitForMessages_ExistingMessages_ReturnsImmediately(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)
	ctx := context.Background()

	msgs := []InboxMessage{
		{From: "leader", To: "agent1", Text: "existing", Timestamp: "t1", Read: false},
	}
	assert.NoError(t, mb.writeInbox(ctx, "agent1", msgs))

	result, err := mb.WaitForMessages(ctx)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "existing", result[0].Text)
}

func TestWaitForMessages_NoMessages_BlocksUntilContextCancelled(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	assert.NoError(t, initInboxFile(context.Background(), backend, inboxPath))

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := mb.WaitForMessages(ctx)
	assert.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestWaitForNewMessages_PollsAndFindsNewMessages(t *testing.T) {
	backend := newInMemoryBackend()
	members := []string{"team-lead", "agent1"}
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", members)

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	assert.NoError(t, initInboxFile(context.Background(), backend, inboxPath))

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	senderMb := newTestMailbox(backend, "/tmp/test", "myteam", "team-lead", members)
	senderMb.inboxLocks = mb.inboxLocks

	go func() {
		time.Sleep(30 * time.Millisecond)
		_ = senderMb.sendToOne(context.Background(), "agent1", &outboxMessage{
			To:      "agent1",
			Type:    messageTypeDM,
			Text:    "delayed message",
			Summary: "test",
		})
	}()

	msgs, err := mb.WaitForMessages(ctx)
	assert.NoError(t, err)
	assert.Len(t, msgs, 1)
	assert.Equal(t, "delayed message", msgs[0].Text)
	assert.Equal(t, "team-lead", msgs[0].From)
}

func TestNewMailboxFromConfig(t *testing.T) {
	backend := newInMemoryBackend()
	conf := &Config{Backend: backend, BaseDir: "/data"}
	conf.ensureInit()

	ctx := context.Background()
	teamName := "test-team"

	_, err := conf.CreateTeam(ctx, teamName, "desc", LeaderAgentName, "general-purpose")
	assert.NoError(t, err)

	mb := newMailboxFromConfig(conf, teamName, "worker-1")

	assert.NotNil(t, mb)
	assert.Equal(t, backend, mb.conf.Backend)
	assert.Equal(t, "/data", mb.conf.BaseDir)
	assert.Equal(t, teamName, mb.conf.TeamName)
	assert.Equal(t, "worker-1", mb.conf.OwnerName)
	assert.Equal(t, defaultPollInterval, mb.conf.PollInterval)
	assert.NotNil(t, mb.inboxLocks)
	assert.Same(t, conf.state.locks, mb.inboxLocks)
	assert.NotNil(t, mb.listMembers)

	names, err := mb.listMembers(ctx)
	assert.NoError(t, err)
	assert.Contains(t, names, LeaderAgentName)
}

func TestBroadcast_ListMembersError(t *testing.T) {
	backend := newInMemoryBackend()
	expectedErr := errors.New("member list unavailable")
	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "leader",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: newNamedLockManager(),
		listMembers: func(ctx context.Context) ([]string, error) {
			return nil, expectedErr
		},
	}

	err := mb.broadcast(context.Background(), &outboxMessage{
		To:   "*",
		Type: messageTypeBroadcast,
		Text: "hello all",
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "member list unavailable")
}

func TestSendToOne_ConcurrentSendsNoLostMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	members := []string{"leader", "agent1"}

	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	assert.NoError(t, initInboxFile(context.Background(), backend, inboxPath))

	const senderCount = 10
	var wg sync.WaitGroup
	wg.Add(senderCount)

	for i := 0; i < senderCount; i++ {
		go func(idx int) {
			defer wg.Done()
			mb := &mailbox{
				conf: &mailboxConfig{
					Backend:      backend,
					BaseDir:      "/tmp/test",
					TeamName:     "myteam",
					OwnerName:    fmt.Sprintf("sender-%d", idx),
					PollInterval: 10 * time.Millisecond,
				},
				inboxLocks: locks,
				listMembers: func(ctx context.Context) ([]string, error) {
					return members, nil
				},
			}
			err := mb.sendToOne(context.Background(), "agent1", &outboxMessage{
				To:      "agent1",
				Type:    messageTypeDM,
				Text:    fmt.Sprintf("msg from sender-%d", idx),
				Summary: "concurrent test",
			})
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	reader := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "agent1",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return members, nil
		},
	}

	msgs, err := reader.readInbox(context.Background(), "agent1")
	assert.NoError(t, err)
	assert.Len(t, msgs, senderCount)

	senders := make(map[string]bool)
	for _, msg := range msgs {
		senders[msg.From] = true
		assert.Equal(t, "agent1", msg.To)
		assert.Equal(t, "concurrent test", msg.Summary)
		assert.False(t, msg.Read)
	}
	for i := 0; i < senderCount; i++ {
		assert.True(t, senders[fmt.Sprintf("sender-%d", i)])
	}
}

func TestReadInbox_InvalidJSON(t *testing.T) {
	backend := newInMemoryBackend()
	inboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "agent1.json")
	backend.mu.Lock()
	backend.files[inboxPath] = `not valid json`
	backend.mu.Unlock()

	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)

	_, err := mb.readInbox(context.Background(), "agent1")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal inbox")
}

func TestWriteInbox_BackendWriteError(t *testing.T) {
	eb := newErrBackend(errors.New("write failed"))
	mb := newTestMailbox(eb, "/tmp/test", "myteam", "agent1", nil)

	err := mb.writeInbox(context.Background(), "agent1", []InboxMessage{
		{From: "leader", Text: "hello", Timestamp: "t1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "write inbox")
}

func TestInitInboxFile_ExistsError(t *testing.T) {
	eb := newErrBackend(errors.New("exists check failed"))
	err := initInboxFile(context.Background(), eb, "/tmp/test/inbox.json")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "check inbox exists")
}

func TestMarkRead_ReadInboxError(t *testing.T) {
	eb := newErrBackend(errors.New("backend error"))
	mb := newTestMailbox(eb, "/tmp/test", "myteam", "agent1", nil)

	err := mb.MarkRead(context.Background(), []InboxMessage{
		{From: "leader", Text: "msg1", Timestamp: "t1"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read inbox")
}

func TestReadUnread_ReadInboxError(t *testing.T) {
	eb := newErrBackend(errors.New("backend error"))
	mb := newTestMailbox(eb, "/tmp/test", "myteam", "agent1", nil)

	_, err := mb.ReadUnread(context.Background())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "read inbox")
}

func TestWaitForMessages_ReadUnreadSucceedsFirstCall(t *testing.T) {
	backend := newInMemoryBackend()
	mb := newTestMailbox(backend, "/tmp/test", "myteam", "agent1", nil)
	ctx := context.Background()

	msgs := []InboxMessage{
		{From: "leader", Text: "urgent", Timestamp: "t1", Read: false},
	}
	assert.NoError(t, mb.writeInbox(ctx, "agent1", msgs))

	result, err := mb.WaitForMessages(ctx)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "urgent", result[0].Text)
}
