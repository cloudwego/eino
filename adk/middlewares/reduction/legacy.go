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

package reduction

import "github.com/cloudwego/eino/adk/middlewares/reduction/internal"

// Package reduction provides historical compatibility exports for reduction middleware APIs.
//
// DEPRECATED: All top-level exports in this file are maintained exclusively for backward compatibility.
// New reduction middleware implementations are now developed and maintained in this package.
// It is STRONGLY RECOMMENDED that new code directly use the New instead.
//
// Existing code relying on these exports will continue to work indefinitely,
// but no new features or bug fixes will be backported to this compatibility layer.

type (
	ClearToolResultConfig = internal.ClearToolResultConfig
	ToolResultConfig      = internal.ToolResultConfig
	Backend               = internal.Backend
)

var (
	// NewClearToolResult creates a new middleware that clears old tool results
	// based on token thresholds while protecting recent messages.
	//
	// Deprecated: Use New instead, which provides a more comprehensive tool result reduction
	// middleware with both truncation and clearing strategies. New returns a ChatModelAgentMiddleware
	// for better context propagation through wrapper methods.
	NewClearToolResult = internal.NewClearToolResult

	// NewToolResultMiddleware creates a tool result reduction middleware.
	// This middleware combines two strategies to manage tool result tokens:
	//
	//  1. Clearing: Replaces old tool results with a placeholder when the total
	//     tool result tokens exceed the threshold, while protecting recent messages.
	//
	//  2. Offloading: Writes large individual tool results to the filesystem and
	//     returns a summary message guiding the LLM to read the full content.
	//
	// NOTE: If you are using the filesystem middleware (github.com/cloudwego/eino/adk/middlewares/filesystem),
	// this functionality is already included by default. Set Config.WithoutLargeToolResultOffloading = true
	// in the filesystem middleware if you want to use this middleware separately instead.
	//
	// NOTE: This middleware only handles offloading results to the filesystem.
	// You MUST also provide a read_file tool to your agent, otherwise the agent
	// will not be able to read the offloaded content. You can either:
	//   - Use the filesystem middleware (github.com/cloudwego/eino/adk/middlewares/filesystem)
	//     which provides the read_file tool automatically, OR
	//   - Implement your own read_file tool that reads from the same Backend
	//
	// Deprecated: Use New instead, which provides a more comprehensive tool result reduction
	// middleware with both truncation and clearing strategies, per-tool configuration support,
	// and returns a ChatModelAgentMiddleware for better context propagation through wrapper methods.
	NewToolResultMiddleware = internal.NewToolResultMiddleware
)
