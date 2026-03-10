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

// Package callbacks provides observability hooks for component execution in Eino.
//
// Callbacks fire at five lifecycle timings around every component invocation:
//   - [TimingOnStart] / [TimingOnEnd]: non-streaming input and output.
//   - [TimingOnStartWithStreamInput] / [TimingOnEndWithStreamOutput]: streaming
//     variants — handlers receive a copy of the stream and MUST close it.
//   - [TimingOnError]: component returned a non-nil error (stream-internal
//     errors are NOT reported here).
//
// # Attaching Handlers
//
// Global handlers (observe every node in every graph):
//
//	callbacks.AppendGlobalHandlers(myHandler) // call once, at startup — NOT thread-safe
//
// Per-invocation handlers (observe one graph run):
//
//	runnable.Invoke(ctx, input, compose.WithCallbacks(myHandler))
//
// Target a specific node:
//
//	compose.WithCallbacks(myHandler).DesignateNode("nodeName")
//
// Handler inheritance: if the context passed to a graph run already carries
// handlers (e.g. from a parent graph), those handlers are inherited by the
// entire child run automatically.
//
// # Building Handlers
//
// Option 1 — [NewHandlerBuilder]: register raw functions for the timings you
// need. Input/output are untyped; use the component package's ConvCallbackInput
// helper to cast to a concrete type:
//
//	handler := callbacks.NewHandlerBuilder().
//		OnStartFn(func(ctx context.Context, info *RunInfo, input CallbackInput) context.Context {
//			// Handle component start
//			return ctx
//		}).
//		OnEndFn(func(ctx context.Context, info *RunInfo, output CallbackOutput) context.Context {
//			// Handle component end
//			return ctx
//		}).
//		OnErrorFn(func(ctx context.Context, info *RunInfo, err error) context.Context {
//			// Handle component error
//			return ctx
//		}).
//		OnStartWithStreamInputFn(func(ctx context.Context, info *RunInfo, input *schema.StreamReader[CallbackInput]) context.Context {
//			defer input.Close() // MUST close — failure causes pipeline goroutine leak
//			return ctx
//		}).
//		OnEndWithStreamOutputFn(func(ctx context.Context, info *RunInfo, output *schema.StreamReader[CallbackOutput]) context.Context {
//			defer output.Close() // MUST close
//			return ctx
//		}).
//		Build()
//
// Option 2 — utils/callbacks.NewHandlerHelper: dispatches by component type, so
// each handler function receives the concrete typed input/output directly:
//
//	handler := callbacks.NewHandlerHelper().
//		ChatModel(&model.CallbackHandler{
//			OnStart: func(ctx context.Context, info *RunInfo, input *model.CallbackInput) context.Context {
//				log.Printf("Model started: %s, messages: %d", info.Name, len(input.Messages))
//				return ctx
//			},
//		}).
//		Prompt(&prompt.CallbackHandler{
//			OnEnd: func(ctx context.Context, info *RunInfo, output *prompt.CallbackOutput) context.Context {
//				log.Printf("Prompt completed")
//				return ctx
//			},
//		}).
//		Handler()
//
// # Passing State Within a Handler
//
// The ctx returned by one timing is passed to the next timing of the SAME
// handler, enabling OnStart→OnEnd state transfer via context.WithValue:
//
//	NewHandlerBuilder().
//		OnStartFn(func(ctx context.Context, info *RunInfo, _ CallbackInput) context.Context {
//			return context.WithValue(ctx, startTimeKey{}, time.Now())
//		}).
//		OnEndFn(func(ctx context.Context, info *RunInfo, _ CallbackOutput) context.Context {
//			start := ctx.Value(startTimeKey{}).(time.Time)
//			log.Printf("duration: %v", time.Since(start))
//			return ctx
//		}).Build()
//
// Between DIFFERENT handlers there is no guaranteed execution order and no
// context chain. To share state between handlers, store it in a
// concurrency-safe variable in the outermost context instead.
//
// # Common Pitfalls
//
//   - Stream copies must be closed: when N handlers register for a streaming
//     timing, the stream is copied N+1 times (one per handler + one for
//     downstream). If any handler's copy is not closed, the original stream
//     cannot be freed and the entire pipeline leaks.
//
//   - Do NOT mutate Input/Output: all downstream nodes and handlers share the
//     same pointer. Mutations cause data races in concurrent graph execution.
//
//   - AppendGlobalHandlers is NOT thread-safe: call only during initialization,
//     never concurrently with graph execution.
//
//   - Stream errors are invisible to OnError: errors that occur while a
//     consumer reads from a StreamReader are not routed through OnError.
//
//   - RunInfo may be nil: always nil-check before dereferencing in handlers,
//     especially when a component is used standalone outside a graph without
//     InitCallbacks being called.
package callbacks
