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

package adk

import "sync"

type turnBuffer[T any] struct {
	buf      []T
	mu       sync.Mutex
	notEmpty *sync.Cond
	closed   bool
	woken    bool
}

func newTurnBuffer[T any]() *turnBuffer[T] {
	tb := &turnBuffer[T]{}
	tb.notEmpty = sync.NewCond(&tb.mu)
	return tb
}

func (tb *turnBuffer[T]) Send(value T) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tb.closed {
		panic("turnBuffer: send on closed buffer")
	}

	tb.buf = append(tb.buf, value)
	tb.notEmpty.Signal()
}

func (tb *turnBuffer[T]) TrySend(value T) bool {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if tb.closed {
		return false
	}

	tb.buf = append(tb.buf, value)
	tb.notEmpty.Signal()
	return true
}

func (tb *turnBuffer[T]) Receive() (T, bool) {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	for len(tb.buf) == 0 && !tb.closed && !tb.woken {
		tb.notEmpty.Wait()
	}

	tb.woken = false

	if len(tb.buf) == 0 {
		var zero T
		return zero, false
	}

	val := tb.buf[0]
	tb.buf = tb.buf[1:]
	return val, true
}

func (tb *turnBuffer[T]) Close() {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if !tb.closed {
		tb.closed = true
		tb.notEmpty.Broadcast()
	}
}

func (tb *turnBuffer[T]) TakeAll() []T {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	if len(tb.buf) == 0 {
		return nil
	}

	values := tb.buf
	tb.buf = nil
	return values
}

func (tb *turnBuffer[T]) PushFront(values []T) {
	if len(values) == 0 {
		return
	}

	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.buf = append(append([]T{}, values...), tb.buf...)
	tb.notEmpty.Signal()
}

func (tb *turnBuffer[T]) Wakeup() {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.woken = true
	tb.notEmpty.Broadcast()
}

func (tb *turnBuffer[T]) ClearWakeup() {
	tb.mu.Lock()
	defer tb.mu.Unlock()

	tb.woken = false
}
