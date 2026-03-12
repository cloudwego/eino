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

// Package retriever defines the Retriever component interface for fetching
// relevant documents from a document store given a query.
//
// # Overview
//
// A Retriever is the read path of a RAG (Retrieval-Augmented Generation)
// pipeline. Given a query string it returns the most relevant [schema.Document]
// values from an underlying store (vector DB, keyword index, etc.).
//
// Concrete implementations (VikingDB, Milvus, Elasticsearch, …) live in
// eino-ext:
//
//	github.com/cloudwego/eino-ext/components/retriever/
//
// # Relationship to Indexer
//
// [Indexer] and Retriever are complementary:
//   - Indexer writes documents (and their vectors) to the store
//   - Retriever reads them back
//
// When both use an [embedding.Embedder], it must be the same model — vector
// dimensions must match or similarity scores will be meaningless.
//
// # Result Ordering
//
// Results are ordered by relevance score (descending). Scores and other
// backend metadata are available via [schema.Document].MetaData.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/retriever_guide/
package retriever
