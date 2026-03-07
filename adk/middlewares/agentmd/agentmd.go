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

// Package agentmd provides a middleware that automatically injects Agent.md
// file contents into model input messages. The injection is transient — content
// is prepended at model call time and never persisted to conversation state,
// so it is naturally excluded from summarization / compression.
package agentmd

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Config defines the configuration for the agentmd middleware.
type Config struct {
	// Backend provides file access for loading Agent.md files.
	// Implementations can use local filesystem, remote storage, or any other backend.
	// Required.
	Backend Backend

	// AgentFiles specifies the ordered list of Agent.md file paths to load.
	// Files are loaded and injected in the given order.
	// Supports @import syntax inside files for recursive inclusion (max depth 5).
	AgentFiles []string

	// AllAgentMDDocsMaxBytes limits the total byte size of all loaded Agent.md content.
	// Files are loaded in order; once the cumulative size exceeds this limit,
	// remaining files are skipped. Each individual file is always loaded in full.
	// 0 means no limit.
	AllAgentMDDocsMaxBytes int
}

// New creates an agentmd middleware that injects Agent.md content into every
// model call. The content is loaded from the configured file paths via Backend
// on each model invocation.
func New(_ context.Context, cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}

	return &middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		loader:                       newLoader(cfg.Backend, cfg.AgentFiles, cfg.AllAgentMDDocsMaxBytes),
	}, nil
}

type middleware struct {
	*adk.BaseChatModelAgentMiddleware
	loader *loader
}

// WrapModel returns a proxy model that prepends Agent.md content to the input
// messages on every Generate/Stream call. The injected message is never written
// back to ChatModelAgentState, so summarization and reduction middlewares are
// unaffected.
func (m *middleware) WrapModel(_ context.Context, cm model.BaseChatModel, _ *adk.ModelContext) (model.BaseChatModel, error) {
	return &agentMDModel{
		inner:  cm,
		loader: m.loader,
	}, nil
}

// agentMDModel wraps a BaseChatModel to prepend Agent.md content to input.
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

// prependAgentMD loads the current Agent.md content and prepends it as the
// first message. If no agent files are configured or all are empty, the
// original input is returned unchanged.
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

	messages := make([]*schema.Message, 0, len(input)+1)
	messages = append(messages, agentMDMsg)
	messages = append(messages, input...)
	return messages, nil
}

func (c *Config) validate() error {
	if c == nil {
		return fmt.Errorf("[agentmd]: config is required")
	}
	if c.Backend == nil {
		return fmt.Errorf("[agentmd]: backend is required")
	}
	if len(c.AgentFiles) == 0 {
		return fmt.Errorf("[agentmd]: at least one agent file path is required")
	}
	if c.AllAgentMDDocsMaxBytes < 0 {
		return fmt.Errorf("[agentmd]: AllAgentMDDocsMaxBytes must be non-negative")
	}
	return nil
}
