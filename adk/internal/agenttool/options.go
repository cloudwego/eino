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

// ForwardGate controls the internal AgentTool event-forwarding gate.
type ForwardGate struct {
	// Enabled marks the AgentTool invocation as background-capable: forwarding to
	// the caller is bounded by Until and uses a non-panicking send.
	Enabled bool
	// Until stops forwarding to the caller once closed. Nil means "never
	// backgrounded".
	Until <-chan struct{}
}

// WithForwardGate marks an AgentTool invocation as background-capable and
// forwards inner-agent events to the caller until done is closed.
func WithForwardGate(done <-chan struct{}) tool.Option {
	return tool.WrapImplSpecificOptFn(func(o *ForwardGate) {
		o.Enabled = true
		o.Until = done
	})
}
