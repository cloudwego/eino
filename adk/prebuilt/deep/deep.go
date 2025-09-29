package deep

import (
	"context"
	"fmt"
	"strings"

	"github.com/slongfield/pyfmt"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
	"github.com/cloudwego/eino/compose"
)

type Config struct {
	Name        string
	Description string

	ChatModel      model.ToolCallingChatModel
	Instruction    string
	SubAgents      []adk.Agent
	Tools          []tool.BaseTool
	MainAgentTools []tool.BaseTool
}

func New(ctx context.Context, cfg *Config) (adk.Agent, error) {

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          cfg.Name,
		Description:   cfg.Description,
		Instruction:   cfg.Instruction,
		Model:         cfg.ChatModel,
		ToolsConfig:   adk.ToolsConfig{},
		GenModelInput: nil,
		Exit:          nil,
		OutputKey:     "",
		MaxIterations: 0,
	})
}

func newTaskTool(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	ts []tool.BaseTool,
	subAgents []adk.Agent,
) (tool.BaseTool, error) {
	generalAgent, err := newGeneralAgent(ctx, cm, append(ts, builtinTools...))
	if err != nil {
		return nil, fmt.Errorf("failed to new general agent: %w", err)
	}

	t := &taskTool{subAgents: map[string]adk.Agent{generalAgent.Name(ctx): generalAgent}}
	subAgentsDescBuilder := strings.Builder{}
	for _, a := range subAgents {
		name := a.Name(ctx)
		desc := a.Description(ctx)
		t.subAgents[name] = a
		subAgentsDescBuilder.WriteString(fmt.Sprintf("- %s: %s", name, desc))
	}

	desc, err := pyfmt.Fmt(taskToolDescription, map[string]any{
		"other_agents": subAgentsDescBuilder.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to format task tool description: %w", err)
	}

	return utils.InferTool("task", desc, t.exec)
}

type taskTool struct {
	subAgents map[string]adk.Agent
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

	return "", nil
}

func newGeneralAgent(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	ts []tool.BaseTool,
) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        generalAgentName,
		Description: "general agent",
		Instruction: baseAgentPrompt,
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: ts}},
	})
}
