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

package agenttool

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestForegroundExecutionProjectsIndependentCopiesUntilDetached(t *testing.T) {
	type event struct{ value string }
	var received []*event
	ctx, detach := WithForegroundExecution(
		context.Background(),
		[]EventReceiver[*event]{
			func(e *event) {
				e.value = "first"
				received = append(received, e)
			},
			func(e *event) {
				received = append(received, e)
			},
		},
		true,
	)
	execution := ForegroundExecutionFromContext[*event](ctx)
	require.NotNil(t, execution)
	assert.True(t, execution.EnableStreaming())

	backgrounded := make(chan struct{})
	source := &event{value: "original"}
	execution.Forward(source, backgrounded, func(e *event) *event {
		copy := *e
		return &copy
	})
	require.Len(t, received, 2)
	assert.Equal(t, "first", received[0].value)
	assert.Equal(t, "original", received[1].value)
	assert.Equal(t, "original", source.value)

	detach()
	assert.False(t, execution.EnableStreaming())
	execution.Forward(source, backgrounded, nil)
	assert.Len(t, received, 2)
}

func TestForegroundExecutionStopsProjectionAfterBackgrounding(t *testing.T) {
	var calls int
	ctx, _ := WithForegroundExecution(
		context.Background(),
		[]EventReceiver[int]{func(int) { calls++ }},
		false,
	)
	execution := ForegroundExecutionFromContext[int](ctx)
	backgrounded := make(chan struct{})
	close(backgrounded)
	execution.Forward(1, backgrounded, nil)
	assert.Zero(t, calls)
}
