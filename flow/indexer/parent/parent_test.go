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
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/document"
	"github.com/cloudwego/eino/components/indexer"
	"github.com/cloudwego/eino/schema"
)

type testIndexer struct{}

func (t *testIndexer) Store(ctx context.Context, docs []*schema.Document, opts ...indexer.Option) (ids []string, err error) {
	ret := make([]string, len(docs))
	for i, d := range docs {
		ret[i] = d.ID
		if !strings.HasPrefix(d.ID, d.MetaData["parent"].(string)) {
			return nil, fmt.Errorf("invalid parent key")
		}
	}
	return ret, nil
}

type testTransformer struct {
}

func (t *testTransformer) Transform(ctx context.Context, src []*schema.Document, opts ...document.TransformerOption) ([]*schema.Document, error) {
	var ret []*schema.Document
	for _, d := range src {
		ret = append(ret, &schema.Document{
			ID:       d.ID,
			Content:  d.Content[:len(d.Content)/2],
			MetaData: deepCopyMap(d.MetaData),
		}, &schema.Document{
			ID:       d.ID,
			Content:  d.Content[len(d.Content)/2:],
			MetaData: deepCopyMap(d.MetaData),
		})
	}
	return ret, nil
}

func TestParentIndexer(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		input  []*schema.Document
		want   []string
	}{
		{
			name: "success",
			config: &Config{
				Indexer:     &testIndexer{},
				Transformer: &testTransformer{},
				ParentIDKey: "parent",
				SubIDGenerator: func(ctx context.Context, parentID string, num int) ([]string, error) {
					ret := make([]string, num)
					for i := range ret {
						ret[i] = parentID + strconv.Itoa(i)
					}
					return ret, nil
				},
			},
			input: []*schema.Document{{
				ID:       "id",
				Content:  "1234567890",
				MetaData: map[string]interface{}{},
			}, {
				ID:       "ID",
				Content:  "0987654321",
				MetaData: map[string]interface{}{},
			}},
			want: []string{"id0", "id1", "ID0", "ID1"},
		},
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			index, err := NewIndexer(ctx, tt.config)
			if err != nil {
				t.Fatal(err)
			}
			ret, err := index.Store(ctx, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ret, tt.want) {
				t.Errorf("NewHeaderSplitter() got = %v, want %v", ret, tt.want)
			}
		})
	}
}

func deepCopyMap(in map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{})
	for k, v := range in {
		out[k] = v
	}
	return out
}
