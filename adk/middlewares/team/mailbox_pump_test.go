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
	"path/filepath"
	"testing"
	"time"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
)

func TestNewPumpManager(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

	assert.NotNil(t, pm)
	assert.NotNil(t, pm.mailboxes)
	assert.NotNil(t, pm.pumps)
	assert.Equal(t, 0, len(pm.mailboxes))
	assert.Equal(t, 0, len(pm.pumps))
}

func TestPumpManager_SetMailbox(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

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
	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm.SetMailbox("worker", ms)

	pm.mu.Lock()
	registered, ok := pm.mailboxes["worker"]
	pm.mu.Unlock()
	assert.True(t, ok)
	assert.Same(t, ms, registered)
}

func TestPumpManager_UnsetMailbox(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

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
	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm.SetMailbox("worker", ms)
	pm.UnsetMailbox("worker")

	pm.mu.Lock()
	_, hasMailbox := pm.mailboxes["worker"]
	_, hasPump := pm.pumps["worker"]
	pm.mu.Unlock()
	assert.False(t, hasMailbox)
	assert.False(t, hasPump)
}

func TestPumpManager_UnsetMailbox_NonExistent(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

	assert.NotPanics(t, func() {
		pm.UnsetMailbox("does-not-exist")
	})
}

func TestPumpManager_StartPump_NoMailbox(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

	ctx := context.Background()
	pm.StartPump(ctx, "worker")

	pm.mu.Lock()
	_, hasPump := pm.pumps["worker"]
	pm.mu.Unlock()
	assert.False(t, hasPump)
}

func TestPumpManager_StartPump_NoLoop(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

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
	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})
	pm.SetMailbox("worker", ms)

	ctx := context.Background()
	pm.StartPump(ctx, "worker")

	pm.mu.Lock()
	_, hasPump := pm.pumps["worker"]
	pm.mu.Unlock()
	assert.False(t, hasPump)
}

func TestPumpManager_StartPump_StartsAndUnsetStops(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm.StartPump(ctx, "worker")

	pm.mu.Lock()
	_, hasPump := pm.pumps["worker"]
	pm.mu.Unlock()
	assert.True(t, hasPump)

	pm.UnsetMailbox("worker")

	pm.mu.Lock()
	_, hasPump = pm.pumps["worker"]
	pm.mu.Unlock()
	assert.False(t, hasPump)
}

func TestRunPump_TryReceiveProcessesPreExistingMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	leaderInboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	msgs := []InboxMessage{{From: "leader", Text: "hello", Timestamp: utcNowMillis()}}
	msgJSON, _ := sonic.MarshalString(msgs)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	pm.StartPump(ctx, "worker")

	assert.Eventually(t, func() bool {
		remaining, err := mb.readInbox(context.Background(), "worker")
		return err == nil && len(remaining) == 0
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestRunPump_WaitForItemProcessesDelayedMessages(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	leaderInboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	pm.StartPump(ctx, "worker")

	time.Sleep(50 * time.Millisecond)
	msgs := []InboxMessage{{From: "leader", Text: "delayed task", Timestamp: utcNowMillis()}}
	msgJSON, _ := sonic.MarshalString(msgs)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})

	assert.Eventually(t, func() bool {
		remaining, err := mb.readInbox(context.Background(), "worker")
		return err == nil && len(remaining) == 0
	}, 2*time.Second, 20*time.Millisecond)

	cancel()
	time.Sleep(50 * time.Millisecond)
}

func TestRunPump_ExitsWhenLoopStopped(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	leaderInboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	msgs := []InboxMessage{{From: "leader", Text: "msg", Timestamp: utcNowMillis()}}
	msgJSON, _ := sonic.MarshalString(msgs)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	loop.Stop()

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartPump(ctx, "worker")

	assert.Eventually(t, func() bool {
		pm.mu.Lock()
		h := pm.pumps["worker"]
		pm.mu.Unlock()
		if h == nil {
			return false
		}
		select {
		case <-h.done:
			return true
		default:
			return false
		}
	}, 2*time.Second, 20*time.Millisecond)
}

func TestRunPump_WaitForItemErrorLogsAndExits(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logged := make(chan string, 10)
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged <- format
	}}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	leaderInboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName:           "worker",
		Role:                teamRoleLeader,
		ExitWhenNoTeammates: true,
		HasActiveTeammates: func(ctx context.Context) (bool, error) {
			return false, nil
		},
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartPump(ctx, "worker")

	select {
	case msg := <-logged:
		assert.Contains(t, msg, "wait error")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for pump to log wait error")
	}
}

func TestRunPump_TryReceiveErrorLogsAndExits(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logged := make(chan string, 10)
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged <- format
	}}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "INVALID_JSON"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pm.StartPump(ctx, "worker")

	select {
	case msg := <-logged:
		assert.Contains(t, msg, "error")
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for pump to log tryReceive error")
	}
}

func TestRunPump_ReplacesOldPump(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (*adk.GenInputResult[TurnInput], error) {
			return &adk.GenInputResult[TurnInput]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

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

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	leaderInboxPath := filepath.Join("/tmp/test", "teams", "myteam", "inboxes", "team-lead.json")
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &MailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pm.StartPump(ctx, "worker")
	pm.mu.Lock()
	firstHandle := pm.pumps["worker"]
	pm.mu.Unlock()
	assert.NotNil(t, firstHandle)

	pm.StartPump(ctx, "worker")

	select {
	case <-firstHandle.done:
	case <-time.After(2 * time.Second):
		t.Fatal("old pump did not exit")
	}

	pm.mu.Lock()
	secondHandle := pm.pumps["worker"]
	pm.mu.Unlock()
	assert.NotNil(t, secondHandle)
	assert.NotSame(t, firstHandle, secondHandle)
}
