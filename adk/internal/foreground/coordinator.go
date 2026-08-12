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

// Package foreground coordinates caller-visible task occupancy without owning
// durable task lifecycle state.
package foreground

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/taskcontrol"
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
		if task.Attempt > 0 && authoritativeClaimStatus(task.Status) {
			return task, nil
		}
		return nil, backgroundtask.ErrIllegalTransition
	}

	executeCtx, timeoutController := taskcontrol.WithTimeoutController(
		detachedCtx{parent: ctx},
	)
	executeCtx, detachProjection := withProjection(executeCtx)
	defer detachProjection()
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
		if err := markBackgrounded(ctx, manager, started); err != nil {
			return nil, err
		}
		return started, nil
	}
	return waitForeground(ctx, manager, policy, request, timeoutController, done)
}

// markBackgrounded announces the deferred TaskCreated event at the moment a task
// detaches into the background. It is a no-op for tasks that emit the created
// event at creation (Spec.EmitCreatedOnBackground unset) and for tasks without a
// parent session (nothing to announce), keeping the born-background submit path
// and session-less local runs unchanged.
func markBackgrounded(
	ctx context.Context,
	manager *backgroundtask.Manager,
	task *backgroundtask.Task,
) error {
	if task == nil || !task.Spec.EmitCreatedOnBackground || task.Spec.SessionID == "" {
		return nil
	}
	if _, err := manager.MarkBackgrounded(ctx, task.Spec.ID); err != nil {
		return err
	}
	return nil
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
		next, err := manager.WaitForTaskVersion(waitCtx, &backgroundtask.WaitForTaskVersionRequest{
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
			current, getErr := manager.Get(ctx, task.Spec.ID)
			if getErr == nil && authoritativeClaimStatus(current.Status) {
				return current, nil
			}
			return nil, executeErr
		}
		return manager.Get(ctx, task.Spec.ID)
	case <-ctx.Done():
		_, _ = manager.RequestCancel(context.Background(), task.Spec.ID)
		return nil, ctx.Err()
	}
}

func authoritativeClaimStatus(status backgroundtask.Status) bool {
	switch status {
	case backgroundtask.StatusRunning, backgroundtask.StatusCompleted,
		backgroundtask.StatusFailed, backgroundtask.StatusCanceled:
		return true
	default:
		return false
	}
}

func waitForeground(
	ctx context.Context,
	manager *backgroundtask.Manager,
	policy Policy,
	request *Request,
	timeoutController *taskcontrol.TimeoutController,
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
					if err := markBackgrounded(ctx, manager, current); err != nil {
						return nil, err
					}
					return current, nil
				}
				reason := fmt.Sprintf("timed out after %dms", timeoutMs)
				if err := timeoutController.RequestTimeout(
					context.Background(), reason,
				); err != nil && !errors.Is(err, taskcontrol.ErrClosed) {
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

type projectionKey struct{}

type projection struct {
	once sync.Once
	done chan struct{}
}

func withProjection(ctx context.Context) (context.Context, func()) {
	state := &projection{done: make(chan struct{})}
	return context.WithValue(ctx, projectionKey{}, state), func() {
		state.once.Do(func() { close(state.done) })
	}
}

// ProjectionDetached returns a signal closed when foreground coordination has
// stopped projecting the current execution. A nil signal means no coordinator
// is attached.
func ProjectionDetached(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(projectionKey{}).(*projection)
	if state == nil {
		return nil
	}
	return state.done
}
