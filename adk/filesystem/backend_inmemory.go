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
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type fileEntry struct {
	content    string
	modifiedAt time.Time
}

// InMemoryBackend is an in-memory implementation of the Backend interface.
// It stores files in a map and is safe for concurrent use.
type InMemoryBackend struct {
	mu    sync.RWMutex
	files map[string]*fileEntry
}

// NewInMemoryBackend creates a new in-memory backend.
func NewInMemoryBackend() *InMemoryBackend {
	return &InMemoryBackend{
		files: make(map[string]*fileEntry),
	}
}

// LsInfo lists file information under the given path.
func (b *InMemoryBackend) LsInfo(ctx context.Context, req *LsInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	// Normalize path
	path := normalizePath(req.Path)

	var result []FileInfo
	seen := make(map[string]bool)
	dirInfo := make(map[string]*FileInfo)

	for filePath, entry := range b.files {
		normalizedFilePath := normalizePath(filePath)

		// Check if file is under the given path
		if path == "/" || strings.HasPrefix(normalizedFilePath, path+"/") || normalizedFilePath == path {
			// For directory listing, we want to show immediate children
			relativePath := strings.TrimPrefix(normalizedFilePath, path)
			relativePath = strings.TrimPrefix(relativePath, "/")

			if relativePath == "" {
				// The path itself is a file
				if !seen[normalizedFilePath] {
					result = append(result, FileInfo{
						Path:       normalizedFilePath,
						IsDir:      false,
						Size:       int64(len(entry.content)),
						ModifiedAt: entry.modifiedAt.Format(time.RFC3339Nano),
					})
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

				isDir := len(parts) > 1
				if !seen[childPath] {
					if isDir {
						dirInfo[childPath] = &FileInfo{
							Path:       childPath,
							IsDir:      true,
							Size:       0,
							ModifiedAt: entry.modifiedAt.Format(time.RFC3339Nano),
						}
					} else {
						result = append(result, FileInfo{
							Path:       childPath,
							IsDir:      false,
							Size:       int64(len(entry.content)),
							ModifiedAt: entry.modifiedAt.Format(time.RFC3339Nano),
						})
					}
					seen[childPath] = true
				} else if isDir {
					if info, ok := dirInfo[childPath]; ok {
						if entry.modifiedAt.After(mustParseTime(info.ModifiedAt)) {
							info.ModifiedAt = entry.modifiedAt.Format(time.RFC3339Nano)
						}
					}
				}
			}
		}
	}

	for _, info := range dirInfo {
		result = append(result, *info)
	}

	return result, nil
}

func mustParseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}

// Read reads file content with offset and limit.
func (b *InMemoryBackend) Read(ctx context.Context, req *ReadRequest) (string, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	filePath := normalizePath(req.FilePath)

	entry, exists := b.files[filePath]
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

	lines := strings.Split(entry.content, "\n")
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

	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern cannot be empty")
	}

	re, err := b.compilePattern(req)
	if err != nil {
		return nil, err
	}

	searchPath := "/"
	if req.Path != "" {
		searchPath = normalizePath(req.Path)
	}

	if req.OutputMode == "" {
		req.OutputMode = FilesWithMatchesOfOutputMode
	}

	collector := newGrepCollector()

	for filePath, entry := range b.files {
		normalizedFilePath := normalizePath(filePath)

		if skip, err := b.shouldSkipFile(normalizedFilePath, searchPath, req); skip || err != nil {
			if err != nil {
				return nil, err
			}
			continue
		}

		collector.processFile(normalizedFilePath, entry.content, re, req)
	}

	return collector.buildResults(b, req)
}

func (b *InMemoryBackend) compilePattern(req *GrepRequest) (*regexp.Regexp, error) {
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}
	return re, nil
}

func (b *InMemoryBackend) shouldSkipFile(filePath, searchPath string, req *GrepRequest) (bool, error) {
	if searchPath != "/" && !strings.HasPrefix(filePath, searchPath+"/") && filePath != searchPath {
		return true, nil
	}

	if req.FileType != "" {
		ext := strings.TrimPrefix(filepath.Ext(filePath), ".")
		if !matchFileType(ext, req.FileType) {
			return true, nil
		}
	}

	if req.Glob != "" {
		matched, err := filepath.Match(req.Glob, filepath.Base(filePath))
		if err != nil {
			return false, fmt.Errorf("invalid glob pattern: %w", err)
		}
		if !matched {
			return true, nil
		}
	}

	return false, nil
}

type grepCollector struct {
	allMatches      []GrepMatch
	fileMatchCounts map[string]int
	matchedFiles    map[string]bool
}

func newGrepCollector() *grepCollector {
	return &grepCollector{
		allMatches:      []GrepMatch{},
		fileMatchCounts: make(map[string]int),
		matchedFiles:    make(map[string]bool),
	}
}

func (c *grepCollector) processFile(filePath, content string, re *regexp.Regexp, req *GrepRequest) {
	if req.OutputMode == FilesWithMatchesOfOutputMode {
		if re.MatchString(content) {
			c.matchedFiles[filePath] = true
		}
		return
	}

	fileMatches := c.findMatches(filePath, content, re, req)
	if len(fileMatches) > 0 {
		c.fileMatchCounts[filePath] = len(fileMatches)
		if c.needDetailedMatches(req) {
			c.allMatches = append(c.allMatches, fileMatches...)
		}
	}
}

func (c *grepCollector) needDetailedMatches(req *GrepRequest) bool {
	return req.OutputMode == ContentOfOutputMode ||
		req.ContextLines > 0 || req.BeforeLines > 0 || req.AfterLines > 0
}

func (c *grepCollector) findMatches(filePath, content string, re *regexp.Regexp, req *GrepRequest) []GrepMatch {
	if req.EnableMultiline {
		return c.findMultilineMatches(filePath, content, re)
	}
	return c.findSingleLineMatches(filePath, content, re)
}

func (c *grepCollector) findMultilineMatches(filePath, content string, re *regexp.Regexp) []GrepMatch {
	var fileMatches []GrepMatch
	matches := re.FindAllStringIndex(content, -1)
	for _, match := range matches {
		matchStart := match[0]
		matchedText := content[matchStart:match[1]]
		startLineNum := 1 + strings.Count(content[:matchStart], "\n")

		matchedLines := strings.Split(matchedText, "\n")
		for i, matchedLine := range matchedLines {
			fileMatches = append(fileMatches, GrepMatch{
				Path:    filePath,
				Line:    startLineNum + i,
				Content: matchedLine,
			})
		}
	}
	return fileMatches
}

func (c *grepCollector) findSingleLineMatches(filePath, content string, re *regexp.Regexp) []GrepMatch {
	var fileMatches []GrepMatch
	lines := strings.Split(content, "\n")
	for lineNum, line := range lines {
		if re.MatchString(line) {
			fileMatches = append(fileMatches, GrepMatch{
				Path:    filePath,
				Line:    lineNum + 1,
				Content: line,
			})
		}
	}
	return fileMatches
}

func (c *grepCollector) buildResults(b *InMemoryBackend, req *GrepRequest) ([]GrepMatch, error) {
	offset := req.Offset
	if offset < 0 {
		offset = 0
	}
	headLimit := req.HeadLimit
	if headLimit < 0 {
		headLimit = 0
	}

	switch req.OutputMode {
	case FilesWithMatchesOfOutputMode:
		return c.buildFilesWithMatchesResult(offset, headLimit), nil
	case ContentOfOutputMode:
		return c.buildContentResult(b, req, offset, headLimit), nil
	case CountOfOutputMode:
		return c.buildCountResult(offset, headLimit), nil
	default:
		return nil, fmt.Errorf("invalid output mode: %s", req.OutputMode)
	}
}

func (c *grepCollector) buildFilesWithMatchesResult(offset, headLimit int) []GrepMatch {
	paths := make([]string, 0, len(c.matchedFiles))
	for path := range c.matchedFiles {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	results := make([]GrepMatch, 0, len(paths))
	for _, path := range paths {
		results = append(results, GrepMatch{Path: path})
	}
	return applyPagination(results, offset, headLimit)
}

func (c *grepCollector) buildContentResult(b *InMemoryBackend, req *GrepRequest, offset, headLimit int) []GrepMatch {
	results := c.allMatches
	if req.ContextLines > 0 || req.BeforeLines > 0 || req.AfterLines > 0 {
		results = b.applyContext(c.allMatches, req)
	}
	return applyPagination(results, offset, headLimit)
}

func (c *grepCollector) buildCountResult(offset, headLimit int) []GrepMatch {
	paths := make([]string, 0, len(c.fileMatchCounts))
	for path := range c.fileMatchCounts {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	results := make([]GrepMatch, 0, len(paths))
	for _, path := range paths {
		results = append(results, GrepMatch{
			Path:  path,
			Count: c.fileMatchCounts[path],
		})
	}
	return applyPagination(results, offset, headLimit)
}

// matchFileType checks if the file extension matches the given file type.
func matchFileType(ext, fileType string) bool {
	typeMap := map[string][]string{
		"ada":          {"adb", "ads"},
		"agda":         {"agda", "lagda"},
		"aidl":         {"aidl"},
		"amake":        {"bp", "mk"},
		"asciidoc":     {"adoc", "asc", "asciidoc"},
		"asm":          {"S", "asm", "s"},
		"asp":          {"ascx", "asp", "aspx"},
		"ats":          {"ats", "dats", "hats", "sats"},
		"avro":         {"avdl", "avpr", "avsc"},
		"awk":          {"awk"},
		"bat":          {"bat"},
		"bazel":        {"BUILD", "bazel", "bzl"},
		"bitbake":      {"bb", "bbappend", "bbclass", "conf", "inc"},
		"c":            {"c", "h", "H", "cats"},
		"cabal":        {"cabal"},
		"cbor":         {"cbor"},
		"ceylon":       {"ceylon"},
		"clojure":      {"clj", "cljc", "cljs", "cljx"},
		"cmake":        {"cmake"},
		"coffeescript": {"coffee"},
		"config":       {"cfg", "conf", "config", "ini"},
		"coq":          {"v"},
		"cpp":          {"C", "cc", "cpp", "cxx", "c++", "h", "hh", "hpp", "hxx", "h++", "inl"},
		"crystal":      {"cr", "ecr"},
		"cs":           {"cs"},
		"csharp":       {"cs"},
		"cshtml":       {"cshtml"},
		"css":          {"css", "scss", "sass", "less"},
		"csv":          {"csv"},
		"cuda":         {"cu", "cuh"},
		"cython":       {"pxd", "pxi", "pyx"},
		"d":            {"d"},
		"dart":         {"dart"},
		"devicetree":   {"dts", "dtsi"},
		"dhall":        {"dhall"},
		"diff":         {"diff", "patch"},
		"docker":       {"dockerfile"},
		"go":           {"go"},
		"groovy":       {"gradle", "groovy"},
		"haskell":      {"c2hs", "cpphs", "hs", "hsc", "lhs"},
		"html":         {"ejs", "htm", "html"},
		"java":         {"java", "jsp", "jspx", "properties"},
		"js":           {"cjs", "js", "jsx", "mjs", "vue"},
		"json":         {"json", "sarif"},
		"jsonl":        {"jsonl"},
		"julia":        {"jl"},
		"jupyter":      {"ipynb", "jpynb"},
		"kotlin":       {"kt", "kts"},
		"less":         {"less"},
		"lua":          {"lua"},
		"make":         {"mak", "mk"},
		"markdown":     {"markdown", "md", "mdown", "mdwn", "mdx", "mkd", "mkdn"},
		"md":           {"markdown", "md", "mdown", "mdwn", "mdx", "mkd", "mkdn"},
		"matlab":       {"m"},
		"ocaml":        {"ml", "mli", "mll", "mly"},
		"perl":         {"PL", "perl", "pl", "plh", "plx", "pm", "t"},
		"php":          {"php", "php3", "php4", "php5", "php7", "php8", "pht", "phtml"},
		"python":       {"py", "pyi"},
		"py":           {"py", "pyi"},
		"ruby":         {"gemspec", "rb", "rbw"},
		"rust":         {"rs"},
		"sass":         {"sass", "scss"},
		"scala":        {"sbt", "scala"},
		"sh":           {"bash", "sh", "zsh"},
		"sql":          {"psql", "sql"},
		"swift":        {"swift"},
		"toml":         {"toml"},
		"ts":           {"cts", "mts", "ts", "tsx"},
		"typescript":   {"cts", "mts", "ts", "tsx"},
		"txt":          {"txt"},
		"vue":          {"vue"},
		"xml":          {"dtd", "xml", "xsd", "xsl", "xslt"},
		"yaml":         {"yaml", "yml"},
		"zig":          {"zig"},
	}

	if exts, ok := typeMap[fileType]; ok {
		for _, e := range exts {
			if ext == e {
				return true
			}
		}
	}
	return ext == fileType
}

// applyContext adds context lines around matches.
func (b *InMemoryBackend) applyContext(matches []GrepMatch, req *GrepRequest) []GrepMatch {
	if len(matches) == 0 {
		return matches
	}

	beforeLines := 0
	afterLines := 0

	if req.ContextLines > 0 {
		beforeLines = req.ContextLines
		afterLines = req.ContextLines
	} else {
		if req.BeforeLines > 0 {
			beforeLines = req.BeforeLines
		}
		if req.AfterLines > 0 {
			afterLines = req.AfterLines
		}
	}

	if beforeLines <= 0 && afterLines <= 0 {
		return matches
	}

	// Group matches by file path for efficient processing
	matchesByFile := make(map[string][]GrepMatch)
	fileOrder := make([]string, 0)
	seenFiles := make(map[string]bool)

	for _, match := range matches {
		if !seenFiles[match.Path] {
			fileOrder = append(fileOrder, match.Path)
			seenFiles[match.Path] = true
		}
		matchesByFile[match.Path] = append(matchesByFile[match.Path], match)
	}

	var result []GrepMatch

	// Process each file once
	for _, filePath := range fileOrder {
		fileMatches := matchesByFile[filePath]

		// Get file content once per file
		b.mu.RLock()
		entry, exists := b.files[filePath]
		b.mu.RUnlock()

		if !exists {
			// If file doesn't exist, keep original matches
			result = append(result, fileMatches...)
			continue
		}

		lines := strings.Split(entry.content, "\n")
		processedLines := make(map[int]bool)

		// Process all matches for this file
		for _, match := range fileMatches {
			startLine := match.Line - beforeLines
			if startLine < 1 {
				startLine = 1
			}

			endLine := match.Line + afterLines
			if endLine > len(lines) {
				endLine = len(lines)
			}

			for lineNum := startLine; lineNum <= endLine; lineNum++ {
				if !processedLines[lineNum] {
					processedLines[lineNum] = true
					result = append(result, GrepMatch{
						Path:    filePath,
						Line:    lineNum,
						Content: lines[lineNum-1],
					})
				}
			}
		}
	}

	return result
}

// applyPagination applies offset and head limit to results.
func applyPagination(matches []GrepMatch, offset, headLimit int) []GrepMatch {
	if offset > 0 {
		if offset >= len(matches) {
			return []GrepMatch{}
		}
		matches = matches[offset:]
	}

	if headLimit > 0 && headLimit < len(matches) {
		matches = matches[:headLimit]
	}

	return matches
}

// GlobInfo returns file info entries matching the glob pattern.
func (b *InMemoryBackend) GlobInfo(ctx context.Context, req *GlobInfoRequest) ([]FileInfo, error) {
	b.mu.RLock()
	defer b.mu.RUnlock()

	basePath := normalizePath(req.Path)

	var result []FileInfo

	for filePath, entry := range b.files {
		normalizedFilePath := normalizePath(filePath)

		if basePath != "/" && !strings.HasPrefix(normalizedFilePath, basePath+"/") && normalizedFilePath != basePath {
			continue
		}

		var relativePath string
		if basePath == "/" {
			relativePath = strings.TrimPrefix(normalizedFilePath, "/")
		} else {
			relativePath = strings.TrimPrefix(normalizedFilePath, basePath+"/")
		}

		matched, err := filepath.Match(req.Pattern, relativePath)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern: %w", err)
		}

		if matched {
			result = append(result, FileInfo{
				Path:       normalizedFilePath,
				IsDir:      false,
				Size:       int64(len(entry.content)),
				ModifiedAt: entry.modifiedAt.Format(time.RFC3339Nano),
			})
		}
	}

	return result, nil
}

// Write creates or overwrites file content.
func (b *InMemoryBackend) Write(ctx context.Context, req *WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	filePath := normalizePath(req.FilePath)
	b.files[filePath] = &fileEntry{
		content:    req.Content,
		modifiedAt: time.Now(),
	}

	return nil
}

// Edit replaces string occurrences in a file.
func (b *InMemoryBackend) Edit(ctx context.Context, req *EditRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	filePath := normalizePath(req.FilePath)

	entry, exists := b.files[filePath]
	if !exists {
		return fmt.Errorf("file not found: %s", filePath)
	}

	if req.OldString == "" {
		return fmt.Errorf("oldString must be non-empty")
	}

	content := entry.content
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

	var newContent string
	if req.ReplaceAll {
		newContent = strings.ReplaceAll(content, req.OldString, req.NewString)
	} else {
		newContent = strings.Replace(content, req.OldString, req.NewString, 1)
	}

	b.files[filePath] = &fileEntry{
		content:    newContent,
		modifiedAt: time.Now(),
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
