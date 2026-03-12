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

// RunInfo describes the entity that triggered a callback. Always nil-check
// before dereferencing — a component that calls OnStart without first calling
// EnsureRunInfo or InitCallbacks will leave RunInfo absent in the context.
//
// Fields:
//   - Name: business-meaningful name specified by the user. For nodes in a
//     graph this is the node name (compose.WithNodeName). For standalone
//     components it must be set explicitly via [InitCallbacks] or
//     [ReuseHandlers]; it is empty string if not set.
//   - Type: implementation identity, e.g. "OpenAI". Set by the component via
//     [components.Typer]; falls back to reflection (struct/func name) if the
//     interface is not implemented. Empty for Graph itself.
//   - Component: category constant, e.g. components.ComponentOfChatModel.
//     Fixed value "Lambda" for lambdas, "Graph"/"Chain"/"Workflow" for graphs.
//     Use this to branch on component kind without caring about implementation.
//
// Handlers should filter using RunInfo rather than assuming a fixed execution
// order — there is no guaranteed ordering between different Handlers.
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
// Each method receives the context returned by the previous timing of the
// SAME handler, which lets a single handler pass state between its OnStart
// and OnEnd calls via context.WithValue. There is NO guaranteed execution
// order between DIFFERENT handlers, and the context chain does not flow
// from one handler to the next — do not rely on handler ordering.
//
// Implement [TimingChecker] (the Needed method) on your handler so the
// framework can skip timings you have not registered; this avoids unnecessary
// stream copies and goroutine allocations on every component invocation.
//
// Stream handlers (OnStartWithStreamInput, OnEndWithStreamOutput) receive a
// [*schema.StreamReader] that has already been copied; they MUST close their
// copy after reading. If any handler's copy is not closed, the original stream
// cannot be freed, causing a goroutine/memory leak for the entire pipeline.
//
// Important: do NOT mutate the Input or Output values. All downstream nodes
// and handlers share the same pointer (direct assignment, not a deep copy).
// Mutations cause data races in concurrent graph execution.
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
