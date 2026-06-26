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

// teammate_registry.go provides a concurrency-safe registry of active
// teammate goroutines and their handles, used for shutdown coordination.

package team

import (
	"context"
	"sync"
	"time"
)

// teammateRegistry tracks active teammate goroutines and their handles.
// It encapsulates the concurrency-safe map, mutex, and runner accounting that
// were previously spread across teamMiddleware fields.
//
// Runner completion is tracked with an explicit counter plus a lazily created
// allExited channel instead of a sync.WaitGroup. A WaitGroup would force every
// waiter to block on Wait() from a helper goroutine; if a teammate hangs, that
// goroutine could never be cancelled and would leak for the lifetime of the
// process. With the counter, the last exiting runner closes allExited inline, so
// waitWithTimeout can select on it without spawning any goroutine — a timed-out
// or cancelled wait therefore leaks nothing.
type teammateRegistry struct {
	mu        sync.Mutex
	teammates map[string]*teammateHandle

	// running is the number of live runner goroutines (addRunner/doneRunner).
	running int
	// allExited is closed by the runner that drops running to 0. It is created
	// lazily by waitWithTimeout and reset to nil after being closed, so a new
	// wait cycle can re-arm it once more runners are added.
	allExited chan struct{}
}

func newTeammateRegistry() *teammateRegistry {
	return &teammateRegistry{
		teammates: make(map[string]*teammateHandle),
	}
}

// register stores a teammateHandle for the given teammate name.
func (r *teammateRegistry) register(name string, result *teammateHandle) {
	r.mu.Lock()
	r.teammates[name] = result
	r.mu.Unlock()
}

// remove atomically removes and returns the teammateHandle for the given name.
// Returns (result, true) if found, or (nil, false) if the name was not registered.
func (r *teammateRegistry) remove(name string) (*teammateHandle, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	result, ok := r.teammates[name]
	if ok {
		delete(r.teammates, name)
	}
	return result, ok
}

// cancelAll cancels every registered teammate's context. Does not wait for exit.
func (r *teammateRegistry) cancelAll() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, result := range r.teammates {
		if result.Cancel != nil {
			result.Cancel()
		}
	}
}

// activeNames returns the names of all currently registered teammates.
func (r *teammateRegistry) activeNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	names := make([]string, 0, len(r.teammates))
	for name := range r.teammates {
		names = append(names, name)
	}
	return names
}

// addRunner increments the running-runner counter. Call before starting a
// goroutine.
func (r *teammateRegistry) addRunner() {
	r.mu.Lock()
	r.running++
	r.mu.Unlock()
}

// doneRunner decrements the running-runner counter. Call when a goroutine exits.
// When the counter reaches zero it closes (and clears) any allExited channel a
// waiter armed, signalling completion without an intermediary goroutine.
func (r *teammateRegistry) doneRunner() {
	r.mu.Lock()
	if r.running > 0 {
		r.running--
	}
	if r.running == 0 && r.allExited != nil {
		close(r.allExited)
		r.allExited = nil
	}
	r.mu.Unlock()
}

// waitWithTimeout waits for all runners to exit. It returns when all runners
// have exited, when the provided ctx is cancelled, or when the timeout elapses —
// whichever happens first. ctx lets a caller bound shutdown to an external
// deadline (e.g. a server's graceful-stop budget); timeout is the fallback cap
// so a hung backend can never block the wait indefinitely.
//
// Unlike a sync.WaitGroup-based wait, this never spawns a helper goroutine: it
// arms an allExited channel under the lock (or observes that no runners remain)
// and selects on it directly, so a timed-out or cancelled wait leaks nothing —
// the channel is simply closed later by the last runner and then garbage
// collected.
func (r *teammateRegistry) waitWithTimeout(ctx context.Context, logger Logger, timeout time.Duration) {
	r.mu.Lock()
	if r.running == 0 {
		r.mu.Unlock()
		return
	}
	if r.allExited == nil {
		r.allExited = make(chan struct{})
	}
	done := r.allExited
	r.mu.Unlock()

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case <-done:
	case <-ctx.Done():
		logger.Printf("teammateRegistry: context cancelled (%v) while waiting for teammates to exit", ctx.Err())
	case <-timer.C:
		logger.Printf("teammateRegistry: timed out after %v waiting for teammates to exit", timeout)
	}
}
