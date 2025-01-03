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

package internal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestRunnableLambda(t *testing.T) {
	ctx := context.Background()

	t.Run("invoke_to_runnable", func(t *testing.T) {
		rl := runnableLambda(
			func(ctx context.Context, input int, opts ...Option) (output string, err error) {
				return strconv.Itoa(input) + "+" + opts[0].options[0].(string), nil
			},
			nil, nil, nil, false)

		ctxWrapper := func(ctx context.Context, opts ...Option) context.Context {
			return ctx
		}
		gr, err := toGenericRunnable[int, string](rl, ctxWrapper)
		assert.NoError(t, err)
		out, err := gr.Invoke(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err := gr.Stream(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sri, swi := schema.Pipe[int](1)
		_ = swi.Send(10, nil)
		swi.Close()
		sriArr := sri.Copy(2)

		out, err = gr.Collect(ctx, sriArr[0], WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err = gr.Transform(ctx, sriArr[1], WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)
	})

	t.Run("stream_to_runnable", func(t *testing.T) {
		rl := runnableLambda(nil,
			func(ctx context.Context, input int, opts ...Option) (output *schema.StreamReader[string], err error) {
				sro, swo := schema.Pipe[string](3)
				_ = swo.Send(strconv.Itoa(input), nil)
				_ = swo.Send("+", nil)
				_ = swo.Send(opts[0].options[0].(string), nil)
				swo.Close()
				return sro, nil
			}, nil, nil, false)

		ctxWrapper := func(ctx context.Context, opts ...Option) context.Context {
			return ctx
		}
		gr, err := toGenericRunnable[int, string](rl, ctxWrapper)
		assert.NoError(t, err)
		out, err := gr.Invoke(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err := gr.Stream(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sri, swi := schema.Pipe[int](1)
		_ = swi.Send(10, nil)
		swi.Close()
		sriArr := sri.Copy(2)

		out, err = gr.Collect(ctx, sriArr[0], WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err = gr.Transform(ctx, sriArr[1], WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)
	})

	t.Run("transform_to_runnable", func(t *testing.T) {
		rl := runnableLambda(
			nil, nil, nil,
			func(ctx context.Context, input *schema.StreamReader[int], opts ...Option) (output *schema.StreamReader[string], err error) {

				in, e := input.Recv()
				if errors.Is(e, io.EOF) {
					return nil, fmt.Errorf("unpected EOF")
				}
				input.Close()

				sro, swo := schema.Pipe[string](3)
				_ = swo.Send(strconv.Itoa(in), nil)
				_ = swo.Send("+", nil)
				_ = swo.Send(opts[0].options[0].(string), nil)
				swo.Close()
				return sro, nil
			},
			false)

		ctxWrapper := func(ctx context.Context, opts ...Option) context.Context {
			return ctx
		}
		gr, err := toGenericRunnable[int, string](rl, ctxWrapper)
		assert.NoError(t, err)
		out, err := gr.Invoke(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err := gr.Stream(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sri, swi := schema.Pipe[int](1)
		_ = swi.Send(10, nil)
		swi.Close()
		sriArr := sri.Copy(2)

		out, err = gr.Collect(ctx, sriArr[0], WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err = gr.Transform(ctx, sriArr[1], WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)
	})

	t.Run("collect_to_runnable", func(t *testing.T) {
		rl := runnableLambda(nil, nil,
			func(ctx context.Context, input *schema.StreamReader[int], opts ...Option) (output string, err error) {
				in, e := input.Recv()
				if errors.Is(e, io.EOF) {
					return "", fmt.Errorf("unpected EOF")
				}
				input.Close()

				return strconv.Itoa(in) + "+" + opts[0].options[0].(string), nil
			},
			nil, false)

		ctxWrapper := func(ctx context.Context, opts ...Option) context.Context {
			return ctx
		}

		gr, err := toGenericRunnable[int, string](rl, ctxWrapper)
		assert.NoError(t, err)
		out, err := gr.Invoke(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err := gr.Stream(ctx, 10, WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sri, swi := schema.Pipe[int](1)
		_ = swi.Send(10, nil)
		swi.Close()
		sriArr := sri.Copy(2)

		out, err = gr.Collect(ctx, sriArr[0], WithLambdaOption("100"))
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)

		sr, err = gr.Transform(ctx, sriArr[1], WithLambdaOption("100"))
		assert.NoError(t, err)
		out, err = ConcatStreamReader(sr)
		assert.NoError(t, err)
		assert.Equal(t, "10+100", out)
	})
}
