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
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

// TestAttack_NestedTaskPreservesDirectParentAndRoot verifies that deep nesting
// keeps the immediate parent for authorization without losing the root scope.
func TestAttack_NestedTaskPreservesDirectParentAndRoot(t *testing.T) {
	ctx := context.Background()
	controller, manager, _ := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
		testEventMapper,
	)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })

	root, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "attack-root", InvocationID: "attack-root",
		RootSessionID: "root-session",
	})
	require.NoError(t, err)
	directParent, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "attack-parent", InvocationID: "attack-parent",
		ChildSessionID: "direct-parent-session",
		ParentExecution: &task.ExecutionContext{
			TaskID: root.Mailbox.TaskID, Owner: task.OwnerParent,
			Generation: root.Mailbox.Generation, RootSessionID: "root-session",
		},
	})
	require.NoError(t, err)
	nestedCtx := task.WithExecutionContext(ctx, task.ExecutionContext{
		TaskID: directParent.Mailbox.TaskID, Owner: task.OwnerParent,
		Generation: directParent.Mailbox.Generation, RootSessionID: "root-session",
	})

	child, err := controller.Start(nestedCtx, &StartRequest[*schema.Message]{
		InvocationID: "attack-parent:child", ParentSessionID: "direct-parent-session",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("nested")},
		},
	})
	require.NoError(t, err)
	_, err = controller.Wait(ctx, child.ID())
	require.NoError(t, err)

	mailbox, err := manager.GetMailbox(ctx, child.ID())
	require.NoError(t, err)
	require.Equal(t, directParent.Mailbox.TaskID, mailbox.ParentTaskID)
	require.Equal(t, "root-session", mailbox.RootSessionID)
	metadata, err := decodeRuntimeMetadata(mailbox.Identity)
	require.NoError(t, err)
	require.Equal(t, "direct-parent-session", metadata.ParentSessionID)
	require.Equal(t, directParent.Mailbox.TaskID, metadata.ParentTaskID)
	require.Equal(t, "root-session", metadata.RootSessionID)
	require.NotEqual(t, metadata.ParentSessionID, metadata.RootSessionID)
}

// TestAttack_ConcurrentContinueAppendsEveryInputToSingleTask verifies that
// concurrent callers cannot split one active child session across tasks or
// overwrite another caller's durable input.
func TestAttack_ConcurrentContinueAppendsEveryInputToSingleTask(t *testing.T) {
	ctx := context.Background()
	agent := &blockingCaptureAgent{
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	t.Cleanup(agent.unblock)
	controller, manager, _ := newControllerWithAgentForTest(
		t,
		agent,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
		testEventMapper,
	)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })

	handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "attack-active", ParentSessionID: "root-session",
		ChildSessionID: "attack-shared-child", AgentName: agent.Name(ctx),
		StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("seed")},
		},
	})
	require.NoError(t, err)
	awaitIntegrationValue(t, agent.started)

	const callers = 12
	type continueResult struct {
		handle *Handle
		err    error
	}
	start := make(chan struct{})
	results := make(chan continueResult, callers)
	var group sync.WaitGroup
	for index := 0; index < callers; index++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			next, continueErr := controller.Continue(
				ctx,
				&ContinueRequest[*schema.Message]{
					ChildSessionID: handle.ChildSessionID(),
					InvocationID:   fmt.Sprintf("attack-continue-%02d", index),
					Input: &adk.AgentInput{Messages: []*schema.Message{
						schema.UserMessage(fmt.Sprintf("message-%02d", index)),
					}},
				},
			)
			results <- continueResult{handle: next, err: continueErr}
		}(index)
	}
	close(start)
	group.Wait()
	close(results)

	taskIDs := make(map[string]struct{})
	for result := range results {
		require.NoError(t, result.err)
		require.NotNil(t, result.handle)
		taskIDs[result.handle.ID()] = struct{}{}
	}
	require.Equal(t, map[string]struct{}{handle.ID(): {}}, taskIDs)
	inputs, err := manager.ListInputs(ctx, &task.ListInputsRequest{TaskID: handle.ID()})
	require.NoError(t, err)
	require.Len(t, inputs.Inputs, callers+1)

	agent.unblock()
	result, err := controller.Wait(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, "done", result.FinalMessage.Content)
	actual, identityConflict := agent.capturedMessages()
	require.False(t, identityConflict)
	sort.Strings(actual)
	expected := make([]string, callers)
	for index := 0; index < callers; index++ {
		expected[index] = fmt.Sprintf("message-%02d", index)
	}
	require.Equal(t, expected, actual)
}

// TestAttack_WaitingInputAndSuspendHaveDistinctReleaseSemantics verifies that
// waiting input wakes on a durable signal while planned suspension requires an
// explicit Continue release.
func TestAttack_WaitingInputAndSuspendHaveDistinctReleaseSemantics(t *testing.T) {
	t.Run("waiting input wakes on resume signal", func(t *testing.T) {
		ctx := context.Background()
		controller, manager, _ := newControllerWithAgentForTest(
			t,
			&interruptThenCompleteAgent{name: "worker"},
			completionBarrierFunc[*schema.Message](func(
				context.Context,
				*CompletionContext[*schema.Message],
			) (CompletionAction, error) {
				return CompletionComplete, nil
			}),
			testEventMapper,
		)
		t.Cleanup(func() { closeIntegrationManager(t, manager) })

		handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
			InvocationID: "attack-waiting", ParentSessionID: "root-session",
			AgentName: "worker", StartMode: task.StartModeBackground,
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("work")},
			},
		})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			snapshot, getErr := manager.Get(ctx, handle.ID())
			return getErr == nil && snapshot.Status == background.StatusWaitingInput
		}, time.Second, time.Millisecond)

		require.NoError(t, controller.SendInput(ctx, handle.ID(), &task.Input{
			EventID: "attack-resume", Kind: ResumeInputKind,
			Data: []byte(`{"approve":"yes"}`),
		}))
		result, err := controller.Wait(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, "approved", result.FinalMessage.Content)
		completed, err := manager.Get(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, background.StatusCompleted, completed.Status)
		require.Equal(t, int64(2), completed.Attempt)
	})

	t.Run("suspend requires explicit release", func(t *testing.T) {
		ctx := context.Background()
		var barrierCalls int64
		controller, manager, _ := newControllerForTest(
			t,
			completionBarrierFunc[*schema.Message](func(
				context.Context,
				*CompletionContext[*schema.Message],
			) (CompletionAction, error) {
				if atomic.AddInt64(&barrierCalls, 1) == 1 {
					return CompletionSuspend, nil
				}
				return CompletionComplete, nil
			}),
			testEventMapper,
		)
		t.Cleanup(func() { closeIntegrationManager(t, manager) })

		handle, err := controller.Start(ctx, &StartRequest[*schema.Message]{
			InvocationID: "attack-suspend", ParentSessionID: "root-session",
			ChildSessionID: "attack-suspended-child",
			AgentName:      "worker", StartMode: task.StartModeBackground,
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("work")},
			},
		})
		require.NoError(t, err)
		require.Eventually(t, func() bool {
			snapshot, getErr := manager.Get(ctx, handle.ID())
			return getErr == nil && snapshot.Status == background.StatusSuspended
		}, time.Second, time.Millisecond)

		encoded, err := encodeTypedInput(&adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("queued")},
		})
		require.NoError(t, err)
		data, err := json.Marshal(encoded)
		require.NoError(t, err)
		require.NoError(t, controller.SendInput(ctx, handle.ID(), &task.Input{
			EventID: "attack-queued", Kind: messageInputKind, Data: data,
		}))
		stillSuspended, err := manager.Get(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, background.StatusSuspended, stillSuspended.Status)

		released, err := controller.Continue(ctx, &ContinueRequest[*schema.Message]{
			ChildSessionID: handle.ChildSessionID(),
			InvocationID:   "attack-explicit-release",
			Input: &adk.AgentInput{
				Messages: []*schema.Message{schema.UserMessage("release")},
			},
		})
		require.NoError(t, err)
		require.Equal(t, handle.ID(), released.ID())
		result, err := controller.Wait(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, "done", result.FinalMessage.Content)
		completed, err := manager.Get(ctx, handle.ID())
		require.NoError(t, err)
		require.Equal(t, background.StatusCompleted, completed.Status)
		require.Equal(t, int64(2), completed.Attempt)
	})
}

// TestAttack_RecoveryCancellationHookIsIdempotent verifies that a hook retry on
// a recovered attempt carries stable identity, allowing one logical side effect
// even when the first acknowledgement fails after applying it.
func TestAttack_RecoveryCancellationHookIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store := background.NewInMemoryStore(&background.InMemoryStoreConfig{
		ActiveAttemptTimeout: 10 * time.Millisecond,
	})
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	hookErr := errors.New("transient cancellation acknowledgement failure")
	var hookCalls int64
	var sideEffects int64
	var seen sync.Map
	observed := make(chan string, 2)
	hook := cancellationHookFunc(func(
		_ context.Context,
		taskID, childSessionID, reason string,
	) error {
		call := atomic.AddInt64(&hookCalls, 1)
		key := taskID + "\x00" + childSessionID + "\x00" + reason
		observed <- key
		if _, loaded := seen.LoadOrStore(key, struct{}{}); !loaded {
			atomic.AddInt64(&sideEffects, 1)
		}
		if call == 1 {
			return hookErr
		}
		return nil
	})

	manager1 := newIntegrationManager(t, store)
	controller1, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager1,
		Barrier: completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
		InputsToAgentInput: testEventMapper,
		CancellationHook:   hook,
		SessionStore:       sessionStore,
		CheckPointStore:    sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller1.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{
			Agent: &resumableTestAgent{name: "worker"},
		},
	))
	metadata := &runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "root-session",
		RootSessionID: "root-session", ChildSessionID: "recovery-child",
		AgentName: "worker", StartMode: task.StartModeBackground,
	}
	identity, err := json.Marshal(metadata)
	require.NoError(t, err)
	reserved, err := manager1.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "recovery-task", InvocationID: "recovery-task",
		Identity: identity, RootSessionID: metadata.RootSessionID,
		ChildSessionID: metadata.ChildSessionID,
	})
	require.NoError(t, err)
	encoded, err := encodeTypedInput(&adk.AgentInput{
		Messages: []*schema.Message{schema.UserMessage("work")},
	})
	require.NoError(t, err)
	initialData, err := json.Marshal(encoded)
	require.NoError(t, err)
	terminal, err := controller1.prepareStartMailbox(
		ctx, reserved, "recovery-task", initialData,
	)
	require.NoError(t, err)
	require.False(t, terminal)
	handle := controller1.newHandle(reserved.Mailbox.TaskID, metadata.ChildSessionID)
	submitted, err := controller1.submitTask(ctx, handle, metadata, nil)
	require.NoError(t, err)
	started, err := store.Start(ctx, &background.StartTaskRequest{
		TaskID: submitted.Spec.ID, ExpectedVersion: submitted.Version,
	})
	require.NoError(t, err)
	yielded, err := store.Yield(ctx, &background.YieldTaskRequest{
		TaskID: started.Spec.ID, ExpectedVersion: started.Version,
	})
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, yielded.Status)
	requested, err := manager1.RequestCancel(
		ctx,
		handle.ID(),
		background.WithCancellationReason("operator stop"),
	)
	require.NoError(t, err)
	require.Equal(t, background.StatusPending, requested.Status)
	require.NotNil(t, requested.CancelRequestedAt)

	err = manager1.Execute(ctx, handle.ID())
	require.ErrorIs(t, err, hookErr)
	closeIntegrationManager(t, manager1)
	require.Eventually(t, func() bool {
		current, getErr := store.Get(ctx, handle.ID())
		return getErr == nil && current.Status == background.StatusPending
	}, time.Second, time.Millisecond)

	manager2 := newIntegrationManager(t, store)
	t.Cleanup(func() { closeIntegrationManager(t, manager2) })
	controller2, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager2,
		Barrier: completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
		InputsToAgentInput: testEventMapper,
		CancellationHook:   hook,
		SessionStore:       sessionStore,
		CheckPointStore:    sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller2.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{
			Agent: &resumableTestAgent{name: "worker"},
		},
	))
	require.NoError(t, manager2.Execute(ctx, handle.ID()))

	canceled, err := manager2.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, background.StatusCanceled, canceled.Status)
	require.Equal(t, "operator stop", canceled.ResultError)
	require.Equal(t, int64(2), atomic.LoadInt64(&hookCalls))
	require.Equal(t, int64(1), atomic.LoadInt64(&sideEffects))
	require.Equal(t, <-observed, <-observed)
}

// TestAttack_ProgressUsesReadAccessAndDirectParentScope verifies that progress
// projection cannot be mistaken for managed execution and never broadens a
// deeply nested task from its direct parent to the root session.
func TestAttack_ProgressUsesReadAccessAndDirectParentScope(t *testing.T) {
	ctx := context.Background()
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	lifecycleStore := background.NewInMemoryStore(nil)
	manager := newIntegrationManager(t, lifecycleStore)
	t.Cleanup(func() { closeIntegrationManager(t, manager) })
	requests := make(chan RuntimeSessionStoreRequest, 4)
	controller, err := NewController(&ControllerConfig[*schema.Message]{
		Manager: manager,
		Barrier: completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
		InputsToAgentInput: testEventMapper,
		SessionStoreFactory: func(
			_ context.Context,
			request *RuntimeSessionStoreRequest,
		) (adk.SessionEventStore[*schema.Message], error) {
			requests <- *request
			return sessionStore, nil
		},
		CheckPointStore: sessionStore,
	})
	require.NoError(t, err)
	require.NoError(t, controller.RegisterAgent(
		"worker",
		&AgentRegistration[*schema.Message]{
			Agent: &resumableTestAgent{name: "worker"},
		},
	))

	root, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "progress-root", InvocationID: "progress-root",
		RootSessionID: "root-session",
	})
	require.NoError(t, err)
	parent, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "progress-parent", InvocationID: "progress-parent",
		ChildSessionID: "direct-parent-session",
		ParentExecution: &task.ExecutionContext{
			TaskID: root.Mailbox.TaskID, Owner: task.OwnerParent,
			Generation: root.Mailbox.Generation, RootSessionID: "root-session",
		},
	})
	require.NoError(t, err)
	nestedCtx := task.WithExecutionContext(ctx, task.ExecutionContext{
		TaskID: parent.Mailbox.TaskID, Owner: task.OwnerParent,
		Generation: parent.Mailbox.Generation, RootSessionID: "root-session",
	})
	handle, err := controller.Start(nestedCtx, &StartRequest[*schema.Message]{
		InvocationID: "progress-child", ParentSessionID: "direct-parent-session",
		AgentName: "worker", StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("work")},
		},
	})
	require.NoError(t, err)
	_, err = controller.Wait(ctx, handle.ID())
	require.NoError(t, err)
	snapshot, err := manager.Get(ctx, handle.ID())
	require.NoError(t, err)
	require.Equal(t, parent.Mailbox.TaskID, snapshot.Spec.ParentTaskID)
	require.Equal(t, "root-session", snapshot.Spec.RootSessionID)

	progress, err := controller.ReadProgress(
		ctx,
		snapshot,
		func(_ context.Context, agentName string, message *schema.Message) (string, error) {
			return agentName + ": " + message.Content, nil
		},
	)
	require.NoError(t, err)
	require.Contains(t, progress, "worker: done")

	var managed, read *RuntimeSessionStoreRequest
	for index := 0; index < 2; index++ {
		request := awaitIntegrationValue(t, requests)
		copy := request
		switch request.AccessMode {
		case RuntimeSessionStoreAccessManagedExecute:
			managed = &copy
		case RuntimeSessionStoreAccessReadProgress:
			read = &copy
		}
	}
	require.NotNil(t, managed)
	require.NotNil(t, read)
	for _, request := range []*RuntimeSessionStoreRequest{managed, read} {
		require.Equal(t, handle.ID(), request.TaskID)
		require.Equal(t, "direct-parent-session", request.ParentSessionID)
		require.NotEqual(t, snapshot.Spec.RootSessionID, request.ParentSessionID)
		require.Equal(t, handle.ChildSessionID(), request.ChildSessionID)
		require.NotNil(t, request.Task)
	}
	require.Equal(t, RuntimeSessionStoreAccessManagedExecute, managed.AccessMode)
	require.Equal(t, RuntimeSessionStoreAccessReadProgress, read.AccessMode)
}

// TestAttack_StreamPrefixReplayAndPersistenceFailure verifies that incomplete
// stream prefixes remain replayable progress while stream-event identity
// failures fail the task closed and replay the same failure.
func TestAttack_StreamPrefixReplayAndPersistenceFailure(t *testing.T) {
	t.Run("incomplete prefix is stable across progress replay", func(t *testing.T) {
		ctx := context.Background()
		controller, manager, sessionStore := newControllerForTest(
			t,
			completionBarrierFunc[*schema.Message](func(
				context.Context,
				*CompletionContext[*schema.Message],
			) (CompletionAction, error) {
				return CompletionComplete, nil
			}),
			testEventMapper,
		)
		t.Cleanup(func() { closeIntegrationManager(t, manager) })
		metadata, err := json.Marshal(&runtimeMetadata{
			Version: runtimeMetadataVersion, ParentSessionID: "direct-parent",
			RootSessionID: "root-session", ChildSessionID: "stream-child",
			AgentName: "worker", StartMode: task.StartModeBackground,
		})
		require.NoError(t, err)
		_, err = manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
			CandidateTaskID: "stream-prefix", InvocationID: "stream-prefix",
			Identity: metadata, RootSessionID: "root-session",
			ChildSessionID: "stream-child",
		})
		require.NoError(t, err)
		payload, err := json.Marshal(&taskPayload{
			Version: payloadVersion, SubAgentName: "worker",
			ChildSessionID: "stream-child",
		})
		require.NoError(t, err)
		snapshot := &background.TaskSnapshot{
			Spec: background.Spec{
				ID: "stream-prefix", ExecutorKey: ExecutorKey, Kind: "subagent",
				Payload: payload, RootSessionID: "root-session",
			},
			Status: background.StatusRunning,
		}
		require.NoError(t, sessionStore.AppendEvents(
			ctx,
			"stream-child",
			[]*adk.SessionEvent[*schema.Message]{
				{
					EventID: "stream-input", Kind: adk.SessionEventMessage,
					Message: schema.UserMessage("secret input"),
					Extra:   map[string]any{taskIDEventExtraKey: "stream-prefix"},
				},
				{
					EventID: "stream-output",
					Kind:    adk.SessionEventMessageStreamIncomplete,
					MessageStreamIncomplete: &adk.MessageStreamIncompleteEvent[*schema.Message]{
						Message: schema.AssistantMessage("durable prefix", nil),
						Error:   "transport failed",
					},
					Extra: map[string]any{taskIDEventExtraKey: "stream-prefix"},
				},
			},
		))
		format := func(
			_ context.Context,
			agentName string,
			message *schema.Message,
		) (string, error) {
			return agentName + ": " + message.Content, nil
		}

		first, err := controller.ReadProgress(ctx, snapshot, format)
		require.NoError(t, err)
		second, err := controller.ReadProgress(ctx, snapshot, format)
		require.NoError(t, err)
		require.Equal(t, first, second)
		require.Contains(t, first, "worker: durable prefix")
		require.NotContains(t, first, "secret input")
	})

	t.Run("stream persistence failure is replayable", func(t *testing.T) {
		ctx := context.Background()
		model := &preemptModel{started: make(chan struct{}), runs: 1}
		agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
			Name: "worker", Description: "stream persistence attack", Model: model,
		})
		require.NoError(t, err)
		sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
		manager := newIntegrationManager(t, background.NewInMemoryStore(nil))
		t.Cleanup(func() { closeIntegrationManager(t, manager) })
		persistErr := errors.New("stream event id allocation failed")
		controller, err := NewController(&ControllerConfig[*schema.Message]{
			Manager: manager,
			Barrier: completionBarrierFunc[*schema.Message](func(
				context.Context,
				*CompletionContext[*schema.Message],
			) (CompletionAction, error) {
				return CompletionComplete, nil
			}),
			InputsToAgentInput: testEventMapper,
			SessionStore:       sessionStore,
			CheckPointStore:    sessionStore,
			SessionConfig: &adk.SessionConfig[*schema.Message]{
				EventIDGenerator: func(
					ctx context.Context,
					event *adk.SessionEvent[*schema.Message],
				) (string, error) {
					if event.Kind == adk.SessionEventMessage && event.Message == nil {
						return "", persistErr
					}
					return adk.DefaultSessionEventIDGenerator[*schema.Message](ctx, event)
				},
			},
		})
		require.NoError(t, err)
		require.NoError(t, controller.RegisterAgent(
			"worker",
			&AgentRegistration[*schema.Message]{Agent: agent},
		))
		request := &StartRequest[*schema.Message]{
			InvocationID: "stream-persist-failure", ParentSessionID: "root-session",
			AgentName: "worker", StartMode: task.StartModeForeground,
			Input: &adk.AgentInput{
				Messages:        []*schema.Message{schema.UserMessage("stream")},
				EnableStreaming: true,
			},
		}

		handle, err := controller.Start(ctx, request)
		require.NoError(t, err)
		_, err = controller.Wait(ctx, handle.ID())
		require.ErrorIs(t, err, persistErr)
		replayed, err := controller.Start(ctx, request)
		require.NoError(t, err)
		require.Equal(t, handle.ID(), replayed.ID())
		_, err = controller.Wait(ctx, replayed.ID())
		require.ErrorContains(t, err, persistErr.Error())

		events, err := sessionStore.LoadEvents(
			ctx,
			handle.ChildSessionID(),
			&adk.LoadSessionEventsRequest{
				Kinds: []adk.SessionEventKind{adk.SessionEventMessage},
			},
		)
		require.NoError(t, err)
		require.Len(t, events.Events, 1)
		require.Equal(t, adk.SessionEventMessage, events.Events[0].Kind)
		require.NotNil(t, events.Events[0].Message)
		require.Equal(t, schema.User, events.Events[0].Message.Role)
		require.Equal(t, "stream", events.Events[0].Message.Content)
		for _, event := range events.Events {
			if event.Kind == adk.SessionEventMessage && event.Message != nil {
				require.NotEqual(t, schema.Assistant, event.Message.Role)
			}
		}
	})
}
