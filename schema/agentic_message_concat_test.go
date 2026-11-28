/*
 * Copyright 2025 CloudWeGo Authors
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package schema

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConcatStringField covers the shared pre-sized string concatenation helper.
func TestConcatStringField(t *testing.T) {
	get := func(s *string) string { return *s }
	str := func(s string) *string { return &s }

	tests := []struct {
		name  string
		items []*string
		want  string
	}{
		{name: "nil slice", items: nil, want: ""},
		{name: "empty fragments", items: []*string{str(""), str("")}, want: ""},
		{name: "nil elements skipped", items: []*string{str("a"), nil, str("b")}, want: "ab"},
		{name: "ordered join", items: []*string{str("a"), str("b"), str("c")}, want: "abc"},
		{name: "mixed empty and value", items: []*string{str(""), str("x"), str(""), str("y")}, want: "xy"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, concatStringField(tt.items, get))
		})
	}
}

func TestConcatReasoning_Optimized(t *testing.T) {
	t.Run("text and signature together, in order", func(t *testing.T) {
		reasons := []*Reasoning{
			{Text: "he", Signature: "sig-"},
			nil,
			{Text: "llo", Signature: ""},
			{Text: " world", Signature: "123"},
		}
		got, err := concatReasoning(reasons)
		require.NoError(t, err)
		assert.Equal(t, "hello world", got.Text)
		assert.Equal(t, "sig-123", got.Signature)
	})

	t.Run("does not mutate input", func(t *testing.T) {
		reasons := []*Reasoning{
			{Text: "a", Signature: "s1"},
			{Text: "b", Signature: "s2"},
		}
		_, err := concatReasoning(reasons)
		require.NoError(t, err)
		assert.Equal(t, "a", reasons[0].Text)
		assert.Equal(t, "s1", reasons[0].Signature)
		assert.Equal(t, "b", reasons[1].Text)
		assert.Equal(t, "s2", reasons[1].Signature)
	})

	t.Run("empty", func(t *testing.T) {
		_, err := concatReasoning(nil)
		require.Error(t, err)
	})
}

func TestConcatAssistantGenTexts_Optimized(t *testing.T) {
	t.Run("multiple chunks in order", func(t *testing.T) {
		texts := []*AssistantGenText{
			{Text: "foo"},
			nil,
			{Text: "bar"},
			{Text: "baz"},
		}
		got, err := concatAssistantGenTexts(texts)
		require.NoError(t, err)
		assert.Equal(t, "foobarbaz", got.Text)
	})

	t.Run("single element fast path returns input pointer", func(t *testing.T) {
		in := &AssistantGenText{Text: "only"}
		got, err := concatAssistantGenTexts([]*AssistantGenText{in})
		require.NoError(t, err)
		assert.Same(t, in, got)
	})

	t.Run("does not mutate input", func(t *testing.T) {
		texts := []*AssistantGenText{{Text: "x"}, {Text: "y"}}
		_, err := concatAssistantGenTexts(texts)
		require.NoError(t, err)
		assert.Equal(t, "x", texts[0].Text)
		assert.Equal(t, "y", texts[1].Text)
	})
}

func TestConcatAssistantGenMedia_Optimized(t *testing.T) {
	t.Run("image base64 concatenated, metadata consistent", func(t *testing.T) {
		imgs := []*AssistantGenImage{
			{Base64Data: "AA", MIMEType: "image/png"},
			{Base64Data: "BB", URL: "http://x"},
			{Base64Data: "CC", MIMEType: "image/png", URL: "http://x"},
		}
		got, err := concatAssistantGenImages(imgs)
		require.NoError(t, err)
		assert.Equal(t, "AABBCC", got.Base64Data)
		assert.Equal(t, "image/png", got.MIMEType)
		assert.Equal(t, "http://x", got.URL)
	})

	t.Run("image conflicting mime type errors", func(t *testing.T) {
		imgs := []*AssistantGenImage{
			{Base64Data: "AA", MIMEType: "image/png"},
			{Base64Data: "BB", MIMEType: "image/jpeg"},
		}
		_, err := concatAssistantGenImages(imgs)
		require.Error(t, err)
	})

	t.Run("audio base64 concatenated", func(t *testing.T) {
		got, err := concatAssistantGenAudios([]*AssistantGenAudio{
			{Base64Data: "12"}, {Base64Data: "34"},
		})
		require.NoError(t, err)
		assert.Equal(t, "1234", got.Base64Data)
	})

	t.Run("video base64 concatenated", func(t *testing.T) {
		got, err := concatAssistantGenVideos([]*AssistantGenVideo{
			{Base64Data: "ab"}, {Base64Data: "cd"},
		})
		require.NoError(t, err)
		assert.Equal(t, "abcd", got.Base64Data)
	})

	t.Run("image does not mutate input", func(t *testing.T) {
		imgs := []*AssistantGenImage{{Base64Data: "AA"}, {Base64Data: "BB"}}
		_, err := concatAssistantGenImages(imgs)
		require.NoError(t, err)
		assert.Equal(t, "AA", imgs[0].Base64Data)
	})
}

func TestConcatFunctionToolCalls_Optimized(t *testing.T) {
	t.Run("sharded json arguments joined byte-exact", func(t *testing.T) {
		calls := []*FunctionToolCall{
			{CallID: "c1", Name: "fn", Arguments: `{"a":`},
			{Arguments: `1,"b":`},
			{Arguments: `"two"}`},
		}
		got, err := concatFunctionToolCalls(calls)
		require.NoError(t, err)
		assert.Equal(t, `{"a":1,"b":"two"}`, got.Arguments)
		assert.Equal(t, "c1", got.CallID)
		assert.Equal(t, "fn", got.Name)
	})

	t.Run("conflicting call id errors", func(t *testing.T) {
		_, err := concatFunctionToolCalls([]*FunctionToolCall{
			{CallID: "c1", Arguments: "a"},
			{CallID: "c2", Arguments: "b"},
		})
		require.Error(t, err)
	})

	t.Run("does not mutate input", func(t *testing.T) {
		calls := []*FunctionToolCall{{Arguments: "x"}, {Arguments: "y"}}
		_, err := concatFunctionToolCalls(calls)
		require.NoError(t, err)
		assert.Equal(t, "x", calls[0].Arguments)
	})
}

func TestConcatMCPToolCalls_Optimized(t *testing.T) {
	t.Run("sharded arguments joined", func(t *testing.T) {
		calls := []*MCPToolCall{
			{ServerLabel: "s", CallID: "c", Name: "n", Arguments: `{"x`},
			{Arguments: `":1}`},
		}
		got, err := concatMCPToolCalls(calls)
		require.NoError(t, err)
		assert.Equal(t, `{"x":1}`, got.Arguments)
		assert.Equal(t, "s", got.ServerLabel)
		assert.Equal(t, "c", got.CallID)
		assert.Equal(t, "n", got.Name)
	})

	t.Run("conflicting server label errors", func(t *testing.T) {
		_, err := concatMCPToolCalls([]*MCPToolCall{
			{ServerLabel: "s1", Arguments: "a"},
			{ServerLabel: "s2", Arguments: "b"},
		})
		require.Error(t, err)
	})
}

func TestConcatMCPToolApprovalRequests_Optimized(t *testing.T) {
	t.Run("sharded arguments joined", func(t *testing.T) {
		reqs := []*MCPToolApprovalRequest{
			{ID: "id", Name: "n", ServerLabel: "s", Arguments: `{"k":`},
			{Arguments: `true}`},
		}
		got, err := concatMCPToolApprovalRequests(reqs)
		require.NoError(t, err)
		assert.Equal(t, `{"k":true}`, got.Arguments)
		assert.Equal(t, "id", got.ID)
		assert.Equal(t, "n", got.Name)
		assert.Equal(t, "s", got.ServerLabel)
	})

	t.Run("conflicting id errors", func(t *testing.T) {
		_, err := concatMCPToolApprovalRequests([]*MCPToolApprovalRequest{
			{ID: "a", Arguments: "x"},
			{ID: "b", Arguments: "y"},
		})
		require.Error(t, err)
	})
}

func TestConcatFunctionToolResults_Optimized(t *testing.T) {
	t.Run("adjacent text chunks merged, non-text preserved", func(t *testing.T) {
		results := []*FunctionToolResult{
			{CallID: "c1", Name: "tool1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "he"}},
			}},
			{CallID: "c1", Name: "tool1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "llo "}},
			}},
			{CallID: "c1", Name: "tool1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "world"}},
			}},
			{CallID: "c1", Name: "tool1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeImage, Image: &UserInputImage{URL: "http://img.png"}},
			}},
		}
		got, err := concatFunctionToolResults(results)
		require.NoError(t, err)
		require.Len(t, got.Content, 2)
		assert.Equal(t, "hello world", got.Content[0].Text.Text)
		assert.Equal(t, "http://img.png", got.Content[1].Image.URL)
	})

	t.Run("text run split by intervening image", func(t *testing.T) {
		results := []*FunctionToolResult{
			{CallID: "c1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "a"}},
				{Type: FunctionToolResultContentBlockTypeImage, Image: &UserInputImage{URL: "u"}},
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "b"}},
			}},
			{CallID: "c1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "c"}},
			}},
		}
		got, err := concatFunctionToolResults(results)
		require.NoError(t, err)
		require.Len(t, got.Content, 3)
		assert.Equal(t, "a", got.Content[0].Text.Text)
		assert.Equal(t, "u", got.Content[1].Image.URL)
		assert.Equal(t, "bc", got.Content[2].Text.Text)
	})

	t.Run("extras on merged text run are concatenated", func(t *testing.T) {
		results := []*FunctionToolResult{
			{CallID: "c1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "a"}, Extra: map[string]any{"k": "1"}},
			}},
			{CallID: "c1", Content: []*FunctionToolResultContentBlock{
				{Type: FunctionToolResultContentBlockTypeText, Text: &UserInputText{Text: "b"}, Extra: map[string]any{"k": "2"}},
			}},
		}
		got, err := concatFunctionToolResults(results)
		require.NoError(t, err)
		require.Len(t, got.Content, 1)
		assert.Equal(t, "ab", got.Content[0].Text.Text)
		assert.Equal(t, "12", got.Content[0].Extra["k"])
	})

	t.Run("empty content stays nil", func(t *testing.T) {
		results := []*FunctionToolResult{
			{CallID: "c1", Name: "t"},
			{CallID: "c1", Name: "t"},
		}
		got, err := concatFunctionToolResults(results)
		require.NoError(t, err)
		assert.Nil(t, got.Content)
	})
}

// ------------------------- Benchmarks -------------------------

var concatChunkCounts = []int{16, 64, 256, 1024}
var concatPayloadSizes = []int{8, 128}

func makePayload(n int) string {
	return strings.Repeat("a", n)
}

func BenchmarkConcatReasoning(b *testing.B) {
	for _, chunks := range concatChunkCounts {
		for _, size := range concatPayloadSizes {
			b.Run(fmt.Sprintf("chunks=%d/payload=%d", chunks, size), func(b *testing.B) {
				payload := makePayload(size)
				in := make([]*Reasoning, chunks)
				for i := range in {
					in[i] = &Reasoning{Text: payload, Signature: payload}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := concatReasoning(in); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkConcatAssistantGenTexts(b *testing.B) {
	for _, chunks := range concatChunkCounts {
		for _, size := range concatPayloadSizes {
			b.Run(fmt.Sprintf("chunks=%d/payload=%d", chunks, size), func(b *testing.B) {
				payload := makePayload(size)
				in := make([]*AssistantGenText, chunks)
				for i := range in {
					in[i] = &AssistantGenText{Text: payload}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := concatAssistantGenTexts(in); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkConcatFunctionToolCalls(b *testing.B) {
	for _, chunks := range concatChunkCounts {
		for _, size := range concatPayloadSizes {
			b.Run(fmt.Sprintf("chunks=%d/payload=%d", chunks, size), func(b *testing.B) {
				payload := makePayload(size)
				in := make([]*FunctionToolCall, chunks)
				for i := range in {
					in[i] = &FunctionToolCall{CallID: "c", Name: "fn", Arguments: payload}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := concatFunctionToolCalls(in); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func BenchmarkConcatMCPToolCalls(b *testing.B) {
	for _, chunks := range concatChunkCounts {
		for _, size := range concatPayloadSizes {
			b.Run(fmt.Sprintf("chunks=%d/payload=%d", chunks, size), func(b *testing.B) {
				payload := makePayload(size)
				in := make([]*MCPToolCall, chunks)
				for i := range in {
					in[i] = &MCPToolCall{ServerLabel: "s", CallID: "c", Name: "fn", Arguments: payload}
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					if _, err := concatMCPToolCalls(in); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
