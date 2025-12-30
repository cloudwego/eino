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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/tool"
)

type inMemoryBackend struct {
	m []Skill
}

func (i *inMemoryBackend) List(ctx context.Context) ([]FrontMatter, error) {
	matters := make([]FrontMatter, 0, len(i.m))
	for _, skill := range i.m {
		matters = append(matters, skill.FrontMatter)
	}
	return matters, nil
}

func (i *inMemoryBackend) Get(ctx context.Context, name string) (Skill, error) {
	for _, skill := range i.m {
		if skill.Name == name {
			return skill, nil
		}
	}
	return Skill{}, errors.New("skill not found")
}

func TestTool(t *testing.T) {
	backend := &inMemoryBackend{m: []Skill{
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
	}}

	ctx := context.Background()
	m, err := New(ctx, &Config{Backend: backend})
	assert.NoError(t, err)
	assert.Len(t, m.AdditionalTools, 1)

	to := m.AdditionalTools[0].(tool.InvokableTool)

	info, err := to.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "skill", info.Name)
	desc := strings.TrimPrefix(info.Desc, toolDescriptionBase)
	assert.Equal(t, `
<available_skills>
<skill>
<name>
name1
</name>
<description>
desc1
</description>
</skill>
<skill>
<name>
name2
</name>
<description>
desc2
</description>
</skill>
</available_skills>
`, desc)

	result, err := to.InvokableRun(ctx, `{"skill": "name1"}`)
	assert.NoError(t, err)
	assert.Equal(t, `Launching skill: name1
Base directory for this skill: basedir1

content1`, result)

	// chinese
	m, err = New(ctx, &Config{Backend: backend, UseChinese: true})
	assert.NoError(t, err)
	assert.Len(t, m.AdditionalTools, 1)

	to = m.AdditionalTools[0].(tool.InvokableTool)

	info, err = to.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "skill", info.Name)
	desc = strings.TrimPrefix(info.Desc, toolDescriptionBaseChinese)
	assert.Equal(t, `
<可用技能>
<技能>
<名称>
name1
</名称>
<描述>
desc1
</描述>
</技能>
<技能>
<名称>
name2
</名称>
<描述>
desc2
</描述>
</技能>
</可用技能>
`, desc)

	result, err = to.InvokableRun(ctx, `{"skill": "name1"}`)
	assert.NoError(t, err)
	assert.Equal(t, `正在启动技能：name1
此技能的目录：basedir1

content1`, result)
}

func TestSkillToolName(t *testing.T) {
	ctx := context.Background()

	// default
	m, err := New(ctx, &Config{Backend: &inMemoryBackend{m: []Skill{}}})
	assert.NoError(t, err)
	// instruction
	assert.Contains(t, m.AdditionalInstruction, "'skill'")
	// tool name
	info, err := m.AdditionalTools[0].Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "skill", info.Name)

	// customized
	name := "load_skill"
	m, err = New(ctx, &Config{Backend: &inMemoryBackend{m: []Skill{}}, SkillToolName: &name})
	assert.NoError(t, err)
	assert.Contains(t, m.AdditionalInstruction, "'load_skill'")
	info, err = m.AdditionalTools[0].Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "load_skill", info.Name)

	// chinese
	m, err = New(ctx, &Config{Backend: &inMemoryBackend{m: []Skill{}}, SkillToolName: &name, UseChinese: true})
	assert.NoError(t, err)
	assert.Contains(t, m.AdditionalInstruction, "'load_skill'")
	info, err = m.AdditionalTools[0].Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "load_skill", info.Name)
}
