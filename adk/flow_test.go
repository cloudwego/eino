/*
 * Copyright 2025 CloudWeGo Authors
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
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"

	"github.com/cloudwego/eino/callbacks"
	mockModel "github.com/cloudwego/eino/internal/mock/components/model"
	"github.com/cloudwego/eino/schema"
)

func strPtr(s string) *string { return &s }

func TestRewriteMessage(t *testing.T) {
	imageCommon := schema.MessagePartCommon{URL: strPtr("http://img.example.com")}
	audioCommon := schema.MessagePartCommon{URL: strPtr("http://audio.example.com")}
	videoCommon := schema.MessagePartCommon{URL: strPtr("http://video.example.com")}

	msg := &schema.Message{
		Role:    schema.Assistant,
		Content: "hello",
		MultiContent: []schema.ChatMessagePart{
			{Type: schema.ChatMessagePartTypeText, Text: "legacy"},
		},
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "pre-existing"},
		},
		AssistantGenMultiContent: []schema.MessageOutputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "gen-text", Extra: map[string]any{"k": "v"}},
			{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageOutputImage{MessagePartCommon: imageCommon}},
			{Type: schema.ChatMessagePartTypeAudioURL, Audio: &schema.MessageOutputAudio{MessagePartCommon: audioCommon}},
			{Type: schema.ChatMessagePartTypeVideoURL, Video: &schema.MessageOutputVideo{MessagePartCommon: videoCommon}},
			{Type: schema.ChatMessagePartTypeReasoning, Reasoning: &schema.MessageOutputReasoning{Text: "secret thoughts"}},
		},
	}

	rewritten := rewriteMessage(msg, "OtherAgent")

	assert.Equal(t, schema.User, rewritten.Role)

	// MultiContent: copied, not shared
	assert.Equal(t, msg.MultiContent, rewritten.MultiContent)
	rewritten.MultiContent[0].Text = "mutated"
	assert.Equal(t, "legacy", msg.MultiContent[0].Text)

	// UserInputMultiContent: pre-existing entry copied, AssistantGenMultiContent appended (reasoning dropped)
	assert.Len(t, rewritten.UserInputMultiContent, 5) // 1 pre-existing + 4 converted (text/image/audio/video)

	// pre-existing entry is not shared
	rewritten.UserInputMultiContent[0].Text = "mutated"
	assert.Equal(t, "pre-existing", msg.UserInputMultiContent[0].Text)

	// text conversion
	assert.Equal(t, schema.ChatMessagePartTypeText, rewritten.UserInputMultiContent[1].Type)
	assert.Equal(t, "gen-text", rewritten.UserInputMultiContent[1].Text)
	assert.Equal(t, map[string]any{"k": "v"}, rewritten.UserInputMultiContent[1].Extra)

	// image conversion
	assert.Equal(t, schema.ChatMessagePartTypeImageURL, rewritten.UserInputMultiContent[2].Type)
	assert.Equal(t, imageCommon, rewritten.UserInputMultiContent[2].Image.MessagePartCommon)

	// audio conversion
	assert.Equal(t, schema.ChatMessagePartTypeAudioURL, rewritten.UserInputMultiContent[3].Type)
	assert.Equal(t, audioCommon, rewritten.UserInputMultiContent[3].Audio.MessagePartCommon)

	// video conversion
	assert.Equal(t, schema.ChatMessagePartTypeVideoURL, rewritten.UserInputMultiContent[4].Type)
	assert.Equal(t, videoCommon, rewritten.UserInputMultiContent[4].Video.MessagePartCommon)

	// reasoning is dropped; AssistantGenMultiContent is not set on rewritten message
	assert.Empty(t, rewritten.AssistantGenMultiContent)
}

// TestTransferToAgent tests the TransferToAgent functionality
func TestTransferToAgent(t *testing.T) {
	ctx := context.Background()

	// Create a mock controller
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// Create mock models for parent and child agents
	parentModel := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel := mockModel.NewMockToolCallingChatModel(ctrl)

	// Set up expectations for the parent model
	// First call: parent model generates a message with TransferToAgent tool call
	parentModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("I'll transfer this to the child agent",
			[]schema.ToolCall{
				{
					ID: "tool-call-1",
					Function: schema.FunctionCall{
						Name:      TransferToAgentToolName,
						Arguments: `{"agent_name": "ChildAgent"}`,
					},
				},
			}), nil).
		Times(1)

	// Set up expectations for the child model
	// Second call: child model generates a response
	childModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent", nil), nil).
		Times(1)

	// Both models should implement WithTools
	parentModel.EXPECT().WithTools(gomock.Any()).Return(parentModel, nil).AnyTimes()
	childModel.EXPECT().WithTools(gomock.Any()).Return(childModel, nil).AnyTimes()

	// Create parent agent
	parentAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ParentAgent",
		Description: "Parent agent that will transfer to child",
		Instruction: "You are a parent agent.",
		Model:       parentModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, parentAgent)

	// Create child agent
	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent",
		Description: "Child agent that handles specific tasks",
		Instruction: "You are a child agent.",
		Model:       childModel,
	})
	assert.NoError(t, err)
	assert.NotNil(t, childAgent)

	// Set up parent-child relationship
	flowAgent, err := SetSubAgents(ctx, parentAgent, []Agent{childAgent})
	assert.NoError(t, err)
	assert.NotNil(t, flowAgent)

	assert.NotNil(t, parentAgent.subAgents)
	assert.NotNil(t, childAgent.parentAgent)

	// Run the parent agent
	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Please transfer this to the child agent"),
		},
	}
	ctx, _ = initRunCtx(ctx, flowAgent.Name(ctx), input)
	iterator := flowAgent.Run(ctx, input)
	assert.NotNil(t, iterator)

	// First event: parent model output with tool call
	event1, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event1)
	assert.Nil(t, event1.Err)
	assert.NotNil(t, event1.Output)
	assert.NotNil(t, event1.Output.MessageOutput)
	assert.Equal(t, schema.Assistant, event1.Output.MessageOutput.Role)

	// Second event: tool output (TransferToAgent)
	event2, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event2)
	assert.Nil(t, event2.Err)
	assert.NotNil(t, event2.Output)
	assert.NotNil(t, event2.Output.MessageOutput)
	assert.Equal(t, schema.Tool, event2.Output.MessageOutput.Role)

	// Verify the action is TransferToAgent
	assert.NotNil(t, event2.Action)
	assert.NotNil(t, event2.Action.TransferToAgent)
	assert.Equal(t, "ChildAgent", event2.Action.TransferToAgent.DestAgentName)

	// Third event: child model output
	event3, ok := iterator.Next()
	assert.True(t, ok)
	assert.NotNil(t, event3)
	assert.Nil(t, event3.Err)
	assert.NotNil(t, event3.Output)
	assert.NotNil(t, event3.Output.MessageOutput)
	assert.Equal(t, schema.Assistant, event3.Output.MessageOutput.Role)

	// Verify the message content from child agent
	msg := event3.Output.MessageOutput.Message
	assert.NotNil(t, msg)
	assert.Equal(t, "Hello from child agent", msg.Content)

	// No more events
	_, ok = iterator.Next()
	assert.False(t, ok)
}

func TestTransferToAgentWithDesignatedCallback(t *testing.T) {
	ctx := context.Background()

	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	parentModel := mockModel.NewMockToolCallingChatModel(ctrl)
	childModel := mockModel.NewMockToolCallingChatModel(ctrl)

	parentModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("I'll transfer this to the child agent",
			[]schema.ToolCall{
				{
					ID: "tool-call-1",
					Function: schema.FunctionCall{
						Name:      TransferToAgentToolName,
						Arguments: `{"agent_name": "ChildAgent"}`,
					},
				},
			}), nil).
		Times(1)

	childModel.EXPECT().Generate(gomock.Any(), gomock.Any(), gomock.Any()).
		Return(schema.AssistantMessage("Hello from child agent", nil), nil).
		Times(1)

	parentModel.EXPECT().WithTools(gomock.Any()).Return(parentModel, nil).AnyTimes()
	childModel.EXPECT().WithTools(gomock.Any()).Return(childModel, nil).AnyTimes()

	parentAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ParentAgent",
		Description: "Parent agent that will transfer to child",
		Instruction: "You are a parent agent.",
		Model:       parentModel,
	})
	assert.NoError(t, err)

	childAgent, err := NewChatModelAgent(ctx, &ChatModelAgentConfig{
		Name:        "ChildAgent",
		Description: "Child agent that handles specific tasks",
		Instruction: "You are a child agent.",
		Model:       childModel,
	})
	assert.NoError(t, err)

	flowAgent, err := SetSubAgents(ctx, parentAgent, []Agent{childAgent})
	assert.NoError(t, err)

	var childCallbackCount int
	var mu sync.Mutex

	handler := callbacks.NewHandlerBuilder().OnStartFn(
		func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
			if info.Component == ComponentOfAgent && info.Name == "ChildAgent" {
				mu.Lock()
				childCallbackCount++
				mu.Unlock()
			}
			return ctx
		}).Build()

	input := &AgentInput{
		Messages: []Message{
			schema.UserMessage("Please transfer this to the child agent"),
		},
	}
	ctx, _ = initRunCtx(ctx, flowAgent.Name(ctx), input)
	iterator := flowAgent.Run(ctx, input, WithCallbacks(handler).DesignateAgent("ChildAgent"))

	for {
		_, ok := iterator.Next()
		if !ok {
			break
		}
	}

	assert.Equal(t, 1, childCallbackCount, "designated callback for ChildAgent should fire exactly once during transfer")
}
