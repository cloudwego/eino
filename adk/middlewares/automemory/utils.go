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
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/cloudwego/eino/adk"
	adkfs "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

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
	if cfg.Read.TopicSelection.Enable == nil {
		cfg.Read.TopicSelection.Enable = boolPtr(true)
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

func boolPtr(v bool) *bool {
	return &v
}

func parseFrontmatter(md string) (fm topicFrontmatter, ok bool) {
	s := strings.TrimLeft(md, "\ufeff \t\r\n")
	if !strings.HasPrefix(s, "---\n") && !strings.HasPrefix(s, "---\r\n") {
		return topicFrontmatter{}, false
	}
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

func hasTopicMemoryInjected[M adk.MessageType](msgs []M) bool {
	for _, msg := range msgs {
		if isTopicMemoryMessage(msg) {
			return true
		}
	}
	return false
}

func hasMemoryIndexInjected[M adk.MessageType](msgs []M) bool {
	for _, msg := range msgs {
		if isMemoryIndexMessage(msg) {
			return true
		}
	}
	return false
}

func insertMessagesBeforeLastUserQuery[M adk.MessageType](msgs []M, inserts []M) []M {
	if len(inserts) == 0 {
		return msgs
	}
	idx := lastUserQueryMessageIndex(msgs)
	if idx < 0 {
		idx = len(msgs)
	}
	out := make([]M, 0, len(msgs)+len(inserts))
	out = append(out, msgs[:idx]...)
	out = append(out, inserts...)
	out = append(out, msgs[idx:]...)
	return out
}

func lastUserQueryMessageIndex[M adk.MessageType](msgs []M) int {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if isNilMessage(msg) || !isUserRole(msg) || isAutomemoryReminderMessage(msg) {
			continue
		}
		return i
	}
	return -1
}

func isAutomemoryReminderMessage[M adk.MessageType](m M) bool {
	if isTopicMemoryMessage(m) || isMemoryIndexMessage(m) {
		return true
	}
	if isNilMessage(m) || !isUserRole(m) {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(userMessageTextContent(m)), "<system-reminder>")
}

func isTopicMemoryMessage[M adk.MessageType](m M) bool {
	if isNilMessage(m) || !isUserRole(m) {
		return false
	}
	if extra := getMsgExtra(m); extra != nil {
		if v, ok := extra[memoryExtraKey]; ok {
			if isTopicMemoryExtra(v) {
				return true
			}
		}
	}
	content := userMessageTextContent(m)
	return strings.Contains(content, "<!-- automemory -->") && !strings.Contains(content, "<!-- automemory:index -->")
}

func isMemoryIndexMessage[M adk.MessageType](m M) bool {
	if isNilMessage(m) || !isUserRole(m) {
		return false
	}
	if extra := getMsgExtra(m); extra != nil {
		if v, ok := extra[memoryExtraKey]; ok {
			if isMemoryIndexExtra(v) {
				return true
			}
		}
	}
	return strings.Contains(userMessageTextContent(m), "<!-- automemory:index -->")
}

func isTopicMemoryExtra(v any) bool {
	switch meta := v.(type) {
	case *memoryExtra:
		return meta != nil && (meta.Type == "memory" || meta.Type == "topic_memory")
	case map[string]any:
		typ, _ := meta["type"].(string)
		return typ == "memory" || typ == "topic_memory"
	default:
		return false
	}
}

func isMemoryIndexExtra(v any) bool {
	switch meta := v.(type) {
	case *memoryExtra:
		return meta != nil && meta.Type == "memory_index"
	case map[string]any:
		typ, _ := meta["type"].(string)
		return typ == "memory_index"
	default:
		return false
	}
}

func newMemoryMessage[M adk.MessageType](content string) M {
	msg := makeUserMsg[M](content)
	copyAndSetMsgExtra(msg, memoryExtraKey, &memoryExtra{Type: "memory"})
	return msg
}

func newMemoryIndexMessage[M adk.MessageType](content string) M {
	msg := makeUserMsg[M](content)
	copyAndSetMsgExtra(msg, memoryExtraKey, &memoryExtra{Type: "memory_index"})
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
		if !isTopicMemoryMessage(m) {
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
	if v, ok := m["filePath"]; ok {
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

func hasMemoryWritesSince[M adk.MessageType](msgs []M, cursor int, memoryDirectory string) bool {
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
			if fp, ok := extractFilePath(tc.Function.Arguments); ok && isPathWithinMemoryDir(memoryDirectory, fp) {
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

func (m *middleware[M]) coordinatorKey(sessionID string) string {
	if sessionID == "" || m == nil || m.resolvedMemoryDirectory == "" {
		return ""
	}
	return m.resolvedMemoryDirectory + "::" + sessionID
}

func (m *middleware[M]) topicSelectionEnabled() bool {
	return m != nil && m.cfg != nil && m.cfg.Read != nil &&
		topicSelectionConfigEnabled(m.cfg.Read.TopicSelection) && m.topicSelectionModel != nil
}

func topicSelectionConfigEnabled(cfg *TopicSelectionConfig) bool {
	return cfg != nil && cfg.Enable != nil && *cfg.Enable
}

func (m *middleware[M]) onErr(ctx context.Context, stage ErrorStage, err error) {
	if err == nil {
		return
	}
	if m.cfg != nil && m.cfg.OnError != nil {
		m.cfg.OnError(ctx, stage, err)
	}
}

func (m *middleware[M]) lastUserMessage(agentIn *adk.TypedAgentInput[M]) (M, bool) {
	if agentIn == nil || len(agentIn.Messages) == 0 {
		return nil, false
	}
	if !m.topicSelectionEnabled() {
		return nil, false
	}
	for i := len(agentIn.Messages) - 1; i >= 0; i-- {
		msg := agentIn.Messages[i]
		if isNilMessage(msg) || !isUserRole(msg) || isAutomemoryReminderMessage(msg) {
			continue
		}
		return msg, true
	}
	return nil, false
}

func (m *middleware[M]) topicSelectionTopK() int {
	topK := m.cfg.Read.TopicSelection.TopK
	if topK <= 0 {
		return defaultTopicTopK
	}
	return topK
}

func (m *middleware[M]) resolveSessionID(ctx context.Context, state *adk.TypedChatModelAgentState[M]) (string, error) {
	if m.coordination != nil {
		return strings.TrimSpace(m.coordination.SessionID), nil
	}
	return "", nil
}

func (m *middleware[M]) sendTopicMemoryEvent(ctx context.Context, msgs []M, memMsg M) {
	var beforeID string
	if len(msgs) > 0 && !isNilMessage(msgs[len(msgs)-1]) {
		beforeID = adk.GetMessageID(msgs[len(msgs)-1])
	}
	if sendEventErr := adk.TypedSendEvent(ctx, &adk.TypedAgentEvent[M]{
		SessionEventVariant: &adk.SessionEventVariant[M]{
			Event: &adk.SessionEvent[M]{
				Kind: adk.SessionEventMessageInserted,
				MessageInserted: &adk.MessageInsertedEvent[M]{
					Message:         memMsg,
					BeforeMessageID: beforeID,
				},
			},
		},
	}); sendEventErr != nil {
		m.onErr(ctx, OnErrorStageSendSessionEvent, sendEventErr)
	}
}
