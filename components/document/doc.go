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

// Package document defines the Loader and Transformer component interfaces
// for ingesting and processing documents in an eino pipeline.
//
// # Components
//
//   - [Loader]: reads raw content from an external source (file, URL, S3, …)
//     and returns [schema.Document] values. Parsing is typically delegated to
//     a [parser.Parser] configured on the loader.
//   - [Transformer]: takes a slice of [schema.Document] values and transforms
//     them — splitting, filtering, merging, re-ranking, etc.
//
// Concrete implementations live in eino-ext:
//
//	github.com/cloudwego/eino-ext/components/document/
//
// # Document Metadata
//
// [schema.Document].MetaData is the primary mechanism for carrying contextual
// information (source URI, scores, chunk indices, embeddings) through the
// pipeline. Transformers should preserve existing metadata and merge rather
// than replace when adding their own keys.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/document_loader_guide/
// See https://www.cloudwego.io/docs/eino/core_modules/components/document_transformer_guide/
package document
