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
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/automemory"
	"github.com/cloudwego/eino/schema"
)

type staleScheduleStore struct {
	Store
	listCalls     int32
	acquireCalls  int32
	prechecksDone chan struct{}
	firstUnlocked chan struct{}
	unlockOnce    sync.Once
}

func (s *staleScheduleStore) ListSessionTouches(
	ctx context.Context,
	memoryDir string,
) ([]SessionTouch, error) {
	call := atomic.AddInt32(&s.listCalls, 1)
	if call <= 2 {
		if call == 2 {
			close(s.prechecksDone)
		}
		<-s.prechecksDone
	}
	return s.Store.ListSessionTouches(ctx, memoryDir)
}

func (s *staleScheduleStore) AcquireRunLock(
	ctx context.Context,
	memoryDir string,
	ttl time.Duration,
) (func(context.Context) error, bool, error) {
	call := atomic.AddInt32(&s.acquireCalls, 1)
	if call == 2 {
		<-s.firstUnlocked
	}
	unlock, ok, err := s.Store.AcquireRunLock(ctx, memoryDir, ttl)
	if err != nil || !ok || call != 1 {
		return unlock, ok, err
	}
	return func(unlockCtx context.Context) error {
		err := unlock(unlockCtx)
		s.unlockOnce.Do(func() { close(s.firstUnlocked) })
		return err
	}, true, nil
}

func newScheduledDreamMiddleware(
	t *testing.T,
	ctx context.Context,
	memoryDir string,
	store Store,
	model *dreamModel,
	sessionID string,
) *middleware[*schema.Message] {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(memoryDir, "MEMORY.md"), nil, 0o644))
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: memoryDir,
		MemoryBackend:   automemory.NewLocalBackend(),
		Model:           model,
		SessionID:       sessionID,
		Schedule: &ScheduleConfig{
			RunInline:         true,
			Store:             store,
			MinInterval:       time.Hour,
			MinTouchedSession: 1,
			ScanInterval:      time.Minute,
			LockTTL:           time.Minute,
		},
	})
	require.NoError(t, err)
	impl, ok := mw.(*middleware[*schema.Message])
	require.True(t, ok)
	return impl
}

func TestMaybeTrigger_RechecksScheduleAfterAcquiringLock(t *testing.T) {
	ctx := context.Background()
	memoryDir := t.TempDir()
	now := time.Now()
	baseStore := NewLocalStore()
	store := &staleScheduleStore{
		Store:         baseStore,
		prechecksDone: make(chan struct{}),
		firstUnlocked: make(chan struct{}),
	}
	require.NoError(t, store.SetScheduleState(ctx, memoryDir, &ScheduleState{
		LastConsolidatedAt: now.Add(-2 * time.Hour),
		NextCheckAt:        now,
	}))
	require.NoError(t, store.RecordSessionTouch(ctx, memoryDir, "session-a", now.Add(-time.Minute)))

	model := &dreamModel{}
	mw := newScheduledDreamMiddleware(t, ctx, memoryDir, store, model, "")
	mw.now = func() time.Time { return now }

	errs := make(chan error, 2)
	go func() { errs <- mw.maybeTrigger(ctx, "", false) }()
	go func() { errs <- mw.maybeTrigger(ctx, "", false) }()
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)

	require.EqualValues(t, 2, atomic.LoadInt32(&model.calls), "only one dream run should execute")
}

func TestMaybeTrigger_PreservesRetouchRecordedDuringDream(t *testing.T) {
	ctx := context.Background()
	memoryDir := t.TempDir()
	selectionTime := time.Now()
	lateTouchTime := selectionTime.Add(time.Minute)
	completionTime := selectionTime.Add(2 * time.Minute)
	store := NewLocalStore()
	require.NoError(t, store.SetScheduleState(ctx, memoryDir, &ScheduleState{
		LastConsolidatedAt: selectionTime.Add(-2 * time.Hour),
		NextCheckAt:        selectionTime,
	}))
	require.NoError(t, store.RecordSessionTouch(ctx, memoryDir, "selected", selectionTime.Add(-time.Minute)))

	var dreamStarted uint32
	model := &dreamModel{
		onFirstGenerate: func() {
			require.NoError(t, store.RecordSessionTouch(ctx, memoryDir, "selected", lateTouchTime))
			atomic.StoreUint32(&dreamStarted, 1)
		},
	}
	mw := newScheduledDreamMiddleware(t, ctx, memoryDir, store, model, "")
	mw.now = func() time.Time {
		if atomic.LoadUint32(&dreamStarted) == 1 {
			return completionTime
		}
		return selectionTime
	}

	require.NoError(t, mw.maybeTrigger(ctx, "", false))

	touched, err := store.ListSessionTouches(ctx, memoryDir)
	require.NoError(t, err)
	require.Contains(t, sessionIDsFromTouches(touched), "selected")

	mw.now = func() time.Time { return completionTime.Add(2 * time.Hour) }
	_, candidates, ready, err := mw.checkSchedule(ctx, "", false)
	require.NoError(t, err)
	require.True(t, ready)
	require.Contains(t, sessionIDsFromTouches(candidates), "selected")
}

func TestAfterAgent_PreservesExcludedCurrentSessionTouch(t *testing.T) {
	ctx := context.Background()
	memoryDir := t.TempDir()
	now := time.Now()
	store := NewLocalStore()
	require.NoError(t, store.SetScheduleState(ctx, memoryDir, &ScheduleState{
		LastConsolidatedAt: now.Add(-2 * time.Hour),
		NextCheckAt:        now,
	}))
	require.NoError(t, store.RecordSessionTouch(ctx, memoryDir, "selected", now.Add(-time.Minute)))

	mw := newScheduledDreamMiddleware(t, ctx, memoryDir, store, &dreamModel{}, "current")
	mw.now = func() time.Time { return now }

	_, err := mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{})
	require.NoError(t, err)

	touched, err := store.ListSessionTouches(ctx, memoryDir)
	require.NoError(t, err)
	require.Contains(t, sessionIDsFromTouches(touched), "current")

	mw.now = func() time.Time { return now.Add(2 * time.Hour) }
	_, candidates, ready, err := mw.checkSchedule(ctx, "", false)
	require.NoError(t, err)
	require.True(t, ready)
	require.Contains(t, sessionIDsFromTouches(candidates), "current")
}

func TestLocalStore_ExpiredOwnerCannotReleaseReplacementLock(t *testing.T) {
	ctx := context.Background()
	store := NewLocalStore().(*localStore)
	const memoryDir = "/mem"

	unlockOld, ok, err := store.AcquireRunLock(ctx, memoryDir, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	store.mu.Lock()
	oldLock := store.locks[memoryDir]
	oldLock.expiry = time.Now().Add(-time.Second)
	store.locks[memoryDir] = oldLock
	store.mu.Unlock()

	unlockNew, ok, err := store.AcquireRunLock(ctx, memoryDir, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	require.Error(t, unlockOld(ctx))
	_, ok, err = store.AcquireRunLock(ctx, memoryDir, time.Minute)
	require.NoError(t, err)
	require.False(t, ok, "replacement lock must remain owned")
	require.NoError(t, unlockNew(ctx))
}
