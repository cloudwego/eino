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
)

// FileInfo represents basic file metadata information.
type FileInfo struct {
	Path string // Full path of the file
}

// GrepMatch represents a single pattern match result.
type GrepMatch struct {
	Path    string // Path of the file where the match occurred
	Line    int    // Line number of the match (1-based)
	Content string // Full text content of the matched line
}

// LsInfoRequest contains parameters for listing file information.
type LsInfoRequest struct {
	// Path is the directory path prefix to list.
	// Empty string means root "/"; prefix matching is applied.
	Path string
}

// ReadRequest contains parameters for reading file content.
type ReadRequest struct {
	// FilePath is the full path of the target file.
	FilePath string

	// Offset is the starting line index (0-based).
	// Negative values are treated as 0.
	Offset int

	// Limit is the maximum number of lines to read.
	// Values <= 0 use the implementation default (typically 200).
	Limit int
}

// GrepRequest contains parameters for searching file content.
type GrepRequest struct {
	// Pattern is the plain text substring to search for (not a regex).
	Pattern string

	// Path is the directory path to limit the search.
	// Empty string means current working directory.
	Path string

	// Glob is a glob pattern to filter files (e.g., "*.py").
	// Empty string means no filtering.
	Glob string
}

// GlobInfoRequest contains parameters for glob pattern matching.
type GlobInfoRequest struct {
	// Pattern is the glob expression applied to file paths (e.g., "*.go").
	Pattern string

	// Path is the root path or prefix filter.
	Path string
}

// WriteRequest contains parameters for writing file content.
type WriteRequest struct {
	// FilePath is the target file path.
	// Creates the file if missing, overwrites if present.
	FilePath string

	// Content is the full file content to write.
	Content string
}

// EditRequest contains parameters for editing file content.
type EditRequest struct {
	// FilePath is the target file path.
	FilePath string

	// OldString is the substring to replace.
	// Must be non-empty.
	OldString string

	// NewString is the replacement substring.
	// Empty string means remove the OldString.
	NewString string

	// ReplaceAll determines the replacement behavior.
	// If true, replaces all occurrences; if false, replaces only the first occurrence.
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

//type SandboxFileSystem interface {
//	Execute(ctx context.Context, command string) (output string, exitCode *int, truncated bool, err error)
//}
