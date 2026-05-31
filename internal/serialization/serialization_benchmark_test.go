/*
 * Copyright 2026 CloudWeGo Authors
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

package serialization

import (
	"encoding/gob"
	"fmt"
	"testing"
)

type benchMessage struct {
	Role             string         `json:"role"`
	Content          string         `json:"content"`
	Name             string         `json:"name,omitempty"`
	ToolCallID       string         `json:"tool_call_id,omitempty"`
	ReasoningContent string         `json:"reasoning_content,omitempty"`
	Extra            map[string]any `json:"extra,omitempty"`
}

type benchToolCall struct {
	ID       string            `json:"id"`
	Type     string            `json:"type"`
	Function benchFunctionCall `json:"function"`
	Extra    map[string]any    `json:"extra,omitempty"`
}

type benchFunctionCall struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type benchCustomType struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Version  int    `json:"version"`
}

type benchStructWithInterface struct {
	A any
	B any
	C map[string]any
}

func init() {
	_ = GenericRegister[benchMessage]("bench_message")
	_ = GenericRegister[benchToolCall]("bench_tool_call")
	_ = GenericRegister[benchFunctionCall]("bench_function_call")
	_ = GenericRegister[benchCustomType]("bench_custom_type")
	_ = GenericRegister[benchStructWithInterface]("bench_struct_with_interface")

	gob.Register(benchMessage{})
	gob.Register(benchToolCall{})
	gob.Register(benchFunctionCall{})
	gob.Register(benchCustomType{})
	gob.Register(benchStructWithInterface{})
	gob.Register(map[string]any{})
	gob.Register([]any{})
}

func createSimpleMessage() benchMessage {
	return benchMessage{
		Role:    "assistant",
		Content: "Hello, how can I help you today?",
		Extra: map[string]any{
			"model":       "gpt-4",
			"temperature": 0.7,
			"max_tokens":  1024,
		},
	}
}

func createComplexMessage() benchMessage {
	return benchMessage{
		Role:             "assistant",
		Content:          "Here is a detailed response with multiple paragraphs of content that simulates a real-world LLM response. This includes various information and explanations that would typically be returned by a language model.",
		Name:             "assistant",
		ReasoningContent: "Let me think about this step by step. First, I need to understand the question. Then I'll formulate a comprehensive response.",
		Extra: map[string]any{
			"model":             "gpt-4-turbo",
			"temperature":       0.7,
			"max_tokens":        4096,
			"top_p":             0.95,
			"frequency_penalty": 0.0,
			"presence_penalty":  0.0,
			"stop_sequences":    []any{"END", "STOP"},
			"metadata": map[string]any{
				"request_id": "req_abc123xyz",
				"timestamp":  1234567890,
				"user_id":    "user_456",
			},
		},
	}
}

func createMessageWithCustomTypes() benchStructWithInterface {
	return benchStructWithInterface{
		A: "simple string",
		B: benchCustomType{
			Provider: "openai",
			Model:    "gpt-4",
			Version:  4,
		},
		C: map[string]any{
			"config": benchCustomType{
				Provider: "anthropic",
				Model:    "claude-3",
				Version:  3,
			},
			"count": 42,
		},
	}
}

func createLargeMessage() benchMessage {
	extra := make(map[string]any)
	for i := 0; i < 50; i++ {
		extra[fmt.Sprintf("key_%d", i)] = fmt.Sprintf("value_%d", i)
	}
	extra["nested"] = map[string]any{
		"level1": map[string]any{
			"level2": map[string]any{
				"level3": "deep value",
			},
		},
	}
	extra["list"] = []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	return benchMessage{
		Role:    "assistant",
		Content: "This is a large message with many extra fields to test serialization performance with larger payloads.",
		Extra:   extra,
	}
}

func BenchmarkInternalSerializer_Marshal_SimpleMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createSimpleMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Marshal_SimpleMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createSimpleMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Unmarshal_SimpleMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createSimpleMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Unmarshal_SimpleMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createSimpleMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Marshal_ComplexMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createComplexMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Marshal_ComplexMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createComplexMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Unmarshal_ComplexMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createComplexMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Unmarshal_ComplexMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createComplexMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Marshal_CustomTypes(b *testing.B) {
	s := &InternalSerializer{}
	msg := createMessageWithCustomTypes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Marshal_CustomTypes(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createMessageWithCustomTypes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Unmarshal_CustomTypes(b *testing.B) {
	s := &InternalSerializer{}
	msg := createMessageWithCustomTypes()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchStructWithInterface
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Unmarshal_CustomTypes(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createMessageWithCustomTypes()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchStructWithInterface
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Marshal_LargeMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createLargeMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Marshal_LargeMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createLargeMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_Unmarshal_LargeMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createLargeMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_Unmarshal_LargeMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createLargeMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkInternalSerializer_RoundTrip_SimpleMessage(b *testing.B) {
	s := &InternalSerializer{}
	msg := createSimpleMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
		var result benchMessage
		err = s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkHumanReadableSerializer_RoundTrip_SimpleMessage(b *testing.B) {
	s := &HumanReadableSerializer{}
	msg := createSimpleMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
		var result benchMessage
		err = s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Marshal_SimpleMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createSimpleMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Unmarshal_SimpleMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createSimpleMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Marshal_ComplexMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createComplexMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Unmarshal_ComplexMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createComplexMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Marshal_CustomTypes(b *testing.B) {
	s := &GobSerializer{}
	msg := createMessageWithCustomTypes()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Unmarshal_CustomTypes(b *testing.B) {
	s := &GobSerializer{}
	msg := createMessageWithCustomTypes()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchStructWithInterface
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Marshal_LargeMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createLargeMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_Unmarshal_LargeMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createLargeMessage()
	data, _ := s.Marshal(msg)

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		var result benchMessage
		err := s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkGobSerializer_RoundTrip_SimpleMessage(b *testing.B) {
	s := &GobSerializer{}
	msg := createSimpleMessage()

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		data, err := s.Marshal(msg)
		if err != nil {
			b.Fatal(err)
		}
		var result benchMessage
		err = s.Unmarshal(data, &result)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func TestOutputSizeComparison(t *testing.T) {
	is := &InternalSerializer{}
	hr := &HumanReadableSerializer{}
	gs := &GobSerializer{}

	testCases := []struct {
		name  string
		input any
	}{
		{"SimpleMessage", createSimpleMessage()},
		{"ComplexMessage", createComplexMessage()},
		{"CustomTypes", createMessageWithCustomTypes()},
		{"LargeMessage", createLargeMessage()},
	}

	for _, tc := range testCases {
		isData, _ := is.Marshal(tc.input)
		hrData, _ := hr.Marshal(tc.input)
		gsData, _ := gs.Marshal(tc.input)

		t.Logf("%s:", tc.name)
		t.Logf("  InternalSerializer:      %d bytes", len(isData))
		t.Logf("  HumanReadableSerializer: %d bytes", len(hrData))
		t.Logf("  GobSerializer:           %d bytes", len(gsData))
		t.Logf("  Ratio (HR/IS):           %.2f%%", float64(len(hrData))/float64(len(isData))*100)
		t.Logf("  Ratio (Gob/IS):          %.2f%%", float64(len(gsData))/float64(len(isData))*100)
		t.Logf("  HumanReadable output:\n%s\n", string(hrData))
	}
}
