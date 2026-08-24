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
	"sync"
)

const projectionBuffer = 16

var errProjectionExists = errors.New("task/tool: projection already exists")

type liveProjection struct {
	ready      chan struct{}
	readyOnce  sync.Once
	updates    chan *Update
	closeOnce  sync.Once
	detached   chan struct{}
	detachOnce sync.Once
	stateMu    sync.Mutex
	isDetached bool
}

func newLiveProjection() *liveProjection {
	return &liveProjection{
		ready: make(chan struct{}), updates: make(chan *Update, projectionBuffer),
		detached: make(chan struct{}),
	}
}

func (p *liveProjection) signalReady() {
	p.readyOnce.Do(func() { close(p.ready) })
}

func (p *liveProjection) closeUpdates() {
	p.signalReady()
	p.closeOnce.Do(func() { close(p.updates) })
}

func (p *liveProjection) detach() {
	p.stateMu.Lock()
	p.detachOnce.Do(func() {
		p.isDetached = true
		close(p.detached)
	})
	p.stateMu.Unlock()
}

func (p *liveProjection) send(ctx context.Context, detached <-chan struct{}, update *Update) {
	if p == nil || update == nil {
		return
	}
	p.stateMu.Lock()
	if p.isDetached {
		p.stateMu.Unlock()
		return
	}
	projectionDetached := p.detached
	p.stateMu.Unlock()
	select {
	case p.updates <- cloneUpdate(update):
	case <-projectionDetached:
	case <-detached:
	case <-ctx.Done():
	}
}

type projectionRegistry struct {
	mu    sync.Mutex
	tasks map[string]*liveProjection
}

func newProjectionRegistry() *projectionRegistry {
	return &projectionRegistry{tasks: make(map[string]*liveProjection)}
}

func (r *projectionRegistry) register(taskID string) (*liveProjection, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[taskID]; exists {
		return nil, errProjectionExists
	}
	projection := newLiveProjection()
	r.tasks[taskID] = projection
	return projection, nil
}

func (r *projectionRegistry) load(taskID string) *liveProjection {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tasks[taskID]
}

func (r *projectionRegistry) remove(taskID string) *liveProjection {
	r.mu.Lock()
	projection := r.tasks[taskID]
	delete(r.tasks, taskID)
	r.mu.Unlock()
	if projection != nil {
		projection.detach()
	}
	return projection
}

func cloneUpdate(update *Update) *Update {
	if update == nil {
		return nil
	}
	copy := *update
	copy.Data = append([]byte(nil), update.Data...)
	if update.Metadata != nil {
		copy.Metadata = make(map[string]string, len(update.Metadata))
		for key, value := range update.Metadata {
			copy.Metadata[key] = value
		}
	}
	return &copy
}
