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
	"fmt"
	"io"
	"path/filepath"
	"strings"

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
//     historical messages. Based on RootDir and ClearRetentionSuffixLimit, it offloads tool call arguments and results
//     to the Backend to reduce token usage, keeping the conversation within limits while retaining access to the
//     important information. After all, ClearPostProcess will be called, which you could save or notify current state.
type Config struct {
	// Backend is the storage backend where truncated content will be saved.
	// Required.
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
	// Required.
	// Default is /tmp, truncated content saves to /tmp/trunc/{tool_call_id}, cleared content saves to  /tmp/clear/{tool_call_id}
	RootDir string

	// MaxLengthForTrunc is the maximum allowed length of the tool output.
	// If the output exceeds this length, it will be truncated.
	// Required. Default is 50000.
	MaxLengthForTrunc int

	// TokenCounter is used to count the number of tokens in the conversation messages.
	// It is used to determine when to trigger clearing based on token usage, and token usage after clearing.
	// Required.
	TokenCounter func(ctx context.Context, msg []adk.Message, tools []*schema.ToolInfo) (int64, error)

	// MaxTokensForClear is the maximum number of tokens allowed in the conversation before clearing is attempted.
	// Required. Default is 30000.
	MaxTokensForClear int64

	// ClearRetentionSuffixLimit is the number of most recent messages to retain without clearing.
	// This ensures the model has some immediate context.
	// Optional. Default is 1.
	ClearRetentionSuffixLimit int

	// ClearPostProcess is clear post process handler.
	// Optional.
	ClearPostProcess func(ctx context.Context, state *adk.ChatModelAgentState) context.Context

	// ToolConfig is the specific configuration that applies to tools by name.
	// This configuration takes precedence over GeneralConfig for the specified tools.
	// Optional.
	ToolConfig map[string]*ToolReductionConfig
}

type ToolReductionConfig struct {
	// Backend is the storage backend where truncated content will be saved.
	// Required.
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

	// ToolResult contains the output returned by the tool.
	ToolResult *schema.ToolResult
}

type TruncResult struct {
	// NeedTrunc indicates whether the tool result should be truncated.
	NeedTrunc bool

	// ToolResult contains the result returned by the tool after trunc
	// Required when NeedTrunc is true.
	ToolResult *schema.ToolResult

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
		MaxLengthForTrunc:         t.MaxLengthForTrunc,
		TokenCounter:              t.TokenCounter,
		MaxTokensForClear:         t.MaxTokensForClear,
		ClearRetentionSuffixLimit: t.ClearRetentionSuffixLimit,
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
	if cfg.MaxLengthForTrunc == 0 {
		cfg.MaxLengthForTrunc = 50000
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
		defaultReductionConfig.TruncHandler = defaultTruncHandler(config.RootDir, config.MaxLengthForTrunc)
	}
	if !defaultReductionConfig.SkipClear {
		defaultReductionConfig.ClearHandler = defaultClearHandler(config.RootDir, config.Backend != nil, config.ReadFileToolName)
	}

	return &toolReductionMiddleware{
		config:        config,
		defaultConfig: defaultReductionConfig,
	}, nil
}

type toolReductionMiddleware struct {
	adk.BaseChatModelAgentMiddleware

	config        *Config
	defaultConfig *ToolReductionConfig
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

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		output, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return nil, err
		}

		var chunks []string
		readers := output.Copy(2)
		output = readers[0]
		origResp := readers[1]
		defer output.Close()

		for {
			var recvErr error
			chunk, recvErr := output.Recv()
			if recvErr != nil {
				if recvErr != io.EOF {
					return origResp, nil
				}
				break
			}
			chunks = append(chunks, chunk)
		}

		result := strings.Join(chunks, "")
		detail := &ToolDetail{
			ToolContext: tCtx,
			ToolArgument: &schema.ToolArgument{
				Text: argumentsInJSON,
			},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: result},
				},
			},
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
		return schema.StreamReaderFromArray([]string{truncResult.ToolResult.Parts[0].Text}), nil
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
		if msg.Role == schema.Assistant && !getMsgOffloadedFlag(msg) {
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

	// recursively handle
	tcMsgIndex := start

	for tcMsgIndex < end {
		tcMsg := state.Messages[tcMsgIndex]
		if tcMsg.Role == schema.Assistant && len(tcMsg.ToolCalls) > 0 {
			trMsgEnd := tcMsgIndex + 1 + len(tcMsg.ToolCalls)
			if trMsgEnd > len(state.Messages) {
				trMsgEnd = len(state.Messages)
			}

			j := tcMsgIndex
			for tcIndex, toolCall := range tcMsg.ToolCalls {
				j++
				if j >= end {
					break
				}
				resultMsg := state.Messages[j]
				if resultMsg.Role != schema.Tool { // unexpected
					break
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
					writeErr := cfg.Backend.Write(ctx, &filesystem.WriteRequest{
						FilePath: offloadInfo.OffloadFilePath,
						Content:  offloadInfo.OffloadContent,
					})
					if writeErr != nil {
						return ctx, state, writeErr
					}
				}

				tcMsg.ToolCalls[tcIndex].Function.Arguments = offloadInfo.ToolArgument.Text
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
			setMsgOffloadedFlag(tcMsg)
		}
		tcMsgIndex++
	}

	if t.config.ClearPostProcess != nil {
		ctx = t.config.ClearPostProcess(ctx, state)
	}

	return ctx, state, nil
}

// defaultTokenCounter estimates tokens, which treats one token as ~4 characters of text for common English text.
// github.com/tiktoken-go/tokenizer is highly recommended to replace it.
func defaultTokenCounter(_ context.Context, msgs []*schema.Message, tools []*schema.ToolInfo) (int64, error) {
	var tokens int64
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		if cached, ok := getMsgCachedToken(msg); ok {
			tokens += cached
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

		n := int64(len(sb.String()) / 4)
		setMsgCachedToken(msg, n)
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

func defaultTruncHandler(rootDir string, truncMaxLength int) func(ctx context.Context, detail *ToolDetail) (truncResult *TruncResult, err error) {
	return func(ctx context.Context, detail *ToolDetail) (offloadInfo *TruncResult, err error) {
		resultText := detail.ToolResult.Parts[0].Text
		if len(resultText) <= truncMaxLength {
			return &TruncResult{NeedTrunc: false}, nil
		}

		filePath := filepath.Join(rootDir, "trunc", detail.ToolContext.CallID)
		previewSize := truncMaxLength / 2
		truncNotify, err := pyfmt.Fmt(getTruncFmt(), map[string]any{
			"original_size": len(resultText),
			"file_path":     filePath,
			"preview_size":  previewSize,
			"preview_first": resultText[:previewSize],
			"preview_last":  resultText[len(resultText)-previewSize:],
		})
		if err != nil {
			return nil, err
		}

		return &TruncResult{
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: resultText[:truncMaxLength] + truncNotify},
				},
			},
			NeedTrunc:       true,
			NeedOffload:     true,
			OffloadFilePath: filePath,
			OffloadContent:  resultText,
		}, nil
	}
}

func defaultClearHandler(rootDir string, needOffload bool, readFileToolName string) func(ctx context.Context, detail *ToolDetail) (*ClearResult, error) {
	return func(ctx context.Context, detail *ToolDetail) (clearResult *ClearResult, err error) {
		if len(detail.ToolResult.Parts) == 0 {
			return &ClearResult{NeedClear: false}, nil
		}
		for _, part := range detail.ToolResult.Parts {
			if part.Type != schema.ToolPartTypeText {
				// brutal judge
				return nil, fmt.Errorf("default offload currently not support multimodal content type=%v", part.Type)
			}
		}

		fileName := detail.ToolContext.CallID
		if fileName == "" {
			fileName = uuid.NewString()
		}

		var nResult string
		if needOffload {
			filePath := filepath.Join(rootDir, "clear", fileName)
			nResult, err = pyfmt.Fmt(getClearWithOffloadingFmt(), map[string]any{
				"file_path":      filePath,
				"read_tool_name": readFileToolName,
			})
			if err != nil {
				return nil, err
			}
			clearResult = &ClearResult{
				ToolArgument:    detail.ToolArgument,
				NeedClear:       true,
				NeedOffload:     true,
				OffloadFilePath: filePath,
				OffloadContent:  detail.ToolResult.Parts[0].Text,
			}
		} else {
			nResult = getClearWithoutOffloadingFmt()
			clearResult = &ClearResult{
				ToolArgument: detail.ToolArgument,
				NeedClear:    true,
				NeedOffload:  false,
			}
		}

		clearResult.ToolResult = &schema.ToolResult{
			Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: nResult},
			},
		}

		return clearResult, nil
	}
}

func getMsgOffloadedFlag(msg *schema.Message) (offloaded bool) {
	return msg.Extra != nil && msg.Extra[msgReducedFlag] != nil
}

func setMsgOffloadedFlag(msg *schema.Message) {
	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	msg.Extra[msgReducedFlag] = struct{}{}
}

func getMsgCachedToken(msg *schema.Message) (int64, bool) {
	if msg.Extra == nil {
		return 0, false
	}
	tokens, ok := msg.Extra[msgReducedTokens].(int64)
	return tokens, ok
}

func setMsgCachedToken(msg *schema.Message, tokens int64) {
	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	msg.Extra[msgReducedTokens] = tokens
}

func toolResultFromMessage(msg *schema.Message) (result *schema.ToolResult, fromContent bool, err error) {
	if msg.Role != schema.Tool {
		return nil, false, fmt.Errorf("message role %s is not a tool", msg.Role)
	}
	if msg.Content != "" {
		return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: msg.Content}}}, true, nil
	}
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
