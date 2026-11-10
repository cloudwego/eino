/*
 * Copyright 2026 CloudWeGo Authors
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

package decision

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

type testOptions struct {
	Concurrency int
}

func TestGetCommonOptions(t *testing.T) {
	defaultModel := "default_model"
	base := &Options{Model: &defaultModel}

	opts := GetCommonOptions(base, WithModel("jev-latest"))
	assert.NotNil(t, opts.Model)
	assert.Equal(t, "jev-latest", *opts.Model)

	opts = GetCommonOptions(nil)
	assert.Nil(t, opts.Model)
}

func TestGetImplSpecificOptions(t *testing.T) {
	opt := WrapImplSpecificOptFn(func(o *testOptions) {
		o.Concurrency = 4
	})

	opts := GetImplSpecificOptions(&testOptions{}, opt, WithModel("jev-latest"))
	assert.Equal(t, 4, opts.Concurrency)

	opts = GetImplSpecificOptions[testOptions](nil)
	assert.NotNil(t, opts)
	assert.Equal(t, 0, opts.Concurrency)
}
