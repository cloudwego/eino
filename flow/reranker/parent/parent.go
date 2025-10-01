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
	"sort"

	"github.com/cloudwego/eino/components/reranker"
	"github.com/cloudwego/eino/schema"
)

// AggregatePolicy describes how to aggregate chunk scores for a parent document.
type AggregatePolicy int

const (
	// AggregateMax takes the maximum score among children.
	AggregateMax AggregatePolicy = iota
	// AggregateMean takes the average score among children.
	AggregateMean
)

const defaultParentIDKey = "parent_id"

// Config contains the configuration for the parent reranker wrapper.
type Config struct {
	// Reranker specifies the original reranker used to rerank documents.
	Reranker reranker.Reranker
	// ParentIDKey specifies the key used in the sub-document metadata to store the parent document ID.
	ParentIDKey string
	// OrigDocGetter specifies the method for getting original documents by ids from the sub-document metadata.
	OrigDocGetter func(ctx context.Context, ids []string) ([]*schema.Document, error)
	// Aggregate controls how child scores are aggregated to parent scores. Defaults to AggregateMax.
	Aggregate AggregatePolicy
	// TopK limits the number of returned parent documents. Non-positive means no limit.
	TopK int
}

// NewReranker creates a new parent reranker that handles reranking original documents
// based on sub-document rerank results.
func NewReranker(config *Config) (reranker.Reranker, error) {
	if config == nil || config.Reranker == nil {
		return nil, fmt.Errorf("reranker is required")
	}
	parentIDKey := config.ParentIDKey
	if parentIDKey == "" {
		parentIDKey = defaultParentIDKey
	}
	aggregate := config.Aggregate
	if aggregate != AggregateMean {
		aggregate = AggregateMax
	}
	if config.OrigDocGetter == nil {
		return nil, fmt.Errorf("orig doc getter is required")
	}

	return &parentReranker{
		reranker:      config.Reranker,
		parentIDKey:   parentIDKey,
		origDocGetter: config.OrigDocGetter,
		aggregate:     aggregate,
		topK:          config.TopK,
	}, nil
}

type parentReranker struct {
	reranker      reranker.Reranker
	parentIDKey   string
	origDocGetter func(ctx context.Context, ids []string) ([]*schema.Document, error)
	aggregate     AggregatePolicy
	topK          int
}

type parentAgg struct {
	max   float64
	sum   float64
	count int
}

func (p *parentAgg) add(score float64) {
	if p.count == 0 || score > p.max {
		p.max = score
	}
	p.sum += score
	p.count++
}

func (p *parentAgg) score(policy AggregatePolicy) float64 {
	if p == nil || p.count == 0 {
		return 0
	}
	switch policy {
	case AggregateMean:
		return p.sum / float64(p.count)
	default:
		return p.max
	}
}

// Rerank delegates to the child reranker to score chunk-level documents and then
// aggregates them back to parent documents according to the configured policy.
func (p *parentReranker) Rerank(ctx context.Context, query string, docs []*schema.Document, opts ...reranker.Option) ([]*schema.Document, error) {
	subDocs, err := p.reranker.Rerank(ctx, query, docs, opts...)
	if err != nil {
		return nil, err
	}

	parentScore := make(map[string]*parentAgg, len(subDocs))
	order := make([]string, 0, len(subDocs))
	seen := make(map[string]struct{}, len(subDocs))

	// Traverse the reranked documents and aggregate them to the total or get the maximum value
	for _, subDoc := range subDocs {
		if subDoc == nil || subDoc.MetaData == nil {
			continue
		}
		rawID, ok := subDoc.MetaData[p.parentIDKey]
		if !ok {
			continue
		}
		id, ok := rawID.(string)
		if !ok || id == "" {
			continue
		}

		score := subDoc.Score()

		agg, ok := parentScore[id]
		if !ok {
			agg = &parentAgg{}
			parentScore[id] = agg
			if _, exists := seen[id]; !exists {
				order = append(order, id)
				seen[id] = struct{}{}
			}
		}
		agg.add(score)
	}

	if len(parentScore) == 0 {
		return []*schema.Document{}, nil
	}

	parentIDs := make([]string, 0, len(order))
	for _, id := range order {
		if _, ok := parentScore[id]; ok {
			parentIDs = append(parentIDs, id)
		}
	}

	// Fetch the original parent documents via the provided OrigDocGetter.
	parents, err := p.origDocGetter(ctx, parentIDs)
	if err != nil {
		return nil, err
	}

	idToDoc := make(map[string]*schema.Document, len(parents))
	for _, doc := range parents {
		if doc == nil {
			continue
		}
		id := getDocumentID(doc, p.parentIDKey)
		if id == "" {
			continue
		}
		idToDoc[id] = doc
	}

	result := make([]*schema.Document, 0, len(parentScore))
	for _, id := range parentIDs {
		doc, ok := idToDoc[id]
		if !ok {
			continue
		}
		score := parentScore[id].score(p.aggregate)
		doc.WithScore(score)
		result = append(result, doc)
	}

	if len(result) == 0 {
		return nil, nil
	}

	index := make(map[string]int, len(parentIDs))
	for idx, id := range parentIDs {
		index[id] = idx
	}

	sort.SliceStable(result, func(i, j int) bool {
		si := result[i].Score()
		sj := result[j].Score()
		if si == sj {
			iID := getDocumentID(result[i], p.parentIDKey)
			jID := getDocumentID(result[j], p.parentIDKey)
			return index[iID] < index[jID]
		}
		return si > sj
	})

	if p.topK > 0 && p.topK < len(result) {
		result = result[:p.topK]
	}

	return result, nil
}

func getDocumentID(doc *schema.Document, key string) string {
	if doc == nil {
		return ""
	}
	if doc.ID != "" {
		return doc.ID
	}
	if doc.MetaData != nil {
		if v, ok := doc.MetaData[key].(string); ok {
			return v
		}
	}
	return ""
}
