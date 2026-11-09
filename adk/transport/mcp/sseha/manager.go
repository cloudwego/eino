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
	"log"
	"sync"
	"time"
)

// SessionManagerConfig holds all configuration for the HA session manager.
type SessionManagerConfig struct {
	// NodeID is the unique identifier for the current node.
	// Must be unique across the cluster.
	NodeID string

	// NodeAddress is the network address of this node (for P2P forwarding).
	NodeAddress string

	// MetadataStore is the backend for shared session metadata.
	// Required. Implementations are provided in sub-packages (e.g. sseha/redis).
	MetadataStore MetadataStore

	// EventBus is the backend for cross-node event forwarding.
	// Required. Implementations are provided in sub-packages (e.g. sseha/redis).
	EventBus EventBus

	// Corrector is the strategy for detecting and correcting session anomalies.
	// If nil, a DefaultSessionCorrector is created using MetadataStore and EventBus.
	Corrector SessionCorrector

	// Forwarder is the optional P2P message forwarder.
	// If nil, events are forwarded only via the event bus.
	Forwarder P2PForwarder

	// CorrectionPolicy configures correction timing and limits.
	// If nil, DefaultCorrectionPolicy() is used.
	CorrectionPolicy *CorrectionPolicy

	// HeartbeatConfig configures node heartbeat behavior.
	// If nil, DefaultHeartbeatConfig() is used.
	HeartbeatConfig *HeartbeatConfig

	// EventBufferCapacity is the number of events to buffer per session for
	// replay on reconnection. Default: 1000.
	EventBufferCapacity int

	// OnCorrection is called whenever a session correction is performed.
	OnCorrection CorrectionCallback

	// Logger is used for logging. If nil, the standard log package is used.
	Logger Logger

	// NodeMetadata carries extra attributes for this node (region, zone, etc.).
	NodeMetadata map[string]string
}

// Logger is a minimal logging interface.
type Logger interface {
	Infof(format string, args ...any)
	Warnf(format string, args ...any)
	Errorf(format string, args ...any)
}

// defaultLogger wraps the standard log package.
type defaultLogger struct{}

func (l *defaultLogger) Infof(format string, args ...any) {
	log.Printf("[INFO] "+format, args...)
}
func (l *defaultLogger) Warnf(format string, args ...any) {
	log.Printf("[WARN] "+format, args...)
}
func (l *defaultLogger) Errorf(format string, args ...any) {
	log.Printf("[ERROR] "+format, args...)
}

// SessionManager is the main coordinator for HA SSE sessions.
// It manages session lifecycle, event buffering, cross-node forwarding,
// and automatic correction (纠偏).
//
// SessionManager depends only on the MetadataStore and EventBus interfaces.
// Concrete backend implementations (Redis, etcd, etc.) are injected via
// configuration.
type SessionManager struct {
	nodeID      string
	nodeAddress string

	store     MetadataStore
	bus       EventBus
	corrector SessionCorrector
	forwarder P2PForwarder

	policy          *CorrectionPolicy
	heartbeatConfig *HeartbeatConfig
	bufferCapacity  int

	// localBuffers maps sessionID -> EventBuffer for sessions owned by this node.
	localBuffers sync.Map // map[string]*EventBuffer

	// localSessions tracks sessions currently active on this node.
	localSessions sync.Map // map[string]*localSessionState

	onCorrection CorrectionCallback
	logger       Logger

	cancelFunc context.CancelFunc
	wg         sync.WaitGroup
	closed     int32
	closeMu    sync.Mutex
}

// localSessionState holds per-session state local to this node.
type localSessionState struct {
	info      *SessionInfo
	buffer    *EventBuffer
	eventSub  <-chan *SSEEvent
	cancelSub context.CancelFunc
	mu        sync.Mutex
}

// NewSessionManager creates and starts a new HA session manager.
func NewSessionManager(config *SessionManagerConfig) (*SessionManager, error) {
	if config.NodeID == "" {
		return nil, fmt.Errorf("sseha: NodeID is required")
	}
	if config.MetadataStore == nil {
		return nil, fmt.Errorf("sseha: MetadataStore is required")
	}
	if config.EventBus == nil {
		return nil, fmt.Errorf("sseha: EventBus is required")
	}

	logger := config.Logger
	if logger == nil {
		logger = &defaultLogger{}
	}

	policy := config.CorrectionPolicy
	if policy == nil {
		policy = DefaultCorrectionPolicy()
	}

	hbConfig := config.HeartbeatConfig
	if hbConfig == nil {
		hbConfig = DefaultHeartbeatConfig()
	}

	bufCap := config.EventBufferCapacity
	if bufCap <= 0 {
		bufCap = 1000
	}

	m := &SessionManager{
		nodeID:          config.NodeID,
		nodeAddress:     config.NodeAddress,
		store:           config.MetadataStore,
		bus:             config.EventBus,
		forwarder:       config.Forwarder,
		policy:          policy,
		heartbeatConfig: hbConfig,
		bufferCapacity:  bufCap,
		onCorrection:    config.OnCorrection,
		logger:          logger,
	}

	// Set up P2P forwarder receive handler if available
	if m.forwarder != nil {
		m.forwarder.SetReceiveHandler(m.handleForwardedEvent)
	}

	// Create corrector if not provided
	if config.Corrector != nil {
		m.corrector = config.Corrector
	} else {
		m.corrector = NewDefaultSessionCorrector(m)
	}

	return m, nil
}

// Start begins background goroutines for heartbeat, correction, etc.
func (m *SessionManager) Start(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	m.cancelFunc = cancel

	// Register this node
	node := &NodeInfo{
		NodeID:        m.nodeID,
		Address:       m.nodeAddress,
		LastHeartbeat: time.Now(),
	}
	if err := m.store.RegisterNode(ctx, node); err != nil {
		cancel()
		return fmt.Errorf("register node: %w", err)
	}

	// Start heartbeat
	m.wg.Add(1)
	go m.heartbeatLoop(ctx)

	// Start automatic correction if enabled
	if m.policy.EnableAutoCorrection {
		m.wg.Add(1)
		go m.correctionLoop(ctx)
	}

	m.logger.Infof("SessionManager started on node %s", m.nodeID)
	return nil
}

// CreateSession creates a new SSE session owned by the current node.
func (m *SessionManager) CreateSession(ctx context.Context, sessionID string, metadata map[string]string) (*SessionInfo, error) {
	now := time.Now()
	info := &SessionInfo{
		SessionID:    sessionID,
		NodeID:       m.nodeID,
		State:        SessionStateActive,
		CreatedAt:    now,
		LastActiveAt: now,
		Metadata:     metadata,
		Version:      1,
	}

	if err := m.store.RegisterSession(ctx, info); err != nil {
		return nil, err
	}

	// Create local state
	buffer := NewEventBuffer(m.bufferCapacity)
	localState := &localSessionState{
		info:   info,
		buffer: buffer,
	}
	m.localSessions.Store(sessionID, localState)
	m.localBuffers.Store(sessionID, buffer)

	// Subscribe to events for this session from the event bus
	subCtx, subCancel := context.WithCancel(ctx)
	eventCh, err := m.bus.Subscribe(subCtx, sessionID)
	if err != nil {
		subCancel()
		m.logger.Warnf("Failed to subscribe to session %s events: %v", sessionID, err)
	} else {
		localState.mu.Lock()
		localState.eventSub = eventCh
		localState.cancelSub = subCancel
		localState.mu.Unlock()

		// Forward bus events to buffer
		m.wg.Add(1)
		go m.bufferBusEvents(subCtx, sessionID, eventCh)
	}

	m.logger.Infof("Session %s created on node %s", sessionID, m.nodeID)
	return info, nil
}

// PublishEvent publishes an SSE event for the given session.
// The event is stored in the local buffer and broadcast via the event bus.
func (m *SessionManager) PublishEvent(ctx context.Context, event *SSEEvent) error {
	event.SourceNodeID = m.nodeID
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now()
	}

	// Buffer locally
	if bufI, ok := m.localBuffers.Load(event.SessionID); ok {
		buf := bufI.(*EventBuffer)
		buf.Append(*event)
	}

	// Broadcast via event bus for other nodes
	if err := m.bus.Publish(ctx, event); err != nil {
		m.logger.Warnf("Failed to publish event %s for session %s: %v",
			event.EventID, event.SessionID, err)
		return err
	}

	// Update last active timestamp
	go m.touchSession(context.Background(), event.SessionID, event.EventID)

	return nil
}

// HandleReconnection handles a client reconnecting (possibly to a different node)
// with a Last-Event-ID. It returns the events to replay and performs session
// correction if necessary.
func (m *SessionManager) HandleReconnection(ctx context.Context, sessionID string, lastEventID string) ([]SSEEvent, error) {
	info, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrSessionNotFound
	}

	if info.State == SessionStateClosed {
		return nil, ErrSessionClosed
	}

	// Case 1: Session is on this node — just replay from buffer
	if info.NodeID == m.nodeID {
		return m.replayFromBuffer(sessionID, lastEventID)
	}

	// Case 2: Session is on another node — need correction (纠偏)
	m.logger.Infof("Session %s correction triggered: owned by %s, reconnecting to %s",
		sessionID, info.NodeID, m.nodeID)

	result, err := m.corrector.HandleReconnection(ctx, sessionID, lastEventID, m.nodeID)
	if err != nil {
		return nil, fmt.Errorf("session correction failed: %w", err)
	}

	if m.onCorrection != nil {
		m.onCorrection(result)
	}

	// After correction, replay from the now-local buffer
	return m.replayFromBuffer(sessionID, lastEventID)
}

// SuspendSession marks a session as suspended (client disconnected).
func (m *SessionManager) SuspendSession(ctx context.Context, sessionID string) error {
	info, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if info == nil {
		return ErrSessionNotFound
	}

	info.State = SessionStateSuspended
	info.LastActiveAt = time.Now()

	return m.store.UpdateSession(ctx, info)
}

// CloseSession terminates a session and cleans up resources.
func (m *SessionManager) CloseSession(ctx context.Context, sessionID string) error {
	// Clean up local state
	if stateI, ok := m.localSessions.LoadAndDelete(sessionID); ok {
		state := stateI.(*localSessionState)
		state.mu.Lock()
		if state.cancelSub != nil {
			state.cancelSub()
		}
		state.mu.Unlock()
	}
	m.localBuffers.Delete(sessionID)

	// Unsubscribe from event bus
	_ = m.bus.Unsubscribe(ctx, sessionID)

	// Update metadata store
	info, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if info == nil {
		return nil
	}

	info.State = SessionStateClosed
	info.LastActiveAt = time.Now()
	if err := m.store.UpdateSession(ctx, info); err != nil {
		// If update fails, try to delete
		return m.store.DeleteSession(ctx, sessionID)
	}

	m.logger.Infof("Session %s closed on node %s", sessionID, m.nodeID)
	return nil
}

// MigrateSession migrates a session from the current node to a target node.
// This is the sender-side of migration.
func (m *SessionManager) MigrateSession(ctx context.Context, sessionID string, targetNodeID string) (*SessionCorrectionResult, error) {
	startTime := time.Now()

	// Acquire migration lock
	acquired, err := m.store.AcquireSessionLock(ctx, sessionID, m.nodeID, m.policy.MigrationTimeout)
	if err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired {
		return nil, ErrSessionLocked
	}
	defer func() {
		_ = m.store.ReleaseSessionLock(ctx, sessionID, m.nodeID)
	}()

	info, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	if info == nil {
		return nil, ErrSessionNotFound
	}

	previousNodeID := info.NodeID

	// Set barrier to prevent race conditions during migration
	barrier := &BarrierToken{
		SessionID: sessionID,
		FromNode:  previousNodeID,
		ToNode:    targetNodeID,
		CreatedAt: time.Now(),
		Released:  false,
	}
	if err := m.store.SetBarrier(ctx, barrier); err != nil {
		return nil, fmt.Errorf("set barrier: %w", err)
	}

	// Update session ownership
	info.State = SessionStateMigrating
	info.NodeID = targetNodeID
	info.LastActiveAt = time.Now()
	if err := m.store.UpdateSession(ctx, info); err != nil {
		return nil, fmt.Errorf("update session for migration: %w", err)
	}

	// Get events to replay
	var replayCount int
	if bufI, ok := m.localBuffers.Load(sessionID); ok {
		buf := bufI.(*EventBuffer)
		events, found := buf.EventsAfter(info.LastEventID)
		if found {
			// Forward buffered events via the event bus so the target node picks them up
			for _, event := range events {
				eventCopy := event
				if err := m.bus.Publish(ctx, &eventCopy); err != nil {
					m.logger.Warnf("Failed to forward buffered event during migration: %v", err)
				}
				replayCount++
			}
		}
	}

	// Release barrier — target node can now process
	if err := m.store.ReleaseBarrier(ctx, sessionID); err != nil {
		m.logger.Warnf("Failed to release barrier for session %s: %v", sessionID, err)
	}

	// Clean up local state
	if stateI, ok := m.localSessions.LoadAndDelete(sessionID); ok {
		state := stateI.(*localSessionState)
		state.mu.Lock()
		if state.cancelSub != nil {
			state.cancelSub()
		}
		state.mu.Unlock()
	}
	m.localBuffers.Delete(sessionID)

	// Finalize session state at new node
	info.State = SessionStateActive
	_ = m.store.UpdateSession(ctx, info)

	result := &SessionCorrectionResult{
		SessionID:         sessionID,
		PreviousNodeID:    previousNodeID,
		NewNodeID:         targetNodeID,
		ReplayedEvents:    replayCount,
		CorrectionLatency: time.Since(startTime),
	}

	m.logger.Infof("Session %s migrated from %s to %s (%d events replayed, latency %v)",
		sessionID, previousNodeID, targetNodeID, replayCount, result.CorrectionLatency)

	return result, nil
}

// AcceptMigratedSession is the receiver-side of session migration.
// It sets up local state for a session migrating to this node.
func (m *SessionManager) AcceptMigratedSession(ctx context.Context, sessionID string) error {
	// Wait for barrier to be released
	for i := 0; i < 50; i++ { // up to 5 seconds with 100ms intervals
		barrier, err := m.store.GetBarrier(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("get barrier: %w", err)
		}
		if barrier == nil || barrier.Released {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	info, err := m.store.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}
	if info == nil {
		return ErrSessionNotFound
	}

	// Create local state
	buffer := NewEventBuffer(m.bufferCapacity)
	localState := &localSessionState{
		info:   info,
		buffer: buffer,
	}
	m.localSessions.Store(sessionID, localState)
	m.localBuffers.Store(sessionID, buffer)

	// Subscribe to events for this session
	subCtx, subCancel := context.WithCancel(ctx)
	eventCh, err := m.bus.Subscribe(subCtx, sessionID)
	if err != nil {
		subCancel()
		m.logger.Warnf("Failed to subscribe to migrated session %s: %v", sessionID, err)
	} else {
		localState.mu.Lock()
		localState.eventSub = eventCh
		localState.cancelSub = subCancel
		localState.mu.Unlock()

		m.wg.Add(1)
		go m.bufferBusEvents(subCtx, sessionID, eventCh)
	}

	m.logger.Infof("Accepted migrated session %s on node %s", sessionID, m.nodeID)
	return nil
}

// GetLocalSession returns the session info if it's active on this node.
func (m *SessionManager) GetLocalSession(sessionID string) (*SessionInfo, bool) {
	stateI, ok := m.localSessions.Load(sessionID)
	if !ok {
		return nil, false
	}
	return stateI.(*localSessionState).info, true
}

// GetEventBuffer returns the event buffer for a local session.
func (m *SessionManager) GetEventBuffer(sessionID string) (*EventBuffer, bool) {
	bufI, ok := m.localBuffers.Load(sessionID)
	if !ok {
		return nil, false
	}
	return bufI.(*EventBuffer), true
}

// NodeID returns this manager's node identifier.
func (m *SessionManager) NodeID() string {
	return m.nodeID
}

// Store returns the metadata store.
func (m *SessionManager) Store() MetadataStore {
	return m.store
}

// Bus returns the event bus.
func (m *SessionManager) Bus() EventBus {
	return m.bus
}

// Policy returns the correction policy.
func (m *SessionManager) Policy() *CorrectionPolicy {
	return m.policy
}

// HeartbeatTimeout returns the configured heartbeat timeout.
func (m *SessionManager) HeartbeatTimeout() time.Duration {
	return m.heartbeatConfig.Timeout
}

// BufferCapacity returns the configured event buffer capacity.
func (m *SessionManager) BufferCapacity() int {
	return m.bufferCapacity
}

// Close shuts down the session manager, cleaning up all resources.
func (m *SessionManager) Close(ctx context.Context) error {
	m.closeMu.Lock()
	defer m.closeMu.Unlock()

	// First, cancel all local session subscriptions so their goroutines can exit.
	m.localSessions.Range(func(key, value any) bool {
		sessionID := key.(string)
		state := value.(*localSessionState)
		state.mu.Lock()
		if state.cancelSub != nil {
			state.cancelSub()
		}
		state.mu.Unlock()
		// Also unsubscribe from the event bus to unblock forwardMessages goroutines.
		_ = m.bus.Unsubscribe(ctx, sessionID)
		m.localSessions.Delete(sessionID)
		return true
	})

	// Cancel the manager's own context (heartbeat, correction loops).
	if m.cancelFunc != nil {
		m.cancelFunc()
	}

	// Wait for all goroutines to finish.
	m.wg.Wait()

	// Deregister node
	_ = m.store.RemoveNode(ctx, m.nodeID)

	if m.forwarder != nil {
		_ = m.forwarder.Close()
	}

	m.logger.Infof("SessionManager on node %s shut down", m.nodeID)
	return nil
}

// ---- Internal methods ----

// SetupLocalSession sets up local state for a session on this node.
// This is used internally by the corrector after migrating a session.
func (m *SessionManager) SetupLocalSession(ctx context.Context, sessionID string, info *SessionInfo) {
	buffer := NewEventBuffer(m.bufferCapacity)
	localState := &localSessionState{
		info:   info,
		buffer: buffer,
	}
	m.localSessions.Store(sessionID, localState)
	m.localBuffers.Store(sessionID, buffer)

	// Subscribe to event bus for this session to receive forwarded events
	subCtx, subCancel := context.WithCancel(ctx)
	eventCh, err := m.bus.Subscribe(subCtx, sessionID)
	if err != nil {
		subCancel()
		m.logger.Warnf("Failed to subscribe during correction: %v", err)
	} else {
		localState.mu.Lock()
		localState.eventSub = eventCh
		localState.cancelSub = subCancel
		localState.mu.Unlock()

		m.wg.Add(1)
		go m.bufferBusEvents(subCtx, sessionID, eventCh)
	}
}

func (m *SessionManager) replayFromBuffer(sessionID string, lastEventID string) ([]SSEEvent, error) {
	bufI, ok := m.localBuffers.Load(sessionID)
	if !ok {
		return nil, nil
	}

	buf := bufI.(*EventBuffer)
	events, found := buf.EventsAfter(lastEventID)
	if !found {
		return nil, ErrEventGap
	}

	// Cap replay
	if m.policy.MaxReplayEvents > 0 && len(events) > m.policy.MaxReplayEvents {
		return nil, ErrEventGap
	}

	return events, nil
}

func (m *SessionManager) touchSession(ctx context.Context, sessionID string, lastEventID string) {
	info, err := m.store.GetSession(ctx, sessionID)
	if err != nil || info == nil {
		return
	}

	info.LastActiveAt = time.Now()
	if lastEventID != "" {
		info.LastEventID = lastEventID
	}

	_ = m.store.UpdateSession(ctx, info)
}

func (m *SessionManager) bufferBusEvents(ctx context.Context, sessionID string, eventCh <-chan *SSEEvent) {
	defer m.wg.Done()

	for {
		select {
		case <-ctx.Done():
			return
		case event, ok := <-eventCh:
			if !ok {
				return
			}

			// Don't re-buffer events we produced ourselves
			if event.SourceNodeID == m.nodeID {
				continue
			}

			if bufI, ok := m.localBuffers.Load(sessionID); ok {
				buf := bufI.(*EventBuffer)
				buf.Append(*event)
			}
		}
	}
}

func (m *SessionManager) handleForwardedEvent(ctx context.Context, event *SSEEvent) error {
	// Buffer the forwarded event
	if bufI, ok := m.localBuffers.Load(event.SessionID); ok {
		buf := bufI.(*EventBuffer)
		buf.Append(*event)
	}
	return nil
}

func (m *SessionManager) heartbeatLoop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.heartbeatConfig.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// Count local sessions
			sessionCount := 0
			m.localSessions.Range(func(_, _ any) bool {
				sessionCount++
				return true
			})

			node := &NodeInfo{
				NodeID:         m.nodeID,
				Address:        m.nodeAddress,
				LastHeartbeat:  time.Now(),
				ActiveSessions: sessionCount,
			}
			if err := m.store.RegisterNode(ctx, node); err != nil {
				m.logger.Warnf("Heartbeat failed: %v", err)
			}
		}
	}
}

func (m *SessionManager) correctionLoop(ctx context.Context) {
	defer m.wg.Done()

	ticker := time.NewTicker(m.policy.DetectionInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			anomalies, err := m.corrector.DetectAnomalies(ctx)
			if err != nil {
				m.logger.Warnf("Anomaly detection failed: %v", err)
				continue
			}

			for _, info := range anomalies {
				result, err := m.corrector.CorrectSession(ctx, info.SessionID, m.nodeID)
				if err != nil {
					m.logger.Warnf("Session correction failed for %s: %v", info.SessionID, err)
					continue
				}

				if m.onCorrection != nil {
					m.onCorrection(result)
				}
			}
		}
	}
}
