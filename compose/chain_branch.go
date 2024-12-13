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

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// ChainBranch represents a conditional branch in a chain of operations.
// It allows for dynamic routing of execution based on a condition.
// All branches within ChainBranch are expected to either end the Chain, or converge to another node in the Chain.
type ChainBranch struct {
	key2BranchNode map[string]*graphNode
	condition      *composableRunnable
	err            error
}

// NewChainBranch creates a new ChainBranch instance based on a given condition.
// It takes a generic type T and a GraphBranchCondition function for that type.
// The returned ChainBranch will have an empty key2BranchNode map and a condition function
// that wraps the provided cond to handle type assertions and error checking.
// eg.
//
//	condition := func(ctx context.Context, in string, opts ...any) (endNode string, err error) {
//		// logic to determine the next node
//		return "some_next_node_key", nil
//	}
//
//	cb := NewChainBranch[string](condition)
//	cb.AddPassthrough("next_node_key_01", xxx) // node in branch, represent one path of branch
//	cb.AddPassthrough("next_node_key_02", xxx) // node in branch
func NewChainBranch[T any](cond GraphBranchCondition[T]) *ChainBranch {
	invokeCond := func(ctx context.Context, in T, opts ...any) (endNode string, err error) {
		return cond(ctx, in)
	}

	return &ChainBranch{
		key2BranchNode: make(map[string]*graphNode),
		condition:      runnableLambda(invokeCond, nil, nil, nil, false),
	}
}

// NewStreamChainBranch creates a new ChainBranch instance based on a given stream condition.
// It takes a generic type T and a StreamGraphBranchCondition function for that type.
// The returned ChainBranch will have an empty key2BranchNode map and a condition function
// that wraps the provided cond to handle type assertions and error checking.
// eg.
//
//	condition := func(ctx context.Context, in *schema.StreamReader[string], opts ...any) (endNode string, err error) {
//		// logic to determine the next node, you can read the stream and make a decision.
//		// to save time, usually read the first chunk of stream, then make a decision which path to go.
//		return "some_next_node_key", nil
//	}
//
//	cb := NewStreamChainBranch[string](condition)
func NewStreamChainBranch[T any](cond StreamGraphBranchCondition[T]) *ChainBranch {
	collectCon := func(ctx context.Context, in *schema.StreamReader[T], opts ...any) (endNode string, err error) {
		return cond(ctx, in)
	}

	return &ChainBranch{
		key2BranchNode: make(map[string]*graphNode),
		condition:      runnableLambda(nil, nil, collectCon, nil, false),
	}
}

// AddChatModel adds a ChatModel node to the branch.
// eg.
//
//	chatModel01, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o",
//	})
//	chatModel02, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o-mini",
//	})
//	cb.AddChatModel("chat_model_key_01", chatModel01)
//	cb.AddChatModel("chat_model_key_02", chatModel02)
func (cb *ChainBranch) AddChatModel(key string, node model.ChatModel, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toChatModelNode(node, opts...))
}

// AddChatTemplate adds a ChatTemplate node to the branch.
// eg.
//
//	chatTemplate, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}.",
//	})
//
//	cb.AddChatTemplate("chat_template_key_01", chatTemplate)
//
//	chatTemplate2, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}, you are not allowed to chat in other topics.",
//	})
//
//	cb.AddChatTemplate("chat_template_key_02", chatTemplate2)
func (cb *ChainBranch) AddChatTemplate(key string, node prompt.ChatTemplate, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toChatTemplateNode(node, opts...))
}

// AddToolsNode adds a ToolsNode to the branch.
// eg.
//
//	toolsNode, err := tools.NewToolNode(ctx, &tools.ToolsNodeConfig{
//		Tools: []tools.Tool{...},
//	})
//
//	cb.AddToolsNode("tools_node_key", toolsNode)
func (cb *ChainBranch) AddToolsNode(key string, node *ToolsNode, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toToolsNode(node, opts...))
}

// AddLambda adds a Lambda node to the branch.
// eg.
//
//	lambdaFunc := func(ctx context.Context, in string, opts ...any) (out string, err error) {
//		// logic to process the input
//		return "processed_output", nil
//	}
//
//	cb.AddLambda("lambda_node_key", compose.InvokeLambda(lambdaFunc))
func (cb *ChainBranch) AddLambda(key string, node *Lambda, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toLambdaNode(node, opts...))
}

// AddEmbedding adds an Embedding node to the branch.
// eg.
//
//	embeddingNode, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
//		Model: "text-embedding-3-small",
//	})
//
//	cb.AddEmbedding("embedding_node_key", embeddingNode)
func (cb *ChainBranch) AddEmbedding(key string, node embedding.Embedder, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toEmbeddingNode(node, opts...))
}

// AddRetriever adds a Retriever node to the branch.
// eg.
//
//	retriever, err := volc_vikingdb.NewRetriever(ctx, &volc_vikingdb.RetrieverConfig{
//		Collection: "my_collection",
//	})
//
//	cb.AddRetriever("retriever_node_key", retriever)
func (cb *ChainBranch) AddRetriever(key string, node retriever.Retriever, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toRetrieverNode(node, opts...))
}

// AddLoaderSplitter adds a LoaderSplitter node to the branch.
// Deprecated: use AddLoader instead.
func (cb *ChainBranch) AddLoaderSplitter(key string, node document.LoaderSplitter, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toLoaderSplitterNode(node, opts...))
}

// AddLoader adds a Loader node to the branch.
// eg.
//
//	pdfParser, err := pdf.NewPDFParser()
//	loader, err := file.NewFileLoader(ctx, &file.FileLoaderConfig{
//		Parser: pdfParser,
//	})
//
//	cb.AddLoader("loader_node_key", loader)
func (cb *ChainBranch) AddLoader(key string, node document.Loader, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toLoaderNode(node, opts...))
}

// AddIndexer adds an Indexer node to the branch.
// eg.
//
//	indexer, err := volc_vikingdb.NewIndexer(ctx, &volc_vikingdb.IndexerConfig{
//		Collection: "my_collection",
//	})
//
//	cb.AddIndexer("indexer_node_key", indexer)
func (cb *ChainBranch) AddIndexer(key string, node indexer.Indexer, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toIndexerNode(node, opts...))
}

// AddDocumentTransformer adds an Document Transformer node to the branch.
// eg.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	cb.AddDocumentTransformer("document_transformer_node_key", markdownSplitter)
func (cb *ChainBranch) AddDocumentTransformer(key string, node document.Transformer, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toDocumentTransformerNode(node, opts...))
}

// AddGraph adds a generic Graph node to the branch.
// eg.
//
//	graph, err := compose.NewGraph[string, string]()
//
//	cb.AddGraph("graph_node_key", graph)
func (cb *ChainBranch) AddGraph(key string, node AnyGraph, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toAnyGraphNode(node, opts...))
}

// AddPassthrough adds a Passthrough node to the branch.
// eg.
//
//	cb.AddPassthrough("passthrough_node_key")
func (cb *ChainBranch) AddPassthrough(key string, opts ...GraphAddNodeOpt) *ChainBranch {
	return cb.addNode(key, toPassthroughNode(opts...))
}

func (cb *ChainBranch) addNode(key string, node *graphNode) *ChainBranch {
	if cb.err != nil {
		return cb
	}

	if cb.key2BranchNode == nil {
		cb.key2BranchNode = make(map[string]*graphNode)
	}

	_, ok := cb.key2BranchNode[key]
	if ok {
		cb.err = fmt.Errorf("chain branch add node, duplicate branch node key= %s", key)
		return cb
	}

	cb.key2BranchNode[key] = node // nolint: byted_use_map_without_nilcheck

	return cb
}