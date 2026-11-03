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
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type panicWaitTool struct {
	name string
	run  func() (string, error)
}

func (p *panicWaitTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: p.name}, nil
}

func (p *panicWaitTool) InvokableRun(context.Context, string, ...tool.Option) (string, error) {
	return p.run()
}

func TestToolsNodeWaitsForSiblingsOnPanic(t *testing.T) {
	for _, tc := range []struct {
		name       string
		panicFirst bool
		stream     bool
	}{
		{name: "invoke_panic_in_caller", panicFirst: true},
		{name: "invoke_panic_in_worker"},
		{name: "stream_panic_in_caller", panicFirst: true, stream: true},
		{name: "stream_panic_in_worker", stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := make(chan struct{})
			panicking := make(chan struct{})
			release := make(chan struct{})
			finished := make(chan struct{})
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
				<-finished
			}()
			wantPanic := errors.New("tool panic")
			blocking := &panicWaitTool{name: "blocking", run: func() (string, error) {
				close(started)
				defer close(finished)
				<-release
				return "released", nil
			}}
			panics := &panicWaitTool{name: "panics", run: func() (string, error) {
				<-started
				close(panicking)
				panic(wantPanic)
			}}
			node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
				Tools: []tool.BaseTool{blocking, panics},
			})
			require.NoError(t, err)
			order := []string{"blocking", "panics"}
			if tc.panicFirst {
				order = []string{"panics", "blocking"}
			}
			calls := make([]schema.ToolCall, len(order))
			for i, toolName := range order {
				calls[i] = schema.ToolCall{ID: toolName, Function: schema.FunctionCall{Name: toolName, Arguments: "{}"}}
			}
			type result struct {
				panicValue any
				err        error
			}
			done := make(chan result, 1)
			go func() {
				var invokeErr error
				defer func() { done <- result{panicValue: recover(), err: invokeErr} }()
				if tc.stream {
					_, invokeErr = node.Stream(context.Background(), schema.AssistantMessage("", calls))
				} else {
					_, invokeErr = node.Invoke(context.Background(), schema.AssistantMessage("", calls))
				}
			}()
			<-panicking
			select {
			case <-done:
				t.Fatal("ToolsNode returned or unwound its panic while a sibling tool was still running")
			case <-time.After(100 * time.Millisecond):
			}
			close(release)
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("ToolsNode did not finish after its sibling tool was released")
			}
			require.Nil(t, got.panicValue)
			require.ErrorContains(t, got.err, "tool panic")
		})
	}
}

func TestToolsNodeReturnsPanicErrors(t *testing.T) {
	for _, sequential := range []bool{false, true} {
		for _, stream := range []bool{false, true} {
			name := "parallel_invoke"
			if sequential {
				name = "sequential_invoke"
			}
			if stream {
				name += "_stream"
			}
			t.Run(name, func(t *testing.T) {
				panics := &panicWaitTool{name: "panics", run: func() (string, error) {
					panic("single tool panic")
				}}
				node, err := NewToolNode(context.Background(), &ToolsNodeConfig{
					Tools: []tool.BaseTool{panics}, ExecuteSequentially: sequential,
				})
				require.NoError(t, err)
				input := schema.AssistantMessage("", []schema.ToolCall{{
					ID: "panic-id", Function: schema.FunctionCall{Name: "panics", Arguments: "{}"},
				}})
				var callErr error
				require.NotPanics(t, func() {
					if stream {
						_, callErr = node.Stream(context.Background(), input)
					} else {
						_, callErr = node.Invoke(context.Background(), input)
					}
				})
				require.ErrorContains(t, callErr, "single tool panic")
				require.ErrorContains(t, callErr, "panic-id")
			})
		}
	}
}
