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
	schema.RegisterName[*TypedCustomizedAction[*schema.Message]]("_eino_adk_summarization_mw_customized_action")
	schema.RegisterName[*TypedCustomizedAction[*schema.AgenticMessage]]("_eino_adk_summarization_mw_customized_action_agentic")
}

type TypedTokenCounterFunc[M adk.MessageType] func(ctx context.Context, input *TypedTokenCounterInput[M]) (int, error)
type TypedGenModelInputFunc[M adk.MessageType] func(ctx context.Context, sysInstruction, userInstruction M, originalMsgs []M) ([]M, error)
type TypedGetFailoverModelFunc[M adk.MessageType] func(ctx context.Context, failoverCtx *TypedFailoverContext[M]) (failoverModel model.BaseModel[M], failoverModelInputMsgs []M, failoverErr error)
type TypedFinalizeFunc[M adk.MessageType] func(ctx context.Context, originalMessages []M, summary M) ([]M, error)
type TypedCallbackFunc[M adk.MessageType] func(ctx context.Context, before, after adk.TypedChatModelAgentState[M]) error
type TypedUserMessageFilterFunc[M adk.MessageType] func(ctx context.Context, msg M) (bool, error)

type TokenCounterFunc = TypedTokenCounterFunc[*schema.Message]
type GenModelInputFunc = TypedGenModelInputFunc[*schema.Message]
type GetFailoverModelFunc = TypedGetFailoverModelFunc[*schema.Message]
type FinalizeFunc = TypedFinalizeFunc[*schema.Message]
type CallbackFunc = TypedCallbackFunc[*schema.Message]
type UserMessageFilterFunc = TypedUserMessageFilterFunc[*schema.Message]

// TypedConfig defines the configuration for the summarization middleware,
// generic over message type M.
type TypedConfig[M adk.MessageType] struct {
	// Model is the chat model used to generate summaries.
	Model model.BaseModel[M]

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
	TokenCounter TypedTokenCounterFunc[M]

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
	//   - []M: the constructed model input messages.
	//
	// Typical model input order: systemInstruction -> contextMessages -> userInstruction.
	//
	// Optional.
	GenModelInput TypedGenModelInputFunc[M]

	// Finalize is called after summary generation. The returned messages are used as the final output.
	//
	// Parameters:
	//   - originalMessages: the original conversation messages before summarization.
	//   - summary: the generated summary message (post-processed).
	//
	// Returns:
	//   - []M: the new conversation history to replace the original messages.
	//
	// Optional.
	Finalize TypedFinalizeFunc[M]

	// Callback is called after Finalize, before exiting the middleware.
	// Read-only, do not modify state.
	//
	// Parameters:
	//   - before: the agent state before summarization.
	//   - after: the agent state after summarization.
	//
	// Optional.
	Callback TypedCallbackFunc[M]

	// PreserveUserMessages controls whether to preserve original user messages in the summary.
	// When enabled, replaces the <all_user_messages> section in the model-generated summary
	// with recent original user messages from the conversation.
	// When disabled, the model-generated content is kept unchanged.
	// Optional. Enabled by default.
	PreserveUserMessages *TypedPreserveUserMessages[M]

	// Retry configures retry behavior for summary generation on the primary model.
	// Optional. Defaults to no retries.
	Retry *TypedRetryConfig[M]

	// Failover configures fallback behavior when summary generation on the primary model fails.
	// Optional.
	Failover *TypedFailoverConfig[M]
}

// Config is a backward-compatible alias for TypedConfig specialized with *schema.Message.
type Config = TypedConfig[*schema.Message]

// TypedTokenCounterInput is the input for TypedTokenCounterFunc.
type TypedTokenCounterInput[M adk.MessageType] struct {
	// Messages is the list of messages to count tokens for.
	Messages []M
	// Tools is the list of tools to count tokens for.
	Tools []*schema.ToolInfo
}

// TokenCounterInput is a backward-compatible alias for TypedTokenCounterInput specialized with *schema.Message.
type TokenCounterInput = TypedTokenCounterInput[*schema.Message]

// TriggerCondition specifies when summarization should be activated.
// Summarization triggers if ANY of the set conditions is met.
type TriggerCondition struct {
	// ContextTokens triggers summarization when total token count exceeds this threshold.
	ContextTokens int
	// ContextMessages triggers summarization when total messages count exceeds this threshold.
	ContextMessages int
}

// TypedPreserveUserMessages controls whether to preserve original user messages in the summary.
type TypedPreserveUserMessages[M adk.MessageType] struct {
	Enabled bool

	// MaxTokens limits the maximum token count for preserved user messages.
	// When set, only the most recent user messages within this limit are preserved.
	// Optional. Defaults to 1/3 of TriggerCondition.ContextTokens if not specified.
	MaxTokens int

	// Filter determines whether a specific user message should be preserved.
	// It is called for each user message. If it returns false, the message will not be preserved.
	// Optional.
	Filter TypedUserMessageFilterFunc[M]
}

// PreserveUserMessages is a backward-compatible alias for TypedPreserveUserMessages specialized with *schema.Message.
type PreserveUserMessages = TypedPreserveUserMessages[*schema.Message]

type TypedRetryConfig[M adk.MessageType] struct {
	// MaxRetries specifies the maximum number of retry attempts.
	// Optional. Defaults to 3.
	MaxRetries *int

	// ShouldRetry determines whether a failed summary generation attempt should be retried.
	// It is called after each failed attempt with the model response and error.
	// Optional. Defaults to retrying when err is non-nil.
	ShouldRetry func(ctx context.Context, resp M, err error) bool

	// BackoffFunc calculates the delay before the next retry attempt.
	// The attempt parameter starts at 1 for the first retry.
	// Optional. Defaults to a default exponential backoff with jitter.
	BackoffFunc func(ctx context.Context, attempt int, resp M, err error) time.Duration
}

// RetryConfig is a backward-compatible alias for TypedRetryConfig specialized with *schema.Message.
type RetryConfig = TypedRetryConfig[*schema.Message]

type TypedFailoverConfig[M adk.MessageType] struct {
	// MaxRetries specifies the maximum number of failover attempts.
	// Optional. Defaults to 3.
	MaxRetries *int

	// ShouldFailover determines whether another failover attempt should be made.
	// It is called after each failover attempt with the model response and error.
	// Optional. Defaults to failing over when err is non-nil.
	ShouldFailover func(ctx context.Context, resp M, err error) bool

	// BackoffFunc calculates the delay before the next failover attempt.
	// The attempt parameter starts at 1 for the first failover attempt.
	// Optional. Defaults to a default exponential backoff with jitter.
	BackoffFunc func(ctx context.Context, attempt int, resp M, err error) time.Duration

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
	GetFailoverModel TypedGetFailoverModelFunc[M]
}

// FailoverConfig is a backward-compatible alias for TypedFailoverConfig specialized with *schema.Message.
type FailoverConfig = TypedFailoverConfig[*schema.Message]

// TypedFailoverContext contains the state for a failover attempt.
type TypedFailoverContext[M adk.MessageType] struct {
	// Attempt is the current failover attempt number, starting at 1.
	Attempt int

	// SystemInstruction is the system instruction used for summary generation.
	// It is set internally by the middleware and is not configurable.
	SystemInstruction M

	// UserInstruction is the user instruction used for summary generation.
	UserInstruction M

	// OriginalMessages is the full original conversation before summarization.
	OriginalMessages []M

	// LastModelResponse is the response returned by the previous attempt, if any.
	LastModelResponse M

	// LastErr is the error returned by the previous attempt, if any.
	LastErr error
}

// FailoverContext is a backward-compatible alias for TypedFailoverContext specialized with *schema.Message.
type FailoverContext = TypedFailoverContext[*schema.Message]

// NewTyped creates a generic summarization middleware that automatically summarizes
// conversation history when trigger conditions are met.
//
// This is the generic constructor that supports both *schema.Message and *schema.AgenticMessage.
func NewTyped[M adk.MessageType](_ context.Context, cfg *TypedConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if err := cfg.check(); err != nil {
		return nil, err
	}
	mw := &typedMiddleware[M]{
		cfg:                               cfg,
		TypedBaseChatModelAgentMiddleware: &adk.TypedBaseChatModelAgentMiddleware[M]{},
	}
	return mw, nil
}

// New creates a summarization middleware that automatically summarizes conversation history
// when trigger conditions are met.
func New(ctx context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped(ctx, cfg)
}

type typedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	cfg *TypedConfig[M]
}

// TypedSummarizeOutput contains the output of a synchronous Summarize call.
//
// Deprecated: See SummarizeMessages.
type TypedSummarizeOutput[M adk.MessageType] struct {
	// FinalizedMessages is the message list after summarization,
	// ready to be used as the new conversation history.
	FinalizedMessages []M

	// ModelResponse is the raw response from the summarization model.
	ModelResponse M
}

// SummarizeOutput is a backward-compatible alias for TypedSummarizeOutput specialized with *schema.Message.
type SummarizeOutput = TypedSummarizeOutput[*schema.Message]

// TypedSummarizeMessages performs synchronous summarization of the given messages.
// EmitInternalEvents and Trigger are not supported and will return an error if set.
//
// Deprecated: Use the summarization middleware (created via New) within a dedicated summarization
// agent instead. In practice, summarization often requires preprocessing by other middlewares
// (e.g., message reduction, tool call patching), which is naturally supported by composing
// middlewares in an agent pipeline.
func TypedSummarizeMessages[M adk.MessageType](ctx context.Context, cfg *TypedConfig[M], messages []M) (*TypedSummarizeOutput[M], error) {
	if cfg.EmitInternalEvents {
		return nil, fmt.Errorf("emitInternalEvents is not supported in synchronous summarization")
	}
	if cfg.Trigger != nil {
		return nil, fmt.Errorf("trigger is not supported in synchronous summarization")
	}
	if err := cfg.check(); err != nil {
		return nil, err
	}

	m := &typedMiddleware[M]{cfg: cfg}

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
		beforeState := adk.TypedChatModelAgentState[M]{Messages: messages}
		afterState := adk.TypedChatModelAgentState[M]{Messages: finalMsgs}
		if err = m.cfg.Callback(ctx, beforeState, afterState); err != nil {
			return nil, err
		}
	}

	return &TypedSummarizeOutput[M]{
		FinalizedMessages: finalMsgs,
		ModelResponse:     rawSummary,
	}, nil
}

// SummarizeMessages performs synchronous summarization of the given messages.
// EmitInternalEvents and Trigger are not supported and will return an error if set.
func SummarizeMessages(ctx context.Context, cfg *Config, messages []adk.Message) (*SummarizeOutput, error) {
	return TypedSummarizeMessages(ctx, cfg, messages)
}

func (m *typedMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M],
	mtx *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {

	var tools []*schema.ToolInfo
	if mtx != nil {
		tools = mtx.Tools
	}

	triggered, err := m.shouldSummarize(ctx, &TypedTokenCounterInput[M]{
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
		err = m.emitEvent(ctx, &TypedCustomizedAction[M]{
			Type: ActionTypeBeforeSummarize,
			Before: &TypedBeforeSummarizeAction[M]{
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

	var finalMsgs []M
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
		err = m.emitEvent(ctx, &TypedCustomizedAction[M]{
			Type: ActionTypeAfterSummarize,
			After: &TypedAfterSummarizeAction[M]{
				Messages: afterState.Messages,
			},
		})
		if err != nil {
			return nil, nil, err
		}
	}

	return ctx, &afterState, nil
}

func (m *typedMiddleware[M]) finalizeSummary(ctx context.Context, originalMsgs []M,
	rawSummary M) (context.Context, []M, error) {

	systemMsgs, contextMsgs := m.splitSystemAndContextMsgs(originalMsgs)

	rawContent := getTextContent(rawSummary)
	summary := newTypedSummaryMessage[M](rawContent)

	processed, err := m.postProcessSummary(ctx, contextMsgs, summary)
	if err != nil {
		return nil, nil, err
	}

	var finalMsgs []M
	if m.cfg.Finalize != nil {
		finalMsgs, err = m.cfg.Finalize(ctx, originalMsgs, processed)
		if err != nil {
			return nil, nil, err
		}
	} else {
		finalMsgs = append(systemMsgs, processed)
	}

	return ctx, finalMsgs, nil
}

func (m *typedMiddleware[M]) shouldSummarize(ctx context.Context, input *TypedTokenCounterInput[M]) (bool, error) {
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

func (m *typedMiddleware[M]) getTriggerContextTokens() int {
	const defaultTriggerContextTokens = 170000
	if m.cfg.Trigger != nil {
		return m.cfg.Trigger.ContextTokens
	}
	return defaultTriggerContextTokens
}

func (m *typedMiddleware[M]) getUserMessageContextTokens() int {
	if m.cfg.PreserveUserMessages != nil && m.cfg.PreserveUserMessages.MaxTokens > 0 {
		return m.cfg.PreserveUserMessages.MaxTokens
	}
	return m.getTriggerContextTokens() / 3
}

func (m *typedMiddleware[M]) emitEvent(ctx context.Context, action *TypedCustomizedAction[M]) error {
	err := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
		Action: &adk.AgentAction{
			CustomizedAction: action,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to send internal event: %w", err)
	}
	return nil
}

func (m *typedMiddleware[M]) emitGenerateSummaryEvent(ctx context.Context, attempt int, phase GenerateSummaryPhase,
	resp M, err error) error {

	if !m.cfg.EmitInternalEvents {
		return nil
	}

	action := &TypedGenerateSummaryAction[M]{
		Attempt:       attempt,
		Phase:         phase,
		ModelResponse: resp,
		err:           err,
	}

	return m.emitEvent(ctx, &TypedCustomizedAction[M]{
		Type:            ActionTypeGenerateSummary,
		GenerateSummary: action,
	})
}

func (m *typedMiddleware[M]) countTokens(ctx context.Context, input *TypedTokenCounterInput[M]) (int, error) {
	if m.cfg.TokenCounter != nil {
		return m.cfg.TokenCounter(ctx, input)
	}
	return defaultTypedTokenCounter(ctx, input)
}

func defaultTypedTokenCounter[M adk.MessageType](_ context.Context, input *TypedTokenCounterInput[M]) (int, error) {
	var totalTokens int
	for _, msg := range input.Messages {
		text := getTextContent(msg)
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

// defaultTokenCounter is kept for backward compatibility with external callers.
func defaultTokenCounter(ctx context.Context, input *TokenCounterInput) (int, error) {
	return defaultTypedTokenCounter(ctx, input)
}

func estimateTokenCount(text string) int {
	return (len(text) + 3) / 4
}

func estimateTokenBytes(tokens int) int {
	return tokens * 4
}

func (m *typedMiddleware[M]) summarize(ctx context.Context, originalMsgs []M) (M, []M, error) {
	var zero M
	_, contextMsgs := m.splitSystemAndContextMsgs(originalMsgs)

	modelInput, err := m.buildSummarizationModelInput(ctx, originalMsgs, contextMsgs)
	if err != nil {
		return zero, nil, err
	}

	rawSummary, err := m.generateWithRetry(ctx, m.cfg.Model, modelInput, m.cfg.ModelOptions, m.cfg.Retry)
	if typedShouldFailover(ctx, m.cfg.Failover, rawSummary, err) {
		rawSummary, modelInput, err = m.runFailover(ctx, originalMsgs, modelInput, rawSummary, err)
		if err != nil {
			return zero, nil, err
		}
	} else if err != nil {
		return zero, nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	return rawSummary, modelInput, nil
}

func (m *typedMiddleware[M]) splitSystemAndContextMsgs(msgs []M) ([]M, []M) {
	var systemMsgs []M
	for _, msg := range msgs {
		if isSystemRole(msg) {
			systemMsgs = append(systemMsgs, msg)
		} else {
			break
		}
	}
	contextMsgs := msgs[len(systemMsgs):]
	return systemMsgs, contextMsgs
}

func (m *typedMiddleware[M]) runFailover(ctx context.Context, originalMsgs, defaultInput []M, lastResp M,
	lastErr error) (M, []M, error) {

	var zero M
	const defaultMaxRetries = 3

	sysInstruction, userInstruction := m.getModelInstructions()

	maxRetries := defaultMaxRetries
	if m.cfg.Failover.MaxRetries != nil {
		maxRetries = *m.cfg.Failover.MaxRetries
	}

	backoff := m.cfg.Failover.BackoffFunc
	if backoff == nil {
		backoff = defaultTypedBackoffFunc[M]
	}

	modelInput := defaultInput

	if maxRetries <= 0 {
		return lastResp, modelInput, lastErr
	}

	for attempt := 1; ; attempt++ {
		fctx := &TypedFailoverContext[M]{
			Attempt:           attempt,
			SystemInstruction: sysInstruction,
			UserInstruction:   userInstruction,
			OriginalMessages:  originalMsgs,
			LastModelResponse: lastResp,
			LastErr:           lastErr,
		}

		failoverModel, nextInput, failoverErr := m.getFailoverModel(ctx, fctx, defaultInput)
		if failoverErr != nil {
			lastResp = zero
			lastErr = failoverErr
			if emitErr := m.emitGenerateSummaryEvent(ctx, attempt, GenerateSummaryPhaseFailover, zero, failoverErr); emitErr != nil {
				return zero, nil, emitErr
			}
		} else {
			modelInput = nextInput
			lastResp, lastErr = m.generateAndEmit(ctx, failoverModel, modelInput, m.cfg.ModelOptions, attempt, GenerateSummaryPhaseFailover)
		}

		if !typedShouldFailover(ctx, m.cfg.Failover, lastResp, lastErr) {
			return lastResp, modelInput, lastErr
		}
		if attempt == maxRetries {
			if lastErr != nil {
				return zero, nil, fmt.Errorf("exceeds max failover attempts: %w", lastErr)
			}
			return zero, nil, fmt.Errorf("exceeds max failover attempts")
		}

		select {
		case <-time.After(backoff(ctx, attempt, lastResp, lastErr)):
		case <-ctx.Done():
			return zero, nil, ctx.Err()
		}
	}
}

func (m *typedMiddleware[M]) getFailoverModel(ctx context.Context, failoverCtx *TypedFailoverContext[M], defaultInput []M) (model.BaseModel[M], []M, error) {
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

func (m *typedMiddleware[M]) buildSummarizationModelInput(ctx context.Context, originMsgs, contextMsgs []M) ([]M, error) {
	sysInstruction, userInstruction := m.getModelInstructions()

	if m.cfg.GenModelInput != nil {
		input, err := m.cfg.GenModelInput(ctx, sysInstruction, userInstruction, originMsgs)
		if err != nil {
			return nil, fmt.Errorf("failed to generate model input: %w", err)
		}
		return input, nil
	}

	input := make([]M, 0, len(contextMsgs)+2)
	input = append(input, sysInstruction)
	input = append(input, contextMsgs...)
	input = append(input, userInstruction)

	return input, nil
}

func (m *typedMiddleware[M]) getModelInstructions() (M, M) {
	userInstruction := m.cfg.UserInstruction
	if userInstruction == "" {
		userInstruction = getUserSummaryInstruction()
	}

	return makeSystemMsg[M](getSystemInstruction()), makeUserMsg[M](userInstruction)
}

func (m *typedMiddleware[M]) postProcessSummary(ctx context.Context, contextMsgs []M, summary M) (M, error) {
	content := getTextContent(summary)

	if m.cfg.PreserveUserMessages == nil || m.cfg.PreserveUserMessages.Enabled {
		maxUserMsgTokens := m.getUserMessageContextTokens()
		var err error
		content, err = m.replaceUserMessagesInSummary(ctx, contextMsgs, content, maxUserMsgTokens)
		if err != nil {
			var zero M
			return zero, fmt.Errorf("failed to replace user messages in summary: %w", err)
		}
	}

	if path := m.cfg.TranscriptFilePath; path != "" {
		content = appendSection(content, fmt.Sprintf(getTranscriptPathInstruction(), path))
	}

	content = appendSection(getSummaryPreamble(), content)

	result := setMsgTextContent(summary, content)
	result = setMsgMultipartContent(result, content, getContinueInstruction())

	return result, nil
}

func (m *typedMiddleware[M]) replaceUserMessagesInSummary(ctx context.Context, contextMsgs []M, summary string, contextTokens int) (string, error) {
	var userMsgs []M
	var hasUserMsgsBeforeFilter bool
	for _, msg := range contextMsgs {
		if typedGetContentType(msg) == contentTypeSummary {
			continue
		}
		if isUserRole(msg) {
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

	var selected []M
	if len(userMsgs) == 1 {
		selected = userMsgs
	} else {
		var totalTokens int
		for i := len(userMsgs) - 1; i >= 0; i-- {
			msg := userMsgs[i]

			tokens, err := m.countTokens(ctx, &TypedTokenCounterInput[M]{
				Messages: []M{msg},
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

			trimmedMsg := defaultTypedTrimUserMessage(msg, remaining)
			var zero M
			if any(trimmedMsg) != any(zero) {
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
		text := getTextContent(msg)
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

func (m *typedMiddleware[M]) generateAndEmit(ctx context.Context, chatModel model.BaseModel[M], input []M,
	opts []model.Option, attempt int, phase GenerateSummaryPhase) (M, error) {

	resp, err := chatModel.Generate(ctx, input, opts...)
	if emitErr := m.emitGenerateSummaryEvent(ctx, attempt, phase, resp, err); emitErr != nil {
		var zero M
		return zero, emitErr
	}
	return resp, err
}

func (m *typedMiddleware[M]) generateWithRetry(ctx context.Context, chatModel model.BaseModel[M], input []M,
	opts []model.Option, retryCfg *TypedRetryConfig[M]) (M, error) {

	const defaultMaxRetries = 3

	if retryCfg == nil {
		return m.generateAndEmit(ctx, chatModel, input, opts, 1, GenerateSummaryPhasePrimary)
	}

	shouldRetry := retryCfg.ShouldRetry
	if shouldRetry == nil {
		shouldRetry = defaultTypedShouldRetry[M]
	}
	backoffFunc := retryCfg.BackoffFunc
	if backoffFunc == nil {
		backoffFunc = defaultTypedBackoffFunc[M]
	}

	maxRetries := defaultMaxRetries
	if retryCfg.MaxRetries != nil {
		maxRetries = *retryCfg.MaxRetries
	}
	totalAttempts := maxRetries + 1

	var (
		lastModelResp M
		lastErr       error
	)
	for attempt := 1; attempt <= totalAttempts; attempt++ {
		resp, err := m.generateAndEmit(ctx, chatModel, input, opts, attempt, GenerateSummaryPhasePrimary)
		if !shouldRetry(ctx, resp, err) {
			return resp, err
		}

		lastModelResp = resp
		lastErr = err
		if attempt < totalAttempts {
			select {
			case <-time.After(backoffFunc(ctx, attempt, resp, err)):
			case <-ctx.Done():
				var zero M
				return zero, ctx.Err()
			}
		}
	}

	if maxRetries > 0 {
		return lastModelResp, fmt.Errorf("exceeds max retries: %w", lastErr)
	}

	return lastModelResp, lastErr
}

// newSummaryMessage is kept for backward compatibility with external callers.
func newSummaryMessage(content string) *schema.Message {
	return newTypedSummaryMessage[*schema.Message](content)
}

// extractTextContent is kept for backward compatibility with external callers.
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

// defaultTrimUserMessage is kept for backward compatibility with external callers.
func defaultTrimUserMessage(msg adk.Message, remainingTokens int) adk.Message {
	return defaultTypedTrimUserMessage(msg, remainingTokens)
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

func (c *TypedConfig[M]) check() error {
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

func (c *TypedRetryConfig[M]) check() error {
	if c.MaxRetries != nil && *c.MaxRetries < 0 {
		return fmt.Errorf("retry.MaxRetries must be non-negative")
	}
	return nil
}

func (c *TypedFailoverConfig[M]) check() error {
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

// setContentType is kept for backward compatibility with external callers.
func setContentType(msg adk.Message, ct summarizationContentType) {
	setExtra(msg, extraKeyContentType, string(ct))
}

// getContentType is kept for backward compatibility with external callers.
func getContentType(msg adk.Message) (summarizationContentType, bool) {
	ct, ok := getExtra[string](msg, extraKeyContentType)
	if !ok {
		return "", false
	}
	return summarizationContentType(ct), true
}

// setExtra is kept for backward compatibility with external callers.
func setExtra(msg adk.Message, key string, value any) {
	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	msg.Extra[key] = value
}

// getExtra is kept for backward compatibility with external callers.
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

// shouldFailover is kept for backward compatibility with external callers.
func shouldFailover(ctx context.Context, cfg *FailoverConfig, resp adk.Message, err error) bool {
	return typedShouldFailover(ctx, cfg, resp, err)
}

func defaultBackoffFunc(_ context.Context, attempt int, _ adk.Message, _ error) time.Duration {
	return defaultBackoffDuration(attempt)
}

// ============================================================================
// Generic helper functions
// ============================================================================

func isSystemRole[M adk.MessageType](msg M) bool {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Role == schema.System
	case *schema.AgenticMessage:
		return m.Role == schema.AgenticRoleTypeSystem
	}
	panic("unreachable")
}

func isUserRole[M adk.MessageType](msg M) bool {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Role == schema.User
	case *schema.AgenticMessage:
		return m.Role == schema.AgenticRoleTypeUser
	}
	panic("unreachable")
}

func getTextContent[M adk.MessageType](msg M) string {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil {
			return ""
		}
		return extractTextContent(m)
	case *schema.AgenticMessage:
		if m == nil {
			return ""
		}
		var parts []string
		for _, block := range m.ContentBlocks {
			if block == nil {
				continue
			}
			if block.UserInputText != nil {
				parts = append(parts, block.UserInputText.Text)
			} else if block.AssistantGenText != nil {
				parts = append(parts, block.AssistantGenText.Text)
			}
		}
		return strings.Join(parts, "")
	}
	panic("unreachable")
}

func getMsgExtra[M adk.MessageType](msg M) map[string]any {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Extra
	case *schema.AgenticMessage:
		return m.Extra
	}
	panic("unreachable")
}

func setMsgExtra[M adk.MessageType](msg M, key string, value any) {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m.Extra == nil {
			m.Extra = map[string]any{}
		}
		m.Extra[key] = value
	case *schema.AgenticMessage:
		if m.Extra == nil {
			m.Extra = map[string]any{}
		}
		m.Extra[key] = value
	}
}

func makeSystemMsg[M adk.MessageType](text string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.SystemMessage(text)).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(text)).(M)
	}
	panic("unreachable")
}

func makeUserMsg[M adk.MessageType](text string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(text)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(text)).(M)
	}
	panic("unreachable")
}

func newTypedSummaryMessage[M adk.MessageType](content string) M {
	msg := makeUserMsg[M](content)
	setMsgExtra(msg, extraKeyContentType, string(contentTypeSummary))
	return msg
}

func typedGetContentType[M adk.MessageType](msg M) summarizationContentType {
	extra := getMsgExtra(msg)
	if extra == nil {
		return ""
	}
	ct, ok := extra[extraKeyContentType].(string)
	if !ok {
		return ""
	}
	return summarizationContentType(ct)
}

func typedShouldFailover[M adk.MessageType](ctx context.Context, cfg *TypedFailoverConfig[M], resp M, err error) bool {
	if cfg == nil {
		return false
	}
	if cfg.ShouldFailover == nil {
		return err != nil
	}
	return cfg.ShouldFailover(ctx, resp, err)
}

func defaultTypedShouldRetry[M adk.MessageType](_ context.Context, _ M, err error) bool {
	return err != nil
}

func defaultTypedBackoffFunc[M adk.MessageType](_ context.Context, attempt int, _ M, _ error) time.Duration {
	return defaultBackoffDuration(attempt)
}

func defaultBackoffDuration(attempt int) time.Duration {
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

func defaultTypedTrimUserMessage[M adk.MessageType](msg M, remainingTokens int) M {
	var zero M
	if remainingTokens <= 0 {
		return zero
	}

	textContent := getTextContent(msg)
	if len(textContent) == 0 {
		return zero
	}

	trimmed := truncateTextByChars(textContent)
	if trimmed == "" {
		return zero
	}

	return makeUserMsg[M](trimmed)
}

// setMsgTextContent sets the text content on a message.
func setMsgTextContent[M adk.MessageType](msg M, content string) M {
	switch m := any(msg).(type) {
	case *schema.Message:
		m.Content = content
		return any(m).(M)
	case *schema.AgenticMessage:
		m.ContentBlocks = []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: content}),
		}
		return any(m).(M)
	}
	panic("unreachable")
}

// setMsgMultipartContent builds the final summary multipart structure.
// For *schema.Message, it uses UserInputMultiContent and clears Content.
// For *schema.AgenticMessage, it sets ContentBlocks with multiple UserInputText blocks.
func setMsgMultipartContent[M adk.MessageType](msg M, summaryContent, continueInstruction string) M {
	switch m := any(msg).(type) {
	case *schema.Message:
		m.UserInputMultiContent = []schema.MessageInputPart{
			{
				Type: schema.ChatMessagePartTypeText,
				Text: summaryContent,
			},
			{
				Type: schema.ChatMessagePartTypeText,
				Text: continueInstruction,
			},
		}
		m.Content = ""
		return any(m).(M)
	case *schema.AgenticMessage:
		m.ContentBlocks = []*schema.ContentBlock{
			schema.NewContentBlock(&schema.UserInputText{Text: summaryContent}),
			schema.NewContentBlock(&schema.UserInputText{Text: continueInstruction}),
		}
		return any(m).(M)
	}
	panic("unreachable")
}
