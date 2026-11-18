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

package multiquery

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type mockRetriever struct {
}

func (m *mockRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	var ret []*schema.Document
	if strings.Contains(query, "1") {
		ret = append(ret, &schema.Document{ID: "1"})
	}
	if strings.Contains(query, "2") {
		ret = append(ret, &schema.Document{ID: "2"})
	}
	if strings.Contains(query, "3") {
		ret = append(ret, &schema.Document{ID: "3"})
	}
	if strings.Contains(query, "4") {
		ret = append(ret, &schema.Document{ID: "4"})
	}
	if strings.Contains(query, "5") {
		ret = append(ret, &schema.Document{ID: "5"})
	}
	return ret, nil
}

type mockModel struct {
}

func (m *mockModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return &schema.Message{
		Content: "12\n23\n34\n14\n23\n45",
	}, nil
}

func (m *mockModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("implement me")
}

func (m *mockModel) BindTools(tools []*schema.ToolInfo) error {
	panic("implement me")
}

func TestMultiQueryRetriever(t *testing.T) {
	ctx := context.Background()

	// use default llm
	mqr, err := NewRetriever(ctx, &Config{
		RewriteLLM:    &mockModel{},
		OrigRetriever: &mockRetriever{},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := compose.NewChain[string, []*schema.Document]()
	cr, err := c.AppendRetriever(mqr).Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}

	result, err := cr.Invoke(ctx, "query")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 4 {
		t.Fatal("default llm retrieve result is unexpected")
	}

	// use custom
	mqr, err = NewRetriever(ctx, &Config{
		RewriteHandler: func(ctx context.Context, query string) ([]string, error) {
			return []string{"1", "3", "5"}, nil
		},
		OrigRetriever: &mockRetriever{},
	})
	if err != nil {
		t.Fatal(err)
	}
	c = compose.NewChain[string, []*schema.Document]()
	cr, err = c.AppendRetriever(mqr).Compile(ctx)
	if err != nil {
		t.Fatal(err)
	}

	result, err = cr.Invoke(ctx, "query")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 3 {
		t.Fatal("default llm retrieve result is unexpected")
	}
}

func TestDeduplicateFusionEmptyID(t *testing.T) {
	ctx := context.Background()

	// Different documents produced by parsers such as TextParser have no ID.
	// They must not all collapse into a single result just because their IDs
	// are the empty string.
	docs := [][]*schema.Document{
		{
			{Content: "alpha from first query"},
			{Content: "beta from first query"},
		},
		{
			{Content: "gamma from second query"},
		},
	}

	got, err := deduplicateFusion(ctx, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 distinct empty-ID documents, got %d", len(got))
	}
}

func TestDeduplicateFusionByID(t *testing.T) {
	ctx := context.Background()

	// Documents carrying an ID are still deduplicated by that ID across
	// different query results.
	docs := [][]*schema.Document{
		{
			{ID: "1", Content: "first"},
			{ID: "2", Content: "second"},
		},
		{
			{ID: "1", Content: "first again"},
			{ID: "3", Content: "third"},
		},
	}

	got, err := deduplicateFusion(ctx, docs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 unique-ID documents, got %d", len(got))
	}
}
