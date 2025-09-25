package deep

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
)

type Config struct {
	ChatModel model.ToolCallingChatModel
	SubAgents []adk.Agent
	Tools     []tool.BaseTool
}

func New(ctx context.Context, cfg *Config) (adk.Agent, error) {

	return nil, nil
}
