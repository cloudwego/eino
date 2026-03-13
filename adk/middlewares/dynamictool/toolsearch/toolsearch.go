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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"text/template"
	"unicode"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
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
//  1. Adds a "tool_search" meta-tool that accepts keyword queries to search tools
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

	tpl, err := template.New("").Parse(systemReminderTpl)
	if err != nil {
		return nil, err
	}

	mapOfDynamicTools := make(map[string]*schema.ToolInfo, len(config.DynamicTools))
	toolNames := make([]string, 0, len(config.DynamicTools))
	for _, t := range config.DynamicTools {
		info, infoErr := t.Info(ctx)
		if infoErr != nil {
			return nil, fmt.Errorf("failed to get dynamic tool info: %w", err)
		}
		toolNames = append(toolNames, info.Name)
		mapOfDynamicTools[info.Name] = info
	}

	buf := &bytes.Buffer{}
	err = tpl.Execute(buf, systemReminder{Tools: toolNames})
	if err != nil {
		return nil, fmt.Errorf("failed to format system reminder template: %w", err)
	}

	return &middleware{
		dynamicTools:      config.DynamicTools,
		mapOfDynamicTools: mapOfDynamicTools,
		sr:                buf.String(),
	}, nil
}

type systemReminder struct {
	Tools []string
}

type middleware struct {
	adk.BaseChatModelAgentMiddleware
	dynamicTools      []tool.BaseTool
	mapOfDynamicTools map[string]*schema.ToolInfo
	sr                string
}

func (m *middleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	nRunCtx.Tools = append(nRunCtx.Tools, newToolSearchTool(m.mapOfDynamicTools))
	nRunCtx.Tools = append(nRunCtx.Tools, m.dynamicTools...)
	return ctx, &nRunCtx, nil
}

func (m *middleware) WrapModel(_ context.Context, cm model.BaseChatModel, mc *adk.ModelContext) (model.BaseChatModel, error) {
	return &wrapper{allTools: mc.Tools, cm: cm, dynamicTools: m.dynamicTools, reminder: m.sr}, nil
}

type wrapper struct {
	allTools     []*schema.ToolInfo
	dynamicTools []tool.BaseTool
	reminder     string

	cm model.BaseChatModel
}

func (w *wrapper) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	tools, err := removeTools(ctx, w.allTools, w.dynamicTools, input)
	if err != nil {
		return nil, fmt.Errorf("failed to load dynamic tools: %w", err)
	}
	return w.cm.Generate(ctx, w.insertReminder(input), append(opts, model.WithTools(tools))...)
}

func (w *wrapper) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	tools, err := removeTools(ctx, w.allTools, w.dynamicTools, input)
	if err != nil {
		return nil, fmt.Errorf("failed to load dynamic tools: %w", err)
	}
	return w.cm.Stream(ctx, w.insertReminder(input), append(opts, model.WithTools(tools))...)
}

func (w *wrapper) insertReminder(input []*schema.Message) []*schema.Message {
	inserted := false
	ret := make([]*schema.Message, 0, len(input)+1)
	for _, m := range input {
		if m.Role != schema.System && !inserted {
			inserted = true
			ret = append(ret, schema.UserMessage(w.reminder))
		}
		ret = append(ret, m)
	}
	if !inserted {
		ret = append(ret, schema.UserMessage(w.reminder))
	}
	return ret
}

func newToolSearchTool(tools map[string]*schema.ToolInfo) *toolSearchTool {
	return &toolSearchTool{tools: tools}
}

type toolSearchTool struct {
	tools map[string]*schema.ToolInfo
}

const (
	toolSearchToolName = "tool_search"
	defaultMaxResults  = 5
)

func (t *toolSearchTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: toolSearchToolName,
		Desc: internal.SelectPrompt(internal.I18nPrompts{
			English: toolDescription,
			Chinese: toolDescriptionChinese,
		}),
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"query": {
				Type:     schema.String,
				Desc:     "Query to find deferred tools. Use \"select:<tool_name>\" for direct selection, or keywords to search.",
				Required: true,
			},
			"max_results": {
				Type:     schema.Integer,
				Desc:     "Maximum number of results to return (default: 5)",
				Required: true,
			},
		}),
	}, nil
}

type toolSearchArgs struct {
	Query      string `json:"query"`
	MaxResults *int   `json:"max_results,omitempty"`
}

type toolSearchResult struct {
	Matches            []string `json:"matches"`
	Query              string   `json:"query"`
	TotalDeferredTools int      `json:"total_deferred_tools"`
}

func (t *toolSearchTool) InvokableRun(_ context.Context, argumentsInJSON *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
	var args toolSearchArgs
	if err := json.Unmarshal([]byte(argumentsInJSON.Text), &args); err != nil {
		return nil, fmt.Errorf("failed to unmarshal tool search arguments: %w", err)
	}

	if args.Query == "" {
		return nil, fmt.Errorf("query is required")
	}

	maxResults := defaultMaxResults
	if args.MaxResults != nil && *args.MaxResults > 0 {
		maxResults = *args.MaxResults
	}

	var matches []string

	// Check for direct selection mode: select:tool1,tool2
	if strings.HasPrefix(args.Query, "select:") {
		names := strings.Split(strings.TrimPrefix(args.Query, "select:"), ",")
		toolSet := make(map[string]bool, len(t.tools))
		for name := range t.tools {
			toolSet[name] = true
		}
		for _, name := range names {
			name = strings.TrimSpace(name)
			if name != "" && toolSet[name] {
				matches = append(matches, name)
			}
		}
	} else {
		matches = t.keywordSearch(args.Query, maxResults)
	}

	ret := make([]*schema.ToolInfo, 0, len(matches))
	for _, name := range matches {
		ti, ok := t.tools[name]
		if !ok {
			continue
		}
		ret = append(ret, ti)
	}

	return &schema.ToolResult{Parts: []schema.ToolOutputPart{
		{
			Type: schema.ToolPartTypeToolSearchResult,
			ToolSearchResult: &schema.ToolSearchResult{
				Tools: ret,
			},
		},
	}}, nil
}

func intMax(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func intMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// scoredTool pairs a tool name with its search score.
type scoredTool struct {
	name  string
	score int
}

// keywordSearch scores all tools against the query keywords and returns the top N.
func (t *toolSearchTool) keywordSearch(query string, maxResults int) []string {
	keywords := parseKeywords(query)
	if len(keywords) == 0 {
		return nil
	}

	var scored []scoredTool

	for name, tm := range t.tools {
		//isMCP := strings.Contains(name, "__")
		nameParts := splitToolName(name)
		nameLower := strings.ToLower(name)
		descLower := strings.ToLower(tm.Desc)

		totalScore := 0
		allRequiredFound := true

		for _, kw := range keywords {
			kwLower := strings.ToLower(kw.word)
			kwScore := 0

			// Score against name parts
			for _, part := range nameParts {
				partLower := strings.ToLower(part)
				if partLower == kwLower {
					// Exact part match
					//if isMCP {
					//	kwScore = intMax(kwScore, 12)
					//} else {
					kwScore = intMax(kwScore, 10)
					//}
				} else if strings.Contains(partLower, kwLower) {
					// Substring part match
					//if isMCP {
					//	kwScore = intMax(kwScore, 6)
					//} else {
					kwScore = intMax(kwScore, 5)
					//}
				}
			}

			// Score against full name
			if strings.Contains(nameLower, kwLower) {
				kwScore = intMax(kwScore, 3)
			}

			// Score against description (word boundary match)
			if descLower != "" && matchWordBoundary(descLower, kwLower) {
				kwScore = intMax(kwScore, 2)
			}

			if kw.required && kwScore == 0 {
				allRequiredFound = false
				break
			}

			totalScore += kwScore
		}

		if !allRequiredFound {
			continue
		}

		if totalScore > 0 {
			scored = append(scored, scoredTool{name: name, score: totalScore})
		}
	}

	// Sort by score descending, then by name for stability
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].score != scored[j].score {
			return scored[i].score > scored[j].score
		}
		return scored[i].name < scored[j].name
	})

	results := make([]string, 0, intMin(maxResults, len(scored)))
	for i := 0; i < len(scored) && i < maxResults; i++ {
		results = append(results, scored[i].name)
	}
	return results
}

// keyword represents a parsed search keyword.
type keyword struct {
	word     string
	required bool
}

// parseKeywords splits a query string into keywords, handling the '+' required prefix.
func parseKeywords(query string) (keywords []keyword) {
	parts := strings.Fields(query)
	for _, p := range parts {
		if strings.HasPrefix(p, "+") {
			word := strings.TrimPrefix(p, "+")
			if word != "" {
				keywords = append(keywords, keyword{word: word, required: true})
			}
		} else if p != "" {
			keywords = append(keywords, keyword{word: p, required: false})
		}
	}
	return
}

// splitToolName splits a tool name into parts by underscores, double underscores (MCP separator),
// and camelCase boundaries.
func splitToolName(name string) []string {
	// First split by double underscore (MCP server__tool separator)
	segments := strings.Split(name, "__")

	var parts []string
	for _, seg := range segments {
		// Split each segment by single underscore
		underscoreParts := strings.Split(seg, "_")
		for _, up := range underscoreParts {
			if up == "" {
				continue
			}
			// Further split by camelCase
			camelParts := splitCamelCase(up)
			parts = append(parts, camelParts...)
		}
	}
	return parts
}

// splitCamelCase splits a camelCase or PascalCase string into its constituent words.
func splitCamelCase(s string) []string {
	if s == "" {
		return nil
	}

	var parts []string
	runes := []rune(s)
	start := 0

	for i := 1; i < len(runes); i++ {
		if unicode.IsUpper(runes[i]) && (i+1 >= len(runes) || unicode.IsLower(runes[i+1]) || unicode.IsLower(runes[i-1])) {
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	parts = append(parts, string(runes[start:]))

	return parts
}

// matchWordBoundary checks if keyword appears at a word boundary in text.
// A word boundary is the start of the string, or preceded by a non-alphanumeric character.
var wordBoundaryCache = map[string]*regexp.Regexp{}

func matchWordBoundary(text, keyword string) bool {
	pattern := `(?i)(?:^|[^a-zA-Z0-9])` + regexp.QuoteMeta(keyword)
	re, ok := wordBoundaryCache[pattern]
	if !ok {
		re = regexp.MustCompile(pattern)
		wordBoundaryCache[pattern] = re
	}
	return re.MatchString(text)
}

// getToolNames extracts just tool names from a slice of BaseTools (used by removeTools).
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

func extractSelectedTools(_ context.Context, messages []*schema.Message) ([]string, error) {
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
		selectedTools = append(selectedTools, result.Matches...)
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
