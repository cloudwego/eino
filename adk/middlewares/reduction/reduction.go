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

	"github.com/google/uuid"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// ToolReductionMiddlewareConfig is the configuration for tool reduction middleware.
// This middleware manages tool outputs in two phases to optimize context usage:
//
//  1. Truncation Phase:
//     Triggered immediately after a tool execution completes.
//     If the tool output length exceeds TruncationConfig.MaxLengthForTrunc, the full content is saved
//     to the configured Backend, and the output in the message is replaced with a truncation notice.
//     This prevents immediate context overflow from a single large tool output.
//
//  2. Clear Phase:
//     Triggered before sending messages to the model (in BeforeModelRewriteState).
//     If the total token count exceeds ClearThreshold.MaxTokens, the middleware iterates through
//     historical messages. Based on ClearConfig, it offloads tool call arguments or results to the Backend
//     to reduce token usage, keeping the conversation within limits while retaining access to the data.
type ToolReductionMiddlewareConfig struct {
	// GeneralConfig is the general configuration that applies to all tools.
	// At least one of GeneralConfig or ToolConfig must be provided.
	// If both are configured, ToolConfig takes precedence for specific tools, and GeneralConfig serves as a fallback for tools not found in ToolConfig.
	// If only GeneralConfig is configured, it applies to all tools.
	GeneralConfig *ToolReductionConfig

	// ToolConfig is the specific configuration that applies to tools by name.
	// At least one of GeneralConfig or ToolConfig must be provided.
	// If both are configured, this configuration takes precedence over GeneralConfig for the specified tools.
	// If only ToolConfig is configured, only the specified tools have configuration.
	ToolConfig map[string]*ToolReductionConfig

	// The following configurations are used when clearing tool context.
	// When any ClearConfig in GeneralConfig or ToolConfig is not empty, these configurations must be set

	// TokenCounter is used to count the number of tokens in the conversation messages.
	// It is used to determine when to trigger clearing based on token usage, and token usage after clearing.
	// Required.
	TokenCounter func(ctx context.Context, msg []adk.Message) (int64, error)

	// ClearThreshold is clear threshold config.
	// Required.
	ClearThreshold *ClearThresholdConfig

	// ClearPostProcess is clear post process handler.
	// Optional.
	ClearPostProcess func(ctx context.Context, state *adk.ChatModelAgentState) context.Context
}

type ToolReductionConfig struct {
	// Backend is the storage backend where truncated content will be saved.
	// Required.
	Backend Backend

	// TruncationConfig is the truncation config.
	// If not set, skip the truncation phase of this tool.
	// Optional.
	TruncationConfig *TruncationConfig

	// ClearConfig is the clear config.
	// If not set, skip the clear phase of this tool.
	// Optional.
	ClearConfig *ClearConfig
}

type TruncationConfig struct {
	// ReadFileToolName is optional, default is "read_file".
	ReadFileToolName string

	// RootDir root dir to save truncated content, file name is {tool_call_id}.
	// Default is /tmp/trunc
	RootDir string

	// MaxLengthForTrunc is the maximum allowed length of the tool output.
	// If the output exceeds this length, it will be truncated.
	MaxLengthForTrunc int
}

type ClearConfig struct {
	// Handler is used to process tool call arguments and results during clearing.
	Handler func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error)
}

type ClearThresholdConfig struct {
	// MaxTokens is the maximum number of tokens allowed in the conversation before offloading is attempted.
	// Default is typically around 30k if not specified.
	MaxTokens int64

	// OffloadBatchSize is the number of tool calls to process in a single offloading batch.
	// Default is 5.
	OffloadBatchSize int

	// RetentionSuffixLimit is the number of most recent messages to retain without offloading.
	// This ensures the model has some immediate context. Default is 0.
	RetentionSuffixLimit int
}

type ToolDetail struct {
	// ToolContext provides metadata about the tool call (e.g., tool name, call ID).
	ToolContext *adk.ToolContext

	// ToolArgument contains the arguments passed to the tool.
	ToolArgument *schema.ToolArgument

	// ToolResult contains the output returned by the tool.
	ToolResult *schema.ToolResult
}

// OffloadInfo contains the result of the Handler's decision.
type OffloadInfo struct {
	// NeedOffload indicates whether the tool output should be offloaded.
	NeedOffload bool

	// FilePath is the path where the offloaded content should be stored.
	// This path is typically relative to the backend's root.
	FilePath string

	// OffloadContent is the actual content to be written to the storage backend.
	OffloadContent string
}

// New creates tool reduction middleware from config
func New(_ context.Context, config *ToolReductionMiddlewareConfig) (adk.ChatModelAgentMiddleware, error) {
	// using default config when nothing provided
	if config == nil || (config.TokenCounter == nil && config.ClearThreshold == nil && config.GeneralConfig == nil && config.ToolConfig == nil) {
		backend := filesystem.NewInMemoryBackend()
		truncOffloadDir := "/tmp/trunc"
		clearOffloadDir := "/tmp/clear"

		cfg := &ToolReductionMiddlewareConfig{
			TokenCounter: defaultTokenCounter,
			ClearThreshold: &ClearThresholdConfig{
				MaxTokens:            30000,
				OffloadBatchSize:     5,
				RetentionSuffixLimit: 0,
			},
			ClearPostProcess: nil,
			GeneralConfig: &ToolReductionConfig{
				Backend: backend,
				TruncationConfig: &TruncationConfig{
					ReadFileToolName:  "read_file",
					RootDir:           truncOffloadDir,
					MaxLengthForTrunc: 50000,
				},
				ClearConfig: &ClearConfig{
					Handler: defaultOffloadHandler(clearOffloadDir),
				},
			},
			ToolConfig: nil,
		}
		return &toolReductionMiddleware{config: cfg}, nil
	}
	if config.GeneralConfig == nil && config.ToolConfig == nil {
		return nil, fmt.Errorf("one of GeneralConfig or ToolConfig must be set")
	}
	checkClearConfig := func() bool {
		return config.ClearThreshold != nil && config.TokenCounter == nil
	}
	if config.GeneralConfig != nil {
		if config.GeneralConfig.Backend == nil {
			return nil, fmt.Errorf("GeneralConfig.Backend must be set")
		}
		if config.GeneralConfig.ClearConfig != nil && !checkClearConfig() {
			return nil, fmt.Errorf("TokenCounter and ClearThreshold must be set when ClearConfig is not nil")
		}
	}
	for toolName, cfg := range config.ToolConfig {
		if cfg == nil {
			continue
		}
		if cfg.Backend == nil {
			return nil, fmt.Errorf("%s: ToolConfig.Backend must be set", toolName)
		}
		if cfg.ClearConfig != nil && !checkClearConfig() {
			return nil, fmt.Errorf("%s: TokenCounter and ClearThreshold must be set when ClearConfig is not nil", toolName)
		}
	}

	return &toolReductionMiddleware{config: config}, nil
}

type toolReductionMiddleware struct {
	adk.BaseChatModelAgentMiddleware

	config *ToolReductionMiddlewareConfig
}

func (t *toolReductionMiddleware) getToolConfig(toolName string) *ToolReductionConfig {
	if t.config.ToolConfig != nil {
		if cfg, ok := t.config.ToolConfig[toolName]; ok {
			return cfg
		}
	}
	return t.config.GeneralConfig
}

func (t *toolReductionMiddleware) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	cfg := t.getToolConfig(tCtx.Name)
	if cfg == nil || cfg.TruncationConfig == nil || cfg.TruncationConfig.MaxLengthForTrunc <= 0 {
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
				TextArgument: argumentsInJSON,
			},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: output},
				},
			},
		}
		truncatedResult, err := t.toolTruncationHandler(ctx, cfg, detail)
		if err != nil {
			return "", err
		}
		return truncatedResult, nil
	}, nil
}

func (t *toolReductionMiddleware) WrapStreamableToolCall(_ context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	cfg := t.getToolConfig(tCtx.Name)
	if cfg == nil || cfg.TruncationConfig == nil || cfg.TruncationConfig.MaxLengthForTrunc <= 0 {
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
		errResp := readers[1]
		defer output.Close()

		for {
			chunk, err := output.Recv()
			if err != nil {
				if err != io.EOF {
					return errResp, nil
				}
				break
			}
			chunks = append(chunks, chunk)
		}
		errResp.Close() // close err resp when not using it

		result := strings.Join(chunks, "")
		detail := &ToolDetail{
			ToolContext: tCtx,
			ToolArgument: &schema.ToolArgument{
				TextArgument: argumentsInJSON,
			},
			ToolResult: &schema.ToolResult{
				Parts: []schema.ToolOutputPart{
					{Type: schema.ToolPartTypeText, Text: result},
				},
			},
		}
		truncResult, err := t.toolTruncationHandler(ctx, cfg, detail)
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]string{truncResult}), nil
	}, nil
}

func (t *toolReductionMiddleware) toolTruncationHandler(ctx context.Context, config *ToolReductionConfig, detail *ToolDetail) (truncResult string, err error) {
	if config.Backend == nil {
		return "", fmt.Errorf("backend is required for truncation")
	}

	truncConfig := config.TruncationConfig
	resultText := detail.ToolResult.Parts[0].Text
	if len(resultText) <= truncConfig.MaxLengthForTrunc {
		return resultText, nil
	}

	filePath := filepath.Join(truncConfig.RootDir, detail.ToolContext.CallID)
	truncatedMsg, err := pyfmt.Fmt(getContentTruncFmt(), map[string]any{
		"removed_count":       len(resultText) - truncConfig.MaxLengthForTrunc,
		"file_path":           filePath,
		"read_file_tool_name": truncConfig.ReadFileToolName,
	})
	if err != nil {
		return "", err
	}

	truncResult = resultText[:truncConfig.MaxLengthForTrunc] + truncatedMsg
	err = config.Backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: filePath,
		Content:  resultText,
	})
	if err != nil {
		return "", err
	}
	return truncResult, nil
}

func (t *toolReductionMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if t.config.ClearThreshold == nil {
		return ctx, state, nil
	}

	var (
		err             error
		estimatedTokens int64
	)

	// init msg tokens
	estimatedTokens, err = t.config.TokenCounter(ctx, state.Messages)
	if err != nil {
		return ctx, state, err
	}

	if estimatedTokens < t.config.ClearThreshold.MaxTokens {
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
	retention := t.config.ClearThreshold.RetentionSuffixLimit
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
	batchCount := 0

	for tcMsgIndex < end {
		tcMsg := state.Messages[tcMsgIndex]
		if tcMsg.Role == schema.Assistant && len(tcMsg.ToolCalls) > 0 {
			trMsgEnd := tcMsgIndex + 1 + len(tcMsg.ToolCalls)
			if trMsgEnd > len(state.Messages) {
				trMsgEnd = len(state.Messages)
			}
			beforeTokens, err := t.config.TokenCounter(ctx, state.Messages[tcMsgIndex:trMsgEnd])
			if err != nil {
				return ctx, state, nil
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

				cfg := t.getToolConfig(toolCall.Function.Name)
				if cfg == nil || cfg.ClearConfig == nil || cfg.ClearConfig.Handler == nil {
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
						TextArgument: toolCall.Function.Arguments,
					},
					ToolResult: toolResult,
				}

				offloadInfo, offloadErr := cfg.ClearConfig.Handler(ctx, td)
				if offloadErr != nil {
					return ctx, state, offloadErr
				}
				if !offloadInfo.NeedOffload {
					continue
				}

				if cfg.Backend == nil {
					return ctx, state, fmt.Errorf("backend is required for offloading tool %s", toolCall.Function.Name)
				}

				writeErr := cfg.Backend.Write(ctx, &filesystem.WriteRequest{
					FilePath: offloadInfo.FilePath,
					Content:  offloadInfo.OffloadContent,
				})
				if writeErr != nil {
					return ctx, state, writeErr
				}

				tcMsg.ToolCalls[tcIndex].Function.Arguments = td.ToolArgument.TextArgument
				if fromContent {
					if len(td.ToolResult.Parts) > 0 {
						resultMsg.Content = td.ToolResult.Parts[0].Text
					}
				} else {
					var convErr error
					resultMsg.UserInputMultiContent, convErr = td.ToolResult.ToMessageInputParts()
					if convErr != nil {
						return ctx, state, convErr
					}
				}
			}

			// set dedup flag
			setMsgOffloadedFlag(tcMsg)

			// calc tool_call msg tokens
			afterTokens, err := t.config.TokenCounter(ctx, state.Messages[tcMsgIndex:trMsgEnd])
			if err != nil {
				return ctx, state, err
			}
			estimatedTokens -= beforeTokens - afterTokens
			batchCount++
		}

		if batchCount == t.config.ClearThreshold.OffloadBatchSize {
			if estimatedTokens < t.config.ClearThreshold.MaxTokens {
				break
			} else {
				batchCount = 0
			}
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
func defaultTokenCounter(_ context.Context, msgs []*schema.Message) (int64, error) {
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

	return tokens, nil
}

func defaultOffloadHandler(rootDir string) func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error) {
	return func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error) {
		fileName := detail.ToolContext.CallID
		if fileName == "" {
			fileName = uuid.NewString()
		}
		filePath := filepath.Join(rootDir, fileName)
		nResult := fmt.Sprintf(getToolOffloadResultFmt(), filePath)
		if len(detail.ToolResult.Parts) == 0 || detail.ToolResult.Parts[0].Type != schema.ToolPartTypeText {
			// brutal judge
			return nil, fmt.Errorf("default offload currently not support multimodal content")
		}
		offloadInfo := &OffloadInfo{
			NeedOffload:    true,
			FilePath:       filePath,
			OffloadContent: detail.ToolResult.Parts[0].Text,
		}
		detail.ToolResult.Parts[0].Text = nResult

		return offloadInfo, nil
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
