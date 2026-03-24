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
	"log"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/bytedance/sonic"
)

// mailboxConfig is the configuration for FileMailbox.
type mailboxConfig struct {
	// Backend is the storage backend for reading and writing mailbox files.
	Backend Backend
	// BaseDir is the root directory where mailbox files are stored.
	BaseDir string
	// TeamName is the name of the team this mailbox belongs to.
	TeamName string
	// OwnerName is the name of the agent that owns this mailbox.
	OwnerName string
	// PollInterval is the fallback polling interval, default 500ms.
	PollInterval time.Duration

	// MaxInboxMessages limits messages per inbox. Default is 1000. Set <= 0 to disable trimming.
	MaxInboxMessages int

	// InboxLocks is the shared lock manager for per-inbox file locking.
	InboxLocks *inboxLockManager

	// TaskLock coordinates task read/write with plantask in team mode.
	TaskLock *sync.Mutex
}

// mailbox implements Mailbox using the filesystem backend.
// Each agent's inbox is a single JSON array file: inboxes/{agentName}.json
// Messages are marked as read by setting the "read" field to true.
type mailbox struct {
	conf        *mailboxConfig
	inboxLocks  *inboxLockManager
	configStore *teamConfigStore
}

type inboxMessageKey struct {
	from    string
	to      string
	text    string
	summary string
	ts      string
}

// newFileMailbox creates a new FileMailbox.
func newFileMailbox(conf *mailboxConfig) *mailbox {
	if conf.PollInterval == 0 {
		conf.PollInterval = 500 * time.Millisecond
	}
	if conf.MaxInboxMessages == 0 {
		conf.MaxInboxMessages = 1000
	}

	locks := conf.InboxLocks
	if locks == nil {
		locks = newInboxLockManager()
	}

	return &mailbox{
		conf:        conf,
		inboxLocks:  locks,
		configStore: newTeamConfigStore(conf.Backend, conf.BaseDir, locks.ForInbox("__config__"), conf.TaskLock),
	}
}

func newMailboxFromConfig(conf *Config, teamName, ownerName string) *mailbox {
	return newFileMailbox(&mailboxConfig{
		Backend:          conf.Backend,
		BaseDir:          conf.BaseDir,
		TeamName:         teamName,
		OwnerName:        ownerName,
		InboxLocks:       conf.InboxLocks,
		TaskLock:         conf.TaskLock,
		PollInterval:     conf.MailboxPollInterval,
		MaxInboxMessages: conf.MailboxMaxMessages,
	})
}

func initInboxFile(ctx context.Context, backend Backend, inboxPath string) error {
	return backend.Write(ctx, &WriteRequest{
		FilePath: inboxPath,
		Content:  "[]",
	})
}

// Close is a no-op, reserved for future cleanup.
func (m *mailbox) Close() {}

// inboxFilePath returns the path to an agent's inbox file.
// Path: {baseDir}/teams/{teamName}/inboxes/{agentName}.json
func (m *mailbox) inboxFilePath(agentName string) string {
	return filepath.Join(inboxDirPath(m.conf.BaseDir, m.conf.TeamName), agentName+".json")
}

// readInbox reads all messages from the given agent's inbox file.
// Returns nil slice if the file doesn't exist or is empty.
// NOTE: caller must hold m.lock when atomicity with writeInbox is required.
func (m *mailbox) readInbox(ctx context.Context, agentName string) ([]InboxMessage, error) {
	inboxPath := m.inboxFilePath(agentName)

	exists, err := m.conf.Backend.Exists(ctx, inboxPath)
	if err != nil {
		return nil, fmt.Errorf("check inbox exists: %w", err)
	}
	if !exists {
		return nil, nil
	}

	content, err := m.conf.Backend.Read(ctx, &ReadRequest{FilePath: inboxPath})
	if err != nil {
		return nil, fmt.Errorf("read inbox file: %w", err)
	}
	if content == "" {
		return nil, nil
	}

	var msgs []InboxMessage
	if err := sonic.UnmarshalString(content, &msgs); err != nil {
		return nil, fmt.Errorf("unmarshal inbox: %w", err)
	}
	return msgs, nil
}

// writeInbox writes the messages to the given agent's inbox file.
// NOTE: caller must hold m.lock when atomicity with readInbox is required.
func (m *mailbox) writeInbox(ctx context.Context, agentName string, msgs []InboxMessage) error {
	data, err := sonic.MarshalString(msgs)
	if err != nil {
		return fmt.Errorf("marshal inbox: %w", err)
	}

	inboxPath := m.inboxFilePath(agentName)
	if err := m.conf.Backend.Write(ctx, &WriteRequest{
		FilePath: inboxPath,
		Content:  data,
	}); err != nil {
		return fmt.Errorf("write inbox: %w", err)
	}
	return nil
}

// Send sends a message to the target agent's inbox.
func (m *mailbox) Send(ctx context.Context, msg *outboxMessage) error {
	if msg.To == "*" {
		return m.broadcast(ctx, msg)
	}
	return m.sendToOne(ctx, msg.To, msg)
}

func (m *mailbox) sendToOne(ctx context.Context, to string, msg *outboxMessage) error {
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")

	inboxMsg := InboxMessage{
		ID:        generateMessageID(m.conf.OwnerName, to),
		From:      m.conf.OwnerName,
		To:        to,
		Text:      msg.Text,
		Summary:   msg.Summary,
		Timestamp: now,
		Read:      false,
	}

	// Use per-target lock so all senders writing to the same inbox are serialized.
	lock := m.inboxLocks.ForInbox(to)
	lock.Lock()
	defer lock.Unlock()

	msgs, err := m.readInbox(ctx, to)
	if err != nil {
		return fmt.Errorf("read inbox: %w", err)
	}

	msgs = append(msgs, inboxMsg)
	if limit := m.conf.MaxInboxMessages; limit > 0 && len(msgs) > limit {
		var removed int
		msgs, removed = compactInboxMessages(msgs, limit)
		if len(msgs) > limit {
			log.Printf("team mailbox: inbox for %s exceeds limit=%d with %d unread messages; retaining unread messages", to, limit, len(msgs)-limit)
		} else if removed > 0 {
			log.Printf("team mailbox: compacted inbox for %s by removing %d read messages (limit=%d)", to, removed, limit)
		}
	}

	return m.writeInbox(ctx, to, msgs)
}

func (m *mailbox) broadcast(ctx context.Context, msg *outboxMessage) error {
	store := m.configStore
	config, err := store.ReadConfigWithLock(ctx, m.conf.TeamName)
	if err != nil {
		return fmt.Errorf("read team config for broadcast: %w", err)
	}

	var failures []string
	for _, member := range config.Members {
		if member.Name == m.conf.OwnerName {
			continue
		}
		if err := m.sendToOne(ctx, member.Name, msg); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", member.Name, err))
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("broadcast failures: %s", strings.Join(failures, "; "))
	}
	return nil
}

// ReadUnread returns all unread messages from this agent's inbox file.
// Use write lock to avoid reading partial writes on non-atomic backends.
func (m *mailbox) ReadUnread(ctx context.Context) ([]InboxMessage, error) {
	lock := m.inboxLocks.ForInbox(m.conf.OwnerName)
	lock.Lock()
	defer lock.Unlock()

	all, err := m.readInbox(ctx, m.conf.OwnerName)
	if err != nil {
		return nil, fmt.Errorf("read inbox: %w", err)
	}

	var unread []InboxMessage
	for _, msg := range all {
		if !msg.Read {
			unread = append(unread, msg)
		}
	}
	return unread, nil
}

// MarkRead marks the given messages as read by setting read=true in the inbox file.
// Messages are matched by from+timestamp.
func (m *mailbox) MarkRead(ctx context.Context, msgs []InboxMessage) error {
	if len(msgs) == 0 {
		return nil
	}

	toMarkIDs := make(map[string]bool, len(msgs))
	toMarkFallback := make(map[inboxMessageKey]bool, len(msgs))
	for _, msg := range msgs {
		if msg.ID != "" {
			toMarkIDs[msg.ID] = true
			continue
		}
		toMarkFallback[mailboxMessageKey(msg)] = true
	}

	// Use per-owner lock: MarkRead modifies the owner's own inbox file.
	lock := m.inboxLocks.ForInbox(m.conf.OwnerName)
	lock.Lock()
	defer lock.Unlock()

	all, err := m.readInbox(ctx, m.conf.OwnerName)
	if err != nil {
		return fmt.Errorf("read inbox: %w", err)
	}

	changed := false
	for i := range all {
		if all[i].Read {
			continue
		}
		if all[i].ID != "" {
			if !toMarkIDs[all[i].ID] {
				continue
			}
			all[i].Read = true
			changed = true
			continue
		}
		if toMarkFallback[mailboxMessageKey(all[i])] {
			all[i].Read = true
			changed = true
		}
	}

	if !changed {
		return nil
	}

	if limit := m.conf.MaxInboxMessages; limit > 0 && len(all) > limit {
		var removed int
		all, removed = compactInboxMessages(all, limit)
		if len(all) > limit {
			log.Printf("team mailbox: inbox for %s exceeds limit=%d with %d unread messages; retaining unread messages", m.conf.OwnerName, limit, len(all)-limit)
		} else if removed > 0 {
			log.Printf("team mailbox: compacted inbox for %s by removing %d read messages (limit=%d)", m.conf.OwnerName, removed, limit)
		}
	}

	return m.writeInbox(ctx, m.conf.OwnerName, all)
}

func mailboxMessageKey(msg InboxMessage) inboxMessageKey {
	return inboxMessageKey{
		from:    msg.From,
		to:      msg.To,
		text:    msg.Text,
		summary: msg.Summary,
		ts:      msg.Timestamp,
	}
}

func generateMessageID(from, to string) string {
	return fmt.Sprintf("%d-%s-%s", time.Now().UnixNano(), from, to)
}

// WaitForMessages blocks until new messages arrive or context is cancelled.
func (m *mailbox) WaitForMessages(ctx context.Context) ([]InboxMessage, error) {
	// check existing messages first
	if msgs, err := m.ReadUnread(ctx); err != nil {
		return nil, err
	} else if len(msgs) > 0 {
		return msgs, nil
	}

	// ensure inbox directory exists
	if err := ensureDir(ctx, m.conf.Backend, inboxDirPath(m.conf.BaseDir, m.conf.TeamName)); err != nil {
		return nil, err
	}

	interval := m.conf.PollInterval
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			// poll filesystem for new messages
		}

		if msgs, err := m.ReadUnread(ctx); err != nil {
			return nil, err
		} else if len(msgs) > 0 {
			return msgs, nil
		}
	}
}

// compactInboxMessages drops the oldest read messages to keep inbox size bounded.
// Unread messages are never dropped.
func compactInboxMessages(msgs []InboxMessage, max int) ([]InboxMessage, int) {
	if max <= 0 || len(msgs) <= max {
		return msgs, 0
	}

	drop := len(msgs) - max
	if drop <= 0 {
		return msgs, 0
	}

	compacted := make([]InboxMessage, 0, len(msgs)-drop)
	removed := 0
	for _, msg := range msgs {
		if msg.Read && drop > 0 {
			drop--
			removed++
			continue
		}
		compacted = append(compacted, msg)
	}
	return compacted, removed
}

// HasActiveTeammates checks if there are active teammates (excluding team-lead).
func (m *mailbox) HasActiveTeammates(ctx context.Context) (bool, error) {
	return m.configStore.HasActiveTeammates(ctx, m.conf.TeamName)
}
