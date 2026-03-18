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

package team

import (
	"context"

	"github.com/cloudwego/eino/adk"
)

// ChanSource is a channel-backed MessageSource[TurnInput] for testing.
//
// Usage:
//
//	src := NewChanSource(1)
//	src.Send(TurnInput{...})   // enqueue a message
//	src.Close()                // signal loop exit after all messages are consumed
//
// Receive blocks until a message is available or the channel is closed.
// When the channel is closed, Receive returns adk.ErrLoopExit.
type ChanSource struct {
	ch chan TurnInput
}

// NewChanSource creates a ChanSource with the given buffer size.
func NewChanSource(bufSize int) *ChanSource {
	return &ChanSource{ch: make(chan TurnInput, bufSize)}
}

// Send enqueues a TurnInput. Blocks if the buffer is full.
func (s *ChanSource) Send(input TurnInput) {
	s.ch <- input
}

// Close signals that no more messages will be sent.
// Subsequent Receive calls will return adk.ErrLoopExit once the buffer is drained.
func (s *ChanSource) Close() {
	close(s.ch)
}

// Receive implements MessageSource[TurnInput].Receive.
func (s *ChanSource) Receive(ctx context.Context,
	_ adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {

	select {
	case <-ctx.Done():
		return ctx, TurnInput{}, nil, ctx.Err()
	case item, ok := <-s.ch:
		if !ok {
			return ctx, TurnInput{}, nil, adk.ErrLoopExit
		}
		return ctx, item, nil, nil
	}
}

// Front implements MessageSource[TurnInput].Front.
// For simplicity it behaves the same as Receive (consumes the item).
func (s *ChanSource) Front(ctx context.Context,
	cfg adk.ReceiveConfig) (context.Context, TurnInput, []adk.ConsumeOption, error) {
	return s.Receive(ctx, cfg)
}
