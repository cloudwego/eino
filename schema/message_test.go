/*
 * Copyright 2024 CloudWeGo Authors
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

package schema

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/generic"
)

func TestMessageTemplate(t *testing.T) {
	pyFmtMessage := UserMessage("input: {question}")
	jinja2Message := UserMessage("input: {{question}}")
	goTemplateMessage := UserMessage("input: {{.question}}")
	ctx := context.Background()
	question := "what's the weather today"
	expected := []*Message{UserMessage("input: " + question)}

	ms, err := pyFmtMessage.Format(ctx, map[string]any{"question": question}, FString)
	assert.Nil(t, err)
	assert.True(t, reflect.DeepEqual(expected, ms))
	ms, err = jinja2Message.Format(ctx, map[string]any{"question": question}, Jinja2)
	assert.Nil(t, err)
	assert.True(t, reflect.DeepEqual(expected, ms))
	ms, err = goTemplateMessage.Format(ctx, map[string]any{"question": question}, GoTemplate)
	assert.Nil(t, err)
	assert.True(t, reflect.DeepEqual(expected, ms))

	mp := MessagesPlaceholder("chat_history", false)
	m1 := UserMessage("how are you?")
	m2 := AssistantMessage("I'm good. how about you?", nil)
	ms, err = mp.Format(ctx, map[string]any{"chat_history": []*Message{m1, m2}}, FString)
	assert.Nil(t, err)

	// len(ms) == 2
	assert.Equal(t, 2, len(ms))
	assert.Equal(t, ms[0], m1)
	assert.Equal(t, ms[1], m2)
}

func TestConcatMessage(t *testing.T) {
	t.Run("tool_call_normal_append", func(t *testing.T) {
		expectMsg := &Message{
			Role:    "assistant",
			Content: "",
			ToolCalls: []ToolCall{
				{
					Index: generic.PtrOf(0),
					ID:    "i_am_a_too_call_id",
					Type:  "function",
					Function: FunctionCall{
						Name:      "i_am_a_tool_name",
						Arguments: "{}",
					},
				},
			},
		}
		givenMsgList := []*Message{
			{
				Role:    "",
				Content: "",
				ToolCalls: []ToolCall{
					{
						Index: generic.PtrOf(0),
						ID:    "",
						Type:  "",
						Function: FunctionCall{
							Name: "",
						},
					},
				},
			},
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{
						Index: generic.PtrOf(0),
						ID:    "i_am_a_too_call_id",
						Type:  "function",
						Function: FunctionCall{
							Name: "i_am_a_tool_name",
						},
					},
				},
			},
			{
				Role:    "",
				Content: "",
				ToolCalls: []ToolCall{
					{
						Index: generic.PtrOf(0),
						ID:    "",
						Type:  "",
						Function: FunctionCall{
							Name:      "",
							Arguments: "{}",
						},
					},
				},
			},
		}

		msg, err := ConcatMessages(givenMsgList)
		assert.NoError(t, err)
		assert.EqualValues(t, expectMsg, msg)
	})

	t.Run("exist_nil_message", func(t *testing.T) {
		givenMsgList := []*Message{
			nil,
			{
				Role:    "assistant",
				Content: "",
				ToolCalls: []ToolCall{
					{
						Index: generic.PtrOf(0),
						ID:    "i_am_a_too_call_id",
						Type:  "function",
						Function: FunctionCall{
							Name: "i_am_a_tool_name",
						},
					},
				},
			},
		}

		_, err := ConcatMessages(givenMsgList)
		assert.ErrorContains(t, err, "unexpected nil chunk in message stream")
	})

	t.Run("response_meta", func(t *testing.T) {
		expectedMsg := &Message{
			Role: "assistant",
			ResponseMeta: &ResponseMeta{
				FinishReason: "stop",
				Usage: &TokenUsage{
					CompletionTokens: 15,
					PromptTokens:     30,
					PromptTokenDetails: PromptTokenDetails{
						CachedTokens: 15,
					},
					TotalTokens: 45,
				},
			},
		}

		givenMsgList := []*Message{
			{
				Role: "assistant",
			},
			{
				Role: "assistant",
				ResponseMeta: &ResponseMeta{
					FinishReason: "",
					Usage: &TokenUsage{
						CompletionTokens: 10,
						PromptTokens:     20,
						PromptTokenDetails: PromptTokenDetails{
							CachedTokens: 10,
						},
						TotalTokens: 30,
					},
				},
			},
			{
				Role: "assistant",
				ResponseMeta: &ResponseMeta{
					FinishReason: "stop",
				},
			},
			{
				Role: "assistant",
				ResponseMeta: &ResponseMeta{
					Usage: &TokenUsage{
						CompletionTokens: 15,
						PromptTokens:     30,
						PromptTokenDetails: PromptTokenDetails{
							CachedTokens: 15,
						},
						TotalTokens: 45,
					},
				},
			},
		}

		msg, err := ConcatMessages(givenMsgList)
		assert.NoError(t, err)
		assert.Equal(t, expectedMsg, msg)

		givenMsgList = append(givenMsgList, &Message{
			Role: "assistant",
			ResponseMeta: &ResponseMeta{
				FinishReason: "tool_calls",
			},
		})
		msg, err = ConcatMessages(givenMsgList)
		assert.NoError(t, err)
		expectedMsg.ResponseMeta.FinishReason = "tool_calls"
		assert.Equal(t, expectedMsg, msg)

	})

	t.Run("err: different roles", func(t *testing.T) {
		msgs := []*Message{
			{Role: User},
			{Role: Assistant},
		}

		msg, err := ConcatMessages(msgs)
		if assert.Error(t, err) {
			assert.ErrorContains(t, err, "cannot concat messages with different roles")
			assert.Nil(t, msg)
		}
	})

	t.Run("err: different name", func(t *testing.T) {
		msgs := []*Message{
			{Role: Assistant, Name: "n", Content: "1"},
			{Role: Assistant, Name: "a", Content: "2"},
		}

		msg, err := ConcatMessages(msgs)
		if assert.Error(t, err) {
			assert.ErrorContains(t, err, "cannot concat messages with different names")
			assert.Nil(t, msg)
		}
	})

	t.Run("err: different tool name", func(t *testing.T) {
		msgs := []*Message{
			{
				Role:       "",
				Content:    "",
				ToolCallID: "123",
				ToolCalls: []ToolCall{
					{
						Index: generic.PtrOf(0),
						ID:    "abc",
						Type:  "",
						Function: FunctionCall{
							Name: "",
						},
					},
				},
			},
			{
				Role:       "assistant",
				Content:    "",
				ToolCallID: "321",
				ToolCalls: []ToolCall{
					{
						Index: generic.PtrOf(0),
						ID:    "abc",
						Type:  "function",
						Function: FunctionCall{
							Name: "i_am_a_tool_name",
						},
					},
				},
			},
		}

		msg, err := ConcatMessages(msgs)
		if assert.Error(t, err) {
			assert.ErrorContains(t, err, "cannot concat messages with different toolCallIDs")
			assert.Nil(t, msg)
		}
	})

	t.Run("first response meta usage is nil", func(t *testing.T) {
		exp := &Message{
			Role: "assistant",
			ResponseMeta: &ResponseMeta{
				FinishReason: "stop",
				Usage: &TokenUsage{
					CompletionTokens: 15,
					PromptTokens:     30,
					TotalTokens:      45,
				},
			},
		}

		msgs := []*Message{
			{
				Role: "assistant",
				ResponseMeta: &ResponseMeta{
					FinishReason: "",
					Usage:        nil,
				},
			},
			{
				Role: "assistant",
				ResponseMeta: &ResponseMeta{
					FinishReason: "stop",
				},
			},
			{
				Role: "assistant",
				ResponseMeta: &ResponseMeta{
					Usage: &TokenUsage{
						CompletionTokens: 15,
						PromptTokens:     30,
						TotalTokens:      45,
					},
				},
			},
		}

		msg, err := ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, exp, msg)
	})

	t.Run("concurrent concat", func(t *testing.T) {
		content := "i_am_a_good_concat_message"
		exp := &Message{Role: Assistant, Content: content}
		var msgs []*Message
		for i := 0; i < len(content); i++ {
			msgs = append(msgs, &Message{Role: Assistant, Content: content[i : i+1]})
		}

		wg := sync.WaitGroup{}
		size := 100
		wg.Add(size)
		for i := 0; i < size; i++ {
			go func() {
				defer wg.Done()
				msg, err := ConcatMessages(msgs)
				assert.NoError(t, err)
				assert.Equal(t, exp, msg)
			}()
		}

		wg.Wait()
	})

	t.Run("concat logprobs", func(t *testing.T) {
		msgs := []*Message{
			{
				Role:    Assistant,
				Content: "🚀",
				ResponseMeta: &ResponseMeta{
					LogProbs: &LogProbs{
						Content: []LogProb{
							{
								Token:   "\\xf0\\x9f\\x9a",
								LogProb: -0.0000073458323,
								Bytes:   []int64{240, 159, 154},
							},
							{
								Token:   "\\x80",
								LogProb: 0,
								Bytes:   []int64{128},
							},
						},
					},
				},
			},
			{
				Role:    "",
				Content: "❤️",
				ResponseMeta: &ResponseMeta{
					LogProbs: &LogProbs{
						Content: []LogProb{
							{
								Token:   "❤️",
								LogProb: -0.0011431955,
								Bytes:   []int64{226, 157, 164, 239, 184, 143},
							},
						},
					},
				},
			},
			{
				Role: "",
				ResponseMeta: &ResponseMeta{
					FinishReason: "stop",
					Usage: &TokenUsage{
						PromptTokens:     7,
						CompletionTokens: 3,
						TotalTokens:      10,
					},
				},
			},
		}

		msg, err := ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, 3, len(msg.ResponseMeta.LogProbs.Content))
		assert.Equal(t, msgs[0].ResponseMeta.LogProbs.Content[0], msg.ResponseMeta.LogProbs.Content[0])
		assert.Equal(t, msgs[0].ResponseMeta.LogProbs.Content[1], msg.ResponseMeta.LogProbs.Content[1])
		assert.Equal(t, msgs[1].ResponseMeta.LogProbs.Content[0], msg.ResponseMeta.LogProbs.Content[2])
	})

	t.Run("fix unexpected setting ResponseMeta of the first element in slice after ConcatMessages", func(t *testing.T) {
		msgs := []*Message{
			{
				Role:    Assistant,
				Content: "🚀",
				//ResponseMeta: &ResponseMeta{},
			},
			{
				Role:         "",
				Content:      "❤️",
				ResponseMeta: &ResponseMeta{},
			},
			{
				Role: "",
				ResponseMeta: &ResponseMeta{
					FinishReason: "stop",
					Usage: &TokenUsage{
						PromptTokens:     7,
						CompletionTokens: 3,
						TotalTokens:      10,
					},
				},
			},
		}

		msg, err := ConcatMessages(msgs)
		assert.NoError(t, err)
		assert.Equal(t, msgs[2].ResponseMeta, msg.ResponseMeta)
		assert.Nil(t, msgs[0].ResponseMeta)
	})

	t.Run("concat assistant multi content", func(t *testing.T) {
		base64Audio1 := "dGVzdF9hdWRpb18x"
		base64Audio2 := "dGVzdF9hdWRpb18y"
		imageURL1 := "https://example.com/image1.png"
		imageURL2 := "https://example.com/image2.png"

		msgs := []*Message{
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeText, Text: "Hello, "},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeText, Text: "world!"},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &base64Audio1}}},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &base64Audio2, MIMEType: "audio/wav"}}},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeImageURL, Image: &MessageOutputImage{MessagePartCommon: MessagePartCommon{URL: &imageURL1}}},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeImageURL, Image: &MessageOutputImage{MessagePartCommon: MessagePartCommon{URL: &imageURL2}}},
				},
			},
		}

		mergedMsg, err := ConcatMessages(msgs)
		assert.NoError(t, err)

		mergedBase64Audio := base64Audio1 + base64Audio2
		expectedContent := []MessageOutputPart{
			{Type: ChatMessagePartTypeText, Text: "Hello, world!"},
			{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &mergedBase64Audio, MIMEType: "audio/wav"}}},
			{Type: ChatMessagePartTypeImageURL, Image: &MessageOutputImage{MessagePartCommon: MessagePartCommon{URL: &imageURL1}}},
			{Type: ChatMessagePartTypeImageURL, Image: &MessageOutputImage{MessagePartCommon: MessagePartCommon{URL: &imageURL2}}},
		}

		assert.Equal(t, expectedContent, mergedMsg.AssistantGenMultiContent)
	})

	t.Run("concat assistant multi content with extra", func(t *testing.T) {
		base64Audio1 := "dGVzdF9hdWRpb18x"
		base64Audio2 := "dGVzdF9hdWRpb18y"

		msgs := []*Message{
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &base64Audio1, Extra: map[string]any{"key1": "val1"}}}},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &base64Audio2, Extra: map[string]any{"key2": "val2"}}}},
				},
			},
		}

		mergedMsg, err := ConcatMessages(msgs)
		assert.NoError(t, err)

		mergedBase64Audio := base64Audio1 + base64Audio2
		expectedContent := []MessageOutputPart{
			{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &mergedBase64Audio, Extra: map[string]any{"key1": "val1", "key2": "val2"}}}},
		}

		assert.Equal(t, expectedContent, mergedMsg.AssistantGenMultiContent)
	})

	t.Run("concat assistant multi content with single extra", func(t *testing.T) {
		base64Audio1 := "dGVzdF9hdWRpb18x"
		base64Audio2 := "dGVzdF9hdWRpb18y"

		msgs := []*Message{
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &base64Audio1, Extra: map[string]any{"key1": "val1"}}}},
				},
			},
			{
				Role: Assistant,
				AssistantGenMultiContent: []MessageOutputPart{
					{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &base64Audio2}}},
				},
			},
		}

		mergedMsg, err := ConcatMessages(msgs)
		assert.NoError(t, err)

		mergedBase64Audio := base64Audio1 + base64Audio2
		expectedContent := []MessageOutputPart{
			{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{MessagePartCommon: MessagePartCommon{Base64Data: &mergedBase64Audio, Extra: map[string]any{"key1": "val1"}}}},
		}

		assert.Equal(t, expectedContent, mergedMsg.AssistantGenMultiContent)
	})

	t.Run("concat multi content (deprecated)", func(t *testing.T) {
		msgs := []*Message{
			{
				Role: Assistant,
				MultiContent: []ChatMessagePart{
					{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: "image1.jpg"}},
				},
			},
			{
				Role: Assistant,
				MultiContent: []ChatMessagePart{
					{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: "image2.jpg"}},
				},
			},
		}

		mergedMsg, err := ConcatMessages(msgs)
		assert.NoError(t, err)

		expectedMultiContent := []ChatMessagePart{
			{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: "image1.jpg"}},
			{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: "image2.jpg"}},
		}

		assert.Equal(t, expectedMultiContent, mergedMsg.MultiContent)
	})
}

func TestConcatToolCalls(t *testing.T) {
	t.Run("atomic_field_in_first_chunk", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name: "tool_name",
				},
			},
			{
				Index: generic.PtrOf(0),
				Function: FunctionCall{
					Arguments: "call me please",
				},
			},
		}

		expectedToolCall := ToolCall{
			Index: generic.PtrOf(0),
			ID:    "tool_call_id",
			Type:  "function",
			Function: FunctionCall{
				Name:      "tool_name",
				Arguments: "call me please",
			},
		}

		tc, err := concatToolCalls(givenToolCalls)
		assert.NoError(t, err)
		assert.Len(t, tc, 1)
		assert.EqualValues(t, expectedToolCall, tc[0])
	})

	t.Run("atomic_field_in_every_chunk", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name: "tool_name",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name:      "tool_name",
					Arguments: "call me please",
				},
			},
		}

		expectedToolCall := ToolCall{
			Index: generic.PtrOf(0),
			ID:    "tool_call_id",
			Type:  "function",
			Function: FunctionCall{
				Name:      "tool_name",
				Arguments: "call me please",
			},
		}

		tc, err := concatToolCalls(givenToolCalls)
		assert.NoError(t, err)
		assert.Len(t, tc, 1)
		assert.EqualValues(t, expectedToolCall, tc[0])
	})

	t.Run("atomic_field_in_interval", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "",
				Type:  "function",
				Function: FunctionCall{
					Name:      "",
					Arguments: "call me please",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "",
				Function: FunctionCall{
					Name:      "",
					Arguments: "",
				},
			},
		}

		expectedToolCall := ToolCall{
			Index: generic.PtrOf(0),
			ID:    "tool_call_id",
			Type:  "function",
			Function: FunctionCall{
				Name:      "",
				Arguments: "call me please",
			},
		}

		tc, err := concatToolCalls(givenToolCalls)
		assert.NoError(t, err)
		assert.Len(t, tc, 1)
		assert.EqualValues(t, expectedToolCall, tc[0])
	})

	t.Run("different_tool_id", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name: "tool_name",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id_1",
				Type:  "function",
				Function: FunctionCall{
					Name:      "tool_name",
					Arguments: "call me please",
				},
			},
		}

		_, err := concatToolCalls(givenToolCalls)
		assert.ErrorContains(t, err, "cannot concat ToolCalls with different tool id")
	})

	t.Run("different_tool_type", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name: "tool_name",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function_1",
				Function: FunctionCall{
					Name:      "tool_name",
					Arguments: "call me please",
				},
			},
		}

		_, err := concatToolCalls(givenToolCalls)
		assert.ErrorContains(t, err, "cannot concat ToolCalls with different tool type")
	})

	t.Run("different_tool_name", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name: "tool_name",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name:      "tool_name_1",
					Arguments: "call me please",
				},
			},
		}

		_, err := concatToolCalls(givenToolCalls)
		assert.ErrorContains(t, err, "cannot concat ToolCalls with different tool name")
	})

	t.Run("multi_tool_call", func(t *testing.T) {
		givenToolCalls := []ToolCall{
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "",
				Type:  "function",
				Function: FunctionCall{
					Name:      "",
					Arguments: "call me please",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "",
				Function: FunctionCall{
					Name:      "",
					Arguments: "",
				},
			},
			{
				Index: generic.PtrOf(1),
				ID:    "tool_call_id",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
			{
				Index: generic.PtrOf(1),
				ID:    "",
				Type:  "function",
				Function: FunctionCall{
					Name:      "",
					Arguments: "call me please",
				},
			},
			{
				Index: generic.PtrOf(1),
				ID:    "tool_call_id",
				Type:  "",
				Function: FunctionCall{
					Name:      "",
					Arguments: "",
				},
			},
			{
				Index: nil,
				ID:    "22",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
			{
				Index: nil,
				ID:    "44",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
		}

		expectedToolCall := []ToolCall{
			{
				Index: nil,
				ID:    "22",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
			{
				Index: nil,
				ID:    "44",
				Type:  "",
				Function: FunctionCall{
					Name: "",
				},
			},
			{
				Index: generic.PtrOf(0),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name:      "",
					Arguments: "call me please",
				},
			},
			{
				Index: generic.PtrOf(1),
				ID:    "tool_call_id",
				Type:  "function",
				Function: FunctionCall{
					Name:      "",
					Arguments: "call me please",
				},
			},
		}

		tc, err := concatToolCalls(givenToolCalls)
		assert.NoError(t, err)
		assert.EqualValues(t, expectedToolCall, tc)
	})
}

func TestFormatMultiContent(t *testing.T) {
	vs := map[string]any{
		"name": "eino",
		"url":  "https://example.com/img.png",
		"id":   "42",
	}

	t.Run("empty input", func(t *testing.T) {
		out, err := formatMultiContent(nil, vs, FString)
		assert.NoError(t, err)
		assert.Equal(t, []ChatMessagePart{}, out)
	})

	t.Run("render text and urls with FString", func(t *testing.T) {
		in := []ChatMessagePart{
			{Type: ChatMessagePartTypeText, Text: "hello {name}"},
			{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: "{url}"}},
			{Type: ChatMessagePartTypeAudioURL, AudioURL: &ChatMessageAudioURL{URL: "http://audio/{id}.wav"}},
			{Type: ChatMessagePartTypeVideoURL, VideoURL: &ChatMessageVideoURL{URL: "http://video/{id}.mp4"}},
			{Type: ChatMessagePartTypeFileURL, FileURL: &ChatMessageFileURL{URL: "http://file/{id}.txt"}},
		}

		out, err := formatMultiContent(in, vs, FString)
		assert.NoError(t, err)
		if assert.Len(t, out, len(in)) {
			assert.Equal(t, "hello eino", out[0].Text)
			assert.Equal(t, "https://example.com/img.png", out[1].ImageURL.URL)
			assert.Equal(t, "http://audio/42.wav", out[2].AudioURL.URL)
			assert.Equal(t, "http://video/42.mp4", out[3].VideoURL.URL)
			assert.Equal(t, "http://file/42.txt", out[4].FileURL.URL)
		}
	})

	t.Run("nil nested pointer should be skipped", func(t *testing.T) {
		in := []ChatMessagePart{
			{Type: ChatMessagePartTypeImageURL, ImageURL: nil},
			{Type: ChatMessagePartTypeAudioURL, AudioURL: nil},
			{Type: ChatMessagePartTypeVideoURL, VideoURL: nil},
			{Type: ChatMessagePartTypeFileURL, FileURL: nil},
		}
		out, err := formatMultiContent(in, vs, FString)
		assert.NoError(t, err)
		assert.Equal(t, in, out)
	})

	t.Run("missing var should error in GoTemplate", func(t *testing.T) {
		in := []ChatMessagePart{{Type: ChatMessagePartTypeText, Text: "hi {{.who}}"}}
		_, err := formatMultiContent(in, map[string]any{"name": "x"}, GoTemplate)
		assert.Error(t, err)
	})

}

func TestFormatUserInputMultiContent(t *testing.T) {
	makeStrPtr := func(s string) *string { return &s }

	vs := map[string]any{
		"x":    "world",
		"img":  "https://example.com/i.png",
		"b64":  "YmFzZTY0",
		"aid":  "99",
		"vid":  "77",
		"file": "abc",
	}

	t.Run("empty input", func(t *testing.T) {
		out, err := formatUserInputMultiContent(nil, vs, FString)
		assert.NoError(t, err)
		assert.Equal(t, []MessageInputPart{}, out)
	})

	t.Run("render text and both URL/Base64 for each type", func(t *testing.T) {
		in := []MessageInputPart{
			{Type: ChatMessagePartTypeText, Text: "hello {x}"},
			{Type: ChatMessagePartTypeImageURL, Image: &MessageInputImage{MessagePartCommon: MessagePartCommon{URL: makeStrPtr("{img}"), Base64Data: makeStrPtr("{b64}")}}},
			{Type: ChatMessagePartTypeAudioURL, Audio: &MessageInputAudio{MessagePartCommon: MessagePartCommon{URL: makeStrPtr("http://a/{aid}.wav"), Base64Data: makeStrPtr("{b64}")}}},
			{Type: ChatMessagePartTypeVideoURL, Video: &MessageInputVideo{MessagePartCommon: MessagePartCommon{URL: makeStrPtr("http://v/{vid}.mp4"), Base64Data: makeStrPtr("{b64}")}}},
			{Type: ChatMessagePartTypeFileURL, File: &MessageInputFile{MessagePartCommon: MessagePartCommon{URL: makeStrPtr("/f/{file}.txt"), Base64Data: makeStrPtr("{b64}")}}},
		}

		out, err := formatUserInputMultiContent(in, vs, FString)
		assert.NoError(t, err)
		if assert.Len(t, out, len(in)) {
			assert.Equal(t, "hello world", out[0].Text)
			assert.Equal(t, "https://example.com/i.png", *out[1].Image.URL)
			assert.Equal(t, "YmFzZTY0", *out[1].Image.Base64Data)
			assert.Equal(t, "http://a/99.wav", *out[2].Audio.URL)
			assert.Equal(t, "YmFzZTY0", *out[2].Audio.Base64Data)
			assert.Equal(t, "http://v/77.mp4", *out[3].Video.URL)
			assert.Equal(t, "YmFzZTY0", *out[3].Video.Base64Data)
			assert.Equal(t, "/f/abc.txt", *out[4].File.URL)
			assert.Equal(t, "YmFzZTY0", *out[4].File.Base64Data)
		}
	})

	t.Run("empty string pointer should not be formatted", func(t *testing.T) {
		empty := ""
		in := []MessageInputPart{
			{Type: ChatMessagePartTypeImageURL, Image: &MessageInputImage{MessagePartCommon: MessagePartCommon{URL: &empty, Base64Data: &empty}}},
		}
		out, err := formatUserInputMultiContent(in, vs, FString)
		assert.NoError(t, err)
		if assert.Len(t, out, 1) {
			assert.NotNil(t, out[0].Image.URL)
			assert.NotNil(t, out[0].Image.Base64Data)
			assert.Equal(t, "", *out[0].Image.URL)
			assert.Equal(t, "", *out[0].Image.Base64Data)
		}
	})
}

func TestConcatToolResults(t *testing.T) {
	t.Run("empty_chunks", func(t *testing.T) {
		result, err := ConcatToolResults([]*ToolResult{})
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, result.Parts)
	})

	t.Run("nil_chunks", func(t *testing.T) {
		result, err := ConcatToolResults([]*ToolResult{nil, nil})
		assert.NoError(t, err)
		assert.NotNil(t, result)
		assert.Empty(t, result.Parts)
	})

	t.Run("single_text_part", func(t *testing.T) {
		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "Hello World"},
				},
			},
		}
		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 1)
		assert.Equal(t, ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, "Hello World", result.Parts[0].Text)
	})

	t.Run("multiple_text_parts_merge", func(t *testing.T) {
		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "Hello "},
				},
			},
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "World"},
				},
			},
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "!"},
				},
			},
		}
		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 3)

	})

	t.Run("cross_chunk_audio_conflict_error", func(t *testing.T) {
		base64Data1 := "YXVkaW8x"
		base64Data2 := "YXVkaW8y"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								Base64Data: &base64Data1,
								MIMEType:   "audio/wav",
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								Base64Data: &base64Data2,
								MIMEType:   "audio/wav",
							},
						},
					},
				},
			},
		}

		_, err := ConcatToolResults(chunks)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "audio")
	})

	t.Run("mixed_types_no_merge", func(t *testing.T) {
		imageURL := "https://example.com/image.png"
		videoURL := "https://example.com/video.mp4"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "Text part"},
					{
						Type: ToolPartTypeImage,
						Image: &ToolOutputImage{
							MessagePartCommon: MessagePartCommon{
								URL: &imageURL,
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeVideo,
						Video: &ToolOutputVideo{
							MessagePartCommon: MessagePartCommon{
								URL: &videoURL,
							},
						},
					},
				},
			},
		}

		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 3)
		assert.Equal(t, ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, ToolPartTypeImage, result.Parts[1].Type)
		assert.Equal(t, ToolPartTypeVideo, result.Parts[2].Type)
	})

	t.Run("mixed_text_and_single_audio", func(t *testing.T) {
		base64Data1 := "YXVkaW8x"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "Part 1 "},
					{Type: ToolPartTypeText, Text: "Part 2"},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								Base64Data: &base64Data1,
								MIMEType:   "audio/wav",
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: " Part 3"},
				},
			},
		}

		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 3)

		assert.Equal(t, ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, "Part 1 Part 2", result.Parts[0].Text)

		assert.Equal(t, ToolPartTypeAudio, result.Parts[1].Type)
		assert.NotNil(t, result.Parts[1].Audio)
		assert.NotNil(t, result.Parts[1].Audio.Base64Data)
		assert.Equal(t, "YXVkaW8x", *result.Parts[1].Audio.Base64Data)

		assert.Equal(t, ToolPartTypeText, result.Parts[2].Type)
		assert.Equal(t, " Part 3", result.Parts[2].Text)
	})

	t.Run("cross_chunk_audio_url_and_base64_conflict_error", func(t *testing.T) {
		audioURL := "https://example.com/audio.wav"
		base64Data := "YXVkaW8x"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								URL:      &audioURL,
								MIMEType: "audio/wav",
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								Base64Data: &base64Data,
								MIMEType:   "audio/wav",
							},
						},
					},
				},
			},
		}

		_, err := ConcatToolResults(chunks)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "audio")
	})

	t.Run("single_audio_with_extra_fields", func(t *testing.T) {
		base64Data1 := "YXVkaW8x"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								Base64Data: &base64Data1,
								MIMEType:   "audio/wav",
								Extra: map[string]any{
									"key1": "value1",
								},
							},
						},
					},
				},
			},
		}

		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 1)
		assert.Equal(t, ToolPartTypeAudio, result.Parts[0].Type)
		assert.NotNil(t, result.Parts[0].Audio)
		assert.NotNil(t, result.Parts[0].Audio.Base64Data)
		assert.Equal(t, "YXVkaW8x", *result.Parts[0].Audio.Base64Data)
		assert.NotNil(t, result.Parts[0].Audio.Extra)
		assert.Equal(t, "value1", result.Parts[0].Audio.Extra["key1"])
	})

	t.Run("cross_chunk_image_conflict_error", func(t *testing.T) {
		imageURL1 := "https://example.com/image1.png"
		imageURL2 := "https://example.com/image2.png"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeImage,
						Image: &ToolOutputImage{
							MessagePartCommon: MessagePartCommon{
								URL: &imageURL1,
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeImage,
						Image: &ToolOutputImage{
							MessagePartCommon: MessagePartCommon{
								URL: &imageURL2,
							},
						},
					},
				},
			},
		}

		_, err := ConcatToolResults(chunks)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "image")
	})

	t.Run("cross_chunk_video_conflict_error", func(t *testing.T) {
		videoURL1 := "https://example.com/video1.mp4"
		videoURL2 := "https://example.com/video2.mp4"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeVideo,
						Video: &ToolOutputVideo{
							MessagePartCommon: MessagePartCommon{
								URL: &videoURL1,
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeVideo,
						Video: &ToolOutputVideo{
							MessagePartCommon: MessagePartCommon{
								URL: &videoURL2,
							},
						},
					},
				},
			},
		}

		_, err := ConcatToolResults(chunks)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "video")
	})

	t.Run("cross_chunk_file_conflict_error", func(t *testing.T) {
		fileURL1 := "https://example.com/file1.pdf"
		fileURL2 := "https://example.com/file2.pdf"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeFile,
						File: &ToolOutputFile{
							MessagePartCommon: MessagePartCommon{
								URL: &fileURL1,
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeFile,
						File: &ToolOutputFile{
							MessagePartCommon: MessagePartCommon{
								URL: &fileURL2,
							},
						},
					},
				},
			},
		}

		_, err := ConcatToolResults(chunks)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "conflicting")
		assert.Contains(t, err.Error(), "file")
	})

	t.Run("cross_chunk_text_not_merged", func(t *testing.T) {
		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "Hello "},
				},
			},
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "World"},
				},
			},
		}

		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 2)
		assert.Equal(t, ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, "Hello ", result.Parts[0].Text)
		assert.Equal(t, ToolPartTypeText, result.Parts[1].Type)
		assert.Equal(t, "World", result.Parts[1].Text)
	})

	t.Run("same_chunk_text_merged", func(t *testing.T) {
		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{Type: ToolPartTypeText, Text: "Hello "},
					{Type: ToolPartTypeText, Text: "World"},
				},
			},
		}

		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 1)
		assert.Equal(t, ToolPartTypeText, result.Parts[0].Type)
		assert.Equal(t, "Hello World", result.Parts[0].Text)
	})

	t.Run("different_non_text_types_across_chunks_allowed", func(t *testing.T) {
		imageURL := "https://example.com/image.png"
		videoURL := "https://example.com/video.mp4"
		base64Audio := "YXVkaW8x"

		chunks := []*ToolResult{
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeImage,
						Image: &ToolOutputImage{
							MessagePartCommon: MessagePartCommon{
								URL: &imageURL,
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeVideo,
						Video: &ToolOutputVideo{
							MessagePartCommon: MessagePartCommon{
								URL: &videoURL,
							},
						},
					},
				},
			},
			{
				Parts: []ToolOutputPart{
					{
						Type: ToolPartTypeAudio,
						Audio: &ToolOutputAudio{
							MessagePartCommon: MessagePartCommon{
								Base64Data: &base64Audio,
								MIMEType:   "audio/wav",
							},
						},
					},
				},
			},
		}

		result, err := ConcatToolResults(chunks)
		assert.NoError(t, err)
		assert.Len(t, result.Parts, 3)
		assert.Equal(t, ToolPartTypeImage, result.Parts[0].Type)
		assert.Equal(t, ToolPartTypeVideo, result.Parts[1].Type)
		assert.Equal(t, ToolPartTypeAudio, result.Parts[2].Type)
	})
}

func TestMessageString(t *testing.T) {
	t.Run("basic message", func(t *testing.T) {
		msg := &Message{
			Role:    User,
			Content: "Hello, world!",
		}
		result := msg.String()
		assert.Contains(t, result, "user: Hello, world!")
	})

	t.Run("message with UserInputMultiContent", func(t *testing.T) {
		imageURL := "https://example.com/image.png"
		msg := &Message{
			Role:    User,
			Content: "",
			UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeText, Text: "Describe this image:"},
				{Type: ChatMessagePartTypeImageURL, Image: &MessageInputImage{
					MessagePartCommon: MessagePartCommon{URL: &imageURL},
				}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "user_input_multi_content:")
		assert.Contains(t, result, "[0] text: Describe this image:")
		assert.Contains(t, result, "[1] image: url=https://example.com/image.png")
	})

	t.Run("message with AssistantGenMultiContent", func(t *testing.T) {
		base64Data := "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="
		msg := &Message{
			Role:    Assistant,
			Content: "",
			AssistantGenMultiContent: []MessageOutputPart{
				{Type: ChatMessagePartTypeText, Text: "Here is the generated image:"},
				{Type: ChatMessagePartTypeImageURL, Image: &MessageOutputImage{
					MessagePartCommon: MessagePartCommon{
						Base64Data: &base64Data,
						MIMEType:   "image/png",
					},
				}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "assistant_gen_multi_content:")
		assert.Contains(t, result, "[0] text: Here is the generated image:")
		assert.Contains(t, result, "[1] image: base64[")
		assert.Contains(t, result, "mime=image/png")
	})

	t.Run("message with MultiContent (deprecated)", func(t *testing.T) {
		msg := &Message{
			Role:    User,
			Content: "",
			MultiContent: []ChatMessagePart{
				{Type: ChatMessagePartTypeText, Text: "What is this?"},
				{Type: ChatMessagePartTypeImageURL, ImageURL: &ChatMessageImageURL{URL: "https://example.com/photo.jpg"}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "multi_content:")
		assert.Contains(t, result, "[0] text: What is this?")
		assert.Contains(t, result, "[1] image_url: https://example.com/photo.jpg")
	})

	t.Run("message with ToolCalls", func(t *testing.T) {
		idx := 0
		msg := &Message{
			Role:    Assistant,
			Content: "",
			ToolCalls: []ToolCall{
				{
					Index: &idx,
					ID:    "call_123",
					Type:  "function",
					Function: FunctionCall{
						Name:      "get_weather",
						Arguments: `{"location": "Beijing"}`,
					},
				},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "tool_calls:")
		assert.Contains(t, result, "index[0]:")
		assert.Contains(t, result, "get_weather")
	})

	t.Run("tool message", func(t *testing.T) {
		msg := &Message{
			Role:       Tool,
			Content:    `{"temperature": 25}`,
			ToolCallID: "call_123",
			ToolName:   "get_weather",
		}
		result := msg.String()
		assert.Contains(t, result, "tool: {\"temperature\": 25}")
		assert.Contains(t, result, "tool_call_id: call_123")
		assert.Contains(t, result, "tool_call_name: get_weather")
	})

	t.Run("message with reasoning content", func(t *testing.T) {
		msg := &Message{
			Role:             Assistant,
			Content:          "The answer is 42.",
			ReasoningContent: "Let me think about this step by step...",
		}
		result := msg.String()
		assert.Contains(t, result, "reasoning content:")
		assert.Contains(t, result, "Let me think about this step by step...")
	})

	t.Run("message with response meta", func(t *testing.T) {
		msg := &Message{
			Role:    Assistant,
			Content: "Hello!",
			ResponseMeta: &ResponseMeta{
				FinishReason: "stop",
				Usage: &TokenUsage{
					PromptTokens:     10,
					CompletionTokens: 5,
					TotalTokens:      15,
				},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "finish_reason: stop")
		assert.Contains(t, result, "usage:")
	})

	t.Run("message with audio input", func(t *testing.T) {
		audioURL := "https://example.com/audio.wav"
		msg := &Message{
			Role: User,
			UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeAudioURL, Audio: &MessageInputAudio{
					MessagePartCommon: MessagePartCommon{URL: &audioURL},
				}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "[0] audio: url=https://example.com/audio.wav")
	})

	t.Run("message with video input", func(t *testing.T) {
		videoURL := "https://example.com/video.mp4"
		msg := &Message{
			Role: User,
			UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeVideoURL, Video: &MessageInputVideo{
					MessagePartCommon: MessagePartCommon{URL: &videoURL},
				}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "[0] video: url=https://example.com/video.mp4")
	})

	t.Run("message with file input", func(t *testing.T) {
		fileURL := "https://example.com/document.pdf"
		msg := &Message{
			Role: User,
			UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeFileURL, File: &MessageInputFile{
					MessagePartCommon: MessagePartCommon{URL: &fileURL},
				}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "[0] file: url=https://example.com/document.pdf")
	})

	t.Run("nil media parts", func(t *testing.T) {
		msg := &Message{
			Role: User,
			UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeImageURL, Image: nil},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "[0] image: <nil>")
	})

	t.Run("combined multi-content types", func(t *testing.T) {
		imageURL := "https://example.com/image.png"
		base64Audio := "YXVkaW9kYXRh"
		msg := &Message{
			Role:    User,
			Content: "Main content",
			UserInputMultiContent: []MessageInputPart{
				{Type: ChatMessagePartTypeText, Text: "User input text"},
				{Type: ChatMessagePartTypeImageURL, Image: &MessageInputImage{
					MessagePartCommon: MessagePartCommon{URL: &imageURL},
				}},
			},
			AssistantGenMultiContent: []MessageOutputPart{
				{Type: ChatMessagePartTypeText, Text: "Assistant output text"},
				{Type: ChatMessagePartTypeAudioURL, Audio: &MessageOutputAudio{
					MessagePartCommon: MessagePartCommon{
						Base64Data: &base64Audio,
						MIMEType:   "audio/wav",
					},
				}},
			},
		}
		result := msg.String()
		assert.Contains(t, result, "user: Main content")
		assert.Contains(t, result, "user_input_multi_content:")
		assert.Contains(t, result, "assistant_gen_multi_content:")
	})
}
