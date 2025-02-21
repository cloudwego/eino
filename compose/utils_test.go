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
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

func TestMergeValues(t *testing.T) {
	// merge maps
	m1 := map[int]int{1: 1, 2: 2, 3: 3, 4: 4}
	m2 := map[int]int{5: 5, 6: 6, 7: 7, 8: 8}
	m3 := map[int]int{9: 9, 10: 10, 11: 11}
	mergedM, err := mergeValues([]any{m1, m2, m3})
	assert.Nil(t, err)

	m := mergedM.(map[int]int)

	// len(m) == len(m1) + len(m2) + len(m3)
	assert.Equal(t, len(m), len(m1)+len(m2)+len(m3))

	_, err = mergeValues([]any{m1, m2, m3, map[int]int{1: 1}})
	assert.NotNil(t, err)

	_, err = mergeValues([]any{m1, m2, m3, map[int]string{1: "1"}})
	assert.NotNil(t, err)

	// merge stream
	ass := []any{
		packStreamReader(schema.StreamReaderFromArray[map[int]bool]([]map[int]bool{{1: true}})),
		packStreamReader(schema.StreamReaderFromArray[map[int]bool]([]map[int]bool{{2: true}})),
		packStreamReader(schema.StreamReaderFromArray[map[int]bool]([]map[int]bool{{3: true}})),
	}
	isr, err := mergeValues(ass)
	assert.Nil(t, err)
	ret, ok := unpackStreamReader[map[int]bool](isr.(streamReader))
	defer ret.Close()

	// check if merge ret is StreamReader
	assert.True(t, ok)

	for i := 1; i <= 3; i++ {
		num, err := ret.Recv()
		assert.Nil(t, err)

		if num[i] != true {
			t.Fatalf("stream read num:%d is out of expect", i)
		}
	}
	_, err = ret.Recv()
	if err != io.EOF {
		t.Fatalf("stream reader isn't return EOF as expect: %v", err)
	}
}

type good interface {
	ThisIsGood() bool
}

type good2 interface {
	ThisIsGood2() bool
}

type good3 interface {
	ThisIsGood() bool
}

type goodImpl struct{}

func (g *goodImpl) ThisIsGood() bool {
	return true
}

type goodNotImpl struct{}

func TestValidateType(t *testing.T) {

	t.Run("equal_type", func(t *testing.T) {
		arg := generic.TypeOf[int]()
		input := generic.TypeOf[int]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMust, result)
	})

	t.Run("unequal_type", func(t *testing.T) {
		arg := generic.TypeOf[int]()
		input := generic.TypeOf[string]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMustNot, result)
	})

	t.Run("implement_interface", func(t *testing.T) {
		arg := generic.TypeOf[good]()
		input := generic.TypeOf[*goodImpl]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMust, result)
	})

	t.Run("may_implement_interface", func(t *testing.T) {
		arg := generic.TypeOf[*goodImpl]()
		input := generic.TypeOf[good]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMay, result)
	})

	t.Run("not_implement_interface", func(t *testing.T) {
		arg := generic.TypeOf[good]()
		input := generic.TypeOf[*goodNotImpl]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMustNot, result)
	})

	t.Run("interface_unequal_interface", func(t *testing.T) {
		arg := generic.TypeOf[good]()
		input := generic.TypeOf[good2]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMustNot, result)
	})

	t.Run("interface_equal_interface", func(t *testing.T) {
		arg := generic.TypeOf[good]()
		input := generic.TypeOf[good3]()

		result := checkAssignable(input, arg)
		assert.Equal(t, assignableTypeMust, result)
	})
}

func TestStreamChunkConvert(t *testing.T) {
	o, err := streamChunkConvertForCBOutput(1)
	assert.Nil(t, err)
	assert.Equal(t, o, 1)

	i, err := streamChunkConvertForCBInput(1)
	assert.Nil(t, err)
	assert.Equal(t, i, 1)
}
