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

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// ClearToolResultConfig configures the tool result clearing middleware.
// This middleware clears old tool results when their total token count exceeds a threshold,
// while protecting recent messages within a token budget.
type ClearToolResultConfig struct {
	// ToolResultTokenThreshold is the threshold for total tool result tokens.
	// When the sum of all tool result tokens exceeds this threshold, old tool results
	// (outside the KeepRecentTokens range) will be replaced with a placeholder.
	// Token estimation uses a simple heuristic: character count / 4.
	// If 0, defaults to 20000.
	ToolResultTokenThreshold int

	// KeepRecentTokens is the token budget for recent messages to keep intact.
	// Messages within this token budget from the end will not have their tool results cleared,
	// even if the total tool result tokens exceed the threshold.
	// If 0, defaults to 40000.
	KeepRecentTokens int

	// ClearToolResultPlaceholder is the text to replace old tool results with.
	// If empty, defaults to "[Old tool result content cleared]".
	ClearToolResultPlaceholder string

	// TokenCounter is a custom function to estimate token count for a message.
	// If nil, uses the default counter (character count / 4).
	TokenCounter func(msg *schema.Message) int

	// ExcludeTools is a list of tool names whose results should never be cleared.
	ExcludeTools []string
}

// NewClearToolResult creates a new middleware that clears old tool results
// based on token thresholds while protecting recent messages.
func NewClearToolResult(ctx context.Context, config *ClearToolResultConfig) (adk.AgentMiddleware, error) {
	if config == nil {
		config = &ClearToolResultConfig{}
	}

	// Set defaults
	toolResultTokenThreshold := config.ToolResultTokenThreshold
	if toolResultTokenThreshold == 0 {
		toolResultTokenThreshold = 20000
	}

	keepRecentTokens := config.KeepRecentTokens
	if keepRecentTokens == 0 {
		keepRecentTokens = 40000
	}

	placeholder := config.ClearToolResultPlaceholder
	if placeholder == "" {
		placeholder = "[Old tool result content cleared]"
	}

	// Set token estimator
	counter := config.TokenCounter
	if counter == nil {
		counter = defaultTokenCounter
	}

	return adk.AgentMiddleware{
		BeforeChatModel: func(ctx context.Context, state *adk.ChatModelAgentState) error {
			return reduceByTokens(state, toolResultTokenThreshold, keepRecentTokens, placeholder, counter, config.ExcludeTools)
		},
	}, nil
}

// defaultTokenCounter estimates token count using character count / 4
// This is a simple heuristic that works reasonably well for most languages
func defaultTokenCounter(msg *schema.Message) int {
	count := len(msg.Content)

	// Also count tool call arguments if present
	for _, tc := range msg.ToolCalls {
		count += len(tc.Function.Arguments)
	}

	// Estimate: roughly 4 characters per token
	return (count + 3) / 4
}

// reduceByTokens reduces context based on tool result token threshold and recent message protection.
// It clears old tool results when:
// 1. The total tokens of all tool results exceed toolResultTokenThreshold
// 2. Only tool results outside the keepRecentTokens range (from the end) are cleared
func reduceByTokens(state *adk.ChatModelAgentState, toolResultTokenThreshold, keepRecentTokens int, placeholder string, counter func(*schema.Message) int, excludedTools []string) error {
	if len(state.Messages) == 0 {
		return nil
	}

	// Step 1: Calculate total tool result tokens
	totalToolResultTokens := 0
	for _, msg := range state.Messages {
		if msg.Role == schema.Tool && msg.Content != placeholder {
			totalToolResultTokens += counter(msg)
		}
	}

	// If total tool result tokens are under the threshold, no reduction needed
	if totalToolResultTokens <= toolResultTokenThreshold {
		return nil
	}

	// Step 2: Calculate the index from which to protect recent messages
	// We need to find the starting index where cumulative tokens from the end <= keepRecentTokens
	recentStartIdx := len(state.Messages)
	cumulativeTokens := 0

	for i := len(state.Messages) - 1; i >= 0; i-- {
		msgTokens := counter(state.Messages[i])
		if cumulativeTokens+msgTokens > keepRecentTokens {
			// Adding this message would exceed the budget, so stop here
			recentStartIdx = i
			break
		}
		cumulativeTokens += msgTokens
		recentStartIdx = i
	}

	// Step 3: Clear tool results outside the protected range (before recentStartIdx)
	for i := 0; i < recentStartIdx; i++ {
		msg := state.Messages[i]
		if msg.Role == schema.Tool && msg.Content != placeholder && !excluded(msg.ToolName, excludedTools) {
			msg.Content = placeholder
		}
	}

	return nil
}

func excluded(name string, exclude []string) bool {
	for _, ex := range exclude {
		if name == ex {
			return true
		}
	}
	return false
}
