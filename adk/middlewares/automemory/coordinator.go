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
// A Redis-backed implementation can map these methods to SETNX + TTL and plain KV get/set.
type Coordinator interface {
	// AcquireLock tries to acquire a lock for a given session. When ok==true,
	// it returns an unlock function that must be called exactly once.
	AcquireLock(ctx context.Context, sessionID string, ttl time.Duration) (unlock func(context.Context) error, ok bool, err error)

	// PopPendingSnapshot returns and deletes the pending snapshot for a session.
	// If there is no pending snapshot, it returns (nil, nil).
	PopPendingSnapshot(ctx context.Context, sessionID string) (*PendingSnapshot, error)
	SetPendingSnapshot(ctx context.Context, sessionID string, snapshot *PendingSnapshot) error

	GetCursor(ctx context.Context, sessionID string) (cursor int, ok bool, err error)
	SetCursor(ctx context.Context, sessionID string, cursor int) error
}

type PendingSnapshot struct {
	Cursor    int             `json:"cursor"`
	Messages  json.RawMessage `json:"messages"`
	ToolInfos json.RawMessage `json:"tool_infos,omitempty"`
}

type CoordinationConfig[M adk.MessageType] struct {
	SessionIDFunc SessionIDFunc[M]
	Coordinator   Coordinator
	LockTTL       time.Duration
}

// LocalCoordinator is the default in-process coordinator used in tests and single-instance deployments.
// For distributed deployments, provide a Coordinator backed by Redis or another shared KV.
type LocalCoordinator struct {
	mu      sync.Mutex
	locks   map[string]localLock
	pending map[string]*PendingSnapshot
	cursor  map[string]int
}

type localLock struct {
	token  string
	expiry time.Time
}

// NewLocalCoordinator returns the default in-process Coordinator implementation.
func NewLocalCoordinator() *LocalCoordinator {
	return &LocalCoordinator{
		locks:   map[string]localLock{},
		pending: map[string]*PendingSnapshot{},
		cursor:  map[string]int{},
	}
}

func (c *LocalCoordinator) AcquireLock(_ context.Context, sessionID string, ttl time.Duration) (func(context.Context) error, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if l, ok := c.locks[sessionID]; ok && now.Before(l.expiry) {
		return nil, false, nil
	}
	token := randToken()
	c.locks[sessionID] = localLock{token: token, expiry: now.Add(ttl)}
	return func(_ context.Context) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		l, ok := c.locks[sessionID]
		if !ok {
			return nil
		}
		if l.token != token {
			return fmt.Errorf("lock token mismatch")
		}
		delete(c.locks, sessionID)
		return nil
	}, true, nil
}

func (c *LocalCoordinator) PopPendingSnapshot(_ context.Context, sessionID string) (*PendingSnapshot, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s, ok := c.pending[sessionID]
	if !ok || s == nil {
		return nil, nil
	}
	cp := *s
	if s.Messages != nil {
		cp.Messages = append([]byte(nil), s.Messages...)
	}
	if s.ToolInfos != nil {
		cp.ToolInfos = append([]byte(nil), s.ToolInfos...)
	}
	delete(c.pending, sessionID)
	return &cp, nil
}

func (c *LocalCoordinator) SetPendingSnapshot(_ context.Context, sessionID string, snapshot *PendingSnapshot) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if snapshot == nil {
		delete(c.pending, sessionID)
		return nil
	}
	cp := *snapshot
	if snapshot.Messages != nil {
		cp.Messages = append([]byte(nil), snapshot.Messages...)
	}
	if snapshot.ToolInfos != nil {
		cp.ToolInfos = append([]byte(nil), snapshot.ToolInfos...)
	}
	c.pending[sessionID] = &cp
	return nil
}

func (c *LocalCoordinator) GetCursor(_ context.Context, sessionID string) (int, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.cursor[sessionID]
	return v, ok, nil
}

func (c *LocalCoordinator) SetCursor(_ context.Context, sessionID string, cursor int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cursor[sessionID] = cursor
	return nil
}

func randToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
