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

// Package subagent provides a durable backgroundtask executor for ADK sub-agent runs.
package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	foregroundcoord "github.com/cloudwego/eino/adk/internal/foreground"
)

const (
	// ExecutorKey is the backgroundtask executor key for durable sub-agent tasks.
	ExecutorKey = "eino.dev/subagent"

	payloadVersion = 2
)

type taskPayload struct {
	Version      int    `json:"version"`
	SubAgentName string `json:"subagent_name"`
	Query        string `json:"query"`
}

type checkpointState struct {
	TargetIDs []string `json:"target_ids,omitempty"`
	Sequence  int64    `json:"sequence"`
}

// RunOptionsFactory reconstructs deployment-owned run options for each task attempt.
// Every worker serving the same registered agent name must configure a semantically
// equivalent factory for the full lifetime of resumable tasks and return fresh option
// values. The executor adds attempt-local checkpoint and cancellation options separately.
type RunOptionsFactory func() ([]adk.AgentRunOption, error)

// AgentRegistration binds a persisted name to worker-local dependencies. Every worker
// eligible to execute that name must provide a semantically equivalent registration for
// the full lifetime of its resumable tasks. Incompatible Agent or run-option changes
// require draining existing tasks or registering a new agent name.
type AgentRegistration[M adk.MessageType] struct {
	Agent             adk.TypedResumableAgent[M]
	RunOptionsFactory RunOptionsFactory
}

// ExecutorConfig provides the durable session dependencies shared by every
// sub-agent task executed by an Executor.
type ExecutorConfig[M adk.MessageType] struct {
	// SessionStore persists the child session event log for every executed task.
	SessionStore adk.SessionEventStore[M]
	// CheckPointStore persists ADK Runner checkpoints for interruption and recovery.
	CheckPointStore adk.CheckPointStore
	// SessionConfig optionally customizes child-session persistence.
	SessionConfig *adk.SessionConfig[M]
}

// Executor runs durable sub-agent tasks through ADK Runner checkpointing.
type Executor[M adk.MessageType] struct {
	sessionStore    adk.SessionEventStore[M]
	checkPointStore adk.CheckPointStore
	sessionConfig   *adk.SessionConfig[M]

	mu            sync.RWMutex
	registrations map[string]*AgentRegistration[M]
}

// NewExecutor constructs a durable sub-agent executor with explicit session
// and checkpoint dependencies.
func NewExecutor[M adk.MessageType](config *ExecutorConfig[M]) (*Executor[M], error) {
	if config == nil || config.SessionStore == nil || config.CheckPointStore == nil {
		return nil, errors.New(
			"backgroundtask/subagent: session store and checkpoint store are required",
		)
	}
	var sessionConfig *adk.SessionConfig[M]
	if config.SessionConfig != nil {
		copy := *config.SessionConfig
		sessionConfig = &copy
	}
	return &Executor[M]{
		sessionStore: config.SessionStore, checkPointStore: config.CheckPointStore,
		sessionConfig: sessionConfig,
	}, nil
}

// SessionEventStore returns the store used for durable child-session progress.
func (e *Executor[M]) SessionEventStore() adk.SessionEventStore[M] {
	if e == nil {
		return nil
	}
	return e.sessionStore
}

// Key returns the backgroundtask executor key for sub-agent tasks.
func (e *Executor[M]) Key() string { return ExecutorKey }

// LeaseExpiryPolicy allows another worker to resume a lost sub-agent attempt.
func (*Executor[M]) LeaseExpiryPolicy() backgroundtask.LeaseExpiryPolicy {
	return backgroundtask.LeaseExpiryRetry
}

// Register binds a stable sub-agent name to the implementation and output policy.
func (e *Executor[M]) Register(name string, registration *AgentRegistration[M]) error {
	if e == nil || name == "" || registration == nil || registration.Agent == nil {
		return errors.New("backgroundtask/subagent: agent name and implementation are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.registrations == nil {
		e.registrations = make(map[string]*AgentRegistration[M])
	}
	if _, exists := e.registrations[name]; exists {
		return backgroundtask.ErrAlreadyExists
	}
	copy := *registration
	e.registrations[name] = &copy
	return nil
}

func (e *Executor[M]) resolveRegistration(name string) (*AgentRegistration[M], error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	registration, ok := e.registrations[name]
	if !ok {
		return nil, fmt.Errorf("backgroundtask/subagent: agent %q is unavailable", name)
	}
	copy := *registration
	return &copy, nil
}

func (e *Executor[M]) resolveAgent(name string) (adk.TypedResumableAgent[M], error) {
	registration, err := e.resolveRegistration(name)
	if err != nil {
		return nil, err
	}
	return registration.Agent, nil
}

func copyMaterializedEvent[M adk.MessageType](
	event *adk.TypedAgentEvent[M],
) *adk.TypedAgentEvent[M] {
	if event == nil {
		return nil
	}
	copy := *event
	copy.RunPath = append([]adk.RunStep(nil), event.RunPath...)
	if event.Output != nil {
		output := *event.Output
		if event.Output.MessageOutput != nil {
			variant := *event.Output.MessageOutput
			output.MessageOutput = &variant
		}
		copy.Output = &output
	}
	return &copy
}

// ValidateSpec verifies that spec contains a compatible sub-agent payload.
func (e *Executor[M]) ValidateSpec(spec backgroundtask.Spec) error {
	payload, err := validateSpecPayload(spec)
	if err != nil {
		return err
	}
	_, err = e.resolveRegistration(payload.SubAgentName)
	return err
}

// ValidateExecution verifies worker dependencies without mutating external state.
func (e *Executor[M]) ValidateExecution(_ context.Context, task *backgroundtask.Task) error {
	if task == nil {
		return errors.New("backgroundtask/subagent: task is required")
	}
	if e == nil || e.sessionStore == nil || e.checkPointStore == nil {
		return errors.New("backgroundtask/subagent: executor dependencies are unavailable")
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return err
	}
	_, err = e.resolveRegistration(payload.SubAgentName)
	return err
}

func (e *Executor[M]) SupportsDrain() bool { return true }

func validateSpecPayload(spec backgroundtask.Spec) (*taskPayload, error) {
	if spec.ExecutorKey != ExecutorKey || spec.Kind != "subagent" {
		return nil, errors.New("backgroundtask/subagent: invalid executor key or task kind")
	}
	payload, err := decodePayload(spec)
	if err != nil {
		return nil, err
	}
	if payload.Version != payloadVersion {
		return nil, fmt.Errorf("%w: subagent payload version %d", backgroundtask.ErrUnsupportedExecutorPayloadVersion, payload.Version)
	}
	if payload.SubAgentName == "" || payload.Query == "" {
		return nil, errors.New("backgroundtask/subagent: subagent name and query are required")
	}
	return payload, nil
}

func childSessionID(taskID string) string {
	return taskID + "/session"
}

func checkpointID(taskID string) string {
	return taskID + "/checkpoint"
}

func validateCheckpoint(
	spec backgroundtask.Spec,
	checkpoint []byte,
) error {
	if _, err := validateSpecPayload(spec); err != nil {
		return err
	}
	if len(checkpoint) == 0 {
		return errors.New("backgroundtask/subagent: compatible checkpoint is required")
	}
	var state checkpointState
	if err := json.Unmarshal(checkpoint, &state); err != nil || state.Sequence <= 0 {
		return errors.New("backgroundtask/subagent: checkpoint state does not match task")
	}
	return nil
}

func (e *Executor[M]) validateResume(
	spec backgroundtask.Spec,
	checkpoint []byte,
	resumeData []byte,
) ([]byte, error) {
	if err := e.ValidateSpec(spec); err != nil {
		return nil, err
	}
	if err := validateCheckpoint(spec, checkpoint); err != nil {
		return nil, err
	}
	var state checkpointState
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		return nil, err
	}
	if len(resumeData) == 0 {
		return nil, nil
	}
	var targets map[string]json.RawMessage
	if err := json.Unmarshal(resumeData, &targets); err != nil || len(targets) == 0 {
		return nil, errors.New("backgroundtask/subagent: resume targets are invalid")
	}
	allowed := make(map[string]struct{}, len(state.TargetIDs))
	for _, id := range state.TargetIDs {
		allowed[id] = struct{}{}
	}
	for id := range targets {
		if _, ok := allowed[id]; !ok {
			return nil, fmt.Errorf("backgroundtask/subagent: resume target %q is not interrupted", id)
		}
	}
	normalized, err := json.Marshal(targets)
	if err != nil {
		return nil, err
	}
	return normalized, nil
}

// Execute runs or resumes the sub-agent task and returns its lifecycle outcome.
func (e *Executor[M]) Execute(
	ctx context.Context,
	task *backgroundtask.Task,
	runtime backgroundtask.ExecutionRuntime,
) (result *backgroundtask.ExecutionResult, err error) {
	if task.Attempt > 1 || len(task.Checkpoint) > 0 {
		if err = validateCheckpoint(task.Spec, task.Checkpoint); err != nil {
			return nil, err
		}
	}
	if len(task.PendingResume) > 0 {
		normalized, resumeErr := e.validateResume(
			task.Spec, task.Checkpoint, task.PendingResume,
		)
		if resumeErr != nil {
			// Resume input may have been persisted through the generic lifecycle
			// API. Keep the task resumable instead of terminally failing it.
			return &backgroundtask.ExecutionResult{
				Status:     backgroundtask.StatusWaitingInput,
				Checkpoint: append([]byte(nil), task.Checkpoint...),
			}, nil
		}
		task.PendingResume = normalized
	}
	payload, err := decodePayload(task.Spec)
	if err != nil {
		return nil, err
	}
	registration, err := e.resolveRegistration(payload.SubAgentName)
	if err != nil {
		return nil, err
	}
	var runOptions []adk.AgentRunOption
	if registration.RunOptionsFactory != nil {
		runOptions, err = registration.RunOptionsFactory()
		if err != nil {
			return nil, fmt.Errorf("backgroundtask/subagent: reconstruct run options: %w", err)
		}
	}
	foreground := agenttool.ForegroundExecutionFromContext[*adk.TypedAgentEvent[M]](ctx)
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{
		Agent: registration.Agent, EnableStreaming: foreground.EnableStreaming(),
		CheckPointStore: e.checkPointStore,
		SessionID:       childSessionID(task.Spec.ID), SessionStore: e.sessionStore,
		SessionConfig: e.sessionConfig,
	})
	cancelOption, cancelRun := adk.WithCancel()
	controlRequests := make(chan backgroundtask.ControlRequest, 1)
	controlWatchDone := make(chan struct{})
	defer close(controlWatchDone)
	go func() {
		select {
		case control := <-runtime.Controls():
			controlRequests <- control
			cancelOptions := []adk.AgentCancelOption{adk.WithRecursive()}
			if control.Kind == backgroundtask.ControlDrain {
				cancelOptions = append(cancelOptions,
					adk.WithAgentCancelMode(adk.CancelAfterChatModel|adk.CancelAfterToolCalls))
			} else {
				cancelOptions = append(cancelOptions, adk.WithAgentCancelMode(adk.CancelImmediate))
			}
			if handle, accepted := cancelRun(cancelOptions...); accepted {
				_ = handle.Wait()
			}
		case <-controlWatchDone:
		case <-ctx.Done():
		}
	}()
	runOptions = append(runOptions, cancelOption)
	iter, err := e.beginRun(ctx, runner, task, payload, runOptions...)
	if err != nil {
		return nil, err
	}

	var final string
	var interrupted *adk.InterruptInfo
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Action != nil && event.Action.Interrupted != nil {
			interrupted = event.Action.Interrupted
		}
		if event.Err != nil && interrupted == nil {
			return e.handleEventError(ctx, iter, task, controlRequests, event.Err)
		}
		materialized := event
		if event.Output != nil && event.Output.MessageOutput != nil {
			message, messageErr := event.Output.MessageOutput.GetMessage()
			if messageErr != nil {
				return nil, messageErr
			}
			materialized = materializedEvent(event, message)
			final = agenttool.ExtractTextContent(message)
		}
		foreground.Forward(
			materialized, foregroundcoord.ProjectionDetached(ctx), copyMaterializedEvent[M],
		)
	}
	if controlResult, controlErr, controlled := e.controlResult(ctx, task, pollControl(controlRequests)); controlled {
		return controlResult, controlErr
	}
	if interrupted != nil {
		return e.interruptResult(ctx, task, interrupted)
	}
	return &backgroundtask.ExecutionResult{
		Status: backgroundtask.StatusCompleted,
		Data:   []byte(final),
	}, nil
}

func materializedEvent[M adk.MessageType](
	event *adk.TypedAgentEvent[M],
	message M,
) *adk.TypedAgentEvent[M] {
	copy := *event
	output := *event.Output
	variant := *event.Output.MessageOutput
	variant.IsStreaming = false
	variant.Message = message
	variant.MessageStream = nil
	output.MessageOutput = &variant
	copy.Output = &output
	return &copy
}

func (e *Executor[M]) beginRun(
	ctx context.Context,
	runner *adk.TypedRunner[M],
	task *backgroundtask.Task,
	payload *taskPayload,
	options ...adk.AgentRunOption,
) (*adk.AsyncIterator[*adk.TypedAgentEvent[M]], error) {
	id := checkpointID(task.Spec.ID)
	if len(task.Checkpoint) == 0 {
		queryOptions := append([]adk.AgentRunOption(nil), options...)
		queryOptions = append(queryOptions, adk.WithCheckPointID(id))
		return runner.Query(
			ctx, payload.Query, queryOptions...,
		), nil
	}
	if len(task.PendingResume) == 0 {
		return runner.Resume(ctx, id, options...)
	}
	targets, err := decodeResumeTargets(task.PendingResume)
	if err != nil {
		return nil, err
	}
	return runner.ResumeWithParams(
		ctx, id, &adk.ResumeParams{Targets: targets}, options...,
	)
}

func decodeResumeTargets(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var targets map[string]any
	if err := decoder.Decode(&targets); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("backgroundtask/subagent: resume targets contain trailing data")
		}
		return nil, err
	}
	return targets, nil
}

func (e *Executor[M]) handleEventError(
	ctx context.Context,
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]],
	task *backgroundtask.Task,
	controlRequests <-chan backgroundtask.ControlRequest,
	err error,
) (*backgroundtask.ExecutionResult, error) {
	control := pollControl(controlRequests)
	var cancelError *adk.CancelError
	if control.Kind == "" && (errors.Is(err, context.Canceled) || errors.As(err, &cancelError)) {
		control = waitForControl(ctx, controlRequests)
	}
	if control.Kind != "" {
		for {
			if _, open := iter.Next(); !open {
				break
			}
		}
	}
	if result, controlErr, controlled := e.controlResult(ctx, task, control); controlled {
		return result, controlErr
	}
	return nil, err
}

func waitForControl(
	ctx context.Context,
	controls <-chan backgroundtask.ControlRequest,
) backgroundtask.ControlRequest {
	select {
	case control := <-controls:
		return control
	case <-ctx.Done():
		return backgroundtask.ControlRequest{}
	case <-time.After(100 * time.Millisecond):
		return backgroundtask.ControlRequest{}
	}
}

func (e *Executor[M]) interruptResult(
	ctx context.Context,
	task *backgroundtask.Task,
	interrupted *adk.InterruptInfo,
) (*backgroundtask.ExecutionResult, error) {
	if _, exists, err := e.checkPointStore.Get(ctx, checkpointID(task.Spec.ID)); err != nil || !exists {
		if err == nil {
			err = errors.New("backgroundtask/subagent: runner checkpoint is missing")
		}
		return nil, err
	}
	state := checkpointState{
		Sequence: nextCheckpointSequence(task.Checkpoint),
	}
	for _, interruptContext := range interrupted.InterruptContexts {
		if interruptContext.ID != "" {
			state.TargetIDs = append(state.TargetIDs, interruptContext.ID)
		}
	}
	stateBytes, err := json.Marshal(state)
	if err != nil || len(state.TargetIDs) == 0 {
		if err == nil {
			err = errors.New("backgroundtask/subagent: interrupt has no resumable targets")
		}
		return nil, err
	}
	return &backgroundtask.ExecutionResult{
		Status:     backgroundtask.StatusWaitingInput,
		Checkpoint: stateBytes,
	}, nil
}

func (e *Executor[M]) controlResult(
	_ context.Context,
	task *backgroundtask.Task,
	control backgroundtask.ControlRequest,
) (*backgroundtask.ExecutionResult, error, bool) {
	switch control.Kind {
	case backgroundtask.ControlStop:
		reason := control.Reason
		if reason == "" {
			reason = "task was canceled"
		}
		return &backgroundtask.ExecutionResult{
			Status: backgroundtask.StatusCanceled,
			Error:  reason,
		}, nil, true
	case backgroundtask.ControlDrain:
		if _, exists, err := e.checkPointStore.Get(
			context.Background(), checkpointID(task.Spec.ID),
		); err != nil || !exists {
			if err == nil {
				err = errors.New("runner checkpoint is missing")
			}
			return nil, fmt.Errorf("%w: %v", backgroundtask.ErrDrainCheckpointUnavailable, err), true
		}
		stateBytes, err := json.Marshal(checkpointState{
			Sequence: nextCheckpointSequence(task.Checkpoint),
		})
		if err != nil {
			return nil, err, true
		}
		return &backgroundtask.ExecutionResult{
			Status:     backgroundtask.StatusSuspended,
			Checkpoint: stateBytes,
		}, nil, true
	case backgroundtask.ControlTimeout:
		return &backgroundtask.ExecutionResult{
			Status: backgroundtask.StatusFailed,
			Error:  control.Reason,
		}, nil, true
	}
	return nil, nil, false
}

func pollControl(controls <-chan backgroundtask.ControlRequest) backgroundtask.ControlRequest {
	select {
	case control := <-controls:
		return control
	default:
		return backgroundtask.ControlRequest{}
	}
}

func decodePayload(spec backgroundtask.Spec) (*taskPayload, error) {
	var payload taskPayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func nextCheckpointSequence(previous []byte) int64 {
	var state checkpointState
	if len(previous) == 0 || json.Unmarshal(previous, &state) != nil || state.Sequence < 1 {
		return 1
	}
	return state.Sequence + 1
}

// SubmitRequest describes a durable sub-agent task to submit.
type SubmitRequest struct {
	TaskID       string
	SubAgentName string
	Query        string
	Description  string
	SessionID    string
}

// Submit persists a durable sub-agent task through manager.
func Submit(ctx context.Context, manager *backgroundtask.Manager, req *SubmitRequest) (*backgroundtask.Task, error) {
	if manager == nil || req == nil || req.SessionID == "" ||
		req.SubAgentName == "" || req.Query == "" {
		return nil, errors.New("backgroundtask/subagent: manager, parent session, subagent name, and query are required")
	}
	id := req.TaskID
	if id == "" {
		var err error
		id, err = manager.AllocateTaskID(ctx, &backgroundtask.AllocateTaskIDRequest{Kind: "subagent"})
		if err != nil {
			return nil, err
		}
	}
	payload := taskPayload{
		Version: payloadVersion, SubAgentName: req.SubAgentName, Query: req.Query,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return manager.Submit(ctx, backgroundtask.Spec{
		ID: id, ExecutorKey: ExecutorKey, Kind: "subagent", Payload: data,
		Description: req.Description, SessionID: req.SessionID,
		NotifySession: true,
	})
}
