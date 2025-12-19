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

	"github.com/cloudwego/eino/components/tool"
)

// setupTestBackend creates a test backend with some initial files
func setupTestBackend() *InMemoryBackend {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create test files
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file1.txt",
		Content:  "line1\nline2\nline3\nline4\nline5",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file2.go",
		Content:  "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/file3.txt",
		Content:  "hello world\nfoo bar\nhello again",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/file4.py",
		Content:  "print('hello')\nprint('world')",
	})
	backend.Write(ctx, &WriteRequest{
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
	lsTool, err := newLsTool(backend, nil)
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
			expected: []string{"/file1.txt", "/file2.go", "/dir1", "/dir2"},
		},
		{
			name:     "list empty path (defaults to root)",
			input:    `{"path": ""}`,
			expected: []string{"/file1.txt", "/file2.go", "/dir1", "/dir2"},
		},
		{
			name:     "list dir1",
			input:    `{"path": "/dir1"}`,
			expected: []string{"/dir1/file3.txt", "/dir1/file4.py"},
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
	readTool, err := newReadFileTool(backend, nil)
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
	writeTool, err := newWriteFileTool(backend, nil)
	if err != nil {
		t.Fatalf("Failed to create write_file tool: %v", err)
	}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "write new file",
			input:    `{"file_path": "/newfile.txt", "content": "new content"}`,
			expected: "Updated file /newfile.txt",
		},
		{
			name:     "overwrite existing file",
			input:    `{"file_path": "/file1.txt", "content": "overwritten"}`,
			expected: "Updated file /file1.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := invokeTool(t, writeTool, tt.input)
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
	content, err := backend.Read(ctx, &ReadRequest{
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
	content, err = backend.Read(ctx, &ReadRequest{
		FilePath: "/file1.txt",
		Offset:   0,
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("Failed to read written file: %v", err)
	}
	if content != "     1\toverwritten" {
		t.Errorf("Expected written content to be 'new content', got %q", content)
	}
}

func TestEditFileTool(t *testing.T) {
	backend := setupTestBackend()
	editTool, err := newEditFileTool(backend, nil)
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
				backend.Write(ctx, &WriteRequest{
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
			result, err := backend.Read(ctx, &ReadRequest{
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
	globTool, err := newGlobTool(backend, nil)
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
			expected: []string{"/file1.txt"},
		},
		{
			name:     "match all .go files in root",
			input:    `{"pattern": "*.go", "path": "/"}`,
			expected: []string{"/file2.go"},
		},
		{
			name:     "match all .txt files in dir1",
			input:    `{"pattern": "*.txt", "path": "/dir1"}`,
			expected: []string{"/dir1/file3.txt"},
		},
		{
			name:     "match all .py files in dir1",
			input:    `{"pattern": "*.py", "path": "/dir1"}`,
			expected: []string{"/dir1/file4.py"},
		},
		{
			name:     "empty path defaults to root",
			input:    `{"pattern": "*.go", "path": ""}`,
			expected: []string{"/file2.go"},
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
	grepTool, err := newGrepTool(backend, nil)
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
			expected: "4", // 2 in file3.txt, 1 in file4.py, 1 in file2.go
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
			expected: "2", // only in file3.txt
		},
		{
			name:     "grep with path filter",
			input:    `{"pattern": "package", "path": "/dir2", "output_mode": "count"}`,
			expected: "1", // only in dir2/file5.go
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
