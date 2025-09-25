package deep

import (
	"context"
	"fmt"
	"strings"

	"github.com/bytedance/sonic"
	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
)
// Config defines the configuration for creating a DeepAgent.
type Config struct {
	// Name is the identifier for the Deep agent.
	Name string
	// Description provides a brief explanation of the agent's purpose.
	Description string

	// ChatModel is the model used by DeepAgent for reasoning and task execution.
	ChatModel model.ToolCallingChatModel
	// Instruction contains the system prompt that guides the agent's behavior.
	Instruction string
	// SubAgents are specialized agents that can be invoked by the agent.
	SubAgents []adk.Agent
	// ToolsConfig provides the tools and tool-calling configurations available for the agent to invoke.
	ToolsConfig adk.ToolsConfig
	// MaxIteration limits the maximum number of reasoning iterations the agent can perform.
	MaxIteration int
}

// New creates a new Deep agent instance with the provided configuration.
// This function initializes built-in tools, creates a task tool for subagent orchestration,
// and returns a fully configured ChatModelAgent ready for execution.
func New(ctx context.Context, cfg *Config) (adk.Agent, error) {
	builtinTools, err := newBuiltinTools()
	if err != nil {
		return nil, err
	}
	tt, err := newTaskTool(ctx, cfg.ChatModel, append(cfg.ToolsConfig.Tools, builtinTools...), cfg.SubAgents)
	if err != nil {
		return nil, fmt.Errorf("new task tool: %w", err)
	}

	cfg.ToolsConfig.Tools = append(cfg.ToolsConfig.Tools, append(builtinTools, tt)...)

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          cfg.Name,
		Description:   cfg.Description,
		Instruction:   cfg.Instruction + "\n" + baseAgentPrompt + "\n" + writeTodosPrompt + "\n" + taskPrompt,
		Model:         cfg.ChatModel,
		ToolsConfig:   cfg.ToolsConfig,
		GenModelInput: nil,
		Exit:          nil,
		OutputKey:     "",
		MaxIterations: cfg.MaxIteration,
	})
}

func newTaskTool(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	ts []tool.BaseTool,
	subAgents []adk.Agent,
) (tool.InvokableTool, error) {
	generalAgent, err := newGeneralAgent(ctx, cm, ts)
	if err != nil {
		return nil, fmt.Errorf("failed to new general agent: %w", err)
	}

	t := &taskTool{subAgents: map[string]tool.InvokableTool{generalAgent.Name(ctx): adk.NewAgentTool(ctx, generalAgent)}}
	subAgentsDescBuilder := strings.Builder{}
	for _, a := range subAgents {
		name := a.Name(ctx)
		desc := a.Description(ctx)
		t.subAgents[name] = adk.NewAgentTool(ctx, a)
		subAgentsDescBuilder.WriteString(fmt.Sprintf("- %s: %s\n", name, desc))
	}

	desc, err := pyfmt.Fmt(taskToolDescription, map[string]any{
		"other_agents": subAgentsDescBuilder.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to format task tool description: %w", err)
	}

	it, err := utils.InferTool("task", desc, t.exec)
	if err != nil {
		return nil, fmt.Errorf("failed to infer task tool: %w", err)
	}
	return it, nil
}

type taskTool struct {
	subAgents map[string]tool.InvokableTool
}

type taskToolArgument struct {
	SubagentType string `json:"subagent_type"`
	Description  string `json:"description"`
}

func (t *taskTool) exec(ctx context.Context, input taskToolArgument) (output string, err error) {
	a, ok := t.subAgents[input.SubagentType]
	if !ok {
		return "", fmt.Errorf("subagent type %s not found", input.SubagentType)
	}

	params, err := sonic.MarshalString(map[string]string{
		"request": input.Description,
	})
	if err != nil {
		return "", err
	}

	return a.InvokableRun(ctx, params)
}

func newGeneralAgent(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	ts []tool.BaseTool,
) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        generalAgentName,
		Description: "general agent",
		Instruction: baseAgentPrompt + "\n" + writeTodosPrompt + "\n",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: ts}},
	})
}
