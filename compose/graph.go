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

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose/internal"
	"github.com/cloudwego/eino/schema"
)

const (
	START = internal.START
	END   = internal.END
)

type AnyGraph = internal.AnyGraph

// ErrExceedMaxSteps graph will throw this error when the number of steps exceeds the maximum number of steps.
var ErrExceedMaxSteps = internal.ErrExceedMaxSteps

// Graph is a generic graph that can be used to compose components.
// I: the input type of graph compiled product
// O: the output type of graph compiled product
type Graph[I, O any] struct {
	*internal.Graph[I, O]
}

// NewGraph create a directed graph that can compose components, lambda, chain, parallel etc.
// simultaneously provide flexible and multi-granular aspect governance capabilities.
// I: the input type of graph compiled product
// O: the output type of graph compiled product
//
// To share state between nodes, use WithGenLocalState option:
//
//	type testState struct {
//		UserInfo *UserInfo
//		KVs     map[string]any
//	}
//
//	genStateFunc := func(ctx context.Context) *testState {
//		return &testState{}
//	}
//
//	graph := compose.NewGraph[string, string](WithGenLocalState(genStateFunc))
//
//	// you can use WithPreHandler and WithPostHandler to do something with state
//	graph.AddNode("node1", someNode, compose.WithPreHandler(func(ctx context.Context, in string, state *testState) (string, error) {
//		// do something with state
//		return in, nil
//	}), compose.WithPostHandler(func(ctx context.Context, out string, state *testState) (string, error) {
//		// do something with state
//		return out, nil
//	}))
func NewGraph[I, O any](opts ...NewGraphOption) *Graph[I, O] {
	return &Graph[I, O]{internal.NewGraph[I, O](opts...)}
}

// Compile take the raw graph and compile it into a form ready to be run.
// e.g.
//
//	graph, err := compose.NewGraph[string, string]()
//	if err != nil {...}
//
//	runnable, err := graph.Compile(ctx, compose.WithGraphName("my_graph"))
//	if err != nil {...}
//
//	runnable.Invoke(ctx, "input") // invoke
//	runnable.Stream(ctx, "input") // stream
//	runnable.Collect(ctx, inputReader) // collect
//	runnable.Transform(ctx, inputReader) // transform
func (g *Graph[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	r, err := g.Graph.Compile(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return convertRunnable(r), nil
}

// AddEdge adds an edge to the graph, edge means a data flow from startNode to endNode.
// the previous node's output type must be set to the next node's input type.
// NOTE: startNode and endNode must have been added to the graph before adding edge.
// e.g.
//
//	graph.AddNode("start_node_key", compose.NewPassthroughNode())
//	graph.AddNode("end_node_key", compose.NewPassthroughNode())
//
//	err := graph.AddEdge("start_node_key", "end_node_key")
func (g *Graph[I, O]) AddEdge(startNode, endNode string) (err error) {
	return g.Graph.AddEdge(startNode, endNode)
}

// AddEmbeddingNode adds a node that implements embedding.Embedder.
// e.g.
//
//	embeddingNode, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
//		Model: "text-embedding-3-small",
//	})
//
//	graph.AddEmbeddingNode("embedding_node_key", embeddingNode)
func (g *Graph[I, O]) AddEmbeddingNode(key string, node embedding.Embedder, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToEmbeddingNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddRetrieverNode adds a node that implements retriever.Retriever.
// e.g.
//
//	retriever, err := vikingdb.NewRetriever(ctx, &vikingdb.RetrieverConfig{})
//
//	graph.AddRetrieverNode("retriever_node_key", retrieverNode)
func (g *Graph[I, O]) AddRetrieverNode(key string, node retriever.Retriever, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToRetrieverNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddLoaderNode adds a node that implements document.Loader.
// e.g.
//
//	loader, err := file.NewLoader(ctx, &file.LoaderConfig{})
//
//	graph.AddLoaderNode("loader_node_key", loader)
func (g *Graph[I, O]) AddLoaderNode(key string, node document.Loader, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToLoaderNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddIndexerNode adds a node that implements indexer.Indexer.
// e.g.
//
//	indexer, err := vikingdb.NewIndexer(ctx, &vikingdb.IndexerConfig{})
//
//	graph.AddIndexerNode("indexer_node_key", indexer)
func (g *Graph[I, O]) AddIndexerNode(key string, node indexer.Indexer, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToIndexerNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddChatModelNode add node that implements model.ChatModel.
// e.g.
//
//	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o",
//	})
//
//	graph.AddChatModelNode("chat_model_node_key", chatModel)
func (g *Graph[I, O]) AddChatModelNode(key string, node model.ChatModel, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToChatModelNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddChatTemplateNode add node that implements prompt.ChatTemplate.
// e.g.
//
//	chatTemplate, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}.",
//	})
//
//	graph.AddChatTemplateNode("chat_template_node_key", chatTemplate)
func (g *Graph[I, O]) AddChatTemplateNode(key string, node prompt.ChatTemplate, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToChatTemplateNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddToolsNode adds a node that implements tools.ToolsNode.
// e.g.
//
//	toolsNode, err := tools.NewToolNode(ctx, &tools.ToolsNodeConfig{})
//
//	graph.AddToolsNode("tools_node_key", toolsNode)
func (g *Graph[I, O]) AddToolsNode(key string, node *ToolsNode, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToToolsNode(node.ToolsNode, opts...)
	return g.AddNode(key, gNode, options)
}

// AddDocumentTransformerNode adds a node that implements document.Transformer.
// e.g.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	graph.AddDocumentTransformerNode("document_transformer_node_key", markdownSplitter)
func (g *Graph[I, O]) AddDocumentTransformerNode(key string, node document.Transformer, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToDocumentTransformerNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddLambdaNode add node that implements at least one of Invoke[I, O], Stream[I, O], Collect[I, O], Transform[I, O].
// due to the lack of supporting method generics, we need to use function generics to generate Lambda run as Runnable[I, O].
// for Invoke[I, O], use compose.InvokableLambda()
// for Stream[I, O], use compose.StreamableLambda()
// for Collect[I, O], use compose.CollectableLambda()
// for Transform[I, O], use compose.TransformableLambda()
// for arbitrary combinations of 4 kinds of lambda, use compose.AnyLambda()
func (g *Graph[I, O]) AddLambdaNode(key string, node *Lambda, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToLambdaNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddGraphNode add one kind of Graph[I, O]、Chain[I, O]、StateChain[I, O, S] as a node.
// for Graph[I, O], comes from NewGraph[I, O]()
// for Chain[I, O], comes from NewChain[I, O]()
func (g *Graph[I, O]) AddGraphNode(key string, node AnyGraph, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToAnyGraphNode(node, opts...)
	return g.AddNode(key, gNode, options)
}

// AddPassthroughNode adds a passthrough node to the graph.
// mostly used in pregel mode of graph.
// e.g.
//
//	graph.AddPassthroughNode("passthrough_node_key")
func (g *Graph[I, O]) AddPassthroughNode(key string, opts ...GraphAddNodeOpt) error {
	gNode, options := internal.ToPassthroughNode(opts...)
	return g.AddNode(key, gNode, options)
}

// AddBranch adds a branch to the graph.
// e.g.
//
//	condition := func(ctx context.Context, in string) (string, error) {
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewGraphBranch(condition, endNodes)
//
//	graph.AddBranch("start_node_key", branch)
func (g *Graph[I, O]) AddBranch(startNode string, branch *GraphBranch) (err error) {
	return g.Graph.AddBranch(startNode, branch)
}

// GraphBranchCondition is the condition type for the branch.
type GraphBranchCondition[T any] func(ctx context.Context, in T) (endNode string, err error)

// StreamGraphBranchCondition is the condition type for the stream branch.
type StreamGraphBranchCondition[T any] func(ctx context.Context, in *schema.StreamReader[T]) (endNode string, err error)

// GraphBranch is the branch type for the graph.
// It is used to determine the next node based on the condition.
type GraphBranch = internal.GraphBranch

// NewGraphBranch creates a new graph branch.
// It is used to determine the next node based on the condition.
// e.g.
//
//	condition := func(ctx context.Context, in string) (string, error) {
//		// logic to determine the next node
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewGraphBranch(condition, endNodes)
//
//	graph.AddBranch("key_of_node_before_branch", branch)
func NewGraphBranch[T any](condition GraphBranchCondition[T], endNodes map[string]bool) *GraphBranch {
	return internal.NewGraphBranch(condition, endNodes)
}

// NewStreamGraphBranch creates a new stream graph branch.
// It is used to determine the next node based on the condition of stream input.
// e.g.
//
//	condition := func(ctx context.Context, in *schema.StreamReader[T]) (string, error) {
//		// logic to determine the next node.
//		// to use the feature of stream, you can use the first chunk to determine the next node.
//		return "next_node_key", nil
//	}
//	endNodes := map[string]bool{"path01": true, "path02": true}
//	branch := compose.NewStreamGraphBranch(condition, endNodes)
//
//	graph.AddBranch("key_of_node_before_branch", branch)
func NewStreamGraphBranch[T any](condition StreamGraphBranchCondition[T],
	endNodes map[string]bool) *GraphBranch {
	return internal.NewStreamGraphBranch(condition, endNodes)
}

// GraphNodeInfo the info which end users pass in when they are adding nodes to graph.
type GraphNodeInfo = internal.GraphNodeInfo

// GraphInfo the info which end users pass in when they are compiling a graph.
// it is used in compile callback for user to get the node info and instance.
// you may need all details info of the graph for observation.
type GraphInfo = internal.GraphInfo

// GraphCompileCallback is the callback which will be called when graph compilation finishes.
type GraphCompileCallback = internal.GraphCompileCallback
