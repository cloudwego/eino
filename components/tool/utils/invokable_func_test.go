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

package utils

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/eino-contrib/jsonschema"
	"github.com/getkin/kin-openapi/openapi3"
	"github.com/stretchr/testify/assert"
	orderedmap "github.com/wk8/go-ordered-map/v2"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type Job struct {
	Company       string  `json:"company" jsonschema:"description=the company where the user works"`
	Position      string  `json:"position,omitempty" jsonschema:"description=the position of the user's job"`
	ServiceLength float32 `json:"service_length,omitempty" jsonschema:"description=the year of user's service"` // 司龄，年
}

type Income struct {
	Source    string `json:"source" jsonschema:"description=the source of income"`
	Amount    int    `json:"amount" jsonschema:"description=the amount of income"`
	HasPayTax bool   `json:"has_pay_tax" jsonschema:"description=whether the user has paid tax"`
	Job       *Job   `json:"job,omitempty" jsonschema:"description=the job of the user when earning this income"`
}

type User struct {
	Name string `json:"name" jsonschema:"required,description=the name of the user"`
	Age  int    `json:"age" jsonschema:"required,description=the age of the user"`

	Job *Job `json:"job,omitempty" jsonschema:"description=the job of the user"`

	Incomes []*Income `json:"incomes" jsonschema:"description=the incomes of the user"`
}

type UserResult struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

var toolInfo = &schema.ToolInfo{
	Name: "update_user_info",
	Desc: "full update user info",
	ParamsOneOf: schema.NewParamsOneOfByJSONSchema(
		&jsonschema.Schema{
			Type:                 openapi3.TypeObject,
			Required:             []string{"name", "age", "incomes"},
			AdditionalProperties: jsonschema.FalseSchema,
			Properties: orderedmap.New[string, *jsonschema.Schema](
				orderedmap.WithInitialData(
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "name",
						Value: &jsonschema.Schema{
							Type:        "string",
							Description: "the name of the user",
						},
					},
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "age",
						Value: &jsonschema.Schema{
							Type:        "integer",
							Description: "the age of the user",
						},
					},
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "job",
						Value: &jsonschema.Schema{
							Type:                 "object",
							Required:             []string{"company"},
							AdditionalProperties: jsonschema.FalseSchema,
							Description:          "the job of the user",
							Properties: orderedmap.New[string, *jsonschema.Schema](
								orderedmap.WithInitialData(
									orderedmap.Pair[string, *jsonschema.Schema]{
										Key: "company",
										Value: &jsonschema.Schema{
											Type:        "string",
											Description: "the company where the user works",
										},
									},
									orderedmap.Pair[string, *jsonschema.Schema]{
										Key: "position",
										Value: &jsonschema.Schema{
											Type:        "string",
											Description: "the position of the user's job",
										},
									},
									orderedmap.Pair[string, *jsonschema.Schema]{
										Key: "service_length",
										Value: &jsonschema.Schema{
											Type:        "number",
											Description: "the year of user's service",
										},
									},
								),
							),
						},
					},
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "incomes",
						Value: &jsonschema.Schema{
							Type:        "array",
							Description: "the incomes of the user",
							Items: &jsonschema.Schema{
								Type:                 "object",
								AdditionalProperties: jsonschema.FalseSchema,
								Required:             []string{"source", "amount", "has_pay_tax"},
								Properties: orderedmap.New[string, *jsonschema.Schema](
									orderedmap.WithInitialData(
										orderedmap.Pair[string, *jsonschema.Schema]{
											Key: "source",
											Value: &jsonschema.Schema{
												Type:        "string",
												Description: "the source of income",
											},
										},
										orderedmap.Pair[string, *jsonschema.Schema]{
											Key: "amount",
											Value: &jsonschema.Schema{
												Type:        "integer",
												Description: "the amount of income",
											},
										},
										orderedmap.Pair[string, *jsonschema.Schema]{
											Key: "has_pay_tax",
											Value: &jsonschema.Schema{
												Type:        "boolean",
												Description: "whether the user has paid tax",
											},
										},
										orderedmap.Pair[string, *jsonschema.Schema]{
											Key: "job",
											Value: &jsonschema.Schema{
												Type:                 "object",
												AdditionalProperties: jsonschema.FalseSchema,
												Required:             []string{"company"},
												Description:          "the job of the user when earning this income",
												Properties: orderedmap.New[string, *jsonschema.Schema](
													orderedmap.WithInitialData(
														orderedmap.Pair[string, *jsonschema.Schema]{
															Key: "company",
															Value: &jsonschema.Schema{
																Type:        "string",
																Description: "the company where the user works",
															},
														},
														orderedmap.Pair[string, *jsonschema.Schema]{
															Key: "position",
															Value: &jsonschema.Schema{
																Type:        "string",
																Description: "the position of the user's job",
															},
														},
														orderedmap.Pair[string, *jsonschema.Schema]{
															Key: "service_length",
															Value: &jsonschema.Schema{
																Type:        "number",
																Description: "the year of user's service",
															},
														},
													),
												),
											},
										},
									),
								),
							},
						},
					},
				),
			),
		}),
}

func updateUserInfo(ctx context.Context, input *User) (output *UserResult, err error) {
	return &UserResult{
		Code: 200,
		Msg:  fmt.Sprintf("update %v success", input.Name),
	}, nil
}

type UserInfoOption struct {
	Field1 string
}

func WithUserInfoOption(s string) tool.Option {
	return tool.WrapImplSpecificOptFn(func(t *UserInfoOption) {
		t.Field1 = s
	})
}

func updateUserInfoWithOption(_ context.Context, input *User, opts ...tool.Option) (output *UserResult, err error) {
	baseOption := &UserInfoOption{
		Field1: "test_origin",
	}

	option := tool.GetImplSpecificOptions(baseOption, opts...)
	return &UserResult{
		Code: 200,
		Msg:  option.Field1,
	}, nil
}

func TestInferTool(t *testing.T) {
	t.Run("invoke_infer_tool", func(t *testing.T) {
		ctx := context.Background()

		tl, err := InferTool("update_user_info", "full update user info", updateUserInfo)
		assert.NoError(t, err)

		info, err := tl.Info(context.Background())
		assert.NoError(t, err)

		actual, err := info.ToJSONSchema()
		assert.NoError(t, err)
		actualStr, err := json.Marshal(actual)
		assert.NoError(t, err)

		expect, err := toolInfo.ToJSONSchema()
		assert.NoError(t, err)
		expectStr, err := json.Marshal(expect)
		assert.NoError(t, err)

		assert.Equal(t, string(expectStr), string(actualStr))

		content, err := tl.InvokableRun(ctx, `{"name": "bruce lee"}`)
		assert.NoError(t, err)
		assert.JSONEq(t, `{"code":200,"msg":"update bruce lee success"}`, content)
	})
}

func TestInferOptionableTool(t *testing.T) {
	ctx := context.Background()

	t.Run("invoke_infer_optionable_tool", func(t *testing.T) {

		tl, err := InferOptionableTool("invoke_infer_optionable_tool", "full update user info", updateUserInfoWithOption)
		assert.NoError(t, err)

		content, err := tl.InvokableRun(ctx, `{"name": "bruce lee"}`, WithUserInfoOption("hello world"))
		assert.NoError(t, err)
		assert.JSONEq(t, `{"code":200,"msg":"hello world"}`, content)
	})
}

func TestNewTool(t *testing.T) {
	ctx := context.Background()
	type Input struct {
		Name string `json:"name"`
	}
	type Output struct {
		Name string `json:"name"`
	}

	t.Run("struct_input_struct_output", func(t *testing.T) {

		tl := NewTool[Input, Output](nil, func(ctx context.Context, input Input) (output Output, err error) {
			return Output{
				Name: input.Name,
			}, nil
		})

		_, err := tl.InvokableRun(ctx, `{"name":"test"}`)
		assert.Nil(t, err)
	})

	t.Run("pointer_input_pointer_output", func(t *testing.T) {
		tl := NewTool[*Input, *Output](nil, func(ctx context.Context, input *Input) (output *Output, err error) {
			return &Output{
				Name: input.Name,
			}, nil
		})

		content, err := tl.InvokableRun(ctx, `{"name":"test"}`)
		assert.NoError(t, err)
		assert.Equal(t, `{"name":"test"}`, content)
	})

	t.Run("string_input_int64_output", func(t *testing.T) {
		tl := NewTool(nil, func(ctx context.Context, input string) (output int64, err error) {
			return 10, nil
		})

		content, err := tl.InvokableRun(ctx, `100`) // json unmarshal must contains double quote if is not json string.
		assert.Error(t, err)
		assert.Equal(t, "", content)
	})

	t.Run("string_pointer_input_int64_pointer_output", func(t *testing.T) {
		tl := NewTool[*string, *int64](nil, func(ctx context.Context, input *string) (output *int64, err error) {
			n := int64(10)
			return &n, nil
		})

		content, err := tl.InvokableRun(ctx, `"100"`)
		assert.NoError(t, err)
		assert.Equal(t, `10`, content)
	})
}

func TestSnakeToCamel(t *testing.T) {
	t.Run("normal_case", func(t *testing.T) {
		assert.Equal(t, "GoogleSearch3", snakeToCamel("google_search_3"))
	})

	t.Run("empty_case", func(t *testing.T) {
		assert.Equal(t, "", snakeToCamel(""))
	})

	t.Run("single_word_case", func(t *testing.T) {
		assert.Equal(t, "Google", snakeToCamel("google"))
	})

	t.Run("upper_case", func(t *testing.T) {
		assert.Equal(t, "HttpHost", snakeToCamel("_HTTP_HOST_"))
	})

	t.Run("underscore_case", func(t *testing.T) {
		assert.Equal(t, "", snakeToCamel("_"))
	})
}

type stringAlias string
type integerAlias uint32
type floatAlias float64
type boolAlias bool

type testEnumStruct struct {
	Field1 string       `json:"field1" jsonschema:"enum=a,enum=b"`
	Field2 int          `json:"field2" jsonschema:"enum=1,enum=2"`
	Field3 float32      `json:"field3" jsonschema:"enum=1.1,enum=2.2"`
	Field4 bool         `json:"field4" jsonschema:"default=true"`
	Field5 stringAlias  `json:"field5" jsonschema:"enum=a,enum=c"`
	Field6 integerAlias `json:"field6" jsonschema:"enum=3,enum=4"`
	Field7 floatAlias   `json:"field7" jsonschema:"enum=3.3,enum=4.4"`
	Field8 boolAlias    `json:"field8" jsonschema:"enum=false"`
}

type testEnumStruct2 struct {
	Field1 int8 `json:"field1" jsonschema:"enum=1.1"`
}

type testEnumStruct3 struct {
	Field1 float64 `json:"field1" jsonschema:"enum=a"`
}

func TestEnumTag(t *testing.T) {
	info, err := goStruct2ParamsOneOf[testEnumStruct]()
	assert.NoError(t, err)
	s, err := info.ToJSONSchema()
	assert.NoError(t, err)

	enum, ok := s.Properties.Get("field1")
	assert.True(t, ok)
	assert.Equal(t, []any{"a", "b"}, enum.Enum)

	enum, ok = s.Properties.Get("field2")
	assert.True(t, ok)
	assert.Equal(t, []any{json.Number("1"), json.Number("2")}, enum.Enum)

	enum, ok = s.Properties.Get("field3")
	assert.True(t, ok)
	assert.Equal(t, []any{json.Number("1.1"), json.Number("2.2")}, enum.Enum)

	enum, ok = s.Properties.Get("field4")
	assert.True(t, ok)
	assert.Equal(t, true, enum.Default)

	enum, ok = s.Properties.Get("field5")
	assert.True(t, ok)
	assert.Equal(t, []any{"a", "c"}, enum.Enum)

	enum, ok = s.Properties.Get("field6")
	assert.True(t, ok)
	assert.Equal(t, []any{json.Number("3"), json.Number("4")}, enum.Enum)

	enum, ok = s.Properties.Get("field7")
	assert.True(t, ok)
	assert.Equal(t, []any{json.Number("3.3"), json.Number("4.4")}, enum.Enum)

	_, err = goStruct2ParamsOneOf[testEnumStruct2]()
	assert.NoError(t, err)

	_, err = goStruct2ParamsOneOf[testEnumStruct3]()
	assert.NoError(t, err)
}
