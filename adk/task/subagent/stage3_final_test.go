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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adkinternal "github.com/cloudwego/eino/adk/internal"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

type stage3FaultStore struct {
	*background.InMemoryStore
	getMailboxErr       error
	listInputsErr       error
	listInputsResult    *task.ListInputsResult
	sendInputErr        error
	advanceCursorErr    error
	commitInputErr      error
	commitInputCalls    int
	commitInputRequests []*background.CommitInputRequest
	waitInputsFault     <-chan error
	waitInputsStarted   chan struct{}
	waitInputsOnce      sync.Once
	sealMailboxHook     func(context.Context, *task.SealMailboxRequest) error
	sealMailboxCalls    int64
}

func (s *stage3FaultStore) GetMailbox(
	ctx context.Context,
	taskID string,
) (*task.Mailbox, error) {
	if s.getMailboxErr != nil {
		return nil, s.getMailboxErr
	}
	return s.InMemoryStore.GetMailbox(ctx, taskID)
}

func (s *stage3FaultStore) ListInputs(
	ctx context.Context,
	req *task.ListInputsRequest,
) (*task.ListInputsResult, error) {
	if s.listInputsErr != nil {
		return nil, s.listInputsErr
	}
	if s.listInputsResult != nil {
		return s.listInputsResult, nil
	}
	return s.InMemoryStore.ListInputs(ctx, req)
}

func (s *stage3FaultStore) SendInput(
	ctx context.Context,
	req *task.SendInputRequest,
) (*task.SendInputResult, error) {
	if s.sendInputErr != nil {
		return nil, s.sendInputErr
	}
	return s.InMemoryStore.SendInput(ctx, req)
}

func (s *stage3FaultStore) AdvanceCursor(
	ctx context.Context,
	req *task.AdvanceCursorRequest,
) error {
	if s.advanceCursorErr != nil {
		return s.advanceCursorErr
	}
	return s.InMemoryStore.AdvanceCursor(ctx, req)
}

func (s *stage3FaultStore) CommitInput(
	ctx context.Context,
	req *background.CommitInputRequest,
) (*background.TaskSnapshot, error) {
	s.commitInputCalls++
	if req != nil {
		cloned := *req
		cloned.Checkpoint = append([]byte(nil), req.Checkpoint...)
		s.commitInputRequests = append(s.commitInputRequests, &cloned)
	}
	if s.commitInputErr != nil {
		return nil, s.commitInputErr
	}
	return s.InMemoryStore.CommitInput(ctx, req)
}

func (s *stage3FaultStore) WaitInputs(
	ctx context.Context,
	req *task.WaitInputsRequest,
) (*task.ListInputsResult, error) {
	if s.waitInputsStarted != nil {
		s.waitInputsOnce.Do(func() { close(s.waitInputsStarted) })
	}
	if s.waitInputsFault != nil {
		select {
		case err := <-s.waitInputsFault:
			return nil, err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return s.InMemoryStore.WaitInputs(ctx, req)
}

func (s *stage3FaultStore) SealIfIdle(
	ctx context.Context,
	req *task.SealMailboxRequest,
) (*task.Mailbox, error) {
	atomic.AddInt64(&s.sealMailboxCalls, 1)
	if s.sealMailboxHook != nil {
		if err := s.sealMailboxHook(ctx, req); err != nil {
			return nil, err
		}
	}
	return s.InMemoryStore.SealIfIdle(ctx, req)
}

type stage3RuntimeExecutor struct {
	key     string
	execute func(
		context.Context,
		*background.TaskSnapshot,
		background.ExecutionRuntime,
	) (*background.ExecutionResult, error)
}

type stage3CountingAgent struct {
	name    string
	waitFor <-chan struct{}
	runs    int64
}

func (a *stage3CountingAgent) Name(context.Context) string { return a.name }

func (*stage3CountingAgent) Description(context.Context) string {
	return "stage3 counting agent"
}

func (a *stage3CountingAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	if a.waitFor != nil {
		select {
		case <-a.waitFor:
		case <-ctx.Done():
			generator.Send(&adk.AgentEvent{Err: ctx.Err()})
			generator.Close()
			return iter
		}
	}
	run := atomic.AddInt64(&a.runs, 1)
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage(fmt.Sprintf("done-%d", run), nil),
		nil,
		schema.Assistant,
		a.name,
	))
	generator.Close()
	return iter
}

func (a *stage3CountingAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

func (e *stage3RuntimeExecutor) Key() string { return e.key }

func (*stage3RuntimeExecutor) LeaseExpiryPolicy() background.LeaseExpiryPolicy {
	return background.LeaseExpiryRetry
}

func (*stage3RuntimeExecutor) ValidateSpec(background.Spec) error { return nil }

func (*stage3RuntimeExecutor) ValidateExecution(
	context.Context,
	*background.TaskSnapshot,
) error {
	return nil
}

func (*stage3RuntimeExecutor) SupportsDrain() bool { return true }

func (e *stage3RuntimeExecutor) Execute(
	ctx context.Context,
	snapshot *background.TaskSnapshot,
	runtime background.ExecutionRuntime,
) (*background.ExecutionResult, error) {
	return e.execute(ctx, snapshot, runtime)
}

func newStage3Manager(
	t *testing.T,
	store *stage3FaultStore,
) *background.Manager {
	t.Helper()
	manager, err := background.New(context.Background(), &background.Config{
		Tasks: store, TaskEvents: store,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	return manager
}

func validStage3Task(t *testing.T, taskID string) *background.TaskSnapshot {
	t.Helper()
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, SubAgentName: "worker",
		ChildSessionID: "child",
	})
	require.NoError(t, err)
	return &background.TaskSnapshot{Spec: background.Spec{
		ID: taskID, ExecutorKey: ExecutorKey, Kind: "subagent", Payload: payload,
	}}
}

type stage3GetMailboxFaultAgent struct {
	*resumableTestAgent
	store *stage3FaultStore
	err   error
}

func (a *stage3GetMailboxFaultAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.store.getMailboxErr = a.err
	return a.resumableTestAgent.Run(ctx, input, options...)
}

func prepareStage3ManagedTask(
	t *testing.T,
	store *stage3FaultStore,
	agent adk.ResumableAgent,
	taskID string,
) (*background.Manager, *Controller[*schema.Message], *Handle) {
	t.Helper()
	ctx := context.Background()
	manager := newStage3Manager(t, store)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	controller, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager, Barrier: completeBarrier[*schema.Message](),
		InputsToAgentInput: testEventMapper,
		SessionStore:       sessionStore, CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{Agent: agent},
	))
	metadata := &runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "root-session",
		RootSessionID: "root-session", ChildSessionID: taskID + "-child",
		AgentName: "worker", StartMode: task.StartModeBackground,
	}
	identity, err := json.Marshal(metadata)
	require.NoError(t, err)
	reserved, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: taskID, InvocationID: taskID,
		Identity: identity, RootSessionID: metadata.RootSessionID,
		ChildSessionID: metadata.ChildSessionID,
	})
	require.NoError(t, err)
	encoded, err := encodeTypedInput(&adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("work")},
	})
	require.NoError(t, err)
	input, err := json.Marshal(encoded)
	require.NoError(t, err)
	terminal, err := controller.prepareStartMailbox(ctx, reserved, taskID, input)
	require.NoError(t, err)
	require.False(t, terminal)
	handle := controller.newHandle(reserved.Mailbox.TaskID, metadata.ChildSessionID)
	pending, err := controller.submitTask(ctx, handle, metadata, nil)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, pending.Status)
	return manager, controller, handle
}

func prepareStage3RuntimeTask(
	t *testing.T,
	store *stage3FaultStore,
	executor *stage3RuntimeExecutor,
	taskID string,
) *background.Manager {
	t.Helper()
	manager := newStage3Manager(t, store)
	_, loaded, err := manager.LoadOrRegisterExecutor(executor)
	require.NoError(t, err)
	require.False(t, loaded)
	created, err := manager.Submit(context.Background(), &background.SubmitRequest{
		Spec: background.Spec{
			ID: taskID, ExecutorKey: executor.Key(), Kind: "stage3-runtime",
		},
	})
	require.NoError(t, err)
	require.Equal(t, int64(1), created.Version)
	_, err = manager.SendInput(context.Background(), &task.SendInputRequest{
		TaskID: taskID,
		Input: task.Input{
			EventID: taskID + ":input", Kind: "resume", Data: []byte("input"),
		},
	})
	require.NoError(t, err)
	return manager
}

func TestControllerRunActivation(t *testing.T) {
	t.Run("processes input that races with attached mailbox sealing", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		store := &stage3FaultStore{
			InMemoryStore: background.NewInMemoryStore(nil),
		}
		manager := newStage3Manager(t, store)
		sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
		agent := &stage3CountingAgent{name: "worker"}
		controller, err := NewController(&ControllerConfig[*schema.Message]{
			Manager: manager, Barrier: completeBarrier[*schema.Message](),
			InputsToAgentInput: testEventMapper,
			SessionStore:       sessionStore, CheckPointStore: sessionStore,
		})
		require.NoError(t, err)
		require.NoError(t, controller.RegisterAgent(
			agent.name,
			&AgentRegistration[*schema.Message]{Agent: agent},
		))

		var injectOnce sync.Once
		store.sealMailboxHook = func(
			ctx context.Context,
			req *task.SealMailboxRequest,
		) error {
			var sendErr error
			injectOnce.Do(func() {
				_, sendErr = store.InMemoryStore.SendInput(
					ctx,
					&task.SendInputRequest{
						TaskID: req.TaskID,
						Input: task.Input{
							EventID: "late-input",
							Kind:    "external",
							Data:    []byte("late"),
						},
					},
				)
			})
			return sendErr
		}

		handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
			InvocationID: "seal-race", ParentSessionID: "parent",
			AgentName: agent.name, StartMode: task.StartModeForeground,
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("initial")},
			},
		})
		require.NoError(t, err)
		result, err := controller.Wait(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, "done-2", result.FinalMessage.Content)
		require.Equal(t, int64(2), atomic.LoadInt64(&agent.runs))
		require.Equal(t, int64(2), atomic.LoadInt64(&store.sealMailboxCalls))

		mailbox, err := manager.GetMailbox(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, task.MailboxSealed, mailbox.State)
		require.Equal(t, int64(2), mailbox.LatestSequence)
		require.Equal(t, int64(2), mailbox.ConsumedCursor)
	})

	t.Run("propagates watcher errors without waiting for context timeout", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()

		waitErr := errors.New("wait inputs failed")
		releaseAgent := make(chan struct{})
		var releaseAgentOnce sync.Once
		releaseAgentRun := func() {
			releaseAgentOnce.Do(func() { close(releaseAgent) })
		}
		defer releaseAgentRun()
		waitInputsFault := make(chan error)
		store := &stage3FaultStore{
			InMemoryStore:     background.NewInMemoryStore(nil),
			waitInputsFault:   waitInputsFault,
			waitInputsStarted: make(chan struct{}),
		}
		manager := newStage3Manager(t, store)
		sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
		agent := &stage3CountingAgent{name: "worker", waitFor: releaseAgent}
		controller, err := NewController(&ControllerConfig[*schema.Message]{
			Manager: manager, Barrier: completeBarrier[*schema.Message](),
			InputsToAgentInput: testEventMapper,
			SessionStore:       sessionStore, CheckPointStore: sessionStore,
		})
		require.NoError(t, err)
		require.NoError(t, controller.RegisterAgent(
			agent.name,
			&AgentRegistration[*schema.Message]{Agent: agent},
		))

		handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
			InvocationID: "wait-error", ParentSessionID: "parent",
			AgentName: agent.name, StartMode: task.StartModeForeground,
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("initial")},
			},
		})
		require.NoError(t, err)
		awaitIntegrationValue(t, store.waitInputsStarted)
		select {
		case waitInputsFault <- waitErr:
		case <-ctx.Done():
			require.FailNow(t, "timed out injecting WaitInputs fault")
		}
		releaseAgentRun()

		result, err := controller.Wait(ctx, handle.ID())
		require.Nil(t, result)
		require.ErrorIs(t, err, waitErr)
		require.NotErrorIs(t, err, context.DeadlineExceeded)
	})
}

func TestSessionConfigForTask(t *testing.T) {
	providerErr := errors.New("event extra failed")
	event := &adk.SessionEvent[*schema.Message]{
		Kind: adk.SessionEventMessage,
	}
	var calls int
	var received *adk.SessionEvent[*schema.Message]
	base := &adk.SessionConfig[*schema.Message]{
		EventExtraProvider: func(
			_ context.Context,
			got *adk.SessionEvent[*schema.Message],
		) (map[string]any, error) {
			calls++
			received = got
			return map[string]any{"base": "must not be returned"}, providerErr
		},
	}

	config := sessionConfigForTask(base, "task-id")
	extra, err := config.EventExtraProvider(context.Background(), event)
	require.Nil(t, extra)
	require.ErrorIs(t, err, providerErr)
	require.Equal(t, 1, calls)
	require.Same(t, event, received)
}

func TestTaskRuntimeCommitInput(t *testing.T) {
	t.Run("commits checkpoint and cursor before final transition", func(t *testing.T) {
		ctx := context.Background()
		store := &stage3FaultStore{InMemoryStore: background.NewInMemoryStore(nil)}
		var committedTask *background.TaskSnapshot
		var committedMailbox *task.Mailbox
		checkpoint := []byte("resume-state")
		executor := &stage3RuntimeExecutor{
			key: "stage3-commit-success",
			execute: func(
				ctx context.Context,
				_ *background.TaskSnapshot,
				runtime background.ExecutionRuntime,
			) (*background.ExecutionResult, error) {
				require.NoError(t, runtime.CommitInput(ctx, 0, 1, checkpoint))
				checkpoint[0] = 'X'
				var err error
				committedTask, err = store.Get(ctx, "commit-success")
				require.NoError(t, err)
				committedMailbox, err = store.GetMailbox(ctx, "commit-success")
				require.NoError(t, err)
				return &background.ExecutionResult{
					Action:      background.ExecutionActionComplete,
					Data:        []byte("done"),
					InputCursor: 1,
				}, nil
			},
		}
		manager := prepareStage3RuntimeTask(t, store, executor, "commit-success")

		require.NoError(t, manager.Execute(ctx, "commit-success"))
		require.Equal(t, background.StatusRunning, committedTask.Status)
		require.Equal(t, int64(1), committedTask.Attempt)
		require.Equal(t, int64(3), committedTask.Version)
		require.Equal(t, []byte("resume-state"), committedTask.Checkpoint)
		require.Equal(t, task.MailboxBackground, committedMailbox.State)
		require.Equal(t, int64(1), committedMailbox.Generation)
		require.Equal(t, int64(1), committedMailbox.ConsumedCursor)
		require.Equal(t, int64(1), committedMailbox.LatestSequence)
		require.Equal(t, 1, store.commitInputCalls)
		require.Len(t, store.commitInputRequests, 1)
		require.Equal(t, &background.CommitInputRequest{
			TaskID: "commit-success", ExpectedVersion: 2, Attempt: 1,
			ExpectedCursor: 0, InputCursor: 1, Checkpoint: []byte("resume-state"),
		}, store.commitInputRequests[0])

		completed, err := manager.Get(ctx, "commit-success")
		require.NoError(t, err)
		require.Equal(t, background.StatusCompleted, completed.Status)
		require.Equal(t, int64(1), completed.Attempt)
		require.Equal(t, int64(4), completed.Version)
		require.Equal(t, []byte("resume-state"), completed.Checkpoint)
		require.Equal(t, []byte("done"), completed.ResultData)
		require.Empty(t, completed.ResultError)
		require.NotNil(t, completed.DoneAt)
		mailbox, err := manager.GetMailbox(ctx, "commit-success")
		require.NoError(t, err)
		require.Equal(t, task.MailboxSealed, mailbox.State)
		require.Equal(t, int64(2), mailbox.Generation)
		require.Equal(t, int64(1), mailbox.ConsumedCursor)
		require.Equal(t, int64(1), mailbox.LatestSequence)
	})

	t.Run("zero checkpoint poisons replay without mutating store", func(t *testing.T) {
		ctx := context.Background()
		store := &stage3FaultStore{InMemoryStore: background.NewInMemoryStore(nil)}
		var firstErr, replayErr error
		executor := &stage3RuntimeExecutor{
			key: "stage3-commit-zero",
			execute: func(
				ctx context.Context,
				_ *background.TaskSnapshot,
				runtime background.ExecutionRuntime,
			) (*background.ExecutionResult, error) {
				firstErr = runtime.CommitInput(ctx, 0, 1, nil)
				replayErr = runtime.CommitInput(ctx, 0, 1, []byte("valid"))
				return &background.ExecutionResult{
					Action:      background.ExecutionActionComplete,
					Data:        []byte("must-not-commit"),
					InputCursor: 1,
				}, nil
			},
		}
		manager := prepareStage3RuntimeTask(t, store, executor, "commit-zero")

		err := manager.Execute(ctx, "commit-zero")
		require.EqualError(
			t,
			firstErr,
			"task/background: commit input request is invalid",
		)
		require.Same(t, firstErr, replayErr)
		require.Same(t, firstErr, err)
		require.Equal(t, 1, store.commitInputCalls)
		require.Len(t, store.commitInputRequests, 1)
		require.Nil(t, store.commitInputRequests[0].Checkpoint)

		running, getErr := manager.Get(ctx, "commit-zero")
		require.NoError(t, getErr)
		require.Equal(t, background.StatusRunning, running.Status)
		require.Equal(t, int64(1), running.Attempt)
		require.Equal(t, int64(2), running.Version)
		require.Nil(t, running.Checkpoint)
		require.Nil(t, running.ResultData)
		require.Empty(t, running.ResultError)
		require.Nil(t, running.DoneAt)
		mailbox, getErr := manager.GetMailbox(ctx, "commit-zero")
		require.NoError(t, getErr)
		require.Equal(t, task.MailboxBackground, mailbox.State)
		require.Equal(t, int64(1), mailbox.Generation)
		require.Equal(t, int64(0), mailbox.ConsumedCursor)
		require.Equal(t, int64(1), mailbox.LatestSequence)
	})

	t.Run("ownership loss poisons replay without mutating store", func(t *testing.T) {
		ctx := context.Background()
		store := &stage3FaultStore{
			InMemoryStore:  background.NewInMemoryStore(nil),
			commitInputErr: task.ErrOwnershipLost,
		}
		var firstErr, replayErr error
		executor := &stage3RuntimeExecutor{
			key: "stage3-commit-ownership",
			execute: func(
				ctx context.Context,
				_ *background.TaskSnapshot,
				runtime background.ExecutionRuntime,
			) (*background.ExecutionResult, error) {
				firstErr = runtime.CommitInput(ctx, 0, 1, []byte("resume-state"))
				replayErr = runtime.CommitInput(ctx, 0, 1, []byte("replay"))
				return &background.ExecutionResult{
					Action:      background.ExecutionActionComplete,
					Data:        []byte("must-not-commit"),
					InputCursor: 1,
				}, nil
			},
		}
		manager := prepareStage3RuntimeTask(t, store, executor, "commit-ownership")

		err := manager.Execute(ctx, "commit-ownership")
		require.ErrorIs(t, firstErr, task.ErrOwnershipLost)
		require.Same(t, firstErr, replayErr)
		require.Same(t, firstErr, err)
		require.Equal(t, 1, store.commitInputCalls)
		require.Len(t, store.commitInputRequests, 1)
		require.Equal(t, []byte("resume-state"), store.commitInputRequests[0].Checkpoint)

		running, getErr := manager.Get(ctx, "commit-ownership")
		require.NoError(t, getErr)
		require.Equal(t, background.StatusRunning, running.Status)
		require.Equal(t, int64(1), running.Attempt)
		require.Equal(t, int64(2), running.Version)
		require.Nil(t, running.Checkpoint)
		require.Nil(t, running.ResultData)
		require.Empty(t, running.ResultError)
		require.Nil(t, running.DoneAt)
		mailbox, getErr := manager.GetMailbox(ctx, "commit-ownership")
		require.NoError(t, getErr)
		require.Equal(t, task.MailboxBackground, mailbox.State)
		require.Equal(t, int64(1), mailbox.Generation)
		require.Equal(t, int64(0), mailbox.ConsumedCursor)
		require.Equal(t, int64(1), mailbox.LatestSequence)
	})
}

func TestEncodeTypedInput(t *testing.T) {
	t.Run("rejects zero inputs exactly", func(t *testing.T) {
		tests := []struct {
			name  string
			input *adk.AgentInput
			want  string
		}{
			{
				name: "nil input",
				want: "task/subagent: typed input messages are required",
			},
			{
				name:  "empty messages",
				input: &adk.AgentInput{},
				want:  "task/subagent: typed input messages are required",
			},
			{
				name: "nil message",
				input: &adk.AgentInput{
					Messages: []*schema.Message{nil},
				},
				want: "task/subagent: typed input contains a nil message",
			},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				encoded, err := encodeTypedInput(tt.input)
				require.Nil(t, encoded)
				require.EqualError(t, err, tt.want)
			})
		}
	})

	t.Run("propagates serializer failure without a partial payload", func(t *testing.T) {
		message := schema.UserMessage("work")
		message.Extra = map[string]any{"unsupported": func() {}}
		encoded, err := encodeTypedInput(&adk.AgentInput{
			Messages: []*schema.Message{message},
		})
		require.Nil(t, encoded)
		require.ErrorContains(t, err, "task/subagent: serialize typed input")
		require.ErrorContains(t, err, "unknown type: func()")
	})

	t.Run("owns bytes and preserves zero streaming value across replay", func(t *testing.T) {
		message := schema.UserAgenticMessage("work")
		input := &adk.TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{message},
		}
		first, err := encodeTypedInput(input)
		require.NoError(t, err)
		require.NotEmpty(t, first.Messages)
		require.False(t, first.EnableStreaming)
		firstBytes := append([]byte(nil), first.Messages...)

		message.ContentBlocks[0].UserInputText.Text = "mutated after encode"
		decoded, err := decodeTypedInput[*schema.AgenticMessage](first)
		require.NoError(t, err)
		require.Len(t, decoded.Messages, 1)
		require.Equal(
			t,
			"work",
			decoded.Messages[0].ContentBlocks[0].UserInputText.Text,
		)
		require.False(t, decoded.EnableStreaming)
		require.Equal(t, firstBytes, []byte(first.Messages))

		replayed, err := encodeTypedInput(decoded)
		require.NoError(t, err)
		require.JSONEq(t, string(first.Messages), string(replayed.Messages))
		require.False(t, replayed.EnableStreaming)
	})
}

func TestExecutorAcknowledgeCancellation(t *testing.T) {
	ctx := context.Background()
	valid := validStage3Task(t, "cancel-task")

	t.Run("validates task and executor", func(t *testing.T) {
		executor := newExecutor[*schema.Message](nil)
		require.EqualError(
			t,
			executor.AcknowledgeCancellation(ctx, nil, "stop"),
			"task/subagent: task is required",
		)
		invalid := *valid
		invalid.Spec.ExecutorKey = "wrong"
		require.EqualError(
			t,
			executor.AcknowledgeCancellation(ctx, &invalid, "stop"),
			"task/subagent: invalid executor key or task kind",
		)
		require.EqualError(
			t,
			executor.AcknowledgeCancellation(ctx, valid, "stop"),
			"task/subagent: controller is unavailable",
		)
	})

	t.Run("no hook is a successful acknowledgement", func(t *testing.T) {
		executor := newExecutor(&Controller[*schema.Message]{})
		require.NoError(t, executor.AcknowledgeCancellation(ctx, valid, "stop"))
	})

	t.Run("passes stable identity and propagates hook failure", func(t *testing.T) {
		hookErr := errors.New("cleanup rejected")
		var gotTaskID, gotChildSessionID, gotReason string
		executor := newExecutor(&Controller[*schema.Message]{
			cancellationHook: cancellationHookFunc(func(
				_ context.Context,
				taskID, childSessionID, reason string,
			) error {
				gotTaskID = taskID
				gotChildSessionID = childSessionID
				gotReason = reason
				return hookErr
			}),
		})
		err := executor.AcknowledgeCancellation(ctx, valid, "operator stop")
		require.ErrorIs(t, err, hookErr)
		require.Equal(t, "cancel-task", gotTaskID)
		require.Equal(t, "child", gotChildSessionID)
		require.Equal(t, "operator stop", gotReason)
	})
}

func TestExecutorExecute(t *testing.T) {
	ctx := context.Background()
	valid := validStage3Task(t, "missing-task")
	executor := newExecutor[*schema.Message](nil)

	result, err := executor.Execute(ctx, nil, nil)
	require.Nil(t, result)
	require.EqualError(t, err, "task/subagent: task is required")

	invalid := *valid
	invalid.Spec.Kind = "wrong"
	result, err = executor.Execute(ctx, &invalid, nil)
	require.Nil(t, result)
	require.EqualError(t, err, "task/subagent: invalid executor key or task kind")

	result, err = executor.Execute(ctx, valid, nil)
	require.Nil(t, result)
	require.EqualError(t, err, "task/subagent: controller is unavailable")

	store := &stage3FaultStore{InMemoryStore: background.NewInMemoryStore(nil)}
	manager := newStage3Manager(t, store)
	executor = newExecutor(&Controller[*schema.Message]{manager: manager})
	result, err = executor.Execute(ctx, valid, nil)
	require.Nil(t, result)
	require.ErrorIs(t, err, task.ErrMailboxNotFound)
}

func TestDecodeTypedInput(t *testing.T) {
	t.Run("round trips messages and streaming", func(t *testing.T) {
		encoded, err := encodeTypedInput(&adk.AgentInput{
			Messages:        []*schema.Message{schema.UserMessage("work")},
			EnableStreaming: true,
		})
		require.NoError(t, err)
		input, err := decodeTypedInput[*schema.Message](encoded)
		require.NoError(t, err)
		require.Equal(t, "work", input.Messages[0].Content)
		require.True(t, input.EnableStreaming)
	})

	t.Run("rejects missing malformed and mismatched payloads", func(t *testing.T) {
		for _, encoded := range []*serializedTypedInput{
			nil,
			{},
		} {
			input, err := decodeTypedInput[*schema.Message](encoded)
			require.Nil(t, input)
			require.EqualError(t, err, "task/subagent: typed input is required")
		}

		input, err := decodeTypedInput[*schema.Message](&serializedTypedInput{
			Messages: json.RawMessage("{"),
		})
		require.Nil(t, input)
		require.ErrorContains(t, err, "task/subagent: deserialize typed input")

		encoded, err := encodeTypedInput(&adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		})
		require.NoError(t, err)
		agentic, err := decodeTypedInput[*schema.AgenticMessage](encoded)
		require.Nil(t, agentic)
		require.EqualError(
			t,
			err,
			"task/subagent: typed input message type does not match executor",
		)
	})
}

func TestControllerHandle(t *testing.T) {
	ctx := context.Background()
	var nilController *Controller[*schema.Message]
	handle, err := nilController.Handle(ctx, "task")
	require.Nil(t, handle)
	require.ErrorIs(t, err, task.ErrMailboxNotFound)

	handle, err = (&Controller[*schema.Message]{}).Handle(ctx, "task")
	require.Nil(t, handle)
	require.ErrorIs(t, err, task.ErrMailboxNotFound)

	storeErr := errors.New("mailbox read failed")
	store := &stage3FaultStore{
		InMemoryStore: background.NewInMemoryStore(nil),
		getMailboxErr: storeErr,
	}
	manager := newStage3Manager(t, store)
	controller := &Controller[*schema.Message]{manager: manager}
	handle, err = controller.Handle(ctx, "task")
	require.Nil(t, handle)
	require.ErrorIs(t, err, storeErr)

	store.getMailboxErr = nil
	_, err = manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "invalid-identity", InvocationID: "invalid-identity",
		Identity: []byte("{"), RootSessionID: "root",
	})
	require.NoError(t, err)
	handle, err = controller.Handle(ctx, "invalid-identity")
	require.Nil(t, handle)
	require.Error(t, err)
}

func TestPrepareStartMailbox(t *testing.T) {
	ctx := context.Background()
	sendErr := errors.New("send failed")
	listErr := errors.New("list failed")
	store := &stage3FaultStore{InMemoryStore: background.NewInMemoryStore(nil)}
	manager := newStage3Manager(t, store)
	controller := &Controller[*schema.Message]{manager: manager}
	created := &task.RegisterMailboxResult{
		Created: true,
		Mailbox: &task.Mailbox{TaskID: "created"},
	}

	store.sendInputErr = sendErr
	terminal, err := controller.prepareStartMailbox(ctx, created, "invoke", []byte("input"))
	require.False(t, terminal)
	require.ErrorIs(t, err, sendErr)

	store.sendInputErr = task.ErrMailboxSealed
	terminal, err = controller.prepareStartMailbox(ctx, created, "invoke", []byte("input"))
	require.NoError(t, err)
	require.False(t, terminal)

	replayed := &task.RegisterMailboxResult{
		Mailbox: &task.Mailbox{TaskID: "replayed", State: task.MailboxBackground},
	}
	store.sendInputErr = nil
	store.listInputsErr = listErr
	terminal, err = controller.prepareStartMailbox(ctx, replayed, "invoke", []byte("input"))
	require.False(t, terminal)
	require.ErrorIs(t, err, listErr)

	store.listInputsErr = nil
	store.listInputsResult = &task.ListInputsResult{}
	store.sendInputErr = task.ErrMailboxSealed
	terminal, err = controller.prepareStartMailbox(ctx, replayed, "invoke", []byte("input"))
	require.False(t, terminal)
	require.ErrorIs(t, err, task.ErrMailboxSealed)
}

func TestStableRuntimeInputHash(t *testing.T) {
	first := schema.UserMessage("work")
	first.Extra = map[string]any{
		adkinternal.EinoMsgIDKey: "generated-1",
		"tenant":                 "alpha",
	}
	second := schema.UserMessage("work")
	second.Extra = map[string]any{
		adkinternal.EinoMsgIDKey: "generated-2",
		"tenant":                 "alpha",
	}
	firstHash, err := stableRuntimeInputHash(&adk.AgentInput{
		Messages: []*schema.Message{nil, first},
	})
	require.NoError(t, err)
	secondHash, err := stableRuntimeInputHash(&adk.AgentInput{
		Messages: []*schema.Message{second},
	})
	require.NoError(t, err)
	require.Equal(t, firstHash, secondHash)
	require.Equal(t, "generated-1", first.Extra[adkinternal.EinoMsgIDKey])

	second.Extra["tenant"] = "beta"
	changedHash, err := stableRuntimeInputHash(&adk.AgentInput{
		Messages: []*schema.Message{second},
	})
	require.NoError(t, err)
	require.NotEqual(t, firstHash, changedHash)

	agentic := schema.UserAgenticMessage("work")
	agentic.Extra = map[string]any{adkinternal.EinoMsgIDKey: "generated"}
	agenticHash, err := stableRuntimeInputHash(
		&adk.TypedAgentInput[*schema.AgenticMessage]{
			Messages: []*schema.AgenticMessage{nil, agentic},
		},
	)
	require.NoError(t, err)
	require.NotEmpty(t, agenticHash)
	require.Equal(t, "generated", agentic.Extra[adkinternal.EinoMsgIDKey])

	unsupported := schema.UserMessage("work")
	unsupported.Extra = map[string]any{"invalid": func() {}}
	hash, err := stableRuntimeInputHash(&adk.AgentInput{
		Messages: []*schema.Message{unsupported},
	})
	require.Nil(t, hash)
	require.ErrorContains(t, err, "task/subagent: serialize runtime input identity")
}

func TestNilRuntimeMessage(t *testing.T) {
	var message *schema.Message
	require.True(t, nilRuntimeMessage(message))
	require.False(t, nilRuntimeMessage(schema.UserMessage("work")))

	var agentic *schema.AgenticMessage
	require.True(t, nilRuntimeMessage(agentic))
	require.False(t, nilRuntimeMessage(schema.UserAgenticMessage("work")))
}

func TestJoinErrors(t *testing.T) {
	primary := errors.New("primary")
	secondary := errors.New("secondary")

	require.Same(t, secondary, joinErrors(nil, secondary))
	require.Same(t, primary, joinErrors(primary, nil))
	combined := joinErrors(primary, secondary)
	require.EqualError(t, combined, "primary: secondary")
	require.ErrorIs(t, combined, primary)
	require.NotErrorIs(t, combined, secondary)
}

func TestManagedAdvanceInputCursorRejectsLostOwnership(t *testing.T) {
	t.Run("advance enforces ownership fence", func(t *testing.T) {
		ctx := context.Background()
		store := &stage3FaultStore{InMemoryStore: background.NewInMemoryStore(nil)}
		manager, _, handle := prepareStage3ManagedTask(
			t,
			store,
			&resumableTestAgent{name: "worker"},
			"ownership-task",
		)
		store.advanceCursorErr = task.ErrOwnershipLost
		require.NoError(t, manager.Execute(ctx, handle.ID()))
		failed, err := manager.Get(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, background.StatusFailed, failed.Status)
		require.Equal(t, task.ErrOwnershipLost.Error(), failed.ResultError)
	})

	t.Run("mailbox read error fails the attempt", func(t *testing.T) {
		ctx := context.Background()
		store := &stage3FaultStore{InMemoryStore: background.NewInMemoryStore(nil)}
		loadErr := errors.New("mailbox unavailable")
		agent := &stage3GetMailboxFaultAgent{
			resumableTestAgent: &resumableTestAgent{name: "worker"},
			store:              store,
			err:                loadErr,
		}
		manager, _, handle := prepareStage3ManagedTask(
			t,
			store,
			agent,
			"mailbox-read-task",
		)
		require.NoError(t, manager.Execute(ctx, handle.ID()))
		store.getMailboxErr = nil
		failed, err := manager.Get(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, background.StatusFailed, failed.Status)
		require.Equal(t, loadErr.Error(), failed.ResultError)
	})
}
