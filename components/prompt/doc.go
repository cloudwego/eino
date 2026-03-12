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

// Package prompt defines the ChatTemplate component interface for building
// structured message lists from templates and runtime variables.
//
// # Overview
//
// A ChatTemplate takes a variables map and produces a []*schema.Message slice
// ready to pass to a [model.BaseChatModel]. It is typically the first node in
// a pipeline, sitting before the ChatModel.
//
// The built-in [DefaultChatTemplate] supports three template syntaxes:
//   - FString: {variable} substitution
//   - GoTemplate: Go's text/template with conditionals and loops
//   - Jinja2: Jinja2 template syntax
//
// # Construction
//
// Use [FromMessages] to build a template from a list of message templates:
//
//	tmpl := prompt.FromMessages(schema.FString,
//	    schema.SystemMessage("You are a helpful assistant."),
//	    schema.UserMessage("Answer this: {question}"),
//	)
//	msgs, err := tmpl.Format(ctx, map[string]any{"question": "What is eino?"})
//
// Use [schema.MessagesPlaceholder] to insert a dynamic list of messages
// (e.g. conversation history) at a fixed position in the template:
//
//	tmpl := prompt.FromMessages(schema.FString,
//	    schema.SystemMessage("You are a helpful assistant."),
//	    schema.MessagesPlaceholder("history", true),
//	    schema.UserMessage("{question}"),
//	)
//
// # Common Pitfall
//
// Variable mismatches (a key present in the template but missing from the
// variables map) produce a runtime error — there is no compile-time check.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/chat_template_guide/
package prompt
