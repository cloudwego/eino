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

package callbacks

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
	t.Run("TestNewComponentTemplate", func(t *testing.T) {
		cnt := 0
		tpl := NewHandlerHelper()
		tpl.ChatModel(&ModelCallbackHandler{
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
			Embedding(&EmbeddingCallbackHandler{
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
			Prompt(&PromptCallbackHandler{
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
			Retriever(&RetrieverCallbackHandler{
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
			Tool(&ToolCallbackHandler{
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
			Lambda(callbacks.NewHandlerBuilder().
				OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
					cnt++
					return ctx
				}).
				OnStartWithStreamInputFn(func(ctx context.Context, info *callbacks.RunInfo, input *schema.StreamReader[callbacks.CallbackInput]) context.Context {
					input.Close()
					cnt++
					return ctx
				}).
				OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
					cnt++
					return ctx
				}).
				OnEndWithStreamOutputFn(func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[callbacks.CallbackOutput]) context.Context {
					output.Close()
					cnt++
					return ctx
				}).
				OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				}).Build()).
			Handler()

		types := []components.Component{
			components.ComponentOfPrompt,
			components.ComponentOfChatModel,
			components.ComponentOfEmbedding,
			components.ComponentOfRetriever,
			components.ComponentOfTool,
			compose.ComponentOfLambda,
		}

		handler := tpl.Handler()
		ctx := context.Background()
		for _, typ := range types {
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
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 22, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfPrompt})
		ctx = callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 23, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfIndexer})
		callbacks.OnEnd[any](ctx, nil)
		assert.Equal(t, 23, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfEmbedding})
		callbacks.OnError(ctx, nil)
		assert.Equal(t, 24, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader})
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 24, cnt)

		tpl.Transformer(&TransformerCallbackHandler{
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
		}).Indexer(&IndexerCallbackHandler{
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
		}).Loader(&LoaderCallbackHandler{
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
		}).ToolsNode(&ToolsNodeCallbackHandlers{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *schema.Message) context.Context {
				cnt++
				return ctx
			},
			OnEndWithStreamOutput: func(ctx context.Context, runInfo *callbacks.RunInfo, output *schema.StreamReader[[]*schema.Message]) context.Context {
				cnt++

				if output == nil {
					return ctx
				}

				for {
					_, err := output.Recv()
					if err != nil {
						return ctx
					}
				}
			},
		})

		handler = tpl.Handler()
		ctx = context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: components.ComponentOfTransformer}, handler)

		ctx = callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 25, cnt)

		callbacks.OnEnd[any](ctx, nil)
		assert.Equal(t, 26, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader})
		callbacks.OnEnd[any](ctx, nil)
		assert.Equal(t, 27, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: compose.ComponentOfToolsNode})
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 28, cnt)

		sr, sw := schema.Pipe[any](0)
		sw.Close()
		callbacks.OnEndWithStreamOutput[any](ctx, sr)
		assert.Equal(t, 29, cnt)

		sr1, sw1 := schema.Pipe[[]*schema.Message](1)
		sw1.Send([]*schema.Message{{}}, nil)
		sw1.Close()
		callbacks.OnEndWithStreamOutput[[]*schema.Message](ctx, sr1)
		assert.Equal(t, 30, cnt)

		callbacks.OnError(ctx, nil)
		assert.Equal(t, 30, cnt)

		ctx = callbacks.ReuseHandlers(ctx, nil)
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 30, cnt)
	})
}
