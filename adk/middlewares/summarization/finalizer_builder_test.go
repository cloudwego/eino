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
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

func TestNewFinalizer(t *testing.T) {
	b := NewFinalizer()
	assert.NotNil(t, b)
	assert.Empty(t, b.handlers)
	assert.Nil(t, b.custom)
}

func TestBuildEmpty(t *testing.T) {
	finalizer, err := NewFinalizer().Build()
	assert.Error(t, err)
	assert.Nil(t, finalizer)
	assert.Contains(t, err.Error(), "at least one handler or custom finalizer is required")
}

func TestBuildConfigError(t *testing.T) {
	ptr := func(i int) *int { return &i }

	t.Run("nil config", func(t *testing.T) {
		finalizer, err := NewFinalizer().
			PreserveSkills(nil).
			Build()
		assert.Error(t, err)
		assert.Nil(t, finalizer)
		assert.Contains(t, err.Error(), "PreserveSkills:")
		assert.Contains(t, err.Error(), "PreserveSkillsConfig is required")
	})

	t.Run("negative max skills", func(t *testing.T) {
		finalizer, err := NewFinalizer().
			PreserveSkills(&PreserveSkillsConfig{MaxSkills: ptr(-1)}).
			Build()
		assert.Error(t, err)
		assert.Nil(t, finalizer)
		assert.Contains(t, err.Error(), "PreserveSkills:")
		assert.Contains(t, err.Error(), "MaxSkills must be non-negative")
	})
}

func TestBuildWithCustomOnly(t *testing.T) {
	ctx := context.Background()

	finalizer, err := NewFinalizer().
		Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			return []adk.Message{
				schema.SystemMessage("system prompt"),
				summary,
			}, nil
		}).
		Build()
	assert.NoError(t, err)

	summary := schema.UserMessage("test summary")
	result, err := finalizer(ctx, []adk.Message{}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, schema.System, result[0].Role)
	assert.Equal(t, "system prompt", result[0].Content)
	assert.Equal(t, "test summary", result[1].Content)
}

func TestBuildCustomOverrides(t *testing.T) {
	ctx := context.Background()

	finalizer, err := NewFinalizer().
		Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			return []adk.Message{schema.UserMessage("first")}, nil
		}).
		Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			return []adk.Message{schema.UserMessage("second")}, nil
		}).
		Build()
	assert.NoError(t, err)

	summary := schema.UserMessage("test")
	result, err := finalizer(ctx, []adk.Message{}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "second", result[0].Content)
}

func TestBuildStepChaining(t *testing.T) {
	ctx := context.Background()

	b := NewFinalizer()
	b.handlers = append(b.handlers, func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		summary.Content = summary.Content + " | step1"
		return []adk.Message{summary}, nil
	})
	b.handlers = append(b.handlers, func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		summary.Content = summary.Content + " | step2"
		return []adk.Message{summary}, nil
	})

	finalizer, err := b.Build()
	assert.NoError(t, err)

	summary := schema.UserMessage("start")
	result, err := finalizer(ctx, []adk.Message{}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 1)
	assert.Equal(t, "start | step1 | step2", result[0].Content)
}

func TestBuildStepChainingWithCustom(t *testing.T) {
	ctx := context.Background()

	b := NewFinalizer()
	b.handlers = append(b.handlers, func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		summary.Content = summary.Content + " | step1"
		return []adk.Message{summary}, nil
	})
	b.Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		return []adk.Message{
			schema.SystemMessage("sys"),
			summary,
		}, nil
	})

	finalizer, err := b.Build()
	assert.NoError(t, err)

	summary := schema.UserMessage("start")
	result, err := finalizer(ctx, []adk.Message{}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 2)
	assert.Equal(t, schema.System, result[0].Role)
	assert.Equal(t, "start | step1", result[1].Content)
}

func TestBuildStepError(t *testing.T) {
	ctx := context.Background()

	b := NewFinalizer()
	b.handlers = append(b.handlers, func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		return nil, errors.New("step failed")
	})

	finalizer, err := b.Build()
	assert.NoError(t, err)

	summary := schema.UserMessage("test")
	_, err = finalizer(ctx, []adk.Message{}, summary)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "step failed")
}

func TestPreserveSkillsConfigCheck(t *testing.T) {
	ptr := func(i int) *int { return &i }

	t.Run("nil config", func(t *testing.T) {
		var c *PreserveSkillsConfig
		err := c.check()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "PreserveSkillsConfig is required")
	})

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

	t.Run("nil max skills", func(t *testing.T) {
		c := &PreserveSkillsConfig{}
		err := c.check()
		assert.NoError(t, err)
	})
}

func TestPreserveSkillsViaBuilder(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()

	finalizer, err := NewFinalizer().
		PreserveSkills(&PreserveSkillsConfig{
			MaxSkills:     ptr(2),
			SkillToolName: "load_skill",
		}).
		Build()
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

	ctx = context.WithValue(ctx, ctxKeyModelInput{}, messages)

	summary := &schema.Message{
		Role: schema.Assistant,
		UserInputMultiContent: []schema.MessageInputPart{
			{Type: schema.ChatMessagePartTypeText, Text: "test summary"},
		},
	}

	result, err := finalizer(ctx, []adk.Message{schema.UserMessage("original")}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 1)

	assert.Empty(t, result[0].Content)
	assert.Len(t, result[0].UserInputMultiContent, 2)
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "test-skill")
	assert.Contains(t, result[0].UserInputMultiContent[0].Text, "skill content 1")
	assert.Equal(t, "test summary", result[0].UserInputMultiContent[1].Text)
}

func TestPreserveSkillsWithCustom(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()

	finalizer, err := NewFinalizer().
		PreserveSkills(&PreserveSkillsConfig{
			MaxSkills:     ptr(2),
			SkillToolName: "load_skill",
		}).
		Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
			return []adk.Message{
				schema.SystemMessage("system prompt"),
				summary,
			}, nil
		}).
		Build()
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

	ctx = context.WithValue(ctx, ctxKeyModelInput{}, messages)
	summary := schema.AssistantMessage("test summary", nil)

	result, err := finalizer(ctx, []adk.Message{schema.UserMessage("original")}, summary)
	assert.NoError(t, err)
	assert.Len(t, result, 2)

	assert.Equal(t, schema.System, result[0].Role)
	assert.Equal(t, "system prompt", result[0].Content)

	assert.Contains(t, result[1].UserInputMultiContent[0].Text, "test-skill")
}

func TestBuildPreservedSkillsText(t *testing.T) {
	ptr := func(i int) *int { return &i }
	ctx := context.Background()

	t.Run("nil config", func(t *testing.T) {
		text, err := buildPreservedSkillsText(ctx, nil, nil)
		assert.NoError(t, err)
		assert.Empty(t, text)
	})

	t.Run("zero max skills", func(t *testing.T) {
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
					{ID: "call_3", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"skill": "skill1"}`}},
					{ID: "call_4", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"skill": "skill3"}`}},
				},
			},
			{Role: schema.Tool, ToolCallID: "call_1", Content: "c1"},
			{Role: schema.Tool, ToolCallID: "call_2", Content: "c2"},
			{Role: schema.Tool, ToolCallID: "call_3", Content: "c3"},
			{Role: schema.Tool, ToolCallID: "call_4", Content: "c4"},
		}

		text, err := buildPreservedSkillsText(ctx, messages, &PreserveSkillsConfig{
			MaxSkills:     ptr(2),
			SkillToolName: "load_skill",
		})
		assert.NoError(t, err)
		assert.Contains(t, text, "skill1")
		assert.Contains(t, text, "c3")
		assert.Contains(t, text, "skill3")
		assert.Contains(t, text, "c4")
		assert.NotContains(t, text, "c1")
		assert.NotContains(t, text, "skill2")
		assert.NotContains(t, text, "c2")
	})
}
