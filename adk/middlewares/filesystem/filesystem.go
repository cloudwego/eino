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
	"strconv"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
)

// Config is the configuration for the filesystem middleware
type Config struct {
	// Backend provides filesystem operations used by tools and offloading
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
}

func (c *Config) Validate() error {
	if c == nil {
		return errors.New("config should not be nil")
	}
	if c.Backend == nil {
		return errors.New("Backend should not be nil")
	}
	return nil
}

// NewMiddleware constructs and returns the filesystem middleware.
func NewMiddleware(ctx context.Context, config *Config) (adk.AgentMiddleware, error) {
	err := config.Validate()
	if err != nil {
		return adk.AgentMiddleware{}, err
	}
	ts, err := getFilesystemTools(ctx, config)
	if err != nil {
		return adk.AgentMiddleware{}, err
	}

	systemPrompt := ToolsSystemPrompt
	if config.CustomSystemPrompt != nil {
		systemPrompt = *config.CustomSystemPrompt
	}

	m := adk.AgentMiddleware{
		AdditionalInstruction: systemPrompt,
		AdditionalTools:       ts,
	}

	if !config.WithoutLargeToolResultOffloading {
		m.WrapToolCall = newToolResultOffloading(ctx, &toolResultOffloadingConfig{
			FS:            config.Backend,
			TokenLimit:    config.LargeToolResultOffloadingTokenLimit,
			PathGenerator: config.LargeToolResultOffloadingPathGen,
		})
	}

	return m, nil
}

func getFilesystemTools(ctx context.Context, validatedConfig *Config) ([]tool.BaseTool, error) {
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

	//execTool, err := newExecuteTool(fs, customToolDescriptions["execute"])
	//if err != nil {
	//	return nil, err
	//}
	//tools = append(tools, execTool)

	return tools, nil
}

type lsArgs struct {
	Path string `json:"path"`
}

func newLsTool(fs Backend, desc *string) (tool.BaseTool, error) {
	d := ListFilesToolDesc
	if desc != nil {
		d = *desc
	}
	return utils.InferTool("ls", d, func(ctx context.Context, input lsArgs) (string, error) {
		p := input.Path
		if p == "" {
			p = "/"
		}
		infos, err := fs.LsInfo(ctx, &LsInfoRequest{Path: p})
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

func newReadFileTool(fs Backend, desc *string) (tool.BaseTool, error) {
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
		return fs.Read(ctx, &ReadRequest{
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

func newWriteFileTool(fs Backend, desc *string) (tool.BaseTool, error) {
	d := WriteFileToolDesc
	if desc != nil {
		d = *desc
	}
	return utils.InferTool("write_file", d, func(ctx context.Context, input writeFileArgs) (string, error) {
		err := fs.Write(ctx, &WriteRequest{
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

func newEditFileTool(fs Backend, desc *string) (tool.BaseTool, error) {
	d := EditFileToolDesc
	if desc != nil {
		d = *desc
	}
	return utils.InferTool("edit_file", d, func(ctx context.Context, input editFileArgs) (string, error) {
		err := fs.Edit(ctx, &EditRequest{
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

func newGlobTool(fs Backend, desc *string) (tool.BaseTool, error) {
	d := GlobToolDesc
	if desc != nil {
		d = *desc
	}
	return utils.InferTool("glob", d, func(ctx context.Context, input globArgs) (string, error) {
		p := input.Path
		if p == "" {
			p = "/"
		}
		infos, err := fs.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: input.Pattern,
			Path:    p,
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

func newGrepTool(fs Backend, desc *string) (tool.BaseTool, error) {
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
		matches, err := fs.GrepRaw(ctx, &GrepRequest{
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
