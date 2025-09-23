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
	"context"
	"fmt"
	"reflect"
	"time"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
)

type graphCancelChanKey struct{}
type graphCancelChanVal struct {
	ch chan *time.Duration
}

type graphInterruptOptions struct {
	timeout *time.Duration
}

type GraphInterruptOption func(o *graphInterruptOptions)

// WithGraphInterruptTimeout specifies the max waiting time before generating an interrupt.
// After the max waiting time, the graph will force an interrupt. Any unfinished tasks will be re-run when the graph is resumed.
func WithGraphInterruptTimeout(timeout time.Duration) GraphInterruptOption {
	return func(o *graphInterruptOptions) {
		o.timeout = &timeout
	}
}

// WithGraphInterrupt creates a context with graph cancellation support.
// When the returned context is used to invoke a graph or workflow, calling the interrupt function will trigger an interrupt.
// The graph will wait for current tasks to complete by default.
func WithGraphInterrupt(parent context.Context) (ctx context.Context, interrupt func(opts ...GraphInterruptOption)) {
	ch := make(chan *time.Duration, 1)
	ctx = context.WithValue(parent, graphCancelChanKey{}, &graphCancelChanVal{
		ch: ch,
	})
	return ctx, func(opts ...GraphInterruptOption) {
		o := &graphInterruptOptions{}
		for _, opt := range opts {
			opt(o)
		}
		ch <- o.timeout
		close(ch)
	}
}

func getGraphCancel(ctx context.Context) *graphCancelChanVal {
	val, ok := ctx.Value(graphCancelChanKey{}).(*graphCancelChanVal)
	if !ok {
		return nil
	}
	return val
}

// Option is a functional option type for calling a graph.
type Option struct {
	options []any
	handler []callbacks.Handler

	paths []*NodePath

	maxRunSteps         int
	checkPointID        *string
	writeToCheckPointID *string
	forceNewRun         bool
	stateModifier       StateModifier
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

// DesignateNode sets the key of the node to which the option will be applied.
// notice: only effective at the top graph.
// e.g.
//
// embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
// runnable.Invoke(ctx, "input", embeddingOption.DesignateNode("embedding_node_key"))
func (o Option) DesignateNode(nodeKey ...string) Option {
	nKeys := make([]*NodePath, len(nodeKey))
	for i, k := range nodeKey {
		nKeys[i] = NewNodePath(k)
	}
	return o.DesignateNodeWithPath(nKeys...)
}

// DesignateNodeWithPath sets the path of the node(s) to which the option will be applied.
// You can specify a node in the subgraph through `NodePath` to make the option only take effect at this node.
//
// e.g.
// nodePath := NewNodePath("sub_graph_node_key", "node_key_within_sub_graph")
// DesignateNodeWithPath(nodePath)
func (o Option) DesignateNodeWithPath(path ...*NodePath) Option {
	o.paths = append(o.paths, path...)
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
