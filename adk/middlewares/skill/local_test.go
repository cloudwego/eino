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
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewLocalBackend(t *testing.T) {
	t.Run("nil config returns error", func(t *testing.T) {
		backend, err := NewLocalBackend(nil)
		assert.Nil(t, backend)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("empty baseDir returns error", func(t *testing.T) {
		backend, err := NewLocalBackend(&LocalBackendConfig{
			BaseDir: "",
		})
		assert.Nil(t, backend)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "baseDir is required")
	})

	t.Run("non-existent baseDir returns error", func(t *testing.T) {
		backend, err := NewLocalBackend(&LocalBackendConfig{
			BaseDir: "/path/that/does/not/exist",
		})
		assert.Nil(t, backend)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to stat baseDir")
	})

	t.Run("baseDir is a file returns error", func(t *testing.T) {
		// Create a temporary file
		tmpFile, err := os.CreateTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.Remove(tmpFile.Name())
		tmpFile.Close()

		backend, err := NewLocalBackend(&LocalBackendConfig{
			BaseDir: tmpFile.Name(),
		})
		assert.Nil(t, backend)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "baseDir is not a directory")
	})

	t.Run("valid baseDir succeeds", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		backend, err := NewLocalBackend(&LocalBackendConfig{
			BaseDir: tmpDir,
		})
		assert.NoError(t, err)
		assert.NotNil(t, backend)
		assert.Equal(t, tmpDir, backend.baseDir)
	})
}

func TestLocalBackend_List(t *testing.T) {
	ctx := context.Background()

	t.Run("empty directory returns empty list", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skills, err := backend.List(ctx)
		assert.NoError(t, err)
		assert.Empty(t, skills)
	})

	t.Run("directory with no SKILL.md files returns empty list", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create a subdirectory without SKILL.md
		subDir := filepath.Join(tmpDir, "subdir")
		require.NoError(t, os.Mkdir(subDir, 0755))

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skills, err := backend.List(ctx)
		assert.NoError(t, err)
		assert.Empty(t, skills)
	})

	t.Run("files in root directory are ignored", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create a SKILL.md in root (should be ignored, only subdirs are scanned)
		skillFile := filepath.Join(tmpDir, "SKILL.md")
		require.NoError(t, os.WriteFile(skillFile, []byte(`---
name: root-skill
description: Root skill
---
Content`), 0644))

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skills, err := backend.List(ctx)
		assert.NoError(t, err)
		assert.Empty(t, skills)
	})

	t.Run("valid skill directory returns skill", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create a skill directory with SKILL.md
		skillDir := filepath.Join(tmpDir, "my-skill")
		require.NoError(t, os.Mkdir(skillDir, 0755))
		skillFile := filepath.Join(skillDir, "SKILL.md")
		require.NoError(t, os.WriteFile(skillFile, []byte(`---
name: pdf-processing
description: Extract text and tables from PDF files, fill forms, merge documents.
license: Apache-2.0
metadata:
  author: example-org
  version: "1.0"
---
This is the skill content.`), 0644))

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skills, err := backend.List(ctx)
		assert.NoError(t, err)
		require.Len(t, skills, 1)
		assert.Equal(t, "pdf-processing", skills[0].Name)
		assert.Equal(t, "Extract text and tables from PDF files, fill forms, merge documents.", skills[0].Description)
	})

	t.Run("multiple skill directories returns all skills", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create first skill
		skill1Dir := filepath.Join(tmpDir, "skill-1")
		require.NoError(t, os.Mkdir(skill1Dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(skill1Dir, "SKILL.md"), []byte(`---
name: skill-1
description: First skill
---
Content 1`), 0644))

		// Create second skill
		skill2Dir := filepath.Join(tmpDir, "skill-2")
		require.NoError(t, os.Mkdir(skill2Dir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(skill2Dir, "SKILL.md"), []byte(`---
name: skill-2
description: Second skill
---
Content 2`), 0644))

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skills, err := backend.List(ctx)
		assert.NoError(t, err)
		assert.Len(t, skills, 2)

		// Check both skills exist (order may vary due to filesystem)
		names := []string{skills[0].Name, skills[1].Name}
		assert.Contains(t, names, "skill-1")
		assert.Contains(t, names, "skill-2")
	})

	t.Run("invalid SKILL.md returns error", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create a skill directory with invalid SKILL.md (no frontmatter)
		skillDir := filepath.Join(tmpDir, "invalid-skill")
		require.NoError(t, os.Mkdir(skillDir, 0755))
		skillFile := filepath.Join(skillDir, "SKILL.md")
		require.NoError(t, os.WriteFile(skillFile, []byte(`No frontmatter here`), 0644))

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skills, err := backend.List(ctx)
		assert.Error(t, err)
		assert.Nil(t, skills)
		assert.Contains(t, err.Error(), "failed to load skill")
	})
}

func TestLocalBackend_Get(t *testing.T) {
	ctx := context.Background()

	t.Run("skill not found returns error", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skill, err := backend.Get(ctx, "non-existent")
		assert.Error(t, err)
		assert.Empty(t, skill)
		assert.Contains(t, err.Error(), "skill not found")
	})

	t.Run("existing skill is returned", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create a skill directory
		skillDir := filepath.Join(tmpDir, "test-skill")
		require.NoError(t, os.Mkdir(skillDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: test-skill
description: Test skill description
---
Test content here.`), 0644))

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skill, err := backend.Get(ctx, "test-skill")
		assert.NoError(t, err)
		assert.Equal(t, "test-skill", skill.Name)
		assert.Equal(t, "Test skill description", skill.Description)
		assert.Equal(t, "Test content here.", skill.Content)
	})

	t.Run("get specific skill from multiple", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		// Create multiple skills
		for _, name := range []string{"alpha", "beta", "gamma"} {
			skillDir := filepath.Join(tmpDir, name)
			require.NoError(t, os.Mkdir(skillDir, 0755))
			require.NoError(t, os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(`---
name: `+name+`
description: Skill `+name+`
---
Content for `+name), 0644))
		}

		backend, err := NewLocalBackend(&LocalBackendConfig{BaseDir: tmpDir})
		require.NoError(t, err)

		skill, err := backend.Get(ctx, "beta")
		assert.NoError(t, err)
		assert.Equal(t, "beta", skill.Name)
		assert.Equal(t, "Skill beta", skill.Description)
		assert.Equal(t, "Content for beta", skill.Content)
	})
}

func TestParseFrontmatter(t *testing.T) {
	t.Run("valid frontmatter", func(t *testing.T) {
		data := `---
name: test
description: test description
---
This is the content.`

		fm, content, err := parseFrontmatter(data)
		assert.NoError(t, err)
		assert.Equal(t, "name: test\ndescription: test description", fm)
		assert.Equal(t, "This is the content.", content)
	})

	t.Run("frontmatter with multiline content", func(t *testing.T) {
		data := `---
name: test
---
Line 1
Line 2
Line 3`

		fm, content, err := parseFrontmatter(data)
		assert.NoError(t, err)
		assert.Equal(t, "name: test", fm)
		assert.Equal(t, "Line 1\nLine 2\nLine 3", content)
	})

	t.Run("frontmatter with leading/trailing whitespace", func(t *testing.T) {
		data := `  
---
name: test
---
Content  `

		fm, content, err := parseFrontmatter(data)
		assert.NoError(t, err)
		assert.Equal(t, "name: test", fm)
		// Note: parseFrontmatter trims trailing whitespace from input data
		assert.Equal(t, "Content", content)
	})

	t.Run("missing opening delimiter returns error", func(t *testing.T) {
		data := `name: test
---
Content`

		fm, content, err := parseFrontmatter(data)
		assert.Error(t, err)
		assert.Empty(t, fm)
		assert.Empty(t, content)
		assert.Contains(t, err.Error(), "does not start with frontmatter delimiter")
	})

	t.Run("missing closing delimiter returns error", func(t *testing.T) {
		data := `---
name: test
Content without closing`

		fm, content, err := parseFrontmatter(data)
		assert.Error(t, err)
		assert.Empty(t, fm)
		assert.Empty(t, content)
		assert.Contains(t, err.Error(), "closing delimiter not found")
	})

	t.Run("empty frontmatter", func(t *testing.T) {
		data := `---
---
Content only`

		fm, content, err := parseFrontmatter(data)
		assert.NoError(t, err)
		assert.Empty(t, fm)
		assert.Equal(t, "Content only", content)
	})

	t.Run("empty content", func(t *testing.T) {
		data := `---
name: test
---`

		fm, content, err := parseFrontmatter(data)
		assert.NoError(t, err)
		assert.Equal(t, "name: test", fm)
		assert.Empty(t, content)
	})

	t.Run("content with --- inside", func(t *testing.T) {
		data := `---
name: test
---
Content with --- in the middle`

		fm, content, err := parseFrontmatter(data)
		assert.NoError(t, err)
		assert.Equal(t, "name: test", fm)
		assert.Equal(t, "Content with --- in the middle", content)
	})
}

func TestLoadSkillFromFile(t *testing.T) {
	t.Run("valid skill file", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		skillFile := filepath.Join(tmpDir, "SKILL.md")
		require.NoError(t, os.WriteFile(skillFile, []byte(`---
name: file-skill
description: Skill from file
---
File skill content.`), 0644))

		backend := &LocalBackend{baseDir: tmpDir}
		skill, err := backend.loadSkillFromFile(skillFile)
		assert.NoError(t, err)
		assert.Equal(t, "file-skill", skill.Name)
		assert.Equal(t, "Skill from file", skill.Description)
		assert.Equal(t, "File skill content.", skill.Content)

		// Verify BaseDirectory is set correctly
		absDir, _ := filepath.Abs(tmpDir)
		assert.Equal(t, absDir, skill.BaseDirectory)
	})

	t.Run("non-existent file returns error", func(t *testing.T) {
		backend := &LocalBackend{baseDir: "/tmp"}
		skill, err := backend.loadSkillFromFile("/path/to/nonexistent/SKILL.md")
		assert.Error(t, err)
		assert.Empty(t, skill)
		assert.Contains(t, err.Error(), "failed to read file")
	})

	t.Run("invalid yaml in frontmatter returns error", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		skillFile := filepath.Join(tmpDir, "SKILL.md")
		require.NoError(t, os.WriteFile(skillFile, []byte(`---
name: [invalid yaml
---
Content`), 0644))

		backend := &LocalBackend{baseDir: tmpDir}
		skill, err := backend.loadSkillFromFile(skillFile)
		assert.Error(t, err)
		assert.Empty(t, skill)
		assert.Contains(t, err.Error(), "failed to unmarshal frontmatter")
	})

	t.Run("content with extra whitespace is trimmed", func(t *testing.T) {
		tmpDir, err := os.MkdirTemp("", "skill-test-*")
		require.NoError(t, err)
		defer os.RemoveAll(tmpDir)

		skillFile := filepath.Join(tmpDir, "SKILL.md")
		require.NoError(t, os.WriteFile(skillFile, []byte(`---
name: trimmed-skill
description: desc
---

   Content with whitespace   

`), 0644))

		backend := &LocalBackend{baseDir: tmpDir}
		skill, err := backend.loadSkillFromFile(skillFile)
		assert.NoError(t, err)
		assert.Equal(t, "Content with whitespace", skill.Content)
	})
}
