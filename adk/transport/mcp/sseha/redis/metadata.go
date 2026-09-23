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
	"time"

	"github.com/cloudwego/eino/adk/transport/mcp/sseha"
)

// MetadataStoreConfig configures the Redis-based metadata store.
type MetadataStoreConfig struct {
	// Client is the Redis client to use.
	Client Client

	// KeyPrefix is prepended to all Redis keys to avoid collisions.
	// Default: "eino:sseha:"
	KeyPrefix string

	// SessionTTL is the TTL for session entries. Sessions not updated within
	// this period are automatically expired by Redis. Default: 24h.
	SessionTTL time.Duration

	// NodeTTL is the TTL for node entries. Default: 30s (renewed by heartbeat).
	NodeTTL time.Duration

	// LockTTL is the default TTL for session migration locks. Default: 30s.
	LockTTL time.Duration
}

// DefaultMetadataStoreConfig returns sensible defaults.
func DefaultMetadataStoreConfig() *MetadataStoreConfig {
	return &MetadataStoreConfig{
		KeyPrefix:  "eino:sseha:",
		SessionTTL: 24 * time.Hour,
		NodeTTL:    30 * time.Second,
		LockTTL:    30 * time.Second,
	}
}

// MetadataStore implements sseha.MetadataStore using Redis as the backend.
type MetadataStore struct {
	client     Client
	keyPrefix  string
	sessionTTL time.Duration
	nodeTTL    time.Duration
	lockTTL    time.Duration
}

// Verify interface compliance at compile time.
var _ sseha.MetadataStore = (*MetadataStore)(nil)

// NewMetadataStore creates a new Redis-backed metadata store.
func NewMetadataStore(config *MetadataStoreConfig) *MetadataStore {
	if config == nil {
		config = DefaultMetadataStoreConfig()
	}
	if config.KeyPrefix == "" {
		config.KeyPrefix = "eino:sseha:"
	}
	if config.SessionTTL == 0 {
		config.SessionTTL = 24 * time.Hour
	}
	if config.NodeTTL == 0 {
		config.NodeTTL = 30 * time.Second
	}
	if config.LockTTL == 0 {
		config.LockTTL = 30 * time.Second
	}

	return &MetadataStore{
		client:     config.Client,
		keyPrefix:  config.KeyPrefix,
		sessionTTL: config.SessionTTL,
		nodeTTL:    config.NodeTTL,
		lockTTL:    config.LockTTL,
	}
}

func (s *MetadataStore) sessionKey(sessionID string) string {
	return fmt.Sprintf("%ssession:%s", s.keyPrefix, sessionID)
}

func (s *MetadataStore) sessionIndexKey() string {
	return fmt.Sprintf("%ssessions:index", s.keyPrefix)
}

func (s *MetadataStore) nodeKey(nodeID string) string {
	return fmt.Sprintf("%snode:%s", s.keyPrefix, nodeID)
}

func (s *MetadataStore) nodeIndexKey() string {
	return fmt.Sprintf("%snodes:index", s.keyPrefix)
}

func (s *MetadataStore) lockKey(sessionID string) string {
	return fmt.Sprintf("%slock:%s", s.keyPrefix, sessionID)
}

func (s *MetadataStore) barrierKey(sessionID string) string {
	return fmt.Sprintf("%sbarrier:%s", s.keyPrefix, sessionID)
}

func (s *MetadataStore) nodeSessionsKey(nodeID string) string {
	return fmt.Sprintf("%snode_sessions:%s", s.keyPrefix, nodeID)
}

// RegisterSession creates a new session entry in Redis.
func (s *MetadataStore) RegisterSession(ctx context.Context, info *sseha.SessionInfo) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	ok, err := s.client.SetNX(ctx, s.sessionKey(info.SessionID), string(data), s.sessionTTL)
	if err != nil {
		return fmt.Errorf("redis setnx session: %w", err)
	}
	if !ok {
		return sseha.ErrSessionAlreadyExists
	}

	// Add to session index set
	if err := s.client.SAdd(ctx, s.sessionIndexKey(), info.SessionID); err != nil {
		return fmt.Errorf("redis sadd session index: %w", err)
	}

	// Track session under its owning node
	if err := s.client.SAdd(ctx, s.nodeSessionsKey(info.NodeID), info.SessionID); err != nil {
		return fmt.Errorf("redis sadd node sessions: %w", err)
	}

	return nil
}

// GetSession retrieves session metadata by ID.
func (s *MetadataStore) GetSession(ctx context.Context, sessionID string) (*sseha.SessionInfo, error) {
	data, err := s.client.Get(ctx, s.sessionKey(sessionID))
	if err != nil {
		return nil, fmt.Errorf("redis get session: %w", err)
	}
	if data == "" {
		return nil, nil
	}

	var info sseha.SessionInfo
	if err := json.Unmarshal([]byte(data), &info); err != nil {
		return nil, fmt.Errorf("unmarshal session: %w", err)
	}

	return &info, nil
}

// luaUpdateSession is a Lua script for atomic conditional update with version check.
const luaUpdateSession = `
local key = KEYS[1]
local expectedVersion = tonumber(ARGV[1])
local newData = ARGV[2]
local ttl = tonumber(ARGV[3])

local current = redis.call('GET', key)
if current == false then
    return redis.error_reply('session_not_found')
end

local info = cjson.decode(current)
if info.version ~= expectedVersion then
    return redis.error_reply('version_conflict')
end

redis.call('SET', key, newData, 'EX', ttl)
return 1
`

// UpdateSession atomically updates session metadata with version check.
func (s *MetadataStore) UpdateSession(ctx context.Context, info *sseha.SessionInfo) error {
	// Increment version for the new write
	newInfo := *info
	newInfo.Version = info.Version + 1

	data, err := json.Marshal(&newInfo)
	if err != nil {
		return fmt.Errorf("marshal session: %w", err)
	}

	result, err := s.client.Eval(ctx, luaUpdateSession,
		[]string{s.sessionKey(info.SessionID)},
		info.Version,
		string(data),
		int64(s.sessionTTL.Seconds()),
	)
	if err != nil {
		errStr := err.Error()
		if errStr == "session_not_found" {
			return sseha.ErrSessionNotFound
		}
		if errStr == "version_conflict" {
			return sseha.ErrVersionConflict
		}
		return fmt.Errorf("redis eval update session: %w", err)
	}
	_ = result

	// Update node session tracking if node changed
	if info.NodeID != newInfo.NodeID {
		_ = s.client.SRem(ctx, s.nodeSessionsKey(info.NodeID), info.SessionID)
		_ = s.client.SAdd(ctx, s.nodeSessionsKey(newInfo.NodeID), info.SessionID)
	}

	// Update caller's version
	info.Version = newInfo.Version

	return nil
}

// DeleteSession removes a session entry.
func (s *MetadataStore) DeleteSession(ctx context.Context, sessionID string) error {
	// Get current session to find its node
	info, err := s.GetSession(ctx, sessionID)
	if err != nil {
		return err
	}

	if err := s.client.Del(ctx, s.sessionKey(sessionID)); err != nil {
		return fmt.Errorf("redis del session: %w", err)
	}

	_ = s.client.SRem(ctx, s.sessionIndexKey(), sessionID)

	if info != nil {
		_ = s.client.SRem(ctx, s.nodeSessionsKey(info.NodeID), sessionID)
	}

	return nil
}

// ListSessions queries sessions matching the given filter.
func (s *MetadataStore) ListSessions(ctx context.Context, filter *sseha.SessionFilter) ([]*sseha.SessionInfo, error) {
	var sessionIDs []string
	var err error

	if filter != nil && filter.NodeID != "" {
		// If filtering by node, use the node sessions index
		sessionIDs, err = s.client.SMembers(ctx, s.nodeSessionsKey(filter.NodeID))
	} else {
		sessionIDs, err = s.client.SMembers(ctx, s.sessionIndexKey())
	}
	if err != nil {
		return nil, fmt.Errorf("redis smembers: %w", err)
	}

	var results []*sseha.SessionInfo
	now := time.Now()

	for _, sid := range sessionIDs {
		if filter != nil && filter.Limit > 0 && len(results) >= filter.Limit {
			break
		}

		info, err := s.GetSession(ctx, sid)
		if err != nil {
			continue
		}
		if info == nil {
			// Stale index entry; clean up
			_ = s.client.SRem(ctx, s.sessionIndexKey(), sid)
			continue
		}

		// Apply filters
		if filter != nil {
			if len(filter.States) > 0 {
				matched := false
				for _, state := range filter.States {
					if info.State == state {
						matched = true
						break
					}
				}
				if !matched {
					continue
				}
			}

			if filter.OlderThan > 0 && now.Sub(info.LastActiveAt) < filter.OlderThan {
				continue
			}
		}

		results = append(results, info)
	}

	return results, nil
}

// AcquireSessionLock attempts to acquire a distributed lock for session migration.
func (s *MetadataStore) AcquireSessionLock(ctx context.Context, sessionID string, nodeID string, ttl time.Duration) (bool, error) {
	if ttl == 0 {
		ttl = s.lockTTL
	}

	ok, err := s.client.SetNX(ctx, s.lockKey(sessionID), nodeID, ttl)
	if err != nil {
		return false, fmt.Errorf("redis setnx lock: %w", err)
	}

	return ok, nil
}

// luaReleaseLock atomically releases a lock only if it's held by the expected node.
const luaReleaseLock = `
local key = KEYS[1]
local expectedNode = ARGV[1]
local current = redis.call('GET', key)
if current == expectedNode then
    redis.call('DEL', key)
    return 1
end
return 0
`

// ReleaseSessionLock releases a previously acquired session lock.
func (s *MetadataStore) ReleaseSessionLock(ctx context.Context, sessionID string, nodeID string) error {
	_, err := s.client.Eval(ctx, luaReleaseLock,
		[]string{s.lockKey(sessionID)},
		nodeID,
	)
	if err != nil {
		return fmt.Errorf("redis eval release lock: %w", err)
	}
	return nil
}

// RegisterNode registers or updates a node in the cluster registry.
func (s *MetadataStore) RegisterNode(ctx context.Context, node *sseha.NodeInfo) error {
	data, err := json.Marshal(node)
	if err != nil {
		return fmt.Errorf("marshal node: %w", err)
	}

	if err := s.client.Set(ctx, s.nodeKey(node.NodeID), string(data), s.nodeTTL); err != nil {
		return fmt.Errorf("redis set node: %w", err)
	}

	if err := s.client.SAdd(ctx, s.nodeIndexKey(), node.NodeID); err != nil {
		return fmt.Errorf("redis sadd node index: %w", err)
	}

	return nil
}

// GetNode retrieves a node's information by ID.
func (s *MetadataStore) GetNode(ctx context.Context, nodeID string) (*sseha.NodeInfo, error) {
	data, err := s.client.Get(ctx, s.nodeKey(nodeID))
	if err != nil {
		return nil, fmt.Errorf("redis get node: %w", err)
	}
	if data == "" {
		return nil, nil
	}

	var node sseha.NodeInfo
	if err := json.Unmarshal([]byte(data), &node); err != nil {
		return nil, fmt.Errorf("unmarshal node: %w", err)
	}

	return &node, nil
}

// ListNodes returns all registered nodes.
func (s *MetadataStore) ListNodes(ctx context.Context, aliveOnly bool, heartbeatTimeout time.Duration) ([]*sseha.NodeInfo, error) {
	nodeIDs, err := s.client.SMembers(ctx, s.nodeIndexKey())
	if err != nil {
		return nil, fmt.Errorf("redis smembers nodes: %w", err)
	}

	now := time.Now()
	var results []*sseha.NodeInfo

	for _, nid := range nodeIDs {
		node, err := s.GetNode(ctx, nid)
		if err != nil {
			continue
		}
		if node == nil {
			// Stale index entry
			_ = s.client.SRem(ctx, s.nodeIndexKey(), nid)
			continue
		}

		if aliveOnly && heartbeatTimeout > 0 {
			if now.Sub(node.LastHeartbeat) > heartbeatTimeout {
				continue
			}
		}

		results = append(results, node)
	}

	return results, nil
}

// RemoveNode removes a node from the cluster registry.
func (s *MetadataStore) RemoveNode(ctx context.Context, nodeID string) error {
	if err := s.client.Del(ctx, s.nodeKey(nodeID)); err != nil {
		return fmt.Errorf("redis del node: %w", err)
	}
	_ = s.client.SRem(ctx, s.nodeIndexKey(), nodeID)
	return nil
}

// SetBarrier creates a migration barrier token.
func (s *MetadataStore) SetBarrier(ctx context.Context, barrier *sseha.BarrierToken) error {
	data, err := json.Marshal(barrier)
	if err != nil {
		return fmt.Errorf("marshal barrier: %w", err)
	}

	return s.client.Set(ctx, s.barrierKey(barrier.SessionID), string(data), 60*time.Second)
}

// GetBarrier retrieves the current barrier token for a session.
func (s *MetadataStore) GetBarrier(ctx context.Context, sessionID string) (*sseha.BarrierToken, error) {
	data, err := s.client.Get(ctx, s.barrierKey(sessionID))
	if err != nil {
		return nil, fmt.Errorf("redis get barrier: %w", err)
	}
	if data == "" {
		return nil, nil
	}

	var barrier sseha.BarrierToken
	if err := json.Unmarshal([]byte(data), &barrier); err != nil {
		return nil, fmt.Errorf("unmarshal barrier: %w", err)
	}

	return &barrier, nil
}

// ReleaseBarrier marks a barrier as released.
func (s *MetadataStore) ReleaseBarrier(ctx context.Context, sessionID string) error {
	barrier, err := s.GetBarrier(ctx, sessionID)
	if err != nil {
		return err
	}
	if barrier == nil {
		return nil
	}

	barrier.Released = true
	data, err := json.Marshal(barrier)
	if err != nil {
		return fmt.Errorf("marshal barrier: %w", err)
	}

	return s.client.Set(ctx, s.barrierKey(sessionID), string(data), 60*time.Second)
}

// Close releases resources.
func (s *MetadataStore) Close() error {
	return nil // the Redis client lifecycle is managed externally
}
