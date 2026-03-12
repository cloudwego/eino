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

package indexer

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Indexer stores documents (and optionally their vector embeddings) in a
// backend for later retrieval.
//
// Store accepts a batch of [schema.Document] values and returns the IDs
// assigned to them by the backend. When [Options.Embedding] is provided,
// the implementation generates vectors before storing — the same embedder
// must be used by the paired [retriever.Retriever].
//
// Use [Options.SubIndexes] to write documents into logical sub-partitions
// within the same store.
//
//go:generate  mockgen -destination ../../internal/mock/components/indexer/indexer_mock.go --package indexer -source interface.go
type Indexer interface {
	// Store stores the documents and returns their assigned IDs.
	Store(ctx context.Context, docs []*schema.Document, opts ...Option) (ids []string, err error) // invoke
}
