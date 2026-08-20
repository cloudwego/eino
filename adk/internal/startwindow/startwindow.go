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

// Package startwindow coordinates the bounded parent-event relay used while a
// background managed tool reaches its Start boundary.
package startwindow

import (
	"context"
	"errors"
	"sync"
	"time"
)

// ErrWindowClosed is returned when a sender-backed start window has closed.
var ErrWindowClosed = errors.New("adk: background start event window is closed")

// ErrWindowTimeout is returned by Window.Wait when its positive timeout wins.
var ErrWindowTimeout = errors.New("adk: background start event window timed out")

// EventSender emits one event through the parent execution context retained by
// an open start window.
type EventSender func(parentCtx context.Context, event any) error

type senderContextKey struct{}
type windowContextKey struct{}

// Window represents one bounded explicit-background Start synchronization
// window. It retains the parent context only while the window is open.
type Window struct {
	mu        sync.Mutex
	done      chan struct{}
	closed    bool
	reason    error
	hadSender bool
	sender    EventSender
	parentCtx context.Context
}

// WithSender installs a parent event sender capability in ctx.
func WithSender(ctx context.Context, sender EventSender) context.Context {
	if ctx == nil || sender == nil {
		return ctx
	}
	return context.WithValue(ctx, senderContextKey{}, sender)
}

// Open creates a Background-rooted context carrying one start window. Parent
// context values are not exposed through the returned background context.
func Open(parentCtx context.Context) (context.Context, *Window) {
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	sender, _ := parentCtx.Value(senderContextKey{}).(EventSender)
	window := &Window{
		done:      make(chan struct{}),
		hadSender: sender != nil,
		sender:    sender,
	}
	if sender != nil {
		window.parentCtx = parentCtx
	}
	return context.WithValue(context.Background(), windowContextKey{}, window), window
}

// Signal closes the start window in ctx, if present.
func Signal(ctx context.Context) {
	if window := fromContext(ctx); window != nil {
		_ = window.close(nil)
	}
}

// Wait blocks until the start boundary is signaled, parentCtx is canceled, or
// timeout expires. A non-positive timeout disables the timer.
func (w *Window) Wait(parentCtx context.Context, timeout time.Duration) error {
	if w == nil {
		return nil
	}
	if parentCtx == nil {
		parentCtx = context.Background()
	}
	var timer *time.Timer
	var timeoutC <-chan time.Time
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutC = timer.C
	}
	if timer != nil {
		defer timer.Stop()
	}
	select {
	case <-w.done:
		return w.closeReason()
	case <-parentCtx.Done():
		return w.close(parentCtx.Err())
	case <-timeoutC:
		return w.close(ErrWindowTimeout)
	}
}

// TrySend sends event through the open sender-backed window in ctx. The send
// is admitted atomically with the open check, but runs outside the window lock.
func TrySend(ctx context.Context, event any) (bool, error) {
	window := fromContext(ctx)
	if window == nil {
		return false, nil
	}
	return window.trySend(event)
}

func fromContext(ctx context.Context) *Window {
	if ctx == nil {
		return nil
	}
	window, _ := ctx.Value(windowContextKey{}).(*Window)
	return window
}

func (w *Window) trySend(event any) (bool, error) {
	w.mu.Lock()
	if w.closed {
		hadSender := w.hadSender
		w.mu.Unlock()
		if hadSender {
			return true, ErrWindowClosed
		}
		return false, nil
	}
	sender := w.sender
	parentCtx := w.parentCtx
	w.mu.Unlock()
	if sender == nil {
		return false, nil
	}
	return true, sender(parentCtx, event)
}

func (w *Window) close(reason error) error {
	w.mu.Lock()
	if w.closed {
		existing := w.reason
		w.mu.Unlock()
		return existing
	}
	w.closed = true
	w.reason = reason
	w.sender = nil
	w.parentCtx = nil
	close(w.done)
	w.mu.Unlock()
	return reason
}

func (w *Window) closeReason() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.reason
}
