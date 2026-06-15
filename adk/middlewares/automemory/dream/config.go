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

// Package dream provides scheduled consolidation middleware built on top of
// automemory-managed session files.
package dream

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/automemory"
	"github.com/cloudwego/eino/components/model"
)

const (
	defaultSessionKey        = "__eino_automemory_dream_session_id__"
	defaultMinInterval       = 24 * time.Hour
	defaultMinTouchedSession = 5
	defaultScanInterval      = 10 * time.Minute
	defaultLockTTL           = time.Hour
)

// OnError handles non-fatal dream errors.
// Optional. Nil means ignore the error.
type OnError func(ctx context.Context, stage string, err error)

// HandleIterator handles the dream sub-agent event stream.
// Optional. Nil means dream drains the iterator itself.
type HandleIterator[M adk.MessageType] func(ctx context.Context, iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) error

// Config configures auto dream for both `New(...)` and `Run(...)`.
type Config[M adk.MessageType] struct {
	// MemoryDirectory is the memory root directory.
	// Required. Relative paths are resolved during init.
	MemoryDirectory string

	// MemoryBackend reads and updates memory files.
	// Required.
	MemoryBackend automemory.Backend

	// Model is the model used by the internal dream agent.
	// Required.
	Model model.BaseModel[M]

	// SessionIDFunc resolves the current session ID.
	// Optional. Default: a generated session-scoped ID.
	SessionIDFunc automemory.SessionIDFunc[M]

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

	// Schedule controls middleware-triggered runs only.
	// Optional. `Run(...)` ignores it.
	Schedule *ScheduleConfig

	// HandleIterator overrides iterator consumption.
	// Optional. Default: nil.
	HandleIterator HandleIterator[M]
}

// ScheduleConfig controls middleware-triggered runs.
type ScheduleConfig struct {
	// MinInterval is the minimum interval between successful runs.
	// Optional. Default: 24h.
	MinInterval time.Duration

	// MinTouchedSession is the minimum touched-session count before a run.
	// Optional. Default: 5.
	MinTouchedSession int

	// ScanInterval is the retry delay when the session threshold is not met.
	// Optional. Default: 10m.
	ScanInterval time.Duration

	// LockTTL is the lease for the per-memory-directory run lock.
	// Optional. Default: 1h.
	LockTTL time.Duration

	// Store persists touched sessions, schedule state, and run locks.
	// Optional. Default: in-process `LocalStore`.
	Store Store

	// RunInline runs triggered dreams in the `AfterAgent` call path.
	// Optional. Default: false.
	RunInline bool
}

func applyCoreDefaults[M adk.MessageType](cfg *Config[M]) error {
	if cfg == nil {
		return fmt.Errorf("auto dream config: nil")
	}
	if cfg.MemoryDirectory == "" || cfg.MemoryBackend == nil || cfg.Model == nil {
		return fmt.Errorf("auto dream config: invalid")
	}
	if cfg.SessionIDFunc == nil {
		cfg.SessionIDFunc = defaultSessionIDFunc[M]
	}
	return nil
}

func cloneConfig[M adk.MessageType](cfg *Config[M]) *Config[M] {
	if cfg == nil {
		return nil
	}

	cp := *cfg
	if cfg.Schedule != nil {
		scheduleCopy := *cfg.Schedule
		cp.Schedule = &scheduleCopy
	}
	return &cp
}

func applyScheduleDefaults[M adk.MessageType](cfg *Config[M]) error {
	if err := applyCoreDefaults(cfg); err != nil {
		return err
	}
	if cfg.Schedule == nil {
		cfg.Schedule = &ScheduleConfig{}
	}
	if cfg.Schedule.MinInterval <= 0 {
		cfg.Schedule.MinInterval = defaultMinInterval
	}
	if cfg.Schedule.MinTouchedSession <= 0 {
		cfg.Schedule.MinTouchedSession = defaultMinTouchedSession
	}
	if cfg.Schedule.ScanInterval <= 0 {
		cfg.Schedule.ScanInterval = defaultScanInterval
	}
	if cfg.Schedule.LockTTL <= 0 {
		cfg.Schedule.LockTTL = defaultLockTTL
	}
	if cfg.Schedule.Store == nil {
		cfg.Schedule.Store = NewLocalStore()
	}
	return nil
}

func defaultSessionIDFunc[M adk.MessageType](ctx context.Context, _ *adk.TypedChatModelAgentState[M]) (string, error) {
	if v, ok := adk.GetSessionValue(ctx, defaultSessionKey); ok {
		if s, ok := v.(string); ok && s != "" {
			return s, nil
		}
	}
	s := fmt.Sprintf("dream-%d", time.Now().UnixNano())
	adk.AddSessionValue(ctx, defaultSessionKey, s)
	return s, nil
}
