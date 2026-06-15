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
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
	fsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

const (
	stageResolveSessionID = "resolve_session_id"
	stageRecordTouch      = "record_touch"
	stageRunDream         = "run_dream"
)

type middleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]

	cfg               *Config[M]
	resolvedMemoryDir string
	fsHandler         adk.TypedChatModelAgentMiddleware[M]
	sessionSearchTool tool.BaseTool
	now               func() time.Time
}

// New creates middleware that triggers dream automatically after agent runs.
func New[M adk.MessageType](ctx context.Context, cfg *Config[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	cfg = cloneConfig(cfg)
	if err := applyScheduleDefaults(cfg); err != nil {
		return nil, err
	}
	return newMiddleware(ctx, cfg)
}

// Run executes a dream immediately, without schedule gating or locking.
func Run[M adk.MessageType](ctx context.Context, cfg *Config[M], req *RunRequest) error {
	cfg = cloneConfig(cfg)
	if err := applyCoreDefaults(cfg); err != nil {
		return err
	}
	m, err := newMiddleware(ctx, cfg)
	if err != nil {
		return err
	}
	if req == nil {
		req = &RunRequest{}
	}
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID, err = cfg.SessionIDFunc(ctx, nil)
		if err != nil {
			m.onErr(ctx, stageResolveSessionID, err)
			return err
		}
	}
	return m.runDream(ctx, sessionID, nil)
}

type RunRequest struct {
	// SessionID identifies the current session.
	// Optional. When empty, `SessionIDFunc` is used.
	SessionID string
}

func newMiddleware[M adk.MessageType](ctx context.Context, cfg *Config[M]) (*middleware[M], error) {
	resolvedMemoryDir, err := ainternal.ResolveMemoryDir(cfg.MemoryDirectory)
	if err != nil {
		return nil, fmt.Errorf("auto dream config: resolve memory dir: %w", err)
	}
	writeFSBackend, err := ainternal.NewFSBackend(cfg.MemoryBackend, ainternal.FSBackendConfig{
		BaseDir:           resolvedMemoryDir,
		AllowLs:           true,
		NotFoundAsContent: true,
		ErrorPrefix:       "dream fs backend",
	})
	if err != nil {
		return nil, err
	}
	fsHandler, err := fsmw.NewTyped[M](ctx, &fsmw.MiddlewareConfig{
		Backend:        writeFSBackend,
		GrepToolConfig: &fsmw.ToolConfig{Disable: true},
	})
	if err != nil {
		return nil, err
	}
	var sessionSearchTool tool.BaseTool
	if cfg.SessionStore != nil {
		sessionSearchTool, err = newSessionHistoryGrepTool(cfg.SessionStore)
	}
	m := &middleware[M]{
		TypedBaseChatModelAgentMiddleware: adk.TypedBaseChatModelAgentMiddleware[M]{},
		cfg:                               cfg,
		resolvedMemoryDir:                 resolvedMemoryDir,
		fsHandler:                         fsHandler,
		sessionSearchTool:                 sessionSearchTool,
		now:                               time.Now,
	}
	return m, nil
}

func (m *middleware[M]) AfterAgent(ctx context.Context, state *adk.TypedChatModelAgentState[M]) (context.Context, error) {
	if m == nil || m.cfg == nil || m.cfg.Schedule == nil {
		return ctx, nil
	}
	sessionID, err := m.cfg.SessionIDFunc(ctx, state)
	if err != nil {
		m.onErr(ctx, stageResolveSessionID, err)
		return ctx, nil
	}
	now := m.now()
	if err := m.cfg.Schedule.Store.RecordSessionTouch(ctx, m.resolvedMemoryDir, sessionID, now); err != nil {
		m.onErr(ctx, stageRecordTouch, err)
		return ctx, nil
	}
	if err := m.maybeTrigger(ctx, sessionID, true); err != nil {
		m.onErr(ctx, stageRunDream, err)
	}
	return ctx, nil
}

func (m *middleware[M]) maybeTrigger(ctx context.Context, currentSessionID string, excludeCurrent bool) error {
	st, err := m.cfg.Schedule.Store.GetScheduleState(ctx, m.resolvedMemoryDir)
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
	if !since.IsZero() && now.Sub(since) < m.cfg.Schedule.MinInterval {
		st.NextCheckAt = st.LastConsolidatedAt.Add(m.cfg.Schedule.MinInterval)
		return m.cfg.Schedule.Store.SetScheduleState(ctx, m.resolvedMemoryDir, st)
	}
	touchedSessions, err := m.cfg.Schedule.Store.ListSessionsTouchedSince(ctx, m.resolvedMemoryDir, since)
	if err != nil {
		return err
	}
	filtered := touchedSessions[:0]
	for _, sessionID := range touchedSessions {
		if excludeCurrent && currentSessionID != "" && sessionID == currentSessionID {
			continue
		}
		filtered = append(filtered, sessionID)
	}
	if len(filtered) < m.cfg.Schedule.MinTouchedSession {
		st.NextCheckAt = now.Add(m.cfg.Schedule.ScanInterval)
		return m.cfg.Schedule.Store.SetScheduleState(ctx, m.resolvedMemoryDir, st)
	}
	unlock, ok, err := m.cfg.Schedule.Store.AcquireRunLock(ctx, m.resolvedMemoryDir, m.cfg.Schedule.LockTTL)
	if err != nil || !ok {
		return err
	}
	runFn := func() {
		defer func() { _ = unlock(context.Background()) }()
		if err := m.runDream(context.Background(), currentSessionID, filtered); err != nil {
			m.onErr(context.Background(), stageRunDream, err)
			st.NextCheckAt = m.now().Add(m.cfg.Schedule.ScanInterval)
			_ = m.cfg.Schedule.Store.SetScheduleState(context.Background(), m.resolvedMemoryDir, st)
			return
		}
		st.LastConsolidatedAt = m.now()
		st.NextCheckAt = st.LastConsolidatedAt.Add(m.cfg.Schedule.MinInterval)
		_ = m.cfg.Schedule.Store.SetScheduleState(context.Background(), m.resolvedMemoryDir, st)
	}
	if m.cfg.Schedule.RunInline {
		runFn()
		return nil
	}
	go runFn()
	return nil
}

func (m *middleware[M]) runDream(ctx context.Context, sessionID string, touchedSessions []string) error {
	agent, err := m.newDreamAgent(ctx)
	if err != nil {
		return err
	}
	prompt := buildConsolidationPrompt(m.resolvedMemoryDir, touchedSessions, m.sessionSearchTool != nil)
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
		return m.cfg.HandleIterator(runCtx, iter)
	}
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			return ev.Err
		}
	}
	return nil
}

func (m *middleware[M]) newDreamAgent(ctx context.Context) (*adk.TypedChatModelAgent[M], error) {
	tools := make([]tool.BaseTool, 0, 1)
	if m.sessionSearchTool != nil {
		tools = append(tools, m.sessionSearchTool)
	}
	agent, err := adk.NewTypedChatModelAgent[M](ctx, &adk.TypedChatModelAgentConfig[M]{
		Name:          "automemory_dream",
		Description:   "Internal auto dream consolidation agent",
		Model:         m.cfg.Model,
		Handlers:      []adk.TypedChatModelAgentMiddleware[M]{m.fsHandler},
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools}},
		MaxIterations: 12,
	})
	if err != nil {
		return nil, fmt.Errorf("auto dream create agent: %w", err)
	}
	return agent, nil
}

func (m *middleware[M]) onErr(ctx context.Context, stage string, err error) {
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
