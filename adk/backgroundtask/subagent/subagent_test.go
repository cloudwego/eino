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
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/backgroundtask"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
)

type resumableTestAgent struct {
	name         string
	events       []*adk.AgentEvent
	eventFactory func(context.Context) *adk.AgentEvent
}

type interruptThenCompleteAgent struct {
	name string
}

type nextTurnAgent struct {
	name  string
	calls int
}

func (a *nextTurnAgent) Name(context.Context) string        { return a.name }
func (a *nextTurnAgent) Description(context.Context) string { return "next-turn agent" }
func (a *nextTurnAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.calls++
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	if a.calls == 1 {
		generator.Send(adk.Interrupt(ctx, "continue in another turn"))
	} else {
		generator.Send(adk.EventFromMessage(
			schema.AssistantMessage("next turn complete", nil), nil, schema.Assistant, a.name,
		))
	}
	generator.Close()
	return iter
}
func (a *nextTurnAgent) Resume(
	_ context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(&adk.AgentEvent{Err: errors.New("native resume must not be used")})
	generator.Close()
	return iter
}

func (a *interruptThenCompleteAgent) Name(context.Context) string { return a.name }
func (a *interruptThenCompleteAgent) Description(context.Context) string {
	return "interrupt then complete"
}
func (a *interruptThenCompleteAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.Interrupt(ctx, "approve"))
	generator.Close()
	return iter
}
func (a *interruptThenCompleteAgent) Resume(
	_ context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("approved", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}

func (a *resumableTestAgent) Name(context.Context) string        { return a.name }
func (a *resumableTestAgent) Description(context.Context) string { return "test agent" }
func (a *resumableTestAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	if len(a.events) == 0 {
		if a.eventFactory != nil {
			generator.Send(a.eventFactory(ctx))
		} else {
			generator.Send(adk.EventFromMessage(
				schema.AssistantMessage("done", nil), nil, schema.Assistant, a.name,
			))
		}
	}
	for _, event := range a.events {
		generator.Send(event)
	}
	generator.Close()
	return iter
}
func (a *resumableTestAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func exactRef(name string) AgentRef {
	return AgentRef{
		Namespace: "test", Name: name, Version: "v1",
		MessageType: "schema.Message", DefinitionDigest: "sha256:test-" + name,
	}
}

type runtimeStub struct {
	reportErr error
	updates   []*backgroundtask.ReportUpdateRequest
	controls  chan backgroundtask.ControlRequest
}

func newRuntimeStub() *runtimeStub {
	return &runtimeStub{controls: make(chan backgroundtask.ControlRequest, 1)}
}

func (r *runtimeStub) ReportUpdate(_ context.Context, req *backgroundtask.ReportUpdateRequest) error {
	r.updates = append(r.updates, req)
	return r.reportErr
}

func (r *runtimeStub) ReportCheckpoint(context.Context, backgroundtask.CheckpointRef) error {
	return nil
}

func (r *runtimeStub) Controls() <-chan backgroundtask.ControlRequest { return r.controls }

func TestAgentRegistryRequiresExactIdentity_BitsUT(t *testing.T) {
	registry := NewAgentRegistry[*schema.Message]()
	ref := exactRef("worker")
	agent := &resumableTestAgent{name: "worker"}
	require.NoError(t, registry.Register(ref, agent))
	assert.ErrorIs(t, registry.Register(ref, agent), backgroundtask.ErrAlreadyExists)

	resolved, err := registry.Resolve(ref)
	require.NoError(t, err)
	assert.Same(t, agent, resolved)

	for _, mutate := range []func(*AgentRef){
		func(candidate *AgentRef) { candidate.Name = "display-name-only" },
		func(candidate *AgentRef) { candidate.Version = "v2" },
		func(candidate *AgentRef) { candidate.DefinitionDigest = "sha256:other" },
		func(candidate *AgentRef) { candidate.Namespace = "other" },
	} {
		candidate := ref
		mutate(&candidate)
		_, err = registry.Resolve(candidate)
		require.Error(t, err)
	}

	wrongType := ref
	wrongType.MessageType = "schema.AgenticMessage"
	_, err = registry.Resolve(wrongType)
	require.Error(t, err)
}

func TestExecutorAdvertisesExactPayloadVersion_BitsUT(t *testing.T) {
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(&Executor[*schema.Message]{}))
	assert.Equal(t, []backgroundtask.ExecutorCapability{{
		ExecutorKey:    ExecutorKey,
		PayloadVersion: PayloadVersion,
	}}, executors.Capabilities())
}

func resumeFixture(t *testing.T, mode ResumeMode, allowEmpty bool, targets ...string) (*Executor[*schema.Message], backgroundtask.ValidateResumeRequest) {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agents := NewAgentRegistry[*schema.Message]()
	require.NoError(t, agents.Register(exactRef("worker"), &resumableTestAgent{name: "worker"}))
	state, err := json.Marshal(checkpointState{
		CheckpointID: "task/checkpoint", TargetIDs: targets,
		AllowEmpty: allowEmpty, Mode: mode,
	})
	require.NoError(t, err)
	payload, err := json.Marshal(TaskPayload{
		Agent: exactRef("worker"), Prompt: []byte("prompt"), PromptEncoding: "utf-8",
		ChildSessionID: "task/session", CheckpointID: "task/checkpoint",
		ResumeMode: mode, AllowEmptyResume: allowEmpty,
	})
	require.NoError(t, err)
	return &Executor[*schema.Message]{
			Agents: agents, CheckPointStore: store, SessionStore: store,
		}, backgroundtask.ValidateResumeRequest{
			Task: backgroundtask.Spec{
				ID: "task", ExecutorKey: ExecutorKey, PayloadVersion: PayloadVersion,
				Payload: payload,
			},
			Checkpoint: &backgroundtask.CheckpointRef{
				ExecutorKey: ExecutorKey, Format: "eino.runner", Version: "v1", Sequence: 1,
				State: inline(state, "application/json"),
			},
		}
}

func TestExecutorValidateResumeTargetsAndModes_BitsUT(t *testing.T) {
	t.Run("native exact target", func(t *testing.T) {
		executor, request := resumeFixture(t, ResumeNativeInterrupt, false, "approval")
		request.ResumeData = []byte(`{"approval":{"approved":true}}`)
		request.ResumeEncoding = "application/json"
		result, err := executor.ValidateResume(context.Background(), &request)
		require.NoError(t, err)
		assert.Equal(t, "application/json", result.NormalizedEncoding)
	})

	t.Run("native unknown target", func(t *testing.T) {
		executor, request := resumeFixture(t, ResumeNativeInterrupt, false, "approval")
		request.ResumeData = []byte(`{"other":"value"}`)
		request.ResumeEncoding = "application/json"
		_, err := executor.ValidateResume(context.Background(), &request)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not interrupted")
	})

	t.Run("empty acknowledgement policy", func(t *testing.T) {
		executor, request := resumeFixture(t, ResumeNativeInterrupt, false, "approval")
		_, err := executor.ValidateResume(context.Background(), &request)
		require.Error(t, err)

		executor, request = resumeFixture(t, ResumeNativeInterrupt, true, "approval")
		result, err := executor.ValidateResume(context.Background(), &request)
		require.NoError(t, err)
		assert.Empty(t, result.NormalizedData)
	})

	t.Run("next turn requires utf8", func(t *testing.T) {
		executor, request := resumeFixture(t, ResumeNextTurn, false)
		request.ResumeData = []byte("continue")
		request.ResumeEncoding = "utf-8"
		result, err := executor.ValidateResume(context.Background(), &request)
		require.NoError(t, err)
		assert.Equal(t, request.ResumeData, result.NormalizedData)

		request.ResumeEncoding = "application/json"
		_, err = executor.ValidateResume(context.Background(), &request)
		require.Error(t, err)
	})

	t.Run("checkpoint schema must match task", func(t *testing.T) {
		executor, request := resumeFixture(t, ResumeNativeInterrupt, true, "approval")
		request.Checkpoint.Format = "other"
		_, err := executor.ValidateResume(context.Background(), &request)
		require.Error(t, err)

		executor, request = resumeFixture(t, ResumeNativeInterrupt, true, "approval")
		var state checkpointState
		require.NoError(t, json.Unmarshal(request.Checkpoint.State.Payload, &state))
		state.Mode = ResumeNextTurn
		payload, marshalErr := json.Marshal(state)
		require.NoError(t, marshalErr)
		request.Checkpoint.State = inline(payload, "application/json")
		_, err = executor.ValidateResume(context.Background(), &request)
		require.Error(t, err)
	})
}

func TestSubmitPersistsIndependentChildIdentities_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agents := NewAgentRegistry[*schema.Message]()
	ref := exactRef("worker")
	require.NoError(t, agents.Register(ref, &resumableTestAgent{name: "worker"}))
	executor := &Executor[*schema.Message]{
		Agents: agents, CheckPointStore: sessionStore, SessionStore: sessionStore,
	}
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: executors})
	defer manager.Close(context.Background())

	task, err := Submit(context.Background(), manager, &SubmitRequest{
		Agent: ref, Prompt: "work", Description: "child work",
		SessionID: "parent-session", ResumeMode: ResumeNativeInterrupt,
	})
	require.NoError(t, err)
	var payload TaskPayload
	require.NoError(t, json.Unmarshal(task.Spec.Payload, &payload))
	assert.Equal(t, task.Spec.ID+"/session", payload.ChildSessionID)
	assert.Equal(t, task.Spec.ID+"/checkpoint", payload.CheckpointID)
	assert.Equal(t, "parent-session", task.Spec.SessionID)
	assert.Equal(t, task.Spec.SessionID, task.Spec.Notify.TargetID)

	reconstructed, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	var reconstructedPayload TaskPayload
	require.NoError(t, json.Unmarshal(reconstructed.Spec.Payload, &reconstructedPayload))
	assert.Equal(t, payload.ChildSessionID, reconstructedPayload.ChildSessionID)
	assert.Equal(t, payload.CheckpointID, reconstructedPayload.CheckpointID)
}

func executionFixture(
	t *testing.T,
	agent *resumableTestAgent,
) (*Executor[*schema.Message], backgroundtask.ExecutionRequest, *adksession.InMemoryStore[*schema.Message]) {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agents := NewAgentRegistry[*schema.Message]()
	ref := exactRef(agent.name)
	require.NoError(t, agents.Register(ref, agent))
	payload, err := json.Marshal(TaskPayload{
		Agent: ref, Prompt: []byte("work"), PromptEncoding: "utf-8",
		ChildSessionID: "task/session", CheckpointID: "task/checkpoint",
		ResumeMode: ResumeNativeInterrupt,
	})
	require.NoError(t, err)
	return &Executor[*schema.Message]{
			Agents: agents, CheckPointStore: store, SessionStore: store,
		}, backgroundtask.ExecutionRequest{
			Task: backgroundtask.Spec{
				ID: "task", ExecutorKey: ExecutorKey, PayloadVersion: PayloadVersion,
				Payload: payload,
				Result:  backgroundtask.ResultPolicy{ResultFormat: "eino.dev/subagent/result"},
			},
			Attempt: 1,
		}, store
}

func TestExecutorInterruptBecomesWaitingInput_BitsUT(t *testing.T) {
	agent := &resumableTestAgent{name: "worker", eventFactory: func(ctx context.Context) *adk.AgentEvent {
		return adk.Interrupt(ctx, "approve")
	}}
	executor, request, store := executionFixture(t, agent)
	runtime := newRuntimeStub()

	outcome := executor.Execute(context.Background(), request, runtime)
	assert.Equal(t, backgroundtask.OutcomeWaitingInput, outcome.Kind, "outcome error: %v", outcome.Err)
	require.NotNil(t, outcome.Checkpoint)
	require.NotNil(t, outcome.InputRequest)
	var state checkpointState
	require.NoError(t, json.Unmarshal(outcome.Checkpoint.State.Payload, &state))
	assert.Equal(t, "task/checkpoint", state.CheckpointID)
	require.Len(t, state.TargetIDs, 1)
	assert.NotEmpty(t, state.TargetIDs[0])
	assert.Empty(t, runtime.updates, "an interrupt is not a terminal message result")
	_, exists, err := store.Get(context.Background(), "task/checkpoint")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestExecutorUpdateBackpressureFailsOutcome_BitsUT(t *testing.T) {
	message := adk.EventFromMessage(
		schema.AssistantMessage("progress", nil), nil, schema.Assistant, "worker",
	)
	executor, request, _ := executionFixture(t, &resumableTestAgent{
		name: "worker", events: []*adk.AgentEvent{message},
	})
	runtime := newRuntimeStub()
	runtime.reportErr = assert.AnError

	outcome := executor.Execute(context.Background(), request, runtime)
	assert.Equal(t, backgroundtask.OutcomeFailed, outcome.Kind)
	assert.ErrorIs(t, outcome.Err, assert.AnError)
	require.Len(t, runtime.updates, 1)
}

func TestExecutorDrainUsesDurableRunnerCheckpoint_BitsUT(t *testing.T) {
	agent := &resumableTestAgent{name: "worker", eventFactory: func(ctx context.Context) *adk.AgentEvent {
		return adk.Interrupt(ctx, "pause for drain")
	}}
	executor, request, _ := executionFixture(t, agent)
	runtime := newRuntimeStub()
	runtime.controls <- backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain}

	outcome := executor.Execute(context.Background(), request, runtime)
	assert.Equal(t, backgroundtask.OutcomeSuspended, outcome.Kind, "outcome error: %v", outcome.Err)
	require.NotNil(t, outcome.Checkpoint)
	assert.Equal(t, int64(1), outcome.Checkpoint.Sequence)
}

func TestNextTurnResumeMarkerRoundTrip_BitsUT(t *testing.T) {
	message, err := resumeMessage[*schema.Message]("continue", "task:1")
	require.NoError(t, err)
	assert.Equal(t, "task:1", messageResumeMarker(message))
	assert.Equal(t, "continue", message.Content)
}

func TestSubAgentTaskResumesAfterManagerReconstruction_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agents := NewAgentRegistry[*schema.Message]()
	ref := exactRef("worker")
	require.NoError(t, agents.Register(ref, &interruptThenCompleteAgent{name: "worker"}))
	executor := &Executor[*schema.Message]{
		Agents: agents, CheckPointStore: sessionStore, SessionStore: sessionStore,
	}
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	taskStore := backgroundtask.NewMemoryStore(nil)

	manager1 := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Store: taskStore, Executors: executors, WorkerID: "worker-1",
	})
	task, err := Submit(context.Background(), manager1, &SubmitRequest{
		Agent: ref, Prompt: "do work", Description: "durable child",
		SessionID: "parent-session", ResumeMode: ResumeNativeInterrupt,
	})
	require.NoError(t, err)
	require.NoError(t, manager1.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager1.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, waiting.Status)
	require.NotNil(t, waiting.Checkpoint)
	var state checkpointState
	require.NoError(t, json.Unmarshal(waiting.Checkpoint.State.Payload, &state))
	require.Len(t, state.TargetIDs, 1)
	require.NoError(t, manager1.Close(context.Background()))

	manager2 := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Store: taskStore, Executors: executors, WorkerID: "worker-2",
	})
	defer manager2.Close(context.Background())
	resumeData, err := json.Marshal(map[string]any{
		state.TargetIDs[0]: map[string]any{"approved": true},
	})
	require.NoError(t, err)
	pending, err := manager2.ResumeTask(context.Background(), &backgroundtask.ResumeTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.TransitionVersion,
		ResumeData: resumeData, ResumeEncoding: "application/json",
	})
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusPending, pending.Status)
	require.NoError(t, manager2.Execute(context.Background(), task.Spec.ID))

	completed, err := manager2.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusCompleted, completed.Status)
	require.NotNil(t, completed.ResultRef)
	assert.Contains(t, string(completed.ResultRef.Value.Payload), "approved")
	assert.Equal(t, int64(2), completed.Attempt)

	var persisted TaskPayload
	require.NoError(t, json.Unmarshal(completed.Spec.Payload, &persisted))
	assert.Equal(t, task.Spec.ID+"/session", persisted.ChildSessionID)
	assert.Equal(t, task.Spec.ID+"/checkpoint", persisted.CheckpointID)
}

func TestSubAgentTaskContinuesSameSessionWithoutDuplicateResumeInput_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &nextTurnAgent{name: "worker"}
	agents := NewAgentRegistry[*schema.Message]()
	ref := exactRef("worker")
	require.NoError(t, agents.Register(ref, agent))
	executor := &Executor[*schema.Message]{
		Agents: agents, CheckPointStore: sessionStore, SessionStore: sessionStore,
	}
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Executors: executors, WorkerID: "worker",
	})
	defer manager.Close(context.Background())
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		Agent: ref, Prompt: "start", Description: "multi turn",
		SessionID: "parent-session", ResumeMode: ResumeNextTurn,
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, waiting.Status)

	pending, err := manager.ResumeTask(context.Background(), &backgroundtask.ResumeTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.TransitionVersion,
		ResumeData: []byte("continue"), ResumeEncoding: "utf-8",
	})
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusPending, pending.Status)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	completed, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusCompleted, completed.Status)

	var payload TaskPayload
	require.NoError(t, json.Unmarshal(completed.Spec.Payload, &payload))
	marker := fmt.Sprintf("%s:%d", completed.Spec.ID, waiting.Checkpoint.Sequence)
	assert.True(t, hasMarkerInSession(t, sessionStore, payload.ChildSessionID, marker))

	// Re-entering the same recovered next-turn request must not append the user
	// input marker a second time.
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.Message]{
		Agent: agent, CheckPointStore: sessionStore,
		SessionID: payload.ChildSessionID, SessionStore: sessionStore,
	})
	iter, err := executor.runNextTurn(context.Background(), runner, backgroundtask.ExecutionRequest{
		Task: completed.Spec, Checkpoint: waiting.Checkpoint,
		ResumeData: []byte("continue"), ResumeEncoding: "utf-8",
	}, &payload)
	require.NoError(t, err)
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}
	assert.Equal(t, 1, countMarkerInSession(t, sessionStore, payload.ChildSessionID, marker))
}

func hasMarkerInSession(
	t *testing.T,
	store adk.SessionEventStore[*schema.Message],
	sessionID, marker string,
) bool {
	t.Helper()
	return countMarkerInSession(t, store, sessionID, marker) > 0
}

func countMarkerInSession(
	t *testing.T,
	store adk.SessionEventStore[*schema.Message],
	sessionID, marker string,
) int {
	t.Helper()
	after := ""
	count := 0
	for {
		page, err := store.LoadEvents(context.Background(), sessionID, &adk.LoadSessionEventsRequest{
			After: after, Limit: 100,
		})
		require.NoError(t, err)
		for _, event := range page.Events {
			if event != nil && messageResumeMarker(event.Message) == marker {
				count++
			}
		}
		if page.Next == "" || page.Next == after || len(page.Events) == 0 {
			return count
		}
		after = page.Next
	}
}
