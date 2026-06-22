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
		// abnormal stays true unless runPump returns a clean ctx-cancel exit. It
		// is read in a defer so it also covers the panic-unwinding path: a panic
		// propagates past runPump (leaving abnormal=true), runs this defer, then
		// reaches safeGoWithLogger's recover for logging.
		abnormal := true
		defer close(pumpDone)
		defer cancel()
		// A teammate pump that exits while its ctx is still live is a degraded
		// state: the pump goroutine is decoupled from the TurnLoop owner
		// goroutine (which blocks in runner.Wait), so without intervention the
		// loop would keep running with nobody draining its inbox — a zombie
		// teammate that can never be delivered to, never cleaned up, and blocks
		// TeamDelete forever. In that case stop the loop so the owner's
		// runner.Wait unblocks and the deferred cleanupExitedTeammate runs the
		// normal crash-teardown path. A clean ctx-cancel exit (UnsetMailbox /
		// shutdown) is the expected teardown path and must not trigger a Stop.
		//
		// Only teammate pumps self-heal this way: the leader loop is driven by the
		// host and torn down via cleanupLeaderMailbox, so a leader pump error is
		// logged inside runPump but must not stop the host-owned leader loop.
		defer func() {
			if abnormal && ms.conf.Role == teamRoleTeammate && pumpCtx.Err() == nil {
				pm.logger.Printf("mailbox pump[%s] exited abnormally; stopping loop to trigger cleanup", agentName)
				loop.Stop(adk.WithImmediate())
			}
		}()
		abnormal = pm.runPump(pumpCtx, agentName, ms, loop)
	})
}

// runPump is the main loop for a mailbox pump goroutine. It alternates between
// non-blocking tryReceive and blocking waitForItem, pushing received messages
// into the agent's TurnLoop.
//
// It returns abnormal=true when it exits for any reason other than a clean
// ctx-cancel (backend error from tryReceive/waitForItem/ack, or a loop that
// rejected a push because it is tearing down). The caller uses this to decide
// whether the owning TurnLoop must be stopped so a teammate cannot linger as a
// zombie (see StartPump). A clean ctx-cancel exit returns abnormal=false.
func (pm *pumpManager) runPump(ctx context.Context, agentName string,
	ms *mailboxMessageSource, loop *adk.TurnLoop[TurnInput, adk.Message]) (abnormal bool) {

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
			return false
		default:
		}

		item, ack, ok, err := ms.tryReceive(ctx, !idleSent)
		if err != nil {
			pm.logger.Printf("mailbox pump[%s] error: %v", agentName, err)
			return true
		}
		if ok {
			idleSent = false
			setActive(true)
			if done, ok := pm.pushAndAck(ctx, agentName, loop, item, ack); done {
				return !ok
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
				return false
			}
			pm.logger.Printf("mailbox pump[%s] wait error: %v", agentName, err)
			return true
		}
		idleSent = false // reset after processing new messages
		setActive(true)
		if done, ok := pm.pushAndAck(ctx, agentName, loop, item, ack); done {
			return !ok
		}
	}
}

// pushAndAck stamps the item for the agent, pushes it into the loop, and acks it.
// It returns done=true when the pump must stop processing, with ok reporting
// whether that stop was clean (ok=true means a normal continuation could not
// happen but it is not an error to report). Specifically:
//
//   - push rejected (loop tearing down): done=true, ok=false. Do NOT ack —
//     leaving the messages unread keeps them recoverable instead of silently
//     dropping them. The leader's ack is a no-op (its snapshot was already
//     consumed for replay-safe side effects), so this only preserves ordinary
//     teammate messages. This is an abnormal exit: the loop is gone, so the
//     pump cannot keep running against it.
//   - ack failed (backend error): done=true, ok=false. Abnormal exit.
//   - success: done=false (caller continues the loop).
func (pm *pumpManager) pushAndAck(ctx context.Context, agentName string,
	loop *adk.TurnLoop[TurnInput, adk.Message], item TurnInput, ack ackFunc) (done, ok bool) {

	item.TargetAgent = agentName
	if accepted, _ := loop.Push(item); !accepted {
		return true, false
	}
	if ackErr := ack(ctx); ackErr != nil {
		pm.logger.Printf("mailbox pump[%s] ack error: %v", agentName, ackErr)
		return true, false
	}
	return false, true
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
