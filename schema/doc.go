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

// Package schema defines the core data structures and utilities shared across
// all Eino components.
//
// # Key Types
//
// [Message] is the universal unit of communication between users, models, and
// tools. It carries role, text content, multimodal media, tool calls, and
// response metadata. Helper constructors — [UserMessage], [SystemMessage],
// [AssistantMessage], [ToolMessage] — cover the most common cases.
//
// [Document] represents a piece of text with a metadata map. Typed accessors
// (Score, SubIndexes, DenseVector, SparseVector, DSLInfo, ExtraInfo) read and
// write well-known metadata keys so pipeline stages can pass structured data
// without coupling to specific struct types.
//
// [ToolInfo] describes a tool's name, description, and parameter schema.
// Parameters can be declared either as a [ParameterInfo] map (simple, struct-
// like) or as a raw [jsonschema.Schema] (full JSON Schema 2020-12 expressiveness).
// [ToolChoice] controls whether the model must, may, or must not call tools.
//
// # Streaming
//
// [StreamReader] and [StreamWriter] are the building blocks for streaming data
// through Eino pipelines. Create a linked pair with [Pipe]:
//
//	sr, sw := schema.Pipe[*schema.Message](10)
//	go func() {
//		defer sw.Close()
//		sw.Send(chunk, nil)
//	}()
//	defer sr.Close()
//	for {
//		chunk, err := sr.Recv()
//		if errors.Is(err, io.EOF) { break }
//	}
//
// Important constraints:
//   - A StreamReader is read-once: only one goroutine may call Recv.
//   - Always call Close, even when the loop ends on io.EOF, to release resources.
//   - To give the same stream to multiple consumers, call [StreamReader.Copy].
//
// # Four Streaming Paradigms
//
// Eino components and Lambda functions are classified by their input/output
// streaming shape. The framework automatically bridges mismatches:
//
//   - Invoke: non-streaming in, non-streaming out (ping-pong).
//   - Stream: non-streaming in, StreamReader out (server-streaming). ChatModel
//     and Tool support this.
//   - Collect: StreamReader in, non-streaming out (client-streaming). Useful
//     for branch conditions that decide after the first chunk.
//   - Transform: StreamReader in, StreamReader out (bidirectional).
//
// When an upstream node outputs T but a downstream node only accepts
// StreamReader[T], the framework wraps T in a single-chunk StreamReader —
// this is called a "fake stream". It satisfies the interface but does NOT
// reduce time-to-first-chunk. Conversely, when a downstream node only accepts
// T but the upstream outputs StreamReader[T], the framework automatically
// concatenates the stream into a complete T.
//
// Utility functions:
//   - [StreamReaderFromArray] wraps a slice as a stream (useful in tests).
//   - [MergeStreamReaders] fans-in multiple streams into one.
//   - [MergeNamedStreamReaders] like MergeStreamReaders but emits [SourceEOF]
//     when each named source ends, useful for tracking per-source completion.
//   - [StreamReaderWithConvert] transforms element types; return [ErrNoValue]
//     from the convert function to skip an element.
//
// See https://www.cloudwego.io/docs/eino/core_modules/chain_and_graph_orchestration/stream_programming_essentials/
package schema
