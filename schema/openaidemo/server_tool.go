package openaidemo

import (
	"github.com/cloudwego/eino/schema"
)

func init() {
	schema.RegisterName[ServerToolCall]("_eino_ext_openai_server_tool_call")
	schema.RegisterName[ServerToolResult]("_eino_ext_openai_server_tool_result")
}

func (s *ServerToolCall) ImplBaseServerToolCall() {}

type ServerToolCall struct {
	WebSearch *WebSearchArguments
}

type WebSearchArguments struct {
	ActionType WebSearchAction `json:"action_type"`

	Search   *WebSearchQuery    `json:"search,omitempty"`
	OpenPage *WebSearchOpenPage `json:"open_page,omitempty"`
	Find     *WebSearchFind     `json:"find,omitempty"`
}

type WebSearchQuery struct {
	Query string `json:"query"`
}

type WebSearchOpenPage struct {
	URL string `json:"url"`
}

type WebSearchFind struct {
	URL     string `json:"url"`
	Pattern string `json:"pattern"`
}

func (s *ServerToolResult) ImplBaseServerToolResult() {}

type ServerToolResult struct {
	WebSearch *WebSearchResult
}

type WebSearchResult struct {
	Status WebSearchStatus `json:"status"`

	Sources []*WebSearchSource `json:"sources"`
}

type WebSearchSource struct {
	URL string `json:"url"`
}
