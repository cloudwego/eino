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
	"context"

	"github.com/cloudwego/eino/components"
	"github.com/cloudwego/eino/internal/callbacks"
	"github.com/cloudwego/eino/schema"
)

// OnStart Fast inject callback input / output aspect for component developer
// e.g.
//
//	func (t *testChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (resp *schema.Message, err error) {
//		defer func() {
//			if err != nil {
//				callbacks.OnError(ctx, err)
//			}
//		}()
//
//		ctx = callbacks.OnStart(ctx, &model.CallbackInput{
//			Messages: input,
//			Tools:    nil,
//			Extra:    nil,
//		})
//
//		// do smt
//
//		ctx = callbacks.OnEnd(ctx, &model.CallbackOutput{
//			Message: resp,
//			Extra:   nil,
//		})
//
//		return resp, nil
//	}

// OnStart invokes the OnStart timing for all registered handlers in the
// context. This is called by component implementations that manage their own
// callbacks (i.e. implement [components.Checker] and return true from
// IsCallbacksEnabled). The returned context must be propagated to subsequent
// OnEnd/OnError calls so handlers can correlate start and end events.
//
// Handlers are invoked in reverse registration order (last registered = first
// called) to match the middleware wrapping convention.
//
// Example — typical usage inside a component's Generate method:
//
//	func (m *myChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
//	    ctx = callbacks.OnStart(ctx, &model.CallbackInput{Messages: input})
//	    resp, err := m.doGenerate(ctx, input, opts...)
//	    if err != nil {
//	        callbacks.OnError(ctx, err)
//	        return nil, err
//	    }
//	    callbacks.OnEnd(ctx, &model.CallbackOutput{Message: resp})
//	    return resp, nil
//	}
func OnStart[T any](ctx context.Context, input T) context.Context {
	ctx, _ = callbacks.On(ctx, input, callbacks.OnStartHandle[T], TimingOnStart, true)

	return ctx
}

// OnEnd invokes the OnEnd timing for all registered handlers. Call this after
// the component produces a successful result. Handlers run in registration
// order (first registered = first called).
//
// Do not call both OnEnd and OnError for the same invocation — OnEnd signals
// success; OnError signals failure.
func OnEnd[T any](ctx context.Context, output T) context.Context {
	ctx, _ = callbacks.On(ctx, output, callbacks.OnEndHandle[T], TimingOnEnd, false)

	return ctx
}

// OnStartWithStreamInput invokes the OnStartWithStreamInput timing. Use this
// when the component's input is itself a stream (Collect / Transform
// paradigms). The framework automatically copies the stream so each handler
// receives an independent reader; handlers MUST close their copy or the
// underlying goroutine will leak.
//
// Returns the updated context and a new StreamReader that the component should
// use going forward (the original is consumed by the framework).
func OnStartWithStreamInput[T any](ctx context.Context, input *schema.StreamReader[T]) (
	nextCtx context.Context, newStreamReader *schema.StreamReader[T]) {

	return callbacks.On(ctx, input, callbacks.OnStartWithStreamInputHandle[T], TimingOnStartWithStreamInput, true)
}

// OnEndWithStreamOutput invokes the OnEndWithStreamOutput timing. Use this
// when the component produces a streaming output (Stream / Transform
// paradigms). Like OnStartWithStreamInput, stream copies are made per
// handler; each handler must close its copy.
//
// Returns the updated context and the StreamReader the component should return
// to its caller.
func OnEndWithStreamOutput[T any](ctx context.Context, output *schema.StreamReader[T]) (
	nextCtx context.Context, newStreamReader *schema.StreamReader[T]) {

	return callbacks.On(ctx, output, callbacks.OnEndWithStreamOutputHandle[T], TimingOnEndWithStreamOutput, false)
}

// OnError invokes the OnError timing for all registered handlers. Call this
// when the component returns an error. Errors that occur mid-stream (after the
// StreamReader has been returned) are NOT routed through OnError; they surface
// as errors inside Recv.
//
// Handlers run in registration order (same as OnEnd).
func OnError(ctx context.Context, err error) context.Context {
	ctx, _ = callbacks.On(ctx, err, callbacks.OnErrorHandle, TimingOnError, false)

	return ctx
}

// EnsureRunInfo ensures the context carries a [RunInfo] for the given type and
// component kind. If the context already has a matching RunInfo, it is
// returned unchanged. Otherwise, a new callback manager is created that
// inherits the global handlers plus any handlers already in ctx.
//
// Component implementations that set IsCallbacksEnabled() = true should call
// this at the start of every public method (Generate, Stream, etc.) before
// calling [OnStart], so that the RunInfo is never missing from callbacks.
func EnsureRunInfo(ctx context.Context, typ string, comp components.Component) context.Context {
	return callbacks.EnsureRunInfo(ctx, typ, comp)
}

// ReuseHandlers creates a new context that inherits all handlers already
// present in ctx and sets a new RunInfo. Global handlers are added if ctx
// carries none yet.
//
// Use this when a component calls another component internally and wants the
// inner component's callbacks to share the same set of handlers as the outer
// component, but with the inner component's own identity in RunInfo:
//
//	innerCtx := callbacks.ReuseHandlers(ctx, &callbacks.RunInfo{
//	    Type:      "InnerChatModel",
//	    Component: components.ComponentOfChatModel,
//	    Name:      "inner-cm",
//	})
func ReuseHandlers(ctx context.Context, info *RunInfo) context.Context {
	return callbacks.ReuseHandlers(ctx, info)
}

// InitCallbacks creates a new context with the given RunInfo and handlers,
// completely replacing any RunInfo and handlers already in ctx.
//
// Use this when running a component standalone outside a Graph — the Graph
// normally manages RunInfo injection automatically, but standalone callers must
// set it up themselves:
//
//	ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
//	    Type:      myModel.GetType(),
//	    Component: components.ComponentOfChatModel,
//	    Name:      "my-model",
//	}, myHandler)
func InitCallbacks(ctx context.Context, info *RunInfo, handlers ...Handler) context.Context {
	return callbacks.InitCallbacks(ctx, info, handlers...)
}
