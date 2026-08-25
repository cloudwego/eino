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
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	foregroundcoord "github.com/cloudwego/eino/adk/internal/foreground"
	"github.com/cloudwego/eino/schema"
)

const (
	// ExecutorKey is the backgroundtask executor key for durable sub-agent tasks.
	ExecutorKey = "eino.dev/subagent"

	payloadVersion          = 4
	maxChildSessionIDLength = 1024
	taskIDEventExtraKey     = "eino.background_task.id"
)

type taskContextKey struct{}

// TaskContext describes the durable task currently executing a sub-agent. The
// child session ID is opaque; use TaskID for task-owned metadata lookups.
type TaskContext struct {
	TaskID          string
	ParentSessionID string
	SubAgentName    string
	ChildSessionID  string
	Attempt         int64
}

// TaskContextFromContext returns durable sub-agent task metadata for the current
// execution attempt.
func TaskContextFromContext(ctx context.Context) (TaskContext, bool) {
	taskCtx, ok := ctx.Value(taskContextKey{}).(TaskContext)
	return taskCtx, ok && taskCtx.TaskID != "" && taskCtx.ChildSessionID != ""
}

func contextWithTaskContext(ctx context.Context, taskCtx TaskContext) context.Context {
	return context.WithValue(ctx, taskContextKey{}, taskCtx)
}

type taskPayload struct {
	Version        int                   `json:"version"`
	SubAgentName   string                `json:"subagent_name"`
	Input          *serializedTypedInput `json:"input,omitempty"`
	ChildSessionID string                `json:"child_session_id"`
}

type serializedTypedInput struct {
	Messages        json.RawMessage `json:"messages"`
	EnableStreaming bool            `json:"enable_streaming,omitempty"`
}

type checkpointState struct {
	TargetIDs []string `json:"target_ids,omitempty"`
	Sequence  int64    `json:"sequence"`
}

// RunOptionsFactory reconstructs deployment-owned run options for each task
// attempt. It may be called concurrently, must return fresh option values, and
// must not panic. Every worker serving the same registered agent name must
// configure a semantically equivalent factory for the full task lifetime. An
// error fails that attempt before agent execution.
type RunOptionsFactory func() ([]adk.AgentRunOption, error)

// AgentRegistration binds a persisted name to worker-local dependencies. Every worker
// eligible to execute that name must provide a semantically equivalent registration for
// the full lifetime of its resumable tasks. Incompatible Agent or run-option changes
// require draining existing tasks or registering a new agent name.
type AgentRegistration[M adk.MessageType] struct {
	Agent             adk.TypedResumableAgent[M]
	RunOptionsFactory RunOptionsFactory
}

// SessionStoreFactory constructs the child Session store for one task access.
// It is called for execution attempts and progress reads. Durable providers use
// task ID and attempt to bind append authorization to the active task lease
// while retaining read access after the attempt ends. It may be called
// concurrently and must return a fresh, semantically equivalent store on every
// call.
type SessionStoreFactory[M adk.MessageType] func(
	context.Context,
	*backgroundtask.Task,
) (adk.SessionEventStore[M], error)

// ExecutorConfig provides the durable session dependencies shared by every
// sub-agent task executed by an Executor.
type ExecutorConfig[M adk.MessageType] struct {
	// SessionStore persists child events without attempt-specific construction.
	// It is retained for providers whose store performs fencing by another
	// mechanism. Persistent child sessions may be used by multiple task IDs, so
	// production providers must serialize concurrent turns for one session
	// across workers. Configure exactly one of SessionStore and
	// SessionStoreFactory.
	SessionStore adk.SessionEventStore[M]
	// SessionStoreFactory constructs a task-bound child event store.
	// Stores returned for tasks sharing a ChildSessionID must coordinate the
	// same durable session and serialize concurrent turns across workers.
	SessionStoreFactory SessionStoreFactory[M]
	// CheckPointStore persists ADK Runner checkpoints for interruption and recovery.
	CheckPointStore adk.CheckPointStore
	// SessionConfig optionally customizes child-session persistence.
	SessionConfig *adk.SessionConfig[M]
	// DrainCancelTimeout bounds safe-point cancellation during ControlDrain. A
	// positive value escalates the cancellation to checkpointable immediate
	// cancellation when the safe point is not reached before the timeout.
	// Zero preserves unbounded safe-point cancellation.
	DrainCancelTimeout time.Duration
}

// Executor runs durable sub-agent tasks through ADK Runner checkpointing.
type Executor[M adk.MessageType] struct {
	sessionStore        adk.SessionEventStore[M]
	sessionStoreFactory SessionStoreFactory[M]
	checkPointStore     adk.CheckPointStore
	sessionConfig       *adk.SessionConfig[M]
	drainCancelTimeout  time.Duration

	mu            sync.RWMutex
	registrations map[string]*AgentRegistration[M]
}

// NewExecutor constructs a durable sub-agent executor with explicit session
// and checkpoint dependencies.
func NewExecutor[M adk.MessageType](config *ExecutorConfig[M]) (*Executor[M], error) {
	if config == nil || config.CheckPointStore == nil ||
		(config.SessionStore == nil) == (config.SessionStoreFactory == nil) {
		return nil, errors.New(
			"backgroundtask/subagent: exactly one session store or factory and a checkpoint store are required",
		)
	}
	var sessionConfig *adk.SessionConfig[M]
	if config.SessionConfig != nil {
		copy := *config.SessionConfig
		sessionConfig = &copy
	}
	return &Executor[M]{
		sessionStore:        config.SessionStore,
		sessionStoreFactory: config.SessionStoreFactory,
		checkPointStore:     config.CheckPointStore,
		sessionConfig:       sessionConfig,
		drainCancelTimeout:  config.DrainCancelTimeout,
	}, nil
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
	if _, err = decodeTypedInput[M](payload.Input); err != nil {
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
	if e == nil || (e.sessionStore == nil && e.sessionStoreFactory == nil) ||
		e.checkPointStore == nil {
		return errors.New("backgroundtask/subagent: executor dependencies are unavailable")
	}
	return e.ValidateSpec(task.Spec)
}

// SupportsDrain reports true because sub-agent drain captures an ADK Runner
// checkpoint before returning a suspended result.
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
	if payload.SubAgentName == "" || payload.ChildSessionID == "" {
		return nil, errors.New(
			"backgroundtask/subagent: subagent name and child session id are required",
		)
	}
	if payload.Input == nil {
		return nil, errors.New("backgroundtask/subagent: typed input is required")
	}
	if len(payload.ChildSessionID) > maxChildSessionIDLength {
		return nil, errors.New(
			"backgroundtask/subagent: child session id exceeds configured bounds",
		)
	}
	return payload, nil
}

func defaultChildSessionID() (string, error) {
	var entropy [8]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", err
	}
	value := binary.BigEndian.Uint64(entropy[:]) & ((uint64(1) << 63) - 1)
	if value == 0 {
		value = 1
	}
	return strconv.FormatInt(int64(value), 10), nil
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
	var initialInput *adk.TypedAgentInput[M]
	if len(task.Checkpoint) == 0 {
		initialInput, err = decodeTypedInput[M](payload.Input)
		if err != nil {
			return nil, err
		}
	}
	sessionStore := e.sessionStore
	if e.sessionStoreFactory != nil {
		sessionStore, err = e.sessionStoreFactory(ctx, task)
		if err != nil {
			return nil, fmt.Errorf(
				"backgroundtask/subagent: construct attempt session store: %w",
				err,
			)
		}
		if sessionStore == nil {
			return nil, errors.New(
				"backgroundtask/subagent: attempt session store factory returned nil",
			)
		}
	}
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{
		Agent: registration.Agent,
		EnableStreaming: foreground.EnableStreaming() ||
			(initialInput != nil && initialInput.EnableStreaming),
		CheckPointStore: e.checkPointStore,
		SessionID:       payload.ChildSessionID, SessionStore: sessionStore,
		SessionConfig: e.sessionConfigForTask(task.Spec.ID),
	})
	ctx = contextWithTaskContext(ctx, TaskContext{
		TaskID:          task.Spec.ID,
		ParentSessionID: task.Spec.SessionID,
		SubAgentName:    payload.SubAgentName,
		ChildSessionID:  payload.ChildSessionID,
		Attempt:         task.Attempt,
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
				if e.drainCancelTimeout > 0 {
					cancelOptions = append(cancelOptions,
						adk.WithAgentCancelTimeout(e.drainCancelTimeout))
				}
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
	iter, err := e.beginRun(ctx, runner, task, initialInput, runOptions...)
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
				var retryErr *adk.WillRetryError
				if errors.As(messageErr, &retryErr) {
					continue
				}
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

func (e *Executor[M]) sessionConfigForTask(taskID string) *adk.SessionConfig[M] {
	config := &adk.SessionConfig[M]{}
	if e.sessionConfig != nil {
		*config = *e.sessionConfig
	}
	base := config.EventExtraProvider
	config.EventExtraProvider = func(
		ctx context.Context,
		event *adk.SessionEvent[M],
	) (map[string]any, error) {
		var extra map[string]any
		if base != nil {
			var err error
			extra, err = base(ctx, event)
			if err != nil {
				return nil, err
			}
		}
		result := make(map[string]any, len(extra)+1)
		for key, value := range extra {
			result[key] = value
		}
		result[taskIDEventExtraKey] = taskID
		return result, nil
	}
	return config
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
	initialInput *adk.TypedAgentInput[M],
	options ...adk.AgentRunOption,
) (*adk.AsyncIterator[*adk.TypedAgentEvent[M]], error) {
	id := checkpointID(task.Spec.ID)
	if len(task.Checkpoint) == 0 {
		runOptions := append([]adk.AgentRunOption(nil), options...)
		runOptions = append(runOptions, adk.WithCheckPointID(id))
		if initialInput == nil {
			return nil, errors.New("backgroundtask/subagent: typed input is required")
		}
		return runner.Run(ctx, initialInput.Messages, runOptions...), nil
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
	if errors.Is(err, adk.ErrSessionBusy) {
		return &backgroundtask.ExecutionResult{
			Directive:  backgroundtask.ExecutionDirectiveYield,
			Checkpoint: append([]byte(nil), task.Checkpoint...),
		}, nil
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

func encodeTypedInput[M adk.MessageType](
	input *adk.TypedAgentInput[M],
) (*serializedTypedInput, error) {
	if err := validateTypedInput(input); err != nil {
		return nil, err
	}
	serializer := &schema.HumanReadableSerializer{}
	messages, err := serializer.Marshal(input.Messages)
	if err != nil {
		return nil, fmt.Errorf("backgroundtask/subagent: serialize typed input: %w", err)
	}
	encoded := &serializedTypedInput{
		Messages:        append(json.RawMessage(nil), messages...),
		EnableStreaming: input.EnableStreaming,
	}
	if _, err = decodeTypedInput[M](encoded); err != nil {
		return nil, err
	}
	return encoded, nil
}

func decodeTypedInput[M adk.MessageType](
	encoded *serializedTypedInput,
) (*adk.TypedAgentInput[M], error) {
	if encoded == nil || len(encoded.Messages) == 0 {
		return nil, errors.New("backgroundtask/subagent: typed input is required")
	}
	var decoded any
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(
		encoded.Messages,
		&decoded,
	); err != nil {
		return nil, fmt.Errorf("backgroundtask/subagent: deserialize typed input: %w", err)
	}
	messages, ok := decoded.([]M)
	if !ok {
		return nil, errors.New(
			"backgroundtask/subagent: typed input message type does not match executor",
		)
	}
	input := &adk.TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: encoded.EnableStreaming,
	}
	if err := validateTypedInput(input); err != nil {
		return nil, err
	}
	return input, nil
}

func validateTypedInput[M adk.MessageType](input *adk.TypedAgentInput[M]) error {
	if input == nil || len(input.Messages) == 0 {
		return errors.New("backgroundtask/subagent: typed input messages are required")
	}
	var zero M
	for _, message := range input.Messages {
		if any(message) == any(zero) {
			return errors.New("backgroundtask/subagent: typed input contains a nil message")
		}
	}
	return nil
}

func nextCheckpointSequence(previous []byte) int64 {
	var state checkpointState
	if len(previous) == 0 || json.Unmarshal(previous, &state) != nil || state.Sequence < 1 {
		return 1
	}
	return state.Sequence + 1
}

// SubmitRequest describes a durable sub-agent task. Input must be non-nil and
// contain at least one non-nil message. Eino serializes it before persistence;
// concrete values stored in interface fields must be registered with schema.
// Empty TaskID asks Manager to allocate one. SessionID identifies the parent
// session notified when the child waits for input or terminates. Empty
// ChildSessionID creates a new opaque child session when empty; a non-empty
// value is used as-is and continues that existing child session with its
// committed history. DisableLifecycleNotifications suppresses automatic waiting
// and terminal notifications without suppressing TaskCreated recovery.
type SubmitRequest[M adk.MessageType] struct {
	TaskID                        string
	SubAgentName                  string
	Input                         *adk.TypedAgentInput[M]
	Description                   string
	SessionID                     string
	ChildSessionID                string
	DisableLifecycleNotifications bool
	InitialCheckpoint             []byte
}

// Submit serializes and persists a durable sub-agent task through manager. If
// the returned error wraps backgroundtask.ErrTaskCreatedEventUndelivered and
// task is non-nil, durable ownership has transferred and callers must not retry
// Submit.
func Submit[M adk.MessageType](
	ctx context.Context,
	manager *backgroundtask.Manager,
	req *SubmitRequest[M],
) (*backgroundtask.Task, error) {
	if manager == nil || req == nil || req.SessionID == "" || req.SubAgentName == "" {
		return nil, errors.New(
			"backgroundtask/subagent: manager, parent session, and subagent name are required",
		)
	}
	input, err := encodeTypedInput(req.Input)
	if err != nil {
		return nil, err
	}
	id := req.TaskID
	if id == "" {
		id, err = manager.AllocateTaskID(
			ctx,
			&backgroundtask.AllocateTaskIDRequest{Kind: "subagent"},
		)
		if err != nil {
			return nil, err
		}
	}
	payload := &taskPayload{
		Version: payloadVersion, SubAgentName: req.SubAgentName, Input: input,
	}
	payload.ChildSessionID = req.ChildSessionID
	if payload.ChildSessionID == "" {
		payload.ChildSessionID, err = defaultChildSessionID()
		if err != nil {
			return nil, err
		}
	}
	if len(payload.ChildSessionID) > maxChildSessionIDLength {
		return nil, errors.New(
			"backgroundtask/subagent: child session id exceeds configured bounds",
		)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return manager.Submit(ctx, &backgroundtask.SubmitRequest{
		Spec: backgroundtask.Spec{
			ID: id, ExecutorKey: ExecutorKey, Kind: "subagent", Payload: data,
			Description: req.Description, SessionID: req.SessionID,
			NotifySession: !req.DisableLifecycleNotifications,
		},
		InitialCheckpoint: append([]byte(nil), req.InitialCheckpoint...),
	})
}

// ChildSessionIDFromTask returns the persistent child session owned by a
// durable sub-agent task.
func ChildSessionIDFromTask(task *backgroundtask.Task) (string, error) {
	if task == nil {
		return "", errors.New("backgroundtask/subagent: task is required")
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return "", err
	}
	return payload.ChildSessionID, nil
}
