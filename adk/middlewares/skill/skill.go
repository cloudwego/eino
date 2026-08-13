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
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/internal"
	"github.com/cloudwego/eino/adk/middlewares/internal/systemreminder"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ContextMode defines the execution mode of a skill.
type ContextMode string

const (
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
	// Content is the markdown body after the frontmatter contains the skill instructions of a SKILL.md file.
	Content string
	// BaseDirectory is the absolute directory path where the SKILL.md file is located (e.g., "/absolute/path/to/skills/my-skill").
	BaseDirectory string
}

// Backend loads skills and provides metadata for tool description rendering.
type Backend interface {
	List(ctx context.Context) ([]FrontMatter, error)
	Get(ctx context.Context, name string) (Skill, error)
}

// TypedAgentHubOptions contains options passed to TypedAgentHub.Get when creating an agent for skill execution.
type TypedAgentHubOptions[M adk.MessageType] struct {
	// Model is the resolved model instance when a skill specifies a "model" field in frontmatter.
	// nil means the skill did not specify a model override; implementations should use their default.
	Model model.BaseModel[M]
}

// AgentHubOptions is a backward-compatible alias for TypedAgentHubOptions instantiated with *schema.Message.
type AgentHubOptions = TypedAgentHubOptions[*schema.Message]

// TypedAgentHub provides agent instances for context mode (fork/fork_with_context) execution.
type TypedAgentHub[M adk.MessageType] interface {
	// Get returns an Agent by name. When name is empty, implementations should return a default agent.
	// The opts parameter carries skill-level overrides (e.g., model) resolved by the framework.
	Get(ctx context.Context, name string, opts *TypedAgentHubOptions[M]) (adk.TypedAgent[M], error)
}

// AgentHub is a backward-compatible alias for TypedAgentHub instantiated with *schema.Message.
type AgentHub = TypedAgentHub[*schema.Message]

// TypedModelHub resolves model instances by name for skills that specify a "model" field in frontmatter.
type TypedModelHub[M adk.MessageType] interface {
	Get(ctx context.Context, name string) (model.BaseModel[M], error)
}

// ModelHub is a backward-compatible alias for TypedModelHub instantiated with *schema.Message.
type ModelHub = TypedModelHub[*schema.Message]

// SystemPromptFunc is a function that returns a custom system prompt.
// The toolName parameter is the name of the skill tool (default: "skill").
type SystemPromptFunc func(ctx context.Context, toolName string) string

// ToolDescriptionFunc is a function that returns a custom tool description.
// The skills parameter contains all available skill front matters.
type ToolDescriptionFunc func(ctx context.Context, skills []FrontMatter) string

// TypedSubAgentInput contains the context available when building the sub-agent's
// initial messages in fork/fork_with_context mode.
type TypedSubAgentInput[M adk.MessageType] struct {
	Skill        Skill
	Mode         ContextMode
	RawArguments string
	SkillContent string
	History      []M
	ToolCallID   string
}

// SubAgentInput is a backward-compatible alias for TypedSubAgentInput instantiated with *schema.Message.
type SubAgentInput = TypedSubAgentInput[*schema.Message]

// TypedSubAgentOutput contains the sub-agent's execution results, available when
// formatting the final tool response.
type TypedSubAgentOutput[M adk.MessageType] struct {
	Skill        Skill
	Mode         ContextMode
	RawArguments string
	Messages     []M
	Results      []string
}

// SubAgentOutput is a backward-compatible alias for TypedSubAgentOutput instantiated with *schema.Message.
type SubAgentOutput = TypedSubAgentOutput[*schema.Message]

// TypedConfig is the configuration for the skill middleware.
type TypedConfig[M adk.MessageType] struct {
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
	AgentHub TypedAgentHub[M]
	// ModelHub provides model instances for skills that specify a "model" field in frontmatter.
	// Used in two scenarios:
	//   - With context mode (fork/fork_with_context): The model is passed to the AgentHub
	//   - Without context mode (inline): The model becomes active for subsequent ChatModel requests
	// If nil, skills with model specification will be ignored in inline mode,
	// or return an error in context mode.
	ModelHub TypedModelHub[M]

	// CustomSystemPrompt allows customizing the system prompt injected into the agent.
	// If nil, the default system prompt is used.
	// The function receives the skill tool name as a parameter.
	CustomSystemPrompt SystemPromptFunc
	// CustomToolDescription allows customizing the tool description for the skill tool.
	// If nil, the default tool description is used.
	// The function receives all available skill front matters as a parameter.
	CustomToolDescription ToolDescriptionFunc

	// CustomToolParams customizes tool parameters for the skill tool.
	// defaults is the default schema with only the required "skill" field.
	// optional
	CustomToolParams func(ctx context.Context, defaults map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error)

	// BuildContent customizes the skill content generated for this invocation.
	// rawArgs contains the original tool call arguments in JSON form.
	// optional
	BuildContent func(ctx context.Context, skill Skill, rawArgs string) (string, error)

	// BuildForkMessages customizes the messages passed to the forked sub-agent.
	// When nil, fork uses [UserMessage(skillContent)] and fork_with_context uses
	// [history..., ToolMessage(skillContent, toolCallID)].
	// optional
	BuildForkMessages func(ctx context.Context, in TypedSubAgentInput[M]) ([]M, error)

	// FormatForkResult customizes the final text returned from the forked sub-agent results.
	// When nil, assistant message contents emitted by the sub-agent are concatenated and returned
	// in a default formatted string.
	// optional
	FormatForkResult func(ctx context.Context, in TypedSubAgentOutput[M]) (string, error)
}

// Config is a backward-compatible alias for TypedConfig instantiated with *schema.Message.
type Config = TypedConfig[*schema.Message]

// NewTyped creates a generic skill middleware handler for TypedChatModelAgent.
//
// This is the generic constructor that supports both *schema.Message and *schema.AgenticMessage.
// For *schema.AgenticMessage, tool execution is message-type-independent; the model override
// via ModelHub only takes effect when M is *schema.Message (for other types it is a no-op).
//
// See NewMiddleware for full usage documentation.
func NewTyped[M adk.MessageType](ctx context.Context, config *TypedConfig[M]) (adk.TypedChatModelAgentMiddleware[M], error) {
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

	return &typedSkillHandler[M]{
		instruction: instruction,
		tool: &typedSkillTool[M]{
			b:                 config.Backend,
			toolName:          name,
			useChinese:        config.UseChinese,
			agentHub:          config.AgentHub,
			modelHub:          config.ModelHub,
			customToolDesc:    config.CustomToolDescription,
			customToolParams:  config.CustomToolParams,
			buildContent:      config.BuildContent,
			buildForkMessages: config.BuildForkMessages,
			formatForkResult:  config.FormatForkResult,
		},
	}, nil
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
//	    Handlers: []adk.ChatModelAgentMiddleware{handler},
//	})
func NewMiddleware(ctx context.Context, config *Config) (adk.ChatModelAgentMiddleware, error) {
	return NewTyped(ctx, config)
}

type typedSkillHandler[M adk.MessageType] struct {
	*adk.TypedBaseChatModelAgentMiddleware[M]
	instruction string
	tool        *typedSkillTool[M]
}

// BeforeAgent injects the skill tool and system prompt.
//
// It does NOT touch the available-skills reminder. The reminder is refreshed on the
// first model call (BeforeModelRewriteState), for two reasons: the add/change diff needs
// the previous reminder's digest, which lives in the full message history (not in
// runCtx.AgentInput.Messages, which holds only the current input); and run-local values
// are backed by the compose graph State, which does not exist yet here — BeforeAgent runs
// before the graph is invoked, so a SetRunLocalValue here is silently dropped.
func (h *typedSkillHandler[M]) BeforeAgent(ctx context.Context, runCtx *adk.ChatModelAgentContext[M]) (context.Context, *adk.ChatModelAgentContext[M], error) {
	runCtx.Instruction = runCtx.Instruction + "\n" + h.instruction
	runCtx.Tools = append(runCtx.Tools, h.tool)
	return ctx, runCtx, nil
}

// BeforeModelRewriteState refreshes the available-skills reminder on the first model call
// of a turn: it lists the current skills, diffs them against the previous reminder's
// digest — read from the FULL history (state.Messages) — and inserts a reminder advertising
// only the added/changed skills. A run-local mark makes this run at most once per agent Run:
// the first model call finds the mark absent, does the work, and sets it; later model calls
// in the same ReAct turn find it set and skip.
//
// The mark lives here, not in BeforeAgent, because run-local values are backed by the
// compose graph State, which is not built until the graph runs — after BeforeAgent. Set in
// BeforeAgent it would be silently dropped. Run-local state is fresh per Run (and restored
// across interrupt/resume), so the mark naturally scopes to one logical turn.
//
// List() runs here, not in BeforeAgent, because the diff also needs prior turns' reminders,
// which only state.Messages carries; BeforeAgent's runCtx.AgentInput.Messages holds just
// the current input.
//
// Design note: advertising only the delta and appending (rather than rewriting or
// removing earlier reminders) is deliberate. A per-change delta stays compact across a
// long conversation and leaves the already-cached prefix (KV cache) untouched; on
// conflicting system messages the model treats a later one as higher priority, so a
// newer delta overrides a stale earlier reminder. A turn with no added/changed skill
// inserts nothing (a pure removal does not trigger a reminder), so the prefix cache holds.
func (h *typedSkillHandler[M]) BeforeModelRewriteState(ctx context.Context, state *adk.TypedChatModelAgentState[M], _ *adk.TypedModelContext[M]) (context.Context, *adk.TypedChatModelAgentState[M], error) {
	if state == nil {
		return ctx, state, nil
	}
	// Align any reminders reconstructed from history with the configured role before the
	// model call; the fresh insert below is already built with that role.
	state.Messages = systemreminder.NormalizeReminderRoles(state.Messages)

	// Once per agent Run: the mark's presence means this turn already refreshed the
	// reminder, so later model calls in the same ReAct turn skip. Absent = new turn.
	if _, handled, _ := adk.GetRunLocalValue(ctx, skillsHandledKey); handled {
		return ctx, state, nil
	}
	// Mark before doing the work so a multi-call ReAct turn lists skills at most once.
	// A best-effort set failure only means a redundant List() on the next model call
	// (the history digest still dedups the insert), so the reminder stays correct.
	_ = adk.SetRunLocalValue(ctx, skillsHandledKey, true)

	skills, err := h.tool.b.List(ctx)
	if err != nil {
		log.Printf("skill middleware: failed to list skills for available-skills reminder, skipping: %v", err)
		return ctx, state, nil
	}
	if len(skills) == 0 {
		return ctx, state, nil
	}

	prev, _ := systemreminder.LatestExtra(state.Messages, skillsReminderExtraKey, skillsDigestExtraKey)
	prevSet := make(map[string]struct{}, len(toStringSlice(prev)))
	for _, d := range toStringSlice(prev) {
		prevSet[d] = struct{}{}
	}

	// A skill is advertised only when its digest is absent from the previous reminder — a
	// newly added skill, or one whose name/description (hence digest) changed. `digests`
	// is the full current snapshot, stored in the reminder's Extra so the next turn diffs
	// against the complete advertised set, not just this delta.
	changed := make([]FrontMatter, 0, len(skills))
	digests := make([]string, 0, len(skills))
	for _, sk := range skills {
		d := skillDigest(sk)
		digests = append(digests, d)
		if _, ok := prevSet[d]; ok {
			continue
		}
		changed = append(changed, FrontMatter{Name: sk.Name, Description: sk.Description})
	}
	if len(changed) == 0 {
		return ctx, state, nil
	}

	extra := map[string]any{skillsDigestExtraKey: digests}
	state.Messages = systemreminder.Insert(ctx, state.Messages, skillsReminderExtraKey, buildSkillsSectionFromEntries(changed), extra)
	return ctx, state, nil
}

const (
	// skillsReminderExtraKey tags a skill reminder message in history.
	skillsReminderExtraKey = "__eino_skill_available_skills__"
	// skillsDigestExtraKey stores, in a skill reminder's Extra, the JSON array of
	// MD5(name+description) digests of the skills it advertised, for cross-turn diffing.
	skillsDigestExtraKey = "__eino_skill_digests__"
	// skillsHandledKey marks (run-local) that the available-skills reminder has already been
	// refreshed this agent Run. The first model call of the turn finds it absent, does
	// List()+diff+insert, and sets it; later model calls in the same ReAct turn find it set
	// and skip, so List() runs at most once per turn. It lives in run-local (compose State)
	// rather than being set in BeforeAgent, where the State does not exist yet.
	skillsHandledKey = "__eino_skill_reminder_handled__"
)

// skillDigest returns the MD5(name+description) digest of a single skill.
func skillDigest(sk FrontMatter) string {
	sum := md5.Sum([]byte(sk.Name + "\x00" + sk.Description))
	return hex.EncodeToString(sum[:])
}

// toStringSlice coerces a value read back from message Extra (which may be []string
// or, after a JSON round-trip through session persistence, []any) into []string.
func toStringSlice(v any) []string {
	switch s := v.(type) {
	case []string:
		return s
	case []any:
		out := make([]string, 0, len(s))
		for _, e := range s {
			str, _ := e.(string)
			out = append(out, str)
		}
		return out
	}
	return nil
}

func buildSkillsSectionFromEntries(skills []FrontMatter) string {
	preamble := internal.SelectPrompt(internal.I18nPrompts{
		English: availableSkillsPreamble,
		Chinese: availableSkillsPreambleChinese,
	})
	var sb strings.Builder
	sb.WriteString(preamble)
	for _, sk := range skills {
		sb.WriteString(fmt.Sprintf("\n- %s: %s", sk.Name, sk.Description))
	}
	return sb.String()
}

func (h *typedSkillHandler[M]) WrapModel(ctx context.Context, m model.BaseModel[M], _ *adk.TypedModelContext[M]) (model.BaseModel[M], error) {
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
		AdditionalTools: []tool.BaseTool{&typedSkillTool[*schema.Message]{
			b:              config.Backend,
			toolName:       name,
			useChinese:     config.UseChinese,
			customToolDesc: config.CustomToolDescription,
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

type typedSkillTool[M adk.MessageType] struct {
	b        Backend
	toolName string

	useChinese bool
	agentHub   TypedAgentHub[M]
	modelHub   TypedModelHub[M]

	customToolDesc ToolDescriptionFunc

	customToolParams func(ctx context.Context, defaults map[string]*schema.ParameterInfo) (map[string]*schema.ParameterInfo, error)
	buildContent     func(ctx context.Context, skill Skill, rawArgs string) (string, error)

	buildForkMessages func(ctx context.Context, in TypedSubAgentInput[M]) ([]M, error)
	formatForkResult  func(ctx context.Context, in TypedSubAgentOutput[M]) (string, error)
}

func (s *typedSkillTool[M]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	skills, err := s.b.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to list skills: %w", err)
	}

	var fullDesc string
	if s.customToolDesc != nil {
		fullDesc = s.customToolDesc(ctx, skills)
	} else {
		// The available-skills list is no longer embedded in the tool description; it is
		// injected as a mid-conversation system reminder in BeforeAgent.
		fullDesc = internal.SelectPrompt(internal.I18nPrompts{
			English: toolDescriptionBase,
			Chinese: toolDescriptionBaseChinese,
		})
	}

	oneOf, err := s.buildParamsOneOf(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to build skill tool params: %w", err)
	}

	return &schema.ToolInfo{
		Name:        s.toolName,
		Desc:        fullDesc,
		ParamsOneOf: oneOf,
	}, nil
}

type inputArguments struct {
	Skill string `json:"skill"`
	Args  string `json:"args"`
}

func (s *typedSkillTool[M]) InvokableRun(ctx context.Context, argumentsInJSON string, _ ...tool.Option) (string, error) {
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

func (s *typedSkillTool[M]) setActiveModel(ctx context.Context, modelName string) {
	_ = adk.SetRunLocalValue(ctx, activeModelKey, modelName)
}

func defaultToolParams() map[string]*schema.ParameterInfo {
	skillParamDesc := internal.SelectPrompt(internal.I18nPrompts{
		English: "The skill name. E.g., \"pdf\" or \"xlsx\"",
		Chinese: "Skill 名称。例如：\"pdf\" 或 \"xlsx\"",
	})
	return map[string]*schema.ParameterInfo{
		"skill": {
			Type:     schema.String,
			Desc:     skillParamDesc,
			Required: true,
		},
		"args": {
			Type: schema.String,
			Desc: internal.SelectPrompt(internal.I18nPrompts{
				English: "Optional arguments for the skill",
				Chinese: "传给该 skill 的可选参数",
			}),
			Required: false,
		},
	}
}

func (s *typedSkillTool[M]) buildParamsOneOf(ctx context.Context) (*schema.ParamsOneOf, error) {
	defaults := defaultToolParams()
	if s.customToolParams == nil {
		return schema.NewParamsOneOfByParams(defaults), nil
	}

	params, err := s.customToolParams(ctx, defaults)
	if err != nil {
		return nil, err
	}
	if params == nil {
		params = defaults
	}

	if _, ok := params["skill"]; !ok {
		params["skill"] = defaults["skill"]
	}

	if p := params["skill"]; p != nil {
		p.Required = true
	}

	return schema.NewParamsOneOfByParams(params), nil
}

func (s *typedSkillTool[M]) buildSkillResult(ctx context.Context, skill Skill, rawArguments string) (string, error) {
	if s.buildContent == nil {
		args := &inputArguments{}
		_ = json.Unmarshal([]byte(rawArguments), args)
		return s.defaultSkillContent(skill, args.Args), nil
	}
	content, err := s.buildContent(ctx, skill, rawArguments)
	if err != nil {
		return "", fmt.Errorf("failed to build skill result: %w", err)
	}
	return content, nil
}

func (s *typedSkillTool[M]) defaultSkillContent(skill Skill, args string) string {
	resultFmt := internal.SelectPrompt(internal.I18nPrompts{
		English: toolResult,
		Chinese: toolResultChinese,
	})
	contentFmt := internal.SelectPrompt(internal.I18nPrompts{
		English: userContent,
		Chinese: userContentChinese,
	})

	content := fmt.Sprintf(resultFmt, skill.Name) + fmt.Sprintf(contentFmt, skill.BaseDirectory, skill.Content)
	if args != "" {
		content += fmt.Sprintf(internal.SelectPrompt(internal.I18nPrompts{
			English: argumentsSuffix,
			Chinese: argumentsSuffixChinese,
		}), args)
	}
	return content
}

func (s *typedSkillTool[M]) runAgentMode(ctx context.Context, skill Skill, forkHistory bool, rawArguments string) (string, error) {
	if s.agentHub == nil {
		return "", fmt.Errorf("skill '%s' requires context:%s but AgentHub is not configured", skill.Name, skill.Context)
	}

	opts := &TypedAgentHubOptions[M]{}
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

	var messages []M
	skillContent, err := s.buildSkillResult(ctx, skill, rawArguments)
	if err != nil {
		return "", fmt.Errorf("failed to build skill result: %w", err)
	}

	var history []M
	var toolCallID string
	if forkHistory {
		history, err = s.getMessagesFromState(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to get messages from state: %w", err)
		}
		toolCallID = compose.GetToolCallID(ctx)
	}

	if s.buildForkMessages != nil {
		messages, err = s.buildForkMessages(ctx, TypedSubAgentInput[M]{
			Skill:        skill,
			Mode:         skill.Context,
			RawArguments: rawArguments,
			SkillContent: skillContent,
			History:      history,
			ToolCallID:   toolCallID,
		})
		if err != nil {
			return "", fmt.Errorf("failed to build fork messages: %w", err)
		}
	} else {
		var zero M
		if forkHistory {
			var toolMsg M
			switch any(zero).(type) {
			case *schema.Message:
				toolMsg = any(schema.ToolMessage(skillContent, toolCallID)).(M)
			case *schema.AgenticMessage:
				toolMsg = any(&schema.AgenticMessage{
					Role: schema.AgenticRoleTypeUser,
					ContentBlocks: []*schema.ContentBlock{
						schema.NewContentBlock(&schema.FunctionToolResult{
							CallID: toolCallID,
							Name:   "",
							Content: []*schema.FunctionToolResultContentBlock{
								{Type: schema.FunctionToolResultContentBlockTypeText, Text: &schema.UserInputText{Text: skillContent}},
							},
						}),
					},
				}).(M)
			}
			messages = append(history, toolMsg)
		} else {
			var userMsg M
			switch any(zero).(type) {
			case *schema.Message:
				userMsg = any(schema.UserMessage(skillContent)).(M)
			case *schema.AgenticMessage:
				userMsg = any(schema.UserAgenticMessage(skillContent)).(M)
			}
			messages = []M{userMsg}
		}
	}

	input := &adk.TypedAgentInput[M]{
		Messages:        messages,
		EnableStreaming: false,
	}

	iter := agent.Run(ctx, input)

	var msgList []M
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

		if !isNilMessage(msg) {
			msgList = append(msgList, msg)
			var content string
			switch m := any(msg).(type) {
			case *schema.Message:
				content = m.Content
			case *schema.AgenticMessage:
				var parts []string
				for _, block := range m.ContentBlocks {
					if block == nil {
						continue
					}
					if block.AssistantGenText != nil {
						parts = append(parts, block.AssistantGenText.Text)
					}
				}
				content = strings.Join(parts, "\n")
			}
			if content != "" {
				results = append(results, content)
			}
		}
	}

	if s.formatForkResult != nil {
		out, err := s.formatForkResult(ctx, TypedSubAgentOutput[M]{
			Skill:        skill,
			Mode:         skill.Context,
			RawArguments: rawArguments,
			Messages:     msgList,
			Results:      results,
		})
		if err != nil {
			return "", fmt.Errorf("failed to format fork result: %w", err)
		}
		return out, nil
	}

	resultFmt := internal.SelectPrompt(internal.I18nPrompts{
		English: subAgentResultFormat,
		Chinese: subAgentResultFormatChinese,
	})

	return fmt.Sprintf(resultFmt, skill.Name, strings.Join(results, "\n")), nil
}

func isNilMessage[M adk.MessageType](msg M) bool {
	var zero M
	return any(msg) == any(zero)
}

func (s *typedSkillTool[M]) getMessagesFromState(ctx context.Context) ([]M, error) {
	var messages []M
	var zero M
	switch any(zero).(type) {
	case *schema.Message:
		err := compose.ProcessState(ctx, func(_ context.Context, st *adk.State) error {
			messages = make([]M, len(st.Messages))
			for i, m := range st.Messages {
				messages[i] = any(m).(M)
			}
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("failed to process state: %w", err)
		}
	case *schema.AgenticMessage:
		// Fork mode is not supported for AgenticMessage because the internal
		// agent state type (agenticState) is unexported from the adk package,
		// making it inaccessible via compose.ProcessState from middleware packages.
		// Agent mode (the default) works normally for AgenticMessage.
		return nil, fmt.Errorf("fork mode is not supported for AgenticMessage; use agent mode instead")
	}
	return messages, nil
}
