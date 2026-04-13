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

package reduction

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/google/uuid"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for tool reduction middleware.
// This middleware manages tool outputs in two phases to optimize context usage:
//
//  1. Truncation Phase:
//     Triggered immediately after a tool execution completes.
//     If the tool output length exceeds MaxLengthForTrunc, the full content is saved
//     to the configured Backend, and the tool output is replaced with a truncated notice.
//     This prevents immediate context overflow from a single large tool output.
//
//  2. Clear Phase:
//     Triggered before sending messages to the model (in BeforeModelRewriteState).
//     If the total token count exceeds MaxTokensForClear, the middleware iterates through
//     historical messages. Based on GenOffloadFilePath (or RootDir when GenOffloadFilePath is nil) and
//     ClearRetentionSuffixLimit, it offloads tool call arguments and results
//     to the Backend to reduce token usage, keeping the conversation within limits while retaining access to the
//     important information. After all, ClearPostProcess will be called, which you could save or notify current state.
type Config struct {
	// Backend is the storage backend where offloaded content will be saved.
	// Required when truncation is enabled (SkipTruncation is false).
	// Optional for clear-only usage. If Backend is nil, clear will still replace tool outputs with placeholders
	// but will not offload content.
	Backend Backend

	// SkipTruncation skip truncating.
	SkipTruncation bool

	// SkipClear skip clearing.
	SkipClear bool

	// ReadFileToolName is tool name used to retrieve from file.
	// After offloading content to file, you should give agent the same tool to retrieve content.
	// Required. Default is "read_file".
	ReadFileToolName string

	// RootDir root dir to save truncated/cleared content.
	// Optional.
	// Default is /tmp, truncated content saves to ${root_dir}/trunc/{tool_call_id}, cleared content saves to  ${root_dir}/clear/{tool_call_id}
	RootDir string

	// GenTruncOffloadFilePath is used to generate offload file path for truncated content.
	// When GenTruncOffloadFilePath is configured, RootDir will be ignored.
	// This is useful when tool_call_id is not unique, which may cause incorrect offload file overwrite.
	// Optional. Default is nil.
	GenTruncOffloadFilePath func(ctx context.Context, toolDetail *ToolDetail) (filePath string, err error)

	// GenClearOffloadFilePath is used to generate offload file path for truncated content.
	// When GenClearOffloadFilePath is configured, RootDir will be ignored.
	// This is useful when tool_call_id is not unique, which may cause incorrect offload file overwrite.
	// Optional. Default is nil.
	GenClearOffloadFilePath func(ctx context.Context, toolDetail *ToolDetail) (filePath string, err error)

	// MaxLengthForTrunc is the maximum allowed length of the tool output.
	// If the output exceeds this length, it will be truncated.
	// Required. Default is 50000.
	MaxLengthForTrunc int

	// TruncExcludeTools is list of tool names whose tool results should never be truncated.
	// Optional. Default is nil.
	TruncExcludeTools []string

	// TokenCounter is used to count the number of tokens in the conversation messages.
	// It is used to determine when to trigger clearing based on token usage, and token usage after clearing.
	// Required.
	TokenCounter func(ctx context.Context, msg []adk.Message, tools []*schema.ToolInfo) (int64, error)

	// MaxTokensForClear is the maximum number of tokens allowed in the conversation before clearing is attempted.
	// Required. Default is 160000.
	MaxTokensForClear int64

	// ClearRetentionSuffixLimit is the number of most recent messages to retain without clearing.
	// This ensures the model has some immediate context.
	// Optional. Default is 1.
	ClearRetentionSuffixLimit int

	// ClearAtLeastTokens ensures a minimum number of tokens is cleared each time the strategy activates.
	// If the strategy couldn't clear at least the specified amount, clear phase will not be applied.
	// This helps determine if context clearing is worth breaking your prompt cache.
	// Optional. Default is 0.
	ClearAtLeastTokens int64

	// ClearExcludeTools is list of tool names whose tool uses and results should never be cleared.
	// Optional. Default is nil.
	ClearExcludeTools []string

	// ClearMessageRewriter is a pre-process handler before clearing specific tool call and tool response pairs.
	// You can rewrite tool call and tool messages extracted as parameters and return a rearranged message slice.
	// This can be useful when you want to remove some tool calls (e.g., write_file / edit_file) and rewrite them
	// as a user message (e.g. <system-reminder>).
	// Returned messages will replace the original tool call and tool messages and will count towards ClearAtLeastTokens.
	// If returned messagesAfterRewrite is nil, tool call and tool messages will be removed.
	// Optional. Default is nil, which means no rewrite.
	ClearMessageRewriter func(ctx context.Context, toolCallMsg adk.Message, toolResponseMsgs []adk.Message) (messagesAfterRewrite []adk.Message, err error)

	// ClearPostProcess is clear post process handler.
	// Optional.
	ClearPostProcess func(ctx context.Context, state *adk.ChatModelAgentState) context.Context

	// ToolConfig is the specific configuration that applies to tools by name.
	// This configuration takes precedence over GeneralConfig for the specified tools.
	// Optional.
	ToolConfig map[string]*ToolReductionConfig
}

type ToolReductionConfig struct {
	// Backend is the storage backend where offloaded content will be saved.
	// Required when truncation is enabled for this tool (SkipTruncation is false).
	// Optional for clear-only usage. If Backend is nil, clear will still replace tool outputs with placeholders
	// but will not offload content.
	Backend Backend

	// SkipTruncation skip truncation for this tool.
	SkipTruncation bool

	// TruncHandler is used to process tool call results during truncation.
	// Optional. Default using defaultTruncHandler when SkipTruncation is false but TruncHandler is nil.
	TruncHandler func(ctx context.Context, detail *ToolDetail) (*TruncResult, error)

	// SkipClear skip clear for this tool.
	SkipClear bool

	// ClearHandler is used to process tool call arguments and results during clearing.
	// Optional. Default using defaultClearHandler when SkipClear is false but ClearHandler is nil.
	ClearHandler func(ctx context.Context, detail *ToolDetail) (*ClearResult, error)
}

type ToolDetail struct {
	// ToolContext provides metadata about the tool call (e.g., tool name, call ID).
	ToolContext *adk.ToolContext

	// ToolArgument contains the arguments passed to the tool.
	ToolArgument *schema.ToolArgument

	// ToolResult contains the output returned by the invokable tool.
	ToolResult *schema.ToolResult

	// StreamToolResult contains the output returned by the streamable tool.
	StreamToolResult *schema.StreamReader[*schema.ToolResult]
}

type TruncResult struct {
	// NeedTrunc indicates whether the tool result should be truncated.
	NeedTrunc bool

	// ToolResult contains the result returned by the invokable tool after trunc.
	// Required when NeedTrunc is true and ToolDetail.ToolResult is not nil.
	ToolResult *schema.ToolResult

	// StreamToolResult contains the output returned by the streamable tool after trunc.
	// Required when NeedTrunc is true and ToolDetail.StreamToolResult is not nil.
	StreamToolResult *schema.StreamReader[*schema.ToolResult]

	// NeedOffload indicates whether the tool result should be offloaded.
	NeedOffload bool

	// OffloadFilePath is the path where the offloaded content should be stored.
	// This path is typically relative to the backend's root.
	// Required when NeedOffload is true.
	OffloadFilePath string

	// OffloadContent is the actual content to be written to the storage backend.
	// Required when NeedOffload is true.
	OffloadContent string
}

// ClearResult contains the result of the Handler's decision.
type ClearResult struct {
	// NeedClear indicates whether the tool argument and result should be cleared.
	NeedClear bool

	// ToolArgument contains the arguments passed to the tool after clear.
	// Required when NeedClear is true.
	ToolArgument *schema.ToolArgument

	// ToolResult contains the output returned by the tool after clear.
	// Required when NeedClear is true
	ToolResult *schema.ToolResult

	// NeedOffload indicates whether the tool argument and result should be offloaded.
	NeedOffload bool

	// OffloadFilePath is the path where the offloaded content should be stored.
	// This path is typically relative to the backend's root.
	// Required when NeedOffload is true.
	OffloadFilePath string

	// OffloadContent is the actual content to be written to the storage backend.
	// Required when NeedOffload is true.
	OffloadContent string
}

func (t *Config) copyAndFillDefaults() (*Config, error) {
	cfg := &Config{
		Backend:                   t.Backend,
		SkipTruncation:            t.SkipTruncation,
		SkipClear:                 t.SkipClear,
		ReadFileToolName:          t.ReadFileToolName,
		RootDir:                   t.RootDir,
		GenTruncOffloadFilePath:   t.GenTruncOffloadFilePath,
		GenClearOffloadFilePath:   t.GenClearOffloadFilePath,
		MaxLengthForTrunc:         t.MaxLengthForTrunc,
		TruncExcludeTools:         t.TruncExcludeTools,
		TokenCounter:              t.TokenCounter,
		MaxTokensForClear:         t.MaxTokensForClear,
		ClearRetentionSuffixLimit: t.ClearRetentionSuffixLimit,
		ClearAtLeastTokens:        t.ClearAtLeastTokens,
		ClearExcludeTools:         t.ClearExcludeTools,
		ClearMessageRewriter:      t.ClearMessageRewriter,
		ClearPostProcess:          t.ClearPostProcess,
	}
	if cfg.TokenCounter == nil {
		cfg.TokenCounter = defaultTokenCounter
	}
	if cfg.ClearRetentionSuffixLimit == 0 {
		cfg.ClearRetentionSuffixLimit = 1
	}
	if cfg.ReadFileToolName == "" {
		cfg.ReadFileToolName = "read_file"
	}
	if cfg.RootDir == "" {
		cfg.RootDir = "/tmp"
	}
	if cfg.GenTruncOffloadFilePath == nil {
		cfg.GenTruncOffloadFilePath = func(ctx context.Context, toolDetail *ToolDetail) (filePath string, err error) {
			tcID := toolDetail.ToolContext.CallID
			if tcID == "" {
				tcID = uuid.NewString()
			}
			return filepath.Join(cfg.RootDir, "trunc", tcID), nil
		}
	}
	if cfg.GenClearOffloadFilePath == nil {
		cfg.GenClearOffloadFilePath = func(ctx context.Context, toolDetail *ToolDetail) (filePath string, err error) {
			tcID := toolDetail.ToolContext.CallID
			if tcID == "" {
				tcID = uuid.NewString()
			}
			return filepath.Join(cfg.RootDir, "clear", tcID), nil
		}
	}
	if cfg.MaxLengthForTrunc == 0 {
		cfg.MaxLengthForTrunc = 50000
	}
	if cfg.MaxTokensForClear == 0 {
		cfg.MaxTokensForClear = 160000
	}
	if t.ToolConfig != nil {
		cfg.ToolConfig = make(map[string]*ToolReductionConfig, len(t.ToolConfig))
		for toolName, trc := range t.ToolConfig {
			cpConfig := &ToolReductionConfig{
				Backend:        trc.Backend,
				SkipTruncation: trc.SkipTruncation,
				SkipClear:      trc.SkipClear,
				TruncHandler:   trc.TruncHandler,
				ClearHandler:   trc.ClearHandler,
			}
			cfg.ToolConfig[toolName] = cpConfig
		}
	}

	return cfg, nil
}

// New creates tool reduction middleware from config
func New(_ context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	var err error
	if config == nil {
		return nil, fmt.Errorf("config must not be nil")
	}
	if config.Backend == nil && !config.SkipTruncation {
		return nil, fmt.Errorf("backend must be set when not skipping truncation")
	}

	config, err = config.copyAndFillDefaults()
	if err != nil {
		return nil, err
	}
	defaultReductionConfig := &ToolReductionConfig{
		Backend:        config.Backend,
		SkipTruncation: config.SkipTruncation,
		SkipClear:      config.SkipClear,
	}
	if !defaultReductionConfig.SkipTruncation {
		defaultReductionConfig.TruncHandler = defaultTruncHandler(config.GenTruncOffloadFilePath, config.MaxLengthForTrunc)
	}
	if !defaultReductionConfig.SkipClear {
		defaultReductionConfig.ClearHandler = defaultClearHandler(config.GenClearOffloadFilePath, config.Backend != nil, config.ReadFileToolName)
	}
	excludeTruncTools := make(map[string]struct{}, len(config.TruncExcludeTools))
	for _, toolName := range config.TruncExcludeTools {
		excludeTruncTools[toolName] = struct{}{}
	}
	excludeClearTools := make(map[string]struct{}, len(config.ClearExcludeTools))
	for _, toolName := range config.ClearExcludeTools {
		excludeClearTools[toolName] = struct{}{}
	}

	return &toolReductionMiddleware{
		config:            config,
		defaultConfig:     defaultReductionConfig,
		excludeTruncTools: excludeTruncTools,
		excludeClearTools: excludeClearTools,
	}, nil
}

type toolReductionMiddleware struct {
	adk.BaseChatModelAgentMiddleware

	config        *Config
	defaultConfig *ToolReductionConfig

	excludeTruncTools map[string]struct{}
	excludeClearTools map[string]struct{}
}

func (t *toolReductionMiddleware) getToolConfig(toolName string, sc scene) *ToolReductionConfig {
	if t.config.ToolConfig != nil {
		if cfg, ok := t.config.ToolConfig[toolName]; ok {
			if (sc == sceneTruncation && !cfg.SkipTruncation && cfg.TruncHandler == nil) ||
				(sc == sceneClear && !cfg.SkipClear && cfg.ClearHandler == nil) {
				return t.defaultConfig
			}
			return cfg
		}
	}
	return t.defaultConfig
}

func (t *toolReductionMiddleware) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	cfg := t.getToolConfig(tCtx.Name, sceneTruncation)
	if cfg == nil || cfg.TruncHandler == nil {
		return endpoint, nil
	}
	if _, excluded := t.excludeTruncTools[tCtx.Name]; excluded {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		output, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return "", err
		}
		detail := &ToolDetail{
			ToolContext: tCtx,
			ToolArgument: &schema.ToolArgument{
				Text: argumentsInJSON,
			},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: output},
				},
			},
		}
		truncResult, err := cfg.TruncHandler(ctx, detail)
		if err != nil {
			return "", err
		}
		if !truncResult.NeedTrunc {
			return output, nil
		}
		if truncResult.NeedOffload {
			if cfg.Backend == nil {
				return "", fmt.Errorf("truncation: no backend for offload")
			}
			if err = cfg.Backend.Write(ctx, &filesystem.WriteRequest{
				FilePath: truncResult.OffloadFilePath,
				Content:  truncResult.OffloadContent,
			}); err != nil {
				return "", err
			}
		}
		return truncResult.ToolResult.Parts[0].Text, nil
	}, nil
}

func (t *toolReductionMiddleware) WrapStreamableToolCall(_ context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	cfg := t.getToolConfig(tCtx.Name, sceneTruncation)
	if cfg == nil || cfg.TruncHandler == nil {
		return endpoint, nil
	}
	if _, excluded := t.excludeTruncTools[tCtx.Name]; excluded {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		output, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return nil, err
		}

		readers := output.Copy(2)
		output = readers[0]
		origResp := readers[1]

		detail := &ToolDetail{
			ToolContext: tCtx,
			ToolArgument: &schema.ToolArgument{
				Text: argumentsInJSON,
			},
			StreamToolResult: schema.StreamReaderWithConvert(output, func(t string) (*schema.ToolResult, error) {
				return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: t}}}, nil
			}),
		}
		truncResult, err := cfg.TruncHandler(ctx, detail)
		if err != nil {
			return nil, err
		}
		if !truncResult.NeedTrunc {
			return origResp, nil
		}
		origResp.Close() // close err resp when not using it

		if truncResult.NeedOffload {
			if cfg.Backend == nil {
				return nil, fmt.Errorf("truncation: no backend for offload")
			}
			if err = cfg.Backend.Write(ctx, &filesystem.WriteRequest{
				FilePath: truncResult.OffloadFilePath,
				Content:  truncResult.OffloadContent,
			}); err != nil {
				return nil, err
			}
		}

		sr := schema.StreamReaderWithConvert(truncResult.StreamToolResult, func(t *schema.ToolResult) (string, error) {
			if t == nil || len(t.Parts) == 0 {
				return "", nil
			}
			return t.Parts[0].Text, nil
		})
		return sr, nil
	}, nil
}

func (t *toolReductionMiddleware) WrapEnhancedInvokableToolCall(ctx context.Context, endpoint adk.EnhancedInvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedInvokableToolCallEndpoint, error) {
	cfg := t.getToolConfig(tCtx.Name, sceneTruncation)
	if cfg == nil || cfg.TruncHandler == nil {
		return endpoint, nil
	}
	if _, excluded := t.excludeTruncTools[tCtx.Name]; excluded {
		return endpoint, nil
	}

	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
		output, err := endpoint(ctx, toolArgument, opts...)
		if err != nil {
			return nil, err
		}
		detail := &ToolDetail{
			ToolContext:  tCtx,
			ToolArgument: toolArgument,
			ToolResult:   output,
		}
		truncResult, err := cfg.TruncHandler(ctx, detail)
		if err != nil {
			return nil, err
		}
		if !truncResult.NeedTrunc {
			return output, nil
		}
		if truncResult.NeedOffload {
			if cfg.Backend == nil {
				return nil, fmt.Errorf("truncation: no backend for offload")
			}
			if err = cfg.Backend.Write(ctx, &filesystem.WriteRequest{
				FilePath: truncResult.OffloadFilePath,
				Content:  truncResult.OffloadContent,
			}); err != nil {
				return nil, err
			}
		}
		return truncResult.ToolResult, nil
	}, nil
}

func (t *toolReductionMiddleware) WrapEnhancedStreamableToolCall(ctx context.Context, endpoint adk.EnhancedStreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.EnhancedStreamableToolCallEndpoint, error) {
	cfg := t.getToolConfig(tCtx.Name, sceneTruncation)
	if cfg == nil || cfg.TruncHandler == nil {
		return endpoint, nil
	}
	if _, excluded := t.excludeTruncTools[tCtx.Name]; excluded {
		return endpoint, nil
	}

	return func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
		output, err := endpoint(ctx, toolArgument, opts...)
		if err != nil {
			return nil, err
		}

		readers := output.Copy(2)
		output = readers[0]
		origResp := readers[1]

		detail := &ToolDetail{
			ToolContext:      tCtx,
			ToolArgument:     toolArgument,
			StreamToolResult: output,
		}
		truncResult, err := cfg.TruncHandler(ctx, detail)
		if err != nil {
			return nil, err
		}
		if !truncResult.NeedTrunc {
			return origResp, nil
		}
		origResp.Close() // close err resp when not using it

		if truncResult.NeedOffload {
			if cfg.Backend == nil {
				return nil, fmt.Errorf("truncation: no backend for offload")
			}
			if err = cfg.Backend.Write(ctx, &filesystem.WriteRequest{
				FilePath: truncResult.OffloadFilePath,
				Content:  truncResult.OffloadContent,
			}); err != nil {
				return nil, err
			}
		}

		return truncResult.StreamToolResult, nil
	}, nil
}

func (t *toolReductionMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (
	context.Context, *adk.ChatModelAgentState, error) {

	var (
		err             error
		estimatedTokens int64
	)

	// init msg tokens
	estimatedTokens, err = t.config.TokenCounter(ctx, state.Messages, mc.Tools)
	if err != nil {
		return ctx, state, err
	}

	if estimatedTokens < t.config.MaxTokensForClear {
		return ctx, state, nil
	}

	// calc range
	var (
		start = 0
		end   = len(state.Messages)
	)
	for ; start < len(state.Messages); start++ {
		msg := state.Messages[start]
		if msg.Role == schema.Assistant && !getMsgClearedFlag(msg) {
			break
		}
	}
	retention := t.config.ClearRetentionSuffixLimit
	for ; retention > 0 && end > 0; end-- {
		msg := state.Messages[end-1]
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			retention--
			if retention == 0 {
				end--
				break
			}
		}
	}
	if start >= end {
		return ctx, state, nil
	}
	var (
		editTarget         []*schema.Message
		clearAtLeastTokens = t.config.ClearAtLeastTokens
		offloadStash       []*offloadStashItem
	)

	editTarget, end, err = t.applyClearRewrite(ctx, state, start, end, clearAtLeastTokens)
	if err != nil {
		return ctx, state, err
	}

	// recursively handle
	toolCallMsgIndex := start

	for toolCallMsgIndex < end {
		toolCallMsg := editTarget[toolCallMsgIndex]
		if toolCallMsg.Role == schema.Assistant && len(toolCallMsg.ToolCalls) > 0 {
			toolMsgIndex := toolCallMsgIndex
			for tooCallOffset, toolCall := range toolCallMsg.ToolCalls {
				toolMsgIndex++
				if toolMsgIndex >= end {
					break
				}
				resultMsg := editTarget[toolMsgIndex]
				if resultMsg.Role != schema.Tool { // unexpected
					break
				}
				if _, found := t.excludeClearTools[toolCall.Function.Name]; found {
					continue
				}
				cfg := t.getToolConfig(toolCall.Function.Name, sceneClear)
				if cfg == nil || cfg.ClearHandler == nil {
					continue
				}

				toolResult, fromContent, toolResultErr := toolResultFromMessage(resultMsg)
				if toolResultErr != nil {
					return ctx, state, toolResultErr
				}

				td := &ToolDetail{
					ToolContext: &adk.ToolContext{
						Name:   toolCall.Function.Name,
						CallID: toolCall.ID,
					},
					ToolArgument: &schema.ToolArgument{
						Text: toolCall.Function.Arguments,
					},
					ToolResult: toolResult,
				}

				offloadInfo, offloadErr := cfg.ClearHandler(ctx, td)
				if offloadErr != nil {
					return ctx, state, offloadErr
				}
				if !offloadInfo.NeedClear {
					continue
				}
				if offloadInfo.NeedOffload {
					if cfg.Backend == nil {
						return ctx, state, fmt.Errorf("clear: no backend for offload")
					}
					if clearAtLeastTokens > 0 { // delay clear offloading
						offloadStash = append(offloadStash, &offloadStashItem{
							config:      cfg,
							offloadInfo: offloadInfo,
						})
					} else { // instant clear offloading
						writeErr := cfg.Backend.Write(ctx, &filesystem.WriteRequest{
							FilePath: offloadInfo.OffloadFilePath,
							Content:  offloadInfo.OffloadContent,
						})
						if writeErr != nil {
							return ctx, state, writeErr
						}
					}
				}

				toolCallMsg.ToolCalls[tooCallOffset].Function.Arguments = offloadInfo.ToolArgument.Text
				if fromContent {
					if len(offloadInfo.ToolResult.Parts) > 0 {
						resultMsg.Content = offloadInfo.ToolResult.Parts[0].Text
					}
				} else {
					var convErr error
					resultMsg.UserInputMultiContent, convErr = offloadInfo.ToolResult.ToMessageInputParts()
					if convErr != nil {
						return ctx, state, convErr
					}
				}
			}

			// set dedup flag
			setMsgClearedFlag(toolCallMsg)
		}
		toolCallMsgIndex++
	}

	if clearAtLeastTokens > 0 {
		estimatedTokensAfterClear, err := t.config.TokenCounter(ctx, editTarget, mc.Tools)
		if err != nil {
			return ctx, state, err
		}
		tokensCleared := estimatedTokens - estimatedTokensAfterClear
		if tokensCleared < clearAtLeastTokens {
			// clear not applied, post process won't apply as well.
			return ctx, state, nil
		}
		for _, item := range offloadStash {
			writeErr := item.config.Backend.Write(ctx, &filesystem.WriteRequest{
				FilePath: item.offloadInfo.OffloadFilePath,
				Content:  item.offloadInfo.OffloadContent,
			})
			if writeErr != nil {
				return ctx, state, writeErr
			}
		}
	}

	state.Messages = editTarget // replace original state messages

	if t.config.ClearPostProcess != nil {
		ctx = t.config.ClearPostProcess(ctx, state)
	}

	return ctx, state, nil
}

func (t *toolReductionMiddleware) applyClearRewrite(ctx context.Context, state *adk.ChatModelAgentState, start, end int, clearAtLeastTokens int64) (
	[]*schema.Message, int, error) {
	var (
		editTarget      []*schema.Message
		needProcessPart []*schema.Message
	)

	editTarget = append(editTarget, state.Messages[:start]...)

	if clearAtLeastTokens > 0 {
		needProcessPart = copyMessages(state.Messages[start:end])
	} else {
		needProcessPart = state.Messages[start:end]
	}

	if t.config.ClearMessageRewriter != nil {
		var (
			rewritten  []*schema.Message
			origLength = len(needProcessPart)
		)
		for i := 0; i < len(needProcessPart); {
			msg := needProcessPart[i]
			switch msg.Role {
			case schema.System, schema.User:
				rewritten = append(rewritten, msg)
				i++
			case schema.Tool:
				i++
			case schema.Assistant:
				if len(msg.ToolCalls) == 0 {
					rewritten = append(rewritten, msg)
					i++
					continue
				}
				var (
					toolResponseMessages []adk.Message
					trStart, trEnd       = i + 1, i + len(msg.ToolCalls) + 1
				)
				if trStart >= trEnd || trStart >= len(needProcessPart) || trEnd > len(needProcessPart) {
					toolResponseMessages = nil
				} else {
					toolResponseMessages = needProcessPart[trStart:trEnd]
				}

				rewrittenMessages, rewriteErr := t.config.ClearMessageRewriter(ctx, msg, toolResponseMessages)
				if rewriteErr != nil {
					return nil, 0, rewriteErr
				}
				rewritten = append(rewritten, rewrittenMessages...)
				i = trEnd
			default: // unexpected
				return nil, 0, fmt.Errorf("[applyClearRewrite] unexpected message role: %v", msg.Role)
			}
		}
		editTarget = append(editTarget, rewritten...)
		editTarget = append(editTarget, state.Messages[end:]...)
		end = end - origLength + len(rewritten)
	} else {
		editTarget = append(editTarget, needProcessPart...)
		editTarget = append(editTarget, state.Messages[end:]...)
	}

	return editTarget, end, nil
}

type offloadStashItem struct {
	config      *ToolReductionConfig
	offloadInfo *ClearResult
}

func copyMessages(msgs []*schema.Message) []*schema.Message {
	resp := make([]*schema.Message, len(msgs))
	for i, msg := range msgs {
		copied := &schema.Message{
			Role:                     msg.Role,
			Content:                  msg.Content,
			MultiContent:             msg.MultiContent,
			UserInputMultiContent:    msg.UserInputMultiContent,
			AssistantGenMultiContent: msg.AssistantGenMultiContent,
			Name:                     msg.Name,
			ToolCalls:                nil,
			ToolCallID:               msg.ToolCallID,
			ToolName:                 msg.ToolName,
			ResponseMeta:             msg.ResponseMeta,
			ReasoningContent:         msg.ReasoningContent,
			Extra:                    nil,
		}
		if msg.ToolCalls != nil {
			copied.ToolCalls = make([]schema.ToolCall, 0, len(msg.ToolCalls))
			for _, tc := range msg.ToolCalls {
				copied.ToolCalls = append(copied.ToolCalls, tc)
			}
		}
		if msg.Extra != nil {
			copied.Extra = make(map[string]any, len(msg.Extra))
			for k, v := range msg.Extra {
				copied.Extra[k] = v
			}
		}
		resp[i] = copied
	}
	return resp
}

// defaultTokenCounter estimates tokens, which treats one token as ~4 characters of text for common English text.
// github.com/tiktoken-go/tokenizer is highly recommended to replace it.
func defaultTokenCounter(_ context.Context, msgs []*schema.Message, tools []*schema.ToolInfo) (int64, error) {
	var tokens int64
	for _, msg := range msgs {
		if msg == nil {
			continue
		}

		var sb strings.Builder
		sb.WriteString(string(msg.Role))
		sb.WriteString("\n")
		sb.WriteString(msg.ReasoningContent)
		sb.WriteString("\n")
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
		if msg.Role == schema.Assistant && len(msg.ToolCalls) > 0 {
			for _, tc := range msg.ToolCalls {
				sb.WriteString(tc.Function.Name)
				sb.WriteString("\n")
				sb.WriteString(tc.Function.Arguments)
			}
		}

		for _, mc := range msg.UserInputMultiContent {
			switch mc.Type {
			case schema.ChatMessagePartTypeText:
				sb.WriteString(mc.Text)
				sb.WriteString("\n")
			default:
				// do nothing for multi-modal content
			}
		}

		for _, mc := range msg.AssistantGenMultiContent {
			switch mc.Type {
			case schema.ChatMessagePartTypeText:
				sb.WriteString(mc.Text)
				sb.WriteString("\n")
			default:
				// do nothing for multi-modal content
			}
		}

		n := int64(len(sb.String()) / 4)
		tokens += n
	}

	for _, tl := range tools {
		tl_ := *tl
		tl_.Extra = nil
		text, err := sonic.MarshalString(tl_)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal tool info: %w", err)
		}

		tokens += int64(len(text) / 4)
	}

	return tokens, nil
}

// defaultTruncHandler applies the same truncation strategy to both non-streaming
// and streaming tool outputs.
//
// Processing steps:
//  1. Read and join tool output into a complete result:
//     - Non-streaming: use ToolResult directly.
//     - Streaming: consume the whole StreamToolResult, then concat all chunks.
//  2. If output is empty or total text length does not exceed truncMaxLength,
//     return NeedTrunc=false.
//  3. If exceeded, replace oversized text parts with truncation notices and
//     offload the full original content.
//
// Streaming-specific behavior:
//   - Truncation is not incremental. The handler waits until the entire stream is read
//     before deciding and producing output.
//   - If stream Recv() returns a non-EOF error, getJointToolResult treats it as
//     "skip processing" (needProcess=false, err=nil), so this handler returns
//     NeedTrunc=false and does not propagate that recv error.
//   - When truncation is applied to a streaming tool result, output is re-emitted as a
//     buffered single-result stream (not original chunk-by-chunk streaming semantics).
//
// If a tool requires strict incremental streaming behavior, provide a custom TruncHandler for that tool.
func defaultTruncHandler(
	genOffloadFilePathFn func(ctx context.Context, toolDetail *ToolDetail) (filePath string, err error),
	truncMaxLength int,
) func(ctx context.Context, detail *ToolDetail) (truncResult *TruncResult, err error) {

	return func(ctx context.Context, detail *ToolDetail) (offloadInfo *TruncResult, err error) {
		isStreamResult := detail.StreamToolResult != nil
		resultParts, needProcess, err := getJointToolResult(detail)
		if err != nil {
			return nil, err
		}
		if !needProcess {
			return &TruncResult{NeedTrunc: false}, nil
		}

		fullLength, textPartsCnt := 0, 0
		for _, part := range resultParts {
			if part.Type == schema.ToolPartTypeText {
				fullLength += len(part.Text)
				textPartsCnt++
			}
		}
		if textPartsCnt == 0 || fullLength <= truncMaxLength {
			return &TruncResult{NeedTrunc: false}, nil
		}

		var (
			offloadContent  = stringifyToolOutputParts(resultParts)
			truncPartLength = truncMaxLength / textPartsCnt
			previewSize     = truncPartLength / 2
		)

		filePath, err := genOffloadFilePathFn(ctx, detail)
		if err != nil {
			return nil, err
		}

		for i, part := range resultParts {
			text := part.Text
			if part.Type != schema.ToolPartTypeText ||
				len(text) < truncPartLength {
				continue
			}
			truncNotify, fmtErr := pyfmt.Fmt(getTruncFmt(), map[string]any{
				"original_size": len(part.Text),
				"file_path":     filePath,
				"preview_size":  previewSize,
				"preview_first": clampPrefixToUTF8Boundary(text, previewSize),
				"preview_last":  clampSuffixToUTF8Boundary(text, previewSize),
			})
			if fmtErr != nil {
				return nil, fmtErr
			}
			resultParts[i].Text = truncNotify
		}

		tr := &TruncResult{
			NeedTrunc:       true,
			NeedOffload:     true,
			OffloadFilePath: filePath,
			OffloadContent:  offloadContent,
		}
		if !isStreamResult {
			tr.ToolResult = &schema.ToolResult{Parts: resultParts}
		} else {
			sr, sw := schema.Pipe[*schema.ToolResult](1)
			sw.Send(&schema.ToolResult{Parts: resultParts}, nil)
			sw.Close()
			tr.StreamToolResult = sr
		}
		return tr, nil
	}
}

func clampPrefixToUTF8Boundary(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if n >= len(s) {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func clampSuffixToUTF8Boundary(s string, n int) string {
	if n <= 0 {
		return ""
	}
	l := len(s)
	if n >= l {
		return s
	}
	start := l - n
	for start < l && !utf8.RuneStart(s[start]) {
		start++
	}
	return s[start:]
}

func defaultClearHandler(
	genOffloadFilePathFn func(ctx context.Context, toolDetail *ToolDetail) (filePath string, err error),
	needOffload bool,
	readFileToolName string,
) func(ctx context.Context, detail *ToolDetail) (*ClearResult, error) {

	return func(ctx context.Context, detail *ToolDetail) (clearResult *ClearResult, err error) {
		resultParts, needProcess, err := getJointToolResult(detail)
		if err != nil {
			return nil, err
		}
		if !needProcess {
			return &ClearResult{NeedClear: false}, nil
		}

		if needOffload {
			filePath, err := genOffloadFilePathFn(ctx, detail)
			if err != nil {
				return nil, err
			}
			textPlaceHolder, fmtErr := pyfmt.Fmt(getClearWithOffloadingFmt(), map[string]any{
				"file_path":      filePath,
				"read_tool_name": readFileToolName,
			})
			if fmtErr != nil {
				return nil, fmtErr
			}

			offloadContent := stringifyToolOutputParts(resultParts)
			for i, part := range resultParts {
				if part.Type != schema.ToolPartTypeText {
					continue
				}
				resultParts[i].Text = textPlaceHolder
			}
			clearResult = &ClearResult{
				NeedClear:       true,
				ToolArgument:    detail.ToolArgument,
				ToolResult:      &schema.ToolResult{Parts: resultParts},
				NeedOffload:     true,
				OffloadFilePath: filePath,
				OffloadContent:  offloadContent,
			}
		} else {
			textPlaceHolder := getClearWithoutOffloadingFmt()
			for i, part := range resultParts {
				if part.Type != schema.ToolPartTypeText {
					continue
				}
				resultParts[i].Text = textPlaceHolder
			}
			clearResult = &ClearResult{
				NeedClear:    true,
				ToolArgument: detail.ToolArgument,
				ToolResult:   &schema.ToolResult{Parts: resultParts},
				NeedOffload:  false,
			}
		}

		return clearResult, nil
	}
}

func getJointToolResult(toolDetail *ToolDetail) (toolOutputParts []schema.ToolOutputPart, needProcess bool, err error) {
	if toolDetail.ToolResult == nil && toolDetail.StreamToolResult == nil {
		return nil, false, fmt.Errorf("ToolResult and StreamToolResult are both nil")
	}

	if toolDetail.ToolResult != nil {
		toolOutputParts = toolDetail.ToolResult.Parts
	} else {
		var toolResultChunks []*schema.ToolResult
		for {
			toolResultChunk, recvErr := toolDetail.StreamToolResult.Recv()
			if recvErr != nil {
				if recvErr == io.EOF {
					break
				}
				// return original stream reader, not sending recvErr
				return nil, false, nil
			}
			toolResultChunks = append(toolResultChunks, toolResultChunk)
		}
		toolResult, concatErr := schema.ConcatToolResults(toolResultChunks)
		if concatErr != nil {
			return nil, false, concatErr
		}
		toolOutputParts = toolResult.Parts
	}

	if len(toolOutputParts) == 0 {
		return nil, false, nil
	}

	return toolOutputParts, true, nil
}

func stringifyToolOutputParts(toolOutputParts []schema.ToolOutputPart) string {
	if len(toolOutputParts) == 0 {
		return ""
	} else if len(toolOutputParts) == 1 && toolOutputParts[0].Type == schema.ToolPartTypeText {
		return toolOutputParts[0].Text
	} else {
		b, _ := json.MarshalIndent(toolOutputParts, "", "\t")
		return string(b)
	}
}

func getMsgClearedFlag(msg *schema.Message) (offloaded bool) {
	if msg.Extra == nil {
		return false
	}
	v, ok := msg.Extra[msgClearedFlag].(bool)
	if !ok {
		return false
	}
	return v
}

func setMsgClearedFlag(msg *schema.Message) {
	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	msg.Extra[msgClearedFlag] = true
}

func toolResultFromMessage(msg *schema.Message) (result *schema.ToolResult, fromContent bool, err error) {
	if msg.Role != schema.Tool {
		return nil, false, fmt.Errorf("message role %s is not a tool", msg.Role)
	}
	if len(msg.UserInputMultiContent) > 0 {
		result = &schema.ToolResult{Parts: make([]schema.ToolOutputPart, 0, len(msg.UserInputMultiContent))}
		for _, part := range msg.UserInputMultiContent {
			top, convErr := convMessageInputPartToToolOutputPart(part)
			if convErr != nil {
				return nil, false, convErr
			}
			result.Parts = append(result.Parts, top)
		}
		return result, false, nil
	}
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: msg.Content}}}, true, nil
}

func convMessageInputPartToToolOutputPart(msgPart schema.MessageInputPart) (schema.ToolOutputPart, error) {
	switch msgPart.Type {
	case schema.ChatMessagePartTypeText:
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeText,
			Text: msgPart.Text,
		}, nil
	case schema.ChatMessagePartTypeImageURL:
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{
				MessagePartCommon: msgPart.Image.MessagePartCommon,
			},
		}, nil
	case schema.ChatMessagePartTypeAudioURL:
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeAudio,
			Audio: &schema.ToolOutputAudio{
				MessagePartCommon: msgPart.Audio.MessagePartCommon,
			},
		}, nil
	case schema.ChatMessagePartTypeVideoURL:
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeVideo,
			Video: &schema.ToolOutputVideo{
				MessagePartCommon: msgPart.Video.MessagePartCommon,
			},
		}, nil
	case schema.ChatMessagePartTypeFileURL:
		return schema.ToolOutputPart{
			Type: schema.ToolPartTypeFile,
			File: &schema.ToolOutputFile{
				MessagePartCommon: msgPart.File.MessagePartCommon,
			},
		}, nil
	default:
		return schema.ToolOutputPart{}, fmt.Errorf("unknown msg part type: %v", msgPart.Type)
	}
}
