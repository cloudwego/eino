/*
 * Copyright 2024 CloudWeGo Authors
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

package router

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

func TestRRFDeterministicTies(t *testing.T) {
	a := &schema.Document{ID: "a"}
	b := &schema.Document{ID: "b"}
	results := map[string][]*schema.Document{"vector": {a, b}, "keyword": {b, a}}
	for i := 0; i < 100; i++ {
		got, err := rrf(context.Background(), results)
		require.NoError(t, err)
		require.Equal(t, []*schema.Document{a, b}, got, "equal scores should use document ID order")
	}
	require.Equal(t, []*schema.Document{a, b}, results["vector"])
	require.Equal(t, []*schema.Document{b, a}, results["keyword"])
}

func TestRRFDeterministicDocumentSelection(t *testing.T) {
	keyword := &schema.Document{ID: "shared", Content: "keyword excerpt", MetaData: map[string]any{"source": "keyword"}}
	vector := &schema.Document{ID: "shared", Content: "vector excerpt", MetaData: map[string]any{"source": "vector"}}
	for i := 0; i < 100; i++ {
		got, err := rrf(context.Background(), map[string][]*schema.Document{"vector": {vector}, "keyword": {keyword}})
		require.NoError(t, err)
		require.Len(t, got, 1)
		require.Same(t, keyword, got[0], "first retriever in name order should supply the document")
	}
	require.Equal(t, "keyword excerpt", keyword.Content)
	require.Equal(t, "vector excerpt", vector.Content)
}

func TestRRFSingleRetrieverPreservesOrder(t *testing.T) {
	docs := []*schema.Document{{ID: "b"}, {ID: "a"}}
	got, err := rrf(context.Background(), map[string][]*schema.Document{"only": docs})
	require.NoError(t, err)
	require.Equal(t, docs, got)
}

type fixedRRFTestRetriever struct {
	docs []*schema.Document
}

func (r *fixedRRFTestRetriever) Retrieve(context.Context, string, ...retriever.Option) ([]*schema.Document, error) {
	return r.docs, nil
}

func TestRouterDefaultFusionDeterministic(t *testing.T) {
	a, b := &schema.Document{ID: "a"}, &schema.Document{ID: "b"}
	keyword := &schema.Document{ID: "shared", Content: "keyword excerpt"}
	vector := &schema.Document{ID: "shared", Content: "vector excerpt"}
	r, err := NewRetriever(context.Background(), &Config{
		Retrievers: map[string]retriever.Retriever{
			"keyword": &fixedRRFTestRetriever{docs: []*schema.Document{keyword, b, a}},
			"vector":  &fixedRRFTestRetriever{docs: []*schema.Document{vector, a, b}},
		},
		Router: func(context.Context, string) ([]string, error) { return []string{"vector", "keyword"}, nil },
	})
	require.NoError(t, err)
	for i := 0; i < 50; i++ {
		got, err := r.Retrieve(context.Background(), "same query")
		require.NoError(t, err)
		require.Equal(t, []*schema.Document{keyword, a, b}, got)
	}
}
