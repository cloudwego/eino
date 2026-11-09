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

package toolsearch

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type batchedSearchModel struct {
	tools []*schema.ToolInfo
}

func (m *batchedSearchModel) Generate(_ context.Context, _ []*schema.AgenticMessage, opts ...model.Option) (*schema.AgenticMessage, error) {
	m.tools = model.GetCommonOptions(nil, opts...).Tools
	return &schema.AgenticMessage{
		Role:          schema.AgenticRoleTypeAssistant,
		ContentBlocks: []*schema.ContentBlock{schema.NewContentBlock(&schema.AssistantGenText{Text: "done"})},
	}, nil
}

func (m *batchedSearchModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...model.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	msg, err := m.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.AgenticMessage{msg}), nil
}

func TestAgenticToolSearchBatchedHistory(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, shape := range []string{"separate", "batched", "split text", "malformed first"} {
			name := "generate/" + shape
			if streaming {
				name = "stream/" + shape
			}
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				mw, err := NewTyped[*schema.AgenticMessage](ctx, &Config{DynamicTools: []tool.BaseTool{
					&simpleTool{name: "weather", desc: "Weather"},
					&simpleTool{name: "stocks", desc: "Stocks"},
				}})
				require.NoError(t, err)
				cm := &batchedSearchModel{}
				agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[*schema.AgenticMessage]{
					Name: "search", Description: "search", Model: cm,
					Handlers: []adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage]{mw},
				})
				require.NoError(t, err)
				results := []*schema.AgenticMessage{
					makeToolResultMsg[*schema.AgenticMessage](`{"matches":["weather"]}`, "call-weather", toolSearchToolName),
					makeToolResultMsg[*schema.AgenticMessage](`{"matches":["stocks"]}`, "call-stocks", toolSearchToolName),
				}
				if shape == "split text" {
					results[0].ContentBlocks[0].FunctionToolResult.Content = []*schema.FunctionToolResultContentBlock{
						{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: `{"matches":[`}},
						{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: `"weather"]}`}},
					}
				}
				if shape == "malformed first" {
					results = append([]*schema.AgenticMessage{
						makeToolResultMsg[*schema.AgenticMessage](`invalid`, "call-invalid", toolSearchToolName),
					}, results...)
				}
				if shape != "separate" {
					merged, mergeErr := schema.ConcatAgenticMessages(results)
					require.NoError(t, mergeErr)
					results = []*schema.AgenticMessage{merged}
				}
				history := []*schema.AgenticMessage{
					schema.UserAgenticMessage("Find weather and stocks"),
					makeAssistantMsgWithToolCalls[*schema.AgenticMessage]([]testToolCall{
						{ID: "call-weather", Name: toolSearchToolName, Arguments: `{"query":"select:weather"}`},
						{ID: "call-stocks", Name: toolSearchToolName, Arguments: `{"query":"select:stocks"}`},
					}),
				}
				history = append(history, results...)
				runner := adk.NewTypedRunner(adk.TypedRunnerConfig[*schema.AgenticMessage]{Agent: agent, EnableStreaming: streaming})
				iter := runner.Run(ctx, history)
				for {
					event, ok := iter.Next()
					if !ok {
						break
					}
					require.NoError(t, event.Err)
					if event.Output != nil && event.Output.MessageOutput != nil {
						_, err = event.Output.MessageOutput.GetMessage()
						require.NoError(t, err)
					}
				}
				require.Equal(t, []string{"stocks", "tool_search", "weather"}, toolNames(cm.tools))
			})
		}
	}
}
