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

type CallInfo struct {
	// Name is the name of the tool to be executed.
	Name string

	// CallID is the unique identifier for this tool call.
	CallID string

	// Arguments contains the arguments for the tool call.
	Arguments string
}

// MultimodalInvokableTool represents a tool that can return structured, multimodal content.
// This interface should be implemented by tools that produce complex outputs beyond a simple string,
// such as content that includes text, images, or other data formats.
type MultimodalInvokableTool interface {
	BaseTool
	// InvokableRun executes the tool with the given call information and options.
	// It returns a ToolOutput, which can contain a mix of different content types.
	InvokableRun(ctx context.Context, callInfo *CallInfo, opts ...Option) (*schema.ToolOutput, error)
}
