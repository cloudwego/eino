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

	"github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/adk/internal/startwindow"
	taskcore "github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

const payloadVersion = 1

const (
	maxArgumentsBytes        = 1 << 20
	maxInputRequestIDBytes   = 256
	maxInputRequestDataBytes = 256 << 10
	maxToolCheckpointBytes   = 256 << 10
	maxUpdateDataBytes       = 256 << 10
	maxUpdateKindBytes       = 128
	maxUpdateMetadata        = 32
	maxUpdateMetadataBytes   = 1024
	terminalUpdateDrainTime  = 5 * time.Second
)

type taskPayload struct {
	Version    int    `json:"version"`
	ToolName   string `json:"tool_name"`
	ToolCallID string `json:"tool_call_id,omitempty"`
	Arguments  string `json:"arguments"`
}

type managedCheckpoint struct {
	Version        int           `json:"version"`
	Started        bool          `json:"started"`
	ToolCheckpoint []byte        `json:"tool_checkpoint,omitempty"`
	Request        *InputRequest `json:"request,omitempty"`
	InputCursor    int64         `json:"input_cursor,omitempty"`
}

const managedCheckpointVersion = 1

type executor struct {
	registry    *Registry
	recoverable bool
}

func registerExecutors(manager *background.Manager, registry *Registry) error {
	if manager == nil || registry == nil {
		return errors.New("task/tool: manager and tool registry are required")
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
			return fmt.Errorf("task/tool: executor key %q is already registered incompatibly", candidate.Key())
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

func (e *executor) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	if e.recoverable {
		return background.LeaseExpiryRetry
	}
	return background.LeaseExpiryFail
}

// SupportsDrain reports whether the tool can reattach by task ID after a
// checkpointless yield.
func (e *executor) SupportsDrain() bool { return e.recoverable }

func (e *executor) ValidateSpec(spec background.Spec) error {
	payload, err := e.decodePayload(spec)
	if err != nil {
		return err
	}
	registration, ok := e.registry.resolve(payload.ToolName, e.recoverable)
	if !ok {
		return fmt.Errorf("task/tool: tool %q is not registered for executor %q", payload.ToolName, e.Key())
	}
	if e.recoverable {
		if _, ok = registration.Tool.(RecoverableTool); !ok {
			return fmt.Errorf("task/tool: tool %q is not recoverable", payload.ToolName)
		}
	} else if _, ok = registration.Tool.(RecoverableTool); ok {
		return fmt.Errorf("task/tool: recoverable tool %q used plain executor", payload.ToolName)
	}
	return registration.Tool.ValidateArguments(payload.Arguments)
}

func (e *executor) ValidateExecution(ctx context.Context, task *background.TaskSnapshot) error {
	if task == nil {
		return errors.New("task/tool: task is required")
	}
	payload, err := e.decodePayload(task.Spec)
	if err != nil {
		return err
	}
	_, ok := e.registry.resolve(payload.ToolName, e.recoverable)
	if !ok {
		return fmt.Errorf("task/tool: tool %q is unavailable", payload.ToolName)
	}
	return nil
}

func (e *executor) Execute( //nolint:cyclop,funlen // execution coordinates the managed-tool state machine
	ctx context.Context,
	task *background.TaskSnapshot,
	runtime background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	payload, err := e.decodePayload(task.Spec)
	if err != nil {
		return nil, err
	}
	registration, ok := e.registry.resolve(payload.ToolName, e.recoverable)
	if !ok {
		return nil, fmt.Errorf("task/tool: tool %q is unavailable", payload.ToolName)
	}
	var run Run
	inputRequest, toolCheckpoint, started, inputCursor, err := decodeManagedCheckpointState(
		task.Checkpoint,
	)
	if err != nil {
		return nil, err
	}
	hasInputRequest := inputRequest != nil
	resumable, supportsResume := registration.Tool.(ResumableTool)
	var resumeCursor int64
	var resumeCheckpoint []byte
	if !hasInputRequest && task.Attempt == 1 {
		if adopted := e.registry.adopted.consume(task.Spec.ID); adopted != nil {
			run = adopted.run
			toolCheckpoint = append([]byte(nil), adopted.checkpoint...)
		}
	}
	if run != nil {
		// The foreground owner already established the operation and handed its
		// checkpoint to TaskSnapshot.Checkpoint during Submit.
	} else if hasInputRequest {
		if !supportsResume {
			return nil, errors.New(
				"task/tool: waiting task implementation is not resumable",
			)
		}
		inputs, listErr := runtime.ListInputs(ctx, inputCursor, 100)
		if listErr != nil {
			return nil, listErr
		}
		var resumeInput *taskcore.InputRecord
		for _, input := range inputs.Inputs {
			if input.Kind == ResumeInputKind {
				resumeInput = input
				break
			}
		}
		if resumeInput == nil {
			nextCursor := inputCursor
			if len(inputs.Inputs) > 0 {
				nextCursor = inputs.Inputs[len(inputs.Inputs)-1].Sequence
			}
			checkpoint, checkpointErr := encodeManagedCheckpointAtCursor(
				inputRequest, toolCheckpoint, nextCursor,
			)
			if checkpointErr != nil {
				return nil, checkpointErr
			}
			return &background.ExecutionResult{
				Action:     background.ExecutionActionWaitInput,
				Checkpoint: checkpoint, InputCursor: nextCursor,
			}, nil
		}
		resumeRequest := &ResumeRequest{
			TaskID: task.Spec.ID, Arguments: payload.Arguments, Attempt: task.Attempt,
			RequestID: inputRequest.ID, Data: append([]byte(nil), resumeInput.Data...),
			Checkpoint: append([]byte(nil), toolCheckpoint...),
		}
		run, err = resumable.Resume(ctx, resumeRequest)
		if errors.Is(err, ErrResumeInputRejected) {
			if run != nil {
				return nil, errors.New(
					"task/tool: rejected resume returned a non-nil run",
				)
			}
			rejectedCheckpoint, checkpointErr := encodeManagedCheckpointAtCursor(
				inputRequest, toolCheckpoint, resumeInput.Sequence,
			)
			if checkpointErr != nil {
				return nil, checkpointErr
			}
			return &background.ExecutionResult{
				Action:      background.ExecutionActionWaitInput,
				Checkpoint:  rejectedCheckpoint,
				InputCursor: resumeInput.Sequence,
			}, nil
		}
		resumeCursor = resumeInput.Sequence
		resumeCheckpoint, err = encodeManagedCheckpointAtCursor(
			nil, toolCheckpoint, resumeCursor,
		)
	} else if e.recoverable && started {
		run, err = registration.Tool.(RecoverableTool).Recover(ctx, &RecoverRequest{
			TaskID: task.Spec.ID, Arguments: payload.Arguments, Attempt: task.Attempt,
			Checkpoint: append([]byte(nil), toolCheckpoint...),
		})
	} else {
		run, toolCheckpoint, err = e.startRun(ctx, registration, runtime, task, payload)
	}
	if err != nil {
		return nil, err
	}
	if run == nil {
		return nil, errors.New("task/tool: implementation returned a nil run")
	}
	if resumeCursor > inputCursor {
		if err = runtime.CommitInput(
			ctx, inputCursor, resumeCursor, resumeCheckpoint,
		); err != nil {
			_ = run.Stop(context.Background())
			return nil, fmt.Errorf("task/tool: commit resumed operation: %w", err)
		}
		task.Checkpoint = append([]byte(nil), resumeCheckpoint...)
		inputCursor = resumeCursor
	}

	projection := e.registry.projections.load(task.Spec.ID)
	if projection != nil {
		defer projection.closeUpdates()
	}
	var updates *schema.StreamReader[*Update]
	if source, supported := run.(UpdateSource); supported {
		updates = source.Updates()
		if updates == nil {
			return nil, errors.New("task/tool: update source returned a nil reader")
		}
		defer updates.Close()
	}
	if projection != nil {
		projection.signalReady()
	}
	if task.CancelRequestedAt != nil {
		if err = run.Stop(context.Background()); err != nil {
			return nil, fmt.Errorf("task/tool: stop recovered canceled operation: %w", err)
		}
		return &background.ExecutionResult{
			Action: background.ExecutionActionCancel, Error: task.CancelReason,
		}, nil
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
	persistence := &updatePersistence{
		task: task, runtime: runtime, registration: registration, projection: projection,
		materializerEnabled: registration.Materializer != nil &&
			task.Spec.OutputFile != "" && task.OutputFileErr == "",
	}
	for {
		select {
		case result := <-waitResult:
			if result.err != nil {
				return nil, result.err
			}
			if updateResults != nil {
				if err = e.drainTerminalUpdates(
					ctx, persistence, updateResults,
				); err != nil {
					return nil, err
				}
				updateResults = nil
			}
			return validateOutcome(
				result.outcome,
				supportsResume,
				toolCheckpoint,
				inputCursor,
			)
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
				ctx, persistence, received.update,
			); err != nil {
				return nil, err
			}
		case control := <-runtime.Controls():
			switch control.Kind {
			case background.ControlDrain:
				if !e.recoverable {
					return nil, errors.New("task/tool: plain tool cannot drain")
				}
				cancelWait()
				return &background.ExecutionResult{
					Action: background.ExecutionActionYield,
				}, nil
			case background.ControlStop:
				if err = run.Stop(context.Background()); err != nil {
					return nil, fmt.Errorf("task/tool: stop operation: %w", err)
				}
				cancelWait()
				return &background.ExecutionResult{
					Action: background.ExecutionActionCancel, Error: control.Reason,
				}, nil
			case background.ControlTimeout:
				stopErr := run.Stop(context.Background())
				cancelWait()
				reason := control.Reason
				if reason == "" {
					reason = "task timed out"
				}
				if stopErr != nil {
					reason = fmt.Sprintf("%s; stop operation: %v", reason, stopErr)
				}
				return &background.ExecutionResult{
					Action: background.ExecutionActionFail, Error: reason,
				}, nil
			}
		case <-ctx.Done():
			cancelWait()
			if e.recoverable {
				return &background.ExecutionResult{
					Action: background.ExecutionActionYield,
				}, nil
			}
			return nil, ctx.Err()
		}
	}
}

func (e *executor) startRun(
	ctx context.Context,
	registration *Registration,
	runtime background.ExecutionRuntime,
	task *background.TaskSnapshot,
	payload *taskPayload,
) (Run, []byte, error) {
	defer startwindow.Signal(ctx)
	startResult, err := registration.Tool.Start(ctx, &StartRequest{
		TaskID: task.Spec.ID, Arguments: payload.Arguments, Attempt: task.Attempt,
	})
	if err != nil {
		return nil, nil, err
	}
	if startResult == nil {
		return nil, nil, errors.New("task/tool: implementation returned a nil start result")
	}
	if startResult.Run == nil {
		return nil, nil, errors.New("task/tool: implementation returned a nil run")
	}
	run := startResult.Run
	toolCheckpoint := append([]byte(nil), startResult.Checkpoint...)
	if !e.recoverable && len(toolCheckpoint) > 0 {
		return nil, nil, stopRunAfterStartCommitFailure(run, errors.New(
			"task/tool: plain tool cannot return a checkpoint",
		))
	}
	if e.recoverable {
		checkpoint, checkpointErr := encodeManagedCheckpoint(nil, toolCheckpoint)
		if checkpointErr != nil {
			return nil, nil, stopRunAfterStartCommitFailure(run, checkpointErr)
		}
		if err = runtime.CommitStart(ctx, checkpoint); err != nil {
			return nil, nil, stopRunAfterStartCommitFailure(run, fmt.Errorf(
				"task/tool: commit external start: %w",
				err,
			))
		}
	}
	return run, toolCheckpoint, nil
}

func (e *executor) drainTerminalUpdates(
	ctx context.Context,
	persistence *updatePersistence,
	results <-chan updateResult,
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
				ctx, persistence, received.update,
			); err != nil {
				return err
			}
		case <-timer.C:
			return errors.New("task/tool: update stream did not close after terminal outcome")
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

type updateResult struct {
	update *Update
	err    error
}

type updatePersistence struct {
	task                *background.TaskSnapshot
	runtime             background.ExecutionRuntime
	registration        *Registration
	projection          *liveProjection
	materializerEnabled bool
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

func stopRunAfterStartCommitFailure(run Run, cause error) error {
	if stopErr := run.Stop(context.Background()); stopErr != nil {
		return fmt.Errorf("%w; stop operation: %v", cause, stopErr)
	}
	return cause
}

func (e *executor) persistUpdate(
	ctx context.Context,
	persistence *updatePersistence,
	update *Update,
) error {
	if update == nil {
		return errors.New("task/tool: update must not be nil")
	}
	if err := validateUpdate(update); err != nil {
		return err
	}
	callerSuppliedEventID := update.EventID != ""
	if e.recoverable && !callerSuppliedEventID {
		return errors.New("task/tool: recoverable update event id is required")
	}
	data, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("task/tool: encode update: %w", err)
	}
	result, err := persistence.runtime.EmitProgress(ctx, update.EventID, data)
	if err != nil {
		return fmt.Errorf("task/tool: persist update: %w", err)
	}
	if persistence.materializerEnabled && callerSuppliedEventID {
		err = persistence.registration.Materializer.AppendOutput(ctx, &MaterializeOutputRequest{
			TaskID: persistence.task.Spec.ID, EventID: result.EventID,
			Path: persistence.task.Spec.OutputFile, Data: append([]byte(nil), update.Data...),
		})
		if err != nil {
			persistence.materializerEnabled = false
			if reportErr := persistence.runtime.ReportTranscriptFailure(ctx, err); reportErr != nil {
				return fmt.Errorf("task/tool: report transcript materialization failure: %w", reportErr)
			}
		}
	}
	if result.FirstEmission && persistence.projection != nil {
		projected := cloneUpdate(update)
		projected.EventID = result.EventID
		persistence.projection.send(ctx, foreground.ProjectionDetached(ctx), projected)
	}
	return nil
}

func validateUpdate(update *Update) error {
	if len(update.Data) > maxUpdateDataBytes {
		return errors.New("task/tool: update data exceeds configured bounds")
	}
	if len(update.Kind) > maxUpdateKindBytes {
		return errors.New("task/tool: update kind exceeds configured bounds")
	}
	if len(update.Metadata) > maxUpdateMetadata {
		return errors.New("task/tool: update metadata exceeds configured bounds")
	}
	for key, value := range update.Metadata {
		if key == "" || len(key) > maxUpdateMetadataBytes || len(value) > maxUpdateMetadataBytes {
			return errors.New("task/tool: update metadata entry exceeds configured bounds")
		}
	}
	return nil
}

func validateOutcome(
	outcome *Outcome,
	supportsResume bool,
	toolCheckpoint []byte,
	inputCursor int64,
) (*background.ExecutionResult, error) {
	if outcome == nil {
		return nil, errors.New("task/tool: run returned a nil outcome")
	}
	result := &background.ExecutionResult{
		Data: append([]byte(nil), outcome.Data...), Error: outcome.Error,
	}
	switch outcome.Status {
	case background.StatusCompleted:
		result.Action = background.ExecutionActionComplete
		if outcome.Error != "" || outcome.InputRequest != nil ||
			len(outcome.Checkpoint) > 0 {
			return nil, errors.New(
				"task/tool: completed outcome cannot contain an error, input request, or checkpoint",
			)
		}
		result.InputCursor = inputCursor
	case background.StatusFailed:
		result.Action = background.ExecutionActionFail
		if outcome.Error == "" {
			return nil, errors.New("task/tool: failed outcome requires an error")
		}
		if len(outcome.Data) != 0 || outcome.InputRequest != nil ||
			len(outcome.Checkpoint) > 0 {
			return nil, errors.New(
				"task/tool: failed outcome cannot contain data, an input request, or a checkpoint",
			)
		}
	case background.StatusCanceled:
		result.Action = background.ExecutionActionCancel
		if len(outcome.Data) != 0 || outcome.InputRequest != nil ||
			len(outcome.Checkpoint) > 0 {
			return nil, errors.New(
				"task/tool: canceled outcome cannot contain data, an input request, or a checkpoint",
			)
		}
	case background.StatusWaitingInput:
		result.Action = background.ExecutionActionWaitInput
		if !supportsResume {
			return nil, errors.New(
				"task/tool: waiting-input outcome requires ResumableTool",
			)
		}
		if len(outcome.Data) != 0 || outcome.Error != "" {
			return nil, errors.New(
				"task/tool: waiting-input outcome cannot contain terminal data or error",
			)
		}
		if outcome.InputRequest == nil {
			return nil, errors.New(
				"task/tool: waiting-input outcome requires an input request ID",
			)
		}
		if len(outcome.Checkpoint) > 0 {
			toolCheckpoint = outcome.Checkpoint
		}
		checkpoint, err := encodeManagedCheckpointAtCursor(
			outcome.InputRequest,
			toolCheckpoint,
			inputCursor,
		)
		if err != nil {
			return nil, err
		}
		result.Checkpoint = checkpoint
		result.InputCursor = inputCursor
	default:
		return nil, fmt.Errorf("task/tool: unsupported outcome status %q", outcome.Status)
	}
	return result, nil
}

func encodeManagedCheckpoint(
	request *InputRequest,
	toolCheckpoint []byte,
) ([]byte, error) {
	return encodeManagedCheckpointAtCursor(request, toolCheckpoint, 0)
}

func encodeManagedCheckpointAtCursor(
	request *InputRequest,
	toolCheckpoint []byte,
	inputCursor int64,
) ([]byte, error) {
	if inputCursor < 0 {
		return nil, errors.New("task/tool: input cursor is invalid")
	}
	if len(toolCheckpoint) > maxToolCheckpointBytes {
		return nil, errors.New(
			"task/tool: tool checkpoint exceeds configured bounds",
		)
	}
	if request != nil {
		if request.ID == "" {
			return nil, errors.New(
				"task/tool: waiting-input outcome requires an input request ID",
			)
		}
		if len(request.ID) > maxInputRequestIDBytes {
			return nil, errors.New("task/tool: input request ID exceeds configured bounds")
		}
		if len(request.Data) > maxInputRequestDataBytes {
			return nil, errors.New("task/tool: input request data exceeds configured bounds")
		}
		if len(request.Data) > 0 && !json.Valid(request.Data) {
			return nil, errors.New("task/tool: input request data must be valid JSON")
		}
	}
	checkpoint := &managedCheckpoint{
		Version:        managedCheckpointVersion,
		Started:        true,
		ToolCheckpoint: append([]byte(nil), toolCheckpoint...),
		InputCursor:    inputCursor,
	}
	if request != nil {
		checkpoint.Request = &InputRequest{
			ID: request.ID, Data: append(json.RawMessage(nil), request.Data...),
		}
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("task/tool: encode checkpoint: %w", err)
	}
	return data, nil
}

func decodeManagedCheckpoint(
	data []byte,
) (*InputRequest, []byte, bool, error) {
	request, checkpoint, started, _, err := decodeManagedCheckpointState(data)
	return request, checkpoint, started, err
}

func decodeManagedCheckpointState(
	data []byte,
) (*InputRequest, []byte, bool, int64, error) {
	if len(data) == 0 {
		return nil, nil, false, 0, nil
	}
	var checkpoint managedCheckpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return nil, nil, false, 0, fmt.Errorf(
			"task/tool: decode checkpoint: %w",
			err,
		)
	}
	if checkpoint.Version != managedCheckpointVersion ||
		checkpoint.InputCursor < 0 ||
		len(checkpoint.ToolCheckpoint) > maxToolCheckpointBytes ||
		(!checkpoint.Started && checkpoint.Request == nil) {
		return nil, nil, false, 0, errors.New(
			"task/tool: incompatible managed-tool checkpoint",
		)
	}
	var request *InputRequest
	if checkpoint.Request != nil {
		if checkpoint.Request.ID == "" ||
			len(checkpoint.Request.ID) > maxInputRequestIDBytes ||
			len(checkpoint.Request.Data) > maxInputRequestDataBytes {
			return nil, nil, false, 0, errors.New(
				"task/tool: incompatible managed-tool checkpoint",
			)
		}
		request = &InputRequest{
			ID:   checkpoint.Request.ID,
			Data: append(json.RawMessage(nil), checkpoint.Request.Data...),
		}
	}
	return request, append([]byte(nil), checkpoint.ToolCheckpoint...),
		checkpoint.Started, checkpoint.InputCursor, nil
}

// ReadInputRequest returns the application-facing request for a managed tool
// currently in StatusWaitingInput. The returned value owns its Data bytes.
func ReadInputRequest(task *background.TaskSnapshot) (*InputRequest, error) {
	if task == nil || task.Status != background.StatusWaitingInput ||
		task.Spec.ExecutorKey != RecoverableExecutorKey ||
		task.Spec.Kind != "background_tool" {
		return nil, errors.New(
			"task/tool: waiting managed-tool task is required",
		)
	}
	request, _, _, err := decodeManagedCheckpoint(task.Checkpoint)
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, errors.New("task/tool: task has no input request")
	}
	return request, nil
}

func (e *executor) decodePayload(spec background.Spec) (*taskPayload, error) {
	if spec.ExecutorKey != e.Key() || spec.Kind != "background_tool" {
		return nil, errors.New("task/tool: invalid executor key or task kind")
	}
	var payload taskPayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		return nil, fmt.Errorf("task/tool: decode payload: %w", err)
	}
	if payload.Version != payloadVersion {
		return nil, fmt.Errorf("%w: managed-tool payload version %d", background.ErrUnsupportedExecutorPayloadVersion, payload.Version)
	}
	if payload.ToolName == "" || payload.Arguments == "" {
		return nil, errors.New("task/tool: payload tool name and arguments are required")
	}
	if len(payload.Arguments) > maxArgumentsBytes {
		return nil, errors.New("task/tool: payload arguments exceed configured bounds")
	}
	return &payload, nil
}

var (
	_ background.Executor = (*executor)(nil)
)
