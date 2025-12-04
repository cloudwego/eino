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
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

type nodeOptionsPair generic.Pair[*graphNode, *graphAddNodeOpts]

// ChainBranch represents a conditional branch in a chain of operations.
// It allows for dynamic routing of execution based on a condition.
// All branches within ChainBranch are expected to either end the Chain, or converge to another node in the Chain.
type ChainBranch struct {
	internalBranch *GraphBranch
	key2BranchNode map[string]nodeOptionsPair
	err            error
}

// NewChainMultiBranch creates a chain branch where a condition selects
// multiple end nodes to route execution.
func NewChainMultiBranch[T any](cond GraphMultiBranchCondition[T]) *ChainBranch {
	invokeCond := func(ctx context.Context, in T, opts ...any) (endNodes []string, err error) {
		ends, err := cond(ctx, in)
		if err != nil {
			return nil, err
		}
		endNodes = make([]string, 0, len(ends))
		for end := range ends {
			endNodes = append(endNodes, end)
		}
		return endNodes, nil
	}

	return &ChainBranch{
		key2BranchNode: make(map[string]nodeOptionsPair),
		internalBranch: newGraphBranch(newRunnablePacker(invokeCond, nil, nil, nil, false), nil),
	}
}

// NewStreamChainMultiBranch creates a chain branch that selects multiple end
// nodes based on a condition evaluated on the input stream.
func NewStreamChainMultiBranch[T any](cond StreamGraphMultiBranchCondition[T]) *ChainBranch {
	collectCon := func(ctx context.Context, in *schema.StreamReader[T], opts ...any) (endNodes []string, err error) {
		ends, err := cond(ctx, in)
		if err != nil {
			return nil, err
		}
		endNodes = make([]string, 0, len(ends))
		for end := range ends {
			endNodes = append(endNodes, end)
		}
		return endNodes, nil
	}

	return &ChainBranch{
		key2BranchNode: make(map[string]nodeOptionsPair),
		internalBranch: newGraphBranch(newRunnablePacker(nil, nil, collectCon, nil, false), nil),
	}
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
	return NewChainMultiBranch(func(ctx context.Context, in T) (endNode map[string]bool, err error) {
		ret, err := cond(ctx, in)
		if err != nil {
			return nil, err
		}
		return map[string]bool{ret: true}, nil
	})
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
	return NewStreamChainMultiBranch(func(ctx context.Context, in *schema.StreamReader[T]) (endNodes map[string]bool, err error) {
		ret, err := cond(ctx, in)
		if err != nil {
			return nil, err
		}
		return map[string]bool{ret: true}, nil
	})
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
func (cb *ChainBranch) AddChatModel(key string, node model.BaseChatModel, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := toChatModelNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toChatTemplateNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toToolsNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toLambdaNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toEmbeddingNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toRetrieverNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toLoaderNode(node, opts...)
	return cb.addNode(key, gNode, options)
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
	gNode, options := toIndexerNode(node, opts...)
	return cb.addNode(key, gNode, options)
}

// AddDocumentTransformer adds an Document Transformer node to the branch.
// eg.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	cb.AddDocumentTransformer("document_transformer_node_key", markdownSplitter)
func (cb *ChainBranch) AddDocumentTransformer(key string, node document.Transformer, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := toDocumentTransformerNode(node, opts...)
	return cb.addNode(key, gNode, options)
}

// AddGraph adds a generic Graph node to the branch.
// eg.
//
//	graph, err := compose.NewGraph[string, string]()
//
//	cb.AddGraph("graph_node_key", graph)
func (cb *ChainBranch) AddGraph(key string, node AnyGraph, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := toAnyGraphNode(node, opts...)
	return cb.addNode(key, gNode, options)
}

// AddPassthrough adds a Passthrough node to the branch.
// eg.
//
//	cb.AddPassthrough("passthrough_node_key")
func (cb *ChainBranch) AddPassthrough(key string, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := toPassthroughNode(opts...)
	return cb.addNode(key, gNode, options)
}

func (cb *ChainBranch) addNode(key string, node *graphNode, options *graphAddNodeOpts) *ChainBranch {
	if cb.err != nil {
		return cb
	}

	if cb.key2BranchNode == nil {
		cb.key2BranchNode = make(map[string]nodeOptionsPair)
	}

	_, ok := cb.key2BranchNode[key]
	if ok {
		cb.err = fmt.Errorf("chain branch add node, duplicate branch node key= %s", key)
		return cb
	}

	cb.key2BranchNode[key] = nodeOptionsPair{node, options}

	return cb
}
