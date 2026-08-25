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

// Package subagent provides foreground and background Task implementations for ADK sub-agent runs.
package subagent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

// RuntimeSessionStoreAccessMode identifies the authority under which the
// runtime accesses a child session store.
type RuntimeSessionStoreAccessMode uint8

const (
	// RuntimeSessionStoreAccessUnknown is the invalid zero value.
	RuntimeSessionStoreAccessUnknown RuntimeSessionStoreAccessMode = iota
	// RuntimeSessionStoreAccessForegroundExecute permits a caller-owned
	// foreground execution to read and write. Task must be nil.
	RuntimeSessionStoreAccessForegroundExecute
	// RuntimeSessionStoreAccessManagedExecute permits a Manager-owned execution
	// attempt to read and write. Task must contain the current attempt snapshot.
	RuntimeSessionStoreAccessManagedExecute
	// RuntimeSessionStoreAccessReadProgress requests read-only progress access.
	// Task must contain the snapshot being projected.
	RuntimeSessionStoreAccessReadProgress
)

// RuntimeSessionStoreRequest identifies one TurnLoop runtime session access.
// ParentSessionID is the child's direct parent session, including for deeply
// nested tasks. Implementations may use it to enforce child-session ownership.
// AccessMode is the sole execution-authority discriminator and determines
// whether Task must be nil or non-nil.
type RuntimeSessionStoreRequest struct {
	TaskID          string
	ParentSessionID string
	ChildSessionID  string
	Task            *background.TaskSnapshot
	AccessMode      RuntimeSessionStoreAccessMode
}

// RuntimeSessionStoreFactory constructs a session store for foreground execution,
// managed execution attempts, and progress reads of one logical task.
type RuntimeSessionStoreFactory[M adk.MessageType] func(
	context.Context,
	*RuntimeSessionStoreRequest,
) (adk.SessionEventStore[M], error)

const (
	// ExecutorKey identifies the TurnLoop task runtime protocol for durable
	// sub-agent tasks. Its payload versions are scoped to this persisted key;
	// version 1 is not compatible with the legacy eino.dev/subagent payload v4.
	ExecutorKey = "eino.dev/task-subagent"

	// payloadVersion is the first payload version under ExecutorKey.
	payloadVersion          = 1
	maxChildSessionIDLength = 1024
	taskIDEventExtraKey     = "eino.task.id"
)

type taskPayload struct {
	Version        int    `json:"version"`
	SubAgentName   string `json:"subagent_name"`
	ChildSessionID string `json:"child_session_id"`
}

type serializedTypedInput struct {
	Messages        json.RawMessage `json:"messages"`
	EnableStreaming bool            `json:"enable_streaming,omitempty"`
}

// RunOptionsFactory reconstructs deployment-owned run options for each task
// attempt. It may be called concurrently and must return fresh option values.
type RunOptionsFactory func() ([]adk.AgentRunOption, error)

// AgentRegistration binds a persisted name to one resumable agent.
type AgentRegistration[M adk.MessageType] struct {
	Agent             adk.TypedResumableAgent[M]
	RunOptionsFactory RunOptionsFactory
}

// executor dispatches background sub-agent attempts to its Controller.
type executor[M adk.MessageType] struct {
	mu            sync.RWMutex
	registrations map[string]*AgentRegistration[M]
	controller    *Controller[M]
}

func newExecutor[M adk.MessageType](controller *Controller[M]) *executor[M] {
	return &executor[M]{controller: controller}
}

// Key returns the background executor key for sub-agent tasks.
func (*executor[M]) Key() string { return ExecutorKey }

// LeaseExpiryPolicy allows another worker to resume a lost sub-agent attempt.
func (*executor[M]) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	return background.LeaseExpiryRetry
}

func (e *executor[M]) register(name string, registration *AgentRegistration[M]) error {
	if e == nil || name == "" || registration == nil || registration.Agent == nil {
		return errors.New("task/subagent: agent name and implementation are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.registrations == nil {
		e.registrations = make(map[string]*AgentRegistration[M])
	}
	if _, exists := e.registrations[name]; exists {
		return background.ErrAlreadyExists
	}
	copy := *registration
	e.registrations[name] = &copy
	return nil
}

func (e *executor[M]) resolveRegistration(name string) (*AgentRegistration[M], error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	registration, ok := e.registrations[name]
	if !ok {
		return nil, fmt.Errorf("task/subagent: agent %q is unavailable", name)
	}
	copy := *registration
	return &copy, nil
}

func (e *executor[M]) resolveAgent(name string) (adk.TypedResumableAgent[M], error) {
	registration, err := e.resolveRegistration(name)
	if err != nil {
		return nil, err
	}
	return registration.Agent, nil
}

// ValidateSpec verifies that spec contains a Controller payload.
func (e *executor[M]) ValidateSpec(spec background.Spec) error {
	payload, err := validateSpecPayload(spec)
	if err != nil {
		return err
	}
	if e == nil || e.controller == nil {
		return errors.New("task/subagent: controller is unavailable")
	}
	_, err = e.resolveRegistration(payload.SubAgentName)
	return err
}

// ValidateExecution verifies worker dependencies without mutating external state.
func (e *executor[M]) ValidateExecution(_ context.Context, task *background.TaskSnapshot) error {
	if task == nil {
		return errors.New("task/subagent: task is required")
	}
	return e.ValidateSpec(task.Spec)
}

// SupportsDrain reports true because Controller checkpoints every turn boundary.
func (*executor[M]) SupportsDrain() bool { return true }

// AcknowledgeCancellation runs domain cleanup after Manager persists cancel intent.
func (e *executor[M]) AcknowledgeCancellation(
	ctx context.Context,
	task *background.TaskSnapshot,
	reason string,
) error {
	if task == nil {
		return errors.New("task/subagent: task is required")
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return err
	}
	if e == nil || e.controller == nil {
		return errors.New("task/subagent: controller is unavailable")
	}
	if e.controller.cancellationHook == nil {
		return nil
	}
	return e.controller.cancellationHook.OnCancel(
		ctx, task.Spec.ID, payload.ChildSessionID, reason,
	)
}

// Execute delegates a background attempt to the shared Controller activation.
func (e *executor[M]) Execute(
	ctx context.Context,
	task *background.TaskSnapshot,
	runtime background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	if task == nil {
		return nil, errors.New("task/subagent: task is required")
	}
	payload, err := validateSpecPayload(task.Spec)
	if err != nil {
		return nil, err
	}
	if e == nil || e.controller == nil {
		return nil, errors.New("task/subagent: controller is unavailable")
	}
	return e.controller.executeTask(ctx, task, runtime, payload)
}

func validateSpecPayload(spec background.Spec) (*taskPayload, error) {
	if spec.ExecutorKey != ExecutorKey || spec.Kind != "subagent" {
		return nil, errors.New("task/subagent: invalid executor key or task kind")
	}
	payload, err := decodePayload(spec)
	if err != nil {
		return nil, err
	}
	if payload.Version != payloadVersion {
		return nil, fmt.Errorf(
			"%w: subagent payload version %d",
			background.ErrUnsupportedExecutorPayloadVersion,
			payload.Version,
		)
	}
	if payload.SubAgentName == "" || payload.ChildSessionID == "" {
		return nil, errors.New(
			"task/subagent: agent name and child session ID are required",
		)
	}
	if len(payload.ChildSessionID) > maxChildSessionIDLength {
		return nil, errors.New(
			"task/subagent: child session ID exceeds configured bounds",
		)
	}
	return payload, nil
}

func decodePayload(spec background.Spec) (*taskPayload, error) {
	var payload taskPayload
	if err := json.Unmarshal(spec.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}

func sessionConfigForTask[M adk.MessageType](
	baseConfig *adk.SessionConfig[M],
	taskID string,
) *adk.SessionConfig[M] {
	config := &adk.SessionConfig[M]{}
	if baseConfig != nil {
		*config = *baseConfig
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
			err = errors.New("task/subagent: resume targets contain trailing data")
		}
		return nil, err
	}
	return targets, nil
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
		return nil, fmt.Errorf("task/subagent: serialize typed input: %w", err)
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
		return nil, errors.New("task/subagent: typed input is required")
	}
	var decoded any
	if err := (&schema.HumanReadableSerializer{}).Unmarshal(
		encoded.Messages,
		&decoded,
	); err != nil {
		return nil, fmt.Errorf("task/subagent: deserialize typed input: %w", err)
	}
	messages, ok := decoded.([]M)
	if !ok {
		return nil, errors.New(
			"task/subagent: typed input message type does not match executor",
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
		return errors.New("task/subagent: typed input messages are required")
	}
	var zero M
	for _, message := range input.Messages {
		if any(message) == any(zero) {
			return errors.New("task/subagent: typed input contains a nil message")
		}
	}
	return nil
}
