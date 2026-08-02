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

package foreground

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
)

const DefaultTimeoutMs = 120_000

// Policy configures foreground occupancy independently from task persistence.
type Policy struct {
	TimeoutMs            int
	ShouldAutoBackground func(context.Context, *backgroundtask.Task) bool
}

// Request starts a pending task and coordinates its foreground lifetime.
type Request struct {
	TaskID          string
	RunInBackground bool
	TimeoutMs       *int
	// ProjectionReady delays the foreground timeout until an output projection
	// has been constructed. Streaming adapters use it to exclude stream setup.
	ProjectionReady <-chan struct{}
}

// Run starts a pending task on the current worker. It returns when the task
// reaches a non-running state or detaches into the background.
func Run(
	ctx context.Context,
	manager *backgroundtask.Manager,
	policy Policy,
	request *Request,
) (*backgroundtask.Task, error) {
	if manager == nil || request == nil || request.TaskID == "" {
		return nil, errors.New("foreground: manager, request, and task id are required")
	}
	task, err := manager.Get(ctx, request.TaskID)
	if err != nil {
		return nil, err
	}
	if task.Status != backgroundtask.StatusPending {
		return nil, backgroundtask.ErrIllegalTransition
	}

	executeCtx := detachedCtx{parent: ctx}
	done := make(chan error, 1)
	go func() {
		done <- manager.Execute(executeCtx, request.TaskID)
	}()

	started, err := waitStarted(ctx, manager, task, done)
	if err != nil {
		return nil, err
	}
	if started.Status != backgroundtask.StatusRunning {
		return started, nil
	}
	if request.RunInBackground {
		if err = manager.MarkBackgrounded(ctx, request.TaskID); err != nil {
			return nil, err
		}
		return started, nil
	}
	return waitForeground(ctx, manager, policy, request, done)
}

func waitStarted(
	ctx context.Context,
	manager *backgroundtask.Manager,
	task *backgroundtask.Task,
	done <-chan error,
) (*backgroundtask.Task, error) {
	waitCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	wait := make(chan struct {
		task *backgroundtask.Task
		err  error
	}, 1)
	go func() {
		next, err := manager.WaitUpdate(waitCtx, &backgroundtask.WaitUpdateRequest{
			TaskID: task.Spec.ID, AfterVersion: task.Version,
		})
		wait <- struct {
			task *backgroundtask.Task
			err  error
		}{task: next, err: err}
	}()
	select {
	case result := <-wait:
		return result.task, result.err
	case executeErr := <-done:
		if executeErr != nil {
			return nil, executeErr
		}
		return manager.Get(ctx, task.Spec.ID)
	case <-ctx.Done():
		_, _ = manager.RequestCancel(context.Background(), task.Spec.ID)
		return nil, ctx.Err()
	}
}

func waitForeground(
	ctx context.Context,
	manager *backgroundtask.Manager,
	policy Policy,
	request *Request,
	done <-chan error,
) (*backgroundtask.Task, error) {
	if request.ProjectionReady != nil {
		select {
		case <-request.ProjectionReady:
		case executeErr := <-done:
			if executeErr != nil {
				return nil, executeErr
			}
			return manager.Get(context.Background(), request.TaskID)
		case <-ctx.Done():
			_, _ = manager.RequestCancel(context.Background(), request.TaskID)
			return nil, ctx.Err()
		}
	}
	timeoutMs := policy.TimeoutMs
	if request.TimeoutMs != nil {
		timeoutMs = *request.TimeoutMs
	}
	var timeout <-chan time.Time
	var timer *time.Timer
	if timeoutMs > 0 {
		timer = time.NewTimer(time.Duration(timeoutMs) * time.Millisecond)
		timeout = timer.C
		defer timer.Stop()
	}
	for {
		select {
		case executeErr := <-done:
			if executeErr != nil {
				return nil, executeErr
			}
			return manager.Get(context.Background(), request.TaskID)
		case <-ctx.Done():
			if _, cancelErr := manager.RequestCancel(
				context.Background(), request.TaskID,
			); cancelErr != nil && !errors.Is(cancelErr, backgroundtask.ErrAlreadyTerminal) {
				return nil, cancelErr
			}
			if executeErr := <-done; executeErr != nil {
				return nil, executeErr
			}
			return manager.Get(context.Background(), request.TaskID)
		case <-timeout:
			current, getErr := manager.Get(context.Background(), request.TaskID)
			if getErr != nil {
				return nil, getErr
			}
			switch current.Status {
			case backgroundtask.StatusWaitingInput, backgroundtask.StatusSuspended,
				backgroundtask.StatusCompleted, backgroundtask.StatusFailed,
				backgroundtask.StatusCanceled:
				return current, nil
			case backgroundtask.StatusRunning:
				if current.CancelRequestedAt != nil {
					timeout = nil
					continue
				}
				if policy.ShouldAutoBackground != nil &&
					policy.ShouldAutoBackground(ctx, current) {
					if err := manager.MarkBackgrounded(context.Background(), request.TaskID); err != nil {
						return nil, err
					}
					return current, nil
				}
				reason := fmt.Sprintf("timed out after %dms", timeoutMs)
				if err := manager.RequestControl(context.Background(), request.TaskID,
					backgroundtask.ControlRequest{
						Kind: backgroundtask.ControlTimeout, Reason: reason,
					},
				); err != nil {
					return nil, err
				}
				timeout = nil
			default:
				return nil, fmt.Errorf("foreground: unexpected task status %q", current.Status)
			}
		}
	}
}

type detachedCtx struct {
	parent context.Context
}

func (detachedCtx) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedCtx) Done() <-chan struct{}       { return nil }
func (detachedCtx) Err() error                  { return nil }
func (c detachedCtx) Value(key any) any         { return c.parent.Value(key) }
