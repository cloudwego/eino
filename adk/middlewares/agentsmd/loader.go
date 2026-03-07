/*
 * Copyright 2026 CloudWeGo Authors
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

package agentsmd

import (
	"context"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/adk/internal"
)

// importRegex matches @path/to/file anywhere in text.
// The path must start with a letter, digit, dot, underscore, slash, or tilde, followed by
// path characters (letters, digits, dots, slashes, hyphens, underscores).
// A post-match filter further requires the path to contain "/" or end with
// an allowed extension (see allowedImportExts), so bare words like @someone
// and email-like patterns like @example.com are ignored.
var importRegex = regexp.MustCompile(`@([a-zA-Z0-9_.~/][a-zA-Z0-9_.~/\-]*)`)

// allowedImportExts is the set of file extensions recognised as @import targets.
// Paths without "/" must end with one of these extensions to be treated as imports;
// this avoids false positives on email addresses (@example.com) and mentions (@foo.bar).
var allowedImportExts = map[string]bool{
	".md":   true,
	".txt":  true,
	".mdx":  true,
	".yaml": true,
	".yml":  true,
	".json": true,
	".toml": true,
}

const maxImportDepth = 5

// ReadRequest is an alias for filesystem.ReadRequest.
type ReadRequest = filesystem.ReadRequest

// Backend defines the file access interface for loading Agents.md files.
// Implementations can use local filesystem, remote storage, or any other backend.
type Backend interface {
	// Read reads the content of a file.
	// If the file does not exist, implementations are recommended to return an
	// empty string (so the file is silently skipped). Alternatively, returning
	// an error is acceptable when Config.IgnoreLoadFileError is set to true.
	Read(ctx context.Context, req *ReadRequest) (string, error)
}

// loader handles loading and @import resolution for agent.md files.
type loader struct {
	backend   Backend
	files     []string // ordered file paths from config
	maxBytes  int      // cumulative read budget; 0 means unlimited
	ignoreErr bool     // when true, file loading errors are silently skipped

	// totalBytes tracks accumulated bytes during a single load() call.
	// Concurrency safety is guaranteed by the caller — each agentMDModel
	// invocation (Generate/Stream) calls load() sequentially.
	totalBytes int
}

func newLoader(backend Backend, files []string, maxBytes int, ignoreErr bool) *loader {
	return &loader{
		backend:   backend,
		files:     files,
		maxBytes:  maxBytes,
		ignoreErr: ignoreErr,
	}
}

// load reads all agent.md files and returns the formatted content.
// Each top-level file and its @imported files appear as separate sections.
func (l *loader) load(ctx context.Context) (string, error) {
	l.totalBytes = 0

	var parts []loadedFile
	seen := make(map[string]bool) // dedup across all files and imports

	for _, filePath := range l.files {
		absPath, err := filepath.Abs(filePath)
		if err != nil {
			if l.ignoreErr {
				log.Printf("[agentmd] warning: failed to resolve path %q: %v", filePath, err)
				continue
			}
			return "", fmt.Errorf("failed to resolve path %q: %w", filePath, err)
		}

		files, err := l.loadFile(ctx, absPath, 0, make(map[string]bool), seen)
		if err != nil {
			if l.ignoreErr {
				log.Printf("[agentmd] warning: failed to load %q: %v", filePath, err)
				continue
			}
			return "", fmt.Errorf("failed to load %q: %w", filePath, err)
		}

		// If loading this file caused the budget to be exceeded, skip it
		// (but always include the first file).
		if l.maxBytes > 0 && l.totalBytes > l.maxBytes && len(parts) > 0 {
			break
		}

		parts = append(parts, files...)
	}

	return formatContent(parts), nil
}

// loadFile reads a file via Backend and collects @imported files as separate entries.
// Returns a slice where the first element is this file itself, followed by all
// transitively imported files (in encounter order, preserving @path in original text).
// visited tracks the current ancestor chain to detect circular imports.
// seen tracks globally loaded files to avoid duplicate reads and byte counting.
func (l *loader) loadFile(ctx context.Context, absPath string, depth int, visited map[string]bool, seen map[string]bool) ([]loadedFile, error) {
	if depth > maxImportDepth {
		if l.ignoreErr {
			log.Printf("[agentmd] warning: @import depth exceeds maximum of %d at %q", maxImportDepth, absPath)
			return nil, nil
		}
		return nil, fmt.Errorf("@import depth exceeds maximum of %d at %q", maxImportDepth, absPath)
	}

	if visited[absPath] {
		if l.ignoreErr {
			log.Printf("[agentmd] warning: circular @import detected: %q", absPath)
			return nil, nil
		}
		return nil, fmt.Errorf("circular @import detected: %q", absPath)
	}

	if seen[absPath] {
		return nil, nil
	}

	visited[absPath] = true
	defer delete(visited, absPath)

	content, err := l.backend.Read(ctx, &ReadRequest{FilePath: absPath})
	if err != nil {
		if l.ignoreErr {
			log.Printf("[agentmd] warning: failed to read %q: %v", absPath, err)
			return nil, nil
		}
		return nil, err
	}

	l.totalBytes += len(content)
	seen[absPath] = true

	if content == "" {
		return nil, nil
	}

	// Collect imported files as separate sections (content stays untouched).
	imports := l.collectImports(ctx, absPath, content, depth, visited, seen)

	// This file first, then its imports.
	result := make([]loadedFile, 0, 1+len(imports))
	result = append(result, loadedFile{path: absPath, content: content})
	result = append(result, imports...)
	return result, nil
}

// collectImports scans content for @path/to/file references and loads each
// imported file (plus its transitive imports). The original content is NOT modified.
// Returns the list of imported loadedFile entries in encounter order.
// seen is shared across the entire load call to avoid duplicate reads.
// On failure: a warning is logged and the import is skipped.
func (l *loader) collectImports(ctx context.Context, hostPath, content string, depth int, visited map[string]bool, seen map[string]bool) []loadedFile {
	dir := filepath.Dir(hostPath)
	var imports []loadedFile

	matches := importRegex.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		rawPath := match[1]

		// Only treat as import if path contains "/" or ends with an allowed extension.
		// This avoids false positives on email addresses and social mentions.
		if !strings.Contains(rawPath, "/") && !allowedImportExts[filepath.Ext(rawPath)] {
			continue
		}

		// If budget is exhausted, skip further imports.
		if l.maxBytes > 0 && l.totalBytes > l.maxBytes {
			break
		}

		importPath := rawPath
		if !filepath.IsAbs(importPath) {
			importPath = filepath.Join(dir, importPath)
		}
		importPath, err := filepath.Abs(importPath)
		if err != nil {
			log.Printf("[agentmd] warning: failed to resolve import path %q in %q: %v", rawPath, hostPath, err)
			continue
		}

		if seen[importPath] {
			continue
		}

		files, err := l.loadFile(ctx, importPath, depth+1, visited, seen)
		if err != nil {
			log.Printf("[agentmd] warning: failed to import %q from %q: %v", rawPath, hostPath, err)
			continue
		}

		imports = append(imports, files...)
	}

	return imports
}

type loadedFile struct {
	path    string
	content string
}

const formatHeaderEn = `<system-reminder>
As you answer the user's questions, you can use the following context:
Codebase and user instructions are shown below. Be sure to adhere to these instructions. IMPORTANT: These instructions OVERRIDE any default behavior and you MUST follow them exactly as written.
`

const formatHeaderCn = `<system-reminder>
在回答用户问题时，你可以使用以下上下文：
代码库和用户指令如下。请务必遵守这些指令。重要提示：这些指令会覆盖任何默认行为，你必须严格按照要求执行。
`

const formatFileHeaderEn = "\nContents of "

const formatFileHeaderCn = "\n文件内容："

const formatFileLabelEn = " (instructions):\n\n"

const formatFileLabelCn = "（指令）：\n\n"

const formatFooterEn = `IMPORTANT: this context may or may not be relevant to your tasks. You should not respond to this context unless it is highly relevant to your task.
</system-reminder>`

const formatFooterCn = `重要提示：此上下文可能与你的任务相关，也可能不相关。除非此上下文与你的任务高度相关，否则不要响应此上下文。
</system-reminder>`

func formatContent(files []loadedFile) string {
	if len(files) == 0 {
		return ""
	}

	header := internal.SelectPrompt(internal.I18nPrompts{
		English: formatHeaderEn,
		Chinese: formatHeaderCn,
	})
	fileHeader := internal.SelectPrompt(internal.I18nPrompts{
		English: formatFileHeaderEn,
		Chinese: formatFileHeaderCn,
	})
	fileLabel := internal.SelectPrompt(internal.I18nPrompts{
		English: formatFileLabelEn,
		Chinese: formatFileLabelCn,
	})
	footer := internal.SelectPrompt(internal.I18nPrompts{
		English: formatFooterEn,
		Chinese: formatFooterCn,
	})

	var sb strings.Builder
	sb.WriteString(header)

	for _, f := range files {
		sb.WriteString(fileHeader)
		sb.WriteString(f.path)
		sb.WriteString(fileLabel)
		sb.WriteString(f.content)
		sb.WriteString("\n")
	}
	sb.WriteString(footer)
	return sb.String()
}
