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

package adk

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func isValidUUID(s string) bool {
	// UUID v4 format: xxxxxxxx-xxxx-xxxx-xxxx-xxxxxxxxxxxx (8-4-4-4-12 = 36 chars)
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// collectEvents drains all events from the iterator (non-streaming).
func collectEvents(t *testing.T, iter *AsyncIterator[*AgentEvent]) []*AgentEvent {
	t.Helper()
	var events []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		events = append(events, event)
	}
	return events
}

// Scenario 1: AgentEvent messages have IDs (Generate mode)
func TestMessageID_EventHasID_Generate(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("hello world", nil), nil).
		Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hi")},
	})

	events := collectEvents(t, iter)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Err)
	require.NotNil(t, events[0].Output.MessageOutput)

	msg := events[0].Output.MessageOutput.Message
	require.NotNil(t, msg)
	msgID := GetMessageID(msg)
	assert.NotEmpty(t, msgID, "event message should have an ID")
	assert.True(t, isValidUUID(msgID), "message ID should be a valid UUID, got: %s", msgID)
}

// Scenario 2: Event and state messages share the same ID
func TestMessageID_EventAndStateShareSameID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("response", nil), nil).
		Times(1)

	var stateMessagesAfterModel []*schema.Message

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
		Middlewares: []AgentMiddleware{
			{
				AfterChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
					// Capture state messages after model call (including the model output)
					stateMessagesAfterModel = make([]*schema.Message, len(state.Messages))
					copy(stateMessagesAfterModel, state.Messages)
					return nil
				},
			},
		},
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hi")},
	})

	events := collectEvents(t, iter)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Err)

	eventMsg := events[0].Output.MessageOutput.Message
	eventMsgID := GetMessageID(eventMsg)
	assert.NotEmpty(t, eventMsgID)

	// The last message in state should be the model output with the same ID
	require.NotEmpty(t, stateMessagesAfterModel)
	lastStateMsg := stateMessagesAfterModel[len(stateMessagesAfterModel)-1]
	stateMsgID := GetMessageID(lastStateMsg)

	assert.Equal(t, eventMsgID, stateMsgID,
		"event msg ID (%s) and state msg ID (%s) must match", eventMsgID, stateMsgID)
}

// Scenario 3: Stream — first chunk carries ID, concatenated message has correct ID
func TestMessageID_Stream_FirstChunkOnly(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.StreamReaderFromArray([]*schema.Message{
			schema.AssistantMessage("chunk1", nil),
			schema.AssistantMessage("chunk2", nil),
			schema.AssistantMessage("chunk3", nil),
		}), nil).
		Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages:        []Message{schema.UserMessage("hi")},
		EnableStreaming: true,
	})

	event, ok := iter.Next()
	require.True(t, ok)
	require.Nil(t, event.Err)
	require.NotNil(t, event.Output.MessageOutput)
	require.True(t, event.Output.MessageOutput.IsStreaming)

	stream := event.Output.MessageOutput.MessageStream
	require.NotNil(t, stream)

	var chunks []*schema.Message
	for {
		msg, err := stream.Recv()
		if err != nil {
			break
		}
		chunks = append(chunks, msg)
	}
	require.GreaterOrEqual(t, len(chunks), 1)

	// First chunk should have the ID
	firstChunkID := GetMessageID(chunks[0])
	assert.NotEmpty(t, firstChunkID, "first chunk should carry the message ID")
	assert.True(t, isValidUUID(firstChunkID))

	// Subsequent chunks should NOT have the ID in Extra (first-chunk-only injection)
	for i := 1; i < len(chunks); i++ {
		chunkID := GetMessageID(chunks[i])
		assert.Empty(t, chunkID, "chunk %d should not have message ID (first-chunk-only)", i)
	}

	// No more events
	_, ok = iter.Next()
	assert.False(t, ok)
}

// Scenario 4: Tool messages have IDs
func TestMessageID_ToolMessagesHaveID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	fakeTool := &fakeToolForTest{tarCount: 1}
	info, err := fakeTool.Info(ctx)
	require.NoError(t, err)

	generateCount := 0
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			generateCount++
			if generateCount == 1 {
				return schema.AssistantMessage("calling tool",
					[]schema.ToolCall{{
						ID: "tc-1",
						Function: schema.FunctionCall{
							Name:      info.Name,
							Arguments: `{"name": "tester"}`,
						},
					}}), nil
			}
			return schema.AssistantMessage("done", nil), nil
		}).AnyTimes()
	cm.EXPECT().WithTools(gomock.Any()).Return(cm, nil).AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
		ToolsConfig: ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: []tool.BaseTool{fakeTool},
			},
		},
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("use tool")},
	})

	events := collectEvents(t, iter)
	// Expect 3 events: model(tool_call) + tool(result) + model(final)
	require.Len(t, events, 3)

	// Tool event (index 1)
	toolEvent := events[1]
	require.Nil(t, toolEvent.Err)
	require.NotNil(t, toolEvent.Output.MessageOutput)
	assert.Equal(t, schema.Tool, toolEvent.Output.MessageOutput.Role)

	toolMsg := toolEvent.Output.MessageOutput.Message
	require.NotNil(t, toolMsg)
	toolMsgID := GetMessageID(toolMsg)
	assert.NotEmpty(t, toolMsgID, "tool message should have an ID")
	assert.True(t, isValidUUID(toolMsgID))

	// All events should have IDs
	for i, ev := range events {
		require.Nil(t, ev.Err)
		require.NotNil(t, ev.Output.MessageOutput)
		msg := ev.Output.MessageOutput.Message
		require.NotNil(t, msg)
		assert.NotEmpty(t, GetMessageID(msg), "event[%d] should have a message ID", i)
	}
}

// Scenario 5: Retry — only surviving result has an ID
func TestMessageID_Retry_OnlySurvivorHasID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	retryErr := errors.New("retryable error")

	var callCount int32
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			count := atomic.AddInt32(&callCount, 1)
			if count < 3 {
				return nil, retryErr
			}
			return schema.AssistantMessage("Success after retry", nil), nil
		}).Times(3)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
		ModelRetryConfig: &ModelRetryConfig{
			MaxRetries:  3,
			IsRetryAble: func(ctx context.Context, err error) bool { return errors.Is(err, retryErr) },
		},
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hello")},
	})

	events := collectEvents(t, iter)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Err)

	msg := events[0].Output.MessageOutput.Message
	msgID := GetMessageID(msg)
	assert.NotEmpty(t, msgID, "surviving message should have an ID")
	assert.True(t, isValidUUID(msgID))
	assert.Equal(t, int32(3), atomic.LoadInt32(&callCount))
}

// Scenario 6: WrapModel handler sees model output with ID
func TestMessageID_WrapModelSeesID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("model output", nil), nil).
		Times(1)

	var capturedMsgID string

	handler := &wrapModelIDCheckHandler{
		BaseChatModelAgentMiddleware: &BaseChatModelAgentMiddleware{},
		onGenerate: func(result *schema.Message) {
			capturedMsgID = GetMessageID(result)
		},
	}

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
		Handlers:    []ChatModelAgentMiddleware{handler},
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hi")},
	})

	events := collectEvents(t, iter)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Err)

	assert.NotEmpty(t, capturedMsgID, "WrapModel handler should see message ID on model output")
	assert.True(t, isValidUUID(capturedMsgID))

	// The event should carry the same ID
	eventMsgID := GetMessageID(events[0].Output.MessageOutput.Message)
	assert.Equal(t, capturedMsgID, eventMsgID,
		"WrapModel-captured ID (%s) should match event ID (%s)", capturedMsgID, eventMsgID)
}

// wrapModelIDCheckHandler wraps the model to inspect the output for message ID.
type wrapModelIDCheckHandler struct {
	*BaseChatModelAgentMiddleware
	onGenerate func(result *schema.Message)
}

func (h *wrapModelIDCheckHandler) WrapModel(_ context.Context, m model.BaseChatModel, _ *ModelContext) (model.BaseChatModel, error) {
	return &idCheckModelWrapper{inner: m, onGenerate: h.onGenerate}, nil
}

type idCheckModelWrapper struct {
	inner      model.BaseChatModel
	onGenerate func(result *schema.Message)
}

func (w *idCheckModelWrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	result, err := w.inner.Generate(ctx, input, opts...)
	if err == nil && w.onGenerate != nil {
		w.onGenerate(result)
	}
	return result, err
}

func (w *idCheckModelWrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return w.inner.Stream(ctx, input, opts...)
}

// Scenario 7: User input messages do NOT get automatic IDs (they are external, not framework-created).
// Only framework-created messages (model output, tool results, TypedSendEvent) get auto-assigned IDs.
func TestMessageID_UserInputNoAutoID(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)

	var stateMessagesBeforeModel []*schema.Message
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
			// Capture input messages
			stateMessagesBeforeModel = make([]*schema.Message, len(input))
			copy(stateMessagesBeforeModel, input)
			return schema.AssistantMessage("response", nil), nil
		}).Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hello")},
	})

	events := collectEvents(t, iter)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Err)

	// User input messages should NOT have auto-assigned IDs.
	// Framework only assigns IDs to messages it creates (model output, tool results, SendEvent).
	require.NotEmpty(t, stateMessagesBeforeModel)

	for i, msg := range stateMessagesBeforeModel {
		msgID := GetMessageID(msg)
		assert.Empty(t, msgID, "input message[%d] (role=%s) should NOT have auto-assigned ID", i, msg.Role)
	}
}

// Scenario 8: Middleware SendEvent auto-assigns ID, pointer identity ensures state consistency
func TestMessageID_SendEvent_PointerIdentity(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("model response", nil), nil).
		Times(1)

	// Track the message pointer that the middleware creates and writes to both state and event
	var middlewareMsg *schema.Message
	var stateMsgIDAfterSendEvent string

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestMsgID",
		Instruction: "test",
		Model:       cm,
		Middlewares: []AgentMiddleware{
			{
				AfterChatModel: func(ctx context.Context, state *ChatModelAgentState) error {
					// Middleware creates a new message and writes the SAME pointer to both state and event
					middlewareMsg = schema.AssistantMessage("middleware injected", nil)

					// Write to state
					state.Messages = append(state.Messages, middlewareMsg)

					// Send as event — TypedSendEvent should auto-assign ID
					event := EventFromMessage(middlewareMsg, nil, schema.Assistant, "")
					err := SendEvent(ctx, event)
					if err != nil {
						return err
					}

					// After SendEvent, check if the state copy (same pointer) also has the ID
					stateMsgIDAfterSendEvent = internal.GetMessageID(middlewareMsg.Extra)

					return nil
				},
			},
		},
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hi")},
	})

	var allEvents []*AgentEvent
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		allEvents = append(allEvents, event)
	}

	// We expect at least 2 events: model response + middleware injected message
	require.GreaterOrEqual(t, len(allEvents), 2)

	// The middleware message pointer should have an ID (assigned by SendEvent)
	require.NotNil(t, middlewareMsg)
	middlewareMsgID := GetMessageID(middlewareMsg)
	assert.NotEmpty(t, middlewareMsgID, "SendEvent should have auto-assigned an ID to the middleware message")
	assert.True(t, isValidUUID(middlewareMsgID))

	// The ID captured right after SendEvent (via pointer identity) should be the same
	assert.Equal(t, middlewareMsgID, stateMsgIDAfterSendEvent,
		"pointer identity: ID read from state pointer (%s) should match message ID (%s)",
		stateMsgIDAfterSendEvent, middlewareMsgID)

	// Find the middleware event in the collected events
	var middlewareEventMsgID string
	for _, ev := range allEvents {
		if ev.Err != nil || ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg := ev.Output.MessageOutput.Message
		if msg != nil && msg.Content == "middleware injected" {
			middlewareEventMsgID = GetMessageID(msg)
			break
		}
	}
	assert.Equal(t, middlewareMsgID, middlewareEventMsgID,
		"event message ID (%s) should match the middleware message ID (%s)",
		middlewareEventMsgID, middlewareMsgID)
}

func TestAttack_ConcatCorruptsIDIfMultipleChunksCarryIt(t *testing.T) {
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "chunk1", Extra: map[string]any{internal.EinoMsgIDKey: id}},
		{Role: schema.Assistant, Content: "chunk2", Extra: map[string]any{internal.EinoMsgIDKey: id}},
		{Role: schema.Assistant, Content: "chunk3", Extra: map[string]any{internal.EinoMsgIDKey: id}},
	}
	concatenated, err := schema.ConcatMessages(msgs)
	require.NoError(t, err)

	resultID := internal.GetMessageID(concatenated.Extra)
	// ConcatMessages string-concatenates duplicate Extra keys, corrupting the ID
	assert.NotEqual(t, id, resultID, "ConcatMessages should corrupt the ID when multiple chunks carry it")
	assert.NotEqual(t, 36, len(resultID), "corrupted ID should not be 36 chars")
	assert.Equal(t, "chunk1chunk2chunk3", concatenated.Content)
}

func TestAttack_ConcatPreservesIDIfOnlyFirstChunkHasIt(t *testing.T) {
	id := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	msgs := []*schema.Message{
		{Role: schema.Assistant, Content: "chunk1", Extra: map[string]any{internal.EinoMsgIDKey: id}},
		{Role: schema.Assistant, Content: "chunk2"},
		{Role: schema.Assistant, Content: "chunk3"},
	}
	concatenated, err := schema.ConcatMessages(msgs)
	require.NoError(t, err)

	resultID := internal.GetMessageID(concatenated.Extra)
	assert.Equal(t, id, resultID, "ID should be preserved when only first chunk carries it")
	assert.Equal(t, "chunk1chunk2chunk3", concatenated.Content)
}

func TestAttack_EnsureMessageIDIdempotentUnderRapidCalls(t *testing.T) {
	msg := schema.AssistantMessage("test", nil)
	EnsureMessageID(msg)
	firstID := GetMessageID(msg)
	require.NotEmpty(t, firstID)

	for i := 0; i < 100; i++ {
		EnsureMessageID(msg)
		assert.Equal(t, firstID, GetMessageID(msg), "iteration %d: ID should not change after first assignment", i)
	}
}

func TestAttack_NilMessagePanics(t *testing.T) {
	assert.Panics(t, func() {
		GetMessageID(nil)
	}, "GetMessageID on nil *Message should panic (nil pointer dereference on .Extra)")
}

func TestAttack_StreamIDUniquePerCall(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Stream(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(ctx context.Context, msgs []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
			return schema.StreamReaderFromArray([]*schema.Message{
				schema.AssistantMessage("a", nil),
				schema.AssistantMessage("b", nil),
			}), nil
		}).
		Times(2)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAttack",
		Instruction: "test",
		Model:       cm,
	})
	require.NoError(t, err)

	getStreamID := func() string {
		iter := agent.Run(ctx, &AgentInput{
			Messages:        []Message{schema.UserMessage("hi")},
			EnableStreaming: true,
		})
		event, ok := iter.Next()
		require.True(t, ok)
		require.Nil(t, event.Err)
		require.NotNil(t, event.Output.MessageOutput)
		require.True(t, event.Output.MessageOutput.IsStreaming)
		stream := event.Output.MessageOutput.MessageStream
		require.NotNil(t, stream)

		msg, err := stream.Recv()
		require.NoError(t, err)
		id := GetMessageID(msg)
		// Drain remaining
		for {
			_, err := stream.Recv()
			if err != nil {
				break
			}
		}
		// Drain iter
		for {
			_, ok := iter.Next()
			if !ok {
				break
			}
		}
		return id
	}

	id1 := getStreamID()
	id2 := getStreamID()
	assert.NotEmpty(t, id1)
	assert.NotEmpty(t, id2)
	assert.NotEqual(t, id1, id2, "each stream call should get a unique message ID")
}

func TestAttack_WrongExtraTypeDoesNotPanic(t *testing.T) {
	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "test",
		Extra:   map[string]any{internal.EinoMsgIDKey: 42},
	}
	assert.NotPanics(t, func() {
		id := GetMessageID(msg)
		assert.Empty(t, id, "wrong type should return empty, not panic")
	})

	// EnsureMessageID should overwrite the wrong type
	EnsureMessageID(msg)
	id := GetMessageID(msg)
	assert.NotEmpty(t, id)
	assert.True(t, isValidUUID(id))
}

func TestAttack_ConcurrentGenerate_NoSharedExtraMutation(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Shared singleton message - same pointer returned every time
	sharedMsg := schema.AssistantMessage("shared response", nil)

	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(sharedMsg, nil).
		AnyTimes()

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAttack",
		Instruction: "test",
		Model:       cm,
	})
	require.NoError(t, err)

	const N = 10
	ids := make([]string, N)
	var wg sync.WaitGroup
	wg.Add(N)
	for i := 0; i < N; i++ {
		go func(idx int) {
			defer wg.Done()
			iter := agent.Run(ctx, &AgentInput{
				Messages: []Message{schema.UserMessage("hi")},
			})
			events := collectEvents(t, iter)
			require.Len(t, events, 1)
			require.Nil(t, events[0].Err)
			msg := events[0].Output.MessageOutput.Message
			require.NotNil(t, msg)
			ids[idx] = GetMessageID(msg)
		}(i)
	}
	wg.Wait()

	// All IDs should be unique and valid
	seen := make(map[string]bool)
	for i, id := range ids {
		assert.NotEmpty(t, id, "goroutine %d should have an ID", i)
		assert.True(t, isValidUUID(id), "goroutine %d ID should be valid UUID: %s", i, id)
		assert.False(t, seen[id], "goroutine %d has duplicate ID: %s", i, id)
		seen[id] = true
	}

	// The original shared message should NOT have been mutated (or if it was, it should still be valid)
	// The important thing is no panic and unique IDs
}

func TestAttack_GenerateCopyDoesNotAffectOriginal(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	originalMsg := schema.AssistantMessage("original", nil)
	cm := mockModel.NewMockToolCallingChatModel(ctrl)
	cm.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(originalMsg, nil).
		Times(1)

	agent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "TestAttack",
		Instruction: "test",
		Model:       cm,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &AgentInput{
		Messages: []Message{schema.UserMessage("hi")},
	})

	events := collectEvents(t, iter)
	require.Len(t, events, 1)
	require.Nil(t, events[0].Err)

	eventMsg := events[0].Output.MessageOutput.Message
	eventMsgID := GetMessageID(eventMsg)
	assert.NotEmpty(t, eventMsgID)

	// The ORIGINAL message returned by the model should NOT have an ID
	// because wrapGenerateEndpoint copies before mutating
	originalID := GetMessageID(originalMsg)
	assert.Empty(t, originalID, "original model output should NOT be mutated by ID assignment")
}
