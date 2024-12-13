/*
 * Copyright 2024 CloudWeGo Authors
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

package template

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestNewComponentTemplate(t *testing.T) {
	t.Run("test no fallback", func(t *testing.T) {
		cnt := 0
		tpl := NewHandlerHelper()
		tpl.ChatModel(&model.CallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *model.CallbackInput) context.Context {
				cnt++
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *model.CallbackOutput) context.Context {
				cnt++
				return ctx
			},
			OnEndWithStreamOutput: func(ctx context.Context, runInfo *callbacks.RunInfo, output *schema.StreamReader[*model.CallbackOutput]) context.Context {
				output.Close()
				cnt++
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				cnt++
				return ctx
			}}).
			Embedding(&embedding.CallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *embedding.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *embedding.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			Prompt(&prompt.CallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *prompt.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *prompt.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			Retriever(&retriever.CallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *retriever.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *retriever.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			Tool(&tool.CallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *tool.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnEndWithStreamOutput: func(ctx context.Context, runInfo *callbacks.RunInfo, output *schema.StreamReader[*tool.CallbackOutput]) context.Context {
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			Lambda(&DefaultCallbackHandler{
				OnStart: func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnStartWithStreamInput: func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
					input.Close()
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnEndWithStreamOutput: func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
					output.Close()
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			})

		typs := []components.Component{
			components.ComponentOfPrompt,
			components.ComponentOfLoaderSplitter,
			components.ComponentOfChatModel,
			components.ComponentOfEmbedding,
			components.ComponentOfRetriever,
			components.ComponentOfTool,
			compose.ComponentOfLambda,
		}

		handler := tpl.Handler()
		ctx := context.Background()
		for _, typ := range typs {
			handler.OnStart(ctx, &callbacks.RunInfo{Component: typ}, nil)
			handler.OnEnd(ctx, &callbacks.RunInfo{Component: typ}, nil)
			handler.OnError(ctx, &callbacks.RunInfo{Component: typ}, fmt.Errorf("mock err"))

			sir, siw := schema.Pipe[callbacks.CallbackInput](1)
			siw.Close()
			handler.OnStartWithStreamInput(ctx, &callbacks.RunInfo{Component: typ}, sir)

			sor, sow := schema.Pipe[callbacks.CallbackOutput](1)
			sow.Close()
			handler.OnEndWithStreamOutput(ctx, &callbacks.RunInfo{Component: typ}, sor)
		}

		assert.Equal(t, 22, cnt)

		ctx = context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: components.ComponentOfTransformer}, handler)
		callbacks.OnStart(ctx, nil)
		assert.Equal(t, 22, cnt)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfPrompt})
		callbacks.OnStart(ctx, nil)
		assert.Equal(t, 23, cnt)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfIndexer})
		callbacks.OnEnd(ctx, nil)
		assert.Equal(t, 23, cnt)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfEmbedding})
		callbacks.OnError(ctx, nil)
		assert.Equal(t, 24, cnt)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader})
		callbacks.OnStart(ctx, nil)
		assert.Equal(t, 24, cnt)

		tpl.Transformer(&document.TransformerCallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *document.TransformerCallbackInput) context.Context {
				cnt++
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *document.TransformerCallbackOutput) context.Context {
				cnt++
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				cnt++
				return ctx
			},
		}).Indexer(&indexer.CallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *indexer.CallbackInput) context.Context {
				cnt++
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *indexer.CallbackOutput) context.Context {
				cnt++
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				cnt++
				return ctx
			},
		}).Loader(&document.LoaderCallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *document.LoaderCallbackInput) context.Context {
				cnt++
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *document.LoaderCallbackOutput) context.Context {
				cnt++
				return ctx
			},
			OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				cnt++
				return ctx
			},
		})

		handler = tpl.Handler()
		ctx = context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: components.ComponentOfTransformer}, handler)
		callbacks.OnEnd(ctx, nil)
		assert.Equal(t, 25, cnt)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfIndexer})
		callbacks.OnStart(ctx, nil)
		assert.Equal(t, 26, cnt)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader})
		callbacks.OnEnd(ctx, nil)
		assert.Equal(t, 27, cnt)
	})

	t.Run("test fallback", func(t *testing.T) {
		cnt, cntf := 0, 0
		tpl := NewHandlerHelper().
			Retriever(&retriever.CallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *retriever.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *retriever.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			Fallback(&DefaultCallbackHandler{
				OnStart: func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
					cntf++
					return ctx
				},
				OnStartWithStreamInput: func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
					input.Close()
					cntf++
					return ctx
				},
				OnEnd: func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
					cntf++
					return ctx
				},
				OnEndWithStreamOutput: func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
					output.Close()
					cntf++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cntf++
					return ctx
				},
			})

		handler := tpl.Handler()
		ctx := context.Background()
		handler.OnStart(ctx, &callbacks.RunInfo{
			Component: compose.ComponentOfLambda,
		}, nil)

		handler.OnEnd(ctx, &callbacks.RunInfo{
			Component: compose.ComponentOfLambda,
		}, nil)

		handler.OnError(ctx, &callbacks.RunInfo{
			Component: compose.ComponentOfLambda,
		}, fmt.Errorf("mock err"))

		sir, siw := schema.Pipe[callbacks.CallbackInput](1)
		siw.Close()
		handler.OnStartWithStreamInput(ctx, &callbacks.RunInfo{
			Component: compose.ComponentOfLambda,
		}, sir)

		sor, sow := schema.Pipe[callbacks.CallbackOutput](1)
		sow.Close()
		handler.OnEndWithStreamOutput(ctx, &callbacks.RunInfo{
			Component: compose.ComponentOfLambda,
		}, sor)

		assert.Equal(t, 0, cnt)
		assert.Equal(t, 5, cntf)

		ctx = context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: compose.ComponentOfGraph}, handler)
		callbacks.OnStart(ctx, nil)
		callbacks.OnEnd(ctx, nil)
		callbacks.OnError(ctx, nil)
		callbacks.OnStartWithStreamInput(ctx, &schema.StreamReader[callbacks.CallbackInput]{})
		callbacks.OnEndWithStreamOutput(ctx, &schema.StreamReader[callbacks.CallbackOutput]{})
		assert.Equal(t, 10, cntf)

		ctx = callbacks.SwitchRunInfo(ctx, &callbacks.RunInfo{Component: components.ComponentOfRetriever})
		callbacks.OnStart(ctx, nil)
		assert.Equal(t, 1, cnt)
	})
}