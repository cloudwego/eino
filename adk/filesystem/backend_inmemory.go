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

	// Normalize path
	path := normalizePath(req.Path)

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

	// Validate pattern is not empty
	if req.Pattern == "" {
		return nil, fmt.Errorf("pattern cannot be empty")
	}

	searchPath := "/"
	if req.Path != "" {
		searchPath = normalizePath(req.Path)
	}

	if req.OutputMode == "" {
		req.OutputMode = FilesWithMatchesOfOutputMode
	}

	// Compile regex pattern once
	var re *regexp.Regexp
	var err error
	if req.CaseInsensitive {
		re, err = regexp.Compile("(?i)" + req.Pattern)
	} else {
		re, err = regexp.Compile(req.Pattern)
	}
	if err != nil {
		return nil, fmt.Errorf("invalid regex pattern: %w", err)
	}

	var allMatches []GrepMatch
	fileMatchCounts := make(map[string]int)
	matchedFiles := make(map[string]bool) // For files_with_matches mode

	// Determine if we need detailed match information
	needDetailedMatches := req.OutputMode == ContentOfOutputMode ||
		req.ContextLines > 0 || req.BeforeLines > 0 || req.AfterLines > 0

	for filePath, content := range b.files {
		normalizedFilePath := normalizePath(filePath)

		// Check if file is under the search path
		if searchPath != "/" && !strings.HasPrefix(normalizedFilePath, searchPath+"/") && normalizedFilePath != searchPath {
			continue
		}

		// Check file type if provided
		if req.FileType != "" {
			ext := strings.TrimPrefix(filepath.Ext(normalizedFilePath), ".")
			if !matchFileType(ext, req.FileType) {
				continue
			}
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

		// For files_with_matches mode, we only need to know if file has any match
		if req.OutputMode == FilesWithMatchesOfOutputMode {
			hasMatch := re.MatchString(content)
			if hasMatch {
				matchedFiles[normalizedFilePath] = true
			}
			continue
		}

		// For other modes, collect detailed match information
		var fileMatches []GrepMatch

		if req.EnableMultiline {
			// Multiline mode: match across entire file content
			matches := re.FindAllStringIndex(content, -1)
			if len(matches) > 0 {
				for _, match := range matches {
					matchStart := match[0]
					matchEnd := match[1]
					matchedText := content[matchStart:matchEnd]

					// Find the starting line number
					startLineNum := 1 + strings.Count(content[:matchStart], "\n")

					// Split matched text into lines and create a GrepMatch for each line
					matchedLines := strings.Split(matchedText, "\n")
					for i, matchedLine := range matchedLines {
						fileMatches = append(fileMatches, GrepMatch{
							Path:       normalizedFilePath,
							LineNumber: startLineNum + i,
							Content:    matchedLine,
						})
					}
				}
			}
		} else {
			// Single-line mode: match within each line
			lines := strings.Split(content, "\n")
			for lineNum, line := range lines {
				if re.MatchString(line) {
					fileMatches = append(fileMatches, GrepMatch{
						Path:       normalizedFilePath,
						LineNumber: lineNum + 1,
						Content:    line,
					})
				}
			}
		}

		if len(fileMatches) > 0 {
			fileMatchCounts[normalizedFilePath] = len(fileMatches)
			if needDetailedMatches {
				allMatches = append(allMatches, fileMatches...)
			}
		}
	}

	// Apply offset and head limit
	offset := 0
	if req.Offset > 0 {
		offset = req.Offset
	}
	headLimit := 0
	if req.HeadLimit > 0 {
		headLimit = req.HeadLimit
	}

	// Process based on output mode
	switch req.OutputMode {
	case FilesWithMatchesOfOutputMode:
		// Return unique file paths (sorted for consistent ordering)
		var paths []string
		for path := range matchedFiles {
			paths = append(paths, path)
		}
		sort.Strings(paths)

		var results []GrepMatch
		for _, path := range paths {
			results = append(results, GrepMatch{
				Path: path,
			})
		}
		return applyPagination(results, offset, headLimit), nil

	case ContentOfOutputMode:
		// Return matching lines with context
		results := allMatches

		// Apply context lines if needed
		if req.ContextLines > 0 || req.BeforeLines > 0 || req.AfterLines > 0 {
			results = b.applyContext(allMatches, req)
		}

		return applyPagination(results, offset, headLimit), nil

	case CountOfOutputMode:
		// Return match counts per file (sorted for consistent ordering)
		var paths []string
		for path := range fileMatchCounts {
			paths = append(paths, path)
		}
		sort.Strings(paths)

		var results []GrepMatch
		for _, path := range paths {
			results = append(results, GrepMatch{
				Path:  path,
				Count: fileMatchCounts[path],
			})
		}
		return applyPagination(results, offset, headLimit), nil

	default:
		return nil, fmt.Errorf("invalid output mode: %s", req.OutputMode)
	}
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
		content, exists := b.files[filePath]
		b.mu.RUnlock()

		if !exists {
			// If file doesn't exist, keep original matches
			result = append(result, fileMatches...)
			continue
		}

		lines := strings.Split(content, "\n")
		processedLines := make(map[int]bool)

		// Process all matches for this file
		for _, match := range fileMatches {
			startLine := match.LineNumber - beforeLines
			if startLine < 1 {
				startLine = 1
			}

			endLine := match.LineNumber + afterLines
			if endLine > len(lines) {
				endLine = len(lines)
			}

			for lineNum := startLine; lineNum <= endLine; lineNum++ {
				if !processedLines[lineNum] {
					processedLines[lineNum] = true
					result = append(result, GrepMatch{
						Path:       filePath,
						LineNumber: lineNum,
						Content:    lines[lineNum-1],
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

	path := normalizePath(req.Path)

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
	if _, ok := b.files[filePath]; ok {
		return fmt.Errorf("file already exists: %s", filePath)
	}

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
