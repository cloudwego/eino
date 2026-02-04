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
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Config is the configuration for the filesystem middleware
type Config struct {
	// Backend provides filesystem operations used by tools and offloading.
	// If the Backend also implements ShellBackend, an additional execute tool
	// will be registered to support shell command execution.
	// required
	Backend Backend

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
		systemPrompt = ToolsSystemPrompt
		_, ok1 := config.Backend.(filesystem.StreamingShellBackend)
		_, ok2 := config.Backend.(filesystem.ShellBackend)
		if ok1 || ok2 {
			systemPrompt += ExecuteToolsSystemPrompt
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
		systemPrompt = ToolsSystemPrompt
		_, ok1 := config.Backend.(filesystem.StreamingShellBackend)
		_, ok2 := config.Backend.(filesystem.ShellBackend)
		if ok1 || ok2 {
			systemPrompt += ExecuteToolsSystemPrompt
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

	if sb, ok := validatedConfig.Backend.(filesystem.StreamingShellBackend); ok {
		var executeTool tool.BaseTool
		executeTool, err = newStreamingExecuteTool(sb, validatedConfig.CustomExecuteToolDesc)
		if err != nil {
			return nil, err
		}
		tools = append(tools, executeTool)
	} else if sb, ok := validatedConfig.Backend.(filesystem.ShellBackend); ok {
		var executeTool tool.BaseTool
		executeTool, err = newExecuteTool(sb, validatedConfig.CustomExecuteToolDesc)
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
	d := ListFilesToolDesc
	if desc != nil {
		d = *desc
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
	FilePath string `json:"file_path"`
	Offset   int    `json:"offset"`
	Limit    int    `json:"limit"`
}

func newReadFileTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d := ReadFileToolDesc
	if desc != nil {
		d = *desc
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
	FilePath string `json:"file_path"`
	Content  string `json:"content"`
}

func newWriteFileTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d := WriteFileToolDesc
	if desc != nil {
		d = *desc
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
	FilePath   string `json:"file_path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func newEditFileTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d := EditFileToolDesc
	if desc != nil {
		d = *desc
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
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func newGlobTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d := GlobToolDesc
	if desc != nil {
		d = *desc
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
	Pattern    string  `json:"pattern"`
	Path       *string `json:"path,omitempty"`
	Glob       *string `json:"glob,omitempty"`
	OutputMode string  `json:"output_mode" jsonschema:"enum=files_with_matches,enum=content,enum=count"`
}

func newGrepTool(fs filesystem.Backend, desc *string) (tool.BaseTool, error) {
	d := GrepToolDesc
	if desc != nil {
		d = *desc
	}
	return utils.InferTool("grep", d, func(ctx context.Context, input grepArgs) (string, error) {
		var path, glob string
		if input.Path != nil {
			path = *input.Path
		}
		if input.Glob != nil {
			glob = *input.Glob
		}
		matches, err := fs.GrepRaw(ctx, &filesystem.GrepRequest{
			Pattern: input.Pattern,
			Path:    path,
			Glob:    glob,
		})
		if err != nil {
			return "", err
		}
		switch input.OutputMode {
		case "count":
			return strconv.Itoa(len(matches)), nil
		case "content":
			var b strings.Builder
			for _, m := range matches {
				b.WriteString(m.Path)
				b.WriteString(":")
				b.WriteString(strconv.Itoa(m.Line))
				b.WriteString(":")
				b.WriteString(m.Content)
				b.WriteString("\n")
			}
			return b.String(), nil
		default:
			// default by files_with_matches
			seen := map[string]struct{}{}
			var files []string
			for _, m := range matches {
				if _, ok := seen[m.Path]; !ok {
					files = append(files, m.Path)
					seen[m.Path] = struct{}{}
				}
			}
			return strings.Join(files, "\n"), nil
		}
	})
}

type executeArgs struct {
	Command string `json:"command"`
}

func newExecuteTool(sb filesystem.ShellBackend, desc *string) (tool.BaseTool, error) {
	d := ExecuteToolDesc
	if desc != nil {
		d = *desc
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

func newStreamingExecuteTool(sb filesystem.StreamingShellBackend, desc *string) (tool.BaseTool, error) {
	d := ExecuteToolDesc
	if desc != nil {
		d = *desc
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
			for {
				chunk, recvErr := result.Recv()
				if recvErr == io.EOF {
					break
				}
				if recvErr != nil {
					sw.Send("", recvErr)
					break
				}

				if str := convExecuteResponse(chunk); str != "" {
					sw.Send(str, nil)
				}
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
		parts = append(parts, fmt.Sprintf("[Output was truncated due to size limits]"))
	}

	return strings.Join(parts, "\n")
}
