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

package local_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/subagent"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/adk/task"
	"github.com/cloudwego/eino/adk/task/background"
	"github.com/cloudwego/eino/adk/task/local"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type stage3ContextCaptureAgent struct {
	captured chan<- context.Context
}

func awaitIntegrationValue[T any](
	t *testing.T,
	values <-chan T,
	description string,
) T {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case value, ok := <-values:
		if !ok {
			t.Fatalf("%s channel closed before producing a value", description)
		}
		return value
	case <-timer.C:
		t.Fatalf("timed out after 1 second waiting for %s", description)
		var zero T
		return zero
	}
}

func (*stage3ContextCaptureAgent) Name(context.Context) string { return "capture" }

func (*stage3ContextCaptureAgent) Description(context.Context) string {
	return "capture runner context"
}

func (a *stage3ContextCaptureAgent) Run(
	ctx context.Context,
	_ *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.captured <- ctx
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("captured", nil),
		nil,
		schema.Assistant,
		"capture",
	))
	generator.Close()
	return iter
}

type stage3StreamingAgent struct{}

func (*stage3StreamingAgent) Name(context.Context) string { return "worker" }

func (*stage3StreamingAgent) Description(context.Context) string {
	return "stream output"
}

func (*stage3StreamingAgent) Run(
	context.Context,
	*adk.AgentInput,
	...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	stream, writer := schema.Pipe[*schema.Message](2)
	writer.Send(schema.AssistantMessage("hello ", nil), nil)
	writer.Send(schema.AssistantMessage("world", nil), nil)
	writer.Close()
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("buffered", nil),
		nil,
		schema.Assistant,
		"worker",
	))
	generator.Send(&adk.AgentEvent{
		AgentName: "worker",
		Output: &adk.AgentOutput{MessageOutput: &adk.MessageVariant{
			IsStreaming:   true,
			MessageStream: stream,
			Role:          schema.Assistant,
		}},
		SessionEventVariant: &adk.SessionEventVariant[*schema.Message]{
			MessageStreamRef: &adk.MessageStreamRef{
				EventID: "stream-event",
				Kind:    adk.SessionEventMessage,
			},
		},
	})
	generator.Close()
	return iter
}

func TestLocalTaskPersisterRestoresStreamingEventContext(t *testing.T) {
	ctx := context.Background()

	captured := make(chan context.Context, 1)
	sessionStore := adksession.NewInMemoryStore[*schema.Message](nil)
	parentRunner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           &stage3ContextCaptureAgent{captured: captured},
		CheckPointStore: sessionStore,
		SessionID:       "parent-session",
		SessionStore:    sessionStore,
	})
	iter := parentRunner.Query(ctx, "capture")
	for {
		if _, ok := iter.Next(); !ok {
			break
		}
	}
	runCtx := awaitIntegrationValue(t, captured, "captured parent runner context")

	store := background.NewInMemoryStore(nil)
	manager, err := background.New(ctx, &background.Config{
		Tasks: store, TaskEvents: store,
		IDGen: func(
			context.Context,
			*background.AllocateTaskIDRequest,
		) (string, error) {
			return "task_1", nil
		},
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
	localRunner, err := local.New(&local.Config{Manager: manager})
	require.NoError(t, err)

	type formatObservation struct {
		execution task.ExecutionContext
		ok        bool
		agentName string
		content   string
	}
	formatted := make(chan formatObservation, 2)
	middleware, err := subagent.New(ctx, &subagent.Config{
		SubAgents: []adk.Agent{&stage3StreamingAgent{}},
		Tasks: &subagent.TaskConfig{
			Local: &subagent.LocalTaskConfig{Runner: localRunner},
			TranscriptFormat: func(
				formatCtx context.Context,
				agentName string,
				message *schema.Message,
			) (string, error) {
				execution, ok := task.ExecutionContextFromContext(formatCtx)
				formatted <- formatObservation{
					execution: execution,
					ok:        ok,
					agentName: agentName,
					content:   message.Content,
				}
				return agentName + ": " + message.Content, nil
			},
		},
	})
	require.NoError(t, err)
	_, agentCtx, err := middleware.BeforeAgent(
		runCtx,
		&adk.ChatModelAgentContext[*schema.Message]{},
	)
	require.NoError(t, err)
	_, err = agentCtx.Tools[0].(tool.InvokableTool).InvokableRun(
		runCtx,
		`{"subagent_type":"worker","prompt":"work","description":"stream","run_in_background":true}`,
	)
	require.NoError(t, err)

	for _, wantContent := range []string{"buffered", "hello world"} {
		select {
		case observation := <-formatted:
			require.True(t, observation.ok)
			require.Equal(t, "task_1", observation.execution.TaskID)
			require.Equal(t, task.OwnerManager, observation.execution.Owner)
			require.Equal(t, int64(1), observation.execution.Attempt)
			require.Equal(t, "worker", observation.agentName)
			require.Equal(t, wantContent, observation.content)
		case <-time.After(time.Second):
			t.Fatal("agent event was not formatted")
		}
	}
	require.Eventually(t, func() bool {
		snapshot, getErr := manager.Get(context.Background(), "task_1")
		return getErr == nil && snapshot.Status == background.StatusCompleted
	}, time.Second, 10*time.Millisecond)
	events, err := manager.ListTaskEvents(
		context.Background(),
		&background.ListTaskEventsRequest{TaskID: "task_1"},
	)
	require.NoError(t, err)
	require.Len(t, events.Parts, 2)
	require.NotEmpty(t, events.Parts[0].EventID)
	require.NotEmpty(t, events.Parts[1].EventID)
	require.Equal(t, "worker: buffered\n", string(events.Parts[0].Data))
	require.Equal(t, "worker: hello world\n", string(events.Parts[1].Data))
}
