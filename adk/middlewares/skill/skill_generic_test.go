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
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// --- Generic mock types ---

type mockGenericModel[M adk.MessageType] struct {
	generateFunc func(ctx context.Context, input []M, opts ...model.Option) (M, error)
}

func (m *mockGenericModel[M]) Generate(ctx context.Context, input []M, opts ...model.Option) (M, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, input, opts...)
	}
	var zero M
	return zero, nil
}

func (m *mockGenericModel[M]) Stream(_ context.Context, _ []M, _ ...model.Option) (*schema.StreamReader[M], error) {
	return nil, nil
}

type mockGenericModelHub[M adk.MessageType] struct {
	models map[string]model.BaseModel[M]
}

func (h *mockGenericModelHub[M]) Get(_ context.Context, name string) (model.BaseModel[M], error) {
	m, ok := h.models[name]
	if !ok {
		return nil, assert.AnError
	}
	return m, nil
}

type mockGenericAgent[M adk.MessageType] struct {
	events []*adk.TypedAgentEvent[M]
	lastIn *adk.TypedAgentInput[M]
}

func (a *mockGenericAgent[M]) Name(_ context.Context) string        { return "mock-generic-agent" }
func (a *mockGenericAgent[M]) Description(_ context.Context) string { return "mock generic agent" }
func (a *mockGenericAgent[M]) Run(_ context.Context, in *adk.TypedAgentInput[M], _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.TypedAgentEvent[M]] {
	a.lastIn = in
	iter, gen := adk.NewAsyncIteratorPair[*adk.TypedAgentEvent[M]]()
	go func() {
		defer gen.Close()
		for _, e := range a.events {
			gen.Send(e)
		}
	}()
	return iter
}

type mockGenericAgentHub[M adk.MessageType] struct {
	agent    adk.TypedAgent[M]
	lastOpts *TypedAgentHubOptions[M]
}

func (h *mockGenericAgentHub[M]) Get(_ context.Context, _ string, opts *TypedAgentHubOptions[M]) (adk.TypedAgent[M], error) {
	h.lastOpts = opts
	return h.agent, nil
}

// --- Helper to build an assistant event for each message type ---

func assistantEvent[M adk.MessageType](text string) *adk.TypedAgentEvent[M] {
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		msg := schema.AssistantMessage(text, nil)
		return any(&adk.TypedAgentEvent[*schema.Message]{
			Output: &adk.TypedAgentOutput[*schema.Message]{
				MessageOutput: &adk.TypedMessageVariant[*schema.Message]{
					Message: msg,
				},
			},
		}).(*adk.TypedAgentEvent[M])
	case *schema.AgenticMessage:
		msg := &schema.AgenticMessage{
			Role: schema.AgenticRoleTypeAssistant,
			ContentBlocks: []*schema.ContentBlock{
				schema.NewContentBlock(&schema.AssistantGenText{Text: text}),
			},
		}
		return any(&adk.TypedAgentEvent[*schema.AgenticMessage]{
			Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{
				MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{
					Message: msg,
				},
			},
		}).(*adk.TypedAgentEvent[M])
	}
	panic("unreachable")
}

// --- Part 1: WrapModel test ---

func testWrapModel[M adk.MessageType](t *testing.T) {
	ctx := context.Background()
	mockModel := &mockGenericModel[M]{}
	hub := &mockGenericModelHub[M]{
		models: map[string]model.BaseModel[M]{
			"test-model": mockModel,
		},
	}

	mw, err := NewTypedMiddleware[M](ctx, &TypedConfig[M]{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: hub,
	})
	require.NoError(t, err)
	require.NotNil(t, mw)

	t.Run("nil ModelHub keeps base model", func(t *testing.T) {
		handler, err := NewTypedMiddleware[M](ctx, &TypedConfig[M]{
			Backend: &inMemoryBackend{m: []Skill{}},
		})
		require.NoError(t, err)
		h := handler.(*typedSkillHandler[M])
		base := &mockGenericModel[M]{}
		got, err := h.WrapModel(ctx, base, &adk.TypedModelContext[M]{})
		require.NoError(t, err)
		assert.Equal(t, base, got)
	})
}

// --- Part 2: Agent mode tests ---

func testAgentMode[M adk.MessageType](t *testing.T) {
	ctx := context.Background()

	t.Run("successful agent run", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				assistantEvent[M]("agent answer"),
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
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
		require.NoError(t, err)
		assert.Contains(t, result, "test-skill")
		assert.Contains(t, result, "agent answer")

		// Verify agent received a user message (fork mode, no history).
		require.NotNil(t, agent.lastIn)
		require.Len(t, agent.lastIn.Messages, 1)
	})

	t.Run("fork mode constructs user message", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				assistantEvent[M]("ok"),
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork},
					Content:       "c1",
					BaseDirectory: "/d",
				},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.NoError(t, err)
		require.NotNil(t, agent.lastIn)
		require.Len(t, agent.lastIn.Messages, 1)

		// Verify the message is a user-role message.
		msg := agent.lastIn.Messages[0]
		var zero M
		switch any(zero).(type) {
		case *schema.Message:
			m := any(msg).(*schema.Message)
			assert.Equal(t, schema.User, m.Role)
		case *schema.AgenticMessage:
			m := any(msg).(*schema.AgenticMessage)
			assert.Equal(t, schema.AgenticRoleTypeUser, m.Role)
		}
	})

	t.Run("custom buildForkMessages called", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				assistantEvent[M]("ok"),
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		var captured TypedSubAgentInput[M]
		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork},
					Content:       "c1",
					BaseDirectory: "/d",
				},
			}},
			toolName: "skill",
			agentHub: hub,
			buildForkMessages: func(_ context.Context, in TypedSubAgentInput[M]) ([]M, error) {
				captured = in
				// Build a simple user message.
				var zero M
				switch any(zero).(type) {
				case *schema.Message:
					return []M{any(schema.UserMessage("custom")).(M)}, nil
				case *schema.AgenticMessage:
					return []M{any(schema.UserAgenticMessage("custom")).(M)}, nil
				}
				return nil, nil
			},
		}

		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.NoError(t, err)
		assert.Equal(t, "s1", captured.Skill.Name)
		assert.Equal(t, ContextModeFork, captured.Mode)
	})

	t.Run("custom formatForkResult called", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				assistantEvent[M]("p1"),
				assistantEvent[M]("p2"),
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork},
					Content:       "c1",
					BaseDirectory: "/d",
				},
			}},
			toolName: "skill",
			agentHub: hub,
			formatForkResult: func(_ context.Context, in TypedSubAgentOutput[M]) (string, error) {
				assert.Equal(t, ContextModeFork, in.Mode)
				assert.Equal(t, []string{"p1", "p2"}, in.Results)
				assert.Len(t, in.Messages, 2)
				return "formatted:" + strings.Join(in.Results, ","), nil
			},
		}

		result, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.NoError(t, err)
		assert.Equal(t, "formatted:p1,p2", result)
	})

	t.Run("agent error event propagates", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				{Err: assert.AnError},
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork},
					Content:       "c1",
					BaseDirectory: "/d",
				},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to run agent event")
	})

	t.Run("multiple events concatenated", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				assistantEvent[M]("part1"),
				{Output: nil}, // skipped
				assistantEvent[M]("part2"),
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork},
					Content:       "c1",
					BaseDirectory: "/d",
				},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		result, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "part1")
		assert.Contains(t, result, "part2")
	})

	t.Run("model passed to agent hub", func(t *testing.T) {
		mdl := &mockGenericModel[M]{}
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{
				assistantEvent[M]("ok"),
			},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork, Model: "m1"},
					Content:       "c1",
					BaseDirectory: "/d",
				},
			}},
			toolName: "skill",
			agentHub: hub,
			modelHub: &mockGenericModelHub[M]{
				models: map[string]model.BaseModel[M]{"m1": mdl},
			},
		}

		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.NoError(t, err)
		require.NotNil(t, hub.lastOpts)
		assert.Equal(t, mdl, hub.lastOpts.Model)
	})

	t.Run("no AgentHub returns error", func(t *testing.T) {
		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1"},
			}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AgentHub is not configured")
	})

	t.Run("no ModelHub with model returns error", func(t *testing.T) {
		agent := &mockGenericAgent[M]{
			events: []*adk.TypedAgentEvent[M]{assistantEvent[M]("ok")},
		}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork, Model: "m1"}, Content: "c1"},
			}},
			toolName: "skill",
			agentHub: hub,
		}
		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ModelHub is not configured")
	})

	t.Run("empty content events still produce result", func(t *testing.T) {
		var emptyEvent *adk.TypedAgentEvent[M]
		var zero M
		switch any(zero).(type) {
		case *schema.Message:
			msg := schema.AssistantMessage("", nil)
			emptyEvent = any(&adk.TypedAgentEvent[*schema.Message]{
				Output: &adk.TypedAgentOutput[*schema.Message]{
					MessageOutput: &adk.TypedMessageVariant[*schema.Message]{
						Message: msg,
					},
				},
			}).(*adk.TypedAgentEvent[M])
		case *schema.AgenticMessage:
			msg := &schema.AgenticMessage{
				Role:          schema.AgenticRoleTypeAssistant,
				ContentBlocks: []*schema.ContentBlock{},
			}
			emptyEvent = any(&adk.TypedAgentEvent[*schema.AgenticMessage]{
				Output: &adk.TypedAgentOutput[*schema.AgenticMessage]{
					MessageOutput: &adk.TypedMessageVariant[*schema.AgenticMessage]{
						Message: msg,
					},
				},
			}).(*adk.TypedAgentEvent[M])
		}

		agent := &mockGenericAgent[M]{events: []*adk.TypedAgentEvent[M]{emptyEvent}}
		hub := &mockGenericAgentHub[M]{agent: agent}

		st := &typedSkillTool[M]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		result, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		require.NoError(t, err)
		assert.Contains(t, result, "s1")
	})
}

// --- Top-level test ---

func TestSkillGeneric(t *testing.T) {
	t.Run("Message", func(t *testing.T) {
		t.Run("WrapModel", testWrapModel[*schema.Message])
		t.Run("AgentMode", testAgentMode[*schema.Message])
	})
	t.Run("AgenticMessage", func(t *testing.T) {
		t.Run("WrapModel", testWrapModel[*schema.AgenticMessage])
		t.Run("AgentMode", testAgentMode[*schema.AgenticMessage])
	})
}
