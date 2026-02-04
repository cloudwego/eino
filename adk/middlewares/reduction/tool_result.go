/*
 * Copyright 2025 CloudWeGo Authors
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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Backend defines the interface provided by the user to implement file storage.
// It is used to save the content of large tool results to a persistent storage.
type Backend interface {
	Write(context.Context, *filesystem.WriteRequest) error
}

// ToolResultConfig configures the tool result reduction middleware.
type ToolResultConfig struct {
	// ClearingTokenThreshold is the threshold for the total token count of all tool results.
	// When the sum of all tool result tokens exceeds this threshold, old tool results
	// (outside the KeepRecentTokens range) will be replaced with a placeholder.
	// Token estimation uses a simple heuristic: character count / 4.
	// optional, 20000 by default
	ClearingTokenThreshold int

	// KeepRecentTokens is the token budget for recent messages to keep intact.
	// Messages within this token budget from the end will not have their tool results cleared,
	// even if the total tool result tokens exceed the threshold.
	// optional, 40000 by default
	KeepRecentTokens int

	// ClearToolResultPlaceholder is the text to replace old tool results with.
	// optional, "[Old tool result content cleared]" by default
	ClearToolResultPlaceholder string

	// TokenCounter is a custom function to estimate token count for a message.
	// optional, uses the default counter (character count / 4) if nil
	TokenCounter func(msg *schema.Message) int

	// ExcludeTools is a list of tool names whose results should never be cleared.
	// optional
	ExcludeTools []string

	// Backend is the storage backend for offloaded tool results.
	// required
	Backend Backend

	// OffloadingTokenLimit is the token threshold for a single tool result to trigger offloading.
	// When a single tool result exceeds OffloadingTokenLimit * 4 characters, it will be
	// offloaded to the filesystem.
	// optional, 20000 by default
	OffloadingTokenLimit int

	// ReadFileToolName is the name of the tool that LLM should use to read offloaded content.
	// This name will be included in the summary message sent to the LLM.
	// optional, "read_file" by default
	//
	// NOTE: If you are using the filesystem middleware, the read_file tool name
	// is exactly "read_file", which matches the default value.
	ReadFileToolName string

	// PathGenerator generates the write path for offloaded results.
	// optional, "/large_tool_result/{ToolCallID}" by default
	PathGenerator func(ctx context.Context, input *compose.ToolInput) (string, error)
}

// NewToolResultMiddleware creates a tool result reduction middleware.
// This middleware combines two strategies to manage tool result tokens:
//
//  1. Clearing: Replaces old tool results with a placeholder when the total
//     tool result tokens exceed the threshold, while protecting recent messages.
//
//  2. Offloading: Writes large individual tool results to the filesystem and
//     returns a summary message guiding the LLM to read the full content.
//
// NOTE: If you are using the filesystem middleware (github.com/cloudwego/eino/adk/middlewares/filesystem),
// this functionality is already included by default. Set Config.WithoutLargeToolResultOffloading = true
// in the filesystem middleware if you want to use this middleware separately instead.
//
// NOTE: This middleware only handles offloading results to the filesystem.
// You MUST also provide a read_file tool to your agent, otherwise the agent
// will not be able to read the offloaded content. You can either:
//   - Use the filesystem middleware (github.com/cloudwego/eino/adk/middlewares/filesystem)
//     which provides the read_file tool automatically, OR
//   - Implement your own read_file tool that reads from the same Backend
//
// Deprecated: Use NewChatModelAgentMiddleware instead. NewChatModelAgentMiddleware returns
// a ChatModelAgentMiddleware which provides better context propagation through wrapper methods
// and is the recommended approach for new code. See ChatModelAgentMiddleware documentation
// for details on the benefits over AgentMiddleware.
func NewToolResultMiddleware(ctx context.Context, cfg *ToolResultConfig) (adk.AgentMiddleware, error) {
	bc := newClearToolResult(ctx, &ClearToolResultConfig{
		ToolResultTokenThreshold:   cfg.ClearingTokenThreshold,
		KeepRecentTokens:           cfg.KeepRecentTokens,
		ClearToolResultPlaceholder: cfg.ClearToolResultPlaceholder,
		TokenCounter:               cfg.TokenCounter,
		ExcludeTools:               cfg.ExcludeTools,
	})
	tm := newToolResultOffloading(ctx, &toolResultOffloadingConfig{
		Backend:          cfg.Backend,
		ReadFileToolName: cfg.ReadFileToolName,
		TokenLimit:       cfg.OffloadingTokenLimit,
		PathGenerator:    cfg.PathGenerator,
	})
	return adk.AgentMiddleware{
		BeforeChatModel: bc,
		WrapToolCall:    tm,
	}, nil
}

// NewChatModelAgentMiddleware creates a tool result reduction middleware as a ChatModelAgentMiddleware.
//
// This is the recommended constructor for new code. It returns a ChatModelAgentMiddleware which provides:
//   - Better context propagation through WrapInvokableToolCall and WrapStreamableToolCall methods
//   - BeforeModelRewriteState hook for modifying agent state before model invocation
//   - More flexible extension points compared to the struct-based AgentMiddleware
//
// This middleware combines two strategies to manage tool result tokens:
//
//  1. Clearing: Replaces old tool results with a placeholder when the total
//     tool result tokens exceed the threshold, while protecting recent messages.
//
//  2. Offloading: Writes large individual tool results to the filesystem and
//     returns a summary message guiding the LLM to read the full content.
//
// NOTE: If you are using the filesystem middleware (github.com/cloudwego/eino/adk/middlewares/filesystem),
// this functionality is already included by default. Set Config.WithoutLargeToolResultOffloading = true
// in the filesystem middleware if you want to use this middleware separately instead.
//
// NOTE: This middleware only handles offloading results to the filesystem.
// You MUST also provide a read_file tool to your agent, otherwise the agent
// will not be able to read the offloaded content. You can either:
//   - Use the filesystem middleware (github.com/cloudwego/eino/adk/middlewares/filesystem)
//     which provides the read_file tool automatically, OR
//   - Implement your own read_file tool that reads from the same Backend
//
// Example usage:
//
//	middleware, err := reduction.NewChatModelAgentMiddleware(ctx, &reduction.ToolResultConfig{
//	    Backend: myBackend,
//	})
//	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
//	    // ...
//	    Handlers: []adk.ChatModelAgentMiddleware{middleware},
//	})
func NewChatModelAgentMiddleware(ctx context.Context, cfg *ToolResultConfig) (adk.ChatModelAgentMiddleware, error) {
	m := &toolResultMiddleware{
		clearConfig: &ClearToolResultConfig{
			ToolResultTokenThreshold:   cfg.ClearingTokenThreshold,
			KeepRecentTokens:           cfg.KeepRecentTokens,
			ClearToolResultPlaceholder: cfg.ClearToolResultPlaceholder,
			TokenCounter:               cfg.TokenCounter,
			ExcludeTools:               cfg.ExcludeTools,
		},
		offloading: &toolResultOffloading{
			backend:       cfg.Backend,
			tokenLimit:    cfg.OffloadingTokenLimit,
			pathGenerator: cfg.PathGenerator,
			toolName:      cfg.ReadFileToolName,
			counter:       cfg.TokenCounter,
		},
	}

	if m.clearConfig.ToolResultTokenThreshold == 0 {
		m.clearConfig.ToolResultTokenThreshold = 20000
	}
	if m.clearConfig.KeepRecentTokens == 0 {
		m.clearConfig.KeepRecentTokens = 40000
	}
	if m.clearConfig.ClearToolResultPlaceholder == "" {
		m.clearConfig.ClearToolResultPlaceholder = "[Old tool result content cleared]"
	}
	if m.clearConfig.TokenCounter == nil {
		m.clearConfig.TokenCounter = defaultTokenCounter
	}

	if m.offloading.tokenLimit == 0 {
		m.offloading.tokenLimit = 20000
	}
	if m.offloading.pathGenerator == nil {
		m.offloading.pathGenerator = func(ctx context.Context, input *compose.ToolInput) (string, error) {
			return fmt.Sprintf("/large_tool_result/%s", input.CallID), nil
		}
	}
	if len(m.offloading.toolName) == 0 {
		m.offloading.toolName = "read_file"
	}
	if m.offloading.counter == nil {
		m.offloading.counter = defaultTokenCounter
	}

	return m, nil
}

type toolResultMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	clearConfig *ClearToolResultConfig
	offloading  *toolResultOffloading
}

func (m *toolResultMiddleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	err := reduceByTokens(
		state,
		m.clearConfig.ToolResultTokenThreshold,
		m.clearConfig.KeepRecentTokens,
		m.clearConfig.ClearToolResultPlaceholder,
		m.clearConfig.TokenCounter,
		m.clearConfig.ExcludeTools,
	)
	if err != nil {
		return ctx, nil, err
	}
	return ctx, state, nil
}

func (m *toolResultMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return "", err
		}
		return m.offloading.handleResult(ctx, result, &compose.ToolInput{
			Name:   tCtx.Name,
			CallID: tCtx.CallID,
		})
	}, nil
}

func (m *toolResultMiddleware) WrapStreamableToolCall(ctx context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		sr, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return nil, err
		}
		result, err := concatString(sr)
		if err != nil {
			return nil, err
		}
		result, err = m.offloading.handleResult(ctx, result, &compose.ToolInput{
			Name:   tCtx.Name,
			CallID: tCtx.CallID,
		})
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]string{result}), nil
	}, nil
}
