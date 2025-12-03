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

// Package multiquery implements a query-rewriting retriever that expands
// user queries into multiple variants to improve recall.
package multiquery

import (
	"context"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/retriever/utils"
	"github.com/cloudwego/eino/schema"
)

const (
	defaultRewritePrompt = `You are an helpful assistant.
	Your role is to create three different versions of the user query to retrieve relevant documents from store.
    Your goal is to improve the performance of similarity search by generating text from different perspectives based on the user query.
	Only provide the generated queries and separate them by newlines. 
	user query: {{query}}`
	defaultQueryVariable = "query"
	defaultMaxQueriesNum = 5
)

var deduplicateFusion = func(ctx context.Context, docs [][]*schema.Document) ([]*schema.Document, error) {
	m := map[string]bool{}
	var ret []*schema.Document
	for i := range docs {
		for j := range docs[i] {
			if _, ok := m[docs[i][j].ID]; !ok {
				m[docs[i][j].ID] = true
				ret = append(ret, docs[i][j])
			}
		}
	}
	return ret, nil
}

// NewRetriever creates a multi-query retriever.
// multi-query retriever is useful when you want to retrieve documents from multiple retrievers with different queries.
// e.g.
//
//	multiRetriever := multiquery.NewRetriever(ctx, &multiquery.Config{})
//	docs, err := multiRetriever.Retrieve(ctx, "how to build agent with eino")
//	if err != nil {
//		...
//	}
//	println(docs)
func NewRetriever(ctx context.Context, config *Config) (retriever.Retriever, error) {
	var err error

	// config validate
	if config.OrigRetriever == nil {
		return nil, fmt.Errorf("OrigRetriever is required")
	}
	if config.RewriteHandler == nil && config.RewriteLLM == nil {
		return nil, fmt.Errorf("at least one of RewriteHandler and RewriteLLM must not be empty")
	}

	// construct rewrite chain
	rewriteChain := compose.NewChain[string, []string]()
	if config.RewriteHandler != nil {
		rewriteChain.AppendLambda(compose.InvokableLambda(config.RewriteHandler), compose.WithNodeName("CustomQueryRewriter"))
	} else {
		tpl := config.RewriteTemplate
		variable := config.QueryVar
		parser := config.LLMOutputParser
		if tpl == nil {
			tpl = prompt.FromMessages(schema.Jinja2, schema.UserMessage(defaultRewritePrompt))
			variable = defaultQueryVariable
		}
		if parser == nil {
			parser = func(ctx context.Context, message *schema.Message) ([]string, error) {
				return strings.Split(message.Content, "\n"), nil
			}
		}

		rewriteChain.
			AppendLambda(compose.InvokableLambda(func(ctx context.Context, input string) (output map[string]any, err error) {
				return map[string]any{variable: input}, nil
			}), compose.WithNodeName("Converter")).
			AppendChatTemplate(tpl).
			AppendChatModel(config.RewriteLLM).
			AppendLambda(compose.InvokableLambda(parser), compose.WithNodeName("OutputParser"))
	}
	rewriteRunner, err := rewriteChain.Compile(ctx, compose.WithGraphName("QueryRewrite"))
	if err != nil {
		return nil, err
	}

	maxQueriesNum := config.MaxQueriesNum
	if maxQueriesNum == 0 {
		maxQueriesNum = defaultMaxQueriesNum
	}

	fusionFunc := config.FusionFunc
	if fusionFunc == nil {
		fusionFunc = deduplicateFusion
	}

	return &multiQueryRetriever{
		queryRunner:   rewriteRunner,
		maxQueriesNum: maxQueriesNum,
		origRetriever: config.OrigRetriever,
		fusionFunc:    fusionFunc,
	}, nil
}

// Config is the config for multi-query retriever.
type Config struct {
	// Rewrite
	// 1. set the following fields to use llm to generate multi queries
	// 	a. chat model, required
	RewriteLLM model.ChatModel
	//	b. prompt llm to generate multi queries, we provide default template so you can leave this field blank
	RewriteTemplate prompt.ChatTemplate
	//	c. origin query variable of your custom template, it can be empty if you use default template
	QueryVar string
	//	d. parser llm output to queries, split content using "\n" by default
	LLMOutputParser func(context.Context, *schema.Message) ([]string, error)
	// 2. set RewriteHandler to provide custom query generation logic, possibly without a ChatModel. If this field is set, it takes precedence over other configurations above
	RewriteHandler func(ctx context.Context, query string) ([]string, error)
	// limit max queries num that Rewrite generates, and excess queries will be truncated, 5 by default
	MaxQueriesNum int

	// Origin Retriever
	OrigRetriever retriever.Retriever

	// fusion docs recalled from multi retrievers, remove dup based on document id by default
	FusionFunc func(ctx context.Context, docs [][]*schema.Document) ([]*schema.Document, error)
}

type multiQueryRetriever struct {
	queryRunner   compose.Runnable[string, []string]
	maxQueriesNum int
	origRetriever retriever.Retriever
	fusionFunc    func(ctx context.Context, docs [][]*schema.Document) ([]*schema.Document, error)
}

// Retrieve retrieves documents from the multi-query retriever.
func (m *multiQueryRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	// generate queries
	queries, err := m.queryRunner.Invoke(ctx, query)
	if err != nil {
		return nil, err
	}
	if len(queries) > m.maxQueriesNum {
		queries = queries[:m.maxQueriesNum]
	}

	// retrieve
	tasks := make([]*utils.RetrieveTask, len(queries))
	for i := range queries {
		tasks[i] = &utils.RetrieveTask{Retriever: m.origRetriever, Query: queries[i]}
	}
	utils.ConcurrentRetrieveWithCallback(ctx, tasks)
	result := make([][]*schema.Document, len(queries))
	for i, task := range tasks {
		if task.Err != nil {
			return nil, task.Err
		}
		result[i] = task.Result
	}

	// fusion
	ctx = ctxWithFusionRunInfo(ctx)
	ctx = callbacks.OnStart(ctx, result)
	fusionDocs, err := m.fusionFunc(ctx, result)
	if err != nil {
		callbacks.OnError(ctx, err)
		return nil, err
	}
	callbacks.OnEnd(ctx, fusionDocs)
	return fusionDocs, nil
}

// GetType returns the type of the retriever (MultiQuery).
func (m *multiQueryRetriever) GetType() string {
	return "MultiQuery"
}

func ctxWithFusionRunInfo(ctx context.Context) context.Context {
	runInfo := &callbacks.RunInfo{
		Component: compose.ComponentOfLambda,
		Type:      "FusionFunc",
	}

	runInfo.Name = runInfo.Type + string(runInfo.Component)

	return callbacks.ReuseHandlers(ctx, runInfo)
}
