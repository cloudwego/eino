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

// Package sseha provides optional high-availability patterns for stateful
// streaming (SSE) sessions in distributed MCP deployments.
//
// This implementation follows SEP-2001 (Optional High Availability Patterns
// for Stateful Streaming in MCP Deployments) and provides:
//   - Shared metadata store for session registry and ownership tracking
//   - Pub/Sub event bus for SSE event forwarding across nodes
//   - Automatic session correction (纠偏) in mesh/long-connection topologies
//   - Extension point abstractions for pluggable backends
//
// The core package defines interfaces only. Backend implementations (Redis,
// etcd, etc.) are provided in sub-packages:
//   - sseha/redis — Redis backend for MetadataStore, EventBus, and NodeDiscovery
package sseha

import (
	"fmt"
	"sync"
	"time"
)

// SessionState represents the lifecycle state of an SSE session.
type SessionState int

const (
	// SessionStateActive indicates the session is actively connected and streaming.
	SessionStateActive SessionState = iota

	// SessionStateSuspended indicates the session lost its connection but can be resumed.
	SessionStateSuspended

	// SessionStateMigrating indicates the session is being migrated to another node.
	SessionStateMigrating

	// SessionStateClosed indicates the session has been terminated.
	SessionStateClosed
)

func (s SessionState) String() string {
	switch s {
	case SessionStateActive:
		return "active"
	case SessionStateSuspended:
		return "suspended"
	case SessionStateMigrating:
		return "migrating"
	case SessionStateClosed:
		return "closed"
	default:
		return fmt.Sprintf("unknown(%d)", int(s))
	}
}

// SessionInfo holds metadata about an SSE session, stored in the shared
// metadata store for cluster-wide visibility.
type SessionInfo struct {
	// SessionID is the globally unique identifier for this session.
	SessionID string `json:"session_id"`

	// NodeID identifies the cluster node currently owning (serving) this session.
	NodeID string `json:"node_id"`

	// State is the current lifecycle state.
	State SessionState `json:"state"`

	// CreatedAt is when the session was first established.
	CreatedAt time.Time `json:"created_at"`

	// LastActiveAt is the last time the session received or sent data.
	LastActiveAt time.Time `json:"last_active_at"`

	// LastEventID is the ID of the last SSE event successfully delivered to the
	// client. Used for resumption — when a client reconnects, events after this
	// ID are replayed.
	LastEventID string `json:"last_event_id"`

	// Metadata carries arbitrary key-value pairs for application-specific data
	// (e.g. partition hints, affinity tags).
	Metadata map[string]string `json:"metadata,omitempty"`

	// Version is an optimistic-concurrency version counter, incremented on
	// every metadata update. Used to prevent stale writes during migration.
	Version int64 `json:"version"`
}

// SSEEvent represents a single server-sent event that flows through the
// event bus for cross-node forwarding.
type SSEEvent struct {
	// SessionID identifies which session this event belongs to.
	SessionID string `json:"session_id"`

	// EventID is the monotonically increasing event identifier within a session.
	EventID string `json:"event_id"`

	// EventType is the SSE event type field (e.g. "message", "error").
	EventType string `json:"event_type"`

	// Data is the event payload.
	Data []byte `json:"data"`

	// SourceNodeID is the node that originally produced this event.
	SourceNodeID string `json:"source_node_id"`

	// Timestamp records when the event was produced.
	Timestamp time.Time `json:"timestamp"`
}

// EventBuffer provides an ordered, bounded buffer of SSE events for a single
// session, enabling replay on reconnection. It is safe for concurrent use.
type EventBuffer struct {
	mu       sync.RWMutex
	events   []SSEEvent
	capacity int
	// index maps eventID -> position in the events slice for O(1) lookup.
	index map[string]int
}

// NewEventBuffer creates a buffer that retains up to capacity events.
func NewEventBuffer(capacity int) *EventBuffer {
	if capacity <= 0 {
		capacity = 1000
	}
	return &EventBuffer{
		events:   make([]SSEEvent, 0, capacity),
		capacity: capacity,
		index:    make(map[string]int, capacity),
	}
}

// Append adds an event to the buffer. If the buffer is full, the oldest event
// is evicted.
func (eb *EventBuffer) Append(event SSEEvent) {
	eb.mu.Lock()
	defer eb.mu.Unlock()

	if len(eb.events) >= eb.capacity {
		// Evict the oldest event
		oldest := eb.events[0]
		delete(eb.index, oldest.EventID)
		eb.events = eb.events[1:]
		// Re-index after shift
		for id, idx := range eb.index {
			eb.index[id] = idx - 1
		}
	}

	eb.index[event.EventID] = len(eb.events)
	eb.events = append(eb.events, event)
}

// EventsAfter returns all events after the given eventID (exclusive).
// If eventID is empty, all buffered events are returned.
// If eventID is not found, nil and false are returned.
func (eb *EventBuffer) EventsAfter(eventID string) ([]SSEEvent, bool) {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	if eventID == "" {
		result := make([]SSEEvent, len(eb.events))
		copy(result, eb.events)
		return result, true
	}

	idx, ok := eb.index[eventID]
	if !ok {
		return nil, false
	}

	start := idx + 1
	if start >= len(eb.events) {
		return nil, true
	}

	result := make([]SSEEvent, len(eb.events)-start)
	copy(result, eb.events[start:])
	return result, true
}

// LastEventID returns the ID of the most recent event, or empty if the buffer
// is empty.
func (eb *EventBuffer) LastEventID() string {
	eb.mu.RLock()
	defer eb.mu.RUnlock()

	if len(eb.events) == 0 {
		return ""
	}
	return eb.events[len(eb.events)-1].EventID
}

// Len returns the number of events in the buffer.
func (eb *EventBuffer) Len() int {
	eb.mu.RLock()
	defer eb.mu.RUnlock()
	return len(eb.events)
}

// SessionCorrectionResult describes the outcome of a session correction
// (纠偏) operation.
type SessionCorrectionResult struct {
	// SessionID is the corrected session.
	SessionID string

	// PreviousNodeID is the node that previously owned the session.
	PreviousNodeID string

	// NewNodeID is the node that now owns the session.
	NewNodeID string

	// ReplayedEvents is the number of events replayed to the new connection.
	ReplayedEvents int

	// CorrectionLatency measures the time from detection to completion.
	CorrectionLatency time.Duration
}

// SessionFilter provides criteria for querying sessions from the metadata store.
type SessionFilter struct {
	// NodeID filters sessions owned by a specific node. Empty means all nodes.
	NodeID string

	// States filters by session state. Empty means all states.
	States []SessionState

	// OlderThan filters sessions whose LastActiveAt is older than this duration.
	OlderThan time.Duration

	// Limit caps the number of results. 0 means no limit.
	Limit int
}

// CorrectionPolicy configures how session corrections are triggered and executed.
type CorrectionPolicy struct {
	// DetectionInterval is how often the manager checks for sessions needing
	// correction (e.g. the owning node is unreachable).
	DetectionInterval time.Duration

	// SuspendTimeout is how long a session can be in suspended state before
	// it's considered for correction/migration.
	SuspendTimeout time.Duration

	// MigrationTimeout is the maximum time allowed for a migration operation.
	MigrationTimeout time.Duration

	// MaxReplayEvents caps the number of events replayed on reconnection.
	// If a client's LastEventID is too far behind, the session is reset.
	MaxReplayEvents int

	// EnableAutoCorrection enables background goroutine that periodically
	// detects and corrects orphaned or misrouted sessions.
	EnableAutoCorrection bool
}

// DefaultCorrectionPolicy returns sensible defaults.
func DefaultCorrectionPolicy() *CorrectionPolicy {
	return &CorrectionPolicy{
		DetectionInterval:    5 * time.Second,
		SuspendTimeout:       30 * time.Second,
		MigrationTimeout:     10 * time.Second,
		MaxReplayEvents:      1000,
		EnableAutoCorrection: true,
	}
}

// NodeInfo represents a cluster node participating in HA.
type NodeInfo struct {
	// NodeID is the unique identifier for this node.
	NodeID string `json:"node_id"`

	// Address is the network address (host:port) for P2P forwarding.
	Address string `json:"address"`

	// LastHeartbeat is when this node last reported itself as alive.
	LastHeartbeat time.Time `json:"last_heartbeat"`

	// ActiveSessions is the count of sessions currently owned by this node.
	ActiveSessions int `json:"active_sessions"`

	// Metadata carries node-specific attributes (e.g. region, zone).
	Metadata map[string]string `json:"metadata,omitempty"`
}

// HeartbeatConfig configures node heartbeat behavior.
type HeartbeatConfig struct {
	// Interval is how often this node publishes its heartbeat.
	Interval time.Duration

	// Timeout is how long since last heartbeat before a node is considered dead.
	Timeout time.Duration
}

// DefaultHeartbeatConfig returns sensible defaults.
func DefaultHeartbeatConfig() *HeartbeatConfig {
	return &HeartbeatConfig{
		Interval: 3 * time.Second,
		Timeout:  10 * time.Second,
	}
}

// BarrierToken is used during session migration to establish happens-before
// ordering between the old and new node. After migration, the new node must
// wait for the barrier to be released before processing the session.
type BarrierToken struct {
	SessionID string    `json:"session_id"`
	FromNode  string    `json:"from_node"`
	ToNode    string    `json:"to_node"`
	CreatedAt time.Time `json:"created_at"`
	Released  bool      `json:"released"`
}

// CorrectionCallback is invoked when a session correction event occurs.
// Implementations can use this for logging, metrics, or custom logic.
type CorrectionCallback func(result *SessionCorrectionResult)
