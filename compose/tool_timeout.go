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

package compose

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// NewToolTimeoutMiddleware sets an independent deadline for each tool call.
// Add the returned middleware to ToolsNodeConfig.ToolCallMiddlewares:
//
//	limit, err := compose.NewToolTimeoutMiddleware(3 * time.Second)
//	if err != nil {
//		return err
//	}
//	config.ToolCallMiddlewares = append(config.ToolCallMiddlewares, limit)
//
// For streaming tools, the deadline lasts until the stream ends or is closed.
// A timed-out reader reports context.DeadlineExceeded, even if the tool is
// blocked producing the next chunk. Tools must honor context cancellation to
// release their own work; an uncooperative tool cannot be forcibly stopped.
func NewToolTimeoutMiddleware(timeout time.Duration) (ToolMiddleware, error) {
	if timeout <= 0 {
		return ToolMiddleware{}, fmt.Errorf("tool timeout must be positive, got %s", timeout)
	}

	return ToolMiddleware{
		Invokable: func(next InvokableToolEndpoint) InvokableToolEndpoint {
			return func(ctx context.Context, input *ToolInput) (*ToolOutput, error) {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				out, err := next(callCtx, input)
				if callCtx.Err() != nil {
					return nil, callCtx.Err()
				}
				return out, err
			}
		},
		EnhancedInvokable: func(next EnhancedInvokableToolEndpoint) EnhancedInvokableToolEndpoint {
			return func(ctx context.Context, input *ToolInput) (*EnhancedInvokableToolOutput, error) {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				defer cancel()
				out, err := next(callCtx, input)
				if callCtx.Err() != nil {
					return nil, callCtx.Err()
				}
				return out, err
			}
		},
		Streamable: func(next StreamableToolEndpoint) StreamableToolEndpoint {
			return func(ctx context.Context, input *ToolInput) (*StreamToolOutput, error) {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				out, err := next(callCtx, input)
				if callCtx.Err() != nil {
					if out != nil && out.Result != nil {
						out.Result.Close()
					}
					cancel()
					return nil, callCtx.Err()
				}
				if err != nil {
					cancel()
					if out != nil && out.Result != nil {
						out.Result.Close()
					}
					return nil, err
				}
				if out == nil || out.Result == nil {
					cancel()
					return out, nil
				}
				return &StreamToolOutput{Result: timeoutStream(callCtx, cancel, out.Result)}, nil
			}
		},
		EnhancedStreamable: func(next EnhancedStreamableToolEndpoint) EnhancedStreamableToolEndpoint {
			return func(ctx context.Context, input *ToolInput) (*EnhancedStreamableToolOutput, error) {
				callCtx, cancel := context.WithTimeout(ctx, timeout)
				out, err := next(callCtx, input)
				if callCtx.Err() != nil {
					if out != nil && out.Result != nil {
						out.Result.Close()
					}
					cancel()
					return nil, callCtx.Err()
				}
				if err != nil {
					cancel()
					if out != nil && out.Result != nil {
						out.Result.Close()
					}
					return nil, err
				}
				if out == nil || out.Result == nil {
					cancel()
					return out, nil
				}
				return &EnhancedStreamableToolOutput{Result: timeoutStream(callCtx, cancel, out.Result)}, nil
			}
		},
	}, nil
}

type timeoutStreamItem[T any] struct {
	value T
	err   error
}

func timeoutStream[T any](ctx context.Context, cancel context.CancelFunc, source *schema.StreamReader[T]) *schema.StreamReader[T] {
	reader, writer := schema.Pipe[T](1)
	items := make(chan timeoutStreamItem[T])
	var sourceClosed sync.Once
	closeSource := func() {
		sourceClosed.Do(func() {
			if !source.ClosePipeReader() {
				source.Close()
			}
		})
	}

	go func() {
		defer closeSource()
		for {
			value, err := source.Recv()
			select {
			case items <- timeoutStreamItem[T]{value: value, err: err}:
			case <-ctx.Done():
				return
			case <-reader.Done():
				return
			}
			if err != nil {
				return
			}
		}
	}()

	go func() {
		defer cancel()
		defer writer.Close()
		defer func() {
			if source.Done() != nil {
				closeSource()
			}
		}()
		for {
			select {
			case <-reader.Done():
				return
			case <-ctx.Done():
				writer.SendTerminal(ctx.Err())
				return
			case item := <-items:
				if ctx.Err() != nil {
					writer.SendTerminal(ctx.Err())
					return
				}
				if item.err == io.EOF {
					return
				}
				if writer.SendContext(ctx, item.value, item.err) {
					if ctx.Err() != nil {
						writer.SendTerminal(ctx.Err())
					}
					return
				}
				if item.err != nil {
					return
				}
			}
		}
	}()

	return reader
}
