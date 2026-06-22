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

// mailbox_pump.go manages per-agent mailbox pump goroutines that read from
// a mailboxMessageSource and push items into the corresponding TurnLoop.
// Separated from source_router.go to follow the Single Responsibility Principle.

package team

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/adk"
)

// pumpHandle tracks a running mailbox pump goroutine so callers can wait for
// it to fully exit before starting a replacement, preventing duplicate message
// processing from two concurrent pumps reading the same inbox.
type pumpHandle struct {
	cancel context.CancelFunc
	done   chan struct{} // closed when the pump goroutine exits
}

// pumpManager manages the lifecycle of per-agent mailbox pump goroutines.
// Each pump reads from a mailboxMessageSource and pushes TurnInput items
// into the corresponding agent's TurnLoop via the sourceRouter.
type pumpManager struct {
	router *sourceRouter
	logger Logger

	mu           sync.Mutex
	mailboxes    map[string]*mailboxMessageSource
	pumps        map[string]*pumpHandle
	startingDone map[string]chan struct{} // closed when StartPump finishes installing the new pump
	// active tracks each teammate's busy/idle status in memory, keyed by agent
	// name. This is volatile runtime state that flips frequently as messages
	// arrive and drain, so it is deliberately NOT persisted to config.json:
	// persisting would rewrite the whole file under the single shared cfgLock on
	// every flip, serializing the hot path against low-frequency member
	// add/remove. No code path reads a persisted busy/idle flag — TeamDelete
	// consults the teammate registry for liveness (see tool_team_delete.go) — so
	// keeping it in process is both cheaper and correct.
	active map[string]bool
}

func newPumpManager(router *sourceRouter, logger Logger) *pumpManager {
	return &pumpManager{
		router:       router,
		logger:       logger,
		mailboxes:    make(map[string]*mailboxMessageSource),
		pumps:        make(map[string]*pumpHandle),
		startingDone: make(map[string]chan struct{}),
		active:       make(map[string]bool),
	}
}

// SetMailbox registers a mailboxMessageSource for the given agent.
//
// A nil pumpManager is a no-op: teammate middleware is constructed without a
// pump manager (only the leader's lifecycleManager owns one), so calling pump
// operations through a teammate's manager must be harmless rather than panic.
func (pm *pumpManager) SetMailbox(agentName string, ms *mailboxMessageSource) {
	if pm == nil {
		return
	}
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.mailboxes[agentName] = ms
}

// UnsetMailbox detaches the mailbox for the given agent and stops its pump.
// A nil pumpManager is a no-op (see SetMailbox).
func (pm *pumpManager) UnsetMailbox(agentName string) {
	if pm == nil {
		return
	}
	pm.mu.Lock()
	delete(pm.mailboxes, agentName)
	delete(pm.active, agentName)
	h := pm.pumps[agentName]
	delete(pm.pumps, agentName)
	startingDone := pm.startingDone[agentName]
	pm.mu.Unlock()

	if h != nil {
		h.cancel()
		<-h.done
	}

	// If StartPump is in progress (lock released while draining the old pump),
	// wait for it to finish installing the new pump, then cancel that pump too.
	// Without this, the new pump created by the concurrent StartPump would leak.
	if startingDone != nil {
		<-startingDone
		pm.mu.Lock()
		h = pm.pumps[agentName]
		delete(pm.pumps, agentName)
		pm.mu.Unlock()
		if h != nil {
			h.cancel()
			<-h.done
		}
	}
}

// StartPump starts a goroutine that reads from the agent's mailbox
// and pushes items into the agent's TurnLoop.
// If a previous pump exists for this agent, it is cancelled and fully drained
// before the new pump starts, preventing duplicate message processing.
// A nil pumpManager is a no-op (see SetMailbox).
func (pm *pumpManager) StartPump(ctx context.Context, agentName string) {
	if pm == nil {
		return
	}
	pm.mu.Lock()
	ms := pm.mailboxes[agentName]
	if ms == nil {
		pm.mu.Unlock()
		// A missing mailbox means SetMailbox was never called (or was already
		// unset) for this agent. Surfacing it helps diagnose teammates whose
		// initial prompt would otherwise sit unread in the inbox forever.
		pm.logger.Printf("mailbox pump[%s] not started: no mailbox registered", agentName)
		return
	}
	loop := pm.router.getLoop(agentName)
	if loop == nil {
		pm.mu.Unlock()
		pm.logger.Printf("mailbox pump[%s] not started: no TurnLoop registered", agentName)
		return
	}

	// If another goroutine is already starting a pump for this agent,
	// skip to avoid the race where two pumps end up running concurrently.
	if pm.startingDone[agentName] != nil {
		pm.mu.Unlock()
		return
	}
	done := make(chan struct{})
	pm.startingDone[agentName] = done

	old := pm.pumps[agentName]
	delete(pm.pumps, agentName)
	pm.mu.Unlock()

	// Wait for the old pump to fully exit before starting a new one.
	// This eliminates the window where two pumps concurrently ReadUnread
	// the same messages and both push duplicates into the TurnLoop.
	if old != nil {
		old.cancel()
		<-old.done
	}

	pumpCtx, cancel := context.WithCancel(ctx)
	pumpDone := make(chan struct{})

	pm.mu.Lock()
	pm.pumps[agentName] = &pumpHandle{cancel: cancel, done: pumpDone}
	delete(pm.startingDone, agentName)
	pm.mu.Unlock()
	close(done) // signal any waiting UnsetMailbox that the new pump is installed

	safeGoWithLogger(pm.logger, func() {
		defer close(pumpDone)
		defer cancel()
		pm.runPump(pumpCtx, agentName, ms, loop)
	})
}

// runPump is the main loop for a mailbox pump goroutine. It alternates between
// non-blocking tryReceive and blocking waitForItem, pushing received messages
// into the agent's TurnLoop. It exits when ctx is cancelled or the loop rejects a push.
func (pm *pumpManager) runPump(ctx context.Context, agentName string,
	ms *mailboxMessageSource, loop *adk.TurnLoop[TurnInput, adk.Message]) {

	// idleSent tracks whether an idle notification has already been sent since
	// the last time messages were processed. This prevents flooding the leader
	// with redundant idle notifications on every empty poll cycle.
	idleSent := false

	isTeammate := ms.conf.Role == teamRoleTeammate

	// active mirrors the busy/idle status last recorded for this teammate. We only
	// call setActive when the value actually flips, so steady-state busy/idle
	// cycles do not touch the shared map every tick. A nil pointer means "not yet
	// recorded", forcing the first transition through.
	var active *bool
	setActive := func(next bool) {
		if !isTeammate {
			return
		}
		if active != nil && *active == next {
			return
		}
		pm.setActive(agentName, next)
		active = &next
	}

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		item, ack, ok, err := ms.tryReceive(ctx, !idleSent)
		if err != nil {
			pm.logger.Printf("mailbox pump[%s] error: %v", agentName, err)
			return
		}
		if ok {
			idleSent = false
			setActive(true)
			item.TargetAgent = agentName
			if accepted, _ := loop.Push(item); !accepted {
				// The loop rejected the push (it is being torn down). Do NOT ack:
				// leaving the messages unread keeps them recoverable instead of
				// silently dropping them. The leader's ack is a no-op (its snapshot
				// was already consumed for replay-safe side effects), so this only
				// preserves ordinary teammate messages.
				return
			}
			if err := ack(ctx); err != nil {
				pm.logger.Printf("mailbox pump[%s] ack error: %v", agentName, err)
				return
			}
			continue
		}

		if !idleSent {
			setActive(false)
		}
		idleSent = true

		item, ack, err = ms.waitForItem(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			pm.logger.Printf("mailbox pump[%s] wait error: %v", agentName, err)
			return
		}
		idleSent = false // reset after processing new messages
		setActive(true)
		item.TargetAgent = agentName
		if accepted, _ := loop.Push(item); !accepted {
			return
		}
		if err := ack(ctx); err != nil {
			pm.logger.Printf("mailbox pump[%s] ack error: %v", agentName, err)
			return
		}
	}
}

// setActive records the teammate's busy/idle status in memory. The status is
// intentionally process-local (see the pumpManager.active doc comment): it is
// not persisted, so this never touches the backend or cfgLock on the hot path.
func (pm *pumpManager) setActive(agentName string, active bool) {
	pm.mu.Lock()
	pm.active[agentName] = active
	pm.mu.Unlock()
}

// isActive reports the last recorded busy/idle status for the given teammate and
// whether any status has been recorded yet.
func (pm *pumpManager) isActive(agentName string) (active bool, ok bool) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	active, ok = pm.active[agentName]
	return active, ok
}
