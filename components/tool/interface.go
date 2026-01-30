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

// BaseTool get tool info for ChatModel intent recognition.
type BaseTool interface {
	Info(ctx context.Context) (*schema.ToolInfo, error)
}

// InvokableTool the tool for ChatModel intent recognition and ToolsNode execution.
type InvokableTool interface {
	BaseTool

	// InvokableRun call function with arguments in JSON format
	InvokableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (string, error)
}

// StreamableTool the stream tool for ChatModel intent recognition and ToolsNode execution.
type StreamableTool interface {
	BaseTool

	StreamableRun(ctx context.Context, argumentsInJSON string, opts ...Option) (*schema.StreamReader[string], error)
}

// EnhancedInvokableTool is a tool interface that supports returning structured multimodal results.
// Unlike InvokableTool which returns a string, this interface returns *schema.ToolResult
// which can contain text, images, audio, video, and files.
type EnhancedInvokableTool interface {
	BaseTool
	InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.ToolResult, error)
}

// EnhancedStreamableTool is a streaming tool interface that supports returning structured multimodal results.
// It provides a stream reader for accessing multimodal content progressively.
type EnhancedStreamableTool interface {
	BaseTool
	StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...Option) (*schema.StreamReader[*schema.ToolResult], error)
}
