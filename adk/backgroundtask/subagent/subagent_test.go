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

type cancelThenMessageAgent struct {
	name    string
	started chan struct{}
	release chan struct{}
}

type contextCaptureAgent struct {
	name     string
	contexts chan context.Context
	release  chan struct{}
}

func (a *contextCaptureAgent) Name(context.Context) string        { return a.name }
func (a *contextCaptureAgent) Description(context.Context) string { return "capture context" }
func (a *contextCaptureAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	a.contexts <- ctx
	go func() {
		<-a.release
		generator.Close()
	}()
	return iter
}
func (a *contextCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
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
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{Agent: agent}))
	assert.ErrorIs(
		t,
		executor.Register("worker", &AgentRegistration[*schema.Message]{Agent: agent}),
		backgroundtask.ErrAlreadyExists,
	)

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
	targets ...string,
) (*Executor[*schema.Message], backgroundtask.Spec, []byte) {
	t.Helper()
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	state, err := json.Marshal(checkpointState{
		TargetIDs: targets, Sequence: 1,
	})
	require.NoError(t, err)
	payload, err := json.Marshal(taskPayload{
		Version: payloadVersion, SubAgentName: "worker", Query: "query",
	})
	require.NoError(t, err)
	return executor, backgroundtask.Spec{
		ID: "task", ExecutorKey: ExecutorKey, Kind: "subagent", Payload: payload,
	}, state
}

func TestExecutorValidateResumeTargets_BitsUT(t *testing.T) {
	t.Run("exact target", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		result, err := executor.ValidateResume(
			context.Background(), spec, checkpoint, []byte(`{"approval":{"approved":true}}`),
		)
		require.NoError(t, err)
		assert.JSONEq(t, `{"approval":{"approved":true}}`, string(result))
	})

	t.Run("unknown target", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		_, err := executor.ValidateResume(
			context.Background(), spec, checkpoint, []byte(`{"other":"value"}`),
		)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "not interrupted")
	})

	t.Run("empty resume uses implicit resume all", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		result, err := executor.ValidateResume(context.Background(), spec, checkpoint, nil)
		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("non-object resume data is rejected", func(t *testing.T) {
		executor, spec, checkpoint := resumeFixture(t, "approval")
		_, err := executor.ValidateResume(context.Background(), spec, checkpoint, []byte("continue"))
		require.ErrorContains(t, err, "resume targets are invalid")
	})

	t.Run("checkpoint schema must be valid", func(t *testing.T) {
		executor, spec, _ := resumeFixture(t, "approval")
		_, err := executor.ValidateResume(context.Background(), spec, []byte("invalid"), nil)
		require.Error(t, err)

		checkpoint, marshalErr := json.Marshal(checkpointState{})
		require.NoError(t, marshalErr)
		_, err = executor.ValidateResume(context.Background(), spec, checkpoint, nil)
		require.Error(t, err)
	})
}

func TestAttack_ValidateResumePreservesLargeIntegers(t *testing.T) {
	executor, spec, checkpoint := resumeFixture(t, "approval")
	const resume = `{"approval":{"ticket":9007199254740993}}`
	normalized, err := executor.ValidateResume(
		context.Background(), spec, checkpoint, []byte(resume),
	)
	require.NoError(t, err)
	t.Logf("normalized resume payload: %s", normalized)
	require.Equal(t, resume, string(normalized))
}

func TestResumeControlHelpers(t *testing.T) {
	targets, err := decodeResumeTargets([]byte(
		`{"approval":{"ticket":9007199254740993}}`,
	))
	require.NoError(t, err)
	approval, ok := targets["approval"].(map[string]any)
	require.True(t, ok)
	ticket, ok := approval["ticket"].(json.Number)
	require.True(t, ok)
	require.Equal(t, "9007199254740993", ticket.String())
	_, err = decodeResumeTargets([]byte(`{"approval":true} trailing`))
	require.Error(t, err)

	controls := make(chan backgroundtask.ControlRequest, 1)
	controls <- backgroundtask.ControlRequest{
		Kind: backgroundtask.ControlTimeout, Reason: "deadline",
	}
	require.Equal(t, backgroundtask.ControlTimeout, pollControl(controls).Kind)
	require.Empty(t, pollControl(controls).Kind)
	controls <- backgroundtask.ControlRequest{Kind: backgroundtask.ControlStop}
	require.Equal(t, backgroundtask.ControlStop,
		waitForControl(context.Background(), controls).Kind)

	executor := &Executor[*schema.Message]{}
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{ID: "task"}}
	result, controlErr, controlled := executor.controlResult(
		context.Background(), task,
		backgroundtask.ControlRequest{Kind: backgroundtask.ControlTimeout, Reason: "deadline"},
	)
	require.True(t, controlled)
	require.NoError(t, controlErr)
	require.Equal(t, backgroundtask.StatusFailed, result.Status)
	require.Equal(t, "deadline", result.Error)

	result, controlErr, controlled = executor.controlResult(
		context.Background(), task,
		backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain},
	)
	require.True(t, controlled)
	require.ErrorIs(t, controlErr, ErrRunnerEnvironmentRequired)
	require.Nil(t, result)

	result, controlErr, controlled = executor.controlResult(
		context.Background(), task, backgroundtask.ControlRequest{},
	)
	require.False(t, controlled)
	require.NoError(t, controlErr)
	require.Nil(t, result)
}

func TestHandleEventErrorControlOutcomes(t *testing.T) {
	executor := &Executor[*schema.Message]{}
	task := &backgroundtask.Task{Spec: backgroundtask.Spec{ID: "task"}}

	t.Run("ordinary error", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		wantErr := errors.New("model failed")
		result, err := executor.handleEventError(
			context.Background(), iter, task,
			make(chan backgroundtask.ControlRequest), wantErr,
		)
		require.ErrorIs(t, err, wantErr)
		require.Nil(t, result)
	})

	t.Run("stop", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		controls := make(chan backgroundtask.ControlRequest, 1)
		controls <- backgroundtask.ControlRequest{Kind: backgroundtask.ControlStop}
		result, err := executor.handleEventError(
			context.Background(), iter, task, controls, context.Canceled,
		)
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusCanceled, result.Status)
	})

	t.Run("timeout", func(t *testing.T) {
		iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
		generator.Close()
		controls := make(chan backgroundtask.ControlRequest, 1)
		controls <- backgroundtask.ControlRequest{
			Kind: backgroundtask.ControlTimeout, Reason: "deadline",
		}
		result, err := executor.handleEventError(
			context.Background(), iter, task, controls, context.Canceled,
		)
		require.NoError(t, err)
		require.Equal(t, backgroundtask.StatusFailed, result.Status)
		require.Equal(t, "deadline", result.Error)
	})
}

func TestSubagentPayloadV2Validation_BitsUT(t *testing.T) {
	executor, spec, _ := resumeFixture(t, "approval")
	require.NoError(t, executor.ValidateSpec(spec))

	var payload taskPayload
	require.NoError(t, json.Unmarshal(spec.Payload, &payload))
	payload.Version = 1
	var err error
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	assert.ErrorIs(t, executor.ValidateSpec(spec), backgroundtask.ErrUnsupportedPayloadVersion)

	payload.Version = payloadVersion
	payload.SubAgentName = ""
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	require.ErrorContains(t, executor.ValidateSpec(spec), "subagent name")

	payload.SubAgentName = "worker"
	payload.Query = ""
	spec.Payload, err = json.Marshal(payload)
	require.NoError(t, err)
	require.ErrorContains(t, executor.ValidateSpec(spec), "query")
}

func TestSubmitPersistsMinimalPayloadAndDerivesChildIdentities_BitsUT(t *testing.T) {
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: executors})
	defer manager.Close(context.Background())

	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: "worker", Query: "work", Description: "child work",
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(task.Spec.Payload, &payload))
	assert.Equal(t, map[string]any{
		"version": float64(payloadVersion), "subagent_name": "worker", "query": "work",
	}, payload)
	assert.Equal(t, task.Spec.ID+"/session", childSessionID(task.Spec.ID))
	assert.Equal(t, task.Spec.ID+"/checkpoint", checkpointID(task.Spec.ID))
	assert.Equal(t, "parent-session", task.Spec.SessionID)
	assert.Equal(t, task.Spec.SessionID, task.Spec.Notify.TargetID)
}

func executionFixture(
	t *testing.T,
	agent *resumableTestAgent,
) (*backgroundtask.Manager, *adk.Runner, *backgroundtask.Task, *adksession.InMemoryStore[*schema.Message]) {
	t.Helper()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{Agent: agent}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: executors})
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: agent.name, Query: "work", Description: "work",
		SessionID: "parent",
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
	result, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StateWaitingInput, result.Status)
	var state checkpointState
	require.NoError(t, json.Unmarshal(result.Checkpoint, &state))
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
	result, err := manager.Get(context.Background(), task.Spec.ID)
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
			SubAgentName: agent.name, Query: "work", Description: "work", SessionID: "parent",
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
	pending, getErr := manager.Get(context.Background(), task.Spec.ID)
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
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{Agent: agent}))
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: registry})
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: agent.name, Query: "work", Description: "work", SessionID: "parent",
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
	canceled, err := manager.Get(context.Background(), task.Spec.ID)
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
	result, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StateWaitingInput, result.Status)
}

func TestControlAndInterruptUseRunnerCheckpoint(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &contextCaptureAgent{
		name: "worker", contexts: make(chan context.Context, 1), release: make(chan struct{}),
	}
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register(agent.name, &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	registry := backgroundtask.NewExecutorRegistry()
	require.NoError(t, registry.Register(executor))
	manager := backgroundtask.New(context.Background(), &backgroundtask.Config{Executors: registry})
	task, err := Submit(context.Background(), manager, &SubmitRequest{
		SubAgentName: agent.name, Query: "work", Description: "work", SessionID: "parent",
	})
	require.NoError(t, err)
	runner := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: store, SessionID: "parent", SessionStore: store,
	})
	executeDone := make(chan error, 1)
	go func() {
		executeDone <- runner.ExecuteBackgroundTask(context.Background(), manager, task.Spec.ID)
	}()
	runCtx := <-agent.contexts

	result, controlErr, controlled := executor.controlResult(
		runCtx, task, backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain},
	)
	require.True(t, controlled)
	require.ErrorIs(t, controlErr, backgroundtask.ErrCheckpointUnavailable)
	require.Nil(t, result)

	require.NoError(t, store.Set(
		context.Background(), checkpointID(task.Spec.ID), []byte("runner checkpoint"),
	))
	result, controlErr, controlled = executor.controlResult(
		runCtx, task, backgroundtask.ControlRequest{Kind: backgroundtask.ControlDrain},
	)
	require.True(t, controlled)
	require.NoError(t, controlErr)
	require.Equal(t, backgroundtask.StatusSuspended, result.Status)

	result, err = executor.interruptResult(runCtx, task, &adk.InterruptInfo{})
	require.ErrorContains(t, err, "no resumable targets")
	require.Nil(t, result)
	result, err = executor.interruptResult(runCtx, task, &adk.InterruptInfo{
		InterruptContexts: []*adk.InterruptCtx{{ID: "approval"}},
	})
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, result.Status)

	_, _ = executor.beginRun(
		runCtx, runner, &backgroundtask.Task{
			Spec: task.Spec, Checkpoint: []byte(`{"sequence":1}`),
		}, &taskPayload{},
	)
	_, _ = executor.beginRun(
		runCtx, runner, &backgroundtask.Task{
			Spec: task.Spec, Checkpoint: []byte(`{"sequence":1}`),
			PendingResume: []byte(`{"approval":true}`),
		}, &taskPayload{},
	)

	close(agent.release)
	require.NoError(t, <-executeDone)
}

func TestWaitForControlBoundaries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Empty(t, waitForControl(
		ctx, make(chan backgroundtask.ControlRequest),
	).Kind)
	require.Empty(t, waitForControl(
		context.Background(), make(chan backgroundtask.ControlRequest),
	).Kind)
}

func TestSubAgentTaskResumesAfterManagerReconstruction_BitsUT(t *testing.T) {
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &interruptThenCompleteAgent{name: "worker"}
	executor := &Executor[*schema.Message]{}
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	taskStore := backgroundtask.NewInMemoryStore(nil)

	manager1 := backgroundtask.New(context.Background(), &backgroundtask.Config{
		Store: taskStore, Executors: executors,
	})
	task, err := Submit(context.Background(), manager1, &SubmitRequest{
		SubAgentName: "worker", Query: "do work", Description: "durable child",
		SessionID: "parent-session",
	})
	require.NoError(t, err)
	runner1 := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: sessionStore,
		SessionID: "parent-session", SessionStore: sessionStore,
	})
	require.NoError(t, runner1.ExecuteBackgroundTask(context.Background(), manager1, task.Spec.ID))
	waiting, err := manager1.Get(context.Background(), task.Spec.ID)
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
	pending, err := manager2.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version,
	})
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StatePending, pending.Status)
	runner2 := adk.NewRunner(context.Background(), adk.RunnerConfig{
		Agent: agent, CheckPointStore: sessionStore,
		SessionID: "parent-session", SessionStore: sessionStore,
	})
	require.NoError(t, runner2.ExecuteBackgroundTask(context.Background(), manager2, task.Spec.ID))

	completed, err := manager2.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	assert.Equal(t, backgroundtask.StateCompleted, completed.Status)
	assert.Contains(t, string(completed.ResultData), "approved")
	assert.Equal(t, int64(2), completed.Attempt)
	assert.Empty(t, completed.Spec.OutputFile)
	feed, err := manager2.ReadRecentTaskEvents(context.Background(), &backgroundtask.ReadRecentTaskEventsRequest{
		TaskID: completed.Spec.ID,
	})
	require.NoError(t, err)
	assert.Empty(t, feed.Events)

	var persisted taskPayload
	require.NoError(t, json.Unmarshal(completed.Spec.Payload, &persisted))
	assert.Equal(t, "do work", persisted.Query)
	assert.Equal(t, childSessionID(task.Spec.ID), completed.Spec.ID+"/session")
	assert.Equal(t, checkpointID(task.Spec.ID), completed.Spec.ID+"/checkpoint")
}
