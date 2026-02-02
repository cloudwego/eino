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

// Package skill provides the skill middleware, types, and a local filesystem backend.
package skill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type ContextMode string

const (
	ContextModeFork ContextMode = "fork"
)

type FrontMatter struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Context     ContextMode `yaml:"context"`
	Agent       string      `yaml:"agent"`
	Model       string      `yaml:"model"`
}

type Skill struct {
	FrontMatter
	Content       string
	BaseDirectory string
}

type Backend interface {
	List(ctx context.Context) ([]FrontMatter, error)
	Get(ctx context.Context, name string) (Skill, error)
}

type AgentFactory func(ctx context.Context, m model.ToolCallingChatModel) (adk.Agent, error)

type AgentHub interface {
	Get(ctx context.Context, name string) (AgentFactory, error)
}

type ModelHub interface {
	Get(ctx context.Context, name string) (model.ToolCallingChatModel, error)
}

// Config is the configuration for the skill middleware.
type Config struct {
	// Backend is the backend for retrieving skills.
	Backend Backend
	// SkillToolName is the custom name for the skill tool. If nil, the default name "skill" is used.
	SkillToolName *string
	// Deprecated: Use adk.SetLanguage(adk.LanguageChinese) instead to enable Chinese prompts globally.
	// This field will be removed in a future version.
	UseChinese bool
	// AgentHub provides agent factories for fork mode execution.
	// When a skill specifies context:fork, the agent factory is retrieved from this hub.
	// Required for fork mode skills.
	AgentHub AgentHub
	// ModelHub provides model instances for skills that specify a model in frontmatter.
	// If nil, skills with model specification will return an error.
	ModelHub ModelHub
	// DefaultAgentName is used when context:fork is specified but no agent name is provided in the skill.
	// The agent factory will be retrieved from AgentHub using this name.
	DefaultAgentName string
}

func NewHandler(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}

	name := toolName
	if config.SkillToolName != nil {
		name = *config.SkillToolName
	}

	return &skillHandler{
		instruction: buildSystemPrompt(name, config.UseChinese),
		tool: &skillTool{
			b:                config.Backend,
			toolName:         name,
			useChinese:       config.UseChinese,
			agentHub:         config.AgentHub,
			modelHub:         config.ModelHub,
			defaultAgentName: config.DefaultAgentName,
		},
	}, nil
}

type skillHandler struct {
	*adk.BaseChatModelAgentMiddleware
	instruction string
	tool        tool.BaseTool
}

func (h *skillHandler) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	runCtx.Instruction = runCtx.Instruction + "\n" + h.instruction
	runCtx.Tools = append(runCtx.Tools, h.tool)
	return ctx, runCtx, nil
}

// New creates a new skill middleware.
// It provides a tool for the agent to use skills.
//
// Deprecated: Use NewHandler instead. New does not support fork mode execution
// because AgentMiddleware cannot save message history for fork mode.
func New(ctx context.Context, config *Config) (adk.AgentMiddleware, error) {
	if config == nil {
		return adk.AgentMiddleware{}, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return adk.AgentMiddleware{}, fmt.Errorf("backend is required")
	}

	name := toolName
	if config.SkillToolName != nil {
		name = *config.SkillToolName
	}

	sp, err := buildSystemPrompt(name, config.UseChinese)
	if err != nil {
		return adk.AgentMiddleware{}, err
	}

	return adk.AgentMiddleware{
		AdditionalInstruction: sp,
		AdditionalTools:       []tool.BaseTool{&skillTool{b: config.Backend, toolName: name, useChinese: config.UseChinese}},
	}, nil
}

func buildSystemPrompt(skillToolName string, useChinese bool) (string, error) {
	prompt, err := internal.SelectPrompt(internal.I18nPrompts{
		English: systemPrompt,
		Chinese: systemPromptChinese,
	})
	if err != nil {
		return "", err
	}
	if useChinese {
		prompt = systemPromptChinese
	}
	return pyfmt.Fmt(prompt, map[string]string{
		"tool_name": skillToolName,
	})
}

type skillTool struct {
	b                Backend
	toolName         string
	useChinese       bool
	agentHub         AgentHub
	modelHub         ModelHub
	defaultAgentName string
}

type descriptionTemplateHelper struct {
	Matters []FrontMatter
}

func (s *skillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	skills, err := s.b.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}

	desc, err := renderToolDescription(skills)
	if err != nil {
		return nil, fmt.Errorf("failed to render skill tool description: %w", err)
	}

	descBase, err := internal.SelectPrompt(internal.I18nPrompts{
		English: toolDescriptionBase,
		Chinese: toolDescriptionBaseChinese,
	})
	if err != nil {
		return nil, err
	}
	paramDesc, err := internal.SelectPrompt(internal.I18nPrompts{
		English: "The skill name (no arguments). E.g., \"pdf\" or \"xlsx\"",
		Chinese: "Skill 名称（无需其他参数）。例如：\"pdf\" 或 \"xlsx\"",
	})
	if err != nil {
		return nil, err
	}

	return &schema.ToolInfo{
		Name: s.toolName,
		Desc: descBase + desc,
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"skill": {
				Type:     schema.String,
				Desc:     paramDesc,
				Required: true,
			},
		}),
	}, nil
}

type inputArguments struct {
	Skill string `json:"skill"`
}

func (s *skillTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	args := &inputArguments{}
	err := json.Unmarshal([]byte(argumentsInJSON), args)
	if err != nil {
		return "", fmt.Errorf("failed to unmarshal arguments: %w", err)
	}
	skill, err := s.b.Get(ctx, args.Skill)
	if err != nil {
		return "", fmt.Errorf("failed to get skill: %w", err)
	}

	if skill.Context == ContextModeFork {
		return s.runForkMode(ctx, skill)
	}

	return s.buildSkillResult(skill)
}

func (s *skillTool) buildSkillResult(skill Skill) (string, error) {
	resultFmt, err := internal.SelectPrompt(internal.I18nPrompts{
		English: toolResult,
		Chinese: toolResultChinese,
	})
	if err != nil {
		return "", err
	}
	contentFmt, err := internal.SelectPrompt(internal.I18nPrompts{
		English: userContent,
		Chinese: userContentChinese,
	})
	if err != nil {
		return "", err
	}

	return fmt.Sprintf(resultFmt, skill.Name) + fmt.Sprintf(contentFmt, skill.BaseDirectory, skill.Content), nil
}

func (s *skillTool) runForkMode(ctx context.Context, skill Skill) (string, error) {
	var m model.ToolCallingChatModel
	var err error

	if skill.Model != "" {
		if s.modelHub == nil {
			return "", fmt.Errorf("skill '%s' requires model '%s' but ModelHub is not configured", skill.Name, skill.Model)
		}
		m, err = s.modelHub.Get(ctx, skill.Model)
		if err != nil {
			return "", fmt.Errorf("failed to get model '%s' from ModelHub: %w", skill.Model, err)
		}
	}

	if s.agentHub == nil {
		return "", fmt.Errorf("skill '%s' requires context:fork but AgentHub is not configured", skill.Name)
	}

	agentName := skill.Agent
	if agentName == "" {
		agentName = s.defaultAgentName
	}
	if agentName == "" {
		return "", fmt.Errorf("skill '%s' requires context:fork but no agent name is specified (neither in skill frontmatter nor DefaultAgentName)", skill.Name)
	}

	agentFactory, err := s.agentHub.Get(ctx, agentName)
	if err != nil {
		return "", fmt.Errorf("failed to get agent '%s' from AgentHub: %w", agentName, err)
	}

	agent, err := agentFactory(ctx, m)
	if err != nil {
		return "", fmt.Errorf("failed to create agent for skill '%s': %w", skill.Name, err)
	}

	messages, err := s.getMessagesFromState(ctx)
	if err != nil {
		return "", fmt.Errorf("failed to get messages from state: %w", err)
	}

	toolCallID := compose.GetToolCallID(ctx)
	skillContent := s.buildSkillResult(skill)
	messages = append(messages, schema.ToolMessage(skillContent, toolCallID))

	input := &adk.AgentInput{
		Messages:        messages,
		EnableStreaming: false,
	}

	iter := agent.Run(ctx, input)

	var results []string
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event == nil {
			continue
		}
		if event.Output != nil && event.Output.MessageOutput != nil {
			msgOutput := event.Output.MessageOutput
			if !msgOutput.IsStreaming && msgOutput.Message.Content != "" {
				results = append(results, msgOutput.Message.Content)
			}
		}
	}

	resultFmt := forkResultFormat
	if s.useChinese {
		resultFmt = forkResultFormatChinese
	}

	return fmt.Sprintf(resultFmt, skill.Name, strings.Join(results, "\n")), nil
}

func (s *skillTool) getMessagesFromState(ctx context.Context) ([]adk.Message, error) {
	var messages []adk.Message
	err := compose.ProcessState(ctx, func(_ context.Context, st *adk.State) error {
		messages = make([]adk.Message, len(st.Messages))
		copy(messages, st.Messages)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("failed to process state: %w", err)
	}
	return messages, nil
}

func renderToolDescription(matters []FrontMatter) (string, error) {
	tpl, err := template.New("skills").Parse(toolDescriptionTemplate)
	if err != nil {
		return "", err
	}

	var buf bytes.Buffer
	err = tpl.Execute(&buf, descriptionTemplateHelper{Matters: matters})
	if err != nil {
		return "", err
	}

	return buf.String(), nil
}
