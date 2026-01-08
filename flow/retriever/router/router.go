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

// Package router provides retrieval routing helpers that merge results
// from multiple retrievers and apply ranking strategies.
package router

import (
	"context"
	"fmt"
	"sort"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/retriever/utils"
	"github.com/cloudwego/eino/schema"
)

var rrf = func(ctx context.Context, result map[string][]*schema.Document) ([]*schema.Document, error) {
	if len(result) < 1 {
		return nil, fmt.Errorf("no documents")
	}
	if len(result) == 1 {
		for _, docs := range result {
			return docs, nil
		}
	}

	docRankMap := make(map[string]float64)
	docMap := make(map[string]*schema.Document)
	for _, v := range result {
		for i := range v {
			docMap[v[i].ID] = v[i]
			if _, ok := docRankMap[v[i].ID]; !ok {
				docRankMap[v[i].ID] = 1.0 / float64(i+60)
			} else {
				docRankMap[v[i].ID] += 1.0 / float64(i+60)
			}
		}
	}
	docList := make([]*schema.Document, 0, len(docMap))
	for id := range docMap {
		docList = append(docList, docMap[id])
	}

	sort.Slice(docList, func(i, j int) bool {
		return docRankMap[docList[i].ID] > docRankMap[docList[j].ID]
	})

	return docList, nil
}

// NewRetriever creates a router retriever.
// router retriever is useful when you want to retrieve documents from multiple retrievers with different queries.
// eg.
//
//	routerRetriever := router.NewRetriever(ctx, &router.Config{})
//	docs, err := routerRetriever.Retrieve(ctx, "how to build agent with eino")
//	if err != nil {
//		...
//	}
//	println(docs)
func NewRetriever(ctx context.Context, config *Config) (retriever.Retriever, error) {
	if len(config.Retrievers) == 0 {
		return nil, fmt.Errorf("retrievers is empty")
	}

	router := config.Router
	if router == nil {
		var retrieverSet []string
		for k := range config.Retrievers {
			retrieverSet = append(retrieverSet, k)
		}
		router = func(ctx context.Context, query string) ([]string, error) {
			return retrieverSet, nil
		}
	}

	fusion := config.FusionFunc
	if fusion == nil {
		fusion = rrf
	}

	return &routerRetriever{
		retrievers: config.Retrievers,
		router:     config.Router,
		fusionFunc: fusion,
	}, nil
}

// Config is the config for router retriever.
type Config struct {
	// Retrievers is the retrievers to be used.
	Retrievers map[string]retriever.Retriever
	// Router is the function to route the query to the retrievers.
	Router func(ctx context.Context, query string) ([]string, error)
	// FusionFunc is the function to fuse the documents from the retrievers.
	FusionFunc func(ctx context.Context, result map[string][]*schema.Document) ([]*schema.Document, error)
}

type routerRetriever struct {
	retrievers map[string]retriever.Retriever
	router     func(ctx context.Context, query string) ([]string, error)
	fusionFunc func(ctx context.Context, result map[string][]*schema.Document) ([]*schema.Document, error)
}

// Retrieve retrieves documents from the router retriever.
func (e *routerRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	routeCtx := ctxWithRouterRunInfo(ctx)
	routeCtx = callbacks.OnStart(routeCtx, query)
	retrieverNames, err := e.router(routeCtx, query)
	if err != nil {
		callbacks.OnError(routeCtx, err)
		return nil, err
	}
	if len(retrieverNames) == 0 {
		err = fmt.Errorf("no retriever has been selected")
		callbacks.OnError(routeCtx, err)
		return nil, err
	}
	callbacks.OnEnd(routeCtx, retrieverNames)

	// retrieve
	tasks := make([]*utils.RetrieveTask, len(retrieverNames))
	for i := range retrieverNames {
		r, ok := e.retrievers[retrieverNames[i]]
		if !ok {
			return nil, fmt.Errorf("router output[%s] has not registered", retrieverNames[i])
		}
		tasks[i] = &utils.RetrieveTask{
			Name:            retrieverNames[i],
			Retriever:       r,
			Query:           query,
			RetrieveOptions: opts,
		}
	}
	utils.ConcurrentRetrieveWithCallback(ctx, tasks)
	result := make(map[string][]*schema.Document)
	for i := range tasks {
		if tasks[i].Err != nil {
			return nil, tasks[i].Err
		}
		result[tasks[i].Name] = tasks[i].Result
	}

	// fusion
	fusionCtx := ctxWithFusionRunInfo(ctx)
	fusionCtx = callbacks.OnStart(fusionCtx, result)
	fusionDocs, err := e.fusionFunc(fusionCtx, result)
	if err != nil {
		callbacks.OnError(fusionCtx, err)
		return nil, err
	}
	callbacks.OnEnd(fusionCtx, fusionDocs)
	return fusionDocs, nil
}

// GetType returns the type of the retriever (Router).
func (e *routerRetriever) GetType() string { return "Router" }

func ctxWithRouterRunInfo(ctx context.Context) context.Context {
	runInfo := &callbacks.RunInfo{
		Component: compose.ComponentOfLambda,
		Type:      "Router",
	}

	runInfo.Name = runInfo.Type + string(runInfo.Component)

	return callbacks.ReuseHandlers(ctx, runInfo)
}

func ctxWithFusionRunInfo(ctx context.Context) context.Context {
	runInfo := &callbacks.RunInfo{
		Component: compose.ComponentOfLambda,
		Type:      "FusionFunc",
	}

	runInfo.Name = runInfo.Type + string(runInfo.Component)

	return callbacks.ReuseHandlers(ctx, runInfo)
}
