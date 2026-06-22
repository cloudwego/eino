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
	assert.NotNil(t, pm.active)
	assert.Equal(t, 0, len(pm.mailboxes))
	assert.Equal(t, 0, len(pm.pumps))
	assert.Equal(t, 0, len(pm.active))
}

// TestPumpManager_SetActiveIsInProcess verifies busy/idle status is tracked in
// memory (not persisted) and is readable via isActive. The flips must never
// touch a backend or config store, so setActive takes no context and there is no
// configStore wired into the pump manager.
func TestPumpManager_SetActiveIsInProcess(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})
	pm := newPumpManager(router, nopLogger{})

	// Unknown teammate: no status recorded yet.
	active, ok := pm.isActive("worker")
	assert.False(t, ok)
	assert.False(t, active)

	pm.setActive("worker", true)
	active, ok = pm.isActive("worker")
	assert.True(t, ok)
	assert.True(t, active)

	pm.setActive("worker", false)
	active, ok = pm.isActive("worker")
	assert.True(t, ok)
	assert.False(t, active)
}

// TestPumpManager_UnsetMailboxClearsActive ensures the in-process busy/idle entry
// is removed when the teammate's mailbox is detached, so the map cannot grow
// unboundedly as teammates come and go.
func TestPumpManager_UnsetMailboxClearsActive(t *testing.T) {
	pm, cleanup := newPumpTestFixture(t, "worker")
	defer cleanup()

	pm.setActive("worker", true)
	_, ok := pm.isActive("worker")
	assert.True(t, ok)

	pm.UnsetMailbox("worker")

	_, ok = pm.isActive("worker")
	assert.False(t, ok, "active entry should be cleared after UnsetMailbox")
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
	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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
	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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
	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
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

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
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
	msgs := []inboxMessage{{From: "leader", Text: "hello", Timestamp: utcNowMillis()}}
	msgJSON, _ := sonic.MarshalString(msgs)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
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

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox("worker", ms)

	ctx, cancel := context.WithCancel(context.Background())
	pm.StartPump(ctx, "worker")

	time.Sleep(50 * time.Millisecond)
	msgs := []inboxMessage{{From: "leader", Text: "delayed task", Timestamp: utcNowMillis()}}
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

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
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
	msgs := []inboxMessage{{From: "leader", Text: "msg", Timestamp: utcNowMillis()}}
	msgJSON, _ := sonic.MarshalString(msgs)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: leaderInboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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
	// failReadAfterBackend lets the initial tryReceive succeed, then makes the
	// blocking waitForItem poll fail so we exercise the "wait error" log path.
	fb := &failReadAfterBackend{inMemoryBackend: backend, failAfter: 1, failSuffix: "worker.json"}
	locks := newNamedLockManager()
	logged := make(chan string, 10)
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged <- format
	}}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      fb,
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

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
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

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
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

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
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

func TestPumpManager_StartPump_LogsWhenNoMailbox(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})

	var logged string
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged += fmt.Sprintf(format, args...)
	}}
	pm := newPumpManager(router, logger)

	pm.StartPump(context.Background(), "worker")

	assert.Contains(t, logged, "no mailbox registered")
}

func TestPumpManager_StartPump_LogsWhenNoLoop(t *testing.T) {
	router := newSourceRouter(LeaderAgentName, nopLogger{})

	var logged string
	logger := &testLogger{onPrintf: func(format string, args ...any) {
		logged += fmt.Sprintf(format, args...)
	}}
	pm := newPumpManager(router, logger)

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
	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})
	pm.SetMailbox("worker", ms)

	pm.StartPump(context.Background(), "worker")

	assert.Contains(t, logged, "no TurnLoop registered")
}

// newPumpTestFixture builds a pumpManager wired to a registered loop and an
// initialized inbox for a single teammate, ready for StartPump/UnsetMailbox.
func newPumpTestFixture(t *testing.T, agentName string) (*pumpManager, func()) {
	t.Helper()

	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop(agentName, loop)

	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    agentName,
			PollInterval: 5 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", agentName}, nil
		},
	}

	inboxPath := inboxFilePath("/tmp/test", "myteam", agentName)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: "[]"})

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
		OwnerName: agentName,
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)
	pm.SetMailbox(agentName, ms)

	cleanup := func() {
		loop.Stop()
	}
	return pm, cleanup
}

// TestPumpManager_StartUnsetConcurrent stresses the StartPump/UnsetMailbox
// handoff under the race detector. The handoff uses a startingDone handshake plus
// out-of-lock pump draining to guarantee two pumps never read the same inbox
// concurrently; this test races those two operations to catch regressions there.
// Run with `go test -race` to be meaningful.
func TestPumpManager_StartUnsetConcurrent(t *testing.T) {
	for iter := 0; iter < 20; iter++ {
		pm, cleanup := newPumpTestFixture(t, "worker")

		ctx, cancel := context.WithCancel(context.Background())

		var wg sync.WaitGroup
		// Several goroutines repeatedly start the pump while others unset it,
		// exercising the concurrent install/drain handshake.
		const workers = 4
		wg.Add(workers * 2)
		for i := 0; i < workers; i++ {
			go func() {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					pm.StartPump(ctx, "worker")
				}
			}()
			go func() {
				defer wg.Done()
				for j := 0; j < 10; j++ {
					pm.UnsetMailbox("worker")
				}
			}()
		}
		wg.Wait()

		// Final UnsetMailbox must leave no pump running regardless of interleaving.
		pm.UnsetMailbox("worker")
		pm.mu.Lock()
		_, hasPump := pm.pumps["worker"]
		_, hasStarting := pm.startingDone["worker"]
		pm.mu.Unlock()
		assert.False(t, hasPump, "no pump should remain after final UnsetMailbox")
		assert.False(t, hasStarting, "no in-flight start should remain after final UnsetMailbox")

		cancel()
		cleanup()
	}
}

// TestRunPump_RejectedPushKeepsTeammateMessageUnread guards the at-least-once
// fix: when the TurnLoop rejects a push (because it has been stopped), an
// ordinary teammate message must NOT be marked read, so it stays recoverable in
// the inbox instead of being silently dropped.
func TestRunPump_RejectedPushKeepsTeammateMessageUnread(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	logger := nopLogger{}
	router := newSourceRouter(LeaderAgentName, logger)

	loop := adk.NewTurnLoop(adk.TurnLoopConfig[TurnInput, adk.Message]{
		GenInput: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (*adk.GenInputResult[TurnInput, adk.Message], error) {
			return &adk.GenInputResult[TurnInput, adk.Message]{Consumed: items}, nil
		},
		PrepareAgent: func(ctx context.Context, l *adk.TurnLoop[TurnInput, adk.Message], items []TurnInput) (adk.Agent, error) {
			return nil, errors.New("not used")
		},
	})
	router.RegisterLoop("worker", loop)

	// Stop + Run + Wait so the loop commits its stop and closes the buffer; any
	// subsequent Push is then deterministically rejected.
	loop.Stop()
	loop.Run(context.Background())
	loop.Wait()

	mb := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/test",
			TeamName:     "myteam",
			OwnerName:    "worker",
			PollInterval: 5 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return []string{"team-lead", "worker"}, nil
		},
	}

	inboxPath := inboxFilePath("/tmp/test", "myteam", "worker")
	msgs := []inboxMessage{{ID: "m1", From: "team-lead", Text: "do work", Timestamp: utcNowMillis()}}
	msgJSON, _ := sonic.MarshalString(msgs)
	_ = backend.Write(context.Background(), &WriteRequest{FilePath: inboxPath, Content: msgJSON})

	ms := newMailboxMessageSource(mb, &mailboxSourceConfig{
		OwnerName: "worker",
		Role:      teamRoleTeammate,
	})

	pm := newPumpManager(router, logger)

	// runPump returns as soon as the push is rejected by the stopped loop.
	pm.runPump(context.Background(), "worker", ms, loop)

	// The message must still be unread: a rejected push does not ack/MarkRead.
	unread, err := mb.ReadUnread(context.Background())
	assert.NoError(t, err)
	assert.Len(t, unread, 1)
	assert.Equal(t, "m1", unread[0].ID)
}
