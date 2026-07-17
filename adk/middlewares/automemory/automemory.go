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
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	ainternal "github.com/cloudwego/eino/adk/middlewares/automemory/internal"
	fsmw "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[*memoryExtra]("_eino_adk_automemory_extra")
}

type Config[M adk.MessageType] struct {
	// MemoryDirectory is the persistent memory root directory exposed to automemory.
	// Required. Relative paths are resolved against the process working directory.
	MemoryDirectory string

	// MemoryBackend is the storage backend used by MemoryDirectory.
	// Required. File operations are bounded to MemoryDirectory.
	MemoryBackend Backend

	// GenInstruction returns the runtime memory instruction appended to the main agent system prompt.
	// Use it to customize how strongly the main agent should read from and write to memory during normal task execution.
	// It does not control the post-run extraction agent; use Write.GenInstruction for extraction-specific save criteria.
	// The framework always appends the memory directory manifest after this block.
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

	// OnError is called when automemory encounters an error at a specific stage.
	// Return a non-nil error to block the middleware and propagate the error to the caller.
	// Return nil to skip the error and continue with best-effort degradation.
	// Note: for stages that fire in background goroutines, the return value is ignored
	// (see ErrorStage documentation for the full list).
	// Optional. Defaults to defaultOnError, which blocks on render_instruction and
	// snapshot_marshal, and logs all other errors without blocking.
	OnError func(ctx context.Context, stage ErrorStage, err error) error
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

	// Index controls whether and how MEMORY.md is loaded as a memory index reminder.
	// Optional. Defaults to enabled with MEMORY.md as the index file.
	Index *IndexConfig

	// TopicSelection controls the "LLM select topics" path.
	// Optional. If nil, default topic selection settings are applied.
	// Topic selection becomes active when Read.Model is available.
	TopicSelection *TopicSelectionConfig
}

type IndexConfig struct {
	// FileName is the index file name under MemoryDirectory.
	// Optional. Defaults to MEMORY.md.
	FileName string

	// MaxLines caps index content injected into the memory index reminder.
	// Optional. Defaults to package default.
	MaxLines int

	// MaxBytes caps index content injected into the memory index reminder.
	// Optional. Defaults to package default.
	MaxBytes int
}

type TopicSelectionConfig struct {
	// Enable controls whether topic memory selection is enabled.
	// When false, automemory will not query, rank, read, or inject topic memories.
	// Optional. Defaults to true when nil.
	Enable *bool

	// CandidateGlob is matched against the RELATIVE path under MemoryDirectory.
	// Example: "**/*.md"
	// Optional. Defaults to CandidateGlobPattern.
	CandidateGlob string

	// CandidateLimit caps the number of candidate topic files considered for selection.
	// Optional. Defaults to 200.
	CandidateLimit int

	// CandidatePreviewLines are read from each candidate to parse YAML frontmatter.
	// Optional. Defaults to 30.
	CandidatePreviewLines int

	// TopK caps the number of topic memory files selected for injection.
	// Optional. Defaults to 5.
	TopK int

	// MaxLines caps single topic memory file read lines.
	// Optional. Defaults to 200.
	MaxLines int

	// MaxBytes caps single topic memory file read bytes.
	// Optional. Defaults to 4k.
	MaxBytes int

	// MaxTotalBytes caps the total rendered topic memory reminder.
	// Optional. Defaults to 16k.
	MaxTotalBytes int

	// OutputMode constrains the response format used for topic selection.
	// Supported values: "json_schema", "json_object".
	// When set, the fallback step uses only this response format (no further fallback).
	// When empty (default), the middleware first tries a plain call (tools configured,
	// no response_format), then falls back to json_schema and json_object in order.
	OutputMode TopicSelectionOutputMode
}

// TopicSelectionOutputMode specifies the response format used for topic selection fallback.
type TopicSelectionOutputMode string

const (
	// TopicSelectionOutputModeJSONSchema uses response_format=json_schema for structured output.
	TopicSelectionOutputModeJSONSchema TopicSelectionOutputMode = "json_schema"
	// TopicSelectionOutputModeJSONObject uses response_format=json_object for structured output.
	TopicSelectionOutputModeJSONObject TopicSelectionOutputMode = "json_object"
)

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

	// GenInstruction returns the save policy block used by the post-run memory extraction agent.
	// Use it to customize which observations should or should not be persisted after a run.
	// This replaces the extractor prompt's built-in "What to save" and "What NOT to save" sections; runtime memory behavior
	// in the main agent system prompt is controlled by Config.GenInstruction.
	// Optional. Defaults to the built-in extraction save criteria.
	GenInstruction func(ctx context.Context) (string, error)

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
	boundedMemoryBackend    *ainternal.FSBackend

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
	memoryExtraKey = "__eino_automemory__"
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
	if topicSelectionConfigEnabled(cfg.Read.TopicSelection) && cfg.Read.Model != nil {
		m.topicSelectionModel = &modelWithTools[M]{
			base:  cfg.Read.Model,
			tools: []*schema.ToolInfo{m.topicSelectionTool},
		}
	}

	if cfg.Write.Mode != WriteModeDisabled && cfg.Write.Model != nil {
		fileSystemMiddleware, err := fsmw.NewTyped[M](ctx, &fsmw.MiddlewareConfig{
			Backend:        boundedMemoryBackend,
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
		sessionID := m.coordination.SessionID
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

	// 1) System prompt: inject stable auto memory instruction and directory manifest.
	// If this fails, skip all subsequent memory injection to avoid partial context.
	instruction, err := m.renderInstruction(ctx, nRunCtx.Instruction)
	if err != nil {
		if blockErr := m.onErr(ctx, OnErrorStageRenderInstruction, err); blockErr != nil {
			return ctx, runCtx, blockErr
		}
		return ctx, &nRunCtx, nil
	}
	nRunCtx.Instruction = instruction

	if nRunCtx.AgentInput == nil || len(nRunCtx.AgentInput.Messages) == 0 {
		return ctx, &nRunCtx, nil
	}

	var reminders []M

	// 2) Memory index reminder: inject dynamic MEMORY.md content before the user's query.
	if !hasMemoryIndexInjected(nRunCtx.AgentInput.Messages) {
		indexMsg, err := m.buildMemoryIndexMessage(ctx)
		if err != nil {
			if blockErr := m.onErr(ctx, OnErrorStageReadMemoryIndex, err); blockErr != nil {
				return ctx, runCtx, blockErr
			}
		} else if !isNilMessage(indexMsg) {
			reminders = append(reminders, indexMsg)
		}
	}

	// 3) Topic memories: sync mode selects from the original user query.
	if !hasTopicMemoryInjected(nRunCtx.AgentInput.Messages) &&
		m.cfg.Read.Mode == ReadModeSync && m.topicSelectionEnabled() {
		memMsg, err := m.selectAndBuildTopicMemoryMessage(ctx, nRunCtx.AgentInput)
		if err != nil {
			if blockErr := m.onErr(ctx, OnErrorStageTopicSelectionSync, err); blockErr != nil {
				return ctx, runCtx, blockErr
			}
		} else if !isNilMessage(memMsg) {
			reminders = append(reminders, memMsg)
		}
	}

	if len(reminders) > 0 {
		for _, r := range reminders {
			m.sendTopicMemoryEvent(ctx, nRunCtx.AgentInput.Messages, r)
		}
		msgs := insertMessagesBeforeLastUserQuery(nRunCtx.AgentInput.Messages, reminders)
		nRunCtx.AgentInput = &adk.TypedAgentInput[M]{Messages: msgs, EnableStreaming: nRunCtx.AgentInput.EnableStreaming}
	}

	// 4) Topic memories: async mode starts selection here (cannot use RunLocalValue in BeforeAgent).
	if !hasTopicMemoryInjected(nRunCtx.AgentInput.Messages) &&
		m.cfg.Read.Mode == ReadModeAsync && m.topicSelectionEnabled() {
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
	if !m.topicSelectionEnabled() {
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
		if blockErr := m.onErr(ctx, OnErrorStageTopicSelectionAsync, err); blockErr != nil {
			return ctx, state, blockErr
		}
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

	nState := *state
	nState.Messages = msgs

	return ctx, &nState, nil
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
			memDesc = custom + "\n\n"
		}
	}

	return buildSystemMemoryInstruction(baseInstruction, memDesc, m.resolvedMemoryDirectory)
}

func (m *middleware[M]) buildMemoryIndexMessage(ctx context.Context) (M, error) {
	indexPath := filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName)
	indexContent := ""
	totalLines := 0

	fc, err := m.boundedMemoryBackend.Read(ctx, &ReadRequest{FilePath: indexPath})
	if err == nil && fc != nil && !isFileNotFoundContent(fc.Content) {
		indexContent = fc.Content
		totalLines = strings.Count(indexContent, "\n") + 1
	}
	truncatedMemoryIndex, _, truncated := linesOrSizeTrunc(indexContent, m.cfg.Read.Index.MaxLines, m.cfg.Read.Index.MaxBytes)
	index := memoryIndexPromptInfo{
		FileName:       m.cfg.Read.Index.FileName,
		Path:           indexPath,
		Content:        truncatedMemoryIndex,
		Empty:          strings.TrimSpace(indexContent) == "",
		Truncated:      truncated,
		Lines:          totalLines,
		IncludeContent: true,
	}
	return newMemoryIndexMessage[M](buildMemoryIndexReminder(index)), nil
}

type topicFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Type        string `yaml:"type"`
}

type topicCandidateBundle struct {
	Key     string
	AbsPath string
	RelPath string
	Info    FileInfo
}

type topicMemoryPromptInfo struct {
	MemoryDirectory string
	Path            string
	Saved           string
	Content         string
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

	topics := m.renderTopicMemories(ctx, selected, relToBundle, topK)
	if len(topics) == 0 {
		return nil, nil
	}

	return newMemoryMessage[M]("<!-- automemory -->\n" + buildTopicMemoryReminder(topics)), nil
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
	files, err := m.boundedMemoryBackend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: m.cfg.Read.TopicSelection.CandidateGlob,
		Path:    m.resolvedMemoryDirectory,
	})
	if err != nil {
		return nil, err
	}

	var candidates []topicCandidateBundle
	indexAbs := filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName)
	for _, fi := range files {
		if filepath.Clean(fi.Path) == filepath.Clean(indexAbs) {
			continue
		}
		rel, relErr := filepath.Rel(m.resolvedMemoryDirectory, fi.Path)
		if relErr != nil {
			rel = filepath.Base(fi.Path)
		}
		rel = filepath.ToSlash(rel)
		candidates = append(candidates, topicCandidateBundle{
			Key:     rel,
			AbsPath: fi.Path,
			RelPath: rel,
			Info:    fi,
		})
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
	preview, err := m.boundedMemoryBackend.Read(ctx, &ReadRequest{
		FilePath: bundle.AbsPath,
		Limit:    m.cfg.Read.TopicSelection.CandidatePreviewLines,
	})
	if err != nil || preview == nil || isFileNotFoundContent(preview.Content) {
		return topicCandidateBundle{}, "", false
	}

	desc := describeTopicCandidate(preview.Content)
	manifestLine := fmt.Sprintf("- %s (saved %s): %s", bundle.Key, bundle.Info.ModifiedAt, desc)
	return bundle, manifestLine, true
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

	valid := make(map[string]struct{}, len(relToBundle))
	for k := range relToBundle {
		valid[k] = struct{}{}
	}

	mode := m.cfg.Read.TopicSelection.OutputMode
	if mode != "" {
		// Fixed mode: skip plain call, directly use the configured response_format.
		return m.selectTopicWithResponseFormat(ctx, mode, userMsg, valid, topK)
	}

	// Step 1: plain call (tools configured, no forced choice, no response_format).
	selected, plainErr := m.selectTopicPlain(ctx, userMsg, valid, topK)
	if plainErr == nil {
		return selected, nil
	}

	// Step 2: auto fallback with response_format — try json_schema, then json_object.
	selected, err = m.selectTopicWithResponseFormat(ctx, TopicSelectionOutputModeJSONSchema, userMsg, valid, topK)
	if err == nil {
		return selected, nil
	}
	return m.selectTopicWithResponseFormat(ctx, TopicSelectionOutputModeJSONObject, userMsg, valid, topK)
}

// selectTopicPlain calls the model with tools configured but no forced choice and no response_format.
func (m *middleware[M]) selectTopicPlain(
	ctx context.Context,
	userMsg string,
	valid map[string]struct{},
	topK int,
) ([]string, error) {
	respStream, err := m.topicSelectionModel.Stream(
		ctx,
		[]M{
			makeSystemMsg[M](getTopicSelectionSystemPrompt() + "\n\n" + getTopicSelectionJSONOutputHint()),
			makeUserMsg[M](userMsg),
		},
	)
	if err != nil {
		return nil, err
	}

	resp, err := concatMessageStream(respStream)
	if err != nil {
		return nil, err
	}

	return m.parseTopicSelectionResponse(resp, valid, topK)
}

// selectTopicWithResponseFormat calls the model with tools and the given response_format.
func (m *middleware[M]) selectTopicWithResponseFormat(
	ctx context.Context,
	mode TopicSelectionOutputMode,
	userMsg string,
	valid map[string]struct{},
	topK int,
) ([]string, error) {
	var rfOpt model.Option
	switch mode {
	case TopicSelectionOutputModeJSONSchema:
		rfOpt = model.WithResponseFormat(&schema.ResponseFormat{
			Type: schema.ResponseFormatTypeJSONSchema,
			JSONSchema: &schema.ResponseFormatJSONSchema{
				Schema: topicSelectionJSONSchema(),
			},
		})
	case TopicSelectionOutputModeJSONObject:
		rfOpt = model.WithResponseFormat(&schema.ResponseFormat{
			Type: schema.ResponseFormatTypeJSONObject,
		})
	default:
		return nil, fmt.Errorf("unsupported topic selection output mode: %q", mode)
	}

	respStream, err := m.topicSelectionModel.Stream(
		ctx,
		[]M{
			makeSystemMsg[M](getTopicSelectionSystemPrompt() + "\n\n" + getTopicSelectionJSONOutputHint()),
			makeUserMsg[M](userMsg),
		},
		rfOpt,
	)
	if err != nil {
		return nil, err
	}

	resp, err := concatMessageStream(respStream)
	if err != nil {
		return nil, err
	}

	return m.parseTopicSelectionResponse(resp, valid, topK)
}

// parseTopicSelectionResponse tries to extract selected memories from a model response,
// checking tool_call first, then falling back to content JSON parsing.
func (m *middleware[M]) parseTopicSelectionResponse(resp M, valid map[string]struct{}, topK int) ([]string, error) {
	// Try tool call first.
	if selected, err := parseTopicSelectionFromToolCall(resp, valid); err == nil {
		if len(selected) > topK {
			return selected[:topK], nil
		}
		return selected, nil
	}

	// Fall back to content JSON parsing.
	selected, err := parseTopicSelectionFromContent[M](resp, valid)
	if err != nil {
		return nil, err
	}
	if len(selected) > topK {
		return selected[:topK], nil
	}
	return selected, nil
}

func (m *middleware[M]) renderTopicMemories(
	ctx context.Context,
	selected []string,
	relToBundle map[string]topicCandidateBundle,
	topK int,
) []topicMemoryPromptInfo {
	capHint := topK
	if capHint > len(selected) {
		capHint = len(selected)
	}
	rendered := make([]topicMemoryPromptInfo, 0, capHint)
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
		topic, ok := m.renderTopicMemory(ctx, bundle)
		if !ok {
			continue
		}
		topicBytes := len(topic.Content) + len(topic.MemoryDirectory) + len(topic.Path)
		if maxTotalBytes > 0 && totalBytes+topicBytes > maxTotalBytes {
			if len(rendered) == 0 {
				if len(topic.Content) > maxTotalBytes {
					topic.Content = topic.Content[:maxTotalBytes]
				}
				rendered = append(rendered, topic)
			}
			break
		}
		rendered = append(rendered, topic)
		totalBytes += topicBytes
	}
	return rendered
}

func (m *middleware[M]) renderTopicMemory(ctx context.Context, bundle topicCandidateBundle) (topicMemoryPromptInfo, bool) {
	full, err := m.boundedMemoryBackend.Read(ctx, &ReadRequest{FilePath: bundle.AbsPath})
	if err != nil || full == nil || isFileNotFoundContent(full.Content) {
		return topicMemoryPromptInfo{}, false
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

	return topicMemoryPromptInfo{
		MemoryDirectory: m.resolvedMemoryDirectory,
		Path:            bundle.RelPath,
		Saved:           bundle.Info.ModifiedAt,
		Content:         content,
	}, true
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

	sessionID := m.coordination.SessionID
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
	if hasMemoryWritesSince(state.Messages, cursor, m.resolvedMemoryDirectory) {
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
			if blockErr := m.onErr(ctx, OnErrorStageMemoryWriteSync, err); blockErr != nil {
				return ctx, blockErr
			}
			return ctx, nil
		}
		if coordKey != "" {
			_ = setCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey, end)
		}
		state = markWriteCursor(state, end)
		return ctx, nil

	case WriteModeAsync:
		if coordKey == "" {
			if err := m.runMemoryExtractionAgent(ctx, state.Messages, cursor, state.ToolInfos); err != nil {
				if blockErr := m.onErr(ctx, OnErrorStageMemoryWriteSync, err); blockErr != nil {
					return ctx, blockErr
				}
				return ctx, nil
			}
			state = markWriteCursor(state, len(state.Messages))
			return ctx, nil
		}
		snap, err := buildPendingSnapshot(state.Messages, cursor, state.ToolInfos)
		if err != nil {
			if blockErr := m.onErr(ctx, OnErrorStageSnapshotMarshal, err); blockErr != nil {
				return ctx, blockErr
			}
			return ctx, nil
		}
		unlock, ok, err := m.coordination.Coordinator.AcquireLock(ctx, coordKey, m.coordination.LockTTL)
		if err != nil {
			if blockErr := m.onErr(ctx, OnErrorStageAcquireExtractionLock, err); blockErr != nil {
				return ctx, blockErr
			}
			return ctx, nil
		}
		if !ok {
			if err := setCoordinatorPendingSnapshot(ctx, m.coordination.Coordinator, coordKey, snap, m.coordination.LockTTL); err != nil {
				m.onErr(ctx, OnErrorStageStashPendingSnapshot, err) // nolint: errcheck
			}
			return ctx, nil
		}
		go m.runExtractionDrain(ctx, coordKey, unlock, snap)
		return ctx, nil

	default:
		return ctx, nil
	}
}

func (m *middleware[M]) runExtractionDrain(ctx context.Context, coordKey string, unlock func(context.Context) error, initial *PendingSnapshot) {
	defer func() {
		if unlock == nil {
			return
		}
		if err := unlock(ctx); err != nil {
			m.onErr(ctx, OnErrorStageReleaseExtractionLock, err) //nolint:errcheck
		}
	}()

	current := initial
	for current != nil {
		msgs, cursor, toolInfos, err := decodePendingSnapshot[M](current)
		if err != nil {
			m.onErr(ctx, OnErrorStageDecodePendingSnapshot, err) //nolint:errcheck
		} else if err := m.runMemoryExtractionAgent(ctx, msgs, cursor, toolInfos); err != nil {
			m.onErr(ctx, OnErrorStageMemoryWriteAsync, err) //nolint:errcheck
		} else {
			_ = setCoordinatorCursor(ctx, m.coordination.Coordinator, coordKey, len(msgs))
		}

		next, loadErr := popCoordinatorPendingSnapshot(ctx, m.coordination.Coordinator, coordKey)
		if loadErr != nil {
			m.onErr(ctx, OnErrorStageLoadPendingSnapshot, loadErr) //nolint:errcheck
			return
		}
		current = next
	}
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
	savePolicy, err := m.extractSavePolicyInstruction(ctx)
	if err != nil {
		return err
	}
	userPrompt := buildExtractAutoOnlyPrompt(m.extractionMemoryDirectoryPrompt(), newMessageCount, manifest, savePolicy)
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

func (m *middleware[M]) extractSavePolicyInstruction(ctx context.Context) (string, error) {
	if m.cfg == nil || m.cfg.Write == nil || m.cfg.Write.GenInstruction == nil {
		return "", nil
	}
	custom, err := m.cfg.Write.GenInstruction(ctx)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(custom), nil
}

func (m *middleware[M]) extractionMemoryDirectoryPrompt() string {
	index := &memoryIndexPromptInfo{
		FileName: m.cfg.Read.Index.FileName,
		Path:     filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName),
	}
	return buildMemoryDirectoryManifest(m.resolvedMemoryDirectory, index)
}

func (m *middleware[M]) buildMemoryManifest(ctx context.Context) (string, error) {
	files, err := m.boundedMemoryBackend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: CandidateGlobPattern,
		Path:    m.resolvedMemoryDirectory,
	})
	if err != nil {
		return "", err
	}
	manifest := memoryManifestPromptInfo{Directory: m.resolvedMemoryDirectory}
	indexAbs := filepath.Join(m.resolvedMemoryDirectory, m.cfg.Read.Index.FileName)
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
		manifest.Files = append(manifest.Files, memoryManifestFilePromptInfo{
			MemoryPath:  rel,
			AbsPath:     fi.Path,
			Saved:       fi.ModifiedAt,
			Description: desc,
		})
	}
	return buildExtractionMemoryManifest(manifest), nil
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
