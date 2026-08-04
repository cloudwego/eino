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

package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/schema"
)

const payloadVersion = 1

const (
	maxArgumentsBytes       = 1 << 20
	maxUpdateDataBytes      = 256 << 10
	maxUpdateKindBytes      = 128
	maxUpdateMIMETypeBytes  = 256
	maxUpdateMetadata       = 32
	maxUpdateMetadataBytes  = 1024
	terminalUpdateDrainTime = 5 * time.Second
)

type taskPayload struct {
	Version    int    `json:"version"`
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Arguments  string `json:"arguments"`
}

type executor struct {
	registry    *Registry
	recoverable bool
}

// RegisterExecutors installs the plain and recoverable managed-tool executors.
func RegisterExecutors(manager *backgroundtask.Manager, registry *Registry) error {
	if manager == nil || registry == nil {
		return errors.New("backgroundtask/tool: manager and registry are required")
	}
	for _, candidate := range []*executor{
		{registry: registry},
		{registry: registry, recoverable: true},
	} {
		actual, _, err := manager.LoadOrRegisterExecutor(candidate)
		if err != nil {
			return err
		}
		registered, ok := actual.(*executor)
		if !ok || registered.registry != registry || registered.recoverable != candidate.recoverable {
			return fmt.Errorf("backgroundtask/tool: executor key %q is already registered incompatibly", candidate.Key())
		}
	}
	return nil
}

func (e *executor) Key() string {
	if e.recoverable {
		return RecoverableExecutorKey
	}
	return ExecutorKey
}

func (e *executor) LeaseExpiryPolicy() backgroundtask.LeaseExpiryPolicy {
	if e.recoverable {
		return backgroundtask.LeaseExpiryRetry
	}
	return backgroundtask.LeaseExpiryFail
}

func (e *executor) SupportsDrain() bool { return e.recoverable }

func (e *executor) ValidateSpec(spec backgroundtask.Spec) error {
	payload, err := e.decodePayload(spec)
	if err != nil {
		return err
	}
	registration, ok := e.registry.resolve(payload.ToolName, e.recoverable)
	if !ok {
		return fmt.Errorf("backgroundtask/tool: tool %q is not registered for executor %q", payload.ToolName, e.Key())
	}
	if e.recoverable {
		if _, ok = registration.Tool.(RecoverableBackgroundTool); !ok {
			return fmt.Errorf("backgroundtask/tool: tool %q is not recoverable", payload.ToolName)
		}
	} else if _, ok = registration.Tool.(RecoverableBackgroundTool); ok {
		return fmt.Errorf("backgroundtask/tool: recoverable tool %q used plain executor", payload.ToolName)
	}
	return registration.Tool.ValidateArguments(payload.Arguments)
}

func (e *executor) ValidateExecution(ctx context.Context, task *backgroundtask.Task) error {
	if task == nil {
		return errors.New("backgroundtask/tool: task is required")
	}
	payload, err := e.decodePayload(task.Spec)
	if err != nil {
		return err
	}
	_, ok := e.registry.resolve(payload.ToolName, e.recoverable)
	if !ok {
		return fmt.Errorf("backgroundtask/tool: tool %q is unavailable", payload.ToolName)
	}
	return nil
}

func (e *executor) ValidateCheckpoint(
	_ context.Context,
	spec backgroundtask.Spec,
	checkpoint []byte,
) error {
	if !e.recoverable {
		if len(checkpoint) != 0 {
			return errors.New("backgroundtask/tool: plain tool cannot recover a checkpoint")
		}
		return nil
	}
	payload, err := e.decodePayload(spec)
	if err != nil {
		return err
	}
	_, ok := e.registry.resolve(payload.ToolName, true)
	if !ok {
		return fmt.Errorf("backgroundtask/tool: recoverable tool %q is unavailable", payload.ToolName)
	}
	// Managed tools validate inside Execute so an incompatible boundary
	// checkpoint fails the claimed attempt instead of being silently discarded
	// by Manager's compatibility fallback.
	return nil
}

func (*executor) ValidateResume(
	context.Context,
	backgroundtask.Spec,
	[]byte,
	[]byte,
) ([]byte, error) {
	return nil, errors.New("backgroundtask/tool: waiting-input resume is unsupported")
}

func (e *executor) Execute(
	ctx context.Context,
	task *backgroundtask.Task,
	runtime backgroundtask.ExecutionRuntime,
) (*backgroundtask.ExecutionResult, error) {
	payload, err := e.decodePayload(task.Spec)
	if err != nil {
		return nil, err
	}
	registration, ok := e.registry.resolve(payload.ToolName, e.recoverable)
	if !ok {
		return nil, fmt.Errorf("backgroundtask/tool: tool %q is unavailable", payload.ToolName)
	}
	var run Run
	if e.recoverable && task.Attempt > 1 {
		if err = registration.Tool.(RecoverableBackgroundTool).ValidateCheckpoint(
			task.Checkpoint,
		); err != nil {
			return nil, fmt.Errorf("backgroundtask/tool: validate recovery checkpoint: %w", err)
		}
		run, err = registration.Tool.(RecoverableBackgroundTool).Recover(ctx, &RecoverRequest{
			TaskID: task.Spec.ID, Arguments: payload.Arguments, Attempt: task.Attempt,
			Checkpoint: append([]byte(nil), task.Checkpoint...),
		})
	} else {
		run, err = registration.Tool.Start(ctx, &StartRequest{
			TaskID: task.Spec.ID, Arguments: payload.Arguments, Attempt: task.Attempt,
		})
	}
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("backgroundtask/tool: implementation returned a nil run")
	}

	projection := e.registry.projections.load(task.Spec.ID)
	if projection != nil {
		defer projection.closeUpdates()
	}
	var updates *schema.StreamReader[*Update]
	if source, supported := run.(UpdateSource); supported {
		updates = source.Updates()
		if updates == nil {
			return nil, errors.New("backgroundtask/tool: update source returned a nil reader")
		}
		defer updates.Close()
	}
	if projection != nil {
		projection.signalReady()
	}
	if task.CancelRequestedAt != nil {
		if err = run.Stop(context.Background()); err != nil {
			return nil, fmt.Errorf("backgroundtask/tool: stop recovered canceled operation: %w", err)
		}
		return &backgroundtask.ExecutionResult{Status: backgroundtask.StatusCanceled}, nil
	}

	waitCtx, cancelWait := context.WithCancel(ctx)
	defer cancelWait()
	waitResult := make(chan struct {
		outcome *Outcome
		err     error
	}, 1)
	go func() {
		outcome, waitErr := run.Wait(waitCtx)
		waitResult <- struct {
			outcome *Outcome
			err     error
		}{outcome: outcome, err: waitErr}
	}()

	var updateResults <-chan updateResult
	var stopUpdates chan struct{}
	if updates != nil {
		results := make(chan updateResult, 1)
		updateResults = results
		stopUpdates = make(chan struct{})
		defer close(stopUpdates)
		go receiveUpdates(updates, results, stopUpdates)
	}
	materializerEnabled := registration.Materializer != nil &&
		task.Spec.OutputFile != "" && task.OutputFileErr == ""
	for {
		select {
		case result := <-waitResult:
			if result.err != nil {
				return nil, result.err
			}
			if updateResults != nil {
				if err = e.drainTerminalUpdates(
					ctx, task, runtime, registration, projection,
					updateResults, &materializerEnabled,
				); err != nil {
					return nil, err
				}
				updateResults = nil
			}
			return validateOutcome(result.outcome)
		case received, open := <-updateResults:
			if !open {
				updateResults = nil
				continue
			}
			if received.err != nil {
				if errors.Is(received.err, io.EOF) {
					updateResults = nil
					continue
				}
				return nil, received.err
			}
			if err = e.persistUpdate(
				ctx, task, runtime, registration, projection,
				received.update, &materializerEnabled,
			); err != nil {
				return nil, err
			}
		case control := <-runtime.Controls():
			switch control.Kind {
			case backgroundtask.ControlDrain:
				if !e.recoverable {
					return nil, errors.New("backgroundtask/tool: plain tool cannot drain")
				}
				checkpoint, checkpointErr := checkpointRun(run)
				if checkpointErr != nil {
					return nil, checkpointErr
				}
				cancelWait()
				return &backgroundtask.ExecutionResult{
					Directive:  backgroundtask.ExecutionDirectiveYield,
					Checkpoint: checkpoint,
				}, nil
			case backgroundtask.ControlStop:
				if err = run.Stop(context.Background()); err != nil {
					return nil, fmt.Errorf("backgroundtask/tool: stop operation: %w", err)
				}
				cancelWait()
				return &backgroundtask.ExecutionResult{Status: backgroundtask.StatusCanceled}, nil
			case backgroundtask.ControlTimeout:
				stopErr := run.Stop(context.Background())
				cancelWait()
				reason := control.Reason
				if reason == "" {
					reason = "background task timed out"
				}
				if stopErr != nil {
					reason = fmt.Sprintf("%s; stop operation: %v", reason, stopErr)
				}
				return &backgroundtask.ExecutionResult{
					Status: backgroundtask.StatusFailed, Error: reason,
				}, nil
			}
		case <-ctx.Done():
			cancelWait()
			if e.recoverable {
				checkpoint, checkpointErr := checkpointRun(run)
				if checkpointErr != nil {
					return nil, checkpointErr
				}
				return &backgroundtask.ExecutionResult{
					Directive:  backgroundtask.ExecutionDirectiveYield,
					Checkpoint: checkpoint,
				}, nil
			}
			return nil, ctx.Err()
		}
	}
}

func (e *executor) drainTerminalUpdates(
	ctx context.Context,
	task *backgroundtask.Task,
	runtime backgroundtask.ExecutionRuntime,
	registration *Registration,
	projection *liveProjection,
	results <-chan updateResult,
	materializerEnabled *bool,
) error {
	timer := time.NewTimer(terminalUpdateDrainTime)
	defer timer.Stop()
	for {
		select {
		case received, open := <-results:
			if !open || errors.Is(received.err, io.EOF) {
				return nil
			}
			if received.err != nil {
				return received.err
			}
			if err := e.persistUpdate(
				ctx, task, runtime, registration, projection,
				received.update, materializerEnabled,
			); err != nil {
				return err
			}
		case <-timer.C:
			return errors.New("backgroundtask/tool: update stream did not close after terminal outcome")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func checkpointRun(run Run) ([]byte, error) {
	checkpointer, supported := run.(Checkpointer)
	if !supported {
		return nil, nil
	}
	checkpoint, err := checkpointer.Checkpoint(context.Background())
	if err != nil {
		return nil, fmt.Errorf("%w: %v", backgroundtask.ErrCheckpointUnavailable, err)
	}
	return checkpoint, nil
}

type updateResult struct {
	update *Update
	err    error
}

func receiveUpdates(
	reader *schema.StreamReader[*Update],
	results chan<- updateResult,
	stop <-chan struct{},
) {
	defer close(results)
	for {
		update, err := reader.Recv()
		select {
		case results <- updateResult{update: update, err: err}:
		case <-stop:
			return
		}
		if err != nil {
			return
		}
	}
}

func (e *executor) persistUpdate(
	ctx context.Context,
	task *backgroundtask.Task,
	runtime backgroundtask.ExecutionRuntime,
	registration *Registration,
	projection *liveProjection,
	update *Update,
	materializerEnabled *bool,
) error {
	if update == nil {
		return errors.New("backgroundtask/tool: update must not be nil")
	}
	if err := validateUpdate(update); err != nil {
		return err
	}
	if e.recoverable && update.SourceID == "" {
		return errors.New("backgroundtask/tool: recoverable update source id is required")
	}
	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("backgroundtask/tool: encode update: %w", err)
	}
	inserted := true
	var record *backgroundtask.OutputRecord
	if update.SourceID == "" {
		record, err = runtime.AppendOutput(ctx, data)
	} else {
		var result *backgroundtask.AppendOutputOnceResult
		result, err = runtime.AppendOutputOnce(ctx, update.SourceID, data)
		if result != nil {
			record, inserted = result.Record, result.Inserted
		}
	}
	if err != nil {
		return fmt.Errorf("backgroundtask/tool: persist update: %w", err)
	}
	if record == nil {
		return errors.New("backgroundtask/tool: Store returned a nil output record")
	}
	if *materializerEnabled && update.SourceID != "" {
		err = registration.Materializer.AppendOutput(ctx, &MaterializeOutputRequest{
			TaskID: task.Spec.ID, SourceID: update.SourceID, Sequence: record.Sequence,
			Path: task.Spec.OutputFile, Data: append([]byte(nil), update.Data...),
		})
		if err != nil {
			*materializerEnabled = false
			if reportErr := runtime.ReportOutputFailure(ctx, err.Error()); reportErr != nil {
				return fmt.Errorf("backgroundtask/tool: report output materialization failure: %w", reportErr)
			}
		}
	}
	if inserted && projection != nil {
		projection.send(ctx, foreground.ProjectionDetached(ctx), update)
	}
	return nil
}

func validateUpdate(update *Update) error {
	if len(update.Data) > maxUpdateDataBytes {
		return errors.New("backgroundtask/tool: update data exceeds configured bounds")
	}
	if len(update.Kind) > maxUpdateKindBytes || len(update.MIMEType) > maxUpdateMIMETypeBytes {
		return errors.New("backgroundtask/tool: update kind or MIME type exceeds configured bounds")
	}
	if len(update.Metadata) > maxUpdateMetadata {
		return errors.New("backgroundtask/tool: update metadata exceeds configured bounds")
	}
	for key, value := range update.Metadata {
		if key == "" || len(key) > maxUpdateMetadataBytes || len(value) > maxUpdateMetadataBytes {
			return errors.New("backgroundtask/tool: update metadata entry exceeds configured bounds")
		}
	}
	return nil
}

func validateOutcome(outcome *Outcome) (*backgroundtask.ExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("backgroundtask/tool: run returned a nil outcome")
	}
	result := &backgroundtask.ExecutionResult{
		Status: outcome.Status, Data: append([]byte(nil), outcome.Data...), Error: outcome.Error,
	}
	switch outcome.Status {
	case backgroundtask.StatusCompleted:
		if outcome.Error != "" {
			return nil, errors.New("backgroundtask/tool: completed outcome cannot contain an error")
		}
	case backgroundtask.StatusFailed:
		if outcome.Error == "" {
			return nil, errors.New("backgroundtask/tool: failed outcome requires an error")
		}
	case backgroundtask.StatusCanceled:
		if len(outcome.Data) != 0 {
			return nil, errors.New("backgroundtask/tool: canceled outcome cannot contain data")
		}
	default:
		return nil, fmt.Errorf("backgroundtask/tool: unsupported outcome status %q", outcome.Status)
	}
	return result, nil
}

func (e *executor) decodePayload(spec backgroundtask.Spec) (*taskPayload, error) {
	if spec.ExecutorKey != e.Key() || spec.Kind != "background_tool" {
		return nil, errors.New("backgroundtask/tool: invalid executor key or task kind")
	}
	var payload taskPayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		return nil, fmt.Errorf("backgroundtask/tool: decode payload: %w", err)
	}
	if payload.Version != payloadVersion {
		return nil, fmt.Errorf("%w: managed-tool payload version %d", backgroundtask.ErrUnsupportedPayloadVersion, payload.Version)
	}
	if payload.ToolName == "" || payload.Arguments == "" {
		return nil, errors.New("backgroundtask/tool: payload tool name and arguments are required")
	}
	if len(payload.Arguments) > maxArgumentsBytes {
		return nil, errors.New("backgroundtask/tool: payload arguments exceed configured bounds")
	}
	return &payload, nil
}

// ArgumentsFromTask returns serialized arguments from a managed-tool task.
func ArgumentsFromTask(task *backgroundtask.Task) string {
	if task == nil {
		return ""
	}
	recoverable := task.Spec.ExecutorKey == RecoverableExecutorKey
	executor := &executor{recoverable: recoverable}
	payload, err := executor.decodePayload(task.Spec)
	if err != nil {
		return ""
	}
	return payload.Arguments
}

var (
	_ backgroundtask.Executor = (*executor)(nil)
)
