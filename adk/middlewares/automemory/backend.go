package automemory

import (
	"context"

	"github.com/cloudwego/eino/adk/filesystem"
)

type ReadRequest = filesystem.ReadRequest
type FileContent = filesystem.FileContent
type GlobInfoRequest = filesystem.GlobInfoRequest
type FileInfo = filesystem.FileInfo

type Backend interface {
	// Read reads file content with support for line-based offset and limit.
	//
	// Returns:
	//   - string: The file content read
	//   - error: Error if file does not exist or read fails
	Read(ctx context.Context, req *ReadRequest) (*FileContent, error)

	// GlobInfo returns file information matching the glob pattern.
	//
	// Returns:
	//   - []FileInfo: List of matching file information
	//   - error: Error if the pattern is invalid or operation fails
	GlobInfo(ctx context.Context, req *GlobInfoRequest) ([]FileInfo, error)
}
