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

package adk

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/compose"
)

func assertNotClosedWithin(t *testing.T, ch <-chan struct{}, d time.Duration) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal("channel was closed but should not have been")
	case <-time.After(d):
	}
}

func setupParentChild(t *testing.T) (parent, child *cancelContext, cleanup func()) {
	parent = newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	child = parent.deriveChild(ctx)
	cleanup = func() {
		child.markDone()
		cancel()
	}
	t.Cleanup(cleanup)
	return parent, child, cleanup
}

func TestDeriveChild(t *testing.T) {
	t.Run("Shallow", func(t *testing.T) {
		t.Run("DoesNotPropagateSafePoint", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.triggerCancel(CancelAfterChatModel)

			assertNotClosedWithin(t, child.cancelChan, 50*time.Millisecond)
		})

		t.Run("ImmediateDoesNotPropagate", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.triggerImmediateCancel()

			assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)
		})

		t.Run("GrandchildNoPropagation", func(t *testing.T) {
			a := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			b := a.deriveChild(ctx)
			c := b.deriveChild(ctx)
			t.Cleanup(func() {
				c.markDone()
				b.markDone()
				cancel()
			})

			a.triggerCancel(CancelAfterChatModel)

			assertNotClosedWithin(t, b.cancelChan, 50*time.Millisecond)
			assertNotClosedWithin(t, c.cancelChan, 50*time.Millisecond)
		})

		t.Run("NeverRecursive_GoroutineCleanup", func(t *testing.T) {
			runtime.GC()
			time.Sleep(50 * time.Millisecond)
			before := runtime.NumGoroutine()

			parent := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			child := parent.deriveChild(ctx)

			parent.triggerCancel(CancelAfterChatModel)
			time.Sleep(100 * time.Millisecond)

			child.markDone()
			cancel()

			time.Sleep(200 * time.Millisecond)
			runtime.GC()
			time.Sleep(50 * time.Millisecond)
			after := runtime.NumGoroutine()

			assert.InDelta(t, before, after, 5, "goroutine leak detected: before=%d after=%d", before, after)
		})
	})

	t.Run("Recursive", func(t *testing.T) {
		t.Run("PropagatesSafePoint", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.setRecursive(true)
			parent.triggerCancel(CancelAfterChatModel)

			select {
			case <-child.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatal("child did not receive cancel within 1s")
			}
			assert.True(t, child.shouldCancel())
		})

		t.Run("ImmediatePropagates", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.setRecursive(true)
			parent.triggerImmediateCancel()

			select {
			case <-child.immediateChan:
			case <-time.After(1 * time.Second):
				t.Fatal("child did not receive immediate cancel within 1s")
			}
			assert.True(t, child.isImmediateCancelled())
		})

		t.Run("GrandchildPropagation", func(t *testing.T) {
			a := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			b := a.deriveChild(ctx)
			c := b.deriveChild(ctx)
			t.Cleanup(func() {
				c.markDone()
				b.markDone()
				cancel()
			})

			a.setRecursive(true)
			a.triggerCancel(CancelAfterChatModel)

			select {
			case <-b.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatal("B did not receive cancel within 1s")
			}

			select {
			case <-c.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatal("C did not receive cancel within 1s")
			}

			assert.True(t, b.shouldCancel())
			assert.True(t, c.shouldCancel())
		})

		t.Run("SetBeforeCancel", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.setRecursive(true)

			parent.triggerCancel(CancelAfterChatModel)

			select {
			case <-child.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatal("child did not receive cancel within 1s")
			}
			assert.True(t, child.shouldCancel())
		})

		t.Run("AfterRecursiveAndCancelAlreadySet", func(t *testing.T) {
			parent := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			parent.setRecursive(true)
			parent.triggerCancel(CancelAfterChatModel)

			child := parent.deriveChild(ctx)
			t.Cleanup(func() {
				child.markDone()
				cancel()
			})

			select {
			case <-child.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatal("child did not immediately receive cancel")
			}
			assert.True(t, child.shouldCancel())
		})
	})

	t.Run("Escalation", func(t *testing.T) {
		t.Run("EscalateFromNonRecursive", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.triggerCancel(CancelAfterChatModel)

			assertNotClosedWithin(t, child.cancelChan, 50*time.Millisecond)

			parent.setRecursive(true)

			select {
			case <-child.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatal("child did not receive cancel after escalation within 1s")
			}
			assert.True(t, child.shouldCancel())
		})

		t.Run("EscalateImmediate", func(t *testing.T) {
			parent, child, _ := setupParentChild(t)

			parent.triggerImmediateCancel()

			assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)

			parent.setRecursive(true)

			select {
			case <-child.immediateChan:
			case <-time.After(1 * time.Second):
				t.Fatal("child did not receive immediate cancel after escalation within 1s")
			}
			assert.True(t, child.isImmediateCancelled())
		})
	})
}

func TestDeriveChild_Race(t *testing.T) {
	t.Run("SetRecursiveConcurrentWithCancelChan", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			parent := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			child := parent.deriveChild(ctx)

			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				parent.setRecursive(true)
			}()

			go func() {
				defer wg.Done()
				parent.triggerCancel(CancelAfterChatModel)
			}()

			wg.Wait()

			select {
			case <-child.cancelChan:
			case <-time.After(1 * time.Second):
				t.Fatalf("iteration %d: child did not receive cancel within 1s", i)
			}

			assert.True(t, child.shouldCancel())
			child.markDone()
			cancel()
		}
	})

	t.Run("ChildCompletesBeforeEscalation", func(t *testing.T) {
		parent := newCancelContext()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		child := parent.deriveChild(ctx)

		parent.triggerCancel(CancelAfterChatModel)
		time.Sleep(50 * time.Millisecond)

		child.markDone()
		time.Sleep(50 * time.Millisecond)

		parent.setRecursive(true)

		assertNotClosedWithin(t, child.cancelChan, 50*time.Millisecond)
	})

	t.Run("MultipleChildren_PartialCompletion", func(t *testing.T) {
		parent := newCancelContext()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		child1 := parent.deriveChild(ctx)
		child2 := parent.deriveChild(ctx)

		parent.triggerCancel(CancelAfterChatModel)
		time.Sleep(50 * time.Millisecond)

		child1.markDone()
		time.Sleep(50 * time.Millisecond)

		parent.setRecursive(true)

		select {
		case <-child2.cancelChan:
		case <-time.After(1 * time.Second):
			t.Fatal("running child did not receive cancel within 1s")
		}

		assert.True(t, child2.shouldCancel())
		assert.False(t, child1.shouldCancel())
		child2.markDone()
	})

	t.Run("ContextCancelConcurrentWithRecursive", func(t *testing.T) {
		done := make(chan struct{})
		go func() {
			defer close(done)

			parent := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			child := parent.deriveChild(ctx)

			parent.triggerCancel(CancelAfterChatModel)

			var wg sync.WaitGroup
			wg.Add(2)

			go func() {
				defer wg.Done()
				cancel()
			}()

			go func() {
				defer wg.Done()
				parent.setRecursive(true)
			}()

			wg.Wait()
			child.markDone()
		}()

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("deadlock detected")
		}
	})

	t.Run("ConcurrentSetRecursive", func(t *testing.T) {
		parent := newCancelContext()

		var wg sync.WaitGroup
		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				parent.setRecursive(true)
			}()
		}

		done := make(chan struct{})
		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(1 * time.Second):
			t.Fatal("deadlock or panic in concurrent setRecursive")
		}

		assert.True(t, parent.isRecursive())
	})
}

func TestGracePeriod_OnlyWhenRecursive(t *testing.T) {
	parent, _, _ := setupParentChild(t)

	var nonRecursiveOptCount int
	wrappedNonRecursive := parent.wrapGraphInterruptWithGracePeriod(func(opts ...compose.GraphInterruptOption) {
		nonRecursiveOptCount = len(opts)
	})
	wrappedNonRecursive()
	assert.Equal(t, 0, nonRecursiveOptCount)

	parent.setRecursive(true)

	var recursiveOptCount int
	wrappedRecursive := parent.wrapGraphInterruptWithGracePeriod(func(opts ...compose.GraphInterruptOption) {
		recursiveOptCount = len(opts)
	})
	wrappedRecursive()
	assert.Equal(t, 1, recursiveOptCount)
}
