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

package redis

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk/transport/mcp/sseha"
)

// NodeDiscoveryConfig configures the Redis-based node discovery.
type NodeDiscoveryConfig struct {
	// Client is the Redis client to use.
	Client Client

	// Store is the MetadataStore used for node registration.
	// Must be a Redis-based MetadataStore or any sseha.MetadataStore implementation.
	Store sseha.MetadataStore

	// KeyPrefix is prepended to all Redis keys.
	// Default: "eino:sseha:"
	KeyPrefix string

	// HeartbeatInterval is how often to publish heartbeat.
	// Default: 3s.
	HeartbeatInterval time.Duration

	// HeartbeatTimeout is how long before a node is considered dead.
	// Default: 10s.
	HeartbeatTimeout time.Duration

	// CheckInterval is how often to check for dead/new nodes.
	// Default: 5s.
	CheckInterval time.Duration

	// NodeEventChannel is the Redis pub/sub channel for node join/leave events.
	// Default: "eino:sseha:node_events"
	NodeEventChannel string
}

// DefaultNodeDiscoveryConfig returns sensible defaults.
func DefaultNodeDiscoveryConfig() *NodeDiscoveryConfig {
	return &NodeDiscoveryConfig{
		KeyPrefix:         "eino:sseha:",
		HeartbeatInterval: 3 * time.Second,
		HeartbeatTimeout:  10 * time.Second,
		CheckInterval:     5 * time.Second,
		NodeEventChannel:  "eino:sseha:node_events",
	}
}

// nodeEvent is published on the node event channel.
type nodeEvent struct {
	Type string          `json:"type"` // "join" or "leave"
	Node *sseha.NodeInfo `json:"node"`
}

// NodeDiscovery implements sseha.NodeDiscovery using Redis.
type NodeDiscovery struct {
	client           Client
	store            sseha.MetadataStore
	keyPrefix        string
	heartbeatTimeout time.Duration
	nodeEventChannel string

	mu             sync.RWMutex
	joinCallbacks  []func(node *sseha.NodeInfo)
	leaveCallbacks []func(node *sseha.NodeInfo)
	knownNodes     map[string]*sseha.NodeInfo

	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// Verify interface compliance at compile time.
var _ sseha.NodeDiscovery = (*NodeDiscovery)(nil)

// NewNodeDiscovery creates a new Redis-based node discovery.
func NewNodeDiscovery(config *NodeDiscoveryConfig) *NodeDiscovery {
	if config == nil {
		config = DefaultNodeDiscoveryConfig()
	}
	if config.KeyPrefix == "" {
		config.KeyPrefix = "eino:sseha:"
	}
	if config.HeartbeatTimeout == 0 {
		config.HeartbeatTimeout = 10 * time.Second
	}
	if config.NodeEventChannel == "" {
		config.NodeEventChannel = "eino:sseha:node_events"
	}

	return &NodeDiscovery{
		client:           config.Client,
		store:            config.Store,
		keyPrefix:        config.KeyPrefix,
		heartbeatTimeout: config.HeartbeatTimeout,
		nodeEventChannel: config.NodeEventChannel,
		knownNodes:       make(map[string]*sseha.NodeInfo),
	}
}

// Start begins background routines for node discovery.
func (d *NodeDiscovery) Start(ctx context.Context) error {
	ctx, d.cancel = context.WithCancel(ctx)

	// Subscribe to node events
	sub, err := d.client.Subscribe(ctx, d.nodeEventChannel)
	if err != nil {
		return fmt.Errorf("subscribe to node events: %w", err)
	}

	d.wg.Add(1)
	go d.listenNodeEvents(ctx, sub)

	d.wg.Add(1)
	go d.checkNodesLoop(ctx)

	return nil
}

// Register registers a node and publishes a join event.
func (d *NodeDiscovery) Register(ctx context.Context, node *sseha.NodeInfo) error {
	if err := d.store.RegisterNode(ctx, node); err != nil {
		return err
	}

	// Publish join event
	evt := &nodeEvent{Type: "join", Node: node}
	data, _ := json.Marshal(evt)
	_ = d.client.Publish(ctx, d.nodeEventChannel, string(data))

	d.mu.Lock()
	d.knownNodes[node.NodeID] = node
	d.mu.Unlock()

	return nil
}

// Deregister removes a node and publishes a leave event.
func (d *NodeDiscovery) Deregister(ctx context.Context, nodeID string) error {
	d.mu.Lock()
	node, exists := d.knownNodes[nodeID]
	delete(d.knownNodes, nodeID)
	d.mu.Unlock()

	if err := d.store.RemoveNode(ctx, nodeID); err != nil {
		return err
	}

	if exists && node != nil {
		evt := &nodeEvent{Type: "leave", Node: node}
		data, _ := json.Marshal(evt)
		_ = d.client.Publish(ctx, d.nodeEventChannel, string(data))
	}

	return nil
}

// Heartbeat updates the node's heartbeat timestamp.
func (d *NodeDiscovery) Heartbeat(ctx context.Context, nodeID string) error {
	d.mu.RLock()
	node, exists := d.knownNodes[nodeID]
	d.mu.RUnlock()

	if !exists {
		node = &sseha.NodeInfo{NodeID: nodeID, LastHeartbeat: time.Now()}
	} else {
		node.LastHeartbeat = time.Now()
	}

	return d.store.RegisterNode(ctx, node)
}

// GetAliveNodes returns all nodes with a recent heartbeat.
func (d *NodeDiscovery) GetAliveNodes(ctx context.Context) ([]*sseha.NodeInfo, error) {
	return d.store.ListNodes(ctx, true, d.heartbeatTimeout)
}

// IsNodeAlive checks if a specific node is alive.
func (d *NodeDiscovery) IsNodeAlive(ctx context.Context, nodeID string) (bool, error) {
	node, err := d.store.GetNode(ctx, nodeID)
	if err != nil {
		return false, err
	}
	if node == nil {
		return false, nil
	}

	return time.Since(node.LastHeartbeat) <= d.heartbeatTimeout, nil
}

// OnNodeJoin registers a callback for node join events.
func (d *NodeDiscovery) OnNodeJoin(callback func(node *sseha.NodeInfo)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.joinCallbacks = append(d.joinCallbacks, callback)
}

// OnNodeLeave registers a callback for node leave events.
func (d *NodeDiscovery) OnNodeLeave(callback func(node *sseha.NodeInfo)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.leaveCallbacks = append(d.leaveCallbacks, callback)
}

// Close stops the discovery and releases resources.
func (d *NodeDiscovery) Close() error {
	if d.cancel != nil {
		d.cancel()
	}
	d.wg.Wait()
	return nil
}

func (d *NodeDiscovery) listenNodeEvents(ctx context.Context, sub Subscription) {
	defer d.wg.Done()
	defer func() { _ = sub.Unsubscribe() }()

	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}

			var evt nodeEvent
			if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
				continue
			}

			d.mu.Lock()
			switch evt.Type {
			case "join":
				d.knownNodes[evt.Node.NodeID] = evt.Node
				for _, cb := range d.joinCallbacks {
					go cb(evt.Node)
				}
			case "leave":
				delete(d.knownNodes, evt.Node.NodeID)
				for _, cb := range d.leaveCallbacks {
					go cb(evt.Node)
				}
			}
			d.mu.Unlock()
		}
	}
}

func (d *NodeDiscovery) checkNodesLoop(ctx context.Context) {
	defer d.wg.Done()

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			d.checkForDeadNodes(ctx)
		}
	}
}

func (d *NodeDiscovery) checkForDeadNodes(ctx context.Context) {
	allNodes, err := d.store.ListNodes(ctx, false, 0)
	if err != nil {
		return
	}

	now := time.Now()
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, node := range allNodes {
		if now.Sub(node.LastHeartbeat) > d.heartbeatTimeout {
			// Node is dead — check if we knew about it
			if _, known := d.knownNodes[node.NodeID]; known {
				delete(d.knownNodes, node.NodeID)
				for _, cb := range d.leaveCallbacks {
					go cb(node)
				}
			}
		} else {
			// Node is alive — check if it's new
			if _, known := d.knownNodes[node.NodeID]; !known {
				d.knownNodes[node.NodeID] = node
				for _, cb := range d.joinCallbacks {
					go cb(node)
				}
			}
		}
	}
}
