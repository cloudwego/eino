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
	"errors"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/embedding"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose/internal"
)

// Chain is a chain of components.
// Chain nodes can be parallel / branch / sequence components.
// Chain is designed to be used in a builder pattern (should Compile() before use).
// And the interface is `Chain style`, you can use it like: `chain.AppendXX(...).AppendXX(...)`
//
// Normal usage:
//  1. create a chain with input/output type: `chain := NewChain[inputType, outputType]()`
//  2. add components to chainable list:
//     2.1 add components: `chain.AppendChatTemplate(...).AppendChatModel(...).AppendToolsNode(...)`
//     2.2 add parallel or branch node if needed: `chain.AppendParallel()`, `chain.AppendBranch()`
//  3. compile: `r, err := c.Compile()`
//  4. run:
//     4.1 `one input & one output` use `r.Invoke(ctx, input)`
//     4.2 `one input & multi output chunk` use `r.Stream(ctx, input)`
//     4.3 `multi input chunk & one output` use `r.Collect(ctx, inputReader)`
//     4.4 `multi input chunk & multi output chunk` use `r.Transform(ctx, inputReader)`
//
// Using in graph or other chain:
// chain1 := NewChain[inputType, outputType]()
// graph := NewGraph[](runTypePregel)
// graph.AddGraph("key", chain1) // chain is an AnyGraph implementation
//
// // or in another chain:
// chain2 := NewChain[inputType, outputType]()
// chain2.AppendGraph(chain1)
type Chain[I, O any] struct {
	*internal.Chain[I, O]
}

// ErrChainCompiled is returned when attempting to modify a chain after it has been compiled
var ErrChainCompiled = internal.ErrChainCompiled

// NewChain create a chain with input/output type.
func NewChain[I, O any](opts ...NewGraphOption) *Chain[I, O] {
	return &Chain[I, O]{internal.NewChain[I, O](opts...)}
}

// Compile to a Runnable.
// Runnable can be used directly.
// e.g.
//
//		chain := NewChain[string, string]()
//		r, err := chain.Compile()
//		if err != nil {}
//
//	 	r.Invoke(ctx, input) // ping => pong
//		r.Stream(ctx, input) // ping => stream out
//		r.Collect(ctx, inputReader) // stream in => pong
//		r.Transform(ctx, inputReader) // stream in => stream out
func (c *Chain[I, O]) Compile(ctx context.Context, opts ...GraphCompileOption) (Runnable[I, O], error) {
	r, err := c.Chain.Compile(ctx, opts...)
	if err != nil {
		return nil, err
	}

	return convertRunnable(r), nil
}

// AppendChatModel add a ChatModel node to the chain.
// e.g.
//
//	model, err := openai.NewChatModel(ctx, config)
//	if err != nil {...}
//	chain.AppendChatModel(model)
func (c *Chain[I, O]) AppendChatModel(node model.ChatModel, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToChatModelNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendChatTemplate add a ChatTemplate node to the chain.
// e.g.
//
//	chatTemplate, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}.",
//	})
//
//	chain.AppendChatTemplate(chatTemplate)
func (c *Chain[I, O]) AppendChatTemplate(node prompt.ChatTemplate, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToChatTemplateNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendToolsNode add a ToolsNode node to the chain.
// e.g.
//
//	toolsNode, err := tools.NewToolNode(ctx, &tools.ToolsNodeConfig{
//		Tools: []tools.Tool{...},
//	})
//
//	chain.AppendToolsNode(toolsNode)
func (c *Chain[I, O]) AppendToolsNode(node *ToolsNode, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToToolsNode(node.ToolsNode, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendDocumentTransformer add a DocumentTransformer node to the chain.
// e.g.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	chain.AppendDocumentTransformer(markdownSplitter)
func (c *Chain[I, O]) AppendDocumentTransformer(node document.Transformer, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToDocumentTransformerNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendLambda add a Lambda node to the chain.
// Lambda is a node that can be used to implement custom logic.
// e.g.
//
//	lambdaNode := compose.InvokableLambda(func(ctx context.Context, docs []*schema.Document) (string, error) {...})
//	chain.AppendLambda(lambdaNode)
//
// Note:
// to create a Lambda node, you need to use `compose.AnyLambda` or `compose.InvokableLambda` or `compose.StreamableLambda` or `compose.TransformableLambda`.
// if you want this node has real stream output, you need to use `compose.StreamableLambda` or `compose.TransformableLambda`, for example.
func (c *Chain[I, O]) AppendLambda(node *Lambda, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToLambdaNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendEmbedding add a Embedding node to the chain.
// e.g.
//
//	embedder, err := openai.NewEmbedder(ctx, config)
//	if err != nil {...}
//	chain.AppendEmbedding(embedder)
func (c *Chain[I, O]) AppendEmbedding(node embedding.Embedder, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToEmbeddingNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendRetriever add a Retriever node to the chain.
// e.g.
//
//		retriever, err := vectorstore.NewRetriever(ctx, config)
//		if err != nil {...}
//		chain.AppendRetriever(retriever)
//
//	 or using fornax knowledge as retriever:
//
//		config := fornaxknowledge.Config{...}
//		retriever, err := fornaxknowledge.NewKnowledgeRetriever(ctx, config)
//		if err != nil {...}
//		chain.AppendRetriever(retriever)
func (c *Chain[I, O]) AppendRetriever(node retriever.Retriever, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToRetrieverNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendLoader adds a Loader node to the chain.
// e.g.
//
//	loader, err := file.NewFileLoader(ctx, &file.FileLoaderConfig{})
//	if err != nil {...}
//	chain.AppendLoader(loader)
func (c *Chain[I, O]) AppendLoader(node document.Loader, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToLoaderNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendIndexer add an Indexer node to the chain.
// Indexer is a node that can store documents.
// e.g.
//
//	vectorStoreImpl, err := vikingdb.NewVectorStorer(ctx, vikingdbConfig) // in components/vectorstore/vikingdb/vectorstore.go
//	if err != nil {...}
//
//	config := vectorstore.IndexerConfig{VectorStore: vectorStoreImpl}
//	indexer, err := vectorstore.NewIndexer(ctx, config)
//	if err != nil {...}
//
//	chain.AppendIndexer(indexer)
func (c *Chain[I, O]) AppendIndexer(node indexer.Indexer, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToIndexerNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendBranch add a conditional branch to chain.
// Each branch within the ChainBranch can be an AnyGraph.
// All branches should either lead to END, or converge to another node within the Chain.
// e.g.
//
//	cb := compose.NewChainBranch(conditionFunc)
//	cb.AddChatTemplate("chat_template_key_01", chatTemplate)
//	cb.AddChatTemplate("chat_template_key_02", chatTemplate2)
//	chain.AppendBranch(cb)
func (c *Chain[I, O]) AppendBranch(b *ChainBranch) *Chain[I, O] {
	if b == nil {
		c.Err = errors.New("nil chain branch")
		return c
	}
	c.Chain.AppendBranch(b.ChainBranch)
	return c
}

// AppendParallel add a Parallel structure (multiple concurrent nodes) to the chain.
// e.g.
//
//	parallel := compose.NewParallel()
//	parallel.AddChatModel("openai", model1) // => "openai": *schema.Message{}
//	parallel.AddChatModel("maas", model2) // => "maas": *schema.Message{}
//
//	chain.AppendParallel(parallel) // => multiple concurrent nodes are added to the Chain
//
//	The next node in the chain is either an END, or a node which accepts a map[string]any, where keys are `openai` `maas` as specified above.
func (c *Chain[I, O]) AppendParallel(p *Parallel) *Chain[I, O] {
	if p == nil {
		c.Err = errors.New("nil parallel")
		return c
	}
	c.Chain.AppendParallel(p.Parallel)
	return c
}

// AppendGraph add a AnyGraph node to the chain.
// AnyGraph can be a chain or a graph.
// e.g.
//
//	graph := compose.NewGraph[string, string]()
//	chain.AppendGraph(graph)
func (c *Chain[I, O]) AppendGraph(node AnyGraph, opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToAnyGraphNode(node, opts...)
	c.AddNode(gNode, options)
	return c
}

// AppendPassthrough add a Passthrough node to the chain.
// Could be used to connect multiple ChainBranch or Parallel.
// e.g.
//
//	chain.AppendPassthrough()
func (c *Chain[I, O]) AppendPassthrough(opts ...GraphAddNodeOpt) *Chain[I, O] {
	gNode, options := internal.ToPassthroughNode(opts...)
	c.AddNode(gNode, options)
	return c
}

// ChainBranch represents a conditional branch in a chain of operations.
// It allows for dynamic routing of execution based on a condition.
// All branches within ChainBranch are expected to either end the Chain, or converge to another node in the Chain.
type ChainBranch struct {
	*internal.ChainBranch
}

// NewChainBranch creates a new ChainBranch instance based on a given condition.
// It takes a generic type T and a GraphBranchCondition function for that type.
// The returned ChainBranch will have an empty key2BranchNode map and a condition function
// that wraps the provided cond to handle type assertions and error checking.
// e.g.
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
	return &ChainBranch{internal.NewChainBranch[T](cond)}
}

// NewStreamChainBranch creates a new ChainBranch instance based on a given stream condition.
// It takes a generic type T and a StreamGraphBranchCondition function for that type.
// The returned ChainBranch will have an empty key2BranchNode map and a condition function
// that wraps the provided cond to handle type assertions and error checking.
// e.g.
//
//	condition := func(ctx context.Context, in *schema.StreamReader[string], opts ...any) (endNode string, err error) {
//		// logic to determine the next node, you can read the stream and make a decision.
//		// to save time, usually read the first chunk of stream, then make a decision which path to go.
//		return "some_next_node_key", nil
//	}
//
//	cb := NewStreamChainBranch[string](condition)
func NewStreamChainBranch[T any](cond StreamGraphBranchCondition[T]) *ChainBranch {
	return &ChainBranch{internal.NewStreamChainBranch[T](cond)}
}

// AddChatModel adds a ChatModel node to the branch.
// e.g.
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
	gNode, options := internal.ToChatModelNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddChatTemplate adds a ChatTemplate node to the branch.
// e.g.
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
	gNode, options := internal.ToChatTemplateNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddToolsNode adds a ToolsNode to the branch.
// e.g.
//
//	toolsNode, err := tools.NewToolNode(ctx, &tools.ToolsNodeConfig{
//		Tools: []tools.Tool{...},
//	})
//
//	cb.AddToolsNode("tools_node_key", toolsNode)
func (cb *ChainBranch) AddToolsNode(key string, node *ToolsNode, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToToolsNode(node.ToolsNode, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddLambda adds a Lambda node to the branch.
// e.g.
//
//	lambdaFunc := func(ctx context.Context, in string, opts ...any) (out string, err error) {
//		// logic to process the input
//		return "processed_output", nil
//	}
//
//	cb.AddLambda("lambda_node_key", compose.InvokeLambda(lambdaFunc))
func (cb *ChainBranch) AddLambda(key string, node *Lambda, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToLambdaNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddEmbedding adds an Embedding node to the branch.
// e.g.
//
//	embeddingNode, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
//		Model: "text-embedding-3-small",
//	})
//
//	cb.AddEmbedding("embedding_node_key", embeddingNode)
func (cb *ChainBranch) AddEmbedding(key string, node embedding.Embedder, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToEmbeddingNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddRetriever adds a Retriever node to the branch.
// e.g.
//
//	retriever, err := volc_vikingdb.NewRetriever(ctx, &volc_vikingdb.RetrieverConfig{
//		Collection: "my_collection",
//	})
//
//	cb.AddRetriever("retriever_node_key", retriever)
func (cb *ChainBranch) AddRetriever(key string, node retriever.Retriever, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToRetrieverNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddLoader adds a Loader node to the branch.
// e.g.
//
//	pdfParser, err := pdf.NewPDFParser()
//	loader, err := file.NewFileLoader(ctx, &file.FileLoaderConfig{
//		Parser: pdfParser,
//	})
//
//	cb.AddLoader("loader_node_key", loader)
func (cb *ChainBranch) AddLoader(key string, node document.Loader, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToLoaderNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddIndexer adds an Indexer node to the branch.
// e.g.
//
//	indexer, err := volc_vikingdb.NewIndexer(ctx, &volc_vikingdb.IndexerConfig{
//		Collection: "my_collection",
//	})
//
//	cb.AddIndexer("indexer_node_key", indexer)
func (cb *ChainBranch) AddIndexer(key string, node indexer.Indexer, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToIndexerNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddDocumentTransformer adds a Document Transformer node to the branch.
// e.g.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	cb.AddDocumentTransformer("document_transformer_node_key", markdownSplitter)
func (cb *ChainBranch) AddDocumentTransformer(key string, node document.Transformer, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToDocumentTransformerNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddGraph adds a generic Graph node to the branch.
// e.g.
//
//	graph, err := compose.NewGraph[string, string]()
//
//	cb.AddGraph("graph_node_key", graph)
func (cb *ChainBranch) AddGraph(key string, node AnyGraph, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToAnyGraphNode(node, opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// AddPassthrough adds a Passthrough node to the branch.
// e.g.
//
//	cb.AddPassthrough("passthrough_node_key")
func (cb *ChainBranch) AddPassthrough(key string, opts ...GraphAddNodeOpt) *ChainBranch {
	gNode, options := internal.ToPassthroughNode(opts...)
	cb.ChainBranch.AddNode(key, gNode, options)
	return cb
}

// Parallel run multiple nodes in parallel
//
// use `NewParallel()` to create a new parallel type
// Example:
//
//	parallel := NewParallel()
//	parallel.AddChatModel("output_key01", chat01)
//	parallel.AddChatModel("output_key01", chat02)
//
//	chain := NewChain[any,any]()
//	chain.AppendParallel(parallel)
type Parallel struct {
	*internal.Parallel
}

// NewParallel creates a new parallel type.
// it is useful when you want to run multiple nodes in parallel in a chain.
func NewParallel() *Parallel {
	return &Parallel{internal.NewParallel()}
}

// AddChatModel adds a chat model to the parallel.
// e.g.
//
//	chatModel01, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o",
//	})
//
//	chatModel02, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
//		Model: "gpt-4o",
//	})
//
//	p.AddChatModel("output_key01", chatModel01)
//	p.AddChatModel("output_key02", chatModel02)
func (p *Parallel) AddChatModel(outputKey string, node model.ChatModel, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToChatModelNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddChatTemplate adds a chat template to the parallel.
// e.g.
//
//	chatTemplate01, err := prompt.FromMessages(schema.FString, &schema.Message{
//		Role:    schema.System,
//		Content: "You are acting as a {role}.",
//	})
//
//	p.AddChatTemplate("output_key01", chatTemplate01)
func (p *Parallel) AddChatTemplate(outputKey string, node prompt.ChatTemplate, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToChatTemplateNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddToolsNode adds a tools node to the parallel.
// e.g.
//
//	toolsNode, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
//		Tools: []tool.BaseTool{...},
//	})
//
//	p.AddToolsNode("output_key01", toolsNode)
func (p *Parallel) AddToolsNode(outputKey string, node *ToolsNode, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToToolsNode(node.ToolsNode, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddLambda adds a lambda node to the parallel.
// e.g.
//
//	lambdaFunc := func(ctx context.Context, input *schema.Message) ([]*schema.Message, error) {
//		return []*schema.Message{input}, nil
//	}
//
//	p.AddLambda("output_key01", compose.InvokeLambda(lambdaFunc))
func (p *Parallel) AddLambda(outputKey string, node *Lambda, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToLambdaNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddEmbedding adds an embedding node to the parallel.
// e.g.
//
//	embeddingNode, err := openai.NewEmbedder(ctx, &openai.EmbeddingConfig{
//		Model: "text-embedding-3-small",
//	})
//
//	p.AddEmbedding("output_key01", embeddingNode)
func (p *Parallel) AddEmbedding(outputKey string, node embedding.Embedder, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToEmbeddingNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddRetriever adds a retriever node to the parallel.
// e.g.
//
// retriever, err := vikingdb.NewRetriever(ctx, &vikingdb.RetrieverConfig{})
//
//	p.AddRetriever("output_key01", retriever)
func (p *Parallel) AddRetriever(outputKey string, node retriever.Retriever, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToRetrieverNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddLoader adds a loader node to the parallel.
// e.g.
//
//	loader, err := file.NewLoader(ctx, &file.LoaderConfig{})
//
//	p.AddLoader("output_key01", loader)
func (p *Parallel) AddLoader(outputKey string, node document.Loader, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToLoaderNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddIndexer adds an indexer node to the parallel.
// e.g.
//
//	indexer, err := volc_vikingdb.NewIndexer(ctx, &volc_vikingdb.IndexerConfig{
//		Collection: "my_collection",
//	})
//
//	p.AddIndexer("output_key01", indexer)
func (p *Parallel) AddIndexer(outputKey string, node indexer.Indexer, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToIndexerNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddDocumentTransformer adds a Document Transformer node to the parallel.
// e.g.
//
//	markdownSplitter, err := markdown.NewHeaderSplitter(ctx, &markdown.HeaderSplitterConfig{})
//
//	p.AddDocumentTransformer("output_key01", markdownSplitter)
func (p *Parallel) AddDocumentTransformer(outputKey string, node document.Transformer, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToDocumentTransformerNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddGraph adds a graph node to the parallel.
// It is useful when you want to use a graph or a chain as a node in the parallel.
// e.g.
//
//	graph, err := compose.NewChain[any,any]()
//
//	p.AddGraph("output_key01", graph)
func (p *Parallel) AddGraph(outputKey string, node AnyGraph, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToAnyGraphNode(node, append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}

// AddPassthrough adds a passthrough node to the parallel.
// e.g.
//
//	p.AddPassthrough("output_key01")
func (p *Parallel) AddPassthrough(outputKey string, opts ...GraphAddNodeOpt) *Parallel {
	gNode, options := internal.ToPassthroughNode(append(opts, WithOutputKey(outputKey))...)
	p.Parallel.AddNode(outputKey, gNode, options)
	return p
}
