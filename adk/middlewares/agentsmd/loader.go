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
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

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

// minTruncatableBytes is a tail-only optimization threshold: once some content has
// already been loaded and the remaining cumulative budget falls below this, the next
// file is dropped whole rather than cut into a uselessly small fragment. It never
// affects the first file (always truncated to fit), so a maxBytes below this value
// still behaves as an ordinary hard cap.
const minTruncatableBytes = 500

// ReadRequest is an alias for filesystem.ReadRequest.
type ReadRequest = filesystem.ReadRequest
type FileContent = filesystem.FileContent

// Backend defines the file access interface for loading Agents.md files.
// Implementations can use local filesystem, remote storage, or any other backend.
type Backend interface {
	// Read reads the content of a file.
	// If the file does not exist, implementations should return an error wrapping
	// os.ErrNotExist (so that errors.Is(err, os.ErrNotExist) returns true). This allows the loader
	// to silently skip missing files and notify via OnLoadWarning callback.
	// Other errors (e.g. permission denied, I/O errors) will abort the loading process.
	Read(ctx context.Context, req *ReadRequest) (*FileContent, error)
}

// loaderConfig holds the immutable configuration for creating loaders.
// It is safe for concurrent use by multiple goroutines.
type loaderConfig struct {
	backend      Backend
	files        []string                         // ordered file paths from config
	maxBytes     int                              // cumulative read budget; 0 means unlimited
	perFileBytes int                              // per-file byte cap; 0 means unlimited
	onWarning    func(filePath string, err error) // callback for non-fatal loading warnings
}

func newLoaderConfig(backend Backend, files []string, maxBytes, perFileBytes int, onWarning func(filePath string, err error)) *loaderConfig {
	if onWarning == nil {
		onWarning = func(filePath string, err error) {
			log.Printf("[agentsmd] warning: %s: %v", filePath, err)
		}
	}
	return &loaderConfig{
		backend:      backend,
		files:        files,
		maxBytes:     maxBytes,
		perFileBytes: perFileBytes,
		onWarning:    onWarning,
	}
}

// loader handles loading and @import resolution for agents.md files.
// A new loader is created for each load() call to avoid sharing mutable state
// (totalBytes, stopped) across concurrent invocations.
type loader struct {
	*loaderConfig
	totalBytes int  // accumulated content bytes during this load call
	stopped    bool // set once the cumulative budget is exhausted; halts further loading
}

func (cfg *loaderConfig) newLoader() *loader {
	return &loader{loaderConfig: cfg}
}

// load reads all agents.md files and returns the formatted content.
// Each top-level file and its @imported files appear as separate sections.
// Files are loaded in order until the cumulative byte budget (maxBytes) is exhausted;
// the file that crosses the budget is truncated to fit, and no further files are loaded.
// A tail file may instead be dropped whole when too little budget remains (see applyBudget).
func (cfg *loaderConfig) load(ctx context.Context) (string, error) {
	l := cfg.newLoader()

	var parts []loadedFile
	var omitted []string          // top-level files not loaded because the budget ran out
	seen := make(map[string]bool) // dedup across all files and imports

	for i, filePath := range l.files {
		if l.stopped {
			// The budget was exhausted by an earlier file; the rest never load.
			for _, p := range l.files[i:] {
				omitted = append(omitted, filepath.Clean(p))
			}
			break
		}

		before := len(parts)
		files, err := l.loadFile(ctx, filePath, 0, make(map[string]bool), seen)
		if err != nil {
			return "", fmt.Errorf("failed to load %q: %w", filePath, err)
		}
		parts = append(parts, files...)

		// This file crossed the budget and was dropped whole (truncated files stay in parts).
		if l.stopped && len(parts) == before {
			omitted = append(omitted, filepath.Clean(filePath))
		}
	}

	return formatContent(parts, omitted), nil
}

// loadFile reads a file via Backend and collects @imported files as separate entries.
// Returns a slice where the first element is this file itself, followed by all
// transitively imported files (in encounter order, preserving @path in original text).
// visited tracks the current ancestor chain to detect circular imports.
// seen tracks globally loaded files to avoid duplicate reads and byte counting.
func (l *loader) loadFile(ctx context.Context, filePath string, depth int, visited map[string]bool, seen map[string]bool) ([]loadedFile, error) {
	filePath = filepath.Clean(filePath)

	if depth > maxImportDepth {
		l.onWarning(filePath, fmt.Errorf("@import depth exceeds maximum of %d", maxImportDepth))
		return nil, nil
	}

	if visited[filePath] {
		l.onWarning(filePath, fmt.Errorf("circular @import detected"))
		return nil, nil
	}

	if seen[filePath] {
		return nil, nil
	}

	visited[filePath] = true
	defer delete(visited, filePath)

	fileContent, err := l.backend.Read(ctx, &ReadRequest{FilePath: filePath, Offset: 1})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			l.onWarning(filePath, fmt.Errorf("file not found, skipping"))
			return nil, nil
		}
		return nil, err
	}
	content := ""
	if fileContent != nil {
		content = fileContent.Content
	}
	seen[filePath] = true

	if content == "" {
		return nil, nil
	}

	content, truncated := l.applyBudget(filePath, content)
	if content == "" { // dropped whole by the cumulative budget
		return nil, nil
	}

	// Only scan for @imports when the file was kept in full; a truncated file has
	// hit a byte cap, so pulling in more content would defeat the cap.
	var imports []loadedFile
	if !truncated {
		imports, err = l.collectImports(ctx, filePath, content, depth, visited, seen)
		if err != nil {
			return nil, err
		}
	}

	// This file first, then its imports.
	result := make([]loadedFile, 0, 1+len(imports))
	result = append(result, loadedFile{path: filePath, content: content})
	result = append(result, imports...)
	return result, nil
}

// applyBudget caps content against the per-file limit and then the cumulative budget,
// updates l.totalBytes with the retained content size, and appends a truncation notice
// when content was cut. It returns "" to signal the file should be dropped whole
// (cumulative budget too small to bother truncating), setting l.stopped in that case.
// The returned bool reports whether any truncation occurred.
func (l *loader) applyBudget(filePath, content string) (string, bool) {
	originalLen := len(content)
	truncated := false

	if l.perFileBytes > 0 && len(content) > l.perFileBytes {
		content = truncateBytes(content, l.perFileBytes)
		truncated = true
	}

	if l.maxBytes > 0 {
		remaining := l.maxBytes - l.totalBytes
		if len(content) > remaining {
			// Drop a boundary-crossing file whole only when it is a tail file
			// (something is already loaded) and the leftover budget is too small
			// to hold a useful fragment. The first file is always truncated to fit
			// instead, so even a tiny limit still loads something.
			if remaining < minTruncatableBytes && l.totalBytes > 0 {
				l.stopped = true
				l.onWarning(filePath, fmt.Errorf("dropped: remaining budget %d below %d", remaining, minTruncatableBytes))
				return "", false
			}
			content = truncateBytes(content, remaining)
			truncated = true
			l.stopped = true
		}
	}

	l.totalBytes += len(content)

	// Once the budget is fully consumed, stop: no later file or @import can add
	// content, so there is no reason to keep reading (and risk a read error on a
	// file that would be dropped anyway).
	if l.maxBytes > 0 && l.totalBytes >= l.maxBytes {
		l.stopped = true
	}

	if truncated {
		content += truncationNotice(originalLen-len(content), originalLen)
	}
	return content, truncated
}

// collectImports scans content for @path/to/file references and loads each
// imported file (plus its transitive imports). The original content is NOT modified.
// Returns the list of imported loadedFile entries in encounter order.
// seen is shared across the entire load call to avoid duplicate reads.
// Non-fatal errors (file not found, depth exceeded, circular import) are reported
// via onWarning and skipped. Fatal errors (e.g. I/O) are returned.
func (l *loader) collectImports(ctx context.Context, hostPath, content string, depth int, visited map[string]bool, seen map[string]bool) ([]loadedFile, error) {
	dir := filepath.Dir(hostPath)
	var imports []loadedFile

	matches := importRegex.FindAllStringSubmatch(content, -1)
	for _, match := range matches {
		if l.stopped {
			break
		}
		rawPath := match[1]

		// Only treat as import if path contains "/" or ends with an allowed extension.
		// This avoids false positives on email addresses and social mentions.
		if !strings.Contains(rawPath, "/") && !allowedImportExts[filepath.Ext(rawPath)] {
			continue
		}

		importPath := rawPath
		if !filepath.IsAbs(importPath) {
			importPath = filepath.Join(dir, importPath)
		}

		if seen[importPath] {
			continue
		}

		files, err := l.loadFile(ctx, importPath, depth+1, visited, seen)
		if err != nil {
			return nil, fmt.Errorf("failed to import %q from %q: %w", rawPath, hostPath, err)
		}

		imports = append(imports, files...)
	}

	return imports, nil
}

type loadedFile struct {
	path    string
	content string
}

// truncateBytes returns the longest prefix of s that is at most max bytes and ends on
// a UTF-8 rune boundary, so multibyte characters are never split.
func truncateBytes(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// truncationNotice renders the marker appended to a file that was cut down, reporting
// how many bytes were removed and the file's original size. It is not counted toward
// the byte budget, matching how the surrounding format headers/footers are excluded.
func truncationNotice(cut, total int) string {
	return internal.SelectPrompt(internal.I18nPrompts{
		English: fmt.Sprintf("\n\n[…content too large, truncated %d bytes; original size %d bytes]", cut, total),
		Chinese: fmt.Sprintf("\n\n[…内容过大，已截断 %d 字节，总大小 %d 字节]", cut, total),
	})
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

// omittedNotice renders the summary line listing top-level files that were never loaded
// because the cumulative size limit was reached. Returns "" when nothing was omitted.
func omittedNotice(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	return internal.SelectPrompt(internal.I18nPrompts{
		English: fmt.Sprintf("\n[%d more file(s) not loaded due to the total byte limit: %s]\n", len(paths), strings.Join(paths, ", ")),
		Chinese: fmt.Sprintf("\n[还有 %d 个文件因超出总字节上限未加载：%s]\n", len(paths), strings.Join(paths, "、")),
	})
}

func formatContent(files []loadedFile, omitted []string) string {
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
	sb.WriteString(omittedNotice(omitted))
	sb.WriteString(footer)
	return sb.String()
}
