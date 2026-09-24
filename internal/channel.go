/*
 * Copyright 2025 CloudWeGo Authors
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
	"sync"
)

// UnboundedChan represents a channel with unlimited capacity
type UnboundedChan[T any] struct {
	buffer   []T           // Internal buffer to store data
	mutex    sync.Mutex    // Mutex to protect buffer access
	notEmpty *sync.Cond    // Condition variable to wait for data
	notify   chan struct{} // Notification channel for context-aware receives
	closed   bool          // Indicates if the channel has been closed
}

// NewUnboundedChan initializes and returns an UnboundedChan
func NewUnboundedChan[T any]() *UnboundedChan[T] {
	ch := &UnboundedChan[T]{notify: make(chan struct{}, 1)}
	ch.notEmpty = sync.NewCond(&ch.mutex)
	return ch
}

func (ch *UnboundedChan[T]) signal() {
	select {
	case ch.notify <- struct{}{}:
	default:
	}
}

// Send puts an item into the channel
func (ch *UnboundedChan[T]) Send(value T) {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if ch.closed {
		panic("send on closed channel")
	}

	ch.buffer = append(ch.buffer, value)
	ch.notEmpty.Signal() // Wake up one goroutine waiting to receive
	ch.signal()
}

// TrySend attempts to put an item into the channel.
// Returns false if the channel is closed, true otherwise.
func (ch *UnboundedChan[T]) TrySend(value T) bool {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if ch.closed {
		return false
	}

	ch.buffer = append(ch.buffer, value)
	ch.notEmpty.Signal()
	ch.signal()
	return true
}

// Receive gets an item from the channel (blocks if empty).
// Returns (value, true) if an item was received.
// Returns (zero, false) if the channel was closed with no data remaining.
func (ch *UnboundedChan[T]) Receive() (T, bool) {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	for len(ch.buffer) == 0 && !ch.closed {
		ch.notEmpty.Wait()
	}

	if len(ch.buffer) == 0 {
		var zero T
		return zero, false
	}

	val := ch.buffer[0]
	ch.buffer = ch.buffer[1:]
	return val, true
}

// ReceiveWithContext gets an item from the channel or returns when ctx is done.
// It has the same closed-channel semantics as Receive.
func (ch *UnboundedChan[T]) ReceiveWithContext(ctx context.Context) (T, bool) {
	if ctx == nil {
		return ch.Receive()
	}

	for {
		ch.mutex.Lock()
		if ctx.Err() != nil {
			var zero T
			ch.mutex.Unlock()
			return zero, false
		}
		if len(ch.buffer) > 0 {
			val := ch.buffer[0]
			ch.buffer = ch.buffer[1:]
			ch.mutex.Unlock()
			return val, true
		}
		if ch.closed {
			var zero T
			ch.mutex.Unlock()
			return zero, false
		}
		ch.mutex.Unlock()

		select {
		case <-ch.notify:
		case <-ctx.Done():
			var zero T
			return zero, false
		}
	}
}

// Close marks the channel as closed
func (ch *UnboundedChan[T]) Close() {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if !ch.closed {
		ch.closed = true
		ch.notEmpty.Broadcast()
		ch.signal()
	}
}
