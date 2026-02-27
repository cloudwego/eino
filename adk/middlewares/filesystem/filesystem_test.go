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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/components/tool"
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
	lsTool, err := newLsTool(backend, nil, nil)
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
	readTool, err := newReadFileTool(backend, nil, nil)
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
			expected: "     3\tline3\n     4\tline4",
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
	writeTool, err := newWriteFileTool(backend, nil, nil)
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
	if content != "     1\tnew content" {
		t.Errorf("Expected written content to be 'new content', got %q", content)
	}
}

func TestEditFileTool(t *testing.T) {
	backend := setupTestBackend()
	editTool, err := newEditFileTool(backend, nil, nil)
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
			expected:     "     1\thello world\n     2\thi\n     3\thello world",
		},
		{
			name:         "replace all occurrences",
			setupFile:    "/edit2.txt",
			setupContent: "hello world\nhello again\nhello world",
			input:        `{"file_path": "/edit2.txt", "old_string": "hello", "new_string": "hi", "replace_all": true}`,
			expected:     "     1\thi world\n     2\thi again\n     3\thi world",
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
			if result != tt.expected {
				t.Errorf("Expected %q, got %q", tt.expected, result)
			}
		})
	}
}

func TestGlobTool(t *testing.T) {
	backend := setupTestBackend()
	globTool, err := newGlobTool(backend, nil, nil)
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
	grepTool, err := newGrepTool(backend, nil, nil)
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
			expected: "/dir1/file3.txt:2\n/dir1/file4.py:1\n/file2.go:1", // 2 in file3.txt, 1 in file4.py, 1 in file2.go
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
			expected: "/dir1/file3.txt:2", // only in file3.txt
		},
		{
			name:     "grep with path filter",
			input:    `{"pattern": "package", "path": "/dir2", "output_mode": "count"}`,
			expected: "/dir2/file5.go:1", // only in dir2/file5.go
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
			}, nil, nil)
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
		assert.Contains(t, fm.additionalInstruction, ToolsSystemPrompt)
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
		assert.Contains(t, newRunCtx.Instruction, ToolsSystemPrompt)
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

	grepTool, err := newGrepTool(backend, nil, nil)
	assert.NoError(t, err)

	t.Run("files sorted by basename", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches"}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 3, len(lines))
		assert.Contains(t, lines[0], "apple.txt")
		assert.Contains(t, lines[1], "banana.txt")
		assert.Contains(t, lines[2], "zebra.txt")
	})

	t.Run("files_with_matches with offset", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": 1}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 2, len(lines))
		assert.Contains(t, lines[0], "banana.txt")
		assert.Contains(t, lines[1], "zebra.txt")
	})

	t.Run("files_with_matches with head_limit", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "head_limit": 2}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 2, len(lines))
		assert.Contains(t, lines[0], "apple.txt")
		assert.Contains(t, lines[1], "banana.txt")
	})

	t.Run("files_with_matches with offset and head_limit", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": 1, "head_limit": 1}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 1, len(lines))
		assert.Contains(t, lines[0], "banana.txt")
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
		assert.Equal(t, 3, len(lines))
		assert.Contains(t, lines[0], "apple.txt:2")
		assert.Contains(t, lines[1], "banana.txt:3")
		assert.Contains(t, lines[2], "zebra.txt:3")
	})

	t.Run("count mode with pagination", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "count", "offset": 1, "head_limit": 1}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 1, len(lines))
		assert.Contains(t, lines[0], "banana.txt:3")
	})

	t.Run("offset exceeds result count", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": 100}`)
		assert.NoError(t, err)
		assert.Equal(t, "", result)
	})

	t.Run("negative offset treated as zero", func(t *testing.T) {
		result, err := invokeTool(t, grepTool, `{"pattern": "match", "output_mode": "files_with_matches", "offset": -5}`)
		assert.NoError(t, err)
		lines := strings.Split(strings.TrimSpace(result), "\n")
		assert.Equal(t, 3, len(lines))
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

		lsTool, err := newLsTool(backend, &customLsName, nil)
		assert.NoError(t, err)
		info, _ := lsTool.Info(ctx)
		assert.Equal(t, "list_files", info.Name)

		readTool, err := newReadFileTool(backend, &customReadName, nil)
		assert.NoError(t, err)
		info, _ = readTool.Info(ctx)
		assert.Equal(t, "read", info.Name)

		writeTool, err := newWriteFileTool(backend, &customWriteName, nil)
		assert.NoError(t, err)
		info, _ = writeTool.Info(ctx)
		assert.Equal(t, "write", info.Name)

		editTool, err := newEditFileTool(backend, &customEditName, nil)
		assert.NoError(t, err)
		info, _ = editTool.Info(ctx)
		assert.Equal(t, "edit", info.Name)

		globTool, err := newGlobTool(backend, &customGlobName, nil)
		assert.NoError(t, err)
		info, _ = globTool.Info(ctx)
		assert.Equal(t, "find_files", info.Name)

		grepTool, err := newGrepTool(backend, &customGrepName, nil)
		assert.NoError(t, err)
		info, _ = grepTool.Info(ctx)
		assert.Equal(t, "search", info.Name)
	})

	t.Run("default tool names when custom names not provided", func(t *testing.T) {
		lsTool, err := newLsTool(backend, nil, nil)
		assert.NoError(t, err)
		info, _ := lsTool.Info(ctx)
		assert.Equal(t, ToolNameLs, info.Name)

		readTool, err := newReadFileTool(backend, nil, nil)
		assert.NoError(t, err)
		info, _ = readTool.Info(ctx)
		assert.Equal(t, ToolNameReadFile, info.Name)

		writeTool, err := newWriteFileTool(backend, nil, nil)
		assert.NoError(t, err)
		info, _ = writeTool.Info(ctx)
		assert.Equal(t, ToolNameWriteFile, info.Name)

		editTool, err := newEditFileTool(backend, nil, nil)
		assert.NoError(t, err)
		info, _ = editTool.Info(ctx)
		assert.Equal(t, ToolNameEditFile, info.Name)

		globTool, err := newGlobTool(backend, nil, nil)
		assert.NoError(t, err)
		info, _ = globTool.Info(ctx)
		assert.Equal(t, ToolNameGlob, info.Name)

		grepTool, err := newGrepTool(backend, nil, nil)
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

		executeTool, err := newExecuteTool(shellBackend, &customExecuteName, nil)
		assert.NoError(t, err)
		info, _ := executeTool.Info(ctx)
		assert.Equal(t, "run_command", info.Name)
	})

	t.Run("custom tool names in getFilesystemTools", func(t *testing.T) {
		customLsName := "list_files"
		customReadName := "read"
		customWriteName := "write"
		customEditName := "edit"
		customGlobName := "find_files"
		customGrepName := "search"

		tools, err := getFilesystemTools(ctx, &MiddlewareConfig{
			Backend:                 backend,
			CustomLsToolName:        &customLsName,
			CustomReadFileToolName:  &customReadName,
			CustomWriteFileToolName: &customWriteName,
			CustomEditFileToolName:  &customEditName,
			CustomGlobToolName:      &customGlobName,
			CustomGrepToolName:      &customGrepName,
		})
		assert.NoError(t, err)
		assert.Len(t, tools, 6)

		toolNames := make(map[string]bool)
		for _, to := range tools {
			info, _ := to.Info(ctx)
			toolNames[info.Name] = true
		}

		assert.True(t, toolNames["list_files"])
		assert.True(t, toolNames["read"])
		assert.True(t, toolNames["write"])
		assert.True(t, toolNames["edit"])
		assert.True(t, toolNames["find_files"])
		assert.True(t, toolNames["search"])
	})

	t.Run("custom tool names in middleware", func(t *testing.T) {
		customLsName := "list_files"
		customReadName := "read"

		m, err := New(ctx, &MiddlewareConfig{
			Backend:                backend,
			CustomLsToolName:       &customLsName,
			CustomReadFileToolName: &customReadName,
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
		result := selectToolName(&customName, "default_tool")
		assert.Equal(t, "custom_tool", result)
	})

	t.Run("returns default name when custom name is nil", func(t *testing.T) {
		result := selectToolName(nil, "default_tool")
		assert.Equal(t, "default_tool", result)
	})
}
