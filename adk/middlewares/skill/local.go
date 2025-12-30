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

// skillFrontmatter represents the YAML frontmatter in a SKILL.md file.
type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// List returns all skills from the local filesystem.
// It scans subdirectories of baseDir for SKILL.md files and parses them as skills.
func (b *LocalBackend) List(ctx context.Context) ([]Skill, error) {
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

// Get returns a skill by name from the local filesystem.
// It searches subdirectories for a SKILL.md file with matching name.
func (b *LocalBackend) Get(ctx context.Context, name string) (Skill, error) {
	skills, err := b.List(ctx)
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

	var fm skillFrontmatter
	if err := yaml.Unmarshal([]byte(frontmatter), &fm); err != nil {
		return Skill{}, fmt.Errorf("failed to unmarshal frontmatter: %w", err)
	}

	// Get the absolute path of the directory containing SKILL.md
	absDir, err := filepath.Abs(filepath.Dir(path))
	if err != nil {
		return Skill{}, fmt.Errorf("failed to get absolute path: %w", err)
	}

	return Skill{
		Name:          fm.Name,
		Description:   fm.Description,
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
