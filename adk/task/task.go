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

// Package task defines logical task identity, messaging, and ownership-neutral
// handles. Foreground and background owners keep their lifecycle state in
// separate stores.
package task

import "context"

// Mode identifies the current lifecycle owner of a logical task.
type Mode uint8

const (
	// ModeForeground means the parent execution owns the task lifecycle.
	ModeForeground Mode = iota
	// ModeBackground means a background manager owns the task lifecycle.
	ModeBackground
)

// OutcomeStatus identifies one owner-neutral wait result.
type OutcomeStatus uint8

const (
	// OutcomeUnknown is the zero value and is never returned by a valid Handle.
	OutcomeUnknown OutcomeStatus = iota
	// OutcomeCompleted reports successful completion; Data contains the result.
	OutcomeCompleted
	// OutcomeInterrupted reports that execution requires caller input.
	OutcomeInterrupted
	// OutcomeFailed reports terminal failure; Error contains the reason.
	OutcomeFailed
	// OutcomeCanceled reports terminal cancellation; Error may contain the reason.
	OutcomeCanceled
)

// Outcome is an owner-neutral terminal or interruption observation.
type Outcome struct {
	// Status is exactly one terminal or interruption state.
	Status OutcomeStatus
	// Data is set only for OutcomeCompleted.
	Data []byte
	// Error is set only for OutcomeFailed or OutcomeCanceled.
	Error string
}

// Handle controls one logical task without exposing its current owner.
type Handle interface {
	ID() string
	SendInput(context.Context, *Input) error
	Wait(context.Context) (*Outcome, error)
	Cancel(context.Context, string) error
}

// InputClient sends durable input when only a task ID is available.
type InputClient struct {
	Sender InputSender
}

// SendInput durably appends one task input.
func (c *InputClient) SendInput(
	ctx context.Context,
	taskID string,
	input *Input,
) (*SendInputResult, error) {
	if c == nil || c.Sender == nil {
		return nil, ErrMailboxStoreRequired
	}
	if input == nil {
		return nil, ErrInputRequired
	}
	return c.Sender.SendInput(ctx, &SendInputRequest{
		TaskID: taskID, EventID: input.EventID, Kind: input.Kind,
		Data: append([]byte(nil), input.Data...), Delivery: input.Delivery,
	})
}
