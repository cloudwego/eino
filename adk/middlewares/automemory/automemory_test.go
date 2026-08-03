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
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type fixedModel struct {
	out string
}

func (m *fixedModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "select-fixed",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      topicSelectionToolName,
				Arguments: m.out,
			},
		},
	}), nil
}

func (m *fixedModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, _ := m.Generate(ctx, input)
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *fixedModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func requireMemoryIndexMessage(t *testing.T, msg *schema.Message, contains ...string) {
	t.Helper()
	require.True(t, isMemoryIndexMessage(msg))
	require.NotNil(t, msg.Extra)
	require.NotNil(t, msg.Extra[memoryExtraKey])
	require.Contains(t, msg.Content, "<system-reminder>")
	require.Contains(t, msg.Content, "<!-- automemory:index -->")
	require.True(t,
		strings.Contains(msg.Content, "# Memory Index") || strings.Contains(msg.Content, "# 记忆索引文件"),
		"memory index reminder should contain an index title",
	)
	require.NotContains(t, msg.Content, "<memory-index>")
	require.NotContains(t, msg.Content, "<memory-index-1>")
	require.NotContains(t, msg.Content, "<index-file-content>")
	require.NotContains(t, msg.Content, "</index-file-content>")
	require.NotContains(t, msg.Content, "### 1. Name:")
	require.NotContains(t, msg.Content, "#### Index file content:")
	for _, s := range contains {
		require.Contains(t, msg.Content, s)
	}
}

func requireTopicMemoryMessage(t *testing.T, msg *schema.Message, contains ...string) {
	t.Helper()
	require.True(t, isTopicMemoryMessage(msg))
	require.NotNil(t, msg.Extra)
	require.NotNil(t, msg.Extra[memoryExtraKey])
	require.Contains(t, msg.Content, "<!-- automemory -->")
	require.Contains(t, msg.Content, "<system-reminder>")
	require.Contains(t, msg.Content, "<topic-memory-1>")
	require.NotContains(t, msg.Content, "<topic-memory-content>")
	require.NotContains(t, msg.Content, "</topic-memory-content>")
	require.Contains(t, msg.Content, "</system-reminder>")
	for _, s := range contains {
		require.Contains(t, msg.Content, s)
	}
}

func requireWriteCursor(t *testing.T, msgs []*schema.Message, cursor int) {
	t.Helper()
	for _, msg := range msgs {
		if msg == nil || msg.Extra == nil {
			continue
		}
		meta, ok := msg.Extra[memoryExtraKey].(*memoryExtra)
		if ok && meta != nil && meta.Type == "write_cursor" {
			require.EqualValues(t, cursor, meta.Cursor)
			return
		}
	}
	require.Fail(t, "write cursor not found")
}

func countMemoryIndexMessages(msgs []*schema.Message) int {
	count := 0
	for _, msg := range msgs {
		if isMemoryIndexMessage(msg) {
			count++
		}
	}
	return count
}

func countTopicMemoryMessages(msgs []*schema.Message) int {
	count := 0
	for _, msg := range msgs {
		if isTopicMemoryMessage(msg) {
			count++
		}
	}
	return count
}

func TestMiddleware_IndexInjection_Empty(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		// Model nil => topic selection disabled.
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out.Instruction, "# Auto memory")
	require.Contains(t, out.Instruction, "persistent file-based memory at `/mem`")
	require.NotContains(t, out.Instruction, "Index file path: /mem/MEMORY.md")
	require.NotContains(t, out.Instruction, "#### Index file content: MEMORY.md")
	require.NotContains(t, out.Instruction, "Rules:")
	require.Len(t, out.AgentInput.Messages, 2)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md", "currently empty")
	require.Contains(t, out.AgentInput.Messages[1].Content, "hi")
}

func TestMiddleware_IndexInjection_ChineseInstruction(t *testing.T) {
	require.NoError(t, adk.SetLanguage(adk.LanguageChinese))
	defer func() {
		require.NoError(t, adk.SetLanguage(adk.LanguageEnglish))
	}()

	ctx := context.Background()
	b := NewInMemoryBackend()

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out.Instruction, "# 自动记忆")
	require.NotContains(t, out.Instruction, "你的 MEMORY.md 当前为空")
	require.Len(t, out.AgentInput.Messages, 2)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "# 记忆索引文件", "文件 /mem/MEMORY.md", "内容为空")
	require.Contains(t, out.AgentInput.Messages[1].Content, "hi")
}

func TestMiddleware_IndexInjection_CustomInstructionKeepsDirectoryManifest(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	custom := "custom memory header"

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		GenInstruction: func(ctx context.Context) (string, error) {
			return custom, nil
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out.Instruction, "custom memory header")
	require.Contains(t, out.Instruction, "## Memory directory")
	require.Contains(t, out.Instruction, "Path: /mem")
	require.NotContains(t, out.Instruction, "Index file path")
	require.Len(t, out.AgentInput.Messages, 2)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	require.Contains(t, out.AgentInput.Messages[1].Content, "hi")
}

func TestMiddleware_IndexInjection_CustomInstructionErrorReportsRenderStage(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	var stages []ErrorStage

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		GenInstruction: func(ctx context.Context) (string, error) {
			return "", fmt.Errorf("custom instruction failed")
		},
		OnError: func(ctx context.Context, stage ErrorStage, err error) {
			stages = append(stages, stage)
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Equal(t, "base", out.Instruction)
	require.Equal(t, []ErrorStage{OnErrorStageRenderInstruction}, stages)
}

func TestNew_DoesNotMutateConfig(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()

	cfgNilNested := &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
	}
	_, err := New(ctx, cfgNilNested)
	require.NoError(t, err)
	require.Nil(t, cfgNilNested.Read)
	require.Nil(t, cfgNilNested.Write)
	require.Nil(t, cfgNilNested.Coordination)

	cfgExplicitNested := &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
		Read:            &ReadConfig[*schema.Message]{},
		Write:           &WriteConfig[*schema.Message]{},
		Coordination:    &CoordinationConfig[*schema.Message]{},
	}
	_, err = New(ctx, cfgExplicitNested)
	require.NoError(t, err)
	require.Empty(t, cfgExplicitNested.Read.Mode)
	require.Nil(t, cfgExplicitNested.Read.Model)
	require.Nil(t, cfgExplicitNested.Read.Index)
	require.Nil(t, cfgExplicitNested.Read.TopicSelection)
	require.Empty(t, cfgExplicitNested.Write.Mode)
	require.Nil(t, cfgExplicitNested.Write.Model)
	require.Zero(t, cfgExplicitNested.Write.MaxTurns)
	require.Nil(t, cfgExplicitNested.Coordination.Coordinator)
	require.Zero(t, cfgExplicitNested.Coordination.LockTTL)
}

func TestMiddleware_TopicSelection_InsertsMemoryMessage(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()

	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md) - notes\n", now)
	b.put("/mem/debugging.md", "---\nname: Debugging\ndescription: build and test commands\ntype: project\n---\n\n# Debugging\npnpm test\n", now)
	b.put("/mem/other.md", "---\nname: Other\ndescription: unrelated\ntype: misc\n---\n", now.Add(-time.Hour))

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
	})
	require.NoError(t, err)

	in := &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to run tests?")}}
	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  in,
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.NotNil(t, out.AgentInput)
	require.Len(t, out.AgentInput.Messages, 3)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	requireTopicMemoryMessage(t, out.AgentInput.Messages[1], "Contents of /mem/debugging.md")
	require.Equal(t, schema.User, out.AgentInput.Messages[2].Role)
	require.Contains(t, out.AgentInput.Messages[2].Content, "How to run tests?")
}

func TestMiddleware_MemoryDirectory_IndexAndTopicSelection(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()

	b.put("/mem/MEMORY.md", "- [prefs.md](prefs.md) - user preferences\n- [debugging.md](debugging.md) - project debugging\n", now)
	b.put("/mem/prefs.md", "---\ndescription: editor preferences\n---\n\nUse concise answers.\n", now)
	b.put("/mem/debugging.md", "---\ndescription: test commands\n---\n\nRun go test ./...\n", now)

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How should I run tests?")}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out.Instruction, "persistent file-based memory at `/mem`")
	require.NotContains(t, out.Instruction, "Index file path: /mem/MEMORY.md")
	require.NotContains(t, out.Instruction, "#### Index file content: MEMORY.md")
	require.Len(t, out.AgentInput.Messages, 3)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0],
		"Contents of /mem/MEMORY.md",
		"- [prefs.md](prefs.md) - user preferences",
		"- [debugging.md](debugging.md) - project debugging",
	)
	indexReminder := out.AgentInput.Messages[0].Content
	userIndexPos := strings.Index(indexReminder, "- [prefs.md](prefs.md) - user preferences")
	projectIndexPos := strings.Index(indexReminder, "- [debugging.md](debugging.md) - project debugging")
	require.True(t, userIndexPos >= 0 && projectIndexPos > userIndexPos)
	requireTopicMemoryMessage(t, out.AgentInput.Messages[1], "Contents of /mem/debugging.md", "Run go test ./...")
	require.NotContains(t, out.AgentInput.Messages[1].Content, "Use concise answers.")
	require.Contains(t, out.AgentInput.Messages[2].Content, "How should I run tests?")
}

func TestMiddleware_TopicSelection_AsyncInjectsInBeforeModel(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()

	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md) - notes\n", now)
	b.put("/mem/debugging.md", "---\nname: Debugging\ndescription: build and test commands\ntype: project\n---\n\n# Debugging\npnpm test\n", now)

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
		Read:            &ReadConfig[*schema.Message]{Mode: ReadModeAsync},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to run tests?")}},
	}
	ctx2, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Len(t, out.AgentInput.Messages, 2) // async doesn't inject topic memory here
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	require.Contains(t, out.AgentInput.Messages[1].Content, "How to run tests?")

	st := &adk.ChatModelAgentState{Messages: []adk.Message{schema.UserMessage("How to run tests?")}}

	require.Eventually(t, func() bool {
		_, next, err := mw.BeforeModelRewriteState(ctx2, st, nil)
		require.NoError(t, err)
		st = next
		last := st.Messages[len(st.Messages)-1]
		return len(st.Messages) == 2 && last.Extra != nil && last.Extra["__eino_automemory__"] != nil
	}, 2*time.Second, 10*time.Millisecond)
}

func TestMiddleware_BeforeModelRewriteState_PreservesToolInfos(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()

	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md) - notes\n", now)
	b.put("/mem/debugging.md", "---\nname: Debugging\ndescription: build and test commands\ntype: project\n---\n\n# Debugging\npnpm test\n", now)

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
		Read:            &ReadConfig[*schema.Message]{Mode: ReadModeAsync},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to run tests?")}},
	}
	ctx2, _, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)

	toolInfos := []*schema.ToolInfo{
		{Name: "tool_a"},
		{Name: "tool_b"},
	}
	deferredToolInfos := []*schema.ToolInfo{
		{Name: "deferred_tool_c"},
	}
	st := &adk.ChatModelAgentState{
		Messages:          []adk.Message{schema.UserMessage("How to run tests?")},
		ToolInfos:         toolInfos,
		DeferredToolInfos: deferredToolInfos,
	}

	require.Eventually(t, func() bool {
		_, next, err := mw.BeforeModelRewriteState(ctx2, st, nil)
		require.NoError(t, err)
		st = next
		last := st.Messages[len(st.Messages)-1]
		return len(st.Messages) == 2 && last.Extra != nil && last.Extra["__eino_automemory__"] != nil
	}, 2*time.Second, 10*time.Millisecond)

	require.Equal(t, toolInfos, st.ToolInfos)
	require.Equal(t, deferredToolInfos, st.DeferredToolInfos)
}

type panicModel struct{}

func (m *panicModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	panic("should not call model")
}

func (m *panicModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	panic("should not call model")
}

func (m *panicModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type toolCallSelectionModel struct {
	calls int32
}

func (m *toolCallSelectionModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.calls, 1)
	return schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "select-1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      topicSelectionToolName,
				Arguments: `{"selected_memories":["debugging.md","hallucinated.md"]}`,
			},
		},
	}), nil
}

func (m *toolCallSelectionModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *toolCallSelectionModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type extractionModel struct {
	mu               sync.Mutex
	promptSeen       []string
	boundToolCalls   [][]string
	topicPath        string
	indexPath        string
	blockFirstRun    chan struct{}
	firstRunStarted  chan struct{}
	blockedOnce      uint32 // atomic (0/1)
	generateCallings int32
}

type countingBackend struct {
	*InMemoryBackend
	writeCalls    int32
	globInfoCalls int32
	mu            sync.Mutex
	paths         []string
}

type outOfBoundsCandidateBackend struct {
	outsideReadCalled int32
}

func (b *outOfBoundsCandidateBackend) Read(_ context.Context, req *ReadRequest) (*FileContent, error) {
	if req == nil {
		return nil, fmt.Errorf("read: invalid request")
	}
	if filepath.Clean(req.FilePath) == filepath.Clean("/outside/secret.md") {
		atomic.StoreInt32(&b.outsideReadCalled, 1)
		return &FileContent{Content: "secret"}, nil
	}
	return nil, fmt.Errorf("file not found: %s", req.FilePath)
}

func (b *outOfBoundsCandidateBackend) GlobInfo(_ context.Context, req *GlobInfoRequest) ([]FileInfo, error) {
	if req == nil {
		return nil, fmt.Errorf("glob: invalid request")
	}
	return []FileInfo{{
		Path:       "/outside/secret.md",
		ModifiedAt: time.Now().Format(time.RFC3339Nano),
	}}, nil
}

func (b *outOfBoundsCandidateBackend) Write(context.Context, *WriteRequest) error {
	return nil
}

func (b *outOfBoundsCandidateBackend) Edit(context.Context, *EditRequest) error {
	return nil
}

func (b *countingBackend) Write(ctx context.Context, req *WriteRequest) error {
	atomic.AddInt32(&b.writeCalls, 1)
	b.mu.Lock()
	b.paths = append(b.paths, req.FilePath)
	b.mu.Unlock()
	return b.InMemoryBackend.Write(ctx, req)
}

func (b *countingBackend) GlobInfo(ctx context.Context, req *GlobInfoRequest) ([]FileInfo, error) {
	atomic.AddInt32(&b.globInfoCalls, 1)
	return b.InMemoryBackend.GlobInfo(ctx, req)
}

func (m *extractionModel) Generate(_ context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	atomic.AddInt32(&m.generateCallings, 1)
	promptIdx := findExtractionPromptIndex(input)
	if promptIdx < 0 {
		return nil, fmt.Errorf("missing extraction prompt")
	}

	m.mu.Lock()
	m.promptSeen = append(m.promptSeen, input[promptIdx].Content)
	m.mu.Unlock()

	if hasToolMessageAfter(input, promptIdx) {
		return schema.AssistantMessage("done", nil), nil
	}

	if m.blockFirstRun != nil && atomic.SwapUint32(&m.blockedOnce, 1) == 0 {
		if m.firstRunStarted != nil {
			close(m.firstRunStarted)
		}
		<-m.blockFirstRun
	}

	payload := lastBusinessUserBeforePrompt(input, promptIdx)
	topicPath := m.topicPath
	if topicPath == "" {
		topicPath = "topic.md"
	}
	indexPath := m.indexPath
	if indexPath == "" {
		indexPath = "MEMORY.md"
	}
	return schema.AssistantMessage("", []schema.ToolCall{
		{
			ID:   "write-topic",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "write_file",
				Arguments: fmt.Sprintf(`{"file_path":%q,"content":%q}`, topicPath, payload),
			},
		},
		{
			ID:   "write-index",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      "write_file",
				Arguments: fmt.Sprintf(`{"file_path":%q,"content":"- [topic.md](topic.md)\n"}`, indexPath),
			},
		},
	}), nil
}

func (m *extractionModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	msg, err := m.Generate(ctx, input)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{msg}), nil
}

func (m *extractionModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	names := make([]string, 0, len(tools))
	for _, ti := range tools {
		if ti == nil {
			continue
		}
		names = append(names, ti.Name)
	}
	m.mu.Lock()
	m.boundToolCalls = append(m.boundToolCalls, names)
	m.mu.Unlock()
	return m, nil
}

func findExtractionPromptIndex(input []*schema.Message) int {
	for i := len(input) - 1; i >= 0; i-- {
		if input[i] != nil && input[i].Role == schema.User &&
			(strings.Contains(input[i].Content, "memory extraction subagent") || strings.Contains(input[i].Content, "记忆提取子智能体")) {
			return i
		}
	}
	return -1
}

func hasToolMessageAfter(input []*schema.Message, idx int) bool {
	for i := idx + 1; i < len(input); i++ {
		if input[i] != nil && input[i].Role == schema.Tool {
			switch input[i].ToolName {
			case "read_file", "glob", "write_file", "edit_file":
				return true
			default:
			}
		}
	}
	return false
}

func lastBusinessUserBeforePrompt(input []*schema.Message, promptIdx int) string {
	for i := promptIdx - 1; i >= 0; i-- {
		if input[i] == nil || input[i].Role != schema.User {
			continue
		}
		if strings.Contains(input[i].Content, "<!-- automemory -->") {
			continue
		}
		return input[i].Content
	}
	return "unknown"
}

func TestMiddleware_TopicSelection_SmallCandidateSetUsesModel(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()

	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md)\n- [patterns.md](patterns.md)\n", now)
	b.put("/mem/debugging.md", "---\ndescription: debug notes\n---\nbody\n", now)
	b.put("/mem/patterns.md", "---\ndescription: patterns\n---\nbody\n", now)
	model := &toolCallSelectionModel{}

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           model,
		Read: &ReadConfig[*schema.Message]{
			Mode: ReadModeSync,
			TopicSelection: &TopicSelectionConfig{
				TopK: 5,
			},
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to run tests?")}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Equal(t, int32(1), atomic.LoadInt32(&model.calls))
	require.Len(t, out.AgentInput.Messages, 3)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	requireTopicMemoryMessage(t, out.AgentInput.Messages[1], "debugging.md")
	require.NotContains(t, out.AgentInput.Messages[1].Content, "patterns.md")
	require.Contains(t, out.AgentInput.Messages[2].Content, "How to run tests?")
}

func TestMiddleware_TopicSelection_DisabledSkipsSelectionAndReminder(t *testing.T) {
	for _, mode := range []ReadMode{ReadModeSync, ReadModeAsync} {
		t.Run(string(mode), func(t *testing.T) {
			ctx := context.Background()
			b := &countingBackend{InMemoryBackend: NewInMemoryBackend()}
			now := time.Now()
			b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md)\n", now)
			b.put("/mem/debugging.md", "---\ndescription: debug notes\n---\nbody\n", now)

			selModel := &toolCallSelectionModel{}
			mw, err := New(ctx, &Config[*schema.Message]{
				MemoryDirectory: "/mem",
				MemoryBackend:   b,
				Model:           selModel,
				Read: &ReadConfig[*schema.Message]{
					Mode: mode,
					TopicSelection: &TopicSelectionConfig{
						Enable: boolPtr(false),
						TopK:   1,
					},
				},
			})
			require.NoError(t, err)

			runCtx := &adk.ChatModelAgentContext[*schema.Message]{
				Instruction: "base",
				AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to debug?")}},
			}
			ctx2, out, err := mw.BeforeAgent(ctx, runCtx)
			require.NoError(t, err)
			require.Len(t, out.AgentInput.Messages, 2)
			requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
			require.Contains(t, out.AgentInput.Messages[1].Content, "How to debug?")
			require.Equal(t, 0, countTopicMemoryMessages(out.AgentInput.Messages))
			require.EqualValues(t, 0, atomic.LoadInt32(&selModel.calls))
			require.EqualValues(t, 0, atomic.LoadInt32(&b.globInfoCalls))

			if mode == ReadModeAsync {
				st := &adk.ChatModelAgentState{Messages: []adk.Message{schema.UserMessage("How to debug?")}}
				_, next, err := mw.BeforeModelRewriteState(ctx2, st, nil)
				require.NoError(t, err)
				require.Len(t, next.Messages, 1)
				require.Equal(t, 0, countTopicMemoryMessages(next.Messages))
				require.EqualValues(t, 0, atomic.LoadInt32(&selModel.calls))
				require.EqualValues(t, 0, atomic.LoadInt32(&b.globInfoCalls))
			}
		})
	}
}

func TestMiddleware_AfterAgent_SyncExtractionWritesMemoryFiles(t *testing.T) {
	ctx := context.Background()
	b := &countingBackend{InMemoryBackend: NewInMemoryBackend()}
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{}
	var onErrStages []ErrorStage
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
		},
		OnError: func(ctx context.Context, stage ErrorStage, err error) {
			onErrStages = append(onErrStages, stage)
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember alpha"),
			schema.AssistantMessage("ack", nil),
		},
	}

	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: state.Messages,
		ToolInfos: []*schema.ToolInfo{
			{Name: "tool_b"},
			{Name: "tool_a"},
		},
	})
	require.NoError(t, err)
	require.Empty(t, onErrStages)
	require.Equal(t, len(state.Messages), getWriteCursorFromMessages(state.Messages))
	require.GreaterOrEqual(t, atomic.LoadInt32(&extModel.generateCallings), int32(1))
	require.GreaterOrEqual(t, atomic.LoadInt32(&b.writeCalls), int32(1))
	b.mu.Lock()
	paths := append([]string(nil), b.paths...)
	b.mu.Unlock()
	require.NotEmpty(t, paths)
	require.Contains(t, paths, "/mem/topic.md")
	require.Contains(t, paths, "/mem/MEMORY.md")

	mem, err := b.Read(ctx, &ReadRequest{FilePath: "/mem/MEMORY.md"})
	require.NoError(t, err)
	require.Contains(t, mem.Content, "topic.md")

	topic, err := b.Read(ctx, &ReadRequest{FilePath: "/mem/topic.md"})
	require.NoError(t, err)
	require.Equal(t, "remember alpha", topic.Content)

	extModel.mu.Lock()
	defer extModel.mu.Unlock()
	require.NotEmpty(t, extModel.promptSeen)
	require.Contains(t, extModel.promptSeen[0], "memory extraction subagent")
	require.Contains(t, extModel.promptSeen[0], "## Memory directory")
	require.Contains(t, extModel.promptSeen[0], "Path: /mem")
}

func TestMiddleware_AfterAgent_SyncExtraction_CustomWriteInstruction(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
			GenInstruction: func(ctx context.Context) (string, error) {
				return "## Custom save policy\n- Save only explicitly requested memories\n- Prefer updating user_preferences.md for user preference changes\n- Do not save temporary debugging notes", nil
			},
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember beta"),
			schema.AssistantMessage("ack", nil),
		},
	}
	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{Messages: state.Messages})
	require.NoError(t, err)

	extModel.mu.Lock()
	defer extModel.mu.Unlock()
	require.NotEmpty(t, extModel.promptSeen)
	prompt := extModel.promptSeen[0]
	require.Contains(t, prompt, "## Custom save policy")
	require.Contains(t, prompt, "- Save only explicitly requested memories")
	require.Contains(t, prompt, "- Prefer updating user_preferences.md for user preference changes")
	require.Contains(t, prompt, "- Do not save temporary debugging notes")
	require.NotContains(t, prompt, "## What to save")
	require.NotContains(t, prompt, "- Stable patterns and conventions confirmed across multiple interactions")
	require.NotContains(t, prompt, "## What NOT to save")
	require.NotContains(t, prompt, "- Session-specific temporary state or current task details")
	require.Contains(t, prompt, "## How to save memories")
}

func TestMiddleware_AfterAgent_SyncExtractionWritesMemoryDirectory(t *testing.T) {
	ctx := context.Background()
	b := &countingBackend{InMemoryBackend: NewInMemoryBackend()}
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{
		topicPath: "topic.md",
		indexPath: "MEMORY.md",
	}
	var onErrStages []ErrorStage
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
		},
		OnError: func(ctx context.Context, stage ErrorStage, err error) {
			onErrStages = append(onErrStages, stage)
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember project convention"),
			schema.AssistantMessage("ack", nil),
		},
	}

	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: state.Messages,
	})
	require.NoError(t, err)
	require.Empty(t, onErrStages)

	topic, err := b.Read(ctx, &ReadRequest{FilePath: "/mem/topic.md"})
	require.NoError(t, err)
	require.Equal(t, "remember project convention", topic.Content)

	b.mu.Lock()
	paths := append([]string(nil), b.paths...)
	b.mu.Unlock()
	require.Contains(t, paths, "/mem/topic.md")
	require.Contains(t, paths, "/mem/MEMORY.md")
}

func TestMiddleware_AfterAgent_SyncExtraction_IteratorHandlerCanDrain(t *testing.T) {
	ctx := context.Background()
	b := &countingBackend{InMemoryBackend: NewInMemoryBackend()}
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{}
	var seen int32
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
			HandleExtractionIterator: func(ctx context.Context, iter *adk.AsyncIterator[*adk.AgentEvent]) error {
				for {
					ev, ok := iter.Next()
					if !ok {
						return nil
					}
					if ev == nil {
						continue
					}
					atomic.AddInt32(&seen, 1)
					if ev.Err != nil {
						return ev.Err
					}
				}
			},
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember handler"),
			schema.AssistantMessage("ack", nil),
		},
	}

	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: state.Messages,
		ToolInfos: []*schema.ToolInfo{
			{Name: "tool_1"},
		},
	})
	require.NoError(t, err)
	require.Greater(t, atomic.LoadInt32(&seen), int32(0))

	// Still writes memory files as usual (handler only changes event draining).
	_, err = b.Read(ctx, &ReadRequest{FilePath: "/mem/topic.md"})
	require.NoError(t, err)
}

func TestMiddleware_AfterAgent_SkipsExtractionWhenMainAgentAlreadyWroteMemory(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember beta"),
			schema.AssistantMessage("", []schema.ToolCall{
				{
					ID:   "call-1",
					Type: "function",
					Function: schema.FunctionCall{
						Name:      "write_file",
						Arguments: `{"file_path":"/mem/topic.md","content":"written by main agent"}`,
					},
				},
			}),
			schema.ToolMessage("ok", "call-1", schema.WithToolName("write_file")),
		},
	}

	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{Messages: state.Messages})
	require.NoError(t, err)
	require.Equal(t, len(state.Messages), getWriteCursorFromMessages(state.Messages))
	require.EqualValues(t, 0, atomic.LoadInt32(&extModel.generateCallings))

	_, err = b.Read(ctx, &ReadRequest{FilePath: "/mem/topic.md"})
	require.Error(t, err)
}

func TestMiddleware_AfterAgent_AsyncExtractionKeepsLatestPendingSnapshot(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	blockCh := make(chan struct{})
	startedCh := make(chan struct{})
	extModel := &extractionModel{
		blockFirstRun:   blockCh,
		firstRunStarted: startedCh,
	}
	coord := &CoordinationConfig[*schema.Message]{
		SessionID:   "session-1",
		Coordinator: NewLocalCoordinator(),
		LockTTL:     time.Minute,
	}

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeAsync,
			Model: extModel,
		},
		Coordination: coord,
	})
	require.NoError(t, err)

	state1 := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember one"),
			schema.AssistantMessage("ack1", nil),
		},
	}
	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: state1.Messages,
		ToolInfos: []*schema.ToolInfo{
			{Name: "tool_one"},
		},
	})
	require.NoError(t, err)

	<-startedCh

	state2 := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember one"),
			schema.AssistantMessage("ack1", nil),
			schema.UserMessage("remember two"),
			schema.AssistantMessage("ack2", nil),
		},
	}
	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: state2.Messages,
		ToolInfos: []*schema.ToolInfo{
			{Name: "tool_one"},
			{Name: "tool_two"},
		},
	})
	require.NoError(t, err)

	close(blockCh)

	require.Eventually(t, func() bool {
		topic, readErr := b.Read(ctx, &ReadRequest{FilePath: "/mem/topic.md"})
		if readErr != nil || topic == nil || topic.Content != "remember two" {
			return false
		}
		cursor, ok, cursorErr := getCoordinatorCursor(ctx, coord.Coordinator, "/mem::session-1")
		if cursorErr != nil || !ok {
			return false
		}
		return cursor == len(state2.Messages)
	}, 2*time.Second, 10*time.Millisecond)
}

func TestMiddleware_BeforeAgent_GenInstructionRendersAndIndexInjectedOnce(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "line1\nline2\n", now)
	var instructionCalls int32

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		GenInstruction: func(ctx context.Context) (string, error) {
			atomic.AddInt32(&instructionCalls, 1)
			return "custom memory policy", nil
		},
		// No topic selection model.
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	}

	_, out1, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out1.Instruction, "custom memory policy")
	require.EqualValues(t, 1, atomic.LoadInt32(&instructionCalls))
	require.Equal(t, 1, countMemoryIndexMessages(out1.AgentInput.Messages))

	// Same turn with already-injected index reminder should not duplicate the reminder.
	_, out2, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: out1.Instruction,
		AgentInput:  &adk.AgentInput{Messages: out1.AgentInput.Messages},
	})
	require.NoError(t, err)
	require.Contains(t, out2.Instruction, "custom memory policy")
	require.EqualValues(t, 2, atomic.LoadInt32(&instructionCalls))
	require.Equal(t, 1, countMemoryIndexMessages(out2.AgentInput.Messages))

	// A later business user message in the same session should not get another MEMORY.md reminder.
	nextMessages := append([]*schema.Message{}, out2.AgentInput.Messages...)
	nextMessages = append(nextMessages, schema.AssistantMessage("ack", nil), schema.UserMessage("next turn"))
	_, out3, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: out2.Instruction,
		AgentInput:  &adk.AgentInput{Messages: nextMessages},
	})
	require.NoError(t, err)
	require.Contains(t, out3.Instruction, "custom memory policy")
	require.EqualValues(t, 3, atomic.LoadInt32(&instructionCalls))
	require.Equal(t, 1, countMemoryIndexMessages(out3.AgentInput.Messages))
}

func TestMiddleware_BeforeAgent_TopicMemoryInjectedOncePerSession(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md)\n", now)
	b.put("/mem/debugging.md", "---\ndescription: debug notes\n---\nbody\n", now)

	selModel := &toolCallSelectionModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           selModel,
		Read: &ReadConfig[*schema.Message]{
			Mode: ReadModeSync,
			TopicSelection: &TopicSelectionConfig{
				TopK: 1,
			},
		},
	})
	require.NoError(t, err)

	_, out1, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to debug?")}},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&selModel.calls))
	require.Equal(t, 1, countMemoryIndexMessages(out1.AgentInput.Messages))
	require.Equal(t, 1, countTopicMemoryMessages(out1.AgentInput.Messages))

	nextMessages := append([]*schema.Message{}, out1.AgentInput.Messages...)
	nextMessages = append(nextMessages, schema.AssistantMessage("ack", nil), schema.UserMessage("How to debug again?"))
	_, out2, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: out1.Instruction,
		AgentInput:  &adk.AgentInput{Messages: nextMessages},
	})
	require.NoError(t, err)
	require.EqualValues(t, 1, atomic.LoadInt32(&selModel.calls))
	require.Equal(t, 1, countMemoryIndexMessages(out2.AgentInput.Messages))
	require.Equal(t, 1, countTopicMemoryMessages(out2.AgentInput.Messages))
}

func TestMiddleware_LastUserMessageSkipsSystemReminderPrefix(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":[]}`},
	})
	require.NoError(t, err)

	last, ok := mw.(*middleware[*schema.Message]).lastUserMessage(&adk.AgentInput{
		Messages: []adk.Message{
			schema.UserMessage("real user query"),
			schema.UserMessage("<system-reminder>\nInjected by another middleware.\n</system-reminder>"),
		},
	})
	require.True(t, ok)
	require.Equal(t, "real user query", last.Content)
}

func TestMiddleware_BeforeAgent_InjectsInstructionWhenMessagesAlreadyContainMemory(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
	})
	require.NoError(t, err)

	memMsg := newMemoryMessage[*schema.Message]("<!-- automemory -->\n<system-reminder>\n<topic-memory-1>\nContents of /mem/preloaded.md (saved now):\npreloaded\n</topic-memory-1>\n</system-reminder>")
	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi"), memMsg}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out.Instruction, "# Auto memory")
	require.Len(t, out.AgentInput.Messages, 3)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	require.Contains(t, out.AgentInput.Messages[1].Content, "hi")
	requireTopicMemoryMessage(t, out.AgentInput.Messages[2], "preloaded")
}

func TestMiddleware_BeforeAgent_DistributedCursorSyncIntoMessageExtra(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	coord := &CoordinationConfig[*schema.Message]{
		SessionID:   "sess-cursor",
		Coordinator: NewLocalCoordinator(),
		LockTTL:     time.Minute,
	}
	require.NoError(t, setCoordinatorCursor(ctx, coord.Coordinator, "/mem::sess-cursor", 5))

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Coordination:    coord,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput: &adk.AgentInput{Messages: []adk.Message{
			schema.UserMessage("hi"),
			schema.AssistantMessage("ack", nil),
		}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	requireWriteCursor(t, out.AgentInput.Messages, 5)
}

func TestMiddleware_BeforeAgent_WriteCursorDoesNotBlockInstructionInjection(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "remembered\n", now)

	coord := &CoordinationConfig[*schema.Message]{
		SessionID:   "sess-cursor",
		Coordinator: NewLocalCoordinator(),
		LockTTL:     time.Minute,
	}
	require.NoError(t, setCoordinatorCursor(ctx, coord.Coordinator, "/mem::sess-cursor", 5))

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Coordination:    coord,
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput: &adk.AgentInput{Messages: []adk.Message{
			schema.AssistantMessage("ack", nil),
			schema.UserMessage("next turn"),
		}},
	}

	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Contains(t, out.Instruction, "# Auto memory")
	require.NotContains(t, out.Instruction, "remembered")
	requireMemoryIndexMessage(t, out.AgentInput.Messages[1], "remembered")

	requireWriteCursor(t, out.AgentInput.Messages, 5)
}

func TestMiddleware_TopicSelection_ToolCallParsingAndFiltering(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md)\n", now)
	b.put("/mem/debugging.md", "---\ndescription: debug notes\n---\nbody\n", now)
	b.put("/mem/other.md", "---\ndescription: other\n---\nbody\n", now.Add(-time.Hour))

	selModel := &toolCallSelectionModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           selModel,
		Read: &ReadConfig[*schema.Message]{
			Mode: ReadModeSync,
			TopicSelection: &TopicSelectionConfig{
				TopK: 1,
			},
		},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("How to debug?")}},
	}
	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Len(t, out.AgentInput.Messages, 3)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	mem := out.AgentInput.Messages[1]
	require.Contains(t, mem.Content, "Contents of /mem/debugging.md")
	require.NotContains(t, mem.Content, "hallucinated.md")
	require.Contains(t, out.AgentInput.Messages[2].Content, "How to debug?")
	require.EqualValues(t, 1, atomic.LoadInt32(&selModel.calls))
}

func TestMiddleware_TopicSelection_AsyncProtectsMemoryMessageFromMutation(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "- [debugging.md](debugging.md)\n", now)
	b.put("/mem/debugging.md", "---\ndescription: debug notes\n---\nbody\n", now)

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Model:           &fixedModel{out: `{"selected_memories":["debugging.md"]}`},
		Read:            &ReadConfig[*schema.Message]{Mode: ReadModeAsync},
	})
	require.NoError(t, err)

	ctx2, _, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	})
	require.NoError(t, err)

	st := &adk.ChatModelAgentState{Messages: []adk.Message{schema.UserMessage("hi")}}

	var expected string
	require.Eventually(t, func() bool {
		_, next, callErr := mw.BeforeModelRewriteState(ctx2, st, nil)
		require.NoError(t, callErr)
		st = next
		if len(st.Messages) < 2 {
			return false
		}
		expected = st.Messages[len(st.Messages)-1].Content
		return strings.Contains(expected, "<!-- automemory -->")
	}, 2*time.Second, 10*time.Millisecond)

	// Mutate the memory message content.
	st.Messages[len(st.Messages)-1].Content = "tampered"
	_, next, err := mw.BeforeModelRewriteState(ctx2, st, nil)
	require.NoError(t, err)
	require.Equal(t, expected, next.Messages[len(next.Messages)-1].Content)
	require.NotNil(t, next.Messages[len(next.Messages)-1].Extra[memoryExtraKey])
}

func TestMiddleware_AfterAgent_SyncExtraction_ChinesePrompt(t *testing.T) {
	require.NoError(t, adk.SetLanguage(adk.LanguageChinese))
	defer func() {
		require.NoError(t, adk.SetLanguage(adk.LanguageEnglish))
	}()

	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
		},
	})
	require.NoError(t, err)

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember chinese"),
			schema.AssistantMessage("ack", nil),
		},
	}
	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{Messages: state.Messages})
	require.NoError(t, err)

	extModel.mu.Lock()
	defer extModel.mu.Unlock()
	require.NotEmpty(t, extModel.promptSeen)
	require.Contains(t, extModel.promptSeen[0], "你现在扮演记忆提取子智能体")
	require.Contains(t, extModel.promptSeen[0], "## 记忆目录")
	require.Contains(t, extModel.promptSeen[0], "路径：/mem")
}

func TestMiddleware_AfterAgent_RelativeMemoryDirRendersAbsolutePath(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmp))
	defer func() {
		_ = os.Chdir(oldwd)
	}()

	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte(""), 0o644))
	expectedDir, err := filepath.Abs(".")
	require.NoError(t, err)

	extModel := &extractionModel{}
	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: ".",
		MemoryBackend:   NewLocalBackend(),
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeSync,
			Model: extModel,
		},
	})
	require.NoError(t, err)

	state := &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: []adk.Message{
			schema.UserMessage("remember relative"),
			schema.AssistantMessage("ack", nil),
		},
	}
	_, err = mw.AfterAgent(ctx, state)
	require.NoError(t, err)

	extModel.mu.Lock()
	require.NotEmpty(t, extModel.promptSeen)
	require.Contains(t, extModel.promptSeen[0], "Path: "+expectedDir)
	extModel.mu.Unlock()

	raw, err := os.ReadFile(filepath.Join(expectedDir, "topic.md"))
	require.NoError(t, err)
	require.Equal(t, "remember relative", string(raw))
}

func TestMiddleware_BeforeAgent_RelativeMemoryDirReadsResolvedDirectoryAfterCWDChange(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	oldwd, err := os.Getwd()
	require.NoError(t, err)
	require.NoError(t, os.Chdir(tmp))
	defer func() {
		_ = os.Chdir(oldwd)
	}()

	require.NoError(t, os.WriteFile(filepath.Join(tmp, "MEMORY.md"), []byte("persisted index\n"), 0o644))

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: ".",
		MemoryBackend:   NewLocalBackend(),
	})
	require.NoError(t, err)

	other := t.TempDir()
	require.NoError(t, os.Chdir(other))

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}},
	}
	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.NotContains(t, out.Instruction, "persisted index")
	require.Len(t, out.AgentInput.Messages, 2)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "persisted index")
	require.Contains(t, out.AgentInput.Messages[1].Content, "hi")
}

func TestFSBackend_ReadMissingFileReturnsContentInsteadOfError(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()

	fs, err := newFSBackend(NewLocalBackend(), tmp)
	require.NoError(t, err)

	content, err := fs.Read(ctx, &ReadRequest{FilePath: "missing.md"})
	require.NoError(t, err)
	require.NotNil(t, content)
	require.Contains(t, content.Content, "File not found:")
	require.Contains(t, content.Content, filepath.Join(tmp, "missing.md"))
}

func TestMiddleware_TopicSelection_IgnoresOutOfBoundsCandidatePaths(t *testing.T) {
	ctx := context.Background()
	backend := &outOfBoundsCandidateBackend{}

	mw, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   backend,
		Model:           &panicModel{},
	})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
		Instruction: "base",
		AgentInput:  &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("show memories")}},
	}
	_, out, err := mw.BeforeAgent(ctx, runCtx)
	require.NoError(t, err)
	require.Len(t, out.AgentInput.Messages, 2)
	requireMemoryIndexMessage(t, out.AgentInput.Messages[0], "Contents of /mem/MEMORY.md")
	require.Contains(t, out.AgentInput.Messages[1].Content, "show memories")
	require.Equal(t, int32(0), atomic.LoadInt32(&backend.outsideReadCalled))
}

func TestMiddleware_AfterAgent_AsyncSetsPendingSnapshotWhenLockHeld(t *testing.T) {
	ctx := context.Background()
	b := NewInMemoryBackend()
	now := time.Now()
	b.put("/mem/MEMORY.md", "", now)

	extModel := &extractionModel{}
	coord := &CoordinationConfig[*schema.Message]{
		SessionID:   "sess-pending",
		Coordinator: NewLocalCoordinator(),
		LockTTL:     time.Minute,
	}
	// Hold the lock.
	coordKey := "/mem::sess-pending"
	unlock, ok, err := coord.Coordinator.AcquireLock(ctx, coordKey, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)

	mwI, err := New(ctx, &Config[*schema.Message]{
		MemoryDirectory: "/mem",
		MemoryBackend:   b,
		Write: &WriteConfig[*schema.Message]{
			Mode:  WriteModeAsync,
			Model: extModel,
		},
		Coordination: coord,
	})
	require.NoError(t, err)
	mw := mwI.(*middleware[*schema.Message])

	state := &adk.ChatModelAgentState{
		Messages: []adk.Message{
			schema.UserMessage("remember pending"),
			schema.AssistantMessage("ack", nil),
		},
	}
	_, err = mw.AfterAgent(ctx, &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: state.Messages,
		ToolInfos: []*schema.ToolInfo{
			{Name: "pending_tool"},
		},
	})
	require.NoError(t, err)

	pending, err := popCoordinatorPendingSnapshot(ctx, coord.Coordinator, coordKey)
	require.NoError(t, err)
	require.NotNil(t, pending)

	// Release and drain manually to complete write synchronously in test.
	require.NoError(t, unlock(ctx))
	unlock2, ok, err := coord.Coordinator.AcquireLock(ctx, coordKey, time.Minute)
	require.NoError(t, err)
	require.True(t, ok)
	mw.runExtractionDrain(ctx, coordKey, unlock2, pending)

	topic, err := b.Read(ctx, &ReadRequest{FilePath: "/mem/topic.md"})
	require.NoError(t, err)
	require.Equal(t, "remember pending", topic.Content)
}
