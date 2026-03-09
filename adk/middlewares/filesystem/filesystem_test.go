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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// setupTestBackend creates a test backend with some initial files
func setupTestBackend() *filesystem.InMemoryBackend {
	backend := filesystem.NewInMemoryBackend()
	ctx := context.Background()

	// Create test files
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/file1.txt",
		Content:  "line1\nline2\nline3\nline4\nline5",
	})
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/file2.go",
		Content:  "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}",
	})
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/dir1/file3.txt",
		Content:  "hello world\nfoo bar\nhello again",
	})
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/dir1/file4.py",
		Content:  "print('hello')\nprint('world')",
	})
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/dir2/file5.go",
		Content:  "package test\n\nfunc test() {}",
	})

	return backend
}

// invokeTool is a helper to invoke a tool with JSON input
func invokeTool(_ *testing.T, bt tool.BaseTool, input string) (string, error) {
	ctx := context.Background()
	result, err := bt.(tool.InvokableTool).InvokableRun(ctx, input)
	if err != nil {
		return "", err
	}
	return result, nil
}

func TestLsTool(t *testing.T) {
	backend := setupTestBackend()
	lsTool, err := newLsTool(backend, "", "")
	if err != nil {
		t.Fatalf("Failed to create ls tool: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected []string // expected paths in output
	}{
		{
			name:     "list root",
			input:    `{"path": "/"}`,
			expected: []string{"file1.txt", "file2.go", "dir1", "dir2"},
		},
		{
			name:     "list empty path (defaults to root)",
			input:    `{"path": ""}`,
			expected: []string{"file1.txt", "file2.go", "dir1", "dir2"},
		},
		{
			name:     "list dir1",
			input:    `{"path": "/dir1"}`,
			expected: []string{"file3.txt", "file4.py"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := invokeTool(t, lsTool, tt.input)
			if err != nil {
				t.Fatalf("ls tool failed: %v", err)
			}

			for _, expectedPath := range tt.expected {
				if !strings.Contains(result, expectedPath) {
					t.Errorf("Expected output to contain %q, got: %s", expectedPath, result)
				}
			}
		})
	}
}

func TestReadFileTool(t *testing.T) {
	backend := setupTestBackend()
	readTool, err := newReadFileTool(backend, "", "")
	if err != nil {
		t.Fatalf("Failed to create read_file tool: %v", err)
	}

	tests := []struct {
		name        string
		input       string
		expected    string
		shouldError bool
	}{
		{
			name:     "read full file",
			input:    `{"file_path": "/file1.txt", "offset": 0, "limit": 100}`,
			expected: "     1\tline1\n     2\tline2\n     3\tline3\n     4\tline4\n     5\tline5",
		},
		{
			name:     "read with offset",
			input:    `{"file_path": "/file1.txt", "offset": 2, "limit": 2}`,
			expected: "     2\tline2\n     3\tline3",
		},
		{
			name:     "read with default limit",
			input:    `{"file_path": "/file1.txt", "offset": 0, "limit": 0}`,
			expected: "     1\tline1\n     2\tline2\n     3\tline3\n     4\tline4\n     5\tline5",
		},
		{
			name:     "read with negative offset (treated as 0)",
			input:    `{"file_path": "/file1.txt", "offset": -1, "limit": 2}`,
			expected: "     1\tline1\n     2\tline2",
		},
		{
			name:        "read non-existent file",
			input:       `{"file_path": "/nonexistent.txt", "offset": 0, "limit": 10}`,
			shouldError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := invokeTool(t, readTool, tt.input)
			if tt.shouldError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("read_file tool failed: %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestWriteFileTool(t *testing.T) {
	backend := setupTestBackend()
	writeTool, err := newWriteFileTool(backend, "", "")
	if err != nil {
		t.Fatalf("Failed to create write_file tool: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
		isError  bool
	}{
		{
			name:     "write new file",
			input:    `{"file_path": "/newfile.txt", "content": "new content"}`,
			expected: "Updated file /newfile.txt",
		},
		{
			name:     "overwrite existing file",
			input:    `{"file_path": "/file1.txt", "content": "overwritten"}`,
			isError:  false,
			expected: "Updated file /file1.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := invokeTool(t, writeTool, tt.input)
			if tt.isError {
				if err == nil {
					t.Errorf("Expected an error, but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("write_file tool failed: %v", err)
			}
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}

	// Verify the file was actually written
	ctx := context.Background()
	content, err := backend.Read(ctx, &filesystem.ReadRequest{
		FilePath: "/newfile.txt",
		Offset:   0,
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("Failed to read written file: %v", err)
	}
	if content.Content != "new content" {
		t.Errorf("Expected written content to be 'new content', got %q", content)
	}
}

func TestEditFileTool(t *testing.T) {
	backend := setupTestBackend()
	editTool, err := newEditFileTool(backend, "", "")
	if err != nil {
		t.Fatalf("Failed to create edit_file tool: %v", err)
	}

	tests := []struct {
		name         string
		setupFile    string
		setupContent string
		input        string
		expected     string
		shouldError  bool
	}{
		{
			name:         "replace first occurrence",
			setupFile:    "/edit1.txt",
			setupContent: "hello world\nhello again\nhello world",
			input:        `{"file_path": "/edit1.txt", "old_string": "hello again", "new_string": "hi", "replace_all": false}`,
			expected:     "hello world\nhi\nhello world",
		},
		{
			name:         "replace all occurrences",
			setupFile:    "/edit2.txt",
			setupContent: "hello world\nhello again\nhello world",
			input:        `{"file_path": "/edit2.txt", "old_string": "hello", "new_string": "hi", "replace_all": true}`,
			expected:     "hi world\nhi again\nhi world",
		},
		{
			name:         "non-existent file",
			setupFile:    "",
			setupContent: "",
			input:        `{"file_path": "/nonexistent.txt", "old_string": "old", "new_string": "new", "replace_all": false}`,
			shouldError:  true,
		},
		{
			name:         "empty old_string",
			setupFile:    "/edit3.txt",
			setupContent: "content",
			input:        `{"file_path": "/edit3.txt", "old_string": "", "new_string": "new", "replace_all": false}`,
			shouldError:  true,
		},
	}

	ctx := context.Background()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup file if needed
			if tt.setupFile != "" {
				backend.Write(ctx, &filesystem.WriteRequest{
					FilePath: tt.setupFile,
					Content:  tt.setupContent,
				})
			}

			_, err := invokeTool(t, editTool, tt.input)
			if tt.shouldError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("edit_file tool failed: %v", err)
			}
			result, err := backend.Read(ctx, &filesystem.ReadRequest{
				FilePath: tt.setupFile,
				Offset:   0,
				Limit:    0,
			})
			if err != nil {
				t.Fatalf("edit_file tool failed: %v", err)
			}
			if result.Content != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result.Content)
			}
		})
	}
}

func TestGlobTool(t *testing.T) {
	backend := setupTestBackend()
	globTool, err := newGlobTool(backend, "", "")
	if err != nil {
		t.Fatalf("Failed to create glob tool: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{
			name:     "match all .txt files in root",
			input:    `{"pattern": "*.txt", "path": "/"}`,
			expected: []string{"file1.txt"},
		},
		{
			name:     "match all .go files in root",
			input:    `{"pattern": "*.go", "path": "/"}`,
			expected: []string{"file2.go"},
		},
		{
			name:     "match all .txt files in dir1",
			input:    `{"pattern": "*.txt", "path": "/dir1"}`,
			expected: []string{"file3.txt"},
		},
		{
			name:     "match all .py files in dir1",
			input:    `{"pattern": "*.py", "path": "/dir1"}`,
			expected: []string{"file4.py"},
		},

		{
			name:     "empty path defaults to root",
			input:    `{"pattern": "*.go", "path": ""}`,
			expected: []string{"file2.go"},
		},

		{
			name:     "match all .txt files in dir1 in root dir",
			input:    `{"pattern": "/dir1/*.txt", "path": "/"}`,
			expected: []string{"/dir1/file3.txt"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := invokeTool(t, globTool, tt.input)
			if err != nil {
				t.Fatalf("glob tool failed: %v", err)
			}

			for _, expectedPath := range tt.expected {
				if !strings.Contains(result, expectedPath) {
					t.Errorf("Expected output to contain %q, got: %s", expectedPath, result)
				}
			}
		})
	}
}

func TestGrepTool(t *testing.T) {
	backend := setupTestBackend()
	grepTool, err := newGrepTool(backend, "", "")
	if err != nil {
		t.Fatalf("Failed to create grep tool: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
		contains []string
	}{
		{
			name:     "grep with count mode",
			input:    `{"pattern": "hello", "output_mode": "count"}`,
			expected: "/dir1/file3.txt:2\n/dir1/file4.py:1\n/file2.go:1\n\nFound 4 total occurrences across 3 files.", // 2 in file3.txt, 1 in file4.py, 1 in file2.go
		},
		{
			name:     "grep with content mode",
			input:    `{"pattern": "hello", "output_mode": "content"}`,
			contains: []string{"/dir1/file3.txt:1:hello world", "/dir1/file3.txt:3:hello again", "/dir1/file4.py:1:print('hello')"},
		},
		{
			name:     "grep with files_with_matches mode (default)",
			input:    `{"pattern": "hello", "output_mode": "files_with_matches"}`,
			contains: []string{"/dir1/file3.txt", "/dir1/file4.py"},
		},
		{
			name:     "grep with glob filter",
			input:    `{"pattern": "hello", "glob": "*.txt", "output_mode": "count"}`,
			expected: "/dir1/file3.txt:2\n\nFound 2 total occurrences across 1 file.", // only in file3.txt
		},
		{
			name:     "grep  withpath filter",
			input:    `{"pattern": "package", "path": "/dir2", "output_mode": "count"}`,
			expected: "/dir2/file5.go:1\n\nFound 1 total occurrence across 1 file.", // only in dir2/file5.go
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := invokeTool(t, grepTool, tt.input)
			if err != nil {
				t.Fatalf("grep tool failed: %v", err)
			}

			if tt.expected != "" {
				if result != tt.expected {
					t.Errorf("Expected %q, got %q", tt.expected, result)
				}
			}

			for _, expectedStr := range tt.contains {
				if !strings.Contains(result, expectedStr) {
					t.Errorf("Expected output to contain %q, got: %s", expectedStr, result)
				}
			}
		})
	}
}

func TestExecuteTool(t *testing.T) {
	backend := setupTestBackend()

	tests := []struct {
		name        string
		resp        *filesystem.ExecuteResponse
		input       string
		expected    string
		shouldError bool
	}{
		{
			name: "successful command execution",
			resp: &filesystem.ExecuteResponse{
				Output:   "hello world",
				ExitCode: ptrOf(0),
			},
			input:    `{"command": "echo hello world"}`,
			expected: "hello world",
		},
		{
			name: "command with non-zero exit code",
			resp: &filesystem.ExecuteResponse{
				Output:   "error: file not found",
				ExitCode: ptrOf(1),
			},
			input:    `{"command": "cat nonexistent.txt"}`,
			expected: "error: file not found\n[Command failed with exit code 1]",
		},
		{
			name: "command with truncated output",
			resp: &filesystem.ExecuteResponse{
				Output:    "partial output...",
				ExitCode:  ptrOf(0),
				Truncated: true,
			},
			input:    `{"command": "cat largefile.txt"}`,
			expected: "partial output...\n[Output was truncated due to size limits]",
		},
		{
			name: "command with both non-zero exit code and truncated output",
			resp: &filesystem.ExecuteResponse{
				Output:    "error output...",
				ExitCode:  ptrOf(2),
				Truncated: true,
			},
			input:    `{"command": "failing command"}`,
			expected: "error output...\n[Command failed with exit code 2]\n[Output was truncated due to size limits]",
		},
		{
			name: "successful command with no output",
			resp: &filesystem.ExecuteResponse{
				Output:   "",
				ExitCode: ptrOf(0),
			},
			input:    `{"command": "mkdir /tmp/test"}`,
			expected: "[Command executed successfully with no output]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			executeTool, err := newExecuteTool(&mockShellBackend{
				Backend: backend,
				resp:    tt.resp,
			}, "", "")
			assert.NoError(t, err)

			result, err := invokeTool(t, executeTool, tt.input)
			if tt.shouldError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func ptrOf[T any](t T) *T {
	return &t
}

type mockShellBackend struct {
	filesystem.Backend
	resp *filesystem.ExecuteResponse
}

func (m *mockShellBackend) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return m.resp, nil
}

func TestGetFilesystemTools(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("returns 6 tools for regular Backend", func(t *testing.T) {
		tools, err := getFilesystemTools(ctx, &MiddlewareConfig{Backend: backend})
		assert.NoError(t, err)
		assert.Len(t, tools, 6)

		// Verify tool names
		toolNames := make([]string, 0, len(tools))
		for _, to := range tools {
			info, _ := to.Info(ctx)
			toolNames = append(toolNames, info.Name)
		}
		assert.Contains(t, toolNames, "ls")
		assert.Contains(t, toolNames, "read_file")
		assert.Contains(t, toolNames, "write_file")
		assert.Contains(t, toolNames, "edit_file")
		assert.Contains(t, toolNames, "glob")
		assert.Contains(t, toolNames, "grep")
	})

	t.Run("returns 7 tools for Shell", func(t *testing.T) {
		shellBackend := &mockShellBackend{
			Backend: backend,
			resp:    &filesystem.ExecuteResponse{Output: "ok"},
		}
		tools, err := getFilesystemTools(ctx, &MiddlewareConfig{Backend: shellBackend, Shell: shellBackend})
		assert.NoError(t, err)
		assert.Len(t, tools, 7)

		// Verify execute tool is included
		toolNames := make([]string, 0, len(tools))
		for _, to := range tools {
			info, _ := to.Info(ctx)
			toolNames = append(toolNames, info.Name)
		}
		assert.Contains(t, toolNames, "execute")
	})

	t.Run("custom tool descriptions", func(t *testing.T) {
		customLsDesc := "Custom ls description"
		customReadDesc := "Custom read description"

		tools, err := getFilesystemTools(ctx, &MiddlewareConfig{
			Backend:                backend,
			CustomLsToolDesc:       &customLsDesc,
			CustomReadFileToolDesc: &customReadDesc,
		})
		assert.NoError(t, err)
		assert.Len(t, tools, 6)

		// Verify custom descriptions are applied
		for _, to := range tools {
			info, _ := to.Info(ctx)
			if info.Name == "ls" {
				assert.Equal(t, customLsDesc, info.Desc)
			}
			if info.Name == "read_file" {
				assert.Equal(t, customReadDesc, info.Desc)
			}
		}
	})
}

func TestNew(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("nil config returns error", func(t *testing.T) {
		_, err := New(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config should not be nil")
	})

	t.Run("nil backend returns error", func(t *testing.T) {
		_, err := New(ctx, &MiddlewareConfig{Backend: nil})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend should not be nil")
	})

	t.Run("valid config with default settings", func(t *testing.T) {
		m, err := New(ctx, &MiddlewareConfig{Backend: backend})
		assert.NoError(t, err)
		assert.NotNil(t, m)

		fm, ok := m.(*filesystemMiddleware)
		assert.True(t, ok)
		assert.Len(t, fm.additionalTools, 6)
	})

	t.Run("custom system prompt", func(t *testing.T) {
		customPrompt := "Custom system prompt"
		m, err := New(ctx, &MiddlewareConfig{
			Backend:            backend,
			CustomSystemPrompt: &customPrompt,
		})
		assert.NoError(t, err)

		fm, ok := m.(*filesystemMiddleware)
		assert.True(t, ok)
		assert.Equal(t, customPrompt, fm.additionalInstruction)
	})

	t.Run("ShellBackend adds execute tool", func(t *testing.T) {
		shellBackend := &mockShellBackend{
			Backend: backend,
			resp:    &filesystem.ExecuteResponse{Output: "ok"},
		}
		m, err := New(ctx, &MiddlewareConfig{Backend: shellBackend, Shell: shellBackend})
		assert.NoError(t, err)

		fm, ok := m.(*filesystemMiddleware)
		assert.True(t, ok)
		assert.Len(t, fm.additionalTools, 7)
	})
}

func TestFilesystemMiddleware_BeforeAgent(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("adds instruction and tools to context", func(t *testing.T) {
		m, err := New(ctx, &MiddlewareConfig{Backend: backend})
		assert.NoError(t, err)

		runCtx := &adk.ChatModelAgentContext{
			Instruction: "Original instruction",
			Tools:       nil,
		}

		newCtx, newRunCtx, err := m.BeforeAgent(ctx, runCtx)
		assert.NoError(t, err)
		assert.NotNil(t, newCtx)
		assert.NotNil(t, newRunCtx)
		assert.Contains(t, newRunCtx.Instruction, "Original instruction")
		assert.Len(t, newRunCtx.Tools, 6)
	})

	t.Run("nil runCtx returns nil", func(t *testing.T) {
		m, err := New(ctx, &MiddlewareConfig{Backend: backend})
		assert.NoError(t, err)

		newCtx, newRunCtx, err := m.BeforeAgent(ctx, nil)
		assert.NoError(t, err)
		assert.NotNil(t, newCtx)
		assert.Nil(t, newRunCtx)
	})
}

func TestFilesystemMiddleware_WrapInvokableToolCall(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("small result passes through unchanged", func(t *testing.T) {
		m, err := New(ctx, &MiddlewareConfig{Backend: backend})
		assert.NoError(t, err)

		endpoint := func(ctx context.Context, args string, opts ...tool.Option) (string, error) {
			return "small result", nil
		}

		tCtx := &adk.ToolContext{Name: "test_tool", CallID: "call-1"}
		wrapped, err := m.WrapInvokableToolCall(ctx, endpoint, tCtx)
		assert.NoError(t, err)

		result, err := wrapped(ctx, "{}")
		assert.NoError(t, err)
		assert.Equal(t, "small result", result)
	})

}

func TestGrepToolWithSortingAndPagination(t *testing.T) {
	backend := filesystem.NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/zebra.txt",
		Content:  "match1\nmatch2\nmatch3",
	})
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/apple.txt",
		Content:  "match4\nmatch5",
	})
	backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: "/banana.txt",
		Content:  "match6\nmatch7\nmatch8",
	})

	grepTool, err := newGrepTool(backend, "", "")
	assert.NoError(t, err)

	t.Run("files sorted by basename", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches"}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 4, len(lines)) // 1 summary + 3 files
		assert.Contains(t, lines[0], "Found 3 files")
		assert.Contains(t, lines[1], "apple.txt")
		assert.Contains(t, lines[2], "banana.txt")
		assert.Contains(t, lines[3], "zebra.txt")
	})

	t.Run("files_with_matches with offset", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": 1}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 3, len(lines))                // 1 summary + 2 files (pagination applied)
		assert.Contains(t, lines[0], "Found 3 files") // total count before pagination
		assert.Contains(t, lines[1], "banana.txt")
		assert.Contains(t, lines[2], "zebra.txt")
	})

	t.Run("files_with_matches with head_limit", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "head_limit": 2}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 3, len(lines))                // 1 summary + 2 files (pagination applied)
		assert.Contains(t, lines[0], "Found 3 files") // total count before pagination
		assert.Contains(t, lines[1], "apple.txt")
		assert.Contains(t, lines[2], "banana.txt")
	})

	t.Run("files_with_matches with offset and head_limit", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": 1, "head_limit": 1}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 2, len(lines))                // 1 summary + 1 file (pagination applied)
		assert.Contains(t, lines[0], "Found 3 files") // total count before pagination
		assert.Contains(t, lines[1], "banana.txt")
	})

	t.Run("content mode sorted and paginated", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "content", "head_limit": 3}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 3, len(lines))
		assert.Contains(t, lines[0], "apple.txt")
	})

	t.Run("content mode with offset", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "content", "offset": 2, "head_limit": 2}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 2, len(lines))
	})

	t.Run("count mode sorted", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "count"}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 5, len(lines)) // 3 file counts + 1 empty line + 1 summary line
		assert.Contains(t, lines[0], "apple.txt:2")
		assert.Contains(t, lines[1], "banana.txt:3")
		assert.Contains(t, lines[2], "zebra.txt:3")
		assert.Contains(t, lines[4], "Found 8 total occurrences across 3 files.")
	})

	t.Run("count mode with pagination", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "count", "offset": 1, "head_limit": 1}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 3, len(lines)) // 1 file count + 1 empty line + 1 summary line
		assert.Contains(t, lines[0], "banana.txt:3")
		assert.Contains(t, lines[2], "Found 8 total occurrences across 3 files.") // summary shows total before pagination
	})

	t.Run("offset exceeds result count", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": 100}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "Found 3 files") // still shows total count
	})

	t.Run("negative offset treated as zero", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": -5}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 4, len(lines)) // 1 summary + 3 files
	})
}

func TestApplyPagination(t *testing.T) {
	t.Run("basic pagination", func(t *testing.T) {
		items := []string{"a", "b", "c", "d", "e"}
		result := applyPagination(items, 0, 3)
		assert.Equal(t, []string{"a", "b", "c"}, result)
	})

	t.Run("with offset", func(t *testing.T) {
		items := []string{"a", "b", "c", "d", "e"}
		result := applyPagination(items, 2, 2)
		assert.Equal(t, []string{"c", "d"}, result)
	})

	t.Run("offset exceeds length", func(t *testing.T) {
		items := []string{"a", "b", "c"}
		result := applyPagination(items, 10, 5)
		assert.Equal(t, []string{}, result)
	})

	t.Run("negative offset", func(t *testing.T) {
		items := []string{"a", "b", "c"}
		result := applyPagination(items, -1, 2)
		assert.Equal(t, []string{"a", "b"}, result)
	})

	t.Run("zero head limit means no limit", func(t *testing.T) {
		items := []string{"a", "b", "c", "d", "e"}
		result := applyPagination(items, 1, 0)
		assert.Equal(t, []string{"b", "c", "d", "e"}, result)
	})
}

func TestCustomToolNames(t *testing.T) {
	backend := setupTestBackend()
	ctx := context.Background()

	t.Run("custom tool names applied to individual tools", func(t *testing.T) {
		customLsName := "list_files"
		customReadName := "read"
		customWriteName := "write"
		customEditName := "edit"
		customGlobName := "find_files"
		customGrepName := "search"

		lsTool, err := newLsTool(backend, customLsName, "")
		assert.NoError(t, err)
		info, _ := lsTool.Info(ctx)
		assert.Equal(t, "list_files", info.Name)

		readTool, err := newReadFileTool(backend, customReadName, "")
		assert.NoError(t, err)
		info, _ = readTool.Info(ctx)
		assert.Equal(t, "read", info.Name)

		writeTool, err := newWriteFileTool(backend, customWriteName, "")
		assert.NoError(t, err)
		info, _ = writeTool.Info(ctx)
		assert.Equal(t, "write", info.Name)

		editTool, err := newEditFileTool(backend, customEditName, "")
		assert.NoError(t, err)
		info, _ = editTool.Info(ctx)
		assert.Equal(t, "edit", info.Name)

		globTool, err := newGlobTool(backend, customGlobName, "")
		assert.NoError(t, err)
		info, _ = globTool.Info(ctx)
		assert.Equal(t, "find_files", info.Name)

		grepTool, err := newGrepTool(backend, customGrepName, "")
		assert.NoError(t, err)
		info, _ = grepTool.Info(ctx)
		assert.Equal(t, "search", info.Name)
	})

	t.Run("default tool names when custom names not provided", func(t *testing.T) {
		lsTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)
		info, _ := lsTool.Info(ctx)
		assert.Equal(t, ToolNameLs, info.Name)

		readTool, err := newReadFileTool(backend, "", "")
		assert.NoError(t, err)
		info, _ = readTool.Info(ctx)
		assert.Equal(t, ToolNameReadFile, info.Name)

		writeTool, err := newWriteFileTool(backend, "", "")
		assert.NoError(t, err)
		info, _ = writeTool.Info(ctx)
		assert.Equal(t, ToolNameWriteFile, info.Name)

		editTool, err := newEditFileTool(backend, "", "")
		assert.NoError(t, err)
		info, _ = editTool.Info(ctx)
		assert.Equal(t, ToolNameEditFile, info.Name)

		globTool, err := newGlobTool(backend, "", "")
		assert.NoError(t, err)
		info, _ = globTool.Info(ctx)
		assert.Equal(t, ToolNameGlob, info.Name)

		grepTool, err := newGrepTool(backend, "", "")
		assert.NoError(t, err)
		info, _ = grepTool.Info(ctx)
		assert.Equal(t, ToolNameGrep, info.Name)
	})

	t.Run("custom execute tool name", func(t *testing.T) {
		customExecuteName := "run_command"
		shellBackend := &mockShellBackend{
			Backend: backend,
			resp:    &filesystem.ExecuteResponse{Output: "ok"},
		}

		executeTool, err := newExecuteTool(shellBackend, customExecuteName, "")
		assert.NoError(t, err)
		info, _ := executeTool.Info(ctx)
		assert.Equal(t, "run_command", info.Name)
	})

	t.Run("custom tool names via ToolConfig in getFilesystemTools", func(t *testing.T) {
		customLsName := "list_files"
		customReadName := "read"

		tools, err := getFilesystemTools(ctx, &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Name: customLsName,
			},
			ReadFileToolConfig: &ToolConfig{
				Name: customReadName,
			},
		})
		assert.NoError(t, err)

		toolNames := make(map[string]bool)
		for _, to := range tools {
			info, _ := to.Info(ctx)
			toolNames[info.Name] = true
		}

		assert.True(t, toolNames["list_files"])
		assert.True(t, toolNames["read"])
	})

	t.Run("custom tool names via ToolConfig in middleware", func(t *testing.T) {
		customLsName := "list_files"
		customReadName := "read"

		m, err := New(ctx, &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Name: customLsName,
			},
			ReadFileToolConfig: &ToolConfig{
				Name: customReadName,
			},
		})
		assert.NoError(t, err)

		fm, ok := m.(*filesystemMiddleware)
		assert.True(t, ok)

		toolNames := make(map[string]bool)
		for _, to := range fm.additionalTools {
			info, _ := to.Info(ctx)
			toolNames[info.Name] = true
		}

		assert.True(t, toolNames["list_files"])
		assert.True(t, toolNames["read"])
	})
}

func TestSelectToolName(t *testing.T) {
	t.Run("returns custom name when provided", func(t *testing.T) {
		customName := "custom_tool"
		result := selectToolName(customName, "default_tool")
		assert.Equal(t, "custom_tool", result)
	})

	t.Run("returns default name when custom name is nil", func(t *testing.T) {
		result := selectToolName("", "default_tool")
		assert.Equal(t, "default_tool", result)
	})
}

func TestGetOrCreateTool(t *testing.T) {
	backend := setupTestBackend()

	t.Run("returns custom tool when provided", func(t *testing.T) {
		customTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)

		result, err := getOrCreateTool(customTool, func() (tool.BaseTool, error) {
			t.Fatal("createFunc should not be called when custom tool is provided")
			return nil, nil
		})

		assert.NoError(t, err)
		assert.Equal(t, customTool, result)
	})

	t.Run("calls createFunc when custom tool is nil", func(t *testing.T) {
		expectedTool, err := newReadFileTool(backend, "", "")
		assert.NoError(t, err)

		createFuncCalled := false
		result, err := getOrCreateTool(nil, func() (tool.BaseTool, error) {
			createFuncCalled = true
			return expectedTool, nil
		})

		assert.NoError(t, err)
		assert.True(t, createFuncCalled, "createFunc should be called when custom tool is nil")
		assert.Equal(t, expectedTool, result)
	})

	t.Run("returns nil when custom tool is nil and createFunc returns nil", func(t *testing.T) {
		result, err := getOrCreateTool(nil, func() (tool.BaseTool, error) {
			return nil, nil
		})

		assert.NoError(t, err)
		assert.Nil(t, result)
	})

	t.Run("propagates error from createFunc", func(t *testing.T) {
		expectedErr := assert.AnError

		result, err := getOrCreateTool(nil, func() (tool.BaseTool, error) {
			return nil, expectedErr
		})

		assert.Error(t, err)
		assert.Equal(t, expectedErr, err)
		assert.Nil(t, result)
	})
}

func TestCustomTools(t *testing.T) {
	backend := setupTestBackend()
	ctx := context.Background()

	t.Run("custom ls tool is used via ToolConfig", func(t *testing.T) {
		customLsTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			LsToolConfig: &ToolConfig{
				CustomTool: customLsTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, customLsTool, tools[0])
	})

	t.Run("custom read file tool is used via ToolConfig", func(t *testing.T) {
		customReadTool, err := newReadFileTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			ReadFileToolConfig: &ToolConfig{
				CustomTool: customReadTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, customReadTool, tools[0])
	})

	t.Run("custom write file tool is used via ToolConfig", func(t *testing.T) {
		customWriteTool, err := newWriteFileTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			WriteFileToolConfig: &ToolConfig{
				CustomTool: customWriteTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, customWriteTool, tools[0])
	})

	t.Run("custom edit file tool is used via ToolConfig", func(t *testing.T) {
		customEditTool, err := newEditFileTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			EditFileToolConfig: &ToolConfig{
				CustomTool: customEditTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, customEditTool, tools[0])
	})

	t.Run("custom glob tool is used via ToolConfig", func(t *testing.T) {
		customGlobTool, err := newGlobTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			GlobToolConfig: &ToolConfig{
				CustomTool: customGlobTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, customGlobTool, tools[0])
	})

	t.Run("custom grep tool is used via ToolConfig", func(t *testing.T) {
		customGrepTool, err := newGrepTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			GrepToolConfig: &ToolConfig{
				CustomTool: customGrepTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)
		assert.Equal(t, customGrepTool, tools[0])
	})

	t.Run("multiple custom tools can be used together", func(t *testing.T) {
		customLsTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)
		customReadTool, err := newReadFileTool(backend, "", "")
		assert.NoError(t, err)
		customGlobTool, err := newGlobTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			LsToolConfig: &ToolConfig{
				CustomTool: customLsTool,
			},
			ReadFileToolConfig: &ToolConfig{
				CustomTool: customReadTool,
			},
			GlobToolConfig: &ToolConfig{
				CustomTool: customGlobTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 3)
	})

	t.Run("custom tools take precedence over backend", func(t *testing.T) {
		customLsTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				CustomTool: customLsTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		lsToolFound := false
		for _, t := range tools {
			if t == customLsTool {
				lsToolFound = true
				break
			}
		}
		assert.True(t, lsToolFound, "custom ls tool should be in the tools list")
	})

	t.Run("backend tools are created when custom tools not provided", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend: backend,
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Greater(t, len(tools), 0, "should create backend tools when custom tools not provided")
	})
}

func TestToolConfig(t *testing.T) {
	backend := setupTestBackend()
	ctx := context.Background()

	t.Run("use new ToolConfig", func(t *testing.T) {
		customName := "my_ls"
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Name: customName,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 6)

		var lsToolFound bool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == "my_ls" {
				lsToolFound = true
				break
			}
		}
		assert.True(t, lsToolFound)
	})

	t.Run("ToolConfig disabled", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Disable: true,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 5)

		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			assert.NotEqual(t, ToolNameLs, info.Name)
		}
	})

	t.Run("ToolConfig with custom tool", func(t *testing.T) {
		customLsTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				CustomTool: customLsTool,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var lsToolFound bool
		for _, tool := range tools {
			if tool == customLsTool {
				lsToolFound = true
				break
			}
		}
		assert.True(t, lsToolFound)
	})

	t.Run("ToolConfig Desc takes precedence over legacy Desc", func(t *testing.T) {
		customDesc := "new description"
		legacyDesc := "old description"
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Desc: &customDesc,
			},
			CustomLsToolDesc: &legacyDesc,
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var found bool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == ToolNameLs && info.Desc == "new description" {
				found = true
				break
			}
		}
		assert.True(t, found)
	})

	t.Run("legacy Desc field still works", func(t *testing.T) {
		legacyDesc := "legacy description"
		config := &MiddlewareConfig{
			Backend:          backend,
			CustomLsToolDesc: &legacyDesc,
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var found bool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == ToolNameLs && info.Desc == "legacy description" {
				found = true
				break
			}
		}
		assert.True(t, found)
	})

	t.Run("multiple ToolConfig", func(t *testing.T) {
		lsName := "my_ls"
		readName := "my_read"
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Name: lsName,
			},
			ReadFileToolConfig: &ToolConfig{
				Name: readName,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		toolNames := make(map[string]bool)
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			toolNames[info.Name] = true
		}

		assert.True(t, toolNames["my_ls"])
		assert.True(t, toolNames["my_read"])
	})
}

func TestToolConfigEdgeCases(t *testing.T) {
	backend := setupTestBackend()
	ctx := context.Background()

	t.Run("nil ToolConfig.Desc with nil legacyDesc", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Desc: nil,
			},
			CustomLsToolDesc: nil,
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var lsTool tool.BaseTool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == ToolNameLs {
				lsTool = tool
				break
			}
		}
		assert.NotNil(t, lsTool, "ls tool should be created even with nil Desc")
	})

	t.Run("nil ToolConfig.Desc falls back to legacyDesc", func(t *testing.T) {
		legacyDesc := "legacy description from pointer"
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Desc: nil,
			},
			CustomLsToolDesc: &legacyDesc,
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var found bool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == ToolNameLs && info.Desc == "legacy description from pointer" {
				found = true
				break
			}
		}
		assert.True(t, found, "nil ToolConfig.Desc should fall back to legacyDesc")
	})

	t.Run("CustomTool with Disable flag should not create tool", func(t *testing.T) {
		customLsTool, err := newLsTool(backend, "", "")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				CustomTool: customLsTool,
				Disable:    true,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			assert.NotEqual(t, ToolNameLs, info.Name, "disabled tool should not be created even if CustomTool is set")
		}
	})

	t.Run("multiple ToolConfig with conflicting settings", func(t *testing.T) {
		legacyDesc := "legacy ls desc"
		customDesc := "custom desc"
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Name:    "custom_ls",
				Desc:    &customDesc,
				Disable: false,
			},
			CustomLsToolDesc: &legacyDesc,
			ReadFileToolConfig: &ToolConfig{
				Disable: true,
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		hasLsTool := false
		hasReadTool := false

		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == "custom_ls" {
				hasLsTool = true
				assert.Equal(t, "custom desc", info.Desc, "ToolConfig.Desc should take precedence over legacy")
			}
			if info.Name == ToolNameReadFile {
				hasReadTool = true
			}
		}

		assert.True(t, hasLsTool, "ls tool should be created")
		assert.False(t, hasReadTool, "read_file tool should be disabled")
	})

	t.Run("nil ToolConfig with nil legacyDesc creates default tool", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend:          backend,
			LsToolConfig:     nil,
			CustomLsToolDesc: nil,
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var lsTool tool.BaseTool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == ToolNameLs {
				lsTool = tool
				break
			}
		}
		assert.NotNil(t, lsTool, "tool should be created with backend even when config is nil")
	})

	t.Run("empty Name in ToolConfig uses default name", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				Name: "",
			},
		}

		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var lsTool tool.BaseTool
		for _, tool := range tools {
			info, _ := tool.Info(ctx)
			if info.Name == ToolNameLs {
				lsTool = tool
				break
			}
		}
		assert.NotNil(t, lsTool, "tool should use default name when Name is empty")
	})
}

func TestGetFilesystemTools_DisableAllTools(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	config := &MiddlewareConfig{
		Backend:             backend,
		LsToolConfig:        &ToolConfig{Disable: true},
		ReadFileToolConfig:  &ToolConfig{Disable: true},
		WriteFileToolConfig: &ToolConfig{Disable: true},
		EditFileToolConfig:  &ToolConfig{Disable: true},
		GlobToolConfig:      &ToolConfig{Disable: true},
		GrepToolConfig:      &ToolConfig{Disable: true},
	}

	tools, err := getFilesystemTools(ctx, config)
	assert.NoError(t, err)
	assert.Len(t, tools, 0)
}

func TestGetFilesystemTools_StreamingShell(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("returns 7 tools with StreamingShell", func(t *testing.T) {
		mockSS := &mockStreamingShell{}
		tools, err := getFilesystemTools(ctx, &MiddlewareConfig{
			Backend:        backend,
			StreamingShell: mockSS,
		})
		assert.NoError(t, err)
		assert.Len(t, tools, 7)

		toolNames := make([]string, 0, len(tools))
		for _, to := range tools {
			info, _ := to.Info(ctx)
			toolNames = append(toolNames, info.Name)
		}
		assert.Contains(t, toolNames, ToolNameExecute)
	})

	t.Run("StreamingShell takes precedence over Shell", func(t *testing.T) {
		mockSS := &mockStreamingShell{}
		shellBackend := &mockShellBackend{
			Backend: backend,
			resp:    &filesystem.ExecuteResponse{Output: "ok"},
		}

		// When both are set, Validate should fail
		config := &MiddlewareConfig{
			Backend:        backend,
			Shell:          shellBackend,
			StreamingShell: mockSS,
		}
		err := config.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "shell and streaming shell should not be both set")
	})
}

func TestGetFilesystemTools_NilBackend(t *testing.T) {
	ctx := context.Background()

	t.Run("nil backend with shell only returns execute tool", func(t *testing.T) {
		mockSS := &mockStreamingShell{}
		config := &MiddlewareConfig{
			Backend:        nil,
			StreamingShell: mockSS,
		}
		// Validate should fail, but getFilesystemTools itself handles nil backend gracefully
		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		// Only execute tool should be returned since backend is nil
		assert.Len(t, tools, 1)

		info, _ := tools[0].Info(ctx)
		assert.Equal(t, ToolNameExecute, info.Name)
	})

	t.Run("nil backend with regular Shell returns execute tool", func(t *testing.T) {
		mockShell := &mockShellBackend{
			resp: &filesystem.ExecuteResponse{Output: "ok"},
		}
		config := &MiddlewareConfig{
			Backend: nil,
			Shell:   mockShell,
		}
		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 1)

		info, _ := tools[0].Info(ctx)
		assert.Equal(t, ToolNameExecute, info.Name)
	})

	t.Run("nil backend and nil shell returns empty tools", func(t *testing.T) {
		config := &MiddlewareConfig{
			Backend: nil,
		}
		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)
		assert.Len(t, tools, 0)
	})
}

func TestGetFilesystemTools_PartialDisable(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	config := &MiddlewareConfig{
		Backend:            backend,
		LsToolConfig:       &ToolConfig{Disable: true},
		ReadFileToolConfig: &ToolConfig{Disable: true},
	}

	tools, err := getFilesystemTools(ctx, config)
	assert.NoError(t, err)
	assert.Len(t, tools, 4)

	toolNames := make([]string, 0, len(tools))
	for _, to := range tools {
		info, _ := to.Info(ctx)
		toolNames = append(toolNames, info.Name)
	}
	assert.NotContains(t, toolNames, ToolNameLs)
	assert.NotContains(t, toolNames, ToolNameReadFile)
	assert.Contains(t, toolNames, ToolNameWriteFile)
	assert.Contains(t, toolNames, ToolNameEditFile)
	assert.Contains(t, toolNames, ToolNameGlob)
	assert.Contains(t, toolNames, ToolNameGrep)
}

type mockStreamingShell struct{}

func (m *mockStreamingShell) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{
			Output:   "streaming output",
			ExitCode: ptrOf(0),
		}, nil)
	}()
	return sr, nil
}

type mockStreamingShellWithError struct{}

func (m *mockStreamingShellWithError) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	return nil, fmt.Errorf("streaming shell error")
}

type mockStreamingShellWithRecvError struct{}

func (m *mockStreamingShellWithRecvError) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(nil, fmt.Errorf("recv error during streaming"))
	}()
	return sr, nil
}

type mockStreamingShellWithExitCode struct {
	exitCode int
}

func (m *mockStreamingShellWithExitCode) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{
			Output:   "some output",
			ExitCode: ptrOf(m.exitCode),
		}, nil)
	}()
	return sr, nil
}

type mockStreamingShellNoOutput struct{}

func (m *mockStreamingShellNoOutput) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{
			ExitCode: ptrOf(0),
		}, nil)
	}()
	return sr, nil
}

type mockStreamingShellTruncated struct{}

func (m *mockStreamingShellTruncated) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{
			Output:    "partial",
			Truncated: true,
			ExitCode:  ptrOf(0),
		}, nil)
	}()
	return sr, nil
}

type mockStreamingShellNilChunk struct{}

func (m *mockStreamingShellNilChunk) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(nil, nil)
		sw.Send(&filesystem.ExecuteResponse{
			Output:   "after nil",
			ExitCode: ptrOf(0),
		}, nil)
	}()
	return sr, nil
}

func TestNewStreamingExecuteTool(t *testing.T) {
	t.Run("successful streaming execution", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShell{}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		sr, err := st.StreamableRun(context.Background(), `{"command": "echo hello"}`)
		assert.NoError(t, err)
		defer sr.Close()

		var chunks []string
		for {
			chunk, recvErr := sr.Recv()
			if recvErr == io.EOF {
				break
			}
			assert.NoError(t, recvErr)
			chunks = append(chunks, chunk)
		}
		assert.True(t, len(chunks) > 0)
		result := strings.Join(chunks, "")
		assert.Contains(t, result, "streaming output")
	})

	t.Run("streaming execution with ExecuteStreaming error", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShellWithError{}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		_, err = st.StreamableRun(context.Background(), `{"command": "fail"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "streaming shell error")
	})

	t.Run("streaming execution with recv error", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShellWithRecvError{}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		sr, err := st.StreamableRun(context.Background(), `{"command": "echo hello"}`)
		assert.NoError(t, err)
		defer sr.Close()

		var gotError bool
		for {
			_, recvErr := sr.Recv()
			if recvErr == io.EOF {
				break
			}
			if recvErr != nil {
				gotError = true
				assert.Contains(t, recvErr.Error(), "recv error during streaming")
				break
			}
		}
		assert.True(t, gotError)
	})

	t.Run("streaming execution with non-zero exit code", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShellWithExitCode{exitCode: 1}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		sr, err := st.StreamableRun(context.Background(), `{"command": "false"}`)
		assert.NoError(t, err)
		defer sr.Close()

		var chunks []string
		for {
			chunk, recvErr := sr.Recv()
			if recvErr == io.EOF {
				break
			}
			assert.NoError(t, recvErr)
			chunks = append(chunks, chunk)
		}
		result := strings.Join(chunks, "")
		assert.Contains(t, result, "[Command failed with exit code 1]")
	})

	t.Run("streaming execution with zero exit code and no output", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShellNoOutput{}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		sr, err := st.StreamableRun(context.Background(), `{"command": "true"}`)
		assert.NoError(t, err)
		defer sr.Close()

		var chunks []string
		for {
			chunk, recvErr := sr.Recv()
			if recvErr == io.EOF {
				break
			}
			assert.NoError(t, recvErr)
			chunks = append(chunks, chunk)
		}
		result := strings.Join(chunks, "")
		assert.Contains(t, result, "[Command executed successfully with no output]")
	})

	t.Run("streaming execution with truncated output", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShellTruncated{}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		sr, err := st.StreamableRun(context.Background(), `{"command": "cat largefile"}`)
		assert.NoError(t, err)
		defer sr.Close()

		var chunks []string
		for {
			chunk, recvErr := sr.Recv()
			if recvErr == io.EOF {
				break
			}
			assert.NoError(t, recvErr)
			chunks = append(chunks, chunk)
		}
		result := strings.Join(chunks, "")
		assert.Contains(t, result, "partial")
		assert.Contains(t, result, "[Output was truncated due to size limits]")
	})

	t.Run("streaming execution with nil chunk skipped", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShellNilChunk{}, "", "")
		assert.NoError(t, err)

		st := executeTool.(tool.StreamableTool)
		sr, err := st.StreamableRun(context.Background(), `{"command": "echo test"}`)
		assert.NoError(t, err)
		defer sr.Close()

		var chunks []string
		for {
			chunk, recvErr := sr.Recv()
			if recvErr == io.EOF {
				break
			}
			assert.NoError(t, recvErr)
			chunks = append(chunks, chunk)
		}
		result := strings.Join(chunks, "")
		assert.Contains(t, result, "after nil")
	})

	t.Run("streaming execution with custom name and desc", func(t *testing.T) {
		executeTool, err := newStreamingExecuteTool(&mockStreamingShell{}, "custom_execute", "custom desc")
		assert.NoError(t, err)

		info, err := executeTool.Info(context.Background())
		assert.NoError(t, err)
		assert.Equal(t, "custom_execute", info.Name)
		assert.Equal(t, "custom desc", info.Desc)
	})
}

func TestNew_StreamingShell(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("StreamingShell adds streaming execute tool", func(t *testing.T) {
		m, err := New(ctx, &MiddlewareConfig{
			Backend:        backend,
			StreamingShell: &mockStreamingShell{},
		})
		assert.NoError(t, err)

		fm, ok := m.(*filesystemMiddleware)
		assert.True(t, ok)
		assert.Len(t, fm.additionalTools, 7)
	})

	t.Run("both Shell and StreamingShell returns error", func(t *testing.T) {
		_, err := New(ctx, &MiddlewareConfig{
			Backend:        backend,
			Shell:          &mockShellBackend{Backend: backend, resp: &filesystem.ExecuteResponse{Output: "ok"}},
			StreamingShell: &mockStreamingShell{},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "shell and streaming shell should not be both set")
	})
}

func TestNewMiddleware_Validation(t *testing.T) {
	ctx := context.Background()

	t.Run("nil config returns error", func(t *testing.T) {
		_, err := NewMiddleware(ctx, nil)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config should not be nil")
	})

	t.Run("nil backend returns error", func(t *testing.T) {
		_, err := NewMiddleware(ctx, &Config{Backend: nil})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend should not be nil")
	})

	t.Run("both Shell and StreamingShell returns error", func(t *testing.T) {
		backend := setupTestBackend()
		_, err := NewMiddleware(ctx, &Config{
			Backend:        backend,
			Shell:          &mockShellBackend{Backend: backend, resp: &filesystem.ExecuteResponse{Output: "ok"}},
			StreamingShell: &mockStreamingShell{},
		})
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "shell and streaming shell should not be both set")
	})
}

func TestMiddlewareConfig_Validate(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		var c *MiddlewareConfig
		err := c.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config should not be nil")
	})

	t.Run("nil backend returns error", func(t *testing.T) {
		c := &MiddlewareConfig{}
		err := c.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend should not be nil")
	})

	t.Run("both shells returns error", func(t *testing.T) {
		c := &MiddlewareConfig{
			Backend:        setupTestBackend(),
			Shell:          &mockShellBackend{},
			StreamingShell: &mockStreamingShell{},
		}
		err := c.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "shell and streaming shell should not be both set")
	})

	t.Run("valid config passes", func(t *testing.T) {
		c := &MiddlewareConfig{
			Backend: setupTestBackend(),
		}
		err := c.Validate()
		assert.NoError(t, err)
	})
}

func TestNewStreamingExecuteTool_MultipleChunks(t *testing.T) {
	mockSS := &mockStreamingShellMultiChunk{}
	executeTool, err := newStreamingExecuteTool(mockSS, "", "")
	assert.NoError(t, err)

	st := executeTool.(tool.StreamableTool)
	sr, err := st.StreamableRun(context.Background(), `{"command": "long-running"}`)
	assert.NoError(t, err)
	defer sr.Close()

	var chunks []string
	for {
		chunk, recvErr := sr.Recv()
		if recvErr == io.EOF {
			break
		}
		assert.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}
	// Should have received multiple chunks
	assert.True(t, len(chunks) >= 3)
	result := strings.Join(chunks, "")
	assert.Contains(t, result, "chunk1")
	assert.Contains(t, result, "chunk2")
	assert.Contains(t, result, "chunk3")
}

type mockStreamingShellMultiChunk struct{}

func (m *mockStreamingShellMultiChunk) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{Output: "chunk1\n"}, nil)
		sw.Send(&filesystem.ExecuteResponse{Output: "chunk2\n"}, nil)
		sw.Send(&filesystem.ExecuteResponse{Output: "chunk3\n", ExitCode: ptrOf(0)}, nil)
	}()
	return sr, nil
}

func TestNewStreamingExecuteTool_ExitCodeOnlyInLastChunk(t *testing.T) {
	mockSS := &mockStreamingShellExitCodeLast{exitCode: 2}
	executeTool, err := newStreamingExecuteTool(mockSS, "", "")
	assert.NoError(t, err)

	st := executeTool.(tool.StreamableTool)
	sr, err := st.StreamableRun(context.Background(), `{"command": "fail-at-end"}`)
	assert.NoError(t, err)
	defer sr.Close()

	var chunks []string
	for {
		chunk, recvErr := sr.Recv()
		if recvErr == io.EOF {
			break
		}
		assert.NoError(t, recvErr)
		chunks = append(chunks, chunk)
	}
	result := strings.Join(chunks, "")
	assert.Contains(t, result, "output line")
	assert.Contains(t, result, "[Command failed with exit code 2]")
}

type mockStreamingShellExitCodeLast struct {
	exitCode int
}

func (m *mockStreamingShellExitCodeLast) ExecuteStreaming(ctx context.Context, input *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	sr, sw := schema.Pipe[*filesystem.ExecuteResponse](10)
	go func() {
		defer sw.Close()
		sw.Send(&filesystem.ExecuteResponse{Output: "output line"}, nil)
		sw.Send(&filesystem.ExecuteResponse{ExitCode: ptrOf(m.exitCode)}, nil)
	}()
	return sr, nil
}

func TestConvExecuteResponse_NilResponse(t *testing.T) {
	result := convExecuteResponse(nil)
	assert.Equal(t, "", result)
}

func TestConvExecuteResponse_NilExitCode(t *testing.T) {
	result := convExecuteResponse(&filesystem.ExecuteResponse{
		Output: "some output",
	})
	assert.Equal(t, "some output", result)
}

func TestConfig_Validate(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		var c *Config
		err := c.Validate()
		assert.Error(t, err)
	})

	t.Run("nil backend returns error", func(t *testing.T) {
		c := &Config{}
		err := c.Validate()
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend should not be nil")
	})

	t.Run("both shells returns error", func(t *testing.T) {
		c := &Config{
			Backend:        setupTestBackend(),
			Shell:          &mockShellBackend{},
			StreamingShell: &mockStreamingShell{},
		}
		err := c.Validate()
		assert.Error(t, err)
	})

	t.Run("valid config passes", func(t *testing.T) {
		c := &Config{
			Backend: setupTestBackend(),
		}
		err := c.Validate()
		assert.NoError(t, err)
	})
}

func TestGetFilesystemTools_CustomToolWithShell(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("custom tool replaces default for all disabled except custom", func(t *testing.T) {
		customLs, err := newLsTool(backend, "my_ls", "my ls desc")
		assert.NoError(t, err)

		config := &MiddlewareConfig{
			Backend: backend,
			LsToolConfig: &ToolConfig{
				CustomTool: customLs,
			},
		}
		tools, err := getFilesystemTools(ctx, config)
		assert.NoError(t, err)

		var found bool
		for _, to := range tools {
			info, _ := to.Info(ctx)
			if info.Name == "my_ls" {
				found = true
				break
			}
		}
		assert.True(t, found)
	})
}

func TestMergeToolConfigWithDesc(t *testing.T) {
	config := &MiddlewareConfig{Backend: setupTestBackend()}

	t.Run("both nil returns empty ToolConfig", func(t *testing.T) {
		result := config.mergeToolConfigWithDesc(nil, nil)
		assert.NotNil(t, result)
		assert.Equal(t, "", result.Name)
		assert.Nil(t, result.Desc)
		assert.False(t, result.Disable)
	})

	t.Run("nil toolConfig with legacyDesc", func(t *testing.T) {
		desc := "legacy"
		result := config.mergeToolConfigWithDesc(nil, &desc)
		assert.NotNil(t, result)
		assert.Equal(t, "legacy", *result.Desc)
	})

	t.Run("toolConfig with Desc overrides legacyDesc", func(t *testing.T) {
		tcDesc := "tc desc"
		legacyDesc := "legacy"
		tc := &ToolConfig{Desc: &tcDesc}
		result := config.mergeToolConfigWithDesc(tc, &legacyDesc)
		assert.Equal(t, "tc desc", *result.Desc)
	})

	t.Run("toolConfig with nil Desc falls back to legacyDesc", func(t *testing.T) {
		legacyDesc := "legacy"
		tc := &ToolConfig{Name: "custom"}
		result := config.mergeToolConfigWithDesc(tc, &legacyDesc)
		assert.Equal(t, "legacy", *result.Desc)
		assert.Equal(t, "custom", result.Name)
	})

	t.Run("toolConfig with nil Desc and nil legacyDesc", func(t *testing.T) {
		tc := &ToolConfig{Name: "custom"}
		result := config.mergeToolConfigWithDesc(tc, nil)
		assert.Nil(t, result.Desc)
		assert.Equal(t, "custom", result.Name)
	})
}

func TestNewMiddleware_WithShell(t *testing.T) {
	ctx := context.Background()
	backend := setupTestBackend()

	t.Run("Shell backend creates execute tool", func(t *testing.T) {
		shellBackend := &mockShellBackend{
			Backend: backend,
			resp:    &filesystem.ExecuteResponse{Output: "ok"},
		}
		m, err := NewMiddleware(ctx, &Config{
			Backend: backend,
			Shell:   shellBackend,
		})
		assert.NoError(t, err)
		assert.Len(t, m.AdditionalTools, 7)
	})

	t.Run("StreamingShell backend creates streaming execute tool", func(t *testing.T) {
		m, err := NewMiddleware(ctx, &Config{
			Backend:        backend,
			StreamingShell: &mockStreamingShell{},
		})
		assert.NoError(t, err)
		assert.Len(t, m.AdditionalTools, 7)
	})
}

func TestNewExecuteTool_ShellError(t *testing.T) {
	mockShell := &mockShellBackendWithError{}
	executeTool, err := newExecuteTool(mockShell, "", "")
	assert.NoError(t, err)

	result, err := invokeTool(t, executeTool, `{"command": "fail"}`)
	assert.Error(t, err)
	assert.Equal(t, "", result)
	assert.Contains(t, err.Error(), "shell execution error")
}

type mockShellBackendWithError struct{}

func (m *mockShellBackendWithError) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	return nil, errors.New("shell execution error")
}
