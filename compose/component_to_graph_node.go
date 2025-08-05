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
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
)

func toComponentNode[I, O, TOption any](
	node any,
	componentType component,
	invoke Invoke[I, O, TOption],
	stream Stream[I, O, TOption],
	collect Collect[I, O, TOption],
	transform Transform[I, O, TOption],
	opts ...GraphAddNodeOpt,
) (*graphNode, *graphAddNodeOpts) {
	meta := parseExecutorInfoFromComponent(componentType, node)
	info, options := getNodeInfo(opts...)
	run := runnableLambda(invoke, stream, collect, transform,
		!meta.isComponentCallbackEnabled,
	)

	gn := toNode(info, run, nil, meta, node, opts...)

	return gn, options
}

func toEmbeddingNode(node embedding.Embedder, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfEmbedding,
		node.EmbedStrings,
		nil,
		nil,
		nil,
		opts...)
}

func toRetrieverNode(node retriever.Retriever, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfRetriever,
		node.Retrieve,
		nil,
		nil,
		nil,
		opts...)
}

func toLoaderNode(node document.Loader, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfLoader,
		node.Load,
		nil,
		nil,
		nil,
		opts...)
}

func toIndexerNode(node indexer.Indexer, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfIndexer,
		node.Store,
		nil,
		nil,
		nil,
		opts...)
}

func toChatModelNode(node model.BaseChatModel, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfChatModel,
		node.Generate,
		node.Stream,
		nil,
		nil,
		opts...)
}

func toChatTemplateNode(node prompt.ChatTemplate, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfPrompt,
		node.Format,
		nil,
		nil,
		nil,
		opts...)
}

func toDocumentTransformerNode(node document.Transformer, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		components.ComponentOfTransformer,
		node.Transform,
		nil,
		nil,
		nil,
		opts...)
}

func toToolsNode(node *ToolsNode, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	return toComponentNode(
		node,
		ComponentOfToolsNode,
		node.Invoke,
		node.Stream,
		nil,
		nil,
		opts...)
}

func toLambdaNode(node *Lambda, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	info, options := getNodeInfo(opts...)

	gn := toNode(info, node.executor, nil, node.executor.meta, node, opts...)

	return gn, options
}

func toAnyGraphNode(node AnyGraph, opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	meta := parseExecutorInfoFromComponent(node.component(), node)
	info, options := getNodeInfo(opts...)

	gn := toNode(info, nil, node, meta, node, opts...)

	return gn, options
}

func toPassthroughNode(opts ...GraphAddNodeOpt) (*graphNode, *graphAddNodeOpts) {
	node := composablePassthrough()
	info, options := getNodeInfo(opts...)
	gn := toNode(info, node, nil, node.meta, node, opts...)
	return gn, options
}

func toNode(nodeInfo *nodeInfo, executor *composableRunnable, graph AnyGraph,
	meta *executorMeta, instance any, opts ...GraphAddNodeOpt) *graphNode {

	if meta == nil {
		meta = &executorMeta{}
	}

	gn := &graphNode{
		nodeInfo: nodeInfo,

		cr:           executor,
		g:            graph,
		executorMeta: meta,

		instance: instance,
		opts:     opts,
	}

	return gn
}
