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

// RunInfo contains information about the running component.
type RunInfo = callbacks.RunInfo

// CallbackInput is the input of the callback.
// the type of input is defined by the component.
// using type Assert or convert func to convert the input to the right type you want.
// e.g.
//
//		CallbackInput in components/model/interface.go is:
//		type CallbackInput struct {
//			Messages []*schema.Message
//			Config   *Config
//			Extra map[string]any
//		}
//
//	 and provide a func of model.ConvCallbackInput() to convert CallbackInput to *model.CallbackInput
//	 in callback handler, you can use the following code to get the input:
//
//		modelCallbackInput := model.ConvCallbackInput(in)
//		if modelCallbackInput == nil {
//			// is not a model callback input, just ignore it
//			return
//		}
type CallbackInput = callbacks.CallbackInput

// CallbackOutput is the unified callback output alias used by Eino.
type CallbackOutput = callbacks.CallbackOutput

// Handler is the unified callback handler alias used by Eino.
type Handler = callbacks.Handler

// InitCallbackHandlers sets the global callback handlers.
// It should be called BEFORE any callback handler by user.
// It's useful when you want to inject some basic callbacks to all nodes.
// Deprecated: Use AppendGlobalHandlers instead.
func InitCallbackHandlers(handlers []Handler) {
	callbacks.GlobalHandlers = handlers
}

// AppendGlobalHandlers appends the given handlers to the global callback handlers.
// This is the preferred way to add global callback handlers as it preserves existing handlers.
// The global callback handlers will be executed for all nodes BEFORE user-specific handlers in CallOption.
// Note: This function is not thread-safe and should only be called during process initialization.
func AppendGlobalHandlers(handlers ...Handler) {
	callbacks.GlobalHandlers = append(callbacks.GlobalHandlers, handlers...)
}

// CallbackTiming enumerates all the timing of callback aspects.
type CallbackTiming = callbacks.CallbackTiming

// CallbackTiming values enumerate the lifecycle moments when handlers run.
const (
	TimingOnStart CallbackTiming = iota
	TimingOnEnd
	TimingOnError
	TimingOnStartWithStreamInput
	TimingOnEndWithStreamOutput
)

// TimingChecker checks if the handler is needed for the given callback aspect timing.
// It's recommended for callback handlers to implement this interface, but not mandatory.
// If a callback handler is created by using callbacks.HandlerHelper or handlerBuilder, then this interface is automatically implemented.
// Eino's callback mechanism will try to use this interface to determine whether any handlers are needed for the given timing.
// Also, the callback handler that is not needed for that timing will be skipped.
type TimingChecker = callbacks.TimingChecker
