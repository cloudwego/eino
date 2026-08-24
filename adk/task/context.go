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

package task

import "context"

// ExecutionContext is the authority inherited by nested task creation.
type ExecutionContext struct {
	TaskID        string
	Mode          Mode
	OwnerEpoch    int64
	Attempt       int64
	RootSessionID string
}

type executionContextKey struct{}

// WithExecutionContext attaches logical task authority to ctx.
func WithExecutionContext(ctx context.Context, execution ExecutionContext) context.Context {
	return context.WithValue(ctx, executionContextKey{}, execution)
}

// ExecutionContextFromContext returns the current logical task authority.
func ExecutionContextFromContext(ctx context.Context) (ExecutionContext, bool) {
	if ctx == nil {
		return ExecutionContext{}, false
	}
	execution, ok := ctx.Value(executionContextKey{}).(ExecutionContext)
	return execution, ok && execution.TaskID != ""
}
