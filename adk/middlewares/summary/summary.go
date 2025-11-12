package summary

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkoukk/tiktoken-go"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/prompt"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type TokenCounter func(ctx context.Context, msgs []adk.Message) (tokenNum []int64, err error)

// Config defines parameters for the conversation summarization middleware.
// It controls when summarization is triggered and how much recent context is retained.
// Required: Model. Optional: SystemPrompt, Counter, and token budgets.
type Config struct {
	// MaxTokensBeforeSummary is the max token threshold to trigger summarization based on total context
	// (system prompt + history). Uses DefaultMaxTokensBeforeSummary when <= 0.
	MaxTokensBeforeSummary int

	// MaxTokensForRecentMessages is the max token budget reserved for recent messages after summarization.
	// Uses DefaultMaxTokensForRecentMessages when <= 0.
	MaxTokensForRecentMessages int

	// Counter custom token counter.
	// Optional
	Counter TokenCounter

	// Model used to generate the summary. Must be provided.
	// Required.
	Model model.BaseChatModel

	// SystemPrompt is the system prompt for the summarizer.
	// Optional. If empty, PromptOfSummary is used.
	SystemPrompt string
}

// New creates an AgentMiddleware that compacts long conversation history
// into a single summary message when the token threshold is exceeded.
// The summarizer chain is: ChatTemplate(SystemPrompt) -> ChatModel(Model).
// It applies defaults for token budgets and allows a custom Counter.
func New(ctx context.Context, cfg *Config) (*adk.AgentMiddleware, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is nil")
	}

	systemPrompt := cfg.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = PromptOfSummary
	}
	maxBefore := DefaultMaxTokensBeforeSummary
	if cfg.MaxTokensBeforeSummary > 0 {
		maxBefore = cfg.MaxTokensBeforeSummary
	}
	maxRecent := DefaultMaxTokensForRecentMessages
	if cfg.MaxTokensForRecentMessages > 0 {
		maxRecent = cfg.MaxTokensForRecentMessages
	}

	tpl := prompt.FromMessages(schema.FString,
		schema.SystemMessage(systemPrompt))

	summarizer, err := compose.NewChain[map[string]any, *schema.Message]().
		AppendChatTemplate(tpl).
		AppendChatModel(cfg.Model).
		Compile(ctx)
	if err != nil {
		return nil, fmt.Errorf("compile summarizer failed, err=%w", err)
	}

	sm := &summaryMiddleware{
		counter:    defaultCounterToken,
		maxBefore:  maxBefore,
		maxRecent:  maxRecent,
		summarizer: summarizer,
	}
	if cfg.Counter != nil {
		sm.counter = cfg.Counter
	}
	return &adk.AgentMiddleware{BeforeChatModel: sm.BeforeModel}, nil
}

const summaryMessageFlag = "_agent_middleware_summary_message"

type summaryMiddleware struct {
	counter   TokenCounter
	maxBefore int
	maxRecent int

	summarizer compose.Runnable[map[string]any, *schema.Message]
}

func (s *summaryMiddleware) BeforeModel(ctx context.Context, state *adk.ChatModelAgentState) (err error) {
	if state == nil || len(state.Messages) == 0 {
		return nil
	}

	messages := state.Messages
	msgsToken, err := s.counter(ctx, messages)
	if err != nil {
		return fmt.Errorf("count token failed, err=%w", err)
	}
	if len(messages) != len(msgsToken) {
		return fmt.Errorf("token count mismatch, msgNum=%d, tokenCountNum=%d", len(messages), len(msgsToken))
	}

	var total int64
	for _, t := range msgsToken {
		total += t
	}
	// Trigger summarization only when exceeding threshold
	if total <= int64(s.maxBefore) {
		return nil
	}

	messages = messages[1:]
	msgsToken = msgsToken[1:]

	// Build blocks with user-messages, summary-message, tool-call pairings
	type block struct {
		msgs   []*schema.Message
		tokens int64
	}
	idx := 0

	systemBlock := block{}
	if idx < len(messages) {
		m := messages[idx]
		if m != nil && m.Role == schema.System {
			systemBlock.msgs = append(systemBlock.msgs, m)
			systemBlock.tokens += msgsToken[idx]
			idx++
		}
	}
	userBlock := block{}
	for idx < len(messages) {
		m := messages[idx]
		if m == nil {
			idx++
			continue
		}
		if m.Role != schema.User {
			break
		}
		userBlock.msgs = append(userBlock.msgs, m)
		userBlock.tokens += msgsToken[idx]
		idx++
	}
	summaryBlock := block{}
	if idx < len(messages) {
		m := messages[idx]
		if m != nil && m.Role == schema.Assistant {
			if _, ok := m.Extra[summaryMessageFlag]; ok {
				summaryBlock.msgs = append(summaryBlock.msgs, m)
				summaryBlock.tokens += msgsToken[idx]
				idx++
			}
		}
	}

	toolBlocks := make([]block, 0)
	for i := idx; i < len(messages); i++ {
		m := messages[i]
		if m == nil {
			continue
		}
		if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
			b := block{msgs: []*schema.Message{m}, tokens: msgsToken[i]}
			// Collect subsequent tool messages matching any tool call id
			callIDs := make(map[string]struct{}, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				callIDs[tc.ID] = struct{}{}
			}
			j := i + 1
			for j < len(messages) {
				nm := messages[j]
				if nm == nil || nm.Role != schema.Tool {
					break
				}
				// Match by ToolCallID when available; if empty, include but keep boundary
				if nm.ToolCallID == "" {
					b.msgs = append(b.msgs, nm)
					b.tokens += msgsToken[j]
				} else {
					if _, ok := callIDs[nm.ToolCallID]; !ok {
						// Tool message not belonging to this assistant call -> end pairing
						break
					}
					b.msgs = append(b.msgs, nm)
					b.tokens += msgsToken[j]
				}
				j++
			}
			toolBlocks = append(toolBlocks, b)
			i = j - 1
			continue
		}
		toolBlocks = append(toolBlocks, block{msgs: []*schema.Message{m}, tokens: msgsToken[i]})
	}

	// Split into recent and older within token budget, from newest to oldest
	var recentBlocks []block
	var olderBlocks []block
	var recentTokens int64
	for i := len(toolBlocks) - 1; i >= 0; i-- {
		b := toolBlocks[i]
		if recentTokens+b.tokens > int64(s.maxRecent) {
			olderBlocks = append([]block{b}, olderBlocks...)
			continue
		}
		recentBlocks = append([]block{b}, recentBlocks...)
		recentTokens += b.tokens
	}

	joinBlocks := func(bs []block) string {
		var sb strings.Builder
		for _, b := range bs {
			for _, m := range b.msgs {
				sb.WriteString(renderMsg(m))
				sb.WriteString("\n")
			}
		}
		return sb.String()
	}

	olderText := joinBlocks(olderBlocks)
	recentText := joinBlocks(recentBlocks)

	msg, err := s.summarizer.Invoke(ctx, map[string]any{
		"system_prompt":    joinBlocks([]block{systemBlock}),
		"user_messages":    joinBlocks([]block{userBlock}),
		"previous_summary": joinBlocks([]block{summaryBlock}),
		"older_messages":   olderText,
		"recent_messages":  recentText,
	})
	if err != nil {
		return fmt.Errorf("summarize failed, err=%w", err)
	}

	if msg.Extra == nil {
		msg.Extra = make(map[string]any)
	}
	msg.Extra[summaryMessageFlag] = true
	msg.Name = "summary"
	// Build new state: prepend summary message, keep recent messages
	newMessages := make([]*schema.Message, 0, 1+len(recentBlocks))
	newMessages = append(newMessages, systemBlock.msgs...)
	newMessages = append(newMessages, userBlock.msgs...)
	newMessages = append(newMessages, msg)
	for _, b := range recentBlocks {
		newMessages = append(newMessages, b.msgs...)
	}

	state.Messages = newMessages
	return nil
}

// Render messages into strings
func renderMsg(m *schema.Message) string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	if m.Role == schema.Tool {
		if m.ToolName != "" {
			sb.WriteString("[tool:")
			sb.WriteString(m.ToolName)
			sb.WriteString("]\n")
		} else {
			sb.WriteString("[tool]\n")
		}
	} else {
		sb.WriteString("[")
		sb.WriteString(string(m.Role))
		sb.WriteString("]\n")
	}
	if m.Content != "" {
		sb.WriteString(m.Content)
		sb.WriteString("\n")
	}
	if m.Role == schema.Assistant && len(m.ToolCalls) > 0 {
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" {
				sb.WriteString("tool_call: ")
				sb.WriteString(tc.Function.Name)
				sb.WriteString("\n")
			}
			if tc.Function.Arguments != "" {
				sb.WriteString("args: ")
				sb.WriteString(tc.Function.Arguments)
				sb.WriteString("\n")
			}
		}
	}
	for _, part := range m.UserInputMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			sb.WriteString(part.Text)
			sb.WriteString("\n")
		}
	}
	for _, part := range m.AssistantGenMultiContent {
		if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
			sb.WriteString(part.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

func defaultCounterToken(ctx context.Context, msgs []adk.Message) (tokenNum []int64, err error) {
	encoding := "cl100k_base"
	tke, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		return nil, fmt.Errorf("get encoding failed, encoding=%v, err=%w", encoding, err)
	}
	tokenNum = make([]int64, len(msgs))

	for i, m := range msgs {
		if m == nil {
			tokenNum[i] = 0
			continue
		}

		var sb strings.Builder

		// Message role contributes to chat tokenization overhead; include it as text.
		if m.Role != "" {
			sb.WriteString(string(m.Role))
			sb.WriteString("\n")
		}

		// Core text content
		if m.Content != "" {
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}

		// Reasoning content if present
		// Reasoning Content is not used by model
		// if m.ReasoningContent != "" {
		//     sb.WriteString(m.ReasoningContent)
		//     sb.WriteString("\n")
		// }

		// Multi modal input/output text parts
		for _, part := range m.UserInputMultiContent {
			if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
				sb.WriteString(part.Text)
				sb.WriteString("\n")
			}
		}
		for _, part := range m.AssistantGenMultiContent {
			if part.Type == schema.ChatMessagePartTypeText && part.Text != "" {
				sb.WriteString(part.Text)
				sb.WriteString("\n")
			}
		}

		// Tool call textual context (name + arguments)
		for _, tc := range m.ToolCalls {
			if tc.Function.Name != "" {
				sb.WriteString(tc.Function.Name)
				sb.WriteString("\n")
			}
			if tc.Function.Arguments != "" {
				sb.WriteString(tc.Function.Arguments)
				sb.WriteString("\n")
			}
		}

		text := sb.String()
		if text == "" {
			tokenNum[i] = 0
			continue
		}

		tokens := tke.Encode(text, nil, nil)
		tokenNum[i] = int64(len(tokens))
	}

	return tokenNum, nil
}
