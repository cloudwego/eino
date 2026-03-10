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

// Package utils provides constructors for building tool implementations without
// writing boilerplate JSON serialization code.
//
// # Choosing a Constructor
//
// There are two main strategies:
//
//  1. Infer from struct tags (recommended): [InferTool], [InferStreamTool],
//     [InferEnhancedTool], [InferEnhancedStreamTool].
//     The parameter JSON schema is derived automatically from the input struct's
//     field names and tags. Requires a typed input struct.
//
//  2. Manual ToolInfo: [NewTool], [NewStreamTool], [NewEnhancedTool],
//     [NewEnhancedStreamTool].
//     You supply a [schema.ToolInfo] directly. Useful when the schema cannot
//     be expressed as a Go struct, or must be dynamically constructed.
//
// # Struct Tag Convention
//
// InferTool and friends use the following tags on the input struct fields:
//
//	type Input struct {
//	    Query    string `json:"query"     jsonschema:"required"             jsonschema_description:"The search query"`
//	    MaxItems int    `json:"max_items"                                   jsonschema_description:"Maximum results to return"`
//	}
//
// Key rules:
//   - Use a separate jsonschema_description tag for field descriptions —
//     embedding descriptions inside the jsonschema tag causes comma-parsing
//     issues.
//   - Use jsonschema:"required" to mark mandatory parameters.
//   - The json tag controls the parameter name visible to the model.
//
// # Schema Utilities
//
// [GoStruct2ToolInfo] and [GoStruct2ParamsOneOf] convert a Go struct to schema
// types without creating a tool — useful for ChatModel structured output via
// ResponseFormat or BindTools.
//
// See https://www.cloudwego.io/docs/eino/core_modules/components/tools_node_guide/how_to_create_a_tool/
package utils
