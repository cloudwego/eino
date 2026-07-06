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
	"sync"
	"time"
)

type stopPhase uint8

const (
	stopOpen stopPhase = iota
	stopIdleWaiting
	stopCommitted
)

type stopDecision struct {
	commit   bool
	wakeIdle bool
}

type stopCancelRequest struct {
	cancel cancelRequestState
}

func newStopCancelRequest(opts []AgentCancelOption, now time.Time) *stopCancelRequest {
	return &stopCancelRequest{cancel: newCancelRequestState(opts, now)}
}

func (r *stopCancelRequest) merge(opts []AgentCancelOption, now time.Time) {
	if r == nil {
		return
	}
	r.cancel.merge(opts, now)
}

func (r *stopCancelRequest) cancelOptions(now time.Time) []AgentCancelOption {
	if r == nil {
		return nil
	}
	return r.cancel.cancelOptions(now)
}

// stopController owns global Stop state and optional active-turn cancel requests.
//
// Stop has two independent layers:
//   - terminal loop intent: committed Stop prevents future turns and closes the buffer;
//   - optional active-turn cancel: cancel-capable Stop calls create a pending request
//     consumed by the watcher if the current turn is still active.
//
// Unlike preempt, Stop is not bound to a turnID. It is global and terminal.
// A pending cancel request is consumed by the active turn or dropped when that
// turn ends before consumption.
type stopController struct {
	mu sync.Mutex

	phase stopPhase

	hasActiveCancelTarget bool
	pending               *stopCancelRequest
	notify                chan struct{}

	idleFor        time.Duration
	skipCheckpoint bool
	stopCause      string

	closed bool
}

func newStopController() *stopController {
	return &stopController{notify: make(chan struct{}, 1)}
}

func (c *stopController) requestStop(cfg *stopConfig) stopDecision {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return stopDecision{}
	}
	if cfg.skipCheckpoint {
		c.skipCheckpoint = true
	}
	if cfg.stopCause != "" && c.stopCause == "" {
		c.stopCause = cfg.stopCause
	}
	if cfg.idleFor > 0 {
		if c.phase != stopCommitted && c.idleFor == 0 {
			c.phase = stopIdleWaiting
			c.idleFor = cfg.idleFor
		}
		return stopDecision{wakeIdle: c.phase == stopIdleWaiting}
	}

	committed := c.commitLocked()
	if cfg.agentCancelOpts != nil {
		now := time.Now()
		if c.pending == nil {
			c.pending = newStopCancelRequest(cfg.agentCancelOpts, now)
		} else {
			c.pending.merge(cfg.agentCancelOpts, now)
		}
		if c.hasActiveCancelTarget {
			c.notifyWatcherLocked()
		}
	}
	return stopDecision{commit: committed}
}

func (c *stopController) commit() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.commitLocked()
}

func (c *stopController) commitLocked() bool {
	if c.closed || c.phase == stopCommitted {
		return false
	}
	c.phase = stopCommitted
	c.idleFor = 0
	return true
}

func (c *stopController) isCommitted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase == stopCommitted
}

func (c *stopController) idleDuration() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.phase != stopIdleWaiting {
		return 0
	}
	return c.idleFor
}

func (c *stopController) skipCheckpointEnabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.skipCheckpoint
}

func (c *stopController) cause() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopCause
}

func (c *stopController) beginActiveTurn() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.hasActiveCancelTarget = true
	if c.pending != nil {
		c.notifyWatcherLocked()
	}
}

func (c *stopController) endActiveTurn() *stopCancelRequest {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.hasActiveCancelTarget = false
	req := c.pending
	c.pending = nil
	return req
}

func (c *stopController) receiveCancel() (*stopCancelRequest, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.hasActiveCancelTarget || c.pending == nil {
		return nil, false
	}
	req := c.pending
	c.pending = nil
	return req, true
}

func (c *stopController) closeForLoopExit() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.hasActiveCancelTarget = false
	c.pending = nil
	select {
	case <-c.notify:
	default:
	}
}

func (c *stopController) notifyWatcherLocked() {
	select {
	case c.notify <- struct{}{}:
	default:
	}
}
