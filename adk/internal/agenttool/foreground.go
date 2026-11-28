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

package agenttool

import (
	"context"
	"sync"
)

type foregroundExecutionKey struct{}

// ForegroundExecution carries process-local projection state from an AgentTool
// invocation to an executor running in the same process.
type ForegroundExecution[E any] struct {
	mu              sync.Mutex
	active          bool
	receivers       []EventReceiver[E]
	enableStreaming bool
}

// WithForegroundExecution attaches process-local foreground projection state.
// detach prevents future receiver calls without waiting for calls already in flight.
func WithForegroundExecution[E any](
	ctx context.Context,
	receivers []EventReceiver[E],
	enableStreaming bool,
) (context.Context, func()) {
	execution := &ForegroundExecution[E]{
		active:          true,
		receivers:       append([]EventReceiver[E](nil), receivers...),
		enableStreaming: enableStreaming,
	}
	return context.WithValue(ctx, foregroundExecutionKey{}, execution), execution.detach
}

// ForegroundExecutionFromContext returns same-process foreground projection state.
func ForegroundExecutionFromContext[E any](ctx context.Context) *ForegroundExecution[E] {
	if ctx == nil {
		return nil
	}
	execution, _ := ctx.Value(foregroundExecutionKey{}).(*ForegroundExecution[E])
	return execution
}

// EnableStreaming reports whether the launching foreground invocation streams.
func (e *ForegroundExecution[E]) EnableStreaming() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.active && e.enableStreaming
}

// Forward sends an independent event copy to each active foreground receiver.
func (e *ForegroundExecution[E]) Forward(
	event E,
	backgrounded <-chan struct{},
	clone func(E) E,
) {
	if e == nil || signalClosed(backgrounded) {
		return
	}
	e.mu.Lock()
	if !e.active || signalClosed(backgrounded) {
		e.mu.Unlock()
		return
	}
	receivers := append([]EventReceiver[E](nil), e.receivers...)
	e.mu.Unlock()
	for _, receiver := range receivers {
		forwarded := event
		if clone != nil {
			forwarded = clone(event)
		}
		receiver(forwarded)
	}
}

func (e *ForegroundExecution[E]) detach() {
	e.mu.Lock()
	e.active = false
	e.receivers = nil
	e.enableStreaming = false
	e.mu.Unlock()
}

func signalClosed(signal <-chan struct{}) bool {
	select {
	case <-signal:
		return true
	default:
		return false
	}
}
