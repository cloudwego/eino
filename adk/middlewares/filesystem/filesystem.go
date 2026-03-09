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
	"path/filepath"
	"runtime/debug"
	"sort"
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

const (
	ToolNameLs        = "ls"
	ToolNameReadFile  = "read_file"
	ToolNameWriteFile = "write_file"
	ToolNameEditFile  = "edit_file"
	ToolNameGlob      = "glob"
	ToolNameGrep      = "grep"
	ToolNameExecute   = "execute"

	noFilesFound   = "No files found"
	noMatchesFound = "No matches found"
)

// ToolConfig configures a filesystem tool
type ToolConfig struct {
	// Name overrides the tool name used in tool registration
	// optional, default tool name will be used if not set (empty string)
	Name string

	// Desc overrides the tool description used in tool registration
	// optional, default tool description will be used if not set (nil pointer)
	Desc *string

	// CustomTool provides a custom implementation for this tool.
	// If set, this custom tool will be used instead of the default implementation associated with Backend.
	// If not set, the default tool implementation associated with Backend will be created automatically.
	// optional
	CustomTool tool.BaseTool

	// Disable disables this tool
	// If true, the tool will not be registered
	// optional, false by default
	Disable bool
}

// Config is the configuration for the filesystem middleware
type Config struct {
	// Backend provides filesystem operations used by tools and offloading.
	// If set, filesystem tools (read_file, write_file, edit_file, glob, grep) will be registered.
	// At least one of Backend, Shell, or StreamingShell must be set.
	Backend filesystem.Backend

	// Shell provides shell command execution capability.
	// If set, an execute tool will be registered to support shell command execution.
	// At least one of Backend, Shell, or StreamingShell must be set.
	// Mutually exclusive with StreamingShell.
	Shell filesystem.Shell
	// StreamingShell provides streaming shell command execution capability.
	// If set, a streaming execute tool will be registered to support streaming shell command execution.
	// At least one of Backend, Shell, or StreamingShell must be set.
	// Mutually exclusive with Shell.
	StreamingShell filesystem.StreamingShell

	// LsToolConfig configures the ls tool
	// optional
	LsToolConfig *ToolConfig
	// ReadFileToolConfig configures the read_file tool
	// optional
	ReadFileToolConfig *ToolConfig
	// WriteFileToolConfig configures the write_file tool
	// optional
	WriteFileToolConfig *ToolConfig
	// EditFileToolConfig configures the edit_file tool
	// optional
	EditFileToolConfig *ToolConfig
	// GlobToolConfig configures the glob tool
	// optional
	GlobToolConfig *ToolConfig
	// GrepToolConfig configures the grep tool
	// optional
	GrepToolConfig *ToolConfig

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
	// Deprecated: Use LsToolConfig.Desc instead
	CustomLsToolDesc *string
	// CustomReadFileToolDesc overrides the read_file tool description
	// optional, ReadFileToolDesc by default
	// Deprecated: Use ReadFileToolConfig.Desc instead
	CustomReadFileToolDesc *string
	// CustomGrepToolDesc overrides the grep tool description
	// optional, GrepToolDesc by default
	// Deprecated: Use GrepToolConfig.Desc instead
	CustomGrepToolDesc *string
	// CustomGlobToolDesc overrides the glob tool description
	// optional, GlobToolDesc by default
	// Deprecated: Use GlobToolConfig.Desc instead
	CustomGlobToolDesc *string
	// CustomWriteFileToolDesc overrides the write_file tool description
	// optional, WriteFileToolDesc by default
	// Deprecated: Use WriteFileToolConfig.Desc instead
	CustomWriteFileToolDesc *string
	// CustomEditToolDesc overrides the edit_file tool description
	// optional, EditFileToolDesc by default
	// Deprecated: Use EditFileToolConfig.Desc instead
	CustomEditToolDesc *string
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
// Deprecated: Use New instead. New returns
// a ChatModelAgentMiddleware which provides better context propagation through wrapper methods
// and is the recommended approach for new code. See ChatModelAgentMiddleware documentation
// for details on the benefits over AgentMiddleware.
func NewMiddleware(ctx context.Context, config *Config) (adk.AgentMiddleware, error) {
	err := config.Validate()
	if err != nil {
		return adk.AgentMiddleware{}, err
	}
	ts, err := getFilesystemTools(ctx, &MiddlewareConfig{
		Backend:                 config.Backend,
		Shell:                   config.Shell,
		StreamingShell:          config.StreamingShell,
		LsToolConfig:            config.LsToolConfig,
		ReadFileToolConfig:      config.ReadFileToolConfig,
		WriteFileToolConfig:     config.WriteFileToolConfig,
		EditFileToolConfig:      config.EditFileToolConfig,
		GlobToolConfig:          config.GlobToolConfig,
		GrepToolConfig:          config.GrepToolConfig,
		CustomSystemPrompt:      config.CustomSystemPrompt,
		CustomLsToolDesc:        config.CustomLsToolDesc,
		CustomReadFileToolDesc:  config.CustomReadFileToolDesc,
		CustomGrepToolDesc:      config.CustomGrepToolDesc,
		CustomGlobToolDesc:      config.CustomGlobToolDesc,
		CustomWriteFileToolDesc: config.CustomWriteFileToolDesc,
		CustomEditToolDesc:      config.CustomEditToolDesc,
	})
	if err != nil {
		return adk.AgentMiddleware{}, err
	}

	var systemPrompt string
	if config.CustomSystemPrompt != nil {
		systemPrompt = *config.CustomSystemPrompt
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

// MiddlewareConfig is the configuration for the filesystem middleware
type MiddlewareConfig struct {
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

	// LsToolConfig configures the ls tool
	// optional
	LsToolConfig *ToolConfig
	// ReadFileToolConfig configures the read_file tool
	// optional
	ReadFileToolConfig *ToolConfig
	// WriteFileToolConfig configures the write_file tool
	// optional
	WriteFileToolConfig *ToolConfig
	// EditFileToolConfig configures the edit_file tool
	// optional
	EditFileToolConfig *ToolConfig
	// GlobToolConfig configures the glob tool
	// optional
	GlobToolConfig *ToolConfig
	// GrepToolConfig configures the grep tool
	// optional
	GrepToolConfig *ToolConfig

	// CustomSystemPrompt overrides the default ToolsSystemPrompt appended to agent instruction
	// optional, ToolsSystemPrompt by default
	CustomSystemPrompt *string

	// CustomLsToolDesc overrides the ls tool description used in tool registration
	// optional, ListFilesToolDesc by default
	// Deprecated: Use LsToolConfig.Desc instead
	CustomLsToolDesc *string
	// CustomReadFileToolDesc overrides the read_file tool description
	// optional, ReadFileToolDesc by default
	// Deprecated: Use ReadFileToolConfig.Desc instead
	CustomReadFileToolDesc *string
	// CustomGrepToolDesc overrides the grep tool description
	// optional, GrepToolDesc by default
	// Deprecated: Use GrepToolConfig.Desc instead
	CustomGrepToolDesc *string
	// CustomGlobToolDesc overrides the glob tool description
	// optional, GlobToolDesc by default
	// Deprecated: Use GlobToolConfig.Desc instead
	CustomGlobToolDesc *string
	// CustomWriteFileToolDesc overrides the write_file tool description
	// optional, WriteFileToolDesc by default
	// Deprecated: Use WriteFileToolConfig.Desc instead
	CustomWriteFileToolDesc *string
	// CustomEditToolDesc overrides the edit_file tool description
	// optional, EditFileToolDesc by default
	// Deprecated: Use EditFileToolConfig.Desc instead
	CustomEditToolDesc *string
}

func (c *MiddlewareConfig) Validate() error {
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

// mergeToolConfigWithDesc merges ToolConfig with legacy Desc field
// Priority: ToolConfig.Desc > legacy Desc
// Returns an empty ToolConfig if both are nil (to allow backend default implementation)
func (c *MiddlewareConfig) mergeToolConfigWithDesc(
	toolConfig *ToolConfig,
	legacyDesc *string,
) *ToolConfig {
	if toolConfig == nil && legacyDesc == nil {
		return &ToolConfig{}
	}

	if toolConfig == nil {
		return &ToolConfig{
			Desc: legacyDesc,
		}
	}

	if toolConfig.Desc == nil && legacyDesc != nil {
		merged := *toolConfig
		merged.Desc = legacyDesc
		return &merged
	}

	return toolConfig
}

// New constructs and returns the filesystem middleware as a ChatModelAgentMiddleware.
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
//	middleware, err := filesystem.New(ctx, &filesystem.Config{
//	    Backend: myBackend,
//	})
//	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
//	    // ...
//	    Handlers: []adk.ChatModelAgentMiddleware{middleware},
//	})
func New(ctx context.Context, config *MiddlewareConfig) (adk.ChatModelAgentMiddleware, error) {
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
	}

	m := &filesystemMiddleware{
		additionalInstruction: systemPrompt,
		additionalTools:       ts,
	}

	return m, nil
}

type filesystemMiddleware struct {
	adk.BaseChatModelAgentMiddleware
	additionalInstruction string
	additionalTools       []tool.BaseTool
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

// toolSpec defines a specification for creating a filesystem tool.
// It unifies the tool creation process by encapsulating the tool configuration,
// legacy descriptor, and the creation function.
type toolSpec struct {
	config     *ToolConfig
	legacyDesc *string
	createFunc func(name, desc string) (tool.BaseTool, error)
}

func getFilesystemTools(_ context.Context, middlewareConfig *MiddlewareConfig) ([]tool.BaseTool, error) {
	var tools []tool.BaseTool

	toolSpecs := []toolSpec{
		{
			config:     middlewareConfig.LsToolConfig,
			legacyDesc: middlewareConfig.CustomLsToolDesc,
			createFunc: func(name, desc string) (tool.BaseTool, error) {
				if middlewareConfig.Backend != nil {
					return newLsTool(middlewareConfig.Backend, name, desc)
				}
				return nil, nil
			},
		},
		{
			config:     middlewareConfig.ReadFileToolConfig,
			legacyDesc: middlewareConfig.CustomReadFileToolDesc,
			createFunc: func(name, desc string) (tool.BaseTool, error) {
				if middlewareConfig.Backend != nil {
					return newReadFileTool(middlewareConfig.Backend, name, desc)
				}
				return nil, nil
			},
		},
		{
			config:     middlewareConfig.WriteFileToolConfig,
			legacyDesc: middlewareConfig.CustomWriteFileToolDesc,
			createFunc: func(name, desc string) (tool.BaseTool, error) {
				if middlewareConfig.Backend != nil {
					return newWriteFileTool(middlewareConfig.Backend, name, desc)
				}
				return nil, nil
			},
		},
		{
			config:     middlewareConfig.EditFileToolConfig,
			legacyDesc: middlewareConfig.CustomEditToolDesc,
			createFunc: func(name, desc string) (tool.BaseTool, error) {
				if middlewareConfig.Backend != nil {
					return newEditFileTool(middlewareConfig.Backend, name, desc)
				}
				return nil, nil
			},
		},
		{
			config:     middlewareConfig.GlobToolConfig,
			legacyDesc: middlewareConfig.CustomGlobToolDesc,
			createFunc: func(name, desc string) (tool.BaseTool, error) {
				if middlewareConfig.Backend != nil {
					return newGlobTool(middlewareConfig.Backend, name, desc)
				}
				return nil, nil
			},
		},
		{
			config:     middlewareConfig.GrepToolConfig,
			legacyDesc: middlewareConfig.CustomGrepToolDesc,
			createFunc: func(name, desc string) (tool.BaseTool, error) {
				if middlewareConfig.Backend != nil {
					return newGrepTool(middlewareConfig.Backend, name, desc)
				}
				return nil, nil
			},
		},
	}

	for _, spec := range toolSpecs {
		t, err := createToolFromSpec(middlewareConfig, spec)
		if err != nil {
			return nil, err
		}
		if t != nil {
			tools = append(tools, t)
		}
	}

	// Create execute tool if Shell or StreamingShell is available
	if middlewareConfig.StreamingShell != nil {
		executeDesc, err := selectToolDesc("", ExecuteToolDesc, ExecuteToolDescChinese)
		if err != nil {
			return nil, err
		}

		executeTool, err := newStreamingExecuteTool(middlewareConfig.StreamingShell, ToolNameExecute, executeDesc)
		if err != nil {
			return nil, err
		}
		tools = append(tools, executeTool)
	} else if middlewareConfig.Shell != nil {
		executeDesc, err := selectToolDesc("", ExecuteToolDesc, ExecuteToolDescChinese)
		if err != nil {
			return nil, err
		}

		executeTool, err := newExecuteTool(middlewareConfig.Shell, ToolNameExecute, executeDesc)
		if err != nil {
			return nil, err
		}
		tools = append(tools, executeTool)
	}

	return tools, nil
}

// createToolFromSpec creates a tool instance based on the provided toolSpec.
// It handles configuration merging (ToolConfig + legacy Desc), checks if the tool
// is disabled, and prioritizes CustomTool over the default implementation.
func createToolFromSpec(middlewareConfig *MiddlewareConfig, spec toolSpec) (tool.BaseTool, error) {
	mergedConfig := middlewareConfig.mergeToolConfigWithDesc(spec.config, spec.legacyDesc)

	if mergedConfig.Disable {
		return nil, nil
	}

	return getOrCreateTool(mergedConfig.CustomTool, func() (tool.BaseTool, error) {
		desc := ""
		if mergedConfig.Desc != nil {
			desc = *mergedConfig.Desc
		}
		return spec.createFunc(mergedConfig.Name, desc)
	})
}

func getOrCreateTool(customTool tool.BaseTool, createFunc func() (tool.BaseTool, error)) (tool.BaseTool, error) {
	if customTool != nil {
		return customTool, nil
	}
	return createFunc()
}

type lsArgs struct {
	Path string `json:"path"`
}

func newLsTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameLs)
	d, err := selectToolDesc(desc, ListFilesToolDesc, ListFilesToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input lsArgs) (string, error) {
		infos, err := fs.LsInfo(ctx, &filesystem.LsInfoRequest{Path: input.Path})
		if err != nil {
			return "", err
		}
		if len(infos) == 0 {
			return noFilesFound, nil
		}
		paths := make([]string, 0, len(infos))
		for _, fi := range infos {
			paths = append(paths, fi.Path)
		}
		return strings.Join(paths, "\n"), nil
	})
}

type readFileArgs struct {
	// FilePath is the path to the file to read.
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to read"`

	// Offset is the line number to start reading from.
	Offset int `json:"offset" jsonschema:"description=The line number to start reading from. Only provide if the file is too large to read at once"`

	// Limit is the number of lines to read.
	Limit int `json:"limit" jsonschema:"description=The number of lines to read. Only provide if the file is too large to read at once."`
}

func newReadFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameReadFile)
	d, err := selectToolDesc(desc, ReadFileToolDesc, ReadFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input readFileArgs) (string, error) {
		if input.Offset <= 0 {
			input.Offset = 1
		}

		fileCt, err := fs.Read(ctx, &filesystem.ReadRequest{
			FilePath: input.FilePath,
			Offset:   input.Offset,
			Limit:    input.Limit,
		})
		if err != nil {
			return "", err
		}

		startLine := input.Offset
		lines := strings.Split(fileCt.Content, "\n")
		var b strings.Builder
		for i, line := range lines {
			if i < len(lines)-1 {
				fmt.Fprintf(&b, "%6d\t%s\n", startLine+i, line)
			} else {
				fmt.Fprintf(&b, "%6d\t%s", startLine+i, line)
			}

		}
		return b.String(), nil
	})
}

type writeFileArgs struct {
	// FilePath is the path to the file to write.
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to write"`

	// Content is the content to write to the file.
	Content string `json:"content" jsonschema:"description=The content to write to the file"`
}

func newWriteFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameWriteFile)
	d, err := selectToolDesc(desc, WriteFileToolDesc, WriteFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input writeFileArgs) (string, error) {
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
	// FilePath is the path to the file to modify.
	FilePath string `json:"file_path" jsonschema:"description=The path to the file to modify"`

	// OldString is the text to replace.
	OldString string `json:"old_string" jsonschema:"description=The text to replace"`

	// NewString is the text to replace it with.
	NewString string `json:"new_string" jsonschema:"description=The text to replace it with (must be different from old_string)"`

	// ReplaceAll indicates whether to replace all occurrences of old_string.
	ReplaceAll bool `json:"replace_all" jsonschema:"description=Replace all occurrences of old_string (default false),default=false"`
}

func newEditFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameEditFile)
	d, err := selectToolDesc(desc, EditFileToolDesc, EditFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input editFileArgs) (string, error) {
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

func newGlobTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameGlob)
	d, err := selectToolDesc(desc, GlobToolDesc, GlobToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input globArgs) (string, error) {
		infos, err := fs.GlobInfo(ctx, &filesystem.GlobInfoRequest{
			Pattern: input.Pattern,
			Path:    input.Path,
		})
		if err != nil {
			return "", err
		}
		if len(infos) == 0 {
			return noFilesFound, nil
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

	// Context is the number of lines to show before and after each match.
	// Only applicable when output_mode is "content".
	Context *int `json:"-C,omitempty" jsonschema:"description=Number of lines to show before and after each match (rg -C). Requires output_mode: 'content'\\, ignored otherwise."`

	// BeforeLines is the number of lines to show before each match.
	// Only applicable when output_mode is "content".
	BeforeLines *int `json:"-B,omitempty" jsonschema:"description=Number of lines to show before each match (rg -B). Requires output_mode: 'content'\\, ignored otherwise."`

	// AfterLines is the number of lines to show after each match.
	// Only applicable when output_mode is "content".
	AfterLines *int `json:"-A,omitempty" jsonschema:"description=Number of lines to show after each match (rg -A). Requires output_mode: 'content'\\, ignored otherwise."`

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

func newGrepTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameGrep)
	d, err := selectToolDesc(desc, GrepToolDesc, GrepToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input grepArgs) (string, error) {
		// Extract string parameters
		path := valueOrDefault(input.Path, "")
		glob := valueOrDefault(input.Glob, "")
		fileType := valueOrDefault(input.FileType, "")
		var beforeLines, afterLines int

		if input.Context != nil {
			beforeLines = valueOrDefault(input.Context, 0)
			afterLines = valueOrDefault(input.Context, 0)
		} else {
			// Extract context parameters
			beforeLines = valueOrDefault(input.BeforeLines, 0)
			afterLines = valueOrDefault(input.AfterLines, 0)
		}

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
			FileType:        fileType,
			CaseInsensitive: caseInsensitive,
			AfterLines:      afterLines,
			BeforeLines:     beforeLines,
			EnableMultiline: enableMultiline,
		})
		if err != nil {
			return "", err
		}

		sort.SliceStable(matches, func(i, j int) bool {
			return filepath.Base(matches[i].Path) < filepath.Base(matches[j].Path)
		})

		switch input.OutputMode {
		case "content":
			matches = applyPagination(matches, offset, headLimit)
			return formatContentMatches(matches, valueOrDefault(input.ShowLineNumbers, true)), nil

		case "count":
			return formatCountMatches(matches, offset, headLimit), nil

		case "files_with_matches":
			return formatFileMatches(matches, offset, headLimit), nil

		default:
			return formatFileMatches(matches, offset, headLimit), nil
		}
	})
}

type executeArgs struct {
	Command string `json:"command"`
}

func newExecuteTool(sb filesystem.Shell, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameExecute)
	d, err := selectToolDesc(desc, ExecuteToolDesc, ExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input executeArgs) (string, error) {
		result, err := sb.Execute(ctx, &filesystem.ExecuteRequest{
			Command: input.Command,
		})
		if err != nil {
			return "", err
		}

		return convExecuteResponse(result), nil
	})
}

func newStreamingExecuteTool(sb filesystem.StreamingShell, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameExecute)
	d, err := selectToolDesc(desc, ExecuteToolDesc, ExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferStreamTool(toolName, d, func(ctx context.Context, input executeArgs) (*schema.StreamReader[string], error) {
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

func applyPagination[T any](items []T, offset, headLimit int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []T{}
	}
	items = items[offset:]

	if headLimit > 0 && headLimit < len(items) {
		items = items[:headLimit]
	}
	return items
}

func formatFileMatches(matches []filesystem.GrepMatch, offset, headLimit int) string {
	if len(matches) == 0 {
		return noFilesFound
	}
	seen := make(map[string]bool)
	var uniquePaths []string
	for _, match := range matches {
		if !seen[match.Path] {
			seen[match.Path] = true
			uniquePaths = append(uniquePaths, match.Path)
		}
	}
	totalFiles := len(uniquePaths)
	uniquePaths = applyPagination(uniquePaths, offset, headLimit)

	fileWord := "files"
	if totalFiles == 1 {
		fileWord = "file"
	}
	return fmt.Sprintf("Found %d %s\n%s", totalFiles, fileWord, strings.Join(uniquePaths, "\n"))
}

func formatContentMatches(matches []filesystem.GrepMatch, showLineNum bool) string {
	if len(matches) == 0 {
		return noMatchesFound
	}
	var b strings.Builder
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
	return strings.TrimSuffix(b.String(), "\n")
}

func formatCountMatches(matches []filesystem.GrepMatch, offset, headLimit int) string {
	countMap := make(map[string]int)
	for _, match := range matches {
		countMap[match.Path]++
	}

	var paths []string
	for path := range countMap {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	totalOccurrences := len(matches)
	totalFiles := len(paths)

	occurrenceWord := "occurrences"
	if totalOccurrences == 1 {
		occurrenceWord = "occurrence"
	}
	fileWord := "files"
	if totalFiles == 1 {
		fileWord = "file"
	}

	if totalOccurrences == 0 {
		return fmt.Sprintf("%s\n\nFound %d total %s across %d %s.", noMatchesFound, totalOccurrences, occurrenceWord, totalFiles, fileWord)
	}

	paths = applyPagination(paths, offset, headLimit)

	var b strings.Builder
	for _, path := range paths {
		b.WriteString(path)
		b.WriteString(":")
		b.WriteString(strconv.Itoa(countMap[path]))
		b.WriteString("\n")
	}
	result := strings.TrimSuffix(b.String(), "\n")
	return fmt.Sprintf("%s\n\nFound %d total %s across %d %s.", result, totalOccurrences, occurrenceWord, totalFiles, fileWord)
}

// selectToolDesc returns the custom description if provided, otherwise selects the appropriate
// i18n description based on the current language setting.
func selectToolDesc(customDesc string, defaultEnglish, defaultChinese string) (string, error) {
	if customDesc != "" {
		return customDesc, nil
	}
	return internal.SelectPrompt(internal.I18nPrompts{
		English: defaultEnglish,
		Chinese: defaultChinese,
	}), nil
}

// selectToolName returns the custom tool name if provided, otherwise returns the default name.
func selectToolName(customName string, defaultName string) string {
	if customName != "" {
		return customName
	}
	return defaultName
}
