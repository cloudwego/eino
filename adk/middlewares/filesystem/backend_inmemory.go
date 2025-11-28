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
// Parameters:
//   - path: Directory prefix to list. Empty means root "/"; prefix match is applied.
func (b *InMemoryBackend) LsInfo(ctx context.Context, path string) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

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
// Parameters:
//   - filePath: Full path of the target file.
//   - offset: Zero-based starting line index; negative values are treated as 0.
//   - limit: Maximum number of lines to read; <= 0 uses implementation default (200).
func (b *InMemoryBackend) Read(ctx context.Context, filePath string, offset, limit int) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	filePath = normalizePath(filePath)

	content, exists := b.files[filePath]
	if !exists {
		return "", fmt.Errorf("file not found: %s", filePath)
	}

	if offset < 0 {
		offset = 0
	}
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

	return strings.Join(lines[offset:end], "\n"), nil
}

// GrepRaw returns matches for the given pattern.
// Parameters:
//   - pattern: Plain substring to search (not a regex).
//   - path: Filter which directory to search in (default is the current working directory)
//   - glob: A glob pattern to filter which files to search (e.g., '*.py')
func (b *InMemoryBackend) GrepRaw(ctx context.Context, pattern string, path, glob *string) (matches []GrepMatch, err error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	searchPath := "/"
	if path != nil && *path != "" {
		searchPath = normalizePath(*path)
	}

	for filePath, content := range b.files {
		normalizedFilePath := normalizePath(filePath)

		// Check if file is under the search path
		if searchPath != "/" && !strings.HasPrefix(normalizedFilePath, searchPath+"/") && normalizedFilePath != searchPath {
			continue
		}

		// Check glob pattern if provided
		if glob != nil && *glob != "" {
			matched, err := filepath.Match(*glob, filepath.Base(normalizedFilePath))
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
			if strings.Contains(line, pattern) {
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
// Parameters:
//   - pattern: Path glob expression applied to file paths.
//   - path: Root/prefix filter
func (b *InMemoryBackend) GlobInfo(ctx context.Context, pattern, path string) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

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
		matched, err := filepath.Match(pattern, filepath.Base(normalizedFilePath))
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
// Parameters:
//   - filePath: Target file path; creates if missing, overwrites if present.
//   - content: Full file content to write.
func (b *InMemoryBackend) Write(ctx context.Context, filePath, content string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	filePath = normalizePath(filePath)
	b.files[filePath] = content

	return nil
}

// Edit replaces string occurrences in a file.
// Parameters:
//   - filePath: Target file path.
//   - oldString: Substring to replace; must be non-empty.
//   - newString: Replacement substring; empty means remove the oldString.
//   - replaceAll: If true, replace all occurrences; if false, replace only the first.
func (b *InMemoryBackend) Edit(ctx context.Context, filePath, oldString, newString string, replaceAll bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	filePath = normalizePath(filePath)

	content, exists := b.files[filePath]
	if !exists {
		return fmt.Errorf("file not found: %s", filePath)
	}

	if oldString == "" {
		return fmt.Errorf("oldString must be non-empty")
	}

	if !strings.Contains(content, oldString) {
		return fmt.Errorf("oldString not found in file: %s", filePath)
	}

	if replaceAll {
		b.files[filePath] = strings.ReplaceAll(content, oldString, newString)
	} else {
		b.files[filePath] = strings.Replace(content, oldString, newString, 1)
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

	// Remove trailing slash unless it's the root
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = strings.TrimSuffix(path, "/")
	}

	return path
}
