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

package compose

import (
	"fmt"
	"io"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/schema"
)

func Test_mergeValues(t *testing.T) {
	t.Run("merge maps", func(t *testing.T) {
		m1 := map[int]int{1: 1, 2: 2, 3: 3, 4: 4}
		m2 := map[int]int{5: 5, 6: 6, 7: 7, 8: 8}
		m3 := map[int]int{9: 9, 10: 10, 11: 11}

		t.Run("regular", func(t *testing.T) {
			mergedM, err := mergeValues([]any{m1, m2, m3}, nil)
			assert.NoError(t, err)

			m := mergedM.(map[int]int)

			// len(m) == len(m1) + len(m2) + len(m3)
			assert.Equal(t, len(m), len(m1)+len(m2)+len(m3))
		})

		t.Run("duplicated key", func(t *testing.T) {
			_, err := mergeValues([]any{m1, m2, m3, map[int]int{1: 1}}, nil)
			assert.ErrorContains(t, err, "duplicated key")
		})

		t.Run("type mismatch", func(t *testing.T) {
			_, err := mergeValues([]any{m1, m2, m3, map[int]string{1: "1"}}, nil)
			assert.ErrorContains(t, err, "type mismatch")
		})
	})

	t.Run("merge stream", func(t *testing.T) {
		ass := []any{
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{1: "1"}})),
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{2: "2"}})),
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{3: "3", 4: "4"}})),
		}
		isr, err := mergeValues(ass, nil)
		require.NoError(t, err)
		ret, ok := unpackStreamReader[map[int]string](isr.(streamReader))
		require.True(t, ok)
		defer ret.Close()

		got := make(map[int]string)
		for i := 0; i < 3; i++ {
			m, err := ret.Recv()
			require.NoError(t, err)
			for k, v := range m {
				got[k] = v
			}
		}
		_, err = ret.Recv()
		require.ErrorIs(t, err, io.EOF)

		assert.Equal(t, map[int]string{
			1: "1",
			2: "2",
			3: "3",
			4: "4",
		}, got)
	})

	t.Run("merge stream with source EOF", func(t *testing.T) {
		ass := []any{
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{1: "1"}})),
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{2: "2"}})),
			packStreamReader(schema.StreamReaderFromArray[map[int]string]([]map[int]string{{3: "3", 4: "4"}})),
		}
		opts := &mergeOptions{
			streamMergeWithSourceEOF: true,
			names: []string{
				"source0",
				"source1",
				"source2",
			},
		}
		isr, err := mergeValues(ass, opts)
		require.NoError(t, err)
		ret, ok := unpackStreamReader[map[int]string](isr.(streamReader))
		require.True(t, ok)
		defer ret.Close()

		got := make(map[int]string)
		endedSources := make(map[string]bool)

		for {
			m, e := ret.Recv()
			if e != nil {
				if sourceName, ok_ := schema.GetSourceName(e); ok_ {
					t.Logf("Source '%s' ended", sourceName)
					endedSources[sourceName] = true
					continue
				}
				if e == io.EOF {
					// This EOF means all chunks from all sources that were not SourceEOF have been merged and sent.
					// Or, if all sources send SourceEOF first, this io.EOF means the merged stream itself is now empty.
					break
				}

				require.NoError(t, e) // Fail on any other error
			}
			// If streamMergeWithSourceEOF is true, the final merged result comes as a single map chunk
			// after all SourceEOFs (if any non-empty streams existed) or directly if all streams were empty.
			for k, v := range m {
				got[k] = v
			}
		}

		// Check that all expected sources have ended if they were part of opts.names
		for i := 0; i < len(ass); i++ {
			expectedSourceName := opts.names[i]
			assert.True(t, endedSources[expectedSourceName], "Expected source %s to have sent SourceEOF", expectedSourceName)
		}

		// The final 'got' map should contain all items because streamMergeWithSourceEOF merges them at the end.
		assert.Equal(t, map[int]string{
			1: "1",
			2: "2",
			3: "3",
			4: "4",
		}, got)
	})

	type TestType struct {
		A int
		B []string
	}

	RegisterValuesMergeFunc(func(vs []*TestType) (*TestType, error) {
		ret := &TestType{}
		for _, v := range vs {
			if v == nil {
				continue
			}
			if ret.A < 0 {
				return nil, fmt.Errorf("test error: %v", ret.A)
			}
			ret.A += v.A
			ret.B = append(ret.B, v.B...)
		}
		sort.Strings(ret.B)
		return ret, nil
	})

	t.Run("custom merge", func(t *testing.T) {
		t.Run("regular", func(t *testing.T) {
			vs := []any{
				&TestType{A: 0, B: []string{}},
				&TestType{A: 1, B: []string{"1"}},
				&TestType{A: 2, B: []string{"2", "22"}},
				&TestType{A: 3, B: []string{"3", "33", "333"}},
			}
			ret, err := mergeValues(vs, nil)
			require.NoError(t, err)
			assert.Equal(t, &TestType{
				A: 6,
				B: []string{"1", "2", "22", "3", "33", "333"},
			}, ret)
		})

		t.Run("custom error", func(t *testing.T) {
			vs := []any{
				&TestType{A: 0, B: []string{}},
				&TestType{A: 1, B: []string{"1"}},
				&TestType{A: -2, B: []string{"2", "22"}},
				&TestType{A: 3, B: []string{"3", "33", "333"}},
			}
			_, err := mergeValues(vs, nil)
			require.ErrorContains(t, err, "test error")
		})

		t.Run("type mismatch", func(t *testing.T) {
			vs := []any{
				&TestType{A: 0, B: []string{}},
				&TestType{A: 1, B: []string{"1"}},
				&TestType{A: 2, B: []string{"2", "22"}},
				"test3",
			}
			_, err := mergeValues(vs, nil)
			require.ErrorContains(t, err, "type mismatch")
		})

		t.Run("stream", func(t *testing.T) {
			ass := []any{
				packStreamReader(schema.StreamReaderFromArray([]*TestType{
					{A: 0, B: []string{}},
				})),
				packStreamReader(schema.StreamReaderFromArray([]*TestType{
					{A: 1, B: []string{"1"}},
				})),
				packStreamReader(schema.StreamReaderFromArray([]*TestType{
					{A: 2, B: []string{"2", "22"}},
				})),
				packStreamReader(schema.StreamReaderFromArray([]*TestType{
					{A: 3, B: []string{"3", "33", "333"}},
				})),
			}
			isr, err := mergeValues(ass, nil)
			require.NoError(t, err)
			ret, ok := unpackStreamReader[*TestType](isr.(streamReader))
			require.True(t, ok)
			defer ret.Close()

			var vs []any
			for i := 0; i < 4; i++ {
				v, err := ret.Recv()
				require.NoError(t, err)
				vs = append(vs, v)
			}

			_, err = ret.Recv()
			require.ErrorIs(t, err, io.EOF)

			merged, err := mergeValues(vs, nil)
			require.NoError(t, err)

			assert.Equal(t, &TestType{
				A: 6,
				B: []string{"1", "2", "22", "3", "33", "333"},
			}, merged)
		})
	})

	t.Run("unregistered type", func(t *testing.T) {
		type Unregistered TestType
		_, err := mergeValues([]any{&Unregistered{}}, nil)
		assert.ErrorContains(t, err, "unsupported type")
	})

}
