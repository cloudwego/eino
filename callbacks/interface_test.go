/*
 * Copyright 2025 CloudWeGo Authors
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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/callbacks"
)

func TestAppendGlobalHandlers(t *testing.T) {
	// Clear global handlers before test
	callbacks.GlobalHandlers = nil

	// Create test handlers
	handler1 := NewHandlerBuilder().
		OnStartFn(func(ctx context.Context, info *RunInfo, input CallbackInput) context.Context {
			return ctx
		}).Build()
	handler2 := NewHandlerBuilder().
		OnEndFn(func(ctx context.Context, info *RunInfo, output CallbackOutput) context.Context {
			return ctx
		}).Build()

	// Test appending first handler
	AppendGlobalHandlers(handler1)
	assert.Equal(t, 1, len(callbacks.GlobalHandlers))
	assert.Contains(t, callbacks.GlobalHandlers, handler1)

	// Test appending second handler
	AppendGlobalHandlers(handler2)
	assert.Equal(t, 2, len(callbacks.GlobalHandlers))
	assert.Contains(t, callbacks.GlobalHandlers, handler1)
	assert.Contains(t, callbacks.GlobalHandlers, handler2)

	// Test appending nil handler
	AppendGlobalHandlers([]Handler{}...)
	assert.Equal(t, 2, len(callbacks.GlobalHandlers))
}
