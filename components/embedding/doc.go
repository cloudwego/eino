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

// Package embedding defines the Embedder component interface for converting
// text into vector representations.
//
// # Overview
//
// An Embedder converts a batch of strings into dense float vectors. Semantically
// similar texts produce vectors that are close in the vector space, making
// embeddings the backbone of semantic search, RAG pipelines, and clustering.
//
// Concrete implementations (OpenAI, Ark, Ollama, …) live in eino-ext:
//
//	github.com/cloudwego/eino-ext/components/embedding/
//
// # Output Format
//
// [Embedder.EmbedStrings] returns `[][]float64` where:
//   - outer index corresponds to the input text at the same position
//   - inner slice is the embedding vector; its length (dimensions) is fixed by
//     the model and is the same for every text
//
// # Consistency Requirement
//
// The same model must be used for both indexing and retrieval. Mixing models
// produces vectors in different spaces — similarity scores become meaningless
// and semantic search breaks silently.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/embedding_guide/
package embedding
