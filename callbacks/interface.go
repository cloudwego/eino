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
	"github.com/cloudwego/eino/internal/callbacks"
)

// RunInfo contains metadata about the component invocation that triggered a
// callback. It is provided to every callback hook so handlers can filter,
// route, or annotate telemetry based on which component is running.
//
// Fields:
//   - Name: human-readable display name (set via compose.WithNodeName); not
//     guaranteed to be unique across the graph.
//   - Type: value returned by the component's GetType() method —
//     e.g. "OpenAIChatModel". Use this to distinguish implementations.
//   - Component: the component category constant from [components.Component] —
//     e.g. components.ComponentOfChatModel. Use this to distinguish component
//     kinds without caring about the specific implementation.
//
// Pitfall: RunInfo may be nil if a component implementation calls one of
// the OnStart/OnEnd helpers (from [callbacks.EnsureRunInfo]) without first
// ensuring the RunInfo is set. Always nil-check inside handlers.
type RunInfo = callbacks.RunInfo

// CallbackInput is the value passed to OnStart and OnStartWithStreamInput
// handlers. The concrete type is defined by the component — for example,
// ChatModel callbacks carry *model.CallbackInput. Use the component package's
// ConvCallbackInput helper (e.g. model.ConvCallbackInput) to cast safely; it
// returns nil if the type does not match, so you can ignore irrelevant
// component types:
//
//	modelInput := model.ConvCallbackInput(in)
//	if modelInput == nil {
//	    return ctx // not a model invocation, skip
//	}
//	log.Printf("prompt: %v", modelInput.Messages)
type CallbackInput = callbacks.CallbackInput

// CallbackOutput is the value passed to OnEnd and OnEndWithStreamOutput
// handlers. Like CallbackInput, the concrete type is component-defined.
// Use the component package's ConvCallbackOutput helper to cast safely.
type CallbackOutput = callbacks.CallbackOutput

// Handler is the unified callback handler interface. Implement all five
// methods (OnStart, OnEnd, OnError, OnStartWithStreamInput,
// OnEndWithStreamOutput) or use [NewHandlerBuilder] to set only the timings
// you care about.
//
// Implement [TimingChecker] (the Needed method) on your handler so the
// framework can skip timings you have not registered; this avoids unnecessary
// stream copies and goroutine allocations on every component invocation.
//
// Stream handlers (OnStartWithStreamInput, OnEndWithStreamOutput) receive a
// [*schema.StreamReader] that has already been copied; they must close their
// copy after reading, or the underlying goroutine will leak.
type Handler = callbacks.Handler

// InitCallbackHandlers sets the global callback handlers.
// It should be called BEFORE any callback handler by user.
// It's useful when you want to inject some basic callbacks to all nodes.
// Deprecated: Use AppendGlobalHandlers instead.
func InitCallbackHandlers(handlers []Handler) {
	callbacks.GlobalHandlers = handlers
}

// AppendGlobalHandlers appends handlers to the process-wide list of callback
// handlers. Global handlers run before per-invocation handlers provided via
// compose.WithCallbacks, giving them higher priority for instrumentation that
// must observe every component invocation (e.g. distributed tracing, metrics).
//
// This function is NOT thread-safe. Call it once during program initialization
// (e.g. in main or TestMain), before any graph executions begin.
// Calling it concurrently with ongoing graph executions leads to data races.
func AppendGlobalHandlers(handlers ...Handler) {
	callbacks.GlobalHandlers = append(callbacks.GlobalHandlers, handlers...)
}

// CallbackTiming enumerates the lifecycle moments at which a callback handler
// is invoked. Implement [TimingChecker] on your handler and return false for
// timings you do not handle, so the framework skips the overhead of stream
// copying and goroutine spawning for those timings.
type CallbackTiming = callbacks.CallbackTiming

// Callback timing constants.
const (
	// TimingOnStart fires just before the component begins processing.
	// Receives a fully-formed input value (non-streaming).
	TimingOnStart CallbackTiming = iota
	// TimingOnEnd fires after the component returns a result successfully.
	// Receives the output value. Only fires on success — not on error.
	TimingOnEnd
	// TimingOnError fires when the component returns a non-nil error.
	// Stream errors (mid-stream panics) are NOT reported here; they surface
	// as errors inside the stream reader.
	TimingOnError
	// TimingOnStartWithStreamInput fires when the component receives a
	// streaming input (Collect / Transform paradigms). The handler receives a
	// copy of the input stream and must close it after reading.
	TimingOnStartWithStreamInput
	// TimingOnEndWithStreamOutput fires after the component returns a
	// streaming output (Stream / Transform paradigms). The handler receives a
	// copy of the output stream and must close it after reading. This is
	// typically where you implement streaming metrics or logging.
	TimingOnEndWithStreamOutput
)

// TimingChecker is an optional interface for [Handler] implementations.
// When a handler implements Needed, the framework calls it before each
// component invocation to decide whether to set up callback infrastructure
// (stream copying, goroutine allocation) for that timing. Returning false
// avoids unnecessary overhead.
//
// Handlers built with [NewHandlerBuilder] or
// utils/callbacks.NewHandlerHelper automatically implement TimingChecker
// based on which callback functions were set.
type TimingChecker = callbacks.TimingChecker
