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
	"github.com/cloudwego/eino/components/agentic"
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
			AgenticModel(&AgenticModelCallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *agentic.CallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *agentic.CallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnEndWithStreamOutput: func(ctx context.Context, runInfo *callbacks.RunInfo, output *schema.StreamReader[*agentic.CallbackOutput]) context.Context {
					output.Close()
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, runInfo *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			AgenticPrompt(&AgenticPromptCallbackHandler{
				OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *prompt.AgenticCallbackInput) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *prompt.AgenticCallbackOutput) context.Context {
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, runInfo *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			AgenticToolsNode(&AgenticToolsNodeCallbackHandlers{
				OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *schema.AgenticMessage) context.Context {
					cnt++
					return ctx
				},
				OnEnd: func(ctx context.Context, info *callbacks.RunInfo, input []*schema.AgenticMessage) context.Context {
					cnt++
					return ctx
				},
				OnEndWithStreamOutput: func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[[]*schema.AgenticMessage]) context.Context {
					output.Close()
					cnt++
					return ctx
				},
				OnError: func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
					cnt++
					return ctx
				},
			}).
			Handler()

		types := []components.Component{
			components.ComponentOfPrompt,
			components.ComponentOfChatModel,
			components.ComponentOfEmbedding,
			components.ComponentOfRetriever,
			components.ComponentOfTool,
			compose.ComponentOfLambda,
			components.ComponentOfAgenticModel,
			components.ComponentOfAgenticPrompt,
			compose.ComponentOfAgenticToolsNode,
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

		assert.Equal(t, 33, cnt)

		ctx = context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{Component: components.ComponentOfTransformer}, handler)
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 33, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfPrompt})
		ctx = callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 34, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfIndexer})
		callbacks.OnEnd[any](ctx, nil)
		assert.Equal(t, 34, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfEmbedding})
		callbacks.OnError(ctx, nil)
		assert.Equal(t, 35, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader})
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 35, cnt)

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
		}).AgenticPrompt(&AgenticPromptCallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *prompt.AgenticCallbackInput) context.Context {
				cnt++
				return ctx
			},
			OnEnd: func(ctx context.Context, runInfo *callbacks.RunInfo, output *prompt.AgenticCallbackOutput) context.Context {
				cnt++
				return ctx
			},
			OnError: func(ctx context.Context, runInfo *callbacks.RunInfo, err error) context.Context {
				cnt++
				return ctx
			},
		}).AgenticToolsNode(&AgenticToolsNodeCallbackHandlers{
			OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *schema.AgenticMessage) context.Context {
				cnt++
				return ctx
			},
			OnEnd: func(ctx context.Context, info *callbacks.RunInfo, input []*schema.AgenticMessage) context.Context {
				cnt++
				return ctx
			},
			OnEndWithStreamOutput: func(ctx context.Context, info *callbacks.RunInfo, output *schema.StreamReader[[]*schema.AgenticMessage]) context.Context {
				output.Close()
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

		ctx = callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 36, cnt)

		callbacks.OnEnd[any](ctx, nil)
		assert.Equal(t, 37, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader})
		callbacks.OnEnd[any](ctx, nil)
		assert.Equal(t, 38, cnt)

		ctx = callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{Component: compose.ComponentOfToolsNode})
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 39, cnt)

		sr, sw := schema.Pipe[any](0)
		sw.Close()
		callbacks.OnEndWithStreamOutput[any](ctx, sr)
		assert.Equal(t, 40, cnt)

		sr1, sw1 := schema.Pipe[[]*schema.Message](1)
		sw1.Send([]*schema.Message{{}}, nil)
		sw1.Close()
		callbacks.OnEndWithStreamOutput[[]*schema.Message](ctx, sr1)
		// Check AgenticModel stream
		sir2, siw2 := schema.Pipe[callbacks.CallbackOutput](1)
		siw2.Close()
		handler.OnEndWithStreamOutput(ctx, &callbacks.RunInfo{Component: components.ComponentOfAgenticModel}, sir2)
		assert.Equal(t, 42, cnt)

		// Check AgenticToolsNode stream
		sir3, siw3 := schema.Pipe[callbacks.CallbackOutput](1)
		siw3.Close()
		handler.OnEndWithStreamOutput(ctx, &callbacks.RunInfo{Component: compose.ComponentOfAgenticToolsNode}, sir3)
		assert.Equal(t, 43, cnt)

		ctx = callbacks.ReuseHandlers(ctx, nil)
		callbacks.OnStart[any](ctx, nil)
		assert.Equal(t, 43, cnt)
	})

	t.Run("EdgeCases", func(t *testing.T) {
		ctx := context.Background()
		cnt := 0

		// 1. Test Graph and Chain Setters and Execution
		tpl := NewHandlerHelper().
			Graph(callbacks.NewHandlerBuilder().
				OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
					cnt++
					return ctx
				}).Build()).
			Chain(callbacks.NewHandlerBuilder().
				OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
					cnt++
					return ctx
				}).Build())

		h := tpl.Handler()

		// Trigger Graph OnStart
		h.OnStart(ctx, &callbacks.RunInfo{Component: compose.ComponentOfGraph}, nil)
		assert.Equal(t, 1, cnt)

		// Trigger Chain OnEnd
		h.OnEnd(ctx, &callbacks.RunInfo{Component: compose.ComponentOfChain}, nil)
		assert.Equal(t, 2, cnt)

		// 2. Test Needed logic for Graph/Chain when handler is present/absent
		// Graph is present (OnStart)
		needed := h.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: compose.ComponentOfGraph}, callbacks.TimingOnStart)
		assert.True(t, needed)

		// Chain is present (OnEnd) - but we check OnStart which is not defined in the builder above?
		// NewHandlerBuilder returns a handler that usually returns true for Needed if the specific func is not nil.
		// Let's verify Chain OnStart is NOT needed because we only set OnEndFn.
		needed = h.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: compose.ComponentOfChain}, callbacks.TimingOnStart)
		assert.False(t, needed) // Should be false because OnStartFn wasn't set for Chain

		// Lambda is NOT present
		needed = h.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: compose.ComponentOfLambda}, callbacks.TimingOnStart)
		assert.False(t, needed)

		// 3. Test Conversion Fallbacks (Default cases)
		// We need a handler with ToolsNode and AgenticToolsNode to test their conversion fallbacks
		tpl2 := NewHandlerHelper().
			ToolsNode(&ToolsNodeCallbackHandlers{
				OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *schema.Message) context.Context {
					if input == nil {
						cnt++
					}
					return ctx
				},
				OnEnd: func(ctx context.Context, info *callbacks.RunInfo, input []*schema.Message) context.Context {
					if input == nil {
						cnt++
					}
					return ctx
				},
			}).
			AgenticToolsNode(&AgenticToolsNodeCallbackHandlers{
				OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *schema.AgenticMessage) context.Context {
					if input == nil {
						cnt++
					}
					return ctx
				},
				OnEnd: func(ctx context.Context, info *callbacks.RunInfo, input []*schema.AgenticMessage) context.Context {
					if input == nil {
						cnt++
					}
					return ctx
				},
			})

		h2 := tpl2.Handler()

		// Pass wrong type (string) to trigger default case in convToolsNodeCallbackInput -> returns nil
		h2.OnStart(ctx, &callbacks.RunInfo{Component: compose.ComponentOfToolsNode}, "wrong-input-type")
		assert.Equal(t, 3, cnt) // +1

		// Pass wrong type to trigger default case in convToolsNodeCallbackOutput -> returns nil
		h2.OnEnd(ctx, &callbacks.RunInfo{Component: compose.ComponentOfToolsNode}, "wrong-output-type")
		assert.Equal(t, 4, cnt) // +1

		// Pass wrong type to trigger default case in convAgenticToolsNodeCallbackInput -> returns nil
		h2.OnStart(ctx, &callbacks.RunInfo{Component: compose.ComponentOfAgenticToolsNode}, "wrong-input-type")
		assert.Equal(t, 5, cnt) // +1

		// Pass wrong type to trigger default case in convAgenticToolsNodeCallbackOutput -> returns nil
		h2.OnEnd(ctx, &callbacks.RunInfo{Component: compose.ComponentOfAgenticToolsNode}, "wrong-output-type")
		assert.Equal(t, 6, cnt) // +1

		// 4. Test Needed for Agentic components when handlers are Set vs Unset
		// tpl2 has AgenticToolsNode set
		needed = h2.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: compose.ComponentOfAgenticToolsNode}, callbacks.TimingOnStart)
		assert.True(t, needed)

		// tpl2 does NOT have AgenticModel set
		needed = h2.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfAgenticModel}, callbacks.TimingOnStart)
		assert.False(t, needed)

		// Set it now
		tpl2.AgenticModel(&AgenticModelCallbackHandler{
			OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *agentic.CallbackInput) context.Context {
				return ctx
			},
		})

		needed = h2.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfAgenticModel}, callbacks.TimingOnStart)
		assert.True(t, needed)

		// Check invalid component
		needed = h2.(callbacks.TimingChecker).Needed(ctx, &callbacks.RunInfo{Component: "UnknownComponent"}, callbacks.TimingOnStart)
		assert.False(t, needed)

		// Check RunInfo nil
		needed = h2.(callbacks.TimingChecker).Needed(ctx, nil, callbacks.TimingOnStart)
		assert.False(t, needed)

		// 5. Test Needed for Transformer, Loader, Indexer, etc to ensure switch coverage
		tpl3 := NewHandlerHelper().
			Transformer(&TransformerCallbackHandler{OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *document.TransformerCallbackInput) context.Context {
				return ctx
			}}).
			Loader(&LoaderCallbackHandler{OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *document.LoaderCallbackInput) context.Context {
				return ctx
			}}).
			Indexer(&IndexerCallbackHandler{OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *indexer.CallbackInput) context.Context {
				return ctx
			}}).
			Retriever(&RetrieverCallbackHandler{OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *retriever.CallbackInput) context.Context {
				return ctx
			}}).
			Embedding(&EmbeddingCallbackHandler{OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *embedding.CallbackInput) context.Context {
				return ctx
			}}).
			Tool(&ToolCallbackHandler{OnStart: func(ctx context.Context, info *callbacks.RunInfo, input *tool.CallbackInput) context.Context {
				return ctx
			}})

		h3 := tpl3.Handler()
		checker := h3.(callbacks.TimingChecker)

		assert.True(t, checker.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfTransformer}, callbacks.TimingOnStart))
		assert.True(t, checker.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader}, callbacks.TimingOnStart))
		assert.True(t, checker.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfIndexer}, callbacks.TimingOnStart))
		assert.True(t, checker.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfRetriever}, callbacks.TimingOnStart))
		assert.True(t, checker.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfEmbedding}, callbacks.TimingOnStart))
		assert.True(t, checker.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfTool}, callbacks.TimingOnStart))

		// Verify False paths (by using a helper without them)
		emptyH := NewHandlerHelper().Handler().(callbacks.TimingChecker)
		assert.False(t, emptyH.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfTransformer}, callbacks.TimingOnStart))
		assert.False(t, emptyH.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfLoader}, callbacks.TimingOnStart))
		assert.False(t, emptyH.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfIndexer}, callbacks.TimingOnStart))
		assert.False(t, emptyH.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfRetriever}, callbacks.TimingOnStart))
		assert.False(t, emptyH.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfEmbedding}, callbacks.TimingOnStart))
		assert.False(t, emptyH.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfTool}, callbacks.TimingOnStart))

		// 6. Test Needed for remaining components (ChatModel, Prompt, AgenticPrompt)
		tpl4 := NewHandlerHelper().
			ChatModel(&ModelCallbackHandler{OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *model.CallbackInput) context.Context {
				return ctx
			}}).
			Prompt(&PromptCallbackHandler{OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *prompt.CallbackInput) context.Context {
				return ctx
			}}).
			AgenticPrompt(&AgenticPromptCallbackHandler{OnStart: func(ctx context.Context, runInfo *callbacks.RunInfo, input *prompt.AgenticCallbackInput) context.Context {
				return ctx
			}})

		h4 := tpl4.Handler()
		checker4 := h4.(callbacks.TimingChecker)

		assert.True(t, checker4.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfChatModel}, callbacks.TimingOnStart))
		assert.True(t, checker4.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfPrompt}, callbacks.TimingOnStart))
		assert.True(t, checker4.Needed(ctx, &callbacks.RunInfo{Component: components.ComponentOfAgenticPrompt}, callbacks.TimingOnStart))
	})
}
