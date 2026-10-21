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

package automemory

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal/startwindow"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func TestSendTopicMemoryEventAnchorsBeforeLastUserQuery(t *testing.T) {
	user := schema.UserMessage("question")
	assistant := schema.AssistantMessage("working", nil)
	toolResult := schema.ToolMessage("done", "call-1")
	for _, msg := range []*schema.Message{user, assistant, toolResult} {
		adk.EnsureMessageID(msg)
	}

	var captured *adk.TypedAgentEvent[*schema.Message]
	parentCtx := startwindow.WithSender(context.Background(), func(_ context.Context, event any) error {
		var ok bool
		captured, ok = event.(*adk.TypedAgentEvent[*schema.Message])
		require.True(t, ok)
		return nil
	})
	sendCtx, _ := startwindow.Open(parentCtx)
	defer startwindow.Signal(sendCtx)

	m := &Middleware[*schema.Message]{}
	m.sendTopicMemoryEvent(
		sendCtx,
		[]*schema.Message{user, assistant, toolResult},
		newMemoryMessage[*schema.Message]("memory"),
	)

	require.NotNil(t, captured)
	require.NotNil(t, captured.SessionEventVariant)
	require.NotNil(t, captured.SessionEventVariant.Event)
	require.NotNil(t, captured.SessionEventVariant.Event.MessageInserted)
	assert.Equal(
		t,
		adk.GetMessageID(user),
		captured.SessionEventVariant.Event.MessageInserted.BeforeMessageID,
	)
}

func TestAppendTopicMemoryEventUsesEmptyAnchor(t *testing.T) {
	var captured *adk.TypedAgentEvent[*schema.Message]
	parentCtx := startwindow.WithSender(context.Background(), func(_ context.Context, event any) error {
		var ok bool
		captured, ok = event.(*adk.TypedAgentEvent[*schema.Message])
		require.True(t, ok)
		return nil
	})
	sendCtx, _ := startwindow.Open(parentCtx)
	defer startwindow.Signal(sendCtx)

	m := &Middleware[*schema.Message]{}
	m.appendTopicMemoryEvent(sendCtx, newMemoryMessage[*schema.Message]("memory"))

	require.NotNil(t, captured)
	require.NotNil(t, captured.SessionEventVariant)
	require.NotNil(t, captured.SessionEventVariant.Event)
	require.NotNil(t, captured.SessionEventVariant.Event.MessageInserted)
	assert.Empty(t, captured.SessionEventVariant.Event.MessageInserted.BeforeMessageID)
}

func TestParseTopicSelectionFromContent(t *testing.T) {
	valid := map[string]struct{}{
		"debugging.md": {},
		"patterns.md":  {},
	}

	tests := []struct {
		name    string
		content string
		want    []string
		wantErr string
	}{
		{
			name:    "pure JSON",
			content: `{"selected_memories":["debugging.md","missing.md"]}`,
			want:    []string{"debugging.md"},
		},
		{
			name:    "markdown code block",
			content: "Result:\n```json\n{\"selected_memories\":[\"patterns.md\"]}\n```",
			want:    []string{"patterns.md"},
		},
		{
			name:    "empty selection",
			content: `{"selected_memories":[]}`,
			want:    []string{},
		},
		{
			name:    "no JSON",
			content: "No relevant memory.",
			wantErr: "no JSON found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &schema.Message{Role: schema.Assistant, Content: tt.content}
			got, err := parseTopicSelectionFromContent(msg, valid)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestTopicSelectionJSONSchema(t *testing.T) {
	result := topicSelectionJSONSchema()
	require.NotNil(t, result)
	assert.Equal(t, "object", result.Type)

	selected, ok := result.Properties.Get("selected_memories")
	require.True(t, ok)
	assert.Equal(t, "array", selected.Type)
}

// Embedding fixedModel supplies the unused Generate endpoint.
type emptyTopicSelectionModel struct {
	fixedModel
	writer *schema.StreamWriter[*schema.Message]
}

func (m *emptyTopicSelectionModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	r, w := schema.Pipe[*schema.Message](1)
	m.writer = w
	w.Send(nil, io.EOF)
	return r, nil
}

func TestTopicSelectionEmptyStream(t *testing.T) {
	for _, mode := range []TopicSelectionOutputMode{"", TopicSelectionOutputModeJSONObject} {
		name := string(mode)
		if name == "" {
			name = "plain"
		}
		t.Run(name, func(t *testing.T) {
			selectionModel := &emptyTopicSelectionModel{}
			mw := &Middleware[*schema.Message]{topicSelectionModel: selectionModel}
			var err error
			if mode == "" {
				_, err = mw.selectTopicPlain(context.Background(), "question", nil, 1)
			} else {
				_, err = mw.selectTopicWithResponseFormat(context.Background(), mode, "question", nil, 1)
			}
			require.ErrorContains(t, err, "empty response content")
			require.NotNil(t, selectionModel.writer)
			defer selectionModel.writer.Close()
			assert.True(t, selectionModel.writer.Send(nil, nil), "topic selection must close the consumed stream")
		})
	}
}
