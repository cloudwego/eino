package deep

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
)

const (
	SessionKeyTodo = "deep_agent_session_key_todo"
)

type Config struct {
	Name        string
	Description string

	ChatModel   model.ToolCallingChatModel
	Instruction string
	SubAgents   []adk.Agent
	Tools       []tool.BaseTool
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

}
