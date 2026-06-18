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

package automemory

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
)

type SessionIDFunc[M adk.MessageType] func(ctx context.Context, state *adk.TypedChatModelAgentState[M]) (string, error)

// Coordinator abstracts distributed coordination for async memory extraction.
// A Redis-backed implementation can map AcquireLock to SETNX + TTL, Set to SET,
// Get to GET, and GetAndDelete to GETDEL.
type Coordinator interface {
	// AcquireLock tries to acquire a lock for key. When ok==true,
	// it returns an unlock function that must be called exactly once.
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (unlock func(context.Context) error, ok bool, err error)

	// Get returns the value for key. When the key does not exist, ok is false.
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)

	// Set stores value for key. ttl<=0 means no expiration.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// GetAndDelete returns the value for key and deletes it atomically.
	// When the key does not exist, ok is false.
	GetAndDelete(ctx context.Context, key string) (value []byte, ok bool, err error)
}

type PendingSnapshot struct {
	Cursor    int    `json:"cursor"`
	Messages  []byte `json:"messages"`
	ToolInfos []byte `json:"tool_infos,omitempty"`
}

type CoordinationConfig[M adk.MessageType] struct {
	// SessionIDFunc returns the logical session ID used to build the coordinator key.
	// Optional. Defaults to an internal context-scoped session ID for write extraction.
	SessionIDFunc SessionIDFunc[M]

	// Coordinator stores cursor/pending state and coordinates async extraction locks.
	// Optional. Defaults to NewLocalCoordinator().
	Coordinator Coordinator

	// LockTTL is the expiration duration for extraction locks and pending snapshots.
	// Optional. Defaults to the package default lock TTL.
	LockTTL time.Duration
}

// LocalCoordinator is the default in-process coordinator used in tests and single-instance deployments.
// For distributed deployments, provide a Coordinator backed by Redis or another shared KV.
type LocalCoordinator struct {
	mu    sync.Mutex
	locks map[string]localLock
	kv    map[string]localValue
}

type localLock struct {
	token  string
	expiry time.Time
}

type localValue struct {
	value  []byte
	expiry time.Time
}

// NewLocalCoordinator returns the default in-process Coordinator implementation.
func NewLocalCoordinator() *LocalCoordinator {
	return &LocalCoordinator{
		locks: map[string]localLock{},
		kv:    map[string]localValue{},
	}
}

func (c *LocalCoordinator) AcquireLock(_ context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if l, ok := c.locks[key]; ok && now.Before(l.expiry) {
		return nil, false, nil
	}
	token := randToken()
	c.locks[key] = localLock{token: token, expiry: now.Add(ttl)}
	return func(_ context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		l, ok := c.locks[key]
		if !ok {
			return nil
		}
		if l.token != token {
			return fmt.Errorf("lock token mismatch")
		}
		delete(c.locks, key)
		return nil
	}, true, nil
}

func (c *LocalCoordinator) Get(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.kv[key]
	if !ok {
		return nil, false, nil
	}
	if !v.expiry.IsZero() && time.Now().After(v.expiry) {
		delete(c.kv, key)
		return nil, false, nil
	}
	return append([]byte(nil), v.value...), true, nil
}

func (c *LocalCoordinator) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var expiry time.Time
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}
	c.kv[key] = localValue{value: append([]byte(nil), value...), expiry: expiry}
	return nil
}

func (c *LocalCoordinator) GetAndDelete(_ context.Context, key string) ([]byte, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.kv[key]
	if !ok {
		return nil, false, nil
	}
	delete(c.kv, key)
	if !v.expiry.IsZero() && time.Now().After(v.expiry) {
		return nil, false, nil
	}
	return append([]byte(nil), v.value...), true, nil
}

func coordinatorCursorKey(key string) string {
	return key + "::cursor"
}

func coordinatorPendingSnapshotKey(key string) string {
	return key + "::pending_snapshot"
}

func getCoordinatorCursor(ctx context.Context, c Coordinator, key string) (int, bool, error) {
	raw, ok, err := c.Get(ctx, coordinatorCursorKey(key))
	if err != nil || !ok {
		return 0, ok, err
	}
	var cursor int
	if _, err := fmt.Sscanf(string(raw), "%d", &cursor); err != nil {
		return 0, false, err
	}
	return cursor, true, nil
}

func setCoordinatorCursor(ctx context.Context, c Coordinator, key string, cursor int) error {
	return c.Set(ctx, coordinatorCursorKey(key), []byte(fmt.Sprintf("%d", cursor)), 0)
}

func popCoordinatorPendingSnapshot(ctx context.Context, c Coordinator, key string) (*PendingSnapshot, error) {
	raw, ok, err := c.GetAndDelete(ctx, coordinatorPendingSnapshotKey(key))
	if err != nil || !ok {
		return nil, err
	}
	var snapshot PendingSnapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, err
	}
	return &snapshot, nil
}

func setCoordinatorPendingSnapshot(ctx context.Context, c Coordinator, key string, snapshot *PendingSnapshot, ttl time.Duration) error {
	raw, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	return c.Set(ctx, coordinatorPendingSnapshotKey(key), raw, ttl)
}

func randToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
