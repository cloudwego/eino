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

	paths []*NodePath

	maxRunSteps int
}

func (o Option) deepCopy() Option {
	nOptions := make([]any, len(o.options))
	copy(nOptions, o.options)
	nHandler := make([]callbacks.Handler, len(o.handler))
	copy(nHandler, o.handler)
	nPaths := make([]*NodePath, len(o.paths))
	for i, path := range o.paths {
		nPath := *path
		nPaths[i] = &nPath
	}
	return Option{
		options:     nOptions,
		handler:     nHandler,
		paths:       nPaths,
		maxRunSteps: o.maxRunSteps,
	}
}

// DesignateNode set the key of the node which will the option be applied to.
// notice: only effective at the top graph.
// e.g.
//
//	embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
//	runnable.Invoke(ctx, "input", embeddingOption.DesignateNode("my_embedding_node"))
func (o Option) DesignateNode(key ...string) Option {
	nKeys := make([]*NodePath, len(key))
	for i, k := range key {
		nKeys[i] = NewNodePath(k)
	}
	return o.DesignateNodeWithPath(nKeys...)
}

// DesignateNodeWithPath sets the path of the node(s) to which the option will be applied to.
// You can make the option take effect in the subgraph by specifying the key of the subgraph.
// e.g.
// DesignateNodeWithPath({"sub graph node key", "node key within sub graph"})
func (o Option) DesignateNodeWithPath(path ...*NodePath) Option {
	o.paths = append(o.paths, path...)
	return o
}

// DesignateNodePrependPath prepends the prefix to the path of the node(s) to which the option will be applied to.
// Useful when you already have an Option designated to a graph's node, and now you want to add this graph as a subgraph.
// e.g.
// Your subgraph has a Node with key "A", and your subgraph's NodeKey is "sub_graph", you can specify option to A using:
//
// option := WithCallbacks(...).DesignateNode("A").DesignateNodePrependPath("sub_graph")
// Note: as an End User, you probably don't need to use this method, as DesignateNodeWithPath will be sufficient in most use cases.
// Note: as a Flow author, if you define your own Option type, and at the same time your flow can be exported to graph and added as GraphNode,
// you can use this method to prepend your Option's designated path with the GraphNode's path.
func (o Option) DesignateNodePrependPath(prefix *NodePath) Option {
	for i := range o.paths {
		p := o.paths[i]
		p.path = append(prefix.path, p.path...)
	}

	return o
}

// WithEmbeddingOption is a functional option type for embedding component.
// e.g.
//
//	embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
//	runnable.Invoke(ctx, "input", embeddingOption)
func WithEmbeddingOption(opts ...embedding.Option) Option {
	return withComponentOption(opts...)
}

// WithRetrieverOption is a functional option type for retriever component.
// e.g.
//
//	retrieverOption := compose.WithRetrieverOption(retriever.WithIndex("my_index"))
//	runnable.Invoke(ctx, "input", retrieverOption)
func WithRetrieverOption(opts ...retriever.Option) Option {
	return withComponentOption(opts...)
}

// WithLoaderOption is a functional option type for loader component.
// e.g.
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
// e.g.
//
//	indexerOption := compose.WithIndexerOption(indexer.WithSubIndexes([]string{"my_sub_index"}))
//	runnable.Invoke(ctx, "input", indexerOption)
func WithIndexerOption(opts ...indexer.Option) Option {
	return withComponentOption(opts...)
}

// WithChatModelOption is a functional option type for chat model component.
// e.g.
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
		paths:   make([]*NodePath, 0),
	}
}

// WithCallbacks set callback handlers for all components in a single call.
// e.g.
//
//	runnable.Invoke(ctx, "input", compose.WithCallbacks(&myCallbacks{}))
func WithCallbacks(cbs ...callbacks.Handler) Option {
	return Option{
		handler: cbs,
	}
}

// WithRuntimeMaxSteps sets the maximum number of steps for the graph runtime.
// e.g.
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
		paths:   make([]*NodePath, 0),
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
