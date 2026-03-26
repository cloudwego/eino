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

package team

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewTeammateRegistry(t *testing.T) {
	reg := newTeammateRegistry()
	assert.NotNil(t, reg)
	assert.NotNil(t, reg.teammates)
	assert.Equal(t, 0, len(reg.teammates))
}

func TestTeammateRegistry_Register(t *testing.T) {
	reg := newTeammateRegistry()
	handle := &teammateHandle{}
	reg.register("agent-a", handle)

	reg.mu.Lock()
	defer reg.mu.Unlock()
	assert.Equal(t, 1, len(reg.teammates))
	assert.Same(t, handle, reg.teammates["agent-a"])
}

func TestTeammateRegistry_Remove_Existing(t *testing.T) {
	reg := newTeammateRegistry()
	handle := &teammateHandle{}
	reg.register("agent-a", handle)

	result, ok := reg.remove("agent-a")
	assert.True(t, ok)
	assert.Same(t, handle, result)
}

func TestTeammateRegistry_Remove_NonExisting(t *testing.T) {
	reg := newTeammateRegistry()
	result, ok := reg.remove("no-such-agent")
	assert.False(t, ok)
	assert.Nil(t, result)
}

func TestTeammateRegistry_RegisterThenRemove(t *testing.T) {
	reg := newTeammateRegistry()
	handle := &teammateHandle{}
	reg.register("agent-a", handle)

	result, ok := reg.remove("agent-a")
	assert.True(t, ok)
	assert.Same(t, handle, result)

	reg.mu.Lock()
	defer reg.mu.Unlock()
	assert.Equal(t, 0, len(reg.teammates))
}

func TestTeammateRegistry_CancelAll(t *testing.T) {
	reg := newTeammateRegistry()

	ctx1, cancel1 := context.WithCancel(context.Background())
	ctx2, cancel2 := context.WithCancel(context.Background())

	reg.register("a", &teammateHandle{Cancel: cancel1})
	reg.register("b", &teammateHandle{Cancel: cancel2})

	reg.cancelAll()

	assert.Error(t, ctx1.Err())
	assert.Error(t, ctx2.Err())
}

func TestTeammateRegistry_AddRunnerDoneRunner(t *testing.T) {
	reg := newTeammateRegistry()
	reg.addRunner()
	reg.addRunner()

	done := make(chan struct{})
	go func() {
		reg.wg.Wait()
		close(done)
	}()

	reg.doneRunner()
	reg.doneRunner()

	select {
	case <-done:
	case <-time.After(1 * time.Second):
		t.Fatal("WaitGroup did not reach zero")
	}
}

func TestTeammateRegistry_WaitWithTimeout_CompletesBeforeTimeout(t *testing.T) {
	reg := newTeammateRegistry()
	reg.addRunner()

	go func() {
		time.Sleep(10 * time.Millisecond)
		reg.doneRunner()
	}()

	start := time.Now()
	reg.waitWithTimeout(nopLogger{}, 1*time.Second)
	elapsed := time.Since(start)

	assert.True(t, elapsed < 1*time.Second)
}

func TestTeammateRegistry_WaitWithTimeout_TimesOut(t *testing.T) {
	reg := newTeammateRegistry()
	reg.addRunner()

	start := time.Now()
	reg.waitWithTimeout(nopLogger{}, 50*time.Millisecond)
	elapsed := time.Since(start)

	assert.True(t, elapsed >= 50*time.Millisecond)

	reg.doneRunner()
}

func TestTeammateRegistry_ConcurrentRegisterAndRemove(t *testing.T) {
	reg := newTeammateRegistry()
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines * 2)

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("agent-%d", idx)
			reg.register(name, &teammateHandle{})
		}(i)
	}

	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			name := fmt.Sprintf("agent-%d", idx)
			reg.remove(name)
		}(i)
	}

	wg.Wait()
}

func TestTeammateRegistry_RegisterOverwritesExistingEntry(t *testing.T) {
	reg := newTeammateRegistry()

	handle1 := &teammateHandle{}
	handle2 := &teammateHandle{}

	reg.register("agent-a", handle1)
	reg.register("agent-a", handle2)

	reg.mu.Lock()
	defer reg.mu.Unlock()
	assert.Equal(t, 1, len(reg.teammates))
	assert.Same(t, handle2, reg.teammates["agent-a"])
}
