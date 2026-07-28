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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"

	"github.com/cloudwego/eino/internal/safe"
)

const (
	processLocalExecutorKey = "eino.dev/process-local"
	processLocalSpecVersion = "v1"
)

type processLocalRuntimeKey struct{}

// ReportUpdate appends a namespaced durable update when ctx belongs to a
// process-local Manager execution.
func ReportUpdate(ctx context.Context, kind, payloadType string, payload []byte, encoding string) error {
	runtime, ok := ctx.Value(processLocalRuntimeKey{}).(Runtime)
	if !ok {
		return errors.New("backgroundtask: context is not a process-local execution")
	}
	var value *UpdatePayload
	if payload != nil {
		artifact := inlineArtifact(payload, encoding)
		value = &UpdatePayload{Type: payloadType, Value: artifact}
	}
	return runtime.ReportUpdate(ctx, &ReportUpdateRequest{Kind: UpdateKind(kind), Payload: value})
}

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
func (e *processLocalExecutor) Capabilities() []ExecutorCapability {
	return []ExecutorCapability{{
		ExecutorKey: processLocalExecutorKey,
		SpecVersion: processLocalSpecVersion,
	}}
}

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
	if spec.ExecutorKey != processLocalExecutorKey || spec.SpecVersion != processLocalSpecVersion ||
		spec.PayloadEncoding != "text/plain" || len(spec.Payload) == 0 {
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

func (e *processLocalExecutor) ValidateResume(context.Context, *ValidateResumeRequest) (*ValidateResumeResult, error) {
	return nil, errors.New("backgroundtask: process-local tasks cannot resume")
}

func (e *processLocalExecutor) Execute(ctx context.Context, req ExecutionRequest, runtime Runtime) Outcome {
	entry, err := e.resolve(req.Task)
	if err != nil {
		return Outcome{Kind: OutcomeFailed, Err: err}
	}
	entry.markStarted()
	select {
	case <-entry.proceed:
	case <-ctx.Done():
		return Outcome{Kind: OutcomeFailed, Err: ctx.Err()}
	}
	workCtx, cancel := context.WithCancel(context.WithValue(ctx, processLocalRuntimeKey{}, runtime))
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
			workCtx, TaskInfo{ID: req.Task.ID, Backgrounded: entry.backgrounded},
		)
	}()

	select {
	case result := <-resultCh:
		if result.err != nil {
			return Outcome{Kind: OutcomeFailed, Err: result.err}
		}
		value := inlineArtifact([]byte(result.value), "text/plain")
		return Outcome{
			Kind: OutcomeCompleted, Result: &ResultRef{Format: req.Task.Result.ResultFormat, Value: value},
		}
	case control := <-runtime.Controls():
		cancel()
		<-resultCh
		if control.Kind == ControlStop {
			return Outcome{Kind: OutcomeCanceled, TerminalReason: canceledError}
		}
		return Outcome{
			Kind: OutcomeFailed, TerminalReason: "process_local_task_cannot_be_recovered_after_drain",
		}
	}
}

func inlineArtifact(payload []byte, encoding string) ArtifactValue {
	sum := sha256.Sum256(payload)
	return ArtifactValue{
		Payload: cloneBytes(payload), Encoding: encoding,
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(payload)),
	}
}

func processLocalSpec(id string, input *RunInput) Spec {
	return Spec{
		ID: id, ExecutorKey: processLocalExecutorKey, SpecVersion: processLocalSpecVersion,
		Payload: []byte(id), PayloadEncoding: "text/plain",
		Type: input.Type, Description: input.Description, ToolUseID: input.ToolUseID,
		Recovery: RecoveryPolicy{
			OnLeaseExpired: RecoveryFail, OnMissingCheckpoint: RecoveryFail, MaxAttempts: 1,
		},
		Result: ResultPolicy{ResultFormat: "text/plain"},
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
