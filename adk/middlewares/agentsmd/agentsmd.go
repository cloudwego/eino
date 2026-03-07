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
	"github.com/cloudwego/eino/components/model"
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

	// IgnoreLoadFileError, when true, causes errors during Agents.md file loading
	// to be silently ignored (logged as warnings). Files that fail to load are
	// simply skipped. The following errors are covered:
	//   - file path resolution failure
	//   - file not found or read failure from Backend
	//   - @import depth exceeding the maximum limit
	//   - circular @import detection
	IgnoreLoadFileError bool
}

// New creates an agentsmd middleware that injects Agents.md content into every
// model call. The content is loaded from the configured file paths via Backend
// on each model invocation.
func New(_ context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		loader:                       newLoader(cfg.Backend, cfg.AgentsMDFiles, cfg.AllAgentsMDMaxBytes, cfg.IgnoreLoadFileError),
	}, nil
}

type middleware struct {
	*adk.BaseChatModelAgentMiddleware
	loader *loader
}

// WrapModel returns a proxy model that prepends Agents.md content to the input
// messages on every Generate/Stream call. The injected message is never written
// back to ChatModelAgentState, so summarization and reduction middlewares are
// unaffected.
func (m *middleware) WrapModel(_ context.Context, cm model.BaseChatModel, _ *adk.ModelContext) (model.BaseChatModel, error) {
	return &agentMDModel{
		inner:  cm,
		loader: m.loader,
	}, nil
}

// agentMDModel wraps a BaseChatModel to prepend Agents.md content to input.
type agentMDModel struct {
	inner  model.BaseChatModel
	loader *loader
}

func (m *agentMDModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	messages, err := m.prependAgentMD(ctx, input)
	if err != nil {
		return nil, err
	}
	return m.inner.Generate(ctx, messages, opts...)
}

func (m *agentMDModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	messages, err := m.prependAgentMD(ctx, input)
	if err != nil {
		return nil, err
	}
	return m.inner.Stream(ctx, messages, opts...)
}

// prependAgentMD loads the current Agents.md content and inserts it before the
// first User role message. If all configured agent files are empty (or skipped),
// the original input is returned unchanged.
func (m *agentMDModel) prependAgentMD(ctx context.Context, input []*schema.Message) ([]*schema.Message, error) {
	content, err := m.loader.load(ctx)
	if err != nil {
		return nil, fmt.Errorf("[agentmd]: failed to load agent files: %w", err)
	}
	if content == "" {
		return input, nil
	}

	agentMDMsg := &schema.Message{
		Role:    schema.User,
		Content: content,
	}

	// Insert agentMDMsg before the first User role message.
	messages := make([]*schema.Message, 0, len(input)+1)
	inserted := false
	for i, msg := range input {
		if !inserted && msg.Role == schema.User {
			messages = append(messages, agentMDMsg)
			messages = append(messages, input[i:]...)
			inserted = true
			break
		}
		messages = append(messages, msg)
	}
	if !inserted {
		// No User message found; append at the end as fallback.
		messages = append(messages, agentMDMsg)
	}
	return messages, nil
}

func (c *Config) validate() error {
	if c == nil {
		return fmt.Errorf("[agentmd]: config is required")
	}
	if c.Backend == nil {
		return fmt.Errorf("[agentmd]: backend is required")
	}
	if len(c.AgentsMDFiles) == 0 {
		return fmt.Errorf("[agentmd]: at least one agent file path is required")
	}
	if c.AllAgentsMDMaxBytes < 0 {
		return fmt.Errorf("[agentmd]: AllAgentMDDocsMaxBytes must be non-negative")
	}
	return nil
}
