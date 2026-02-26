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

import "sync"

// UnboundedChan represents a channel with unlimited capacity
type UnboundedChan[T any] struct {
	buffer   []T        // Internal buffer to store data
	mutex    sync.Mutex // Mutex to protect buffer access
	notEmpty *sync.Cond // Condition variable to wait for data
	closed   bool       // Indicates if the channel has been closed
}

// NewUnboundedChan initializes and returns an UnboundedChan
func NewUnboundedChan[T any]() *UnboundedChan[T] {
	ch := &UnboundedChan[T]{}
	ch.notEmpty = sync.NewCond(&ch.mutex)
	return ch
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
	return true
}

// Receive gets an item from the channel (blocks if empty)
func (ch *UnboundedChan[T]) Receive() (T, bool) {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	for len(ch.buffer) == 0 && !ch.closed {
		ch.notEmpty.Wait() // Wait until data is available
	}

	if len(ch.buffer) == 0 {
		// Channel is closed and empty
		var zero T
		return zero, false
	}

	val := ch.buffer[0]
	ch.buffer = ch.buffer[1:]
	return val, true
}

// Close marks the channel as closed
func (ch *UnboundedChan[T]) Close() {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if !ch.closed {
		ch.closed = true
		ch.notEmpty.Broadcast() // Wake up all waiting goroutines
	}
}

// TakeAll removes and returns all values from the channel atomically.
// Returns nil if the channel is empty.
func (ch *UnboundedChan[T]) TakeAll() []T {
	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	if len(ch.buffer) == 0 {
		return nil
	}

	values := ch.buffer
	ch.buffer = nil
	return values
}

// PushFront adds values to the front of the channel.
// This is useful for recovering values that need to be reprocessed.
// Does nothing if values is empty.
func (ch *UnboundedChan[T]) PushFront(values []T) {
	if len(values) == 0 {
		return
	}

	ch.mutex.Lock()
	defer ch.mutex.Unlock()

	ch.buffer = append(values, ch.buffer...)
	ch.notEmpty.Signal()
}
