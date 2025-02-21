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

package generic

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestNewInstance(t *testing.T) {
	t.Run("struct", func(t *testing.T) {
		type Test struct{}

		inst := NewInstance[Test]()

		assert.IsType(t, Test{}, inst)
	})

	t.Run("pointer", func(t *testing.T) {
		type Test struct{}

		inst := NewInstance[*Test]()

		assert.IsType(t, &Test{}, inst)
	})

	t.Run("interface", func(t *testing.T) {
		type Test interface{}

		inst := NewInstance[Test]()
		assert.IsType(t, Test(nil), inst)
	})

	t.Run("pointer of pointer of pointer", func(t *testing.T) {
		type Test struct {
			Value int
		}

		inst := NewInstance[***Test]()

		ptr := &Test{}
		ptrOfPtr := &ptr
		assert.NotNil(t, inst)
		assert.NotNil(t, *inst)
		assert.IsType(t, ptrOfPtr, *inst)
		assert.NotNil(t, **inst)
		assert.Equal(t, Test{Value: 0}, ***inst)
	})

	t.Run("primitive_map", func(t *testing.T) {
		inst := NewInstance[map[string]any]()
		assert.NotNil(t, inst)
		inst["a"] = 1
		assert.Equal(t, map[string]any{"a": 1}, inst)
	})

	t.Run("primitive_slice", func(t *testing.T) {
		inst := NewInstance[[]int]()
		assert.NotNil(t, inst)
		inst = append(inst, 1)
		assert.Equal(t, []int{1}, inst)
	})

	t.Run("primitive_string", func(t *testing.T) {
		inst := NewInstance[string]()
		assert.Equal(t, "", inst)
	})

	t.Run("primitive_int64", func(t *testing.T) {
		inst := NewInstance[int64]()
		assert.Equal(t, int64(0), inst)
	})
}

func TestReverse(t *testing.T) {
	t.Run("reverse int slice", func(t *testing.T) {
		input := []int{1, 2, 3, 4, 5}
		expected := []int{5, 4, 3, 2, 1}
		result := Reverse(input)
		assert.Equal(t, expected, result)
	})

	t.Run("reverse string slice", func(t *testing.T) {
		input := []string{"a", "b", "c"}
		expected := []string{"c", "b", "a"}
		result := Reverse(input)
		assert.Equal(t, expected, result)
	})
}
