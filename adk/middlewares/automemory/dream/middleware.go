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
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	adkfs "github.com/cloudwego/eino/adk/filesystem"
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
	fsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// copyGlobPattern matches the files dream stages and promotes. It is limited to
// markdown so that, when a memory directory mixes memory files with unrelated files,
// only the memory files are copied into the working copy and back out — matching the
// memory scope used elsewhere (automemory.CandidateGlobPattern).
const copyGlobPattern = "**/*.md"

// cancelPollInterval is how many agent events pass between cross-process cancel
// checks (re-reading the job status from the store) during a drained run.
const cancelPollInterval = 8

// errPromoteInPlaceBestEffort warns that an in-place promotion (OutputDirectory ==
// MemoryDirectory) is a non-atomic, best-effort copy. The source is untouched until
// promotion, but a crash mid-copy can leave the live directory partially updated.
var errPromoteInPlaceBestEffort = fmt.Errorf(
	"dream: promoting in place (output dir == memory dir) via non-atomic copy; " +
		"a crash mid-promotion can leave the directory partially updated")

// New creates middleware that triggers dream automatically after agent runs.
//
// When MiddlewareConfig.Store is nil, an in-process store is used and a warning is
// reported through OnError; see KVStore for why production deployments must inject a
// shared, durable store.
func New[M adk.MessageType](ctx context.Context, cfg *MiddlewareConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if cfg == nil {
		return nil, fmt.Errorf("auto dream config: nil")
	}
	cfg = cloneMiddlewareConfig(cfg)
	if err := applyCoreDefaults(ctx, &cfg.BaseConfig); err != nil {
		return nil, err
	}
	applyScheduleDefaults(cfg)
	// The touched set is keyed by SessionID. With the count gate active but no
	// SessionID, the distinct-session count is pinned at 1 and dreams never trigger;
	// warn so the misconfiguration is visible rather than silent.
	if cfg.MinTouchedSession > 1 && strings.TrimSpace(cfg.SessionID) == "" && cfg.OnError != nil {
		cfg.OnError(ctx, OnErrorStageInit, errCountGateNeedsSessionID)
	}
	return newMiddleware(&cfg.BaseConfig, cfg.SessionID, &scheduleParams{
		minInterval:            cfg.MinInterval,
		minTouchedSession:      cfg.MinTouchedSession,
		scanInterval:           cfg.ScanInterval,
		maxConsecutiveFailures: cfg.MaxConsecutiveFailures,
		runInline:              cfg.RunInline,
	})
}

type middleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]

	cfg               *BaseConfig[M]
	sched             *scheduleParams
	sessionID         string
	resolvedMemoryDir string
	resolvedOutputDir string
	stagingRoot       string
	inputFS           *ainternal.FSBackend
	outputFS          *ainternal.FSBackend
	shell             adkfs.Shell
	sessionSearchTool tool.BaseTool
	now               func() time.Time
}

func newMiddleware[M adk.MessageType](cfg *BaseConfig[M], sessionID string, sched *scheduleParams) (*middleware[M], error) {
	resolvedMemoryDir, err := ainternal.ResolveMemoryDir(cfg.MemoryDirectory)
	if err != nil {
		return nil, fmt.Errorf("auto dream config: resolve memory dir: %w", err)
	}
	outputDir := cfg.OutputDirectory
	if strings.TrimSpace(outputDir) == "" {
		outputDir = cfg.MemoryDirectory
	}
	resolvedOutputDir, err := ainternal.ResolveMemoryDir(outputDir)
	if err != nil {
		return nil, fmt.Errorf("auto dream config: resolve output dir: %w", err)
	}
	stagingRoot := cfg.StagingDirectory
	if strings.TrimSpace(stagingRoot) == "" {
		stagingRoot = filepath.Join(os.TempDir(), "eino-dream")
	}
	resolvedStagingRoot, err := ainternal.ResolveMemoryDir(stagingRoot)
	if err != nil {
		return nil, fmt.Errorf("auto dream config: resolve staging dir: %w", err)
	}

	inputFS, err := ainternal.NewFSBackend(cfg.MemoryBackend, ainternal.FSBackendConfig{
		BaseDir:           resolvedMemoryDir,
		AllowLs:           true,
		NotFoundAsContent: true,
		ErrorPrefix:       "dream input backend",
	})
	if err != nil {
		return nil, err
	}
	outputFS, err := ainternal.NewFSBackend(cfg.MemoryBackend, ainternal.FSBackendConfig{
		BaseDir:           resolvedOutputDir,
		AllowLs:           true,
		NotFoundAsContent: true,
		ErrorPrefix:       "dream output backend",
	})
	if err != nil {
		return nil, err
	}

	var sessionSearchTool tool.BaseTool
	if cfg.SessionStore != nil {
		sessionSearchTool, err = newSessionHistoryGrepTool(cfg.SessionStore)
		if err != nil {
			return nil, err
		}
	}
	m := &middleware[M]{
		cfg:               cfg,
		sched:             sched,
		sessionID:         strings.TrimSpace(sessionID),
		resolvedMemoryDir: resolvedMemoryDir,
		resolvedOutputDir: resolvedOutputDir,
		stagingRoot:       resolvedStagingRoot,
		inputFS:           inputFS,
		outputFS:          outputFS,
		shell:             resolveShell(cfg),
		sessionSearchTool: sessionSearchTool,
		now:               time.Now,
	}
	return m, nil
}

// resolveShell returns the Shell used for staging cleanup. An explicit Config.Shell
// wins; otherwise the MemoryBackend is reused when it also implements filesystem.Shell,
// so a backend struct that satisfies both interfaces need not be configured twice.
func resolveShell[M adk.MessageType](cfg *BaseConfig[M]) adkfs.Shell {
	if cfg.Shell != nil {
		return cfg.Shell
	}
	if sh, ok := cfg.MemoryBackend.(adkfs.Shell); ok {
		return sh
	}
	return nil
}

func (m *middleware[M]) store() KVStore {
	if m.cfg == nil {
		return nil
	}
	return m.cfg.Store
}

func (m *middleware[M]) lockTTL() time.Duration {
	if m.cfg != nil && m.cfg.LockTTL > 0 {
		return m.cfg.LockTTL
	}
	return defaultLockTTL
}

func (m *middleware[M]) AfterAgent(ctx context.Context, _ *adk.TypedChatModelAgentState[M]) (context.Context, error) {
	if m == nil || m.cfg == nil || m.sched == nil {
		return ctx, nil
	}
	sessionID := m.sessionID
	now := m.now()
	touchTTL := m.sched.minInterval * 2
	if err := m.store().AddToSet(ctx, touchSetKey(m.resolvedMemoryDir), sessionID, now, touchTTL); err != nil {
		m.onErr(ctx, OnErrorStageRecordTouch, err)
		return ctx, nil
	}
	if err := m.maybeTrigger(ctx, sessionID, true); err != nil {
		m.onErr(ctx, OnErrorStageRunDream, err)
	}
	return ctx, nil
}

func (m *middleware[M]) maybeTrigger(ctx context.Context, currentSessionID string, excludeCurrent bool) error {
	st, err := getScheduleState(ctx, m.store(), m.resolvedMemoryDir)
	if err != nil {
		return err
	}
	if st == nil {
		st = &ScheduleState{}
	}
	now := m.now()
	if st.NextCheckAt.After(now) {
		return nil
	}
	since := st.LastConsolidatedAt
	if !since.IsZero() && now.Sub(since) < m.sched.minInterval {
		st.NextCheckAt = st.LastConsolidatedAt.Add(m.sched.minInterval)
		return setScheduleState(ctx, m.store(), m.resolvedMemoryDir, st)
	}
	touchedSessions, err := m.store().ListSet(ctx, touchSetKey(m.resolvedMemoryDir), since)
	if err != nil {
		return err
	}
	// Filter in place: ListSet returns a freshly allocated slice the store no longer
	// references, so reusing its backing array is safe.
	filtered := touchedSessions[:0]
	for _, sessionID := range touchedSessions {
		if excludeCurrent && currentSessionID != "" && sessionID == currentSessionID {
			continue
		}
		filtered = append(filtered, sessionID)
	}
	if len(filtered) < m.sched.minTouchedSession {
		st.NextCheckAt = now.Add(m.sched.scanInterval)
		return setScheduleState(ctx, m.store(), m.resolvedMemoryDir, st)
	}
	unlock, ok, err := m.store().AcquireLock(ctx, runLockKey(m.resolvedMemoryDir), m.lockTTL())
	if err != nil || !ok {
		return err
	}

	job := m.newJob(currentSessionID, filtered)
	m.persistJob(ctx, job)

	// Detach from the request lifecycle (which ends when AfterAgent returns) while
	// preserving context values such as loggers and trace spans.
	runBaseCtx := withoutCancel(ctx)
	runFn := func() {
		defer func() { _ = unlock(runBaseCtx) }()
		runErr := m.executeJob(runBaseCtx, job, currentSessionID, filtered)
		if runErr != nil && job.Status != StatusCanceled {
			st.ConsecutiveFailures++
			// After repeated failures, advance the window so the next run does not
			// replay the same failing sessions forever.
			if st.ConsecutiveFailures >= m.sched.maxConsecutiveFailures {
				st.LastConsolidatedAt = m.now()
				st.ConsecutiveFailures = 0
			}
			st.NextCheckAt = m.now().Add(m.sched.scanInterval)
			_ = setScheduleState(runBaseCtx, m.store(), m.resolvedMemoryDir, st)
			return
		}
		if runErr != nil { // canceled: back off without counting as a failure
			st.NextCheckAt = m.now().Add(m.sched.scanInterval)
			_ = setScheduleState(runBaseCtx, m.store(), m.resolvedMemoryDir, st)
			return
		}
		st.LastConsolidatedAt = m.now()
		st.ConsecutiveFailures = 0
		st.NextCheckAt = st.LastConsolidatedAt.Add(m.sched.minInterval)
		_ = setScheduleState(runBaseCtx, m.store(), m.resolvedMemoryDir, st)
		// Touches consumed by this run are no longer needed; drop them so the set
		// does not grow without bound.
		_ = m.store().PruneSet(runBaseCtx, touchSetKey(m.resolvedMemoryDir), st.LastConsolidatedAt)
	}
	if m.sched.runInline {
		runFn()
		return nil
	}
	go runFn()
	return nil
}

// newJob builds a pending Job record for the given scope.
func (m *middleware[M]) newJob(sessionID string, sessionScope []string) *Job {
	jobID := newJobID(m.resolvedMemoryDir + sessionID)
	scope := sessionScope
	if len(scope) == 0 && sessionID != "" {
		scope = []string{sessionID}
	}
	return &Job{
		ID:         jobID,
		Status:     StatusPending,
		InputDir:   m.resolvedMemoryDir,
		OutputDir:  m.resolvedOutputDir,
		StagingDir: filepath.Join(m.stagingRoot, jobID),
		SessionIDs: append([]string(nil), scope...),
		CreatedAt:  m.now(),
	}
}

func (m *middleware[M]) persistJob(ctx context.Context, job *Job) {
	if m.store() == nil || job == nil {
		return
	}
	if err := setJob(ctx, m.store(), job, jobTTL); err != nil {
		m.onErr(ctx, OnErrorStagePersistJob, err)
	}
}

// executeJob runs one dream job through running -> completed/failed/canceled,
// persisting status transitions. It returns the consolidation error (nil on success).
func (m *middleware[M]) executeJob(ctx context.Context, job *Job, sessionID string, touchedSessions []string) error {
	runCtx, cancel := context.WithCancel(ctx)
	handle := &cancelHandle{cancel: cancel}
	defer cancel()
	registerCancel(job.ID, handle)
	defer unregisterCancel(job.ID)

	job.Status = StatusRunning
	m.persistJob(ctx, job)

	err := m.consolidate(runCtx, job, sessionID, touchedSessions)

	job.EndedAt = m.now()
	switch {
	case err != nil && m.wasCanceled(job.ID, runCtx, err):
		job.Status = StatusCanceled
	case err != nil:
		job.Status = StatusFailed
		job.ErrMsg = err.Error()
	default:
		job.Status = StatusCompleted
	}
	// Do not resurrect a job that was canceled out-of-band into a completed state.
	if job.Status == StatusCompleted && m.store() != nil {
		if latest, gerr := getJob(ctx, m.store(), job.ID); gerr == nil && latest != nil && latest.Status == StatusCanceled {
			job.Status = StatusCanceled
		}
	}
	m.persistJob(ctx, job)
	return err
}

func (m *middleware[M]) wasCanceled(jobID string, ctx context.Context, err error) bool {
	if errors.Is(err, errDreamCanceled) {
		return true
	}
	return errors.Is(jobCancelCause(jobID, ctx), errDreamCanceled)
}

// consolidate seeds a staging working copy, runs the dream agent against it, then
// promotes the result to the output directory. The input directory is never modified.
func (m *middleware[M]) consolidate(ctx context.Context, job *Job, sessionID string, touchedSessions []string) error {
	stagingFS, err := ainternal.NewFSBackend(m.cfg.MemoryBackend, ainternal.FSBackendConfig{
		BaseDir:           job.StagingDir,
		AllowLs:           true,
		NotFoundAsContent: true,
		ErrorPrefix:       "dream staging backend",
	})
	if err != nil {
		return err
	}

	// Seed staging with a copy of the input directory so the model edits a working
	// copy. Tolerate seed errors (e.g. an empty/new memory directory).
	if seedErr := m.copyTree(ctx, m.inputFS, m.resolvedMemoryDir, stagingFS); seedErr != nil {
		m.onErr(ctx, OnErrorStageSeedStaging, seedErr)
	}

	fsHandler, err := fsmw.NewTyped[M](ctx, &fsmw.MiddlewareConfig{
		Backend:        stagingFS,
		GrepToolConfig: &fsmw.ToolConfig{Disable: true},
	})
	if err != nil {
		return err
	}
	agent, err := m.newDreamAgent(ctx, fsHandler)
	if err != nil {
		return err
	}

	prompt := buildConsolidationPrompt(job.StagingDir, touchedSessions, m.sessionSearchTool != nil)
	searchSessionIDs := touchedSessions
	if len(searchSessionIDs) == 0 && sessionID != "" {
		searchSessionIDs = []string{sessionID}
	}
	runCtx := withDreamRunMeta(ctx, &dreamRunMeta{
		MemoryDirectory:  m.resolvedMemoryDir,
		SessionID:        sessionID,
		SearchSessionIDs: append([]string(nil), searchSessionIDs...),
	})
	iter := agent.Run(runCtx, &adk.TypedAgentInput[M]{Messages: []M{makeUserMsg[M](prompt)}})
	if m.cfg.HandleIterator != nil {
		teed, observedErr := m.teeIteratorErr(iter)
		if err := m.cfg.HandleIterator(runCtx, teed); err != nil {
			return err
		}
		if err := observedErr(); err != nil {
			return err
		}
	} else if err := m.drainWithCancel(runCtx, job.ID, iter); err != nil {
		return err
	}

	return m.promote(ctx, stagingFS, job.StagingDir)
}

// teeIteratorErr forwards every event from src to a fresh iterator handed to a custom
// HandleIterator, while recording the first event.Err seen on the stream. The
// returned func reports that error after the handler returns. This keeps failure
// detection correct regardless of whether the handler inspects event.Err itself.
func (m *middleware[M]) teeIteratorErr(src *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) (*adk.AsyncIterator[*adk.TypedAgentEvent[M]], func() error) {
	out, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[M]]()
	var mu sync.Mutex
	var firstErr error
	go func() {
		defer gen.Close()
		for {
			ev, ok := src.Next()
			if !ok {
				return
			}
			if ev != nil && ev.Err != nil {
				mu.Lock()
				if firstErr == nil {
					firstErr = ev.Err
				}
				mu.Unlock()
			}
			gen.Send(ev)
		}
	}()
	return out, func() error {
		mu.Lock()
		defer mu.Unlock()
		return firstErr
	}
}

// drainWithCancel drains the dream agent's event stream, aborting if the run is
// canceled (in-process via context, or cross-process via the job status in the store).
func (m *middleware[M]) drainWithCancel(ctx context.Context, jobID string, iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) error {
	i := 0
	for {
		ev, ok := iter.Next()
		if !ok {
			return nil
		}
		if ev != nil && ev.Err != nil {
			return ev.Err
		}
		if err := ctx.Err(); err != nil {
			return jobCancelCause(jobID, ctx)
		}
		i++
		if m.store() != nil && i%cancelPollInterval == 0 {
			if job, _ := getJob(ctx, m.store(), jobID); job != nil && job.Status == StatusCanceled {
				return errDreamCanceled
			}
		}
	}
}

// promote copies the staged result to the output directory, holding the output
// directory lock so a concurrent dream (or, by convention, automemory extraction)
// does not interleave writes. Promotion is a non-atomic, best-effort copy.
func (m *middleware[M]) promote(ctx context.Context, stagingFS *ainternal.FSBackend, stagingDir string) error {
	if m.store() != nil {
		unlock, ok, err := m.store().AcquireLock(ctx, dirLockKey(m.resolvedOutputDir), m.lockTTL())
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("dream: output directory %q is busy", m.resolvedOutputDir)
		}
		defer func() { _ = unlock(ctx) }()
	}
	if m.resolvedOutputDir == m.resolvedMemoryDir {
		m.onErr(ctx, OnErrorStagePromote, errPromoteInPlaceBestEffort)
	}
	if err := m.copyTree(ctx, stagingFS, stagingDir, m.outputFS); err != nil {
		return err
	}
	m.cleanupStaging(ctx, stagingDir)
	return nil
}

// cleanupStaging removes the staging directory using the resolved Shell. When no
// Shell is available the staging directory is left in place.
func (m *middleware[M]) cleanupStaging(ctx context.Context, stagingDir string) {
	if m.shell == nil {
		return
	}
	if _, err := m.shell.Execute(ctx, &adkfs.ExecuteRequest{
		Command: fmt.Sprintf("rm -rf %q", stagingDir),
	}); err != nil {
		m.onErr(ctx, OnErrorStageCleanup, err)
	}
}

// copyTree copies the memory files (markdown, per copyGlobPattern) from src
// (bounded at srcBase) to dst, preserving relative paths. Non-markdown files in the
// memory directory are left untouched.
func (m *middleware[M]) copyTree(ctx context.Context, src *ainternal.FSBackend, srcBase string, dst *ainternal.FSBackend) error {
	files, err := src.GlobInfo(ctx, &adkfs.GlobInfoRequest{Path: srcBase, Pattern: copyGlobPattern})
	if err != nil {
		return err
	}
	for _, fi := range files {
		if fi.IsDir {
			continue
		}
		rel, relErr := filepath.Rel(srcBase, fi.Path)
		if relErr != nil {
			rel = filepath.Base(fi.Path)
		}
		rel = filepath.ToSlash(rel)
		fc, err := src.Read(ctx, &adkfs.ReadRequest{FilePath: rel})
		if err != nil {
			return err
		}
		if fc == nil {
			continue
		}
		if err := dst.Write(ctx, &adkfs.WriteRequest{FilePath: rel, Content: fc.Content}); err != nil {
			return err
		}
	}
	return nil
}

func (m *middleware[M]) newDreamAgent(ctx context.Context, fsHandler adk.TypedChatModelAgentMiddleware[M]) (*adk.TypedChatModelAgent[M], error) {
	tools := make([]tool.BaseTool, 0, 1)
	if m.sessionSearchTool != nil {
		tools = append(tools, m.sessionSearchTool)
	}
	maxIterations := m.cfg.MaxIterations
	if maxIterations <= 0 {
		maxIterations = defaultMaxIterations
	}
	agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[M]{
		Name:        "automemory_dream",
		Description: "Internal auto dream consolidation agent",
		Model:       m.cfg.Model,
		Handlers:    []adk.TypedChatModelAgentMiddleware[M]{fsHandler},
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{
			Tools:               tools,
			ToolCallMiddlewares: []compose.ToolMiddleware{m.toolErrorRecoveryMiddleware(ctx)},
		}},
		MaxIterations: maxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("auto dream create agent: %w", err)
	}
	return agent, nil
}

// toolErrorRecoveryMiddleware turns a failed tool call into a message fed back to the
// model, so a recoverable failure (e.g. an edit_file whose old_string no longer
// matches) lets the agent retry or switch tools rather than aborting the whole run.
// The error is still reported through OnError for observability. Runtime errors that
// the model cannot act on (model-call failures, context cancellation) are not tool
// errors and are unaffected — they continue to fail the run.
func (m *middleware[M]) toolErrorRecoveryMiddleware(ctx context.Context) compose.ToolMiddleware {
	recover := func(name string, err error) string {
		m.onErr(ctx, OnErrorStageToolCall, fmt.Errorf("tool %q failed: %w", name, err))
		return fmt.Sprintf("Tool %q failed: %v\nThis is not fatal. Re-check your arguments "+
			"(for edits, read_file the target again and correct old_string), then retry or use a "+
			"different tool. Do not repeat the same failing call.", name, err)
	}
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, in *compose.ToolInput) (*compose.ToolOutput, error) {
				out, err := next(ctx, in)
				if err != nil {
					return &compose.ToolOutput{Result: recover(in.Name, err)}, nil
				}
				return out, nil
			}
		},
		EnhancedInvokable: func(next compose.EnhancedInvokableToolEndpoint) compose.EnhancedInvokableToolEndpoint {
			return func(ctx context.Context, in *compose.ToolInput) (*compose.EnhancedInvokableToolOutput, error) {
				out, err := next(ctx, in)
				if err != nil {
					return &compose.EnhancedInvokableToolOutput{
						Result: &schema.ToolResult{
							Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: recover(in.Name, err)}},
						},
					}, nil
				}
				return out, nil
			}
		},
	}
}

func (m *middleware[M]) onErr(ctx context.Context, stage ErrorStage, err error) {
	if err == nil || m == nil || m.cfg == nil || m.cfg.OnError == nil {
		return
	}
	m.cfg.OnError(ctx, stage, err)
}

func makeUserMsg[M adk.MessageType](text string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.UserMessage(text)).(M)
	case *schema.AgenticMessage:
		return any(schema.UserAgenticMessage(text)).(M)
	default:
		panic("unreachable")
	}
}
