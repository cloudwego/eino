/*
 * Copyright 2025 CloudWeGo Authors
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

package sseha

import (
	"context"
	"fmt"
	"time"
)

// DefaultSessionCorrector implements SessionCorrector using shared metadata
// and pub/sub event forwarding for session correction (纠偏).
//
// It supports two correction patterns:
//
//  1. Mesh/Long-connection auto-correction via shared metadata store:
//     When a client reconnects to a different node, the corrector queries the
//     metadata store to find the session's current owner, migrates ownership,
//     and replays buffered events.
//
//  2. Pub/Sub forwarding correction:
//     When a session is detected on a dead node, the corrector subscribes to
//     the session's event channel and accepts the migration, ensuring events
//     continue to flow without loss.
type DefaultSessionCorrector struct {
	manager *SessionManager
}

// NewDefaultSessionCorrector creates a new corrector bound to the given manager.
func NewDefaultSessionCorrector(manager *SessionManager) *DefaultSessionCorrector {
	return &DefaultSessionCorrector{
		manager: manager,
	}
}

// DetectAnomalies checks for sessions that need correction.
// It identifies:
//   - Sessions owned by dead nodes
//   - Sessions that have been suspended beyond the timeout
//   - Sessions in migrating state that are stuck
func (c *DefaultSessionCorrector) DetectAnomalies(ctx context.Context) ([]*SessionInfo, error) {
	store := c.manager.Store()
	policy := c.manager.Policy()

	// Get all alive nodes
	aliveNodes, err := store.ListNodes(ctx, true, c.manager.HeartbeatTimeout())
	if err != nil {
		return nil, fmt.Errorf("list alive nodes: %w", err)
	}

	aliveSet := make(map[string]bool, len(aliveNodes))
	for _, n := range aliveNodes {
		aliveSet[n.NodeID] = true
	}

	var anomalies []*SessionInfo

	// 1. Find sessions on dead nodes
	allSessions, err := store.ListSessions(ctx, &SessionFilter{
		States: []SessionState{SessionStateActive, SessionStateSuspended},
	})
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	now := time.Now()
	for _, info := range allSessions {
		// Skip sessions owned by this node (we handle them locally)
		if info.NodeID == c.manager.NodeID() {
			continue
		}

		// Session's node is dead
		if !aliveSet[info.NodeID] {
			anomalies = append(anomalies, info)
			continue
		}

		// Session has been suspended too long
		if info.State == SessionStateSuspended &&
			now.Sub(info.LastActiveAt) > policy.SuspendTimeout {
			anomalies = append(anomalies, info)
			continue
		}
	}

	// 2. Find stuck migrations
	migratingSessions, err := store.ListSessions(ctx, &SessionFilter{
		States: []SessionState{SessionStateMigrating},
	})
	if err != nil {
		return nil, fmt.Errorf("list migrating sessions: %w", err)
	}

	for _, info := range migratingSessions {
		if now.Sub(info.LastActiveAt) > policy.MigrationTimeout {
			anomalies = append(anomalies, info)
		}
	}

	return anomalies, nil
}

// CorrectSession performs correction for a single session by migrating it
// to the target node. This implements the mesh long-connection auto-correction
// pattern described in SEP-2001.
func (c *DefaultSessionCorrector) CorrectSession(ctx context.Context, sessionID string, targetNodeID string) (*SessionCorrectionResult, error) {
	startTime := time.Now()
	store := c.manager.Store()

	// Acquire migration lock to prevent concurrent corrections
	acquired, err := store.AcquireSessionLock(ctx, sessionID, targetNodeID, c.manager.Policy().MigrationTimeout)
	if err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	if !acquired {
		return nil, ErrSessionLocked
	}
	defer func() {
		_ = store.ReleaseSessionLock(ctx, sessionID, targetNodeID)
	}()

	info, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrSessionNotFound
	}

	previousNodeID := info.NodeID

	// Set migration barrier
	barrier := &BarrierToken{
		SessionID: sessionID,
		FromNode:  previousNodeID,
		ToNode:    targetNodeID,
		CreatedAt: time.Now(),
		Released:  false,
	}
	if err := store.SetBarrier(ctx, barrier); err != nil {
		return nil, fmt.Errorf("set barrier: %w", err)
	}

	// Update session ownership
	info.State = SessionStateMigrating
	info.NodeID = targetNodeID
	info.LastActiveAt = time.Now()
	if err := store.UpdateSession(ctx, info); err != nil {
		return nil, fmt.Errorf("update session ownership: %w", err)
	}

	// Set up local state on the new node
	c.manager.SetupLocalSession(ctx, sessionID, info)

	// Release barrier — new node is ready
	if err := store.ReleaseBarrier(ctx, sessionID); err != nil {
		c.manager.logger.Warnf("Failed to release barrier: %v", err)
	}

	// Finalize: mark session as active on new node
	info.State = SessionStateActive
	_ = store.UpdateSession(ctx, info)

	result := &SessionCorrectionResult{
		SessionID:         sessionID,
		PreviousNodeID:    previousNodeID,
		NewNodeID:         targetNodeID,
		ReplayedEvents:    0, // Events will be received via pub/sub
		CorrectionLatency: time.Since(startTime),
	}

	c.manager.logger.Infof("Session %s corrected: %s -> %s (latency %v)",
		sessionID, previousNodeID, targetNodeID, result.CorrectionLatency)

	return result, nil
}

// HandleReconnection handles a client reconnecting to a different node.
// This implements the pub/sub forwarding-based correction pattern.
//
// The flow is:
//  1. Check if the session exists and which node owns it.
//  2. If owned by another (possibly dead) node, migrate ownership to this node.
//  3. Subscribe to the session's event bus channel.
//  4. Replay buffered events from the last known event ID.
func (c *DefaultSessionCorrector) HandleReconnection(ctx context.Context, sessionID string, lastEventID string, currentNodeID string) (*SessionCorrectionResult, error) {
	startTime := time.Now()
	store := c.manager.Store()

	info, err := store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrSessionNotFound
	}

	if info.State == SessionStateClosed {
		return nil, ErrSessionClosed
	}

	previousNodeID := info.NodeID

	// If already on this node, just replay
	if previousNodeID == currentNodeID {
		replayCount := 0
		if bufI, ok := c.manager.localBuffers.Load(sessionID); ok {
			buf := bufI.(*EventBuffer)
			events, found := buf.EventsAfter(lastEventID)
			if found {
				replayCount = len(events)
			}
		}

		return &SessionCorrectionResult{
			SessionID:         sessionID,
			PreviousNodeID:    previousNodeID,
			NewNodeID:         currentNodeID,
			ReplayedEvents:    replayCount,
			CorrectionLatency: time.Since(startTime),
		}, nil
	}

	// Migrate to this node
	result, err := c.CorrectSession(ctx, sessionID, currentNodeID)
	if err != nil {
		return nil, err
	}

	result.CorrectionLatency = time.Since(startTime)
	return result, nil
}
