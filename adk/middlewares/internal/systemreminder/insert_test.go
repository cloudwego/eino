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

package systemreminder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

const testExtraKey = "__test_reminder__"

func TestInsert(t *testing.T) {
	ctx := context.Background()

	t.Run("after the latest user message", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hi"},
		}
		got := Insert(ctx, input, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 3)
		assert.Equal(t, "sys", got[0].Content)
		assert.Equal(t, schema.User, got[1].Role)
		assert.Equal(t, schema.System, got[2].Role)
		assert.Equal(t, "<reminder>", got[2].Content)
		assert.Equal(t, true, got[2].Extra[testExtraKey])
		assert.Equal(t, true, got[2].Extra[adk.MessageExtraKeySystemReminder])
	})

	t.Run("after a final assistant answer", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.User, Content: "hi"},
			{Role: schema.Assistant, Content: "hello"},
		}
		got := Insert(ctx, input, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 3)
		assert.Equal(t, schema.User, got[0].Role)
		assert.Equal(t, schema.Assistant, got[1].Role)
		assert.Equal(t, "<reminder>", got[2].Content)
	})

	t.Run("before pending tool-call scaffolding, after the user message", func(t *testing.T) {
		toolCall := &schema.Message{
			Role:      schema.Assistant,
			ToolCalls: []schema.ToolCall{{ID: "call-1"}},
		}
		input := []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hi"},
			toolCall,
		}
		got := Insert(ctx, input, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 4)
		// The latest anchor is the user message; the reminder lands right after it and
		// before the pending tool-call, never inside the tool scaffolding.
		assert.Equal(t, schema.User, got[1].Role)
		assert.Equal(t, "<reminder>", got[2].Content)
		assert.Equal(t, toolCall, got[3])
	})

	t.Run("past a settled sibling reminder already after the user message", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys"},
			{Role: schema.User, Content: "hi"},
			{Role: schema.System, Content: "sibling-reminder", Extra: map[string]any{"__sibling__": true}},
		}
		got := Insert(ctx, input, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 4)
		// Insert lands after the user message, then skips the settled sibling reminder,
		// so sibling order is preserved and the fresh reminder follows it.
		assert.Equal(t, schema.User, got[1].Role)
		assert.Equal(t, "sibling-reminder", got[2].Content)
		assert.Equal(t, "<reminder>", got[3].Content)
	})

	t.Run("tail when no anchor", func(t *testing.T) {
		input := []*schema.Message{
			{Role: schema.System, Content: "sys1"},
			{Role: schema.System, Content: "sys2"},
		}
		got := Insert(ctx, input, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 3)
		assert.Equal(t, "<reminder>", got[2].Content)
	})

	t.Run("empty input", func(t *testing.T) {
		got := Insert[*schema.Message](ctx, nil, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 1)
		assert.Equal(t, schema.System, got[0].Role)
		assert.Equal(t, "<reminder>", got[0].Content)
	})

	t.Run("does not mutate input", func(t *testing.T) {
		input := []*schema.Message{{Role: schema.User, Content: "hi"}}
		_ = Insert[*schema.Message](ctx, input, testExtraKey, "<reminder>", nil)
		assert.Len(t, input, 1)
	})

	t.Run("carries extra entries", func(t *testing.T) {
		input := []*schema.Message{{Role: schema.User, Content: "hi"}}
		got := Insert(ctx, input, testExtraKey, "<reminder>", map[string]any{"digest": []string{"a", "b"}})
		require.Len(t, got, 2)
		assert.Equal(t, []string{"a", "b"}, got[1].Extra["digest"])
	})

	t.Run("agentic message", func(t *testing.T) {
		user := schema.UserAgenticMessage("hi")
		got := Insert(ctx, []*schema.AgenticMessage{user}, testExtraKey, "<reminder>", nil)
		require.Len(t, got, 2)
		assert.Equal(t, schema.AgenticRoleTypeUser, got[0].Role)
		assert.Equal(t, schema.AgenticRoleTypeSystem, got[1].Role)
		assert.Equal(t, true, got[1].Extra[testExtraKey])
		assert.NotEmpty(t, adk.GetMessageID(got[1]))
	})
}

func TestHas(t *testing.T) {
	ctx := context.Background()
	input := []*schema.Message{{Role: schema.User, Content: "hi"}}
	assert.False(t, Has(input, testExtraKey))
	got := Insert(ctx, input, testExtraKey, "<reminder>", nil)
	assert.True(t, Has(got, testExtraKey))
	assert.False(t, Has(got, "__other__"))
}

func TestLatestExtra(t *testing.T) {
	ctx := context.Background()
	input := []*schema.Message{{Role: schema.User, Content: "hi"}}

	_, ok := LatestExtra(input, testExtraKey, "digest")
	assert.False(t, ok)

	got := Insert(ctx, input, testExtraKey, "old", map[string]any{"digest": []string{"a"}})
	got = Insert(ctx, got, testExtraKey, "new", map[string]any{"digest": []string{"a", "b"}})

	v, ok := LatestExtra(got, testExtraKey, "digest")
	require.True(t, ok)
	// Returns the value from the LAST reminder tagged extraKey.
	assert.Equal(t, []string{"a", "b"}, v)
}
