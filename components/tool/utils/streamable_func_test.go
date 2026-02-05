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

type EnhancedStreamInput struct {
	Query string `json:"query" jsonschema:"description=the search query"`
}

func TestNewEnhancedStreamTool(t *testing.T) {
	ctx := context.Background()

	t.Run("simple_case", func(t *testing.T) {
		tl := NewEnhancedStreamTool[*EnhancedStreamInput](
			&schema.ToolInfo{
				Name: "enhanced_stream_search",
				Desc: "search with enhanced stream output",
				ParamsOneOf: schema.NewParamsOneOfByParams(
					map[string]*schema.ParameterInfo{
						"query": {
							Type: "string",
							Desc: "the search query",
						},
					}),
			},
			func(ctx context.Context, input *EnhancedStreamInput) (*schema.StreamReader[*schema.ToolResult], error) {
				sr, sw := schema.Pipe[*schema.ToolResult](2)
				sw.Send(&schema.ToolResult{
					Parts: []schema.ToolOutputPart{
						{Type: schema.ToolPartTypeText, Text: "result for: " + input.Query},
					},
				}, nil)
				sw.Send(&schema.ToolResult{
					Parts: []schema.ToolOutputPart{
						{Type: schema.ToolPartTypeText, Text: "more results"},
					},
				}, nil)
				sw.Close()
				return sr, nil
			},
		)

		info, err := tl.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "enhanced_stream_search", info.Name)

		sr, err := tl.StreamableRun(ctx, &schema.ToolArgument{TextArgument: `{"query":"test"}`})
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
				assert.Len(t, m.Parts, 1)
				assert.Equal(t, schema.ToolPartTypeText, m.Parts[0].Type)
				assert.Equal(t, "result for: test", m.Parts[0].Text)
			} else {
				assert.Len(t, m.Parts, 1)
				assert.Equal(t, "more results", m.Parts[0].Text)
			}
			idx++
		}
		assert.Equal(t, 2, idx)
	})
}

type FakeEnhancedStreamOption struct {
	Prefix string
}

func FakeWithEnhancedStreamOption(prefix string) tool.Option {
	return tool.WrapImplSpecificOptFn(func(t *FakeEnhancedStreamOption) {
		t.Prefix = prefix
	})
}

func fakeEnhancedStreamFunc(ctx context.Context, input EnhancedStreamInput) (*schema.StreamReader[*schema.ToolResult], error) {
	return schema.StreamReaderFromArray([]*schema.ToolResult{
		{
			Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: "result: " + input.Query},
			},
		},
	}), nil
}

func fakeOptionableEnhancedStreamFunc(ctx context.Context, input EnhancedStreamInput, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	baseOpt := &FakeEnhancedStreamOption{
		Prefix: "default",
	}
	option := tool.GetImplSpecificOptions(baseOpt, opts...)

	return schema.StreamReaderFromArray([]*schema.ToolResult{
		{
			Parts: []schema.ToolOutputPart{
				{Type: schema.ToolPartTypeText, Text: option.Prefix + ": " + input.Query},
			},
		},
	}), nil
}

func TestInferEnhancedStreamTool(t *testing.T) {
	ctx := context.Background()

	t.Run("infer_enhanced_stream_tool", func(t *testing.T) {
		tl, err := InferEnhancedStreamTool("infer_enhanced_stream", "test infer enhanced stream tool", fakeEnhancedStreamFunc)
		assert.NoError(t, err)

		info, err := tl.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "infer_enhanced_stream", info.Name)

		sr, err := tl.StreamableRun(ctx, &schema.ToolArgument{TextArgument: `{"query":"hello"}`})
		assert.NoError(t, err)
		defer sr.Close()

		m, err := sr.Recv()
		assert.NoError(t, err)
		assert.Len(t, m.Parts, 1)
		assert.Equal(t, "result: hello", m.Parts[0].Text)
	})
}

func TestInferOptionableEnhancedStreamTool(t *testing.T) {
	ctx := context.Background()

	t.Run("infer_optionable_enhanced_stream_tool", func(t *testing.T) {
		tl, err := InferOptionableEnhancedStreamTool("infer_optionable_enhanced_stream", "test infer optionable enhanced stream tool", fakeOptionableEnhancedStreamFunc)
		assert.NoError(t, err)

		info, err := tl.Info(ctx)
		assert.NoError(t, err)
		assert.Equal(t, "infer_optionable_enhanced_stream", info.Name)

		sr, err := tl.StreamableRun(ctx, &schema.ToolArgument{TextArgument: `{"query":"world"}`}, FakeWithEnhancedStreamOption("custom"))
		assert.NoError(t, err)
		defer sr.Close()

		m, err := sr.Recv()
		assert.NoError(t, err)
		assert.Len(t, m.Parts, 1)
		assert.Equal(t, "custom: world", m.Parts[0].Text)
	})

	t.Run("infer_optionable_enhanced_stream_tool_default_option", func(t *testing.T) {
		tl, err := InferOptionableEnhancedStreamTool("infer_optionable_enhanced_stream", "test infer optionable enhanced stream tool", fakeOptionableEnhancedStreamFunc)
		assert.NoError(t, err)

		sr, err := tl.StreamableRun(ctx, &schema.ToolArgument{TextArgument: `{"query":"test"}`})
		assert.NoError(t, err)
		defer sr.Close()

		m, err := sr.Recv()
		assert.NoError(t, err)
		assert.Len(t, m.Parts, 1)
		assert.Equal(t, "default: test", m.Parts[0].Text)
	})
}
