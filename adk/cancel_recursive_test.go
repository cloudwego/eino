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

	"github.com/cloudwego/eino/schema"
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
	child = parent.deriveAgentToolCancelContext(ctx)
	cleanup = func() {
		child.markDone()
		cancel()
	}
	t.Cleanup(cleanup)
	return parent, child, cleanup
}

func setupAbortOnlyChild(t *testing.T) (parent, child *cancelContext, cleanup func()) {
	parent = newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	child = deriveAbortOnlyCancelContext(ctx, parent)
	cleanup = func() {
		child.markDone()
		cancel()
	}
	t.Cleanup(cleanup)
	return parent, child, cleanup
}

func TestDeriveAgentToolCancelContext(t *testing.T) {
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

			b := a.deriveAgentToolCancelContext(ctx)
			c := b.deriveAgentToolCancelContext(ctx)
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

			child := parent.deriveAgentToolCancelContext(ctx)

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

			b := a.deriveAgentToolCancelContext(ctx)
			c := b.deriveAgentToolCancelContext(ctx)
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

			child := parent.deriveAgentToolCancelContext(ctx)
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

func TestDeriveAbortOnlyCancelContext(t *testing.T) {
	t.Run("SafePointDoesNotPropagate", func(t *testing.T) {
		parent, child, _ := setupAbortOnlyChild(t)

		parent.setRecursive(true)
		parent.triggerCancel(CancelAfterChatModel)

		assertNotClosedWithin(t, child.cancelChan, 50*time.Millisecond)
		assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)
	})

	t.Run("RecursiveImmediatePropagates", func(t *testing.T) {
		parent, child, _ := setupAbortOnlyChild(t)

		parent.setRecursive(true)
		parent.triggerImmediateCancel()

		select {
		case <-child.immediateChan:
		case <-time.After(time.Second):
			t.Fatal("abort-only child did not receive immediate cancel")
		}
		assert.True(t, child.isImmediateCancelled())
	})

	t.Run("LateRecursiveImmediateEscalationPropagates", func(t *testing.T) {
		parent, child, _ := setupAbortOnlyChild(t)

		parent.triggerImmediateCancel()
		assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)

		parent.setRecursive(true)

		select {
		case <-child.immediateChan:
		case <-time.After(time.Second):
			t.Fatal("abort-only child did not receive late recursive immediate cancel")
		}
	})

	t.Run("ChildCompletionPreventsLaterPropagation", func(t *testing.T) {
		parent, child, _ := setupAbortOnlyChild(t)

		parent.triggerImmediateCancel()
		time.Sleep(50 * time.Millisecond)
		child.markDone()
		parent.setRecursive(true)

		assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)
	})

	t.Run("ParentImmediateThenContextDoneExits", func(t *testing.T) {
		parent := newCancelContext()
		ctx, cancel := context.WithCancel(context.Background())
		child := deriveAbortOnlyCancelContext(ctx, parent)
		t.Cleanup(func() {
			child.markDone()
			cancel()
		})

		parent.triggerImmediateCancel()
		assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)

		cancel()
		assertNotClosedWithin(t, child.immediateChan, 50*time.Millisecond)
	})

	t.Run("AgentToolDescendantStopsAtAbortOnlyBoundary", func(t *testing.T) {
		root := newCancelContext()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		abortOnly := deriveAbortOnlyCancelContext(ctx, root)
		agentToolChild := abortOnly.deriveAgentToolCancelContext(ctx)
		t.Cleanup(func() {
			agentToolChild.markDone()
			abortOnly.markDone()
		})

		agentToolChild.markAgentToolDescendant()

		assert.True(t, agentToolChild.hasAgentToolDescendant())
		assert.True(t, abortOnly.hasAgentToolDescendant())
		assert.False(t, root.hasAgentToolDescendant())
	})
}

func TestWithAbortOnlyCancelContext(t *testing.T) {
	cc := newCancelContext()
	cc.abortOnly = true

	ctx, cancel := withAbortOnlyCancelContext(context.Background(), cc)
	defer cancel()

	cc.markDone()

	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("abort-only context was not canceled when cancel context completed")
	}
}

func TestCheckPreExecCancel(t *testing.T) {
	t.Run("AbortOnlyImmediateTerminatesWithoutEvent", func(t *testing.T) {
		cc := newCancelContext()
		cc.abortOnly = true
		cc.triggerImmediateCancel()

		iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.Message]]()

		assert.True(t, checkPreExecCancel(cc, gen))
		select {
		case <-cc.doneChan:
		case <-time.After(time.Second):
			t.Fatal("abort-only pre-exec cancel did not mark context done")
		}

		gen.Close()
		_, ok := iter.Next()
		assert.False(t, ok)
	})

	t.Run("AlreadyHandledCancelTerminatesWithoutDuplicateEvent", func(t *testing.T) {
		cc := newCancelContext()
		cc.triggerImmediateCancel()
		assert.True(t, cc.markCancelHandled())

		iter, gen := NewAsyncIteratorPair[*TypedAgentEvent[*schema.Message]]()

		assert.True(t, checkPreExecCancel(cc, gen))

		gen.Close()
		_, ok := iter.Next()
		assert.False(t, ok)
	})
}

func TestDeriveAgentToolCancelContext_Race(t *testing.T) {
	t.Run("SetRecursiveConcurrentWithCancelChan", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			parent := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			child := parent.deriveAgentToolCancelContext(ctx)

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

		child := parent.deriveAgentToolCancelContext(ctx)

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

		child1 := parent.deriveAgentToolCancelContext(ctx)
		child2 := parent.deriveAgentToolCancelContext(ctx)

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

			child := parent.deriveAgentToolCancelContext(ctx)

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
