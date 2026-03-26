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
	"sync"
	"time"
)

// teammateRegistry tracks active teammate goroutines and their handles.
// It encapsulates the concurrency-safe map, mutex, and WaitGroup that were
// previously spread across teamMiddleware fields.
type teammateRegistry struct {
	mu        sync.Mutex
	teammates map[string]*teammateHandle
	wg        sync.WaitGroup
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

// addRunner increments the WaitGroup counter. Call before starting a goroutine.
func (r *teammateRegistry) addRunner() {
	r.wg.Add(1)
}

// doneRunner decrements the WaitGroup counter. Call when a goroutine exits.
func (r *teammateRegistry) doneRunner() {
	r.wg.Done()
}

// waitWithTimeout waits for all runners to exit, with a timeout.
func (r *teammateRegistry) waitWithTimeout(logger Logger, timeout time.Duration) {
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(timeout):
		logger.Printf("teammateRegistry: timed out after %v waiting for teammates to exit", timeout)
	}
}
