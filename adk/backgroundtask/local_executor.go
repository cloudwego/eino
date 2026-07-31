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
	work WorkFunc
}

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
		work: work,
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
	if spec.ExecutorKey != processLocalExecutorKey || spec.ID == "" {
		return nil, errors.New("backgroundtask: invalid process-local task spec")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	work, ok := e.works[spec.ID]
	if !ok {
		return nil, errors.New("backgroundtask: process-local work is unavailable after process loss")
	}
	return work, nil
}

func (e *processLocalExecutor) ValidateSpec(spec Spec) error {
	if spec.ExecutorKey != processLocalExecutorKey {
		return errors.New("backgroundtask: invalid process-local task spec")
	}
	return nil
}

func (e *processLocalExecutor) ValidateExecution(_ context.Context, task *Task) error {
	if task == nil {
		return errors.New("backgroundtask: process-local task is required")
	}
	spec := task.Spec
	_, err := e.resolve(spec)
	return err
}

func (e *processLocalExecutor) ValidateCheckpoint(context.Context, Spec, []byte) error {
	return errors.New("backgroundtask: process-local tasks do not support checkpoints")
}

func (e *processLocalExecutor) ValidateResume(context.Context, Spec, []byte, []byte) ([]byte, error) {
	return nil, errors.New("backgroundtask: process-local tasks cannot resume")
}

func (e *processLocalExecutor) SupportsDrain() bool { return false }

func (e *processLocalExecutor) Execute(ctx context.Context, task *Task, runtime ExecutionRuntime) (*ExecutionResult, error) {
	if task.Attempt > 1 && len(task.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask: process-local task cannot restart without a checkpoint")
	}
	entry, err := e.resolve(task.Spec)
	if err != nil {
		return nil, err
	}
	defer e.remove(task.Spec.ID)
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
		result.value, result.err = entry.work(workCtx, runtime)
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
	case control := <-runtime.Controls():
		cancel()
		if control.Kind == ControlStop {
			return &ExecutionResult{
				Status: StatusCanceled,
				Error:  canceledError,
			}, nil
		}
		if control.Kind == ControlTimeout {
			return &ExecutionResult{Status: StatusFailed, Error: control.Reason}, nil
		}
		return nil, ErrCheckpointUnavailable
	}
}

func processLocalSpec(id string, input *RunInput) Spec {
	return Spec{
		ID: id, ExecutorKey: processLocalExecutorKey, Kind: input.Type,
		Payload: cloneBytes(input.Payload), Description: input.Description,
		OutputFile: input.OutputFile, LeaseExpiryPolicy: LeaseExpiryFail,
	}
}

func (m *Manager) submitProcessLocal(ctx context.Context, input *RunInput, work WorkFunc) (*Task, *localWork, error) {
	if input == nil {
		return nil, nil, errors.New("backgroundtask: RunInput is required")
	}
	id, err := m.AllocateTaskID(ctx, &AllocateTaskIDRequest{Kind: input.Type})
	if err != nil {
		return nil, nil, err
	}
	entry, err := m.local.register(id, work)
	if err != nil {
		return nil, nil, err
	}
	spec := processLocalSpec(id, input)
	if err = m.local.ValidateSpec(spec); err != nil {
		m.local.remove(id)
		return nil, nil, err
	}
	if err = m.local.ValidateExecution(ctx, &Task{Spec: cloneSpec(spec)}); err != nil {
		m.local.remove(id)
		return nil, nil, err
	}
	task, err := m.store.CreateAndStart(ctx, &CreateTaskRequest{Spec: spec})
	if err != nil {
		m.local.remove(id)
		return nil, nil, fmt.Errorf("backgroundtask: create and start process-local task: %w", err)
	}
	m.submittedMu.Lock()
	m.submitted[task.Spec.ID] = struct{}{}
	m.submittedMu.Unlock()
	return task, entry, nil
}
