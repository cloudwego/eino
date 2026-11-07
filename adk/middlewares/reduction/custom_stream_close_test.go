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

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestCustomTruncationClosesUnusedStreamCopy(t *testing.T) {
	for _, enhanced := range []bool{false, true} {
		for _, fail := range []bool{false, true} {
			t.Run(map[bool]string{false: "plain", true: "enhanced"}[enhanced]+"/"+
				map[bool]string{false: "passthrough", true: "handler-error"}[fail], func(t *testing.T) {
				ctx := context.Background()
				failure := errors.New("handler failed")
				mw, err := New(ctx, &Config{
					Backend: filesystem.NewInMemoryBackend(),
					ToolConfig: map[string]*ToolReductionConfig{"custom": {
						TruncHandler: func(context.Context, *ToolDetail) (*TruncResult, error) {
							if fail {
								return nil, failure
							}
							return &TruncResult{NeedTrunc: false}, nil
						},
					}},
				})
				require.NoError(t, err)
				tc := &adk.ToolContext{Name: "custom", CallID: "call-1"}
				if enhanced {
					source, writer := schema.Pipe[*schema.ToolResult](1)
					defer writer.Close()
					endpoint, wrapErr := mw.WrapEnhancedStreamableToolCall(ctx,
						func(context.Context, *schema.ToolArgument, ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
							return source, nil
						}, tc)
					require.NoError(t, wrapErr)
					first := &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: "original"}}}
					if !fail {
						require.False(t, writer.Send(first, nil))
					}
					output, callErr := endpoint(ctx, &schema.ToolArgument{Text: "{}"})
					if fail {
						require.ErrorIs(t, callErr, failure)
					} else {
						require.NoError(t, callErr)
						require.NotNil(t, output)
						got, recvErr := output.Recv()
						require.NoError(t, recvErr)
						require.Equal(t, first, got)
						output.Close()
					}
					require.True(t, writer.Send(&schema.ToolResult{}, nil),
						"the discarded analysis copy must not keep the producer alive")
				} else {
					source, writer := schema.Pipe[string](1)
					defer writer.Close()
					endpoint, wrapErr := mw.WrapStreamableToolCall(ctx,
						func(context.Context, string, ...tool.Option) (*schema.StreamReader[string], error) {
							return source, nil
						}, tc)
					require.NoError(t, wrapErr)
					if !fail {
						require.False(t, writer.Send("original", nil))
					}
					output, callErr := endpoint(ctx, "{}")
					if fail {
						require.ErrorIs(t, callErr, failure)
					} else {
						require.NoError(t, callErr)
						require.NotNil(t, output)
						got, recvErr := output.Recv()
						require.NoError(t, recvErr)
						require.Equal(t, "original", got)
						output.Close()
					}
					require.True(t, writer.Send("late", nil),
						"the discarded analysis copy must not keep the producer alive")
				}
			})
		}
	}
}
