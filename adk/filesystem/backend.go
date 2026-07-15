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
	"io"

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
}

// LsInfoRequest contains parameters for listing file information.
type LsInfoRequest struct {
	// Path specifies the directory path to list.
	Path string
}

// ReadRequest contains parameters for reading file content.
type ReadRequest struct {
	// FilePath is the path to the file to be read.
	FilePath string

	// Offset specifies the starting line number (1-based) for reading.
	// Line 1 is the first line of the file.
	// Use this when the file is too large to read at once.
	// Defaults to 1 (start from the first line).
	// Values < 1 will be treated as 1.
	Offset int

	// Limit specifies the maximum number of lines to read.
	// When Limit is 0 (default), the entire file content is returned.
	Limit int
}

// MultiModalReadRequest extends ReadRequest with parameters only applicable
// to MultiModalReader implementations (e.g. PDF page ranges).
type MultiModalReadRequest struct {
	ReadRequest

	// Pages specifies the page range for PDF files (e.g. "1-5", "3", "10-20").
	Pages string
}

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

	// ===== Context Display (Content mode only) =====

	// AfterLines shows N lines after each match.
	// Only applicable when OutputMode is "content".
	// Values <= 0 are treated as unset.
	AfterLines int

	// BeforeLines shows N lines before each match.
	// Only applicable when OutputMode is "content".
	// Values <= 0 are treated as unset.
	BeforeLines int
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
	Path string
}

// WriteRequest contains parameters for writing file content.
type WriteRequest struct {
	// FilePath is the path of the file to write.
	FilePath string

	// Content is the data to be written to the file.
	Content string
}

// OpenAppendRequest contains parameters for opening an append stream.
type OpenAppendRequest struct {
	// FilePath is the path of the file to append to; it is created if absent.
	FilePath string
}

// EditRequest contains parameters for editing file content.
type EditRequest struct {
	// FilePath is the path of the file to edit.
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

// FileContentPartType defines the type of a multimodal file content part.
type FileContentPartType string

const (
	// FileContentPartTypeImage represents an image part (e.g. PNG, JPG).
	FileContentPartTypeImage FileContentPartType = "image"
	// FileContentPartTypePDF represents a file part (e.g. PDF).
	FileContentPartTypePDF FileContentPartType = "pdf"
)

// FileContentPart represents a multimodal part of file content.
// Data holds raw bytes; encoding (e.g. base64) is handled by the consumer.
type FileContentPart struct {
	// Type is the kind of content this part represents.
	// Required.
	Type FileContentPartType

	// MIMEType is the MIME type of the content (e.g. "image/png", "application/pdf").
	// Required.
	MIMEType string

	// Data is the raw binary content.
	// Required.
	Data []byte
}

// FileContent holds the result of a Read operation.
type FileContent struct {
	// Content holds the plain text content of the file.
	Content string
}

// MultiFileContent holds the result of a MultiModalRead operation.
//
// FileContent and Parts are mutually exclusive (one-of):
//   - Set FileContent for plain text results (same as a normal Read).
//   - Set Parts for multimodal results (images, PDFs, etc.).
//
// When Parts is non-empty, FileContent is ignored.
type MultiFileContent struct {
	*FileContent

	// Parts holds multimodal output parts (e.g. image, PDF).
	Parts []FileContentPart
}

// MultiModalReader is an optional extension interface for Backend.
// Backends that implement this interface support multimodal file reading,
// returning structured parts (images, PDFs) instead of plain text.
//
// For large file handling, there are two approaches to control output size:
//   - Implement size control within MultiModalRead (e.g. reject files exceeding a threshold,
//     downsample images, or limit PDF page counts at the backend level).
//   - Use ToolMiddleware's EnhancedInvokable to customize result transformation,
//     or use the built-in reduction middleware with configurable policies.
type MultiModalReader interface {
	MultiModalRead(ctx context.Context, req *MultiModalReadRequest) (*MultiFileContent, error)
}

// AppendOpener opens a write-only, tail-appending session to a file, letting
// callers stream content to the end of the file incrementally without rewriting
// the whole file — e.g. teeing a long-running background task's output to its
// output file as chunks arrive.
//
// It is an optional Backend extension (like MultiModalReader): a Backend that can
// keep a file handle or RPC stream open implements it directly, so per-chunk backend
// latency can be amortized over the session.
//
// The io.WriteCloser returned by OpenAppend has the following contract:
//
//   - Write appends bytes to the end of the file. It MAY buffer: a nil error does
//     NOT guarantee the bytes are durable or yet visible to a concurrent Read. If a
//     prior buffered write already failed, the stream is permanently broken and
//     Write returns that sticky error; callers should stop writing.
//   - Close flushes any buffered content, finalizes the session, and returns the
//     first error observed over the stream's lifetime (nil if all writes landed).
//     Closing a session in which nothing was written yields an empty file (the file
//     is created on open). Close is idempotent, and a Write after Close is rejected
//     (it returns an error, e.g. io.ErrClosedPipe, and does not modify the file).
//
// A handle is single-consumer: it is not safe for concurrent use.
//
// Two obligations fall on a backend that holds OS or network resources (an fd, an
// RPC stream), because callers may abandon a session without a clean Close:
//
//   - The ctx passed to OpenAppend bounds the whole session. Callers guarantee Close
//     on normal completion and on a stream error, but NOT on every abandonment
//     (a canceled or timed-out run may just drop the handle). So the implementation
//     MUST release its resources when that ctx is canceled — not only in Close, or
//     it leaks a handle on those paths. Canceling the ctx aborts pending writes.
//   - When Write buffers, the implementation SHOULD flush on its own cadence (time or
//     size based) so a concurrent Read of a still-running task sees recent interim
//     output; the caller does not drive flushing, since a per-write flush would
//     reintroduce the per-write latency this abstraction exists to amortize.
//
// Implementations SHOULD additionally implement io.StringWriter so text callers can
// append without a []byte copy (use io.WriteString, which detects it). They MAY
// implement interface{ Flush() error } to expose a mid-stream durability/visibility
// checkpoint without ending the session.
type AppendOpener interface {
	// OpenAppend opens an append stream to req.FilePath, creating the file if it
	// does not exist.
	OpenAppend(ctx context.Context, req *OpenAppendRequest) (io.WriteCloser, error)
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
	Read(ctx context.Context, req *ReadRequest) (*FileContent, error)

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
//
// Foreground/background switching and timeouts are the caller's concern (e.g. the
// backgroundtask Manager): a backend simply runs the command and must honor ctx
// cancellation, which is how a timed-out or canceled run is stopped.
type ExecuteRequest struct {
	Command string // The command to execute
}

// ExecuteResponse contains the response result of command execution.
type ExecuteResponse struct {
	Output    string // Command output content
	ExitCode  *int   // Command exit code
	Truncated bool   // Whether the output was truncated
}

// Shell executes shell commands. Execute must honor ctx cancellation by stopping
// the underlying command (e.g. via exec.CommandContext): a timed-out or canceled
// run is stopped solely by canceling ctx, so an implementation that ignores it
// will leak the process and its goroutine after the run is reported stopped.
type Shell interface {
	Execute(ctx context.Context, input *ExecuteRequest) (result *ExecuteResponse, err error)
}

// StreamingShell is the streaming counterpart of Shell. ExecuteStreaming must honor
// ctx cancellation by stopping the underlying command, as described on Shell.
type StreamingShell interface {
	ExecuteStreaming(ctx context.Context, input *ExecuteRequest) (result *schema.StreamReader[*ExecuteResponse], err error)
}
