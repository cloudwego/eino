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
	"path/filepath"
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

func TestInMemoryBackend_LsInfo_PathIsFilename(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

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

	t.Run("RootDirectory", func(t *testing.T) {
		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		for _, info := range infos {
			if strings.Contains(info.Path, "/") {
				t.Errorf("Path should be filename only, got: %s", info.Path)
			}
			if info.IsDir {
				if info.Path != "dir1" {
					t.Errorf("Expected directory name 'dir1', got: %s", info.Path)
				}
			} else {
				if info.Path != "file1.txt" && info.Path != "file2.txt" {
					t.Errorf("Expected filename 'file1.txt' or 'file2.txt', got: %s", info.Path)
				}
			}
		}
	})

	t.Run("Subdirectory", func(t *testing.T) {
		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/dir1"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		for _, info := range infos {
			if strings.Contains(info.Path, "/") {
				t.Errorf("Path should be filename only, got: %s", info.Path)
			}
			if info.IsDir {
				if info.Path != "subdir" {
					t.Errorf("Expected directory name 'subdir', got: %s", info.Path)
				}
			} else {
				if info.Path != "file3.txt" {
					t.Errorf("Expected filename 'file3.txt', got: %s", info.Path)
				}
			}
		}
	})

	t.Run("NestedSubdirectory", func(t *testing.T) {
		infos, err := backend.LsInfo(ctx, &LsInfoRequest{Path: "/dir1/subdir"})
		if err != nil {
			t.Fatalf("LsInfo failed: %v", err)
		}

		if len(infos) != 1 {
			t.Fatalf("Expected 1 file, got %d", len(infos))
		}

		info := infos[0]
		if info.Path != "file4.txt" {
			t.Errorf("Expected filename 'file4.txt', got: %s", info.Path)
		}
		if strings.Contains(info.Path, "/") {
			t.Errorf("Path should be filename only, got: %s", info.Path)
		}
	})
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

	// Test GlobInfo - match .txt files in root only
	infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
		Pattern: "*.txt",
		Path:    "/",
	})
	if err != nil {
		t.Fatalf("GlobInfo failed: %v", err)
	}
	if len(infos) != 1 { // only file1.txt in root
		t.Errorf("Expected 1 .txt file in root, got %d", len(infos))
	}
	if infos[0].Path != "file1.txt" {
		t.Errorf("Expected relative path 'file1.txt', got %s", infos[0].Path)
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
	if infos[0].Path != "file4.py" {
		t.Errorf("Expected relative path 'file4.py', got %s", infos[0].Path)
	}
}

func TestInMemoryBackend_GlobInfo_RelativePath(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/Users/bytedance/Desktop/github/eino/file1.go",
		Content:  "content1",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/Users/bytedance/Desktop/github/openai-go/paginationmanual_test.go",
		Content:  "content2",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/Users/bytedance/Desktop/github/openai-go/paginationauto_test.go",
		Content:  "content3",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/Users/bytedance/Desktop/other/test.go",
		Content:  "content4",
	})

	t.Run("GlobFromRootWithPattern", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "**/*.go",
			Path:    "/Users/bytedance/Desktop/github",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 3 {
			t.Fatalf("Expected 3 .go files, got %d", len(infos))
		}

		expectedPaths := map[string]bool{
			"eino/file1.go":                      false,
			"openai-go/paginationmanual_test.go": false,
			"openai-go/paginationauto_test.go":   false,
		}

		for _, info := range infos {
			if _, exists := expectedPaths[info.Path]; exists {
				expectedPaths[info.Path] = true
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}

		for path, found := range expectedPaths {
			if !found {
				t.Errorf("Expected path not found: %s", path)
			}
		}
	})

	t.Run("GlobFromSubdirectory", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "*.go",
			Path:    "/Users/bytedance/Desktop/github/openai-go",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 2 {
			t.Fatalf("Expected 2 .go files, got %d", len(infos))
		}

		expectedPaths := map[string]bool{
			"paginationmanual_test.go": false,
			"paginationauto_test.go":   false,
		}

		for _, info := range infos {
			if _, exists := expectedPaths[info.Path]; exists {
				expectedPaths[info.Path] = true
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}

		for path, found := range expectedPaths {
			if !found {
				t.Errorf("Expected path not found: %s", path)
			}
		}
	})

	t.Run("GlobFromRootWithAbsolutePattern", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "/Users/bytedance/Desktop/github/**/*.go",
			Path:    "/",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		expected := map[string]bool{
			"/Users/bytedance/Desktop/github/eino/file1.go":                      false,
			"/Users/bytedance/Desktop/github/openai-go/paginationmanual_test.go": false,
			"/Users/bytedance/Desktop/github/openai-go/paginationauto_test.go":   false,
		}
		for _, info := range infos {
			if _, ok := expected[info.Path]; ok {
				expected[info.Path] = true
			}
		}
		for path, found := range expected {
			if !found {
				t.Errorf("Expected absolute path not found: %s", path)
			}
		}
	})

	t.Run("GlobRecursiveWithRelativePattern", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "**/*.go",
			Path:    "/Users/bytedance/Desktop/github",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 3 {
			t.Fatalf("Expected 3 .go files with ** pattern, got %d", len(infos))
		}

		expected := map[string]bool{
			"eino/file1.go":                      false,
			"openai-go/paginationmanual_test.go": false,
			"openai-go/paginationauto_test.go":   false,
		}

		for _, info := range infos {
			if _, ok := expected[info.Path]; ok {
				expected[info.Path] = true
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}

		for path, found := range expected {
			if !found {
				t.Errorf("Expected relative path not found: %s", path)
			}
		}
	})
}

func TestInMemoryBackend_GlobInfo_RecursivePattern(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/src/main.go",
		Content:  "main",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/src/utils/helper.go",
		Content:  "helper",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/src/utils/deep/nested.go",
		Content:  "nested",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/test/test.go",
		Content:  "test",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/README.md",
		Content:  "readme",
	})

	t.Run("DoubleStarMatchesAllSubdirectories", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "**/*.go",
			Path:    "/project",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 4 {
			t.Fatalf("Expected 4 .go files, got %d", len(infos))
		}

		expected := map[string]bool{
			"src/main.go":              false,
			"src/utils/helper.go":      false,
			"src/utils/deep/nested.go": false,
			"test/test.go":             false,
		}

		for _, info := range infos {
			if _, ok := expected[info.Path]; ok {
				expected[info.Path] = true
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}

		for path, found := range expected {
			if !found {
				t.Errorf("Expected path not found: %s", path)
			}
		}
	})

	t.Run("DoubleStarInMiddleOfPattern", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "src/**/*.go",
			Path:    "/project",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 3 {
			t.Fatalf("Expected 3 .go files under src/, got %d", len(infos))
		}

		expected := map[string]bool{
			"src/main.go":              false,
			"src/utils/helper.go":      false,
			"src/utils/deep/nested.go": false,
		}

		for _, info := range infos {
			if _, ok := expected[info.Path]; ok {
				expected[info.Path] = true
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}

		for path, found := range expected {
			if !found {
				t.Errorf("Expected path not found: %s", path)
			}
		}
	})

	t.Run("DoubleStarAtEnd", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "src/**",
			Path:    "/project",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 3 {
			t.Fatalf("Expected 3 files under src/, got %d", len(infos))
		}

		expected := map[string]bool{
			"src/main.go":              false,
			"src/utils/helper.go":      false,
			"src/utils/deep/nested.go": false,
		}

		for _, info := range infos {
			if _, ok := expected[info.Path]; ok {
				expected[info.Path] = true
			}
		}

		for path, found := range expected {
			if !found {
				t.Errorf("Expected path not found: %s", path)
			}
		}
	})

	t.Run("AbsolutePatternWithDoubleStarRecursive", func(t *testing.T) {
		infos, err := backend.GlobInfo(ctx, &GlobInfoRequest{
			Pattern: "/project/**/*.go",
			Path:    "/",
		})
		if err != nil {
			t.Fatalf("GlobInfo failed: %v", err)
		}

		if len(infos) != 4 {
			t.Fatalf("Expected 4 .go files, got %d", len(infos))
		}

		expected := map[string]bool{
			"/project/src/main.go":              false,
			"/project/src/utils/helper.go":      false,
			"/project/src/utils/deep/nested.go": false,
			"/project/test/test.go":             false,
		}

		for _, info := range infos {
			if _, ok := expected[info.Path]; ok {
				expected[info.Path] = true
			} else {
				t.Errorf("Unexpected path: %s", info.Path)
			}
		}

		for path, found := range expected {
			if !found {
				t.Errorf("Expected absolute path not found: %s", path)
			}
		}
	})
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
				Limit:    10,
			})
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
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
		if info.Path != "test.txt" {
			t.Errorf("Expected path test.txt, got %s", info.Path)
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
		if info.Path != "dir1" {
			t.Errorf("Expected path dir1, got %s", info.Path)
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
				if info.Path != "dir1" {
					t.Errorf("Expected directory path dir1, got %s", info.Path)
				}
			} else {
				fileCount++
				if info.Path != "file1.txt" {
					t.Errorf("Expected file path file1.txt, got %s", info.Path)
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
			if info.Path == "file1.txt" {
				if info.IsDir {
					t.Error("Expected file1.txt to be a file")
				}
				if info.Size != int64(len("short")) {
					t.Errorf("Expected size %d, got %d", len("short"), info.Size)
				}
			} else if info.Path == "subdir" {
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
		if info.Path != "test.txt" {
			t.Errorf("Expected path test.txt, got %s", info.Path)
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

func TestInMemoryBackend_Read_EdgeCases(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/test.txt",
		Content:  "line1\nline2\nline3",
	})

	t.Run("negative offset should be treated as zero", func(t *testing.T) {
		content, err := backend.Read(ctx, &ReadRequest{
			FilePath: "/test.txt",
			Offset:   -5,
			Limit:    2,
		})
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		expected := "     1\tline1\n     2\tline2"
		if content != expected {
			t.Errorf("Expected: %q, Got: %q", expected, content)
		}
	})

	t.Run("offset exceeds file length", func(t *testing.T) {
		content, err := backend.Read(ctx, &ReadRequest{
			FilePath: "/test.txt",
			Offset:   100,
			Limit:    10,
		})
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		if content != "" {
			t.Errorf("Expected empty content, got: %q", content)
		}
	})

	t.Run("zero or negative limit should use default 200", func(t *testing.T) {
		content, err := backend.Read(ctx, &ReadRequest{
			FilePath: "/test.txt",
			Offset:   0,
			Limit:    0,
		})
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		lines := strings.Split(content, "\n")
		if len(lines) != 3 {
			t.Errorf("Expected 3 lines, got %d", len(lines))
		}
	})

	t.Run("limit exceeds remaining lines", func(t *testing.T) {
		content, err := backend.Read(ctx, &ReadRequest{
			FilePath: "/test.txt",
			Offset:   1,
			Limit:    100,
		})
		if err != nil {
			t.Fatalf("Read failed: %v", err)
		}
		lines := strings.Split(content, "\n")
		if len(lines) != 2 {
			t.Errorf("Expected 2 lines, got %d", len(lines))
		}
	})
}

func TestInMemoryBackend_Edit_EdgeCases(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	t.Run("edit non-existent file", func(t *testing.T) {
		err := backend.Edit(ctx, &EditRequest{
			FilePath:  "/nonexistent.txt",
			OldString: "old",
			NewString: "new",
		})
		if err == nil {
			t.Error("Expected error for non-existent file")
		}
		if !strings.Contains(err.Error(), "not found") {
			t.Errorf("Expected 'not found' error, got: %v", err)
		}
	})

	t.Run("empty oldString", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  "content",
		})

		err := backend.Edit(ctx, &EditRequest{
			FilePath:  "/test.txt",
			OldString: "",
			NewString: "new",
		})
		if err == nil {
			t.Error("Expected error for empty oldString")
		}
		if !strings.Contains(err.Error(), "non-empty") {
			t.Errorf("Expected 'non-empty' error, got: %v", err)
		}
	})

	t.Run("oldString not found", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  "hello world",
		})

		err := backend.Edit(ctx, &EditRequest{
			FilePath:  "/test.txt",
			OldString: "notfound",
			NewString: "new",
		})
		if err == nil {
			t.Error("Expected error when oldString not found")
		}
		if !strings.Contains(err.Error(), "not found in file") {
			t.Errorf("Expected 'not found in file' error, got: %v", err)
		}
	})

	t.Run("multiple occurrences with ReplaceAll false", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  "foo bar foo baz",
		})

		err := backend.Edit(ctx, &EditRequest{
			FilePath:   "/test.txt",
			OldString:  "foo",
			NewString:  "FOO",
			ReplaceAll: false,
		})
		if err == nil {
			t.Error("Expected error for multiple occurrences with ReplaceAll=false")
		}
		if !strings.Contains(err.Error(), "multiple occurrences") {
			t.Errorf("Expected 'multiple occurrences' error, got: %v", err)
		}
	})

	t.Run("single occurrence with ReplaceAll false", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  "foo bar baz",
		})

		err := backend.Edit(ctx, &EditRequest{
			FilePath:   "/test.txt",
			OldString:  "foo",
			NewString:  "FOO",
			ReplaceAll: false,
		})
		if err != nil {
			t.Fatalf("Edit failed: %v", err)
		}

		content, _ := backend.Read(ctx, &ReadRequest{
			FilePath: "/test.txt",
			Limit:    100,
		})
		if !strings.Contains(content, "FOO") {
			t.Error("Expected content to contain 'FOO'")
		}
	})

	t.Run("ReplaceAll replaces all occurrences", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/test.txt",
			Content:  "foo bar foo baz foo",
		})

		err := backend.Edit(ctx, &EditRequest{
			FilePath:   "/test.txt",
			OldString:  "foo",
			NewString:  "FOO",
			ReplaceAll: true,
		})
		if err != nil {
			t.Fatalf("Edit failed: %v", err)
		}

		content, _ := backend.Read(ctx, &ReadRequest{
			FilePath: "/test.txt",
			Limit:    100,
		})
		if strings.Contains(content, "foo") {
			t.Error("Expected all 'foo' to be replaced")
		}
		fooCount := strings.Count(content, "FOO")
		if fooCount != 3 {
			t.Errorf("Expected 3 occurrences of 'FOO', got %d", fooCount)
		}
	})
}

func TestInMemoryBackend_NormalizePath(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	t.Run("paths are normalized on write", func(t *testing.T) {
		testCases := []struct {
			inputPath      string
			normalizedPath string
		}{
			{"test.txt", "/test.txt"},
			{"/test.txt", "/test.txt"},
			{"//test.txt", "/test.txt"},
			{"/dir//file.txt", "/dir/file.txt"},
			{"/dir/../file.txt", "/file.txt"},
		}

		for _, tc := range testCases {
			backend.Write(ctx, &WriteRequest{
				FilePath: tc.inputPath,
				Content:  "content",
			})

			content, err := backend.Read(ctx, &ReadRequest{
				FilePath: tc.normalizedPath,
				Limit:    10,
			})
			if err != nil {
				t.Errorf("Failed to read normalized path %s (from %s): %v", tc.normalizedPath, tc.inputPath, err)
			}
			if !strings.Contains(content, "content") {
				t.Errorf("Content not found for normalized path %s (from %s)", tc.normalizedPath, tc.inputPath)
			}
		}
	})
}

func TestInMemoryBackend_MatchFileType(t *testing.T) {
	testCases := []struct {
		ext      string
		fileType string
		expected bool
	}{
		{"go", "go", true},
		{"py", "python", true},
		{"py", "py", true},
		{"js", "js", true},
		{"ts", "typescript", true},
		{"ts", "ts", true},
		{"cpp", "cpp", true},
		{"c", "c", true},
		{"h", "c", true},
		{"md", "markdown", true},
		{"txt", "txt", true},
		{"go", "python", false},
		{"js", "typescript", false},
		{"unknown", "go", false},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%s matches %s", tc.ext, tc.fileType), func(t *testing.T) {
			result := matchFileType(tc.ext, tc.fileType)
			if result != tc.expected {
				t.Errorf("matchFileType(%q, %q) = %v, expected %v", tc.ext, tc.fileType, result, tc.expected)
			}
		})
	}
}

func TestInMemoryBackend_GrepRaw(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/test.go",
		Content:  "package main\nfunc main() {\n\tlog.Error(\"error\")\n\tfmt.Println(\"hello\")\n}",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/test.py",
		Content:  "def hello():\n    print('error')\n    print('world')",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir/file.go",
		Content:  "package test\nfunc TestError() {\n\tlog.Error(\"test error\")\n}",
	})

	t.Run("basic pattern search", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "error",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 3 {
			t.Errorf("Expected 2 matches, got %d", len(matches))
		}
	})

	t.Run("empty pattern error", func(t *testing.T) {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "",
		})
		if err == nil {
			t.Error("Expected error for empty pattern")
		}
		if !strings.Contains(err.Error(), "cannot be empty") {
			t.Errorf("Expected 'cannot be empty' error, got: %v", err)
		}
	})

	t.Run("invalid regex pattern", func(t *testing.T) {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "[invalid",
		})
		if err == nil {
			t.Error("Expected error for invalid regex")
		}
		if !strings.Contains(err.Error(), "invalid regex") {
			t.Errorf("Expected 'invalid regex' error, got: %v", err)
		}
	})

	t.Run("case sensitive search", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "Error",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 3 {
			t.Errorf("Expected 2 matches, got %d", len(matches))
		}
	})

	t.Run("case insensitive search", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:         "ERROR",
			CaseInsensitive: true,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 3 {
			t.Errorf("Expected at least 2 matches, got %d", len(matches))
		}
	})

	t.Run("filter by file type", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:  "error",
			FileType: "go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasSuffix(match.Path, ".go") {
				t.Errorf("Expected only .go files, got: %s", match.Path)
			}
		}
	})

	t.Run("filter by glob pattern", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "Error",
			Glob:    "*.go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasSuffix(match.Path, ".go") {
				t.Errorf("Expected only .go files, got: %s", match.Path)
			}
		}
	})

	t.Run("invalid glob pattern", func(t *testing.T) {
		_, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "error",
			Glob:    "[invalid",
		})
		if err == nil {
			t.Error("Expected error for invalid glob pattern")
		}
		if !strings.Contains(err.Error(), "invalid glob") {
			t.Errorf("Expected 'invalid glob' error, got: %v", err)
		}
	})

	t.Run("search in specific path", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "Error",
			Path:    "/dir",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasPrefix(match.Path, "/dir") {
				t.Errorf("Expected matches only from /dir, got: %s", match.Path)
			}
		}
	})

	t.Run("search with non-existent path", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "error",
			Path:    "/nonexistent",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("Expected 0 matches for non-existent path, got %d", len(matches))
		}
	})

	t.Run("regex pattern matching", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "log\\..*Error",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 1 {
			t.Errorf("Expected at least 1 match, got %d", len(matches))
		}
	})

	t.Run("no matches found", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "nonexistent_pattern_xyz",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("Expected 0 matches, got %d", len(matches))
		}
	})

	t.Run("match line numbers", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:  "log\\.Error",
			FileType: "go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if match.Line <= 0 {
				t.Errorf("Expected positive line number, got %d", match.Line)
			}
		}
	})

	t.Run("match content is returned", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "package main",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 1 {
			t.Fatal("Expected at least 1 match")
		}
		found := false
		for _, match := range matches {
			if strings.Contains(match.Content, "package main") {
				found = true
				break
			}
		}
		if !found {
			t.Error("Expected match content to contain 'package main'")
		}
	})
}

func TestInMemoryBackend_GrepRaw_WithContext(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/context.txt",
		Content:  "line1\nline2\ntarget line\nline4\nline5\nline6",
	})

	t.Run("with before context", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "target",
			BeforeLines: 2,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 3 {
			t.Errorf("Expected at least 3 matches (2 before + target), got %d", len(matches))
		}
	})

	t.Run("with after context", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:    "target",
			AfterLines: 2,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 3 {
			t.Errorf("Expected at least 3 matches (target + 2 after), got %d", len(matches))
		}
	})

	t.Run("with both before and after context", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "target",
			BeforeLines: 1,
			AfterLines:  1,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 3 {
			t.Errorf("Expected at least 3 matches (1 before + target + 1 after), got %d", len(matches))
		}
	})

	t.Run("context at file boundaries", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/boundary.txt",
			Content:  "first line target\nsecond line",
		})
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "target",
			Path:        "/boundary.txt",
			BeforeLines: 5,
			AfterLines:  5,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) == 0 {
			t.Error("Expected at least 1 match")
		}
	})

	t.Run("zero context lines", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "target",
			BeforeLines: 0,
			AfterLines:  0,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 1 {
			t.Error("Expected at least 1 match")
		}
	})

	t.Run("negative context lines treated as zero", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:     "target",
			BeforeLines: -5,
			AfterLines:  -5,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) < 1 {
			t.Error("Expected at least 1 match")
		}
	})
}

func TestInMemoryBackend_GrepRaw_Multiline(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/multiline.txt",
		Content:  "start\nmiddle line\nend",
	})

	t.Run("single line mode (default)", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:         "start.*end",
			EnableMultiline: false,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("Expected 0 matches in single-line mode, got %d", len(matches))
		}
	})

	t.Run("multiline mode enabled", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:         "start[\\s\\S]*end",
			EnableMultiline: true,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) == 0 {
			t.Error("Expected matches in multiline mode")
		}
	})

	t.Run("multiline with multiple matches", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/multiline2.txt",
			Content:  "block1 start\nblock1 middle\nblock1 end\n\nblock2 start\nblock2 end",
		})
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:         "start[\\s\\S]*?end",
			Path:            "/multiline2.txt",
			EnableMultiline: true,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) == 0 {
			t.Error("Expected matches in multiline mode")
		}
	})

	t.Run("multiline with multiple matches v2", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/multiline3.txt",
			Content: `
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
		})
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:         "function calculateTotal\\([^\\)]*\\)",
			Path:            "/multiline3.txt",
			EnableMultiline: true,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) == 0 {
			t.Error("Expected matches in multiline mode")
		}

		foundLastLine := false
		for _, match := range matches {
			if match.Line == 6 && strings.Contains(match.Content, ") {") {
				foundLastLine = true
				break
			}
		}
		if !foundLastLine {
			t.Error("Expected to find line 5 with ') {' in content")
			for _, match := range matches {
				t.Logf("Line %d: %s", match.Line, match.Content)
			}
		}
	})

}

func TestInMemoryBackend_GrepRaw_EmptyFiles(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	t.Run("search in empty file", func(t *testing.T) {
		backend.Write(ctx, &WriteRequest{
			FilePath: "/empty.txt",
			Content:  "",
		})
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "anything",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("Expected 0 matches in empty file, got %d", len(matches))
		}
	})

	t.Run("search with no files", func(t *testing.T) {
		emptyBackend := NewInMemoryBackend()
		matches, err := emptyBackend.GrepRaw(ctx, &GrepRequest{
			Pattern: "anything",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("Expected 0 matches with no files, got %d", len(matches))
		}
	})
}

func TestInMemoryBackend_GrepRaw_SpecialCharacters(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/special.txt",
		Content:  "interface{}\nmap[string]int\nfunc() error\n$variable\n*pointer",
	})

	t.Run("match curly braces", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "interface\\{\\}",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 1 {
			t.Errorf("Expected 1 match, got %d", len(matches))
		}
	})

	t.Run("match square brackets", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "map\\[.*\\]",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 1 {
			t.Errorf("Expected 1 match, got %d", len(matches))
		}
	})

	t.Run("match parentheses", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "func\\(\\)",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 1 {
			t.Errorf("Expected 1 match, got %d", len(matches))
		}
	})

	t.Run("match dollar sign", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "\\$variable",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 1 {
			t.Errorf("Expected 1 match, got %d", len(matches))
		}
	})

	t.Run("match asterisk", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "\\*pointer",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 1 {
			t.Errorf("Expected 1 match, got %d", len(matches))
		}
	})
}

func TestInMemoryBackend_GrepRaw_Concurrent(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 10; i++ {
		backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/file%d.txt", i),
			Content:  fmt.Sprintf("content%d with error message", i),
		})
	}

	t.Run("concurrent grep operations", func(t *testing.T) {
		done := make(chan bool)
		for i := 0; i < 10; i++ {
			go func() {
				_, err := backend.GrepRaw(ctx, &GrepRequest{
					Pattern: "error",
				})
				if err != nil {
					t.Errorf("Concurrent GrepRaw failed: %v", err)
				}
				done <- true
			}()
		}

		for i := 0; i < 10; i++ {
			<-done
		}
	})

	t.Run("parallel file processing", func(t *testing.T) {
		backend := NewInMemoryBackend()
		for i := 0; i < 100; i++ {
			backend.Write(ctx, &WriteRequest{
				FilePath: fmt.Sprintf("/large/file%d.go", i),
				Content:  fmt.Sprintf("package main\nimport \"log\"\nfunc test%d() {\n\tlog.Error(\"error %d\")\n}", i, i),
			})
		}

		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:  "log\\.Error",
			FileType: "go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 100 {
			t.Errorf("Expected 100 matches, got %d", len(matches))
		}
	})

	t.Run("single file no parallelism", func(t *testing.T) {
		backend := NewInMemoryBackend()
		backend.Write(ctx, &WriteRequest{
			FilePath: "/single.txt",
			Content:  "error line 1\nerror line 2\nerror line 3",
		})

		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "error",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 3 {
			t.Errorf("Expected 3 matches, got %d", len(matches))
		}
	})

	t.Run("empty files list", func(t *testing.T) {
		backend := NewInMemoryBackend()
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "anything",
			Path:    "/nonexistent",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) != 0 {
			t.Errorf("Expected 0 matches, got %d", len(matches))
		}
	})

	t.Run("concurrent operations are safe", func(t *testing.T) {
		backend := NewInMemoryBackend()
		for i := 0; i < 20; i++ {
			backend.Write(ctx, &WriteRequest{
				FilePath: fmt.Sprintf("/concurrent/file%d.txt", i),
				Content:  fmt.Sprintf("line1\nline2\npattern%d\nline4", i),
			})
		}

		done := make(chan error, 5)
		for i := 0; i < 5; i++ {
			go func(id int) {
				_, err := backend.GrepRaw(ctx, &GrepRequest{
					Pattern: "pattern\\d+",
				})
				done <- err
			}(i)
		}

		for i := 0; i < 5; i++ {
			if err := <-done; err != nil {
				t.Errorf("Concurrent operation %d failed: %v", i, err)
			}
		}
	})
}

func BenchmarkInMemoryBackend_GrepRaw(b *testing.B) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		content := fmt.Sprintf(`package main

import (
	"fmt"
	"log"
)

func process%d() error {
	log.Error("processing error %d")
	fmt.Println("hello world")
	return nil
}

func calculate%d(x, y int) int {
	return x + y
}
`, i, i, i)
		backend.Write(ctx, &WriteRequest{
			FilePath: fmt.Sprintf("/project/src/file%d.go", i),
			Content:  content,
		})
	}

	b.Run("parallel_grep", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := backend.GrepRaw(ctx, &GrepRequest{
				Pattern:  "log\\.Error",
				FileType: "go",
			})
			if err != nil {
				b.Fatalf("GrepRaw failed: %v", err)
			}
		}
	})

	b.Run("with_glob_filter", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := backend.GrepRaw(ctx, &GrepRequest{
				Pattern: "Error",
				Glob:    "**/*.go",
			})
			if err != nil {
				b.Fatalf("GrepRaw failed: %v", err)
			}
		}
	})

	b.Run("case_insensitive", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := backend.GrepRaw(ctx, &GrepRequest{
				Pattern:         "ERROR",
				CaseInsensitive: true,
			})
			if err != nil {
				b.Fatalf("GrepRaw failed: %v", err)
			}
		}
	})
}

func TestInMemoryBackend_GrepRaw_ComplexScenarios(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/src/main.go",
		Content:  "package main\nimport \"log\"\nfunc main() {\n\tlog.Error(\"error\")\n}",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/src/utils/helper.go",
		Content:  "package utils\nfunc Helper() error {\n\treturn nil\n}",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/project/test/main_test.go",
		Content:  "package main\nimport \"testing\"\nfunc TestMain(t *testing.T) {\n}",
	})

	t.Run("combine path and file type filters", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:  "package",
			Path:     "/project/src",
			FileType: "go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasPrefix(match.Path, "/project/src") {
				t.Errorf("Expected path to start with /project/src, got: %s", match.Path)
			}
			if !strings.HasSuffix(match.Path, ".go") {
				t.Errorf("Expected .go file, got: %s", match.Path)
			}
		}
	})

	t.Run("complex regex with case insensitive", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern:         "func\\s+\\w+",
			CaseInsensitive: true,
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		if len(matches) == 0 {
			t.Error("Expected at least 1 match for function declarations")
		}
	})

	t.Run("glob with directory structure", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "package",
			Glob:    "*_test.go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasSuffix(match.Path, "_test.go") {
				t.Errorf("Expected test file, got: %s", match.Path)
			}
		}
	})

	t.Run("glob with recursive pattern", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "package",
			Glob:    "**/*.go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasSuffix(match.Path, ".go") {
				t.Errorf("Expected .go file, got: %s", match.Path)
			}
		}
		if len(matches) == 0 {
			t.Error("Expected at least 1 match for **/*.go pattern")
		}
	})

	t.Run("glob with path prefix", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "package",
			Glob:    "src/**/*.go",
			Path:    "/project",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if !strings.HasPrefix(match.Path, "/project/src") {
				t.Errorf("Expected path to start with /project/src, got: %s", match.Path)
			}
			if !strings.HasSuffix(match.Path, ".go") {
				t.Errorf("Expected .go file, got: %s", match.Path)
			}
		}
	})

	t.Run("glob simple filename pattern", func(t *testing.T) {
		matches, err := backend.GrepRaw(ctx, &GrepRequest{
			Pattern: "package",
			Glob:    "main.go",
		})
		if err != nil {
			t.Fatalf("GrepRaw failed: %v", err)
		}
		for _, match := range matches {
			if filepath.Base(match.Path) != "main.go" {
				t.Errorf("Expected filename 'main.go', got: %s", match.Path)
			}
		}
	})
}
