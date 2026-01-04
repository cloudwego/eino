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

type ToolMeta struct {
	Tool           tool.BaseTool
	ReturnDirectly bool
}

type AgentConfig struct {
	Instruction string
	Tools       []ToolMeta
	Input       *AgentInput
	RunOptions  *AgentRunOptions
}

type AgentRunOptions struct {
}

type HandlerToolsConfig struct {
	tools          []tool.BaseTool
	returnDirectly map[string]bool
}

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

func (c *HandlerToolsConfig) AddTool(t tool.BaseTool) {
	c.tools = append(c.tools, t)
}

func (c *HandlerToolsConfig) AddTools(ts ...tool.BaseTool) {
	c.tools = append(c.tools, ts...)
}

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

func (c *HandlerToolsConfig) SetReturnDirectly(toolName string, returnDirectly bool) {
	if returnDirectly {
		c.returnDirectly[toolName] = true
	} else {
		delete(c.returnDirectly, toolName)
	}
}

func (c *HandlerToolsConfig) Tools() []tool.BaseTool {
	result := make([]tool.BaseTool, len(c.tools))
	copy(result, c.tools)
	return result
}

func (c *HandlerToolsConfig) ReturnDirectly() map[string]bool {
	result := make(map[string]bool)
	for k, v := range c.returnDirectly {
		result[k] = v
	}
	return result
}

func (c *HandlerToolsConfig) ToToolMetas() []ToolMeta {
	result := make([]ToolMeta, len(c.tools))
	for i, t := range c.tools {
		result[i] = ToolMeta{Tool: t, ReturnDirectly: false}
	}
	for name, rd := range c.returnDirectly {
		for i, t := range c.tools {
			info, err := t.Info(context.Background())
			if err != nil {
				continue
			}
			if info.Name == name {
				result[i].ReturnDirectly = rd
				break
			}
		}
	}
	return result
}

func ToolMetasToHandlerToolsConfig(metas []ToolMeta) *HandlerToolsConfig {
	tools := make([]tool.BaseTool, len(metas))
	returnDirectly := make(map[string]bool)
	for i, m := range metas {
		tools[i] = m.Tool
		if m.ReturnDirectly {
			info, err := m.Tool.Info(context.Background())
			if err == nil {
				returnDirectly[info.Name] = true
			}
		}
	}
	return &HandlerToolsConfig{
		tools:          tools,
		returnDirectly: returnDirectly,
	}
}
