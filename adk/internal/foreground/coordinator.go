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

// Package foreground coordinates caller-visible task occupancy without owning
// durable task lifecycle state.
package foreground

import (
	"context"
	"sync"
	"time"
)

const DefaultTimeoutMs = 120_000

// Policy configures foreground occupancy independently from task persistence.
type Policy struct {
	TimeoutMs            int
	ShouldAutoBackground func(context.Context, *CandidateInfo) bool
}

// CandidateInfo describes a process-local foreground execution that may be
// handed off to background ownership. TaskID is pre-allocated for correlation;
// no task/background.TaskSnapshot exists until a handoff callback submits one.
type CandidateInfo struct {
	TaskID      string
	Kind        string
	Description string
	OutputFile  string
	StartedAt   time.Time
	Elapsed     time.Duration
}

type projectionKey struct{}

type projection struct {
	once sync.Once
	done chan struct{}
}

func withProjection(ctx context.Context) (context.Context, func()) {
	state := &projection{done: make(chan struct{})}
	return context.WithValue(ctx, projectionKey{}, state), func() {
		state.once.Do(func() { close(state.done) })
	}
}

// ProjectionDetached returns a signal closed when foreground coordination has
// stopped projecting the current execution. A nil signal means no coordinator
// is attached.
func ProjectionDetached(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.Value(projectionKey{}).(*projection)
	if state == nil {
		return nil
	}
	return state.done
}
