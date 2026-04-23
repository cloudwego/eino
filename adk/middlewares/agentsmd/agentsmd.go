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

// Package agentsmd provides a middleware that automatically injects Agents.md
// file contents into model input messages. The injection is transient — content
// is prepended at model call time and never persisted to conversation state,
// so it is naturally excluded from summarization / compression.
package agentsmd

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// Config defines the configuration for the agentsmd middleware.
type Config struct {
	// Backend provides file access for loading Agents.md files.
	// Implementations can use local filesystem, remote storage, or any other backend.
	// Required.
	Backend Backend

	// AgentsMDFiles specifies the ordered list of Agents.md file paths to load.
	// Files are loaded and injected in the given order.
	// Supports @import syntax inside files for recursive inclusion (max depth 5).
	AgentsMDFiles []string

	// AllAgentsMDMaxBytes limits the total byte size of all loaded Agents.md content.
	// Files are loaded in order; once the cumulative size exceeds this limit,
	// remaining files are skipped. Each individual file is always loaded in full.
	// 0 means no limit.
	AllAgentsMDMaxBytes int

	// OnLoadWarning is an optional callback invoked when a non-fatal error occurs
	// during Agents.md file loading (e.g. file not found, circular @import, depth
	// exceeded). If nil, warnings are logged via log.Printf.
	//
	// Note: Backend.Read errors other than os.ErrNotExist (e.g. permission denied,
	// I/O errors) are NOT treated as warnings and will abort the loading process.
	OnLoadWarning func(filePath string, err error)
}

// New creates an agentsmd middleware that injects Agents.md content into every
// model call. The content is loaded from the configured file paths via Backend
// on each model invocation.
//
// Recommended: place this middleware AFTER the summarization middleware, so that
// Agents.md content is excluded from summarization/compression.
func New(_ context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		loader:                       newLoaderConfig(cfg.Backend, cfg.AgentsMDFiles, cfg.AllAgentsMDMaxBytes, cfg.OnLoadWarning),
	}, nil
}

type middleware struct {
	*adk.BaseChatModelAgentMiddleware
	loader *loaderConfig
}

const agentsMDCacheKey = "__agentsmd_content_cache__"
const agentsMDExtraKey = "__agentsmd_content__"

// BeforeModelRewriteState injects Agents.md content as a User message before
// the first User message in the conversation. The injected message is tagged
// with an Extra key so that repeated invocations are idempotent.
func (m *middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, mc *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	// Idempotent: if we already injected, return early.
	for _, msg := range state.Messages {
		if msg.Extra != nil {
			if _, ok := msg.Extra[agentsMDExtraKey]; ok {
				return ctx, state, nil
			}
		}
	}

	content, err := m.loadContent(ctx)
	if err != nil {
		return ctx, nil, err
	}
	if content == "" {
		return ctx, state, nil
	}

	msg := schema.UserMessage(fmt.Sprintf("<system-reminder>\n%s\n</system-reminder>", content))
	msg.Extra = map[string]any{agentsMDExtraKey: true}

	nState := *state
	nState.Messages = insertBeforeFirstUser(state.Messages, msg)
	return ctx, &nState, nil
}

// loadContent retrieves the Agents.md content, using a per-Run cache to avoid
// reloading on every model call within the same Run().
func (m *middleware) loadContent(ctx context.Context) (string, error) {
	if cached, found, err := adk.GetRunLocalValue(ctx, agentsMDCacheKey); err == nil && found {
		if s, ok := cached.(string); ok {
			return s, nil
		}
	}

	content, err := m.loader.load(ctx)
	if err != nil {
		return "", fmt.Errorf("[agentsmd]: failed to load agent files: %w", err)
	}

	if content != "" {
		_ = adk.SetRunLocalValue(ctx, agentsMDCacheKey, content)
	}

	return content, nil
}

// insertBeforeFirstUser inserts newMsg before the first User role message.
// If no User message is found, newMsg is appended at the end.
func insertBeforeFirstUser(msgs []*schema.Message, newMsg *schema.Message) []*schema.Message {
	result := make([]*schema.Message, 0, len(msgs)+1)
	inserted := false
	for i, msg := range msgs {
		if !inserted && msg.Role == schema.User {
			result = append(result, newMsg)
			result = append(result, msgs[i:]...)
			inserted = true
			break
		}
		result = append(result, msg)
	}
	if !inserted {
		result = append(result, newMsg)
	}
	return result
}

func (c *Config) validate() error {
	if c == nil {
		return fmt.Errorf("[agentsmd]: config is required")
	}
	if c.Backend == nil {
		return fmt.Errorf("[agentsmd]: backend is required")
	}
	if len(c.AgentsMDFiles) == 0 {
		return fmt.Errorf("[agentsmd]: at least one agent file path is required")
	}
	if c.AllAgentsMDMaxBytes < 0 {
		return fmt.Errorf("[agentsmd]: AllAgentMDDocsMaxBytes must be non-negative")
	}
	return nil
}
