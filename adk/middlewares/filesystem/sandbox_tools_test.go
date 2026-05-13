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
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// sandboxCapturingBackend wraps InMemoryBackend and captures the sandbox name from context on each call.
type sandboxCapturingBackend struct {
	*filesystem.InMemoryBackend
	lastSandboxName string
}

func (b *sandboxCapturingBackend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	b.lastSandboxName = filesystem.SandboxFromContext(ctx)
	return b.InMemoryBackend.LsInfo(ctx, req)
}

func (b *sandboxCapturingBackend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	b.lastSandboxName = filesystem.SandboxFromContext(ctx)
	return b.InMemoryBackend.Read(ctx, req)
}

func (b *sandboxCapturingBackend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	b.lastSandboxName = filesystem.SandboxFromContext(ctx)
	return b.InMemoryBackend.Write(ctx, req)
}

func (b *sandboxCapturingBackend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	b.lastSandboxName = filesystem.SandboxFromContext(ctx)
	return b.InMemoryBackend.Edit(ctx, req)
}

func (b *sandboxCapturingBackend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	b.lastSandboxName = filesystem.SandboxFromContext(ctx)
	return b.InMemoryBackend.GlobInfo(ctx, req)
}

func (b *sandboxCapturingBackend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	b.lastSandboxName = filesystem.SandboxFromContext(ctx)
	return b.InMemoryBackend.GrepRaw(ctx, req)
}

// sandboxCapturingShell captures the sandbox name from context on Execute.
type sandboxCapturingShell struct {
	lastSandboxName string
}

func (s *sandboxCapturingShell) Execute(ctx context.Context, _ *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	s.lastSandboxName = filesystem.SandboxFromContext(ctx)
	exitCode := 0
	return &filesystem.ExecuteResponse{Output: "ok", ExitCode: &exitCode}, nil
}

// sandboxCapturingStreamingShell captures the sandbox name from context on ExecuteStreaming.
type sandboxCapturingStreamingShell struct {
	lastSandboxName string
}

func (s *sandboxCapturingStreamingShell) ExecuteStreaming(ctx context.Context, _ *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	s.lastSandboxName = filesystem.SandboxFromContext(ctx)
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](1)
	go func() {
		exitCode := 0
		sw.Send(&filesystem.ExecuteResponse{Output: "streaming ok", ExitCode: &exitCode}, nil)
		sw.Close()
	}()
	return sr, nil
}

func setupSandboxTestBackend() *sandboxCapturingBackend {
	backend := filesystem.NewInMemoryBackend()
	ctx := context.Background()
	backend.Write(ctx, &filesystem.WriteRequest{FilePath: "/test.txt", Content: "hello world"})
	return &sandboxCapturingBackend{InMemoryBackend: backend}
}

func invokeSandboxTool(t *testing.T, bt tool.BaseTool, input string) (string, error) {
	t.Helper()
	return bt.(tool.InvokableTool).InvokableRun(context.Background(), input)
}

func TestSandboxTools_ContextPropagation(t *testing.T) {
	backend := setupSandboxTestBackend()
	const sandboxName = "test-sandbox"

	t.Run("ls", func(t *testing.T) {
		bt, err := newSandboxLsTool(backend, "", "")
		require.NoError(t, err)
		_, err = invokeSandboxTool(t, bt, `{"path": "/", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, backend.lastSandboxName)
	})

	t.Run("read_file", func(t *testing.T) {
		bt, err := newSandboxReadFileTool(backend, "", "")
		require.NoError(t, err)
		_, err = invokeSandboxTool(t, bt, `{"file_path": "/test.txt", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, backend.lastSandboxName)
	})

	t.Run("write_file", func(t *testing.T) {
		bt, err := newSandboxWriteFileTool(backend, "", "")
		require.NoError(t, err)
		_, err = invokeSandboxTool(t, bt, `{"file_path": "/new.txt", "content": "data", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, backend.lastSandboxName)
	})

	t.Run("edit_file", func(t *testing.T) {
		bt, err := newSandboxEditFileTool(backend, "", "")
		require.NoError(t, err)
		_, err = invokeSandboxTool(t, bt, `{"file_path": "/test.txt", "old_string": "hello", "new_string": "hi", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, backend.lastSandboxName)
	})

	t.Run("glob", func(t *testing.T) {
		bt, err := newSandboxGlobTool(backend, "", "")
		require.NoError(t, err)
		_, err = invokeSandboxTool(t, bt, `{"pattern": "*.txt", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, backend.lastSandboxName)
	})

	t.Run("grep", func(t *testing.T) {
		bt, err := newSandboxGrepTool(backend, "", "")
		require.NoError(t, err)
		_, err = invokeSandboxTool(t, bt, `{"pattern": "hello", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, backend.lastSandboxName)
	})

	t.Run("execute", func(t *testing.T) {
		shell := &sandboxCapturingShell{}
		bt, err := newSandboxExecuteTool(shell, "", "")
		require.NoError(t, err)
		result, err := invokeSandboxTool(t, bt, `{"command": "echo hi", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		assert.Equal(t, sandboxName, shell.lastSandboxName)
		assert.Contains(t, result, "ok")
	})

	t.Run("streaming_execute", func(t *testing.T) {
		shell := &sandboxCapturingStreamingShell{}
		bt, err := newSandboxStreamingExecuteTool(shell, "", "")
		require.NoError(t, err)

		streamTool, ok := bt.(tool.StreamableTool)
		require.True(t, ok, "expected StreamableTool interface")

		sr, err := streamTool.StreamableRun(context.Background(), `{"command": "echo hi", "sandbox_name": "`+sandboxName+`"}`)
		require.NoError(t, err)
		defer sr.Close()

		var chunks []string
		for {
			chunk, recvErr := sr.Recv()
			if recvErr != nil {
				break
			}
			chunks = append(chunks, chunk)
		}

		assert.Equal(t, sandboxName, shell.lastSandboxName)
		require.NotEmpty(t, chunks)
		assert.Contains(t, chunks[0], "streaming ok")
	})
}

func TestSandboxTools_EmptySandboxName(t *testing.T) {
	backend := setupSandboxTestBackend()
	bt, err := newSandboxLsTool(backend, "", "")
	require.NoError(t, err)
	_, err = invokeSandboxTool(t, bt, `{"path": "/", "sandbox_name": ""}`)
	require.NoError(t, err)
	assert.Equal(t, "", backend.lastSandboxName)
}

func TestSandboxTools_SchemaContainsSandboxName(t *testing.T) {
	backend := setupSandboxTestBackend()
	ctx := context.Background()

	tools := []struct {
		name string
		tool tool.BaseTool
	}{
		{"ls", must(newSandboxLsTool(backend, "", ""))},
		{"read_file", must(newSandboxReadFileTool(backend, "", ""))},
		{"write_file", must(newSandboxWriteFileTool(backend, "", ""))},
		{"edit_file", must(newSandboxEditFileTool(backend, "", ""))},
		{"glob", must(newSandboxGlobTool(backend, "", ""))},
		{"grep", must(newSandboxGrepTool(backend, "", ""))},
		{"execute", must(newSandboxExecuteTool(&sandboxCapturingShell{}, "", ""))},
		{"streaming_execute", must(newSandboxStreamingExecuteTool(&sandboxCapturingStreamingShell{}, "", ""))},
	}

	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			info, err := tt.tool.Info(ctx)
			require.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			_, ok := js.Properties.Get("sandbox_name")
			assert.True(t, ok, "expected sandbox_name in schema for %s, got properties: %v", tt.name, js.Properties)
		})
	}
}

func TestNonSandboxTools_SchemaLacksSandboxName(t *testing.T) {
	backend := setupSandboxTestBackend()
	ctx := context.Background()

	tools := []struct {
		name string
		tool tool.BaseTool
	}{
		{"ls", must(newLsTool(backend, "", ""))},
		{"read_file", must(newReadFileTool(backend, "", ""))},
		{"write_file", must(newWriteFileTool(backend, "", ""))},
		{"edit_file", must(newEditFileTool(backend, "", ""))},
		{"glob", must(newGlobTool(backend, "", ""))},
		{"grep", must(newGrepTool(backend, "", ""))},
	}

	for _, tt := range tools {
		t.Run(tt.name, func(t *testing.T) {
			info, err := tt.tool.Info(ctx)
			require.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			_, ok := js.Properties.Get("sandbox_name")
			assert.False(t, ok, "unexpected sandbox_name in schema for %s", tt.name)
		})
	}
}

func TestGetFilesystemTools_EnableMultiSandboxBackend(t *testing.T) {
	backend := setupSandboxTestBackend()
	ctx := context.Background()

	t.Run("sandbox tools have sandbox_name in schema", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend:                   backend,
			Shell:                     &sandboxCapturingShell{},
			EnableMultiSandboxBackend: true,
		}
		tools, err := getFilesystemTools(ctx, config)
		require.NoError(t, err)
		require.NotEmpty(t, tools)

		for _, bt := range tools {
			info, err := bt.Info(ctx)
			require.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			_, ok := js.Properties.Get("sandbox_name")
			assert.True(t, ok, "expected sandbox_name for tool %s", info.Name)
		}
	})

	t.Run("non-sandbox tools lack sandbox_name in schema", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend:                   backend,
			Shell:                     &sandboxCapturingShell{},
			EnableMultiSandboxBackend: false,
		}
		tools, err := getFilesystemTools(ctx, config)
		require.NoError(t, err)
		require.NotEmpty(t, tools)

		for _, bt := range tools {
			info, err := bt.Info(ctx)
			require.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			_, ok := js.Properties.Get("sandbox_name")
			assert.False(t, ok, "unexpected sandbox_name for tool %s", info.Name)
		}
	})

	t.Run("sandbox streaming tools have sandbox_name in schema", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend:                   backend,
			StreamingShell:            &sandboxCapturingStreamingShell{},
			EnableMultiSandboxBackend: true,
		}
		tools, err := getFilesystemTools(ctx, config)
		require.NoError(t, err)
		require.NotEmpty(t, tools)

		for _, bt := range tools {
			info, err := bt.Info(ctx)
			require.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			_, ok := js.Properties.Get("sandbox_name")
			assert.True(t, ok, "expected sandbox_name for tool %s", info.Name)
		}
	})

	t.Run("non-sandbox streaming tools lack sandbox_name in schema", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend:                   backend,
			StreamingShell:            &sandboxCapturingStreamingShell{},
			EnableMultiSandboxBackend: false,
		}
		tools, err := getFilesystemTools(ctx, config)
		require.NoError(t, err)
		require.NotEmpty(t, tools)

		for _, bt := range tools {
			info, err := bt.Info(ctx)
			require.NoError(t, err)
			js, err := info.ParamsOneOf.ToJSONSchema()
			require.NoError(t, err)
			_, ok := js.Properties.Get("sandbox_name")
			assert.False(t, ok, "unexpected sandbox_name for tool %s", info.Name)
		}
	})
}

func must(bt tool.BaseTool, err error) tool.BaseTool {
	if err != nil {
		panic(err)
	}
	return bt
}
