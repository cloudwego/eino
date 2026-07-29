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

package backgroundtask

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/safe"
)

const (
	processLocalExecutorKey = "eino.dev/process-local"
)

type localWork struct {
	work         WorkFunc
	backgrounded chan struct{}
	started      chan struct{}
	proceed      chan struct{}
	startOnce    sync.Once
	bgOnce       sync.Once
	proceedOnce  sync.Once
}

func (w *localWork) markStarted()      { w.startOnce.Do(func() { close(w.started) }) }
func (w *localWork) markBackgrounded() { w.bgOnce.Do(func() { close(w.backgrounded) }) }
func (w *localWork) allowStart()       { w.proceedOnce.Do(func() { close(w.proceed) }) }

type processLocalExecutor struct {
	mu    sync.Mutex
	works map[string]*localWork
}

func newProcessLocalExecutor() *processLocalExecutor {
	return &processLocalExecutor{works: make(map[string]*localWork)}
}

func (e *processLocalExecutor) Key() string { return processLocalExecutorKey }

func (e *processLocalExecutor) register(token string, work WorkFunc) (*localWork, error) {
	if token == "" || work == nil {
		return nil, errors.New("backgroundtask: process-local token and work are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.works[token]; exists {
		return nil, ErrAlreadyExists
	}
	entry := &localWork{
		work:         work,
		backgrounded: make(chan struct{}), started: make(chan struct{}), proceed: make(chan struct{}),
	}
	e.works[token] = entry
	return entry, nil
}

func (e *processLocalExecutor) remove(token string) {
	e.mu.Lock()
	delete(e.works, token)
	e.mu.Unlock()
}

func (e *processLocalExecutor) resolve(spec Spec) (*localWork, error) {
	if spec.ExecutorKey != processLocalExecutorKey || len(spec.Payload) == 0 {
		return nil, errors.New("backgroundtask: invalid process-local task spec")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	work, ok := e.works[string(spec.Payload)]
	if !ok {
		return nil, errors.New("backgroundtask: process-local work is unavailable after process loss")
	}
	return work, nil
}

func (e *processLocalExecutor) Validate(spec Spec) error {
	_, err := e.resolve(spec)
	return err
}

func (e *processLocalExecutor) ValidateCheckpoint(context.Context, Spec, []byte) error {
	return errors.New("backgroundtask: process-local tasks do not support checkpoints")
}

func (e *processLocalExecutor) ValidateResume(context.Context, Spec, []byte, []byte) ([]byte, error) {
	return nil, errors.New("backgroundtask: process-local tasks cannot resume")
}

func (e *processLocalExecutor) Execute(ctx context.Context, task *Task, controls <-chan ControlRequest) (*ExecutionResult, error) {
	if task.Attempt > 1 && len(task.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask: process-local task cannot restart without a checkpoint")
	}
	entry, err := e.resolve(task.Spec)
	if err != nil {
		return nil, err
	}
	entry.markStarted()
	select {
	case <-entry.proceed:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type workResult struct {
		value string
		err   error
	}
	resultCh := make(chan workResult, 1)
	go func() {
		result := workResult{}
		defer func() {
			if panicValue := recover(); panicValue != nil {
				result.err = safe.NewPanicErr(panicValue, debug.Stack())
			}
			resultCh <- result
		}()
		result.value, result.err = entry.work(
			workCtx, TaskInfo{ID: task.Spec.ID, Backgrounded: entry.backgrounded},
		)
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return nil, result.err
		}
		return &ExecutionResult{
			Status: StatusCompleted,
			Data:   []byte(result.value),
		}, nil
	case control := <-controls:
		cancel()
		<-resultCh
		if control.Kind == ControlStop {
			return &ExecutionResult{
				Status: StatusCanceled,
				Error:  canceledError,
			}, nil
		}
		return nil, ErrCheckpointUnavailable
	}
}

func processLocalSpec(id string, input *RunInput) Spec {
	return Spec{
		ID: id, ExecutorKey: processLocalExecutorKey, Payload: []byte(id),
		Description: input.Description,
	}
}

func (m *Manager) submitProcessLocal(ctx context.Context, input *RunInput, work WorkFunc) (*Task, *localWork, error) {
	if input == nil {
		return nil, nil, errors.New("backgroundtask: RunInput is required")
	}
	id, err := m.AllocateTaskID(ctx)
	if err != nil {
		return nil, nil, err
	}
	entry, err := m.local.register(id, work)
	if err != nil {
		return nil, nil, err
	}
	task, err := m.Submit(ctx, processLocalSpec(id, input))
	if err != nil {
		m.local.remove(id)
		return nil, nil, fmt.Errorf("backgroundtask: submit process-local task: %w", err)
	}
	return task, entry, nil
}
