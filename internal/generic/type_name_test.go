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
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseTypeName(t *testing.T) {
	t.Run("named_struct", func(t *testing.T) {
		type OpenAI struct{}
		model := &OpenAI{}
		name := ParseTypeName(reflect.Indirect(reflect.ValueOf(model)))
		assert.Equal(t, "OpenAI", name)
	})

	t.Run("anonymous_struct", func(t *testing.T) {
		model := &struct{}{}
		name := ParseTypeName(reflect.ValueOf(model))
		assert.Equal(t, "", name)
	})

	t.Run("anonymous_struct_from_func", func(t *testing.T) {
		model := genStruct()
		name := ParseTypeName(reflect.ValueOf(model))
		assert.Equal(t, "", name)
	})

	t.Run("named_interface", func(t *testing.T) {
		type OpenAI interface{}
		model := OpenAI(&struct{}{})
		name := ParseTypeName(reflect.ValueOf(model))
		assert.Equal(t, "", name)

		name = ParseTypeName(reflect.ValueOf((*OpenAI)(nil)))
		assert.Equal(t, "OpenAI", name)
	})

	t.Run("named_function", func(t *testing.T) {
		f := genOpenAI
		name := ParseTypeName(reflect.ValueOf(f))
		assert.Equal(t, "genOpenAI", name)
	})

	t.Run("anonymous_function", func(t *testing.T) {
		f := genAnonymousFunc()
		name := ParseTypeName(reflect.ValueOf(f))
		assert.Equal(t, "", name)

		ff := func(n string) {
			_ = n
		}

		name = ParseTypeName(reflect.ValueOf(ff))
		assert.Equal(t, "", name)
	})
}

func genStruct() *struct{} {
	return &struct{}{}
}

func genOpenAI() {}

func genAnonymousFunc() func(n string) {
	return func(n string) {
		_ = n
	}
}
