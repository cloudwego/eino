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

package embedding

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOptions(t *testing.T) {
	defaultModel := "default_model"
	opts := GetCommonOptions(&Options{Model: &defaultModel}, WithModel("test_model"))
	assert.NotNil(t, opts.Model)
	assert.Equal(t, *opts.Model, "test_model")
}

func TestGetImplSpecificOptions(t *testing.T) {
	type MySpecificOptions struct {
		Field1 string
		Field2 int
	}

	withField1 := func(field1 string) func(o *MySpecificOptions) {
		return func(opts *MySpecificOptions) {
			opts.Field1 = field1
		}
	}

	withField2 := func(field2 int) func(o *MySpecificOptions) {
		return func(opts *MySpecificOptions) {
			opts.Field2 = field2
		}
	}

	opt1 := WrapImplSpecificOptFn(withField1("value1"))
	opt2 := WrapImplSpecificOptFn(withField2(100))
	specificOpts := GetImplSpecificOptions[MySpecificOptions](nil, opt1, opt2)
	assert.Equal(t, "value1", specificOpts.Field1)
	assert.Equal(t, 100, specificOpts.Field2)
}
