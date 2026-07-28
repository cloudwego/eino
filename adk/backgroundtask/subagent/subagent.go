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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/schema"
)

const (
	ExecutorKey    = "eino.dev/subagent"
	PayloadVersion = "v1"
)

type ResumeMode string

const (
	ResumeNativeInterrupt ResumeMode = "native_interrupt"
	ResumeNextTurn        ResumeMode = "next_turn"
)

type AgentRef struct {
	Namespace        string `json:"namespace"`
	Name             string `json:"name"`
	Version          string `json:"version"`
	MessageType      string `json:"message_type"`
	DefinitionDigest string `json:"definition_digest"`
}

type TaskPayload struct {
	Agent            AgentRef   `json:"agent"`
	Prompt           []byte     `json:"prompt"`
	PromptEncoding   string     `json:"prompt_encoding"`
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
}

type AgentRegistry[M adk.MessageType] struct {
	mu     sync.RWMutex
	agents map[string]adk.TypedResumableAgent[M]
}

func NewAgentRegistry[M adk.MessageType]() *AgentRegistry[M] {
	return &AgentRegistry[M]{agents: make(map[string]adk.TypedResumableAgent[M])}
}

func (r *AgentRegistry[M]) Register(ref AgentRef, agent adk.TypedResumableAgent[M]) error {
	if err := validateAgentRef[M](ref); err != nil {
		return err
	}
	if agent == nil {
		return errors.New("backgroundtask/subagent: agent is required")
	}
	key := agentKey(ref)
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.agents[key]; exists {
		return backgroundtask.ErrAlreadyExists
	}
	r.agents[key] = agent
	return nil
}

func (r *AgentRegistry[M]) Resolve(ref AgentRef) (adk.TypedResumableAgent[M], error) {
	if err := validateAgentRef[M](ref); err != nil {
		return nil, err
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	agent, ok := r.agents[agentKey(ref)]
	if !ok {
		return nil, errors.New("backgroundtask/subagent: exact agent definition is unavailable")
	}
	return agent, nil
}

func agentKey(ref AgentRef) string {
	return ref.Namespace + "\x00" + ref.Name + "\x00" + ref.Version + "\x00" + ref.MessageType + "\x00" + ref.DefinitionDigest
}

func validateAgentRef[M adk.MessageType](ref AgentRef) error {
	if ref.Namespace == "" || ref.Name == "" || ref.Version == "" || ref.MessageType == "" || ref.DefinitionDigest == "" {
		return errors.New("backgroundtask/subagent: all agent identity fields are required")
	}
	if ref.MessageType != messageType[M]() {
		return fmt.Errorf("backgroundtask/subagent: message type %q does not match %q", ref.MessageType, messageType[M]())
	}
	return nil
}

func messageType[M adk.MessageType]() string {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return "schema.Message"
	default:
		return "schema.AgenticMessage"
	}
}

type Executor[M adk.MessageType] struct {
	Agents          *AgentRegistry[M]
	CheckPointStore adk.CheckPointStore
	SessionStore    adk.SessionEventStore[M]
}

func (e *Executor[M]) Key() string { return ExecutorKey }
func (e *Executor[M]) Capabilities() []backgroundtask.ExecutorCapability {
	return []backgroundtask.ExecutorCapability{{
		ExecutorKey:    ExecutorKey,
		PayloadVersion: PayloadVersion,
	}}
}

func (e *Executor[M]) Validate(spec backgroundtask.Spec) error {
	if e == nil || e.Agents == nil || e.CheckPointStore == nil || e.SessionStore == nil {
		return errors.New("backgroundtask/subagent: agent registry, checkpoint store, and session store are required")
	}
	payload, err := validateSpecPayload(spec)
	if err != nil {
		return err
	}
	_, err = e.Agents.Resolve(payload.Agent)
	return err
}

func validateSpecPayload(spec backgroundtask.Spec) (*TaskPayload, error) {
	if spec.PayloadVersion != PayloadVersion {
		return nil, errors.New("backgroundtask/subagent: unsupported payload version")
	}
	payload, err := decodePayload(spec)
	if err != nil {
		return nil, err
	}
	if payload.ChildSessionID == "" || payload.CheckpointID == "" ||
		payload.ChildSessionID != spec.ID+"/session" || payload.CheckpointID != spec.ID+"/checkpoint" {
		return nil, errors.New("backgroundtask/subagent: child identities must be persisted in the task namespace")
	}
	if payload.PromptEncoding != "utf-8" {
		return nil, errors.New("backgroundtask/subagent: unsupported prompt encoding")
	}
	if payload.ResumeMode != ResumeNativeInterrupt && payload.ResumeMode != ResumeNextTurn {
		return nil, errors.New("backgroundtask/subagent: unsupported resume mode")
	}
	return payload, nil
}

func (e *Executor[M]) ValidateCheckpoint(spec backgroundtask.Spec, checkpoint *backgroundtask.CheckpointRef) error {
	payload, err := validateSpecPayload(spec)
	if err != nil {
		return err
	}
	if checkpoint == nil ||
		checkpoint.ExecutorKey != ExecutorKey ||
		checkpoint.Format != "eino.runner" ||
		checkpoint.Version != "v1" ||
		checkpoint.State.Payload == nil ||
		checkpoint.State.Ref != nil {
		return errors.New("backgroundtask/subagent: compatible checkpoint is required")
	}
	var state checkpointState
	if err = json.Unmarshal(checkpoint.State.Payload, &state); err != nil ||
		state.CheckpointID == "" ||
		state.CheckpointID != payload.CheckpointID ||
		state.Mode != payload.ResumeMode ||
		state.AllowEmpty != payload.AllowEmptyResume {
		return errors.New("backgroundtask/subagent: checkpoint state does not match task")
	}
	return nil
}

func (e *Executor[M]) ValidateResume(_ context.Context, req *backgroundtask.ValidateResumeRequest) (*backgroundtask.ValidateResumeResult, error) {
	if req == nil {
		return nil, errors.New("backgroundtask/subagent: compatible checkpoint is required")
	}
	if err := e.Validate(req.Task); err != nil {
		return nil, err
	}
	if err := e.ValidateCheckpoint(req.Task, req.Checkpoint); err != nil {
		return nil, err
	}
	var state checkpointState
	if err := json.Unmarshal(req.Checkpoint.State.Payload, &state); err != nil {
		return nil, err
	}
	if len(req.ResumeData) == 0 {
		if !state.AllowEmpty {
			return nil, errors.New("backgroundtask/subagent: this checkpoint requires targeted resume data")
		}
		return &backgroundtask.ValidateResumeResult{}, nil
	}
	if state.Mode == ResumeNextTurn {
		if req.ResumeEncoding != "utf-8" {
			return nil, errors.New("backgroundtask/subagent: next-turn input must be utf-8")
		}
		return &backgroundtask.ValidateResumeResult{
			NormalizedData: append([]byte(nil), req.ResumeData...), NormalizedEncoding: "utf-8",
		}, nil
	}
	if req.ResumeEncoding != "application/json" {
		return nil, errors.New("backgroundtask/subagent: native resume data must be application/json")
	}
	var targets map[string]any
	if err := json.Unmarshal(req.ResumeData, &targets); err != nil || len(targets) == 0 {
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
	return &backgroundtask.ValidateResumeResult{NormalizedData: normalized, NormalizedEncoding: "application/json"}, nil
}

func (e *Executor[M]) Execute(ctx context.Context, req backgroundtask.ExecutionRequest, runtime backgroundtask.Runtime) backgroundtask.Outcome {
	payload, err := decodePayload(req.Task)
	if err != nil {
		return failed(err)
	}
	agent, err := e.Agents.Resolve(payload.Agent)
	if err != nil {
		return failed(err)
	}
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{
		Agent: agent, CheckPointStore: e.CheckPointStore,
		SessionID: payload.ChildSessionID, SessionStore: e.SessionStore,
	})
	cancelOption, cancelRun := adk.WithCancel()
	controlKinds := make(chan backgroundtask.ControlKind, 1)
	controlWatchDone := make(chan struct{})
	defer close(controlWatchDone)
	go func() {
		select {
		case control := <-runtime.Controls():
			controlKinds <- control.Kind
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
	var iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]]
	if req.Checkpoint != nil {
		if payload.ResumeMode == ResumeNextTurn {
			iter, err = e.runNextTurn(ctx, runner, req, payload, cancelOption)
		} else if len(req.ResumeData) == 0 {
			iter, err = runner.Resume(ctx, payload.CheckpointID, cancelOption)
		} else {
			var targets map[string]any
			if unmarshalErr := json.Unmarshal(req.ResumeData, &targets); unmarshalErr != nil {
				return failed(unmarshalErr)
			}
			iter, err = runner.ResumeWithParams(
				ctx, payload.CheckpointID, &adk.ResumeParams{Targets: targets}, cancelOption,
			)
		}
	} else {
		iter = runner.Query(
			ctx, string(payload.Prompt), adk.WithCheckPointID(payload.CheckpointID), cancelOption,
		)
	}
	if err != nil {
		return failed(err)
	}

	var final []byte
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
			control := pollControl(controlKinds)
			var cancelError *adk.CancelError
			if control == "" && (errors.Is(event.Err, context.Canceled) || errors.As(event.Err, &cancelError)) {
				select {
				case control = <-controlKinds:
				case <-ctx.Done():
				case <-time.After(100 * time.Millisecond):
				}
			}
			if control != "" {
				for {
					if _, open := iter.Next(); !open {
						break
					}
				}
			}
			if controlled := e.controlOutcome(req, payload, control); controlled != nil {
				return *controlled
			}
			return failed(event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, messageErr := event.Output.MessageOutput.GetMessage()
		if messageErr != nil {
			return failed(messageErr)
		}
		data, marshalErr := json.Marshal(message)
		if marshalErr != nil {
			return failed(marshalErr)
		}
		final = data
		if reportErr := runtime.ReportUpdate(ctx, &backgroundtask.ReportUpdateRequest{
			Kind:    backgroundtask.UpdateMessage,
			Payload: &backgroundtask.UpdatePayload{Type: "eino.dev/subagent/message", Value: inline(data, "application/json")},
		}); reportErr != nil {
			return failed(reportErr)
		}
	}
	if interrupted != nil {
		if _, exists, getErr := e.CheckPointStore.Get(ctx, payload.CheckpointID); getErr != nil || !exists {
			if getErr == nil {
				getErr = errors.New("backgroundtask/subagent: runner checkpoint is missing")
			}
			return failed(getErr)
		}
		request, _ := json.Marshal(interrupted)
		state := checkpointState{
			CheckpointID: payload.CheckpointID, Mode: payload.ResumeMode,
			AllowEmpty: payload.AllowEmptyResume,
		}
		for _, interruptContext := range interrupted.InterruptContexts {
			if interruptContext.ID != "" {
				state.TargetIDs = append(state.TargetIDs, interruptContext.ID)
			}
		}
		stateBytes, stateErr := json.Marshal(state)
		if stateErr != nil || len(state.TargetIDs) == 0 {
			if stateErr == nil {
				stateErr = errors.New("backgroundtask/subagent: interrupt has no resumable targets")
			}
			return failed(stateErr)
		}
		return backgroundtask.Outcome{
			Kind: backgroundtask.OutcomeWaitingInput,
			Checkpoint: &backgroundtask.CheckpointRef{
				ExecutorKey: ExecutorKey, Format: "eino.runner", Version: "v1", Sequence: checkpointSequence(req.Checkpoint),
				State: inline(stateBytes, "application/json"), CreatedAt: time.Now(),
			},
			InputRequest: &backgroundtask.UpdatePayload{
				Type: "eino.dev/subagent/input-required", Value: inline(request, "application/json"),
			},
		}
	}
	if final == nil {
		if controlled := e.controlOutcome(req, payload, pollControl(controlKinds)); controlled != nil {
			return *controlled
		}
	}
	if final == nil {
		final = []byte("null")
	}
	return backgroundtask.Outcome{
		Kind: backgroundtask.OutcomeCompleted,
		Result: &backgroundtask.ResultRef{
			Format: req.Task.Result.ResultFormat, Value: inline(final, "application/json"), CreatedAt: time.Now(),
		},
	}
}

func (e *Executor[M]) controlOutcome(
	req backgroundtask.ExecutionRequest,
	payload *TaskPayload,
	control backgroundtask.ControlKind,
) *backgroundtask.Outcome {
	switch control {
	case backgroundtask.ControlStop:
		outcome := backgroundtask.Outcome{
			Kind: backgroundtask.OutcomeCanceled, TerminalReason: "canceled",
		}
		return &outcome
	case backgroundtask.ControlDrain:
		if _, exists, err := e.CheckPointStore.Get(context.Background(), payload.CheckpointID); err != nil || !exists {
			if err == nil {
				err = errors.New("runner checkpoint is missing")
			}
			outcome := failed(fmt.Errorf("%w: %v", backgroundtask.ErrCheckpointUnavailable, err))
			return &outcome
		}
		stateBytes, err := json.Marshal(checkpointState{
			CheckpointID: payload.CheckpointID, Mode: payload.ResumeMode,
			AllowEmpty: payload.AllowEmptyResume,
		})
		if err != nil {
			outcome := failed(err)
			return &outcome
		}
		outcome := backgroundtask.Outcome{
			Kind: backgroundtask.OutcomeSuspended,
			Checkpoint: &backgroundtask.CheckpointRef{
				ExecutorKey: ExecutorKey, Format: "eino.runner", Version: "v1",
				Sequence: checkpointSequence(req.Checkpoint),
				State:    inline(stateBytes, "application/json"), CreatedAt: time.Now(),
			},
		}
		return &outcome
	}
	return nil
}

func pollControl(controls <-chan backgroundtask.ControlKind) backgroundtask.ControlKind {
	select {
	case control := <-controls:
		return control
	default:
		return ""
	}
}

const resumeMarkerKey = "eino.dev/backgroundtask_resume"

func (e *Executor[M]) runNextTurn(
	ctx context.Context,
	runner *adk.TypedRunner[M],
	req backgroundtask.ExecutionRequest,
	payload *TaskPayload,
	options ...adk.AgentRunOption,
) (*adk.AsyncIterator[*adk.TypedAgentEvent[M]], error) {
	marker := fmt.Sprintf("%s:%d", req.Task.ID, req.Checkpoint.Sequence)
	seen, err := e.hasResumeMarker(ctx, payload.ChildSessionID, marker)
	if err != nil {
		return nil, err
	}
	var messages []M
	if !seen {
		message, messageErr := resumeMessage[M](string(req.ResumeData), marker)
		if messageErr != nil {
			return nil, messageErr
		}
		messages = []M{message}
	}
	options = append(options, adk.WithCheckPointID(payload.CheckpointID))
	return runner.Run(ctx, messages, options...), nil
}

func (e *Executor[M]) hasResumeMarker(ctx context.Context, sessionID, marker string) (bool, error) {
	after := ""
	for {
		page, err := e.SessionStore.LoadEvents(ctx, sessionID, &adk.LoadSessionEventsRequest{
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

func checkpointSequence(previous *backgroundtask.CheckpointRef) int64 {
	if previous == nil {
		return 1
	}
	return previous.Sequence + 1
}

func inline(payload []byte, encoding string) backgroundtask.ArtifactValue {
	sum := sha256.Sum256(payload)
	cloned := make([]byte, len(payload))
	copy(cloned, payload)
	return backgroundtask.ArtifactValue{
		Payload: cloned, Encoding: encoding,
		Digest: "sha256:" + hex.EncodeToString(sum[:]), Size: int64(len(payload)),
	}
}

func failed(err error) backgroundtask.Outcome {
	return backgroundtask.Outcome{Kind: backgroundtask.OutcomeFailed, Err: err}
}

type SubmitRequest struct {
	Agent            AgentRef
	Prompt           string
	Description      string
	SessionID        string
	ToolUseID        string
	ResumeMode       ResumeMode
	AllowEmptyResume bool
}

func Submit(ctx context.Context, manager *backgroundtask.Manager, req *SubmitRequest) (*backgroundtask.Task, error) {
	if manager == nil || req == nil || req.SessionID == "" {
		return nil, errors.New("backgroundtask/subagent: manager, request, and parent session id are required")
	}
	id, err := manager.AllocateTaskID(ctx)
	if err != nil {
		return nil, err
	}
	payload := TaskPayload{
		Agent: req.Agent, Prompt: []byte(req.Prompt), PromptEncoding: "utf-8",
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
		ID: id, ExecutorKey: ExecutorKey, PayloadVersion: PayloadVersion,
		Payload: data,
		Type:    "subagent", Description: req.Description, SessionID: req.SessionID,
		ToolUseID: req.ToolUseID,
		Notify:    &backgroundtask.NotificationTarget{Kind: "session_inbox", TargetID: req.SessionID},
		Recovery: backgroundtask.RecoveryPolicy{
			OnLeaseExpired:      backgroundtask.RecoveryResumeCheckpoint,
			OnMissingCheckpoint: backgroundtask.RecoveryFail, MaxAttempts: 3,
		},
		Result: backgroundtask.ResultPolicy{ResultFormat: "eino.dev/subagent/result"},
	})
}
