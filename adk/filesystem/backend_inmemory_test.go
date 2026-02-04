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
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestInMemoryBackend_WriteAndRead(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Test Write
	err := backend.Write(ctx, &WriteRequest{
		FilePath: "/test.txt",
		Content:  "line1\nline2\nline3\nline4\nline5",
	})
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Test Read - full content
	content, err := backend.Read(ctx, &ReadRequest{
		FilePath: "/test.txt",
		Offset:   0,
		Limit:    100,
	})
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	expected := "     1\tline1\n     2\tline2\n     3\tline3\n     4\tline4\n     5\tline5"
	if content != expected {
		t.Errorf("Read content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Read - with offset and limit
	content, err = backend.Read(ctx, &ReadRequest{
		FilePath: "/test.txt",
		Offset:   1,
		Limit:    2,
	})
	if err != nil {
		t.Fatalf("Read with offset failed: %v", err)
	}
	expected = "     2\tline2\n     3\tline3"
	if content != expected {
		t.Errorf("Read with offset content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Read - non-existent file
	_, err = backend.Read(ctx, &ReadRequest{
		FilePath: "/nonexistent.txt",
		Offset:   0,
		Limit:    10,
	})
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

func TestInMemoryBackend_LsInfo(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create some files
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file1.txt",
		Content:  "content1",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file2.txt",
		Content:  "content2",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/file3.txt",
		Content:  "content3",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/subdir/file4.txt",
		Content:  "content4",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir2/file5.txt",
		Content:  "content5",
	})

	// Test LsInfo - root
	infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
	if err != nil {
		t.Fatalf("LsInfo failed: %v", err)
	}
	if len(infos) != 4 { // file1.txt, file2.txt, dir1, dir2
		t.Errorf("Expected 4 items in root, got %d", len(infos))
	}

	// Test LsInfo - specific directory
	infos, err = backend.LsInfo(ctx, &LsInfoRequest{Path: "/dir1"})
	if err != nil {
		t.Fatalf("LsInfo for /dir1 failed: %v", err)
	}
	if len(infos) != 2 { // file3.txt, subdir
		t.Errorf("Expected 2 items in /dir1, got %d", len(infos))
	}
}

func TestInMemoryBackend_Edit(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create a file
	backend.Write(ctx, &WriteRequest{
		FilePath: "/edit.txt",
		Content:  "hello world\nhello again\nhello world",
	})

	// Test Edit - report error if old string occurs
	err := backend.Edit(ctx, &EditRequest{
		FilePath:   "/edit.txt",
		OldString:  "hello",
		NewString:  "hi",
		ReplaceAll: false,
	})
	if err == nil {
		t.Fatal("should have failed")
	}

	// Test Edit - replace all occurrences
	backend.Write(ctx, &WriteRequest{
		FilePath: "/edit2.txt",
		Content:  "hello world\nhello again\nhello world",
	})
	err = backend.Edit(ctx, &EditRequest{
		FilePath:   "/edit2.txt",
		OldString:  "hello",
		NewString:  "hi",
		ReplaceAll: true,
	})
	if err != nil {
		t.Fatalf("Edit (replace all) failed: %v", err)
	}

	content, _ := backend.Read(ctx, &ReadRequest{
		FilePath: "/edit2.txt",
		Offset:   0,
		Limit:    100,
	})
	expected := "     1\thi world\n     2\thi again\n     3\thi world"
	if content != expected {
		t.Errorf("Edit (replace all) content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Edit - non-existent file
	err = backend.Edit(ctx, &EditRequest{
		FilePath:   "/nonexistent.txt",
		OldString:  "old",
		NewString:  "new",
		ReplaceAll: false,
	})
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}

	// Test Edit - empty oldString
	err = backend.Edit(ctx, &EditRequest{
		FilePath:   "/edit.txt",
		OldString:  "",
		NewString:  "new",
		ReplaceAll: false,
	})
	if err == nil {
		t.Error("Expected error for empty oldString, got nil")
	}
}

func TestInMemoryBackend_GrepRaw(t *testing.T) {
	tests := []struct {
		name           string
		setupFiles     map[string]string
		request        *GrepRequest
		expectedCount  int
		expectedPaths  []string
		expectedLines  []int
		expectedError  bool
		validateResult func(t *testing.T, matches []GrepMatch)
	}{
		{
			name: "basic pattern search across all files",
			setupFiles: map[string]string{
				"/file1.txt":      "hello world\nfoo bar\nhello again",
				"/file2.py":       "print('hello')\nprint('world')",
				"/dir1/file3.txt": "hello from dir1",
			},
			request: &GrepRequest{
				Pattern:    "hello",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 4,
			expectedPaths: []string{"/file1.txt", "/file2.py", "/dir1/file3.txt"},
		},
		{
			name: "search with glob filter for python files",
			setupFiles: map[string]string{
				"/file1.txt": "hello world",
				"/file2.py":  "print('hello')\nprint('world')",
				"/file3.js":  "console.log('hello')",
			},
			request: &GrepRequest{
				Pattern: "hello",
				Glob:    "*.py",
			},
			expectedCount: 1,
			expectedPaths: []string{"/file2.py"},
		},
		{
			name: "search with path filter",
			setupFiles: map[string]string{
				"/file1.txt":      "hello world",
				"/dir1/file2.txt": "hello from dir1",
				"/dir2/file3.txt": "hello from dir2",
			},
			request: &GrepRequest{
				Pattern: "hello",
				Path:    "/dir1",
			},
			expectedCount: 1,
			expectedPaths: []string{"/dir1/file2.txt"},
		},
		{
			name: "search with file type filter",
			setupFiles: map[string]string{
				"/file1.txt": "hello world",
				"/file2.py":  "hello python",
				"/file3.go":  "hello golang",
			},
			request: &GrepRequest{
				Pattern:  "hello",
				FileType: "py",
			},
			expectedCount: 1,
			expectedPaths: []string{"/file2.py"},
		},
		{
			name: "case insensitive search",
			setupFiles: map[string]string{
				"/file1.txt": "Hello World\nHELLO WORLD\nhello world",
			},
			request: &GrepRequest{
				Pattern:         "hello",
				CaseInsensitive: true,
				OutputMode:      ContentOfOutputMode,
			},
			expectedCount: 3,
		},
		{
			name: "case sensitive search",
			setupFiles: map[string]string{
				"/file1.txt": "Hello World\nHELLO WORLD\nhello world",
			},
			request: &GrepRequest{
				Pattern:         "hello",
				CaseInsensitive: false,
			},
			expectedCount: 1,
		},
		{
			name: "regex pattern with special characters",
			setupFiles: map[string]string{
				"/file1.txt": "error: something\nwarning: test\nerror: another",
			},
			request: &GrepRequest{
				Pattern:    "error:.*",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 2,
		},
		{
			name: "multiline pattern matching",
			setupFiles: map[string]string{
				"/file1.txt": `
const a = 1;

function calculateTotal(
  items,
  discount
) {
  return items.reduce((sum, item) => sum + item.price, 0);
}

const b = 2;

/*
 * This is a comment
 * spanning multiple lines
 */

class UserService {
  constructor(db) {
    this.db = db;
  }
}
`,
			},
			request: &GrepRequest{
				Pattern:         "function calculateTotal\\([^\\)]*\\)",
				EnableMultiline: true,
				OutputMode:      ContentOfOutputMode,
			},
			expectedCount: 4,
		},
		{
			name: "output mode: files_with_matches",
			setupFiles: map[string]string{
				"/file1.txt": "hello\nworld\nhello",
				"/file2.txt": "hello\nhello",
			},
			request: &GrepRequest{
				Pattern:    "hello",
				OutputMode: FilesWithMatchesOfOutputMode,
			},
			expectedCount: 2,
			validateResult: func(t *testing.T, matches []GrepMatch) {
				for _, match := range matches {
					if match.Content != "" {
						t.Errorf("Expected empty content in files_with_matches mode, got: %s", match.Content)
					}
					if match.Path == "" {
						t.Error("Expected non-empty path")
					}
				}
			},
		},
		{
			name: "output mode: content",
			setupFiles: map[string]string{
				"/file1.txt": "line1\nhello world\nline3",
			},
			request: &GrepRequest{
				Pattern:    "hello",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, matches []GrepMatch) {
				if len(matches) > 0 {
					if matches[0].Content != "hello world" {
						t.Errorf("Expected content 'hello world', got: %s", matches[0].Content)
					}
					if matches[0].Line != 2 {
						t.Errorf("Expected line number 2, got: %d", matches[0].Line)
					}
				}
			},
		},
		{
			name: "output mode: count",
			setupFiles: map[string]string{
				"/file1.txt": "hello\nhello\nhello",
				"/file2.txt": "hello\nworld",
			},
			request: &GrepRequest{
				Pattern:    "hello",
				OutputMode: CountOfOutputMode,
			},
			expectedCount: 2,
			validateResult: func(t *testing.T, matches []GrepMatch) {
				for _, match := range matches {
					if match.Path == "/file1.txt" && match.Count != 3 {
						t.Errorf("Expected count 3 for file1.txt, got: %d", match.Count)
					}
					if match.Path == "/file2.txt" && match.Count != 1 {
						t.Errorf("Expected count 1 for file2.txt, got: %d", match.Count)
					}
				}
			},
		},
		{
			name: "with context lines",
			setupFiles: map[string]string{
				"/file1.txt": "line1\nline2\nmatch\nline4\nline5",
			},
			request: &GrepRequest{
				Pattern:      "match",
				OutputMode:   ContentOfOutputMode,
				ContextLines: 1,
			},
			expectedCount: 3,
			expectedLines: []int{2, 3, 4},
		},
		{
			name: "with before lines only",
			setupFiles: map[string]string{
				"/file1.txt": "line1\nline2\nmatch\nline4\nline5",
			},
			request: &GrepRequest{
				Pattern:     "match",
				OutputMode:  ContentOfOutputMode,
				BeforeLines: 2,
			},
			expectedCount: 3,
			expectedLines: []int{1, 2, 3},
		},
		{
			name: "with after lines only",
			setupFiles: map[string]string{
				"/file1.txt": "line1\nline2\nmatch\nline4\nline5",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				AfterLines: 2,
			},
			expectedCount: 3,
			expectedLines: []int{3, 4, 5},
		},
		{
			name: "with head limit",
			setupFiles: map[string]string{
				"/file1.txt": "match1\nmatch2\nmatch3\nmatch4\nmatch5",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				HeadLimit:  3,
			},
			expectedCount: 3,
		},
		{
			name: "with offset",
			setupFiles: map[string]string{
				"/file1.txt": "match1\nmatch2\nmatch3\nmatch4\nmatch5",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				Offset:     2,
			},
			expectedCount: 3,
		},
		{
			name: "with offset and head limit",
			setupFiles: map[string]string{
				"/file1.txt": "match1\nmatch2\nmatch3\nmatch4\nmatch5",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				Offset:     1,
				HeadLimit:  2,
			},
			expectedCount: 2,
			validateResult: func(t *testing.T, matches []GrepMatch) {
				if len(matches) == 2 {
					if matches[0].Content != "match2" {
						t.Errorf("Expected first match 'match2', got: %s", matches[0].Content)
					}
					if matches[1].Content != "match3" {
						t.Errorf("Expected second match 'match3', got: %s", matches[1].Content)
					}
				}
			},
		},
		{
			name: "no matches found",
			setupFiles: map[string]string{
				"/file1.txt": "hello world",
			},
			request: &GrepRequest{
				Pattern: "notfound",
			},
			expectedCount: 0,
		},
		{
			name: "invalid regex pattern",
			setupFiles: map[string]string{
				"/file1.txt": "hello world",
			},
			request: &GrepRequest{
				Pattern: "[invalid",
			},
			expectedError: true,
		},
		{
			name: "empty pattern",
			setupFiles: map[string]string{
				"/file1.txt": "hello world",
			},
			request: &GrepRequest{
				Pattern: "",
			},
			expectedError: true,
		},
		{
			name: "search in empty file",
			setupFiles: map[string]string{
				"/file1.txt": "",
			},
			request: &GrepRequest{
				Pattern: "hello",
			},
			expectedCount: 0,
		},
		{
			name: "multiple files with same pattern",
			setupFiles: map[string]string{
				"/file1.txt": "error occurred",
				"/file2.txt": "error detected",
				"/file3.txt": "no issues",
			},
			request: &GrepRequest{
				Pattern: "error",
			},
			expectedCount: 2,
			expectedPaths: []string{"/file1.txt", "/file2.txt"},
		},
		{
			name: "pattern at start of line",
			setupFiles: map[string]string{
				"/file1.txt": "hello world\nworld hello\nhello again",
			},
			request: &GrepRequest{
				Pattern:    "^hello",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 2,
		},
		{
			name: "pattern at end of line",
			setupFiles: map[string]string{
				"/file1.txt": "say hello\nhello world\nworld hello",
			},
			request: &GrepRequest{
				Pattern:    "hello$",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 2,
		},
		{
			name: "word boundary matching",
			setupFiles: map[string]string{
				"/file1.txt": "hello\nhelloworld\nworld hello world",
			},
			request: &GrepRequest{
				Pattern:    "\\bhello\\b",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 2,
		},
		{
			name: "zero context lines should not add context",
			setupFiles: map[string]string{
				"/file1.txt": "line1\nmatch\nline3",
			},
			request: &GrepRequest{
				Pattern:      "match",
				OutputMode:   ContentOfOutputMode,
				ContextLines: 0,
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, matches []GrepMatch) {
				if len(matches) == 1 && matches[0].Content != "match" {
					t.Errorf("Expected only matching line, got: %s", matches[0].Content)
				}
			},
		},
		{
			name: "negative offset should be treated as zero",
			setupFiles: map[string]string{
				"/file1.txt": "match1\nmatch2",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				Offset:     -5,
			},
			expectedCount: 2,
		},
		{
			name: "negative head limit should be treated as no limit",
			setupFiles: map[string]string{
				"/file1.txt": "match1\nmatch2\nmatch3",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				HeadLimit:  -1,
			},
			expectedCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewInMemoryBackend()
			ctx := context.Background()

			for path, content := range tt.setupFiles {
				err := backend.Write(ctx, &WriteRequest{
					FilePath: path,
					Content:  content,
				})
				if err != nil {
					t.Fatalf("Failed to setup file %s: %v", path, err)
				}
			}

			matches, err := backend.GrepRaw(ctx, tt.request)

			if tt.expectedError {
				if err == nil {
					t.Error("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(matches) != tt.expectedCount {
				t.Errorf("Expected %d matches, got %d", tt.expectedCount, len(matches))
				for i, match := range matches {
					t.Logf("Match %d: Path=%s, Line=%d, Content=%s, Count=%d",
						i, match.Path, match.Line, match.Content, match.Count)
				}
			}

			if tt.expectedPaths != nil {
				foundPaths := make(map[string]bool)
				for _, match := range matches {
					foundPaths[match.Path] = true
				}
				for _, expectedPath := range tt.expectedPaths {
					if !foundPaths[expectedPath] {
						t.Errorf("Expected path %s not found in results", expectedPath)
					}
				}
			}

			if tt.expectedLines != nil {
				actualLines := make([]int, len(matches))
				for i, match := range matches {
					actualLines[i] = match.Line
				}
				if len(actualLines) != len(tt.expectedLines) {
					t.Errorf("Expected line numbers %v, got %v", tt.expectedLines, actualLines)
				} else {
					for i, expectedLine := range tt.expectedLines {
						if actualLines[i] != expectedLine {
							t.Errorf("Line number mismatch at index %d: expected %d, got %d",
								i, expectedLine, actualLines[i])
						}
					}
				}
			}

			if tt.validateResult != nil {
				tt.validateResult(t, matches)
			}
		})
	}
}

func TestInMemoryBackend_GrepRaw_Concurrent(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		err := backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/file%d.txt", i),
			Content:  fmt.Sprintf("line1\nmatch%d\nline3", i),
		})
		if err != nil {
			t.Fatalf("Failed to setup file: %v", err)
		}
	}

	const numGoroutines = 50
	done := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(id int) {
			matches, err := backend.GrepRaw(ctx, &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			})
			if err != nil {
				done <- fmt.Errorf("goroutine %d: %v", id, err)
				return
			}
			if len(matches) != 10 {
				done <- fmt.Errorf("goroutine %d: expected 10 matches, got %d", id, len(matches))
				return
			}
			done <- nil
		}(i)
	}

	for i := 0; i < numGoroutines; i++ {
		if err := <-done; err != nil {
			t.Error(err)
		}
	}
}

func TestInMemoryBackend_GrepRaw_EdgeCases(t *testing.T) {
	tests := []struct {
		name          string
		setupFiles    map[string]string
		request       *GrepRequest
		expectedCount int
		description   string
	}{
		{
			name: "very long line",
			setupFiles: map[string]string{
				"/file1.txt": strings.Repeat("a", 10000) + "match" + strings.Repeat("b", 10000),
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1,
			description:   "should handle very long lines correctly",
		},
		{
			name: "many matches in single file",
			setupFiles: map[string]string{
				"/file1.txt": strings.Repeat("match\n", 1000),
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1000,
			description:   "should handle many matches efficiently",
		},
		{
			name: "unicode characters",
			setupFiles: map[string]string{
				"/file1.txt": "你好世界\nmatch中文\n测试",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1,
			description:   "should handle unicode characters correctly",
		},
		{
			name: "special regex characters in content",
			setupFiles: map[string]string{
				"/file1.txt": "test [brackets]\ntest (parentheses)\ntest {braces}",
			},
			request: &GrepRequest{
				Pattern:    "\\[brackets\\]",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1,
			description:   "should handle escaped special characters",
		},
		{
			name: "newline at end of file",
			setupFiles: map[string]string{
				"/file1.txt": "match\n",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1,
			description:   "should handle trailing newline correctly",
		},
		{
			name: "no newline at end of file",
			setupFiles: map[string]string{
				"/file1.txt": "match",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 1,
			description:   "should handle missing trailing newline",
		},
		{
			name: "multiple consecutive newlines",
			setupFiles: map[string]string{
				"/file1.txt": "match\n\n\nmatch",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
			},
			expectedCount: 2,
			description:   "should handle multiple consecutive newlines",
		},
		{
			name: "offset larger than result count",
			setupFiles: map[string]string{
				"/file1.txt": "match1\nmatch2",
			},
			request: &GrepRequest{
				Pattern:    "match",
				OutputMode: ContentOfOutputMode,
				Offset:     100,
			},
			expectedCount: 0,
			description:   "should return empty when offset exceeds results",
		},
		{
			name: "context lines at file boundaries",
			setupFiles: map[string]string{
				"/file1.txt": "match",
			},
			request: &GrepRequest{
				Pattern:      "match",
				OutputMode:   ContentOfOutputMode,
				ContextLines: 10,
			},
			expectedCount: 1,
			description:   "should not fail when context exceeds file boundaries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewInMemoryBackend()
			ctx := context.Background()

			for path, content := range tt.setupFiles {
				err := backend.Write(ctx, &WriteRequest{
					FilePath: path,
					Content:  content,
				})
				if err != nil {
					t.Fatalf("Failed to setup file: %v", err)
				}
			}

			matches, err := backend.GrepRaw(ctx, tt.request)
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			if len(matches) != tt.expectedCount {
				t.Errorf("%s: expected %d matches, got %d", tt.description, tt.expectedCount, len(matches))
			}
		})
	}
}

func BenchmarkInMemoryBackend_GrepRaw_SimplePattern(b *testing.B) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		content := fmt.Sprintf("line1\nline2\nmatch%d\nline4\nline5", i)
		backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/file%d.txt", i),
			Content:  content,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:    "match",
			OutputMode: ContentOfOutputMode,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInMemoryBackend_GrepRaw_ComplexPattern(b *testing.B) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		content := "error: something happened\nwarning: test\ninfo: data\nerror: another issue"
		backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/file%d.txt", i),
			Content:  content,
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:    "error:.*",
			OutputMode: ContentOfOutputMode,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInMemoryBackend_GrepRaw_WithContext(b *testing.B) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		lines := make([]string, 100)
		for j := 0; j < 100; j++ {
			if j%10 == 0 {
				lines[j] = fmt.Sprintf("match%d", j)
			} else {
				lines[j] = fmt.Sprintf("line%d", j)
			}
		}
		backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/file%d.txt", i),
			Content:  strings.Join(lines, "\n"),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 2,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInMemoryBackend_GrepRaw_FilesWithMatches(b *testing.B) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 1000; i++ {
		backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/file%d.txt", i),
			Content:  "line1\nmatch\nline3",
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:    "match",
			OutputMode: FilesWithMatchesOfOutputMode,
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestInMemoryBackend_GrepRaw_ContentValidation(t *testing.T) {
	tests := []struct {
		name            string
		fileContent     string
		pattern         string
		outputMode      OutputMode
		contextLines    int
		beforeLines     int
		afterLines      int
		expectedMatches []struct {
			content    string
			lineNumber int
			count      int
		}
	}{
		{
			name:        "content mode returns exact line content",
			fileContent: "first line\nsecond line with match\nthird line",
			pattern:     "match",
			outputMode:  ContentOfOutputMode,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "second line with match", lineNumber: 2, count: 0},
			},
		},
		{
			name:        "content mode with multiple matches",
			fileContent: "match on line 1\nno match\nmatch on line 3\nno match\nmatch on line 5",
			pattern:     "match on",
			outputMode:  ContentOfOutputMode,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "match on line 1", lineNumber: 1, count: 0},
				{content: "match on line 3", lineNumber: 3, count: 0},
				{content: "match on line 5", lineNumber: 5, count: 0},
			},
		},
		{
			name:         "content mode with context lines includes surrounding lines",
			fileContent:  "line0\nline1\nmatch\nline3\nline4",
			pattern:      "match",
			outputMode:   ContentOfOutputMode,
			contextLines: 1,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "line1", lineNumber: 2, count: 0},
				{content: "match", lineNumber: 3, count: 0},
				{content: "line3", lineNumber: 4, count: 0},
			},
		},
		{
			name:        "content mode with before lines",
			fileContent: "line1\nline2\nmatch\nline4\nline5",
			pattern:     "match",
			outputMode:  ContentOfOutputMode,
			beforeLines: 2,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "line1", lineNumber: 1, count: 0},
				{content: "line2", lineNumber: 2, count: 0},
				{content: "match", lineNumber: 3, count: 0},
			},
		},
		{
			name:        "content mode with after lines",
			fileContent: "line1\nline2\nmatch\nline4\nline5",
			pattern:     "match",
			outputMode:  ContentOfOutputMode,
			afterLines:  2,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "match", lineNumber: 3, count: 0},
				{content: "line4", lineNumber: 4, count: 0},
				{content: "line5", lineNumber: 5, count: 0},
			},
		},
		{
			name:        "count mode returns correct count per file",
			fileContent: "match\nmatch\nmatch\nno\nmatch",
			pattern:     "match",
			outputMode:  CountOfOutputMode,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "", lineNumber: 0, count: 4},
			},
		},
		{
			name:        "files_with_matches mode returns empty content",
			fileContent: "match line\nanother match\nno",
			pattern:     "match",
			outputMode:  FilesWithMatchesOfOutputMode,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "", lineNumber: 0, count: 0},
			},
		},
		{
			name:        "content with special characters preserved",
			fileContent: "line with\ttabs\nline with  spaces\nline with special: @#$%",
			pattern:     "special",
			outputMode:  ContentOfOutputMode,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "line with special: @#$%", lineNumber: 3, count: 0},
			},
		},
		{
			name:        "content with unicode characters",
			fileContent: "普通行\n包含match的中文行\n另一行",
			pattern:     "match",
			outputMode:  ContentOfOutputMode,
			expectedMatches: []struct {
				content    string
				lineNumber int
				count      int
			}{
				{content: "包含match的中文行", lineNumber: 2, count: 0},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := NewInMemoryBackend()
			ctx := context.Background()

			err := backend.Write(ctx, &WriteRequest{
				FilePath: "/test.txt",
				Content:  tt.fileContent,
			})
			if err != nil {
				t.Fatalf("Failed to write test file: %v", err)
			}

			matches, err := backend.GrepRaw(ctx, &GrepRequest{
				Pattern:      tt.pattern,
				OutputMode:   tt.outputMode,
				ContextLines: tt.contextLines,
				BeforeLines:  tt.beforeLines,
				AfterLines:   tt.afterLines,
			})
			if err != nil {
				t.Fatalf("GrepRaw failed: %v", err)
			}

			if len(matches) != len(tt.expectedMatches) {
				t.Fatalf("Expected %d matches, got %d", len(tt.expectedMatches), len(matches))
			}

			for i, expected := range tt.expectedMatches {
				actual := matches[i]

				if actual.Content != expected.content {
					t.Errorf("Match %d: expected content %q, got %q",
						i, expected.content, actual.Content)
				}

				if expected.lineNumber > 0 && actual.Line != expected.lineNumber {
					t.Errorf("Match %d: expected line number %d, got %d",
						i, expected.lineNumber, actual.Line)
				}

				if expected.count > 0 && actual.Count != expected.count {
					t.Errorf("Match %d: expected count %d, got %d",
						i, expected.count, actual.Count)
				}

				if actual.Path != "/test.txt" {
					t.Errorf("Match %d: expected path /test.txt, got %s",
						i, actual.Path)
				}
			}

			if tt.outputMode == FilesWithMatchesOfOutputMode {
				for i, match := range matches {
					if match.Content != "" {
						t.Errorf("Match %d: expected empty content in files_with_matches mode, got %q",
							i, match.Content)
					}
					if match.Line != 0 {
						t.Errorf("Match %d: expected zero line number in files_with_matches mode, got %d",
							i, match.Line)
					}
				}
			}

			if tt.outputMode == CountOfOutputMode {
				for i, match := range matches {
					if match.Content != "" {
						t.Errorf("Match %d: expected empty content in count mode, got %q",
							i, match.Content)
					}
					if match.Count == 0 {
						t.Errorf("Match %d: expected non-zero count in count mode", i)
					}
				}
			}
		})
	}
}

func TestInMemoryBackend_GlobInfo(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create some files
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file1.txt",
		Content:  "content1",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file2.py",
		Content:  "content2",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/file3.txt",
		Content:  "content3",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/file4.py",
		Content:  "content4",
	})

	// Test GlobInfo - match all .txt files
	infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: "*.txt",
		Path:    "/",
	})
	if err != nil {
		t.Fatalf("GlobInfo failed: %v", err)
	}
	if len(infos) != 2 { // only file1.txt in root
		t.Errorf("Expected 1 .txt file in root, got %d", len(infos))
	}

	// Test GlobInfo - match all .py files in dir1
	infos, err = backend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: "*.py",
		Path:    "/dir1",
	})
	if err != nil {
		t.Fatalf("GlobInfo for /dir1 failed: %v", err)
	}
	if len(infos) != 1 { // file4.py
		t.Errorf("Expected 1 .py file in /dir1, got %d", len(infos))
	}
}

func TestInMemoryBackend_Concurrent(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Test concurrent writes and reads
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(n int) {
			backend.Write(ctx, &WriteRequest{
				FilePath: "/concurrent.txt",
				Content:  "content",
			})
			backend.Read(ctx, &ReadRequest{
				FilePath: "/concurrent.txt",
				Offset:   0,
				Limit:    10,
			})
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestInMemoryBackend_GrepWithContext(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	testContent := `line 1
line 2
line 3 match
line 4
line 5
line 6 match
line 7
line 8
line 9
line 10`

	backend.Write(ctx, &WriteRequest{
		FilePath: "/test.txt",
		Content:  testContent,
	})

	t.Run("ContextLines", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 1,
		})
		if err != nil {
			t.Fatalf("GrepRaw with ContextLines failed: %v", err)
		}

		expectedLines := []int{2, 3, 4, 5, 6, 7}
		if len(matches) != len(expectedLines) {
			t.Errorf("Expected %d matches, got %d", len(expectedLines), len(matches))
		}

		for i, match := range matches {
			if i < len(expectedLines) && match.Line != expectedLines[i] {
				t.Errorf("Match %d: expected line %d, got %d", i, expectedLines[i], match.Line)
			}
		}
	})

	t.Run("BeforeLines", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "match",
			OutputMode:  ContentOfOutputMode,
			BeforeLines: 2,
		})
		if err != nil {
			t.Fatalf("GrepRaw with BeforeLines failed: %v", err)
		}

		expectedLines := []int{1, 2, 3, 4, 5, 6}
		if len(matches) != len(expectedLines) {
			t.Errorf("Expected %d matches, got %d", len(expectedLines), len(matches))
		}

		for i, match := range matches {
			if i < len(expectedLines) && match.Line != expectedLines[i] {
				t.Errorf("Match %d: expected line %d, got %d", i, expectedLines[i], match.Line)
			}
		}
	})

	t.Run("AfterLines", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:    "match",
			OutputMode: ContentOfOutputMode,
			AfterLines: 2,
		})
		if err != nil {
			t.Fatalf("GrepRaw with AfterLines failed: %v", err)
		}

		expectedLines := []int{3, 4, 5, 6, 7, 8}
		if len(matches) != len(expectedLines) {
			t.Errorf("Expected %d matches, got %d", len(expectedLines), len(matches))
		}

		for i, match := range matches {
			if i < len(expectedLines) && match.Line != expectedLines[i] {
				t.Errorf("Match %d: expected line %d, got %d", i, expectedLines[i], match.Line)
			}
		}
	})

	t.Run("BeforeAndAfterLines", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "match",
			OutputMode:  ContentOfOutputMode,
			BeforeLines: 1,
			AfterLines:  1,
		})
		if err != nil {
			t.Fatalf("GrepRaw with BeforeLines and AfterLines failed: %v", err)
		}

		expectedLines := []int{2, 3, 4, 5, 6, 7}
		if len(matches) != len(expectedLines) {
			t.Errorf("Expected %d matches, got %d", len(expectedLines), len(matches))
		}

		for i, match := range matches {
			if i < len(expectedLines) && match.Line != expectedLines[i] {
				t.Errorf("Match %d: expected line %d, got %d", i, expectedLines[i], match.Line)
			}
		}
	})

	t.Run("ContextLinesOverridesBeforeAfter", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 1,
			BeforeLines:  5,
			AfterLines:   5,
		})
		if err != nil {
			t.Fatalf("GrepRaw with ContextLines overriding failed: %v", err)
		}

		expectedLines := []int{2, 3, 4, 5, 6, 7}
		if len(matches) != len(expectedLines) {
			t.Errorf("Expected %d matches (ContextLines should override), got %d", len(expectedLines), len(matches))
		}
	})

	t.Run("BoundaryAtFileStart", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/boundary_start.txt",
			Content:  "match line\nline 2\nline 3",
		})

		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			Path:         "/boundary_start.txt",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 5,
		})
		if err != nil {
			t.Fatalf("GrepRaw at file start failed: %v", err)
		}

		if len(matches) == 0 {
			t.Fatal("Expected at least 1 match")
		}
		if matches[0].Line != 1 {
			t.Errorf("Expected first match at line 1, got %d", matches[0].Line)
		}
	})

	t.Run("BoundaryAtFileEnd", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/boundary_end.txt",
			Content:  "line 1\nline 2\nmatch line",
		})

		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			Path:         "/boundary_end.txt",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 5,
		})
		if err != nil {
			t.Fatalf("GrepRaw at file end failed: %v", err)
		}

		if len(matches) != 3 {
			t.Errorf("Expected 3 matches (all lines), got %d", len(matches))
		}
		if matches[len(matches)-1].Line != 3 {
			t.Errorf("Expected last match at line 3, got %d", matches[len(matches)-1].Line)
		}
	})

	t.Run("OverlappingContexts", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/overlap.txt",
			Content:  "line 1\nmatch 2\nline 3\nmatch 4\nline 5",
		})

		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			Path:         "/overlap.txt",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 1,
		})
		if err != nil {
			t.Fatalf("GrepRaw with overlapping contexts failed: %v", err)
		}

		expectedLines := []int{1, 2, 3, 4, 5}
		if len(matches) != len(expectedLines) {
			t.Errorf("Expected %d unique lines (no duplicates), got %d", len(expectedLines), len(matches))
		}

		seenLines := make(map[int]bool)
		for _, match := range matches {
			if seenLines[match.Line] {
				t.Errorf("Duplicate line number %d in results", match.Line)
			}
			seenLines[match.Line] = true
		}
	})

	t.Run("NoContextWhenNotSpecified", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:    "match",
			Path:       "/test.txt",
			OutputMode: ContentOfOutputMode,
		})
		if err != nil {
			t.Fatalf("GrepRaw without context failed: %v", err)
		}

		if len(matches) != 2 {
			t.Errorf("Expected 2 matches (only matching lines), got %d", len(matches))
		}
		for _, match := range matches {
			if match.Content != "line 3 match" && match.Content != "line 6 match" {
				t.Errorf("Unexpected match content: %s", match.Content)
			}
		}
	})

	t.Run("ZeroContextLines", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			Path:         "/test.txt",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 0,
		})
		if err != nil {
			t.Fatalf("GrepRaw with zero context failed: %v", err)
		}

		if len(matches) != 2 {
			t.Errorf("Expected 2 matches (only matching lines), got %d", len(matches))
		}
	})

	t.Run("MultipleFiles", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/multi1.txt",
			Content:  "line 1\nmatch line\nline 3",
		})
		backend.Write(ctx, &WriteRequest{
			FilePath: "/multi2.txt",
			Content:  "line 1\nmatch line\nline 3",
		})

		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:      "match",
			Glob:         "multi*.txt",
			OutputMode:   ContentOfOutputMode,
			ContextLines: 1,
		})
		if err != nil {
			t.Fatalf("GrepRaw with multiple files failed: %v", err)
		}

		fileCount := make(map[string]int)
		for _, match := range matches {
			fileCount[match.Path]++
		}

		if len(fileCount) != 2 {
			t.Errorf("Expected matches from 2 files, got %d", len(fileCount))
		}

		for path, count := range fileCount {
			if count != 3 {
				t.Errorf("Expected 3 lines per file for %s, got %d", path, count)
			}
		}
	})
}

func TestInMemoryBackend_LsInfo_FileInfoMetadata(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	t.Run("FileMetadata", func(t *testing.T) {
		content := "hello world"
		err := backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  content,
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		if len(infos) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos))
		}

		info := infos[0]
		if info.Path != "/test.txt" {
			t.Errorf("Expected path /test.txt, got %s", info.Path)
		}
		if info.IsDir {
			t.Error("Expected IsDir to be false for file")
		}
		if info.Size != int64(len(content)) {
			t.Errorf("Expected size %d, got %d", len(content), info.Size)
		}
		if info.ModifiedAt == "" {
			t.Error("Expected ModifiedAt to be non-empty")
		}
		_, err = time.Parse(time.RFC3339Nano, info.ModifiedAt)
		if err != nil {
			t.Errorf("ModifiedAt is not valid RFC3339 format: %v", err)
		}
	})

	t.Run("DirectoryMetadata", func(t *testing.T) {
		backend := NewInMemoryBackend()
		err := backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/file1.txt",
			Content:  "content1",
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		if len(infos) != 1 {
			t.Fatalf("Expected 1 directory, got %d", len(infos))
		}

		info := infos[0]
		if info.Path != "/dir1" {
			t.Errorf("Expected path /dir1, got %s", info.Path)
		}
		if !info.IsDir {
			t.Error("Expected IsDir to be true for directory")
		}
		if info.Size != 0 {
			t.Errorf("Expected size 0 for directory, got %d", info.Size)
		}
		if info.ModifiedAt == "" {
			t.Error("Expected ModifiedAt to be non-empty for directory")
		}
	})

	t.Run("MixedFilesAndDirectories", func(t *testing.T) {
		backend := NewInMemoryBackend()
		backend.Write(ctx, &WriteRequest{
			FilePath: "/file1.txt",
			Content:  "content1",
		})
		backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/file2.txt",
			Content:  "content2",
		})
		backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/subdir/file3.txt",
			Content:  "content3",
		})

		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		if len(infos) != 2 {
			t.Fatalf("Expected 2 items (file1.txt, dir1), got %d", len(infos))
		}

		fileCount := 0
		dirCount := 0
		for _, info := range infos {
			if info.IsDir {
				dirCount++
				if info.Path != "/dir1" {
					t.Errorf("Expected directory path /dir1, got %s", info.Path)
				}
			} else {
				fileCount++
				if info.Path != "/file1.txt" {
					t.Errorf("Expected file path /file1.txt, got %s", info.Path)
				}
				if info.Size != int64(len("content1")) {
					t.Errorf("Expected file size %d, got %d", len("content1"), info.Size)
				}
			}
		}

		if fileCount != 1 {
			t.Errorf("Expected 1 file, got %d", fileCount)
		}
		if dirCount != 1 {
			t.Errorf("Expected 1 directory, got %d", dirCount)
		}
	})

	t.Run("SubdirectoryListing", func(t *testing.T) {
		backend := NewInMemoryBackend()
		backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/file1.txt",
			Content:  "short",
		})
		backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/subdir/file2.txt",
			Content:  "longer content here",
		})

		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/dir1"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		if len(infos) != 2 {
			t.Fatalf("Expected 2 items (file1.txt, subdir), got %d", len(infos))
		}

		for _, info := range infos {
			if info.Path == "/dir1/file1.txt" {
				if info.IsDir {
					t.Error("Expected file1.txt to be a file")
				}
				if info.Size != int64(len("short")) {
					t.Errorf("Expected size %d, got %d", len("short"), info.Size)
				}
			} else if info.Path == "/dir1/subdir" {
				if !info.IsDir {
					t.Error("Expected subdir to be a directory")
				}
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}
	})

	t.Run("DirectoryModifiedAtUsesLatestFile", func(t *testing.T) {
		backend := NewInMemoryBackend()

		backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/file1.txt",
			Content:  "content1",
		})
		time.Sleep(10 * time.Millisecond)

		backend.Write(ctx, &WriteRequest{
			FilePath: "/dir1/file2.txt",
			Content:  "content2",
		})

		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		if len(infos) != 1 {
			t.Fatalf("Expected 1 directory, got %d", len(infos))
		}

		dirInfo := infos[0]
		if !dirInfo.IsDir {
			t.Fatal("Expected directory")
		}

		dirModTime, _ := time.Parse(time.RFC3339Nano, dirInfo.ModifiedAt)

		subInfos, _ := backend.LsInfo(ctx, &LsInfoRequest{Path: "/dir1"})
		var latestFileTime time.Time
		for _, info := range subInfos {
			fileTime, _ := time.Parse(time.RFC3339Nano, info.ModifiedAt)
			if fileTime.After(latestFileTime) {
				latestFileTime = fileTime
			}
		}

		if !dirModTime.Equal(latestFileTime) && dirModTime.Before(latestFileTime) {
			t.Logf("Directory mod time: %v, Latest file time: %v", dirModTime, latestFileTime)
		}
	})
}

func TestInMemoryBackend_GlobInfo_FileInfoMetadata(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	t.Run("BasicMetadata", func(t *testing.T) {
		content := "test content"
		backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  content,
		})

		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "*.txt",
			Path:    "/",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos))
		}

		info := infos[0]
		if info.Path != "/test.txt" {
			t.Errorf("Expected path /test.txt, got %s", info.Path)
		}
		if info.IsDir {
			t.Error("Expected IsDir to be false")
		}
		if info.Size != int64(len(content)) {
			t.Errorf("Expected size %d, got %d", len(content), info.Size)
		}
		if info.ModifiedAt == "" {
			t.Error("Expected ModifiedAt to be non-empty")
		}
	})

	t.Run("MultipleFilesMetadata", func(t *testing.T) {
		backend := NewInMemoryBackend()
		backend.Write(ctx, &WriteRequest{
			FilePath: "/file1.txt",
			Content:  "short",
		})
		backend.Write(ctx, &WriteRequest{
			FilePath: "/file2.txt",
			Content:  "much longer content",
		})
		backend.Write(ctx, &WriteRequest{
			FilePath: "/file3.py",
			Content:  "python",
		})

		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "*.txt",
			Path:    "/",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 2 {
			t.Fatalf("Expected 2 .txt files, got %d", len(infos))
		}

		for _, info := range infos {
			if info.IsDir {
				t.Errorf("Expected IsDir to be false for %s", info.Path)
			}
			if info.Size <= 0 {
				t.Errorf("Expected positive size for %s, got %d", info.Path, info.Size)
			}
			if info.ModifiedAt == "" {
				t.Errorf("Expected ModifiedAt to be non-empty for %s", info.Path)
			}
		}
	})
}

func TestInMemoryBackend_WriteAndEdit_ModifiedAt(t *testing.T) {
	ctx := context.Background()

	t.Run("WriteUpdatesModifiedAt", func(t *testing.T) {
		backend := NewInMemoryBackend()
		beforeWrite := time.Now()
		time.Sleep(1 * time.Millisecond)

		err := backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  "initial content",
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		time.Sleep(1 * time.Millisecond)
		afterWrite := time.Now()

		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos))
		}

		modTime, err := time.Parse(time.RFC3339Nano, infos[0].ModifiedAt)
		if err != nil {
			t.Fatalf("Failed to parse ModifiedAt: %v", err)
		}

		if modTime.Before(beforeWrite) || modTime.After(afterWrite) {
			t.Errorf("ModifiedAt %v should be between %v and %v", modTime, beforeWrite, afterWrite)
		}
	})

	t.Run("EditUpdatesModifiedAt", func(t *testing.T) {
		backend := NewInMemoryBackend()
		err := backend.Write(ctx, &WriteRequest{
			FilePath: "/edit.txt",
			Content:  "hello world",
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		infos1, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos1) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos1))
		}
		modTime1, err := time.Parse(time.RFC3339Nano, infos1[0].ModifiedAt)
		if err != nil {
			t.Fatalf("Failed to parse ModifiedAt: %v", err)
		}

		time.Sleep(10 * time.Millisecond)

		err = backend.Edit(ctx, &EditRequest{
			FilePath:   "/edit.txt",
			OldString:  "hello",
			NewString:  "hi",
			ReplaceAll: true,
		})
		if err != nil {
			t.Fatalf("Edit failed: %v", err)
		}

		infos2, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos2) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos2))
		}
		modTime2, err := time.Parse(time.RFC3339Nano, infos2[0].ModifiedAt)
		if err != nil {
			t.Fatalf("Failed to parse ModifiedAt: %v", err)
		}

		if !modTime2.After(modTime1) {
			t.Errorf("ModifiedAt should be updated after edit. Before: %v, After: %v", modTime1, modTime2)
		}
	})

	t.Run("OverwriteUpdatesModifiedAt", func(t *testing.T) {
		backend := NewInMemoryBackend()
		err := backend.Write(ctx, &WriteRequest{
			FilePath: "/overwrite.txt",
			Content:  "original",
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		infos1, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos1) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos1))
		}
		modTime1, err := time.Parse(time.RFC3339Nano, infos1[0].ModifiedAt)
		if err != nil {
			t.Fatalf("Failed to parse ModifiedAt: %v", err)
		}

		time.Sleep(10 * time.Millisecond)

		err = backend.Write(ctx, &WriteRequest{
			FilePath: "/overwrite.txt",
			Content:  "new content",
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		infos2, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos2) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos2))
		}
		modTime2, err := time.Parse(time.RFC3339Nano, infos2[0].ModifiedAt)
		if err != nil {
			t.Fatalf("Failed to parse ModifiedAt: %v", err)
		}

		if !modTime2.After(modTime1) {
			t.Errorf("ModifiedAt should be updated after overwrite. Before: %v, After: %v", modTime1, modTime2)
		}
	})

	t.Run("SizeUpdatesAfterEdit", func(t *testing.T) {
		backend := NewInMemoryBackend()
		err := backend.Write(ctx, &WriteRequest{
			FilePath: "/size.txt",
			Content:  "hello",
		})
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}

		infos1, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos1) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos1))
		}
		size1 := infos1[0].Size

		err = backend.Edit(ctx, &EditRequest{
			FilePath:   "/size.txt",
			OldString:  "hello",
			NewString:  "hello world",
			ReplaceAll: true,
		})
		if err != nil {
			t.Fatalf("Edit failed: %v", err)
		}

		infos2, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}
		if len(infos2) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos2))
		}
		size2 := infos2[0].Size

		if size2 <= size1 {
			t.Errorf("Size should increase after edit. Before: %d, After: %d", size1, size2)
		}
		if size2 != int64(len("hello world")) {
			t.Errorf("Expected size %d, got %d", len("hello world"), size2)
		}
	})
}
