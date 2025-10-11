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

type Config struct {
	Name        string
	Description string

	ChatModel      model.ToolCallingChatModel
	Instruction    string
	SubAgents      []adk.Agent
	Tools          []tool.InvokableTool
	MainAgentTools []tool.InvokableTool
}

func New(ctx context.Context, cfg *Config) (adk.Agent, error) {
	builtinTools, err := newBuiltinTools()
	if err != nil {
		return nil, err
	}
	tt, err := newTaskTool(ctx, cfg.ChatModel, append(cfg.Tools, builtinTools...), cfg.SubAgents)
	if err != nil {
		return nil, fmt.Errorf("new task tool: %w", err)
	}
	submitResult, err := newSubmitResultTool()
	if err != nil {
		return nil, fmt.Errorf("new submit result tool: %w", err)
	}

	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        cfg.Name,
		Description: cfg.Description,
		Instruction: cfg.Instruction + "\n" + baseAgentPrompt + "\n" + writeTodosPrompt + "\n" + taskPrompt,
		Model:       cfg.ChatModel,
		ToolsConfig: adk.ToolsConfig{
			ToolsNodeConfig: compose.ToolsNodeConfig{
				Tools: convSliceType(append(append(append(cfg.MainAgentTools, tt), builtinTools...), submitResult), func(f tool.InvokableTool) tool.BaseTool {
					return f
				}),
			},
			ReturnDirectly: map[string]bool{
				"submit_result": true,
			},
		},
		GenModelInput: nil,
		Exit:          nil,
		OutputKey:     "",
		MaxIterations: 300,
	})
}

func newTaskTool(
	ctx context.Context,
	cm model.ToolCallingChatModel,
	ts []tool.InvokableTool,
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
	ts []tool.InvokableTool,
) (adk.Agent, error) {
	return adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:        generalAgentName,
		Description: "general agent",
		Instruction: baseAgentPrompt + "\n" + writeTodosPrompt + "\n",
		Model:       cm,
		ToolsConfig: adk.ToolsConfig{ToolsNodeConfig: compose.ToolsNodeConfig{Tools: convSliceType(ts, func(f tool.InvokableTool) tool.BaseTool { return f })}},
	})
}
