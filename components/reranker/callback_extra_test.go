package reranker

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/schema"
)

func TestConvCallbackInput(t *testing.T) {
	docs := []*schema.Document{{ID: "d1"}}

	tests := []struct {
		name string
		src  any
		want *CallbackInput
	}{
		{
			name: "typed input",
			src: &CallbackInput{
				Request: &Request{Query: "q", Docs: docs},
				Config:  &Config{Model: "m", ReturnDocuments: true},
			},
			want: &CallbackInput{
				Request: &Request{Query: "q", Docs: docs},
				Config:  &Config{Model: "m", ReturnDocuments: true},
			},
		},
		{
			name: "map input",
			src: map[string]any{
				"query": "map-query",
				"docs":  docs,
			},
			want: nil,
		},
		{
			name: "string input",
			src:  "just-query",
			want: &CallbackInput{Request: &Request{Query: "just-query"}},
		},
		{
			name: "unsupported type",
			src:  123,
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvCallbackInput(tt.src)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.Equal(t, tt.want.Request, got.Request)
				assert.Equal(t, tt.want.TopK, got.TopK)
				assert.Equal(t, tt.want.Extra, got.Extra)
				assert.Equal(t, tt.want.Config, got.Config)
			}
		})
	}
}

func TestConvCallbackOutput(t *testing.T) {
	docs := []*schema.Document{{ID: "d1"}}

	usage := &TokenUsage{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3}
	config := &Config{Model: "reranker-model", ReturnDocuments: true}

	tests := []struct {
		name string
		src  any
		want *CallbackOutput
	}{
		{
			name: "typed output",
			src: &CallbackOutput{
				Docs:       docs,
				Config:     config,
				TokenUsage: usage,
			},
			want: &CallbackOutput{
				Docs:       docs,
				Config:     config,
				TokenUsage: usage,
			},
		},
		{
			name: "docs slice",
			src:  docs,
			want: &CallbackOutput{Docs: docs},
		},
		{
			name: "unsupported type",
			src:  "invalid",
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConvCallbackOutput(tt.src)
			if tt.want == nil {
				assert.Nil(t, got)
				return
			}
			if assert.NotNil(t, got) {
				assert.Equal(t, tt.want.Docs, got.Docs)
				assert.Equal(t, tt.want.Config, got.Config)
				assert.Equal(t, tt.want.TokenUsage, got.TokenUsage)
			}
		})
	}
}
