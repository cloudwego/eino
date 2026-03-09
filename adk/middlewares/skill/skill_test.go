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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
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
	internal.SetLanguage(internal.LanguageChinese)
	defer internal.SetLanguage(internal.LanguageEnglish)
	m, err = New(ctx, &Config{Backend: backend})
	assert.NoError(t, err)
	assert.Len(t, m.AdditionalTools, 1)

	to = m.AdditionalTools[0].(tool.InvokableTool)

	info, err = to.Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "skill", info.Name)
	desc = strings.TrimPrefix(info.Desc, toolDescriptionBaseChinese)
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

	result, err = to.InvokableRun(ctx, `{"skill": "name1"}`)
	assert.NoError(t, err)
	assert.Equal(t, `正在启动 Skill：name1
此 Skill 的目录：basedir1

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
}

// --- Mock types for NewMiddleware tests ---

type mockModel struct {
	model.ToolCallingChatModel
	name string
}

type mockModelHub struct {
	models map[string]model.ToolCallingChatModel
}

func (h *mockModelHub) Get(_ context.Context, name string) (model.ToolCallingChatModel, error) {
	m, ok := h.models[name]
	if !ok {
		return nil, fmt.Errorf("model not found: %s", name)
	}
	return m, nil
}

type mockAgent struct {
	events []*adk.AgentEvent
}

func (a *mockAgent) Name(_ context.Context) string        { return "mock-agent" }
func (a *mockAgent) Description(_ context.Context) string  { return "mock agent for testing" }
func (a *mockAgent) Run(_ context.Context, _ *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	iter, gen := adk.NewAsyncIteratorPair[*adk.AgentEvent]()
	go func() {
		defer gen.Close()
		for _, e := range a.events {
			gen.Send(e)
		}
	}()
	return iter
}

type mockAgentHub struct {
	agents     map[string]adk.Agent
	lastOpts   *AgentHubOptions
	defaultAgent adk.Agent
}

func (h *mockAgentHub) Get(_ context.Context, name string, opts *AgentHubOptions) (adk.Agent, error) {
	h.lastOpts = opts
	if name == "" && h.defaultAgent != nil {
		return h.defaultAgent, nil
	}
	a, ok := h.agents[name]
	if !ok {
		return nil, fmt.Errorf("agent not found: %s", name)
	}
	return a, nil
}

type errorBackend struct {
	listErr error
	getErr  error
}

func (b *errorBackend) List(_ context.Context) ([]FrontMatter, error) {
	return nil, b.listErr
}
func (b *errorBackend) Get(_ context.Context, _ string) (Skill, error) {
	return Skill{}, b.getErr
}

// --- NewMiddleware tests ---

func TestNewMiddleware(t *testing.T) {
	ctx := context.Background()

	t.Run("nil config returns error", func(t *testing.T) {
		handler, err := NewMiddleware(ctx, nil)
		assert.Nil(t, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "config is required")
	})

	t.Run("nil backend returns error", func(t *testing.T) {
		handler, err := NewMiddleware(ctx, &Config{})
		assert.Nil(t, handler)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "backend is required")
	})

	t.Run("valid config succeeds", func(t *testing.T) {
		backend := &inMemoryBackend{m: []Skill{}}
		handler, err := NewMiddleware(ctx, &Config{Backend: backend})
		assert.NoError(t, err)
		assert.NotNil(t, handler)
	})

	t.Run("custom tool name", func(t *testing.T) {
		backend := &inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "s1", Description: "d1"}, Content: "c1"},
		}}
		name := "load_skill"
		handler, err := NewMiddleware(ctx, &Config{Backend: backend, SkillToolName: &name})
		require.NoError(t, err)

		h := handler.(*skillHandler)
		assert.Contains(t, h.instruction, "'load_skill'")
		assert.Equal(t, "load_skill", h.tool.toolName)
	})

	t.Run("custom system prompt", func(t *testing.T) {
		backend := &inMemoryBackend{m: []Skill{}}
		handler, err := NewMiddleware(ctx, &Config{
			Backend: backend,
			CustomSystemPrompt: func(_ context.Context, toolName string) string {
				return "custom prompt for " + toolName
			},
		})
		require.NoError(t, err)

		h := handler.(*skillHandler)
		assert.Equal(t, "custom prompt for skill", h.instruction)
	})

	t.Run("custom tool description", func(t *testing.T) {
		backend := &inMemoryBackend{m: []Skill{
			{FrontMatter: FrontMatter{Name: "s1", Description: "d1"}, Content: "c1"},
		}}
		handler, err := NewMiddleware(ctx, &Config{
			Backend: backend,
			CustomToolDescription: func(_ context.Context, skills []FrontMatter) string {
				return fmt.Sprintf("custom desc with %d skills", len(skills))
			},
		})
		require.NoError(t, err)

		h := handler.(*skillHandler)
		info, err := h.tool.Info(ctx)
		require.NoError(t, err)
		assert.Equal(t, "custom desc with 1 skills", info.Desc)
	})
}

func TestBeforeAgent(t *testing.T) {
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{
		{FrontMatter: FrontMatter{Name: "s1", Description: "d1"}, Content: "c1"},
	}}
	handler, err := NewMiddleware(ctx, &Config{Backend: backend})
	require.NoError(t, err)

	runCtx := &adk.ChatModelAgentContext{
		Instruction: "base instruction",
		Tools:       []tool.BaseTool{},
	}
	_, newRunCtx, err := handler.BeforeAgent(ctx, runCtx)
	assert.NoError(t, err)
	assert.Contains(t, newRunCtx.Instruction, "base instruction")
	assert.Contains(t, newRunCtx.Instruction, "Skills System")
	assert.Len(t, newRunCtx.Tools, 1)

	// verify the added tool is the skill tool
	info, err := newRunCtx.Tools[0].Info(ctx)
	assert.NoError(t, err)
	assert.Equal(t, "skill", info.Name)
}

func TestSkillToolInfo(t *testing.T) {
	ctx := context.Background()

	t.Run("list error propagates", func(t *testing.T) {
		st := &skillTool{
			b:        &errorBackend{listErr: errors.New("list failed")},
			toolName: "skill",
		}
		info, err := st.Info(ctx)
		assert.Nil(t, info)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "list failed")
	})

	t.Run("description contains all skills", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
				{FrontMatter: FrontMatter{Name: "beta", Description: "desc-beta"}},
			}},
			toolName: "skill",
		}
		info, err := st.Info(ctx)
		require.NoError(t, err)
		assert.Contains(t, info.Desc, "alpha")
		assert.Contains(t, info.Desc, "desc-alpha")
		assert.Contains(t, info.Desc, "beta")
		assert.Contains(t, info.Desc, "desc-beta")
	})
}

func TestInvokableRun_InlineMode(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid json returns error", func(t *testing.T) {
		st := &skillTool{
			b:        &inMemoryBackend{m: []Skill{}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, "not json")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal")
	})

	t.Run("skill not found returns error", func(t *testing.T) {
		st := &skillTool{
			b:        &inMemoryBackend{m: []Skill{}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, `{"skill": "nonexistent"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get skill")
	})

	t.Run("inline mode returns skill content", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "pdf", Description: "PDF processing"},
					Content:       "Process PDF files here",
					BaseDirectory: "/skills/pdf",
				},
			}},
			toolName: "skill",
		}
		result, err := st.InvokableRun(ctx, `{"skill": "pdf"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "pdf")
		assert.Contains(t, result, "/skills/pdf")
		assert.Contains(t, result, "Process PDF files here")
	})
}

func TestInvokableRun_AgentMode(t *testing.T) {
	ctx := context.Background()

	t.Run("fork mode without AgentHub returns error", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1"},
			}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "AgentHub is not configured")
	})

	t.Run("fork_with_context mode without AgentHub returns error", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeForkWithContext}, Content: "c1"},
			}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "AgentHub is not configured")
	})

	t.Run("model specified without ModelHub returns error", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork, Model: "gpt-4"}, Content: "c1"},
			}},
			toolName: "skill",
			agentHub: &mockAgentHub{},
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "ModelHub is not configured")
	})

	t.Run("model not found in ModelHub returns error", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork, Model: "gpt-4"}, Content: "c1"},
			}},
			toolName: "skill",
			agentHub: &mockAgentHub{},
			modelHub: &mockModelHub{models: map[string]model.ToolCallingChatModel{}},
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get model")
	})

	t.Run("agent not found in AgentHub returns error", func(t *testing.T) {
		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork, Agent: "nonexistent"}, Content: "c1"},
			}},
			toolName: "skill",
			agentHub: &mockAgentHub{agents: map[string]adk.Agent{}},
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get agent")
	})

	t.Run("fork mode runs agent and returns result", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("agent response", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "test-skill", Context: ContextModeFork},
					Content:       "skill content",
					BaseDirectory: "/skills/test",
				},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		result, err := st.InvokableRun(ctx, `{"skill": "test-skill"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "test-skill")
		assert.Contains(t, result, "agent response")
		assert.Contains(t, result, "completed")
		// verify no model was passed in opts
		assert.NotNil(t, hub.lastOpts)
		assert.Nil(t, hub.lastOpts.Model)
	})

	t.Run("fork mode with model passes model to AgentHub", func(t *testing.T) {
		m := &mockModel{name: "test-model"}
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("response", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork, Model: "test-model"},
					Content:       "c1",
					BaseDirectory: "/skills/s1",
				},
			}},
			toolName: "skill",
			agentHub: hub,
			modelHub: &mockModelHub{models: map[string]model.ToolCallingChatModel{"test-model": m}},
		}

		result, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "s1")
		// verify model was passed
		assert.NotNil(t, hub.lastOpts)
		assert.Equal(t, m, hub.lastOpts.Model)
	})

	t.Run("agent returns multiple events", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("part1", nil),
						},
					},
				},
				{Output: nil}, // nil output should be skipped
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("part2", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		result, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "part1")
		assert.Contains(t, result, "part2")
	})

	t.Run("agent returns empty content events", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &skillTool{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		result, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.NoError(t, err)
		// result should contain skill name but no extra content
		assert.Contains(t, result, "s1")
	})
}
