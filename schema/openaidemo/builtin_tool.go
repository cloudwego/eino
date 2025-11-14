package openaidemo

import (
	"github.com/cloudwego/eino/schema"
)

type BuiltinToolResult struct {
	WebSearch     *WebSearchResult
	CodeExecution *CodeExecutionResult
}

func (b *BuiltinToolResult) BuiltinToolResultProvider() string {
	return provider
}

func (b *BuiltinToolResult) GetWebSearchResult() *schema.WebSearchResult {
	if b == nil || b.WebSearch == nil {
		return nil
	}

	sources := make([]*schema.WebSearchSource, 0, len(b.WebSearch.Sources))

	for _, source := range b.WebSearch.Sources {
		sources = append(sources, &schema.WebSearchSource{
			URL: source.URL,
		})
	}

	return &schema.WebSearchResult{
		Sources: sources,
	}
}

type WebSearchResult struct {
	Sources []*ResponseFunctionWebSearchActionSearchSource `json:"sources"`
}

type ResponseFunctionWebSearchActionSearchSource struct {
	URL string `json:"url,required"`
}

type CodeExecutionResult struct {
	Outputs []*ResponseCodeInterpreterToolCallOutput `json:"outputs"`
}

type ResponseCodeInterpreterToolCallOutput struct {
	Logs *string `json:"logs"`
	URL  *string `json:"url"`
}
