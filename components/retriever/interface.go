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

// Retriever is the interface for retriever.
// It is used to retrieve documents from a source.
//
// e.g.
//
//		retriever, err := redis.NewRetriever(ctx, &redis.RetrieverConfig{})
//		if err != nil {...}
//		docs, err := retriever.Retrieve(ctx, "query") // <= using directly
//		docs, err := retriever.Retrieve(ctx, "query", retriever.WithTopK(3)) // <= using options
//
//	 	graph := compose.NewGraph[inputType, outputType](compose.RunTypeDAG)
//		graph.AddRetrieverNode("retriever_node_key", retriever) // <= using in graph
type Retriever interface {
	Retrieve(ctx context.Context, query string, opts ...Option) ([]*schema.Document, error)
}
