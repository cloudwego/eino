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
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcurrentSendToSameInbox fires many goroutines sending to the
// same inbox simultaneously. If per-target locking fails, the read-modify-write
// cycle loses messages. We verify all messages survive.
func TestConcurrentSendToSameInbox(t *testing.T) {
	backend := newInMemoryBackend()
	locks := newNamedLockManager()
	members := []string{"leader", "victim"}

	inboxPath := filepath.Join("/tmp/attack", "teams", "t1", "inboxes", "victim.json")
	require.NoError(t, initInboxFile(context.Background(), backend, inboxPath))

	const goroutines = 50
	var wg sync.WaitGroup
	wg.Add(goroutines)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			mb := &mailbox{
				conf: &mailboxConfig{
					Backend:      backend,
					BaseDir:      "/tmp/attack",
					TeamName:     "t1",
					OwnerName:    fmt.Sprintf("attacker-%d", idx),
					PollInterval: 10 * time.Millisecond,
				},
				inboxLocks: locks,
				listMembers: func(ctx context.Context) ([]string, error) {
					return members, nil
				},
			}
			err := mb.sendToOne(context.Background(), "victim", &outboxMessage{
				To:      "victim",
				Type:    messageTypeDM,
				Text:    fmt.Sprintf("payload-%d", idx),
				Summary: "attack",
			})
			assert.NoError(t, err)
		}(i)
	}
	wg.Wait()

	reader := &mailbox{
		conf: &mailboxConfig{
			Backend:      backend,
			BaseDir:      "/tmp/attack",
			TeamName:     "t1",
			OwnerName:    "victim",
			PollInterval: 10 * time.Millisecond,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			return members, nil
		},
	}
	msgs, err := reader.readInbox(context.Background(), "victim")
	require.NoError(t, err)
	assert.Equal(t, goroutines, len(msgs), "expected all %d messages to survive concurrent writes, got %d", goroutines, len(msgs))

	// Verify all senders are present
	seen := make(map[string]bool)
	for _, msg := range msgs {
		seen[msg.From] = true
	}
	for i := 0; i < goroutines; i++ {
		assert.True(t, seen[fmt.Sprintf("attacker-%d", i)], "missing message from attacker-%d", i)
	}
}

// TestCreateTeamWithEmptyName verifies that creating a team with empty name is rejected.
func TestCreateTeamWithEmptyName(t *testing.T) {
	conf, _ := newTestConfig()
	ctx := context.Background()

	_, err := conf.CreateTeam(ctx, "", "empty name team", "leader", "general")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "team name must not be empty")
}

// TestBroadcastToSelfExclusion verifies that broadcast skips the sender.
// Edge case: if the sender is the ONLY member, broadcast writes to zero inboxes.
func TestBroadcastToSelfExclusion(t *testing.T) {
	backend := newInMemoryBackend()

	// Case 1: sender is the only member
	onlyMember := []string{"lonely"}
	mb := newTestMailbox(backend, "/tmp/attack", "solo-team", "lonely", onlyMember)
	inboxPath := filepath.Join("/tmp/attack", "teams", "solo-team", "inboxes", "lonely.json")
	require.NoError(t, initInboxFile(context.Background(), backend, inboxPath))

	err := mb.Send(context.Background(), &outboxMessage{
		To:   "*",
		Type: messageTypeBroadcast,
		Text: "echo?",
	})
	assert.NoError(t, err, "broadcast with sender as only member should not error")

	// Verify sender's inbox is still empty (no self-send)
	msgs, err := mb.readInbox(context.Background(), "lonely")
	assert.NoError(t, err)
	assert.Len(t, msgs, 0, "broadcast should not deliver to sender")

	// Case 2: sender + one other member
	twoMembers := []string{"sender", "receiver"}
	mb2 := newTestMailbox(backend, "/tmp/attack", "duo-team", "sender", twoMembers)
	senderInbox := filepath.Join("/tmp/attack", "teams", "duo-team", "inboxes", "sender.json")
	receiverInbox := filepath.Join("/tmp/attack", "teams", "duo-team", "inboxes", "receiver.json")
	require.NoError(t, initInboxFile(context.Background(), backend, senderInbox))
	require.NoError(t, initInboxFile(context.Background(), backend, receiverInbox))

	err = mb2.Send(context.Background(), &outboxMessage{
		To:   "*",
		Type: messageTypeBroadcast,
		Text: "hello team",
	})
	assert.NoError(t, err)

	// Sender should NOT receive their own broadcast
	senderMsgs, err := mb2.readInbox(context.Background(), "sender")
	assert.NoError(t, err)
	assert.Len(t, senderMsgs, 0, "sender must not receive own broadcast")

	// Receiver should get the message
	receiverMsgs, err := mb2.readInbox(context.Background(), "receiver")
	assert.NoError(t, err)
	assert.Len(t, receiverMsgs, 1)
	assert.Equal(t, "hello team", receiverMsgs[0].Text)
	assert.Equal(t, "sender", receiverMsgs[0].From)
}

// TestInboxCorruptedJSON writes garbage bytes to an inbox file, then
// attempts to read it. Verifies the error is clean (no panic, wrapped properly).
func TestInboxCorruptedJSON(t *testing.T) {
	backend := newInMemoryBackend()
	inboxPath := filepath.Join("/tmp/attack", "teams", "corrupt", "inboxes", "victim.json")

	testCases := []struct {
		name    string
		content string
	}{
		{"random_bytes", "\x00\x01\x02\xff\xfe garbage"},
		{"truncated_json", `[{"from":"a","text":"hello`},
		{"wrong_type", `"this is a string not an array"`},
		{"nested_garbage", `[{"from": null, "text": [1,2,3]}]`},
		{"empty_object", `{}`},
		{"html_injection", `<script>alert('xss')</script>`},
		{"huge_number", `[{"from":"a","text":"b","timestamp":"t","read":99999999999999999999999}]`},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			backend.mu.Lock()
			backend.files[inboxPath] = tc.content
			backend.mu.Unlock()

			mb := newTestMailbox(backend, "/tmp/attack", "corrupt", "victim", nil)
			msgs, err := mb.readInbox(context.Background(), "victim")

			// Must not panic. Either returns error or parses (some JSON is
			// technically valid but semantically wrong).
			if err != nil {
				assert.Contains(t, err.Error(), "unmarshal inbox",
					"error should be wrapped with context")
			} else {
				_ = msgs // parsed successfully (semantically valid JSON)
			}
		})
	}
}

// TestResolveTeamNameCollision creates a team with a name that already
// exists and verifies the system appends a timestamp suffix to avoid collision.
func TestResolveTeamNameCollision(t *testing.T) {
	conf, backend := newTestConfig()
	ctx := context.Background()

	// Pre-create a team with the desired name
	_, err := conf.CreateTeam(ctx, "my-team", "original", "lead", "general")
	require.NoError(t, err)

	// Try to create another team with the same name
	cfg, err := conf.CreateTeam(ctx, "my-team", "duplicate", "lead2", "general")
	require.NoError(t, err)
	require.NotNil(t, cfg)

	// Name should have been resolved with a timestamp suffix
	assert.NotEqual(t, "my-team", cfg.Name)
	assert.True(t, strings.HasPrefix(cfg.Name, "my-team-"),
		"resolved name should start with 'my-team-', got %q", cfg.Name)

	// Both configs should exist in the backend
	originalPath := conf.configFilePath("my-team")
	resolvedPath := conf.configFilePath(cfg.Name)
	_, okOrig := backend.files[originalPath]
	_, okResolved := backend.files[resolvedPath]
	assert.True(t, okOrig, "original config should still exist")
	assert.True(t, okResolved, "resolved config should exist")

	// Verify the suffix is a valid Unix nano timestamp
	suffix := strings.TrimPrefix(cfg.Name, "my-team-")
	assert.NotEmpty(t, suffix)
	for _, c := range suffix {
		assert.True(t, c >= '0' && c <= '9', "suffix %q should be numeric, found char %c", suffix, c)
	}
}
