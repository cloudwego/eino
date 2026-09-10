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

package core

import "context"

type executionGateKey struct{}

// ExecutionGate waits until a new side effect may start.
type ExecutionGate func(context.Context) error

// WithExecutionGate attaches a side-effect admission gate to ctx.
func WithExecutionGate(ctx context.Context, gate ExecutionGate) context.Context {
	if ctx == nil || gate == nil {
		return ctx
	}
	return context.WithValue(ctx, executionGateKey{}, gate)
}

// WaitExecutionGate waits for the gate attached to ctx, if any.
func WaitExecutionGate(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	gate, _ := ctx.Value(executionGateKey{}).(ExecutionGate)
	if gate == nil {
		return nil
	}
	return gate(ctx)
}
