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
	"time"
)

// MetadataStore is the extension point for shared session metadata storage.
//
// Implementations may use Redis, etcd, a relational database, or any other
// distributed store capable of atomic read-modify-write operations.
//
// Backend implementations are provided in sub-packages:
//   - sseha/redis — Redis-based implementation
//
// All methods must be safe for concurrent use from multiple goroutines and
// multiple cluster nodes.
type MetadataStore interface {
	// RegisterSession creates a new session entry in the store.
	// Returns ErrSessionAlreadyExists if a session with the same ID already exists.
	RegisterSession(ctx context.Context, info *SessionInfo) error

	// GetSession retrieves session metadata by ID.
	// Returns nil and no error if the session does not exist.
	GetSession(ctx context.Context, sessionID string) (*SessionInfo, error)

	// UpdateSession atomically updates session metadata.
	// The update is conditional on the version field (optimistic concurrency):
	// if the stored version differs from info.Version, the update fails with
	// ErrVersionConflict.
	UpdateSession(ctx context.Context, info *SessionInfo) error

	// DeleteSession removes a session entry.
	DeleteSession(ctx context.Context, sessionID string) error

	// ListSessions queries sessions matching the given filter criteria.
	ListSessions(ctx context.Context, filter *SessionFilter) ([]*SessionInfo, error)

	// AcquireSessionLock attempts to acquire an exclusive lock on a session
	// for migration purposes. The lock auto-expires after ttl.
	// Returns true if the lock was acquired, false if it's held by another node.
	AcquireSessionLock(ctx context.Context, sessionID string, nodeID string, ttl time.Duration) (bool, error)

	// ReleaseSessionLock releases a previously acquired session lock.
	ReleaseSessionLock(ctx context.Context, sessionID string, nodeID string) error

	// RegisterNode registers or updates a node's information in the cluster registry.
	RegisterNode(ctx context.Context, node *NodeInfo) error

	// GetNode retrieves a node's information by ID.
	GetNode(ctx context.Context, nodeID string) (*NodeInfo, error)

	// ListNodes returns all registered nodes, optionally filtered by liveness.
	// If aliveOnly is true, only nodes with a heartbeat within the timeout are returned.
	ListNodes(ctx context.Context, aliveOnly bool, heartbeatTimeout time.Duration) ([]*NodeInfo, error)

	// RemoveNode removes a node from the cluster registry.
	RemoveNode(ctx context.Context, nodeID string) error

	// SetBarrier creates a migration barrier token for a session.
	SetBarrier(ctx context.Context, barrier *BarrierToken) error

	// GetBarrier retrieves the current barrier token for a session.
	GetBarrier(ctx context.Context, sessionID string) (*BarrierToken, error)

	// ReleaseBarrier marks a barrier as released, allowing the new node to
	// proceed with session processing.
	ReleaseBarrier(ctx context.Context, sessionID string) error

	// Close releases any resources held by the store.
	Close() error
}

// EventBus is the extension point for the distributed event pub/sub system.
//
// It enables SSE events to be forwarded across cluster nodes so that any node
// can serve any session, regardless of which node originally produced the event.
//
// Backend implementations are provided in sub-packages:
//   - sseha/redis — Redis Pub/Sub-based implementation
//
// All methods must be safe for concurrent use.
type EventBus interface {
	// Publish sends an SSE event to the event bus.
	// The event is delivered to all subscribers of the session's channel.
	Publish(ctx context.Context, event *SSEEvent) error

	// Subscribe creates a subscription for events of a specific session.
	// The returned channel receives events as they arrive. The subscription
	// remains active until Unsubscribe is called or the context is cancelled.
	Subscribe(ctx context.Context, sessionID string) (<-chan *SSEEvent, error)

	// Unsubscribe removes a subscription for a specific session.
	Unsubscribe(ctx context.Context, sessionID string) error

	// SubscribeAll creates a subscription for events of all sessions on this
	// event bus. This is useful for monitoring or debugging.
	SubscribeAll(ctx context.Context) (<-chan *SSEEvent, error)

	// Close releases resources and closes all subscriptions.
	Close() error
}

// SessionCorrector is the extension point for the session correction (纠偏)
// strategy. It decides how to handle sessions that need correction — for
// example, when the owning node goes down or a reconnecting client is
// routed to a different node.
//
// Two correction patterns are supported:
//
//  1. Mesh/Long-connection auto-correction (via shared metadata):
//     Detects orphaned sessions and migrates them to a healthy node.
//
//  2. Pub/Sub forwarding correction:
//     On reconnection to a new node, subscribes to event bus for seamless
//     event forwarding without migrating the producer.
type SessionCorrector interface {
	// DetectAnomalies checks for sessions that need correction.
	// This is called periodically by the SessionManager.
	DetectAnomalies(ctx context.Context) ([]*SessionInfo, error)

	// CorrectSession performs the correction for a single session.
	// This may involve migrating the session to the current node, replaying
	// missed events, and establishing a forwarding subscription.
	CorrectSession(ctx context.Context, sessionID string, targetNodeID string) (*SessionCorrectionResult, error)

	// HandleReconnection is called when a client reconnects (possibly to a
	// different node) with a Last-Event-ID header. It determines whether to
	// replay events, migrate the session, or reject the reconnection.
	HandleReconnection(ctx context.Context, sessionID string, lastEventID string, currentNodeID string) (*SessionCorrectionResult, error)
}

// NodeDiscovery is the extension point for cluster membership and node
// discovery. It allows the session manager to know which nodes are alive
// and route messages appropriately.
//
// Backend implementations are provided in sub-packages:
//   - sseha/redis — Redis-based implementation with heartbeat detection
type NodeDiscovery interface {
	// Register registers this node in the cluster.
	Register(ctx context.Context, node *NodeInfo) error

	// Deregister removes this node from the cluster.
	Deregister(ctx context.Context, nodeID string) error

	// Heartbeat sends a heartbeat signal indicating this node is alive.
	Heartbeat(ctx context.Context, nodeID string) error

	// GetAliveNodes returns the list of currently alive nodes.
	GetAliveNodes(ctx context.Context) ([]*NodeInfo, error)

	// IsNodeAlive checks if a specific node is considered alive.
	IsNodeAlive(ctx context.Context, nodeID string) (bool, error)

	// OnNodeJoin registers a callback invoked when a new node joins.
	OnNodeJoin(callback func(node *NodeInfo))

	// OnNodeLeave registers a callback invoked when a node leaves or is
	// detected as dead.
	OnNodeLeave(callback func(node *NodeInfo))

	// Close stops the discovery mechanism and releases resources.
	Close() error
}

// P2PForwarder is the extension point for direct node-to-node message
// forwarding. When a session's owning node receives a request for a session
// it doesn't own, it can forward the request to the correct node.
//
// This is an optional extension; if not provided, events are forwarded only
// via the EventBus.
type P2PForwarder interface {
	// Forward sends a message directly to a specific node.
	Forward(ctx context.Context, targetNodeID string, event *SSEEvent) error

	// SetReceiveHandler sets the handler invoked when this node receives a
	// forwarded message from another node.
	SetReceiveHandler(handler func(ctx context.Context, event *SSEEvent) error)

	// Close stops the forwarder and releases resources.
	Close() error
}
