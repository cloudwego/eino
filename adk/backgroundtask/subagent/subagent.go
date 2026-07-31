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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	"github.com/cloudwego/eino/schema"
)

const (
	// ExecutorKey is the backgroundtask executor key for durable sub-agent tasks.
	ExecutorKey = "eino.dev/subagent"

	payloadVersion = 1
)

var ErrRunnerEnvironmentRequired = errors.New("backgroundtask/subagent: runner environment is required")

// ResumeMode describes how a sub-agent task resumes after waiting for input.
type ResumeMode string

const (
	// ResumeNativeInterrupt resumes by passing structured interrupt responses.
	ResumeNativeInterrupt ResumeMode = "native_interrupt"
	// ResumeNextTurn resumes by appending a new user turn to the child session.
	ResumeNextTurn ResumeMode = "next_turn"
)

// TaskPayload is the executor-private serialized payload for sub-agent tasks.
type TaskPayload struct {
	Version          int        `json:"version"`
	SubAgentName     string     `json:"subagent_name"`
	Prompt           string     `json:"prompt"`
	ChildSessionID   string     `json:"child_session_id"`
	CheckpointID     string     `json:"checkpoint_id"`
	ResumeMode       ResumeMode `json:"resume_mode"`
	AllowEmptyResume bool       `json:"allow_empty_resume"`
}

type checkpointState struct {
	CheckpointID string     `json:"checkpoint_id"`
	TargetIDs    []string   `json:"target_ids"`
	AllowEmpty   bool       `json:"allow_empty"`
	Mode         ResumeMode `json:"mode"`
	Sequence     int64      `json:"sequence"`
}

// EventFormat encodes one materialized event as one output transcript record.
type EventFormat[M adk.MessageType] func(context.Context, *adk.TypedAgentEvent[M]) (string, error)

// AgentRegistration binds a persisted name to worker-local dependencies.
type AgentRegistration[M adk.MessageType] struct {
	Agent       adk.TypedResumableAgent[M]
	OutputStore filesystem.AppendOpener
	EventFormat EventFormat[M]
}

type foregroundObserver[M adk.MessageType] struct {
	mu              sync.Mutex
	active          bool
	receivers       []agenttool.EventReceiver[*adk.TypedAgentEvent[M]]
	runOptions      []adk.AgentRunOption
	enableStreaming bool
}

// Executor runs durable sub-agent tasks through ADK Runner checkpointing.
type Executor[M adk.MessageType] struct {
	mu            sync.RWMutex
	registrations map[string]*AgentRegistration[M]
	observersMu   sync.Mutex
	observers     map[string]*foregroundObserver[M]
}

// Key returns the backgroundtask executor key for sub-agent tasks.
func (e *Executor[M]) Key() string { return ExecutorKey }

// RegisterAgent registers the resumable implementation used for a persisted sub-agent name.
func (e *Executor[M]) RegisterAgent(name string, agent adk.TypedResumableAgent[M]) error {
	return e.Register(name, &AgentRegistration[M]{Agent: agent})
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

// RegisterObserver attaches launching-turn receivers to one foreground task.
func (e *Executor[M]) RegisterObserver(
	taskID string,
	receivers []agenttool.EventReceiver[*adk.TypedAgentEvent[M]],
	runOptions []adk.AgentRunOption,
	enableStreaming bool,
) error {
	if taskID == "" {
		return errors.New("backgroundtask/subagent: observer task id is required")
	}
	e.observersMu.Lock()
	defer e.observersMu.Unlock()
	if e.observers == nil {
		e.observers = make(map[string]*foregroundObserver[M])
	}
	if _, exists := e.observers[taskID]; exists {
		return backgroundtask.ErrAlreadyExists
	}
	e.observers[taskID] = &foregroundObserver[M]{
		active: true, receivers: append([]agenttool.EventReceiver[*adk.TypedAgentEvent[M]](nil), receivers...),
		runOptions:      append([]adk.AgentRunOption(nil), runOptions...),
		enableStreaming: enableStreaming,
	}
	return nil
}

// DeactivateObserver prevents future receiver calls and removes the registry entry.
func (e *Executor[M]) DeactivateObserver(taskID string) {
	e.observersMu.Lock()
	observer := e.observers[taskID]
	delete(e.observers, taskID)
	e.observersMu.Unlock()
	if observer == nil {
		return
	}
	observer.mu.Lock()
	observer.active = false
	observer.receivers = nil
	observer.runOptions = nil
	observer.mu.Unlock()
}

func (e *Executor[M]) resolveObserver(taskID string) *foregroundObserver[M] {
	e.observersMu.Lock()
	observer := e.observers[taskID]
	e.observersMu.Unlock()
	return observer
}

func (o *foregroundObserver[M]) options() ([]adk.AgentRunOption, bool) {
	if o == nil {
		return nil, false
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if !o.active {
		return nil, false
	}
	return append([]adk.AgentRunOption(nil), o.runOptions...), o.enableStreaming
}

func (o *foregroundObserver[M]) forward(
	event *adk.TypedAgentEvent[M],
	backgrounded <-chan struct{},
) {
	if o == nil || signalClosed(backgrounded) {
		return
	}
	o.mu.Lock()
	if !o.active || signalClosed(backgrounded) {
		o.mu.Unlock()
		return
	}
	receivers := append([]agenttool.EventReceiver[*adk.TypedAgentEvent[M]](nil), o.receivers...)
	o.mu.Unlock()
	for _, receiver := range receivers {
		receiver(copyMaterializedEvent(event))
	}
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

func signalClosed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}

type eventOutputWriter[M adk.MessageType] struct {
	ctx     context.Context
	writer  io.WriteCloser
	format  EventFormat[M]
	runtime backgroundtask.ExecutionRuntime
	failed  bool
}

func openEventOutput[M adk.MessageType](
	ctx context.Context,
	task *backgroundtask.Task,
	registration *AgentRegistration[M],
	runtime backgroundtask.ExecutionRuntime,
) (*eventOutputWriter[M], error) {
	if task.Spec.OutputFile == "" {
		return nil, nil
	}
	writer, err := registration.OutputStore.OpenAppend(
		ctx, &filesystem.OpenAppendRequest{FilePath: task.Spec.OutputFile},
	)
	output := &eventOutputWriter[M]{
		ctx: ctx, writer: writer, format: registration.EventFormat, runtime: runtime,
	}
	if err != nil {
		output.writer = nil
		if reportErr := output.fail(err); reportErr != nil {
			return nil, reportErr
		}
	}
	return output, nil
}

func (w *eventOutputWriter[M]) write(event *adk.TypedAgentEvent[M]) error {
	if w == nil || w.failed || w.writer == nil {
		return nil
	}
	if w.format == nil {
		return w.fail(errors.New("backgroundtask/subagent: event format is required for output file"))
	}
	line, err := w.format(w.ctx, event)
	if err != nil {
		return w.fail(fmt.Errorf("encode agent output event: %w", err))
	}
	if line == "" {
		return nil
	}
	data := line + "\n"
	n, err := io.WriteString(w.writer, data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err != nil {
		return w.fail(fmt.Errorf("write agent output event: %w", err))
	}
	return nil
}

func (w *eventOutputWriter[M]) close() error {
	if w == nil || w.writer == nil {
		return nil
	}
	err := w.writer.Close()
	w.writer = nil
	if err != nil && !w.failed {
		return w.fail(fmt.Errorf("close agent output file: %w", err))
	}
	return nil
}

func (w *eventOutputWriter[M]) fail(err error) error {
	if w == nil || w.failed {
		return nil
	}
	w.failed = true
	if reportErr := w.runtime.ReportOutputFailure(w.ctx, err.Error()); reportErr != nil {
		return fmt.Errorf("report output failure after %v: %w", err, reportErr)
	}
	return nil
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
func (e *Executor[M]) ValidateExecution(ctx context.Context, task *backgroundtask.Task) error {
	if task == nil {
		return errors.New("backgroundtask/subagent: task is required")
	}
	environment, ok := adk.TypedRunnerEnvironmentFromContext[M](ctx)
	if !ok || environment.SessionID() == "" ||
		environment.SessionStore() == nil || environment.CheckPointStore() == nil {
		return ErrRunnerEnvironmentRequired
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return err
	}
	registration, err := e.resolveRegistration(payload.SubAgentName)
	if err != nil {
		return err
	}
	if task.Spec.OutputFile != "" && registration.OutputStore == nil {
		return errors.New("backgroundtask/subagent: output store is required for persisted output file")
	}
	return nil
}

func (e *Executor[M]) SupportsDrain() bool { return true }

func validateSpecPayload(spec backgroundtask.Spec) (*TaskPayload, error) {
	if spec.ExecutorKey != ExecutorKey || spec.Kind != "subagent" {
		return nil, errors.New("backgroundtask/subagent: invalid executor key or task kind")
	}
	payload, err := decodePayload(spec)
	if err != nil {
		return nil, err
	}
	if payload.Version != payloadVersion {
		return nil, fmt.Errorf("%w: subagent payload version %d", backgroundtask.ErrUnsupportedPayloadVersion, payload.Version)
	}
	if payload.ChildSessionID == "" || payload.CheckpointID == "" ||
		payload.ChildSessionID != spec.ID+"/session" || payload.CheckpointID != spec.ID+"/checkpoint" {
		return nil, errors.New("backgroundtask/subagent: child identities must be persisted in the task namespace")
	}
	if payload.SubAgentName == "" || payload.Prompt == "" {
		return nil, errors.New("backgroundtask/subagent: subagent name and prompt are required")
	}
	if payload.ResumeMode != ResumeNativeInterrupt && payload.ResumeMode != ResumeNextTurn {
		return nil, errors.New("backgroundtask/subagent: unsupported resume mode")
	}
	return payload, nil
}

// ValidateCheckpoint verifies that checkpoint can resume the task described by spec.
func (e *Executor[M]) ValidateCheckpoint(
	_ context.Context,
	spec backgroundtask.Spec,
	checkpoint []byte,
) error {
	payload, err := validateSpecPayload(spec)
	if err != nil {
		return err
	}
	if len(checkpoint) == 0 {
		return errors.New("backgroundtask/subagent: compatible checkpoint is required")
	}
	var state checkpointState
	if err = json.Unmarshal(checkpoint, &state); err != nil ||
		state.CheckpointID == "" ||
		state.CheckpointID != payload.CheckpointID ||
		state.Mode != payload.ResumeMode ||
		state.AllowEmpty != payload.AllowEmptyResume ||
		state.Sequence <= 0 {
		return errors.New("backgroundtask/subagent: checkpoint state does not match task")
	}
	return nil
}

// ValidateResume validates and normalizes opaque resume data for a checkpoint.
func (e *Executor[M]) ValidateResume(
	ctx context.Context,
	spec backgroundtask.Spec,
	checkpoint []byte,
	resumeData []byte,
) ([]byte, error) {
	if err := e.ValidateSpec(spec); err != nil {
		return nil, err
	}
	if err := e.ValidateCheckpoint(ctx, spec, checkpoint); err != nil {
		return nil, err
	}
	var state checkpointState
	if err := json.Unmarshal(checkpoint, &state); err != nil {
		return nil, err
	}
	if len(resumeData) == 0 {
		if !state.AllowEmpty {
			return nil, errors.New("backgroundtask/subagent: this checkpoint requires targeted resume data")
		}
		return nil, nil
	}
	if state.Mode == ResumeNextTurn {
		if !utf8.Valid(resumeData) {
			return nil, errors.New("backgroundtask/subagent: next-turn input must be utf-8")
		}
		return append([]byte(nil), resumeData...), nil
	}
	var targets map[string]any
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
	if task.Attempt > 1 && len(task.Checkpoint) == 0 {
		return nil, errors.New("backgroundtask/subagent: task cannot restart without a checkpoint")
	}
	payload, err := decodePayload(task.Spec)
	if err != nil {
		return nil, err
	}
	registration, err := e.resolveRegistration(payload.SubAgentName)
	if err != nil {
		return nil, err
	}
	observer := e.resolveObserver(task.Spec.ID)
	runOptions, enableStreaming := observer.options()
	environment, ok := adk.TypedRunnerEnvironmentFromContext[M](ctx)
	if !ok {
		return nil, ErrRunnerEnvironmentRequired
	}
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{
		Agent: registration.Agent, EnableStreaming: enableStreaming,
		CheckPointStore: environment.CheckPointStore(),
		SessionID:       payload.ChildSessionID, SessionStore: environment.SessionStore(),
		SessionConfig: environment.SessionConfig(),
	})
	output, err := openEventOutput(ctx, task, registration, runtime)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := output.close(); closeErr != nil && err == nil {
			result = nil
			err = closeErr
		}
	}()
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
			return e.handleEventError(ctx, iter, task, payload, controlRequests, event.Err)
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
		if writeErr := output.write(materialized); writeErr != nil {
			return nil, writeErr
		}
		observer.forward(materialized, runtime.Backgrounded())
	}
	if controlResult, controlErr, controlled := e.controlResult(
		ctx, task, payload, pollControl(controlRequests),
	); controlled {
		return controlResult, controlErr
	}
	if interrupted != nil {
		return e.interruptResult(ctx, task, payload, interrupted)
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
	payload *TaskPayload,
	options ...adk.AgentRunOption,
) (*adk.AsyncIterator[*adk.TypedAgentEvent[M]], error) {
	if len(task.Checkpoint) == 0 {
		queryOptions := append([]adk.AgentRunOption{adk.WithCheckPointID(payload.CheckpointID)}, options...)
		return runner.Query(
			ctx, payload.Prompt, queryOptions...,
		), nil
	}
	if payload.ResumeMode == ResumeNextTurn {
		return e.runNextTurn(ctx, runner, task, payload, options...)
	}
	if len(task.PendingResume) == 0 {
		return runner.Resume(ctx, payload.CheckpointID, options...)
	}
	var targets map[string]any
	if err := json.Unmarshal(task.PendingResume, &targets); err != nil {
		return nil, err
	}
	return runner.ResumeWithParams(
		ctx, payload.CheckpointID, &adk.ResumeParams{Targets: targets}, options...,
	)
}

func (e *Executor[M]) handleEventError(
	ctx context.Context,
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]],
	task *backgroundtask.Task,
	payload *TaskPayload,
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
	if result, controlErr, controlled := e.controlResult(ctx, task, payload, control); controlled {
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
	payload *TaskPayload,
	interrupted *adk.InterruptInfo,
) (*backgroundtask.ExecutionResult, error) {
	environment, ok := adk.TypedRunnerEnvironmentFromContext[M](ctx)
	if !ok {
		return nil, ErrRunnerEnvironmentRequired
	}
	if _, exists, err := environment.CheckPointStore().Get(ctx, payload.CheckpointID); err != nil || !exists {
		if err == nil {
			err = errors.New("backgroundtask/subagent: runner checkpoint is missing")
		}
		return nil, err
	}
	state := checkpointState{
		CheckpointID: payload.CheckpointID, Mode: payload.ResumeMode,
		AllowEmpty: payload.AllowEmptyResume, Sequence: nextCheckpointSequence(task.Checkpoint),
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
	ctx context.Context,
	task *backgroundtask.Task,
	payload *TaskPayload,
	control backgroundtask.ControlRequest,
) (*backgroundtask.ExecutionResult, error, bool) {
	switch control.Kind {
	case backgroundtask.ControlStop:
		return &backgroundtask.ExecutionResult{
			Status: backgroundtask.StatusCanceled,
			Error:  "canceled",
		}, nil, true
	case backgroundtask.ControlDrain:
		environment, ok := adk.TypedRunnerEnvironmentFromContext[M](ctx)
		if !ok {
			return nil, ErrRunnerEnvironmentRequired, true
		}
		if _, exists, err := environment.CheckPointStore().Get(context.Background(), payload.CheckpointID); err != nil || !exists {
			if err == nil {
				err = errors.New("runner checkpoint is missing")
			}
			return nil, fmt.Errorf("%w: %v", backgroundtask.ErrCheckpointUnavailable, err), true
		}
		stateBytes, err := json.Marshal(checkpointState{
			CheckpointID: payload.CheckpointID, Mode: payload.ResumeMode,
			AllowEmpty: payload.AllowEmptyResume, Sequence: nextCheckpointSequence(task.Checkpoint),
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

const resumeMarkerKey = "eino.dev/backgroundtask_resume"

func (e *Executor[M]) runNextTurn(
	ctx context.Context,
	runner *adk.TypedRunner[M],
	task *backgroundtask.Task,
	payload *TaskPayload,
	options ...adk.AgentRunOption,
) (*adk.AsyncIterator[*adk.TypedAgentEvent[M]], error) {
	var state checkpointState
	if err := json.Unmarshal(task.Checkpoint, &state); err != nil {
		return nil, err
	}
	marker := fmt.Sprintf("%s:%d", task.Spec.ID, state.Sequence)
	seen, err := e.hasResumeMarker(ctx, payload.ChildSessionID, marker)
	if err != nil {
		return nil, err
	}
	var messages []M
	if !seen {
		var data []byte
		data = task.PendingResume
		message, messageErr := resumeMessage[M](string(data), marker)
		if messageErr != nil {
			return nil, messageErr
		}
		messages = []M{message}
	}
	options = append(options, adk.WithCheckPointID(payload.CheckpointID))
	return runner.Run(ctx, messages, options...), nil
}

func (e *Executor[M]) hasResumeMarker(ctx context.Context, sessionID, marker string) (bool, error) {
	environment, ok := adk.TypedRunnerEnvironmentFromContext[M](ctx)
	if !ok {
		return false, ErrRunnerEnvironmentRequired
	}
	after := ""
	for {
		page, err := environment.SessionStore().LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
			After: after, Limit: 100,
		})
		if err != nil {
			return false, err
		}
		for _, event := range page.Events {
			if event != nil && messageResumeMarker(event.Message) == marker {
				return true, nil
			}
		}
		if page.Next == "" || page.Next == after || len(page.Events) == 0 {
			return false, nil
		}
		after = page.Next
	}
}

func resumeMessage[M adk.MessageType](content, marker string) (M, error) {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		message := schema.UserMessage(content)
		message.Extra = map[string]any{resumeMarkerKey: marker}
		return any(message).(M), nil
	case *schema.AgenticMessage:
		message := schema.UserAgenticMessage(content)
		message.Extra = map[string]any{resumeMarkerKey: marker}
		return any(message).(M), nil
	default:
		return zero, errors.New("backgroundtask/subagent: unsupported message type")
	}
}

func messageResumeMarker[M adk.MessageType](message M) string {
	switch typed := any(message).(type) {
	case *schema.Message:
		if typed != nil {
			value, _ := typed.Extra[resumeMarkerKey].(string)
			return value
		}
	case *schema.AgenticMessage:
		if typed != nil {
			value, _ := typed.Extra[resumeMarkerKey].(string)
			return value
		}
	}
	return ""
}

func decodePayload(spec backgroundtask.Spec) (*TaskPayload, error) {
	var payload TaskPayload
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
	TaskID           string
	SubAgentName     string
	Prompt           string
	Description      string
	SessionID        string
	OutputFile       string
	ResumeMode       ResumeMode
	AllowEmptyResume bool
}

// Submit persists a durable sub-agent task through manager.
func Submit(ctx context.Context, manager *backgroundtask.Manager, req *SubmitRequest) (*backgroundtask.Task, error) {
	if manager == nil || req == nil || req.SessionID == "" ||
		req.SubAgentName == "" || req.Prompt == "" {
		return nil, errors.New("backgroundtask/subagent: manager, parent session, subagent name, and prompt are required")
	}
	id := req.TaskID
	if id == "" {
		var err error
		id, err = manager.AllocateTaskID(ctx, &backgroundtask.AllocateTaskIDRequest{Kind: "subagent"})
		if err != nil {
			return nil, err
		}
	}
	payload := TaskPayload{
		Version: payloadVersion, SubAgentName: req.SubAgentName, Prompt: req.Prompt,
		ChildSessionID: id + "/session", CheckpointID: id + "/checkpoint",
		ResumeMode: req.ResumeMode, AllowEmptyResume: req.AllowEmptyResume,
	}
	if payload.ResumeMode == "" {
		payload.ResumeMode = ResumeNativeInterrupt
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return manager.Submit(ctx, backgroundtask.Spec{
		ID: id, ExecutorKey: ExecutorKey, Kind: "subagent", Payload: data,
		Description: req.Description, OutputFile: req.OutputFile,
		LeaseExpiryPolicy: backgroundtask.LeaseExpiryRetry, SessionID: req.SessionID,
		Notify: &backgroundtask.NotificationTarget{Kind: "session_inbox", TargetID: req.SessionID},
	})
}
