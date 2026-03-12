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

// Package parser defines the Parser interface for converting raw byte streams
// into [schema.Document] values.
//
// # Overview
//
// A Parser is not a standalone pipeline component — it is used inside a
// [document.Loader] to handle format-specific decoding. The loader fetches
// raw bytes; the parser converts them into documents.
//
// # Built-in Implementations
//
//   - TextParser: treats the entire reader as plain text, one document per call
//   - ExtParser: selects a parser by file extension (from [Options.URI]), with
//     a configurable fallback for unknown extensions
//
// Use ExtParser when you want format-agnostic loading: pass the source URI
// via [WithURI] and ExtParser picks the right sub-parser automatically.
//
// # Reader Contract
//
// The [io.Reader] passed to [Parser.Parse] is consumed during the call —
// it cannot be read again. Loaders must not reuse the same reader across
// multiple Parse calls.
//
// # Metadata Propagation
//
// Use [WithExtraMeta] to attach key-value pairs that are merged into every
// document's MetaData. This is the standard way to tag documents with source
// information (URI, content type, etc.) at parse time.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/document_loader_guide/document_parser_interface_guide/
package parser
