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

package subagent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/cloudwego/eino/adk"
	adkinternal "github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

const (
	runtimeMetadataVersion         = 2
	legacyRuntimeCheckpointVersion = 1
	runtimeCheckpointVersion       = 2
	legacyForegroundResultVersion  = 1
	foregroundResultVersion        = 2
	foregroundResultTerminal       = "terminal"
	foregroundResultInvalidated    = "invalidated"
	initialSignalKind              = "eino.subagent.initial"
	messageInputKind               = "eino.subagent.message"
	runtimeCheckpointIdle          = "idle"
	runtimeCheckpointResume        = "runner_interrupt"
	runtimeCheckpointMagic         = "EINO"
	runtimeCheckpointModeIdle      = byte(0)
	runtimeCheckpointModeResume    = byte(1)
	// Keep sparse acknowledgement metadata comfortably below the default 1 MiB
	// lifecycle checkpoint limit while allowing 128 default-size input batches.
	maxSparseAcks = 4096
)

var childSessionNamespace = uuid.MustParse("a78763c0-a953-40d6-a88e-a9ca706cb88f")

// InputsToAgentInput converts durable non-resume inputs into one child-agent turn.
type InputsToAgentInput[M adk.MessageType] func(
	context.Context,
	[]*task.InputRecord,
) (*adk.TypedAgentInput[M], error)

// ControllerConfig configures a durable TurnLoop-backed sub-agent runtime.
type ControllerConfig[M adk.MessageType] struct {
	// Manager owns background lifecycle and the shared durable mailbox.
	Manager *background.Manager

	// Barrier decides whether a completed turn ends or waits for another signal.
	Barrier CompletionBarrier[M]
	// InputsToAgentInput maps durable application inputs to child-agent input.
	InputsToAgentInput InputsToAgentInput[M]
	// CancellationHook performs optional business-owned cancellation side effects.
	CancellationHook CancellationHook
	// InputPreemptPolicy constrains durable preempt intents. Nil uses AnySafePoint.
	InputPreemptPolicy InputPreemptPolicy[M]

	// Configure exactly one of SessionStore and SessionStoreFactory.
	SessionStore adk.SessionEventStore[M]
	// SessionStoreFactory may enforce deployment-specific child-session access
	// and fencing from AccessMode and the direct ParentSessionID. Task is nil
	// only for foreground execution. The factory must return stores backed by
	// the same durable session data on every worker.
	SessionStoreFactory RuntimeSessionStoreFactory[M]
	// CheckPointStore persists TurnLoop and Runner recovery state.
	CheckPointStore adk.CheckPointStore
	// SessionConfig customizes child-session persistence and is copied at construction.
	SessionConfig *adk.SessionConfig[M]
	// InputBatchSize bounds one durable mailbox read. Non-positive values use 32.
	InputBatchSize int
	// DrainCancelTimeout bounds graceful stop during ControlDrain. A positive
	// value escalates to checkpointed immediate cancellation when no safe point
	// is reached before the timeout. Zero preserves an unbounded graceful stop.
	DrainCancelTimeout time.Duration
}

// Controller executes sub-agent turns from a durable task mailbox.
type Controller[M adk.MessageType] struct {
	manager  *background.Manager
	executor *executor[M]

	barrier            CompletionBarrier[M]
	inputsToAgentInput InputsToAgentInput[M]
	cancellationHook   CancellationHook
	preemptPolicy      InputPreemptPolicy[M]

	sessionStore        adk.SessionEventStore[M]
	sessionStoreFactory RuntimeSessionStoreFactory[M]
	checkPointStore     adk.CheckPointStore
	sessionConfig       *adk.SessionConfig[M]
	inputBatchSize      int
	drainCancelTimeout  time.Duration

	activeMu sync.Mutex
	active   map[string]*activeRun[M]
}

type activeRun[M adk.MessageType] struct {
	handle Handle
	cancel context.CancelFunc
	done   chan struct{}

	result *Result[M]
	err    error
}

type runtimeMetadata struct {
	Version         int            `json:"version"`
	ParentSessionID string         `json:"parent_session_id"`
	RootSessionID   string         `json:"root_session_id"`
	ParentTaskID    string         `json:"parent_task_id,omitempty"`
	ChildSessionID  string         `json:"child_session_id"`
	AgentName       string         `json:"agent_name"`
	Description     string         `json:"description,omitempty"`
	StartMode       task.StartMode `json:"start_mode"`
	InputHash       []byte         `json:"input_hash"`
}

type turnLoopCheckpoint struct {
	Version            int             `json:"version"`
	Mode               string          `json:"mode"`
	InputCursor        int64           `json:"input_cursor"`
	SparseAcks         []int64         `json:"sparse_acks,omitempty"`
	FinalMessage       json.RawMessage `json:"final_message,omitempty"`
	TargetIDs          []string        `json:"target_ids,omitempty"`
	TurnLoopCheckpoint []byte          `json:"turn_loop_checkpoint,omitempty"`
}

type decodedRuntimeCheckpoint[M adk.MessageType] struct {
	Version            int
	InputCursor        int64
	Final              M
	Mode               string
	TargetIDs          []string
	SparseAcks         []int64
	TurnLoopCheckpoint []byte
}

type foregroundResultCheckpoint struct {
	Version      int                `json:"version"`
	State        string             `json:"state,omitempty"`
	Status       task.OutcomeStatus `json:"status"`
	InputCursor  int64              `json:"input_cursor"`
	FinalMessage json.RawMessage    `json:"final_message,omitempty"`
	Error        string             `json:"error,omitempty"`
}

type activationResult[M adk.MessageType] struct {
	decision           CompletionAction
	final              M
	interrupted        *adk.InterruptInfo
	cursor             int64
	sparseAcks         []int64
	control            background.ControlRequest
	turnLoopCheckpoint []byte
}

type activationRequest[M adk.MessageType] struct {
	runID          string
	metadata       *runtimeMetadata
	task           *background.TaskSnapshot
	controlRuntime background.ExecutionRuntime
	signalRuntime  background.ExecutionRuntime
	attached       bool
	onEvent        func(*adk.TypedAgentEvent[M])
}

type captureCheckpointStore struct {
	mu         sync.Mutex
	checkpoint []byte
	exists     bool
	onSet      func(context.Context, []byte) error
}

func newCaptureCheckpointStore(
	checkpoint []byte,
	exists bool,
) *captureCheckpointStore {
	return &captureCheckpointStore{
		checkpoint: append([]byte(nil), checkpoint...),
		exists:     exists,
	}
}

func (s *captureCheckpointStore) Get(
	_ context.Context,
	_ string,
) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.checkpoint...), s.exists, nil
}

func (s *captureCheckpointStore) Set(
	ctx context.Context,
	_ string,
	checkpoint []byte,
) error {
	captured := append([]byte(nil), checkpoint...)
	s.mu.Lock()
	s.checkpoint = captured
	s.exists = true
	onSet := s.onSet
	s.mu.Unlock()
	if onSet != nil {
		return onSet(ctx, append([]byte(nil), captured...))
	}
	return nil
}

func (s *captureCheckpointStore) Delete(
	_ context.Context,
	_ string,
) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoint = nil
	s.exists = false
	return nil
}

func (s *captureCheckpointStore) snapshot() ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]byte(nil), s.checkpoint...), s.exists
}

func (s *captureCheckpointStore) setOnSet(
	onSet func(context.Context, []byte) error,
) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.onSet = onSet
}

// NewController constructs a durable TurnLoop-backed sub-agent runtime.
func NewController[M adk.MessageType](
	config *ControllerConfig[M],
) (*Controller[M], error) {
	if config == nil || config.Manager == nil || config.Barrier == nil ||
		config.InputsToAgentInput == nil || config.CheckPointStore == nil {
		return nil, errors.New(
			"task/subagent: turn loop runtime dependencies are required",
		)
	}
	if (config.SessionStore == nil) == (config.SessionStoreFactory == nil) {
		return nil, errors.New(
			"task/subagent: exactly one runtime session store or factory is required",
		)
	}
	batchSize := config.InputBatchSize
	if batchSize <= 0 {
		batchSize = 32
	}
	var sessionConfig *adk.SessionConfig[M]
	if config.SessionConfig != nil {
		copy := *config.SessionConfig
		sessionConfig = &copy
	}
	runtime := &Controller[M]{
		manager: config.Manager,
		barrier: config.Barrier, inputsToAgentInput: config.InputsToAgentInput,
		cancellationHook: config.CancellationHook, preemptPolicy: config.InputPreemptPolicy,
		sessionStore: config.SessionStore, sessionStoreFactory: config.SessionStoreFactory,
		checkPointStore: config.CheckPointStore, sessionConfig: sessionConfig,
		inputBatchSize: batchSize, drainCancelTimeout: config.DrainCancelTimeout,
		active: make(map[string]*activeRun[M]),
	}
	runtime.executor = newExecutor(runtime)
	actual, loaded, err := config.Manager.LoadOrRegisterExecutor(runtime.executor)
	if err != nil {
		return nil, fmt.Errorf("task/subagent: register runtime executor: %w", err)
	}
	if loaded || actual != runtime.executor {
		return nil, errors.New(
			"task/subagent: Manager already has a sub-agent runtime",
		)
	}
	return runtime, nil
}

// RegisterAgent binds a stable persisted name to one resumable agent.
func (r *Controller[M]) RegisterAgent(
	name string,
	registration *AgentRegistration[M],
) error {
	if r == nil || r.executor == nil {
		return errors.New("task/subagent: Controller is required")
	}
	return r.executor.register(name, registration)
}

// Manager returns the lifecycle manager shared by this Controller.
func (r *Controller[M]) Manager() *background.Manager {
	if r == nil {
		return nil
	}
	return r.manager
}

// Handle restores an operational handle for an existing task mailbox.
func (r *Controller[M]) Handle(
	ctx context.Context,
	taskID string,
) (*Handle, error) {
	if r == nil || r.manager == nil || taskID == "" {
		return nil, task.ErrMailboxNotFound
	}
	mailbox, err := r.manager.GetMailbox(ctx, taskID)
	if err != nil {
		return nil, err
	}
	metadata, err := decodeRuntimeMetadata(mailbox.Identity)
	if err != nil {
		return nil, err
	}
	return r.newHandle(mailbox.TaskID, metadata.ChildSessionID), nil
}

// Start reserves a stable run and starts attached or detached execution.
func (r *Controller[M]) Start(
	ctx context.Context,
	req *StartRequest[M],
) (*Handle, error) {
	if req == nil || req.InvocationID == "" || req.ParentSessionID == "" ||
		req.AgentName == "" || req.Input == nil || len(req.Input.Messages) == 0 {
		return nil, errors.New("task/subagent: runtime start request is incomplete")
	}
	if req.StartMode != task.StartModeForeground &&
		req.StartMode != task.StartModeBackground {
		return nil, errors.New("task/subagent: runtime start mode is invalid")
	}
	if len(req.ChildSessionID) > maxChildSessionIDLength {
		return nil, errors.New(
			"task/subagent: child session id exceeds configured bounds",
		)
	}
	if _, err := r.executor.resolveRegistration(req.AgentName); err != nil {
		return nil, err
	}
	input := *req.Input
	inputBytes, err := encodeTypedInput(&input)
	if err != nil {
		return nil, err
	}
	serializedInput, err := json.Marshal(inputBytes)
	if err != nil {
		return nil, err
	}
	childSessionID := req.ChildSessionID
	if childSessionID == "" {
		childSessionID = "subagent:" +
			uuid.NewSHA1(childSessionNamespace, []byte(req.InvocationID)).String()
	}
	inputHash, err := stableRuntimeInputHash(&input)
	if err != nil {
		return nil, err
	}
	parentTaskID := ""
	rootSessionID := req.ParentSessionID
	var parentExecution *task.ExecutionContext
	if execution, ok := task.ExecutionContextFromContext(ctx); ok {
		parentTaskID = execution.TaskID
		copy := execution
		parentExecution = &copy
		if execution.RootSessionID != "" {
			rootSessionID = execution.RootSessionID
		}
	}
	metadataBytes, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: req.ParentSessionID,
		RootSessionID: rootSessionID, ParentTaskID: parentTaskID,
		ChildSessionID: childSessionID, AgentName: req.AgentName,
		Description: req.Description, StartMode: req.StartMode,
		InputHash: inputHash,
	})
	if err != nil {
		return nil, err
	}
	candidate, err := r.manager.AllocateTaskID(
		ctx,
		&background.AllocateTaskIDRequest{Kind: "subagent_run"},
	)
	if err != nil {
		return nil, err
	}
	registerRequest := &task.RegisterMailboxRequest{
		CandidateTaskID: candidate, InvocationID: req.InvocationID,
		Identity: metadataBytes, ChildSessionID: childSessionID,
	}
	if parentExecution == nil {
		registerRequest.RootSessionID = rootSessionID
	} else {
		registerRequest.ParentExecution = parentExecution
	}
	reserved, err := r.manager.RegisterMailbox(
		ctx,
		registerRequest,
	)
	if err != nil {
		return nil, err
	}
	metadata, err := decodeRuntimeMetadata(reserved.Mailbox.Identity)
	if err != nil {
		return nil, err
	}
	handle := r.newHandle(reserved.Mailbox.TaskID, metadata.ChildSessionID)
	terminal, err := r.prepareStartMailbox(
		ctx, reserved, req.InvocationID, serializedInput,
	)
	if err != nil {
		return nil, err
	}
	if terminal {
		return handle, nil
	}
	if req.StartMode == task.StartModeBackground {
		if _, err = r.submitTask(ctx, handle, metadata, nil); err != nil {
			return nil, err
		}
		go func() { _ = r.manager.Execute(detachedRuntimeContext{parent: ctx}, handle.ID()) }()
		return handle, nil
	}

	r.activeMu.Lock()
	if current := r.active[handle.ID()]; current != nil {
		select {
		case <-current.done:
			if current.err == nil && current.result != nil &&
				current.result.Interrupted == nil {
				r.activeMu.Unlock()
				return handle, nil
			}
			delete(r.active, handle.ID())
		default:
			r.activeMu.Unlock()
			return handle, nil
		}
	}
	runCtx, cancel := context.WithCancel(ctx)
	active := &activeRun[M]{handle: *handle, cancel: cancel, done: make(chan struct{})}
	r.active[handle.ID()] = active
	r.activeMu.Unlock()
	go r.runAttached(runCtx, active, metadata, req)
	return handle, nil
}

func (r *Controller[M]) prepareStartMailbox(
	ctx context.Context,
	reserved *task.RegisterMailboxResult,
	invocationID string,
	serializedInput []byte,
) (bool, error) {
	if reserved.Created {
		if _, enqueueErr := r.manager.SendInput(ctx, &task.SendInputRequest{
			TaskID: reserved.Mailbox.TaskID,
			Input: task.Input{
				EventID: invocationID + ":initial",
				Kind:    initialSignalKind, Data: serializedInput,
			},
		}); enqueueErr != nil && !errors.Is(enqueueErr, task.ErrMailboxSealed) {
			return false, enqueueErr
		}
	} else {
		signals, listErr := r.manager.ListInputs(
			ctx,
			&task.ListInputsRequest{TaskID: reserved.Mailbox.TaskID, Limit: 1},
		)
		if listErr != nil {
			return false, listErr
		}
		if len(signals.Inputs) == 0 {
			if _, enqueueErr := r.manager.SendInput(ctx, &task.SendInputRequest{
				TaskID: reserved.Mailbox.TaskID,
				Input: task.Input{
					EventID: invocationID + ":initial",
					Kind:    initialSignalKind, Data: serializedInput,
				},
			}); enqueueErr != nil {
				return false, enqueueErr
			}
		}
	}
	if !reserved.Created && reserved.Mailbox.State == task.MailboxForeground {
		terminal, recoverErr := r.recoverForegroundCandidate(
			ctx, reserved.Mailbox,
		)
		if recoverErr != nil {
			return false, recoverErr
		}
		return terminal, nil
	}
	return false, nil
}

func (r *Controller[M]) newHandle(taskID, childSessionID string) *Handle {
	handle := &Handle{taskID: taskID, childSessionID: childSessionID}
	handle.sendInput = func(ctx context.Context, input *task.Input) error {
		return r.SendInput(ctx, taskID, input)
	}
	handle.wait = func(ctx context.Context) (*task.Outcome, error) {
		return r.waitOutcome(ctx, taskID)
	}
	handle.cancel = func(ctx context.Context, reason string) error {
		return r.cancelTask(ctx, taskID, reason)
	}
	return handle
}

func (r *Controller[M]) waitOutcome(
	ctx context.Context,
	taskID string,
) (*task.Outcome, error) {
	result, err := r.Wait(ctx, taskID)
	if err == nil {
		if result.Interrupted != nil {
			return &task.Outcome{Status: task.OutcomeInterrupted}, nil
		}
		data, encodeErr := encodeRuntimeMessage(result.FinalMessage)
		if encodeErr != nil {
			return nil, encodeErr
		}
		return &task.Outcome{Status: task.OutcomeCompleted, Data: data}, nil
	}
	if snapshot, getErr := r.manager.Get(ctx, taskID); getErr == nil {
		switch snapshot.Status {
		case background.StatusFailed:
			return &task.Outcome{
				Status: task.OutcomeFailed, Error: snapshot.ResultError,
			}, nil
		case background.StatusCanceled:
			return &task.Outcome{
				Status: task.OutcomeCanceled, Error: snapshot.ResultError,
			}, nil
		}
	}
	mailbox, mailboxErr := r.manager.GetMailbox(ctx, taskID)
	if mailboxErr == nil && mailbox.State == task.MailboxSealed {
		data, exists, checkpointErr := r.checkPointStore.Get(
			ctx,
			runtimeForegroundResultCheckpointID(taskID),
		)
		if checkpointErr == nil && exists {
			checkpoint, decodeErr := decodeForegroundResultCheckpoint(data)
			if decodeErr == nil && checkpoint.Error != "" {
				return &task.Outcome{
					Status: checkpoint.Status,
					Error:  checkpoint.Error,
				}, nil
			}
		}
	}
	return nil, err
}

// Continue sends input to the active finite task and explicitly releases it
// when suspended, or uses IfIdle to start a new task in the same persistent
// child session.
func (r *Controller[M]) Continue(
	ctx context.Context,
	req *ContinueRequest[M],
) (*Handle, error) {
	if req == nil || req.ChildSessionID == "" ||
		req.InvocationID == "" || req.Input == nil ||
		len(req.Input.Messages) == 0 {
		return nil, errors.New("task/subagent: continue request is incomplete")
	}
	encoded, err := encodeTypedInput(req.Input)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(encoded)
	if err != nil {
		return nil, err
	}

	for {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		active, lookupErr := r.manager.GetActiveMailboxBySession(
			ctx,
			req.ChildSessionID,
		)
		if lookupErr == nil {
			handle := r.newHandle(active.TaskID, req.ChildSessionID)
			if pushErr := handle.SendInput(ctx, &task.Input{
				EventID: req.InvocationID + ":send", Kind: messageInputKind,
				Data: data, Delivery: req.Delivery,
			}); pushErr != nil {
				if !errors.Is(pushErr, task.ErrMailboxSealed) &&
					!errors.Is(pushErr, task.ErrMailboxNotFound) {
					return nil, pushErr
				}
			} else {
				if releaseErr := r.releaseSuspended(ctx, handle.ID()); releaseErr != nil {
					return nil, releaseErr
				}
				return handle, nil
			}
			continue
		}
		if !errors.Is(lookupErr, task.ErrMailboxNotFound) {
			return nil, lookupErr
		}
		if req.IfIdle == nil {
			return nil, task.ErrMailboxNotFound
		}

		handle, startErr := r.Start(ctx, &StartRequest[M]{
			InvocationID:    req.InvocationID,
			ParentSessionID: req.IfIdle.ParentSessionID,
			ChildSessionID:  req.ChildSessionID,
			AgentName:       req.IfIdle.AgentName,
			Description:     req.IfIdle.Description,
			Input:           req.Input,
			StartMode:       req.IfIdle.StartMode,
			OnEvent:         req.IfIdle.OnEvent,
		})
		if errors.Is(startErr, task.ErrSessionBusy) {
			continue
		}
		return handle, startErr
	}
}

// SendInput durably appends one idempotent event and wakes a waiting run.
func (r *Controller[M]) SendInput(
	ctx context.Context,
	runID string,
	event *task.Input,
) error {
	if runID == "" || event == nil || event.EventID == "" || event.Kind == "" {
		return errors.New("task/subagent: runtime event identity is required")
	}
	_, err := r.manager.SendInput(ctx, &task.SendInputRequest{
		TaskID: runID,
		Input: task.Input{
			EventID: event.EventID, Kind: event.Kind,
			Data: append([]byte(nil), event.Data...), Delivery: event.Delivery,
		},
	})
	if err == nil {
		if backgroundTask, getErr := r.manager.Get(ctx, runID); getErr == nil &&
			backgroundTask.Status == background.StatusPending {
			go func() {
				_ = r.manager.Execute(detachedRuntimeContext{parent: ctx}, runID)
			}()
		}
	}
	return err
}

func (r *Controller[M]) releaseSuspended(ctx context.Context, runID string) error {
	current, err := r.manager.Get(ctx, runID)
	if errors.Is(err, background.ErrNotFound) {
		return nil
	}
	if err != nil || current.Status != background.StatusSuspended {
		return err
	}
	released, err := r.manager.ReleaseSuspension(ctx, runID)
	if errors.Is(err, background.ErrIllegalTransition) {
		return nil
	}
	if err != nil {
		return err
	}
	go func() {
		_ = r.manager.Execute(detachedRuntimeContext{parent: ctx}, released.Spec.ID)
	}()
	return nil
}

// Wait blocks for an attached result or a durable task terminal state.
func (r *Controller[M]) Wait(
	ctx context.Context,
	runID string,
) (*Result[M], error) {
	r.activeMu.Lock()
	active := r.active[runID]
	r.activeMu.Unlock()
	if active != nil {
		select {
		case <-active.done:
			result, err := active.result, active.err
			if result == nil || result.Interrupted == nil {
				r.activeMu.Lock()
				if r.active[runID] == active {
					delete(r.active, runID)
				}
				r.activeMu.Unlock()
			}
			return result, err
		case <-ctx.Done():
			cancelErr := r.Cancel(context.Background(), runID)
			return nil, joinErrors(ctx.Err(), cancelErr)
		}
	}
	return r.waitTask(ctx, runID)
}

// Cancel propagates parent cancellation to the run and its lifecycle hook.
func (r *Controller[M]) Cancel(ctx context.Context, runID string) error {
	return r.cancelTask(ctx, runID, "parent canceled sub-agent")
}

func (r *Controller[M]) cancelTask(
	ctx context.Context,
	runID, reason string,
) error {
	if reason == "" {
		reason = "parent canceled sub-agent"
	}
	r.activeMu.Lock()
	active := r.active[runID]
	r.activeMu.Unlock()
	backgroundTask, err := r.manager.Get(ctx, runID)
	if err == nil {
		if terminalTaskStatus(backgroundTask.Status) {
			return nil
		}
		_, err = r.manager.RequestCancel(
			ctx, runID, background.WithCancellationReason(reason),
		)
		if err == nil && active != nil {
			active.cancel()
		}
		return err
	}
	if !errors.Is(err, background.ErrNotFound) {
		if active != nil {
			active.cancel()
		}
		return err
	}
	if err = r.invokeCancelHook(ctx, runID, reason); err != nil {
		return err
	}
	if active != nil {
		active.cancel()
		return nil
	}
	return r.failAttached(ctx, runID, task.OutcomeCanceled, errors.New(reason))
}

func (r *Controller[M]) invokeCancelHook(
	ctx context.Context,
	runID, reason string,
) error {
	stream, streamErr := r.manager.GetMailbox(ctx, runID)
	if streamErr != nil {
		return streamErr
	}
	if stream.State == task.MailboxSealed {
		return nil
	}
	if r.cancellationHook == nil {
		return nil
	}
	metadata, streamErr := decodeRuntimeMetadata(stream.Identity)
	if streamErr != nil {
		return streamErr
	}
	return r.cancellationHook.OnCancel(ctx, runID, metadata.ChildSessionID, reason)
}

func (r *Controller[M]) runAttached(
	ctx context.Context,
	active *activeRun[M],
	metadata *runtimeMetadata,
	req *StartRequest[M],
) {
	defer close(active.done)
	result, checkpoint, err := r.runActivation(
		ctx,
		&activationRequest[M]{
			runID: active.handle.ID(), metadata: metadata,
			attached: true, onEvent: req.OnEvent,
		},
	)
	if err != nil {
		status := task.OutcomeFailed
		if errors.Is(err, context.Canceled) {
			status = task.OutcomeCanceled
		}
		if persistErr := r.failAttached(
			context.Background(), active.handle.ID(), status, err,
		); persistErr != nil {
			active.err = joinErrors(err, persistErr)
			return
		}
		active.err = err
		return
	}
	if result.interrupted != nil {
		active.result = &Result[M]{
			Handle: active.handle, FinalMessage: result.final,
			Interrupted: result.interrupted,
		}
		return
	}
	if result.decision == CompletionComplete {
		active.result = &Result[M]{
			Handle: active.handle, FinalMessage: result.final,
		}
		return
	}
	backgroundTask, err := r.submitTask(
		ctx, &active.handle, metadata, checkpoint,
	)
	if err != nil {
		if persistErr := r.failAttached(
			context.Background(), active.handle.ID(), task.OutcomeFailed, err,
		); persistErr != nil {
			active.err = joinErrors(err, persistErr)
			return
		}
		active.err = err
		return
	}
	if persisted, decodeErr := decodeRuntimeCheckpoint[M](
		backgroundTask.Checkpoint,
	); decodeErr == nil && persisted.Version == runtimeCheckpointVersion {
		deleteCheckpointBestEffort(
			ctx,
			r.checkPointStore,
			runtimeTurnLoopCheckpointID(active.handle.ID()),
		)
	}
	if ctx.Err() != nil {
		_, cancelErr := r.manager.RequestCancel(
			context.Background(),
			active.handle.ID(),
			background.WithCancellationReason("parent canceled sub-agent"),
		)
		active.err = joinErrors(ctx.Err(), cancelErr)
		return
	}
	if backgroundTask.Status == background.StatusPending {
		go func() {
			_ = r.manager.Execute(detachedRuntimeContext{parent: ctx}, active.handle.ID())
		}()
	}
	active.result, active.err = r.waitTask(ctx, active.handle.ID())
}

func (r *Controller[M]) waitTask(
	ctx context.Context,
	runID string,
) (*Result[M], error) {
	current, err := r.manager.Get(ctx, runID)
	if errors.Is(err, background.ErrNotFound) {
		mailbox, mailboxErr := r.manager.GetMailbox(ctx, runID)
		if mailboxErr != nil {
			return nil, mailboxErr
		}
		if mailbox.State == task.MailboxSealed {
			return r.recoverForegroundResult(ctx, mailbox)
		}
		return nil, errors.New("task/subagent: foreground task is not active in this process")
	}
	if err != nil {
		return nil, err
	}
	for {
		switch current.Status {
		case background.StatusCompleted:
			final, decodeErr := decodeRuntimeMessage[M](current.ResultData)
			if decodeErr != nil {
				return nil, decodeErr
			}
			mailbox, streamErr := r.manager.GetMailbox(ctx, runID)
			if streamErr != nil {
				return nil, streamErr
			}
			metadata, streamErr := decodeRuntimeMetadata(mailbox.Identity)
			if streamErr != nil {
				return nil, streamErr
			}
			return &Result[M]{
				Handle:       *r.newHandle(runID, metadata.ChildSessionID),
				FinalMessage: final,
			}, nil
		case background.StatusFailed, background.StatusCanceled:
			return nil, fmt.Errorf(
				"task/subagent: run %q %s: %s",
				runID, current.Status, current.ResultError,
			)
		}
		current, err = r.manager.WaitForTaskVersion(
			ctx,
			&background.WaitForTaskVersionRequest{
				TaskID: runID, AfterVersion: current.Version,
			},
		)
		if err != nil {
			return nil, err
		}
	}
}

func (r *Controller[M]) recoverForegroundResult(
	ctx context.Context,
	mailbox *task.Mailbox,
) (*Result[M], error) {
	if mailbox == nil || mailbox.State != task.MailboxSealed {
		return nil, errors.New("task/subagent: foreground mailbox is not sealed")
	}
	data, exists, err := r.checkPointStore.Get(
		ctx,
		runtimeForegroundResultCheckpointID(mailbox.TaskID),
	)
	if err != nil {
		return nil, err
	}
	if !exists {
		return nil, errors.New("task/subagent: sealed foreground result is unavailable")
	}
	checkpoint, err := decodeForegroundResultCheckpoint(data)
	if err != nil {
		return nil, err
	}
	if checkpoint.State == foregroundResultInvalidated {
		return nil, errors.New("task/subagent: sealed foreground result is unavailable")
	}
	if checkpoint.Error != "" {
		return nil, errors.New(checkpoint.Error)
	}
	final, err := decodeRuntimeMessage[M](checkpoint.FinalMessage)
	if err != nil {
		return nil, err
	}
	metadata, err := decodeRuntimeMetadata(mailbox.Identity)
	if err != nil {
		return nil, err
	}
	return &Result[M]{
		Handle:       *r.newHandle(mailbox.TaskID, metadata.ChildSessionID),
		FinalMessage: final,
	}, nil
}

func (r *Controller[M]) recoverForegroundCandidate(
	ctx context.Context,
	mailbox *task.Mailbox,
) (bool, error) {
	data, exists, err := r.checkPointStore.Get(
		ctx,
		runtimeForegroundResultCheckpointID(mailbox.TaskID),
	)
	if err != nil || !exists {
		return false, err
	}
	checkpoint, err := decodeForegroundResultCheckpoint(data)
	if err != nil {
		return false, err
	}
	if checkpoint.State == foregroundResultInvalidated {
		return false, nil
	}
	if checkpoint.InputCursor < mailbox.ConsumedCursor {
		return false, r.invalidateForegroundCandidate(ctx, mailbox.TaskID)
	}
	if checkpoint.InputCursor > mailbox.ConsumedCursor {
		return false, task.ErrCursorConflict
	}
	if checkpoint.Error != "" {
		_, err = r.manager.AbandonMailbox(ctx, &task.AbandonMailboxRequest{
			TaskID: mailbox.TaskID, ExpectedGeneration: mailbox.Generation,
		})
		return err == nil, err
	}
	_, err = r.manager.SealMailbox(ctx, &task.SealMailboxRequest{
		TaskID: mailbox.TaskID, ExpectedCursor: checkpoint.InputCursor,
		ExpectedGeneration: mailbox.Generation,
	})
	if errors.Is(err, task.ErrInputsPending) {
		return false, r.invalidateForegroundCandidate(ctx, mailbox.TaskID)
	}
	if errors.Is(err, task.ErrCursorConflict) {
		current, currentErr := r.manager.GetMailbox(ctx, mailbox.TaskID)
		if currentErr != nil {
			return false, currentErr
		}
		if current.State == task.MailboxForeground &&
			checkpoint.InputCursor < current.ConsumedCursor {
			return false, r.invalidateForegroundCandidate(ctx, mailbox.TaskID)
		}
	}
	return err == nil, err
}

func (r *Controller[M]) submitTask(
	ctx context.Context,
	handle *Handle,
	metadata *runtimeMetadata,
	checkpoint []byte,
) (*background.TaskSnapshot, error) {
	if task, err := r.manager.Get(ctx, handle.ID()); err == nil {
		return task, nil
	} else if !errors.Is(err, background.ErrNotFound) {
		return nil, err
	}
	payloadBytes, err := json.Marshal(&taskPayload{
		Version: payloadVersion, SubAgentName: metadata.AgentName,
		ChildSessionID: metadata.ChildSessionID,
	})
	if err != nil {
		return nil, err
	}
	adoptRequest := &background.AdoptForegroundRequest{
		Spec: background.Spec{
			ID: handle.ID(), ExecutorKey: ExecutorKey, Kind: "subagent",
			Payload: payloadBytes, Description: metadata.Description,
			ParentTaskID:  metadata.ParentTaskID,
			RootSessionID: metadata.RootSessionID, NotifySession: true,
		},
		ExpectedGeneration: 1,
		InputCursor:        checkpointCursor(checkpoint),
		InitialCheckpoint:  append([]byte(nil), checkpoint...),
		StartPending:       len(checkpoint) == 0,
	}
	adopted, err := r.manager.AdoptForeground(ctx, adoptRequest)
	if errors.Is(err, task.ErrInputsPending) && !adoptRequest.StartPending {
		adoptRequest.StartPending = true
		adopted, err = r.manager.AdoptForeground(ctx, adoptRequest)
	}
	if err != nil {
		return nil, err
	}
	return adopted, nil
}

func (r *Controller[M]) executeTask(
	ctx context.Context,
	task *background.TaskSnapshot,
	execution background.ExecutionRuntime,
	payload *taskPayload,
) (*background.ExecutionResult, error) {
	stream, err := r.manager.GetMailbox(ctx, task.Spec.ID)
	if err != nil {
		return nil, err
	}
	metadata, err := decodeRuntimeMetadata(stream.Identity)
	if err != nil {
		return nil, err
	}
	result, _, err := r.runActivation(
		ctx,
		&activationRequest[M]{
			runID: task.Spec.ID, metadata: metadata, task: task,
			controlRuntime: execution, signalRuntime: execution,
		},
	)
	if err != nil {
		if errors.Is(err, adk.ErrSessionBusy) {
			return &background.ExecutionResult{
				Action:     background.ExecutionActionYield,
				Checkpoint: append([]byte(nil), task.Checkpoint...),
			}, nil
		}
		return nil, err
	}
	if result.interrupted != nil {
		return r.interruptResult(ctx, result)
	}
	if result.control.Kind != "" {
		return r.controlResult(result)
	}
	if result.decision == CompletionComplete {
		data, encodeErr := encodeRuntimeMessage(result.final)
		if encodeErr != nil {
			return nil, encodeErr
		}
		return &background.ExecutionResult{
			Action: background.ExecutionActionComplete,
			Data:   data, InputCursor: result.cursor,
		}, nil
	}
	checkpoint, err := encodeRuntimeCheckpointState(
		result.cursor, result.sparseAcks, result.final,
		result.turnLoopCheckpoint,
	)
	if err != nil {
		return nil, err
	}
	return &background.ExecutionResult{
		Action:     background.ExecutionActionSuspend,
		Checkpoint: checkpoint, InputCursor: result.cursor,
	}, nil
}

func (r *Controller[M]) interruptResult(
	_ context.Context,
	result *activationResult[M],
) (*background.ExecutionResult, error) {
	if len(result.turnLoopCheckpoint) == 0 {
		return nil, errors.New("task/subagent: turn loop checkpoint is missing")
	}
	var targetIDs []string
	for _, interruptContext := range result.interrupted.InterruptContexts {
		if interruptContext.ID != "" {
			targetIDs = append(targetIDs, interruptContext.ID)
		}
	}
	if len(targetIDs) == 0 {
		return nil, errors.New("task/subagent: runtime interrupt has no targets")
	}
	final, err := encodeRuntimeMessage(result.final)
	if err != nil && !nilRuntimeMessage(result.final) {
		return nil, err
	}
	checkpoint, err := encodeRuntimeCheckpointEnvelope(&turnLoopCheckpoint{
		Version: runtimeCheckpointVersion, Mode: runtimeCheckpointResume,
		InputCursor: result.cursor, SparseAcks: result.sparseAcks,
		FinalMessage: final, TargetIDs: targetIDs,
		TurnLoopCheckpoint: result.turnLoopCheckpoint,
	})
	if err != nil {
		return nil, err
	}
	return &background.ExecutionResult{
		Action:     background.ExecutionActionWaitInput,
		Checkpoint: checkpoint, InputCursor: result.cursor,
	}, nil
}

func (r *Controller[M]) runActivation( //nolint:cyclop,funlen,revive // Coordinates TurnLoop callbacks and durable boundaries.
	ctx context.Context,
	req *activationRequest[M],
) (*activationResult[M], []byte, error) {
	runID := req.runID
	metadata := req.metadata
	backgroundTask := req.task
	stream, err := r.manager.GetMailbox(ctx, runID)
	if err != nil {
		return nil, nil, err
	}
	cursor := stream.ConsumedCursor
	var sparseAcks []int64
	var final M
	attempt := int64(0)
	var initialTurnLoopCheckpoint []byte
	var hasInitialTurnLoopCheckpoint bool
	var legacyCheckpointID string
	if backgroundTask != nil {
		attempt = backgroundTask.Attempt
	}
	if stream.State == task.MailboxSealed {
		recovered, recoverErr := r.recoverForegroundResult(ctx, stream)
		if recoverErr != nil {
			return nil, nil, recoverErr
		}
		return &activationResult[M]{
			decision: CompletionComplete, final: recovered.FinalMessage, cursor: cursor,
		}, nil, nil
	}
	if backgroundTask != nil && len(backgroundTask.Checkpoint) > 0 {
		cp, decodeErr := decodeRuntimeCheckpoint[M](backgroundTask.Checkpoint)
		if decodeErr != nil {
			return nil, nil, fmt.Errorf(
				"task/subagent: decode runtime checkpoint: %w",
				decodeErr,
			)
		}
		if cp.InputCursor > cursor {
			return nil, nil, errors.New(
				"task/subagent: runtime checkpoint cursor is ahead of mailbox",
			)
		}
		// CompleteIfNoSignals may return the task to Pending after a late
		// signal. In that case the task checkpoint is stale, while the signal
		// stream cursor already reflects the successfully committed turn.
		if cp.InputCursor == cursor {
			if err = validateSparseAcks(
				cp.SparseAcks, cursor, stream.LatestSequence,
			); err != nil {
				return nil, nil, err
			}
			final = cp.Final
			sparseAcks = cp.SparseAcks
			if cp.Version == legacyRuntimeCheckpointVersion {
				legacyCheckpointID = runtimeTurnLoopCheckpointID(runID)
				initialTurnLoopCheckpoint, hasInitialTurnLoopCheckpoint, err =
					r.checkPointStore.Get(ctx, legacyCheckpointID)
				if err != nil {
					return nil, nil, fmt.Errorf(
						"task/subagent: load legacy turn loop checkpoint: %w",
						err,
					)
				}
			} else {
				initialTurnLoopCheckpoint = append(
					[]byte(nil),
					cp.TurnLoopCheckpoint...,
				)
				hasInitialTurnLoopCheckpoint =
					len(initialTurnLoopCheckpoint) > 0
			}
		}
	}
	accessMode := RuntimeSessionStoreAccessForegroundExecute
	if backgroundTask != nil {
		accessMode = RuntimeSessionStoreAccessManagedExecute
	}
	sessionStore, err := r.sessionStoreFor(
		ctx, runID, metadata.ParentSessionID, metadata.ChildSessionID,
		backgroundTask, accessMode,
	)
	if err != nil {
		return nil, nil, err
	}
	registration, err := r.executor.resolveRegistration(metadata.AgentName)
	if err != nil {
		return nil, nil, err
	}
	var runOptions []adk.AgentRunOption
	if registration.RunOptionsFactory != nil {
		runOptions, err = registration.RunOptionsFactory()
		if err != nil {
			return nil, nil, err
		}
	}
	result := &activationResult[M]{
		cursor: cursor, sparseAcks: sparseAcks, final: final,
	}
	var stateMu sync.Mutex
	var activeTurnInputs []*task.InputRecord
	queuedSequences := make(map[int64]struct{})
	committedCursor := cursor
	pendingCommit := false
	owner := task.OwnerParent
	if backgroundTask != nil {
		owner = task.OwnerManager
	}
	executionContext := task.ExecutionContext{
		TaskID: runID, Owner: owner, Generation: stream.Generation,
		Attempt: attempt, RootSessionID: metadata.RootSessionID,
	}
	var loop *adk.TurnLoop[*task.InputRecord, M]
	observedSequence := cursor
	pushInput := func(input *task.InputRecord, replaySparseAck bool) {
		if input == nil {
			return
		}
		stateMu.Lock()
		if input.Sequence > observedSequence {
			observedSequence = input.Sequence
		}
		sparseAcked := containsSequence(result.sparseAcks, input.Sequence)
		_, queued := queuedSequences[input.Sequence]
		if (!sparseAcked || replaySparseAck) && !queued {
			queuedSequences[input.Sequence] = struct{}{}
		}
		stateMu.Unlock()
		if (sparseAcked && !replaySparseAck) || queued {
			return
		}
		var accepted bool
		if input.Delivery != task.InputPreempt {
			accepted, _ = loop.Push(input)
		} else {
			accepted, _ = loop.Push(input, adk.WithPushStrategy(
				func(
					pushCtx context.Context,
					turn *adk.TurnContext[*task.InputRecord, M],
				) []adk.PushOption[*task.InputRecord, M] {
					if r.preemptPolicy != nil {
						return r.preemptPolicy(pushCtx, input, turn)
					}
					return []adk.PushOption[*task.InputRecord, M]{
						adk.WithPreempt[*task.InputRecord, M](adk.AnySafePoint),
					}
				},
			))
		}
		if !accepted {
			stateMu.Lock()
			delete(queuedSequences, input.Sequence)
			stateMu.Unlock()
		}
	}
	loopStore := r.checkPointStore
	var checkpointStore *captureCheckpointStore
	var commitManagedState func(context.Context, []byte) error
	if backgroundTask != nil {
		commitManagedState = func(
			commitCtx context.Context,
			turnLoopCheckpoint []byte,
		) error {
			stateMu.Lock()
			defer stateMu.Unlock()
			if len(activeTurnInputs) > 0 {
				nextCursor, nextSparseAcks, acknowledgeErr :=
					acknowledgeInputRecords(
						result.cursor,
						result.sparseAcks,
						activeTurnInputs,
					)
				if acknowledgeErr != nil {
					return acknowledgeErr
				}
				result.cursor = nextCursor
				result.sparseAcks = nextSparseAcks
				pendingCommit = true
			}
			checkpoint, encodeErr := encodeRuntimeCheckpointState(
				result.cursor,
				result.sparseAcks,
				result.final,
				turnLoopCheckpoint,
			)
			if encodeErr != nil {
				return encodeErr
			}
			if commitErr := req.signalRuntime.CommitInput(
				commitCtx,
				committedCursor,
				result.cursor,
				checkpoint,
			); commitErr != nil {
				return commitErr
			}
			committedCursor = result.cursor
			pendingCommit = false
			activeTurnInputs = activeTurnInputs[:0]
			if legacyCheckpointID != "" {
				deleteCheckpointBestEffort(
					commitCtx,
					r.checkPointStore,
					legacyCheckpointID,
				)
				legacyCheckpointID = ""
			}
			return nil
		}
		checkpointStore = newCaptureCheckpointStore(
			initialTurnLoopCheckpoint,
			hasInitialTurnLoopCheckpoint,
		)
		checkpointStore.setOnSet(func(
			setCtx context.Context,
			turnLoopCheckpoint []byte,
		) error {
			return commitManagedState(setCtx, turnLoopCheckpoint)
		})
		loopStore = checkpointStore
	}
	loadLoopCheckpoint := func() ([]byte, bool, error) {
		if checkpointStore != nil {
			checkpoint, exists := checkpointStore.snapshot()
			return checkpoint, exists, nil
		}
		return loopStore.Get(ctx, runtimeTurnLoopCheckpointID(runID))
	}
	loop = adk.NewTurnLoop(adk.TurnLoopConfig[*task.InputRecord, M]{
		Store: loopStore, CheckpointID: runtimeTurnLoopCheckpointID(runID),
		SessionID: metadata.ChildSessionID, SessionStore: sessionStore,
		SessionConfig: sessionConfigForTask(r.sessionConfig, runID),
		GenInput: func(
			turnCtx context.Context,
			_ *adk.TurnLoop[*task.InputRecord, M],
			signals []*task.InputRecord,
		) (*adk.GenInputResult[*task.InputRecord, M], error) {
			stateMu.Lock()
			currentSparseAcks := append([]int64(nil), result.sparseAcks...)
			stateMu.Unlock()
			_, signals, mergeErr := mergeResumeInputRecords(
				nil, nil, signals, currentSparseAcks,
			)
			if mergeErr != nil {
				return nil, mergeErr
			}
			input, inputErr := r.signalsToInput(turnCtx, signals)
			if inputErr != nil {
				return nil, inputErr
			}
			stampRuntimeInputIDs(input, signals)
			stateMu.Lock()
			for _, signal := range signals {
				delete(queuedSequences, signal.Sequence)
			}
			stateMu.Unlock()
			runCtx := task.WithExecutionContext(turnCtx, executionContext)
			runCtx = withChildSessionID(runCtx, metadata.ChildSessionID)
			return &adk.GenInputResult[*task.InputRecord, M]{
				RunCtx: runCtx,
				Input:  input, RunOpts: append([]adk.AgentRunOption(nil), runOptions...),
				Consumed: signals,
			}, nil
		},
		GenResume: func(
			resumeCtx context.Context,
			_ *adk.TurnLoop[*task.InputRecord, M],
			interrupted, unhandled, newItems []*task.InputRecord,
		) (*adk.GenResumeResult[*task.InputRecord, M], error) {
			stateMu.Lock()
			currentSparseAcks := append([]int64(nil), result.sparseAcks...)
			stateMu.Unlock()
			interrupted, pending, mergeErr := mergeResumeInputRecords(
				interrupted, unhandled, newItems, currentSparseAcks,
			)
			if mergeErr != nil {
				return nil, mergeErr
			}
			var resumeParams *adk.ResumeParams
			var resumeSignals []*task.InputRecord
			var remaining []*task.InputRecord
			for _, signal := range pending {
				if signal.Kind != ResumeInputKind {
					remaining = append(remaining, signal)
					continue
				}
				if len(resumeSignals) > 0 {
					return nil, errors.New(
						"task/subagent: multiple resume signals are ambiguous",
					)
				}
				targets, decodeErr := decodeRuntimeResumeTargets(signal.Data)
				if decodeErr != nil {
					return nil, decodeErr
				}
				resumeParams = &adk.ResumeParams{Targets: targets}
				resumeSignals = append(resumeSignals, signal)
			}
			runCtx := task.WithExecutionContext(resumeCtx, executionContext)
			runCtx = withChildSessionID(runCtx, metadata.ChildSessionID)
			resume := &adk.GenResumeResult[*task.InputRecord, M]{
				RunCtx:       runCtx,
				Decision:     adk.TurnLoopResumeDecisionResume,
				ResumeParams: resumeParams,
				Consumed: append(
					append([]*task.InputRecord(nil), interrupted...),
					resumeSignals...,
				),
				Remaining: remaining,
			}
			stateMu.Lock()
			for _, signal := range resume.Consumed {
				delete(queuedSequences, signal.Sequence)
			}
			stateMu.Unlock()
			return resume, nil
		},
		PrepareAgent: func(
			context.Context,
			*adk.TurnLoop[*task.InputRecord, M],
			[]*task.InputRecord,
		) (adk.TypedAgent[M], error) {
			return registration.Agent, nil
		},
		OnAgentEvents: func(
			turnCtx context.Context,
			tc *adk.TurnContext[*task.InputRecord, M],
			events *adk.AsyncIterator[*adk.TypedAgentEvent[M]],
		) error {
			activeTurnInputs = append(activeTurnInputs[:0], tc.Consumed...)
			defer func() {
				select {
				case <-tc.Stopped:
					return
				default:
				}
				select {
				case <-tc.Preempted:
					activeTurnInputs = activeTurnInputs[:0]
				default:
				}
			}()
			for {
				event, open := events.Next()
				if !open {
					break
				}
				if event.Err != nil {
					return event.Err
				}
				if event.Action != nil && event.Action.Interrupted != nil {
					result.interrupted = event.Action.Interrupted
				}
				forwarded := event
				if event.Output != nil && event.Output.MessageOutput != nil {
					message, messageErr := event.Output.MessageOutput.GetMessage()
					if messageErr != nil {
						return messageErr
					}
					result.final = message
					forwarded = materializedEvent(event, message)
				}
				if req.attached && req.onEvent != nil {
					req.onEvent(forwarded)
				}
			}
			stateMu.Lock()
			nextCursor, nextSparseAcks, acknowledgeErr := acknowledgeInputRecords(
				result.cursor,
				result.sparseAcks,
				tc.Consumed,
			)
			if acknowledgeErr == nil && req.attached && nextCursor > result.cursor {
				acknowledgeErr = r.advanceCursor(
					turnCtx,
					req.signalRuntime,
					runID,
					result.cursor,
					nextCursor,
					true,
				)
			}
			if acknowledgeErr == nil {
				result.cursor = nextCursor
				result.sparseAcks = nextSparseAcks
				if !req.attached {
					pendingCommit = true
				}
			}
			stateMu.Unlock()
			if acknowledgeErr != nil {
				return acknowledgeErr
			}
			activeTurnInputs = activeTurnInputs[:0]
			if result.interrupted != nil {
				return nil
			}
			next, loadErr := r.manager.ListInputs(
				turnCtx,
				&task.ListInputsRequest{
					TaskID: runID, AfterSequence: result.cursor,
					Limit: r.inputBatchSize,
				},
			)
			if loadErr != nil {
				return loadErr
			}
			if len(next.Inputs) > 0 {
				for _, signal := range next.Inputs {
					pushInput(signal, false)
				}
				return nil
			}
			decision, barrierErr := r.barrier.Check(
				turnCtx,
				&CompletionContext[M]{
					TaskID: runID, ChildSessionID: metadata.ChildSessionID,
					AgentName: metadata.AgentName, FinalMessage: result.final,
				},
			)
			if barrierErr != nil {
				return barrierErr
			}
			if barrierErr = validateCompletionAction(decision); barrierErr != nil {
				return barrierErr
			}
			result.decision = decision
			if decision == CompletionComplete && req.attached {
				_, completeErr := r.completeAttached(
					turnCtx, runID, result.cursor, result.final,
				)
				if errors.Is(completeErr, task.ErrInputsPending) {
					if invalidateErr := r.invalidateForegroundCandidate(
						turnCtx, runID,
					); invalidateErr != nil {
						return invalidateErr
					}
					next, loadErr = r.manager.ListInputs(
						turnCtx,
						&task.ListInputsRequest{
							TaskID: runID, AfterSequence: result.cursor,
							Limit: r.inputBatchSize,
						},
					)
					if loadErr != nil {
						return loadErr
					}
					for _, signal := range next.Inputs {
						pushInput(signal, false)
					}
					return nil
				}
				if completeErr != nil {
					return completeErr
				}
			}
			loop.Stop()
			return nil
		},
	})
	signals, err := r.manager.ListInputs(ctx, &task.ListInputsRequest{
		TaskID: runID, AfterSequence: cursor, Limit: r.inputBatchSize,
	})
	if err != nil {
		return nil, nil, err
	}
	_, hasLoopCheckpoint, checkpointErr := loadLoopCheckpoint()
	if checkpointErr != nil {
		return nil, nil, checkpointErr
	}
	if len(signals.Inputs) == 0 && !hasLoopCheckpoint {
		decision, barrierErr := r.barrier.Check(
			ctx,
			&CompletionContext[M]{
				TaskID: runID, ChildSessionID: metadata.ChildSessionID,
				AgentName: metadata.AgentName, FinalMessage: result.final,
			},
		)
		if barrierErr != nil {
			return nil, nil, barrierErr
		}
		if barrierErr = validateCompletionAction(decision); barrierErr != nil {
			return nil, nil, barrierErr
		}
		result.decision = decision
		if decision == CompletionComplete && req.attached {
			if _, completeErr := r.completeAttached(
				ctx, runID, result.cursor, result.final,
			); completeErr != nil {
				if errors.Is(completeErr, task.ErrInputsPending) {
					if invalidateErr := r.invalidateForegroundCandidate(
						ctx, runID,
					); invalidateErr != nil {
						return nil, nil, invalidateErr
					}
					// Re-enter through TurnLoop with the signal that won the race.
				} else {
					return nil, nil, completeErr
				}
			} else {
				checkpoint, checkpointErr := encodeRuntimeCheckpointState(
					result.cursor, result.sparseAcks, result.final, nil,
				)
				return result, checkpoint, checkpointErr
			}
			signals, err = r.manager.ListInputs(
				ctx,
				&task.ListInputsRequest{
					TaskID: runID, AfterSequence: cursor, Limit: r.inputBatchSize,
				},
			)
			if err != nil {
				return nil, nil, err
			}
		} else {
			checkpoint, checkpointErr := encodeRuntimeCheckpointState(
				result.cursor, result.sparseAcks, result.final, nil,
			)
			return result, checkpoint, checkpointErr
		}
	}
	for _, signal := range signals.Inputs {
		pushInput(signal, hasLoopCheckpoint)
	}
	watchCtx, stopWatcher := context.WithCancel(ctx)
	watchDone := make(chan struct{})
	watchErr := make(chan error, 1)
	go func() {
		defer close(watchDone)
		for {
			stateMu.Lock()
			after := observedSequence
			stateMu.Unlock()
			next, waitErr := r.manager.WaitInputs(
				watchCtx,
				&task.WaitInputsRequest{TaskID: runID, AfterSequence: after},
			)
			if waitErr != nil {
				if !errors.Is(waitErr, context.Canceled) {
					watchErr <- waitErr
					loop.Stop(adk.WithImmediate())
				}
				return
			}
			for _, input := range next.Inputs {
				pushInput(input, false)
			}
			if next.MailboxState == task.MailboxSealed {
				return
			}
		}
	}()
	controlDone := make(chan struct{})
	controlReceived := make(chan background.ControlRequest, 1)
	if req.controlRuntime != nil {
		go func() {
			select {
			case control := <-req.controlRuntime.Controls():
				controlReceived <- control
				switch control.Kind {
				case background.ControlDrain:
					if r.drainCancelTimeout > 0 {
						loop.Stop(adk.WithGracefulTimeout(r.drainCancelTimeout))
					} else {
						loop.Stop(adk.WithGraceful())
					}
				default:
					loop.Stop(adk.WithImmediate())
				}
			case <-controlDone:
			case <-ctx.Done():
			}
		}()
	}
	loop.Run(ctx)
	exit := loop.Wait()
	stopWatcher()
	<-watchDone
	control := pollActivationControl(controlReceived)
	close(controlDone)
	result.control = control
	if exit.CheckpointErr != nil {
		return nil, nil, exit.CheckpointErr
	}
	capturedCheckpoint, captured, err := loadLoopCheckpoint()
	if err != nil {
		return nil, nil, err
	}
	if captured {
		result.turnLoopCheckpoint = capturedCheckpoint
	} else {
		result.turnLoopCheckpoint = nil
	}
	if pendingCommit {
		if err = commitManagedState(ctx, result.turnLoopCheckpoint); err != nil {
			return nil, nil, err
		}
	}
	// Agent side effects are at-least-once: a crash before the TurnLoop
	// checkpoint capture may repeat them. CommitInput atomically binds the
	// captured TurnLoop state, cursor, and sparse acknowledgements.
	if result.control.Kind == background.ControlDrain &&
		len(activeTurnInputs) > 0 {
		if !exit.CheckpointAttempted {
			return nil, nil, background.ErrDrainCheckpointUnavailable
		}
	}
	select {
	case err = <-watchErr:
		return nil, nil, err
	default:
	}
	if exit.ExitReason != nil && result.control.Kind == "" {
		var interruptErr *adk.InterruptError
		if errors.As(exit.ExitReason, &interruptErr) {
			result.interrupted = &adk.InterruptInfo{
				InterruptContexts: interruptErr.InterruptContexts,
			}
		} else {
			return nil, nil, exit.ExitReason
		}
	}
	checkpoint, err := encodeRuntimeCheckpointState(
		result.cursor,
		result.sparseAcks,
		result.final,
		result.turnLoopCheckpoint,
	)
	return result, checkpoint, err
}

type inputRecordIdentity struct {
	taskID   string
	sequence int64
}

func mergeResumeInputRecords(
	interrupted, unhandled, newItems []*task.InputRecord,
	sparseAcks []int64,
) ([]*task.InputRecord, []*task.InputRecord, error) {
	mergedInterrupted := make([]*task.InputRecord, 0, len(interrupted))
	pending := make([]*task.InputRecord, 0, len(unhandled)+len(newItems))
	seen := make(map[inputRecordIdentity]*task.InputRecord, len(interrupted)+len(unhandled)+len(newItems))
	groups := []struct {
		name    string
		inputs  []*task.InputRecord
		target  *[]*task.InputRecord
		mailbox bool
	}{
		{name: "checkpoint interrupted", inputs: interrupted, target: &mergedInterrupted},
		{name: "checkpoint unhandled", inputs: unhandled, target: &pending},
		{name: "mailbox", inputs: newItems, target: &pending, mailbox: true},
	}
	for _, group := range groups {
		for _, input := range group.inputs {
			if input == nil {
				return nil, nil, fmt.Errorf(
					"task/subagent: %s input is nil",
					group.name,
				)
			}
			identity := inputRecordIdentity{
				taskID: input.TaskID, sequence: input.Sequence,
			}
			if checkpointInput, exists := seen[identity]; exists {
				if !equalInputRecords(checkpointInput, input) {
					return nil, nil, fmt.Errorf(
						"task/subagent: conflicting input replay for task %q sequence %d",
						input.TaskID,
						input.Sequence,
					)
				}
				continue
			}
			if group.mailbox && containsSequence(sparseAcks, input.Sequence) {
				continue
			}
			seen[identity] = input
			*group.target = append(*group.target, input)
		}
	}
	return mergedInterrupted, pending, nil
}

func equalInputRecords(left, right *task.InputRecord) bool {
	return left != nil && right != nil &&
		left.TaskID == right.TaskID &&
		left.Sequence == right.Sequence &&
		left.EventID == right.EventID &&
		left.Kind == right.Kind &&
		bytes.Equal(left.Data, right.Data) &&
		left.Delivery == right.Delivery &&
		left.CreatedAt.Equal(right.CreatedAt)
}

func acknowledgeInputRecords(
	cursor int64,
	sparseAcks []int64,
	inputs []*task.InputRecord,
) (int64, []int64, error) {
	if len(sparseAcks) > maxSparseAcks {
		return 0, nil, errors.New(
			"task/subagent: runtime checkpoint sparse acks exceed limit",
		)
	}
	acks := make([]int64, 0, len(sparseAcks)+len(inputs))
	acks = append(acks, sparseAcks...)
	for _, input := range inputs {
		if input == nil {
			return 0, nil, errors.New(
				"task/subagent: consumed input is nil",
			)
		}
		if input.Sequence > cursor {
			acks = append(acks, input.Sequence)
		}
	}
	sort.Slice(acks, func(i, j int) bool { return acks[i] < acks[j] })
	result := make([]int64, 0, len(acks))
	var previous int64
	hasPrevious := false
	for _, sequence := range acks {
		if sequence <= cursor || hasPrevious && sequence == previous {
			continue
		}
		hasPrevious = true
		previous = sequence
		if sequence == cursor+1 {
			cursor = sequence
			continue
		}
		result = append(result, sequence)
		if len(result) > maxSparseAcks {
			return 0, nil, errors.New(
				"task/subagent: runtime checkpoint sparse acks exceed limit",
			)
		}
	}
	return cursor, result, nil
}

func containsSequence(sequences []int64, target int64) bool {
	index := sort.Search(len(sequences), func(i int) bool {
		return sequences[i] >= target
	})
	return index < len(sequences) && sequences[index] == target
}

func validateSparseAcks(sparseAcks []int64, cursor, latest int64) error {
	if err := validateSparseAckOrdering(sparseAcks, cursor); err != nil {
		return err
	}
	for _, sequence := range sparseAcks {
		if sequence > latest {
			return errors.New("task/subagent: runtime checkpoint sparse acks are invalid")
		}
	}
	return nil
}

func validateSparseAckOrdering(sparseAcks []int64, cursor int64) error {
	if len(sparseAcks) > maxSparseAcks {
		return errors.New(
			"task/subagent: runtime checkpoint sparse acks exceed limit",
		)
	}
	previous := cursor
	for _, sequence := range sparseAcks {
		if sequence <= previous {
			return errors.New("task/subagent: runtime checkpoint sparse acks are invalid")
		}
		if previous == cursor && sequence == cursor+1 {
			return errors.New("task/subagent: runtime checkpoint sparse acks are not folded")
		}
		previous = sequence
	}
	return nil
}

func pollActivationControl(
	controls <-chan background.ControlRequest,
) background.ControlRequest {
	select {
	case control := <-controls:
		return control
	default:
		return background.ControlRequest{}
	}
}

func validateCompletionAction(action CompletionAction) error {
	switch action {
	case CompletionComplete, CompletionSuspend:
		return nil
	default:
		return fmt.Errorf(
			"task/subagent: invalid completion action %d",
			action,
		)
	}
}

func (r *Controller[M]) completeAttached(
	ctx context.Context,
	runID string,
	cursor int64,
	final M,
) (*task.Mailbox, error) {
	finalMessage, err := encodeRuntimeMessage(final)
	if err != nil {
		return nil, err
	}
	checkpoint, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion, State: foregroundResultTerminal,
		Status:       task.OutcomeCompleted,
		InputCursor:  cursor,
		FinalMessage: finalMessage,
	})
	if err != nil {
		return nil, err
	}
	if err = r.checkPointStore.Set(
		ctx,
		runtimeForegroundResultCheckpointID(runID),
		checkpoint,
	); err != nil {
		return nil, err
	}
	mailbox, err := r.manager.GetMailbox(ctx, runID)
	if err != nil {
		return nil, err
	}
	return r.manager.SealMailbox(
		ctx,
		&task.SealMailboxRequest{
			TaskID: runID, ExpectedCursor: cursor,
			ExpectedGeneration: mailbox.Generation,
		},
	)
}

func (r *Controller[M]) failAttached(
	ctx context.Context,
	runID string,
	status task.OutcomeStatus,
	runErr error,
) error {
	if runErr == nil ||
		(status != task.OutcomeFailed && status != task.OutcomeCanceled) {
		return nil
	}
	mailbox, err := r.manager.GetMailbox(ctx, runID)
	if err != nil {
		return err
	}
	checkpoint, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion, State: foregroundResultTerminal,
		Status:      status,
		InputCursor: mailbox.ConsumedCursor, Error: runErr.Error(),
	})
	if err != nil {
		return err
	}
	if err = r.checkPointStore.Set(
		ctx,
		runtimeForegroundResultCheckpointID(runID),
		checkpoint,
	); err != nil {
		return err
	}
	if mailbox.State == task.MailboxSealed ||
		mailbox.State == task.MailboxBackground {
		return nil
	}
	_, err = r.manager.AbandonMailbox(ctx, &task.AbandonMailboxRequest{
		TaskID: runID, ExpectedGeneration: mailbox.Generation,
	})
	return err
}

func decodeForegroundResultCheckpoint(
	data []byte,
) (*foregroundResultCheckpoint, error) {
	var checkpoint foregroundResultCheckpoint
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf(
			"task/subagent: invalid foreground result checkpoint: %w",
			err,
		)
	}
	if !validForegroundResultCheckpoint(checkpoint) {
		return nil, errors.New("task/subagent: invalid foreground result checkpoint")
	}
	return &checkpoint, nil
}

func validForegroundResultCheckpoint(checkpoint foregroundResultCheckpoint) bool {
	switch checkpoint.Version {
	case legacyForegroundResultVersion:
		return checkpoint.State == "" &&
			checkpoint.InputCursor >= 0 &&
			validForegroundOutcome(checkpoint)
	case foregroundResultVersion:
		switch checkpoint.State {
		case foregroundResultTerminal:
			return checkpoint.InputCursor >= 0 &&
				validForegroundOutcome(checkpoint)
		case foregroundResultInvalidated:
			return checkpoint.Status == task.OutcomeStatus(0) &&
				checkpoint.InputCursor == 0 &&
				len(checkpoint.FinalMessage) == 0 &&
				checkpoint.Error == ""
		default:
			return false
		}
	default:
		return false
	}
}

func validForegroundOutcome(checkpoint foregroundResultCheckpoint) bool {
	switch checkpoint.Status {
	case task.OutcomeCompleted:
		return checkpoint.Error == "" && len(checkpoint.FinalMessage) > 0
	case task.OutcomeFailed, task.OutcomeCanceled:
		return checkpoint.Error != "" && len(checkpoint.FinalMessage) == 0
	default:
		return false
	}
}

func (r *Controller[M]) controlResult(
	result *activationResult[M],
) (*background.ExecutionResult, error) {
	switch result.control.Kind {
	case background.ControlStop:
		reason := result.control.Reason
		if reason == "" {
			reason = "task was canceled"
		}
		return &background.ExecutionResult{
			Action: background.ExecutionActionCancel, Error: reason,
		}, nil
	case background.ControlDrain:
		checkpoint, err := encodeRuntimeCheckpointState(
			result.cursor,
			result.sparseAcks,
			result.final,
			result.turnLoopCheckpoint,
		)
		if err != nil {
			return nil, err
		}
		return &background.ExecutionResult{
			Action: background.ExecutionActionSuspend, Checkpoint: checkpoint,
			InputCursor: result.cursor,
		}, nil
	case background.ControlTimeout:
		reason := result.control.Reason
		if reason == "" {
			reason = "sub-agent runtime timed out"
		}
		return &background.ExecutionResult{
			Action: background.ExecutionActionFail, Error: reason,
		}, nil
	default:
		return nil, errors.New("task/subagent: unsupported runtime control")
	}
}

func (r *Controller[M]) signalsToInput(
	ctx context.Context,
	signals []*task.InputRecord,
) (*adk.TypedAgentInput[M], error) {
	result := &adk.TypedAgentInput[M]{}
	var external []*task.InputRecord
	for _, signal := range signals {
		if signal.Kind == initialSignalKind || signal.Kind == messageInputKind {
			var encoded serializedTypedInput
			if err := json.Unmarshal(signal.Data, &encoded); err != nil {
				return nil, err
			}
			input, err := decodeTypedInput[M](&encoded)
			if err != nil {
				return nil, err
			}
			result.Messages = append(result.Messages, input.Messages...)
			result.EnableStreaming = result.EnableStreaming || input.EnableStreaming
			continue
		}
		if signal.Kind == ResumeInputKind {
			continue
		}
		external = append(external, &task.InputRecord{
			Input: task.Input{
				EventID: signal.EventID, Kind: signal.Kind,
				Data: append([]byte(nil), signal.Data...), Delivery: signal.Delivery,
			},
		})
	}
	if len(external) > 0 {
		input, err := r.inputsToAgentInput(ctx, external)
		if err != nil {
			return nil, err
		}
		if input == nil {
			return nil, errors.New("task/subagent: InputsToAgentInput returned nil")
		}
		result.Messages = append(result.Messages, input.Messages...)
		result.EnableStreaming = result.EnableStreaming || input.EnableStreaming
	}
	if err := validateTypedInput(result); err != nil {
		return nil, fmt.Errorf(
			"task/subagent: runtime signal batch produced invalid input: %w",
			err,
		)
	}
	return result, nil
}

func stampRuntimeInputIDs[M adk.MessageType](
	input *adk.TypedAgentInput[M],
	signals []*task.InputRecord,
) {
	if input == nil {
		return
	}
	identity := ""
	for _, signal := range signals {
		identity += signal.EventID + "\x00"
	}
	for index, message := range input.Messages {
		id := "subagent-input:" + uuid.NewSHA1(
			childSessionNamespace,
			[]byte(fmt.Sprintf("%s%d", identity, index)),
		).String()
		switch typed := any(message).(type) {
		case *schema.Message:
			typed.Extra = adkinternal.SetMessageID(typed.Extra, id)
		case *schema.AgenticMessage:
			typed.Extra = adkinternal.SetMessageID(typed.Extra, id)
		}
	}
}

func (r *Controller[M]) advanceCursor(
	ctx context.Context,
	execution background.ExecutionRuntime,
	runID string,
	expected, cursor int64,
	attached bool,
) error {
	if attached {
		mailbox, err := r.manager.GetMailbox(ctx, runID)
		if err != nil {
			return err
		}
		return r.manager.AdvanceInputCursor(
			ctx,
			&task.AdvanceCursorRequest{
				TaskID: runID, ExpectedCursor: expected, Cursor: cursor,
				ExpectedGeneration: mailbox.Generation,
			},
		)
	}
	if execution == nil {
		return errors.New("task/subagent: signal execution runtime is required")
	}
	return execution.AdvanceInputCursor(ctx, expected, cursor)
}

func equalSequences(left, right []int64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (r *Controller[M]) sessionStoreFor(
	ctx context.Context,
	runID, parentSessionID, childSessionID string,
	runtimeTask *background.TaskSnapshot,
	accessMode RuntimeSessionStoreAccessMode,
) (adk.SessionEventStore[M], error) {
	request := &RuntimeSessionStoreRequest{
		TaskID: runID, ParentSessionID: parentSessionID,
		ChildSessionID: childSessionID,
		Task:           runtimeTask, AccessMode: accessMode,
	}
	if err := validateRuntimeSessionStoreRequest(request); err != nil {
		return nil, err
	}
	store := r.sessionStore
	var err error
	if r.sessionStoreFactory != nil {
		store, err = r.sessionStoreFactory(ctx, request)
	}
	if err != nil {
		return nil, err
	}
	if store == nil {
		return nil, errors.New("task/subagent: runtime session store is nil")
	}
	return store, nil
}

func validateRuntimeSessionStoreRequest(request *RuntimeSessionStoreRequest) error {
	if request == nil || request.TaskID == "" || request.ParentSessionID == "" ||
		request.ChildSessionID == "" {
		return errors.New("task/subagent: runtime session store request is incomplete")
	}
	switch request.AccessMode {
	case RuntimeSessionStoreAccessForegroundExecute:
		if request.Task != nil {
			return errors.New(
				"task/subagent: foreground session store access must not include a task",
			)
		}
	case RuntimeSessionStoreAccessManagedExecute,
		RuntimeSessionStoreAccessReadProgress:
		if request.Task == nil {
			return errors.New(
				"task/subagent: non-foreground session store access requires a task",
			)
		}
		if request.Task.Spec.ID != request.TaskID {
			return errors.New(
				"task/subagent: session store task snapshot ID does not match request",
			)
		}
	default:
		return errors.New("task/subagent: runtime session store access mode is invalid")
	}
	return nil
}

func encodeRuntimeCheckpoint[M adk.MessageType](
	cursor int64,
	final M,
) ([]byte, error) {
	return encodeRuntimeCheckpointState(cursor, nil, final, nil)
}

func encodeRuntimeCheckpointState[M adk.MessageType](
	cursor int64,
	sparseAcks []int64,
	final M,
	turnLoopState []byte,
) ([]byte, error) {
	if cursor < 0 {
		return nil, errors.New("task/subagent: runtime checkpoint cursor is invalid")
	}
	if err := validateSparseAckOrdering(sparseAcks, cursor); err != nil {
		return nil, err
	}
	var finalBytes []byte
	var err error
	if !nilRuntimeMessage(final) {
		finalBytes, err = encodeRuntimeMessage(final)
		if err != nil {
			return nil, err
		}
	}
	return encodeRuntimeCheckpointEnvelope(&turnLoopCheckpoint{
		Version: runtimeCheckpointVersion, Mode: runtimeCheckpointIdle,
		InputCursor: cursor, SparseAcks: sparseAcks, FinalMessage: finalBytes,
		TurnLoopCheckpoint: turnLoopState,
	})
}

func decodeRuntimeCheckpoint[M adk.MessageType](
	data []byte,
) (*decodedRuntimeCheckpoint[M], error) {
	checkpoint, err := decodeRuntimeCheckpointEnvelope(data)
	if err != nil {
		return nil, err
	}
	var final M
	if len(checkpoint.FinalMessage) > 0 {
		final, err = decodeRuntimeMessage[M](checkpoint.FinalMessage)
		if err != nil {
			return nil, err
		}
	}
	return &decodedRuntimeCheckpoint[M]{
		Version: checkpoint.Version, InputCursor: checkpoint.InputCursor,
		Final: final,
		Mode:  checkpoint.Mode, TargetIDs: append([]string(nil), checkpoint.TargetIDs...),
		SparseAcks: append([]int64(nil), checkpoint.SparseAcks...),
		TurnLoopCheckpoint: append(
			[]byte(nil),
			checkpoint.TurnLoopCheckpoint...,
		),
	}, nil
}

func encodeRuntimeCheckpointEnvelope(checkpoint *turnLoopCheckpoint) ([]byte, error) {
	if checkpoint == nil || checkpoint.Version != runtimeCheckpointVersion {
		return nil, errors.New("task/subagent: incompatible runtime checkpoint")
	}
	if err := validateRuntimeCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	metadata := make([]byte, 0, 32)
	switch checkpoint.Mode {
	case runtimeCheckpointIdle:
		metadata = append(metadata, runtimeCheckpointModeIdle)
	case runtimeCheckpointResume:
		metadata = append(metadata, runtimeCheckpointModeResume)
	default:
		return nil, errors.New("task/subagent: runtime checkpoint mode is invalid")
	}
	metadata = appendRuntimeUvarint(metadata, uint64(checkpoint.InputCursor))
	metadata = appendRuntimeUvarint(metadata, uint64(len(checkpoint.SparseAcks)))
	for _, sequence := range checkpoint.SparseAcks {
		metadata = appendRuntimeUvarint(metadata, uint64(sequence))
	}
	metadata = appendRuntimeUvarint(metadata, uint64(len(checkpoint.TargetIDs)))
	for _, targetID := range checkpoint.TargetIDs {
		metadata = appendRuntimeBytes(metadata, []byte(targetID))
	}

	encoded := make([]byte, 0,
		len(runtimeCheckpointMagic)+1+len(metadata)+
			len(checkpoint.FinalMessage)+len(checkpoint.TurnLoopCheckpoint)+30,
	)
	encoded = append(encoded, runtimeCheckpointMagic...)
	encoded = append(encoded, byte(runtimeCheckpointVersion))
	encoded = appendRuntimeBytes(encoded, metadata)
	encoded = appendRuntimeBytes(encoded, checkpoint.FinalMessage)
	encoded = appendRuntimeBytes(encoded, checkpoint.TurnLoopCheckpoint)
	return encoded, nil
}

func decodeRuntimeCheckpointEnvelope(data []byte) (*turnLoopCheckpoint, error) {
	if !bytes.HasPrefix(data, []byte(runtimeCheckpointMagic)) {
		var legacy turnLoopCheckpoint
		if err := json.Unmarshal(data, &legacy); err != nil {
			return nil, fmt.Errorf("task/subagent: decode legacy runtime checkpoint: %w", err)
		}
		if legacy.Version != legacyRuntimeCheckpointVersion {
			return nil, errors.New("task/subagent: incompatible runtime checkpoint")
		}
		if err := validateRuntimeCheckpoint(&legacy); err != nil {
			return nil, err
		}
		return &legacy, nil
	}

	reader := runtimeCheckpointReader{data: data[len(runtimeCheckpointMagic):]}
	version, err := reader.readByte()
	if err != nil {
		return nil, err
	}
	if version != byte(runtimeCheckpointVersion) {
		return nil, errors.New("task/subagent: incompatible runtime checkpoint")
	}
	metadataBytes, err := reader.readBytes()
	if err != nil {
		return nil, err
	}
	finalMessage, err := reader.readBytes()
	if err != nil {
		return nil, err
	}
	turnLoopState, err := reader.readBytes()
	if err != nil {
		return nil, err
	}
	if reader.remaining() != 0 {
		return nil, errors.New("task/subagent: runtime checkpoint has trailing data")
	}

	metadata := runtimeCheckpointReader{data: metadataBytes}
	mode, err := metadata.readByte()
	if err != nil {
		return nil, err
	}
	cursor, err := metadata.readUvarint()
	if err != nil || cursor > uint64(^uint64(0)>>1) {
		return nil, errors.New("task/subagent: runtime checkpoint cursor is invalid")
	}
	sparseCount, err := metadata.readCount(maxSparseAcks)
	if err != nil {
		return nil, err
	}
	sparseAcks := make([]int64, sparseCount)
	for index := range sparseAcks {
		sequence, sequenceErr := metadata.readUvarint()
		if sequenceErr != nil || sequence > uint64(^uint64(0)>>1) {
			return nil, errors.New("task/subagent: runtime checkpoint sparse acks are invalid")
		}
		sparseAcks[index] = int64(sequence)
	}
	targetCount, err := metadata.readBoundedCount()
	if err != nil {
		return nil, err
	}
	targetIDs := make([]string, targetCount)
	for index := range targetIDs {
		targetID, targetErr := metadata.readBytes()
		if targetErr != nil {
			return nil, targetErr
		}
		targetIDs[index] = string(targetID)
	}
	if metadata.remaining() != 0 {
		return nil, errors.New("task/subagent: runtime checkpoint metadata has trailing data")
	}
	checkpoint := &turnLoopCheckpoint{
		Version: runtimeCheckpointVersion, InputCursor: int64(cursor),
		SparseAcks: sparseAcks, FinalMessage: append([]byte(nil), finalMessage...),
		TargetIDs:          targetIDs,
		TurnLoopCheckpoint: append([]byte(nil), turnLoopState...),
	}
	switch mode {
	case runtimeCheckpointModeIdle:
		checkpoint.Mode = runtimeCheckpointIdle
	case runtimeCheckpointModeResume:
		checkpoint.Mode = runtimeCheckpointResume
	default:
		return nil, errors.New("task/subagent: runtime checkpoint mode is invalid")
	}
	if err := validateRuntimeCheckpoint(checkpoint); err != nil {
		return nil, err
	}
	return checkpoint, nil
}

func validateRuntimeCheckpoint(checkpoint *turnLoopCheckpoint) error {
	if checkpoint.InputCursor < 0 {
		return errors.New("task/subagent: incompatible runtime checkpoint")
	}
	if checkpoint.Version == legacyRuntimeCheckpointVersion &&
		(len(checkpoint.SparseAcks) != 0 ||
			len(checkpoint.TurnLoopCheckpoint) != 0) {
		return errors.New("task/subagent: invalid legacy runtime checkpoint")
	}
	if err := validateSparseAckOrdering(
		checkpoint.SparseAcks,
		checkpoint.InputCursor,
	); err != nil {
		return err
	}
	switch checkpoint.Mode {
	case runtimeCheckpointIdle:
		if len(checkpoint.TargetIDs) != 0 {
			return errors.New(
				"task/subagent: idle runtime checkpoint contains resume targets",
			)
		}
	case runtimeCheckpointResume:
		if len(checkpoint.TargetIDs) == 0 {
			return errors.New(
				"task/subagent: interrupt runtime checkpoint has no targets",
			)
		}
		if checkpoint.Version == runtimeCheckpointVersion &&
			len(checkpoint.TurnLoopCheckpoint) == 0 {
			return errors.New(
				"task/subagent: interrupt runtime checkpoint has no turn loop state",
			)
		}
	default:
		return errors.New("task/subagent: runtime checkpoint mode is invalid")
	}
	return nil
}

func appendRuntimeUvarint(dst []byte, value uint64) []byte {
	var encoded [binary.MaxVarintLen64]byte
	size := binary.PutUvarint(encoded[:], value)
	return append(dst, encoded[:size]...)
}

func appendRuntimeBytes(dst, value []byte) []byte {
	dst = appendRuntimeUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

type runtimeCheckpointReader struct {
	data   []byte
	offset int
}

func (r *runtimeCheckpointReader) remaining() int {
	return len(r.data) - r.offset
}

func (r *runtimeCheckpointReader) readByte() (byte, error) {
	if r.remaining() < 1 {
		return 0, errors.New("task/subagent: runtime checkpoint is truncated")
	}
	value := r.data[r.offset]
	r.offset++
	return value, nil
}

func (r *runtimeCheckpointReader) readUvarint() (uint64, error) {
	if r.remaining() == 0 {
		return 0, errors.New("task/subagent: runtime checkpoint is truncated")
	}
	value, size := binary.Uvarint(r.data[r.offset:])
	switch {
	case size == 0:
		return 0, errors.New("task/subagent: runtime checkpoint is truncated")
	case size < 0:
		return 0, errors.New("task/subagent: runtime checkpoint integer overflows")
	default:
		r.offset += size
		return value, nil
	}
}

func (r *runtimeCheckpointReader) readBytes() ([]byte, error) {
	size, err := r.readUvarint()
	if err != nil {
		return nil, err
	}
	if size > uint64(r.remaining()) {
		return nil, errors.New("task/subagent: runtime checkpoint is truncated")
	}
	start := r.offset
	r.offset += int(size)
	return r.data[start:r.offset], nil
}

func (r *runtimeCheckpointReader) readCount(limit int) (int, error) {
	count, err := r.readUvarint()
	if err != nil {
		return 0, err
	}
	if count > uint64(limit) {
		return 0, errors.New(
			"task/subagent: runtime checkpoint sparse acks exceed limit",
		)
	}
	return int(count), nil
}

func (r *runtimeCheckpointReader) readBoundedCount() (int, error) {
	count, err := r.readUvarint()
	if err != nil {
		return 0, err
	}
	if count > uint64(r.remaining()) {
		return 0, errors.New("task/subagent: runtime checkpoint count is invalid")
	}
	return int(count), nil
}

func decodeRuntimeResumeTargets(data []byte) (map[string]any, error) {
	targets, err := decodeResumeTargets(data)
	if err != nil {
		return nil, err
	}
	if len(targets) == 0 {
		return nil, errors.New("task/subagent: resume targets are empty")
	}
	return targets, nil
}

func encodeRuntimeMessage[M adk.MessageType](message M) ([]byte, error) {
	if nilRuntimeMessage(message) {
		return nil, errors.New("task/subagent: runtime final message is required")
	}
	return (&schema.HumanReadableSerializer{}).Marshal(message)
}

func decodeRuntimeMessage[M adk.MessageType](data []byte) (M, error) {
	var zero M
	var decoded any
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(data, &decoded); err != nil {
		return zero, err
	}
	message, ok := decoded.(M)
	if !ok {
		return zero, errors.New("task/subagent: runtime result type mismatch")
	}
	return message, nil
}

func decodeRuntimeMetadata(data []byte) (*runtimeMetadata, error) {
	var metadata runtimeMetadata
	if err := json.Unmarshal(data, &metadata); err != nil {
		return nil, err
	}
	if metadata.Version != runtimeMetadataVersion || metadata.ParentSessionID == "" ||
		metadata.RootSessionID == "" ||
		metadata.ChildSessionID == "" || metadata.AgentName == "" ||
		(metadata.StartMode != task.StartModeForeground &&
			metadata.StartMode != task.StartModeBackground) {
		return nil, errors.New("task/subagent: invalid runtime metadata")
	}
	return &metadata, nil
}

func stableRuntimeInputHash[M adk.MessageType](
	input *adk.TypedAgentInput[M],
) ([]byte, error) {
	var value any = input
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		typed := any(input).(*adk.TypedAgentInput[*schema.Message])
		copyInput := &adk.AgentInput{EnableStreaming: typed.EnableStreaming}
		for _, message := range typed.Messages {
			if message == nil {
				continue
			}
			copyMessage := *message
			copyMessage.Extra = runtimeIdentityExtra(message.Extra)
			copyInput.Messages = append(copyInput.Messages, &copyMessage)
		}
		value = copyInput
	case *schema.AgenticMessage:
		typed := any(input).(*adk.TypedAgentInput[*schema.AgenticMessage])
		copyInput := &adk.TypedAgentInput[*schema.AgenticMessage]{
			EnableStreaming: typed.EnableStreaming,
		}
		for _, message := range typed.Messages {
			if message == nil {
				continue
			}
			copyMessage := *message
			copyMessage.Extra = runtimeIdentityExtra(message.Extra)
			copyInput.Messages = append(copyInput.Messages, &copyMessage)
		}
		value = copyInput
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf(
			"task/subagent: serialize runtime input identity: %w",
			err,
		)
	}
	hash := sha256.Sum256(data)
	return hash[:], nil
}

func runtimeIdentityExtra(extra map[string]any) map[string]any {
	if len(extra) == 0 {
		return nil
	}
	result := make(map[string]any, len(extra))
	for key, value := range extra {
		if key != adkinternal.EinoMsgIDKey {
			result[key] = value
		}
	}
	return result
}

func runtimeTurnLoopCheckpointID(runID string) string {
	return runID + "/turn_loop"
}

func deleteCheckpointBestEffort(
	ctx context.Context,
	store adk.CheckPointStore,
	checkpointID string,
) bool {
	if checkpointID == "" {
		return true
	}
	deleter, ok := store.(adk.CheckPointDeleter)
	if !ok {
		return false
	}
	return deleter.Delete(ctx, checkpointID) == nil
}

func runtimeForegroundResultCheckpointID(runID string) string {
	return runID + "/foreground_result"
}

func (r *Controller[M]) invalidateForegroundCandidate(
	ctx context.Context,
	runID string,
) error {
	marker, err := json.Marshal(&foregroundResultCheckpoint{
		Version: foregroundResultVersion,
		State:   foregroundResultInvalidated,
	})
	if err != nil {
		return err
	}
	checkpointID := runtimeForegroundResultCheckpointID(runID)
	if err = r.checkPointStore.Set(
		ctx,
		checkpointID,
		marker,
	); err != nil {
		return fmt.Errorf(
			"task/subagent: invalidate foreground result: %w",
			err,
		)
	}
	deleteCheckpointBestEffort(ctx, r.checkPointStore, checkpointID)
	return nil
}

func terminalTaskStatus(status background.Status) bool {
	return status == background.StatusCompleted ||
		status == background.StatusFailed ||
		status == background.StatusCanceled
}

func nilRuntimeMessage[M adk.MessageType](message M) bool {
	switch typed := any(message).(type) {
	case *schema.Message:
		return typed == nil
	case *schema.AgenticMessage:
		return typed == nil
	default:
		return true
	}
}

func joinErrors(primary, secondary error) error {
	if primary == nil {
		return secondary
	}
	if secondary == nil {
		return primary
	}
	return fmt.Errorf("%w: %v", primary, secondary)
}

func checkpointCursor(checkpoint []byte) int64 {
	if len(checkpoint) == 0 {
		return 0
	}
	envelope, err := decodeRuntimeCheckpointEnvelope(checkpoint)
	if err != nil {
		return 0
	}
	return envelope.InputCursor
}

type detachedRuntimeContext struct{ parent context.Context }

func (detachedRuntimeContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (detachedRuntimeContext) Done() <-chan struct{}       { return nil }
func (detachedRuntimeContext) Err() error                  { return nil }
func (c detachedRuntimeContext) Value(key any) any         { return c.parent.Value(key) }
