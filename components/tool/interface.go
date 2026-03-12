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

package tool

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// BaseTool provides the metadata that a ChatModel uses to decide whether and
// how to call a tool. Info returns a [schema.ToolInfo] containing the tool
// name, description, and parameter JSON schema.
//
// BaseTool alone is sufficient when passing tool definitions to a ChatModel
// via WithTools — the model only needs the schema to generate tool calls.
// To also execute the tool, implement [InvokableTool] or [StreamableTool].
type BaseTool interface {
	Info(ctx context.Context) (*schema.ToolInfo, error)
}

// InvokableTool is a tool that can be executed by ToolsNode.
//
// InvokableRun receives the model's tool call arguments as a JSON-encoded
// string and returns a plain string result that is sent back to the model as
// a tool message. The framework handles JSON decoding automatically when using
// the [utils.InferTool] or [utils.NewTool] constructors.
type InvokableTool interface {
	BaseTool

	// InvokableRun executes the tool with arguments encoded as a JSON string.
	InvokableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (string, error)
}

// StreamableTool is a streaming variant of [InvokableTool].
//
// StreamableRun returns a [schema.StreamReader] that yields string chunks
// incrementally. The caller (ToolsNode) is responsible for closing the reader.
type StreamableTool interface {
	BaseTool

	StreamableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (*schema.StreamReader[string], error)
}

// EnhancedInvokableTool is a tool that returns structured multimodal results.
//
// Unlike [InvokableTool], arguments arrive as a [schema.ToolArgument] (not a
// raw JSON string) and the result is a [schema.ToolResult] which can carry
// text, images, audio, video, and file content.
//
// When a tool implements both a standard and an enhanced interface, ToolsNode
// prioritises the enhanced interface.
type EnhancedInvokableTool interface {
	BaseTool
	InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.ToolResult, error)
}

// EnhancedStreamableTool is the streaming variant of [EnhancedInvokableTool].
//
// It streams [schema.ToolResult] chunks, enabling incremental multimodal
// output. The caller is responsible for closing the returned [schema.StreamReader].
type EnhancedStreamableTool interface {
	BaseTool
	StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.StreamReader[*schema.ToolResult], error)
}
