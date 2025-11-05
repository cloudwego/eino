package summary

import (
	"context"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
)

type TokenCounter func(ctx context.Context, msgs []adk.Message) (tokenNum []int64, err error)

type Config struct {
	// 触发进行Summarization的消息中的Token数量
	MaxTokensBeforeSummary     int
	MaxTokensForRecentMessages int
	Counter                    TokenCounter

	Model model.BaseChatModel
}

func New(ctx context.Context, cfg *Config) (*adk.AgentMiddleware, error) {

	return &adk.AgentMiddleware{}, nil
}
