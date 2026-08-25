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
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

func TestRuntimeSessionStoreFactoryAccessModes(t *testing.T) {
	ctx := context.Background()
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	lifecycleStore := background.NewInMemoryStore(nil)
	manager, err := background.New(ctx, &background.Config{
		Tasks: lifecycleStore, TaskEvents: lifecycleStore,
		SendTaskCreatedEvent: func(
			context.Context,
			*background.TaskSnapshot,
		) error {
			return nil
		},
	})
	require.NoError(t, err)
	t.Cleanup(func() {
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		require.NoError(t, manager.Close(closeCtx))
	})

	requests := make(chan RuntimeSessionStoreRequest, 3)
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

	foreground, err := controller.Start(ctx, &StartRequest[*schema.Message]{
		InvocationID: "foreground", ParentSessionID: "parent",
		AgentName: "worker", StartMode: task.StartModeForeground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("foreground")},
		},
	})
	require.NoError(t, err)
	_, err = foreground.Wait(ctx)
	require.NoError(t, err)
	foregroundRequest := <-requests
	require.Equal(
		t,
		RuntimeSessionStoreAccessForegroundExecute,
		foregroundRequest.AccessMode,
	)
	require.Nil(t, foregroundRequest.Task)

	root, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "root-task", InvocationID: "root-task",
		RootSessionID: "root-session",
	})
	require.NoError(t, err)
	parent, err := manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "parent-task", InvocationID: "parent-task",
		ChildSessionID: "parent-child-session",
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
	detached, err := controller.Start(nestedCtx, &StartRequest[*schema.Message]{
		InvocationID:    "deep-background",
		ParentSessionID: "parent-child-session",
		AgentName:       "worker", StartMode: task.StartModeBackground,
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("background")},
		},
	})
	require.NoError(t, err)
	_, err = detached.Wait(ctx)
	require.NoError(t, err)
	backgroundRequest := <-requests
	require.Equal(
		t,
		RuntimeSessionStoreAccessManagedExecute,
		backgroundRequest.AccessMode,
	)
	require.Equal(t, "parent-child-session", backgroundRequest.ParentSessionID)
	require.NotNil(t, backgroundRequest.Task)

	snapshot, err := manager.Get(ctx, detached.ID())
	require.NoError(t, err)
	require.Equal(t, parent.Mailbox.TaskID, snapshot.Spec.ParentTaskID)
	require.Equal(t, "root-session", snapshot.Spec.RootSessionID)
	_, err = controller.ReadProgress(
		ctx,
		snapshot,
		func(_ context.Context, _ string, message *schema.Message) (string, error) {
			return message.Content, nil
		},
	)
	require.NoError(t, err)
	progressRequest := <-requests
	require.Equal(
		t,
		RuntimeSessionStoreAccessReadProgress,
		progressRequest.AccessMode,
	)
	require.Equal(t, "parent-child-session", progressRequest.ParentSessionID)
	require.NotEqual(t, snapshot.Spec.RootSessionID, progressRequest.ParentSessionID)
	require.Same(t, snapshot, progressRequest.Task)
}

func TestRuntimeSessionStoreRequestValidation(t *testing.T) {
	runtimeTask := &background.TaskSnapshot{
		Spec: background.Spec{ID: "task"},
	}
	base := RuntimeSessionStoreRequest{
		TaskID: "task", ParentSessionID: "parent", ChildSessionID: "child",
	}
	for _, testCase := range []struct {
		name    string
		request RuntimeSessionStoreRequest
		wantErr bool
	}{
		{
			name: "foreground",
			request: RuntimeSessionStoreRequest{
				TaskID: "task", ParentSessionID: "parent",
				ChildSessionID: "child",
				AccessMode:     RuntimeSessionStoreAccessForegroundExecute,
			},
		},
		{
			name: "managed",
			request: RuntimeSessionStoreRequest{
				TaskID: "task", ParentSessionID: "parent",
				ChildSessionID: "child", Task: runtimeTask,
				AccessMode: RuntimeSessionStoreAccessManagedExecute,
			},
		},
		{
			name: "read progress",
			request: RuntimeSessionStoreRequest{
				TaskID: "task", ParentSessionID: "parent",
				ChildSessionID: "child", Task: runtimeTask,
				AccessMode: RuntimeSessionStoreAccessReadProgress,
			},
		},
		{name: "unknown mode", request: base, wantErr: true},
		{
			name: "foreground with task",
			request: RuntimeSessionStoreRequest{
				TaskID: "task", ParentSessionID: "parent",
				ChildSessionID: "child", Task: runtimeTask,
				AccessMode: RuntimeSessionStoreAccessForegroundExecute,
			},
			wantErr: true,
		},
		{
			name: "managed without task",
			request: RuntimeSessionStoreRequest{
				TaskID: "task", ParentSessionID: "parent",
				ChildSessionID: "child",
				AccessMode:     RuntimeSessionStoreAccessManagedExecute,
			},
			wantErr: true,
		},
		{
			name: "read progress without task",
			request: RuntimeSessionStoreRequest{
				TaskID: "task", ParentSessionID: "parent",
				ChildSessionID: "child",
				AccessMode:     RuntimeSessionStoreAccessReadProgress,
			},
			wantErr: true,
		},
		{
			name: "snapshot ID mismatch",
			request: RuntimeSessionStoreRequest{
				TaskID: "other", ParentSessionID: "parent",
				ChildSessionID: "child", Task: runtimeTask,
				AccessMode: RuntimeSessionStoreAccessManagedExecute,
			},
			wantErr: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateRuntimeSessionStoreRequest(&testCase.request)
			if testCase.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
