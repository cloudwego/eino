/*
 * Copyright 2025 CloudWeGo Authors
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

// Package otel provides a native OpenTelemetry callback handler for eino.
//
// It implements the callbacks.Handler interface and creates OpenTelemetry spans
// following the GenAI semantic conventions:
// https://opentelemetry.io/docs/specs/semconv/gen-ai/
//
// # Quick Start
//
// Register the handler globally (recommended for tracing all components):
//
//	callbacks.AppendGlobalHandlers(otel.NewHandler())
//
// Or pass it per-invocation:
//
//	handler := otel.NewHandler(otel.WithTracerProvider(tp))
//	graph.Invoke(ctx, input, compose.WithCallbacks(handler))
//
// # Spans by Component Type
//
//   - ChatModel / AgenticModel: "gen_ai.client.chat" with model, usage (input/output/total tokens),
//     and message count attributes
//   - Tool: "gen_ai.tool {name}" with tool name attribute
//   - Retriever: "gen_ai.retriever {name}" with query and document count attributes
//   - Embedding: "gen_ai.client.embed" with operation name attribute
//   - Graph / Chain / Workflow / Lambda: "eino.{Component} {name}" spans
//
// # GenAI Semantic Conventions
//
// Standard attributes used:
//   - gen_ai.system: "eino"
//   - gen_ai.operation.name: "chat" / "retrieve" / "embed"
//   - gen_ai.request.model: requested model name
//   - gen_ai.response.model: actual model from response
//   - gen_ai.usage.input_tokens: prompt token count
//   - gen_ai.usage.output_tokens: completion token count
//   - gen_ai.usage.total_tokens: total token count
//   - gen_ai.tool.name: tool name
//
// Eino-specific attributes:
//   - eino.component: component type (ChatModel, Tool, Retriever, etc.)
//   - eino.name: component name
//   - eino.type: implementation type (OpenAI, CustomTool, etc.)
//
// # Performance
//
// The handler implements callbacks.TimingChecker and only registers for
// OnStart, OnEnd, and OnError timings. Stream timings are skipped to avoid
// unnecessary stream copying and goroutine overhead.
package otel
