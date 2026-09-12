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
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestConcatMessageStream_MultipleChunks(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "hello "},
		{Role: schema.Assistant, Content: "world"},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, schema.Assistant, msg.Role)
	assert.Equal(t, "hello world", msg.Content)
}

func TestConcatMessageStream_SingleChunk(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "only one"},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "only one", msg.Content)
}

func TestConcatMessageStream_EmptyStream(t *testing.T) {
	r := schema.StreamReaderFromArray([]*schema.Message{})

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	assert.Nil(t, msg)
}

func TestConcatMessageStream_ConcatError(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "a"},
		{Role: schema.User, Content: "b"},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.Error(t, err)
	assert.Nil(t, msg)
	assert.Contains(t, err.Error(), "different roles")
}

func TestConcatMessageStream_WithToolCalls(t *testing.T) {
	chunks := []*schema.Message{
		{Role: schema.Assistant, Content: "thinking..."},
		{Role: schema.Assistant, ToolCalls: []schema.ToolCall{
			{ID: "call_1", Type: "function", Function: schema.FunctionCall{Name: "search", Arguments: `{"q":"test"}`}},
		}},
	}
	r := schema.StreamReaderFromArray(chunks)

	msg, err := concatMessageStream(r)
	require.NoError(t, err)
	require.NotNil(t, msg)
	assert.Equal(t, "thinking...", msg.Content)
	require.Len(t, msg.ToolCalls, 1)
	assert.Equal(t, "search", msg.ToolCalls[0].Function.Name)
}

func TestTopicMemoryQueryScope(t *testing.T) {
	t.Run("message", testTopicMemoryQueryScope[*schema.Message])
	t.Run("agentic_message", testTopicMemoryQueryScope[*schema.AgenticMessage])
}

func testTopicMemoryQueryScope[M adk.MessageType](t *testing.T) {
	query := makeUserMsg[M]("refund rules")
	key := topicMemoryQueryKey([]M{query})
	topic := newMemoryMessage[M]("<!-- automemory -->refund notes")
	copyAndSetMsgExtra(topic, topicMemoryQueryExtraKey, key)
	index := newMemoryIndexMessage[M]("<!-- automemory:index -->index")
	reminder := makeUserMsg[M]("<system-reminder>other middleware</system-reminder>")
	var nilMsg M
	for _, messages := range [][]M{
		{index, topic, query}, // Sync inserts before the query.
		{index, query, topic}, // Async appends after the query.
		{nilMsg, index, query, topic, reminder},
	} {
		require.True(t, hasTopicMemoryInjected(messages))
		data, err := json.Marshal(messages)
		require.NoError(t, err)
		var restored []M
		require.NoError(t, json.Unmarshal(data, &restored))
		require.True(t, hasTopicMemoryInjected(restored))
		// A consecutive query must not inherit an earlier async reminder,
		// even when there is no assistant response between the two queries.
		require.False(t, hasTopicMemoryInjected(append(restored, makeUserMsg[M]("change appointment"))))
		require.False(t, hasTopicMemoryInjected(append(messages, makeUserMsg[M]("refund rules"))))
	}
	require.False(t, hasTopicMemoryInjected([]M{nilMsg, index, topic}))
	require.False(t, hasTopicMemoryInjected([]M{newMemoryMessage[M]("legacy topic"), query}))
}

func TestTopicMemoryQueryScopeIgnoresAgenticToolResults(t *testing.T) {
	query := schema.UserAgenticMessage("refund rules")
	key := topicMemoryQueryKey([]*schema.AgenticMessage{query})
	topic := newMemoryMessage[*schema.AgenticMessage]("<!-- automemory -->refund notes")
	copyAndSetMsgExtra(topic, topicMemoryQueryExtraKey, key)
	result := &schema.AgenticMessage{
		Role: schema.AgenticRoleTypeUser,
		ContentBlocks: []*schema.ContentBlock{
			schema.NewContentBlock(&schema.FunctionToolResult{CallID: "call-1", Name: "lookup"}),
		},
	}
	messages := []*schema.AgenticMessage{topic, query, result}
	require.True(t, hasTopicMemoryInjected(messages))
	require.Equal(t, 1, lastUserQueryMessageIndex(messages))
}
