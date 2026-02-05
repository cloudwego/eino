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
	NewClearToolResult      = internal.NewClearToolResult
	NewToolResultMiddleware = internal.NewToolResultMiddleware
	NewToolResultHandler    = internal.NewToolResultHandler
)
