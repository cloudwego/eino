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

package deepagents

import (
	"context"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/cloudwego/eino/components/tool"
)

// mockStorage is a simple in-memory implementation of Storage for testing.
type mockStorage struct {
	files map[string][]byte
	dirs  map[string]bool
}

func newMockStorage() *mockStorage {
	return &mockStorage{
		files: make(map[string][]byte),
		dirs:  make(map[string]bool),
	}
}

// NewMockStorage creates a new mock storage instance for testing.
// This is exported so it can be used by other test files.
func NewMockStorage() Storage {
	return newMockStorage()
}

func (m *mockStorage) Read(ctx context.Context, path string) ([]byte, error) {
	if m.dirs[path] {
		return nil, &storageError{path: path, isDir: true}
	}
	data, ok := m.files[path]
	if !ok {
		return nil, &storageError{path: path, notFound: true}
	}
	return data, nil
}

func (m *mockStorage) Write(ctx context.Context, path string, content []byte) error {
	m.files[path] = content
	return nil
}

func (m *mockStorage) List(ctx context.Context, path string) ([]StorageItem, error) {
	if !m.dirs[path] && path != "/" {
		// Check if path exists as a file
		if _, ok := m.files[path]; ok {
			return nil, &storageError{path: path, isFile: true}
		}
		return nil, &storageError{path: path, notFound: true}
	}

	items := make([]StorageItem, 0)
	prefix := path
	if path != "/" {
		prefix = path + "/"
	}

	// List files
	for filePath := range m.files {
		if len(filePath) > len(prefix) && filePath[:len(prefix)] == prefix {
			// Extract the name after the prefix
			name := filePath[len(prefix):]
			// Check if it's a direct child (no more slashes)
			if len(name) > 0 && name[0] != '/' {
				items = append(items, StorageItem{Name: name, IsDir: false})
			}
		}
	}

	// List directories
	for dirPath := range m.dirs {
		if len(dirPath) > len(prefix) && dirPath[:len(prefix)] == prefix {
			name := dirPath[len(prefix):]
			if len(name) > 0 && name[0] != '/' {
				items = append(items, StorageItem{Name: name, IsDir: true})
			}
		}
	}

	return items, nil
}

func (m *mockStorage) Delete(ctx context.Context, path string) error {
	delete(m.files, path)
	delete(m.dirs, path)
	return nil
}

func (m *mockStorage) Exists(ctx context.Context, path string) (bool, error) {
	_, fileExists := m.files[path]
	_, dirExists := m.dirs[path]
	return fileExists || dirExists, nil
}

type storageError struct {
	path     string
	notFound bool
	isDir    bool
	isFile   bool
}

func (e *storageError) Error() string {
	if e.notFound {
		return "path not found: " + e.path
	}
	if e.isDir {
		return "path is a directory: " + e.path
	}
	if e.isFile {
		return "path is a file: " + e.path
	}
	return "storage error: " + e.path
}

func TestStorageTools(t *testing.T) {
	Convey("TestStorageTools", t, func() {
		ctx := context.Background()
		storage := newMockStorage()

		// Setup: create a directory and a file
		storage.dirs["/"] = true
		storage.files["/test.txt"] = []byte("hello world")

		// Test ls tool
		Convey("Test ls tool", func() {
			lsTool := &lsTool{storage: storage}

			// Test listing root directory
			lsArgs := `{"path": "/"}`
			result, err := lsTool.InvokableRun(ctx, lsArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "test.txt")

			// Test empty directory
			storage.dirs["/empty"] = true
			lsArgs = `{"path": "/empty"}`
			result, err = lsTool.InvokableRun(ctx, lsArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "empty")

			// Test nested directory
			storage.dirs["/nested"] = true
			storage.files["/nested/file.txt"] = []byte("nested content")
			lsArgs = `{"path": "/nested"}`
			result, err = lsTool.InvokableRun(ctx, lsArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "file.txt")

			// Test path not found
			lsArgs = `{"path": "/nonexistent"}`
			_, err = lsTool.InvokableRun(ctx, lsArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to list directory")

			// Test path is a file (should error)
			lsArgs = `{"path": "/test.txt"}`
			_, err = lsTool.InvokableRun(ctx, lsArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to list directory")
		})

		// Test read_file tool
		Convey("Test read_file tool", func() {
			tools := NewStorageTools(storage)
			var readTool tool.BaseTool
			for _, t := range tools {
				info, _ := t.Info(ctx)
				if info.Name == "read_file" {
					readTool = t
					break
				}
			}
			So(readTool, ShouldNotBeNil)

			type invokable interface {
				InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
			}
			invReadTool := readTool.(invokable)

			// Test successful read
			readArgs := `{"path": "/test.txt"}`
			result, err := invReadTool.InvokableRun(ctx, readArgs)
			So(err, ShouldBeNil)
			So(result, ShouldEqual, "hello world")

			// Test file not found
			readArgs = `{"path": "/nonexistent.txt"}`
			_, err = invReadTool.InvokableRun(ctx, readArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to read file")

			// Test path is a directory (should error)
			readArgs = `{"path": "/"}`
			_, err = invReadTool.InvokableRun(ctx, readArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to read file")
		})

		// Test write_file tool
		Convey("Test write_file tool", func() {
			tools := NewStorageTools(storage)
			var writeTool tool.BaseTool
			for _, t := range tools {
				info, _ := t.Info(ctx)
				if info.Name == "write_file" {
					writeTool = t
					break
				}
			}
			So(writeTool, ShouldNotBeNil)

			type invokable interface {
				InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
			}
			invWriteTool := writeTool.(invokable)

			// Test creating new file
			writeArgs := `{"path": "/new.txt", "content": "new content"}`
			result, err := invWriteTool.InvokableRun(ctx, writeArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote")

			// Verify the file was written
			content, err := storage.Read(ctx, "/new.txt")
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, "new content")

			// Test overwriting existing file
			writeArgs = `{"path": "/test.txt", "content": "overwritten"}`
			result, err = invWriteTool.InvokableRun(ctx, writeArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully wrote")

			// Verify overwrite
			content, err = storage.Read(ctx, "/test.txt")
			So(err, ShouldBeNil)
			So(string(content), ShouldEqual, "overwritten")
		})

		// Test edit_file tool
		Convey("Test edit_file tool", func() {
			tools := NewStorageTools(storage)
			var editTool tool.BaseTool
			for _, t := range tools {
				info, _ := t.Info(ctx)
				if info.Name == "edit_file" {
					editTool = t
					break
				}
			}
			So(editTool, ShouldNotBeNil)

			type invokable interface {
				InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
			}
			invEditTool := editTool.(invokable)

			// Reset test file
			storage.files["/test.txt"] = []byte("line1\nline2\nline3")

			// Test insert operation - normal case
			editArgs := `{"path": "/test.txt", "operation": "insert", "position": 1, "content": "inserted\n"}`
			result, err := invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully edited")

			// Verify the insert
			content, err := storage.Read(ctx, "/test.txt")
			So(err, ShouldBeNil)
			So(string(content), ShouldContainSubstring, "inserted")

			// Test insert at end
			storage.files["/test.txt"] = []byte("line1\nline2")
			editArgs = `{"path": "/test.txt", "operation": "insert", "position": 3, "content": "line3\n"}`
			result, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully edited")

			// Test insert - missing position
			editArgs = `{"path": "/test.txt", "operation": "insert", "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "position is required")

			// Test insert - position out of range (too high)
			storage.files["/test.txt"] = []byte("line1\nline2")
			editArgs = `{"path": "/test.txt", "operation": "insert", "position": 10, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "out of range")

			// Test insert - position out of range (too low)
			editArgs = `{"path": "/test.txt", "operation": "insert", "position": 0, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "out of range")

			// Test replace operation - normal case
			storage.files["/test2.txt"] = []byte("line1\nline2\nline3")
			editArgs = `{"path": "/test2.txt", "operation": "replace", "position": 1, "end_position": 2, "content": "replaced"}`
			result, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully edited")

			// Verify replace
			content, err = storage.Read(ctx, "/test2.txt")
			So(err, ShouldBeNil)
			So(string(content), ShouldContainSubstring, "replaced")

			// Test replace - missing position
			editArgs = `{"path": "/test2.txt", "operation": "replace", "end_position": 2, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "position and end_position are required")

			// Test replace - missing end_position
			editArgs = `{"path": "/test2.txt", "operation": "replace", "position": 1, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "position and end_position are required")

			// Test replace - invalid range (end < start)
			editArgs = `{"path": "/test2.txt", "operation": "replace", "position": 3, "end_position": 1, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "invalid range")

			// Test replace - range out of bounds
			storage.files["/test2.txt"] = []byte("line1\nline2")
			editArgs = `{"path": "/test2.txt", "operation": "replace", "position": 1, "end_position": 10, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "invalid range")

			// Test delete operation - normal case
			storage.files["/test3.txt"] = []byte("line1\nline2\nline3")
			editArgs = `{"path": "/test3.txt", "operation": "delete", "position": 2, "end_position": 3}`
			result, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldBeNil)
			So(result, ShouldContainSubstring, "Successfully edited")

			// Verify delete
			content, err = storage.Read(ctx, "/test3.txt")
			So(err, ShouldBeNil)
			So(string(content), ShouldNotContainSubstring, "line2")

			// Test delete - missing position
			editArgs = `{"path": "/test3.txt", "operation": "delete", "end_position": 2}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "position and end_position are required")

			// Test delete - invalid range
			storage.files["/test3.txt"] = []byte("line1\nline2")
			editArgs = `{"path": "/test3.txt", "operation": "delete", "position": 1, "end_position": 10}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "invalid range")

			// Test invalid operation
			editArgs = `{"path": "/test.txt", "operation": "invalid_op", "position": 1}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unknown operation")

			// Test file not found
			editArgs = `{"path": "/nonexistent.txt", "operation": "insert", "position": 1, "content": "test"}`
			_, err = invEditTool.InvokableRun(ctx, editArgs)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "failed to read file")
		})
	})
}

func TestStorageToolsCreation(t *testing.T) {
	Convey("TestStorageToolsCreation", t, func() {
		storage := newMockStorage()
		tools := NewStorageTools(storage)

		// Should return 4 tools: ls, read_file, write_file, edit_file
		So(len(tools), ShouldEqual, 4)

		ctx := context.Background()

		// Verify each tool's info
		toolNames := make([]string, 0)
		for _, tool := range tools {
			info, err := tool.Info(ctx)
			So(err, ShouldBeNil)
			So(info, ShouldNotBeNil)
			toolNames = append(toolNames, info.Name)
		}

		So(toolNames, ShouldContain, "ls")
		So(toolNames, ShouldContain, "read_file")
		So(toolNames, ShouldContain, "write_file")
		So(toolNames, ShouldContain, "edit_file")
	})
}

