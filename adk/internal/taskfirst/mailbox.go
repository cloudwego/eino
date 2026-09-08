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

package taskfirst

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
)

const foregroundMailboxFinalizationTimeout = 5 * time.Second

// ForegroundMailboxFinalizer closes one parent-owned foreground mailbox at
// most once using bounded cleanup detached from the caller context.
type ForegroundMailboxFinalizer struct {
	manager    *background.Manager
	taskID     string
	generation int64
	cursor     int64
	once       sync.Once
	err        error
}

// NewForegroundMailboxFinalizer binds finalization to one mailbox generation.
func NewForegroundMailboxFinalizer(
	manager *background.Manager,
	taskID string,
	generation int64,
	cursor int64,
) *ForegroundMailboxFinalizer {
	return &ForegroundMailboxFinalizer{
		manager: manager, taskID: taskID, generation: generation, cursor: cursor,
	}
}

// SealIfIdle synchronously seals a successfully completed foreground mailbox.
// ErrInputsPending is returned without discarding the authoritative input.
func (f *ForegroundMailboxFinalizer) SealIfIdle() error {
	return f.finalize(false)
}

// Abandon closes a failed or canceled foreground mailbox and discards pending
// input.
func (f *ForegroundMailboxFinalizer) Abandon() error {
	return f.finalize(true)
}

func (f *ForegroundMailboxFinalizer) finalize(abandon bool) error {
	if f == nil || f.manager == nil || f.taskID == "" || f.generation <= 0 {
		return nil
	}
	f.once.Do(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			foregroundMailboxFinalizationTimeout,
		)
		defer cancel()
		if abandon {
			_, f.err = f.manager.AbandonMailbox(ctx, &task.AbandonMailboxRequest{
				TaskID: f.taskID, ExpectedGeneration: f.generation,
			})
			return
		}
		_, f.err = f.manager.SealMailbox(ctx, &task.SealMailboxRequest{
			TaskID: f.taskID, ExpectedCursor: f.cursor,
			ExpectedGeneration: f.generation,
		})
	})
	return f.err
}

// CombineForegroundErrors preserves an operation error while also exposing a
// foreground mailbox finalization error through errors.Is. When both are
// present, the operation error is reported first.
func CombineForegroundErrors(operationErr, finalizationErr error) error {
	if finalizationErr == nil {
		return operationErr
	}
	if operationErr == nil {
		return finalizationErr
	}
	return &foregroundCleanupError{
		operationErr:    operationErr,
		finalizationErr: finalizationErr,
	}
}

type foregroundCleanupError struct {
	operationErr    error
	finalizationErr error
}

func (e *foregroundCleanupError) Error() string {
	return fmt.Sprintf(
		"%v (foreground mailbox cleanup failed: %v)",
		e.operationErr,
		e.finalizationErr,
	)
}

func (e *foregroundCleanupError) Unwrap() error {
	return e.operationErr
}

func (e *foregroundCleanupError) Is(target error) bool {
	return errors.Is(e.operationErr, target) ||
		errors.Is(e.finalizationErr, target)
}
