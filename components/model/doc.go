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

// Package model defines the ChatModel component interface for interacting with
// large language models (LLMs).
//
// # Overview
//
// A ChatModel takes a slice of [schema.Message] as input and returns a response
// message — either in full ([BaseChatModel.Generate]) or incrementally as a
// stream ([BaseChatModel.Stream]). It is the most fundamental building block in
// an eino pipeline: every application that talks to an LLM goes through this
// interface.
//
// Concrete implementations (OpenAI, Ark, Ollama, …) live in eino-ext:
//
//	github.com/cloudwego/eino-ext/components/model/
//
// # Interface Hierarchy
//
//	BaseChatModel         — Generate + Stream (all implementations)
//	├── ToolCallingChatModel  — preferred; WithTools returns a new instance (concurrency-safe)
//	└── ChatModel             — deprecated; BindTools mutates state (avoid in new code)
//
// # Choosing Generate vs Stream
//
// Use [BaseChatModel.Generate] when the full response is needed before
// proceeding (e.g. structured extraction, classification).
// Use [BaseChatModel.Stream] when output should be forwarded to the caller
// incrementally (e.g. chat UI, long-form generation). Always close the
// [schema.StreamReader] returned by Stream — failing to do so leaks the
// underlying connection:
//
//	reader, err := model.Stream(ctx, messages)
//	if err != nil { ... }
//	defer reader.Close()
//
// # Implementing a ChatModel
//
// Implementations must call [GetCommonOptions] to extract standard options and
// [GetImplSpecificOptions] to extract their own options from the Option list.
// Expose implementation-specific options via [WrapImplSpecificOptFn].
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/chat_model_guide/
// for the full component guide.
package model
