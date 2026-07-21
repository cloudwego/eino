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
	"encoding/json"
	"errors"
	"fmt"
	"io"
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
	// The available-skills list is no longer embedded in the description; it is injected
	// as a mid-conversation system message. The description is now just the base text.
	assert.Equal(t, toolDescriptionBase, info.Desc)

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
	assert.Equal(t, toolDescriptionBaseChinese, info.Desc)

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

func TestBuildParamsOneOf_CustomParams(t *testing.T) {
	internal.SetLanguage(internal.LanguageEnglish)
	ctx := context.Background()

	st := &typedSkillTool[*schema.Message]{
		customToolParams: func(context.Context, map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
			return map[string]*schema.ParameterInfo{
				"foo": {
					Type:     schema.String,
					Desc:     "foo desc",
					Required: true,
				},
				"bar": {
					Type:     schema.Integer,
					Desc:     "bar desc",
					Required: false,
				},
				"skill": {
					Type:     schema.String,
					Desc:     "custom skill desc",
					Required: false,
				},
			}, nil
		},
	}

	oneOf, err := st.buildParamsOneOf(ctx)
	require.NoError(t, err)
	js, err := oneOf.ToJSONSchema()
	require.NoError(t, err)
	require.NotNil(t, js)
	require.NotNil(t, js.Properties)

	skillSchema, ok := js.Properties.Get("skill")
	require.True(t, ok)
	require.NotNil(t, skillSchema)
	assert.Equal(t, string(schema.String), skillSchema.Type)
	assert.Equal(t, "custom skill desc", skillSchema.Description)

	_, ok = js.Properties.Get("foo")
	assert.True(t, ok)
	_, ok = js.Properties.Get("bar")
	assert.True(t, ok)

	assert.Equal(t, []string{"foo", "skill"}, js.Required)
}

func TestBuildParamsOneOf_CustomParamsNilFallsBackToDefault(t *testing.T) {
	ctx := context.Background()
	st := &typedSkillTool[*schema.Message]{
		customToolParams: func(context.Context, map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
			return nil, nil
		},
	}

	oneOf, err := st.buildParamsOneOf(ctx)
	require.NoError(t, err)
	js, err := oneOf.ToJSONSchema()
	require.NoError(t, err)
	require.NotNil(t, js)
	require.NotNil(t, js.Properties)
	_, ok := js.Properties.Get("skill")
	require.True(t, ok)
	assert.Contains(t, js.Required, "skill")
}

// --- Mock types for NewMiddleware tests ---

type mockModel struct {
	model.BaseModel[*schema.Message]
	name string
}

type mockModelHub struct {
	models map[string]model.BaseModel[*schema.Message]
}

func (h *mockModelHub) Get(_ context.Context, name string) (model.BaseModel[*schema.Message], error) {
	m, ok := h.models[name]
	if !ok {
		return nil, fmt.Errorf("model not found: %s", name)
	}
	return m, nil
}

type fakeToolCallingModel struct {
	id    string
	calls int
}

func (m *fakeToolCallingModel) Generate(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	m.calls++
	return schema.AssistantMessage(m.id, nil), nil
}

func (m *fakeToolCallingModel) Stream(_ context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, io.EOF
}

func (m *fakeToolCallingModel) WithTools(_ []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

type runLocalSetterHandler struct {
	*adk.BaseChatModelAgentMiddleware
	key string
	val any
}

func (h *runLocalSetterHandler) BeforeModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	if err := adk.SetRunLocalValue(ctx, h.key, h.val); err != nil {
		return nil, nil, err
	}
	return ctx, state, nil
}

type stateMessagesCaptureHandler struct {
	*adk.BaseChatModelAgentMiddleware
	st       *typedSkillTool[*schema.Message]
	captured []adk.Message
}

func (h *stateMessagesCaptureHandler) AfterModelRewriteState(ctx context.Context, state *adk.ChatModelAgentState, _ *adk.ModelContext) (context.Context, *adk.ChatModelAgentState, error) {
	msgs, err := h.st.getMessagesFromState(ctx)
	if err != nil {
		return nil, nil, err
	}
	h.captured = msgs
	return ctx, state, nil
}

type mockAgent struct {
	events []*adk.AgentEvent
	lastIn *adk.AgentInput
}

func (a *mockAgent) Name(_ context.Context) string        { return "mock-agent" }
func (a *mockAgent) Description(_ context.Context) string { return "mock agent for testing" }
func (a *mockAgent) Run(_ context.Context, in *adk.AgentInput, _ ...adk.AgentRunOption) *adk.AsyncIterator[*adk.AgentEvent] {
	a.lastIn = in
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
	agents       map[string]adk.Agent
	lastOpts     *AgentHubOptions
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

	t.Run("custom tool params error surfaces in Info", func(t *testing.T) {
		backend := &inMemoryBackend{m: []Skill{}}
		handler, err := NewMiddleware(ctx, &Config{
			Backend: backend,
			CustomToolParams: func(context.Context, map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
				return nil, errors.New("bad params")
			},
		})
		require.NoError(t, err)
		h := handler.(*typedSkillHandler[*schema.Message])
		_, err = h.tool.Info(ctx)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "failed to build skill tool params")
		assert.Contains(t, err.Error(), "bad params")
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

		h := handler.(*typedSkillHandler[*schema.Message])
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

		h := handler.(*typedSkillHandler[*schema.Message])
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

		h := handler.(*typedSkillHandler[*schema.Message])
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

	runCtx := &adk.ChatModelAgentContext[*schema.Message]{
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

func TestWrapModel_SwitchesModelWhenRunLocalIsSet(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	other := &fakeToolCallingModel{id: "other"}

	handler, err := NewMiddleware(ctx, &Config{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{"other": other}},
	})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "t",
		Description: "t",
		Model:       base,
		Handlers: []adk.ChatModelAgentMiddleware{
			&runLocalSetterHandler{key: activeModelKey, val: "other"},
			handler,
		},
		MaxIterations: 1,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	var last string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg, err := ev.Output.MessageOutput.GetMessage()
		require.NoError(t, err)
		if msg != nil {
			last = msg.Content
		}
	}

	assert.Equal(t, "other", last)
	assert.Equal(t, 0, base.calls)
	assert.Equal(t, 1, other.calls)
}

func TestWrapModel_OutsideAgentContextReturnsError(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	other := &fakeToolCallingModel{id: "other"}

	handler, err := NewMiddleware(ctx, &Config{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{"other": other}},
	})
	require.NoError(t, err)

	h := handler.(*typedSkillHandler[*schema.Message])
	_, err = h.WrapModel(ctx, base, &adk.ModelContext{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get active model from run local value")
}

func TestWrapModel_IgnoresNonStringRunLocalValue(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	other := &fakeToolCallingModel{id: "other"}

	handler, err := NewMiddleware(ctx, &Config{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{"other": other}},
	})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "t",
		Description: "t",
		Model:       base,
		Handlers: []adk.ChatModelAgentMiddleware{
			&runLocalSetterHandler{key: activeModelKey, val: 123},
			handler,
		},
		MaxIterations: 1,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	var last string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg, err := ev.Output.MessageOutput.GetMessage()
		require.NoError(t, err)
		if msg != nil {
			last = msg.Content
		}
	}

	assert.Equal(t, "base", last)
	assert.Equal(t, 1, base.calls)
	assert.Equal(t, 0, other.calls)
}

func TestWrapModel_ModelHubGetError(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}

	handler, err := NewMiddleware(ctx, &Config{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{}},
	})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "t",
		Description: "t",
		Model:       base,
		Handlers: []adk.ChatModelAgentMiddleware{
			&runLocalSetterHandler{key: activeModelKey, val: "missing"},
			handler,
		},
		MaxIterations: 1,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	var gotErr error
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		if ev.Err != nil {
			gotErr = ev.Err
			break
		}
	}
	require.Error(t, gotErr)
	assert.Contains(t, gotErr.Error(), "failed to get model")
	assert.Equal(t, 0, base.calls)
}

func TestWrapModel_ModelHubNilKeepsBase(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	handler, err := NewMiddleware(ctx, &Config{
		Backend: &inMemoryBackend{m: []Skill{}},
	})
	require.NoError(t, err)

	h := handler.(*typedSkillHandler[*schema.Message])
	m, err := h.WrapModel(ctx, base, &adk.ModelContext{})
	require.NoError(t, err)
	assert.Equal(t, base, m)
}

func TestWrapModel_RunLocalNotFoundKeepsBase(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	other := &fakeToolCallingModel{id: "other"}

	handler, err := NewMiddleware(ctx, &Config{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{"other": other}},
	})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "t",
		Description: "t",
		Model:       base,
		Handlers: []adk.ChatModelAgentMiddleware{
			handler,
		},
		MaxIterations: 1,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	var last string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg, err := ev.Output.MessageOutput.GetMessage()
		require.NoError(t, err)
		if msg != nil {
			last = msg.Content
		}
	}

	assert.Equal(t, "base", last)
	assert.Equal(t, 1, base.calls)
	assert.Equal(t, 0, other.calls)
}

func TestWrapModel_IgnoresEmptyStringRunLocalValue(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	other := &fakeToolCallingModel{id: "other"}

	handler, err := NewMiddleware(ctx, &Config{
		Backend:  &inMemoryBackend{m: []Skill{}},
		ModelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{"other": other}},
	})
	require.NoError(t, err)

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "t",
		Description: "t",
		Model:       base,
		Handlers: []adk.ChatModelAgentMiddleware{
			&runLocalSetterHandler{key: activeModelKey, val: ""},
			handler,
		},
		MaxIterations: 1,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	var last string
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
		if ev.Output == nil || ev.Output.MessageOutput == nil {
			continue
		}
		msg, err := ev.Output.MessageOutput.GetMessage()
		require.NoError(t, err)
		if msg != nil {
			last = msg.Content
		}
	}

	assert.Equal(t, "base", last)
	assert.Equal(t, 1, base.calls)
	assert.Equal(t, 0, other.calls)
}

func TestGetMessagesFromState_InAgentContext(t *testing.T) {
	ctx := context.Background()

	base := &fakeToolCallingModel{id: "base"}
	st := &typedSkillTool[*schema.Message]{}
	capture := &stateMessagesCaptureHandler{st: st}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        "t",
		Description: "t",
		Model:       base,
		Handlers: []adk.ChatModelAgentMiddleware{
			capture,
		},
		MaxIterations: 1,
	})
	require.NoError(t, err)

	iter := agent.Run(ctx, &adk.AgentInput{Messages: []adk.Message{schema.UserMessage("hi")}})
	for {
		ev, ok := iter.Next()
		if !ok {
			break
		}
		require.NoError(t, ev.Err)
	}

	require.NotNil(t, capture.captured)
	require.NotEmpty(t, capture.captured)
}

func TestSkillToolInfo(t *testing.T) {
	ctx := context.Background()

	t.Run("list error propagates", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b:        &errorBackend{listErr: errors.New("list failed")},
			toolName: "skill",
		}
		info, err := st.Info(ctx)
		assert.Nil(t, info)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "list failed")
	})

	t.Run("description contains all skills", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
				{FrontMatter: FrontMatter{Name: "beta", Description: "desc-beta"}},
			}},
			toolName: "skill",
		}
		info, err := st.Info(ctx)
		require.NoError(t, err)
		// The skill list is no longer embedded in the description; it is injected as a
		// mid-conversation system message by the handler's BeforeModelRewriteState.
		assert.NotContains(t, info.Desc, "desc-alpha")
		h := &typedSkillHandler[*schema.Message]{tool: st}
		state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: []*schema.Message{schema.UserMessage("hi")}}
		_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
		require.NoError(t, err)
		require.Len(t, ns.Messages, 2)
		section := ns.Messages[1].Content
		assert.Contains(t, section, "alpha")
		assert.Contains(t, section, "desc-alpha")
		assert.Contains(t, section, "beta")
		assert.Contains(t, section, "desc-beta")
	})

	t.Run("custom tool params is used", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
			}},
			toolName: "skill",
			customToolParams: func(_ context.Context, _ map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
				return map[string]*schema.ParameterInfo{
					"foo":   {Type: schema.String, Desc: "foo-desc", Required: true},
					"skill": {Type: schema.String, Desc: "custom-skill-desc", Required: false},
				}, nil
			},
		}
		info, err := st.Info(ctx)
		require.NoError(t, err)
		js, err := info.ParamsOneOf.ToJSONSchema()
		require.NoError(t, err)
		_, ok := js.Properties.Get("foo")
		require.True(t, ok)
		v, ok := js.Properties.Get("skill")
		require.True(t, ok)
		assert.Equal(t, "custom-skill-desc", v.Description)
		assert.Contains(t, js.Required, "skill")
		assert.Contains(t, js.Required, "foo")
	})
}

func TestInvokableRun_InlineMode(t *testing.T) {
	ctx := context.Background()

	t.Run("invalid json returns error", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b:        &inMemoryBackend{m: []Skill{}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, "not json")
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to unmarshal")
	})

	t.Run("skill not found returns error", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b:        &inMemoryBackend{m: []Skill{}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, `{"skill": "nonexistent"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get skill")
	})

	t.Run("inline mode returns skill content", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
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

	t.Run("inline mode with model triggers setActiveModel", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "pdf", Description: "PDF processing", Model: "m1"},
					Content:       "Process PDF files here",
					BaseDirectory: "/skills/pdf",
				},
			}},
			toolName: "skill",
		}
		result, err := st.InvokableRun(ctx, `{"skill": "pdf"}`)
		assert.NoError(t, err)
		assert.Contains(t, result, "pdf")
	})

	t.Run("custom skill content is used", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "pdf", Description: "PDF processing"},
					Content:       "Process PDF files here",
					BaseDirectory: "/skills/pdf",
				},
			}},
			toolName: "skill",
			buildContent: func(_ context.Context, _ Skill, rawArgs string) (string, error) {
				var raw map[string]any
				require.NoError(t, json.Unmarshal([]byte(rawArgs), &raw))
				assert.Equal(t, "pdf", raw["skill"])
				return "custom-content", nil
			},
		}
		result, err := st.InvokableRun(ctx, `{"skill":"pdf"}`)
		assert.NoError(t, err)
		assert.Equal(t, "custom-content", result)
	})

	t.Run("custom tool params with decoder is used", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "pdf", Description: "PDF processing"},
					Content:       "Process PDF files here",
					BaseDirectory: "/skills/pdf",
				},
			}},
			toolName: "skill",
			customToolParams: func(_ context.Context, _ map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error) {
				return map[string]*schema.ParameterInfo{
					"skill": {Type: schema.String, Desc: "custom", Required: false},
					"task":  {Type: schema.String, Desc: "custom", Required: false},
					"x":     {Type: schema.Integer, Desc: "custom", Required: false},
				}, nil
			},
			buildContent: func(_ context.Context, _ Skill, rawArgs string) (string, error) {
				var raw struct {
					Skill string `json:"skill"`
					Task  string `json:"task"`
					X     int    `json:"x"`
				}
				require.NoError(t, json.Unmarshal([]byte(rawArgs), &raw))
				assert.Equal(t, "pdf", raw.Skill)
				assert.Equal(t, "t", raw.Task)
				assert.Equal(t, 1, raw.X)
				return "decoded", nil
			},
		}
		result, err := st.InvokableRun(ctx, `{"skill":"pdf","task":"t","x":1}`)
		assert.NoError(t, err)
		assert.Equal(t, "decoded", result)
	})

	t.Run("custom skill content returns error", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "pdf", Description: "PDF processing"},
					Content:       "Process PDF files here",
					BaseDirectory: "/skills/pdf",
				},
			}},
			toolName: "skill",
			buildContent: func(context.Context, Skill, string) (string, error) {
				return "", errors.New("boom")
			},
		}
		_, err := st.InvokableRun(ctx, `{"skill":"pdf"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to build skill result")
	})
}

func TestInvokableRun_AgentMode(t *testing.T) {
	ctx := context.Background()

	t.Run("fork mode without AgentHub returns error", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
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
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeForkWithContext}, Content: "c1"},
			}},
			toolName: "skill",
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "AgentHub is not configured")
	})

	t.Run("fork_with_context mode without state returns error", func(t *testing.T) {
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

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeForkWithContext}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get messages from state")
	})

	t.Run("model specified without ModelHub returns error", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
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
		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork, Model: "gpt-4"}, Content: "c1"},
			}},
			toolName: "skill",
			agentHub: &mockAgentHub{},
			modelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{}},
		}
		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to get model")
	})

	t.Run("agent not found in AgentHub returns error", func(t *testing.T) {
		st := &typedSkillTool[*schema.Message]{
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

		st := &typedSkillTool[*schema.Message]{
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

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{
					FrontMatter:   FrontMatter{Name: "s1", Context: ContextModeFork, Model: "test-model"},
					Content:       "c1",
					BaseDirectory: "/skills/s1",
				},
			}},
			toolName: "skill",
			agentHub: hub,
			modelHub: &mockModelHub{models: map[string]model.BaseModel[*schema.Message]{"test-model": m}},
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

		st := &typedSkillTool[*schema.Message]{
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

		st := &typedSkillTool[*schema.Message]{
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

	t.Run("agent event error returns error", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{Err: errors.New("boom")},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
		}

		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to run agent event")
	})

	t.Run("custom fork messages is used", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("ok", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
			buildForkMessages: func(_ context.Context, in SubAgentInput) ([]adk.Message, error) {
				assert.Equal(t, ContextModeFork, in.Mode)
				assert.Equal(t, "s1", in.Skill.Name)
				return []adk.Message{schema.UserMessage("custom")}, nil
			},
		}

		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.NoError(t, err)
		require.NotNil(t, agent.lastIn)
		require.Len(t, agent.lastIn.Messages, 1)
		msg := agent.lastIn.Messages[0]
		assert.Equal(t, "custom", msg.Content)
	})

	t.Run("custom fork messages returns error", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("ok", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
			buildForkMessages: func(context.Context, SubAgentInput) ([]adk.Message, error) {
				return nil, errors.New("build msg fail")
			},
		}

		_, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to build fork messages")
	})

	t.Run("custom fork result prompts from results is used", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("p1", nil),
						},
					},
				},
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("p2", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
			formatForkResult: func(_ context.Context, in SubAgentOutput) (string, error) {
				assert.Equal(t, ContextModeFork, in.Mode)
				assert.Equal(t, []string{"p1", "p2"}, in.Results)
				return "E:" + strings.Join(in.Results, ","), nil
			},
		}

		result, err := st.InvokableRun(ctx, `{"skill": "s1"}`)
		assert.NoError(t, err)
		assert.Equal(t, "E:p1,p2", result)
	})

	t.Run("custom fork result prompts returns error", func(t *testing.T) {
		agent := &mockAgent{
			events: []*adk.AgentEvent{
				{
					Output: &adk.AgentOutput{
						MessageOutput: &adk.MessageVariant{
							Message: schema.AssistantMessage("p1", nil),
						},
					},
				},
			},
		}
		hub := &mockAgentHub{defaultAgent: agent}

		st := &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "s1", Context: ContextModeFork}, Content: "c1", BaseDirectory: "/d"},
			}},
			toolName: "skill",
			agentHub: hub,
			formatForkResult: func(context.Context, SubAgentOutput) (string, error) {
				return "", errors.New("format fail")
			},
		}

		_, err := st.InvokableRun(ctx, `{"skill":"s1"}`)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "failed to format fork result")
		assert.Contains(t, err.Error(), "format fail")
	})
}

func TestNewTypedAgenticMessage(t *testing.T) {
	ctx := context.Background()
	mw, err := NewTyped(ctx, &TypedConfig[*schema.AgenticMessage]{
		Backend: &inMemoryBackend{m: []Skill{}},
	})
	assert.NoError(t, err)
	assert.NotNil(t, mw)

	var _ adk.TypedChatModelAgentMiddleware[*schema.AgenticMessage] = mw
}

func TestSkill_BeforeModelRewriteState_UpsertsReminderByExtra(t *testing.T) {
	ctx := context.Background()
	h := &typedSkillHandler[*schema.Message]{
		tool: &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
			}},
		},
	}

	state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: []*schema.Message{schema.UserMessage("hi")}}

	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	// A dedicated system reminder is appended at the end, identified by the skill
	// middleware's own Extra key.
	require.Len(t, ns.Messages, 2)
	reminder := ns.Messages[1]
	assert.Equal(t, schema.System, reminder.Role)
	assert.True(t, reminder.Extra[skillsReminderExtraKey].(bool))
	assert.Contains(t, reminder.Content, "alpha")
	assert.Contains(t, reminder.Content, "desc-alpha")

	// Repeated invocation with an unchanged skill list is idempotent: no second
	// reminder is appended.
	_, ns, err = h.BeforeModelRewriteState(ctx, ns, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	assert.Equal(t, reminder.Content, ns.Messages[1].Content)
}

// TestSkill_BeforeModelRewriteState_AppendsWhenSkillsChange verifies that when a
// skill is installed between invocations, the skill list changes and a fresh
// reminder is appended at the end (the stale reminder is left untouched so the
// model's KV cache prefix is preserved). The latest reminder supersedes it.
func TestSkill_BeforeModelRewriteState_AppendsWhenSkillsChange(t *testing.T) {
	ctx := context.Background()
	backend := &inMemoryBackend{m: []Skill{
		{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
	}}
	h := &typedSkillHandler[*schema.Message]{
		tool: &typedSkillTool[*schema.Message]{b: backend},
	}

	state := &adk.TypedChatModelAgentState[*schema.Message]{Messages: []*schema.Message{schema.UserMessage("hi")}}

	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	require.Len(t, ns.Messages, 2)
	first := ns.Messages[1]
	assert.Contains(t, first.Content, "alpha")
	assert.NotContains(t, first.Content, "beta")

	// Simulate installing a new skill between turns.
	backend.m = append(backend.m, Skill{FrontMatter: FrontMatter{Name: "beta", Description: "desc-beta"}})

	_, ns, err = h.BeforeModelRewriteState(ctx, ns, nil)
	require.NoError(t, err)
	// The stale reminder stays in place; a fresh one carrying only the changed
	// skill entry is appended at the end.
	require.Len(t, ns.Messages, 3)
	assert.Equal(t, first.Content, ns.Messages[1].Content, "stale reminder must be left untouched to preserve prefix cache")
	latest := ns.Messages[2]
	assert.Equal(t, schema.System, latest.Role)
	assert.True(t, latest.Extra[skillsReminderExtraKey].(bool))
	assert.Contains(t, latest.Content, availableSkillsPreamble)
	assert.NotContains(t, latest.Content, "alpha")
	assert.Contains(t, latest.Content, "beta")
	assert.Contains(t, latest.Content, "desc-beta")
}

// TestSkill_BeforeModelRewriteState_PreservesOtherMessages verifies that the
// skill middleware never mutates or removes existing messages — including a
// leading system message and reminders owned by other middlewares — and simply
// appends its own reminder at the end.
func TestSkill_BeforeModelRewriteState_PreservesOtherMessages(t *testing.T) {
	ctx := context.Background()
	h := &typedSkillHandler[*schema.Message]{
		tool: &typedSkillTool[*schema.Message]{
			b: &inMemoryBackend{m: []Skill{
				{FrontMatter: FrontMatter{Name: "alpha", Description: "desc-alpha"}},
			}},
		},
	}

	// A leading instruction system message plus another middleware's reminder,
	// guarded by its own dedicated key.
	const otherKey = "__eino_other_middleware_section__"
	instruction := schema.SystemMessage("base instruction")
	otherReminder := schema.SystemMessage("other middleware section")
	otherReminder.Extra = map[string]any{otherKey: true}
	state := &adk.TypedChatModelAgentState[*schema.Message]{
		Messages: []*schema.Message{instruction, schema.UserMessage("hi"), otherReminder},
	}

	_, ns, err := h.BeforeModelRewriteState(ctx, state, nil)
	require.NoError(t, err)
	// The three existing messages are preserved verbatim; the skill reminder is
	// appended at the end.
	require.Len(t, ns.Messages, 4)
	assert.Equal(t, "base instruction", ns.Messages[0].Content)
	assert.Equal(t, "other middleware section", ns.Messages[2].Content)
	assert.True(t, ns.Messages[2].Extra[otherKey].(bool))
	_, mutated := ns.Messages[2].Extra[skillsReminderExtraKey]
	assert.False(t, mutated, "the other middleware's reminder must not be tagged with the skill key")

	skillReminder := ns.Messages[3]
	assert.Equal(t, schema.System, skillReminder.Role)
	assert.True(t, skillReminder.Extra[skillsReminderExtraKey].(bool))
	assert.Contains(t, skillReminder.Content, "alpha")
	assert.Contains(t, skillReminder.Content, "desc-alpha")

	// Re-invocation is idempotent: the skill reminder is not appended twice.
	_, ns2, err := h.BeforeModelRewriteState(ctx, ns, nil)
	require.NoError(t, err)
	require.Len(t, ns2.Messages, 4)
	assert.Equal(t, skillReminder.Content, ns2.Messages[3].Content)
}
