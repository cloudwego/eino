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

package compose

import (
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal"
	"github.com/cloudwego/eino/schema"
)

type tStreamConcatItemForTest struct {
	s string
}

func concatTStreamForTest(items []tStreamConcatItemForTest) (tStreamConcatItemForTest, error) {
	var s string
	for _, item := range items {
		s += item.s
	}

	return tStreamConcatItemForTest{s: s}, nil
}

func concatIntForTest(items []int) (int, error) {
	var i int
	for _, item := range items {
		i += item
	}

	return i, nil
}

type tConcatErrForTest struct{}

func concatTStreamError(_ []tConcatErrForTest) (tConcatErrForTest, error) {
	return tConcatErrForTest{}, errors.New("test error")
}

func TestConcatRegistry(t *testing.T) {

	RegisterStreamChunkConcatFunc(concatTStreamForTest)

	sr, sw := schema.Pipe[tStreamConcatItemForTest](10)
	go func() {
		for i := 0; i < 10; i++ {
			sw.Send(tStreamConcatItemForTest{s: strconv.Itoa(i)}, nil)
		}
		sw.Close()
	}()

	lastVal, err := concatStreamReader(sr)
	assert.Nil(t, err)

	assert.Equal(t, "0123456789", lastVal.s)
}

func TestStringConcat(t *testing.T) {
	sr, sw := schema.Pipe[string](10)
	go func() {
		for i := 0; i < 10; i++ {
			sw.Send(strconv.Itoa(i), nil)
		}

		sw.Close()
	}()

	lastVal, err := concatStreamReader(sr)
	assert.Nil(t, err)

	assert.Equal(t, "0123456789", lastVal)
}

func TestMessageConcat(t *testing.T) {
	sr, sw := schema.Pipe[*schema.Message](10)
	go func() {
		for i := 0; i < 10; i++ {
			content := schema.UserMessage(strconv.Itoa(i))
			if i%4 == 0 {
				content.Extra = map[string]any{
					"key_1":         strconv.Itoa(i),
					strconv.Itoa(i): strconv.Itoa(i),
				}
			}
			sw.Send(content, nil)
		}
		sw.Close()
	}()

	lastVal, err := concatStreamReader(sr)
	assert.Nil(t, err)
	assert.Equal(t, "0123456789", lastVal.Content)
	assert.Len(t, lastVal.Extra, 4)
	assert.Equal(t, map[string]any{
		"key_1": "048",
		"0":     "0",
		"4":     "4",
		"8":     "8",
	}, lastVal.Extra)

}

func TestMapConcat(t *testing.T) {
	RegisterStreamChunkConcatFunc(concatTStreamForTest)
	RegisterStreamChunkConcatFunc(concatIntForTest)

	t.Run("simple map", func(t *testing.T) {
		sr, sw := schema.Pipe[map[string]any](10)

		go func() {
			for i := 0; i < 10; i++ {
				sw.Send(map[string]any{
					"string":        strconv.Itoa(i),
					"custom_concat": tStreamConcatItemForTest{s: strconv.Itoa(9 - i)},
					"count":         i,
				}, nil)
			}
			sw.Close()
		}()

		lastVal, err := concatStreamReader(sr)
		assert.Nil(t, err)

		assert.Equal(t, "0123456789", lastVal["string"])
		assert.Equal(t, "9876543210", lastVal["custom_concat"].(tStreamConcatItemForTest).s)
		assert.Equal(t, 45, lastVal["count"])

	})

	t.Run("complex map", func(t *testing.T) {
		sr, sw := schema.Pipe[map[string]any](10)

		go func() {
			for i := 0; i < 10; i++ {
				// 嵌套 map, 仅允许第一层做类型合并，第二层直接覆盖
				sw.Send(map[string]any{ // 嵌套 map
					"string": strconv.Itoa(i),
					"deep_map": map[string]any{
						"message": &schema.Message{
							Content: strconv.Itoa(i),
						},
						"custom_concat_deep": tStreamConcatItemForTest{s: strconv.Itoa(9 - i)},
						"count":              i,
					},
					"custom_concat": tStreamConcatItemForTest{s: strconv.Itoa(9 - i)},
					"count":         i,
				}, nil)
			}
			sw.Close()
		}()

		lastVal, err := concatStreamReader(sr)
		assert.Nil(t, err)

		assert.Equal(t, "0123456789", lastVal["string"])
		assert.Equal(t, 45, lastVal["count"])
		assert.Equal(t, "0123456789", lastVal["deep_map"].(map[string]any)["message"].(*schema.Message).Content)
		assert.Equal(t, "9876543210", lastVal["deep_map"].(map[string]any)["custom_concat_deep"].(tStreamConcatItemForTest).s)
		assert.Equal(t, 45, lastVal["deep_map"].(map[string]any)["count"])
	})
}

func TestConcatError(t *testing.T) {
	t.Run("map type not equal", func(t *testing.T) {
		a := map[string]any{
			"str": "string_01",
			"x":   "string_in_a",
		}

		b := map[string]any{
			"str": "string_02",
			"x":   123,
		}
		_, err := internal.ConcatItems([]map[string]any{a, b})
		assert.NotNil(t, err)
	})

	t.Run("merge error", func(t *testing.T) {
		RegisterStreamChunkConcatFunc(concatTStreamError)

		_, err := internal.ConcatItems([]tConcatErrForTest{{}, {}})
		assert.NotNil(t, err)
	})
}

func TestConcatSliceValue(t *testing.T) {
	type testStruct struct {
		A string
	}

	s := []testStruct{{}, {A: "123"}, {}}
	result, err := internal.ConcatItems(s)
	assert.Nil(t, err)
	assert.Equal(t, testStruct{A: "123"}, result)

	s = []testStruct{{}, {}, {}}
	result, err = internal.ConcatItems(s)
	assert.Nil(t, err)
	assert.Equal(t, testStruct{}, result)
}
