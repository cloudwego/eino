package automemory

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type TypedConfig struct {
	ApplyType ApplyType
}

type ApplyType int

const (
	ApplyTypeDirect ApplyType = 1

	// ApplyTypeAsync ApplyType = 2

	ApplyTypeCustomized ApplyType = 100
)

// DirectConfig agent 直接读写 auto memory
// 写：使用 write_file / edit_file 工具（本 mw 不注册，但如果当前 tools 中不包含将报错）
// 读：memory 内容在 agent 运行前自动加入 system + user prompt
type DirectConfig struct {
	MemoryDirectory string

	MemoryBackend Backend

	// Model topic memories selection
	Model model.BaseChatModel

	// CustomizeMemoryInjection customize agent instruction and input
	CustomizeMemoryInjection MemoryInjectionFunction
}

// AsyncPostProcessConfig agent 运行后异步写入 auto memory
// 写：agent 运行后异步流程写入，不阻塞主流程
// 读：memory 内容在 agent 运行前自动加入 system + user prompt

// Async

type Config struct {
	MemoryDirectory string

	MemoryBackend Backend

	// Model topic memories selection
	Model model.BaseChatModel

	// CustomizeMemoryInjection customize agent instruction and input
	CustomizeMemoryInjection MemoryInjectionFunction
}

type MemoryInjectionFunction func(ctx context.Context, input *MemoryInjectionInput) (*MemoryInjectionResult, error)

type MemoryInjectionInput struct {
	Instruction   string
	AgentInput    *adk.AgentInput
	IndexMemory   string
	TopicMemoires []FileInfo
}

type MemoryInjectionResult struct {
	Instruction string
	AgentInput  *adk.AgentInput
}

type middleware struct {
	adk.BaseChatModelAgentMiddleware

	memoryDirectory string

	memoryBackend Backend

	memoryInjectionFn MemoryInjectionFunction
}

func New(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	if config == nil || config.MemoryDirectory == "" || config.MemoryBackend == nil {
		return nil, fmt.Errorf("auto memory config: invalid")
	}
	if config.CustomizeMemoryInjection == nil && config.Model == nil {
		return nil, fmt.Errorf("auto memory config: provide at least one of Model or CustomizeMemoryInjection")
	}
	fn := config.CustomizeMemoryInjection
	if fn == nil {
		var err error
		fn, err = buildDefaultMemoryInjection(ctx, config.MemoryDirectory, config.Model, config.MemoryBackend)
		if err != nil {
			return nil, err
		}
	}
	return &middleware{
		memoryDirectory:   config.MemoryDirectory,
		memoryBackend:     config.MemoryBackend,
		memoryInjectionFn: fn,
	}, nil
}

func (m middleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	memoryFilesInfo, err := m.memoryBackend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: memorySearchPattern,
		Path:    m.memoryDirectory,
	})
	if err != nil {
		return nil, nil, err
	}

	memInjectionInput := &MemoryInjectionInput{
		Instruction:   runCtx.Instruction,
		AgentInput:    runCtx.AgentInput,
		IndexMemory:   "",
		TopicMemoires: memoryFilesInfo,
	}

	for i := len(memoryFilesInfo) - 1; i >= 0; i-- {
		path := memoryFilesInfo[i].Path

		if dir, file := filepath.Split(path); dir == m.memoryDirectory && file == memoryIndexFileName {
			fileContent, err := m.memoryBackend.Read(ctx, &ReadRequest{
				FilePath: path,
				Offset:   0,
				Limit:    200,
			})
			if err != nil {
				return nil, nil, err
			}
			memInjectionInput.IndexMemory = fileContent.Content
			memoryFilesInfo = append(memoryFilesInfo[:i], memoryFilesInfo[i+1:]...)
			break
		}
	}

	result, err := m.memoryInjectionFn(ctx, memInjectionInput)
	if err != nil {
		return nil, nil, err
	}

	runCtx.Instruction = result.Instruction
	runCtx.AgentInput = result.AgentInput

	return ctx, runCtx, nil
}

func buildDefaultMemoryInjection(ctx context.Context, memDir string, model model.BaseChatModel, backend Backend) (MemoryInjectionFunction, error) {
	memDesc, err := pyfmt.Fmt(defaultMemoryInstruction, map[string]any{"memory_dir": memDir})
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context, input *MemoryInjectionInput) (result *MemoryInjectionResult, err error) {
		indexMemAppendix := make([]string, 0, 3)
		indexMemAppendix = append(indexMemAppendix, memDesc)
		if input.IndexMemory == "" {
			indexMemAppendix = append(indexMemAppendix, defaultAppendEmptyIndexTemplate)
		} else {
			lines := strings.Split(input.IndexMemory, "\n")
			if len(lines) > 200 {
				indexMemAppendix = append(indexMemAppendix, strings.Join(lines[:200], "\n"))
				indexMemAppendix = append(indexMemAppendix, defaultAppendCurrentIndexTruncNotify)
			} else {
				indexMemAppendix = append(indexMemAppendix, input.IndexMemory)
			}
		}

		result = &MemoryInjectionResult{
			Instruction: input.Instruction + "\n" + strings.Join(indexMemAppendix, "\n"),
			AgentInput:  nil,
		}

		result.AgentInput, err = defaultTopicSelection(ctx, model, backend, input)
		if err != nil {
			return nil, err
		}

		return result, nil
	}, nil
}

type topicSelectionResp struct {
	SelectedMemories []string `json:"selected_memories"`
}

func defaultTopicSelection(ctx context.Context, model model.BaseChatModel, backend Backend, input *MemoryInjectionInput) (
	*adk.AgentInput, error) {

	if input.AgentInput == nil || len(input.AgentInput.Messages) == 0 {
		// resume, skip
		return nil, nil
	}

	if len(input.TopicMemoires) == 0 {
		// no topic memories, do nothing
		return input.AgentInput, nil
	}

	inputLength := len(input.AgentInput.Messages)
	userQuery := input.AgentInput.Messages[inputLength-1]
	if userQuery.Role != schema.User {
		return nil, fmt.Errorf("unexpected agent input for auto memory, last message role=%s", userQuery.Role)
	}

	type bundle struct {
		FileInfo FileInfo
		Content  string
	}
	topicMemoires := input.TopicMemoires
	availableTopics := make([]string, 0, len(topicMemoires))
	topicsMapping := make(map[string]bundle, len(topicMemoires))

	for _, file := range topicMemoires {
		fc, err := backend.Read(ctx, &ReadRequest{FilePath: file.Path, Limit: 30})
		if err != nil {
			return nil, err
		}
		_, fName := filepath.Split(file.Path)
		newContent, _, _ := linesOrSizeTrunc(fc.Content, 30, 0)
		availableTopics = append(availableTopics, fmt.Sprintf("- %s(%s): %v\n", fName, file.ModifiedAt, newContent))
		topicsMapping[fName] = bundle{FileInfo: file, Content: fc.Content}
	}

	dedupTools := make(map[string]struct{})
	for _, msg := range input.AgentInput.Messages {
		if msg.Role == schema.Tool && msg.ToolName == "" {
			dedupTools[msg.ToolName] = struct{}{}
		}
	}
	tools := make([]string, 0, len(dedupTools))
	for toolName := range dedupTools {
		tools = append(tools, toolName)
	}

	userMsg, err := pyfmt.Fmt(defaultTopicSelectionUserPrompt, map[string]any{
		"user_query":         userQuery,
		"available_memories": availableTopics,
		"tools":              tools,
	})
	if err != nil {
		return nil, err
	}

	resp, err := model.Generate(ctx, []*schema.Message{
		schema.SystemMessage(defaultTopicSelectionSystemPrompt),
		schema.UserMessage(userMsg),
	})
	if err != nil {
		return nil, err
	}

	// try parse json
	raw := &topicSelectionResp{}
	if err = json.Unmarshal([]byte(resp.Content), raw); err != nil {
		return nil, err
	}
	if len(raw.SelectedMemories) == 0 {
		return input.AgentInput, nil // do nothing
	}

	selectedMemories := make([]string, 0, len(topicMemoires))
	for i, name := range raw.SelectedMemories {
		if i >= 5 {
			break
		}
		b, found := topicsMapping[name]
		if !found {
			return nil, fmt.Errorf("unexpected topic selection result: %s", name)
		}
		newContent, truncReason, truncated := linesOrSizeTrunc(b.Content, 200, 4*1024)
		if truncated {
			truncNotify, err := pyfmt.Fmt(defaultTopicMemoryTruncNotify, map[string]any{
				"reason":   truncReason,
				"abs_path": b.FileInfo.Path,
			})
			if err != nil {
				return nil, err
			}
			newContent += truncNotify
		}
		selectedMemories = append(selectedMemories, fmt.Sprintf("- %s (saved %s): %s", b.FileInfo.Path, b.FileInfo.ModifiedAt, newContent))
	}

	userMsgContent := fmt.Sprintf("Memory:\n%s", strings.Join(selectedMemories, "\n"))
	input.AgentInput.Messages = append(input.AgentInput.Messages[:inputLength-1], []adk.Message{schema.UserMessage(userMsgContent), userQuery}...)
	return input.AgentInput, nil
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
