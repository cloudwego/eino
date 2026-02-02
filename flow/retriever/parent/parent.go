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

package parent

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

// Config configures the parent retriever.
type Config struct {
	// Retriever specifies the original retriever used to retrieve documents.
	// For example: a vector database retriever like Milvus, or a full-text search retriever like Elasticsearch.
	Retriever retriever.Retriever
	// ParentIDKey specifies the key used in the sub-document metadata to store the parent document ID.
	// Documents without this key will be removed from the recall results.
	// For example: if ParentIDKey is "parent_id", it will look for metadata like:
	// {"parent_id": "original_doc_123"}
	ParentIDKey string
	// OrigDocGetter specifies the method for getting original documents by ids from the sub-document metadata.
	// Parameters:
	//   - ctx: context for the operation
	//   - ids: slice of parent document IDs to retrieve
	// Returns:
	//   - []*schema.Document: slice of retrieved parent documents
	//   - error: any error encountered during retrieval
	//
	// For example: if sub-documents with parent IDs ["doc_1", "doc_2"] are retrieved,
	// OrigDocGetter will be called to fetch the original documents with these IDs.
	OrigDocGetter func(ctx context.Context, ids []string) ([]*schema.Document, error)
}

// NewRetriever creates a new parent retriever that handles retrieving original documents
// based on sub-document search results.
//
// Parameters:
//   - ctx: context for the operation
//   - config: configuration for the parent retriever
//
// Example usage:
//
//	retriever, err := NewRetriever(ctx, &Config{
//	    Retriever: milvusRetriever,
//	    ParentIDKey: "source_doc_id",
//	    OrigDocGetter: func(ctx context.Context, ids []string) ([]*schema.Document, error) {
//	        return documentStore.GetByIDs(ctx, ids)
//	    },
//	})
//
// Returns:
//   - retriever.Retriever: the created parent retriever
//   - error: any error encountered during creation
func NewRetriever(ctx context.Context, config *Config) (retriever.Retriever, error) {
	if config.Retriever == nil {
		return nil, fmt.Errorf("retriever is required")
	}
	if config.OrigDocGetter == nil {
		return nil, fmt.Errorf("orig doc getter is required")
	}
	return &parentRetriever{
		retriever:     config.Retriever,
		parentIDKey:   config.ParentIDKey,
		origDocGetter: config.OrigDocGetter,
	}, nil
}

type parentRetriever struct {
	retriever     retriever.Retriever
	parentIDKey   string
	origDocGetter func(ctx context.Context, ids []string) ([]*schema.Document, error)
}

func (p *parentRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	subDocs, err := p.retriever.Retrieve(ctx, query, opts...)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(subDocs))
	for _, subDoc := range subDocs {
		if k, ok := subDoc.MetaData[p.parentIDKey]; ok {
			if s, okk := k.(string); okk && !inList(s, ids) {
				ids = append(ids, s)
			}
		}
	}
	return p.origDocGetter(ctx, ids)
}

func inList(elem string, list []string) bool {
	for _, v := range list {
		if v == elem {
			return true
		}
	}
	return false
}
