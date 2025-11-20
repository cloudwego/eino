package openaidemo

import (
	"context"

	"github.com/cloudwego/eino/components/agentic"
	"github.com/cloudwego/eino/schema"
)

type AgenticModel struct{}

func (*AgenticModel) Generate(ctx context.Context, input []*schema.AgenticMessage, opts ...agentic.Option) (*schema.AgenticMessage, error) {
	return nil, nil
}

func (*AgenticModel) Stream(ctx context.Context, input []*schema.AgenticMessage, opts ...agentic.Option) (*schema.StreamReader[*schema.AgenticMessage], error) {
	return nil, nil
}
