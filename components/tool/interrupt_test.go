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

package tool

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/internal/core"
)

func TestInterrupt(t *testing.T) {
	ctx := context.Background()

	t.Run("basic interrupt", func(t *testing.T) {
		err := Interrupt(ctx, "test info")
		assert.Error(t, err)

		var signal *core.InterruptSignal
		assert.True(t, errors.As(err, &signal))
		assert.Equal(t, "test info", signal.Info)
		assert.True(t, signal.IsRootCause)
	})
}

func TestStatefulInterrupt(t *testing.T) {
	ctx := context.Background()

	t.Run("stateful interrupt", func(t *testing.T) {
		type myState struct {
			Value int
		}
		state := &myState{Value: 42}

		err := StatefulInterrupt(ctx, "test info", state)
		assert.Error(t, err)

		var signal *core.InterruptSignal
		assert.True(t, errors.As(err, &signal))
		assert.Equal(t, "test info", signal.Info)
		assert.Equal(t, state, signal.State)
		assert.True(t, signal.IsRootCause)
	})
}

func TestCompositeInterrupt(t *testing.T) {
	ctx := context.Background()

	t.Run("no sub errors falls back to StatefulInterrupt", func(t *testing.T) {
		err := CompositeInterrupt(ctx, "composite info", "my state")
		assert.Error(t, err)

		var signal *core.InterruptSignal
		assert.True(t, errors.As(err, &signal))
		assert.Equal(t, "composite info", signal.Info)
		assert.Equal(t, "my state", signal.State)
		assert.True(t, signal.IsRootCause)
		assert.Empty(t, signal.Subs)
	})

	t.Run("with InterruptSignal sub error", func(t *testing.T) {
		subSignal, _ := core.Interrupt(ctx, "sub info", "sub state", nil)

		err := CompositeInterrupt(ctx, "composite info", "my state", subSignal)
		assert.Error(t, err)

		var signal *core.InterruptSignal
		assert.True(t, errors.As(err, &signal))
		assert.Equal(t, "composite info", signal.Info)
		assert.Equal(t, "my state", signal.State)
		assert.Len(t, signal.Subs, 1)
		assert.Equal(t, "sub info", signal.Subs[0].Info)
	})

	t.Run("with non-interrupt error returns error", func(t *testing.T) {
		nonInterruptErr := errors.New("regular error")

		err := CompositeInterrupt(ctx, "composite info", "my state", nonInterruptErr)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "composite interrupt but one of the sub error is not interrupt error")

		var signal *core.InterruptSignal
		assert.False(t, errors.As(err, &signal))
	})

	t.Run("with multiple sub errors", func(t *testing.T) {
		subSignal1, _ := core.Interrupt(ctx, "sub1 info", nil, nil)
		subSignal2, _ := core.Interrupt(ctx, "sub2 info", nil, nil)

		err := CompositeInterrupt(ctx, "composite info", nil, subSignal1, subSignal2)
		assert.Error(t, err)

		var signal *core.InterruptSignal
		assert.True(t, errors.As(err, &signal))
		assert.Len(t, signal.Subs, 2)
	})
}

func TestGetInterruptState(t *testing.T) {
	t.Run("not interrupted returns false", func(t *testing.T) {
		ctx := context.Background()
		wasInterrupted, hasState, state := GetInterruptState[string](ctx)
		assert.False(t, wasInterrupted)
		assert.False(t, hasState)
		assert.Empty(t, state)
	})
}

func TestGetResumeContext(t *testing.T) {
	t.Run("not resume target returns false", func(t *testing.T) {
		ctx := context.Background()
		isResumeTarget, hasData, data := GetResumeContext[string](ctx)
		assert.False(t, isResumeTarget)
		assert.False(t, hasData)
		assert.Empty(t, data)
	})
}
