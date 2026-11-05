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

package toolsearch_test

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/dynamictool/toolsearch"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

func ExampleConfig_SearchFunc() {
	ctx := context.Background()
	weather := utils.NewTool(&schema.ToolInfo{
		Name: "get_weather",
		Desc: "Get the weather forecast",
	}, func(context.Context, map[string]string) (string, error) {
		return "Rain expected", nil
	})

	// An application owns its index. This small read-only lookup stands in for
	// an external retriever and maps a query to registered tool names.
	index := map[string][]string{"will it rain?": {"get_weather"}}
	middleware, err := toolsearch.New(ctx, &toolsearch.Config{
		DynamicTools: []tool.BaseTool{weather},
		SearchFunc: func(ctx context.Context, query string, maxResults int) ([]string, error) {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			names := index[query]
			if len(names) > maxResults {
				names = names[:maxResults]
			}
			return names, nil
		},
	})
	if err != nil {
		panic(err)
	}

	// Normally the agent invokes this lifecycle hook and then calls tool_search.
	// Invoke them directly here so the example needs no model or credentials.
	_, run, err := middleware.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		panic(err)
	}
	result, err := run.Tools[0].(tool.InvokableTool).InvokableRun(ctx,
		`{"query":"will it rain?","max_results":1}`)
	if err != nil {
		panic(err)
	}
	fmt.Println(result)
	// Output: {"matches":["get_weather"]}
}
