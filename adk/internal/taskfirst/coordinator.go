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

// Package taskfirst coordinates Manager-owned execution with foreground observation.
package taskfirst

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/internal/startwindow"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/foreground"
)

type Policy struct {
	TimeoutMs                 int
	ShouldAutoBackground      foreground.ShouldAutoBackground
	ShouldCancelOnCallerAbort foreground.ShouldCancelOnCallerAbort
}

type StartRequest struct {
	Spec               background.Spec
	InitialCheckpoint  []byte
	ExplicitBackground bool
	WaitForStart       time.Duration
}

type Outcome struct {
	Task         *background.TaskSnapshot
	Backgrounded bool
}

// Execution is driven either by one Await call or by one streaming observer
// using Terminal, Timeout, and one resolution method. The modes must not be
// mixed and an Execution must not have multiple concurrent observers.
type Execution struct {
	manager   *background.Manager
	initial   *background.TaskSnapshot
	policy    Policy
	candidate foreground.CandidateInfo

	timeout <-chan time.Time
	timer   *time.Timer

	boundary     chan struct{}
	boundaryOnce sync.Once
	boundaryMu   sync.Mutex
	boundaryTask *background.TaskSnapshot
	boundaryErr  error
	stopWatch    context.CancelFunc

	detached     chan struct{}
	detachOnce   sync.Once
	resolveOnce  sync.Once
	resolveDone  chan struct{}
	resolveValue *Outcome
	resolveErr   error
}

type projectionContextKey struct{}

type detachedContext struct {
	execution context.Context
	values    context.Context
}

func (c detachedContext) Deadline() (time.Time, bool) { return c.execution.Deadline() }
func (c detachedContext) Done() <-chan struct{}       { return c.execution.Done() }
func (c detachedContext) Err() error                  { return c.execution.Err() }
func (c detachedContext) Value(key any) any {
	if value := c.execution.Value(key); value != nil {
		return value
	}
	return c.values.Value(key)
}

// Start persists and starts one Manager-owned task before returning.
func Start(
	ctx context.Context,
	manager *background.Manager,
	policy *Policy,
	req *StartRequest,
) (*Execution, error) {
	if manager == nil || req == nil {
		return nil, errors.New("taskfirst: manager and start request are required")
	}
	effectivePolicy := Policy{}
	if policy != nil {
		effectivePolicy = *policy
	}
	publication := background.PublicationDeferred
	if req.ExplicitBackground {
		publication = background.PublicationOnCreate
	}
	initial, err := manager.Submit(ctx, &background.SubmitRequest{
		Spec: req.Spec, InitialCheckpoint: req.InitialCheckpoint,
		Publication: publication,
	})
	if err != nil && !errors.Is(err, background.ErrTaskCreatedEventUndelivered) {
		return nil, err
	}
	if initial == nil {
		return nil, errors.New("taskfirst: submit returned a nil task")
	}
	return launch(ctx, manager, &effectivePolicy, initial, req)
}

// Observe starts a new attempt for an existing task and observes its foreground boundary.
func Observe(
	ctx context.Context,
	manager *background.Manager,
	policy *Policy,
	taskID string,
) (*Execution, error) {
	if manager == nil || taskID == "" {
		return nil, errors.New("taskfirst: manager and task id are required")
	}
	initial, err := manager.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	effectivePolicy := Policy{}
	if policy != nil {
		effectivePolicy = *policy
	}
	return launch(ctx, manager, &effectivePolicy, initial, &StartRequest{})
}

func launch(
	ctx context.Context,
	manager *background.Manager,
	policy *Policy,
	initial *background.TaskSnapshot,
	req *StartRequest,
) (*Execution, error) {
	startedAt := time.Now()
	execution := &Execution{
		manager: manager, initial: initial, policy: *policy,
		candidate: foreground.CandidateInfo{
			TaskID: initial.Spec.ID, Kind: initial.Spec.Kind,
			Description: initial.Spec.Description, OutputFile: initial.Spec.OutputFile,
			StartedAt: startedAt,
		},
		boundary: make(chan struct{}), detached: make(chan struct{}),
		resolveDone: make(chan struct{}),
	}
	if !req.ExplicitBackground && policy.TimeoutMs > 0 {
		execution.timer = time.NewTimer(
			time.Duration(policy.TimeoutMs) * time.Millisecond,
		)
		execution.timeout = execution.timer.C
	}
	windowCtx, window := startwindow.Open(ctx)
	runCtx := context.WithValue(
		windowCtx,
		projectionContextKey{},
		execution.detached,
	)
	watchCtx, stopWatch := context.WithCancel(runCtx)
	execution.stopWatch = stopWatch
	go execution.watchBoundary(watchCtx)
	go func() {
		defer startwindow.Signal(runCtx)
		executeErr := manager.Execute(runCtx, initial.Spec.ID)
		if executeErr == nil {
			return
		}
		current, getErr := manager.Get(context.Background(), initial.Spec.ID)
		if getErr != nil {
			execution.setBoundary(nil, executeErr)
			return
		}
		if foregroundBoundary(current.Status) {
			execution.setBoundary(current, nil)
			return
		}
		// Another Manager may win the claim after Submit. Its attempt remains
		// authoritative, so keep observing instead of failing the projection.
		if current.Status != background.StatusPending {
			return
		}
		execution.setBoundary(current, executeErr)
	}()
	if req.WaitForStart > 0 {
		_ = window.Wait(ctx, req.WaitForStart)
	}
	return execution, nil
}

func (e *Execution) TaskID() string {
	if e == nil || e.initial == nil {
		return ""
	}
	return e.initial.Spec.ID
}

func (e *Execution) Initial() *background.TaskSnapshot {
	if e == nil {
		return nil
	}
	return e.initial
}

func (e *Execution) Await(callerCtx context.Context) (*Outcome, error) {
	if e == nil {
		return nil, errors.New("taskfirst: execution is required")
	}
	if callerCtx == nil {
		callerCtx = context.Background()
	}
	select {
	case <-e.boundary:
		task, err := e.WaitTerminal(context.Background())
		return &Outcome{Task: task}, err
	case <-e.timeout:
		outcome, err := e.ResolveTimeout(callerCtx)
		if err != nil || outcome == nil || outcome.Backgrounded {
			return outcome, err
		}
		return nil, e.ForegroundTimeoutError()
	case <-callerCtx.Done():
		return e.ResolveCallerAbort(callerCtx, callerCtx.Err())
	}
}

func (e *Execution) Terminal() <-chan struct{} {
	if e == nil {
		return nil
	}
	return e.boundary
}

func (e *Execution) Timeout() <-chan time.Time {
	if e == nil {
		return nil
	}
	return e.timeout
}

// ForegroundTimeoutError returns the timeout associated with this execution's
// immutable policy snapshot.
func (e *Execution) ForegroundTimeoutError() error {
	if e == nil {
		return context.DeadlineExceeded
	}
	return &task.ForegroundTimeoutError{
		Timeout: time.Duration(e.policy.TimeoutMs) * time.Millisecond,
		TaskID:  e.TaskID(),
	}
}

func (e *Execution) WaitTerminal(ctx context.Context) (*background.TaskSnapshot, error) {
	if e == nil {
		return nil, errors.New("taskfirst: execution is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case <-e.boundary:
		e.boundaryMu.Lock()
		task, err := e.boundaryTask, e.boundaryErr
		e.boundaryMu.Unlock()
		return task, err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (e *Execution) ResolveTimeout(ctx context.Context) (*Outcome, error) {
	return e.resolve(ctx, nil)
}

func (e *Execution) ResolveCallerAbort(
	ctx context.Context,
	cause error,
) (*Outcome, error) {
	return e.resolve(ctx, cause)
}

func (e *Execution) resolve(ctx context.Context, callerAbort error) (*Outcome, error) {
	if e == nil {
		return nil, errors.New("taskfirst: execution is required")
	}
	e.resolveOnce.Do(func() {
		defer close(e.resolveDone)
		if ctx == nil {
			ctx = context.Background()
		}
		policyCtx := detachedContext{execution: context.Background(), values: ctx}
		e.candidate.Elapsed = time.Since(e.candidate.StartedAt)
		if callerAbort != nil {
			if e.policy.ShouldCancelOnCallerAbort != nil &&
				e.policy.ShouldCancelOnCallerAbort(
					policyCtx,
					&foreground.CallerAbortInfo{
						Candidate: e.candidate,
						Err:       callerAbort,
					},
				) {
				e.resolveValue, e.resolveErr = e.cancelAndWait(
					policyCtx,
					"caller aborted foreground projection",
				)
				return
			}
			e.resolveValue, e.resolveErr = e.detach(policyCtx)
			if e.resolveErr != nil {
				_, _ = e.cancelAndWait(
					policyCtx,
					"foreground projection publication failed",
				)
			}
			return
		}
		if e.policy.ShouldAutoBackground != nil &&
			e.policy.ShouldAutoBackground(policyCtx, &e.candidate) {
			e.resolveValue, e.resolveErr = e.detach(policyCtx)
			if e.resolveErr != nil {
				_, _ = e.cancelAndWait(
					policyCtx,
					"foreground projection publication failed",
				)
			}
			return
		}
		e.resolveValue, e.resolveErr = e.cancelAndWait(
			policyCtx,
			fmt.Sprintf("timed out after %dms", e.policy.TimeoutMs),
		)
	})
	<-e.resolveDone
	return e.resolveValue, e.resolveErr
}

func (e *Execution) detach(ctx context.Context) (*Outcome, error) {
	published, err := e.manager.Publish(ctx, e.TaskID())
	if errors.Is(err, background.ErrAlreadyTerminal) {
		task, waitErr := e.WaitTerminal(ctx)
		return &Outcome{Task: task}, waitErr
	}
	if err != nil {
		return nil, err
	}
	e.detachOnce.Do(func() { close(e.detached) })
	return &Outcome{Task: published, Backgrounded: true}, nil
}

func (e *Execution) cancelAndWait(
	ctx context.Context,
	reason string,
) (*Outcome, error) {
	_, err := e.manager.RequestCancel(
		ctx,
		e.TaskID(),
		background.WithCancellationReason(reason),
	)
	if err != nil && !errors.Is(err, background.ErrAlreadyTerminal) {
		return nil, err
	}
	task, err := e.WaitTerminal(ctx)
	if err != nil {
		return nil, err
	}
	return &Outcome{Task: task}, nil
}

func (e *Execution) watchBoundary(ctx context.Context) {
	current := e.initial
	for {
		if foregroundBoundary(current.Status) {
			e.setBoundary(current, nil)
			return
		}
		next, err := e.manager.WaitForTaskVersion(
			ctx,
			&background.WaitForTaskVersionRequest{
				TaskID: e.TaskID(), AfterVersion: current.Version,
			},
		)
		if err != nil {
			e.setBoundary(nil, err)
			return
		}
		current = next
	}
}

func (e *Execution) setBoundary(
	task *background.TaskSnapshot,
	err error,
) {
	e.boundaryOnce.Do(func() {
		if e.timer != nil {
			e.timer.Stop()
		}
		e.boundaryMu.Lock()
		e.boundaryTask = task
		e.boundaryErr = err
		e.boundaryMu.Unlock()
		close(e.boundary)
		if e.stopWatch != nil {
			e.stopWatch()
		}
	})
}

func foregroundBoundary(status background.Status) bool {
	return status == background.StatusWaitingInput ||
		status == background.StatusCompleted ||
		status == background.StatusFailed ||
		status == background.StatusCanceled
}

// ProjectionDetached returns a stable signal closed when observation detaches.
func ProjectionDetached(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	detached, _ := ctx.Value(projectionContextKey{}).(<-chan struct{})
	return detached
}
