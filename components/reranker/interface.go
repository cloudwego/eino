/*package reranker

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

package reranker

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

//go:generate mockgen -destination ../../internal/mock/components/reranker/reranker_mock.go --package reranker -source interface.go

// Reranker is the interface for reranker.
// It is used to rerank documents based on a query and options.
//
// e.g.
//
//	reranker, err := somepkg.NewReranker(ctx, &somepkg.RerankerConfig{})
//	if err != nil {...}
//	docs, err := reranker.Rerank(ctx, query, docs) // <= using directly
//	docs, err := reranker.Rerank(ctx, query, docs, reranker.WithTopK(3)) // <= using options
//
//	graph := compose.NewGraph[*reranker.Request, []*schema.Document](compose.RunTypeDAG)
//	graph.AddRerankerNode("reranker_node_key", reranker) // <= using in graph
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []*schema.Document, opts ...Option) ([]*schema.Document, error)
}
