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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type ToolReductionMiddlewareConfig struct {
	ToolTruncation *ToolTruncation

	ToolOffload *ToolOffload
}

type ToolTruncation struct {
	ToolTruncationConfigMapping map[string]ToolTruncationConfig
}

// ToolTruncationConfig configures how a tool's result is truncated
type ToolTruncationConfig struct {
	// provide one of the configs at least
	MaxLength *int

	MaxLineLength *int
}

type ToolOffload struct {
	Tokenizer func(ctx context.Context, msg []adk.Message) (int64, error)

	ToolOffloadThreshold *ToolOffloadThresholdConfig

	// ToolOffloadConfigMapping maps tool names to their offloading configurations.
	ToolOffloadConfigMapping map[string]ToolOffloadConfig

	// ToolOffloadPostProcess processes the agent state after offloading, called once per reduction.
	ToolOffloadPostProcess func(ctx context.Context, state *adk.ChatModelAgentState) context.Context
}

type ToolOffloadThresholdConfig struct {
	// required, default 30k
	MaxTokens int64

	// messages except system
	// optional, default 5
	OffloadBatchSize int

	// optional, default 0
	RetentionSuffixLimit int
}

// ToolOffloadConfig configures how a tool's result is offloaded
type ToolOffloadConfig struct {
	// OffloadBackend is the backend interface used to store offloaded content
	OffloadBackend Backend

	OffloadHandler func(ctx context.Context, detail *ToolDetail) (*OffloadInfo, error)
}

// ToolDetail contains details about a tool's input and output
type ToolDetail struct {
	// Input is the tool's input parameters
	Input *compose.ToolInput
	// Output is the tool's execution result
	Output *compose.ToolOutput
}

type OffloadInfo struct {
	NeedOffload bool

	FilePath string

	OffloadContent string
}

// NewToolReductionMiddleware creates tool reduction middleware from config
func NewToolReductionMiddleware(_ context.Context, config *ToolReductionMiddlewareConfig) (mw adk.ChatModelAgentMiddleware, err error) {
	if config.ToolTruncation == nil && config.ToolOffload == nil {
		return mw, fmt.Errorf("at least provide one of ToolTruncationMapping or ToolOffloadMapping")
	}
	if config.ToolTruncation != nil {
		if config.ToolTruncation.ToolTruncationConfigMapping == nil {
			return mw, fmt.Errorf("ToolTruncation.ToolTruncationConfigMapping must be set")
		}
		for toolName, truncConfig := range config.ToolTruncation.ToolTruncationConfigMapping {
			if truncConfig.MaxLength == nil && truncConfig.MaxLineLength == nil {
				return mw, fmt.Errorf("trunc config for %s error: at least one of MaxLength / MaxLineLength must be set", toolName)
			}
		}
	}
	if config.ToolOffload != nil {
		to := config.ToolOffload
		if to.Tokenizer == nil || to.ToolOffloadThreshold == nil || to.ToolOffloadConfigMapping == nil {
			return mw, fmt.Errorf("ToolOffload.Tokenizer / ToolOffloadThreshold / ToolOffloadConfigMapping must be set")
		}
		for toolName, offloadConfig := range to.ToolOffloadConfigMapping {
			if offloadConfig.OffloadBackend == nil || offloadConfig.OffloadHandler == nil {
				return mw, fmt.Errorf(" offload config for %s error: ToolOffload.ToolOffloadBackend / ToolOffloadHandler must be set", toolName)
			}
		}
	}

	return &toolReductionMiddleware{config: config}, nil
}

// NewDefaultToolReductionMiddleware creates default tool reduction middleware
func NewDefaultToolReductionMiddleware(ctx context.Context, needReductionTools []tool.BaseTool) (adk.ChatModelAgentMiddleware, error) {
	truncMaxLength := 30000
	backend := filesystem.NewInMemoryBackend()
	offloadRootDir := "/tmp"

	config := &ToolReductionMiddlewareConfig{
		ToolTruncation: &ToolTruncation{
			ToolTruncationConfigMapping: make(map[string]ToolTruncationConfig, len(needReductionTools)),
		},
		ToolOffload: &ToolOffload{
			Tokenizer: defaultTokenizer,
			ToolOffloadThreshold: &ToolOffloadThresholdConfig{
				MaxTokens:        300000,
				OffloadBatchSize: 5,
			},
			ToolOffloadConfigMapping: make(map[string]ToolOffloadConfig, len(needReductionTools)),
		},
	}

	for _, t := range needReductionTools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		config.ToolTruncation.ToolTruncationConfigMapping[info.Name] = ToolTruncationConfig{MaxLength: &truncMaxLength}
		config.ToolOffload.ToolOffloadConfigMapping[info.Name] = ToolOffloadConfig{
			OffloadBackend: backend,
			OffloadHandler: defaultOffloadHandler(offloadRootDir),
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
	toolName := tCtx.Name
	config := t.config.ToolTruncation
	if config == nil || config.ToolTruncationConfigMapping == nil {
		return endpoint, nil
	}
	tc, found := config.ToolTruncationConfigMapping[tCtx.Name]
	if !found {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		output, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return "", err
		}

		detail := &ToolDetail{
			Input: &compose.ToolInput{
				Name:        toolName,
				Arguments:   argumentsInJSON,
				CallID:      tCtx.CallID,
				CallOptions: opts,
			},
			Output: &compose.ToolOutput{
				Result: output,
			},
		}
		return t.toolTruncationHandler(ctx, tc, detail), nil
	}, nil
}

func (t *toolReductionMiddleware) WrapStreamableToolCall(_ context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	toolName := tCtx.Name
	config := t.config.ToolTruncation
	if config == nil || config.ToolTruncationConfigMapping == nil {
		return endpoint, nil
	}
	tc, found := config.ToolTruncationConfigMapping[tCtx.Name]
	if !found {
		return endpoint, nil
	}

	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		output, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return nil, err
		}

		sr, sw := schema.Pipe[string](10)
		go func() {
			defer sw.Close()

			result := strings.Builder{}
			for {
				chunk, err := output.Recv()
				if err != nil {
					if err != io.EOF {
						sw.Send("", err)
					}
					break
				}
				result.WriteString(chunk)
			}

			detail := &ToolDetail{
				Input: &compose.ToolInput{
					Name:        toolName,
					Arguments:   argumentsInJSON,
					CallID:      tCtx.CallID,
					CallOptions: opts,
				},
				Output: &compose.ToolOutput{
					Result: result.String(),
				},
			}
			truncResult := t.toolTruncationHandler(ctx, tc, detail)
			sw.Send(truncResult, nil)
		}()

		return sr, nil
	}, nil
}

func (t *toolReductionMiddleware) toolTruncationHandler(ctx context.Context, config ToolTruncationConfig, detail *ToolDetail) (truncResult string) {
	if config.MaxLineLength != nil {
		sb := strings.Builder{}
		lines := strings.Split(detail.Output.Result, "\n")
		for i, line := range lines {
			if i > 0 {
				sb.WriteString("\n")
			}

			if config.MaxLength != nil {
				leftMaxLength := *config.MaxLength - sb.Len()
				if leftMaxLength < 0 {
					leftMaxLength = 0
				}
				s, truncated := truncateIfTooLong(leftMaxLength, contentTruncFmt, line)
				if truncated {
					sb.WriteString(s)
					break
				}
			}

			s, _ := truncateIfTooLong(*config.MaxLineLength, lineTruncFmt, line)
			sb.WriteString(s)
		}
		return sb.String()
	} else if config.MaxLength != nil {
		truncResult, _ = truncateIfTooLong(*config.MaxLength, contentTruncFmt, detail.Output.Result)
		return truncResult
	} else {
		return detail.Output.Result
	}
}

func truncateIfTooLong(maxLength int, format, content string) (string, bool) {
	truncatedMsg := fmt.Sprintf(format, len(content))
	if maxLength+len(truncatedMsg) < len(content) {
		return content[:maxLength] + truncatedMsg, true
	}
	return content, false
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
	estimatedTokens, err = offloadConfig.Tokenizer(ctx, state.Messages)
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
			beforeTokens, err := offloadConfig.Tokenizer(ctx, state.Messages[tcMsgIndex:trMsgEnd])
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
				tc, found := offloadConfig.ToolOffloadConfigMapping[toolCall.Function.Name]
				if !found {
					continue
				}

				td := &ToolDetail{
					Input: &compose.ToolInput{
						Name:      toolCall.Function.Name,
						Arguments: toolCall.Function.Arguments,
						CallID:    toolCall.ID,
					},
					Output: &compose.ToolOutput{
						Result: resultMsg.Content,
					},
				}
				offloadInfo, offloadErr := tc.OffloadHandler(ctx, td)
				if offloadErr != nil {
					return ctx, state, offloadErr
				}
				if !offloadInfo.NeedOffload {
					continue
				}

				writeErr := tc.OffloadBackend.Write(ctx, &filesystem.WriteRequest{
					FilePath: offloadInfo.FilePath,
					Content:  offloadInfo.OffloadContent,
				})
				if writeErr != nil {
					return ctx, state, writeErr
				}

				tcMsg.ToolCalls[tcIndex].Function.Arguments = td.Input.Arguments
				resultMsg.Content = td.Output.Result
			}

			// set dedup flag
			setMsgOffloadedFlag(tcMsg)

			// calc tool_call msg tokens
			afterTokens, err := offloadConfig.Tokenizer(ctx, state.Messages[tcMsgIndex:trMsgEnd])
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

// defaultTokenizer estimates tokens, which treats one token as ~4 characters of text for common English text.
// github.com/tiktoken-go/tokenizer is highly recommended to replace it.
func defaultTokenizer(_ context.Context, msgs []*schema.Message) (int64, error) {
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
		fileName := detail.Input.CallID
		if fileName == "" {
			fileName = uuid.NewString()
		}
		filePath := filepath.Join(rootDir, fileName)
		nResult := fmt.Sprintf(toolOffloadResultFmt, filePath)
		offloadInfo := &OffloadInfo{
			NeedOffload:    true,
			FilePath:       filePath,
			OffloadContent: detail.Output.Result,
		}
		detail.Output.Result = nResult

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
