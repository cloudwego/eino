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

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// cancelMonitoredModel wraps a chat model with cancel monitoring.
// For Generate: checks safe-point after completion for CancelAfterChatModel.
// For Stream: monitors immediateChan for stream abort, and checks safe-point at EOF.
type cancelMonitoredModel struct {
	inner     model.BaseChatModel
	cancelCtx *cancelContext
}

type recvResult[T any] struct {
	data T
	err  error
}

func (m *cancelMonitoredModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	result, err := m.inner.Generate(ctx, input, opts...)
	if err != nil {
		return nil, err
	}

	// Non-streaming safe-point for CancelAfterChatModel
	if m.cancelCtx.shouldCancel() {
		m.cancelCtx.triggerSafePointInterrupt()
	}

	return result, nil
}

func (m *cancelMonitoredModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	stream, err := m.inner.Stream(ctx, input, opts...)
	if err != nil {
		return nil, err
	}

	reader, writer := schema.Pipe[*schema.Message](1)
	cc := m.cancelCtx

	go func() {
		// done is closed when this goroutine exits, unblocking the recv helper
		// if it's stuck trying to send to ch.
		done := make(chan struct{})
		defer close(done)
		defer writer.Close()
		defer stream.Close()

		ch := make(chan recvResult[*schema.Message])
		go func() {
			defer close(ch)
			for {
				chunk, recvErr := stream.Recv()
				select {
				case ch <- recvResult[*schema.Message]{chunk, recvErr}:
				case <-done:
					return
				}
				if recvErr != nil {
					return
				}
			}
		}()

		for {
			select {
			case <-cc.immediateChan:
				// CancelImmediate or timeout escalation — abort stream
				writer.Send(nil, ErrStreamCancelled)
				return
				// defers: stream.Close() unblocks inner Recv,
				//         close(done) unblocks inner ch send

			case r, ok := <-ch:
				if !ok {
					return // inner goroutine exited unexpectedly
				}
				if r.err != nil {
					if r.err == io.EOF {
						// Stream drained naturally — safe-point for CancelAfterChatModel
						if cc.shouldCancel() {
							cc.triggerSafePointInterrupt()
						}
						return
					}
					writer.Send(nil, r.err)
					return
				}
				if closed := writer.Send(r.data, nil); closed {
					return
				}
			}
		}
	}()

	return reader, nil
}
