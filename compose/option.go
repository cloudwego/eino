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
	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose/internal"
)

type Option internal.Option
type GraphAddNodeOpt = internal.GraphAddNodeOpt
type GraphCompileOption = internal.GraphCompileOption
type NewGraphOption = internal.NewGraphOption

func NewNodePath(path ...string) *NodePath {
	return &NodePath{path: path}
}

type NodePath struct {
	path []string
}

// DesignateNode set the key of the node which will the option be applied to.
// notice: only effective at the top graph.
// e.g.
//
//	embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
//	runnable.Invoke(ctx, "input", embeddingOption.DesignateNode("my_embedding_node"))
func (o Option) DesignateNode(key ...string) Option {
	return Option(internal.Option(o).DesignateNode(key...))
}

// DesignateNodeWithPath sets the path of the node(s) to which the option will be applied to.
// You can make the option take effect in the subgraph by specifying the key of the subgraph.
// e.g.
// DesignateNodeWithPath({"sub graph node key", "node key within sub graph"})
func (o Option) DesignateNodeWithPath(path ...*NodePath) Option {
	iPaths := make([]*internal.NodePath, len(path))
	for i, p := range path {
		iPaths[i] = internal.NewNodePath(p.path...)
	}
	return Option(internal.Option(o).DesignateNodeWithPath(iPaths...))
}

// WithEmbeddingOption is a functional option type for embedding component.
// e.g.
//
//	embeddingOption := compose.WithEmbeddingOption(embedding.WithModel("text-embedding-3-small"))
//	runnable.Invoke(ctx, "input", embeddingOption)
func WithEmbeddingOption(opts ...embedding.Option) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithRetrieverOption is a functional option type for retriever component.
// e.g.
//
//	retrieverOption := compose.WithRetrieverOption(retriever.WithIndex("my_index"))
//	runnable.Invoke(ctx, "input", retrieverOption)
func WithRetrieverOption(opts ...retriever.Option) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithLoaderOption is a functional option type for loader component.
// e.g.
//
//	loaderOption := compose.WithLoaderOption(document.WithCollection("my_collection"))
//	runnable.Invoke(ctx, "input", loaderOption)
func WithLoaderOption(opts ...document.LoaderOption) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithDocumentTransformerOption is a functional option type for document transformer component.
func WithDocumentTransformerOption(opts ...document.TransformerOption) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithIndexerOption is a functional option type for indexer component.
// e.g.
//
//	indexerOption := compose.WithIndexerOption(indexer.WithSubIndexes([]string{"my_sub_index"}))
//	runnable.Invoke(ctx, "input", indexerOption)
func WithIndexerOption(opts ...indexer.Option) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithChatModelOption is a functional option type for chat model component.
// e.g.
//
//	chatModelOption := compose.WithChatModelOption(model.WithTemperature(0.7))
//	runnable.Invoke(ctx, "input", chatModelOption)
func WithChatModelOption(opts ...model.Option) Option {
	o := Option(internal.WithComponentOption(opts...))
	return o
}

// WithChatTemplateOption is a functional option type for chat template component.
func WithChatTemplateOption(opts ...prompt.Option) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithToolsNodeOption is a functional option type for tools node component.
func WithToolsNodeOption(opts ...ToolsNodeOption) Option {
	return Option(internal.WithComponentOption(opts...))
}

// WithLambdaOption is a functional option type for lambda component.
func WithLambdaOption(opts ...any) Option {
	return Option(internal.WithLambdaOption(opts...))
}

// WithCallbacks set callback handlers for all components in a single call.
// e.g.
//
//	runnable.Invoke(ctx, "input", compose.WithCallbacks(&myCallbacks{}))
func WithCallbacks(cbs ...callbacks.Handler) Option {
	return Option(internal.WithCallbacks(cbs...))
}

// WithRuntimeMaxSteps sets the maximum number of steps for the graph runtime.
// e.g.
//
//	runnable.Invoke(ctx, "input", compose.WithRuntimeMaxSteps(20))
func WithRuntimeMaxSteps(maxSteps int) Option {
	return Option(internal.WithRuntimeMaxSteps(maxSteps))
}

// WithNodeName sets the name of the node.
func WithNodeName(n string) GraphAddNodeOpt {
	return internal.WithNodeName(n)
}

// WithNodeKey set the node key, which is used to identify the node in the chain.
// only for use in Chain/StateChain.
func WithNodeKey(key string) GraphAddNodeOpt {
	return internal.WithNodeKey(key)
}

// WithInputKey sets the input key of the node.
// this will change the input value of the node, for example, if the pre node's output is map[string]any{"key01": "value01"},
// and the current node's input key is "key01", then the current node's input value will be "value01".
func WithInputKey(k string) GraphAddNodeOpt {
	return internal.WithInputKey(k)
}

// WithOutputKey sets the output key of the node.
// this will change the output value of the node, for example, if the current node's output key is "key01",
// then the node's output value will be map[string]any{"key01": value}.
func WithOutputKey(k string) GraphAddNodeOpt {
	return internal.WithOutputKey(k)
}

// WithGraphCompileOptions when the node is an AnyGraph, use this option to set compile option for the node.
// e.g.
//
//	graph.AddNode("node_name", node, compose.WithGraphCompileOptions(compose.WithGraphName("my_sub_graph")))
func WithGraphCompileOptions(opts ...GraphCompileOption) GraphAddNodeOpt {
	return internal.WithGraphCompileOptions(opts...)
}

// WithStatePreHandler modify node's input of I according to state S and input or store input information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// I: input type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStatePreHandler[I, S any](pre StatePreHandler[I, S]) GraphAddNodeOpt {
	return internal.WithStatePreHandler(pre)
}

// WithStatePostHandler modify node's output of O according to state S and output or store output information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// O: output type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStatePostHandler[O, S any](post StatePostHandler[O, S]) GraphAddNodeOpt {
	return internal.WithStatePostHandler(post)
}

// WithStreamStatePreHandler modify node's streaming input of I according to state S and input or store input information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// when to use: when upstream node's output is an actual stream, and you want the current node's input to remain an actual stream after state pre handler.
// caution: while StreamStatePreHandler is thread safe, modifying state within your own goroutine is NOT.
// I: input type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStreamStatePreHandler[I, S any](pre StreamStatePreHandler[I, S]) GraphAddNodeOpt {
	return internal.WithStreamStatePreHandler(pre)
}

// WithStreamStatePostHandler modify node's streaming output of O according to state S and output or store output information into state, and it's thread-safe.
// notice: this option requires Graph to be created with WithGenLocalState option.
// when to use: when current node's output is an actual stream, and you want the downstream node's input to remain an actual stream after state post handler.
// caution: while StreamStatePostHandler is thread safe, modifying state within your own goroutine is NOT.
// O: output type of the Node like ChatModel, Lambda, Retriever etc.
// S: state type defined in WithGenLocalState
func WithStreamStatePostHandler[O, S any](post StreamStatePostHandler[O, S]) GraphAddNodeOpt {
	return internal.WithStreamStatePostHandler(post)
}

func WithMaxRunSteps(maxSteps int) GraphCompileOption {
	return internal.WithMaxRunSteps(maxSteps)
}

func WithGraphName(graphName string) GraphCompileOption {
	return internal.WithGraphName(graphName)
}

// WithNodeTriggerMode sets node trigger mode for the graph.
// Different node trigger mode will affect graph execution order and result for specific graphs, such as those with parallel branches having different length of nodes.
func WithNodeTriggerMode(triggerMode NodeTriggerMode) GraphCompileOption {
	return internal.WithNodeTriggerMode(internal.NodeTriggerMode(triggerMode))
}

// WithGraphCompileCallbacks sets callbacks for graph compilation.
func WithGraphCompileCallbacks(cbs ...GraphCompileCallback) GraphCompileOption {
	return internal.WithGraphCompileCallbacks(cbs...)
}

// InitGraphCompileCallbacks set global graph compile callbacks,
// which ONLY will be added to top level graph compile options
func InitGraphCompileCallbacks(cbs []GraphCompileCallback) {
	internal.InitGraphCompileCallbacks(cbs)
}

func WithGenLocalState[S any](gls GenLocalState[S]) NewGraphOption {
	return internal.WithGenLocalState[S](gls)
}
