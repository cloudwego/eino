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

// Package summarization provides a middleware that automatically summarizes
// conversation history when token count exceeds the configured threshold.
package summarization

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type TokenCounterFunc func(ctx context.Context, input *TokenCounterInput) (int, error)
type PrepareFunc func(ctx context.Context, originalMessages []adk.Message) ([]adk.Message, error)
type FinalizeFunc func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error)
type CallbackFunc func(ctx context.Context, before, after adk.ChatModelAgentState) error

// Config defines the configuration for the summarization middleware.
type Config struct {
	// Model is the chat model used to generate summaries.
	Model model.BaseChatModel

	// ModelOptions specifies options passed to the model when generating summaries.
	// Optional.
	ModelOptions []model.Option

	// TokenCounter calculates the token count for a message.
	// Optional. Defaults to a simple estimator (~4 chars/token).
	TokenCounter TokenCounterFunc

	// Trigger specifies the conditions that activate summarization.
	// Optional. Defaults to triggering when total tokens exceed 190k.
	Trigger *TriggerCondition

	// EmitInternalEvents indicates whether internal events should be emitted during summarization,
	// allowing external observers to track the summarization process.
	//
	// Event Scoping:
	//   - ActionTypeBeforeSummary: emitted before calling model to generate summary
	//   - ActionTypeAfterSummary: emitted after summary generation completes
	// Optional. Defaults to false.
	EmitInternalEvents bool

	// Instruction overrides the default summarization instruction.
	// Optional.
	Instruction string

	// TranscriptFilePath is the path to the file containing the full conversation history.
	// It is appended to the summary to remind the model where to read the original context.
	// Optional but strongly recommended.
	TranscriptFilePath string

	// Prepare is called before summary generation. The returned messages are the context to be summarized.
	// Optional.
	Prepare PrepareFunc

	// Finalize is called after summary generation. The returned messages are used as the final output.
	// Optional.
	Finalize FinalizeFunc

	// Callback is called after Finalize, before exiting the middleware.
	// Read-only, do not modify state.
	// Optional.
	Callback CallbackFunc

	// PreserveUserMessages controls whether to preserve original user messages in the summary.
	// When enabled, replaces the <all_user_messages> section in the model-generated summary
	// with recent original user messages from the conversation.
	// When disabled, the model-generated content is kept unchanged.
	// Optional. Enabled by default.
	PreserveUserMessages *PreserveUserMessages
}

type TokenCounterInput struct {
	Messages []adk.Message
	Tools    []*schema.ToolInfo
}

// TriggerCondition specifies when summarization should be activated.
type TriggerCondition struct {
	// ContextTokens triggers summarization when total token count exceeds this threshold.
	ContextTokens int
}

// PreserveUserMessages controls whether to preserve original user messages in the summary.
type PreserveUserMessages struct {
	Enabled bool

	// MaxTokens limits the maximum token count for preserved user messages.
	// When set, only the most recent user messages within this limit are preserved.
	// Optional. Defaults to 1/3 of TriggerCondition.ContextTokens if not specified.
	MaxTokens int
}

// New creates a summarization middleware that automatically summarizes conversation history
// when trigger conditions are met.
func New(ctx context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	return &middleware{
		cfg:                          cfg,
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
	}, nil
}

type middleware struct {
	*adk.BaseChatModelAgentMiddleware
	cfg *Config
}

func (m *middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState,
	mtx *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {

	var tools []*schema.ToolInfo
	if mtx != nil {
		tools = mtx.Tools
	}

	triggered, err := m.shouldSummarize(ctx, &TokenCounterInput{
		Messages: state.Messages,
		Tools:    tools,
	})
	if err != nil {
		return nil, nil, err
	}
	if !triggered {
		return ctx, state, nil
	}

	beforeState := *state

	if m.cfg.EmitInternalEvents {
		err = m.emitEvent(ctx, &CustomizedAction{
			Type:   ActionTypeBeforeSummary,
			Before: &BeforeSummaryAction{Messages: state.Messages},
		})
		if err != nil {
			return nil, nil, err
		}
	}

	var (
		systemMsgs     []adk.Message
		summarizeInput []adk.Message
	)

	for _, msg := range state.Messages {
		if msg.Role == schema.System {
			systemMsgs = append(systemMsgs, msg)
		} else {
			summarizeInput = append(summarizeInput, msg)
		}
	}

	if m.cfg.Prepare != nil {
		summarizeInput, err = m.cfg.Prepare(ctx, state.Messages)
		if err != nil {
			return nil, nil, err
		}
	}

	summary, err := m.summarize(ctx, summarizeInput)
	if err != nil {
		return nil, nil, err
	}

	summary, err = m.postProcessSummary(ctx, summarizeInput, summary)
	if err != nil {
		return nil, nil, err
	}

	if m.cfg.Finalize != nil {
		state.Messages, err = m.cfg.Finalize(ctx, state.Messages, summary)
		if err != nil {
			return nil, nil, err
		}
	} else {
		state.Messages = append(systemMsgs, summary)
	}

	if m.cfg.Callback != nil {
		err = m.cfg.Callback(ctx, beforeState, *state)
		if err != nil {
			return nil, nil, err
		}
	}

	if m.cfg.EmitInternalEvents {
		err = m.emitEvent(ctx, &CustomizedAction{
			Type:  ActionTypeAfterSummary,
			After: &AfterSummaryAction{Messages: state.Messages},
		})
		if err != nil {
			return nil, nil, err
		}
	}

	return ctx, state, nil
}

func (m *middleware) shouldSummarize(ctx context.Context, input *TokenCounterInput) (bool, error) {
	tokens, err := m.countTokens(ctx, input)
	if err != nil {
		return false, fmt.Errorf("failed to count tokens: %w", err)
	}
	return tokens > m.getTriggerContextTokens(), nil
}

func (m *middleware) getTriggerContextTokens() int {
	const defaultTriggerContextTokens = 190000
	if m.cfg.Trigger != nil {
		return m.cfg.Trigger.ContextTokens
	}
	return defaultTriggerContextTokens
}

func (m *middleware) getUserMessageContextTokens() int {
	if m.cfg.PreserveUserMessages != nil && m.cfg.PreserveUserMessages.MaxTokens > 0 {
		return m.cfg.PreserveUserMessages.MaxTokens
	}
	return m.getTriggerContextTokens() / 3
}

func (m *middleware) emitEvent(ctx context.Context, action *CustomizedAction) error {
	err := adk.SendEvent(ctx, &adk.AgentEvent{
		Action: &adk.AgentAction{
			CustomizedAction: action,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send internal event: %w", err)
	}
	return nil
}

func (m *middleware) countTokens(ctx context.Context, input *TokenCounterInput) (int, error) {
	if m.cfg.TokenCounter != nil {
		return m.cfg.TokenCounter(ctx, input)
	}
	return defaultTokenCounter(ctx, input)
}

func defaultTokenCounter(ctx context.Context, input *TokenCounterInput) (int, error) {
	var totalTokens int
	for _, msg := range input.Messages {
		text := extractTextContent(msg)
		totalTokens += estimateTokenCount(text)
	}

	for _, tl := range input.Tools {
		tl_ := *tl
		tl_.Extra = nil
		text, err := sonic.MarshalString(tl_)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal tool info: %w", err)
		}

		totalTokens += estimateTokenCount(text)
	}

	return totalTokens, nil
}

func estimateTokenCount(text string) int {
	return (len(text) + 3) / 4
}

func (m *middleware) summarize(ctx context.Context, msgs []adk.Message) (adk.Message, error) {
	instruction := m.cfg.Instruction
	if instruction == "" {
		instruction = getSummaryInstruction()
	}

	input := make([]adk.Message, 0, len(msgs)+2)
	input = append(input, &schema.Message{
		Role:    schema.System,
		Content: getSystemPrompt(),
	})
	input = append(input, msgs...)
	input = append(input, &schema.Message{
		Role:    schema.User,
		Content: instruction,
	})

	resp, err := m.cfg.Model.Generate(ctx, input, m.cfg.ModelOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	summary := &schema.Message{
		Role:    schema.User,
		Content: resp.Content,
	}

	setContentType(summary, contentTypeSummary)

	return summary, nil
}

func (m *middleware) postProcessSummary(ctx context.Context, messages []adk.Message, summary adk.Message) (adk.Message, error) {
	if m.cfg.PreserveUserMessages == nil || m.cfg.PreserveUserMessages.Enabled {
		maxUserMsgTokens := m.getUserMessageContextTokens()
		content, err := m.replaceUserMessagesInSummary(ctx, messages, summary.Content, maxUserMsgTokens)
		if err != nil {
			return nil, fmt.Errorf("failed to replace user messages in summary: %w", err)
		}
		summary.Content = content
	}

	if path := m.cfg.TranscriptFilePath; path != "" {
		summary.Content = appendSection(summary.Content, fmt.Sprintf(getTranscriptPathInstruction(), path))
	}

	summary.Content = appendSection(getSummaryPreamble(), summary.Content)

	summary.UserInputMultiContent = []schema.MessageInputPart{
		{
			Type: schema.ChatMessagePartTypeText,
			Text: summary.Content,
		},
		{
			Type: schema.ChatMessagePartTypeText,
			Text: getContinueInstruction(),
		},
	}

	summary.Content = ""

	return summary, nil
}

func (m *middleware) replaceUserMessagesInSummary(ctx context.Context, messages []adk.Message, summary string, contextTokens int) (string, error) {
	var userMsgs []adk.Message
	for _, msg := range messages {
		if typ, ok := getContentType(msg); ok && typ == contentTypeSummary {
			continue
		}
		if msg.Role == schema.User {
			userMsgs = append(userMsgs, msg)
		}
	}

	if len(userMsgs) == 0 {
		return summary, nil
	}

	var selected []adk.Message
	if len(userMsgs) == 1 {
		selected = userMsgs
	} else {
		var totalTokens int
		for i := len(userMsgs) - 1; i >= 0; i-- {
			msg := userMsgs[i]

			tokens, err := m.countTokens(ctx, &TokenCounterInput{
				Messages: []adk.Message{msg},
			})
			if err != nil {
				return "", fmt.Errorf("failed to count tokens: %w", err)
			}

			remaining := contextTokens - totalTokens
			if tokens <= remaining {
				totalTokens += tokens
				selected = append(selected, msg)
				continue
			}

			trimmedMsg := defaultTrimUserMessage(msg, remaining)
			if trimmedMsg != nil {
				selected = append(selected, trimmedMsg)
			}

			break
		}

		for i, j := 0, len(selected)-1; i < j; i, j = i+1, j-1 {
			selected[i], selected[j] = selected[j], selected[i]
		}
	}

	var msgLines []string
	for _, msg := range selected {
		text := extractTextContent(msg)
		if text != "" {
			msgLines = append(msgLines, "    - "+text)
		}
	}
	userMsgsText := strings.Join(msgLines, "\n")

	if userMsgsText == "" {
		return summary, nil
	}

	lastMatch := findLastMatch(allUserMessagesTagRegex, summary)
	if lastMatch == nil {
		return summary, nil
	}

	var replacement string
	if len(selected) < len(userMsgs) {
		replacement = "<all_user_messages>\n" + getUserMessagesReplacedNote() + "\n" + userMsgsText + "\n</all_user_messages>"
	} else {
		replacement = "<all_user_messages>\n" + userMsgsText + "\n</all_user_messages>"
	}

	content := summary[:lastMatch[0]] + replacement + summary[lastMatch[1]:]

	return content, nil
}

func findLastMatch(re *regexp.Regexp, s string) []int {
	matches := re.FindAllStringIndex(s, -1)
	if len(matches) == 0 {
		return nil
	}
	return matches[len(matches)-1]
}

func appendSection(base, section string) string {
	if base == "" {
		return section
	}
	if section == "" {
		return base
	}
	return base + "\n\n" + section
}

func defaultTrimUserMessage(msg adk.Message, remainingTokens int) adk.Message {
	if remainingTokens <= 0 {
		return nil
	}

	textContent := extractTextContent(msg)
	if len(textContent) == 0 {
		return nil
	}

	trimmed := truncateTextByChars(textContent)
	if trimmed == "" {
		return nil
	}

	return &schema.Message{
		Role:    schema.User,
		Content: trimmed,
	}
}

func truncateTextByChars(text string) string {
	const maxRunes = 2000

	if text == "" {
		return ""
	}

	if utf8.RuneCountInString(text) <= maxRunes {
		return text
	}

	halfRunes := maxRunes / 2
	runes := []rune(text)
	totalRunes := len(runes)

	prefix := string(runes[:halfRunes])
	suffix := string(runes[totalRunes-halfRunes:])
	removedChars := totalRunes - maxRunes

	marker := fmt.Sprintf(getTruncatedMarkerFormat(), removedChars)

	return prefix + marker + suffix
}

func extractTextContent(msg adk.Message) string {
	if msg == nil {
		return ""
	}
	if msg.Content != "" {
		return msg.Content
	}

	var sb strings.Builder
	for _, part := range msg.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
		}
	}

	return sb.String()
}

func (c *Config) check() error {
	if c == nil {
		return fmt.Errorf("config is required")
	}
	if c.Model == nil {
		return fmt.Errorf("model is required")
	}
	if c.Trigger != nil {
		if err := c.Trigger.check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *TriggerCondition) check() error {
	if c.ContextTokens <= 0 {
		return fmt.Errorf("trigger.ContextTokens must be positive")
	}
	return nil
}

func setContentType(msg adk.Message, ct summarizationContentType) {
	setExtra(msg, extraKeyContentType, string(ct))
}

func getContentType(msg adk.Message) (summarizationContentType, bool) {
	ct, ok := getExtra[string](msg, extraKeyContentType)
	if !ok {
		return "", false
	}
	return summarizationContentType(ct), true
}

func setExtra(msg adk.Message, key string, value any) {
	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	msg.Extra[key] = value
}

func getExtra[T any](msg adk.Message, key string) (T, bool) {
	var zero T
	if msg == nil || msg.Extra == nil {
		return zero, false
	}
	v, ok := msg.Extra[key].(T)
	if !ok {
		return zero, false
	}
	return v, true
}
