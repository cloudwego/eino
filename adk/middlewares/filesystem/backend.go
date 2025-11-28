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

// Backend is a pluggable, unified file backend protocol interface.
type Backend interface {
	// LsInfo lists file information under the given path.
	//
	// Parameters:
	//   - path: Directory path prefix to list. Empty string means root "/"; prefix matching is applied.
	//
	// Returns:
	//   - []FileInfo: List of matching file information
	//   - error: Error if the operation fails
	LsInfo(ctx context.Context, path string) ([]FileInfo, error)

	// Read reads file content with support for line-based offset and limit.
	//
	// Parameters:
	//   - filePath: Full path of the target file
	//   - offset: Starting line index (0-based); negative values are treated as 0
	//   - limit: Maximum number of lines to read; <= 0 uses implementation default
	//
	// Returns:
	//   - string: The file content read
	//   - error: Error if file does not exist or read fails
	Read(ctx context.Context, filePath string, offset, limit int) (string, error)

	// GrepRaw searches for content matching the specified pattern in files.
	//
	// Parameters:
	//   - pattern: Plain text substring to search for (not a regex)
	//   - path: Directory path to limit the search (nil or empty means current working directory)
	//   - glob: Glob pattern to filter files (e.g., '*.py'; nil means no filtering)
	//
	// Returns:
	//   - matches: List of all matching results
	//   - err: Error if the search fails
	GrepRaw(ctx context.Context, pattern string, path, glob *string) (matches []GrepMatch, err error)

	// GlobInfo returns file information matching the glob pattern.
	//
	// Parameters:
	//   - pattern: Glob expression applied to file paths (e.g., "*.go")
	//   - path: Root path or prefix filter
	//
	// Returns:
	//   - []FileInfo: List of matching file information
	//   - error: Error if the pattern is invalid or operation fails
	GlobInfo(ctx context.Context, pattern, path string) ([]FileInfo, error)

	// Write creates or updates file content.
	//
	// Parameters:
	//   - filePath: Target file path; creates if missing, overwrites if present
	//   - content: Full file content to write
	//
	// Returns:
	//   - error: Error if the write operation fails
	Write(ctx context.Context, filePath, content string) error

	// Edit replaces string occurrences in a file.
	//
	// Parameters:
	//   - filePath: Target file path
	//   - oldString: Substring to replace; must be non-empty
	//   - newString: Replacement substring; empty string means remove oldString
	//   - replaceAll: If true, replace all occurrences; if false, replace only the first occurrence
	//
	// Returns:
	//   - error: Error if file does not exist, oldString is empty, or oldString is not found
	Edit(ctx context.Context, filePath, oldString, newString string, replaceAll bool) error
}

//type SandboxFileSystem interface {
//	Execute(ctx context.Context, command string) (output string, exitCode *int, truncated bool, err error)
//}
