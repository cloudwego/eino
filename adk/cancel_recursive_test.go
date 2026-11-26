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
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func TestRecursiveImmediateInterrupt_GraceAndGraphRegistration(t *testing.T) {
	parent, child, _ := setupParentChild(t)
	abortCtx, abortCancel := context.WithCancel(context.Background())
	t.Cleanup(abortCancel)
	abortOnly := deriveAbortOnlyCancelContext(abortCtx, parent)
	t.Cleanup(abortOnly.markDone)
	parent.markCheckpointAwareDescendant()
	parent.setRecursive(true)

	var calls int32
	newInterrupt := func(checkOrder bool) func(...compose.GraphInterruptOption) {
		var called int32
		return func(...compose.GraphInterruptOption) {
			if atomic.AddInt32(&called, 1) != 1 {
				t.Error("graph interrupt fired more than once")
			}
			if checkOrder && parent.isImmediateCancelled() {
				t.Error("local stream teardown preceded its graph interrupt")
			}
			atomic.AddInt32(&calls, 1)
		}
	}
	forceStarted := make(chan struct{})
	continueForce := make(chan struct{})
	var releaseOnce sync.Once
	releaseForce := func() { releaseOnce.Do(func() { close(continueForce) }) }
	t.Cleanup(releaseForce)
	firstInterrupt := newInterrupt(true)
	parent.setGraphInterruptFunc(func(opts ...compose.GraphInterruptOption) {
		close(forceStarted)
		<-continueForce
		firstInterrupt(opts...)
	})

	finished := make(chan struct{})
	go func() {
		defer close(finished)
		parent.triggerImmediateCancel()
	}()
	select {
	case <-parent.immediateRequestChan:
	case <-time.After(time.Second):
		t.Fatal("descendant cancellation was not requested")
	}
	select {
	case <-child.immediateChan:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("checkpoint child did not receive cancellation during the grace period")
	}
	select {
	case <-abortOnly.immediateChan:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("abort-only child did not receive cancellation during the grace period")
	}
	require.False(t, parent.isImmediateCancelled())

	const registrations = 16
	var wg sync.WaitGroup
	for i := 0; i < registrations; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			parent.setGraphInterruptFunc(newInterrupt(true))
			assert.False(t, parent.sendImmediateInterrupt(), "duplicate request must not restart the grace period")
		}()
	}
	registered := make(chan struct{})
	go func() {
		wg.Wait()
		close(registered)
	}()
	select {
	case <-registered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("graph registration or repeated cancellation blocked during the grace period")
	}
	require.Equal(t, int32(0), atomic.LoadInt32(&calls), "new graphs must not fire before the grace period expires")

	// Park the force phase inside its first graph interrupt, then register
	// more graphs while that phase is in progress. They must receive exactly
	// one retroactive signal after the existing graphs have been interrupted.
	select {
	case <-forceStarted:
	case <-time.After(2 * defaultCancelImmediateGracePeriod):
		t.Fatal("graph interrupt force phase did not start")
	}
	atForceStarted := make(chan struct{}, registrations)
	var forceWG sync.WaitGroup
	for i := 0; i < registrations; i++ {
		forceWG.Add(1)
		go func() {
			defer forceWG.Done()
			atForceStarted <- struct{}{}
			parent.setGraphInterruptFunc(newInterrupt(false))
			assert.False(t, parent.sendImmediateInterrupt())
		}()
	}
	for i := 0; i < registrations; i++ {
		select {
		case <-atForceStarted:
		case <-time.After(time.Second):
			t.Fatal("concurrent graph registrar did not start")
		}
	}
	registeredAtForce := make(chan struct{})
	go func() {
		forceWG.Wait()
		close(registeredAtForce)
	}()
	assertNotClosedWithin(t, registeredAtForce, 20*time.Millisecond)
	require.Equal(t, int32(0), atomic.LoadInt32(&calls), "registration must not race past the graph interrupt in progress")
	releaseForce()

	select {
	case <-finished:
	case <-time.After(2 * defaultCancelImmediateGracePeriod):
		t.Fatal("recursive cancellation exceeded its bounded grace period")
	}
	select {
	case <-registeredAtForce:
	case <-time.After(time.Second):
		t.Fatal("graph registration did not finish after the force phase")
	}
	require.True(t, parent.isImmediateCancelled())
	require.Equal(t, int32(2*registrations+1), atomic.LoadInt32(&calls))

	var lateCalls int32
	parent.setGraphInterruptFunc(func(...compose.GraphInterruptOption) {
		atomic.AddInt32(&lateCalls, 1)
	})
	require.Equal(t, int32(1), atomic.LoadInt32(&lateCalls), "registration after the force phase must fire retroactively")
	require.False(t, parent.sendImmediateInterrupt())
	require.Equal(t, int32(1), atomic.LoadInt32(&lateCalls))
}

func TestRecursiveImmediateInterrupt_CompletionDuringGraceDoesNotForce(t *testing.T) {
	parent := newCancelContext()
	parent.markCheckpointAwareDescendant()
	parent.setRecursive(true)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		parent.triggerImmediateCancel()
	}()
	select {
	case <-parent.immediateRequestChan:
	case <-time.After(time.Second):
		t.Fatal("descendant cancellation was not requested")
	}

	var calls int32
	registered := make(chan struct{})
	go func() {
		defer close(registered)
		parent.setGraphInterruptFunc(func(...compose.GraphInterruptOption) {
			atomic.AddInt32(&calls, 1)
		})
	}()
	select {
	case <-registered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("graph registration blocked during the grace period")
	}
	// A normal completion or business error remains a non-cancel outcome.
	parent.markDone()
	select {
	case <-finished:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("completion did not end the grace period")
	}
	require.Equal(t, stateDone, atomic.LoadInt32(&parent.state))
	require.Equal(t, int32(0), atomic.LoadInt32(&calls))
	require.False(t, parent.isImmediateCancelled(), "local teardown requires a graph interrupt")
}

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
	child = parent.deriveCheckpointAwareCancelContext(ctx)
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

func TestDeriveCheckpointAwareCancelContext(t *testing.T) {
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

			b := a.deriveCheckpointAwareCancelContext(ctx)
			c := b.deriveCheckpointAwareCancelContext(ctx)
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

			child := parent.deriveCheckpointAwareCancelContext(ctx)

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

			b := a.deriveCheckpointAwareCancelContext(ctx)
			c := b.deriveCheckpointAwareCancelContext(ctx)
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

			child := parent.deriveCheckpointAwareCancelContext(ctx)
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
	t.Run("NilParentReturnsNil", func(t *testing.T) {
		assert.Nil(t, deriveAbortOnlyCancelContext(context.Background(), nil))
	})

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

	t.Run("CheckpointAwareDescendantStopsAtAbortOnlyBoundary", func(t *testing.T) {
		root := newCancelContext()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		abortOnly := deriveAbortOnlyCancelContext(ctx, root)
		checkpointAwareScope := abortOnly.deriveCheckpointAwareCancelContext(ctx)
		t.Cleanup(func() {
			checkpointAwareScope.markDone()
			abortOnly.markDone()
		})

		checkpointAwareScope.markCheckpointAwareDescendant()

		assert.True(t, checkpointAwareScope.hasCheckpointAwareDescendant())
		assert.False(t, abortOnly.hasCheckpointAwareDescendant())
		assert.False(t, root.hasCheckpointAwareDescendant())
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

func TestDeriveCheckpointAwareCancelContext_Race(t *testing.T) {
	t.Run("SetRecursiveConcurrentWithCancelChan", func(t *testing.T) {
		for i := 0; i < 100; i++ {
			parent := newCancelContext()
			ctx, cancel := context.WithCancel(context.Background())

			child := parent.deriveCheckpointAwareCancelContext(ctx)

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

		child := parent.deriveCheckpointAwareCancelContext(ctx)

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

		child1 := parent.deriveCheckpointAwareCancelContext(ctx)
		child2 := parent.deriveCheckpointAwareCancelContext(ctx)

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

			child := parent.deriveCheckpointAwareCancelContext(ctx)

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

func TestAppendCancelContextOption_CopiesInputSlice(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	child := parent.deriveCheckpointAwareCancelContext(ctx)
	t.Cleanup(child.markDone)

	raw := make([]AgentRunOption, 1, 2)
	raw[0] = WrapImplSpecificOptFn(func(o *options) {
		o.skipTransferMessages = true
	})
	opts := raw[:1]

	childOpts := appendCancelContextOption(opts, child)

	requireLen := func(name string, got, want int) {
		t.Helper()
		if got != want {
			t.Fatalf("%s length = %d, want %d", name, got, want)
		}
	}
	requireLen("opts", len(opts), 1)
	requireLen("childOpts", len(childOpts), 2)

	originalCommon := getCommonOptions(nil, raw[:2]...)
	if originalCommon.cancelCtx != nil {
		t.Fatal("appendCancelContextOption reused the caller's backing array")
	}

	childCommon := getCommonOptions(nil, childOpts...)
	if childCommon.cancelCtx != child {
		t.Fatal("sub-agent opts did not receive the requested cancel context")
	}
}

func TestDeriveCheckpointAwareSubAgentCancelContext(t *testing.T) {
	t.Run("CheckpointAwareParent", func(t *testing.T) {
		parent := newCancelContext()
		baseCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		ctx := withCancelContext(baseCtx, parent)

		child := deriveCheckpointAwareSubAgentCancelContext(ctx, nil)
		if child != nil {
			t.Cleanup(child.markDone)
		}

		if child == nil {
			t.Fatal("sub-agent cancel context was not derived")
		}
		if child == parent {
			t.Fatal("sub-agent cancel context reused the parent cancel context")
		}
		if child.parent != parent || child.abortOnly {
			t.Fatal("sub-agent cancel context is not checkpoint-aware")
		}
		if !parent.hasCheckpointAwareDescendant() {
			t.Fatal("parent was not marked as having a checkpoint-aware descendant")
		}
	})

	t.Run("AbortOnlyParentIsResumeBarrier", func(t *testing.T) {
		root := newCancelContext()
		baseCtx, cancel := context.WithCancel(context.Background())
		defer cancel()
		abortOnly := deriveAbortOnlyCancelContext(baseCtx, root)
		t.Cleanup(abortOnly.markDone)
		ctx := withCancelContext(baseCtx, abortOnly)

		child := deriveCheckpointAwareSubAgentCancelContext(ctx, nil)
		if child != nil {
			t.Cleanup(child.markDone)
		}

		if child != nil {
			t.Fatal("sub-agent cancel context crossed an abort-only resume barrier")
		}
		if abortOnly.hasCheckpointAwareDescendant() {
			t.Fatal("abort-only scope was marked as having a checkpoint-aware descendant")
		}
		if root.hasCheckpointAwareDescendant() {
			t.Fatal("root checkpoint-aware ancestor was marked through an abort-only barrier")
		}
	})
}

func TestResolveRunCancelContext_CheckpointAwareInheritedScope(t *testing.T) {
	parent := newCancelContext()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveCheckpointAwareCancelContext(baseCtx)
	t.Cleanup(child.markDone)
	ctx := withCancelContext(baseCtx, child)

	got, owned := resolveRunCancelContext(ctx, &options{})
	if got != child {
		t.Fatal("checkpoint-aware inherited scope was not reused")
	}
	if owned {
		t.Fatal("checkpoint-aware inherited scope should not be owned by the child run")
	}
}

func TestAgentCancelFunc_ExpiredSafePointTimeout(t *testing.T) {
	cc := newCancelContext()
	t.Cleanup(cc.markDone)
	var interrupts int32
	cc.setGraphInterruptFunc(func(...compose.GraphInterruptOption) {
		atomic.AddInt32(&interrupts, 1)
	})

	handle, contributed := cc.buildCancelFunc()(
		WithAgentCancelMode(CancelAfterChatModel),
		WithAgentCancelTimeout(time.Nanosecond),
	)
	require.True(t, contributed)
	select {
	case <-cc.immediateChan:
	case <-time.After(time.Second):
		t.Fatal("expired safe-point timeout did not abort the stream")
	}

	cancelErr, handled := cc.createAndMarkCancelHandled()
	require.True(t, handled)
	require.Equal(t, &AgentCancelInfo{
		Mode: CancelAfterChatModel, Escalated: true, Timeout: true,
	}, cancelErr.Info)
	require.ErrorIs(t, handle.Wait(), ErrCancelTimeout)
	require.Equal(t, int32(1), atomic.LoadInt32(&interrupts))
}

func TestWrapStreamWithCancelMonitoring_ConsumerCloseStopsProducer(t *testing.T) {
	cc := newCancelContext()
	t.Cleanup(cc.markDone)
	upstream, writer := schema.Pipe[string](0)
	upstream.SetAutomaticClose()
	t.Cleanup(upstream.Close)
	stream := wrapStreamWithCancelMonitoring(upstream, cc)
	stream.SetAutomaticClose()
	t.Cleanup(stream.Close)

	stopped := make(chan bool, 1)
	go func() {
		defer writer.Close()
		for i := 0; i < 16; i++ {
			if writer.Send("chunk", nil) {
				stopped <- true
				return
			}
		}
		stopped <- false
	}()

	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, "chunk", chunk)
	stream.Close()
	select {
	case closed := <-stopped:
		require.True(t, closed, "producer drained instead of observing consumer closure")
	case <-time.After(time.Second):
		t.Fatal("closing the monitored stream left its producer blocked")
	}
}

func TestAttack_DirectNestedAgentUnderCheckpointAwareParentGetsAbortOnlyScope(t *testing.T) {
	parent := newCancelContext()
	baseCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	checkpointAware := parent.deriveCheckpointAwareCancelContext(baseCtx)
	t.Cleanup(checkpointAware.markDone)
	ctx := withCancelContext(baseCtx, checkpointAware)
	ctx = withTypedChatModelAgentExecCtx(ctx, &chatModelAgentExecCtx{
		cancelCtx: checkpointAware,
	})

	got, owned := resolveRunCancelContext(ctx, &options{})
	if got == checkpointAware {
		t.Fatal("direct nested agent reused the checkpoint-aware parent scope")
	}
	if got == nil || !got.abortOnly || got.parent != checkpointAware {
		t.Fatalf("direct nested agent should derive an abort-only child scope, got %#v", got)
	}
	if !owned {
		t.Fatal("direct nested agent should own the derived abort-only scope")
	}
}
