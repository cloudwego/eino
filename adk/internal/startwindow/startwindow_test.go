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

package startwindow

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type markerKey struct{}

func TestWindowTimeoutAndCancellationCloseRelay(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		var sends int64
		parent := context.WithValue(context.Background(), markerKey{}, "parent-marker")
		parent = WithSender(parent, func(ctx context.Context, event any) error {
			require.Equal(t, "parent-marker", ctx.Value(markerKey{}))
			require.Equal(t, "before", event)
			atomic.AddInt64(&sends, 1)
			return nil
		})
		backgroundCtx, window := Open(parent)

		handled, err := TrySend(backgroundCtx, "before")
		require.True(t, handled)
		require.NoError(t, err)
		require.ErrorIs(t, window.Wait(parent, time.Millisecond), ErrWindowTimeout)
		handled, err = TrySend(backgroundCtx, "after")
		require.True(t, handled)
		require.ErrorIs(t, err, ErrWindowClosed)
		Signal(backgroundCtx)
		require.Equal(t, int64(1), atomic.LoadInt64(&sends))
	})

	t.Run("cancellation", func(t *testing.T) {
		parent := WithSender(context.Background(), func(context.Context, any) error {
			t.Fatal("send after cancellation must not be admitted")
			return nil
		})
		cancelCtx, cancel := context.WithCancel(parent)
		backgroundCtx, window := Open(cancelCtx)
		cancel()

		require.ErrorIs(t, window.Wait(cancelCtx, time.Hour), context.Canceled)
		handled, err := TrySend(backgroundCtx, "after")
		require.True(t, handled)
		require.ErrorIs(t, err, ErrWindowClosed)
		Signal(backgroundCtx)
	})
}

func TestWindowSignalTimeoutRace(t *testing.T) {
	for i := 0; i < 1000; i++ {
		parent := WithSender(context.Background(), func(context.Context, any) error {
			return nil
		})
		backgroundCtx, window := Open(parent)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			Signal(backgroundCtx)
		}()
		err := window.Wait(parent, time.Nanosecond)
		require.True(t, err == nil || errors.Is(err, ErrWindowTimeout), "unexpected result: %v", err)
		wg.Wait()

		handled, sendErr := TrySend(backgroundCtx, "after")
		require.True(t, handled)
		require.ErrorIs(t, sendErr, ErrWindowClosed)
	}
}
