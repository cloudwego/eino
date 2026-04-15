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
	"math/rand"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*CustomizedAction]("_eino_adk_summarization_mw_customized_action")
}

type (
	TokenCounterFunc      func(ctx context.Context, input *TokenCounterInput) (int, error)
	GenModelInputFunc     func(ctx context.Context, sysInstruction, userInstruction adk.Message, originalMsgs []adk.Message) ([]adk.Message, error)
	GetFailoverModelFunc  func(ctx context.Context, failoverCtx *FailoverContext) (failoverModel model.BaseChatModel, failoverModelInputMsgs []*schema.Message, failoverErr error)
	FinalizeFunc          func(ctx context.Context, originalMessages []adk.Message, summary adk.Message) ([]adk.Message, error)
	CallbackFunc          func(ctx context.Context, before, after adk.ChatModelAgentState) error
	UserMessageFilterFunc func(ctx context.Context, msg adk.Message) (bool, error)
)

// Config defines the configuration for the summarization middleware.
type Config struct {
	// Model is the chat model used to generate summaries.
	Model model.BaseChatModel

	// ModelOptions specifies options passed to the model when generating summaries.
	// Optional.
	ModelOptions []model.Option

	// TokenCounter calculates the token count for given messages and tools.
	//
	// Parameters:
	//   - input: contains the messages and tools to count tokens for.
	//
	// Returns:
	//   - int: the total token count.
	//
	// Optional. Defaults to a simple estimator (~4 chars/token).
	TokenCounter TokenCounterFunc

	// Trigger specifies the conditions that activate summarization.
	// Optional. Defaults to triggering when total tokens exceed 190k.
	Trigger *TriggerCondition

	// EmitInternalEvents indicates whether internal events should be emitted during summarization,
	// allowing external observers to track the summarization process.
	//
	// Event Scoping:
	//   - ActionTypeBeforeSummarize: emitted before calling model to generate summary
	//   - ActionTypeGenerateSummary: emitted after each model generate attempt
	//   - ActionTypeAfterSummarize: emitted after summary generation completes
	//
	// Optional. Defaults to false.
	EmitInternalEvents bool

	// UserInstruction serves as the user-level instruction to guide the model on how to summarize the context.
	// It is appended to the message history as a User message.
	// If provided, it overrides the default user summarization instruction.
	// Optional.
	UserInstruction string

	// TranscriptFilePath is the path to the file containing the full conversation history.
	// It is appended to the summary to remind the model where to read the original context.
	// Optional but strongly recommended.
	TranscriptFilePath string

	// GenModelInput allows full control over the summarization model input construction.
	//
	// Parameters:
	//   - sysInstruction: System message defining the model's role. It is set
	//     internally by the middleware and is not configurable.
	//   - userInstruction: User message with the task instruction.
	//   - originalMsgs: original complete message list.
	//
	// Returns:
	//   - []adk.Message: the constructed model input messages.
	//
	// Typical model input order: systemInstruction -> contextMessages -> userInstruction.
	//
	// Optional.
	GenModelInput GenModelInputFunc

	// Finalize is called after summary generation. The returned messages are used as the final output.
	//
	// Parameters:
	//   - originalMessages: the original conversation messages before summarization.
	//   - summary: the generated summary message (post-processed).
	//
	// Returns:
	//   - []adk.Message: the new conversation history to replace the original messages.
	//
	// Optional.
	Finalize FinalizeFunc

	// Callback is called after Finalize, before exiting the middleware.
	// Read-only, do not modify state.
	//
	// Parameters:
	//   - before: the agent state before summarization.
	//   - after: the agent state after summarization.
	//
	// Optional.
	Callback CallbackFunc

	// PreserveUserMessages controls whether to preserve original user messages in the summary.
	// When enabled, replaces the <all_user_messages> section in the model-generated summary
	// with recent original user messages from the conversation.
	// When disabled, the model-generated content is kept unchanged.
	// Optional. Enabled by default.
	PreserveUserMessages *PreserveUserMessages

	// Retry configures retry behavior for summary generation on the primary model.
	// Optional. Defaults to no retries.
	Retry *RetryConfig

	// Failover configures fallback behavior when summary generation on the primary model fails.
	// Optional.
	Failover *FailoverConfig
}

// TokenCounterInput is the input for TokenCounterFunc.
type TokenCounterInput struct {
	// Messages is the list of messages to count tokens for.
	Messages []adk.Message
	// Tools is the list of tools to count tokens for.
	Tools []*schema.ToolInfo
}

// TriggerCondition specifies when summarization should be activated.
// Summarization triggers if ANY of the set conditions is met.
type TriggerCondition struct {
	// ContextTokens triggers summarization when total token count exceeds this threshold.
	ContextTokens int
	// ContextMessages triggers summarization when total messages count exceeds this threshold.
	ContextMessages int
}

// PreserveUserMessages controls whether to preserve original user messages in the summary.
type PreserveUserMessages struct {
	Enabled bool

	// MaxTokens limits the maximum token count for preserved user messages.
	// When set, only the most recent user messages within this limit are preserved.
	// Optional. Defaults to 1/3 of TriggerCondition.ContextTokens if not specified.
	MaxTokens int

	// Filter determines whether a specific user message should be preserved.
	// It is called for each user message. If it returns false, the message will not be preserved.
	// Optional.
	Filter UserMessageFilterFunc
}

type RetryConfig struct {
	// MaxRetries specifies the maximum number of retry attempts.
	// Optional. Defaults to 3.
	MaxRetries *int

	// ShouldRetry determines whether a failed summary generation attempt should be retried.
	// It is called after each failed attempt with the model response and error.
	// Optional. Defaults to retrying when err is non-nil.
	ShouldRetry func(ctx context.Context, resp adk.Message, err error) bool

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
	// Optional. Defaults to a default exponential backoff with jitter.
	BackoffFunc func(ctx context.Context, attempt int, resp adk.Message, err error) time.Duration
}

type FailoverConfig struct {
	// MaxRetries specifies the maximum number of retry attempts for failover.
	// Optional. Defaults to 3.
	MaxRetries *int

	// ShouldFailover determines whether another failover attempt should be made.
	// It is called after each failover attempt with the model response and error.
	// Optional. Defaults to failing over when err is non-nil.
	ShouldFailover func(ctx context.Context, resp adk.Message, err error) bool

	// BackoffFunc calculates the delay before the next failover attempt.
	// The attempt parameter starts at 1 for the first failover attempt.
	// Optional. Defaults to a default exponential backoff with jitter.
	BackoffFunc func(ctx context.Context, attempt int, resp adk.Message, err error) time.Duration

	// GetFailoverModel selects the model and input messages for the current failover attempt.
	//
	// Parameters:
	//   - failoverCtx: contains the context for the current failover attempt.
	//
	// Returns:
	//   - failoverModel: the model to use for this failover attempt.
	//   - failoverModelInputMsgs: the input messages to send to failoverModel.
	//   - failoverErr: an error encountered while preparing the failover model or input.
	//
	// Constraints:
	//   - When provided, it must return a non-nil model and a non-empty input message list.
	//
	// Optional. Defaults to reusing the primary model with the default input messages.
	GetFailoverModel GetFailoverModelFunc
}

// FailoverContext contains the state for a failover attempt.
type FailoverContext struct {
	// Attempt is the current failover attempt number, starting at 1.
	Attempt int

	// SystemInstruction is the system instruction used for summary generation.
	// It is set internally by the middleware and is not configurable.
	SystemInstruction adk.Message

	// UserInstruction is the user instruction used for summary generation.
	UserInstruction adk.Message

	// OriginalMessages is the full original conversation before summarization.
	OriginalMessages []adk.Message

	// LastModelResponse is the response returned by the previous attempt, if any.
	LastModelResponse *schema.Message

	// LastErr is the error returned by the previous attempt, if any.
	LastErr error
}

// New creates a summarization middleware that automatically summarizes conversation history
// when trigger conditions are met.
func New(_ context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
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

// SummarizeOutput contains the output of a synchronous Summarize call.
type SummarizeOutput struct {
	// FinalizedMessages is the message list after summarization,
	// ready to be used as the new conversation history.
	FinalizedMessages []adk.Message

	// ModelResponse is the raw response from the summarization model.
	ModelResponse adk.Message
}

// SummarizeMessages performs synchronous summarization of the given messages.
// EmitInternalEvents and Trigger are not supported and will return an error if set.
func SummarizeMessages(ctx context.Context, cfg *Config, messages []adk.Message) (*SummarizeOutput, error) {
	if cfg.EmitInternalEvents {
		return nil, fmt.Errorf("emitInternalEvents is not supported in synchronous summarization")
	}
	if cfg.Trigger != nil {
		return nil, fmt.Errorf("trigger is not supported in synchronous summarization")
	}
	if err := cfg.check(); err != nil {
		return nil, err
	}

	m := &middleware{cfg: cfg}

	rawSummary, modelInput, err := m.summarize(ctx, messages)
	if err != nil {
		return nil, err
	}

	finalizeCtx := context.WithValue(ctx, ctxKeyModelInput{}, modelInput)

	_, finalMsgs, err := m.finalizeSummary(finalizeCtx, messages, rawSummary)
	if err != nil {
		return nil, err
	}

	if m.cfg.Callback != nil {
		beforeState := adk.ChatModelAgentState{Messages: messages}
		afterState := adk.ChatModelAgentState{Messages: finalMsgs}
		if err = m.cfg.Callback(ctx, beforeState, afterState); err != nil {
			return nil, err
		}
	}

	return &SummarizeOutput{
		FinalizedMessages: finalMsgs,
		ModelResponse:     rawSummary,
	}, nil
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
			Type: ActionTypeBeforeSummarize,
			Before: &BeforeSummarizeAction{
				Messages: beforeState.Messages,
			},
		})
		if err != nil {
			return nil, nil, err
		}
	}

	rawSummary, modelInput, err := m.summarize(ctx, beforeState.Messages)
	if err != nil {
		return nil, nil, err
	}

	finalizeCtx := context.WithValue(ctx, ctxKeyModelInput{}, modelInput)

	var finalMsgs []adk.Message
	_, finalMsgs, err = m.finalizeSummary(finalizeCtx, beforeState.Messages, rawSummary)
	if err != nil {
		return nil, nil, err
	}

	afterState := beforeState
	afterState.Messages = finalMsgs

	if m.cfg.Callback != nil {
		err = m.cfg.Callback(ctx, beforeState, afterState)
		if err != nil {
			return nil, nil, err
		}
	}

	if m.cfg.EmitInternalEvents {
		err = m.emitEvent(ctx, &CustomizedAction{
			Type: ActionTypeAfterSummarize,
			After: &AfterSummarizeAction{
				Messages: afterState.Messages,
			},
		})
		if err != nil {
			return nil, nil, err
		}
	}

	return ctx, &afterState, nil
}

func (m *middleware) finalizeSummary(ctx context.Context, originalMsgs []adk.Message,
	rawSummary adk.Message) (context.Context, []adk.Message, error) {

	systemMsgs, contextMsgs := m.splitSystemAndContextMsgs(originalMsgs)

	summary, err := m.postProcessSummary(ctx, contextMsgs, newSummaryMessage(rawSummary.Content))
	if err != nil {
		return nil, nil, err
	}

	var finalMsgs []adk.Message
	if m.cfg.Finalize != nil {
		finalMsgs, err = m.cfg.Finalize(ctx, originalMsgs, summary)
		if err != nil {
			return nil, nil, err
		}
	} else {
		finalMsgs = append(systemMsgs, summary)
	}

	return ctx, finalMsgs, nil
}

func (m *middleware) shouldSummarize(ctx context.Context, input *TokenCounterInput) (bool, error) {
	if m.cfg.Trigger != nil && m.cfg.Trigger.ContextMessages > 0 {
		if len(input.Messages) > m.cfg.Trigger.ContextMessages {
			return true, nil
		}
	}
	tokens, err := m.countTokens(ctx, input)
	if err != nil {
		return false, fmt.Errorf("failed to count tokens: %w", err)
	}
	return tokens > m.getTriggerContextTokens(), nil
}

func (m *middleware) getTriggerContextTokens() int {
	const defaultTriggerContextTokens = 170000
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

func (m *middleware) emitGenerateSummaryEvent(ctx context.Context, attempt int, phase GenerateSummaryPhase,
	resp adk.Message, err error) error {

	if !m.cfg.EmitInternalEvents {
		return nil
	}

	action := &GenerateSummaryAction{
		Attempt:       attempt,
		Phase:         phase,
		ModelResponse: resp,
		err:           err,
	}

	return m.emitEvent(ctx, &CustomizedAction{
		Type:            ActionTypeGenerateSummary,
		GenerateSummary: action,
	})
}

func (m *middleware) countTokens(ctx context.Context, input *TokenCounterInput) (int, error) {
	if m.cfg.TokenCounter != nil {
		return m.cfg.TokenCounter(ctx, input)
	}
	return defaultTokenCounter(ctx, input)
}

func defaultTokenCounter(_ context.Context, input *TokenCounterInput) (int, error) {
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

func estimateTokenBytes(tokens int) int {
	return tokens * 4
}

func (m *middleware) summarize(ctx context.Context, originalMsgs []adk.Message) (adk.Message, []adk.Message, error) {
	_, contextMsgs := m.splitSystemAndContextMsgs(originalMsgs)

	modelInput, err := m.buildSummarizationModelInput(ctx, originalMsgs, contextMsgs)
	if err != nil {
		return nil, nil, err
	}

	rawSummary, err := m.generateWithRetry(ctx, m.cfg.Model, modelInput, m.cfg.ModelOptions, m.cfg.Retry)
	if shouldFailover(ctx, m.cfg.Failover, rawSummary, err) {
		rawSummary, modelInput, err = m.runFailover(ctx, originalMsgs, modelInput, rawSummary, err)
		if err != nil {
			return nil, nil, err
		}
	} else if err != nil {
		return nil, nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	return rawSummary, modelInput, nil
}

func (m *middleware) splitSystemAndContextMsgs(msgs []adk.Message) ([]adk.Message, []adk.Message) {
	var systemMsgs []adk.Message
	for _, msg := range msgs {
		if msg.Role == schema.System {
			systemMsgs = append(systemMsgs, msg)
		} else {
			break
		}
	}
	contextMsgs := msgs[len(systemMsgs):]
	return systemMsgs, contextMsgs
}

func (m *middleware) runFailover(ctx context.Context, originalMsgs, defaultInput []adk.Message, lastResp adk.Message,
	lastErr error) (adk.Message, []adk.Message, error) {

	const defaultMaxRetries = 3

	sysInstruction, userInstruction := m.getModelInstructions()

	maxRetries := defaultMaxRetries
	if m.cfg.Failover.MaxRetries != nil {
		maxRetries = *m.cfg.Failover.MaxRetries
	}

	backoff := m.cfg.Failover.BackoffFunc
	if backoff == nil {
		backoff = defaultBackoffFunc
	}

	modelInput := defaultInput
	total := maxRetries + 1

	for attempt := 1; ; attempt++ {
		fctx := &FailoverContext{
			Attempt:           attempt,
			SystemInstruction: sysInstruction,
			UserInstruction:   userInstruction,
			OriginalMessages:  originalMsgs,
			LastModelResponse: lastResp,
			LastErr:           lastErr,
		}

		failoverModel, nextInput, failoverErr := m.getFailoverModel(ctx, fctx, defaultInput)
		if failoverErr != nil {
			lastResp = nil
			lastErr = failoverErr
			if emitErr := m.emitGenerateSummaryEvent(ctx, attempt, GenerateSummaryPhaseFailover, nil, failoverErr); emitErr != nil {
				return nil, nil, emitErr
			}
		} else {
			modelInput = nextInput
			lastResp, lastErr = m.generateAndEmit(ctx, failoverModel, modelInput, m.cfg.ModelOptions, attempt, GenerateSummaryPhaseFailover)
		}

		if !shouldFailover(ctx, m.cfg.Failover, lastResp, lastErr) {
			return lastResp, modelInput, lastErr
		}
		if attempt == total {
			if lastErr != nil {
				return nil, nil, fmt.Errorf("exceeds max failover attempts: %w", lastErr)
			}
			return nil, nil, fmt.Errorf("exceeds max failover attempts")
		}

		select {
		case <-time.After(backoff(ctx, attempt, lastResp, lastErr)):
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		}
	}
}

func (m *middleware) getFailoverModel(ctx context.Context, failoverCtx *FailoverContext, defaultInput []adk.Message) (model.BaseChatModel, []adk.Message, error) {
	if m.cfg.Failover == nil {
		return nil, nil, fmt.Errorf("failover config is required")
	}
	if m.cfg.Failover.GetFailoverModel == nil {
		return m.cfg.Model, defaultInput, nil
	}

	failoverModel, nextModelInput, err := m.cfg.Failover.GetFailoverModel(ctx, failoverCtx)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to get failover model: %w", err)
	}

	if failoverModel == nil {
		return nil, nil, fmt.Errorf("failover model is required")
	}
	if len(nextModelInput) == 0 {
		return nil, nil, fmt.Errorf("failover model input messages are required")
	}

	return failoverModel, nextModelInput, nil
}

func (m *middleware) buildSummarizationModelInput(ctx context.Context, originMsgs, contextMsgs []adk.Message) ([]adk.Message, error) {
	sysInstruction, userInstruction := m.getModelInstructions()

	if m.cfg.GenModelInput != nil {
		input, err := m.cfg.GenModelInput(ctx, sysInstruction, userInstruction, originMsgs)
		if err != nil {
			return nil, fmt.Errorf("failed to generate model input: %w", err)
		}
		return input, nil
	}

	input := make([]adk.Message, 0, len(contextMsgs)+2)
	input = append(input, sysInstruction)
	input = append(input, contextMsgs...)
	input = append(input, userInstruction)

	return input, nil
}

func (m *middleware) getModelInstructions() (adk.Message, adk.Message) {
	userInstruction := m.cfg.UserInstruction
	if userInstruction == "" {
		userInstruction = getUserSummaryInstruction()
	}

	userInstructionMsg := &schema.Message{
		Role:    schema.User,
		Content: userInstruction,
	}

	sysInstructionMsg := &schema.Message{
		Role:    schema.System,
		Content: getSystemInstruction(),
	}

	return sysInstructionMsg, userInstructionMsg
}

func newSummaryMessage(content string) *schema.Message {
	summary := &schema.Message{
		Role:    schema.User,
		Content: content,
	}
	setContentType(summary, contentTypeSummary)
	return summary
}

func (m *middleware) postProcessSummary(ctx context.Context, contextMsgs []adk.Message, summary adk.Message) (adk.Message, error) {
	if m.cfg.PreserveUserMessages == nil || m.cfg.PreserveUserMessages.Enabled {
		maxUserMsgTokens := m.getUserMessageContextTokens()
		content, err := m.replaceUserMessagesInSummary(ctx, contextMsgs, summary.Content, maxUserMsgTokens)
		if err != nil {
			return nil, fmt.Errorf("failed to replace user messages in summary: %w", err)
		}
		summary.Content = content
	}

	if path := m.cfg.TranscriptFilePath; path != "" {
		summary.Content = appendSection(summary.Content, fmt.Sprintf(getTranscriptPathInstruction(), path))
	}

	summary.Content = appendSection(getSummaryPreamble(), summary.Content)

	var inputParts []schema.MessageInputPart

	inputParts = append(inputParts, schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: summary.Content,
	}, schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: getContinueInstruction(),
	})

	summary.UserInputMultiContent = inputParts
	summary.Content = ""

	return summary, nil
}

func (m *middleware) replaceUserMessagesInSummary(ctx context.Context, contextMsgs []adk.Message, summary string, contextTokens int) (string, error) {
	var userMsgs []adk.Message
	var hasUserMsgsBeforeFilter bool
	for _, msg := range contextMsgs {
		if typ, ok := getContentType(msg); ok && typ == contentTypeSummary {
			continue
		}
		if msg.Role == schema.User {
			hasUserMsgsBeforeFilter = true
			if m.cfg.PreserveUserMessages != nil && m.cfg.PreserveUserMessages.Filter != nil {
				keep, err := m.cfg.PreserveUserMessages.Filter(ctx, msg)
				if err != nil {
					return "", fmt.Errorf("failed to filter user message: %w", err)
				}
				if !keep {
					continue
				}
			}
			userMsgs = append(userMsgs, msg)
		}
	}

	if !hasUserMsgsBeforeFilter {
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

	var sb strings.Builder
	for _, part := range msg.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			if sb.Len() > 0 {
				sb.WriteString("\n")
			}
			sb.WriteString(part.Text)
		}
	}

	if sb.Len() > 0 {
		return sb.String()
	}

	return msg.Content
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
	if c.Retry != nil {
		if err := c.Retry.check(); err != nil {
			return err
		}
	}
	if c.Failover != nil {
		if err := c.Failover.check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *RetryConfig) check() error {
	if c.MaxRetries != nil && *c.MaxRetries < 0 {
		return fmt.Errorf("retry.MaxRetries must be non-negative")
	}
	return nil
}

func (c *FailoverConfig) check() error {
	if c.MaxRetries != nil && *c.MaxRetries < 0 {
		return fmt.Errorf("failover.MaxRetries must be non-negative")
	}
	return nil
}

func (c *TriggerCondition) check() error {
	if c.ContextTokens < 0 {
		return fmt.Errorf("contextTokens must be non-negative")
	}
	if c.ContextMessages < 0 {
		return fmt.Errorf("contextMessages must be non-negative")
	}
	if c.ContextTokens == 0 && c.ContextMessages == 0 {
		return fmt.Errorf("at least one of contextTokens or contextMessages must be non-negative")
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

func shouldFailover(ctx context.Context, cfg *FailoverConfig, resp adk.Message, err error) bool {
	if cfg == nil {
		return false
	}
	if cfg.ShouldFailover == nil {
		return err != nil
	}
	return cfg.ShouldFailover(ctx, resp, err)
}

func (m *middleware) generateAndEmit(ctx context.Context, chatModel model.BaseChatModel, input []adk.Message,
	opts []model.Option, attempt int, phase GenerateSummaryPhase) (adk.Message, error) {

	resp, err := chatModel.Generate(ctx, input, opts...)
	if emitErr := m.emitGenerateSummaryEvent(ctx, attempt, phase, resp, err); emitErr != nil {
		return nil, emitErr
	}
	return resp, err
}

func (m *middleware) generateWithRetry(ctx context.Context, chatModel model.BaseChatModel, input []adk.Message,
	opts []model.Option, retryCfg *RetryConfig) (adk.Message, error) {

	const defaultMaxRetries = 3

	if retryCfg == nil {
		return m.generateAndEmit(ctx, chatModel, input, opts, 1, GenerateSummaryPhasePrimary)
	}

	shouldRetry := retryCfg.ShouldRetry
	if shouldRetry == nil {
		shouldRetry = defaultShouldRetry
	}
	backoffFunc := retryCfg.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultBackoffFunc
	}

	maxRetries := defaultMaxRetries
	if retryCfg.MaxRetries != nil {
		maxRetries = *retryCfg.MaxRetries
	}
	totalAttempts := maxRetries + 1

	var (
		lastModelResp adk.Message
		lastErr       error
	)
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		resp, err := m.generateAndEmit(ctx, chatModel, input, opts, attempt, GenerateSummaryPhasePrimary)
		if err == nil {
			return resp, nil
		}
		if !shouldRetry(ctx, resp, err) {
			return resp, err
		}

		lastModelResp = resp
		lastErr = err
		if attempt < totalAttempts {
			select {
			case <-time.After(backoffFunc(ctx, attempt, resp, err)):
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
	}

	if maxRetries > 0 {
		return lastModelResp, fmt.Errorf("exceeds max retries: %w", lastErr)
	}

	return lastModelResp, lastErr
}

func defaultShouldRetry(_ context.Context, _ adk.Message, err error) bool {
	return err != nil
}

func defaultBackoffFunc(_ context.Context, attempt int, _ adk.Message, _ error) time.Duration {
	const (
		baseDelay = time.Second
		maxDelay  = 10 * time.Second
	)

	if attempt <= 0 {
		return baseDelay
	}

	if attempt > 7 {
		return maxDelay + time.Duration(rand.Int63n(int64(maxDelay/2)))
	}

	delay := baseDelay * time.Duration(1<<uint(attempt-1))
	if delay > maxDelay {
		delay = maxDelay
	}

	jitter := time.Duration(rand.Int63n(int64(delay / 2)))

	return delay + jitter
}
