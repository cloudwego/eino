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

package compose

import (
	"fmt"
	"reflect"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
)

// Option is a functional option type for calling a graph.
type Option struct {
	options []any
	handler []callbacks.Handler

	nodeHandler []callbacks.Handler // deprecated
	keys        []string

	graphHandler []callbacks.Handler // deprecated
	maxRunSteps  int
}

// DesignateNode set the key of the node which will be used to.
// eg.
//
//	embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
//	runnable.Invoke(ctx, "input", embeddingOption.DesignateNode("my_embedding_node"))
func (o Option) DesignateNode(key ...string) Option {
	o.keys = append(o.keys, key...)
	return o
}

// WithEmbeddingOption is a functional option type for embedding component.
// eg.
//
//	embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
//	runnable.Invoke(ctx, "input", embeddingOption)
func WithEmbeddingOption(opts ...embedding.Option) Option {
	return withComponentOption(opts...)
}

// WithRetrieverOption is a functional option type for retriever component.
// eg.
//
//	retrieverOption := compose.WithRetrieverOption(retriever.WithIndex("my_index"))
//	runnable.Invoke(ctx, "input", retrieverOption)
func WithRetrieverOption(opts ...retriever.Option) Option {
	return withComponentOption(opts...)
}

// Deprecated: use WithLoaderOption instead.
func WithLoaderSplitterOption(opts ...document.LoaderSplitterOption) Option {
	return withComponentOption(opts...)
}

// WithLoaderOption is a functional option type for loader component.
// eg.
//
//	loaderOption := compose.WithLoaderOption(document.WithCollection("my_collection"))
//	runnable.Invoke(ctx, "input", loaderOption)
func WithLoaderOption(opts ...document.LoaderOption) Option {
	return withComponentOption(opts...)
}

// WithDocumentTransformerOption is a functional option type for document transformer component.
func WithDocumentTransformerOption(opts ...document.TransformerOption) Option {
	return withComponentOption(opts...)
}

// WithIndexerOption is a functional option type for indexer component.
// eg.
//
//	indexerOption := compose.WithIndexerOption(indexer.WithSubIndexes([]string{"my_sub_index"}))
//	runnable.Invoke(ctx, "input", indexerOption)
func WithIndexerOption(opts ...indexer.Option) Option {
	return withComponentOption(opts...)
}

// WithChatModelOption is a functional option type for chat model component.
// eg.
//
//	chatModelOption := compose.WithChatModelOption(model.WithTemperature(0.7))
//	runnable.Invoke(ctx, "input", chatModelOption)
func WithChatModelOption(opts ...model.Option) Option {
	return withComponentOption(opts...)
}

// WithChatTemplateOption is a functional option type for chat template component.
func WithChatTemplateOption(opts ...prompt.Option) Option {
	return withComponentOption(opts...)
}

// WithToolsNodeOption is a functional option type for tools node component.
func WithToolsNodeOption(opts ...ToolsNodeOption) Option {
	return withComponentOption(opts...)
}

// WithLambdaOption is a functional option type for lambda component.
func WithLambdaOption(opts ...any) Option {
	return Option{
		options: opts,
		keys:    make([]string, 0),
	}
}

// WithCallbacks set callback handlers for all components in a single call.
// eg.
//
//	runnable.Invoke(ctx, "input", compose.WithCallbacks(&myCallbacks{}))
func WithCallbacks(cbs ...callbacks.Handler) Option {
	return Option{
		handler: cbs,
	}
}

// Deprecated: use WithCallbacks and perform the type checking for component within it instead
func WithNodeCallbacks(cbs ...callbacks.Handler) Option {
	return Option{
		nodeHandler: cbs,
	}
}

// Deprecated: use WithCallbacks and perform the type checking for component within it instead
func WithGraphCallbacks(cbs ...callbacks.Handler) Option {
	return Option{
		graphHandler: cbs,
	}
}

// Deprecated: use WithRuntimeMaxSteps directly instead.
func WithGraphRunOption(opt Option) Option {
	return opt
}

// WithRuntimeMaxSteps sets the maximum number of steps for the graph runtime.
// eg.
//
//	runnable.Invoke(ctx, "input", compose.WithRuntimeMaxSteps(20))
func WithRuntimeMaxSteps(maxSteps int) Option {
	return Option{
		maxRunSteps: maxSteps,
	}
}

func withComponentOption[TOption any](opts ...TOption) Option {
	o := make([]any, 0, len(opts))
	for i := range opts {
		o = append(o, opts[i])
	}
	return Option{
		options: o,
		keys:    make([]string, 0),
	}
}

func convertOption[TOption any](opts ...any) ([]TOption, error) {
	if len(opts) == 0 {
		return nil, nil
	}
	ret := make([]TOption, 0, len(opts))
	for i := range opts {
		o, ok := opts[i].(TOption)
		if !ok {
			return nil, fmt.Errorf("unexpected component option type, expected:%s, actual:%s", reflect.TypeOf((*TOption)(nil)).Elem().String(), reflect.TypeOf(opts[i]).String())
		}
		ret = append(ret, o)
	}
	return ret, nil
}