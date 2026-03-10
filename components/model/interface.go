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

package model

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// BaseChatModel defines the core interface for all chat model implementations.
//
// It exposes two modes of interaction:
//   - [BaseChatModel.Generate]: blocks until the model returns a complete response.
//   - [BaseChatModel.Stream]: returns a [schema.StreamReader] that yields message
//     chunks incrementally as the model generates them.
//
// The input is a slice of [schema.Message] representing the conversation history.
// Messages carry a role (system, user, assistant, tool) and support multimodal
// content (text, images, audio, video). Tool messages must include a ToolCallID
// that correlates them with a prior assistant tool-call message.
//
// Stream usage — the caller is responsible for closing the reader:
//
//	reader, err := m.Stream(ctx, messages)
//	if err != nil { ... }
//	defer reader.Close()
//	for {
//	    chunk, err := reader.Recv()
//	    if errors.Is(err, io.EOF) { break }
//	    if err != nil { ... }
//	    // handle chunk
//	}
//
// Note: a [schema.StreamReader] can only be read once. If multiple consumers
// need the stream, it must be copied before reading.
//
//go:generate  mockgen -destination ../../internal/mock/components/model/ChatModel_mock.go --package model -source interface.go
type BaseChatModel interface {
	Generate(ctx context.Context, input []*schema.Message, opts ...Option) (*schema.Message, error)
	Stream(ctx context.Context, input []*schema.Message, opts ...Option) (
		*schema.StreamReader[*schema.Message], error)
}

// Deprecated: Use [ToolCallingChatModel] instead.
//
// ChatModel extends [BaseChatModel] with tool binding via [ChatModel.BindTools].
// BindTools mutates the instance in place, which causes a race condition when
// the same instance is used concurrently: one goroutine's tool list can
// overwrite another's. Prefer [ToolCallingChatModel.WithTools], which returns
// a new immutable instance and is safe for concurrent use.
type ChatModel interface {
	BaseChatModel

	// BindTools bind tools to the model.
	// BindTools before requesting ChatModel generally.
	// notice the non-atomic problem of BindTools and Generate.
	BindTools(tools []*schema.ToolInfo) error
}

// ToolCallingChatModel extends [BaseChatModel] with safe tool binding.
//
// Unlike the deprecated [ChatModel.BindTools], [ToolCallingChatModel.WithTools]
// does not mutate the receiver — it returns a new instance with the given tools
// attached. This makes it safe to share a base model instance across goroutines
// and derive per-request variants with different tool sets:
//
//	base, _ := openai.NewChatModel(ctx, cfg)           // shared, no tools
//	withSearch, _ := base.WithTools([]*schema.ToolInfo{searchTool})
//	withCalc, _  := base.WithTools([]*schema.ToolInfo{calcTool})
type ToolCallingChatModel interface {
	BaseChatModel

	// WithTools returns a new ToolCallingChatModel instance with the specified tools bound.
	// This method does not modify the current instance, making it safer for concurrent use.
	WithTools(tools []*schema.ToolInfo) (ToolCallingChatModel, error)
}
