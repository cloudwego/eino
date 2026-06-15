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
	MemoryDirectory string

	MemoryBackend Backend

	// Model is the default model used by topic selection and memory extraction.
	// Per-read/per-write overrides can be configured in Read.Model / Write.Model.
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
	OnError func(ctx context.Context, stage string, err error)
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

	// Instruction overrides the default auto memory instruction block appended to system prompt.
	// Optional.
	Instruction *string

	// Index controls how MEMORY.md is loaded into system prompt.
	// Optional.
	Index *IndexConfig

	// TopicSelection controls the "LLM select topics" path.
	// Optional. If nil, default topic selection settings are applied.
	// Topic selection becomes active when Read.Model is available.
	TopicSelection *TopicSelectionConfig
}

type IndexConfig struct {
	FileName string
	MaxLines int
	MaxBytes int
}

type TopicSelectionConfig struct {
	// CandidateGlob is matched against the RELATIVE path under MemoryDirectory.
	// Example: "**/*.md"
	CandidateGlob  string
	CandidateLimit int
	// CandidatePreviewLines are read from each candidate to parse YAML frontmatter.
	CandidatePreviewLines int

	TopK int

	MaxLines int
	MaxBytes int
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

	resolvedMemoryDirectory string
	boundedMemoryBackend    Backend

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

// New creates an automemory middleware from the provided configuration.
func New[M adk.MessageType](ctx context.Context, config *Config[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
	if config == nil {
		return nil, fmt.Errorf("auto memory config: invalid")
	}

	cfg := cloneConfig(config)
	if cfg.MemoryDirectory == "" || cfg.MemoryBackend == nil {
		return nil, fmt.Errorf("auto memory config: invalid")
	}

	resolvedMemoryDir, err := ainternal.ResolveMemoryDir(cfg.MemoryDirectory)
	if err != nil {
		return nil, fmt.Errorf("auto memory config: resolve memory directory: %w", err)
	}
	boundedMemoryBackend, err := ainternal.NewFSBackend(cfg.MemoryBackend, ainternal.FSBackendConfig{
		BaseDir:           resolvedMemoryDir,
		NotFoundAsContent: true,
		ErrorPrefix:       "memory backend",
	})
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
		resolvedMemoryDirectory:           resolvedMemoryDir,
		boundedMemoryBackend:              boundedMemoryBackend,
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
		writeFSBackend, err := newFSBackend(cfg.MemoryBackend, resolvedMemoryDir)
		if err != nil {
			return nil, err
		}
		fileSystemMiddleware, err := fsmw.NewTyped[M](ctx, &fsmw.MiddlewareConfig{
			Backend:        writeFSBackend,
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
			if remoteCursor, ok, err := m.coordination.Coordinator.GetCursor(ctx, sessionID); err == nil && ok && remoteCursor > localCursor {
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

	// If automemory was already injected into the instruction or message list,
	// skip all memory-loading work for this run and let the agent continue.
	if hasInstructionInjected(nRunCtx.Instruction) || (nRunCtx.AgentInput != nil && alreadyInjected(nRunCtx.AgentInput.Messages)) {
		return ctx, &nRunCtx, nil
	}

	// 1) System prompt: inject auto memory instruction + MEMORY.md content (best-effort).
	nRunCtx.Instruction = m.injectIndexIntoInstruction(ctx, nRunCtx.Instruction)

	// 2) Topic memories: sync mode injects before the user's query.
	if m.cfg.Read.Mode == ReadModeSync && m.cfg.Read.TopicSelection != nil && m.topicSelectionModel != nil {
		memMsg, err := m.selectAndBuildTopicMemoryMessage(ctx, nRunCtx.AgentInput)
		if err != nil {
			m.onErr(ctx, OnErrorStageTopicSelectionSync, err)
		} else if memMsg != nil && nRunCtx.AgentInput != nil && len(nRunCtx.AgentInput.Messages) > 0 {
			msgs := append([]M{}, nRunCtx.AgentInput.Messages...)
			msgs = append(msgs, memMsg)
			nRunCtx.AgentInput = &adk.TypedAgentInput[M]{Messages: msgs, EnableStreaming: nRunCtx.AgentInput.EnableStreaming}
		}
	}

	// 3) Topic memories: async mode starts selection here (cannot use RunLocalValue in BeforeAgent).
	if m.cfg.Read.Mode == ReadModeAsync && m.cfg.Read.TopicSelection != nil && m.topicSelectionModel != nil && nRunCtx.AgentInput != nil {
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
		msgs = append(msgs, state.Messages...)
		msgs = append(msgs, newMemoryMessage[M](content))
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
		cfg.Read.Index = &IndexConfig{}
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
		if cfg.Read.Instruction != nil {
			instructionCopy := *cfg.Read.Instruction
			cp.Read.Instruction = &instructionCopy
		}
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

func (m *middleware[M]) injectIndexIntoInstruction(ctx context.Context, baseInstruction string) string {
	memDir := m.resolvedMemoryDirectory

	var memDesc string
	if m.cfg.Read.Instruction != nil {
		memDesc = *m.cfg.Read.Instruction
	} else {
		s, err := pyfmt.Fmt(getDefaultMemoryInstruction(), map[string]any{"memory_dir": memDir})
		if err != nil {
			m.onErr(ctx, OnErrorStageRenderInstruction, err)
			return baseInstruction
		}
		memDesc = s
	}

	indexPath := filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName)
	indexContent := ""
	totalLines := 0

	fc, err := m.boundedMemoryBackend.Read(ctx, &ReadRequest{FilePath: indexPath})
	if err == nil && fc != nil {
		if isFileNotFoundContent(fc.Content) {
			indexContent = ""
		} else {
			indexContent = fc.Content
			totalLines = strings.Count(indexContent, "\n") + 1
		}
	} else {
		// Missing index is not fatal; keep empty.
		indexContent = ""
	}

	sb := make([]string, 0, 5)
	sb = append(sb, memDesc)
	sb = append(sb, "## "+m.cfg.Read.Index.FileName)
	if strings.TrimSpace(indexContent) == "" {
		sb = append(sb, getAppendEmptyIndexTemplate())
	} else {
		truncatedMemoryIndex, _, truncated := linesOrSizeTrunc(indexContent, m.cfg.Read.Index.MaxLines, m.cfg.Read.Index.MaxBytes)
		sb = append(sb, truncatedMemoryIndex)
		if truncated {
			notify, err := pyfmt.Fmt(getAppendCurrentIndexTruncNotify(), map[string]any{
				"memory_lines": totalLines,
			})
			if err == nil {
				sb = append(sb, notify)
			}
		}
	}

	return baseInstruction + "\n" + instructionMarker + "\n" + strings.Join(sb, "\n")
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

func (m *middleware[M]) onErr(ctx context.Context, stage string, err error) {
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
	AbsPath string
	RelPath string
	Info    FileInfo
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
		relToBundle[bundle.RelPath] = bundle
		available = append(available, manifestLine)
		orderedRel = append(orderedRel, bundle.RelPath)
	}

	return relToBundle, available, orderedRel, nil
}

func (m *middleware[M]) topicSelectionCandidates(ctx context.Context) ([]FileInfo, error) {
	files, err := m.boundedMemoryBackend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: m.cfg.Read.TopicSelection.CandidateGlob,
		Path:    m.resolvedMemoryDirectory,
	})
	if err != nil || len(files) == 0 {
		return nil, err
	}

	indexAbs := filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName)
	candidates := make([]FileInfo, 0, len(files))
	for _, fi := range files {
		if filepath.Clean(fi.Path) == filepath.Clean(indexAbs) {
			continue
		}
		candidates = append(candidates, fi)
	}
	if len(candidates) == 0 {
		return nil, nil
	}

	sort.Slice(candidates, func(i, j int) bool {
		return parseRFC3339NanoBestEffort(candidates[i].ModifiedAt).After(parseRFC3339NanoBestEffort(candidates[j].ModifiedAt))
	})
	if len(candidates) > m.cfg.Read.TopicSelection.CandidateLimit {
		candidates = candidates[:m.cfg.Read.TopicSelection.CandidateLimit]
	}
	return candidates, nil
}

func (m *middleware[M]) buildTopicCandidateBundle(ctx context.Context, fi FileInfo) (topicCandidateBundle, string, bool) {
	rel, relErr := filepath.Rel(m.resolvedMemoryDirectory, fi.Path)
	if relErr != nil {
		rel = filepath.Base(fi.Path)
	}
	rel = filepath.ToSlash(rel)

	preview, err := m.boundedMemoryBackend.Read(ctx, &ReadRequest{
		FilePath: fi.Path,
		Limit:    m.cfg.Read.TopicSelection.CandidatePreviewLines,
	})
	if err != nil || preview == nil || isFileNotFoundContent(preview.Content) {
		return topicCandidateBundle{}, "", false
	}

	desc := describeTopicCandidate(preview.Content)
	manifestLine := fmt.Sprintf("- %s (saved %s): %s", rel, fi.ModifiedAt, desc)
	return topicCandidateBundle{AbsPath: fi.Path, RelPath: rel, Info: fi}, manifestLine, true
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
	if len(orderedRel) <= topK {
		return orderedRel, nil
	}

	userMsg, err := pyfmt.Fmt(getTopicSelectionUserPrompt(), map[string]any{
		"user_query":         userQuery,
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
	return parseTopicSelectionFromToolCall(resp, valid)
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
		rendered = append(rendered, renderedContent)
	}
	return rendered
}

func (m *middleware[M]) renderTopicMemory(ctx context.Context, bundle topicCandidateBundle) (string, bool) {
	full, err := m.boundedMemoryBackend.Read(ctx, &ReadRequest{FilePath: bundle.AbsPath})
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
		"<system-reminder>\nContents of %s (saved %s):\n\n%s\n</system-reminder>",
		bundle.AbsPath,
		bundle.Info.ModifiedAt,
		content,
	), true
}

func topicSelectionToolInfo() *schema.ToolInfo {
	return &schema.ToolInfo{
		Name: topicSelectionToolName,
		Desc: "Select which memory files to surface for the current query. Return selected_memories as RELATIVE paths (relative to the memory directory).",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"selected_memories": {
				Type:     schema.Array,
				Desc:     "Relative paths of selected memory files, e.g. \"debugging.md\" or \"notes/patterns.md\".",
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
		if v, ok := extra[memoryExtraKey]; ok && v != nil {
			return true
		}
	}
	// Backward compatible marker (older versions).
	return strings.Contains(userMessageTextContent(m), "<!-- automemory -->")
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

	cursor := getWriteCursorFromMessages(state.Messages)
	if sessionID != "" {
		if remoteCursor, ok, err := m.coordination.Coordinator.GetCursor(ctx, sessionID); err == nil && ok && remoteCursor > cursor {
			cursor = remoteCursor
			state = markWriteCursor(state, cursor)
		}
	}
	if cursor >= len(state.Messages) {
		return ctx, nil
	}

	// Skip background extraction if the main agent already wrote memory files in this range.
	if hasMemoryWritesSince(state.Messages, cursor, m.resolvedMemoryDirectory) {
		end := len(state.Messages)
		if sessionID != "" {
			_ = m.coordination.Coordinator.SetCursor(ctx, sessionID, end)
		}
		state = markWriteCursor(state, end)
		return ctx, nil
	}

	if countModelVisibleMessages(state.Messages[cursor:]) == 0 {
		end := len(state.Messages)
		if sessionID != "" {
			_ = m.coordination.Coordinator.SetCursor(ctx, sessionID, end)
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
		if sessionID != "" {
			_ = m.coordination.Coordinator.SetCursor(ctx, sessionID, end)
		}
		state = markWriteCursor(state, end)
		return ctx, nil

	case WriteModeAsync:
		if sessionID == "" {
			sessionID = getOrInitWriteSessionID(ctx)
		}
		snap, err := buildPendingSnapshot(state.Messages, cursor, state.ToolInfos)
		if err != nil {
			m.onErr(ctx, OnErrorStageSnapshotMarshal, err)
			return ctx, nil
		}
		unlock, ok, err := m.coordination.Coordinator.AcquireLock(ctx, sessionID, m.coordination.LockTTL)
		if err != nil {
			m.onErr(ctx, OnErrorStageAcquireExtractionLock, err)
			return ctx, nil
		}
		if !ok {
			if err := m.coordination.Coordinator.SetPendingSnapshot(ctx, sessionID, snap); err != nil {
				m.onErr(ctx, OnErrorStageStashPendingSnapshot, err)
			}
			return ctx, nil
		}
		go m.runExtractionDrain(ctx, sessionID, unlock, snap)
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

func (m *middleware[M]) runExtractionDrain(ctx context.Context, sessionID string, unlock func(context.Context) error, initial *PendingSnapshot) {
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
			_ = m.coordination.Coordinator.SetCursor(ctx, sessionID, len(msgs))
		}

		next, loadErr := m.coordination.Coordinator.PopPendingSnapshot(ctx, sessionID)
		if loadErr != nil {
			m.onErr(ctx, OnErrorStageLoadPendingSnapshot, loadErr)
			return
		}
		current = next
	}
}

func hasMemoryWritesSince[M adk.MessageType](msgs []M, cursor int, memoryDir string) bool {
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
			if fp, ok := extractFilePath(tc.Function.Arguments); ok && isPathWithinMemoryDir(memoryDir, fp) {
				return true
			}
		}
	}
	return false
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
	userPrompt := buildExtractAutoOnlyPrompt(m.resolvedMemoryDirectory, newMessageCount, manifest, m.cfg.Write.SkipIndex)
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

func (m *middleware[M]) buildMemoryManifest(ctx context.Context) (string, error) {
	files, err := m.boundedMemoryBackend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: CandidateGlobPattern,
		Path:    m.resolvedMemoryDirectory,
	})
	if err != nil {
		return "", err
	}
	indexAbs := filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName)
	lines := make([]string, 0, len(files))
	for _, fi := range files {
		rel, relErr := filepath.Rel(m.resolvedMemoryDirectory, fi.Path)
		if relErr != nil {
			rel = filepath.Base(fi.Path)
		}
		rel = filepath.ToSlash(rel)
		if filepath.Clean(fi.Path) == filepath.Clean(indexAbs) {
			rel = m.cfg.Read.Index.FileName
		}
		desc := ""
		preview, rerr := m.boundedMemoryBackend.Read(ctx, &ReadRequest{FilePath: fi.Path, Limit: defaultCandidatePreviewLine})
		if rerr == nil && preview != nil && !isFileNotFoundContent(preview.Content) {
			if fm, ok := parseFrontmatter(preview.Content); ok {
				desc = strings.TrimSpace(fm.Description)
			}
		}
		if desc != "" {
			lines = append(lines, fmt.Sprintf("- %s (saved %s): %s", rel, fi.ModifiedAt, desc))
		} else {
			lines = append(lines, fmt.Sprintf("- %s (saved %s)", rel, fi.ModifiedAt))
		}
	}
	return strings.Join(lines, "\n"), nil
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
