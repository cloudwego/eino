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
// a MailboxMessageSource and push items into the corresponding TurnLoop.
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
// Each pump reads from a MailboxMessageSource and pushes TurnInput items
// into the corresponding agent's TurnLoop via the sourceRouter.
type pumpManager struct {
	router     *sourceRouter
	logger     Logger
	teamCfg    *Config
	teamNameFn func() string

	mu           sync.Mutex
	mailboxes    map[string]*MailboxMessageSource
	pumps        map[string]*pumpHandle
	startingDone map[string]chan struct{} // closed when StartPump finishes installing the new pump
}

func newPumpManager(router *sourceRouter, logger Logger) *pumpManager {
	return &pumpManager{
		router:       router,
		logger:       logger,
		mailboxes:    make(map[string]*MailboxMessageSource),
		pumps:        make(map[string]*pumpHandle),
		startingDone: make(map[string]chan struct{}),
	}
}

// SetMailbox registers a MailboxMessageSource for the given agent.
func (pm *pumpManager) SetMailbox(agentName string, ms *MailboxMessageSource) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.mailboxes[agentName] = ms
}

// UnsetMailbox detaches the mailbox for the given agent and stops its pump.
func (pm *pumpManager) UnsetMailbox(agentName string) {
	pm.mu.Lock()
	delete(pm.mailboxes, agentName)
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
func (pm *pumpManager) StartPump(ctx context.Context, agentName string) {
	pm.mu.Lock()
	ms := pm.mailboxes[agentName]
	if ms == nil {
		pm.mu.Unlock()
		return
	}
	loop := pm.router.getLoop(agentName)
	if loop == nil {
		pm.mu.Unlock()
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
	ms *MailboxMessageSource, loop *adk.TurnLoop[TurnInput]) {

	// idleSent tracks whether an idle notification has already been sent since
	// the last time messages were processed. This prevents flooding the leader
	// with redundant idle notifications on every empty poll cycle.
	idleSent := false

	isTeammate := ms.conf.Role == teamRoleTeammate

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		item, ok, err := ms.tryReceive(ctx, !idleSent)
		if err != nil {
			pm.logger.Printf("mailbox pump[%s] error: %v", agentName, err)
			return
		}
		if ok {
			idleSent = false
			if isTeammate {
				pm.setActive(ctx, agentName, true)
			}
			item.TargetAgent = agentName
			if accepted, _ := loop.Push(item); !accepted {
				return
			}
			continue
		}

		if isTeammate && !idleSent {
			pm.setActive(ctx, agentName, false)
		}
		idleSent = true

		item, err = ms.waitForItem(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			pm.logger.Printf("mailbox pump[%s] wait error: %v", agentName, err)
			return
		}
		idleSent = false // reset after processing new messages
		if isTeammate {
			pm.setActive(ctx, agentName, true)
		}
		item.TargetAgent = agentName
		if accepted, _ := loop.Push(item); !accepted {
			return
		}
	}
}

// setActive updates the member's isActive status in the team config.
func (pm *pumpManager) setActive(ctx context.Context, agentName string, active bool) {
	if pm.teamCfg == nil || pm.teamNameFn == nil {
		return
	}
	teamName := pm.teamNameFn()
	if teamName == "" {
		return
	}
	if err := pm.teamCfg.SetMemberActive(ctx, teamName, agentName, active); err != nil {
		pm.logger.Printf("mailbox pump[%s] setActive(%v): %v", agentName, active, err)
	}
}
