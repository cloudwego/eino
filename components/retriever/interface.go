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

package retriever

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

//go:generate mockgen -destination ../../internal/mock/components/retriever/retriever_mock.go --package retriever -source interface.go

// Retriever fetches the most relevant documents from a store for a given query.
//
// Retrieve accepts a natural-language query string and returns matching
// [schema.Document] values ordered by relevance (most relevant first).
// Relevance scores and backend-specific metadata are available in
// [schema.Document].MetaData.
//
// When [Options.Embedding] is set, the implementation converts the query to a
// vector before searching. The embedder must be the same model used at index
// time — see [indexer.Options.Embedding].
//
// [Options.ScoreThreshold] is a filter, not a sort: documents scoring below
// the threshold are excluded entirely. [Options.TopK] caps the number of
// results returned.
//
// Retrieve can be used standalone or added to a Graph via AddRetrieverNode:
//
//	retriever, _ := redis.NewRetriever(ctx, cfg)
//	docs, _ := retriever.Retrieve(ctx, "what is eino?", retriever.WithTopK(5))
//
//	graph.AddRetrieverNode("retriever", retriever)
type Retriever interface {
	Retrieve(ctx context.Context, query string, opts ...Option) ([]*schema.Document, error)
}
