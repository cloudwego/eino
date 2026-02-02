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
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const skillFileName = "SKILL.md"

// LocalBackend is a Backend implementation that reads skills from the local filesystem.
// Skills are stored in subdirectories of baseDir, each containing a SKILL.md file.
type LocalBackend struct {
	// baseDir is the root directory containing skill subdirectories.
	baseDir string
}

// LocalBackendConfig is the configuration for creating a LocalBackend.
type LocalBackendConfig struct {
	// BaseDir is the root directory containing skill subdirectories.
	// Each subdirectory should contain a SKILL.md file with frontmatter and content.
	BaseDir string
}

// NewLocalBackend creates a new LocalBackend with the given configuration.
func NewLocalBackend(config *LocalBackendConfig) (*LocalBackend, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.BaseDir == "" {
		return nil, fmt.Errorf("baseDir is required")
	}

	// Verify the directory exists
	info, err := os.Stat(config.BaseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to stat baseDir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("baseDir is not a directory: %s", config.BaseDir)
	}

	return &LocalBackend{
		baseDir: config.BaseDir,
	}, nil
}

// List returns all skills from the local filesystem.
// It scans subdirectories of baseDir for SKILL.md files and parses them as skills.
func (b *LocalBackend) List(ctx context.Context) ([]FrontMatter, error) {
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

// Get returns a skill by name from the local filesystem.
// It searches subdirectories for a SKILL.md file with matching name.
func (b *LocalBackend) Get(ctx context.Context, name string) (Skill, error) {
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

func (b *LocalBackend) list(ctx context.Context) ([]Skill, error) {
	var skills []Skill

	entries, err := os.ReadDir(b.baseDir)
	if err != nil {
		return nil, fmt.Errorf("failed to read directory: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(b.baseDir, entry.Name())
		skillPath := filepath.Join(skillDir, skillFileName)

		// Check if SKILL.md exists in this directory
		if _, err := os.Stat(skillPath); os.IsNotExist(err) {
			continue
		}

		skill, err := b.loadSkillFromFile(skillPath)
		if err != nil {
			return nil, fmt.Errorf("failed to load skill from %s: %w", skillPath, err)
		}

		skills = append(skills, skill)
	}

	return skills, nil
}

// loadSkillFromFile loads a skill from a SKILL.md file.
// The file format is:
//
//	---
//	name: skill-name
//	description: skill description
//	---
//	Content goes here...
func (b *LocalBackend) loadSkillFromFile(path string) (Skill, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Skill{}, fmt.Errorf("failed to read file: %w", err)
	}

	frontmatter, content, err := parseFrontmatter(string(data))
	if err != nil {
		return Skill{}, fmt.Errorf("failed to parse frontmatter: %w", err)
	}

	var fm FrontMatter
	if err = yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return Skill{}, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}

	// Get the absolute path of the directory containing SKILL.md
	absDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Skill{}, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return Skill{
		FrontMatter: FrontMatter{
			Name:        fm.Name,
			Description: fm.Description,
		},
		Content:       strings.TrimSpace(content),
		BaseDirectory: absDir,
	}, nil
}

// parseFrontmatter parses a markdown file with YAML frontmatter.
// Returns the frontmatter content (without ---), the remaining content, and any error.
func parseFrontmatter(data string) (frontmatter string, content string, err error) {
	const delimiter = "---"

	data = strings.TrimSpace(data)

	// Must start with ---
	if !strings.HasPrefix(data, delimiter) {
		return "", "", fmt.Errorf("file does not start with frontmatter delimiter")
	}

	// Find the closing ---
	rest := data[len(delimiter):]
	endIdx := strings.Index(rest, "\n"+delimiter)
	if endIdx == -1 {
		return "", "", fmt.Errorf("frontmatter closing delimiter not found")
	}

	frontmatter = strings.TrimSpace(rest[:endIdx])
	content = rest[endIdx+len("\n"+delimiter):]

	// Remove the newline after the closing ---
	if strings.HasPrefix(content, "\n") {
		content = content[1:]
	}

	return frontmatter, content, nil
}
