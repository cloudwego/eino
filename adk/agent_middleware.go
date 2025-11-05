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
type ModelMW func(next ModelHandler) ModelHandler

type ToolHandler func(ctx context.Context, req *ToolRequest) (result string, err error)

type ToolMW func(next ToolHandler) ToolHandler

type AgentMiddleware struct {
	BeforeModel      BeforeModel
	AfterModel       AfterModel
	WrapModelHandler ModelMW
	WrapToolHandler  ToolMW
}
