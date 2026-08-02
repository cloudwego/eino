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
	"github.com/cloudwego/eino/adk/filesystem"
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

type cancelThenMessageAgent struct {
	name    string
	started chan struct{}
	release chan struct{}
}

type registrationRunOptions struct {
	value string
}

type optionCaptureAgent struct {
	name string
	seen []string
}

func (a *optionCaptureAgent) Name(context.Context) string        { return a.name }
func (a *optionCaptureAgent) Description(context.Context) string { return "option capture" }
func (a *optionCaptureAgent) Run(
	_ context.Context,
	_ *adk.AgentInput,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	resolved := adk.GetImplSpecificOptions[registrationRunOptions](nil, options...)
	a.seen = append(a.seen, resolved.value)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("done", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}
func (a *optionCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (a *cancelThenMessageAgent) Name(context.Context) string        { return a.name }
func (a *cancelThenMessageAgent) Description(context.Context) string { return "cancel then message" }
func (a *cancelThenMessageAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		close(a.started)
		<-a.release
		generator.Send(adk.EventFromMessage(
			schema.AssistantMessage("late completion", nil), nil, schema.Assistant, a.name,
		))
		generator.Close()
	}()
	return iter
}
func (a *cancelThenMessageAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
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
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("before interrupt", nil), nil, schema.Assistant, a.name,
	))
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

func TestExecutorRegistersAgentsByStableName_BitsUT(t *testing.T) {
	executor := &Executor[*schema.Message]{}
	agent := &resumableTestAgent{name: "worker"}
	require.NoError(t, executor.RegisterAgent("worker", agent))
	assert.ErrorIs(t, executor.RegisterAgent("worker", agent), backgroundtask.ErrAlreadyExists)

	resolved, err := executor.resolveAgent("worker")
	require.NoError(t, err)
	assert.Same(t, agent, resolved)

	_, err = executor.resolveAgent("other")
	require.Error(t, err)
}

func TestExecutorRegistryListsExecutorKey_BitsUT(t *testing.T) {
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(&Executor[*schema.Message]{}))
	assert.Equal(t, []string{ExecutorKey}, executors.Keys())
}

func resumeFixture(
	t *testing.T,
	mode ResumeMode,
	allowEmpty bool,
	targets ...string,
) (*Executor[*schema.Message], backgroundtask.Spec, []byte) {
	t.Helper()
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.RegisterAgent("worker", &resumableTestAgent{name: "worker"}))
	state, err := json.Marshal(checkpointState{
		CheckpointID: "task/checkpoint", TargetIDs: targets,
		AllowEmpty: allowEmpty, Mode: mode, Sequence: 1,
	})
	require.NoError(t, err)
	payload, err := json.Marshal(TaskPayload{
		Version: payloadVersion, SubAgentName: "worker", Prompt: "prompt",
		ChildSessionID: "task/session", CheckpointID: "task/checkpoint",
		ResumeMode: mode, AllowEmptyResume: allowEmpty,
	})
	require.NoError(t, err)
	return executor, backgroundtask.Spec{
		ID: "task", ExecutorKey: ExecutorKey, Kind: "subagent", Payload: payload,
		LeaseExpiryPolicy: backgroundtask.LeaseExpiryRetry,
	}, state
}

func TestExecutorValidateResumeTargetsAndModes_BitsUT(t *testing.T) {
	t.Run("native exact target", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, ResumeNativeInterrupt, false, "approval")
		result, err := executor.ValidateResume(
			context.Background(), spec, checkpoint, []byte(`{"approval":{"approved":true}}`),
		)
		require.NoError(t, err)
		assert.JSONEq(t, `{"approval":{"approved":true}}`, string(result))
	})

	t.Run("native unknown target", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, ResumeNativeInterrupt, false, "approval")
		_, err := executor.ValidateResume(
			context.Background(), spec, checkpoint, []byte(`{"other":"value"}`),
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not interrupted")
	})

	t.Run("empty acknowledgement policy", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, ResumeNativeInterrupt, false, "approval")
		_, err := executor.ValidateResume(context.Background(), spec, checkpoint, nil)
		require.Error(t, err)

		executor, spec, checkpoint = resumeFixture(t, ResumeNativeInterrupt, true, "approval")
		result, err := executor.ValidateResume(context.Background(), spec, checkpoint, nil)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("next turn requires utf8", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, ResumeNextTurn, false)
		result, err := executor.ValidateResume(
			context.Background(), spec, checkpoint, []byte("continue"),
		)
		require.NoError(t, err)
		assert.Equal(t, []byte("continue"), result)

		_, err = executor.ValidateResume(context.Background(), spec, checkpoint, []byte{0xff})
		require.Error(t, err)
	})

	t.Run("checkpoint schema must match task", func(t *testing.T) {
		executor, spec, _ := resumeFixture(t, ResumeNativeInterrupt, true, "approval")
		_, err := executor.ValidateResume(context.Background(), spec, []byte("invalid"), nil)
		require.Error(t, err)

		executor, spec, checkpoint := resumeFixture(t, ResumeNativeInterrupt, true, "approval")
		var state checkpointState
		require.NoError(t, json.Unmarshal(checkpoint, &state))
		state.Mode = ResumeNextTurn
		payload, marshalErr := json.Marshal(state)
		require.NoError(t, marshalErr)
		_, err = executor.ValidateResume(context.Background(), spec, payload, nil)
		require.Error(t, err)
	})
}

func TestSubagentPayloadV1Validation_BitsUT(t *testing.T) {
	executor, spec, _ := resumeFixture(t, ResumeNativeInterrupt, true, "approval")
	require.NoError(t, executor.ValidateSpec(spec))

	var payload TaskPayload
	require.NoError(t, json.Unmarshal(spec.Payload, &payload))
	payload.Version = 2
	var err error
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	assert.ErrorIs(t, executor.ValidateSpec(spec), backgroundtask.ErrUnsupportedPayloadVersion)

	payload.Version = payloadVersion
	payload.SubAgentName = ""
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	require.ErrorContains(t, executor.ValidateSpec(spec), "subagent name")
}

func TestSubmitPersistsIndependentChildIdentities_BitsUT(t *testing.T) {
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.RegisterAgent("worker", &resumableTestAgent{name: "worker"}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: executors})
	defer manager.Close(context.Background())

	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: "worker", Prompt: "work", Description: "child work",
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
) (*backgroundtask.Manager, *adk.Runner, *backgroundtask.Task, *adksession.InMemoryStore[*schema.Message]) {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.RegisterAgent(agent.name, agent))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: executors})
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: agent.name, Prompt: "work", Description: "work",
		SessionID: "parent", ResumeMode: ResumeNativeInterrupt,
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store,
		SessionID: "parent", SessionStore: store,
	})
	return manager, runner, task, store
}

func TestExecutorInterruptBecomesWaitingInput_BitsUT(t *testing.T) {
	agent := &resumableTestAgent{name: "worker", eventFactory: func(ctx context.Context) *adk.AgentEvent {
		return adk.Interrupt(ctx, "approve")
	}}
	manager, runner, task, store := executionFixture(t, agent)
	defer manager.Close(context.Background())

	require.NoError(t, runner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID))
	result, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StateWaitingInput, result.Status)
	var state checkpointState
	require.NoError(t, json.Unmarshal(result.Checkpoint, &state))
	assert.Equal(t, task.Spec.ID+"/checkpoint", state.CheckpointID)
	require.Len(t, state.TargetIDs, 1)
	assert.NotEmpty(t, state.TargetIDs[0])
	assert.Empty(t, result.ResultData, "an interrupt is not a terminal message result")
	assert.Empty(t, result.ResultError)
	_, exists, err := store.Get(context.Background(), task.Spec.ID+"/checkpoint")
	require.NoError(t, err)
	assert.True(t, exists)
}

func TestExecutorMessageBecomesTerminalResult_BitsUT(t *testing.T) {
	message := adk.EventFromMessage(
		schema.AssistantMessage("progress", nil), nil, schema.Assistant, "worker",
	)
	manager, runner, task, _ := executionFixture(t, &resumableTestAgent{
		name: "worker", events: []*adk.AgentEvent{message},
	})
	defer manager.Close(context.Background())

	require.NoError(t, runner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID))
	result, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, backgroundtask.StatusCompleted, result.Status)
	assert.Equal(t, "progress", string(result.ResultData))
}

func TestExecutorReconstructsRegisteredRunOptionsForEveryAttempt_BitsUT(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &optionCaptureAgent{name: "worker"}
	var factoryCalls int
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{
		Agent: agent,
		RunOptionsFactory: func() ([]adk.AgentRunOption, error) {
			factoryCalls++
			return []adk.AgentRunOption{adk.WrapImplSpecificOptFn(
				func(options *registrationRunOptions) {
					options.value = "registered"
				},
			)}, nil
		},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: executors})
	defer manager.Close(context.Background())
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store, SessionID: "parent", SessionStore: store,
	})

	for i := 0; i < 2; i++ {
		task, err := Submit(context.Background(), manager, &SubmitRequest{
			SubAgentName: agent.name, Prompt: "work", Description: "work", SessionID: "parent",
		})
		require.NoError(t, err)
		require.NoError(t, runner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID))
	}
	assert.Equal(t, 2, factoryCalls)
	assert.Equal(t, []string{"registered", "registered"}, agent.seen)
}

func TestManagerExecuteWithoutRunnerEnvironmentLeavesTaskPending_BitsUT(t *testing.T) {
	manager, _, task, _ := executionFixture(t, &resumableTestAgent{name: "worker"})
	err := manager.Execute(context.Background(), task.Spec.ID)
	assert.ErrorIs(t, err, ErrRunnerEnvironmentRequired)
	pending, getErr := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, getErr)
	assert.Equal(t, backgroundtask.StatusPending, pending.Status)
	assert.Zero(t, pending.Attempt)
}

func TestStopControlWinsOverLateFinalMessage_BitsUT(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &cancelThenMessageAgent{
		name: "worker", started: make(chan struct{}), release: make(chan struct{}),
	}
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.RegisterAgent(agent.name, agent))
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: registry})
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: agent.name, Prompt: "work", Description: "work", SessionID: "parent",
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store, SessionID: "parent", SessionStore: store,
	})
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- runner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID)
	}()
	<-agent.started
	_, err = manager.RequestCancel(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	close(agent.release)
	require.NoError(t, <-executeDone)
	canceled, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatusCanceled, canceled.Status)
	assert.NotEqual(t, "late completion", string(canceled.ResultData))
}

func TestExecutorDrainUsesDurableRunnerCheckpoint_BitsUT(t *testing.T) {
	agent := &resumableTestAgent{name: "worker", eventFactory: func(ctx context.Context) *adk.AgentEvent {
		return adk.Interrupt(ctx, "pause for drain")
	}}
	manager, runner, task, _ := executionFixture(t, agent)
	require.NoError(t, runner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID))
	result, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StateWaitingInput, result.Status)
}

func TestNextTurnResumeMarkerRoundTrip_BitsUT(t *testing.T) {
	message, err := resumeMessage[*schema.Message]("continue", "task:1")
	require.NoError(t, err)
	assert.Equal(t, "task:1", messageResumeMarker(message))
	assert.Equal(t, "continue", message.Content)
}

func TestSubAgentTaskResumesAfterManagerReconstruction_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	outputStore := filesystem.NewInMemoryBackend()
	agent := &interruptThenCompleteAgent{name: "worker"}
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent, OutputStore: outputStore,
		EventFormat: func(_ context.Context, event *adk.AgentEvent) (string, error) {
			if event == nil || event.Output == nil || event.Output.MessageOutput == nil {
				return "", nil
			}
			message, err := event.Output.MessageOutput.GetMessage()
			if err != nil {
				return "", err
			}
			return message.Content, nil
		},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	taskStore := backgroundtask.NewMemoryStore(nil)

	manager1 := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Store: taskStore, Executors: executors,
	})
	task, err := Submit(context.Background(), manager1, &SubmitRequest{
		SubAgentName: "worker", Prompt: "do work", Description: "durable child",
		SessionID: "parent-session", OutputFile: "/tasks/worker.events",
		ResumeMode: ResumeNativeInterrupt,
	})
	require.NoError(t, err)
	runner1 := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: sessionStore,
		SessionID: "parent-session", SessionStore: sessionStore,
	})
	require.NoError(t, runner1.ExecuteBackgroundTask(context.Background(), manager1, task.Spec.ID))
	waiting, err := manager1.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StateWaitingInput, waiting.Status)
	var state checkpointState
	require.NoError(t, json.Unmarshal(waiting.Checkpoint, &state))
	require.Len(t, state.TargetIDs, 1)
	require.NoError(t, manager1.Close(context.Background()))

	manager2 := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Store: taskStore, Executors: executors,
	})
	defer manager2.Close(context.Background())
	resumeData, err := json.Marshal(map[string]any{
		state.TargetIDs[0]: map[string]any{"approved": true},
	})
	require.NoError(t, err)
	pending, err := manager2.ResumeTask(context.Background(), &backgroundtask.ResumeTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version,
		Data: resumeData,
	})
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatePending, pending.Status)
	runner2 := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: sessionStore,
		SessionID: "parent-session", SessionStore: sessionStore,
	})
	require.NoError(t, runner2.ExecuteBackgroundTask(context.Background(), manager2, task.Spec.ID))

	completed, err := manager2.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StateCompleted, completed.Status)
	assert.Contains(t, string(completed.ResultData), "approved")
	assert.Equal(t, int64(2), completed.Attempt)
	output, err := outputStore.Read(context.Background(), &filesystem.ReadRequest{
		FilePath: completed.Spec.OutputFile,
	})
	require.NoError(t, err)
	assert.Equal(t, "before interrupt\napproved\n", output.Content)

	var persisted TaskPayload
	require.NoError(t, json.Unmarshal(completed.Spec.Payload, &persisted))
	assert.Equal(t, task.Spec.ID+"/session", persisted.ChildSessionID)
	assert.Equal(t, task.Spec.ID+"/checkpoint", persisted.CheckpointID)
}

func TestSubAgentTaskContinuesSameSessionWithoutDuplicateResumeInput_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &nextTurnAgent{name: "worker"}
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.RegisterAgent("worker", agent))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Executors: executors,
	})
	defer manager.Close(context.Background())
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: "worker", Prompt: "start", Description: "multi turn",
		SessionID: "parent-session", ResumeMode: ResumeNextTurn,
	})
	require.NoError(t, err)
	parentRunner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: sessionStore,
		SessionID: "parent-session", SessionStore: sessionStore,
	})
	require.NoError(t, parentRunner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID))
	waiting, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StateWaitingInput, waiting.Status)

	pending, err := manager.ResumeTask(context.Background(), &backgroundtask.ResumeTaskRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version,
		Data: []byte("continue"),
	})
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatePending, pending.Status)
	require.NoError(t, parentRunner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID))
	completed, err := manager.GetTask(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StateCompleted, completed.Status)

	var payload TaskPayload
	require.NoError(t, json.Unmarshal(completed.Spec.Payload, &payload))
	var checkpoint checkpointState
	require.NoError(t, json.Unmarshal(waiting.Checkpoint, &checkpoint))
	marker := fmt.Sprintf("%s:%d", completed.Spec.ID, checkpoint.Sequence)
	assert.True(t, hasMarkerInSession(t, sessionStore, payload.ChildSessionID, marker))
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
