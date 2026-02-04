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

// ToolReductionMiddlewareConfig is the configuration for the tool reduction middleware.
type ToolReductionMiddlewareConfig struct {
	// Backend is the storage backend where truncated content will be saved.
	Backend Backend

	// ToolTruncation configures the truncation strategy for tool outputs.
	// If nil, no truncation will be applied.
	ToolTruncation *ToolTruncation

	// ToolOffload configures the offloading strategy for tool outputs.
	// If nil, no offloading will be applied.
	ToolOffload *ToolOffload
}

// ToolTruncation holds configuration for tool output truncation.
type ToolTruncation struct {
	// ReadFileToolName is used in truncated content to tell agent how to retrieve original content.
	ReadFileToolName string

	// ToolConfigs maps tool names to their specific truncation configurations.
	// The key is the tool name.
	ToolConfigs map[string]ToolTruncationConfig
}

// ToolTruncationConfig configures how a tool's result is truncated.
type ToolTruncationConfig struct {
	// RootDir root dir to save truncated content, file name is {tool_call_id}.
	// Default is /tmp/trunc
	RootDir string

	// MaxLength is the maximum allowed length of the tool output.
	// If the output exceeds this length, it will be truncated.
	MaxLength int
}

// ToolOffload holds configuration for tool output offloading.
type ToolOffload struct {
	// TokenCounter is used to count the number of tokens in the conversation messages.
	// It is required to determine when to trigger offloading based on token usage.
	TokenCounter func(ctx context.Context, msg []adk.Message) (int64, error)

	// ToolOffloadThreshold defines the thresholds for triggering the offloading process.
	ToolOffloadThreshold *ToolOffloadThresholdConfig

	// ToolConfigs maps tool names to their specific offloading configurations.
	// The key is the tool name.
	ToolConfigs map[string]ToolOffloadConfig

	// ToolOffloadPostProcess is an optional callback function to process the agent state after offloading.
	// It is called once per reduction cycle if offloading occurred.
	ToolOffloadPostProcess func(ctx context.Context, state *adk.ChatModelAgentState) context.Context
}

// ToolOffloadThresholdConfig defines the conditions under which tool offloading is triggered.
type ToolOffloadThresholdConfig struct {
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

// ToolOffloadConfig configures how a specific tool's result is offloaded.
type ToolOffloadConfig struct {
	// Handler determines the content to offload and the file path for a given tool execution.
	// It receives name, call_id, input and output of this tool call, which you could modify them in-place.
	// It returns an OffloadInfo struct containing the decision and data.
	// Different from ToolTruncationConfig, you need to set OffloadInfo.FilePath to tell Backend full path to write.
	Handler func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error)
}

// ToolDetail contains detailed information about a tool execution, used by the Handler.
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

// NewToolReductionMiddleware creates tool reduction middleware from config
func NewToolReductionMiddleware(_ context.Context, config *ToolReductionMiddlewareConfig) (mw adk.ChatModelAgentMiddleware, err error) {
	if config.ToolTruncation == nil && config.ToolOffload == nil {
		return mw, fmt.Errorf("at least provide one of ToolTruncationMapping or ToolOffloadMapping")
	}
	if config.Backend == nil {
		return mw, fmt.Errorf("at least provide Backend")
	}
	if config.ToolTruncation != nil {
		if config.ToolTruncation.ToolConfigs == nil {
			return mw, fmt.Errorf("ToolTruncation.ToolConfigs must be set")
		}
		if config.ToolTruncation.ReadFileToolName == "" {
			return mw, fmt.Errorf("ToolTruncation.ReadFileToolName must be set")
		}
		for toolName, truncConfig := range config.ToolTruncation.ToolConfigs {
			if truncConfig.RootDir == "" {
				truncConfig.RootDir = "/tmp/trunc"
			}
			if truncConfig.MaxLength == 0 {
				return mw, fmt.Errorf("trunc config for %s error: MaxLength must be set", toolName)
			}
		}
	}
	if config.ToolOffload != nil {
		to := config.ToolOffload
		if to.TokenCounter == nil || to.ToolOffloadThreshold == nil || to.ToolConfigs == nil {
			return mw, fmt.Errorf("ToolOffload.TokenCounter / ToolOffloadThreshold / ToolConfigs must be set")
		}
		for toolName, offloadConfig := range to.ToolConfigs {
			if offloadConfig.Handler == nil {
				return mw, fmt.Errorf(" offload config for %s error: ToolOffloadHandler must be set", toolName)
			}
		}
	}

	return &toolReductionMiddleware{config: config}, nil
}

// NewDefaultToolReductionMiddleware creates default tool reduction middleware
func NewDefaultToolReductionMiddleware(ctx context.Context, needReductionTools []tool.BaseTool) (adk.ChatModelAgentMiddleware, error) {
	truncMaxLength := 30000
	backend := filesystem.NewInMemoryBackend()
	truncRootDir := "/tmp/trunc"
	offloadRootDir := "/tmp/offload"

	config := &ToolReductionMiddlewareConfig{
		Backend: backend,
		ToolTruncation: &ToolTruncation{
			ReadFileToolName: "read_file",
			ToolConfigs:      make(map[string]ToolTruncationConfig, len(needReductionTools)),
		},
		ToolOffload: &ToolOffload{
			TokenCounter: defaultTokenCounter,
			ToolOffloadThreshold: &ToolOffloadThresholdConfig{
				MaxTokens:        200000,
				OffloadBatchSize: 5,
			},
			ToolConfigs: make(map[string]ToolOffloadConfig, len(needReductionTools)),
		},
	}

	for _, t := range needReductionTools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		config.ToolTruncation.ToolConfigs[info.Name] = ToolTruncationConfig{
			RootDir:   truncRootDir,
			MaxLength: truncMaxLength,
		}
		config.ToolOffload.ToolConfigs[info.Name] = ToolOffloadConfig{
			Handler: defaultOffloadHandler(offloadRootDir),
		}
	}

	return &toolReductionMiddleware{
		BaseChatModelAgentMiddleware: adk.BaseChatModelAgentMiddleware{},
		config:                       config,
	}, nil
}

type toolReductionMiddleware struct {
	adk.BaseChatModelAgentMiddleware

	config *ToolReductionMiddlewareConfig
}

func (t *toolReductionMiddleware) WrapInvokableToolCall(_ context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	config := t.config.ToolTruncation
	if config == nil || config.ToolConfigs == nil {
		return endpoint, nil
	}
	tc, found := config.ToolConfigs[tCtx.Name]
	if !found {
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
		truncatedResult, err := t.toolTruncationHandler(ctx, tc, detail)
		if err != nil {
			return "", err
		}
		return truncatedResult, nil
	}, nil
}

func (t *toolReductionMiddleware) WrapStreamableToolCall(_ context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	config := t.config.ToolTruncation
	if config == nil || config.ToolConfigs == nil {
		return endpoint, nil
	}
	tc, found := config.ToolConfigs[tCtx.Name]
	if !found {
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
		truncResult, err := t.toolTruncationHandler(ctx, tc, detail)
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]string{truncResult}), nil
	}, nil
}

func (t *toolReductionMiddleware) toolTruncationHandler(ctx context.Context, config ToolTruncationConfig, detail *ToolDetail) (truncResult string, err error) {
	resultText := detail.ToolResult.Parts[0].Text
	if len(resultText) <= config.MaxLength {
		return resultText, nil
	}

	filePath := filepath.Join(config.RootDir, detail.ToolContext.CallID)
	truncatedMsg, err := pyfmt.Fmt(getContentTruncFmt(), map[string]any{
		"removed_count":       len(resultText) - config.MaxLength,
		"file_path":           filePath,
		"read_file_tool_name": t.config.ToolTruncation.ReadFileToolName,
	})
	if err != nil {
		return "", err
	}

	truncResult = resultText[:config.MaxLength] + truncatedMsg
	err = t.config.Backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: filepath.Join(config.RootDir, detail.ToolContext.CallID),
		Content:  resultText,
	})
	if err != nil {
		return "", err
	}
	return truncResult, nil
}

func (t *toolReductionMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if t.config.ToolOffload == nil {
		return ctx, state, nil
	}

	var (
		err             error
		estimatedTokens int64
		offloadConfig   = t.config.ToolOffload
	)

	// init msg tokens
	estimatedTokens, err = offloadConfig.TokenCounter(ctx, state.Messages)
	if err != nil {
		return ctx, state, err
	}

	if estimatedTokens < offloadConfig.ToolOffloadThreshold.MaxTokens {
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
	retention := offloadConfig.ToolOffloadThreshold.RetentionSuffixLimit
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
			beforeTokens, err := offloadConfig.TokenCounter(ctx, state.Messages[tcMsgIndex:trMsgEnd])
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
				tc, found := offloadConfig.ToolConfigs[toolCall.Function.Name]
				if !found {
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

				offloadInfo, offloadErr := tc.Handler(ctx, td)
				if offloadErr != nil {
					return ctx, state, offloadErr
				}
				if !offloadInfo.NeedOffload {
					continue
				}

				writeErr := t.config.Backend.Write(ctx, &filesystem.WriteRequest{
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
			afterTokens, err := offloadConfig.TokenCounter(ctx, state.Messages[tcMsgIndex:trMsgEnd])
			if err != nil {
				return ctx, state, err
			}
			estimatedTokens -= beforeTokens - afterTokens
			batchCount++
		}

		if batchCount == offloadConfig.ToolOffloadThreshold.OffloadBatchSize {
			if estimatedTokens < offloadConfig.ToolOffloadThreshold.MaxTokens {
				break
			} else {
				batchCount = 0
			}
		}
		tcMsgIndex++
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
