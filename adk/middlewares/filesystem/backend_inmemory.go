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
	"fmt"
	"path/filepath"
	"strings"
	"sync"
)

// InMemoryBackend is an in-memory implementation of the Backend interface.
// It stores files in a map and is safe for concurrent use.
type InMemoryBackend struct {
	mu    sync.RWMutex
	files map[string]string // map[filePath]content
}

// NewInMemoryBackend creates a new in-memory backend.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{
		files: make(map[string]string),
	}
}

// LsInfo lists file information under the given path.
func (b *InMemoryBackend) LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	path := req.Path
	if path == "" {
		path = "/"
	}

	// Normalize path
	path = normalizePath(path)

	var result []FileInfo
	seen := make(map[string]bool)

	for filePath := range b.files {
		normalizedFilePath := normalizePath(filePath)

		// Check if file is under the given path
		if path == "/" || strings.HasPrefix(normalizedFilePath, path+"/") || normalizedFilePath == path {
			// For directory listing, we want to show immediate children
			relativePath := strings.TrimPrefix(normalizedFilePath, path)
			relativePath = strings.TrimPrefix(relativePath, "/")

			if relativePath == "" {
				// The path itself is a file
				if !seen[normalizedFilePath] {
					result = append(result, FileInfo{Path: normalizedFilePath})
					seen[normalizedFilePath] = true
				}
				continue
			}

			// Get the first segment (immediate child)
			parts := strings.SplitN(relativePath, "/", 2)
			if len(parts) > 0 {
				childPath := path
				if path != "/" {
					childPath += "/"
				}
				childPath += parts[0]

				if !seen[childPath] {
					result = append(result, FileInfo{Path: childPath})
					seen[childPath] = true
				}
			}
		}
	}

	return result, nil
}

// Read reads file content with offset and limit.
func (b *InMemoryBackend) Read(ctx context.Context, req *ReadRequest) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	filePath := normalizePath(req.FilePath)

	content, exists := b.files[filePath]
	if !exists {
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 200
	}

	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	if offset >= totalLines {
		return "", nil
	}

	end := offset + limit
	if end > totalLines {
		end = totalLines
	}

	sb := &strings.Builder{}
	i := offset
	for ; i < end-1; i++ {
		sb.WriteString(fmt.Sprintf("%6d\t%s\n", i+1, lines[i]))
	}
	sb.WriteString(fmt.Sprintf("%6d\t%s", i+1, lines[i]))

	return sb.String(), nil
}

// GrepRaw returns matches for the given pattern.
func (b *InMemoryBackend) GrepRaw(ctx context.Context, req *GrepRequest) ([]GrepMatch, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	searchPath := "/"
	if req.Path != "" {
		searchPath = normalizePath(req.Path)
	}

	var matches []GrepMatch

	for filePath, content := range b.files {
		normalizedFilePath := normalizePath(filePath)

		// Check if file is under the search path
		if searchPath != "/" && !strings.HasPrefix(normalizedFilePath, searchPath+"/") && normalizedFilePath != searchPath {
			continue
		}

		// Check glob pattern if provided
		if req.Glob != "" {
			matched, err := filepath.Match(req.Glob, filepath.Base(normalizedFilePath))
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern: %w", err)
			}
			if !matched {
				continue
			}
		}

		// Search for pattern in file content
		lines := strings.Split(content, "\n")
		for lineNum, line := range lines {
			if strings.Contains(line, req.Pattern) {
				matches = append(matches, GrepMatch{
					Path:    normalizedFilePath,
					Line:    lineNum + 1, // 1-based line number
					Content: line,
				})
			}
		}
	}

	return matches, nil
}

// GlobInfo returns file info entries matching the glob pattern.
func (b *InMemoryBackend) GlobInfo(ctx context.Context, req *GlobInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	path := req.Path
	if path == "" {
		path = "/"
	}
	path = normalizePath(path)

	var result []FileInfo

	for filePath := range b.files {
		normalizedFilePath := normalizePath(filePath)

		// Check if file is under the given path
		if path != "/" && !strings.HasPrefix(normalizedFilePath, path+"/") && normalizedFilePath != path {
			continue
		}

		// Match against the pattern
		matched, err := filepath.Match(req.Pattern, filepath.Base(normalizedFilePath))
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}

		if matched {
			result = append(result, FileInfo{Path: normalizedFilePath})
		}
	}

	return result, nil
}

// Write creates or updates file content.
func (b *InMemoryBackend) Write(ctx context.Context, req *WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	filePath := normalizePath(req.FilePath)
	b.files[filePath] = req.Content

	return nil
}

// Edit replaces string occurrences in a file.
func (b *InMemoryBackend) Edit(ctx context.Context, req *EditRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	filePath := normalizePath(req.FilePath)

	content, exists := b.files[filePath]
	if !exists {
		return fmt.Errorf("file not found: %s", filePath)
	}

	if req.OldString == "" {
		return fmt.Errorf("oldString must be non-empty")
	}

	if !strings.Contains(content, req.OldString) {
		return fmt.Errorf("oldString not found in file: %s", filePath)
	}

	if !req.ReplaceAll {
		firstIndex := strings.Index(content, req.OldString)
		if firstIndex != -1 {
			// Check if there's another occurrence after the first one
			if strings.Contains(content[firstIndex+len(req.OldString):], req.OldString) {
				return fmt.Errorf("multiple occurrences of oldString found in file %s, but ReplaceAll is false", filePath)
			}
		}
	}

	if req.ReplaceAll {
		b.files[filePath] = strings.ReplaceAll(content, req.OldString, req.NewString)
	} else {
		b.files[filePath] = strings.Replace(content, req.OldString, req.NewString, 1)
	}

	return nil
}

// normalizePath normalizes a file path by ensuring it starts with "/" and removing trailing slashes.
func normalizePath(path string) string {
	if path == "" {
		return "/"
	}

	// Ensure path starts with "/"
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}

	return filepath.Clean(path)
}
