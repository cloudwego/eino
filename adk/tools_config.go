/*
 * Copyright 2025 CloudWeGo Authors
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

package adk

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
)

// HandlerToolsConfig holds the tools configuration that can be modified by ToolsModifier.
// It provides methods for safe modification of tools and their settings.
type HandlerToolsConfig struct {
	tools          []tool.BaseTool
	returnDirectly map[string]bool
}

// NewHandlerToolsConfig creates a new HandlerToolsConfig with the given tools and return-directly settings.
func NewHandlerToolsConfig(tools []tool.BaseTool, returnDirectly map[string]bool) *HandlerToolsConfig {
	tc := &HandlerToolsConfig{
		tools:          make([]tool.BaseTool, len(tools)),
		returnDirectly: make(map[string]bool),
	}
	copy(tc.tools, tools)
	for k, v := range returnDirectly {
		tc.returnDirectly[k] = v
	}
	return tc
}

// AddTool adds a tool to the configuration.
func (c *HandlerToolsConfig) AddTool(t tool.BaseTool) {
	c.tools = append(c.tools, t)
}

// AddTools adds multiple tools to the configuration.
func (c *HandlerToolsConfig) AddTools(ts ...tool.BaseTool) {
	c.tools = append(c.tools, ts...)
}

// RemoveTool removes a tool by name. Returns true if found and removed.
func (c *HandlerToolsConfig) RemoveTool(ctx context.Context, name string) bool {
	for i, t := range c.tools {
		info, err := t.Info(ctx)
		if err != nil {
			continue
		}
		if info.Name == name {
			c.tools = append(c.tools[:i], c.tools[i+1:]...)
			delete(c.returnDirectly, name)
			return true
		}
	}
	return false
}

// SetReturnDirectly sets whether a tool should cause immediate return when called.
func (c *HandlerToolsConfig) SetReturnDirectly(toolName string, returnDirectly bool) {
	if returnDirectly {
		c.returnDirectly[toolName] = true
	} else {
		delete(c.returnDirectly, toolName)
	}
}

// Tools returns a copy of the current tools list.
func (c *HandlerToolsConfig) Tools() []tool.BaseTool {
	result := make([]tool.BaseTool, len(c.tools))
	copy(result, c.tools)
	return result
}

// ReturnDirectly returns a copy of the return-directly map.
func (c *HandlerToolsConfig) ReturnDirectly() map[string]bool {
	result := make(map[string]bool)
	for k, v := range c.returnDirectly {
		result[k] = v
	}
	return result
}
