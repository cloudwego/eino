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

package compose

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestLambda(t *testing.T) {
	t.Run("InvokableLambda", func(t *testing.T) {
		ld := InvokableLambdaWithOption(
			func(ctx context.Context, input string, opts ...any) (output string, err error) {
				return "good", nil
			},
			WithLambdaCallbackEnable(false),
			WithLambdaType("ForTest"),
		)

		assert.Equal(t, false, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "ForTest", ld.GetType())

		ld = InvokableLambda(
			func(ctx context.Context, input string) (output string, err error) {
				return "good", nil
			},
			WithLambdaCallbackEnable(false),
			WithLambdaType("ForTest"),
		)

		assert.Equal(t, false, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "ForTest", ld.GetType())
	})

	t.Run("StreamableLambda", func(t *testing.T) {
		ld := StreamableLambdaWithOption(
			func(ctx context.Context, input string, opts ...any) (output *schema.StreamReader[string], err error) {
				sr, sw := schema.Pipe[string](1)
				sw.Close()
				return sr, nil
			},
			WithLambdaCallbackEnable(false),
			WithLambdaType("ForTest"),
		)

		assert.Equal(t, false, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "ForTest", ld.GetType())

		ld = StreamableLambda(
			func(ctx context.Context, input string) (output *schema.StreamReader[string], err error) {
				sr, sw := schema.Pipe[string](1)
				sw.Close()
				return sr, nil
			},
			WithLambdaCallbackEnable(false),
			WithLambdaType("ForTest"),
		)

		assert.Equal(t, false, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "ForTest", ld.GetType())
	})

	t.Run("CollectableLambda", func(t *testing.T) {
		ld := CollectableLambdaWithOption(
			func(ctx context.Context, input *schema.StreamReader[string], opts ...any) (output string, err error) {
				return "good", nil
			},
			WithLambdaCallbackEnable(true),
		)

		assert.Equal(t, true, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "", ld.GetType())

		ld = CollectableLambda(
			func(ctx context.Context, input *schema.StreamReader[string]) (output string, err error) {
				return "good", nil
			},
			WithLambdaCallbackEnable(true),
		)

		assert.Equal(t, true, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "", ld.GetType())
	})

	t.Run("TransformableLambda", func(t *testing.T) {
		ld := TransformableLambdaWithOption(
			func(ctx context.Context, input *schema.StreamReader[string], opts ...any) (output *schema.StreamReader[string], err error) {
				sr, sw := schema.Pipe[string](1)
				sw.Close()
				return sr, nil
			},
			WithLambdaCallbackEnable(true),
		)

		assert.Equal(t, true, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "", ld.GetType())

		ld = TransformableLambda(
			func(ctx context.Context, input *schema.StreamReader[string]) (output *schema.StreamReader[string], err error) {
				sr, sw := schema.Pipe[string](1)
				sw.Close()
				return sr, nil
			},
			WithLambdaCallbackEnable(true),
		)

		assert.Equal(t, true, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "", ld.GetType())
	})

	t.Run("AnyLambda", func(t *testing.T) {
		ld, err := AnyLambda[string, string](
			func(ctx context.Context, input string, opts ...any) (output string, err error) {
				return "good", nil
			},
			func(ctx context.Context, input string, opts ...any) (output *schema.StreamReader[string], err error) {
				sr, sw := schema.Pipe[string](1)
				sw.Close()
				return sr, nil
			},
			func(ctx context.Context, input *schema.StreamReader[string], opts ...any) (output string, err error) {
				return "good", nil
			},
			func(ctx context.Context, input *schema.StreamReader[string], opts ...any) (output *schema.StreamReader[string], err error) {
				sr, sw := schema.Pipe[string](1)
				sw.Close()
				return sr, nil
			},
			WithLambdaCallbackEnable(true),
			WithLambdaType("ForTest"),
		)
		assert.NoError(t, err)

		assert.Equal(t, true, ld.IsCallbacksEnabled())
		assert.Equal(t, ComponentOfLambda, ld.Component())
		assert.Equal(t, "ForTest", ld.GetType())
	})
}

type TestStructForParse struct {
	ID int `json:"id"`
}

func TestMessageParser(t *testing.T) {
	t.Run("parse from content", func(t *testing.T) {
		parser := schema.NewMessageJSONParser[TestStructForParse](&schema.MessageJSONParseConfig{
			ParseFrom: schema.MessageParseFromContent,
		})

		parserLambda := MessageParser(parser)

		chain := NewChain[*schema.Message, TestStructForParse]()
		chain.AppendLambda(parserLambda)

		r, err := chain.Compile(context.Background())
		assert.Nil(t, err)

		parsed, err := r.Invoke(context.Background(), &schema.Message{
			Content: `{"id": 1}`,
		})
		assert.Nil(t, err)
		assert.Equal(t, 1, parsed.ID)
	})

	t.Run("parse from tool call", func(t *testing.T) {
		parser := schema.NewMessageJSONParser[*TestStructForParse](&schema.MessageJSONParseConfig{
			ParseFrom: schema.MessageParseFromToolCall,
		})

		parserLambda := MessageParser(parser)

		chain := NewChain[*schema.Message, *TestStructForParse]()
		chain.AppendLambda(parserLambda)

		r, err := chain.Compile(context.Background())
		assert.Nil(t, err)

		parsed, err := r.Invoke(context.Background(), &schema.Message{
			ToolCalls: []schema.ToolCall{
				{Function: schema.FunctionCall{Arguments: `{"id": 1}`}},
			},
		})
		assert.Nil(t, err)
		assert.Equal(t, 1, parsed.ID)
	})
}
