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

package memory

import (
	"context"
	"errors"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

// MemoryStore abstracts long-term memory retrieval and persistence. It is the
// seam that lets applications plug in any backend — a Retriever/Indexer pair,
// a vector database client, or a remote memory service — without the middleware
// depending on a specific implementation.
type MemoryStore interface {
	// Search returns documents relevant to the query, most relevant first.
	Search(ctx context.Context, query string, opts ...MemoryOption) ([]*schema.Document, error)
	// Save persists documents to memory.
	Save(ctx context.Context, docs []*schema.Document, opts ...MemoryOption) error
}

// MemoryOption configures a single Search or Save operation.
type MemoryOption func(*memoryOptions)

type memoryOptions struct {
	namespace string
}

// WithNamespace scopes the operation to a memory namespace such as a user,
// session, agent or project identifier. Stores that do not support namespaces
// may ignore it.
func WithNamespace(namespace string) MemoryOption {
	return func(o *memoryOptions) {
		o.namespace = namespace
	}
}

// Config configures the memory middleware. Only Store is required; the
// remaining fields fall back to sensible defaults.
type Config struct {
	// Store is the memory backend. Required.
	Store MemoryStore

	// NamespaceResolver returns the namespace to scope the current run's
	// retrieval and writes. It is called once per agent run in BeforeAgent.
	// Defaults to returning "" (a single shared namespace).
	NamespaceResolver func(ctx context.Context, runCtx *adk.ChatModelAgentContext) string

	// QueryBuilder builds the retrieval query from the conversation state.
	// Called once per run before the first model invocation. Defaults to the
	// content of the most recent user message.
	QueryBuilder func(ctx context.Context, state *adk.ChatModelAgentState) string

	// MemoryFormatter renders retrieved documents as messages to inject into
	// the conversation. Defaults to one system message per document,
	// prefixed with "[memory] ".
	MemoryFormatter func(ctx context.Context, docs []*schema.Document) []*schema.Message

	// WritePolicy decides what to persist once the agent reaches a successful
	// terminal state. It is called in AfterAgent. Returning false means
	// nothing is written. Defaults to nil (nothing written).
	WritePolicy func(ctx context.Context, state *adk.ChatModelAgentState) ([]*schema.Document, bool, error)
}

type namespaceKey struct{}

type memoryInjectedKey struct{}

type middleware struct {
	*adk.BaseChatModelAgentMiddleware
	cfg *Config
}

// New builds a ChatModelAgentMiddleware that injects long-term memory before
// each agent run and optionally writes memory back after the run.
func New(cfg *Config) (adk.ChatModelAgentMiddleware, error) {
	if cfg == nil {
		return nil, errors.New("memory: nil Config")
	}
	if cfg.Store == nil {
		return nil, errors.New("memory: Config.Store is required")
	}
	return &middleware{
		BaseChatModelAgentMiddleware: &adk.BaseChatModelAgentMiddleware{},
		cfg:                          cfg,
	}, nil
}

// BeforeAgent resolves the memory namespace once per run and stashes it in the
// context for the later hooks.
func (m *middleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	ns := ""
	if m.cfg.NamespaceResolver != nil {
		ns = m.cfg.NamespaceResolver(ctx, runCtx)
	}
	return context.WithValue(ctx, namespaceKey{}, ns), runCtx, nil
}

// BeforeModelRewriteState retrieves and injects memory before the first model
// invocation of a run. Subsequent model iterations are left untouched to avoid
// re-injecting the same memory on every tool-call loop.
func (m *middleware) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if ctx.Value(memoryInjectedKey{}) != nil {
		return ctx, state, nil
	}

	query := m.buildQuery(ctx, state)
	if query == "" {
		return ctx, state, nil
	}

	docs, err := m.cfg.Store.Search(ctx, query, WithNamespace(namespaceFrom(ctx)))
	if err != nil {
		return ctx, state, err
	}
	if len(docs) == 0 {
		return ctx, state, nil
	}

	memoryMsgs := m.format(ctx, docs)
	if len(memoryMsgs) == 0 {
		return ctx, state, nil
	}

	state.Messages = append(append([]*schema.Message{}, memoryMsgs...), state.Messages...)
	return context.WithValue(ctx, memoryInjectedKey{}, true), state, nil
}

// AfterAgent writes memory back once the agent reaches a successful terminal
// state.
func (m *middleware) AfterAgent(ctx context.Context, state *adk.ChatModelAgentState) (context.Context, error) {
	if m.cfg.WritePolicy == nil {
		return ctx, nil
	}

	docs, ok, err := m.cfg.WritePolicy(ctx, state)
	if err != nil {
		return ctx, err
	}
	if !ok || len(docs) == 0 {
		return ctx, nil
	}

	if err := m.cfg.Store.Save(ctx, docs, WithNamespace(namespaceFrom(ctx))); err != nil {
		return ctx, err
	}
	return ctx, nil
}

func (m *middleware) buildQuery(ctx context.Context, state *adk.ChatModelAgentState) string {
	if m.cfg.QueryBuilder != nil {
		return m.cfg.QueryBuilder(ctx, state)
	}

	for i := len(state.Messages) - 1; i >= 0; i-- {
		if state.Messages[i].Role == schema.User && state.Messages[i].Content != "" {
			return state.Messages[i].Content
		}
	}
	return ""
}

func (m *middleware) format(ctx context.Context, docs []*schema.Document) []*schema.Message {
	if m.cfg.MemoryFormatter != nil {
		return m.cfg.MemoryFormatter(ctx, docs)
	}

	msgs := make([]*schema.Message, 0, len(docs))
	for _, doc := range docs {
		if doc == nil || doc.Content == "" {
			continue
		}
		msgs = append(msgs, schema.SystemMessage("[memory] "+doc.Content))
	}
	return msgs
}

func namespaceFrom(ctx context.Context) string {
	ns, _ := ctx.Value(namespaceKey{}).(string)
	return ns
}
