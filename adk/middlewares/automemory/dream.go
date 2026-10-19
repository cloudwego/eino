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
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/adk"
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
	fsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	internalschema "github.com/cloudwego/eino/internal/schema"
)

var (
	// ErrDreamNotConfigured means New was called without Config.Dream.
	ErrDreamNotConfigured = errors.New("auto memory dream is not configured")
	// ErrDreamInterrupted means the consolidation agent requested an interrupt.
	// RunDream does not provide checkpoints or resume support.
	ErrDreamInterrupted = errors.New("auto memory dream was interrupted")
)

// DreamOptions configures one consolidation task, independently of scheduling.
type DreamOptions[M adk.MessageType] struct {
	// Model is required for standalone RunDream. In Config.Dream, nil inherits
	// Config.Model (not Read.Model or Write.Model).
	Model model.BaseModel[M]
	// SessionStore is only used to search DreamRequest.SessionIDs.
	// Dream's own events are never written to this store.
	// Search reads message events, not replayed state; replaced or rolled-back
	// messages may still appear. Result limits do not cap scanned events.
	SessionStore adk.SessionEventStore[M]
	// MaxIterations caps model calls. Zero defaults to 12; negative is invalid.
	MaxIterations int
	// Handlers apply to the internal consolidation agent, after the built-in
	// memory filesystem middleware. Do not include AutoMemory itself.
	Handlers []adk.TypedChatModelAgentMiddleware[M]
	// OnEvent observes successful events synchronously in order. Returning an error cancels
	// the run. RunDream owns iterator consumption and checks execution errors
	// before invoking this callback. Streamed events from handlers are materialized.
	OnEvent func(context.Context, *adk.TypedAgentEvent[M]) error
}

// DreamConfig supplies the memory environment for a standalone consolidation.
type DreamConfig[M adk.MessageType] struct {
	// MemoryDirectory and MemoryBackend are required. Relative directories are
	// resolved against the process working directory on each standalone call.
	MemoryDirectory string
	MemoryBackend   Backend
	// IndexFileName is a file name inside MemoryDirectory. Default: MEMORY.md.
	IndexFileName string
	Options       *DreamOptions[M]
}

// DreamRequest selects the optional session-history search scope.
type DreamRequest struct {
	// SessionIDs are trimmed and deduplicated in order. Blank IDs are invalid.
	// A nonempty list requires DreamOptions.SessionStore. An empty request
	// consolidates memory files only, even when a SessionStore is configured.
	SessionIDs []string
}

type dreamRunner[M adk.MessageType] struct {
	options         DreamOptions[M]
	memoryDirectory string
	indexFileName   string
	handlers        []adk.TypedChatModelAgentMiddleware[M]
}

// RunDream consolidates memory once and blocks until execution has finished.
// It obeys ctx, returns the first execution/callback error, and never schedules
// background work. File changes already made are not rolled back on failure.
// Callers must coordinate Dream with other writers to the same memory storage.
func RunDream[M adk.MessageType](ctx context.Context, config *DreamConfig[M], request *DreamRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if config == nil || strings.TrimSpace(config.MemoryDirectory) == "" || config.MemoryBackend == nil {
		return fmt.Errorf("auto memory dream config: memory directory and backend are required")
	}
	directory, err := ainternal.ResolveMemoryDir(config.MemoryDirectory)
	if err != nil {
		return fmt.Errorf("auto memory dream config: resolve memory directory: %w", err)
	}
	runner, err := newDreamRunner(ctx, directory, config.MemoryBackend, config.IndexFileName, config.Options, nil)
	if err != nil {
		return err
	}
	return runner.run(ctx, request)
}

// RunDream consolidates the memory environment bound by New. Config.Dream must
// be non-nil. AutoMemory's lifecycle methods never trigger this method.
func (m *Middleware[M]) RunDream(ctx context.Context, request *DreamRequest) error {
	if m == nil || m.dream == nil {
		return ErrDreamNotConfigured
	}
	return m.dream.run(ctx, request)
}

func cloneDreamOptions[M adk.MessageType](options *DreamOptions[M]) *DreamOptions[M] {
	if options == nil {
		return nil
	}
	cp := *options
	cp.Handlers = append([]adk.TypedChatModelAgentMiddleware[M](nil), options.Handlers...)
	return &cp
}

func newDreamRunner[M adk.MessageType](ctx context.Context, directory string, backend Backend, index string,
	options *DreamOptions[M], fallback model.BaseModel[M]) (*dreamRunner[M], error) {
	options = cloneDreamOptions(options)
	if options == nil {
		return nil, fmt.Errorf("auto memory dream config: options are required")
	}
	if options.Model == nil {
		options.Model = fallback
	}
	if options.Model == nil {
		return nil, fmt.Errorf("auto memory dream config: model is required")
	}
	if options.MaxIterations < 0 {
		return nil, fmt.Errorf("auto memory dream config: max iterations must be nonnegative")
	}
	if options.MaxIterations == 0 {
		options.MaxIterations = 12
	}
	if index == "" {
		index = memoryIndexFileName
	}
	if strings.TrimSpace(index) == "" || index == "." || index == ".." ||
		filepath.Base(index) != index || strings.ContainsAny(index, `/\`) {
		return nil, fmt.Errorf("auto memory dream config: index must be a file name inside the memory directory")
	}
	fsBackend, err := ainternal.NewFSBackend(backend, ainternal.FSBackendConfig{
		BaseDir: directory, AllowLs: true, NotFoundAsContent: true, ErrorPrefix: "dream fs backend",
	})
	if err != nil {
		return nil, err
	}
	fsHandler, err := fsmw.NewTyped[M](ctx, &fsmw.MiddlewareConfig{
		Backend: fsBackend, GrepToolConfig: &fsmw.ToolConfig{Disable: true},
	})
	if err != nil {
		return nil, err
	}
	return &dreamRunner[M]{
		options: *options, memoryDirectory: directory, indexFileName: index,
		handlers: append([]adk.TypedChatModelAgentMiddleware[M]{fsHandler}, options.Handlers...),
	}, nil
}

func normalizeDreamSessions(request *DreamRequest) ([]string, error) {
	if request == nil {
		return nil, nil
	}
	var ids []string
	seen := make(map[string]struct{}, len(request.SessionIDs))
	for _, id := range request.SessionIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			return nil, fmt.Errorf("auto memory dream request: blank session ID")
		}
		if _, ok := seen[id]; !ok {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *dreamRunner[M]) run(ctx context.Context, request *DreamRequest) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	ids, err := normalizeDreamSessions(request)
	if err != nil {
		return err
	}
	var tools []tool.BaseTool
	if len(ids) > 0 {
		if r.options.SessionStore == nil {
			return fmt.Errorf("auto memory dream request: session IDs require a session store")
		}
		var search tool.BaseTool
		if search, err = newSessionHistoryGrepTool(r.options.SessionStore); err != nil {
			return err
		}
		tools = append(tools, search)
	}
	// A fresh agent and Runner isolate each run's state. The historical store
	// is a search dependency, not the Runner's persistence or replay target.
	agent, err := adk.NewTypedChatModelAgent(ctx, &adk.TypedChatModelAgentConfig[M]{
		Name: "automemory_dream", Description: "Internal memory consolidation agent",
		Model: r.options.Model, Handlers: r.handlers,
		ToolsConfig:   adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: tools}},
		MaxIterations: r.options.MaxIterations,
	})
	if err != nil {
		return fmt.Errorf("auto memory dream create agent: %w", err)
	}
	runCtx, cancel := context.WithCancel(withDreamRunMeta(ctx, &dreamRunMeta{SearchSessionIDs: ids}))
	defer cancel()
	runner := adk.NewTypedRunner(adk.TypedRunnerConfig[M]{Agent: agent})
	prompt := buildConsolidationPrompt(r.memoryDirectory, r.indexFileName, ids, len(tools) > 0)
	return consumeDreamEvents(runCtx, cancel, runner.Run(runCtx, []M{makeUserMsg[M](prompt)}), r.options.OnEvent)
}

func consumeDreamEvents[M adk.MessageType](ctx context.Context, cancel context.CancelFunc,
	iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]], onEvent func(context.Context, *adk.TypedAgentEvent[M]) error) error {
	var firstErr error
	fail := func(err error) {
		if firstErr == nil && err != nil {
			firstErr = err
			cancel()
		}
	}
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		fail(ctx.Err())
		if event == nil {
			continue
		}
		fail(event.Err)
		if event.Action != nil && event.Action.Interrupted != nil {
			fail(ErrDreamInterrupted)
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			output := event.Output.MessageOutput
			if output.IsStreaming && output.MessageStream != nil {
				if firstErr != nil {
					output.MessageStream.Close()
				} else {
					msg, err := internalschema.ConcatMessageStream(output.MessageStream)
					fail(err)
					output.Message = msg
					output.MessageStream = nil
					output.IsStreaming = false
				}
			}
		}
		// Execution errors have already been captured; observers cannot erase them.
		if firstErr == nil && onEvent != nil {
			fail(onEvent(ctx, event))
		}
		// Always drain after cancellation: AsyncIterator has no Close method.
	}
	if firstErr != nil {
		return firstErr
	}
	return ctx.Err()
}
