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

// Package filesystem provides middlewares.
package filesystem

import (
	"context"

	"github.com/cloudwego/eino/adk/filesystem"
)

type FileInfo = filesystem.FileInfo
type GrepMatch = filesystem.GrepMatch
type LsInfoRequest = filesystem.LsInfoRequest
type ReadRequest = filesystem.ReadRequest
type GrepRequest = filesystem.GrepRequest
type GlobInfoRequest = filesystem.GlobInfoRequest
type WriteRequest = filesystem.WriteRequest
type EditRequest = filesystem.EditRequest

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
