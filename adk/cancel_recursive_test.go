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

func TestDeriveChild_Shallow_DoesNotPropagateSafePoint(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.triggerCancel(CancelAfterChatModel)

	time.Sleep(200 * time.Millisecond)
	assert.False(t, child.shouldCancel())
}

func TestDeriveChild_Recursive_PropagatesSafePoint(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.setRecursive(true)
	parent.triggerCancel(CancelAfterChatModel)

	select {
	case <-child.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("child did not receive cancel within 1s")
	}
	assert.True(t, child.shouldCancel())
}

func TestDeriveChild_Shallow_ImmediateDoesNotPropagate(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.triggerImmediateCancel()

	time.Sleep(200 * time.Millisecond)
	assert.False(t, child.isImmediateCancelled())
}

func TestDeriveChild_Recursive_ImmediatePropagates(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.setRecursive(true)
	parent.triggerImmediateCancel()

	select {
	case <-child.immediateChan:
	case <-time.After(1 * time.Second):
		t.Fatal("child did not receive immediate cancel within 1s")
	}
	assert.True(t, child.isImmediateCancelled())
}

func TestRecursiveCancel_EscalateFromNonRecursive(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.triggerCancel(CancelAfterChatModel)

	time.Sleep(200 * time.Millisecond)
	assert.False(t, child.shouldCancel())

	parent.setRecursive(true)

	select {
	case <-child.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("child did not receive cancel after escalation within 1s")
	}
	assert.True(t, child.shouldCancel())
}

func TestRecursiveCancel_EscalateImmediate(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.triggerImmediateCancel()

	time.Sleep(200 * time.Millisecond)
	assert.False(t, child.isImmediateCancelled())

	parent.setRecursive(true)

	select {
	case <-child.immediateChan:
	case <-time.After(1 * time.Second):
		t.Fatal("child did not receive immediate cancel after escalation within 1s")
	}
	assert.True(t, child.isImmediateCancelled())
}

func TestRecursive_Grandchild_Propagation(t *testing.T) {
	a := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := a.deriveChild(ctx)
	defer b.markDone()
	c := b.deriveChild(ctx)
	defer c.markDone()

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
}

func TestShallow_Grandchild_NoPropagation(t *testing.T) {
	a := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	b := a.deriveChild(ctx)
	defer b.markDone()
	c := b.deriveChild(ctx)
	defer c.markDone()

	a.triggerCancel(CancelAfterChatModel)

	time.Sleep(200 * time.Millisecond)
	assert.False(t, b.shouldCancel())
	assert.False(t, c.shouldCancel())
}

func TestRace_SetRecursiveConcurrentWithCancelChan(t *testing.T) {
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
}

func TestRace_ChildCompletesBeforeEscalation(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)

	parent.triggerCancel(CancelAfterChatModel)
	time.Sleep(50 * time.Millisecond)

	child.markDone()
	time.Sleep(50 * time.Millisecond)

	parent.setRecursive(true)
	time.Sleep(200 * time.Millisecond)

	assert.False(t, child.shouldCancel())
}

func TestRace_MultipleChildren_PartialCompletion(t *testing.T) {
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
}

func TestRace_ContextCancelConcurrentWithRecursive(t *testing.T) {
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
}

func TestRace_ConcurrentSetRecursive(t *testing.T) {
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
}

func TestGracePeriod_OnlyWhenRecursive(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

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

func TestDeriveChild_NeverRecursive_GoroutineCleanup(t *testing.T) {
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
}

func TestDeriveChild_AfterRecursiveAndCancelAlreadySet(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	parent.setRecursive(true)
	parent.triggerCancel(CancelAfterChatModel)

	child := parent.deriveChild(ctx)
	defer child.markDone()

	select {
	case <-child.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("child did not immediately receive cancel")
	}
	assert.True(t, child.shouldCancel())
}

func TestRecursive_SetBeforeCancel(t *testing.T) {
	parent := newCancelContext()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	child := parent.deriveChild(ctx)
	defer child.markDone()

	parent.setRecursive(true)

	parent.triggerCancel(CancelAfterChatModel)

	select {
	case <-child.cancelChan:
	case <-time.After(1 * time.Second):
		t.Fatal("child did not receive cancel within 1s")
	}
	assert.True(t, child.shouldCancel())
}
