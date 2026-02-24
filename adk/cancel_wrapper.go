/*
 * Copyright 2026 CloudWeGo Authors
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

package adk

import (
	"context"
	"io"
	"runtime/debug"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/internal/safe"
	"github.com/cloudwego/eino/schema"
)

type cancelWaitResult[T any] struct {
	result    T
	err       error
	cancelled bool
}

func waitWithCancel[T any](cs *cancelSig, resultCh <-chan cancelWaitResult[T]) cancelWaitResult[T] {
	var timeCh <-chan time.Time
	select {
	case <-cs.done:
		cfg := cs.config.Load().(*cancelConfig)
		if cfg.Mode == CancelImmediate {
			if cfg.Timeout == nil {
				return cancelWaitResult[T]{cancelled: true}
			}
			timeCh = time.After(*cfg.Timeout)
		}
	case res := <-resultCh:
		return res
	}
	select {
	case <-timeCh:
		return cancelWaitResult[T]{cancelled: true}
	case res := <-resultCh:
		return res
	}
}

type cancelableChatModel struct {
	inner model.BaseChatModel
	cs    *cancelSig
}

func wrapModelForCancelable(m model.BaseChatModel, cs *cancelSig) *cancelableChatModel {
	return &cancelableChatModel{inner: m, cs: cs}
}

func (c *cancelableChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	if cfg := checkCancelSig(c.cs); cfg != nil && cfg.Mode == CancelImmediate {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}

	resultCh := make(chan cancelWaitResult[*schema.Message], 1)
	go func() {
		defer func() {
			if panicErr := recover(); panicErr != nil {
				resultCh <- cancelWaitResult[*schema.Message]{err: safe.NewPanicErr(panicErr, debug.Stack())}
			}
		}()
		res, err := c.inner.Generate(ctx, input, opts...)
		resultCh <- cancelWaitResult[*schema.Message]{result: res, err: err}
	}()

	res := waitWithCancel(c.cs, resultCh)
	if res.cancelled {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}
	return res.result, res.err
}

func (c *cancelableChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if cfg := checkCancelSig(c.cs); cfg != nil && cfg.Mode == CancelImmediate {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}

	resultCh := make(chan cancelWaitResult[*schema.StreamReader[*schema.Message]], 1)
	go func() {
		defer func() {
			if panicErr := recover(); panicErr != nil {
				resultCh <- cancelWaitResult[*schema.StreamReader[*schema.Message]]{err: safe.NewPanicErr(panicErr, debug.Stack())}
			}
		}()

		stream, err := c.inner.Stream(ctx, input, opts...)
		if err != nil {
			resultCh <- cancelWaitResult[*schema.StreamReader[*schema.Message]]{err: err}
			return
		}
		copies := stream.Copy(2)
		_ = consumeStreamForError(copies[0])
		resultCh <- cancelWaitResult[*schema.StreamReader[*schema.Message]]{result: copies[1]}
	}()

	res := waitWithCancel(c.cs, resultCh)
	if res.cancelled {
		return nil, compose.Interrupt(ctx, "cancelled externally")
	}
	return res.result, res.err
}

func cancelableToolInvokable(cs *cancelSig, endpoint compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}

		resultCh := make(chan cancelWaitResult[*compose.ToolOutput], 1)
		go func() {
			defer func() {
				if panicErr := recover(); panicErr != nil {
					resultCh <- cancelWaitResult[*compose.ToolOutput]{err: safe.NewPanicErr(panicErr, debug.Stack())}
				}
			}()
			output, err := endpoint(ctx, input)
			resultCh <- cancelWaitResult[*compose.ToolOutput]{result: output, err: err}
		}()

		res := waitWithCancel(cs, resultCh)
		if res.cancelled {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}
		return res.result, res.err
	}
}

func cancelableToolStreamable(cs *cancelSig, endpoint compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}

		resultCh := make(chan cancelWaitResult[*schema.StreamReader[string]], 1)
		go func() {
			defer func() {
				if panicErr := recover(); panicErr != nil {
					resultCh <- cancelWaitResult[*schema.StreamReader[string]]{err: safe.NewPanicErr(panicErr, debug.Stack())}
				}
			}()
			output, err := endpoint(ctx, input)
			if err != nil {
				resultCh <- cancelWaitResult[*schema.StreamReader[string]]{err: err}
				return
			}
			copies := output.Result.Copy(2)
			_ = consumeStreamForErrorString(copies[0])
			resultCh <- cancelWaitResult[*schema.StreamReader[string]]{result: copies[1]}
		}()

		res := waitWithCancel(cs, resultCh)
		if res.cancelled {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}
		if res.err != nil {
			return nil, res.err
		}
		return &compose.StreamToolOutput{Result: res.result}, nil
	}
}

func cancelableToolEnhancedInvokable(cs *cancelSig, endpoint compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
		if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}

		resultCh := make(chan cancelWaitResult[*compose.EnhancedInvokableToolOutput], 1)
		go func() {
			defer func() {
				if panicErr := recover(); panicErr != nil {
					resultCh <- cancelWaitResult[*compose.EnhancedInvokableToolOutput]{err: safe.NewPanicErr(panicErr, debug.Stack())}
				}
			}()
			output, err := endpoint(ctx, input)
			resultCh <- cancelWaitResult[*compose.EnhancedInvokableToolOutput]{result: output, err: err}
		}()

		res := waitWithCancel(cs, resultCh)
		if res.cancelled {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}
		return res.result, res.err
	}
}

func cancelableToolEnhancedStreamable(cs *cancelSig, endpoint compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
	return func(ctx context.Context, input *compose.ToolInput) (*compose.EnhancedStreamableToolOutput, error) {
		if cfg := checkCancelSig(cs); cfg != nil && cfg.Mode == CancelImmediate {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}

		resultCh := make(chan cancelWaitResult[*schema.StreamReader[*schema.ToolResult]], 1)
		go func() {
			defer func() {
				if panicErr := recover(); panicErr != nil {
					resultCh <- cancelWaitResult[*schema.StreamReader[*schema.ToolResult]]{err: safe.NewPanicErr(panicErr, debug.Stack())}
				}
			}()
			output, err := endpoint(ctx, input)
			if err != nil {
				resultCh <- cancelWaitResult[*schema.StreamReader[*schema.ToolResult]]{err: err}
				return
			}
			copies := output.Result.Copy(2)
			_ = consumeStreamForErrorToolResult(copies[0])
			resultCh <- cancelWaitResult[*schema.StreamReader[*schema.ToolResult]]{result: copies[1]}
		}()

		res := waitWithCancel(cs, resultCh)
		if res.cancelled {
			return nil, compose.Interrupt(ctx, "cancelled externally")
		}
		if res.err != nil {
			return nil, res.err
		}
		return &compose.EnhancedStreamableToolOutput{Result: res.result}, nil
	}
}

func cancelableTool(cs *cancelSig) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(endpoint compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return cancelableToolInvokable(cs, endpoint)
		},
		Streamable: func(endpoint compose.StreamableToolEndpoint) compose.StreamableToolEndpoint {
			return cancelableToolStreamable(cs, endpoint)
		},
		EnhancedInvokable: func(endpoint compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
			return cancelableToolEnhancedInvokable(cs, endpoint)
		},
		EnhancedStreamable: func(endpoint compose.EnhancedStreamableToolEndpoint) compose.EnhancedStreamableToolEndpoint {
			return cancelableToolEnhancedStreamable(cs, endpoint)
		},
	}
}

func consumeStreamForErrorString(stream *schema.StreamReader[string]) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func consumeStreamForErrorToolResult(stream *schema.StreamReader[*schema.ToolResult]) error {
	defer stream.Close()
	for {
		_, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}
