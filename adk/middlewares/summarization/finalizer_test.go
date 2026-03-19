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

package summarization

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestPreserveSkillsConfigCheck(t *testing.T) {
	ptr := func(i int) *int { return &i }

	t.Run("valid config", func(t *testing.T) {
		c := &PreserveSkillsConfig{
			MaxSkills:     ptr(5),
			SkillToolName: "load_skill",
		}
		assert.NoError(t, c.check())
	})

	t.Run("zero max skills", func(t *testing.T) {
		c := &PreserveSkillsConfig{
			MaxSkills: ptr(0),
		}
		assert.NoError(t, c.check())
	})

	t.Run("negative max skills", func(t *testing.T) {
		c := &PreserveSkillsConfig{
			MaxSkills: ptr(-1),
		}
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "MaxSkills must be non-negative")
	})

	t.Run("missing tool name sets default", func(t *testing.T) {
		c := &PreserveSkillsConfig{
			MaxSkills: ptr(5),
		}
		err := c.check()
		assert.NoError(t, err)
	})

	t.Run("nil max skills sets default", func(t *testing.T) {
		c := &PreserveSkillsConfig{}
		err := c.check()
		assert.NoError(t, err)
	})
}

func TestPreserveSkillsFinalizer(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()
	finalizer, err := PreserveSkillsFinalizer(ctx, PreserveSkillsConfig{
		MaxSkills:     ptr(2),
		SkillToolName: "load_skill",
	})
	assert.NoError(t, err)

	messages := []adk.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: "call_1",
					Function: schema.FunctionCall{
						Name:      "load_skill",
						Arguments: `{"skill": "test-skill"}`,
					},
				},
			},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			Content:    "skill content 1",
		},
	}

	ctx = context.WithValue(ctx, ctxKeySummarizeInput, messages)

	summary := schema.AssistantMessage("test summary", nil)

	result, err := finalizer(ctx, []adk.Message{schema.UserMessage("original")}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 1)

	assert.Empty(t, result[0].Content)
	assert.Len(t, result[0].UserInputMultiContent, 2)
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "test-skill")
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "skill content 1")
	assert.Equal(t, "test summary", result[0].UserInputMultiContent[1].Text)
}

func TestPreserveSkillsFinalizerWithNext(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()
	finalizer, err := PreserveSkillsFinalizer(ctx, PreserveSkillsConfig{
		MaxSkills:     ptr(2),
		SkillToolName: "load_skill",
		Next: func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			summary.Content = "next called"
			return append([]adk.Message{summary}, schema.UserMessage("next msg")), nil
		},
	})
	assert.NoError(t, err)

	messages := []adk.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{
					ID: "call_1",
					Function: schema.FunctionCall{
						Name:      "load_skill",
						Arguments: `{"skill": "test-skill"}`,
					},
				},
			},
		},
		{
			Role:       schema.Tool,
			ToolCallID: "call_1",
			Content:    "skill content 1",
		},
	}

	ctx = context.WithValue(ctx, ctxKeySummarizeInput, messages)

	summary := schema.AssistantMessage("test summary", nil)

	result, err := finalizer(ctx, []adk.Message{schema.UserMessage("original")}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	assert.Equal(t, "next called", result[0].Content)
	assert.Len(t, result[0].UserInputMultiContent, 2)
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "test-skill")
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "skill content 1")
	assert.Equal(t, "test summary", result[0].UserInputMultiContent[1].Text)

	assert.Equal(t, "next msg", result[1].Content)
}

func TestBuildPreservedSkillsText(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()

	t.Run("nil config sets default", func(t *testing.T) {
		text, err := buildPreservedSkillsText(ctx, nil, nil)
		assert.NoError(t, err)
		assert.Empty(t, text)
	})

	t.Run("empty config", func(t *testing.T) {
		text, err := buildPreservedSkillsText(ctx, nil, &PreserveSkillsConfig{MaxSkills: ptr(0)})
		assert.NoError(t, err)
		assert.Empty(t, text)
	})

	t.Run("no matching skills", func(t *testing.T) {
		text, err := buildPreservedSkillsText(ctx, []adk.Message{
			schema.UserMessage("hi"),
		}, &PreserveSkillsConfig{
			MaxSkills:     ptr(5),
			SkillToolName: "load_skill",
		})
		assert.NoError(t, err)
		assert.Empty(t, text)
	})

	t.Run("with default skill tool name", func(t *testing.T) {
		messages := []adk.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{
						ID: "call_1",
						Function: schema.FunctionCall{
							Name:      "skill",
							Arguments: `{"skill": "test-skill"}`,
						},
					},
				},
			},
			{
				Role:       schema.Tool,
				ToolCallID: "call_1",
				Content:    "skill content 1",
			},
		}

		config := &PreserveSkillsConfig{
			MaxSkills: ptr(2),
		}
		_ = config.check() // to set default tool name

		text, err := buildPreservedSkillsText(ctx, messages, config)
		assert.NoError(t, err)
		assert.Contains(t, text, "test-skill")
		assert.Contains(t, text, "skill content 1")
	})

	t.Run("parse error", func(t *testing.T) {
		messages := []adk.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{
						ID: "call_1",
						Function: schema.FunctionCall{
							Name:      "load_skill",
							Arguments: `invalid json`,
						},
					},
				},
			},
			{
				Role:       schema.Tool,
				ToolCallID: "call_1",
				Content:    "content",
			},
		}

		_, err := buildPreservedSkillsText(ctx, messages, &PreserveSkillsConfig{
			MaxSkills:     ptr(2),
			SkillToolName: "load_skill",
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to parse skill arguments")
	})

	t.Run("max skills truncation and deduplication", func(t *testing.T) {
		messages := []adk.Message{
			{
				Role: schema.Assistant,
				ToolCalls: []schema.ToolCall{
					{ID: "call_1", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"skill": "skill1"}`}},
					{ID: "call_2", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"skill": "skill2"}`}},
					{ID: "call_3", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"skill": "skill1"}`}}, // duplicate
					{ID: "call_4", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"skill": "skill3"}`}},
				},
			},
			{Role: schema.Tool, ToolCallID: "call_1", Content: "c1"},
			{Role: schema.Tool, ToolCallID: "call_2", Content: "c2"},
			{Role: schema.Tool, ToolCallID: "call_3", Content: "c3"}, // latest skill1
			{Role: schema.Tool, ToolCallID: "call_4", Content: "c4"},
		}

		text, err := buildPreservedSkillsText(ctx, messages, &PreserveSkillsConfig{
			MaxSkills:     ptr(2),
			SkillToolName: "load_skill",
		})
		assert.NoError(t, err)
		// Should keep skill1 (latest, c3) and skill3 (c4) because max is 2
		assert.Contains(t, text, "skill1")
		assert.Contains(t, text, "c3")
		assert.Contains(t, text, "skill3")
		assert.Contains(t, text, "c4")
		assert.NotContains(t, text, "c1")
		assert.NotContains(t, text, "skill2")
		assert.NotContains(t, text, "c2")
	})
}
