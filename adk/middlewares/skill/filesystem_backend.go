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
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/cloudwego/eino/adk/filesystem"
)

const skillFileName = "SKILL.md"

type filesystemBackend struct {
	backend filesystem.Backend
	baseDir string
}

// FilesystemBackendConfig contains configuration for creating a FilesystemBackend.
type FilesystemBackendConfig struct {
	// Backend is the filesystem.Backend implementation used for file operations.
	Backend filesystem.Backend
	// BaseDir is the base directory where skill directories are located.
	// Each skill should be in a subdirectory containing a SKILL.md file.
	BaseDir string
}

// NewBackendFromFilesystem creates a new Backend implementation that reads skills from a filesystem.
// It searches for SKILL.md files in immediate subdirectories of the configured BaseDir.
// Only first-level subdirectories are scanned; deeply nested SKILL.md files are ignored.
func NewBackendFromFilesystem(config *FilesystemBackendConfig) (Backend, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	return &filesystemBackend{
		backend: config.Backend,
		baseDir: config.BaseDir,
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
		skill, err := b.loadSkillFromFile(ctx, entry.Path)
		if err != nil {
			return nil, fmt.Errorf("failed to load skill from %s: %w", entry.Path, err)
		}

		skills = append(skills, skill)
	}

	return skills, nil
}

func (b *filesystemBackend) loadSkillFromFile(ctx context.Context, path string) (Skill, error) {
	data, err := b.backend.Read(ctx, &filesystem.ReadRequest{
		FilePath: path,
	})
	if err != nil {
		return Skill{}, fmt.Errorf("failed to read file: %w", err)
	}

	data = stripLineNumbers(data)

	frontmatter, content, err := parseFrontmatter(data)
	if err != nil {
		return Skill{}, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	var fm FrontMatter
	if err = yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return Skill{}, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}

	absDir := filepath.Dir(path)

	return Skill{
		FrontMatter: FrontMatter{
			Name:        fm.Name,
			Description: fm.Description,
		},
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

	if strings.HasPrefix(content, "\n") {
		content = content[1:]
	}

	return frontmatter, content, nil
}
