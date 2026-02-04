/*
 * Copyright 2025 CloudWeGo Authors
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

package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"runtime/debug"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for the filesystem middleware
type Config struct {
	// Backend provides filesystem operations used by tools and offloading.
	// required
	Backend filesystem.Backend

	// Shell provides shell command execution capability.
	// If set, an execute tool will be registered to support shell command execution.
	// optional, mutually exclusive with StreamingShell
	Shell filesystem.Shell
	// StreamingShell provides streaming shell command execution capability.
	// If set, a streaming execute tool will be registered for real-time output.
	// optional, mutually exclusive with Shell
	StreamingShell filesystem.StreamingShell

	// WithoutLargeToolResultOffloading disables automatic offloading of large tool result to Backend
	// optional, false(enabled) by default
	WithoutLargeToolResultOffloading bool
	// LargeToolResultOffloadingTokenLimit sets the token threshold to trigger offloading
	// optional, 20000 by default
	LargeToolResultOffloadingTokenLimit int
	// LargeToolResultOffloadingPathGen generates the write path for offloaded results based on context and ToolInput
	// optional, "/large_tool_result/{ToolCallID}" by default
	LargeToolResultOffloadingPathGen func(ctx context.Context, input *compose.ToolInput) (string, error)

	// CustomSystemPrompt overrides the default ToolsSystemPrompt appended to agent instruction
	// optional, ToolsSystemPrompt by default
	CustomSystemPrompt *string

	// CustomLsToolDesc overrides the ls tool description used in tool registration
	// optional, ListFilesToolDesc by default
	CustomLsToolDesc *string
	// CustomReadFileToolDesc overrides the read_file tool description
	// optional, ReadFileToolDesc by default
	CustomReadFileToolDesc *string
	// CustomGrepToolDesc overrides the grep tool description
	// optional, GrepToolDesc by default
	CustomGrepToolDesc *string
	// CustomGlobToolDesc overrides the glob tool description
	// optional, GlobToolDesc by default
	CustomGlobToolDesc *string
	// CustomWriteFileToolDesc overrides the write_file tool description
	// optional, WriteFileToolDesc by default
	CustomWriteFileToolDesc *string
	// CustomEditToolDesc overrides the edit_file tool description
	// optional, EditFileToolDesc by default
	CustomEditToolDesc *string
	// CustomExecuteToolDesc overrides the execute tool description
	// optional, ExecuteToolDesc by default
	CustomExecuteToolDesc *string
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config should not be nil")
	}
	if c.Backend == nil {
		return errors.New("backend should not be nil")
	}
	if c.StreamingShell != nil && c.Shell != nil {
		return errors.New("shell and streaming shell should not be both set")
	}
	return nil
}

// NewMiddleware constructs and returns the filesystem middleware.
//
// Deprecated: Use NewChatModelAgentMiddleware instead. NewChatModelAgentMiddleware returns
// a ChatModelAgentMiddleware which provides better context propagation through wrapper methods
// and is the recommended approach for new code. See ChatModelAgentMiddleware documentation
// for details on the benefits over AgentMiddleware.
func NewMiddleware(ctx context.Context, config *Config) (adk.AgentMiddleware, error) {
	err := config.Validate()
	if err != nil {
		return adk.AgentMiddleware{}, err
	}
	ts, err := getFilesystemTools(ctx, config)
	if err != nil {
		return adk.AgentMiddleware{}, err
	}

	var systemPrompt string
	if config.CustomSystemPrompt != nil {
		systemPrompt = *config.CustomSystemPrompt
	} else {
		systemPrompt, err = internal.SelectPrompt(internal.I18nPrompts{
			English: ToolsSystemPrompt,
			Chinese: ToolsSystemPromptChinese,
		})
		if err != nil {
			return adk.AgentMiddleware{}, err
		}
		if config.Shell != nil || config.StreamingShell != nil {
			var executePrompt string
			executePrompt, err = internal.SelectPrompt(internal.I18nPrompts{
				English: ExecuteToolsSystemPrompt,
				Chinese: ExecuteToolsSystemPromptChinese,
			})
			if err != nil {
				return adk.AgentMiddleware{}, err
			}
			systemPrompt += executePrompt
		}
	}

	m := adk.AgentMiddleware{
		AdditionalInstruction: systemPrompt,
		AdditionalTools:       ts,
	}

	if !config.WithoutLargeToolResultOffloading {
		m.WrapToolCall = newToolResultOffloading(ctx, &toolResultOffloadingConfig{
			Backend:       config.Backend,
			TokenLimit:    config.LargeToolResultOffloadingTokenLimit,
			PathGenerator: config.LargeToolResultOffloadingPathGen,
		})
	}

	return m, nil
}

// NewChatModelAgentMiddleware constructs and returns the filesystem middleware as a ChatModelAgentMiddleware.
//
// This is the recommended constructor for new code. It returns a ChatModelAgentMiddleware which provides:
//   - Better context propagation through WrapInvokableToolCall and WrapStreamableToolCall methods
//   - BeforeAgent hook for modifying agent instruction and tools at runtime
//   - More flexible extension points compared to the struct-based AgentMiddleware
//
// The middleware provides filesystem tools (ls, read_file, write_file, edit_file, glob, grep)
// and optionally an execute tool if the Backend implements ShellBackend or StreamingShellBackend.
//
// Example usage:
//
//	middleware, err := filesystem.NewChatModelAgentMiddleware(ctx, &filesystem.Config{
//	    Backend: myBackend,
//	})
//	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
//	    // ...
//	    Handlers: []adk.ChatModelAgentMiddleware{middleware},
//	})
func NewChatModelAgentMiddleware(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	err := config.Validate()
	if err != nil {
		return nil, err
	}
	ts, err := getFilesystemTools(ctx, config)
	if err != nil {
		return nil, err
	}

	var systemPrompt string
	if config.CustomSystemPrompt != nil {
		systemPrompt = *config.CustomSystemPrompt
	} else {
		systemPrompt, err = internal.SelectPrompt(internal.I18nPrompts{
			English: ToolsSystemPrompt,
			Chinese: ToolsSystemPromptChinese,
		})
		if err != nil {
			return nil, err
		}
		if config.Shell != nil || config.StreamingShell != nil {
			var executePrompt string
			executePrompt, err = internal.SelectPrompt(internal.I18nPrompts{
				English: ExecuteToolsSystemPrompt,
				Chinese: ExecuteToolsSystemPromptChinese,
			})
			if err != nil {
				return nil, err
			}
			systemPrompt += executePrompt
		}
	}

	m := &filesystemMiddleware{
		additionalInstruction: systemPrompt,
		additionalTools:       ts,
	}

	if !config.WithoutLargeToolResultOffloading {
		m.offloading = &toolResultOffloading{
			backend:       config.Backend,
			tokenLimit:    config.LargeToolResultOffloadingTokenLimit,
			pathGenerator: config.LargeToolResultOffloadingPathGen,
		}
		if m.offloading.tokenLimit == 0 {
			m.offloading.tokenLimit = 20000
		}
		if m.offloading.pathGenerator == nil {
			m.offloading.pathGenerator = func(ctx context.Context, input *compose.ToolInput) (string, error) {
				return fmt.Sprintf("/large_tool_result/%s", input.CallID), nil
			}
		}
	}

	return m, nil
}

type filesystemMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	additionalInstruction string
	additionalTools       []tool.BaseTool
	offloading            *toolResultOffloading
}

func (m *filesystemMiddleware) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	if runCtx == nil {
		return ctx, runCtx, nil
	}

	nRunCtx := *runCtx
	if m.additionalInstruction != "" {
		nRunCtx.Instruction = nRunCtx.Instruction + "\n" + m.additionalInstruction
	}
	nRunCtx.Tools = append(nRunCtx.Tools, m.additionalTools...)
	return ctx, &nRunCtx, nil
}

func (m *filesystemMiddleware) WrapInvokableToolCall(ctx context.Context, endpoint adk.InvokableToolCallEndpoint, tCtx *adk.ToolContext) (adk.InvokableToolCallEndpoint, error) {
	if m.offloading == nil {
		return endpoint, nil
	}
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
		result, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return "", err
		}
		return m.offloading.handleResult(ctx, result, &compose.ToolInput{
			Name:   tCtx.Name,
			CallID: tCtx.CallID,
		})
	}, nil
}

func (m *filesystemMiddleware) WrapStreamableToolCall(ctx context.Context, endpoint adk.StreamableToolCallEndpoint, tCtx *adk.ToolContext) (adk.StreamableToolCallEndpoint, error) {
	if m.offloading == nil {
		return endpoint, nil
	}
	return func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
		sr, err := endpoint(ctx, argumentsInJSON, opts...)
		if err != nil {
			return nil, err
		}
		result, err := concatString(sr)
		if err != nil {
			return nil, err
		}
		result, err = m.offloading.handleResult(ctx, result, &compose.ToolInput{
			Name:   tCtx.Name,
			CallID: tCtx.CallID,
		})
		if err != nil {
			return nil, err
		}
		return schema.StreamReaderFromArray([]string{result}), nil
	}, nil
}

func getFilesystemTools(_ context.Context, validatedConfig *Config) ([]tool.BaseTool, error) {
	var tools []tool.BaseTool

	lsTool, err := newLsTool(validatedConfig.Backend, validatedConfig.CustomLsToolDesc)
	if err != nil {
		return nil, err
	}
	tools = append(tools, lsTool)

	readTool, err := newReadFileTool(validatedConfig.Backend, validatedConfig.CustomReadFileToolDesc)
	if err != nil {
		return nil, err
	}
	tools = append(tools, readTool)

	writeTool, err := newWriteFileTool(validatedConfig.Backend, validatedConfig.CustomWriteFileToolDesc)
	if err != nil {
		return nil, err
	}
	tools = append(tools, writeTool)

	editTool, err := newEditFileTool(validatedConfig.Backend, validatedConfig.CustomEditToolDesc)
	if err != nil {
		return nil, err
	}
	tools = append(tools, editTool)

	globTool, err := newGlobTool(validatedConfig.Backend, validatedConfig.CustomGlobToolDesc)
	if err != nil {
		return nil, err
	}
	tools = append(tools, globTool)

	grepTool, err := newGrepTool(validatedConfig.Backend, validatedConfig.CustomGrepToolDesc)
	if err != nil {
		return nil, err
	}
	tools = append(tools, grepTool)

	if validatedConfig.StreamingShell != nil {
		var executeTool tool.BaseTool
		executeTool, err = newStreamingExecuteTool(validatedConfig.StreamingShell, validatedConfig.CustomExecuteToolDesc)
		if err != nil {
			return nil, err
		}
		tools = append(tools, executeTool)
	} else if validatedConfig.Shell != nil {
		var executeTool tool.BaseTool
		executeTool, err = newExecuteTool(validatedConfig.Shell, validatedConfig.CustomExecuteToolDesc)
		if err != nil {
			return nil, err
		}
		tools = append(tools, executeTool)
	}

	return tools, nil
}

type lsArgs struct {
	Path string `json:"path"`
}

func newLsTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, ListFilesToolDesc, ListFilesToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("ls", d, func(ctx context.Context, input lsArgs) (string, error) {
		infos, err := fs.LsInfo(ctx, &filesystem.LsInfoRequest{Path: input.Path})
		if err != nil {
			return "", err
		}
		paths := make([]string, 0, len(infos))
		for _, fi := range infos {
			paths = append(paths, fi.Path)
		}
		return strings.Join(paths, "\n"), nil
	})
}

type readFileArgs struct {
	// FilePath is the absolute path to the file to read.
	FilePath string `json:"file_path" jsonschema:"description=The absolute path to the file to read"`

	// Offset is the line number to start reading from.
	Offset int `json:"offset" jsonschema:"description=The line number to start reading from. Only provide if the file is too large to read at once"`

	// Limit is the number of lines to read.
	Limit int `json:"limit" jsonschema:"description=The number of lines to read. Only provide if the file is too large to read at once."`
}

func newReadFileTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, ReadFileToolDesc, ReadFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("read_file", d, func(ctx context.Context, input readFileArgs) (string, error) {
		if input.Offset < 0 {
			input.Offset = 0
		}
		if input.Limit <= 0 {
			input.Limit = 200
		}
		return fs.Read(ctx, &filesystem.ReadRequest{
			FilePath: input.FilePath,
			Offset:   input.Offset,
			Limit:    input.Limit,
		})
	})
}

type writeFileArgs struct {
	// FilePath is the absolute path to the file to write.
	FilePath string `json:"file_path" jsonschema:"description=The absolute path to the file to write (must be absolute\\, not relative)"`

	// Content is the content to write to the file.
	Content string `json:"content" jsonschema:"description=The content to write to the file"`
}

func newWriteFileTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, WriteFileToolDesc, WriteFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("write_file", d, func(ctx context.Context, input writeFileArgs) (string, error) {
		err := fs.Write(ctx, &filesystem.WriteRequest{
			FilePath: input.FilePath,
			Content:  input.Content,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Updated file %s", input.FilePath), nil
	})
}

type editFileArgs struct {
	// FilePath is the absolute path to the file to modify.
	FilePath string `json:"file_path" jsonschema:"description=The absolute path to the file to modify"`

	// OldString is the text to replace.
	OldString string `json:"old_string" jsonschema:"description=The text to replace"`

	// NewString is the text to replace it with.
	NewString string `json:"new_string" jsonschema:"description=The text to replace it with (must be different from old_string)"`

	// ReplaceAll indicates whether to replace all occurrences of old_string.
	ReplaceAll bool `json:"replace_all" jsonschema:"description=Replace all occurrences of old_string (default false),default=false"`
}

func newEditFileTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, EditFileToolDesc, EditFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("edit_file", d, func(ctx context.Context, input editFileArgs) (string, error) {
		err := fs.Edit(ctx, &filesystem.EditRequest{
			FilePath:   input.FilePath,
			OldString:  input.OldString,
			NewString:  input.NewString,
			ReplaceAll: input.ReplaceAll,
		})
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("Successfully replaced the string in '%s'", input.FilePath), nil
	})
}

type globArgs struct {
	// Pattern is the glob pattern to match files against.
	Pattern string `json:"pattern" jsonschema:"description=The glob pattern to match files against"`

	// Path is the directory to search in.
	Path string `json:"path" jsonschema:"description=The directory to search in. If not specified\\, the current working directory will be used. IMPORTANT: Omit this field to use the default directory. DO NOT enter 'undefined' or 'null' - simply omit it for the default behavior. Must be a valid directory path if provided."`
}

func newGlobTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, GlobToolDesc, GlobToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("glob", d, func(ctx context.Context, input globArgs) (string, error) {
		infos, err := fs.GlobInfo(ctx, &filesystem.GlobInfoRequest{
			Pattern: input.Pattern,
			Path:    input.Path,
		})
		if err != nil {
			return "", err
		}
		paths := make([]string, 0, len(infos))
		for _, fi := range infos {
			paths = append(paths, fi.Path)
		}
		return strings.Join(paths, "\n"), nil
	})
}

type grepArgs struct {
	// Pattern is the regular expression pattern to search for in file contents.
	Pattern string `json:"pattern" jsonschema:"description=The regular expression pattern to search for in file contents"`

	// Path is the file or directory to search in. Defaults to current working directory.
	Path *string `json:"path,omitempty" jsonschema:"description=File or directory to search in (rg PATH). Defaults to current working directory."`

	// Glob is the glob pattern to filter files (e.g. "*.js", "*.{ts,tsx}").
	Glob *string `json:"glob,omitempty" jsonschema:"description=Glob pattern to filter files (e.g. '*.js'\\, '*.{ts\\,tsx}') - maps to rg --glob"`

	// OutputMode specifies the output format.
	// "content" shows matching lines (supports context, line numbers, head_limit).
	// "files_with_matches" shows file paths (supports head_limit).
	// "count" shows match counts (supports head_limit).
	// Defaults to "files_with_matches".
	OutputMode string `json:"output_mode,omitempty" jsonschema:"description=Output mode: 'content' shows matching lines (supports -A/-B/-C context\\, -n line numbers\\, head_limit)\\, 'files_with_matches' shows file paths (supports head_limit)\\, 'count' shows match counts (supports head_limit). Defaults to 'files_with_matches'.,enum=content,enum=files_with_matches,enum=count"`

	// BeforeLines is the number of lines to show before each match.
	// Only applicable when output_mode is "content".
	BeforeLines *int `json:"-B,omitempty" jsonschema:"description=Number of lines to show before each match (rg -B). Requires output_mode: 'content'\\, ignored otherwise."`

	// AfterLines is the number of lines to show after each match.
	// Only applicable when output_mode is "content".
	AfterLines *int `json:"-A,omitempty" jsonschema:"description=Number of lines to show after each match (rg -A). Requires output_mode: 'content'\\, ignored otherwise."`

	// ContextAlias is an alias for Context (number of lines before and after).
	ContextAlias *int `json:"-C,omitempty" jsonschema:"description=Alias for context."`

	// Context is the number of lines to show before and after each match.
	// Only applicable when output_mode is "content".
	Context *int `json:"context,omitempty" jsonschema:"description=Number of lines to show before and after each match (rg -C). Requires output_mode: 'content'\\, ignored otherwise."`

	// ShowLineNumbers enables showing line numbers in output.
	// Only applicable when output_mode is "content". Defaults to true.
	ShowLineNumbers *bool `json:"-n,omitempty" jsonschema:"description=Show line numbers in output (rg -n). Requires output_mode: 'content'\\, ignored otherwise. Defaults to true."`

	// CaseInsensitive enables case insensitive search.
	CaseInsensitive *bool `json:"-i,omitempty" jsonschema:"description=Case insensitive search (rg -i)"`

	// FileType is the file type to search (e.g., js, py, rust, go, java).
	// More efficient than Glob for standard file types.
	FileType *string `json:"type,omitempty" jsonschema:"description=File type to search (rg --type). Common types: js\\, py\\, rust\\, go\\, java\\, etc. More efficient than include for standard file types."`

	// HeadLimit limits output to first N lines/entries.
	// Works across all output modes. Defaults to 0 (unlimited).
	HeadLimit *int `json:"head_limit,omitempty" jsonschema:"description=Limit output to first N lines/entries\\, equivalent to '| head -N'. Works across all output modes: content (limits output lines)\\, files_with_matches (limits file paths)\\, count (limits count entries). Defaults to 0 (unlimited)."`

	// Offset skips first N lines/entries before applying HeadLimit.
	// Works across all output modes. Defaults to 0.
	Offset *int `json:"offset,omitempty" jsonschema:"description=Skip first N lines/entries before applying head_limit\\, equivalent to '| tail -n +N | head -N'. Works across all output modes. Defaults to 0."`

	// Multiline enables multiline mode where patterns can span lines.
	//   - true: Allows patterns to match across lines, "." matches newlines
	//   - false: Default, matches only within single lines
	Multiline *bool `json:"multiline,omitempty" jsonschema:"description=Enable multiline mode where . matches newlines and patterns can span lines (rg -U --multiline-dotall). Default: false."`
}

func newGrepTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, GrepToolDesc, GrepToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("grep", d, func(ctx context.Context, input grepArgs) (string, error) {
		// Extract string parameters
		path := valueOrDefault(input.Path, "")
		glob := valueOrDefault(input.Glob, "")
		fileType := valueOrDefault(input.FileType, "")

		// Determine output mode
		var outputMode filesystem.OutputMode
		switch input.OutputMode {
		case "content":
			outputMode = filesystem.ContentOfOutputMode
		case "count":
			outputMode = filesystem.CountOfOutputMode
		default:
			outputMode = filesystem.FilesWithMatchesOfOutputMode
		}

		// Extract context parameters with priority handling
		contextLines := valueOrDefault(input.ContextAlias, valueOrDefault(input.Context, 0))
		beforeLines := valueOrDefault(input.BeforeLines, 0)
		afterLines := valueOrDefault(input.AfterLines, 0)

		// Extract boolean flags
		caseInsensitive := valueOrDefault(input.CaseInsensitive, false)
		enableMultiline := valueOrDefault(input.Multiline, false)

		// Extract pagination parameters
		headLimit := valueOrDefault(input.HeadLimit, 0)
		offset := valueOrDefault(input.Offset, 0)

		matches, err := fs.GrepRaw(ctx, &filesystem.GrepRequest{
			Pattern:         input.Pattern,
			Path:            path,
			Glob:            glob,
			OutputMode:      outputMode,
			FileType:        fileType,
			CaseInsensitive: caseInsensitive,
			AfterLines:      afterLines,
			BeforeLines:     beforeLines,
			ContextLines:    contextLines,
			HeadLimit:       headLimit,
			Offset:          offset,
			EnableMultiline: enableMultiline,
		})
		if err != nil {
			return "", err
		}

		switch input.OutputMode {
		case "files_with_matches":
			var results []string
			for _, match := range matches {
				results = append(results, match.Path)
			}
			return strings.Join(results, "\n"), nil

		case "content":
			var b strings.Builder
			showLineNum := valueOrDefault(input.ShowLineNumbers, true)

			for _, match := range matches {
				b.WriteString(match.Path)
				if showLineNum {
					b.WriteString(":")
					b.WriteString(strconv.Itoa(match.Line))
				}
				b.WriteString(":")
				b.WriteString(match.Content)
				b.WriteString("\n")
			}
			return strings.TrimSuffix(b.String(), "\n"), nil

		case "count":
			var b strings.Builder
			for _, match := range matches {
				b.WriteString(match.Path)
				b.WriteString(":")
				b.WriteString(strconv.Itoa(match.Count))
				b.WriteString("\n")
			}
			return strings.TrimSuffix(b.String(), "\n"), nil

		default:
			var results []string
			for _, match := range matches {
				results = append(results, match.Path)
			}
			return strings.Join(results, "\n"), nil
		}
	})
}

type executeArgs struct {
	Command string `json:"command"`
}

func newExecuteTool(sb filesystem.Shell, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, ExecuteToolDesc, ExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool("execute", d, func(ctx context.Context, input executeArgs) (string, error) {
		result, err := sb.Execute(ctx, &filesystem.ExecuteRequest{
			Command: input.Command,
		})
		if err != nil {
			return "", err
		}

		return convExecuteResponse(result), nil
	})
}

func newStreamingExecuteTool(sb filesystem.StreamingShell, desc *string) (tool.BaseTool, error) {
	d, err := selectToolDesc(desc, ExecuteToolDesc, ExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferStreamTool("execute", d, func(ctx context.Context, input executeArgs) (*schema.StreamReader[string], error) {
		result, err := sb.ExecuteStreaming(ctx, &filesystem.ExecuteRequest{
			Command: input.Command,
		})
		if err != nil {
			return nil, err
		}
		sr, sw := schema.Pipe[string](10)
		go func() {
			defer func() {
				e := recover()
				if e != nil {
					sw.Send("", fmt.Errorf("panic: %v,\n stack: %s", e, string(debug.Stack())))
				}
				sw.Close()
			}()

			var hasSentContent bool
			var exitCode *int

			for {
				chunk, recvErr := result.Recv()
				if recvErr == io.EOF {
					break
				}
				if recvErr != nil {
					sw.Send("", recvErr)
					return
				}

				if chunk == nil {
					continue
				}
				if chunk.ExitCode != nil {
					exitCode = chunk.ExitCode
				}

				parts := make([]string, 0, 2)
				if chunk.Output != "" {
					parts = append(parts, chunk.Output)
				}
				if chunk.Truncated {
					parts = append(parts, "[Output was truncated due to size limits]")
				}
				if len(parts) > 0 {
					sw.Send(strings.Join(parts, "\n"), nil)
					hasSentContent = true
				}
			}

			if exitCode != nil && *exitCode != 0 {
				sw.Send(fmt.Sprintf("\n[Command failed with exit code %d]", *exitCode), nil)
			} else if !hasSentContent {
				sw.Send("[Command executed successfully with no output]", nil)
			}
		}()

		return sr, nil
	})
}

func convExecuteResponse(response *filesystem.ExecuteResponse) string {
	if response == nil {
		return ""
	}
	parts := []string{response.Output}
	if response.ExitCode != nil && *response.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("[Command failed with exit code %d]", *response.ExitCode))
	}
	if response.Truncated {
		parts = append(parts, "[Output was truncated due to size limits]")
	}

	result := strings.Join(parts, "\n")
	if result == "" && (response.ExitCode == nil || *response.ExitCode == 0) {
		return "[Command executed successfully with no output]"
	}
	return result
}

// valueOrDefault returns the value pointed to by ptr, or defaultValue if ptr is nil.
func valueOrDefault[T any](ptr *T, defaultValue T) T {
	if ptr != nil {
		return *ptr
	}
	return defaultValue
}

// selectToolDesc returns the custom description if provided, otherwise selects the appropriate
// i18n description based on the current language setting.
func selectToolDesc(customDesc *string, defaultEnglish, defaultChinese string) (string, error) {
	if customDesc != nil {
		return *customDesc, nil
	}
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultEnglish,
		Chinese: defaultChinese,
	})
}
