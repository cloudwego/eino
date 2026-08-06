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
	"github.com/cloudwego/eino/adk/backgroundtask"
	"github.com/cloudwego/eino/adk/internal/agenttool"
	adksession "github.com/cloudwego/eino/adk/session"
	"github.com/cloudwego/eino/schema"
)

type typedInputCaptureAgent struct {
	name   string
	inputs []*adk.AgentInput
}

func (a *typedInputCaptureAgent) Name(context.Context) string        { return a.name }
func (a *typedInputCaptureAgent) Description(context.Context) string { return "capture typed input" }
func (a *typedInputCaptureAgent) Run(
	_ context.Context,
	input *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.inputs = append(a.inputs, input)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("done", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}
func (a *typedInputCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	return a.Run(ctx, &adk.AgentInput{}, options...)
}

type typedInterruptAgent struct {
	name        string
	runInputs   []*adk.AgentInput
	resumeCalls int
}

func (a *typedInterruptAgent) Name(context.Context) string        { return a.name }
func (a *typedInterruptAgent) Description(context.Context) string { return "interrupt typed input" }
func (a *typedInterruptAgent) Run(
	ctx context.Context,
	input *adk.AgentInput,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.runInputs = append(a.runInputs, input)
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.Interrupt(ctx, "approve"))
	generator.Close()
	return iter
}
func (a *typedInterruptAgent) Resume(
	_ context.Context,
	_ *adk.ResumeInfo,
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.AgentEvent] {
	a.resumeCalls++
	iter, generator := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	generator.Send(adk.EventFromMessage(
		schema.AssistantMessage("resumed", nil), nil, schema.Assistant, a.name,
	))
	generator.Close()
	return iter
}

type agenticInputCaptureAgent struct {
	name  string
	input *adk.TypedAgentInput[*schema.AgenticMessage]
}

func (a *agenticInputCaptureAgent) Name(context.Context) string { return a.name }
func (a *agenticInputCaptureAgent) Description(context.Context) string {
	return "capture agentic input"
}
func (a *agenticInputCaptureAgent) Run(
	_ context.Context,
	input *adk.TypedAgentInput[*schema.AgenticMessage],
	_ ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	a.input = input
	iter, generator := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[*schema.AgenticMessage]]()
	generator.Send(adk.EventFromAgenticMessage(
		&schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: "done"}),
			},
		},
		nil,
		schema.AgenticRoleTypeAssistant,
	))
	generator.Close()
	return iter
}
func (a *agenticInputCaptureAgent) Resume(
	ctx context.Context,
	_ *adk.ResumeInfo,
	options ...adk.AgentRunOption,
) *adk.AsyncIterator[*adk.TypedAgentEvent[*schema.AgenticMessage]] {
	return a.Run(ctx, &adk.TypedAgentInput[*schema.AgenticMessage]{}, options...)
}

func stringPointer(value string) *string {
	return &value
}

func TestSubmitPersistsDeepCopiedMultimodalInput(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &typedInputCaptureAgent{name: "worker"}
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())

	input := &adk.AgentInput{
		EnableStreaming: true,
		Messages: []*schema.Message{{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{Type: schema.ChatMessagePartTypeText, Text: "describe these inputs"},
				{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
					MessagePartCommon: schema.MessagePartCommon{URL: stringPointer("https://example.com/image.png")},
				}},
				{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageInputAudio{
					MessagePartCommon: schema.MessagePartCommon{Base64Data: stringPointer("audio-data"), MIMEType: "audio/wav"},
				}},
				{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageInputVideo{
					MessagePartCommon: schema.MessagePartCommon{URL: stringPointer("https://example.com/video.mp4")},
				}},
				{Type: schema.ChatMessagePartTypeFileURL, File: &schema.MessageInputFile{
					MessagePartCommon: schema.MessagePartCommon{Base64Data: stringPointer("file-data"), MIMEType: "application/pdf"},
					Name:              "document.pdf",
				}},
			},
		}},
	}
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker",
		Input:        input,
		Description:  "multimodal work",
		SessionID:    "parent",
	})
	require.NoError(t, err)

	input.EnableStreaming = false
	input.Messages[0].UserInputMultiContent[0].Text = "mutated"
	*input.Messages[0].UserInputMultiContent[1].Image.URL = "https://example.com/mutated.png"

	payload, err := decodePayload(task.Spec)
	require.NoError(t, err)
	require.Equal(t, payloadVersion, payload.Version)
	persisted, err := decodeTypedInput[*schema.Message](payload.Input)
	require.NoError(t, err)
	require.True(t, persisted.EnableStreaming)
	require.Equal(t, "describe these inputs", persisted.Messages[0].UserInputMultiContent[0].Text)
	require.Equal(t, "https://example.com/image.png", *persisted.Messages[0].UserInputMultiContent[1].Image.URL)
	require.Equal(t, "audio-data", *persisted.Messages[0].UserInputMultiContent[2].Audio.Base64Data)
	require.Equal(t, "https://example.com/video.mp4", *persisted.Messages[0].UserInputMultiContent[3].Video.URL)
	require.Equal(t, "document.pdf", persisted.Messages[0].UserInputMultiContent[4].File.Name)

	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.Len(t, agent.inputs, 1)
	require.True(t, agent.inputs[0].EnableStreaming)
	require.Equal(
		t,
		persisted.Messages[0].UserInputMultiContent,
		agent.inputs[0].Messages[0].UserInputMultiContent,
	)
	require.Nil(t, input.Messages[0].Extra)
}

func TestSubmitSupportsAgenticInput(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.AgenticMessage](nil)
	executor, err := NewExecutor(&ExecutorConfig[*schema.AgenticMessage]{
		SessionStore: store, CheckPointStore: store,
	})
	require.NoError(t, err)
	agent := &agenticInputCaptureAgent{name: "worker"}
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.AgenticMessage]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())

	input := &adk.TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{{
			Role: schema.AgenticRoleTypeUser,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.UserInputText{Text: "inspect"}),
				schema.NewContentBlock(&schema.UserInputImage{URL: "https://example.com/image.png"}),
				schema.NewContentBlock(&schema.UserInputAudio{Base64Data: "audio"}),
				schema.NewContentBlock(&schema.UserInputVideo{URL: "https://example.com/video.mp4"}),
				schema.NewContentBlock(&schema.UserInputFile{Name: "document.pdf", Base64Data: "file"}),
			},
		}},
	}
	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.AgenticMessage]{
		SubAgentName: "worker", Input: input, SessionID: "parent",
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.NotNil(t, agent.input)
	require.Equal(t, input.Messages[0].ContentBlocks, agent.input.Messages[0].ContentBlocks)
	require.Nil(t, input.Messages[0].Extra)
}

func TestAttack_TypedInputRecoveryDoesNotReplayInitialInput(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &typedInterruptAgent{name: "worker"}
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())

	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("initial input")},
		},
		SessionID: "parent",
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	waiting, err := manager.Get(context.Background(), task.Spec.ID)
	require.NoError(t, err)
	require.Equal(t, backgroundtask.StatusWaitingInput, waiting.Status)
	require.Len(t, agent.runInputs, 1)
	require.Equal(t, "initial input", agent.runInputs[0].Messages[0].Content)

	_, err = manager.Resume(context.Background(), &backgroundtask.ResumeRequest{
		TaskID: task.Spec.ID, ExpectedVersion: waiting.Version,
	})
	require.NoError(t, err)
	require.NoError(t, manager.Execute(context.Background(), task.Spec.ID))
	require.Len(t, agent.runInputs, 1)
	require.Equal(t, 1, agent.resumeCalls)
	t.Log("checkpoint recovery called Resume without a second Run")
}

func TestSubmitValidatesInputBeforePersistence(t *testing.T) {
	_, err := Submit[*schema.Message](context.Background(), nil, nil)
	require.EqualError(
		t,
		err,
		"backgroundtask/subagent: manager, parent session, and subagent name are required",
	)
	_, err = decodeTypedInput[*schema.Message](nil)
	require.EqualError(t, err, "backgroundtask/subagent: typed input is required")
	_, err = decodeTypedInput[*schema.Message](&serializedTypedInput{
		Messages: json.RawMessage(`{`),
	})
	require.ErrorContains(t, err, "deserialize typed input")
	emptyMessages, err := (&schema.HumanReadableSerializer{}).Marshal(
		[]*schema.Message{},
	)
	require.NoError(t, err)
	_, err = decodeTypedInput[*schema.Message](&serializedTypedInput{
		Messages: emptyMessages,
	})
	require.ErrorContains(t, err, "messages are required")

	store := adksession.NewInMemoryStore[*schema.Message](nil)
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())

	for _, testCase := range []struct {
		name  string
		input *adk.AgentInput
		err   string
	}{
		{name: "nil input", err: "messages are required"},
		{name: "empty messages", input: &adk.AgentInput{}, err: "messages are required"},
		{name: "nil message", input: &adk.AgentInput{Messages: []*schema.Message{nil}}, err: "nil message"},
		{
			name: "unserializable extra",
			input: &adk.AgentInput{Messages: []*schema.Message{{
				Role: schema.User, Content: "input", Extra: map[string]any{"channel": make(chan int)},
			}}},
			err: "serialize typed input",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
				TaskID: "must-not-persist", SubAgentName: "worker",
				Input: testCase.input, SessionID: "parent",
			})
			require.ErrorContains(t, err, testCase.err)
			_, getErr := manager.Get(context.Background(), "must-not-persist")
			require.ErrorIs(t, getErr, backgroundtask.ErrNotFound)
		})
	}
}

func TestAttack_TypedPayloadRejectsMismatchedExecutorMessageType(t *testing.T) {
	input, err := encodeTypedInput(&adk.TypedAgentInput[*schema.AgenticMessage]{
		Messages: []*schema.AgenticMessage{schema.UserAgenticMessage("input")},
	})
	require.NoError(t, err)
	payload, err := json.Marshal(taskPayload{
		Version: payloadVersion, SubAgentName: "worker", Input: input,
		ChildSessionID: defaultChildSessionID("parent", "worker", "task"),
	})
	require.NoError(t, err)

	executor := newTestExecutor(t, nil)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: &resumableTestAgent{name: "worker"},
	}))
	err = executor.ValidateSpec(backgroundtask.Spec{
		ID: "task", ExecutorKey: ExecutorKey, Kind: "subagent",
		Payload: payload, SessionID: "parent",
	})
	require.ErrorContains(t, err, "message type does not match executor")
	t.Log("executor rejected an AgenticMessage payload registered as Message")
}

func TestAttack_TypedInputRoundTripPreservesNestedExtraAndLargeInteger(t *testing.T) {
	const ticket = int64(9007199254740993)
	nested := map[string]any{"value": "original"}
	input := &adk.AgentInput{Messages: []*schema.Message{{
		Role:    schema.User,
		Content: "input",
		Extra: map[string]any{
			"ticket": ticket,
			"nested": nested,
		},
	}}}

	encoded, err := encodeTypedInput(input)
	require.NoError(t, err)
	nested["value"] = "mutated"
	input.Messages[0].Extra["ticket"] = int64(1)

	decoded, err := decodeTypedInput[*schema.Message](encoded)
	require.NoError(t, err)
	require.Equal(t, ticket, decoded.Messages[0].Extra["ticket"])
	decodedNested, ok := decoded.Messages[0].Extra["nested"].(map[string]any)
	require.True(t, ok)
	require.Equal(
		t,
		"original",
		decodedNested["value"],
	)
	t.Log("serializer preserved concrete integer type and isolated nested aliases")
}

func TestAttack_ForegroundStreamingOverridesPersistedInputMode(t *testing.T) {
	store := adksession.NewInMemoryStore[*schema.Message](nil)
	agent := &typedInputCaptureAgent{name: "worker"}
	executor := newTestExecutor(t, store)
	require.NoError(t, executor.Register("worker", &AgentRegistration[*schema.Message]{
		Agent: agent,
	}))
	executors := backgroundtask.NewExecutorRegistry()
	require.NoError(t, executors.Register(executor))
	manager := mustNewBackgroundManager(
		t,
		context.Background(),
		&backgroundtask.Config{Executors: executors},
	)
	defer manager.Close(context.Background())

	task, err := Submit(context.Background(), manager, &SubmitRequest[*schema.Message]{
		SubAgentName: "worker",
		Input: &adk.AgentInput{
			Messages: []*schema.Message{schema.UserMessage("input")},
		},
		SessionID: "parent",
	})
	require.NoError(t, err)
	runCtx, detach := agenttool.WithForegroundExecution[*adk.AgentEvent](
		context.Background(),
		nil,
		true,
	)
	defer detach()
	require.NoError(t, manager.Execute(runCtx, task.Spec.ID))
	require.Len(t, agent.inputs, 1)
	require.True(t, agent.inputs[0].EnableStreaming)
	t.Log("foreground projection enabled streaming without mutating persisted input")
}
