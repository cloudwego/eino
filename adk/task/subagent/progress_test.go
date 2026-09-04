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
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

func TestControllerReadProgressIsolatesPersistentSessionByTask(t *testing.T) {
	ctx := context.Background()
	controller, manager, sessionStore := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	metadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "parent",
		RootSessionID: "parent", ChildSessionID: "shared-child",
		AgentName: "worker", StartMode: task.StartModeBackground,
	})
	require.NoError(t, err)
	_, err = manager.RegisterMailbox(ctx, &task.RegisterMailboxRequest{
		CandidateTaskID: "target", InvocationID: "target",
		Identity: metadata, RootSessionID: "parent",
		ChildSessionID: "shared-child",
	})
	require.NoError(t, err)
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, SubAgentName: "worker",
		ChildSessionID: "shared-child",
	})
	require.NoError(t, err)
	backgroundTask := &background.TaskSnapshot{
		Spec: background.Spec{
			ID: "target", ExecutorKey: ExecutorKey, Kind: "subagent",
			Payload: payload, RootSessionID: "parent",
		},
		Status: background.StatusWaitingInput,
	}
	events := []*adk.SessionEvent[*schema.Message]{
		{
			EventID: "input", Kind: adk.SessionEventMessage,
			Message: schema.UserMessage("submitted"),
			Extra:   map[string]any{taskIDEventExtraKey: "target"},
		},
		{
			EventID: "progress", Kind: adk.SessionEventMessage,
			Message: schema.AssistantMessage("progress", nil),
			Extra:   map[string]any{taskIDEventExtraKey: "target"},
		},
		{
			EventID: "other", Kind: adk.SessionEventMessage,
			Message: schema.AssistantMessage("other task", nil),
			Extra:   map[string]any{taskIDEventExtraKey: "other"},
		},
		{
			EventID: "interrupt", Kind: adk.SessionEventInterrupt,
			Interrupt: &adk.InterruptEvent{Contexts: []*adk.InterruptContext{{
				InterruptID: "approval", Info: "approve?",
			}}},
			Extra: map[string]any{taskIDEventExtraKey: "target"},
		},
	}
	require.NoError(t, sessionStore.AppendEvents(ctx, "shared-child", events))

	progress, err := controller.ReadProgress(
		ctx,
		backgroundTask,
		func(_ context.Context, agentName string, message *schema.Message) (string, error) {
			return agentName + ": " + message.Content, nil
		},
	)
	require.NoError(t, err)
	require.Contains(t, progress, "worker: progress")
	require.NotContains(t, progress, "submitted")
	require.NotContains(t, progress, "other task")
	require.Contains(t, progress, "Input required:")
	require.Contains(t, progress, "approval")
}

func TestReadProgressEnforcesByteLimit(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	const (
		sessionID = "byte-limit-session"
		taskID    = "byte-limit-task"
	)
	events := []*adk.SessionEvent[*schema.Message]{{
		EventID: "query", Kind: adk.SessionEventMessage,
		Message: schema.UserMessage("query"),
		Extra:   map[string]any{taskIDEventExtraKey: taskID},
	}}
	for index := 1; index <= 70; index++ {
		content := fmt.Sprintf("%03d%s", index, strings.Repeat("x", 1020))
		events = append(events, &adk.SessionEvent[*schema.Message]{
			EventID: fmt.Sprintf("progress-%03d", index),
			Kind:    adk.SessionEventMessage,
			Message: schema.AssistantMessage(content, nil),
			Extra:   map[string]any{taskIDEventExtraKey: taskID},
		})
	}
	require.NoError(t, store.AppendEvents(ctx, sessionID, events))

	progress, err := readProgress[*schema.Message](
		ctx,
		adk.SessionEventStore[*schema.Message](store),
		&background.TaskSnapshot{
			Spec: background.Spec{ID: taskID},
		},
		sessionID,
		"worker",
		func(_ context.Context, _ string, message *schema.Message) (string, error) {
			return message.Content, nil
		},
	)
	require.NoError(t, err)
	lines := strings.Split(progress, "\n")
	require.Len(t, lines, 66)
	require.Equal(t, "Transcript:", lines[0])
	require.Equal(t, "[transcript records omitted due to display limits]", lines[1])
	require.True(t, strings.HasPrefix(lines[2], "007"))
	require.True(t, strings.HasPrefix(lines[len(lines)-1], "070"))
	require.LessOrEqual(
		t,
		len(strings.Join(lines[2:], "\n"))+1,
		maxProgressBytes,
	)
}

func TestControllerReadProgressValidation(t *testing.T) {
	var nilController *Controller[*schema.Message]
	_, err := nilController.ReadProgress(
		context.Background(),
		&background.TaskSnapshot{Spec: background.Spec{ExecutorKey: ExecutorKey}},
		func(context.Context, string, *schema.Message) (string, error) {
			return "", nil
		},
	)
	require.Error(t, err)

	controller, _, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	progress, err := controller.ReadProgress(
		context.Background(),
		&background.TaskSnapshot{Spec: background.Spec{ExecutorKey: "other"}},
		func(context.Context, string, *schema.Message) (string, error) {
			return "", nil
		},
	)
	require.NoError(t, err)
	require.Empty(t, progress)
}

func TestControllerReadProgressValidatesPersistedRuntimeMetadata(t *testing.T) {
	ctx := context.Background()
	controller, manager, _ := newControllerForTest(
		t,
		completeBarrier[*schema.Message](),
		testEventMapper,
	)
	validMetadata, err := json.Marshal(&runtimeMetadata{
		Version: runtimeMetadataVersion, ParentSessionID: "direct-parent",
		RootSessionID: "root", ChildSessionID: "other-child",
		AgentName: "worker", StartMode: task.StartModeBackground,
	})
	require.NoError(t, err)
	for _, testCase := range []struct {
		name      string
		identity  []byte
		childID   string
		errorText string
	}{
		{
			name: "malformed identity", identity: []byte("{"),
			childID: "child", errorText: "decode progress runtime metadata",
		},
		{
			name: "payload mismatch", identity: validMetadata,
			childID: "other-child", errorText: "do not match",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			taskID := "task-" + testCase.name
			_, registerErr := manager.RegisterMailbox(
				ctx,
				&task.RegisterMailboxRequest{
					CandidateTaskID: taskID, InvocationID: taskID,
					Identity: testCase.identity, RootSessionID: "root",
					ChildSessionID: testCase.childID,
				},
			)
			require.NoError(t, registerErr)
			payload, marshalErr := json.Marshal(&taskPayload{
				Version: payloadVersion, SubAgentName: "worker",
				ChildSessionID: "child",
			})
			require.NoError(t, marshalErr)
			_, readErr := controller.ReadProgress(
				ctx,
				&background.TaskSnapshot{Spec: background.Spec{
					ID: taskID, ExecutorKey: ExecutorKey, Kind: "subagent",
					Payload: payload, RootSessionID: "root",
				}},
				func(context.Context, string, *schema.Message) (string, error) {
					return "", nil
				},
			)
			require.ErrorContains(t, readErr, testCase.errorText)
		})
	}
}
