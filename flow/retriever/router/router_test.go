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
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/retriever"
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

func (m *mockRetriever) GetType() string {
	return "Mock"
}

func TestRouterRetriever(t *testing.T) {
	ctx := context.Background()
	r, err := NewRetriever(ctx, &Config{
		Retrievers: map[string]retriever.Retriever{
			"1": &mockRetriever{},
			"2": &mockRetriever{},
			"3": &mockRetriever{},
		},
		Router: func(ctx context.Context, query string) ([]string, error) {
			return []string{"2", "3"}, nil
		},
		FusionFunc: func(ctx context.Context, result map[string][]*schema.Document) ([]*schema.Document, error) {
			var ret []*schema.Document
			for _, v := range result {
				ret = append(ret, v...)
			}
			return ret, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := callbacks.NewHandlerBuilder().
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			switch info.Name {
			case "FusionFuncLambda":
				if _, ok := output.([]*schema.Document); !ok {
					t.Fatal("FusionFuncLambda output is not a []*schema.Document")
				}
			case "RouterLambda":
				if _, ok := output.([]string); !ok {
					t.Fatal("RouterLambda output is not a []string")
				}
			case "MockRetriever":
				if _, ok := output.([]*schema.Document); !ok {
					t.Fatal("MockRetriever output is not a []string")
				}
			default:
				t.Fatalf("unknown name: %s", info.Name)
			}
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			t.Fatal(err)
			return ctx
		}).Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{}, handler)
	result, err := r.Retrieve(ctx, "3")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) != 2 {
		t.Fatal("expected 2 results")
	}
}

func TestRRF(t *testing.T) {
	doc1 := &schema.Document{ID: "1"}
	doc2 := &schema.Document{ID: "2"}
	doc3 := &schema.Document{ID: "3"}
	doc4 := &schema.Document{ID: "4"}
	doc5 := &schema.Document{ID: "5"}

	input := map[string][]*schema.Document{
		"1": {doc1, doc2, doc3, doc4, doc5},
		"2": {doc2, doc3, doc4, doc5, doc1},
		"3": {doc3, doc4, doc5, doc1, doc2},
	}

	result, err := rrf(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result, []*schema.Document{doc3, doc2, doc4, doc1, doc5}) {
		t.Fatal("rrf fail")
	}
}

func TestRouterDefaultRouter(t *testing.T) {
	// When config.Router is nil, NewRetriever should use a default router
	// that routes to all registered retrievers.
	// Previously, a bug caused config.Router (nil) to be used directly
	// instead of the locally-assigned default function, leading to
	// nil pointer dereference on Retrieve.
	ctx := context.Background()
	r, err := NewRetriever(ctx, &Config{
		Retrievers: map[string]retriever.Retriever{
			"1": &mockRetriever{},
			"2": &mockRetriever{},
		},
		// Router is intentionally nil — should default to all-retrievers routing
	})
	if err != nil {
		t.Fatal(err)
	}

	handler := callbacks.NewHandlerBuilder().
		OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
			return ctx
		}).
		OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
			t.Fatal(err)
			return ctx
		}).Build()
	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{}, handler)

	result, err := r.Retrieve(ctx, "1 2")
	if err != nil {
		t.Fatal(err)
	}
	if len(result) == 0 {
		t.Fatal("expected non-empty results from default router")
	}
}
