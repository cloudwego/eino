/*
 * Copyright 2026 CloudWeGo Authors
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

// Package memory provides a pluggable long-term memory middleware for Eino ADK
// agents.
//
// It connects the ADK middleware hooks with an application-provided
// [MemoryStore] so that long-running agents can retrieve relevant memory
// before a run and optionally write facts or summaries back after a run,
// without depending on any specific vector database.
//
// The store implementation lives outside Eino core: an in-memory or
// Retriever/Indexer-backed implementation can be provided by the application
// or by eino-ext.
package memory
