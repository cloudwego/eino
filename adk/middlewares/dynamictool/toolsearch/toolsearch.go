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

// Package toolsearch provides tool search middleware.
package toolsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for the tool search middleware.
type Config struct {
	// DynamicTools is a list of tools that can be dynamically searched and loaded by the agent.
	DynamicTools []tool.BaseTool
}

// New constructs and returns the tool search middleware.
//
// The tool search middleware enables dynamic tool selection for agents with large tool libraries.
// Instead of passing all tools to the model at once (which can overwhelm context limits),
// this middleware:
//
//  1. Adds a "tool_search" meta-tool that accepts a regex pattern to search tool names
//  2. Initially hides all dynamic tools from the model's tool list
//  3. When the model calls tool_search, matching tools become available for subsequent calls
//
// Example usage:
//
//	middleware, _ := toolsearch.New(ctx, &toolsearch.Config{
//	    DynamicTools: []tool.BaseTool{weatherTool, stockTool, currencyTool, ...},
//	})
//	agent, _ := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
//	    // ...
//	    Handlers: []adk.ChatModelAgentMiddleware{middleware},
//	})
func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if len(config.DynamicTools) == 0 {
		return nil, fmt.Errorf("tools is required")
	}

	return &middleware{
		dynamicTools: config.DynamicTools,
	}, nil
}

type middleware struct {
	adk.BaseChatModelAgentMiddleware
	dynamicTools []tool.BaseTool
}

func (m *middleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	toolNames, err := getToolNames(ctx, m.dynamicTools)
	if err != nil {
		return ctx, nil, fmt.Errorf("failed to get tool names: %w", err)
	}
	nRunCtx.Tools = append(nRunCtx.Tools, newToolSearchTool(toolNames))
	nRunCtx.Tools = append(nRunCtx.Tools, m.dynamicTools...)
	return ctx, &nRunCtx, nil
}

func (m *middleware) WrapModel(_ context.Context, cm model.BaseChatModel, mc *adk.ModelContext) (model.BaseChatModel, error) {
	return &wrapper{allTools: mc.Tools, cm: cm, dynamicTools: m.dynamicTools}, nil
}

type wrapper struct {
	allTools     []*schema.ToolInfo
	dynamicTools []tool.BaseTool

	cm model.BaseChatModel
}

func (w *wrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	tools, err := removeTools(ctx, w.allTools, w.dynamicTools, input)
	if err != nil {
		return nil, fmt.Errorf("failed to load dynamic tools: %w", err)
	}
	return w.cm.Generate(ctx, input, append(opts, model.WithTools(tools))...)
}

func (w *wrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	tools, err := removeTools(ctx, w.allTools, w.dynamicTools, input)
	if err != nil {
		return nil, fmt.Errorf("failed to load dynamic tools: %w", err)
	}
	return w.cm.Stream(ctx, input, append(opts, model.WithTools(tools))...)
}

func newToolSearchTool(toolNames []string) *toolSearchTool {
	return &toolSearchTool{toolNames: toolNames}
}

type toolSearchTool struct {
	toolNames []string
}

const (
	toolSearchToolName = "tool_search"
)

func (t *toolSearchTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "tool_search",
		Desc: "Search for tools using a regex pattern that matches tool names. Returns a list of matching tool names. Use this when you need a tool but don't have it available yet.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"regex_pattern": {
				Type:     schema.String,
				Desc:     "A regex pattern to match tool names against.",
				Required: true,
			},
		}),
	}, nil
}

type toolSearchArgs struct {
	RegexPattern string `json:"regex_pattern"`
}

type toolSearchResult struct {
	SelectedTools []string `json:"selectedTools"`
}

func (t *toolSearchTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	var args toolSearchArgs
	if err := json.Unmarshal([]byte(argumentsInJSON), &args); err != nil {
		return "", fmt.Errorf("failed to unmarshal tool search arguments: %w", err)
	}

	if args.RegexPattern == "" {
		return "", fmt.Errorf("regex_pattern is required")
	}

	re, err := regexp.Compile(args.RegexPattern)
	if err != nil {
		return "", fmt.Errorf("invalid regex pattern: %w", err)
	}

	var matchedTools []string
	for _, name := range t.toolNames {
		if re.MatchString(name) {
			matchedTools = append(matchedTools, name)
		}
	}

	result := toolSearchResult{
		SelectedTools: matchedTools,
	}

	output, err := json.Marshal(result)
	if err != nil {
		return "", fmt.Errorf("failed to marshal result: %w", err)
	}

	return string(output), nil
}

func getToolNames(ctx context.Context, tools []tool.BaseTool) ([]string, error) {
	ret := make([]string, 0, len(tools))
	for _, t := range tools {
		info, err := t.Info(ctx)
		if err != nil {
			return nil, err
		}
		ret = append(ret, info.Name)
	}
	return ret, nil
}

func extractSelectedTools(ctx context.Context, messages []*schema.Message) ([]string, error) {
	var selectedTools []string
	for _, message := range messages {
		if message.Role != schema.Tool || message.ToolName != toolSearchToolName {
			continue
		}

		result := &toolSearchResult{}
		err := json.Unmarshal([]byte(message.Content), result)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal tool search tool result: %w", err)
		}
		selectedTools = append(selectedTools, result.SelectedTools...)
	}
	return selectedTools, nil
}

func invertSelect[T comparable](all []T, selected []T) map[T]struct{} {
	selectedSet := make(map[T]struct{}, len(selected))
	for _, s := range selected {
		selectedSet[s] = struct{}{}
	}

	result := make(map[T]struct{})
	for _, item := range all {
		if _, ok := selectedSet[item]; !ok {
			result[item] = struct{}{}
		}
	}
	return result
}

func removeTools(ctx context.Context, all []*schema.ToolInfo, dynamicTools []tool.BaseTool, messages []*schema.Message) ([]*schema.ToolInfo, error) {
	selectedToolNames, err := extractSelectedTools(ctx, messages)
	if err != nil {
		return nil, err
	}
	dynamicToolNames, err := getToolNames(ctx, dynamicTools)
	if err != nil {
		return nil, err
	}
	removeMap := invertSelect(dynamicToolNames, selectedToolNames)
	ret := make([]*schema.ToolInfo, 0, len(all)-len(dynamicTools))
	for _, info := range all {
		if _, ok := removeMap[info.Name]; ok {
			continue
		}
		ret = append(ret, info)
	}
	return ret, nil
}
