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
	// Path is the path of the file or directory, which can be a filename, relative path, or absolute path.
	Path string

	// IsDir indicates whether the entry is a directory.
	// true for directories, false for regular files.
	IsDir bool

	// Size is the file size in bytes.
	// For directories, this value may be 0 or platform-dependent.
	Size int64

	// ModifiedAt is the last modification time in ISO 8601 format.
	// Example: "2025-01-15T10:30:00Z"
	ModifiedAt string
}

// GrepMatch represents a single pattern match result.
type GrepMatch struct {
	Content string

	// Path is the file path where the match was found.
	Path string

	// Line is the 1-based line number of the match.
	Line int

	// Count is the total number of matches found in the file.
	Count int
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

	// Offset specifies the starting line number (1-based) for reading.
	// Line 1 is the first line of the file.
	// Use this when the file is too large to read at once.
	// Defaults to 1 (start from the first line).
	// Values < 1 will be treated as 1.
	Offset int

	// Limit specifies the maximum number of lines to read.
	// Use this when the file is too large to read at once.
	// Defaults to 2000 if not provided or non-positive (<= 0).
	Limit int
}

type OutputMode string

const (
	FilesWithMatchesOfOutputMode OutputMode = "files_with_matches"
	ContentOfOutputMode          OutputMode = "content"
	CountOfOutputMode            OutputMode = "count"
)

// GrepRequest contains parameters for searching file content.
type GrepRequest struct {
	// ===== Search Parameters =====

	// Pattern is the search pattern, supports full regular expression syntax.
	// Uses ripgrep syntax (not grep). Examples:
	//   - "log.*Error" matches lines with "log" followed by "Error"
	//   - "function\\s+\\w+" matches "function" followed by whitespace and word characters
	//   - Literal braces need escaping: "interface\\{\\}" matches "interface{}"
	Pattern string

	// Path is an optional directory path to limit the search scope.
	// If empty, the search is performed from the working directory.
	Path string

	// ===== File Filtering =====

	// Glob is an optional pattern to filter the files to be searched.
	// It filters by file path, not content. If empty, no files are filtered.
	// Supports standard glob wildcards:
	//   - `*` matches any characters except path separators.
	//   - `**` matches any directories recursively.
	//   - `?` matches a single character.
	//   - `[abc]` matches one character from the set.
	Glob string

	// FileType is the file type filter, e.g., "js", "py", "rust".
	// More efficient than Glob for standard file types.
	FileType string

	// ===== Search Options =====

	// CaseInsensitive enables case insensitive search.
	CaseInsensitive bool

	// EnableMultiline enables multiline mode where patterns can span lines.
	// Default: false (patterns match within single lines only).
	EnableMultiline bool

	// ===== Output Configuration =====

	// OutputMode specifies the output format.
	// Default: "files_with_matches"
	// See GrepMatch.Content for format details.
	OutputMode OutputMode

	// ===== Context Display (Content mode only) =====

	// AfterLines shows N lines after each match.
	// Only applicable when OutputMode is "content".
	// Values <= 0 are treated as unset.
	// Priority: This parameter is ignored if ContextLines > 0.
	AfterLines int

	// BeforeLines shows N lines before each match.
	// Only applicable when OutputMode is "content".
	// Values <= 0 are treated as unset.
	// Priority: This parameter is ignored if ContextLines > 0.
	BeforeLines int

	// ContextLines shows N lines before and after each match.
	// Only applicable when OutputMode is "content".
	// Values <= 0 are treated as unset.
	// Priority: When ContextLines > 0, it takes precedence over both AfterLines and BeforeLines.
	// This means setting ContextLines will override any values set in AfterLines or BeforeLines.
	ContextLines int

	// ===== Result Pagination =====

	// HeadLimit limits output to first N entries.
	// Works across all output modes. Default: 0 (no limit).
	// Values <= 0 are treated as no limit.
	HeadLimit int

	// Offset skips first N results before applying HeadLimit.
	// Works across all output modes. Default: 0.
	// Values <= 0 are treated as 0.
	Offset int
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
	// Creates the file if it does not exist, overwrites if it exists.
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

// ExecuteRequest contains parameters for executing a command.
type ExecuteRequest struct {
	Command            string // The command to execute
	RunInBackendGround bool
}

// ExecuteResponse contains the response result of command execution.
type ExecuteResponse struct {
	Output    string // Command output content
	ExitCode  *int   // Command exit code
	Truncated bool   // Whether the output was truncated
}

type Shell interface {
	Execute(ctx context.Context, input *ExecuteRequest) (result *ExecuteResponse, err error)
}

type StreamingShell interface {
	ExecuteStreaming(ctx context.Context, input *ExecuteRequest) (result *schema.StreamReader[*ExecuteResponse], err error)
}
