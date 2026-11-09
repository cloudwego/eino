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

package compose_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/compose"
)

func TestOptionDesignationsRemainIndependent(t *testing.T) {
	for _, mode := range []string{"node", "path", "nested-path"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			graph := compose.NewGraph[string, string]()
			previous := compose.START
			for _, key := range []string{"common1", "common2", "common3", "left", "right"} {
				key := key
				require.NoError(t, graph.AddLambdaNode(key, compose.InvokableLambdaWithOption(
					func(_ context.Context, input string, opts ...bool) (string, error) {
						for _, enabled := range opts {
							if enabled {
								input += key + ";"
							}
						}
						return input, nil
					})))
				require.NoError(t, graph.AddEdge(previous, key))
				previous = key
			}
			require.NoError(t, graph.AddEdge(previous, compose.END))
			if mode == "nested-path" {
				outer := compose.NewGraph[string, string]()
				require.NoError(t, outer.AddGraphNode("sub", graph))
				require.NoError(t, outer.AddEdge(compose.START, "sub"))
				require.NoError(t, outer.AddEdge("sub", compose.END))
				graph = outer
			}
			runnable, err := graph.Compile(ctx)
			require.NoError(t, err)

			designate := func(option compose.Option, key string) compose.Option {
				if mode == "node" {
					return option.DesignateNode(key)
				}
				if mode == "nested-path" {
					return option.DesignateNodeWithPath(compose.NewNodePath("sub", key))
				}
				return option.DesignateNodeWithPath(compose.NewNodePath(key))
			}
			base := compose.WithLambdaOption(true)
			for _, key := range []string{"common1", "common2", "common3"} {
				base = designate(base, key)
			}
			left := designate(base, "left")
			right := designate(base, "right")
			for _, tc := range []struct {
				name   string
				option compose.Option
				want   string
			}{
				{"base", base, "common1;common2;common3;"},
				{"left", left, "common1;common2;common3;left;"},
				{"right", right, "common1;common2;common3;right;"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got, err := runnable.Invoke(ctx, "", tc.option)
					require.NoError(t, err)
					require.Equal(t, tc.want, got)
				})
			}

			// A reusable base option can also be specialized by concurrent callers.
			start := make(chan struct{})
			var wg sync.WaitGroup
			derived := make([]compose.Option, 2)
			for i, key := range []string{"left", "right"} {
				i, key := i, key
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					derived[i] = designate(base, key)
				}()
			}
			close(start)
			wg.Wait()
			for i, key := range []string{"left", "right"} {
				got, err := runnable.Invoke(ctx, "", derived[i])
				require.NoError(t, err)
				require.Equal(t, "common1;common2;common3;"+key+";", got)
			}
		})
	}
}
