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

package callbacks

import (
	"context"
	"fmt"
	"io"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/callbacks"
	"github.com/cloudwego/eino/schema"
)

func TestAspectInject(t *testing.T) {
	t.Run("ctx without manager", func(t *testing.T) {
		ctx := context.Background()
		ctx = OnStart(ctx, 1)
		ctx = OnEnd(ctx, 2)
		ctx = OnError(ctx, fmt.Errorf("3"))
		isr, isw := schema.Pipe[int](2)
		go func() {
			for i := 0; i < 10; i++ {
				isw.Send(i, nil)
			}
			isw.Close()
		}()

		var nisr *schema.StreamReader[int]
		ctx, nisr = OnStartWithStreamInput(ctx, isr)
		j := 0
		for {
			i, err := nisr.Recv()
			if err == io.EOF {
				break
			}

			assert.NoError(t, err)
			assert.Equal(t, j, i)
			j++
		}
		nisr.Close()

		osr, osw := schema.Pipe[int](2)
		go func() {
			for i := 0; i < 10; i++ {
				osw.Send(i, nil)
			}
			osw.Close()
		}()

		var nosr *schema.StreamReader[int]
		ctx, nosr = OnEndWithStreamOutput(ctx, osr)
		j = 0
		for {
			i, err := nosr.Recv()
			if err == io.EOF {
				break
			}

			assert.NoError(t, err)
			assert.Equal(t, j, i)
			j++
		}
		nosr.Close()
	})

	t.Run("ctx with manager", func(t *testing.T) {
		ctx := context.Background()
		cnt := 0

		hb := NewHandlerBuilder().
			OnStartFn(func(ctx context.Context, info *RunInfo, input CallbackInput) context.Context {
				cnt += input.(int)
				return ctx
			}).
			OnEndFn(func(ctx context.Context, info *RunInfo, output CallbackOutput) context.Context {
				cnt += output.(int)
				return ctx
			}).
			OnErrorFn(func(ctx context.Context, info *RunInfo, err error) context.Context {
				v, _ := strconv.ParseInt(err.Error(), 10, 64)
				cnt += int(v)
				return ctx
			}).
			OnStartWithStreamInputFn(func(ctx context.Context, info *RunInfo, input *schema.StreamReader[CallbackInput]) context.Context {
				for {
					i, err := input.Recv()
					if err == io.EOF {
						break
					}

					cnt += i.(int)
				}

				input.Close()
				return ctx
			}).
			OnEndWithStreamOutputFn(func(ctx context.Context, info *RunInfo, output *schema.StreamReader[CallbackOutput]) context.Context {
				for {
					o, err := output.Recv()
					if err == io.EOF {
						break
					}

					cnt += o.(int)
				}

				output.Close()
				return ctx
			}).Build()

		ctx = InitCallbacks(ctx, nil, hb)

		ctx = OnStart(ctx, 1)
		ctx = OnEnd(ctx, 2)
		ctx = OnError(ctx, fmt.Errorf("3"))
		isr, isw := schema.Pipe[int](2)
		go func() {
			for i := 0; i < 10; i++ {
				isw.Send(i, nil)
			}
			isw.Close()
		}()

		var nisr *schema.StreamReader[int]
		ctx, nisr = OnStartWithStreamInput(ctx, isr)
		j := 0
		for {
			i, err := nisr.Recv()
			if err == io.EOF {
				break
			}

			assert.NoError(t, err)
			assert.Equal(t, j, i)
			j++
			cnt += i
		}
		nisr.Close()

		osr, osw := schema.Pipe[int](2)
		go func() {
			for i := 0; i < 10; i++ {
				osw.Send(i, nil)
			}
			osw.Close()
		}()

		var nosr *schema.StreamReader[int]
		ctx, nosr = OnEndWithStreamOutput(ctx, osr)
		j = 0
		for {
			i, err := nosr.Recv()
			if err == io.EOF {
				break
			}

			assert.NoError(t, err)
			assert.Equal(t, j, i)
			j++
			cnt += i
		}
		nosr.Close()
		assert.Equal(t, 186, cnt)
	})
}

func TestGlobalCallbacksRepeated(t *testing.T) {
	times := 0
	testHandler := NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
		times++
		return ctx
	}).Build()
	callbacks.GlobalHandlers = append(callbacks.GlobalHandlers, testHandler)

	ctx := context.Background()
	ctx = callbacks.AppendHandlers(ctx, &RunInfo{})
	ctx = callbacks.AppendHandlers(ctx, &RunInfo{})

	callbacks.On(ctx, "test", callbacks.OnStartHandle[string], TimingOnStart)
	assert.Equal(t, times, 1)
}

func TestEnsureRunInfo(t *testing.T) {
	ctx := context.Background()

	var name, typ, comp string
	ctx = InitCallbacks(ctx, &RunInfo{Name: "name", Type: "type", Component: "component"}, NewHandlerBuilder().OnStartFn(func(ctx context.Context, info *RunInfo, input CallbackInput) context.Context {
		name = info.Name
		typ = info.Type
		comp = string(info.Component)
		return ctx
	}).Build())

	ctx2 := EnsureRunInfo(ctx, "type2", "component2")

	OnStart(ctx, "")
	assert.Equal(t, "name", name)
	assert.Equal(t, "type", typ)
	assert.Equal(t, "component", comp)
	OnStart(ctx2, "")
	assert.Equal(t, "", name)
	assert.Equal(t, "type2", typ)
	assert.Equal(t, "component2", comp)
}
