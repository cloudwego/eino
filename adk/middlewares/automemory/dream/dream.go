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

// Package dream consolidates automemory-managed memory directories: it is a
// reflective "sleep" pass that an agent runs over its accumulated memory files to
// merge duplicates, drop stale entries, convert relative dates to absolute ones, and
// keep the MEMORY.md index tidy, so future sessions orient quickly.
//
// # Two ways to run
//
// Scheduled (middleware): New returns a chat-model-agent middleware that, after each
// agent run, records the session and triggers a consolidation once enough sessions
// have accumulated and a minimum interval has elapsed. Configure it with
// MiddlewareConfig.
//
//	mw, err := dream.New(ctx, &dream.MiddlewareConfig[*schema.Message]{
//	    BaseConfig: dream.BaseConfig[*schema.Message]{
//	        MemoryDirectory: memDir,
//	        MemoryBackend:   backend,
//	        Model:           model,
//	        Store:           sharedStore, // production: a shared, durable KVStore
//	    },
//	    SessionID:         sessionID,
//	    MinInterval:       24 * time.Hour,
//	    MinTouchedSession: 5,
//	})
//
// On demand: Run starts one consolidation and returns a job id. It is asynchronous
// by default — the job runs in the background and is tracked via GetDreamStatus /
// CancelDream; set RunConfig.Sync to block until it finishes. Configure it with
// RunConfig, which adds the session this run is for and the Sync flag.
//
//	jobID, err := dream.Run(ctx, &dream.RunConfig[*schema.Message]{
//	    BaseConfig: dream.BaseConfig[*schema.Message]{
//	        MemoryDirectory: memDir,
//	        MemoryBackend:   backend,
//	        Model:           model,
//	        Store:           sharedStore,
//	    },
//	    SessionID: sessionID,
//	})
//
// # Staged, non-destructive output
//
// A run never mutates the input MemoryDirectory. The source is copied into a
// per-run working copy under StagingDirectory; the model edits only that copy; on
// success the result is promoted (a best-effort, non-atomic copy) to OutputDirectory
// (which defaults to MemoryDirectory for in-place consolidation). A failed or
// canceled run therefore leaves the source untouched. When a Shell is available
// (configured, or auto-derived from a MemoryBackend that also implements
// filesystem.Shell) the staging copy is removed afterward.
//
// # Lifecycle
//
// Both entry points create a Job record in the KVStore. GetDreamStatus polls a
// job's status (pending, running, completed, failed, canceled) and CancelDream
// aborts a pending or running one. With the in-process default store these queries
// are same-process only; inject a shared store for cross-instance visibility and for
// the run lock that prevents concurrent writes to the same directory.
//
// # Coordination store
//
// The KVStore persists job records, the run lock, and (for the scheduled path) the
// touched-session set and schedule state. The default NewLocalKVStore is
// single-process only and emits a warning through OnError; production deployments
// MUST inject a shared, durable KVStore (for example Redis-backed). See KVStore.
//
// # Source layout
//
//   - api.go        — Run, GetDreamStatus, CancelDream (direct operations)
//   - middleware.go — New plus the scheduling/staging/promotion internals
//   - config.go     — BaseConfig, RunConfig, and MiddlewareConfig
//   - store.go      — the KVStore interface and the in-process default
//   - lifecycle.go  — the Job model, statuses, and cancel plumbing
//   - consts.go     — ErrorStage values reported through OnError
//   - prompt.go     — the consolidation prompt
//   - session.go    — the optional grep_session_history tool
package dream
