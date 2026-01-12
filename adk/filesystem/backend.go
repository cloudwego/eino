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

// Package filesystem provides file system operations.
package filesystem

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// FileInfo represents basic file metadata information.
type FileInfo struct {
	// Path is the absolute path of the file or directory.
	Path string
}

// GrepMatch represents a single pattern match result.
type GrepMatch struct {
	// Path is the absolute path of the file where the match occurred.
	Path string
	// Line is the 1-based line number of the match.
	Line int
	// Content is the full text content of the line containing the match.
	Content string
}

// LsInfoRequest contains parameters for listing file information.
type LsInfoRequest struct {
	// Path specifies the absolute directory path to list.
	// It must be an absolute path starting with '/'.
	// An empty string is treated as the root directory ("/").
	Path string
}

// ReadRequest contains parameters for reading file content.
type ReadRequest struct {
	// FilePath is the absolute path to the file to be read. Must start with '/'.
	FilePath string

	// Offset is the 0-based line number to start reading from.
	// If negative, it is treated as 0. Defaults to 0.
	Offset int

	// Limit specifies the maximum number of lines to read.
	// If non-positive (<= 0), a default limit is used (typically 200).
	Limit int
}

// GrepRequest contains parameters for searching file content.
type GrepRequest struct {
	// Pattern is the literal string to search for. This is not a regular expression.
	// The search performs an exact substring match within the file's content.
	// For example, "TODO" will match any line containing "TODO".
	Pattern string

	// Path is an optional directory path to limit the search scope.
	// If empty, the search is performed from the working directory.
	Path string

	// Glob is an optional pattern to filter the files to be searched.
	// It filters by file path, not content. If empty, no files are filtered.
	// Supports standard glob wildcards:
	//   - `*` matches any characters except path separators.
	//   - `**` matches any directories recursively.
	//   - `?` matches a single character.
	//   - `[abc]` matches one character from the set.
	Glob string
}

// GlobInfoRequest contains parameters for glob pattern matching.
type GlobInfoRequest struct {
	// Pattern is the glob expression used to match file paths.
	// It supports standard glob syntax:
	//   - `*` matches any characters except path separators.
	//   - `**` matches any directories recursively.
	//   - `?` matches a single character.
	//   - `[abc]` matches one character from the set.
	Pattern string

	// Path is the base directory from which to start the search.
	// The glob pattern is applied relative to this path. Defaults to the root directory ("/").
	Path string
}

// WriteRequest contains parameters for writing file content.
type WriteRequest struct {
	// FilePath is the absolute path of the file to write. Must start with '/'.
	// The file will be created if it does not exist, or error if file exists.
	FilePath string

	// Content is the data to be written to the file.
	Content string
}

// EditRequest contains parameters for editing file content.
type EditRequest struct {
	// FilePath is the absolute path of the file to edit. Must start with '/'.
	FilePath string

	// OldString is the exact string to be replaced. It must be non-empty and will be matched literally, including whitespace.
	OldString string

	// NewString is the string that will replace OldString.
	// It must be different from OldString.
	// An empty string can be used to effectively delete OldString.
	NewString string

	// ReplaceAll controls the replacement behavior.
	// If true, all occurrences of OldString are replaced.
	// If false, the operation fails unless OldString appears exactly once in the file.
	ReplaceAll bool
}

// Backend is a pluggable, unified file backend protocol interface.
//
// All methods use struct-based parameters to allow future extensibility
// without breaking backward compatibility.
type Backend interface {
	// LsInfo lists file information under the given path.
	//
	// Returns:
	//   - []FileInfo: List of matching file information
	//   - error: Error if the operation fails
	LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error)

	// Read reads file content with support for line-based offset and limit.
	//
	// Returns:
	//   - string: The file content read
	//   - error: Error if file does not exist or read fails
	Read(ctx context.Context, req *ReadRequest) (string, error)

	// GrepRaw searches for content matching the specified pattern in files.
	//
	// Returns:
	//   - []GrepMatch: List of all matching results
	//   - error: Error if the search fails
	GrepRaw(ctx context.Context, req *GrepRequest) ([]GrepMatch, error)

	// GlobInfo returns file information matching the glob pattern.
	//
	// Returns:
	//   - []FileInfo: List of matching file information
	//   - error: Error if the pattern is invalid or operation fails
	GlobInfo(ctx context.Context, req *GlobInfoRequest) ([]FileInfo, error)

	// Write creates or updates file content.
	//
	// Returns:
	//   - error: Error if the write operation fails
	Write(ctx context.Context, req *WriteRequest) error

	// Edit replaces string occurrences in a file.
	//
	// Returns:
	//   - error: Error if file does not exist, OldString is empty, or OldString is not found
	Edit(ctx context.Context, req *EditRequest) error
}

type ExecuteRequest struct {
	Command string
}

type ExecuteResponse struct {
	Output    string
	ExitCode  *int
	Truncated bool
}

type ShellBackend interface {
	Backend
	Execute(ctx context.Context, input *ExecuteRequest) (result *ExecuteResponse, err error)
}

type StreamingShellBackend interface {
	Backend
	ExecuteStreaming(ctx context.Context, input *ExecuteRequest) (result *schema.StreamReader[*ExecuteResponse], err error)
}
