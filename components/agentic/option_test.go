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

package agentic

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestCommon(t *testing.T) {
	o := GetCommonOptions(nil,
		WithTools([]*schema.ToolInfo{{Name: "test"}}),
		WithModel("test"),
		WithTemperature(0.1),
		WithToolChoice(schema.ToolChoiceAllowed),
		WithTopP(0.1),
	)
	assert.Len(t, o.Tools, 1)
	assert.Equal(t, "test", o.Tools[0].Name)
	assert.Equal(t, "test", *o.Model)
	assert.Equal(t, float64(0.1), *o.Temperature)
	assert.Equal(t, schema.ToolChoiceAllowed, *o.ToolChoice)
	assert.Equal(t, float64(0.1), *o.TopP)
}

func TestImplSpecificOpts(t *testing.T) {
	type implSpecificOptions struct {
		conf  string
		index int
	}

	withConf := func(conf string) func(o *implSpecificOptions) {
		return func(o *implSpecificOptions) {
			o.conf = conf
		}
	}

	withIndex := func(index int) func(o *implSpecificOptions) {
		return func(o *implSpecificOptions) {
			o.index = index
		}
	}

	documentOption1 := WrapImplSpecificOptFn(withConf("test_conf"))
	documentOption2 := WrapImplSpecificOptFn(withIndex(1))

	implSpecificOpts := GetImplSpecificOptions(&implSpecificOptions{}, documentOption1, documentOption2)

	assert.Equal(t, &implSpecificOptions{
		conf:  "test_conf",
		index: 1,
	}, implSpecificOpts)
	documentOption1 = WrapImplSpecificOptFn(withConf("test_conf"))
	documentOption2 = WrapImplSpecificOptFn(withIndex(1))

	implSpecificOpts = GetImplSpecificOptions(&implSpecificOptions{}, documentOption1, documentOption2)

	assert.Equal(t, &implSpecificOptions{
		conf:  "test_conf",
		index: 1,
	}, implSpecificOpts)
}
