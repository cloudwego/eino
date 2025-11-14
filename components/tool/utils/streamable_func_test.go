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
	"errors"
	"io"
	"testing"

	"github.com/eino-contrib/jsonschema"
	"github.com/stretchr/testify/assert"
	orderedmap "github.com/wk8/go-ordered-map/v2"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestNewStreamableTool(t *testing.T) {
	ctx := context.Background()
	type Input struct {
		Name string `json:"name"`
	}
	type Output struct {
		Name string `json:"name"`
	}

	t.Run("simple_case", func(t *testing.T) {
		tl := NewStreamTool[*Input, *Output](
			&schema.ToolInfo{
				Name: "search_user",
				Desc: "search user info",
				ParamsOneOf: schema.NewParamsOneOfByParams(
					map[string]*schema.ParameterInfo{
						"name": {
							Type: "string",
							Desc: "user name",
						},
					}),
			},
			func(ctx context.Context, input *Input) (output *schema.StreamReader[*Output], err error) {
				sr, sw := schema.Pipe[*Output](2)
				sw.Send(&Output{
					Name: input.Name,
				}, nil)
				sw.Send(&Output{
					Name: "lee",
				}, nil)
				sw.Close()

				return sr, nil
			},
		)

		info, err := tl.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "search_user", info.Name)

		js, err := info.ToJSONSchema()
		assert.NoError(t, err)

		assert.Equal(t, &jsonschema.Schema{
			Type: "object",
			Properties: orderedmap.New[string, *jsonschema.Schema](
				orderedmap.WithInitialData[string, *jsonschema.Schema](
					orderedmap.Pair[string, *jsonschema.Schema]{
						Key: "name",
						Value: &jsonschema.Schema{
							Type:        "string",
							Description: "user name",
						},
					},
				),
			),
			Required: make([]string, 0),
		}, js)

		sr, err := tl.StreamableRun(ctx, `{"name":"xxx"}`)
		assert.NoError(t, err)

		defer sr.Close()

		idx := 0
		for {
			m, err := sr.Recv()
			if errors.Is(err, io.EOF) {
				break
			}
			assert.NoError(t, err)

			if idx == 0 {
				assert.Equal(t, `{"name":"xxx"}`, m)
			} else {
				assert.Equal(t, `{"name":"lee"}`, m)
			}
			idx++
		}

		assert.Equal(t, 2, idx)
	})
}

type FakeStreamOption struct {
	Field string
}

type FakeStreamInferToolInput struct {
	Field string `json:"field"`
}

type FakeStreamInferToolOutput struct {
	Field string `json:"field"`
}

func FakeWithToolOption(s string) tool.Option {
	return tool.WrapImplSpecificOptFn(func(t *FakeStreamOption) {
		t.Field = s
	})
}

func fakeStreamFunc(ctx context.Context, input FakeStreamInferToolInput, opts ...tool.Option) (output *schema.StreamReader[*FakeStreamInferToolOutput], err error) {
	baseOpt := &FakeStreamOption{
		Field: "default_field_value",
	}
	option := tool.GetImplSpecificOptions(baseOpt, opts...)

	return schema.StreamReaderFromArray([]*FakeStreamInferToolOutput{
		{
			Field: option.Field,
		},
	}), nil
}

func TestInferStreamTool(t *testing.T) {
	st, err := InferOptionableStreamTool("infer_optionable_stream_tool", "test infer stream tool with option", fakeStreamFunc)
	assert.Nil(t, err)

	sr, err := st.StreamableRun(context.Background(), `{"field": "value"}`, FakeWithToolOption("hello world"))
	assert.Nil(t, err)

	defer sr.Close()

	idx := 0
	for {
		m, err := sr.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		assert.NoError(t, err)

		if idx == 0 {
			assert.JSONEq(t, `{"field":"hello world"}`, m)
		}
	}
}
