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

package compose

import (
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestAgenticToolSearchIgnoresEmptyStreamChunks(t *testing.T) {
	search := &schema.ToolResult{Parts: []schema.ToolOutputPart{{
		Type: schema.ToolPartTypeToolSearchResult,
		ToolSearchResult: &schema.ToolSearchResult{Tools: []*schema.ToolInfo{{
			Name: "search_db", Desc: "Search the database",
		}}},
	}}}
	empty := &schema.ToolResult{}
	for _, tc := range []struct {
		name   string
		chunks []*schema.ToolResult
	}{
		{"search_only", []*schema.ToolResult{search}},
		{"empty_before", []*schema.ToolResult{empty, search}},
		{"empty_after", []*schema.ToolResult{search, empty}},
		{"empty_both_sides", []*schema.ToolResult{empty, search, empty}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			node, err := NewAgenticToolsNode(ctx, &ToolsNodeConfig{Tools: []tool.BaseTool{
				&enhancedStreamableTool{
					info: &schema.ToolInfo{Name: "tool_search", Desc: "Find available tools"},
					fn: func(context.Context, *schema.ToolArgument) (*schema.StreamReader[*schema.ToolResult], error) {
						return schema.StreamReaderFromArray(tc.chunks), nil
					},
				},
			}})
			require.NoError(t, err)
			input := &schema.AgenticMessage{ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.FunctionToolCall{
					CallID: "search_call", Name: "tool_search", Arguments: "{}",
				}),
			}}
			invoked, err := node.Invoke(ctx, input)
			require.NoError(t, err, "the same enhanced stream is valid during Invoke")
			require.Len(t, invoked, 1)
			require.Equal(t, schema.ContentBlockTypeToolSearchResult, invoked[0].ContentBlocks[0].Type)
			stream, err := node.Stream(ctx, input)
			require.NoError(t, err)
			defer stream.Close()
			var chunks [][]*schema.AgenticMessage
			for {
				chunk, recvErr := stream.Recv()
				if recvErr == io.EOF {
					break
				}
				require.NoError(t, recvErr)
				chunks = append(chunks, chunk)
			}
			merged, err := schema.ConcatAgenticMessagesArray(chunks)
			require.NoError(t, err, "empty enhanced chunks must not choose a conflicting tool-result type")
			require.Len(t, merged, 1)
			require.Len(t, merged[0].ContentBlocks, 1)
			result := merged[0].ContentBlocks[0].ToolSearchFunctionToolResult
			require.NotNil(t, result)
			require.Equal(t, "search_call", result.CallID)
			require.Equal(t, search.Parts[0].ToolSearchResult, result.Result)
		})
	}
}
