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
type FinalizeFunc func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error)

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

	// Finalize is called after summary is generated. The returned messages are used as the final output.
	// Optional.
	Finalize FinalizeFunc
}

type TokenCounterInput struct {
	Messages []adk.Message
	Tools    []*schema.ToolInfo
}

// TriggerCondition specifies when summarization should be activated.
type TriggerCondition struct {
	// MaxTokens triggers summarization when total token count exceeds this threshold.
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

	if m.cfg.EmitInternalEvents {
		err = adk.SendEvent(ctx, &adk.AgentEvent{
			Action: &adk.AgentAction{
				CustomizedAction: &CustomizedAction{
					Type: ActionTypeBeforeSummary,
					Before: &BeforeSummaryAction{
						Messages: state.Messages,
					},
				},
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to send internal event: %w", err)
		}
	}

	var (
		systemMsgs     []adk.Message
		msgsExclSystem []adk.Message
	)

	for _, msg := range state.Messages {
		if msg.Role == schema.System {
			systemMsgs = append(systemMsgs, msg)
		} else {
			msgsExclSystem = append(msgsExclSystem, msg)
		}
	}

	summary, err := m.summarize(ctx, msgsExclSystem)
	if err != nil {
		return nil, nil, err
	}

	summary, err = m.postProcessSummary(ctx, state.Messages, summary)
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

	if m.cfg.EmitInternalEvents {
		err = adk.SendEvent(ctx, &adk.AgentEvent{
			Action: &adk.AgentAction{
				CustomizedAction: &CustomizedAction{
					Type: ActionTypeAfterSummary,
					After: &AfterSummaryAction{
						Messages: state.Messages,
					},
				},
			},
		})
		if err != nil {
			return nil, nil, fmt.Errorf("failed to send internal event: %w", err)
		}
	}

	return ctx, state, nil
}

func (m *middleware) shouldSummarize(ctx context.Context, input *TokenCounterInput) (bool, error) {
	tokens, err := m.countTokens(ctx, input)
	if err != nil {
		return false, fmt.Errorf("failed to count tokens: %w", err)
	}
	return tokens > m.getTriggerMaxTokens(), nil
}

func (m *middleware) getTriggerMaxTokens() int {
	const defaultTriggerMaxTokens = 190000
	if m.cfg.Trigger != nil {
		return m.cfg.Trigger.MaxTokens
	}
	return defaultTriggerMaxTokens
}

func (m *middleware) getMaxUserMessageTokens() int {
	return m.getTriggerMaxTokens() * 2 / 3
}

func (m *middleware) countTokens(ctx context.Context, input *TokenCounterInput) (int, error) {
	if m.cfg.TokenCounter != nil {
		return m.cfg.TokenCounter(ctx, input)
	}
	return defaultTokenCounter(ctx, input)
}

func defaultTokenCounter(ctx context.Context, input *TokenCounterInput) (int, error) {
	counter := func(text string) int {
		return (len(text) + 3) / 4
	}

	var totalTokens int
	for _, msg := range input.Messages {
		text := extractTextContent(msg)
		totalTokens += counter(text)
	}

	for _, tl := range input.Tools {
		tl_ := *tl
		tl_.Extra = nil
		text, err := sonic.MarshalString(tl_)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal tool info: %w", err)
		}

		totalTokens += counter(text)
	}

	return totalTokens, nil
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

var (
	allUserMessagesCloseTagRegex = regexp.MustCompile(`</?all_user_messages>`)
	pendingTasksRegex            = regexp.MustCompile(`(?m)^(\d+\.\s*)?Pending Tasks:`)
)

func (m *middleware) postProcessSummary(ctx context.Context, messages []adk.Message, summary adk.Message) (adk.Message, error) {
	maxUserMsgTokens := m.getMaxUserMessageTokens()
	content, err := m.insertUserMessagesIntoSummary(ctx, messages, summary.Content, maxUserMsgTokens)
	if err != nil {
		return nil, fmt.Errorf("failed to insert user messages into summary: %w", err)
	}
	summary.Content = content

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

func (m *middleware) insertUserMessagesIntoSummary(ctx context.Context, messages []adk.Message, summary string, maxTokens int) (string, error) {
	var userMsgs []adk.Message
	for _, msg := range messages {
		if typ, ok := getContentType(msg); ok && typ == contentTypeSummary {
			continue
		}
		if msg.Role == schema.User {
			userMsgs = append(userMsgs, msg)
		}
	}

	var (
		totalTokens int
		selected    []adk.Message
	)
	for i := len(userMsgs) - 1; i >= 0; i-- {
		msg := userMsgs[i]
		tokens, err := m.countTokens(ctx, &TokenCounterInput{
			Messages: []adk.Message{msg},
		})
		if err != nil {
			return "", fmt.Errorf("failed to count tokens: %w", err)
		}

		remaining := maxTokens - totalTokens
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

	var msgLines []string
	for _, msg := range selected {
		text := extractTextContent(msg)
		if text != "" {
			msgLines = append(msgLines, "    - "+text)
		}
	}
	userMsgsText := strings.Join(msgLines, "\n")

	if userMsgsText == "" || strings.Contains(summary, userMsgsText) {
		return summary, nil
	}

	if loc := findLastMatch(allUserMessagesCloseTagRegex, summary); loc != nil {
		return summary[:loc[0]] + userMsgsText + "\n" + summary[loc[0]:], nil
	}

	if loc := findLastMatch(pendingTasksRegex, summary); loc != nil {
		return summary[:loc[0]] + userMsgsText + "\n\n" + summary[loc[0]:], nil
	}

	return appendSection(summary, fmt.Sprintf(getFallbackUserMessagesInstruction(), userMsgsText)), nil
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

	trimmed := truncateTextByTokens(textContent, remainingTokens)
	if trimmed == "" {
		return nil
	}

	return &schema.Message{
		Role:    schema.User,
		Content: trimmed,
	}
}

func truncateTextByTokens(text string, maxTokens int) string {
	const approxBytesPerToken = 4

	if text == "" {
		return ""
	}

	maxBytes := maxTokens * approxBytesPerToken
	if len(text) <= maxBytes {
		return text
	}

	leftBudget := maxBytes / 2
	rightBudget := maxBytes - leftBudget

	prefix, suffix, removedBytes := splitStringAtUTF8Boundary(text, leftBudget, rightBudget)

	removedTokens := (removedBytes + approxBytesPerToken - 1) / approxBytesPerToken
	marker := fmt.Sprintf(getTruncatedMarkerFormat(), removedTokens)

	return prefix + marker + suffix
}

func splitStringAtUTF8Boundary(s string, leftBytes, rightBytes int) (prefix, suffix string, removedBytes int) {
	if s == "" {
		return "", "", 0
	}

	totalBytes := len(s)

	prefixEnd := 0
	for i, r := range s {
		charEnd := i + utf8.RuneLen(r)
		if charEnd <= leftBytes {
			prefixEnd = charEnd
		} else {
			break
		}
	}

	suffixStart := totalBytes
	targetStart := totalBytes - rightBytes
	for i := range s {
		if i >= targetStart {
			suffixStart = i
			break
		}
	}

	if suffixStart < prefixEnd {
		suffixStart = prefixEnd
	}

	prefix = s[:prefixEnd]
	suffix = s[suffixStart:]
	removedBytes = suffixStart - prefixEnd

	return prefix, suffix, removedBytes
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
	if c.MaxTokens <= 0 {
		return fmt.Errorf("trigger.MaxTokens must be positive")
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
