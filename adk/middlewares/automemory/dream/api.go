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
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/adk"
)

// This file holds the operations a caller invokes directly on a dream: starting a
// run, querying its status, and canceling it. The scheduled middleware is created
// with New in middleware.go.

// Run starts a one-shot dream and returns the job id.
//
// By default (RunConfig.Sync == false) Run is asynchronous: it persists a pending
// job, starts the consolidation in a background goroutine, and returns the job id
// immediately with a nil error. Track it with GetDreamStatus and stop it with
// CancelDream using that id. Set RunConfig.Sync to block until the run reaches a
// terminal state and return its error instead.
//
// The job's lifecycle is observable through GetDreamStatus and cancelable through
// CancelDream (the in-process default store only supports same-process queries and
// requires the process to outlive the run; inject a shared store for cross-process
// visibility).
//
// Run acquires the per-memory-directory run lock so a manual dream does not write
// concurrently with a scheduled run sharing the same store; if the lock is held, Run
// returns an empty id and a nil error. The input memory directory is never modified:
// the model edits a staged working copy that is promoted to the output directory on
// success.
func Run[M adk.MessageType](ctx context.Context, cfg *RunConfig[M]) (string, error) {
	cfg = cloneRunConfig(cfg)
	if err := applyCoreDefaults(ctx, &cfg.BaseConfig); err != nil {
		return "", err
	}
	m, err := newMiddleware(&cfg.BaseConfig, cfg.SessionID, nil)
	if err != nil {
		return "", err
	}
	sessionID := strings.TrimSpace(cfg.SessionID)

	var unlock func(context.Context) error
	if store := m.store(); store != nil {
		u, ok, lockErr := store.AcquireLock(ctx, runLockKey(m.resolvedMemoryDir), m.lockTTL())
		if lockErr != nil || !ok {
			return "", lockErr
		}
		unlock = u
	}

	job := m.newJob(sessionID, nil)
	m.persistJob(ctx, job)

	if cfg.Sync {
		if unlock != nil {
			defer func() { _ = unlock(ctx) }()
		}
		if err := m.executeJob(ctx, job, sessionID, nil); err != nil {
			return job.ID, err
		}
		return job.ID, nil
	}

	// Asynchronous: detach from the request lifecycle (so the run can outlive ctx)
	// while preserving its values, and hand lock ownership to the goroutine. The
	// run's outcome is reported through the job record and OnError, not the return.
	runCtx := withoutCancel(ctx)
	go func() {
		if unlock != nil {
			defer func() { _ = unlock(runCtx) }()
		}
		if err := m.executeJob(runCtx, job, sessionID, nil); err != nil {
			m.onErr(runCtx, OnErrorStageRunDream, err)
		}
	}()
	return job.ID, nil
}

// GetDreamStatus returns the current Job record for jobID. It returns (nil, nil)
// when no such job exists (for example after the retention TTL elapsed).
func GetDreamStatus(ctx context.Context, store KVStore, jobID string) (*Job, error) {
	if store == nil {
		return nil, fmt.Errorf("dream: nil store")
	}
	return getJob(ctx, store, jobID)
}

// CancelDream requests cancellation of a pending or running dream job. It marks the
// job canceled in the store and signals any in-process run to abort. Canceling a job
// that has already reached a terminal state is a no-op. Cross-process runs observe
// the canceled status on their next iteration check and stop best-effort.
func CancelDream(ctx context.Context, store KVStore, jobID string) error {
	if store == nil {
		return fmt.Errorf("dream: nil store")
	}
	job, err := getJob(ctx, store, jobID)
	if err != nil {
		return err
	}
	if job == nil {
		return fmt.Errorf("dream: job not found: %s", jobID)
	}
	if job.Status.IsTerminal() {
		return nil
	}
	job.Status = StatusCanceled
	job.EndedAt = time.Now()
	if err := setJob(ctx, store, job, jobTTL); err != nil {
		return err
	}
	signalCancel(jobID)
	return nil
}
