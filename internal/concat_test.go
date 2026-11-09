/*
 * Copyright 2025 CloudWeGo Authors
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

package internal

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConcat(t *testing.T) {
	t.Run("concat map chunks with nil value", func(t *testing.T) {
		c1 := map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c1": nil,
				},
			},
		}
		c2 := map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c2": "c2",
				},
			},
		}
		m, err := ConcatItems([]map[string]any{c1, c2})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{
			"a": map[string]any{
				"b": map[string]any{
					"c1": nil,
					"c2": "c2",
				},
			},
		}, m)
	})

	// nil and non-nil for the same key across chunks must concat order-
	// independently and must never panic. See issue #1181.
	t.Run("concat same key: nil then value does not panic", func(t *testing.T) {
		m, err := ConcatItems([]map[string]any{
			{"a": nil},
			{"a": "str"},
		})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{"a": "str"}, m)
	})

	t.Run("concat same key: value then nil is order-independent", func(t *testing.T) {
		m, err := ConcatItems([]map[string]any{
			{"a": "str"},
			{"a": nil},
		})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{"a": "str"}, m)
	})

	t.Run("concat same key: all nil preserves nil", func(t *testing.T) {
		m, err := ConcatItems([]map[string]any{
			{"a": nil},
			{"a": nil},
		})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{"a": nil}, m)
	})

	t.Run("concat same key: nil mixed with multiple non-nil strings", func(t *testing.T) {
		chunks := []map[string]any{
			{"a": nil},
			{"a": "foo"},
			{"a": nil},
			{"a": "bar"},
			{"a": nil},
		}
		m, err := ConcatItems(chunks)
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{"a": "foobar"}, m)
		assert.Equal(t, []map[string]any{
			{"a": nil},
			{"a": "foo"},
			{"a": nil},
			{"a": "bar"},
			{"a": nil},
		}, chunks)
	})

	t.Run("concat same key: nested map with nil then value", func(t *testing.T) {
		m, err := ConcatItems([]map[string]any{
			{"a": map[string]any{"x": nil}},
			{"a": map[string]any{"x": "v"}},
		})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{"a": map[string]any{"x": "v"}}, m)
	})

	t.Run("concat same key: nested map all nil preserves nil", func(t *testing.T) {
		m, err := ConcatItems([]map[string]any{
			{"a": map[string]any{"x": nil}},
			{"a": map[string]any{"x": nil}},
		})
		assert.Nil(t, err)
		assert.Equal(t, map[string]any{"a": map[string]any{"x": nil}}, m)
	})
}

var concatMapsBenchmarkResult map[string]any

func BenchmarkConcatMapsNilValues(b *testing.B) {
	for _, chunkCount := range []int{2, 64, 256} {
		for _, test := range []struct {
			name     string
			nilEvery int
		}{
			{name: "all_non_nil"},
			{name: "mixed_nil", nilEvery: 2},
			{name: "all_nil", nilEvery: 1},
		} {
			b.Run(fmt.Sprintf("chunks=%d/%s", chunkCount, test.name), func(b *testing.B) {
				chunks := make([]map[string]any, chunkCount)
				for i := range chunks {
					var value any = "x"
					if test.nilEvery > 0 && i%test.nilEvery == 0 {
						value = nil
					}
					chunks[i] = map[string]any{"key": value}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					result, err := ConcatItems(chunks)
					if err != nil {
						b.Fatal(err)
					}
					concatMapsBenchmarkResult = result
				}
			})
		}
	}
}
