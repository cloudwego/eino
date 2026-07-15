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

// Package agenttool carries ADK-internal AgentTool event-forwarding options
// shared between AgentTool and middleware plumbing.
package agenttool

import "github.com/cloudwego/eino/components/tool"

// EventReceiver is a caller-provided function that receives an event emitted by
// the AgentTool's inner agent. Receivers must be best-effort and safe to call
// after their downstream consumer has stopped.
type EventReceiver[E any] func(event E)

// EventReceiverTransform derives the receiver list for an AgentTool invocation
// from the receivers configured so far. Transforms run in tool-option order.
// The receiver list may be empty, but must not contain nil elements.
type EventReceiverTransform[E any] func(current []EventReceiver[E]) []EventReceiver[E]

type eventReceiverOptions[E any] struct {
	transforms []EventReceiverTransform[E]
}

// WithEventReceiverTransform adds an internal receiver-list transform to an
// AgentTool invocation.
func WithEventReceiverTransform[E any](transform EventReceiverTransform[E]) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *eventReceiverOptions[E]) {
		if transform != nil {
			o.transforms = append(o.transforms, transform)
		}
	})
}

// ResolveEventReceivers applies the configured transforms in option order.
func ResolveEventReceivers[E any](opts ...tool.Option) []EventReceiver[E] {
	o := tool.GetImplSpecificOptions[eventReceiverOptions[E]](nil, opts...)
	receivers := make([]EventReceiver[E], 0, len(o.transforms))
	for _, transform := range o.transforms {
		receivers = transform(receivers)
	}
	return receivers
}
