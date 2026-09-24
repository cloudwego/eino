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
	"testing"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
)

type fakeStore struct {
	searchedQueries []string
	savedDocs       []*schema.Document
	savedNamespaces []string
}

func (f *fakeStore) Search(_ context.Context, query string, opts ...MemoryOption) ([]*schema.Document, error) {
	f.searchedQueries = append(f.searchedQueries, query)
	ns := ""
	for _, opt := range opts {
		o := &memoryOptions{}
		opt(o)
		ns = o.namespace
	}
	f.savedNamespaces = append(f.savedNamespaces, ns)
	return []*schema.Document{{ID: "1", Content: "remember this fact"}}, nil
}

func (f *fakeStore) Save(_ context.Context, docs []*schema.Document, opts ...MemoryOption) error {
	f.savedDocs = append(f.savedDocs, docs...)
	ns := ""
	for _, opt := range opts {
		o := &memoryOptions{}
		opt(o)
		ns = o.namespace
	}
	f.savedNamespaces = append(f.savedNamespaces, ns)
	return nil
}

func TestNewRequiresStore(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("expected error for nil Config")
	}
	if _, err := New(&Config{}); err == nil {
		t.Fatal("expected error for nil Store")
	}
}

func TestMemoryInjectedOnce(t *testing.T) {
	store := &fakeStore{}
	mw, err := New(&Config{Store: store})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ctx, _, err = mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		t.Fatal(err)
	}

	state := &adk.ChatModelAgentState{
		Messages: []*schema.Message{schema.UserMessage("what did I tell you?")},
	}

	// First model call injects memory.
	ctx, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("expected memory message prepended, got %d messages", len(state.Messages))
	}
	if state.Messages[0].Role != schema.System || state.Messages[0].Content != "[memory] remember this fact" {
		t.Fatalf("unexpected injected message: %+v", state.Messages[0])
	}

	// Second model call must not re-inject.
	ctx, state, err = mw.BeforeModelRewriteState(ctx, state, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Messages) != 2 {
		t.Fatalf("expected no re-injection, got %d messages", len(state.Messages))
	}
	if len(store.searchedQueries) != 1 {
		t.Fatalf("expected 1 search, got %d", len(store.searchedQueries))
	}
}

func TestWritePolicySavesAfterAgent(t *testing.T) {
	store := &fakeStore{}
	mw, err := New(&Config{
		Store:             store,
		NamespaceResolver: func(context.Context, *adk.ChatModelAgentContext) string { return "user-42" },
		WritePolicy: func(_ context.Context, state *adk.ChatModelAgentState) ([]*schema.Document, bool, error) {
			return []*schema.Document{{ID: "2", Content: "user preference"}}, true, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	ctx, _, err = mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		t.Fatal(err)
	}

	_, err = mw.AfterAgent(ctx, &adk.ChatModelAgentState{})
	if err != nil {
		t.Fatal(err)
	}

	if len(store.savedDocs) != 1 || store.savedDocs[0].Content != "user preference" {
		t.Fatalf("unexpected saved docs: %+v", store.savedDocs)
	}
	if len(store.savedNamespaces) != 1 || store.savedNamespaces[0] != "user-42" {
		t.Fatalf("unexpected saved namespaces: %v", store.savedNamespaces)
	}
}

func TestWritePolicyFalseSkipsSave(t *testing.T) {
	store := &fakeStore{}
	mw, err := New(&Config{
		Store: store,
		WritePolicy: func(context.Context, *adk.ChatModelAgentState) ([]*schema.Document, bool, error) {
			return nil, false, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := mw.AfterAgent(context.Background(), &adk.ChatModelAgentState{}); err != nil {
		t.Fatal(err)
	}
	if len(store.savedDocs) != 0 {
		t.Fatalf("expected no save, got %d docs", len(store.savedDocs))
	}
}
