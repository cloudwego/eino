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

type TokenCounterFunc = TypedTokenCounterFunc[*schema.Message]
type GenModelInputFunc = TypedGenModelInputFunc[*schema.Message]
type GetFailoverModelFunc = TypedGetFailoverModelFunc[*schema.Message]
type FinalizeFunc = TypedFinalizeFunc[*schema.Message]
type CallbackFunc = TypedCallbackFunc[*schema.Message]

// TypedConfig defines the configuration for the summarization middleware,
// generic over message type M.
type TypedConfig[M adk.MessageType] struct {
	// Model is the chat model used to generate summaries.
	Model model.BaseModel[M]

	// ModelOptions specifies options passed to the model when generating summaries.
	// Optional.
	ModelOptions []model.Option

	// ReusePromptCaching indicates whether the summarization call should
	// reuse the main conversation's cached prompt instead of rebuilding a
	// fresh input. For example, the input becomes
	// [sysMsg, userMsg1, assistantMsg1, ..., userInstruction] rather than
	// [innerSysInstruction, userMsg1, assistantMsg1, ..., userInstruction].
	// The current state's ToolInfos and DeferredToolInfos are also passed
	// via model.WithTools/model.WithDeferredTools so the tool envelope
	// matches the main conversation's, preserving the cache.
	//
	// Because the model still sees the original messages and tools, there is
	// a small chance that it chooses to call a tool instead of producing a
	// summary directly. It is therefore recommended to also configure Retry
	// or Failover to handle this case.
	//
	// Optional. Defaults to false.
	ReusePromptCaching bool

	// TokenCounter calculates the token count for given messages and tools.
	//
	// Parameters:
	//   - input: contains the messages and tools to count tokens for.
	//
	// Returns:
	//   - int: the total token count.
	//
	// Optional. Defaults to using the total tokens reported in the last assistant
	// message as baseline, with incremental messages estimated at ~4 chars/token.
	TokenCounter TypedTokenCounterFunc[M]

	// Trigger specifies the conditions that activate summarization.
	// Optional. Defaults to triggering when total tokens exceed 160k.
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
	// This field takes effect only when Finalize is not set.
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

	// Finalize is called after summary generation.
	// The returned messages are used as the final conversation history.
	// When set, the middleware does not perform any post-processing on the summary.
	// Use DefaultFinalize to apply the same post-processing as the default path.
	//
	// Parameters:
	//   - originalMessages: the original conversation messages before summarization.
	//   - summary: the model-generated summary message.
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
	mw := &TypedMiddleware[M]{
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

type TypedMiddleware[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	cfg *TypedConfig[M]
}

func (m *TypedMiddleware[M]) Summarize(ctx context.Context, state *adk.TypedChatModelAgentState[M]) ([]M, error) {
	beforeState := *state

	if m.cfg.EmitInternalEvents {
		err := m.emitEvent(ctx, &TypedCustomizedAction[M]{
			Type: ActionTypeBeforeSummarize,
			Before: &TypedBeforeSummarizeAction[M]{
				Messages: beforeState.Messages,
			},
		})
		if err != nil {
			return nil, err
		}
	}

	rawSummary, modelInput, err := m.summarize(ctx, beforeState)
	if err != nil {
		return nil, err
	}

	finalizeCtx := context.WithValue(ctx, ctxKeyModelInput{}, modelInput)

	finalizer := m.cfg.Finalize
	if finalizer == nil {
		finalizer = buildInternalFinalizer(m.cfg)
	}
	finalMsgs, err := finalizer(finalizeCtx, beforeState.Messages, rawSummary)
	if err != nil {
		return nil, err
	}

	if m.cfg.Callback != nil {
		afterState := beforeState
		afterState.Messages = finalMsgs
		if err = m.cfg.Callback(ctx, beforeState, afterState); err != nil {
			return nil, err
		}
	}

	if m.cfg.EmitInternalEvents {
		err = m.emitEvent(ctx, &TypedCustomizedAction[M]{
			Type: ActionTypeAfterSummarize,
			After: &TypedAfterSummarizeAction[M]{
				Messages: finalMsgs,
			},
		})
		if err != nil {
			return nil, err
		}
	}

	return finalMsgs, nil
}

func (m *TypedMiddleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}
	// Append the context-management note to the agent system prompt so the model knows
	// long conversations are summarized automatically ({{summarization prompt}}).
	note := getContextManagementInstruction()
	if strings.TrimSpace(note) != "" && !strings.Contains(runCtx.Instruction, note) {
		nRunCtx := *runCtx
		if strings.TrimSpace(nRunCtx.Instruction) == "" {
			nRunCtx.Instruction = note
		} else {
			nRunCtx.Instruction = nRunCtx.Instruction + "\n\n" + note
		}
		return ctx, &nRunCtx, nil
	}
	return ctx, runCtx, nil
}

func (m *TypedMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M],
	_ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {

	triggered, err := m.shouldSummarize(ctx, &TypedTokenCounterInput[M]{
		Messages: state.Messages,
		Tools:    state.ToolInfos,
	})
	if err != nil {
		return nil, nil, err
	}
	if !triggered {
		return ctx, state, nil
	}

	finalMsgs, err := m.Summarize(ctx, state)
	if err != nil {
		return nil, nil, err
	}

	afterState := *state
	afterState.Messages = finalMsgs

	// Emit a session mutation event so the persisted event log reflects the new
	// message state at the summarization boundary. Independent of EmitInternalEvents.
	// Error is ignored: when not in an execution context (e.g. unit tests), the
	// event simply has no consumer.
	//
	// The emitted durable payload strips runtime-generated leading system messages
	// because they are recalculated on each run via applyBeforeAgent -> GenModelInput
	// and should not be reconstructed from session history.
	msgs := stripRuntimeGeneratedLeadingSystemMessages(afterState.Messages)
	_ = adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
		SessionEventVariant: &adk.SessionEventVariant[M]{
			Event: &adk.SessionEvent[M]{
				Kind:             adk.SessionEventMessagesReplaced,
				MessagesReplaced: &msgs,
			},
		},
	})

	return ctx, &afterState, nil
}

func (m *TypedMiddleware[M]) shouldSummarize(ctx context.Context, input *TypedTokenCounterInput[M]) (bool, error) {
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

func (m *TypedMiddleware[M]) getTriggerContextTokens() int {
	const defaultTriggerContextTokens = 160000
	if m.cfg.Trigger != nil {
		return m.cfg.Trigger.ContextTokens
	}
	return defaultTriggerContextTokens
}

func (m *TypedMiddleware[M]) emitEvent(ctx context.Context, action *TypedCustomizedAction[M]) error {
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

func (m *TypedMiddleware[M]) emitGenerateSummaryEvent(ctx context.Context, attempt int, phase GenerateSummaryPhase,
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

func (m *TypedMiddleware[M]) countTokens(ctx context.Context, input *TypedTokenCounterInput[M]) (int, error) {
	if m.cfg.TokenCounter != nil {
		return m.cfg.TokenCounter(ctx, input)
	}
	return defaultTypedTokenCounter(ctx, input)
}

func defaultTypedTokenCounter[M adk.MessageType](_ context.Context, input *TypedTokenCounterInput[M]) (int, error) {
	var (
		baseTokens     int
		incrementStart int
	)

	for i := len(input.Messages) - 1; i >= 0; i-- {
		if tokens := getAssistantTotalTokens(input.Messages[i]); tokens > 0 {
			baseTokens = tokens
			incrementStart = i + 1
			break
		}
	}

	var incrementTokens int
	for _, msg := range input.Messages[incrementStart:] {
		switch m := any(msg).(type) {
		case *schema.Message:
			incrementTokens += estimateMessageTokens(m)
		case *schema.AgenticMessage:
			incrementTokens += estimateAgenticMessageTokens(m)
		}
	}

	for _, tl := range input.Tools {
		tl_ := *tl
		tl_.Extra = nil
		text, err := sonic.MarshalString(tl_)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal tool info: %w", err)
		}
		incrementTokens += estimateTokenCount(len(text))
	}

	return baseTokens + incrementTokens, nil
}

func getAssistantTotalTokens[M adk.MessageType](msg M) int {
	if msg == nil {
		return 0
	}
	switch m := any(msg).(type) {
	case *schema.Message:
		if m.Role != schema.Assistant || m.ResponseMeta == nil || m.ResponseMeta.Usage == nil {
			return 0
		}
		return m.ResponseMeta.Usage.TotalTokens
	case *schema.AgenticMessage:
		if m.Role != schema.AgenticRoleTypeAssistant || m.ResponseMeta == nil || m.ResponseMeta.TokenUsage == nil {
			return 0
		}
		return m.ResponseMeta.TokenUsage.TotalTokens
	}
	return 0
}

func estimateTokenCount(charLen int) int {
	return charLen / 4
}

func estimateTokenBytes(tokens int) int {
	return tokens * 4
}

func (m *TypedMiddleware[M]) summarize(ctx context.Context, state adk.TypedChatModelAgentState[M]) (M, []M, error) {
	var zero M
	_, contextMsgs := splitSystemAndContextMsgs(state.Messages)

	modelInput, err := m.buildSummarizationModelInput(ctx, state.Messages, contextMsgs)
	if err != nil {
		return zero, nil, err
	}

	opts := m.summarizeModelOptions(state.ToolInfos, state.DeferredToolInfos)

	rawSummary, err := m.generateWithRetry(ctx, m.cfg.Model, modelInput, opts, m.cfg.Retry)
	if typedShouldFailover(ctx, m.cfg.Failover, rawSummary, err) {
		rawSummary, modelInput, err = m.runFailover(ctx, state.Messages, modelInput, opts, rawSummary, err)
		if err != nil {
			return zero, nil, err
		}
	} else if err != nil {
		return zero, nil, fmt.Errorf("failed to generate summary: %w", err)
	}

	return rawSummary, modelInput, nil
}

func (m *TypedMiddleware[M]) summarizeModelOptions(toolInfos, deferredToolInfos []*schema.ToolInfo) []model.Option {
	if !m.cfg.ReusePromptCaching {
		return m.cfg.ModelOptions
	}
	opts := make([]model.Option, len(m.cfg.ModelOptions), len(m.cfg.ModelOptions)+2)
	copy(opts, m.cfg.ModelOptions)
	opts = append(opts, model.WithTools(toolInfos))
	opts = append(opts, model.WithDeferredTools(deferredToolInfos))
	return opts
}

func splitSystemAndContextMsgs[M adk.MessageType](msgs []M) ([]M, []M) {
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

func (m *TypedMiddleware[M]) runFailover(ctx context.Context, originalMsgs, defaultInput []M, opts []model.Option,
	lastResp M, lastErr error) (M, []M, error) {

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
			lastResp, lastErr = m.generateAndEmit(ctx, failoverModel, modelInput, opts, attempt, GenerateSummaryPhaseFailover)
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

func (m *TypedMiddleware[M]) getFailoverModel(ctx context.Context, failoverCtx *TypedFailoverContext[M], defaultInput []M) (model.BaseModel[M], []M, error) {
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

func (m *TypedMiddleware[M]) buildSummarizationModelInput(ctx context.Context, originMsgs, contextMsgs []M) ([]M, error) {
	sysInstruction, userInstruction := m.getModelInstructions()

	if m.cfg.GenModelInput != nil {
		input, err := m.cfg.GenModelInput(ctx, sysInstruction, userInstruction, originMsgs)
		if err != nil {
			return nil, fmt.Errorf("failed to generate model input: %w", err)
		}
		return input, nil
	}

	if m.cfg.ReusePromptCaching {
		input := make([]M, 0, len(originMsgs)+1)
		input = append(input, originMsgs...)
		input = append(input, userInstruction)
		return input, nil
	}

	input := make([]M, 0, len(contextMsgs)+2)
	input = append(input, sysInstruction)
	input = append(input, contextMsgs...)
	input = append(input, userInstruction)

	return input, nil
}

func (m *TypedMiddleware[M]) getModelInstructions() (M, M) {
	userInstruction := m.cfg.UserInstruction
	if userInstruction == "" {
		userInstruction = getUserSummaryInstruction()
	}

	return makeSystemMsg[M](getSystemInstruction()), makeUserMsg[M](userInstruction)
}

func buildInternalFinalizer[M adk.MessageType](cfg *TypedConfig[M]) TypedFinalizeFunc[M] {
	return func(ctx context.Context, originalMessages []M, rawSummary M) ([]M, error) {
		systemMsgs, contextMsgs := splitSystemAndContextMsgs(originalMessages)

		processed, err := postProcessSummary(ctx, &postProcessSummaryParams[M]{
			contextMsgs:    contextMsgs,
			summaryContent: getAssistantTextContent(rawSummary),
			transcriptPath: cfg.TranscriptFilePath,
		})
		if err != nil {
			return nil, err
		}

		return append(systemMsgs, processed), nil
	}
}

func getAssistantTextContent[M adk.MessageType](msg M) string {
	switch r := any(msg).(type) {
	case *schema.Message:
		if r.Role != schema.Assistant {
			return ""
		}
		var parts []string
		for _, part := range r.AssistantGenMultiContent {
			if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
		return r.Content
	case *schema.AgenticMessage:
		if r.Role != schema.AgenticRoleTypeAssistant {
			return ""
		}
		var parts []string
		for _, block := range r.ContentBlocks {
			if block != nil && block.AssistantGenText != nil {
				parts = append(parts, block.AssistantGenText.Text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

type postProcessSummaryParams[M adk.MessageType] struct {
	contextMsgs    []M
	summaryContent string
	transcriptPath string
}

func postProcessSummary[M adk.MessageType](ctx context.Context, p *postProcessSummaryParams[M]) (processed M, err error) {
	content := p.summaryContent
	if content == "" {
		return processed, fmt.Errorf("summary content is empty")
	}

	if len(p.contextMsgs) > 0 {
		newContent, err := replaceUserMessagesInSummary(ctx, &replaceUserMessagesInSummaryParams[M]{
			contextMsgs: p.contextMsgs,
			summaryText: content,
		})
		if err != nil {
			return processed, fmt.Errorf("failed to populate user messages in summary: %w", err)
		}
		content = newContent
	}

	if p.transcriptPath != "" {
		content = appendSection(content, fmt.Sprintf(getTranscriptPathInstruction(), p.transcriptPath))
	}

	content = appendSection(getSummaryPreamble(), content)
	content = appendSection(content, getContinueInstruction())

	processed = newTypedSummaryMessage[M](content)

	return processed, nil
}

type replaceUserMessagesInSummaryParams[M adk.MessageType] struct {
	contextMsgs []M
	summaryText string
}

func replaceUserMessagesInSummary[M adk.MessageType](ctx context.Context, p *replaceUserMessagesInSummaryParams[M]) (string, error) {
	var userMsgs []M
	var hasUserMsgs bool
	for _, msg := range p.contextMsgs {
		if isInternalUserMessage(msg) {
			continue
		}
		if isUserRole(msg) {
			hasUserMsgs = true
			userMsgs = append(userMsgs, msg)
		}
	}

	if !hasUserMsgs {
		return p.summaryText, nil
	}

	var selected []M
	if len(userMsgs) == 1 {
		selected = userMsgs
	} else {
		var totalTokens int
		for i := len(userMsgs) - 1; i >= 0; i-- {
			msg := userMsgs[i]

			tokens, err := defaultTypedTokenCounter(ctx, &TypedTokenCounterInput[M]{
				Messages: []M{msg},
			})
			if err != nil {
				return "", fmt.Errorf("failed to count tokens: %w", err)
			}

			remaining := preserveUserMsgsMaxTokens - totalTokens
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
		text := getUserMsgTextContent(msg)
		if text != "" {
			msgLines = append(msgLines, "    - "+text)
		}
	}
	userMsgsText := strings.Join(msgLines, "\n")

	lastMatch := findLastMatch(allUserMessagesTagRegex, p.summaryText)
	if lastMatch == nil {
		return p.summaryText, nil
	}

	var replacement string
	if len(selected) < len(userMsgs) {
		replacement = "<all_user_messages>\n" + getUserMessagesReplacedNote() + "\n" + userMsgsText + "\n</all_user_messages>"
	} else {
		replacement = "<all_user_messages>\n" + userMsgsText + "\n</all_user_messages>"
	}

	newSummaryText := p.summaryText[:lastMatch[0]] + replacement + p.summaryText[lastMatch[1]:]

	return newSummaryText, nil
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

func (m *TypedMiddleware[M]) generateAndEmit(ctx context.Context, chatModel model.BaseModel[M], input []M,
	opts []model.Option, attempt int, phase GenerateSummaryPhase) (M, error) {

	resp, err := chatModel.Generate(ctx, input, opts...)
	if emitErr := m.emitGenerateSummaryEvent(ctx, attempt, phase, resp, err); emitErr != nil {
		var zero M
		return zero, emitErr
	}
	return resp, err
}

func (m *TypedMiddleware[M]) generateWithRetry(ctx context.Context, chatModel model.BaseModel[M], input []M,
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

// ============================================================================
// Generic helper functions
// ============================================================================

const extraKeyRuntimeGeneratedSystemMessage = "_eino_adk_runtime_generated_system_message"

func isSystemRole[M adk.MessageType](msg M) bool {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Role == schema.System
	case *schema.AgenticMessage:
		return m.Role == schema.AgenticRoleTypeSystem
	}
	panic("unreachable")
}

func isMarkedRuntimeGeneratedSystemMessage[M adk.MessageType](msg M) bool {
	if !isSystemRole(msg) {
		return false
	}
	extra := getMsgExtra(msg)
	if extra == nil {
		return false
	}
	v, ok := extra[extraKeyRuntimeGeneratedSystemMessage]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

func stripRuntimeGeneratedLeadingSystemMessages[M adk.MessageType](msgs []M) []M {
	i := 0
	for i < len(msgs) && isMarkedRuntimeGeneratedSystemMessage(msgs[i]) {
		i++
	}
	out := make([]M, 0, len(msgs)-i)
	out = append(out, msgs[i:]...)
	return out
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

func messageUserTextContent(m *schema.Message) string {
	if m == nil {
		return ""
	}
	var parts []string
	for _, part := range m.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			parts = append(parts, part.Text)
		}
	}
	if len(parts) > 0 {
		return strings.Join(parts, "\n")
	}
	return m.Content
}

func getUserMsgTextContent[M adk.MessageType](msg M) string {
	switch m := any(msg).(type) {
	case *schema.Message:
		return messageUserTextContent(m)

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
			}
		}
		return strings.Join(parts, "\n")

	default:
		panic("unreachable")
	}
}

const multimodalTokenEstimate = 2000

func estimateMessageTokens(msg *schema.Message) int {
	if msg == nil {
		return 0
	}
	var totalLen int
	var multimodalTokens int

	if msg.Role == schema.Assistant {
		if len(msg.AssistantGenMultiContent) > 0 {
			hasReasoning := false
			for _, part := range msg.AssistantGenMultiContent {
				switch part.Type {
				case schema.ChatMessagePartTypeText:
					totalLen += len(part.Text)
				case schema.ChatMessagePartTypeReasoning:
					hasReasoning = true
					if part.Reasoning != nil {
						totalLen += len(part.Reasoning.Text)
					}
				case schema.ChatMessagePartTypeImageURL, schema.ChatMessagePartTypeAudioURL,
					schema.ChatMessagePartTypeVideoURL, schema.ChatMessagePartTypeFileURL:
					multimodalTokens += multimodalTokenEstimate
				}
			}
			if !hasReasoning {
				totalLen += len(msg.ReasoningContent)
			}
		} else {
			totalLen += len(msg.Content) + len(msg.ReasoningContent)
		}
		for _, tc := range msg.ToolCalls {
			totalLen += len(tc.Function.Name) + len(tc.Function.Arguments)
		}
	} else {
		if len(msg.UserInputMultiContent) > 0 {
			for _, part := range msg.UserInputMultiContent {
				switch part.Type {
				case schema.ChatMessagePartTypeText:
					totalLen += len(part.Text)
				case schema.ChatMessagePartTypeToolSearchResult:
					if part.ToolSearchResult != nil {
						for _, tl := range part.ToolSearchResult.Tools {
							totalLen += len(tl.Name) + len(tl.Desc)
							if b, err := sonic.Marshal(tl.ParamsOneOf); err == nil {
								totalLen += len(b)
							}
						}
					}
				case schema.ChatMessagePartTypeImageURL, schema.ChatMessagePartTypeAudioURL,
					schema.ChatMessagePartTypeVideoURL, schema.ChatMessagePartTypeFileURL:
					multimodalTokens += multimodalTokenEstimate
				}
			}
		} else {
			totalLen += len(msg.Content)
		}
	}

	return estimateTokenCount(totalLen) + multimodalTokens
}

func estimateAgenticMessageTokens(msg *schema.AgenticMessage) int {
	if msg == nil {
		return 0
	}
	var totalLen int
	var multimodalTokens int

	if msg.Role == schema.AgenticRoleTypeAssistant {
		for _, block := range msg.ContentBlocks {
			if block == nil {
				continue
			}
			switch block.Type {
			case schema.ContentBlockTypeAssistantGenText:
				totalLen += len(block.AssistantGenText.Text)
			case schema.ContentBlockTypeFunctionToolCall:
				totalLen += len(block.FunctionToolCall.Name) + len(block.FunctionToolCall.Arguments)
			case schema.ContentBlockTypeReasoning:
				totalLen += len(block.Reasoning.Text)
			case schema.ContentBlockTypeAssistantGenImage, schema.ContentBlockTypeAssistantGenAudio,
				schema.ContentBlockTypeAssistantGenVideo:
				multimodalTokens += multimodalTokenEstimate
			}
		}
	} else {
		for _, block := range msg.ContentBlocks {
			if block == nil {
				continue
			}
			switch block.Type {
			case schema.ContentBlockTypeUserInputText:
				totalLen += len(block.UserInputText.Text)
			case schema.ContentBlockTypeFunctionToolResult:
				for _, cb := range block.FunctionToolResult.Content {
					if cb == nil {
						continue
					}
					switch cb.Type {
					case schema.FunctionToolResultContentBlockTypeText:
						if cb.Text != nil {
							totalLen += len(cb.Text.Text)
						}
					case schema.FunctionToolResultContentBlockTypeImage, schema.FunctionToolResultContentBlockTypeAudio,
						schema.FunctionToolResultContentBlockTypeVideo, schema.FunctionToolResultContentBlockTypeFile:
						multimodalTokens += multimodalTokenEstimate
					}
				}
			case schema.ContentBlockTypeToolSearchResult:
				if block.ToolSearchFunctionToolResult != nil && block.ToolSearchFunctionToolResult.Result != nil {
					for _, tl := range block.ToolSearchFunctionToolResult.Result.Tools {
						totalLen += len(tl.Name) + len(tl.Desc)
						if b, err := sonic.Marshal(tl.ParamsOneOf); err == nil {
							totalLen += len(b)
						}
					}
				}
			case schema.ContentBlockTypeUserInputImage, schema.ContentBlockTypeUserInputFile,
				schema.ContentBlockTypeUserInputAudio, schema.ContentBlockTypeUserInputVideo:
				multimodalTokens += multimodalTokenEstimate
			}
		}
	}

	return estimateTokenCount(totalLen) + multimodalTokens
}

func getMsgExtra[M adk.MessageType](msg M) map[string]any {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m.Extra
	case *schema.AgenticMessage:
		return m.Extra
	default:
		panic("unreachable")
	}
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
	default:
		panic("unreachable")
	}
}

func makeUserMsg[M adk.MessageType](text string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(text)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(text)).(M)
	default:
		panic("unreachable")
	}
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

func isInternalUserMessage[M adk.MessageType](msg M) bool {
	return typedGetContentType(msg) == contentTypeSummary || isPreservedMessage(msg)
}

func isPreservedMessage[M adk.MessageType](msg M) bool {
	return typedGetContentType(msg) == contentTypeSkills
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

	textContent := getUserMsgTextContent(msg)
	if len(textContent) == 0 {
		return zero
	}

	trimmed := truncateTextByChars(textContent)
	if trimmed == "" {
		return zero
	}

	return makeUserMsg[M](trimmed)
}
