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
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"

	"github.com/eino-contrib/jsonschema"
	"github.com/slongfield/pyfmt"
	orderedmap "github.com/wk8/go-ordered-map/v2"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ContextMode defines the execution mode of a skill
type ContextMode string

const (
	// ContextModeInline is the default mode, skill content is returned directly to the current agent
	ContextModeInline ContextMode = ""
	// ContextModeFork creates a new sub-agent without parent history
	ContextModeFork ContextMode = "fork"
	// ContextModeForkWithContext creates a new sub-agent with parent history
	ContextModeForkWithContext ContextMode = "fork_with_context"
)

// FrontMatter defines the YAML frontmatter schema parsed from a SKILL.md file.
type FrontMatter struct {
	Name        string      `yaml:"name"`
	Description string      `yaml:"description"`
	Context     ContextMode `yaml:"context"`
	Agent       string      `yaml:"agent"`
	Model       string      `yaml:"model"`
}

// Skill represents a skill loaded from a backend.
type Skill struct {
	FrontMatter
	Content       string
	BaseDirectory string
}

// Backend loads skills and provides metadata for tool description rendering.
type Backend interface {
	List(ctx context.Context) ([]FrontMatter, error)
	Get(ctx context.Context, name string) (Skill, error)
}

// AgentHubOptions contains options passed to AgentHub.Get when creating an agent for skill execution.
type AgentHubOptions struct {
	// Model is the resolved model instance when a skill specifies a "model" field in frontmatter.
	// nil means the skill did not specify a model override; implementations should use their default.
	Model model.ToolCallingChatModel
}

// AgentHub provides agent instances for context mode (fork/fork_with_context) execution.
type AgentHub interface {
	// Get returns an Agent by name. When name is empty, implementations should return a default agent.
	// The opts parameter carries skill-level overrides (e.g., model) resolved by the framework.
	Get(ctx context.Context, name string, opts *AgentHubOptions) (adk.Agent, error)
}

// ModelHub resolves model instances by name for skills that specify a "model" field in frontmatter.
type ModelHub interface {
	Get(ctx context.Context, name string) (model.ToolCallingChatModel, error)
}

// SystemPromptFunc is a function that returns a custom system prompt.
// The toolName parameter is the name of the skill tool (default: "skill").
type SystemPromptFunc func(ctx context.Context, toolName string) string

// ToolDescriptionFunc is a function that returns a custom tool description.
// The skills parameter contains all available skill front matters.
type ToolDescriptionFunc func(ctx context.Context, skills []FrontMatter) string

// ForkMessageInput contains input data for CustomForkMessages.
type ForkMessageInput struct {
	Skill        Skill
	RawArguments string
	SkillContent string
	History      []adk.Message
	ToolCallID   string
}

// ForkResultInput contains input data for CustomForkResultPrompts.
type ForkResultInput struct {
	Skill        Skill
	RawArguments string
	Results      []string
}

// ForkMessagesFunc customizes the message list passed to sub-agents in fork modes.
type ForkMessagesFunc func(ctx context.Context, in ForkMessageInput) ([]adk.Message, error)

// ForkResultPromptsFunc customizes the final aggregated result returned from sub-agent execution.
type ForkResultPromptsFunc func(ctx context.Context, in ForkResultInput) (adk.I18nPrompts, error)

// SkillContentInput contains input data for CustomToolParamsHandler.SkillContentFunc.
type SkillContentInput struct {
	Skill        Skill
	RawArguments string
}

// CustomToolParamsHandler allows extending the tool schema and generating customized skill content.
type CustomToolParamsHandler struct {
	// AdditionToolParamsFunc appends extra tool parameters to the default schema.
	// It can also override the default "skill" parameter's description.
	// optional
	AdditionToolParamsFunc func(ctx context.Context) *schema.ParamsOneOf
	// SkillContentFunc customizes the skill content generated for this invocation.
	// RawArguments contains the original tool call arguments in JSON form.
	// required when CustomToolParamsHandler is set
	SkillContentFunc func(ctx context.Context, in SkillContentInput) (adk.I18nPrompts, error)
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
	// AgentHub provides agent instances for context mode (fork/fork_with_context) execution.
	// Required when skills use "context: fork" or "context: fork_with_context" in frontmatter.
	// The agent factory is retrieved by agent name (skill.Agent) from this hub.
	// When skill.Agent is empty, AgentHub.Get is called with an empty string,
	// allowing the hub implementation to return a default agent.
	AgentHub AgentHub
	// ModelHub provides model instances for skills that specify a "model" field in frontmatter.
	// Used in two scenarios:
	//   - With context mode (fork/fork_with_context): The model is passed to the AgentHub
	//   - Without context mode (inline): The model becomes active for subsequent ChatModel requests
	// If nil, skills with model specification will be ignored in inline mode,
	// or return an error in context mode.
	ModelHub ModelHub

	// CustomSystemPrompt allows customizing the system prompt injected into the agent.
	// If nil, the default system prompt is used.
	// The function receives the skill tool name as a parameter.
	CustomSystemPrompt SystemPromptFunc
	// CustomToolDescription allows customizing the tool description for the skill tool.
	// If nil, the default tool description is used.
	// The function receives all available skill front matters as a parameter.
	CustomToolDescription ToolDescriptionFunc

	// CustomToolParamsHandler configures custom tool parameters and skill content rendering.
	// When set, SkillContentFunc must be non-nil.
	// optional
	CustomToolParamsHandler *CustomToolParamsHandler

	// CustomForkMessages customizes the messages passed to the forked sub-agent.
	// When nil, fork_with_context appends the resolved skill content as a ToolMessage to history,
	// and fork passes the resolved skill content as a single UserMessage.
	// optional
	CustomForkMessages ForkMessagesFunc
	// CustomForkResultPrompts customizes the final text returned from the forked sub-agent results.
	// When nil, assistant message contents emitted by the sub-agent are concatenated and returned
	// in a default formatted string.
	// optional
	CustomForkResultPrompts ForkResultPromptsFunc
}

// NewMiddleware creates a new skill middleware handler for ChatModelAgent.
//
// The handler provides a skill tool that allows agents to load and execute skills
// defined in SKILL.md files. Skills can run in different modes based on their
// frontmatter configuration:
//
//   - Inline mode (default): Skill content is returned directly as tool result
//   - Fork mode (context: fork): Forks a new agent with a clean context, discarding message history
//   - Fork with context mode (context: fork_with_context): Forks a new agent carrying over message history
//
// Example usage:
//
//	handler, err := skill.NewMiddleware(ctx, &skill.Config{
//	    Backend:  backend,
//	    AgentHub: myAgentHub,
//	    ModelHub: myModelHub,
//	})
//	if err != nil {
//	    return err
//	}
//
//	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
//	    // ...
//	    Middlewares: []adk.ChatModelAgentMiddleware{handler},
//	})
func NewMiddleware(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	if config == nil {
		return nil, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return nil, fmt.Errorf("backend is required")
	}
	if config.CustomToolParamsHandler != nil {
		if config.CustomToolParamsHandler.SkillContentFunc == nil {
			return nil, fmt.Errorf("custom tool params handler is incomplete")
		}
	}

	name := toolName
	if config.SkillToolName != nil {
		name = *config.SkillToolName
	}

	var instruction string
	if config.CustomSystemPrompt != nil {
		instruction = config.CustomSystemPrompt(ctx, name)
	} else {
		var err error
		instruction, err = buildSystemPrompt(name, config.UseChinese)
		if err != nil {
			return nil, err
		}
	}

	return &skillHandler{
		instruction: instruction,
		tool: &skillTool{
			b:                       config.Backend,
			toolName:                name,
			useChinese:              config.UseChinese,
			agentHub:                config.AgentHub,
			modelHub:                config.ModelHub,
			customToolDescription:   config.CustomToolDescription,
			customToolParamsHandler: config.CustomToolParamsHandler,
			customForkMessages:      config.CustomForkMessages,
			customForkResult:        config.CustomForkResultPrompts,
		},
	}, nil
}

type skillHandler struct {
	*adk.BaseChatModelAgentMiddleware
	instruction string
	tool        *skillTool
}

func (h *skillHandler) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext) (context.Context, *adk.ChatModelAgentContext, error) {
	runCtx.Instruction = runCtx.Instruction + "\n" + h.instruction
	runCtx.Tools = append(runCtx.Tools, h.tool)
	return ctx, runCtx, nil
}

func (h *skillHandler) WrapModel(ctx context.Context, m model.BaseChatModel, mc *adk.ModelContext) (model.BaseChatModel, error) {
	if h.tool.modelHub == nil {
		return m, nil
	}
	modelName, found, err := adk.GetRunLocalValue(ctx, activeModelKey)
	if err != nil {
		return nil, fmt.Errorf("failed to get active model from run local value: %w", err)
	}
	if !found {
		return m, nil
	}
	name, ok := modelName.(string)
	if !ok || name == "" {
		return m, nil
	}
	newModel, err := h.tool.modelHub.Get(ctx, name)
	if err != nil {
		return nil, fmt.Errorf("failed to get model '%s' from ModelHub: %w", name, err)
	}
	return newModel, nil
}

const activeModelKey = "__skill_active_model__"

// New creates a new skill middleware.
// It provides a tool for the agent to use skills.
//
// Deprecated: Use NewMiddleware instead. New does not support fork mode execution
// because AgentMiddleware cannot save message history for fork mode.
func New(ctx context.Context, config *Config) (adk.AgentMiddleware, error) {
	if config == nil {
		return adk.AgentMiddleware{}, fmt.Errorf("config is required")
	}
	if config.Backend == nil {
		return adk.AgentMiddleware{}, fmt.Errorf("backend is required")
	}
	if config.CustomToolParamsHandler != nil {
		if config.CustomToolParamsHandler.SkillContentFunc == nil {
			return adk.AgentMiddleware{}, fmt.Errorf("custom tool params handler is incomplete")
		}
	}

	name := toolName
	if config.SkillToolName != nil {
		name = *config.SkillToolName
	}

	var sp string
	if config.CustomSystemPrompt != nil {
		sp = config.CustomSystemPrompt(ctx, name)
	} else {
		var err error
		sp, err = buildSystemPrompt(name, config.UseChinese)
		if err != nil {
			return adk.AgentMiddleware{}, err
		}
	}

	return adk.AgentMiddleware{
		AdditionalInstruction: sp,
		AdditionalTools: []tool.BaseTool{&skillTool{
			b:                       config.Backend,
			toolName:                name,
			useChinese:              config.UseChinese,
			customToolDescription:   config.CustomToolDescription,
			customToolParamsHandler: config.CustomToolParamsHandler,
			customForkMessages:      config.CustomForkMessages,
			customForkResult:        config.CustomForkResultPrompts,
		}},
	}, nil
}

func buildSystemPrompt(skillToolName string, useChinese bool) (string, error) {
	var prompt string
	if useChinese {
		prompt = systemPromptChinese
	} else {
		prompt = internal.SelectPrompt(internal.I18nPrompts{
			English: systemPrompt,
			Chinese: systemPromptChinese,
		})
	}
	return pyfmt.Fmt(prompt, map[string]string{
		"tool_name": skillToolName,
	})
}

type skillTool struct {
	b                       Backend
	toolName                string
	useChinese              bool
	agentHub                AgentHub
	modelHub                ModelHub
	customToolDescription   ToolDescriptionFunc
	customToolParamsHandler *CustomToolParamsHandler
	customForkMessages      ForkMessagesFunc
	customForkResult        ForkResultPromptsFunc
}

type descriptionTemplateHelper struct {
	Matters []FrontMatter
}

func (s *skillTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	skills, err := s.b.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}

	var fullDesc string
	if s.customToolDescription != nil {
		fullDesc = s.customToolDescription(ctx, skills)
	} else {
		desc, err := renderToolDescription(skills)
		if err != nil {
			return nil, fmt.Errorf("failed to render skill tool description: %w", err)
		}

		descBase := internal.SelectPrompt(internal.I18nPrompts{
			English: toolDescriptionBase,
			Chinese: toolDescriptionBaseChinese,
		})
		fullDesc = descBase + desc
	}

	return &schema.ToolInfo{
		Name:        s.toolName,
		Desc:        fullDesc,
		ParamsOneOf: s.buildParamsOneOf(ctx),
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

	switch skill.Context {
	case ContextModeForkWithContext:
		return s.runAgentMode(ctx, skill, true, argumentsInJSON)
	case ContextModeFork:
		return s.runAgentMode(ctx, skill, false, argumentsInJSON)
	default:
		if skill.Model != "" {
			s.setActiveModel(ctx, skill.Model)
		}
		return s.buildSkillResult(ctx, skill, argumentsInJSON)
	}
}

func (s *skillTool) setActiveModel(ctx context.Context, modelName string) {
	_ = adk.SetRunLocalValue(ctx, activeModelKey, modelName)
}

func defaultToolParams() map[string]*schema.ParameterInfo {
	skillParamDesc := internal.SelectPrompt(internal.I18nPrompts{
		English: "The skill name (no arguments). E.g., \"pdf\" or \"xlsx\"",
		Chinese: "Skill 名称（无需其他参数）。例如：\"pdf\" 或 \"xlsx\"",
	})
	return map[string]*schema.ParameterInfo{
		"skill": {
			Type:     schema.String,
			Desc:     skillParamDesc,
			Required: true,
		},
	}
}

func (s *skillTool) buildParamsOneOf(ctx context.Context) *schema.ParamsOneOf {
	base := schema.NewParamsOneOfByParams(defaultToolParams())
	if s.customToolParamsHandler == nil || s.customToolParamsHandler.AdditionToolParamsFunc == nil {
		return base
	}

	custom := s.customToolParamsHandler.AdditionToolParamsFunc(ctx)
	if custom == nil {
		return base
	}

	js, err := custom.ToJSONSchema()
	if err != nil || js == nil {
		return base
	}
	if js.Properties == nil {
		js.Properties = orderedmap.New[string, *jsonschema.Schema]()
	}

	desc := internal.SelectPrompt(internal.I18nPrompts{
		English: "The skill name (no arguments). E.g., \"pdf\" or \"xlsx\"",
		Chinese: "Skill 名称（无需其他参数）。例如：\"pdf\" 或 \"xlsx\"",
	})
	if skillSchema, ok := js.Properties.Get("skill"); ok && skillSchema != nil && skillSchema.Description != "" {
		desc = skillSchema.Description
	}

	js.Properties.Set("skill", &jsonschema.Schema{
		Type:        string(schema.String),
		Description: desc,
	})

	js.Required = append(js.Required, "skill")
	js.Required = uniqStrings(js.Required)

	return schema.NewParamsOneOfByJSONSchema(js)
}

func uniqStrings(in []string) []string {
	if len(in) == 0 {
		return in
	}
	sort.Strings(in)
	out := in[:0]
	var last string
	for i, s := range in {
		if i == 0 || s != last {
			out = append(out, s)
			last = s
		}
	}
	return out
}

func (s *skillTool) buildSkillResult(ctx context.Context, skill Skill, rawArguments string) (string, error) {
	if s.customToolParamsHandler == nil {
		return s.defaultSkillContent(skill), nil
	}
	prompts, err := s.customToolParamsHandler.SkillContentFunc(ctx, SkillContentInput{
		Skill:        skill,
		RawArguments: rawArguments,
	})
	if err != nil {
		return "", fmt.Errorf("failed to build skill result: %w", err)
	}
	return internal.SelectPrompt(prompts), nil
}

func (s *skillTool) defaultSkillContent(skill Skill) string {
	resultFmt := internal.SelectPrompt(internal.I18nPrompts{
		English: toolResult,
		Chinese: toolResultChinese,
	})
	contentFmt := internal.SelectPrompt(internal.I18nPrompts{
		English: userContent,
		Chinese: userContentChinese,
	})

	return fmt.Sprintf(resultFmt, skill.Name) + fmt.Sprintf(contentFmt, skill.BaseDirectory, skill.Content)
}

func (s *skillTool) runAgentMode(ctx context.Context, skill Skill, forkHistory bool, rawArguments string) (string, error) {
	if s.agentHub == nil {
		return "", fmt.Errorf("skill '%s' requires context:%s but AgentHub is not configured", skill.Name, skill.Context)
	}

	opts := &AgentHubOptions{}
	if skill.Model != "" {
		if s.modelHub == nil {
			return "", fmt.Errorf("skill '%s' requires model '%s' but ModelHub is not configured", skill.Name, skill.Model)
		}
		m, err := s.modelHub.Get(ctx, skill.Model)
		if err != nil {
			return "", fmt.Errorf("failed to get model '%s' from ModelHub: %w", skill.Model, err)
		}
		opts.Model = m
	}

	agent, err := s.agentHub.Get(ctx, skill.Agent, opts)
	if err != nil {
		return "", fmt.Errorf("failed to get agent '%s' from AgentHub: %w", skill.Agent, err)
	}

	var messages []adk.Message
	skillContent, err := s.buildSkillResult(ctx, skill, rawArguments)
	if err != nil {
		return "", fmt.Errorf("failed to build skill result: %w", err)
	}

	var history []adk.Message
	var toolCallID string
	if forkHistory {
		history, err = s.getMessagesFromState(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get messages from state: %w", err)
		}
		toolCallID = compose.GetToolCallID(ctx)
	}

	if s.customForkMessages != nil {
		messages, err = s.customForkMessages(ctx, ForkMessageInput{
			Skill:        skill,
			RawArguments: rawArguments,
			SkillContent: skillContent,
			History:      history,
			ToolCallID:   toolCallID,
		})
		if err != nil {
			return "", fmt.Errorf("failed to build fork messages: %w", err)
		}
	} else {
		if forkHistory {
			messages = append(history, schema.ToolMessage(skillContent, toolCallID))
		} else {
			messages = []adk.Message{schema.UserMessage(skillContent)}
		}
	}

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

		if event.Err != nil {
			return "", fmt.Errorf("failed to run agent event: %w", event.Err)
		}

		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}

		msg, msgErr := event.Output.MessageOutput.GetMessage()
		if msgErr != nil {
			return "", fmt.Errorf("failed to get message from event: %w", msgErr)
		}

		if msg != nil && msg.Content != "" {
			results = append(results, msg.Content)
		}
	}

	if s.customForkResult != nil {
		prompts, err := s.customForkResult(ctx, ForkResultInput{
			Skill:        skill,
			RawArguments: rawArguments,
			Results:      results,
		})
		if err != nil {
			return "", err
		}
		return internal.SelectPrompt(prompts), nil
	}

	resultFmt := internal.SelectPrompt(internal.I18nPrompts{
		English: subAgentResultFormat,
		Chinese: subAgentResultFormatChinese,
	})

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
