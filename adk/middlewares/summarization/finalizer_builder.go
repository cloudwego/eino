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
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type FinalizerBuilder struct {
	handlers []FinalizeFunc
	custom   FinalizeFunc
	errs     []error
}

// NewFinalizer creates a new FinalizerBuilder that builds a FinalizeFunc
// by chaining handlers and an optional custom finalizer.
// Handlers (e.g. PreserveSkills) transform the summary message sequentially,
// and the custom finalizer (set via Custom) determines the final output messages.
//
// Example:
//
//	finalizer, err := NewFinalizer().
//		PreserveSkills(&PreserveSkillsConfig{}).
//		Custom(func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
//			return []adk.Message{schema.SystemMessage("system prompt"), summary}, nil
//		}).
//		Build()
//
//	cfg := &Config{
//		Finalize: finalizer,
//		// ...
//	}
func NewFinalizer() *FinalizerBuilder {
	return &FinalizerBuilder{}
}

// Custom sets a custom finalizer that determines the final output messages.
// If called multiple times, the last custom finalizer takes effect.
func (b *FinalizerBuilder) Custom(fn FinalizeFunc) *FinalizerBuilder {
	b.custom = fn
	return b
}

func (b *FinalizerBuilder) Build() (FinalizeFunc, error) {
	if len(b.errs) > 0 {
		msgs := make([]string, len(b.errs))
		for i, e := range b.errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("failed to build finalizer:\n%s", strings.Join(msgs, "\n"))
	}

	if len(b.handlers) == 0 && b.custom == nil {
		return nil, fmt.Errorf("at least one handler or custom finalizer is required")
	}

	handlers := make([]FinalizeFunc, len(b.handlers))
	copy(handlers, b.handlers)
	custom := b.custom

	return func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		for _, fn := range handlers {
			result, err := fn(ctx, originalMessages, summary)
			if err != nil {
				return nil, err
			}
			summary = result[0]
		}

		if custom != nil {
			return custom(ctx, originalMessages, summary)
		}

		return []adk.Message{summary}, nil
	}, nil
}

type PreserveSkillsConfig struct {
	// SkillToolName is the tool name used for loading skills.
	// Must match the tool name configured in the ADK skill middleware.
	// Optional. Defaults to "skill".
	SkillToolName string

	// MaxSkills limits the maximum number of skills to preserve.
	// = 0 means do not preserve any skills (disabled).
	// > 0 means preserve up to this many most recent skills.
	// Optional. Defaults to 5.
	MaxSkills *int

	// MaxTokensPerSkill limits the maximum token count for a single preserved skill.
	// Skills exceeding this limit are truncated, with the truncated portion replaced
	// by a short marker text (e.g. "[... skill content truncated ...]").
	// Note: if this value is set smaller than the token count of the marker text itself,
	// the skill will contain only the marker text with no original content preserved.
	// Optional. Defaults to 5000.
	MaxTokensPerSkill *int

	// SkillsTokenBudget limits the total token count for all preserved skills combined.
	// Skills are preserved from most recent to oldest; once the budget is exhausted,
	// remaining skills are dropped.
	// Optional. Defaults to 25000.
	SkillsTokenBudget *int
}

// PreserveSkills extracts skill contents loaded by the ADK skill middleware
// from the conversation history and prepends them to the summary message,
// ensuring the agent retains skill knowledge after the context window is compacted.
func (b *FinalizerBuilder) PreserveSkills(config *PreserveSkillsConfig) *FinalizerBuilder {
	if err := config.check(); err != nil {
		b.errs = append(b.errs, fmt.Errorf("PreserveSkills: %w", err))
		return b
	}
	b.handlers = append(b.handlers, func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error) {
		modelInput, _ := ctx.Value(ctxKeyModelInput{}).([]adk.Message)
		if len(modelInput) == 0 {
			panic("impossible: model input is empty")
		}

		skillText, err := buildPreservedSkillsText(ctx, modelInput, config)
		if err != nil {
			return nil, err
		}

		if skillText != "" {
			summary.UserInputMultiContent = append([]schema.MessageInputPart{
				{
					Type: schema.ChatMessagePartTypeText,
					Text: skillText,
				},
			}, summary.UserInputMultiContent...)
		}

		return []adk.Message{summary}, nil
	})
	return b
}

func (c *PreserveSkillsConfig) check() error {
	if c == nil {
		return fmt.Errorf("PreserveSkillsConfig is required")
	}
	if c.MaxSkills != nil && *c.MaxSkills < 0 {
		return fmt.Errorf("MaxSkills must be non-negative")
	}
	if c.MaxTokensPerSkill != nil && *c.MaxTokensPerSkill < 0 {
		return fmt.Errorf("MaxTokensPerSkill must be non-negative")
	}
	if c.SkillsTokenBudget != nil && *c.SkillsTokenBudget < 0 {
		return fmt.Errorf("SkillsTokenBudget must be non-negative")
	}
	return nil
}

type skillInfo struct {
	Name    string
	Content string
}

func buildPreservedSkillsText(_ context.Context, messages []adk.Message, config *PreserveSkillsConfig) (string, error) {
	const (
		defaultSkillTool         = "skill"
		defaultMaxTokensPerSkill = 5000
		defaultSkillsTokenBudget = 25000
	)

	if config == nil {
		config = &PreserveSkillsConfig{}
	}

	maxSkills := 5
	if config.MaxSkills != nil {
		maxSkills = *config.MaxSkills
	}
	if maxSkills <= 0 {
		return "", nil
	}

	skillTool := defaultSkillTool
	if config.SkillToolName != "" {
		skillTool = config.SkillToolName
	}

	maxTokensPerSkill := defaultMaxTokensPerSkill
	if config.MaxTokensPerSkill != nil {
		maxTokensPerSkill = *config.MaxTokensPerSkill
	}

	skillsTokenBudget := defaultSkillsTokenBudget
	if config.SkillsTokenBudget != nil {
		skillsTokenBudget = *config.SkillsTokenBudget
	}

	var skills []*skillInfo
	argsMap := make(map[string]string)

	for _, msg := range messages {
		switch msg.Role {
		case schema.Assistant:
			for _, tc := range msg.ToolCalls {
				if tc.Function.Name == skillTool {
					argsMap[tc.ID] = tc.Function.Arguments
				}
			}

		case schema.Tool:
			arguments, ok := argsMap[msg.ToolCallID]
			if !ok {
				continue
			}

			var arg struct {
				Skill string `json:"skill"`
			}
			if err := sonic.UnmarshalString(arguments, &arg); err != nil {
				return "", fmt.Errorf("failed to parse skill arguments from tool call %s: %w", msg.ToolCallID, err)
			}

			skills = append(skills, &skillInfo{
				Name:    arg.Skill,
				Content: msg.Content,
			})
		}
	}

	if len(skills) == 0 {
		return "", nil
	}

	uniqueSkills := make([]*skillInfo, 0, len(skills))
	seenNames := make(map[string]bool)
	for i := len(skills) - 1; i >= 0; i-- {
		skill := skills[i]
		if !seenNames[skill.Name] {
			seenNames[skill.Name] = true
			uniqueSkills = append(uniqueSkills, skill)
		}
	}

	for i, j := 0, len(uniqueSkills)-1; i < j; i, j = i+1, j-1 {
		uniqueSkills[i], uniqueSkills[j] = uniqueSkills[j], uniqueSkills[i]
	}
	skills = uniqueSkills

	if len(skills) > maxSkills {
		skills = skills[len(skills)-maxSkills:]
	}

	totalTokens := 0
	var budgetedSkills []*skillInfo
	for i := len(skills) - 1; i >= 0; i-- {
		skill := skills[i]
		tokens := estimateTokenCount(skill.Content)

		if tokens > maxTokensPerSkill {
			skill = &skillInfo{
				Name:    skill.Name,
				Content: truncateSkillContent(skill.Content, maxTokensPerSkill),
			}
			tokens = maxTokensPerSkill
		}

		if totalTokens+tokens > skillsTokenBudget {
			break
		}

		totalTokens += tokens
		budgetedSkills = append(budgetedSkills, skill)
	}

	if len(budgetedSkills) == 0 {
		return "", nil
	}

	// Reverse to restore chronological order.
	for i, j := 0, len(budgetedSkills)-1; i < j; i, j = i+1, j-1 {
		budgetedSkills[i], budgetedSkills[j] = budgetedSkills[j], budgetedSkills[i]
	}

	var parts []string
	for _, skill := range budgetedSkills {
		parts = append(parts, fmt.Sprintf(skillSectionFormat, skill.Name, skill.Content))
	}

	skillsText := strings.Join(parts, "\n\n---\n\n")
	skillsText = fmt.Sprintf(getSkillPreamble(), skillsText)
	skillsText = fmt.Sprintf("<system-reminder>\n%s"+"\n</system-reminder>", skillsText)

	return skillsText, nil
}

// truncateSkillContent truncates skill content to fit within maxTokens.
// It keeps the first portion of the content and appends a truncation marker
// (e.g. "[... skill content truncated ...]") to indicate the omission.
// If maxTokens is smaller than the marker itself, only the marker is returned.
func truncateSkillContent(content string, maxTokens int) string {
	if len(content) == 0 {
		return content
	}

	if estimateTokenCount(content) <= maxTokens {
		return content
	}

	marker := getSkillTruncationMarker()
	targetBytes := estimateTokenBytes(maxTokens) - len(marker)
	if targetBytes < 0 {
		targetBytes = 0
	}
	if targetBytes > len(content) {
		targetBytes = len(content)
	}

	// Back up to a valid UTF-8 rune boundary.
	for targetBytes > 0 && targetBytes < len(content) && !utf8.RuneStart(content[targetBytes]) {
		targetBytes--
	}

	return content[:targetBytes] + marker
}
