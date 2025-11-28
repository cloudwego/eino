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

	"github.com/smartystreets/goconvey/convey"
)

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

	convey.Convey("TestLoaderImplSpecificOpts", t, func() {
		documentOption1 := WrapImplSpecificOptFn(withConf("test_conf"))
		documentOption2 := WrapImplSpecificOptFn(withIndex(1))

		implSpecificOpts := GetImplSpecificOptions(&implSpecificOptions{}, documentOption1, documentOption2)

		convey.So(implSpecificOpts, convey.ShouldResemble, &implSpecificOptions{
			conf:  "test_conf",
			index: 1,
		})
	})
	convey.Convey("TestTransformerImplSpecificOpts", t, func() {
		documentOption1 := WrapImplSpecificOptFn(withConf("test_conf"))
		documentOption2 := WrapImplSpecificOptFn(withIndex(1))

		implSpecificOpts := GetImplSpecificOptions(&implSpecificOptions{}, documentOption1, documentOption2)

		convey.So(implSpecificOpts, convey.ShouldResemble, &implSpecificOptions{
			conf:  "test_conf",
			index: 1,
		})
	})
}
