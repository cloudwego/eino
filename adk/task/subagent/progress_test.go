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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/schema"
)

func TestControllerReadProgressIsolatesPersistentSessionByTask(t *testing.T) {
	ctx := context.Background()
	controller, _, sessionStore := newControllerForTest(
		t,
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
		testEventMapper,
	)
	payload, err := json.Marshal(&taskPayload{
		Version: payloadVersion, SubAgentName: "worker",
		ChildSessionID: "shared-child",
	})
	require.NoError(t, err)
	backgroundTask := &background.TaskSnapshot{
		Spec: background.Spec{
			ID: "target", ExecutorKey: ExecutorKey, Kind: "subagent",
			Payload: payload,
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
		completionBarrierFunc[*schema.Message](func(
			context.Context,
			*CompletionContext[*schema.Message],
		) (CompletionAction, error) {
			return CompletionComplete, nil
		}),
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
