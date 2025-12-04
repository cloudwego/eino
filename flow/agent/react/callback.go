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

// Package react provides helpers to build callback handlers for React agents.
package react

import (
	"github.com/cloudwego/eino/callbacks"
	template "github.com/cloudwego/eino/utils/callbacks"
)

// BuildAgentCallback builds a callback handler for agent.
// e.g.
//
//	callback := BuildAgentCallback(modelHandler, toolHandler)
//	agent, err := react.NewAgent(ctx, &AgentConfig{})
//	agent.Generate(ctx, input, agent.WithComposeOptions(compose.WithCallbacks(callback)))
func BuildAgentCallback(modelHandler *template.ModelCallbackHandler, toolHandler *template.ToolCallbackHandler) callbacks.Handler {
	return template.NewHandlerHelper().ChatModel(modelHandler).Tool(toolHandler).Handler()
}
