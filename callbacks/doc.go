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
//	callbacks.AppendGlobalHandlers(myHandler) // call once, at startup
//
// Per-invocation handlers (observe one graph run):
//
//	runnable.Invoke(ctx, input, compose.WithCallbacks(myHandler))
//
// Global handlers execute before per-invocation handlers.
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
//			defer input.Close() // MUST close
//			// Handle component start with stream input
//			return ctx
//		}).
//		OnEndWithStreamOutputFn(func(ctx context.Context, info *RunInfo, output *schema.StreamReader[CallbackOutput]) context.Context {
//			defer output.Close() // MUST close
//			// Handle component end with stream output
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
//				log.Printf("Model execution started: %s", info.Name)
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
// # Common Pitfalls
//
//   - Forgetting to close stream copies: handlers for
//     OnStartWithStreamInput / OnEndWithStreamOutput receive independent
//     copies of the stream. Failing to close them leaks goroutines.
//
//   - Thread safety of AppendGlobalHandlers: call only during initialization,
//     never concurrently with graph execution.
//
//   - Stream errors are invisible to OnError: errors that occur while a
//     consumer reads from a StreamReader are not routed through OnError. Add
//     error checking inside the stream consumer directly.
package callbacks
