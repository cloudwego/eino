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

package dream

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// KVStore is the Redis-shaped coordination backend dream relies on. It persists
// scheduling state, the touched-session set, run locks, and dream job records.
//
// The shape deliberately mirrors a key-value store with locks so it maps cleanly
// onto Redis (AcquireLock -> SET NX PX, Get -> GET, Set -> SET PX, Del -> DEL) and
// onto a sorted set for the touch operations (AddToSet -> ZADD, ListSet -> ZRANGEBYSCORE,
// PruneSet -> ZREMRANGEBYSCORE).
//
// The in-process NewLocalKVStore is the default, but it is single-process only:
// when the dream middleware is constructed per session (the common server pattern),
// each instance gets its own store, touched-session counts never reach the trigger
// threshold, and dreams never fire. Production deployments MUST inject a shared,
// durable KVStore whose AcquireLock is atomic across processes.
type KVStore interface {
	// AcquireLock tries to acquire a lock for key. When ok==true it returns an
	// unlock function that must be called exactly once. ok==false means another
	// holder owns the lock.
	AcquireLock(ctx context.Context, key string, ttl time.Duration) (unlock func(context.Context) error, ok bool, err error)

	// Get returns the value for key. When the key does not exist, ok is false.
	Get(ctx context.Context, key string) (value []byte, ok bool, err error)

	// Set stores value for key. ttl<=0 means no expiration.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error

	// Del removes key. Removing a missing key is a no-op.
	Del(ctx context.Context, key string) error

	// AddToSet adds member to the set at key, scored by at. ttl<=0 means no
	// expiration on the set.
	AddToSet(ctx context.Context, key, member string, at time.Time, ttl time.Duration) error

	// ListSet returns distinct members of the set at key scored strictly after since.
	ListSet(ctx context.Context, key string, since time.Time) ([]string, error)

	// PruneSet removes members of the set at key scored at or before before.
	PruneSet(ctx context.Context, key string, before time.Time) error
}

// ScheduleState stores per-memory-directory scheduling state.
type ScheduleState struct {
	// LastConsolidatedAt is the completion time of the last successful run.
	LastConsolidatedAt time.Time `json:"last_consolidated_at"`

	// NextCheckAt is the next time the middleware should re-check this directory.
	NextCheckAt time.Time `json:"next_check_at"`

	// ConsecutiveFailures counts dream runs that have failed since the last
	// success. It is reset to zero on a successful run.
	ConsecutiveFailures int `json:"consecutive_failures"`
}

// Key derivation: all keys are scoped to the resolved memory directory so multiple
// memory directories sharing one KVStore do not collide.

func dirHash(memoryDir string) string {
	sum := sha256.Sum256([]byte(memoryDir))
	return hex.EncodeToString(sum[:8])
}

func scheduleKey(memoryDir string) string { return "dream::" + dirHash(memoryDir) + "::schedule" }
func touchSetKey(memoryDir string) string { return "dream::" + dirHash(memoryDir) + "::touch" }
func runLockKey(memoryDir string) string  { return "dream::" + dirHash(memoryDir) + "::lock" }
func dirLockKey(dir string) string        { return "dream::" + dirHash(dir) + "::dirlock" }
func jobKey(jobID string) string          { return "dream::job::" + jobID }

func getScheduleState(ctx context.Context, store KVStore, memoryDir string) (*ScheduleState, error) {
	raw, ok, err := store.Get(ctx, scheduleKey(memoryDir))
	if err != nil || !ok {
		return nil, err
	}
	var st ScheduleState
	if err := json.Unmarshal(raw, &st); err != nil {
		return nil, err
	}
	return &st, nil
}

func setScheduleState(ctx context.Context, store KVStore, memoryDir string, st *ScheduleState) error {
	if st == nil {
		return store.Del(ctx, scheduleKey(memoryDir))
	}
	raw, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return store.Set(ctx, scheduleKey(memoryDir), raw, 0)
}

func getJob(ctx context.Context, store KVStore, jobID string) (*Job, error) {
	raw, ok, err := store.Get(ctx, jobKey(jobID))
	if err != nil || !ok {
		return nil, err
	}
	var job Job
	if err := json.Unmarshal(raw, &job); err != nil {
		return nil, err
	}
	return &job, nil
}

func setJob(ctx context.Context, store KVStore, job *Job, ttl time.Duration) error {
	raw, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return store.Set(ctx, jobKey(job.ID), raw, ttl)
}

// localKVStore is the default in-process KVStore. It is single-process only and is
// suitable for tests and single-instance deployments.
type localKVStore struct {
	mu    sync.Mutex
	kv    map[string]localKVValue
	locks map[string]localKVLock
	sets  map[string]map[string]time.Time
}

type localKVValue struct {
	value  []byte
	expiry time.Time
}

type localKVLock struct {
	token  string
	expiry time.Time
}

// NewLocalKVStore returns an in-process KVStore.
// It is suitable for tests and single-process use only.
func NewLocalKVStore() KVStore {
	return &localKVStore{
		kv:    make(map[string]localKVValue),
		locks: make(map[string]localKVLock),
		sets:  make(map[string]map[string]time.Time),
	}
}

func (s *localKVStore) AcquireLock(_ context.Context, key string, ttl time.Duration) (func(context.Context) error, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if l, ok := s.locks[key]; ok && now.Before(l.expiry) {
		return nil, false, nil
	}
	token := randToken()
	s.locks[key] = localKVLock{token: token, expiry: now.Add(ttl)}
	return func(context.Context) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		l, ok := s.locks[key]
		if !ok {
			return nil
		}
		if l.token != token {
			return fmt.Errorf("lock token mismatch")
		}
		delete(s.locks, key)
		return nil
	}, true, nil
}

func (s *localKVStore) Get(_ context.Context, key string) ([]byte, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.kv[key]
	if !ok {
		return nil, false, nil
	}
	if !v.expiry.IsZero() && time.Now().After(v.expiry) {
		delete(s.kv, key)
		return nil, false, nil
	}
	return append([]byte(nil), v.value...), true, nil
}

func (s *localKVStore) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var expiry time.Time
	if ttl > 0 {
		expiry = time.Now().Add(ttl)
	}
	s.kv[key] = localKVValue{value: append([]byte(nil), value...), expiry: expiry}
	return nil
}

func (s *localKVStore) Del(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.kv, key)
	return nil
}

func (s *localKVStore) AddToSet(_ context.Context, key, member string, at time.Time, _ time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sets[key] == nil {
		s.sets[key] = make(map[string]time.Time)
	}
	s.sets[key][member] = at
	return nil
}

func (s *localKVStore) ListSet(_ context.Context, key string, since time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.sets[key]
	if len(items) == 0 {
		return nil, nil
	}
	out := make([]string, 0, len(items))
	for member, at := range items {
		if at.After(since) {
			out = append(out, member)
		}
	}
	return out, nil
}

func (s *localKVStore) PruneSet(_ context.Context, key string, before time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.sets[key]
	for member, at := range items {
		if !at.After(before) {
			delete(items, member)
		}
	}
	return nil
}

func randToken() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}
