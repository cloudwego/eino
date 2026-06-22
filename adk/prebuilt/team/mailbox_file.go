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

// mailbox_file.go implements the file-system-backed mailbox: per-agent inbox
// files stored as JSON arrays. Provides read, write, send, broadcast, and
// polling operations with per-target locking to prevent lost updates.
// Message types (outboxMessage, InboxMessage) are defined in protocol.go and types.go.

package team

import (
	"context"
	"fmt"
	"time"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
)

// mailboxConfig is the configuration for mailbox.
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
}

// memberLister returns the list of team member names for broadcast.
type memberLister func(ctx context.Context) ([]string, error)

// mailbox implements file-system-backed per-agent inbox operations.
// Each agent's inbox is a single JSON array file: inboxes/{agentName}.json
// Messages are marked as read by setting the "read" field to true.
type mailbox struct {
	conf        *mailboxConfig
	inboxLocks  *namedLockManager
	listMembers memberLister // for broadcast: returns all member names
}

// newMailboxFromConfig creates a mailbox using the shared resources from Config.state.
// This is the primary constructor used in team mode.
func newMailboxFromConfig(conf *Config, teamName, ownerName string) *mailbox {
	pollInterval := defaultPollInterval

	locks := conf.state.locks

	return &mailbox{
		conf: &mailboxConfig{
			Backend:      conf.Backend,
			BaseDir:      conf.BaseDir,
			TeamName:     teamName,
			OwnerName:    ownerName,
			PollInterval: pollInterval,
		},
		inboxLocks: locks,
		listMembers: func(ctx context.Context) ([]string, error) {
			var names []string
			err := conf.readConfigWithReadLock(ctx, teamName, func(cfg *teamConfig) error {
				for _, m := range cfg.Members {
					names = append(names, m.Name)
				}
				return nil
			})
			return names, err
		},
	}
}

func initInboxFile(ctx context.Context, backend Backend, inboxPath string) error {
	exists, err := backend.Exists(ctx, inboxPath)
	if err != nil {
		return fmt.Errorf("check inbox exists: %w", err)
	}
	if exists {
		return nil
	}
	return backend.Write(ctx, &WriteRequest{
		FilePath: inboxPath,
		Content:  "[]",
	})
}

// inboxFilePathForOwner returns the path to an agent's inbox file.
func (m *mailbox) inboxFilePathForOwner(agentName string) string {
	return inboxFilePath(m.conf.BaseDir, m.conf.TeamName, agentName)
}

// readInbox reads all messages from the given agent's inbox file.
// Returns nil slice if the file doesn't exist or is empty.
// NOTE: caller must hold the per-inbox lock when atomicity with writeInbox is required.
func (m *mailbox) readInbox(ctx context.Context, agentName string) ([]InboxMessage, error) {
	inboxPath := m.inboxFilePathForOwner(agentName)

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
	if content == nil || content.Content == "" {
		return nil, nil
	}

	var msgs []InboxMessage
	if err := sonic.UnmarshalString(content.Content, &msgs); err != nil {
		return nil, fmt.Errorf("unmarshal inbox: %w", err)
	}
	return msgs, nil
}

// writeInbox writes the messages to the given agent's inbox file.
// NOTE: caller must hold the per-inbox lock when atomicity with readInbox is required.
func (m *mailbox) writeInbox(ctx context.Context, agentName string, msgs []InboxMessage) error {
	data, err := sonic.MarshalString(msgs)
	if err != nil {
		return fmt.Errorf("marshal inbox: %w", err)
	}

	inboxPath := m.inboxFilePathForOwner(agentName)
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
	now := utcNowMillis()

	inboxMsg := InboxMessage{
		ID:        uuid.New().String(),
		From:      m.conf.OwnerName,
		To:        to,
		Text:      msg.Text,
		Summary:   msg.Summary,
		Timestamp: now,
		Read:      false,
	}

	// Use per-target lock so all senders writing to the same inbox are serialized.
	lock := m.inboxLocks.ForName(to)
	lock.Lock()
	defer lock.Unlock()

	msgs, err := m.readInbox(ctx, to)
	if err != nil {
		return fmt.Errorf("read inbox: %w", err)
	}

	msgs = append(msgs, inboxMsg)

	return m.writeInbox(ctx, to, msgs)
}

func (m *mailbox) broadcast(ctx context.Context, msg *outboxMessage) error {
	names, err := m.listMembers(ctx)
	if err != nil {
		return fmt.Errorf("list members for broadcast: %w", err)
	}

	var errs []error
	for _, name := range names {
		if name == m.conf.OwnerName {
			continue
		}
		if err := m.sendToOne(ctx, name, msg); err != nil {
			errs = append(errs, fmt.Errorf("broadcast to %s: %w", name, err))
		}
	}
	return joinErrors(errs...)
}

// ReadUnread returns all unread messages from this agent's inbox file.
func (m *mailbox) ReadUnread(ctx context.Context) ([]InboxMessage, error) {
	lock := m.inboxLocks.ForName(m.conf.OwnerName)
	lock.RLock()
	defer lock.RUnlock()

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

// MarkRead removes the given messages from the inbox file, compacting it to
// only retain unread messages. This prevents the inbox file from growing
// unboundedly over time.
// Messages are matched by ID.
func (m *mailbox) MarkRead(ctx context.Context, msgs []InboxMessage) error {
	if len(msgs) == 0 {
		return nil
	}

	toRemove := make(map[string]bool, len(msgs))
	for _, msg := range msgs {
		toRemove[msg.ID] = true
	}

	// Use per-owner lock: MarkRead modifies the owner's own inbox file.
	lock := m.inboxLocks.ForName(m.conf.OwnerName)
	lock.Lock()
	defer lock.Unlock()

	all, err := m.readInbox(ctx, m.conf.OwnerName)
	if err != nil {
		return fmt.Errorf("read inbox: %w", err)
	}

	remaining := make([]InboxMessage, 0, len(all))
	for _, msg := range all {
		if !toRemove[msg.ID] {
			remaining = append(remaining, msg)
		}
	}

	if len(remaining) == len(all) {
		return nil
	}

	return m.writeInbox(ctx, m.conf.OwnerName, remaining)
}

// WaitForMessages blocks until new messages arrive or context is cancelled.
func (m *mailbox) WaitForMessages(ctx context.Context) ([]InboxMessage, error) {
	// check existing messages first
	if msgs, err := m.ReadUnread(ctx); err != nil {
		return nil, err
	} else if len(msgs) > 0 {
		return msgs, nil
	}

	return m.waitForNewMessages(ctx)
}

// waitForNewMessages blocks until new messages arrive, without checking existing
// messages first. Use this when the caller has already verified no unread messages
// exist, to avoid a redundant ReadUnread call.
func (m *mailbox) waitForNewMessages(ctx context.Context) ([]InboxMessage, error) {
	return m.waitForNewMessagesWithCheck(ctx, nil)
}

// waitForNewMessagesWithCheck is like waitForNewMessages but runs an optional
// tickCheck callback on every poll cycle. If tickCheck returns a non-nil error
// the wait is aborted and that error is returned. This allows callers (e.g. the
// leader's ExitWhenNoTeammates logic) to break out of the blocking poll when an
// external condition changes, without waiting for a new inbox message.
func (m *mailbox) waitForNewMessagesWithCheck(ctx context.Context, tickCheck func(ctx context.Context) error) ([]InboxMessage, error) {
	ticker := time.NewTicker(m.conf.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-ticker.C:
			// poll filesystem for new messages
		}

		if tickCheck != nil {
			if err := tickCheck(ctx); err != nil {
				return nil, err
			}
		}

		if msgs, err := m.ReadUnread(ctx); err != nil {
			return nil, err
		} else if len(msgs) > 0 {
			return msgs, nil
		}
	}
}
