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

// Package indexer defines the Indexer component interface for storing documents
// and their vector representations in a backend store.
//
// # Overview
//
// An Indexer is the write path of a RAG pipeline. It takes [schema.Document]
// values, optionally generates vector embeddings, and persists them in a
// backend (vector DB, search engine, etc.) for later retrieval.
//
// Concrete implementations (VikingDB, Milvus, Elasticsearch, …) live in
// eino-ext:
//
//	github.com/cloudwego/eino-ext/components/indexer/
//
// # Vector Dimension Consistency
//
// When using the [Options.Embedding] option, the embedding model must be
// identical to the one used by the paired [retriever.Retriever]. Mismatched
// models produce vectors in different spaces — queries will not match stored
// documents.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/indexer_guide/
package indexer
