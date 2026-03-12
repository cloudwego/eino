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

// Package tool defines the tool component interfaces that allow language models
// to invoke external capabilities, and helpers for interrupt/resume within tools.
//
// # Interface Hierarchy
//
//	BaseTool                  — Info() only; for passing tool metadata to a ChatModel
//	├── InvokableTool         — standard: args as JSON string, returns string
//	├── StreamableTool        — standard streaming: args as JSON string, returns StreamReader[string]
//	├── EnhancedInvokableTool — multimodal: args as *schema.ToolArgument, returns *schema.ToolResult
//	└── EnhancedStreamableTool— multimodal streaming
//
// # Choosing an Interface
//
// Implement [InvokableTool] for most tools — arguments arrive as a JSON string
// automatically decoded from the model's tool call, and the result is a string
// sent back to the model.
//
// Implement [EnhancedInvokableTool] when the tool needs to return structured
// multimodal content (images, audio, files) rather than plain text. When a
// tool implements both a standard and an enhanced interface, ToolsNode
// prioritises the enhanced interface.
//
// # Creating Tools
//
// The [utils] sub-package provides constructors that eliminate boilerplate:
//   - [utils.InferTool] / [utils.InferStreamTool] — infer parameter schema from Go struct tags
//   - [utils.NewTool] / [utils.NewStreamTool] — manual ToolInfo + typed function
//
// # Interrupt / Resume
//
// Tools can pause execution and wait for external input using [Interrupt],
// [StatefulInterrupt], and [CompositeInterrupt]. Use [GetInterruptState] and
// [GetResumeContext] inside the tool to distinguish first-run from resumed-run.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/
// See https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/how_to_create_a_tool/
package tool
