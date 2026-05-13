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

package filesystem

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/schema"
)

// sandbox-prefixed args structs for multi-sandbox mode.
// Each embeds the original args and adds a SandboxName field
// that is exposed to the model in the tool JSON schema.

type sandboxLsArgs struct {
	lsArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxReadFileArgs struct {
	readFileArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxMultiModalReadFileArgs struct {
	multiModalReadFileArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxWriteFileArgs struct {
	writeFileArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxEditFileArgs struct {
	editFileArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxGlobArgs struct {
	globArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxGrepArgs struct {
	grepArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

type sandboxExecuteArgs struct {
	executeArgs
	SandboxName string `json:"sandbox_name" jsonschema:"description=The target sandbox name to operate on"`
}

func newSandboxLsTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameLs)
	d, err := selectToolDesc(desc, ListFilesToolDesc, ListFilesToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxLsArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return lsHandler(ctx, fs, input.lsArgs)
	})
}

func newSandboxReadFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameReadFile)
	d, err := selectToolDesc(desc, ReadFileToolDesc, ReadFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxReadFileArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return readFileHandler(ctx, fs, input.readFileArgs)
	})
}

func newSandboxMultiModalReadFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	er, ok := fs.(filesystem.MultiModalReader)
	if !ok {
		return nil, fmt.Errorf("UseMultiModalRead is enabled, but backend (type %T) does not implement filesystem.MultiModalReader interface. "+
			"Either implement the MultiModalReader interface on your backend, or set UseMultiModalRead to false", fs)
	}
	toolName := selectToolName(name, ToolNameReadFile)
	d, err := selectToolDesc(desc, ReadFileToolDesc, ReadFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	if desc == "" {
		d += internal.SelectPrompt(internal.I18nPrompts{
			English: EnhancedReadFileDescSuffix,
			Chinese: EnhancedReadFileDescSuffixChinese,
		})
	}

	return utils.InferEnhancedTool(toolName, d, func(ctx context.Context, input sandboxMultiModalReadFileArgs) (*schema.ToolResult, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return multiModalReadFileHandler(ctx, er, input.multiModalReadFileArgs)
	})
}

func newSandboxWriteFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameWriteFile)
	d, err := selectToolDesc(desc, WriteFileToolDesc, WriteFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxWriteFileArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return writeFileHandler(ctx, fs, input.writeFileArgs)
	})
}

func newSandboxEditFileTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameEditFile)
	d, err := selectToolDesc(desc, EditFileToolDesc, EditFileToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxEditFileArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return editFileHandler(ctx, fs, input.editFileArgs)
	})
}

func newSandboxGlobTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameGlob)
	d, err := selectToolDesc(desc, GlobToolDesc, GlobToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxGlobArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return globHandler(ctx, fs, input.globArgs)
	})
}

func newSandboxGrepTool(fs filesystem.Backend, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameGrep)
	d, err := selectToolDesc(desc, GrepToolDesc, GrepToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxGrepArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return grepHandler(ctx, fs, input.grepArgs)
	})
}

func newSandboxExecuteTool(sb filesystem.Shell, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameExecute)
	d, err := selectToolDesc(desc, ExecuteToolDesc, ExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferTool(toolName, d, func(ctx context.Context, input sandboxExecuteArgs) (string, error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return executeHandler(ctx, sb, input.executeArgs)
	})
}

func newSandboxStreamingExecuteTool(sb filesystem.StreamingShell, name string, desc string) (tool.BaseTool, error) {
	toolName := selectToolName(name, ToolNameExecute)
	d, err := selectToolDesc(desc, ExecuteToolDesc, ExecuteToolDescChinese)
	if err != nil {
		return nil, err
	}
	return utils.InferStreamTool(toolName, d, func(ctx context.Context, input sandboxExecuteArgs) (*schema.StreamReader[string], error) {
		ctx = filesystem.WithSandboxName(ctx, input.SandboxName)
		return streamingExecuteHandler(ctx, sb, input.executeArgs)
	})
}
