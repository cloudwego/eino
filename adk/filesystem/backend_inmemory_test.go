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
	"testing"
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
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create some files
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file1.txt",
		Content:  "hello world\nfoo bar\nhello again",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/file2.py",
		Content:  "print('hello')\nprint('world')",
	})
	backend.Write(ctx, &WriteRequest{
		FilePath: "/dir1/file3.txt",
		Content:  "hello from dir1",
	})

	// Test GrepRaw - search all files
	matches, err := backend.GrepRaw(ctx, &GrepRequest{
		Pattern: "hello",
		Path:    "",
		Glob:    "",
	})
	if err != nil {
		t.Fatalf("GrepRaw failed: %v", err)
	}
	if len(matches) != 4 { // 3 in file1.txt, 1 in file2.py, 1 in dir1/file3.txt
		t.Errorf("Expected 4 matches, got %d", len(matches))
	}

	// Test GrepRaw - with glob filter
	glob := "*.py"
	matches, err = backend.GrepRaw(ctx, &GrepRequest{
		Pattern: "hello",
		Path:    "",
		Glob:    glob,
	})
	if err != nil {
		t.Fatalf("GrepRaw with glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("Expected 1 match with *.py glob, got %d", len(matches))
	}

	// Test GrepRaw - with path filter
	path := "/dir1"
	matches, err = backend.GrepRaw(ctx, &GrepRequest{
		Pattern: "hello",
		Path:    path,
		Glob:    "",
	})
	if err != nil {
		t.Fatalf("GrepRaw with path failed: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("Expected 1 match in /dir1, got %d", len(matches))
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
