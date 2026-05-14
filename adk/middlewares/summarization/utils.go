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

package summarization

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
)

type PopulateUserMessagesConfig[M adk.MessageType] struct {
	// MaxTokens limits the maximum token count for preserved user messages.
	// Optional. Defaults to 30000.
	MaxTokens *int

	// Filter determines whether a specific user message should be preserved.
	// It is called for each user message. If it returns false, the message will not be preserved.
	// Optional.
	Filter TypedUserMessageFilterFunc[M]

	// TokenCounter provides custom token counting.
	// Optional. Uses default estimator if not set.
	TokenCounter TypedTokenCounterFunc[M]
}

// PopulateUserMessages replaces the <all_user_messages>...</all_user_messages>
// section in summaryText with recent user messages from the given messages.
func PopulateUserMessages[M adk.MessageType](ctx context.Context, messages []M, summaryText string, config PopulateUserMessagesConfig[M]) (string, error) {
	if config.MaxTokens != nil && *config.MaxTokens < 0 {
		return "", fmt.Errorf("MaxTokens must be non-negative")
	}

	const defaultMaxTokens = 30000

	maxTokens := defaultMaxTokens
	if config.MaxTokens != nil {
		maxTokens = *config.MaxTokens
	}

	var userMsgs []M
	for i, msg := range messages {
		if isUserRole(msg) {
			userMsgs = messages[i:]
			break
		}
	}

	return populateUserMessages(ctx, &populateUserMessagesParams[M]{
		contextMsgs:  userMsgs,
		summaryText:  summaryText,
		maxTokens:    maxTokens,
		filter:       config.Filter,
		tokenCounter: config.TokenCounter,
	})
}
