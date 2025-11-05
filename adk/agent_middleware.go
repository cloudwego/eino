package adk

import (
	"context"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

type AgentState struct {
	SystemPrompt string
	// Messages are the messages to be sent to the model.
	// excluding system prompt
	Messages []Message
	Tools    []tool.BaseTool
	Choice   *schema.ToolChoice

	Extra map[string]any
}

type ToolRequest struct {
	State    *AgentState
	ToolCall schema.ToolCall
}

type BeforeModel func(ctx context.Context, origin *AgentState) (changed *AgentState, err error)
type AfterModel func(ctx context.Context, resp *AgentState) (changed *AgentState, err error)
type ModelHandler func(ctx context.Context, req *AgentState) (msg Message, err error)
type ModelMW func(ctx context.Context, req *AgentState, next ModelHandler) (msg Message, err error)

type ToolHandler func(ctx context.Context, req *ToolRequest) (result string, err error)

type ToolMW func(ctx context.Context, req *ToolRequest, next ToolHandler) (result string, err error)

type AgentMiddleware interface {
	MiddlewareName() string
}

type AgentMiddlewareBeforeModel interface {
	AgentMiddleware
	BeforeModel
}

type AgentMiddlewareAfterModel interface {
	AgentMiddleware
	AfterModel
}

type AgentMiddlewareModelMW interface {
	AgentMiddleware
	ModelMW
}

type AgentMiddlewareToolMW interface {
	AgentMiddleware
	ToolMW
}

type AgentMiddleware1 interface {
	BeforeModel
	AfterModel
	ModelMW
	ToolMW
}

type AgentMiddleware2 struct {
	BeforeModel      BeforeModel
	AfterModel       AfterModel
	WrapModelHandler ModelMW
	WrapToolHandler  ToolMW
}
