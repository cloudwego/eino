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

// DefaultFinalize is the default TypedFinalizeFunc implementation, providing the same
// summary post-processing as the middleware does internally:
//  1. Replaces the <all_user_messages>...</all_user_messages> section in the model-generated
//     summary with recent original user messages from the conversation (up to ~30k tokens).
//  2. Adds a preamble and a postamble around the summary content.
//  3. Converts the summary into a user message, prepended with the original system messages.
func DefaultFinalize[M adk.MessageType](ctx context.Context, originalMessages []M, summary M) ([]M, error) {
	systemMsgs, contextMsgs := splitSystemAndContextMsgs(originalMessages)

	processed, err := postProcessSummary(ctx, &postProcessSummaryParams[M]{
		contextMsgs:    contextMsgs,
		summaryContent: getAssistantTextContent(summary),
	})
	if err != nil {
		return nil, err
	}

	result := make([]M, 0, len(systemMsgs)+1)
	result = append(result, systemMsgs...)
	result = append(result, processed)
	return result, nil
}

// TypedFinalizerBuilder builds a TypedFinalizeFunc by chaining handlers,
// generic over message type M.
type TypedFinalizerBuilder[M adk.MessageType] struct {
	handlers []TypedFinalizeFunc[M]
	errs     []error
}

// FinalizerBuilder is a backward-compatible alias for TypedFinalizerBuilder
// specialized with *schema.Message.
type FinalizerBuilder = TypedFinalizerBuilder[*schema.Message]

// NewTypedFinalizer creates a new TypedFinalizerBuilder.
//
// Handlers run in registration order, and the summary message is post-processed
// as DefaultFinalize does after all handlers have run. For example, with
// PreserveSkills and a system message in originalMessages, the final output is:
//
//	[system message, preserved skill message, processed summary]
//
// Example:
//
//	finalizer, err := NewTypedFinalizer[*schema.Message]().
//	    PreserveSkills(&PreserveSkillsConfig{}).
//	    Build()
//
//	cfg := &Config{
//	    Finalize: finalizer,
//	    // ...
//	}
func NewTypedFinalizer[M adk.MessageType]() *TypedFinalizerBuilder[M] {
	return &TypedFinalizerBuilder[M]{}
}

// NewFinalizer creates a new FinalizerBuilder that builds a FinalizeFunc
// by chaining handlers.
func NewFinalizer() *FinalizerBuilder {
	return &FinalizerBuilder{}
}

// Build constructs the final TypedFinalizeFunc by chaining all registered handlers
// and post-processing the summary message as DefaultFinalize does.
// For example, with PreserveSkills and a system message in
// originalMessages, the final output is:
//
//	[system message, preserved skill message, processed summary]
func (b *TypedFinalizerBuilder[M]) Build() (TypedFinalizeFunc[M], error) {
	if len(b.errs) > 0 {
		msgs := make([]string, len(b.errs))
		for i, e := range b.errs {
			msgs[i] = e.Error()
		}
		return nil, fmt.Errorf("failed to build finalizer:\n%s", strings.Join(msgs, "\n"))
	}

	if len(b.handlers) == 0 {
		return nil, fmt.Errorf("at least one handler is required")
	}

	handlers := make([]TypedFinalizeFunc[M], len(b.handlers))
	copy(handlers, b.handlers)

	return func(ctx context.Context, originalMessages []M, summary M) ([]M, error) {
		var extraMessages []M
		for _, fn := range handlers {
			result, err := fn(ctx, originalMessages, summary)
			if err != nil {
				return nil, err
			}
			if len(result) == 0 {
				return nil, fmt.Errorf("finalizer handler returned no messages")
			}
			extraMessages = append(extraMessages, result[:len(result)-1]...)
			summary = result[len(result)-1]
		}

		systemMsgs, contextMsgs := splitSystemAndContextMsgs(originalMessages)

		processed, err := postProcessSummary(ctx, &postProcessSummaryParams[M]{
			contextMsgs:    contextMsgs,
			summaryContent: getAssistantTextContent(summary),
		})
		if err != nil {
			return nil, err
		}

		result := make([]M, 0, len(systemMsgs)+len(extraMessages)+1)
		result = append(result, systemMsgs...)
		result = append(result, extraMessages...)
		result = append(result, processed)
		return result, nil
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

// PreserveSkills preserves skill contents loaded by the ADK skill middleware.
// It scans the conversation for matching skill tool calls and returns the preserved
// skill content as a user message before the summary.
//
// Example:
//
//	messages: [assistant(tool_call: skill "foo"), tool(content: "bar")]
//	summary:  S
//
// When skill content is found, PreserveSkills returns:
//
//	[]M{user("<preserved foo: bar>"), S}
func (b *TypedFinalizerBuilder[M]) PreserveSkills(config *PreserveSkillsConfig) *TypedFinalizerBuilder[M] {
	if err := config.check(); err != nil {
		b.errs = append(b.errs, fmt.Errorf("PreserveSkills: %w", err))
		return b
	}
	b.handlers = append(b.handlers, func(ctx context.Context, originalMessages []M, summary M) ([]M, error) {
		messages := originalMessages

		modelInput, ok := ctx.Value(ctxKeyModelInput{}).([]M)
		if ok && len(modelInput) > 0 {
			messages = modelInput
		}

		if len(messages) == 0 {
			return []M{summary}, nil
		}

		skillText, err := buildPreservedSkillsText(ctx, messages, config)
		if err != nil {
			return nil, err
		}

		if skillText == "" {
			return []M{summary}, nil
		}

		preserved := makeUserMsg[M](skillText)
		setMsgExtra(preserved, extraKeyContentType, string(contentTypeSkills))
		return []M{preserved, summary}, nil
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

func extractSkillInfos[M adk.MessageType](messages []M, skillTool string) ([]*skillInfo, error) {
	var skills []*skillInfo
	argsMap := make(map[string]string)

	for _, msg := range messages {
		switch m := any(msg).(type) {
		case *schema.Message:
			if m.Role == schema.Assistant {
				for _, tc := range m.ToolCalls {
					if tc.Function.Name == skillTool {
						argsMap[tc.ID] = tc.Function.Arguments
					}
				}
			} else if m.Role == schema.Tool {
				arguments, ok := argsMap[m.ToolCallID]
				if !ok {
					continue
				}
				var arg struct {
					Skill string `json:"skill"`
				}
				if err := sonic.UnmarshalString(arguments, &arg); err != nil {
					return nil, fmt.Errorf("failed to parse skill arguments from tool call %s: %w", m.ToolCallID, err)
				}
				skills = append(skills, &skillInfo{
					Name:    arg.Skill,
					Content: messageUserTextContent(m),
				})
			}

		case *schema.AgenticMessage:
			for _, block := range m.ContentBlocks {
				if block == nil {
					continue
				}
				if block.Type == schema.ContentBlockTypeFunctionToolCall && block.FunctionToolCall != nil {
					if block.FunctionToolCall.Name == skillTool {
						argsMap[block.FunctionToolCall.CallID] = block.FunctionToolCall.Arguments
					}
				}
				if block.Type == schema.ContentBlockTypeFunctionToolResult && block.FunctionToolResult != nil {
					arguments, ok := argsMap[block.FunctionToolResult.CallID]
					if !ok {
						continue
					}
					var arg struct {
						Skill string `json:"skill"`
					}
					if err := sonic.UnmarshalString(arguments, &arg); err != nil {
						return nil, fmt.Errorf("failed to parse skill arguments from tool call %s: %w", block.FunctionToolResult.CallID, err)
					}
					var contentParts []string
					for _, cb := range block.FunctionToolResult.Content {
						if cb != nil && cb.Type == schema.FunctionToolResultContentBlockTypeText && cb.Text != nil {
							contentParts = append(contentParts, cb.Text.Text)
						}
					}
					skills = append(skills, &skillInfo{
						Name:    arg.Skill,
						Content: strings.Join(contentParts, "\n"),
					})
				}
			}
		}
	}

	return skills, nil
}

func buildPreservedSkillsText[M adk.MessageType](_ context.Context, messages []M, config *PreserveSkillsConfig) (string, error) {
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

	skills, err := extractSkillInfos(messages, skillTool)
	if err != nil {
		return "", err
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
		tokens := estimateTokenCount(len(skill.Content))

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

	if estimateTokenCount(len(content)) <= maxTokens {
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
