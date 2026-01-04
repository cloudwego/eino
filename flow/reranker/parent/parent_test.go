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
	"testing"

	"github.com/cloudwego/eino/components/reranker"
	"github.com/cloudwego/eino/schema"
	"github.com/smartystreets/goconvey/convey"
)

type passthroughReranker struct {
	output []*schema.Document
}

func (m *passthroughReranker) Rerank(ctx context.Context, req *reranker.Request, opts ...reranker.Option) ([]*schema.Document, error) {
	if m.output != nil {
		return m.output, nil
	}
	return req.Docs, nil
}

func TestParentReranker_Defaults(t *testing.T) {
	convey.Convey("parent reranker aggregates chunk scores and preserves order", t, func() {
		parentIDKey := "pid"
		makeSubDoc := func(id, parent string, score float64) *schema.Document {
			doc := &schema.Document{ID: id, MetaData: map[string]any{parentIDKey: parent}}
			doc.WithScore(score)
			return doc
		}
		subDocs := []*schema.Document{
			makeSubDoc("c1", "p1", 0.2),
			makeSubDoc("c2", "p2", 0.9),
			makeSubDoc("c3", "p1", 0.8),
			makeSubDoc("c4", "p3", 0.4),
		}
		child := &passthroughReranker{output: subDocs}
		getter := func(ctx context.Context, ids []string) ([]*schema.Document, error) {
			docs := make([]*schema.Document, len(ids))
			for i := range ids {
				// Return in reverse order for test
				docs[i] = &schema.Document{ID: ids[len(ids)-1-i]}
			}
			return docs, nil
		}

		parent, err := NewReranker(&Config{
			Reranker:      child,
			ParentIDKey:   parentIDKey,
			OrigDocGetter: getter,
		})
		convey.So(err, convey.ShouldBeNil)

		res, err := parent.Rerank(context.Background(), &reranker.Request{
			Query: "query",
			Docs:  subDocs,
		})
		convey.So(err, convey.ShouldBeNil)
		convey.So(len(res), convey.ShouldEqual, 3)

		// p2(0.9) > p1(0.8) > p3(0.4)
		convey.So(res[0].ID, convey.ShouldEqual, "p2")
		convey.So(res[0].Score(), convey.ShouldAlmostEqual, 0.9)
		convey.So(res[1].ID, convey.ShouldEqual, "p1")
		convey.So(res[1].Score(), convey.ShouldAlmostEqual, 0.8)
		convey.So(res[2].ID, convey.ShouldEqual, "p3")
		convey.So(res[2].Score(), convey.ShouldAlmostEqual, 0.4)
	})
}

func TestParentReranker_AggregateMeanAndTopK(t *testing.T) {
	convey.Convey("parent reranker supports mean aggregation and topK", t, func() {
		makeSubDoc := func(id, parent string, score float64) *schema.Document {
			doc := &schema.Document{ID: id, MetaData: map[string]any{"parent_id": parent}}
			doc.WithScore(score)
			return doc
		}
		subDocs := []*schema.Document{
			makeSubDoc("c1", "p1", 0.2),
			makeSubDoc("c2", "p1", 0.6),
			makeSubDoc("c3", "p2", 0.9),
			makeSubDoc("c4", "p2", 0.3),
		}
		child := &passthroughReranker{output: subDocs}
		getter := func(ctx context.Context, ids []string) ([]*schema.Document, error) {
			docs := make([]*schema.Document, len(ids))
			for i, id := range ids {
				docs[i] = &schema.Document{ID: id}
			}
			return docs, nil
		}

		parent, err := NewReranker(&Config{
			Reranker:      child,
			OrigDocGetter: getter,
			Aggregate:     AggregateMean,
			TopK:          1,
		})

		convey.So(err, convey.ShouldBeNil)

		res, err := parent.Rerank(context.Background(), &reranker.Request{
			Query: "query",
			Docs:  subDocs,
		})
		convey.So(err, convey.ShouldBeNil)
		convey.So(len(res), convey.ShouldEqual, 1)
		convey.So(res[0].ID, convey.ShouldEqual, "p2")
		convey.So(res[0].Score(), convey.ShouldAlmostEqual, (0.9+0.3)/2)
	})
}
