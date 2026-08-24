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

// Handle is the owner-neutral facade for one task record.
type Handle struct {
	manager *Manager
	taskID  string
}

// Handle returns a logical task handle backed by this Manager.
func (m *Manager) Handle(taskID string) (*Handle, error) {
	if m == nil || taskID == "" {
		return nil, errorsNewHandleRequired()
	}
	return &Handle{manager: m, taskID: taskID}, nil
}

func errorsNewHandleRequired() error {
	return fmt.Errorf("task/background: manager and task ID are required")
}

// ID implements task.Handle.
func (h *Handle) ID() string { return h.taskID }

// SendInput implements task.Handle.
func (h *Handle) SendInput(ctx context.Context, input *task.Input) error {
	if input == nil {
		return task.ErrInputRequired
	}
	_, err := h.manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: h.taskID, EventID: input.EventID, Kind: input.Kind,
		Data: append([]byte(nil), input.Data...), Delivery: input.Delivery,
	})
	return err
}

// Wait implements task.Handle.
func (h *Handle) Wait(ctx context.Context) (*task.Outcome, error) {
	var version int64 = -1
	for {
		current, err := h.manager.Get(ctx, h.taskID)
		if err != nil {
			return nil, err
		}
		switch current.Status {
		case StatusCompleted:
			return &task.Outcome{
				Status: task.OutcomeCompleted,
				Data:   cloneBytes(current.ResultData),
			}, nil
		case StatusFailed:
			return &task.Outcome{
				Status: task.OutcomeFailed,
				Error:  current.ResultError,
			}, nil
		case StatusWaitingInput:
			return &task.Outcome{Status: task.OutcomeInterrupted}, nil
		case StatusCanceled:
			return &task.Outcome{
				Status: task.OutcomeCanceled,
				Error:  current.ResultError,
			}, nil
		}
		version = current.Version
		if _, err = h.manager.WaitForTaskVersion(ctx, &WaitForTaskVersionRequest{
			TaskID: h.taskID, AfterVersion: version,
		}); err != nil {
			return nil, err
		}
	}
}

// Cancel implements task.Handle.
func (h *Handle) Cancel(ctx context.Context, reason string) error {
	_, err := h.manager.RequestCancel(ctx, h.taskID, WithCancellationReason(reason))
	if errors.Is(err, ErrAlreadyTerminal) {
		return nil
	}
	return err
}

var _ task.Handle = (*Handle)(nil)
