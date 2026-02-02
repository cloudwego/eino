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

// Package utils provides helper utilities for retriever flows, including
// concurrent retrieval with callback instrumentation.
package utils

import (
	"context"
	"fmt"
	"sync"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// RetrieveTask is a task for retrieving documents.
// RetrieveTask represents a single retrieval job with its result or error.
type RetrieveTask struct {
	Name            string
	Retriever       retriever.Retriever
	Query           string
	RetrieveOptions []retriever.Option
	Result          []*schema.Document
	Err             error
}

// ConcurrentRetrieveWithCallback concurrently retrieves documents with callback.
func ConcurrentRetrieveWithCallback(ctx context.Context, tasks []*RetrieveTask) {
	wg := sync.WaitGroup{}
	for i := range tasks {
		wg.Add(1)
		go func(ctx context.Context, t *RetrieveTask) {
			ctx = ctxWithRetrieverRunInfo(ctx, t.Retriever)

			defer func() {
				if e := recover(); e != nil {
					t.Err = fmt.Errorf("retrieve panic, query: %s, error: %v", t.Query, e)
					ctx = callbacks.OnError(ctx, t.Err)
				}
				wg.Done()
			}()

			ctx = callbacks.OnStart(ctx, t.Query)
			docs, err := t.Retriever.Retrieve(ctx, t.Query, t.RetrieveOptions...)
			if err != nil {
				callbacks.OnError(ctx, err)
				t.Err = err
				return
			}

			callbacks.OnEnd(ctx, docs)
			t.Result = docs
		}(ctx, tasks[i])
	}
	wg.Wait()
}

func ctxWithRetrieverRunInfo(ctx context.Context, r retriever.Retriever) context.Context {
	runInfo := &callbacks.RunInfo{
		Component: components.ComponentOfRetriever,
	}

	if typ, okk := components.GetType(r); okk {
		runInfo.Type = typ
	}

	runInfo.Name = runInfo.Type + string(runInfo.Component)

	return callbacks.ReuseHandlers(ctx, runInfo)
}
