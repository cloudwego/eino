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
	err := backend.Write(ctx, "/test.txt", "line1\nline2\nline3\nline4\nline5")
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	// Test Read - full content
	content, err := backend.Read(ctx, "/test.txt", 0, 100)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	expected := "line1\nline2\nline3\nline4\nline5"
	if content != expected {
		t.Errorf("Read content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Read - with offset and limit
	content, err = backend.Read(ctx, "/test.txt", 1, 2)
	if err != nil {
		t.Fatalf("Read with offset failed: %v", err)
	}
	expected = "line2\nline3"
	if content != expected {
		t.Errorf("Read with offset content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Read - non-existent file
	_, err = backend.Read(ctx, "/nonexistent.txt", 0, 10)
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}
}

func TestInMemoryBackend_LsInfo(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create some files
	backend.Write(ctx, "/file1.txt", "content1")
	backend.Write(ctx, "/file2.txt", "content2")
	backend.Write(ctx, "/dir1/file3.txt", "content3")
	backend.Write(ctx, "/dir1/subdir/file4.txt", "content4")
	backend.Write(ctx, "/dir2/file5.txt", "content5")

	// Test LsInfo - root
	infos, err := backend.LsInfo(ctx, "/")
	if err != nil {
		t.Fatalf("LsInfo failed: %v", err)
	}
	if len(infos) != 4 { // file1.txt, file2.txt, dir1, dir2
		t.Errorf("Expected 4 items in root, got %d", len(infos))
	}

	// Test LsInfo - specific directory
	infos, err = backend.LsInfo(ctx, "/dir1")
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
	backend.Write(ctx, "/edit.txt", "hello world\nhello again\nhello world")

	// Test Edit - replace first occurrence
	err := backend.Edit(ctx, "/edit.txt", "hello", "hi", false)
	if err != nil {
		t.Fatalf("Edit failed: %v", err)
	}

	content, _ := backend.Read(ctx, "/edit.txt", 0, 100)
	expected := "hi world\nhello again\nhello world"
	if content != expected {
		t.Errorf("Edit (replace first) content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Edit - replace all occurrences
	backend.Write(ctx, "/edit2.txt", "hello world\nhello again\nhello world")
	err = backend.Edit(ctx, "/edit2.txt", "hello", "hi", true)
	if err != nil {
		t.Fatalf("Edit (replace all) failed: %v", err)
	}

	content, _ = backend.Read(ctx, "/edit2.txt", 0, 100)
	expected = "hi world\nhi again\nhi world"
	if content != expected {
		t.Errorf("Edit (replace all) content mismatch. Expected: %q, Got: %q", expected, content)
	}

	// Test Edit - non-existent file
	err = backend.Edit(ctx, "/nonexistent.txt", "old", "new", false)
	if err == nil {
		t.Error("Expected error for non-existent file, got nil")
	}

	// Test Edit - empty oldString
	err = backend.Edit(ctx, "/edit.txt", "", "new", false)
	if err == nil {
		t.Error("Expected error for empty oldString, got nil")
	}
}

func TestInMemoryBackend_GrepRaw(t *testing.T) {
	backend := NewInMemoryBackend()
	ctx := context.Background()

	// Create some files
	backend.Write(ctx, "/file1.txt", "hello world\nfoo bar\nhello again")
	backend.Write(ctx, "/file2.py", "print('hello')\nprint('world')")
	backend.Write(ctx, "/dir1/file3.txt", "hello from dir1")

	// Test GrepRaw - search all files
	matches, err := backend.GrepRaw(ctx, "hello", nil, nil)
	if err != nil {
		t.Fatalf("GrepRaw failed: %v", err)
	}
	if len(matches) != 4 { // 3 in file1.txt, 1 in file2.py, 1 in dir1/file3.txt
		t.Errorf("Expected 4 matches, got %d", len(matches))
	}

	// Test GrepRaw - with glob filter
	glob := "*.py"
	matches, err = backend.GrepRaw(ctx, "hello", nil, &glob)
	if err != nil {
		t.Fatalf("GrepRaw with glob failed: %v", err)
	}
	if len(matches) != 1 {
		t.Errorf("Expected 1 match with *.py glob, got %d", len(matches))
	}

	// Test GrepRaw - with path filter
	path := "/dir1"
	matches, err = backend.GrepRaw(ctx, "hello", &path, nil)
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
	backend.Write(ctx, "/file1.txt", "content1")
	backend.Write(ctx, "/file2.py", "content2")
	backend.Write(ctx, "/dir1/file3.txt", "content3")
	backend.Write(ctx, "/dir1/file4.py", "content4")

	// Test GlobInfo - match all .txt files
	infos, err := backend.GlobInfo(ctx, "*.txt", "/")
	if err != nil {
		t.Fatalf("GlobInfo failed: %v", err)
	}
	if len(infos) != 2 { // only file1.txt in root
		t.Errorf("Expected 1 .txt file in root, got %d", len(infos))
	}

	// Test GlobInfo - match all .py files in dir1
	infos, err = backend.GlobInfo(ctx, "*.py", "/dir1")
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
			backend.Write(ctx, "/concurrent.txt", "content")
			backend.Read(ctx, "/concurrent.txt", 0, 10)
			done <- true
		}(i)
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}
