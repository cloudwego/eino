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

package schema

import (
	"testing"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/smartystreets/goconvey/convey"
)

func TestParamsOneOfToJSONSchema(t *testing.T) {
	convey.Convey("TestParamsOneOfToJSONSchema", t, func() {
		var (
			oneOf     ParamsOneOf
			converted any
			err       error
		)

		convey.Convey("user provides openAPIV3.0 json schema directly, use what the user provides", func() {
			oneOf.openAPIV3 = &openapi3.Schema{
				Type:        &openapi3.Types{openapi3.TypeString},
				Description: "this is the only argument",
			}
			converted, err = oneOf.ToOpenAPIV3()
			convey.So(err, convey.ShouldBeNil)
			convey.So(converted, convey.ShouldResemble, oneOf.openAPIV3)
		})

		convey.Convey("user provides map[string]ParameterInfo, converts to json schema", func() {
			oneOf.params = map[string]*ParameterInfo{
				"arg1": {
					Type:     String,
					Desc:     "this is the first argument",
					Required: true,
					Enum:     []string{"1", "2"},
				},
				"arg2": {
					Type: Object,
					Desc: "this is the second argument",
					SubParams: map[string]*ParameterInfo{
						"sub_arg1": {
							Type:     String,
							Desc:     "this is the sub argument",
							Required: true,
							Enum:     []string{"1", "2"},
						},
						"sub_arg2": {
							Type: String,
							Desc: "this is the sub argument 2",
						},
					},
					Required: true,
				},
				"arg3": {
					Type: Array,
					Desc: "this is the third argument",
					ElemInfo: &ParameterInfo{
						Type:     String,
						Desc:     "this is the element of the third argument",
						Required: true,
						Enum:     []string{"1", "2"},
					},
					Required: true,
				},
			}
			converted, err = oneOf.ToOpenAPIV3()
			convey.So(err, convey.ShouldBeNil)
		})
	})
}
