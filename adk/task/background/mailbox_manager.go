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

package background

import (
	"context"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/adk/task"
)

// The methods in this file are runtime-facing convenience facades over the
// configured LifecycleStore. They do not keep mailbox state in Manager; the
// store remains authoritative for every read, fence, and transition.

// RegisterMailbox creates or replays one foreground communication endpoint.
func (m *Manager) RegisterMailbox(
	ctx context.Context,
	req *task.RegisterMailboxRequest,
) (*task.RegisterMailboxResult, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.Register(ctx, req)
}

// GetMailbox returns one mailbox snapshot.
func (m *Manager) GetMailbox(
	ctx context.Context,
	taskID string,
) (*task.Mailbox, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.GetMailbox(ctx, taskID)
}

// GetActiveMailboxBySession resolves the current nonterminal task in a child session.
func (m *Manager) GetActiveMailboxBySession(
	ctx context.Context,
	childSessionID string,
) (*task.Mailbox, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.GetActiveMailboxBySession(ctx, childSessionID)
}

// SendInput durably sends input to either owner mode. A process-local per-task
// gate linearizes persistence with attempt registration and final waiting-input
// commits.
func (m *Manager) SendInput(
	ctx context.Context,
	req *task.SendInputRequest,
) (*task.SendInputResult, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if req == nil || req.TaskID == "" {
		return m.tasks.SendInput(ctx, req)
	}
	gate, err := m.acquireTaskGate(ctx, req.TaskID)
	if err != nil {
		return nil, err
	}
	defer m.releaseTaskGate(req.TaskID, gate)
	return m.tasks.SendInput(ctx, req)
}

// ListInputs reads one mailbox page.
func (m *Manager) ListInputs(
	ctx context.Context,
	req *task.ListInputsRequest,
) (*task.ListInputsResult, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.ListInputs(ctx, req)
}

// WaitInputs waits for a mailbox sequence change.
func (m *Manager) WaitInputs(
	ctx context.Context,
	req *task.WaitInputsRequest,
) (*task.ListInputsResult, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.WaitInputs(ctx, req)
}

// AdvanceInputCursor advances one mailbox cursor under owner fencing.
func (m *Manager) AdvanceInputCursor(
	ctx context.Context,
	req *task.AdvanceCursorRequest,
) error {
	if m == nil || m.tasks == nil {
		return task.ErrMailboxStoreRequired
	}
	return m.tasks.AdvanceCursor(ctx, req)
}

// SealMailbox seals an idle foreground communication endpoint.
func (m *Manager) SealMailbox(
	ctx context.Context,
	req *task.SealMailboxRequest,
) (*task.Mailbox, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.SealIfIdle(ctx, req)
}

// AbandonMailbox closes a failed or canceled foreground communication endpoint.
func (m *Manager) AbandonMailbox(
	ctx context.Context,
	req *task.AbandonMailboxRequest,
) (*task.Mailbox, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.Abandon(ctx, req)
}

// ListChildren returns direct logical child mailboxes.
func (m *Manager) ListChildren(
	ctx context.Context,
	req *task.ListChildrenRequest,
) (*task.ListChildrenResult, error) {
	if m == nil || m.tasks == nil {
		return nil, task.ErrMailboxStoreRequired
	}
	return m.tasks.ListChildren(ctx, req)
}

// AdoptForeground atomically creates background ownership for an existing
// foreground mailbox.
func (m *Manager) AdoptForeground(
	ctx context.Context,
	req *AdoptForegroundRequest,
) (*TaskSnapshot, error) {
	if m == nil || m.tasks == nil {
		return nil, errors.New("task/background: foreground adoption store is required")
	}
	if req == nil || req.Spec.ID == "" {
		return nil, errors.New("task/background: foreground adoption request is required")
	}
	executor, ok := m.executors.resolve(req.Spec.ExecutorKey)
	if !ok {
		return nil, fmt.Errorf(
			"task/background: executor %q is unavailable",
			req.Spec.ExecutorKey,
		)
	}
	if err := validateSpec(req.Spec); err != nil {
		return nil, err
	}
	if err := executor.ValidateSpec(cloneSpec(req.Spec)); err != nil {
		return nil, fmt.Errorf("task/background: validate spec: %w", err)
	}
	contextSnapshot, _, err := m.captureContextSnapshot(ctx)
	if err != nil {
		return nil, err
	}
	return m.tasks.AdoptForeground(
		ctx,
		&AdoptForegroundStoreRequest{
			AdoptForegroundRequest: *req,
			LeaseExpiryPolicy:      executor.LeaseExpiryPolicy(),
			ContextSnapshot:        contextSnapshot,
		},
	)
}
