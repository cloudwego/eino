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
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/middlewares/automemory"
	"github.com/cloudwego/eino/components/model"
)

const (
	defaultMinInterval            = 24 * time.Hour
	defaultMinTouchedSession      = 5
	defaultScanInterval           = 10 * time.Minute
	defaultLockTTL                = time.Hour
	defaultMaxConsecutiveFailures = 3
	defaultMaxIterations          = 24
)

// errLocalKVStoreSingleProcess is surfaced through OnError when Store falls back to
// the in-process default. It is a warning, not a fatal error: dreams still run, but
// coordination (run lock, job status, scheduler touch counting) is not shared across
// instances.
var errLocalKVStoreSingleProcess = fmt.Errorf(
	"dream: Store not set, using in-process KVStore; this is single-process only, so run " +
		"locks and job status are not shared across instances and scheduled dreams will not " +
		"trigger when the middleware is constructed per session across instances " +
		"(inject a shared, durable KVStore in production)")

// errCountGateNeedsSessionID is surfaced through OnError when the scheduled count
// gate is active (MinTouchedSession > 1) but no SessionID is configured. The touched
// set is keyed by SessionID, so without one every run records the same empty member,
// the distinct-session count is stuck at 1, and the gate never opens. It is a
// warning, not a fatal error.
var errCountGateNeedsSessionID = fmt.Errorf(
	"dream: MinTouchedSession > 1 but SessionID is empty; touched sessions are " +
		"counted by SessionID, so the count stays at 1 and scheduled dreams will never " +
		"trigger (set a per-session SessionID, or set MinTouchedSession to 1 to gate on " +
		"MinInterval alone)")

// OnError handles non-fatal dream errors. The stage argument is one of the
// OnErrorStage* constants identifying where the failure occurred.
// Optional. Nil means ignore the error.
type OnError func(ctx context.Context, stage ErrorStage, err error)

// HandleIterator handles the dream sub-agent event stream. The handler is
// responsible for draining the iterator (calling Next until ok==false); it may
// return an error to fail the run explicitly. Even if it does not inspect
// event.Err, dream still records the first agent error on the stream and fails the
// job accordingly, so a failed consolidation is never reported as completed.
// Optional. Nil means dream drains the iterator itself.
type HandleIterator[M adk.MessageType] func(ctx context.Context, iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) error

// BaseConfig holds the resources and policy shared by every dream entry point: what
// dream reads and writes, the model it runs, and how it coordinates. It carries no
// per-invocation or scheduling fields; RunConfig and MiddlewareConfig embed it and
// add those.
type BaseConfig[M adk.MessageType] struct {
	// MemoryDirectory is the memory root directory.
	// Required. Relative paths are resolved during init.
	// The dream run reads from this directory but never modifies it: the model
	// edits a working copy under StagingDirectory, which is then promoted to
	// OutputDirectory.
	MemoryDirectory string

	// OutputDirectory is where consolidated memory is written when a run succeeds.
	// Optional. Default: MemoryDirectory (in-place consolidation).
	//
	// When equal to MemoryDirectory, the staged result is promoted over the source
	// after the run. The source is left untouched until promotion, so a failed or
	// canceled run never leaves the source half-processed. Promotion is a
	// best-effort copy (not atomic across files); a warning is reported via OnError.
	OutputDirectory string

	// StagingDirectory is the root under which per-run working copies are created
	// (one subdirectory per job id). The model's writes are bounded to the staging
	// subdirectory; the source MemoryDirectory is copied in before the run.
	// Optional. Default: a directory under os.TempDir().
	StagingDirectory string

	// MemoryBackend reads and updates memory files.
	// Required.
	MemoryBackend automemory.Backend

	// Shell, when set, is used only to clean up the staging subdirectory after a
	// successful promotion (rm -rf).
	// Optional. When nil, the Shell is auto-derived from MemoryBackend if that value
	// also implements filesystem.Shell (the common case for sandbox/filesystem
	// backends that satisfy both interfaces in one struct), so it need not be
	// configured twice. Set this field only to override that, or to supply a Shell
	// when MemoryBackend is not one. When neither yields a Shell, staging directories
	// are left in place under StagingDirectory.
	Shell filesystem.Shell

	// Model is the model used by the internal dream agent.
	// Required.
	Model model.BaseModel[M]

	// MaxIterations caps the dream agent's tool-call loop.
	// Consolidation reads the index, skims and reads multiple topic files, then
	// writes/edits several files plus the index, so this needs headroom on large
	// memory directories.
	// Optional. Default: 24.
	MaxIterations int

	// OnError handles non-fatal runtime errors.
	// Optional. Default: nil.
	OnError OnError

	// SessionStore enables session timeline lookup through `grep_session_history`.
	// Optional. Default: nil.
	//
	// When nil, dream consolidates using only memory files plus scheduler-provided
	// touch signals; no session-history search tool is exposed to the model.
	//
	// When set, dream exposes `grep_session_history` for the sessions included in
	// the current run scope:
	//   - middleware-triggered runs search the touched sessions selected by the scheduler
	//   - manual `Run(...)` searches the provided/current session only
	SessionStore adk.SessionEventStore[M]

	// Store persists job records and the per-memory-directory run lock, enabling
	// GetDreamStatus/CancelDream and preventing concurrent writes to the same memory
	// directory. The scheduled path additionally uses it for touched sessions and
	// schedule state.
	// Optional. Default: in-process store from NewLocalKVStore (single-process only;
	// a warning is emitted through OnError when this default is used).
	//
	// See KVStore for why production deployments MUST inject a shared, durable store.
	Store KVStore

	// LockTTL is the lease for the per-memory-directory run lock.
	// It must comfortably exceed the longest expected dream runtime: if a run
	// outlives the lease, another process may acquire the lock and write the same
	// memory directory concurrently.
	// Optional. Default: 1h.
	LockTTL time.Duration

	// HandleIterator overrides iterator consumption.
	// Optional. Default: nil.
	HandleIterator HandleIterator[M]
}

// RunConfig is the input to Run(...): a one-shot consolidation. It embeds BaseConfig
// and adds the session this run is for.
type RunConfig[M adk.MessageType] struct {
	BaseConfig[M]

	// SessionID identifies the session this manual run is associated with. It scopes
	// the optional grep_session_history tool.
	// Optional. When empty, dream runs without session grouping.
	SessionID string

	// Sync makes Run block until the consolidation reaches a terminal state and
	// return its error. When false (the default), Run starts the consolidation in a
	// background goroutine and returns the job id immediately with a nil error; poll
	// GetDreamStatus and stop it with CancelDream using that id.
	//
	// Note: with the in-process default Store, an asynchronous run is only observable
	// from the same process and must outlive it; inject a shared, durable Store (and
	// keep the process alive) to track an async run reliably.
	// Optional. Default: false (asynchronous).
	Sync bool
}

// MiddlewareConfig configures the scheduled dream middleware created by New(...). It
// embeds BaseConfig and adds the per-instance session plus the trigger knobs that
// only apply to automatic, middleware-driven runs.
type MiddlewareConfig[M adk.MessageType] struct {
	BaseConfig[M]

	// SessionID identifies the session this middleware instance serves. The scheduled
	// trigger counts distinct SessionIDs that have touched the memory directory, so a
	// per-session SessionID is required for MinTouchedSession > 1 to be meaningful;
	// without one the count stays at 1. New warns through OnError when this is
	// misconfigured.
	// Optional. When empty, the count gate effectively degrades to 1.
	SessionID string

	// MinInterval is the minimum interval between successful runs.
	// Optional. Default: 24h.
	MinInterval time.Duration

	// MinTouchedSession is the minimum number of distinct sessions (by SessionID)
	// that must touch the memory directory before a run. Set it to 1 to gate on
	// MinInterval alone. Values > 1 require a per-session SessionID.
	// Optional. Default: 5.
	MinTouchedSession int

	// ScanInterval is the retry delay when the session threshold is not met.
	// Optional. Default: 10m.
	ScanInterval time.Duration

	// MaxConsecutiveFailures caps how many times a failing run is retried against
	// the same unconsolidated window before the window is advanced to avoid
	// replaying the same sessions forever.
	// Optional. Default: 3.
	MaxConsecutiveFailures int

	// RunInline runs triggered dreams in the `AfterAgent` call path.
	// Optional. Default: false.
	RunInline bool
}

// scheduleParams carries the resolved scheduler knobs onto the middleware. It is nil
// for a Run(...)-only construction, which never triggers on a schedule.
type scheduleParams struct {
	minInterval            time.Duration
	minTouchedSession      int
	scanInterval           time.Duration
	maxConsecutiveFailures int
	runInline              bool
}

func applyCoreDefaults[M adk.MessageType](ctx context.Context, cfg *BaseConfig[M]) error {
	if cfg == nil {
		return fmt.Errorf("auto dream config: nil")
	}
	if cfg.MemoryDirectory == "" || cfg.MemoryBackend == nil || cfg.Model == nil {
		return fmt.Errorf("auto dream config: invalid")
	}
	if cfg.LockTTL <= 0 {
		cfg.LockTTL = defaultLockTTL
	}
	if cfg.Store == nil {
		cfg.Store = NewLocalKVStore()
		if cfg.OnError != nil {
			cfg.OnError(ctx, OnErrorStageInit, errLocalKVStoreSingleProcess)
		}
	}
	return nil
}

func cloneRunConfig[M adk.MessageType](cfg *RunConfig[M]) *RunConfig[M] {
	if cfg == nil {
		return nil
	}
	cp := *cfg
	return &cp
}

func cloneMiddlewareConfig[M adk.MessageType](cfg *MiddlewareConfig[M]) *MiddlewareConfig[M] {
	if cfg == nil {
		return nil
	}
	cp := *cfg
	return &cp
}

func applyScheduleDefaults[M adk.MessageType](cfg *MiddlewareConfig[M]) {
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = defaultMinInterval
	}
	if cfg.MinTouchedSession <= 0 {
		cfg.MinTouchedSession = defaultMinTouchedSession
	}
	if cfg.ScanInterval <= 0 {
		cfg.ScanInterval = defaultScanInterval
	}
	if cfg.MaxConsecutiveFailures <= 0 {
		cfg.MaxConsecutiveFailures = defaultMaxConsecutiveFailures
	}
}
