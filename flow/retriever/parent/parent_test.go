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
	"reflect"
	"testing"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
)

type testRetriever struct{}

func (t *testRetriever) Retrieve(ctx context.Context, query string, opts ...retriever.Option) ([]*schema.Document, error) {
	ret := make([]*schema.Document, 0)
	for i := range query {
		ret = append(ret, &schema.Document{
			ID:      "",
			Content: "",
			MetaData: map[string]interface{}{
				"parent": query[i : i+1],
			},
		})
	}
	return ret, nil
}

func TestParentRetriever(t *testing.T) {
	tests := []struct {
		name   string
		config *Config
		input  string
		want   []*schema.Document
	}{
		{
			name: "success",
			config: &Config{
				Retriever:   &testRetriever{},
				ParentIDKey: "parent",
				OrigDocGetter: func(ctx context.Context, ids []string) ([]*schema.Document, error) {
					var ret []*schema.Document
					for i := range ids {
						ret = append(ret, &schema.Document{ID: ids[i]})
					}
					return ret, nil
				},
			},
			input: "123233",
			want: []*schema.Document{
				{ID: "1"},
				{ID: "2"},
				{ID: "3"},
			},
		},
	}
	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := NewRetriever(ctx, tt.config)
			if err != nil {
				t.Fatal(err)
			}
			ret, err := r.Retrieve(ctx, tt.input)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ret, tt.want) {
				t.Errorf("got %v, want %v", ret, tt.want)
			}
		})
	}
}
