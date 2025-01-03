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

package internal

import (
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
)

func toComponentNode[I, O, TOption any]( // nolint: byted_s_args_length_limit
	node any,
	componentType component,
	invoke Invoke[I, O, TOption],
	stream Stream[I, O, TOption],
	collect Collect[I, O, TOption],
	transform Transform[I, O, TOption],
	opts ...GraphAddNodeOpt,
) (*GraphNode, *GraphAddNodeOpts) {
	meta := parseExecutorInfoFromComponent(componentType, node)
	info, options := getNodeInfo(opts...)
	run := runnableLambda(invoke, stream, collect, transform,
		!meta.isComponentCallbackEnabled,
	)

	gn := toNode(info, run, nil, meta, node, opts...)

	return gn, options
}

func ToEmbeddingNode(node embedding.Embedder, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfEmbedding,
		node.EmbedStrings,
		nil,
		nil,
		nil,
		opts...)
}

func ToRetrieverNode(node retriever.Retriever, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfRetriever,
		node.Retrieve,
		nil,
		nil,
		nil,
		opts...)
}

func ToLoaderNode(node document.Loader, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfLoader,
		node.Load,
		nil,
		nil,
		nil,
		opts...)
}

func ToIndexerNode(node indexer.Indexer, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfIndexer,
		node.Store,
		nil,
		nil,
		nil,
		opts...)
}

func ToChatModelNode(node model.ChatModel, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfChatModel,
		node.Generate,
		node.Stream,
		nil,
		nil,
		opts...)
}

func ToChatTemplateNode(node prompt.ChatTemplate, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfPrompt,
		node.Format,
		nil,
		nil,
		nil,
		opts...)
}

func ToDocumentTransformerNode(node document.Transformer, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfTransformer,
		node.Transform,
		nil,
		nil,
		nil,
		opts...)
}

func ToToolsNode(node *ToolsNode, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	return toComponentNode(
		node,
		ComponentOfToolsNode,
		node.Invoke,
		node.Stream,
		nil,
		nil,
		opts...)
}

func ToLambdaNode(node *Lambda, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	info, options := getNodeInfo(opts...)

	gn := toNode(info, node.executor, nil, node.executor.meta, node, opts...)

	return gn, options
}

func ToAnyGraphNode(node AnyGraph, opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	meta := parseExecutorInfoFromComponent(node.component(), node)
	info, options := getNodeInfo(opts...)

	gn := toNode(info, nil, node, meta, node, opts...)

	return gn, options
}

func ToPassthroughNode(opts ...GraphAddNodeOpt) (*GraphNode, *GraphAddNodeOpts) {
	node := composablePassthrough()
	info, options := getNodeInfo(opts...)
	gn := toNode(info, node, nil, node.meta, node, opts...)
	return gn, options
}

func toNode(nodeInfo *nodeInfo, executor *composableRunnable, graph AnyGraph, // nolint: byted_s_args_length_limit
	meta *executorMeta, instance any, opts ...GraphAddNodeOpt) *GraphNode {

	if meta == nil {
		meta = &executorMeta{}
	}

	gn := &GraphNode{
		nodeInfo: nodeInfo,

		cr:           executor,
		g:            graph,
		executorMeta: meta,

		instance: instance,
		opts:     opts,
	}

	return gn
}
