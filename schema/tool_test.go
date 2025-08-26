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

	"github.com/eino-contrib/jsonschema"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/smartystreets/goconvey/convey"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

func TestParamsOneOfToOpenAPIV3(t *testing.T) {
	convey.Convey("ParamsOneOfToOpenAPIV3", t, func() {
		var (
			oneOf     ParamsOneOf
			converted any
			err       error
		)

		convey.Convey("user provides openAPIV3.0 json schema directly, use what the user provides", func() {
			oneOf.openAPIV3 = &openapi3.Schema{
				Type:        openapi3.TypeString,
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

func TestParamsOneOfToJSONSchema(t *testing.T) {
	convey.Convey("ParamsOneOfToJSONSchema", t, func() {
		var (
			oneOf     ParamsOneOf
			converted any
			err       error
		)

		convey.Convey("user provides JSON schema directly, use what the user provides", func() {
			oneOf.jsonschema = &jsonschema.Schema{
				Type:        "string",
				Description: "this is the only argument",
			}
			converted, err = oneOf.ToJSONSchema()
			convey.So(err, convey.ShouldBeNil)
			convey.So(converted, convey.ShouldResemble, oneOf.jsonschema)
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
			converted, err = oneOf.ToJSONSchema()
			convey.So(err, convey.ShouldBeNil)
		})
	})
}

func TestJsonSchemaToOpenAPIV3(t *testing.T) {
	convey.Convey("", t, func() {
		js := &jsonschema.Schema{
			Type:        "object",
			Description: "this is the only argument",
			Properties: orderedmap.New[string, *jsonschema.Schema](
				orderedmap.WithInitialData(
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "arg1",
						Value: &jsonschema.Schema{
							Type:        "string",
							Description: "this is the first argument",
						},
					},
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "arg2",
						Value: &jsonschema.Schema{
							Type:        "array",
							Description: "this is the second argument",
							Items: &jsonschema.Schema{
								Type:        "string",
								Description: "this is the element of the second argument",
							},
						},
					},
				),
			),
			Required: []string{"arg1"},
		}

		expect := &openapi3.SchemaRef{
			Value: &openapi3.Schema{
				Type:        openapi3.TypeObject,
				Description: "this is the only argument",
				Properties: map[string]*openapi3.SchemaRef{
					"arg1": {
						Value: &openapi3.Schema{
							Type:        openapi3.TypeString,
							Description: "this is the first argument",
						},
					},
					"arg2": {
						Value: &openapi3.Schema{
							Type:        openapi3.TypeArray,
							Description: "this is the second argument",
							Items: &openapi3.SchemaRef{
								Value: &openapi3.Schema{
									Type:        openapi3.TypeString,
									Description: "this is the element of the second argument",
								},
							},
						},
					},
				},
				Required: []string{"arg1"},
			},
		}

		js_, err := jsonSchemaToOpenAPIV3(js)
		convey.So(err, convey.ShouldBeNil)
		convey.So(js_, convey.ShouldEqual, expect)
	})
}

func TestOpenAPIV3ToJSONSchema(t *testing.T) {
	convey.Convey("", t, func() {
		openAPIV3 := &openapi3.SchemaRef{
			Value: &openapi3.Schema{
				Type:        openapi3.TypeObject,
				Description: "this is the only argument",
				Properties: map[string]*openapi3.SchemaRef{
					"arg1": {
						Value: &openapi3.Schema{
							Type:        openapi3.TypeString,
							Description: "this is the first argument",
						},
					},
					"arg2": {
						Value: &openapi3.Schema{
							Type:        openapi3.TypeArray,
							Description: "this is the second argument",
							Items: &openapi3.SchemaRef{
								Value: &openapi3.Schema{
									Type:        openapi3.TypeString,
									Description: "this is the element of the second argument",
								},
							},
						},
					},
				},
				Required: []string{"arg1"},
			},
		}

		expected := &jsonschema.Schema{
			Type:        "object",
			Description: "this is the only argument",
			Properties: orderedmap.New[string, *jsonschema.Schema](
				orderedmap.WithInitialData(
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "arg1",
						Value: &jsonschema.Schema{
							Type:        "string",
							Description: "this is the first argument",
						},
					},
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "arg2",
						Value: &jsonschema.Schema{
							Type:        "array",
							Description: "this is the second argument",
							Items: &jsonschema.Schema{
								Type:        "string",
								Description: "this is the element of the second argument",
							},
						},
					},
				),
			),
			Required: []string{"arg1"},
		}

		js, err := openapiV3ToJSONSchema(openAPIV3.Value)
		convey.So(err, convey.ShouldBeNil)
		convey.So(js, convey.ShouldResemble, expected)
	})
}
