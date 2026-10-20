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
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
)

func newPreloadBackend() *inMemoryBackend {
	return &inMemoryBackend{m: []Skill{
		{
			FrontMatter: FrontMatter{
				Name:        "name1",
				Description: "desc1",
			},
			Content:       "content1",
			BaseDirectory: "basedir1",
		},
		{
			FrontMatter: FrontMatter{
				Name:        "name2",
				Description: "desc2",
			},
			Content:       "content2",
			BaseDirectory: "basedir2",
		},
		{
			FrontMatter: FrontMatter{
				Name:        "fork-skill",
				Description: "desc3",
				Context:     ContextModeFork,
			},
			Content:       "content3",
			BaseDirectory: "basedir3",
		},
	}}
}

func TestPreloadSkills(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	mw, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"name1"},
	})
	assert.NoError(t, err)

	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	assert.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "# Preloaded Skills")
	assert.Contains(t, runCtx.Instruction, "name1")
	assert.Contains(t, runCtx.Instruction, "content1")
	assert.Contains(t, runCtx.Instruction, "basedir1")
	assert.NotContains(t, runCtx.Instruction, "content2")
}

func TestPreloadSkills_Multiple(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	mw, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"name1", "name2"},
	})
	assert.NoError(t, err)

	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	assert.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "content1")
	assert.Contains(t, runCtx.Instruction, "content2")
}

func TestPreloadSkills_Chinese(t *testing.T) {
	internal.SetLanguage(internal.LanguageChinese)
	defer internal.SetLanguage(internal.LanguageEnglish)

	ctx := context.Background()
	backend := newPreloadBackend()

	mw, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"name1"},
	})
	assert.NoError(t, err)

	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	assert.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "# 预加载 Skill")
	assert.Contains(t, runCtx.Instruction, "content1")
}

func TestPreloadSkills_UnknownSkill(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	_, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"missing"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to preload skill 'missing'")
}

func TestPreloadSkills_ForkSkill(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	_, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"fork-skill"},
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cannot be preloaded")
}

func TestPreloadSkills_ToolStillListsAllSkills(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	mw, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"name1"},
	})
	assert.NoError(t, err)

	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	assert.NoError(t, err)
	assert.Len(t, runCtx.Tools, 1)

	info, err := runCtx.Tools[0].Info(ctx)
	assert.NoError(t, err)
	assert.Contains(t, info.Desc, "name1")
	assert.Contains(t, info.Desc, "name2")
}

func TestPreloadSkills_DeprecatedNew(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	m, err := New(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"name1"},
	})
	assert.NoError(t, err)
	assert.Contains(t, m.AdditionalInstruction, "# Preloaded Skills")
	assert.Contains(t, m.AdditionalInstruction, "content1")
}

func TestPreloadSkills_CustomSystemPromptStillPreloads(t *testing.T) {
	ctx := context.Background()
	backend := newPreloadBackend()

	mw, err := NewMiddleware(ctx, &Config{
		Backend:       backend,
		PreloadSkills: []string{"name1"},
		CustomSystemPrompt: func(_ context.Context, _ string) string {
			return "custom prompt"
		},
	})
	assert.NoError(t, err)

	_, runCtx, err := mw.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	assert.NoError(t, err)
	assert.Contains(t, runCtx.Instruction, "custom prompt")
	assert.Contains(t, runCtx.Instruction, "content1")
}
