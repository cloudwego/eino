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

// Package automemory provides middleware that injects and persists session
// memories around chat-model agent runs.
package automemory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/slongfield/pyfmt"
	"gopkg.in/yaml.v3"

	"github.com/cloudwego/eino/adk"
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
	adkfs "github.com/cloudwego/eino/adk/middlewares/filesystem"
	fsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*memoryExtra]("_eino_adk_automemory_extra")
}

type Config[M adk.MessageType] struct {
	// MemoryStores defines the persistent memory stores exposed to automemory.
	// Required. At least one store must be configured.
	MemoryStores []MemoryStore

	// MemoryBackend is the storage backend used by all MemoryStores.
	// Required. Store paths are resolved against this backend and bounded per store.
	MemoryBackend Backend

	// GenInstruction returns the auto memory policy block appended to the system prompt.
	// Use it to customize memory read/write strength and criteria. The framework always
	// appends the memory store manifest and memory indexes after this block.
	// Optional. Defaults to the built-in auto memory instruction.
	GenInstruction func(ctx context.Context) (string, error)

	// Model is the default model used by topic selection and memory extraction.
	// Per-read/per-write overrides can be configured in Read.Model / Write.Model.
	// Optional. Defaults to nil; topic selection and extraction must then provide their own models.
	Model model.BaseModel[M]

	// Read controls how memories are loaded and injected.
	// Optional. Defaults to Sync load with topic selection enabled (if Model is set).
	Read *ReadConfig[M]

	// Write controls post-run memory extraction and persistence.
	// Optional. Default: disabled.
	Write *WriteConfig[M]

	// Coordination controls session identity and distributed async extraction coordination.
	// Optional. Defaults to a local in-process coordinator.
	Coordination *CoordinationConfig[M]

	// OnError is called when automemory encounters an error. Errors are best-effort by default:
	// the middleware will skip memory injection and allow the agent to continue.
	// Optional.
	OnError func(ctx context.Context, stage ErrorStage, err error)
}

type MemoryStore struct {
	// Path is the root path of this memory store.
	// Required. Relative paths are resolved against the process working directory.
	Path string

	// Name is the display name and relative path prefix used to disambiguate this store.
	// Optional. Defaults to the base name of Path.
	Name string

	// Description describes the purpose of this memory store in the system prompt manifest.
	// Optional. Defaults to empty.
	Description string
}

type ReadMode string

const (
	ReadModeSync  ReadMode = "sync"
	ReadModeAsync ReadMode = "async"
)

type ReadConfig[M adk.MessageType] struct {
	Mode ReadMode

	// Model is used for topic selection. Defaults to Config.Model.
	Model model.BaseModel[M]

	// Index controls how MEMORY.md is loaded into system prompt.
	// Optional.
	Index *IndexConfig

	// TopicSelection controls the "LLM select topics" path.
	// Optional. If nil, default topic selection settings are applied.
	// Topic selection becomes active when Read.Model is available.
	TopicSelection *TopicSelectionConfig
}

type IndexConfig struct {
	EnableMemoryIndex bool
	FileName          string
	MaxLines          int
	MaxBytes          int
}

type TopicSelectionConfig struct {
	// CandidateGlob is matched against the RELATIVE path under each memory store.
	// Example: "**/*.md"
	CandidateGlob  string
	CandidateLimit int
	// CandidatePreviewLines are read from each candidate to parse YAML frontmatter.
	CandidatePreviewLines int

	TopK int

	// MaxLines caps single topic memory file read lines.
	MaxLines int
	// MaxBytes caps single topic memory file read bytes.
	MaxBytes int
	// MaxTotalBytes caps the total rendered topic memory reminder across all stores.
	MaxTotalBytes int
}

type WriteMode string

const (
	WriteModeDisabled WriteMode = "disabled"
	WriteModeAsync    WriteMode = "async"
	WriteModeSync     WriteMode = "sync"
)

type WriteConfig[M adk.MessageType] struct {
	Mode WriteMode

	// Model is used for memory extraction. Defaults to Config.Model.
	Model model.BaseModel[M]

	// MaxTurns caps the extractor's tool-call loop.
	MaxTurns int

	SkipIndex bool

	// HandleExtractionIterator, if set, is called with the extractionAgent's event
	// iterator returned by Run(). The handler is responsible for draining the
	// iterator (calling Next until it returns ok=false) and returning any error
	// it wants to surface to the middleware.
	//
	// If nil, automemory uses the default drain behavior: ignore all events and
	// return the first ev.Err encountered (if any).
	HandleExtractionIterator func(ctx context.Context, iter *adk.AsyncIterator[*adk.TypedAgentEvent[M]]) error
}

type middleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]

	cfg *Config[M]

	memoryStores []runtimeMemoryStore

	topicSelectionModel model.BaseModel[M]
	extractionHandler   adk.TypedChatModelAgentMiddleware[M]
	topicSelectionTool  *schema.ToolInfo
	coordination        *CoordinationConfig[M]
}

type selectionFuture struct {
	done chan struct{}
	mu   sync.Mutex

	// Store an immutable snapshot to avoid being mutated via shared pointers.
	content string
	err     error
	applied bool
}

type ctxKeySelectionFuture struct{}

const (
	memoryExtraKey    = "__eino_automemory__"
	instructionMarker = "<!-- automemory:instruction -->"
)

type memoryExtra struct {
	Type   string
	Cursor int
}

type runtimeMemoryStore struct {
	MemoryStore

	Path    string
	Backend *ainternal.FSBackend
}

func buildRuntimeMemoryStores[M adk.MessageType](cfg *Config[M]) ([]runtimeMemoryStore, error) {
	stores := append([]MemoryStore{}, cfg.MemoryStores...)
	if len(stores) == 0 {
		return nil, fmt.Errorf("auto memory config: no memory stores")
	}

	out := make([]runtimeMemoryStore, 0, len(stores))
	seenName := make(map[string]struct{}, len(stores))
	seenPath := make(map[string]struct{}, len(stores))
	for i, store := range stores {
		if strings.TrimSpace(store.Path) == "" {
			return nil, fmt.Errorf("auto memory config: memory store %d has empty path", i)
		}
		resolvedPath, err := ainternal.ResolveMemoryDir(store.Path)
		if err != nil {
			return nil, fmt.Errorf("auto memory config: resolve memory store %d: %w", i, err)
		}
		if _, ok := seenPath[resolvedPath]; ok {
			return nil, fmt.Errorf("auto memory config: duplicate memory store path: %s", resolvedPath)
		}
		seenPath[resolvedPath] = struct{}{}

		name := strings.TrimSpace(store.Name)
		if name == "" {
			name = filepath.Base(resolvedPath)
			if name == "." || name == string(filepath.Separator) || name == "" {
				name = fmt.Sprintf("memory_%d", i+1)
			}
			store.Name = name
		}
		if strings.ContainsAny(name, `/\`) {
			return nil, fmt.Errorf("auto memory config: memory store name must not contain path separators: %s", name)
		}
		if _, ok := seenName[name]; ok {
			return nil, fmt.Errorf("auto memory config: duplicate memory store name: %s", name)
		}
		seenName[name] = struct{}{}

		bounded, err := ainternal.NewFSBackend(cfg.MemoryBackend, ainternal.FSBackendConfig{
			BaseDir:           resolvedPath,
			NotFoundAsContent: true,
			ErrorPrefix:       "memory backend",
		})
		if err != nil {
			return nil, err
		}
		store.Path = resolvedPath
		out = append(out, runtimeMemoryStore{
			MemoryStore: store,
			Path:        resolvedPath,
			Backend:     bounded,
		})
	}
	return out, nil
}

func (s runtimeMemoryStore) displayName() string {
	if strings.TrimSpace(s.Name) != "" {
		return strings.TrimSpace(s.Name)
	}
	return s.Path
}

func (m *middleware[M]) coordinatorKey(sessionID string) string {
	if sessionID == "" || m == nil || len(m.memoryStores) == 0 {
		return ""
	}
	paths := m.memoryStorePaths()
	sort.Strings(paths)
	return strings.Join(paths, "\n") + "::" + sessionID
}

func (m *middleware[M]) memoryStorePaths() []string {
	if m == nil || len(m.memoryStores) == 0 {
		return nil
	}
	paths := make([]string, 0, len(m.memoryStores))
	for _, store := range m.memoryStores {
		paths = append(paths, store.Path)
	}
	return paths
}

// New creates an automemory middleware from the provided configuration.
func New[M adk.MessageType](ctx context.Context, config *Config[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if config == nil {
		return nil, fmt.Errorf("auto memory config: invalid")
	}

	cfg := cloneConfig(config)
	if cfg.MemoryBackend == nil {
		return nil, fmt.Errorf("auto memory config: invalid")
	}

	stores, err := buildRuntimeMemoryStores(cfg)
	if err != nil {
		return nil, err
	}
	if cfg.Read == nil {
		cfg.Read = &ReadConfig[M]{}
	}
	applyReadDefaults(cfg)

	m := &middleware[M]{
		TypedBaseChatModelAgentMiddleware: adk.TypedBaseChatModelAgentMiddleware[M]{},
		cfg:                               cfg,
		memoryStores:                      stores,
		coordination:                      cfg.Coordination,
	}

	m.topicSelectionTool = topicSelectionToolInfo()
	if cfg.Read.TopicSelection != nil && cfg.Read.Model != nil {
		m.topicSelectionModel = &modelWithTools[M]{
			base:  cfg.Read.Model,
			tools: []*schema.ToolInfo{m.topicSelectionTool},
		}
	}

	if cfg.Write.Mode != WriteModeDisabled && cfg.Write.Model != nil {
		fileSystemMiddleware, err := fsmw.NewTyped[M](ctx, &fsmw.MiddlewareConfig{
			Backend:        newMultiStoreBackend(stores),
			LsToolConfig:   &fsmw.ToolConfig{Disable: true},
			GrepToolConfig: &fsmw.ToolConfig{Disable: true},
		})
		if err != nil {
			return nil, err
		}
		m.extractionHandler = fileSystemMiddleware
	}

	return m, nil
}

func (m *middleware[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}
	nRunCtx := *runCtx

	// Sync distributed write cursor back into message extras so later runs on other
	// machines still carry a transcript-local marker.
	if nRunCtx.AgentInput != nil && len(nRunCtx.AgentInput.Messages) > 0 && m.coordination != nil && m.coordination.Coordinator != nil {
		if sessionID, err := m.resolveSessionID(ctx, &adk.TypedChatModelAgentState[M]{Messages: nRunCtx.AgentInput.Messages}); err == nil && sessionID != "" {
			localCursor := getWriteCursorFromMessages(nRunCtx.AgentInput.Messages)
			coordKey := m.coordinatorKey(sessionID)
			if remoteCursor, ok, err := getCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey); err == nil && ok && remoteCursor > localCursor {
				st := markWriteCursor(&adk.TypedChatModelAgentState[M]{Messages: nRunCtx.AgentInput.Messages}, remoteCursor)
				if st != nil {
					nRunCtx.AgentInput = &adk.TypedAgentInput[M]{
						Messages:        st.Messages,
						EnableStreaming: nRunCtx.AgentInput.EnableStreaming,
					}
				}
			}
		}
	}

	// System-prompt injection and transcript-memory injection are idempotent,
	// but they are independent concerns: instruction should be rebuilt each run
	// unless this exact instruction already carries the marker, while transcript
	// memory messages should only be skipped when a real automemory reminder is
	// already present in the message list.
	// 1) System prompt: inject auto memory instruction + MEMORY.md content (best-effort).
	if !hasInstructionInjected(nRunCtx.Instruction) {
		instruction, err := m.renderInstruction(ctx, nRunCtx.Instruction)
		if err != nil {
			m.onErr(ctx, OnErrorStageRenderInstruction, err)
		} else {
			nRunCtx.Instruction = instruction
		}
	}

	// Skip topic memories injection if they already exist.
	if nRunCtx.AgentInput == nil || alreadyInjected(nRunCtx.AgentInput.Messages) {
		return ctx, &nRunCtx, nil
	}

	// 2) Topic memories: sync mode injects before the user's query.
	if m.cfg.Read.Mode == ReadModeSync && m.cfg.Read.TopicSelection != nil && m.topicSelectionModel != nil {
		memMsg, err := m.selectAndBuildTopicMemoryMessage(ctx, nRunCtx.AgentInput)
		if err != nil {
			m.onErr(ctx, OnErrorStageTopicSelectionSync, err)
		} else if memMsg != nil && nRunCtx.AgentInput != nil && len(nRunCtx.AgentInput.Messages) > 0 {
			m.sendTopicMemoryEvent(ctx, nRunCtx.AgentInput.Messages, memMsg)
			msgs := append([]M{}, nRunCtx.AgentInput.Messages...)
			msgs = append(msgs, memMsg)
			nRunCtx.AgentInput = &adk.TypedAgentInput[M]{Messages: msgs, EnableStreaming: nRunCtx.AgentInput.EnableStreaming}
		}
	}

	// 3) Topic memories: async mode starts selection here (cannot use RunLocalValue in BeforeAgent).
	if m.cfg.Read.Mode == ReadModeAsync && m.cfg.Read.TopicSelection != nil && m.topicSelectionModel != nil {
		if existing, _ := ctx.Value(ctxKeySelectionFuture{}).(*selectionFuture); existing == nil {
			fut := &selectionFuture{done: make(chan struct{})}
			ctx = context.WithValue(ctx, ctxKeySelectionFuture{}, fut)

			// Snapshot current messages for selection; async path is best-effort.
			msgSnapshot := append([]M{}, nRunCtx.AgentInput.Messages...)
			go func() {
				defer close(fut.done)
				memMsg, selErr := m.selectAndBuildTopicMemoryMessage(ctx, &adk.TypedAgentInput[M]{Messages: msgSnapshot})
				fut.mu.Lock()
				defer fut.mu.Unlock()
				if selErr != nil {
					fut.err = selErr
					return
				}
				if !isNilMessage(memMsg) {
					fut.content = userMessageTextContent(memMsg)
				}
			}()
		}
	}

	return ctx, &nRunCtx, nil
}

func (m *middleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], _ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if state == nil {
		return ctx, state, nil
	}
	// Best-effort protection: if automemory content has been injected before and later
	// mutated by other components, restore it using the immutable snapshot stored in the future.
	if fut, _ := ctx.Value(ctxKeySelectionFuture{}).(*selectionFuture); fut != nil {
		fut.mu.Lock()
		expected := fut.content
		fut.mu.Unlock()
		if strings.TrimSpace(expected) != "" {
			state = ensureMemoryMsgUnchanged(state, expected)
		}
	}
	if m.cfg.Read.Mode != ReadModeAsync {
		return ctx, state, nil
	}
	fut, _ := ctx.Value(ctxKeySelectionFuture{}).(*selectionFuture)
	if fut == nil {
		return ctx, state, nil
	}

	select {
	case <-fut.done:
	default:
		return ctx, state, nil
	}

	fut.mu.Lock()
	if fut.applied {
		fut.mu.Unlock()
		return ctx, state, nil
	}
	content := fut.content
	err := fut.err
	fut.mu.Unlock()
	if err != nil {
		m.onErr(ctx, OnErrorStageTopicSelectionAsync, err)
	}

	var msgs []M
	if strings.TrimSpace(content) != "" {
		memMsg := newMemoryMessage[M](content)
		m.sendTopicMemoryEvent(ctx, state.Messages, memMsg)
		msgs = append(msgs, state.Messages...)
		msgs = append(msgs, memMsg)
	} else {
		msgs = state.Messages
	}

	fut.mu.Lock()
	fut.applied = true
	fut.mu.Unlock()

	return ctx, &adk.TypedChatModelAgentState[M]{Messages: msgs}, nil
}

func applyReadDefaults[M adk.MessageType](cfg *Config[M]) {
	if cfg.Read.Mode == "" {
		cfg.Read.Mode = ReadModeSync
	}
	if cfg.Read.Index == nil {
		cfg.Read.Index = &IndexConfig{EnableMemoryIndex: true}
	}
	if cfg.Read.Index.FileName == "" {
		cfg.Read.Index.FileName = memoryIndexFileName
	}
	if cfg.Read.Index.MaxLines <= 0 {
		cfg.Read.Index.MaxLines = defaultIndexMaxLines
	}
	if cfg.Read.Index.MaxBytes <= 0 {
		cfg.Read.Index.MaxBytes = defaultIndexMaxBytes
	}
	if cfg.Read.Model == nil {
		cfg.Read.Model = cfg.Model
	}
	if cfg.Read.TopicSelection == nil {
		cfg.Read.TopicSelection = &TopicSelectionConfig{}
	}
	if cfg.Read.TopicSelection.TopK <= 0 {
		cfg.Read.TopicSelection.TopK = defaultTopicTopK
	}
	if cfg.Read.TopicSelection.CandidateGlob == "" {
		cfg.Read.TopicSelection.CandidateGlob = CandidateGlobPattern
	}
	if cfg.Read.TopicSelection.CandidateLimit <= 0 {
		cfg.Read.TopicSelection.CandidateLimit = defaultCandidateLimit
	}
	if cfg.Read.TopicSelection.CandidatePreviewLines <= 0 {
		cfg.Read.TopicSelection.CandidatePreviewLines = defaultCandidatePreviewLine
	}
	if cfg.Read.TopicSelection.MaxLines <= 0 {
		cfg.Read.TopicSelection.MaxLines = defaultTopicMaxLines
	}
	if cfg.Read.TopicSelection.MaxBytes <= 0 {
		cfg.Read.TopicSelection.MaxBytes = defaultTopicMaxBytes
	}
	if cfg.Read.TopicSelection.MaxTotalBytes <= 0 {
		cfg.Read.TopicSelection.MaxTotalBytes = defaultTopicMaxTotalBytes
	}

	if cfg.Write == nil {
		cfg.Write = &WriteConfig[M]{Mode: WriteModeDisabled}
	}
	if cfg.Write.Mode == "" {
		cfg.Write.Mode = WriteModeDisabled
	}
	if cfg.Write.Model == nil {
		cfg.Write.Model = cfg.Model
	}
	if cfg.Write.MaxTurns <= 0 {
		cfg.Write.MaxTurns = defaultMemoryWriteMaxTurns
	}

	if cfg.Coordination == nil {
		cfg.Coordination = &CoordinationConfig[M]{}
	}
	if cfg.Coordination.Coordinator == nil {
		cfg.Coordination.Coordinator = NewLocalCoordinator()
	}
	if cfg.Coordination.LockTTL <= 0 {
		cfg.Coordination.LockTTL = 2 * time.Minute
	}
}

func cloneConfig[M adk.MessageType](cfg *Config[M]) *Config[M] {
	if cfg == nil {
		return nil
	}

	cp := *cfg
	if cfg.Read != nil {
		readCopy := *cfg.Read
		cp.Read = &readCopy
		if cfg.Read.Index != nil {
			indexCopy := *cfg.Read.Index
			cp.Read.Index = &indexCopy
		}
		if cfg.Read.TopicSelection != nil {
			topicSelectionCopy := *cfg.Read.TopicSelection
			cp.Read.TopicSelection = &topicSelectionCopy
		}
	}
	if cfg.Write != nil {
		writeCopy := *cfg.Write
		cp.Write = &writeCopy
	}
	if cfg.Coordination != nil {
		coordinationCopy := *cfg.Coordination
		cp.Coordination = &coordinationCopy
	}
	return &cp
}

type topicSelectionResp struct {
	SelectedMemories []string `json:"selected_memories"`
}

func (m *middleware[M]) renderInstruction(ctx context.Context, baseInstruction string) (string, error) {
	memDesc := getDefaultMemoryInstruction()
	if m.cfg.GenInstruction != nil {
		custom, err := m.cfg.GenInstruction(ctx)
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(custom) != "" {
			memDesc = custom
		}
	}

	stores := make([]memoryStorePromptInfo, 0, len(m.memoryStores))
	for _, store := range m.memoryStores {
		info := memoryStorePromptInfo{
			Name:        store.displayName(),
			Mount:       store.Path,
			Description: strings.TrimSpace(store.Description),
		}
		if m.cfg.Read.Index.EnableMemoryIndex {
			indexPath := filepath.Join(store.Path, m.cfg.Read.Index.FileName)
			indexContent := ""
			totalLines := 0

			fc, err := store.Backend.Read(ctx, &ReadRequest{FilePath: indexPath})
			if err == nil && fc != nil && !isFileNotFoundContent(fc.Content) {
				indexContent = fc.Content
				totalLines = strings.Count(indexContent, "\n") + 1
			}
			truncatedMemoryIndex, _, truncated := linesOrSizeTrunc(indexContent, m.cfg.Read.Index.MaxLines, m.cfg.Read.Index.MaxBytes)
			info.Index = &memoryIndexPromptInfo{
				FileName:  m.cfg.Read.Index.FileName,
				Path:      indexPath,
				Content:   truncatedMemoryIndex,
				Empty:     strings.TrimSpace(indexContent) == "",
				Truncated: truncated,
				Lines:     totalLines,
			}
		}
		stores = append(stores, info)
	}

	return buildSystemMemoryInstruction(baseInstruction, instructionMarker, memDesc, stores)
}

func linesOrSizeTrunc(content string, lines, size int) (newContent string, reason string, truncated bool) {
	linesTrunc := func(content string, lines int) {
		sp := strings.Split(content, "\n")
		if len(sp) > lines {
			newContent = strings.Join(sp[:lines], "\n")
			reason = fmt.Sprintf("first %d lines", lines)
			truncated = true
		} else {
			newContent = content
		}
	}

	sizeTrunc := func(content string, size int) {
		if len(content) > size {
			newContent = content[:size]
			reason = fmt.Sprintf("%d byte limit", size)
			truncated = true
		} else {
			newContent = content
		}
	}

	if lines == 0 && size == 0 {
		return content, "", false
	} else if lines == 0 {
		sizeTrunc(content, size)
	} else if size == 0 {
		linesTrunc(content, lines)
	} else {
		linesTrunc(content, lines)
		sizeTrunc(newContent, size)
	}
	return
}

func isFileNotFoundContent(content string) bool {
	return strings.HasPrefix(strings.TrimSpace(content), "File not found: ")
}

func (m *middleware[M]) onErr(ctx context.Context, stage ErrorStage, err error) {
	if err == nil {
		return
	}
	if m.cfg != nil && m.cfg.OnError != nil {
		m.cfg.OnError(ctx, stage, err)
	}
}

type topicFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
}

type topicCandidateBundle struct {
	StoreName string
	StorePath string
	Backend   Backend
	Key       string
	AbsPath   string
	RelPath   string
	Info      FileInfo
}

func parseFrontmatter(md string) (fm topicFrontmatter, ok bool) {
	// Only consider YAML frontmatter at the beginning.
	s := strings.TrimLeft(md, "\ufeff \t\r\n")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return topicFrontmatter{}, false
	}
	// Find the next delimiter.
	parts := strings.SplitN(s, "\n---", 2)
	if len(parts) != 2 {
		return topicFrontmatter{}, false
	}
	yml := strings.TrimPrefix(parts[0], "---\n")
	if err := yaml.Unmarshal([]byte(yml), &fm); err != nil {
		return topicFrontmatter{}, false
	}
	return fm, true
}

func (m *middleware[M]) selectAndBuildTopicMemoryMessage(ctx context.Context, agentIn *adk.TypedAgentInput[M]) (M, error) {
	last, ok := m.lastUserMessage(agentIn)
	if !ok {
		return nil, nil
	}

	relToBundle, available, orderedRel, err := m.listTopicCandidates(ctx)
	if err != nil || len(orderedRel) == 0 {
		return nil, err
	}

	topK := m.topicSelectionTopK()
	selected, err := m.selectTopicCandidates(ctx, agentIn, userMessageTextContent(last), available, orderedRel, relToBundle)
	if err != nil || len(selected) == 0 {
		return nil, err
	}

	rendered := m.renderTopicMemories(ctx, selected, relToBundle, topK)
	if len(rendered) == 0 {
		return nil, nil
	}

	return newMemoryMessage[M]("<!-- automemory -->\n" + strings.Join(rendered, "\n\n")), nil
}

func (m *middleware[M]) lastUserMessage(agentIn *adk.TypedAgentInput[M]) (M, bool) {
	if agentIn == nil || len(agentIn.Messages) == 0 {
		return nil, false
	}
	if m.cfg.Read.TopicSelection == nil || m.topicSelectionModel == nil {
		return nil, false
	}
	last := agentIn.Messages[len(agentIn.Messages)-1]
	if isNilMessage(last) || !isUserRole(last) {
		return nil, false
	}
	return last, true
}

func (m *middleware[M]) listTopicCandidates(ctx context.Context) (map[string]topicCandidateBundle, []string, []string, error) {
	candidates, err := m.topicSelectionCandidates(ctx)
	if err != nil || len(candidates) == 0 {
		return nil, nil, nil, err
	}

	relToBundle := make(map[string]topicCandidateBundle, len(candidates))
	available := make([]string, 0, len(candidates))
	orderedRel := make([]string, 0, len(candidates))

	for _, fi := range candidates {
		bundle, manifestLine, ok := m.buildTopicCandidateBundle(ctx, fi)
		if !ok {
			continue
		}
		relToBundle[bundle.Key] = bundle
		available = append(available, manifestLine)
		orderedRel = append(orderedRel, bundle.Key)
	}

	return relToBundle, available, orderedRel, nil
}

func (m *middleware[M]) topicSelectionCandidates(ctx context.Context) ([]topicCandidateBundle, error) {
	var candidates []topicCandidateBundle
	for _, store := range m.memoryStores {
		files, err := store.Backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: m.cfg.Read.TopicSelection.CandidateGlob,
			Path:    store.Path,
		})
		if err != nil {
			return nil, err
		}
		indexAbs := filepath.Join(store.Path, m.cfg.Read.Index.FileName)
		for _, fi := range files {
			if filepath.Clean(fi.Path) == filepath.Clean(indexAbs) {
				continue
			}
			rel, relErr := filepath.Rel(store.Path, fi.Path)
			if relErr != nil {
				rel = filepath.Base(fi.Path)
			}
			rel = filepath.ToSlash(rel)
			key := filepath.ToSlash(filepath.Join(store.displayName(), rel))
			candidates = append(candidates, topicCandidateBundle{
				StoreName: store.displayName(),
				StorePath: store.Path,
				Backend:   store.Backend,
				Key:       key,
				AbsPath:   fi.Path,
				RelPath:   rel,
				Info:      fi,
			})
		}
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return parseRFC3339NanoBestEffort(candidates[i].Info.ModifiedAt).After(parseRFC3339NanoBestEffort(candidates[j].Info.ModifiedAt))
	})
	if len(candidates) > m.cfg.Read.TopicSelection.CandidateLimit {
		candidates = candidates[:m.cfg.Read.TopicSelection.CandidateLimit]
	}
	return candidates, nil
}

func (m *middleware[M]) buildTopicCandidateBundle(ctx context.Context, bundle topicCandidateBundle) (topicCandidateBundle, string, bool) {
	preview, err := bundle.Backend.Read(ctx, &ReadRequest{
		FilePath: bundle.AbsPath,
		Limit:    m.cfg.Read.TopicSelection.CandidatePreviewLines,
	})
	if err != nil || preview == nil || isFileNotFoundContent(preview.Content) {
		return topicCandidateBundle{}, "", false
	}

	desc := describeTopicCandidate(preview.Content)
	manifestLine := fmt.Sprintf("- %s (store: %s, saved %s): %s", bundle.Key, bundle.StoreName, bundle.Info.ModifiedAt, desc)
	return bundle, manifestLine, true
}

func describeTopicCandidate(content string) string {
	desc := ""
	if fm, ok := parseFrontmatter(content); ok {
		switch {
		case strings.TrimSpace(fm.Description) != "":
			desc = strings.TrimSpace(fm.Description)
		case strings.TrimSpace(fm.Name) != "":
			desc = strings.TrimSpace(fm.Name)
		}
		if strings.TrimSpace(fm.Type) != "" {
			if desc == "" {
				desc = "type=" + strings.TrimSpace(fm.Type)
			} else {
				desc = desc + " (type=" + strings.TrimSpace(fm.Type) + ")"
			}
		}
	}
	if desc == "" {
		snippet, _, _ := linesOrSizeTrunc(content, 3, 256)
		desc = strings.TrimSpace(snippet)
	}
	return desc
}

func (m *middleware[M]) topicSelectionTopK() int {
	topK := m.cfg.Read.TopicSelection.TopK
	if topK <= 0 {
		return defaultTopicTopK
	}
	return topK
}

func (m *middleware[M]) selectTopicCandidates(
	ctx context.Context,
	agentIn *adk.TypedAgentInput[M],
	userQuery string,
	available []string,
	orderedRel []string,
	relToBundle map[string]topicCandidateBundle,
) ([]string, error) {
	topK := m.topicSelectionTopK()

	userMsg, err := pyfmt.Fmt(getTopicSelectionUserPrompt(), map[string]any{
		"user_query":         userQuery,
		"top_k":              topK,
		"available_memories": strings.Join(available, "\n"),
		"tools":              strings.Join(collectToolNames(agentIn.Messages), ", "),
	})
	if err != nil {
		return nil, err
	}

	toolInfo := topicSelectionToolInfo()
	resp, err := m.topicSelectionModel.Generate(
		ctx,
		[]M{
			makeSystemMsg[M](getTopicSelectionSystemPrompt()),
			makeUserMsg[M](userMsg),
		},
		makeToolChoiceForced[M](toolInfo.Name),
	)
	if err != nil {
		return nil, err
	}

	valid := make(map[string]struct{}, len(relToBundle))
	for k := range relToBundle {
		valid[k] = struct{}{}
	}
	selected, err := parseTopicSelectionFromToolCall(resp, valid)
	if err != nil {
		return nil, err
	}
	if len(selected) > topK {
		return selected[:topK], nil
	}
	return selected, nil
}

func collectToolNames[M adk.MessageType](msgs []M) []string {
	dedupTools := make(map[string]struct{})
	for _, msg := range msgs {
		for _, name := range messageToolNames(msg) {
			dedupTools[name] = struct{}{}
		}
	}
	tools := make([]string, 0, len(dedupTools))
	for t := range dedupTools {
		tools = append(tools, t)
	}
	sort.Strings(tools)
	return tools
}

func (m *middleware[M]) renderTopicMemories(
	ctx context.Context,
	selected []string,
	relToBundle map[string]topicCandidateBundle,
	topK int,
) []string {
	capHint := topK
	if capHint > len(selected) {
		capHint = len(selected)
	}
	rendered := make([]string, 0, capHint)
	totalBytes := 0
	maxTotalBytes := m.cfg.Read.TopicSelection.MaxTotalBytes
	for _, rel := range selected {
		if len(rendered) >= topK {
			break
		}
		bundle, ok := relToBundle[rel]
		if !ok {
			continue
		}
		renderedContent, ok := m.renderTopicMemory(ctx, bundle)
		if !ok {
			continue
		}
		if maxTotalBytes > 0 && totalBytes+len(renderedContent) > maxTotalBytes {
			if len(rendered) == 0 {
				renderedContent = renderedContent[:maxTotalBytes]
				rendered = append(rendered, renderedContent)
			}
			break
		}
		rendered = append(rendered, renderedContent)
		totalBytes += len(renderedContent)
	}
	return rendered
}

func (m *middleware[M]) renderTopicMemory(ctx context.Context, bundle topicCandidateBundle) (string, bool) {
	full, err := bundle.Backend.Read(ctx, &ReadRequest{FilePath: bundle.AbsPath})
	if err != nil || full == nil || isFileNotFoundContent(full.Content) {
		return "", false
	}

	content, truncReason, truncated := linesOrSizeTrunc(full.Content, m.cfg.Read.TopicSelection.MaxLines, m.cfg.Read.TopicSelection.MaxBytes)
	if truncated {
		truncNotify, err := pyfmt.Fmt(getTopicMemoryTruncNotify(), map[string]any{
			"reason":   truncReason,
			"abs_path": bundle.AbsPath,
		})
		if err == nil {
			content += truncNotify
		}
	}

	return fmt.Sprintf(
		"<system-reminder>\nMemory Store: %s\nPath: %s\nSaved: %s\n\n%s\n</system-reminder>",
		bundle.StoreName,
		bundle.RelPath,
		bundle.Info.ModifiedAt,
		content,
	), true
}

func topicSelectionToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: topicSelectionToolName,
		Desc: "Select which memory files to surface for the current query. Return selected_memories as memory paths exactly as shown in the available memories list.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"selected_memories": {
				Type:     schema.Array,
				Desc:     "Memory paths exactly as shown in the available memories list, e.g. \"user_profile/preferences.md\" or \"project_context/notes/patterns.md\".",
				Required: true,
				ElemInfo: &schema.ParameterInfo{Type: schema.String},
			},
		}),
	}
}

func parseTopicSelectionFromToolCall[M adk.MessageType](msg M, valid map[string]struct{}) ([]string, error) {
	toolCalls := messageToolCalls(msg)
	if len(toolCalls) == 0 {
		return nil, fmt.Errorf("no tool calls")
	}
	tc := toolCalls[0]
	if tc.Function.Name != topicSelectionToolName {
		return nil, fmt.Errorf("unexpected tool call: %s", tc.Function.Name)
	}
	var parsed topicSelectionResp
	if err := json.Unmarshal([]byte(tc.Function.Arguments), &parsed); err != nil {
		return nil, err
	}
	out := normalizeSelected(parsed.SelectedMemories)
	// Filter to known candidates to avoid hallucinated paths.
	filtered := make([]string, 0, len(out))
	for _, p := range out {
		if _, ok := valid[p]; ok {
			filtered = append(filtered, p)
		}
	}
	return filtered, nil
}

func normalizeSelected(in []string) []string {
	out := make([]string, 0, len(in))
	seen := make(map[string]struct{}, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		s = strings.TrimPrefix(s, "./")
		s = filepath.ToSlash(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func isNilMessage[M adk.MessageType](msg M) bool {
	var zero M
	return any(msg) == any(zero)
}

func isUserRole[M adk.MessageType](msg M) bool {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m != nil && m.Role == schema.User
	case *schema.AgenticMessage:
		return m != nil && m.Role == schema.AgenticRoleTypeUser
	default:
		panic("unreachable")
	}
}

func isAssistantRole[M adk.MessageType](msg M) bool {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m != nil && m.Role == schema.Assistant
	case *schema.AgenticMessage:
		return m != nil && m.Role == schema.AgenticRoleTypeAssistant
	default:
		panic("unreachable")
	}
}

func userMessageTextContent[M adk.MessageType](msg M) string {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil {
			return ""
		}
		if len(m.UserInputMultiContent) == 0 {
			return m.Content
		}
		parts := make([]string, 0, len(m.UserInputMultiContent))
		for _, part := range m.UserInputMultiContent {
			if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
				parts = append(parts, part.Text)
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "\n")
		}
		return m.Content
	case *schema.AgenticMessage:
		if m == nil {
			return ""
		}
		parts := make([]string, 0, len(m.ContentBlocks))
		for _, block := range m.ContentBlocks {
			if block != nil && block.UserInputText != nil {
				parts = append(parts, block.UserInputText.Text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		panic("unreachable")
	}
}

func getMsgExtra[M adk.MessageType](msg M) map[string]any {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil {
			return nil
		}
		return m.Extra
	case *schema.AgenticMessage:
		if m == nil {
			return nil
		}
		return m.Extra
	default:
		panic("unreachable")
	}
}

func copyAndSetMsgExtra[M adk.MessageType](msg M, key string, value any) {
	existing := getMsgExtra(msg)
	newExtra := make(map[string]any, len(existing)+1)
	for k, v := range existing {
		newExtra[k] = v
	}
	newExtra[key] = value

	switch m := any(msg).(type) {
	case *schema.Message:
		m.Extra = newExtra
	case *schema.AgenticMessage:
		m.Extra = newExtra
	default:
		panic("unreachable")
	}
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

func makeSystemMsg[M adk.MessageType](text string) M {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return any(schema.SystemMessage(text)).(M)
	case *schema.AgenticMessage:
		return any(schema.SystemAgenticMessage(text)).(M)
	default:
		panic("unreachable")
	}
}

func makeToolChoiceForced[M adk.MessageType](name string) model.Option {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		return model.WithToolChoice(schema.ToolChoiceForced, name)
	case *schema.AgenticMessage:
		return model.WithAgenticToolChoice(&schema.AgenticToolChoice{
			Type: schema.ToolChoiceForced,
			Forced: &schema.AgenticForcedToolChoice{
				Tools: []*schema.AllowedTool{{FunctionName: name}},
			},
		})
	default:
		panic("unreachable")
	}
}

func messageToolCalls[M adk.MessageType](msg M) []schema.ToolCall {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil {
			return nil
		}
		return m.ToolCalls
	case *schema.AgenticMessage:
		if m == nil {
			return nil
		}
		out := make([]schema.ToolCall, 0, len(m.ContentBlocks))
		for _, block := range m.ContentBlocks {
			if block == nil || block.FunctionToolCall == nil {
				continue
			}
			out = append(out, schema.ToolCall{
				ID:   block.FunctionToolCall.CallID,
				Type: "function",
				Function: schema.FunctionCall{
					Name:      block.FunctionToolCall.Name,
					Arguments: block.FunctionToolCall.Arguments,
				},
			})
		}
		return out
	default:
		panic("unreachable")
	}
}

func messageToolNames[M adk.MessageType](msg M) []string {
	switch m := any(msg).(type) {
	case *schema.Message:
		if m == nil || m.Role != schema.Tool || m.ToolName == "" {
			return nil
		}
		return []string{m.ToolName}
	case *schema.AgenticMessage:
		if m == nil {
			return nil
		}
		var out []string
		for _, block := range m.ContentBlocks {
			if block == nil || block.FunctionToolResult == nil || block.FunctionToolResult.Name == "" {
				continue
			}
			out = append(out, block.FunctionToolResult.Name)
		}
		return out
	default:
		panic("unreachable")
	}
}

func projectMessagesToSchema[M adk.MessageType](msgs []M) []adk.Message {
	out := make([]adk.Message, 0, len(msgs))
	for _, msg := range msgs {
		if projected := projectMessageToSchema(msg); projected != nil {
			out = append(out, projected)
		}
	}
	return out
}

func projectMessageToSchema[M adk.MessageType](msg M) adk.Message {
	switch m := any(msg).(type) {
	case *schema.Message:
		return m
	case *schema.AgenticMessage:
		if m == nil {
			return nil
		}
		text := m.String()
		switch m.Role {
		case schema.AgenticRoleTypeSystem:
			return schema.SystemMessage(text)
		case schema.AgenticRoleTypeAssistant:
			return schema.AssistantMessage(text, messageToolCalls(msg))
		case schema.AgenticRoleTypeUser:
			return schema.UserMessage(text)
		default:
			return schema.UserMessage(text)
		}
	default:
		panic("unreachable")
	}
}

func alreadyInjected[M adk.MessageType](msgs []M) bool {
	for _, m := range msgs {
		if isMemoryMessage(m) {
			return true
		}
	}
	return false
}

func isMemoryMessage[M adk.MessageType](m M) bool {
	if isNilMessage(m) || !isUserRole(m) {
		return false
	}
	if extra := getMsgExtra(m); extra != nil {
		if v, ok := extra[memoryExtraKey]; ok {
			if isAutomemoryMemoryExtra(v) {
				return true
			}
		}
	}
	// Backward compatible marker (older versions).
	return strings.Contains(userMessageTextContent(m), "<!-- automemory -->")
}

func isAutomemoryMemoryExtra(v any) bool {
	switch meta := v.(type) {
	case *memoryExtra:
		return meta != nil && meta.Type == "memory"
	case map[string]any:
		typ, _ := meta["type"].(string)
		return typ == "memory"
	default:
		return false
	}
}

func hasInstructionInjected(instruction string) bool {
	return strings.Contains(instruction, instructionMarker)
}

func newMemoryMessage[M adk.MessageType](content string) M {
	msg := makeUserMsg[M](content)
	copyAndSetMsgExtra(msg, memoryExtraKey, &memoryExtra{Type: "memory"})
	return msg
}

func ensureMemoryMsgUnchanged[M adk.MessageType](state *adk.TypedChatModelAgentState[M], expectedContent string) *adk.TypedChatModelAgentState[M] {
	if state == nil || strings.TrimSpace(expectedContent) == "" {
		return state
	}
	changed := false
	out := *state
	out.Messages = append([]M{}, state.Messages...)

	for i, m := range out.Messages {
		if !isMemoryMessage(m) {
			continue
		}
		extra := getMsgExtra(m)
		if userMessageTextContent(m) != expectedContent || extra == nil || extra[memoryExtraKey] == nil {
			out.Messages[i] = newMemoryMessage[M](expectedContent)
			changed = true
		}
	}
	if !changed {
		return state
	}
	return &out
}

func extractFilePath(args string) (string, bool) {
	var m map[string]any
	if err := json.Unmarshal([]byte(args), &m); err != nil {
		return "", false
	}
	if v, ok := m["file_path"]; ok {
		if s, ok := v.(string); ok && s != "" {
			return s, true
		}
	}
	if v, ok := m["filePath"]; ok { // tolerate camelCase
		if s, ok := v.(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}

func isPathWithinMemoryDir(memDir string, filePath string) bool {
	if memDir == "" || filePath == "" {
		return false
	}
	md := filepath.Clean(memDir)
	fp := filepath.Clean(filePath)
	if !filepath.IsAbs(fp) {
		fp = filepath.Join(md, fp)
		fp = filepath.Clean(fp)
	}
	if fp == md {
		return true
	}
	sep := string(filepath.Separator)
	return strings.HasPrefix(fp, md+sep)
}

func (m *middleware[M]) AfterAgent(ctx context.Context, state *adk.TypedChatModelAgentState[M]) (context.Context, error) {
	if m.cfg == nil || m.cfg.Write == nil || m.cfg.Write.Mode == WriteModeDisabled {
		return ctx, nil
	}
	if m.cfg.Write.Model == nil || m.extractionHandler == nil {
		return ctx, nil
	}
	if state == nil || len(state.Messages) == 0 {
		return ctx, nil
	}

	sessionID, err := m.resolveSessionID(ctx, state)
	if err != nil {
		m.onErr(ctx, OnErrorStageResolveSessionID, err)
		return ctx, nil
	}
	coordKey := m.coordinatorKey(sessionID)

	cursor := getWriteCursorFromMessages(state.Messages)
	if coordKey != "" {
		if remoteCursor, ok, err := getCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey); err == nil && ok && remoteCursor > cursor {
			cursor = remoteCursor
			state = markWriteCursor(state, cursor)
		}
	}
	if cursor >= len(state.Messages) {
		return ctx, nil
	}

	// Skip background extraction if the main agent already wrote memory files in this range.
	if hasMemoryWritesSince(state.Messages, cursor, m.memoryStores) {
		end := len(state.Messages)
		if coordKey != "" {
			_ = setCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey, end)
		}
		state = markWriteCursor(state, end)
		return ctx, nil
	}

	if countModelVisibleMessages(state.Messages[cursor:]) == 0 {
		end := len(state.Messages)
		if coordKey != "" {
			_ = setCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey, end)
		}
		state = markWriteCursor(state, end)
		return ctx, nil
	}

	switch m.cfg.Write.Mode {
	case WriteModeDisabled:
		// do nothing
		return ctx, nil

	case WriteModeSync:
		end := len(state.Messages)
		if err := m.runMemoryExtractionAgent(ctx, state.Messages, cursor, state.ToolInfos); err != nil {
			m.onErr(ctx, OnErrorStageMemoryWriteSync, err)
			return ctx, nil
		}
		if coordKey != "" {
			_ = setCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey, end)
		}
		state = markWriteCursor(state, end)
		return ctx, nil

	case WriteModeAsync:
		if sessionID == "" {
			sessionID = getOrInitWriteSessionID(ctx)
			coordKey = m.coordinatorKey(sessionID)
		}
		snap, err := buildPendingSnapshot(state.Messages, cursor, state.ToolInfos)
		if err != nil {
			m.onErr(ctx, OnErrorStageSnapshotMarshal, err)
			return ctx, nil
		}
		unlock, ok, err := m.coordination.Coordinator.AcquireLock(ctx, coordKey, m.coordination.LockTTL)
		if err != nil {
			m.onErr(ctx, OnErrorStageAcquireExtractionLock, err)
			return ctx, nil
		}
		if !ok {
			if err := setCoordinatorPendingSnapshot(ctx, m.coordination.Coordinator, coordKey, snap, m.coordination.LockTTL); err != nil {
				m.onErr(ctx, OnErrorStageStashPendingSnapshot, err)
			}
			return ctx, nil
		}
		go m.runExtractionDrain(ctx, coordKey, unlock, snap)
		return ctx, nil

	default:
		return ctx, nil
	}
}

func getWriteCursorFromMessages[M adk.MessageType](msgs []M) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		extra := getMsgExtra(m)
		if isNilMessage(m) || extra == nil {
			continue
		}
		v, ok := extra[memoryExtraKey]
		if !ok {
			continue
		}
		switch meta := v.(type) {
		case *memoryExtra:
			if meta != nil && meta.Type == "write_cursor" {
				return meta.Cursor
			}
		case map[string]any:
			if typ, _ := meta["type"].(string); typ != "write_cursor" {
				continue
			}
			switch c := meta["cursor"].(type) {
			case int:
				return c
			case int64:
				return int(c)
			case float64:
				return int(c)
			}
		}
	}
	return 0
}

func markWriteCursor[M adk.MessageType](state *adk.TypedChatModelAgentState[M], cursor int) *adk.TypedChatModelAgentState[M] {
	if state == nil || len(state.Messages) == 0 {
		return state
	}
	last := state.Messages[len(state.Messages)-1]
	if isNilMessage(last) {
		return state
	}

	copyAndSetMsgExtra(last, memoryExtraKey, &memoryExtra{
		Type:   "write_cursor",
		Cursor: cursor,
	})

	return state
}

func countModelVisibleMessages[M adk.MessageType](msgs []M) int {
	n := 0
	for _, m := range msgs {
		if isNilMessage(m) {
			continue
		}
		if isUserRole(m) || isAssistantRole(m) {
			n++
		}
	}
	return n
}

func getOrInitWriteSessionID(ctx context.Context) string {
	const key = "__automemory_write_session_id__"
	if v, ok := adk.GetSessionValue(ctx, key); ok {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	// Stable enough for in-process session identity.
	s := fmt.Sprintf("%d", time.Now().UnixNano())
	adk.AddSessionValue(ctx, key, s)
	return s
}

func (m *middleware[M]) resolveSessionID(ctx context.Context, state *adk.TypedChatModelAgentState[M]) (string, error) {
	if m.coordination != nil && m.coordination.SessionIDFunc != nil {
		return m.coordination.SessionIDFunc(ctx, state)
	}
	return getOrInitWriteSessionID(ctx), nil
}

func buildPendingSnapshot[M adk.MessageType](messages []M, cursor int, toolInfos []*schema.ToolInfo) (*PendingSnapshot, error) {
	raw, err := json.Marshal(messages)
	if err != nil {
		return nil, err
	}
	var rawToolInfos json.RawMessage
	if toolInfos != nil {
		rawToolInfos, err = json.Marshal(toolInfos)
		if err != nil {
			return nil, err
		}
	}
	return &PendingSnapshot{Cursor: cursor, Messages: raw, ToolInfos: rawToolInfos}, nil
}

func decodePendingSnapshot[M adk.MessageType](snapshot *PendingSnapshot) ([]M, int, []*schema.ToolInfo, error) {
	if snapshot == nil {
		return nil, 0, nil, nil
	}
	var msgs []M
	if err := json.Unmarshal(snapshot.Messages, &msgs); err != nil {
		return nil, 0, nil, err
	}
	var toolInfos []*schema.ToolInfo
	if len(snapshot.ToolInfos) > 0 {
		if err := json.Unmarshal(snapshot.ToolInfos, &toolInfos); err != nil {
			return nil, 0, nil, err
		}
	}
	return msgs, snapshot.Cursor, toolInfos, nil
}

func (m *middleware[M]) runExtractionDrain(ctx context.Context, coordKey string, unlock func(context.Context) error, initial *PendingSnapshot) {
	defer func() {
		if unlock == nil {
			return
		}
		if err := unlock(ctx); err != nil {
			m.onErr(ctx, OnErrorStageReleaseExtractionLock, err)
		}
	}()

	current := initial
	for current != nil {
		msgs, cursor, toolInfos, err := decodePendingSnapshot[M](current)
		if err != nil {
			m.onErr(ctx, OnErrorStageDecodePendingSnapshot, err)
		} else if err := m.runMemoryExtractionAgent(ctx, msgs, cursor, toolInfos); err != nil {
			m.onErr(ctx, OnErrorStageMemoryWriteAsync, err)
		} else {
			_ = setCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey, len(msgs))
		}

		next, loadErr := popCoordinatorPendingSnapshot(ctx, m.coordination.Coordinator, coordKey)
		if loadErr != nil {
			m.onErr(ctx, OnErrorStageLoadPendingSnapshot, loadErr)
			return
		}
		current = next
	}
}

func hasMemoryWritesSince[M adk.MessageType](msgs []M, cursor int, stores []runtimeMemoryStore) bool {
	if cursor < 0 {
		cursor = 0
	}
	for _, msg := range msgs[cursor:] {
		if isNilMessage(msg) || !isAssistantRole(msg) {
			continue
		}
		for _, tc := range messageToolCalls(msg) {
			if tc.Function.Name != adkfs.ToolNameWriteFile && tc.Function.Name != adkfs.ToolNameEditFile {
				continue
			}
			if fp, ok := extractFilePath(tc.Function.Arguments); ok && isPathWithinMemoryStores(stores, fp) {
				return true
			}
		}
	}
	return false
}

func isPathWithinMemoryStores(stores []runtimeMemoryStore, filePath string) bool {
	if filePath == "" {
		return false
	}
	if filepath.IsAbs(filePath) {
		for _, store := range stores {
			if isPathWithinMemoryDir(store.Path, filePath) {
				return true
			}
		}
		return false
	}

	clean := filepath.ToSlash(filepath.Clean(filePath))
	for _, store := range stores {
		name := filepath.ToSlash(store.displayName())
		if clean == name || strings.HasPrefix(clean, name+"/") {
			return true
		}
	}
	return len(stores) == 1 && isPathWithinMemoryDir(stores[0].Path, filePath)
}

func countModelVisibleMessagesSince[M adk.MessageType](msgs []M, cursor int) int {
	if cursor < 0 {
		cursor = 0
	}
	if cursor >= len(msgs) {
		return 0
	}
	return countModelVisibleMessages(msgs[cursor:])
}

func (m *middleware[M]) newExtractionAgent(ctx context.Context, toolInfos []*schema.ToolInfo) (*adk.TypedChatModelAgent[M], error) {
	if m.cfg == nil || m.cfg.Write == nil || m.cfg.Write.Model == nil {
		return nil, fmt.Errorf("auto memory extraction agent init failed: missing write model")
	}
	if m.extractionHandler == nil {
		return nil, fmt.Errorf("auto memory extraction agent init failed: missing extraction handler")
	}

	agent, err := adk.NewTypedChatModelAgent[M](ctx, &adk.TypedChatModelAgentConfig[M]{
		Name:  "automemory_extractor",
		Model: m.cfg.Write.Model,
		Handlers: []adk.TypedChatModelAgentMiddleware[M]{
			m.extractionHandler, // fs middleware
			&toolInfoOverrideMiddleware[M]{toolInfos: toolInfos}, // tool info override, for prefix cache
		},
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				UnknownToolsHandler: func(ctx context.Context, name, input string) (string, error) {
					return "This tool is not allowed to be called. Please follow user prompt to proceed.", nil
				},
			},
			EmitInternalEvents: false,
		},
		MaxIterations: m.cfg.Write.MaxTurns,
	})
	if err != nil {
		return nil, fmt.Errorf("auto memory extraction agent init failed: %w", err)
	}
	return agent, nil
}

func (m *middleware[M]) runMemoryExtractionAgent(ctx context.Context, snapshot []M, cursor int, toolInfos []*schema.ToolInfo) error {
	if len(snapshot) == 0 || cursor >= len(snapshot) {
		return nil
	}
	manifest, err := m.buildMemoryManifest(ctx)
	if err != nil {
		return err
	}
	newMessageCount := countModelVisibleMessagesSince(snapshot, cursor)
	skipIndex := m.cfg.Write.SkipIndex || !m.cfg.Read.Index.EnableMemoryIndex
	userPrompt := buildExtractAutoOnlyPrompt(m.extractionMemoryStoresPrompt(), newMessageCount, manifest, skipIndex)
	msgs := append(append([]M{}, snapshot...), makeUserMsg[M](userPrompt))
	extractionAgent, err := m.newExtractionAgent(ctx, toolInfos)
	if err != nil {
		return err
	}

	iter := extractionAgent.Run(ctx, &adk.TypedAgentInput[M]{
		Messages:        msgs,
		EnableStreaming: true,
	})

	if m.cfg != nil && m.cfg.Write != nil && m.cfg.Write.HandleExtractionIterator != nil {
		return m.cfg.Write.HandleExtractionIterator(ctx, iter)
	}

	for {
		ev, ok := iter.Next()
		if !ok {
			return nil
		}
		if ev == nil {
			continue
		}
		if ev.Err != nil {
			return ev.Err
		}
	}
}

func (m *middleware[M]) extractionMemoryStoresPrompt() string {
	stores := make([]memoryStorePromptInfo, 0, len(m.memoryStores))
	for _, store := range m.memoryStores {
		stores = append(stores, memoryStorePromptInfo{
			Name:        store.displayName(),
			Mount:       store.Path,
			Description: strings.TrimSpace(store.Description),
		})
	}
	return buildExtractionMemoryStores(stores)
}

func (m *middleware[M]) buildMemoryManifest(ctx context.Context) (string, error) {
	var stores []memoryManifestStorePromptInfo
	for _, store := range m.memoryStores {
		files, err := store.Backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: CandidateGlobPattern,
			Path:    store.Path,
		})
		if err != nil {
			return "", err
		}
		storeInfo := memoryManifestStorePromptInfo{
			Name:  store.displayName(),
			Mount: store.Path,
		}
		indexAbs := filepath.Join(store.Path, m.cfg.Read.Index.FileName)
		if len(files) == 0 {
			stores = append(stores, storeInfo)
			continue
		}
		for _, fi := range files {
			rel, relErr := filepath.Rel(store.Path, fi.Path)
			if relErr != nil {
				rel = filepath.Base(fi.Path)
			}
			rel = filepath.ToSlash(rel)
			if filepath.Clean(fi.Path) == filepath.Clean(indexAbs) {
				rel = m.cfg.Read.Index.FileName
			}
			desc := ""
			preview, rerr := store.Backend.Read(ctx, &ReadRequest{FilePath: fi.Path, Limit: defaultCandidatePreviewLine})
			if rerr == nil && preview != nil && !isFileNotFoundContent(preview.Content) {
				if fm, ok := parseFrontmatter(preview.Content); ok {
					desc = strings.TrimSpace(fm.Description)
				}
			}
			storeInfo.Files = append(storeInfo.Files, memoryManifestFilePromptInfo{
				MemoryPath:  filepath.ToSlash(filepath.Join(store.displayName(), rel)),
				AbsPath:     fi.Path,
				Saved:       fi.ModifiedAt,
				Description: desc,
			})
		}
		stores = append(stores, storeInfo)
	}
	return buildExtractionMemoryManifest(stores), nil
}

func parseRFC3339NanoBestEffort(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	return time.Time{}
}

type toolInfoOverrideMiddleware[M adk.MessageType] struct {
	adk.TypedBaseChatModelAgentMiddleware[M]

	toolInfos []*schema.ToolInfo
}

func (t *toolInfoOverrideMiddleware[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], _ *adk.TypedModelContext[M]) (
	context.Context, *adk.TypedChatModelAgentState[M], error) {

	toolNameMapping := make(map[string]struct{}, len(t.toolInfos))
	for _, tool := range t.toolInfos {
		toolNameMapping[tool.Name] = struct{}{}
	}

	overrideTools := append([]*schema.ToolInfo{}, t.toolInfos...)
	for _, tool := range state.ToolInfos {
		if _, ok := toolNameMapping[tool.Name]; !ok {
			overrideTools = append(overrideTools, tool)
		}
	}
	state.ToolInfos = overrideTools

	return ctx, state, nil
}

type modelWithTools[M adk.MessageType] struct {
	base  model.BaseModel[M]
	tools []*schema.ToolInfo
}

func (m *modelWithTools[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	newOpts := make([]model.Option, len(opts)+1)
	copy(newOpts, opts)
	newOpts[len(opts)] = model.WithTools(m.tools)
	return m.base.Generate(ctx, input, newOpts...)
}

func (m *modelWithTools[M]) Stream(ctx context.Context, input []M, opts ...model.Option) (*schema.StreamReader[M], error) {
	newOpts := make([]model.Option, len(opts)+1)
	copy(newOpts, opts)
	newOpts[len(opts)] = model.WithTools(m.tools)
	return m.base.Stream(ctx, input, newOpts...)
}

func (m *middleware[M]) sendTopicMemoryEvent(ctx context.Context, msgs []M, memMsg M) {
	var beforeID string
	if len(msgs) > 0 && !isNilMessage(msgs[len(msgs)-1]) {
		beforeID = adk.GetMessageID(msgs[len(msgs)-1])
	}
	if sendEventErr := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{SessionEvent: &adk.SessionEvent[M]{
		Kind: adk.SessionEventMessageInserted,
		MessageInserted: &adk.MessageInsertedEvent[M]{
			Message:         memMsg,
			BeforeMessageID: beforeID,
		},
	}}); sendEventErr != nil {
		m.onErr(ctx, OnErrorStageSendSessionEvent, sendEventErr)
	}
}
