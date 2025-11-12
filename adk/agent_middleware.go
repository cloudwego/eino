package adk

import (
	"context"
)

// ChatModelAgentState represents the state of a chat model agent during conversation.
type ChatModelAgentState struct {
	SystemPrompt string

	// History contains all messages in the current conversation session.
	History []Message
}

type BeforeModel func(ctx context.Context, s *ChatModelAgentState) (err error)

type AgentMiddleware struct {
	BeforeModel BeforeModel
}
