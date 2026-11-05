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

package skill

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cloudwego/eino/adk/filesystem"
)

const skillFileName = "SKILL.md"

type filesystemBackend struct {
	backend       filesystem.Backend
	baseDir       string
	onLoadWarning func(filePath string, err error)
}

// BackendFromFilesystemConfig contains configuration for NewBackendFromFilesystem.
type BackendFromFilesystemConfig struct {
	// Backend is the filesystem.Backend implementation used for file operations.
	Backend filesystem.Backend
	// BaseDir is the base directory where skill directories are located.
	// Each skill should be in a subdirectory containing a SKILL.md file.
	BaseDir string

	// OnLoadWarning is an optional callback invoked when a malformed SKILL.md
	// file is skipped during loading. If nil, warnings are logged via log.Printf.
	OnLoadWarning func(filePath string, err error)
}

// NewBackendFromFilesystem creates a new Backend implementation that reads skills from a filesystem.
// It searches for SKILL.md files in immediate subdirectories of the configured BaseDir.
// Only first-level subdirectories are scanned; deeply nested SKILL.md files are ignored.
func NewBackendFromFilesystem(_ context.Context, config *BackendFromFilesystemConfig) (Backend, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	onLoadWarning := config.OnLoadWarning
	if onLoadWarning == nil {
		onLoadWarning = func(filePath string, err error) {
			log.Printf("[skill] warning: %s: %v", filePath, err)
		}
	}

	return &filesystemBackend{
		backend:       config.Backend,
		baseDir:       config.BaseDir,
		onLoadWarning: onLoadWarning,
	}, nil
}

func (b *filesystemBackend) List(ctx context.Context) ([]FrontMatter, error) {
	skills, err := b.list(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}

	matters := make([]FrontMatter, 0, len(skills))
	for _, skill := range skills {
		matters = append(matters, skill.FrontMatter)
	}

	return matters, nil
}

func (b *filesystemBackend) Get(ctx context.Context, name string) (Skill, error) {
	skills, err := b.list(ctx)
	if err != nil {
		return Skill{}, fmt.Errorf("failed to list skills: %w", err)
	}

	for _, skill := range skills {
		if skill.Name == name {
			return skill, nil
		}
	}

	return Skill{}, fmt.Errorf("skill not found: %s", name)
}

func (b *filesystemBackend) list(ctx context.Context) ([]Skill, error) {
	var skills []Skill

	pattern := "*/" + skillFileName
	entries, err := b.backend.GlobInfo(ctx, &filesystem.GlobInfoRequest{
		Pattern: pattern,
		Path:    b.baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to glob skill files: %w", err)
	}

	for _, entry := range entries {
		filePath := entry.Path
		if !filepath.IsAbs(filePath) {
			filePath = filepath.Join(b.baseDir, filePath)
		}
		skill, loadErr := b.loadSkillFromFile(ctx, filePath)
		if loadErr != nil {
			var malformedErr *malformedSkillFileError
			if errors.As(loadErr, &malformedErr) {
				b.warnLoadFailure(filePath, loadErr)
				continue
			}
			return nil, fmt.Errorf("failed to load skill from %s: %w", filePath, loadErr)
		}

		skills = append(skills, skill)
	}

	return skills, nil
}

func (b *filesystemBackend) warnLoadFailure(filePath string, err error) {
	if b.onLoadWarning != nil {
		b.onLoadWarning(filePath, err)
		return
	}

	log.Printf("[skill] warning: %s: %v", filePath, err)
}

type malformedSkillFileError struct {
	err error
}

func (e *malformedSkillFileError) Error() string {
	return e.err.Error()
}

func (e *malformedSkillFileError) Unwrap() error {
	return e.err
}

func (b *filesystemBackend) loadSkillFromFile(ctx context.Context, path string) (Skill, error) {
	fileContent, err := b.backend.Read(ctx, &filesystem.ReadRequest{
		FilePath: path,
	})
	if err != nil {
		return Skill{}, fmt.Errorf("failed to read file: %w", err)
	}

	data := stripLineNumbers(fileContent.Content)

	frontmatter, content, err := parseFrontmatter(data)
	if err != nil {
		return Skill{}, &malformedSkillFileError{
			err: fmt.Errorf("failed to parse frontmatter: %w", err),
		}
	}

	var fm FrontMatter
	if err = yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return Skill{}, &malformedSkillFileError{
			err: fmt.Errorf("failed to unmarshal frontmatter: %w", err),
		}
	}

	absDir := filepath.Dir(path)

	return Skill{
		FrontMatter:   fm,
		Content:       strings.TrimSpace(content),
		BaseDirectory: absDir,
	}, nil
}

func stripLineNumbers(data string) string {
	lines := strings.Split(data, "\n")
	result := make([]string, 0, len(lines))
	for _, line := range lines {
		idx := strings.Index(line, "\t")
		if idx != -1 {
			line = line[idx+1:]
		}
		result = append(result, line)
	}
	return strings.Join(result, "\n")
}

func parseFrontmatter(data string) (frontmatter string, content string, err error) {
	const delimiter = "---"

	data = strings.TrimSpace(data)

	if !strings.HasPrefix(data, delimiter) {
		return "", "", fmt.Errorf("file does not start with frontmatter delimiter")
	}

	rest := data[len(delimiter):]
	endIdx := strings.Index(rest, "\n"+delimiter)
	if endIdx == -1 {
		return "", "", fmt.Errorf("frontmatter closing delimiter not found")
	}

	frontmatter = strings.TrimSpace(rest[:endIdx])
	content = rest[endIdx+len("\n"+delimiter):]

	content = strings.TrimPrefix(content, "\n")

	return frontmatter, content, nil
}
