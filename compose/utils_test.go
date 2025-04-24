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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/generic"
)

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
