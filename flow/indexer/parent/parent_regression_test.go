/*
 * Copyright 2026 CloudWeGo Authors
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
	"testing"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

type interleavingTransformer struct{}

func (interleavingTransformer) Transform(_ context.Context, docs []*schema.Document, _ ...document.TransformerOption) ([]*schema.Document, error) {
	return []*schema.Document{
		{ID: docs[0].ID, Content: "a-first"},
		{ID: docs[1].ID, Content: "b-first"},
		{ID: docs[0].ID, Content: "a-second"},
		{ID: docs[1].ID, Content: "b-second"},
	}, nil
}

func TestParentIndexerInterleavedSubDocuments(t *testing.T) {
	ctx := context.Background()
	var generatedFor []string
	idx, err := NewIndexer(ctx, &Config{
		Indexer:     &testIndexer{},
		Transformer: interleavingTransformer{},
		ParentIDKey: "parent",
		SubIDGenerator: func(_ context.Context, parentID string, num int) ([]string, error) {
			generatedFor = append(generatedFor, parentID)
			ids := make([]string, num)
			for i := range ids {
				ids[i] = fmt.Sprintf("%s-%d", parentID, i)
			}
			return ids, nil
		},
	})
	require.NoError(t, err)
	ids, err := idx.Store(ctx, []*schema.Document{{ID: "a"}, {ID: "b"}})
	require.NoError(t, err)
	// Each parent needs one batch of unique IDs, without reordering the chunks.
	require.Equal(t, []string{"a-0", "b-0", "a-1", "b-1"}, ids)
	require.Equal(t, []string{"a", "b"}, generatedFor)
}
