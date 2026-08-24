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
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/bytedance/sonic"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	durablesubagent "github.com/cloudwego/eino/adk/task/subagent"
	"github.com/cloudwego/eino/schema"
)

const expectedProgressRecords = 100

func TestNewDurableProgressReaderRequiresExecutor_BitsUT(t *testing.T) {
	reader, err := NewDurableProgressReader[*schema.Message](nil, nil)
	require.Error(t, err)
	require.Nil(t, reader)
}

func TestDurableProgressReaderDelegatesAndRejectsNilReceiver_BitsUT(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	task := durableProgressTask[*schema.Message](t, background.StatusRunning)
	reader, err := NewDurableProgressReader(progressController(t, store), nil)
	require.NoError(t, err)

	progress, err := reader.ReadProgress(ctx, task)
	require.NoError(t, err)
	require.Empty(t, progress)

	var nilReader *DurableProgressReader[*schema.Message]
	progress, err = nilReader.ReadProgress(ctx, task)
	require.Empty(t, progress)
	require.EqualError(t, err, "subagent: durable Controller is required to read task progress")
}

func TestReadDurableTaskProgress(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	task := durableProgressTask[*schema.Message](t, background.StatusWaitingInput)
	sessionID := progressChildSessionID
	require.NoError(t, store.AppendEvents(ctx, sessionID, taskProgressEvents(
		task.Spec.ID,
		[]*adk.SessionEvent[*schema.Message]{
			{
				EventID: "query", Kind: adk.SessionEventMessage,
				Message: schema.UserMessage("submitted query"),
			},
			{
				EventID: "first", Kind: adk.SessionEventMessage,
				Message: schema.AssistantMessage("first progress", nil),
			},
			{
				EventID: "partial", Kind: adk.SessionEventMessageStreamIncomplete,
				MessageStreamIncomplete: &adk.MessageStreamIncompleteEvent[*schema.Message]{
					Message: schema.AssistantMessage("partial progress", nil),
					Error:   "stream interrupted",
				},
			},
			{
				EventID: "interrupt", Kind: adk.SessionEventInterrupt,
				Interrupt: &adk.InterruptEvent{Contexts: []*adk.InterruptContext{
					{InterruptID: "agent:worker;tool:approve:call_1", Info: "approve?"},
				}},
			},
		},
	)))

	format := func(_ context.Context, agentName string, message *schema.Message) (string, error) {
		return agentName + ": " + message.Content, nil
	}
	controller := progressController(t, store)
	progress, err := controller.ReadProgress(
		ctx, task, format,
	)
	require.NoError(t, err)
	assert.Contains(t, progress, "worker: first progress\nworker: partial progress")
	assert.NotContains(t, progress, "submitted query")
	assert.Contains(t, progress, "Input required:")
	assert.Contains(t, progress, "agent:worker;tool:approve:call_1")
	assert.Contains(t, progress, "approve?")
}

func TestReadDurableTaskProgressIncludesAgenticToolResults(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.AgenticMessage](nil)
	task := durableProgressTask[*schema.AgenticMessage](t, background.StatusRunning)
	sessionID := progressChildSessionID
	toolResult := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{{
			Type: schema.ContentBlockTypeFunctionToolResult,
			FunctionToolResult: &schema.FunctionToolResult{
				CallID: "call_1",
				Name:   "lookup",
				Content: []*schema.FunctionToolResultContentBlock{{
					Type: schema.FunctionToolResultContentBlockTypeText,
					Text: &schema.UserInputText{Text: "tool result"},
				}},
			},
		}},
	}
	require.NoError(t, store.AppendEvents(ctx, sessionID, taskProgressEvents(
		task.Spec.ID,
		[]*adk.SessionEvent[*schema.AgenticMessage]{
			{
				EventID: "query", Kind: adk.SessionEventMessage,
				Message: schema.UserAgenticMessage("submitted query"),
			},
			{EventID: "tool-result", Kind: adk.SessionEventMessage, Message: toolResult},
		},
	)))

	controller := progressController(t, store)
	progress, err := controller.ReadProgress(
		ctx, task,
		func(_ context.Context, agentName string, message *schema.AgenticMessage) (string, error) {
			return agentName + ": " + message.String(), nil
		},
	)
	require.NoError(t, err)
	assert.Contains(t, progress, "tool result")
	assert.NotContains(t, progress, "submitted query")
}

func TestDefaultTranscriptFormatStripsMessageExtra(t *testing.T) {
	message := schema.AssistantMessage("done", nil)
	message.Extra = map[string]any{"private": "value"}
	formatted, err := defaultTranscriptFormat(
		context.Background(), "worker", message,
	)
	require.NoError(t, err)
	assert.Contains(t, formatted, `"agent_name":"worker"`)
	assert.Contains(t, formatted, `"content":"done"`)
	assert.NotContains(t, formatted, "private")

	customSawExtra := false
	custom := TranscriptFormat[*schema.Message](func(
		_ context.Context,
		_ string,
		got *schema.Message,
	) (string, error) {
		customSawExtra = got.Extra["private"] == "value"
		return got.Content, nil
	})
	_, err = custom(context.Background(), "worker", message)
	require.NoError(t, err)
	assert.True(t, customSawExtra)
}

func TestReadDurableTaskProgressBoundsRecentMessages(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	task := durableProgressTask[*schema.Message](t, background.StatusRunning)
	sessionID := progressChildSessionID
	events := []*adk.SessionEvent[*schema.Message]{{
		EventID: "query", Kind: adk.SessionEventMessage,
		Message: schema.UserMessage("submitted query"),
	}}
	for i := 1; i <= expectedProgressRecords+2; i++ {
		events = append(events, &adk.SessionEvent[*schema.Message]{
			EventID: fmt.Sprintf("progress-%03d", i),
			Kind:    adk.SessionEventMessage,
			Message: schema.AssistantMessage(fmt.Sprintf("progress-%03d", i), nil),
		})
	}
	require.NoError(t, store.AppendEvents(
		ctx, sessionID, taskProgressEvents(task.Spec.ID, events),
	))

	controller := progressController(t, store)
	progress, err := controller.ReadProgress(
		ctx, task,
		func(_ context.Context, agentName string, message *schema.Message) (string, error) {
			return agentName + ": " + message.Content, nil
		},
	)
	require.NoError(t, err)
	assert.Contains(t, progress, "transcript records omitted due to display limits")
	assert.Equal(t, expectedProgressRecords, strings.Count(progress, "worker: progress-"))
	assert.NotContains(t, progress, "worker: progress-001")
	assert.Contains(t, progress, "worker: progress-102")
}

func TestAttack_SharedSessionProgressDoesNotLeakAcrossTasks(t *testing.T) {
	ctx := context.Background()
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	task := durableProgressTask[*schema.Message](t, background.StatusCompleted)
	events := taskProgressEvents(task.Spec.ID, []*adk.SessionEvent[*schema.Message]{
		{
			EventID: "target-query", Kind: adk.SessionEventMessage,
			Message: schema.UserMessage("target query"),
		},
		{
			EventID: "target-result", Kind: adk.SessionEventMessage,
			Message: schema.AssistantMessage("target result", nil),
		},
	})
	for index := 0; index < 150; index++ {
		events = append(events, taskProgressEvents(
			"other-task",
			[]*adk.SessionEvent[*schema.Message]{{
				EventID: fmt.Sprintf("other-%03d", index),
				Kind:    adk.SessionEventMessage,
				Message: schema.AssistantMessage("other result", nil),
			}},
		)...)
	}
	require.NoError(t, store.AppendEvents(
		ctx, progressChildSessionID, events,
	))

	controller := progressController(t, store)
	progress, err := controller.ReadProgress(
		ctx,
		task,
		func(
			_ context.Context,
			_ string,
			message *schema.Message,
		) (string, error) {
			return message.Content, nil
		},
	)
	require.NoError(t, err)
	require.Contains(t, progress, "target result")
	require.NotContains(t, progress, "target query")
	require.NotContains(t, progress, "other result")
}

const progressChildSessionID = "subagent-session/cGFyZW50/d29ya2Vy/subagent_task"

func taskProgressEvents[M adk.MessageType](
	taskID string,
	events []*adk.SessionEvent[M],
) []*adk.SessionEvent[M] {
	for _, event := range events {
		if event != nil {
			event.Extra = map[string]any{"eino.task.id": taskID}
		}
	}
	return events
}

func durableProgressTask[M adk.MessageType](
	t *testing.T,
	status background.Status,
) *background.TaskSnapshot {
	t.Helper()
	payload, err := sonic.Marshal(map[string]any{
		"version": 1, "subagent_name": "worker",
		"child_session_id": progressChildSessionID,
	})
	require.NoError(t, err)
	return &background.TaskSnapshot{
		Spec: background.Spec{
			ID: "subagent_task", ExecutorKey: durablesubagent.ExecutorKey,
			Kind: TaskKindSubagent, Payload: payload, SessionID: "parent",
		},
		Status: status,
	}
}

type progressAgent[M adk.MessageType] struct{}

func (*progressAgent[M]) Name(context.Context) string        { return "worker" }
func (*progressAgent[M]) Description(context.Context) string { return "worker" }
func (*progressAgent[M]) Run(
	context.Context,
	*adk.TypedAgentInput[M],
	...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.TypedAgentEvent[M]] {
	iter, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[M]]()
	generator.Close()
	return iter
}
func (a *progressAgent[M]) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.TypedAgentEvent[M]] {
	return a.Run(ctx, &adk.TypedAgentInput[M]{}, options...)
}

type progressBarrier[M adk.MessageType] struct{}

func (progressBarrier[M]) Check(
	context.Context,
	*durablesubagent.CompletionContext[M],
) (durablesubagent.CompletionDecision, error) {
	return durablesubagent.Complete, nil
}

func progressController[M adk.MessageType](
	t *testing.T,
	store *adksession.InMemoryStore[M],
) *durablesubagent.Controller[M] {
	t.Helper()
	taskStore := background.NewInMemoryStore(nil)
	manager, err := background.New(context.Background(), &background.Config{
		Tasks: taskStore, TaskEvents: taskStore,
		SendTaskCreatedEvent: func(context.Context, *background.TaskSnapshot) error {
			return nil
		},
	})
	require.NoError(t, err)
	controller, err := durablesubagent.NewController(
		&durablesubagent.ControllerConfig[M]{
			Manager: manager,
			Barrier: progressBarrier[M]{},
			EventToInput: func(
				context.Context,
				[]*task.Input,
			) (*adk.TypedAgentInput[M], error) {
				return nil, errors.New("unused")
			},
			SessionStore: store, CheckPointStore: store,
		},
	)
	require.NoError(t, err)
	require.NoError(t, controller.RegisterAgent(
		"worker",
		&durablesubagent.AgentRegistration[M]{Agent: &progressAgent[M]{}},
	))
	return controller
}
