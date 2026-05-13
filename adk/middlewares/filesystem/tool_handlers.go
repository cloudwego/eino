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
	"encoding/base64"
	"fmt"
	"io"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

func lsHandler(ctx context.Context, fs filesystem.Backend, input lsArgs) (string, error) {
	infos, err := fs.LsInfo(ctx, &filesystem.LsInfoRequest{Path: input.Path})
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return noFilesFound, nil
	}
	paths := make([]string, 0, len(infos))
	for _, fi := range infos {
		paths = append(paths, fi.Path)
	}
	return strings.Join(paths, "\n"), nil
}

func readFileHandler(ctx context.Context, fs filesystem.Backend, input readFileArgs) (string, error) {
	if input.Offset <= 0 {
		input.Offset = 1
	}
	if input.Limit <= 0 {
		input.Limit = 2000
	}

	fileCt, err := fs.Read(ctx, &filesystem.ReadRequest{
		FilePath: input.FilePath,
		Offset:   input.Offset,
		Limit:    input.Limit,
	})
	if err != nil {
		return "", err
	}
	if fileCt == nil {
		return fmt.Sprintf("No content found at path: %s", input.FilePath), nil
	}

	return formatLineNumbers(fileCt.Content, input.Offset), nil
}

func multiModalReadFileHandler(ctx context.Context, er filesystem.MultiModalReader, input multiModalReadFileArgs) (*schema.ToolResult, error) {
	if input.Offset <= 0 {
		input.Offset = 1
	}
	if input.Limit <= 0 {
		input.Limit = 2000
	}

	if input.Pages != "" {
		if err := validatePages(input.Pages); err != nil {
			return &schema.ToolResult{
				Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: err.Error()}},
			}, nil
		}
	}

	fileCt, err := er.MultiModalRead(ctx, &filesystem.MultiModalReadRequest{
		ReadRequest: filesystem.ReadRequest{
			FilePath: input.FilePath,
			Offset:   input.Offset,
			Limit:    input.Limit,
		},
		Pages: input.Pages,
	})
	if err != nil {
		return nil, err
	}

	if fileCt == nil {
		return &schema.ToolResult{
			Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: fmt.Sprintf("No content found at path: %s", input.FilePath)}},
		}, nil
	}

	if len(fileCt.Parts) > 0 {
		parts := make([]schema.ToolOutputPart, 0, len(fileCt.Parts))
		enc := base64Encoder{}
		for _, p := range fileCt.Parts {
			if len(p.Data) == 0 {
				return nil, fmt.Errorf("FileContentPart.Data is empty for type %s", p.Type)
			}
			if p.MIMEType == "" {
				return nil, fmt.Errorf("FileContentPart.MIMEType is empty for type %s", p.Type)
			}
			b64 := enc.encode(p.Data)
			switch p.Type {
			case filesystem.FileContentPartTypeImage:
				parts = append(parts, schema.ToolOutputPart{
					Type: schema.ToolPartTypeImage,
					Image: &schema.ToolOutputImage{
						MessagePartCommon: schema.MessagePartCommon{
							MIMEType:   p.MIMEType,
							Base64Data: &b64,
						},
					},
				})
			case filesystem.FileContentPartTypePDF:
				parts = append(parts, schema.ToolOutputPart{
					Type: schema.ToolPartTypeFile,
					File: &schema.ToolOutputFile{
						MessagePartCommon: schema.MessagePartCommon{
							MIMEType:   p.MIMEType,
							Base64Data: &b64,
						},
					},
				})
			default:
				return nil, fmt.Errorf("unsupported FileContentPartType: %s", p.Type)
			}
		}
		return &schema.ToolResult{Parts: parts}, nil
	}

	if fileCt.FileContent == nil {
		return &schema.ToolResult{
			Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: fmt.Sprintf("No content found at path: %s", input.FilePath)}},
		}, nil
	}

	return &schema.ToolResult{
		Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: formatLineNumbers(fileCt.Content, input.Offset)}},
	}, nil
}

func writeFileHandler(ctx context.Context, fs filesystem.Backend, input writeFileArgs) (string, error) {
	err := fs.Write(ctx, &filesystem.WriteRequest{
		FilePath: input.FilePath,
		Content:  input.Content,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Updated file %s", input.FilePath), nil
}

func editFileHandler(ctx context.Context, fs filesystem.Backend, input editFileArgs) (string, error) {
	err := fs.Edit(ctx, &filesystem.EditRequest{
		FilePath:   input.FilePath,
		OldString:  input.OldString,
		NewString:  input.NewString,
		ReplaceAll: input.ReplaceAll,
	})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Successfully replaced the string in '%s'", input.FilePath), nil
}

func globHandler(ctx context.Context, fs filesystem.Backend, input globArgs) (string, error) {
	infos, err := fs.GlobInfo(ctx, &filesystem.GlobInfoRequest{
		Pattern: input.Pattern,
		Path:    input.Path,
	})
	if err != nil {
		return "", err
	}
	if len(infos) == 0 {
		return noFilesFound, nil
	}
	paths := make([]string, 0, len(infos))
	for _, fi := range infos {
		paths = append(paths, fi.Path)
	}
	return strings.Join(paths, "\n"), nil
}

func grepHandler(ctx context.Context, fs filesystem.Backend, input grepArgs) (string, error) {
	path := valueOrDefault(input.Path, "")
	glob := valueOrDefault(input.Glob, "")
	fileType := valueOrDefault(input.FileType, "")
	var beforeLines, afterLines int

	if input.Context != nil {
		beforeLines = valueOrDefault(input.Context, 0)
		afterLines = valueOrDefault(input.Context, 0)
	} else {
		beforeLines = valueOrDefault(input.BeforeLines, 0)
		afterLines = valueOrDefault(input.AfterLines, 0)
	}

	caseInsensitive := valueOrDefault(input.CaseInsensitive, false)
	enableMultiline := valueOrDefault(input.Multiline, false)

	headLimit := valueOrDefault(input.HeadLimit, 0)
	offset := valueOrDefault(input.Offset, 0)

	matches, err := fs.GrepRaw(ctx, &filesystem.GrepRequest{
		Pattern:         input.Pattern,
		Path:            path,
		Glob:            glob,
		FileType:        fileType,
		CaseInsensitive: caseInsensitive,
		AfterLines:      afterLines,
		BeforeLines:     beforeLines,
		EnableMultiline: enableMultiline,
	})
	if err != nil {
		return "", err
	}

	sort.SliceStable(matches, func(i, j int) bool {
		return filepath.Base(matches[i].Path) < filepath.Base(matches[j].Path)
	})

	switch input.OutputMode {
	case "content":
		matches = applyPagination(matches, offset, headLimit)
		return formatContentMatches(matches, valueOrDefault(input.ShowLineNumbers, true)), nil

	case "count":
		return formatCountMatches(matches, offset, headLimit), nil

	case "files_with_matches":
		return formatFileMatches(matches, offset, headLimit), nil

	default:
		return formatFileMatches(matches, offset, headLimit), nil
	}
}

func executeHandler(ctx context.Context, sb filesystem.Shell, input executeArgs) (string, error) {
	result, err := sb.Execute(ctx, &filesystem.ExecuteRequest{
		Command: input.Command,
	})
	if err != nil {
		return "", err
	}
	return convExecuteResponse(result), nil
}

func streamingExecuteHandler(ctx context.Context, sb filesystem.StreamingShell, input executeArgs) (*schema.StreamReader[string], error) {
	result, err := sb.ExecuteStreaming(ctx, &filesystem.ExecuteRequest{
		Command: input.Command,
	})
	if err != nil {
		return nil, err
	}
	sr, sw := schema.Pipe[string](10)
	go func() {
		defer func() {
			e := recover()
			if e != nil {
				sw.Send("", fmt.Errorf("panic: %v,\n stack: %s", e, string(debug.Stack())))
			}
			sw.Close()
		}()

		var hasSentContent bool
		var exitCode *int

		for {
			chunk, recvErr := result.Recv()
			if recvErr == io.EOF {
				break
			}
			if recvErr != nil {
				sw.Send("", recvErr)
				return
			}

			if chunk == nil {
				continue
			}
			if chunk.ExitCode != nil {
				exitCode = chunk.ExitCode
			}

			parts := make([]string, 0, 2)
			if chunk.Output != "" {
				parts = append(parts, chunk.Output)
			}
			if chunk.Truncated {
				parts = append(parts, "[Output was truncated due to size limits]")
			}
			if len(parts) > 0 {
				sw.Send(strings.Join(parts, "\n"), nil)
				hasSentContent = true
			}
		}

		if exitCode != nil && *exitCode != 0 {
			sw.Send(fmt.Sprintf("\n[Command failed with exit code %d]", *exitCode), nil)
		} else if !hasSentContent {
			sw.Send("[Command executed successfully with no output]", nil)
		}
	}()

	return sr, nil
}

func convExecuteResponse(response *filesystem.ExecuteResponse) string {
	if response == nil {
		return ""
	}
	parts := []string{response.Output}
	if response.ExitCode != nil && *response.ExitCode != 0 {
		parts = append(parts, fmt.Sprintf("[Command failed with exit code %d]", *response.ExitCode))
	}
	if response.Truncated {
		parts = append(parts, "[Output was truncated due to size limits]")
	}

	result := strings.Join(parts, "\n")
	if result == "" && (response.ExitCode == nil || *response.ExitCode == 0) {
		return "[Command executed successfully with no output]"
	}
	return result
}

// formatLineNumbers prefixes each line of content with a 1-based line number
// starting at startLine (e.g. "     1\tfoo"). startLine corresponds to the
// line number of the first line in content (usually ReadRequest.Offset).
func formatLineNumbers(content string, startLine int) string {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i, line := range lines {
		if i < len(lines)-1 {
			fmt.Fprintf(&b, "%6d\t%s\n", startLine+i, line)
		} else {
			fmt.Fprintf(&b, "%6d\t%s", startLine+i, line)
		}
	}
	return b.String()
}

const maxPagesPerRequest = 20

func validatePages(pages string) error {
	parts := strings.SplitN(pages, "-", 2)
	start, err := strconv.Atoi(parts[0])
	if err != nil || start < 1 {
		return fmt.Errorf("invalid pages parameter %q: expected format like \"3\" or \"1-10\"", pages)
	}
	if len(parts) == 1 {
		return nil
	}
	if parts[1] == "" {
		return fmt.Errorf("invalid pages parameter %q: expected format like \"3\" or \"1-10\"", pages)
	}
	end, err := strconv.Atoi(parts[1])
	if err != nil || end < 1 {
		return fmt.Errorf("invalid pages parameter %q: expected format like \"3\" or \"1-10\"", pages)
	}
	if end < start {
		return fmt.Errorf("invalid pages parameter %q: end page must be >= start page", pages)
	}
	if end-start+1 > maxPagesPerRequest {
		return fmt.Errorf("invalid pages parameter %q: range exceeds maximum of %d pages per request", pages, maxPagesPerRequest)
	}
	return nil
}

// valueOrDefault returns the value pointed to by ptr, or defaultValue if ptr is nil.
func valueOrDefault[T any](ptr *T, defaultValue T) T {
	if ptr != nil {
		return *ptr
	}
	return defaultValue
}

// base64Encoder reuses a buffer across multiple base64 encoding calls to reduce allocations.
type base64Encoder struct {
	buf []byte
}

func (e *base64Encoder) encode(data []byte) string {
	n := base64.StdEncoding.EncodedLen(len(data))
	if cap(e.buf) < n {
		e.buf = make([]byte, n)
	} else {
		e.buf = e.buf[:n]
	}
	base64.StdEncoding.Encode(e.buf, data)
	return string(e.buf)
}

func applyPagination[T any](items []T, offset, headLimit int) []T {
	if offset < 0 {
		offset = 0
	}
	if offset >= len(items) {
		return []T{}
	}
	items = items[offset:]

	if headLimit > 0 && headLimit < len(items) {
		items = items[:headLimit]
	}
	return items
}

func formatFileMatches(matches []filesystem.GrepMatch, offset, headLimit int) string {
	if len(matches) == 0 {
		return noFilesFound
	}
	seen := make(map[string]bool)
	var uniquePaths []string
	for _, match := range matches {
		if !seen[match.Path] {
			seen[match.Path] = true
			uniquePaths = append(uniquePaths, match.Path)
		}
	}
	totalFiles := len(uniquePaths)
	uniquePaths = applyPagination(uniquePaths, offset, headLimit)

	fileWord := "files"
	if totalFiles == 1 {
		fileWord = "file"
	}
	return fmt.Sprintf("Found %d %s\n%s", totalFiles, fileWord, strings.Join(uniquePaths, "\n"))
}

func formatContentMatches(matches []filesystem.GrepMatch, showLineNum bool) string {
	if len(matches) == 0 {
		return noMatchesFound
	}
	var b strings.Builder
	for _, match := range matches {
		b.WriteString(match.Path)
		if showLineNum {
			b.WriteString(":")
			b.WriteString(strconv.Itoa(match.Line))
		}
		b.WriteString(":")
		b.WriteString(match.Content)
		b.WriteString("\n")
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func formatCountMatches(matches []filesystem.GrepMatch, offset, headLimit int) string {
	countMap := make(map[string]int)
	for _, match := range matches {
		countMap[match.Path]++
	}

	var paths []string
	for path := range countMap {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	totalOccurrences := len(matches)
	totalFiles := len(paths)

	occurrenceWord := "occurrences"
	if totalOccurrences == 1 {
		occurrenceWord = "occurrence"
	}
	fileWord := "files"
	if totalFiles == 1 {
		fileWord = "file"
	}

	if totalOccurrences == 0 {
		return fmt.Sprintf("%s\n\nFound %d total %s across %d %s.", noMatchesFound, totalOccurrences, occurrenceWord, totalFiles, fileWord)
	}

	paths = applyPagination(paths, offset, headLimit)

	var b strings.Builder
	for _, path := range paths {
		b.WriteString(path)
		b.WriteString(":")
		b.WriteString(strconv.Itoa(countMap[path]))
		b.WriteString("\n")
	}
	result := strings.TrimSuffix(b.String(), "\n")
	return fmt.Sprintf("%s\n\nFound %d total %s across %d %s.", result, totalOccurrences, occurrenceWord, totalFiles, fileWord)
}
