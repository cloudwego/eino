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

package filesystem

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// mockBackend is a simple in-memory backend for testing
type mockBackend struct {
	files map[string]string
}

func newMockBackend() *mockBackend {
	return &mockBackend{
		files: make(map[string]string),
	}
}

func (m *mockBackend) Write(ctx context.Context, req *WriteRequest) error {
	m.files[req.FilePath] = req.Content
	return nil
}

func (m *mockBackend) Read(ctx context.Context, req *ReadRequest) (string, error) {
	content, ok := m.files[req.FilePath]
	if !ok {
		return "", errors.New("file not found")
	}
	return content, nil
}

func (m *mockBackend) LsInfo(ctx context.Context, _ *LsInfoRequest) ([]FileInfo, error) {
	return nil, nil
}

func (m *mockBackend) GrepRaw(ctx context.Context, _ *GrepRequest) ([]GrepMatch, error) {
	return nil, nil
}

func (m *mockBackend) GlobInfo(ctx context.Context, _ *GlobInfoRequest) ([]FileInfo, error) {
	return nil, nil
}

func (m *mockBackend) Edit(ctx context.Context, _ *EditRequest) error {
	return nil
}

func TestToolResultOffloading_SmallResult(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 100, // Small limit for testing
	}

	middleware := newToolResultOffloading(ctx, config)

	// Create a mock endpoint that returns a small result
	smallResult := "This is a small result"
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: smallResult}, nil
	}

	// Wrap the endpoint with the middleware
	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	// Execute
	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_123",
	}
	output, err := wrappedEndpoint(ctx, input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Small result should pass through unchanged
	if output.Result != smallResult {
		t.Errorf("expected result %q, got %q", smallResult, output.Result)
	}

	// No file should be written
	if len(backend.files) != 0 {
		t.Errorf("expected no files to be written, got %d files", len(backend.files))
	}
}

func TestToolResultOffloading_LargeResult(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 10, // Very small limit to trigger offloading
	}

	middleware := newToolResultOffloading(ctx, config)

	// Create a large result (more than 10 * 4 = 40 bytes)
	largeResult := strings.Repeat("This is a long line of text that will exceed the token limit.\n", 10)
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: largeResult}, nil
	}

	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_456",
	}
	output, err := wrappedEndpoint(ctx, input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Result should be replaced with a message
	if !strings.Contains(output.Result, "Tool result too large") {
		t.Errorf("expected result to contain 'Tool result too large', got %q", output.Result)
	}

	if !strings.Contains(output.Result, "call_456") {
		t.Errorf("expected result to contain call ID 'call_456', got %q", output.Result)
	}

	if !strings.Contains(output.Result, "/large_tool_result/call_456") {
		t.Errorf("expected result to contain file path, got %q", output.Result)
	}

	// File should be written
	if len(backend.files) != 1 {
		t.Fatalf("expected 1 file to be written, got %d files", len(backend.files))
	}

	savedContent, ok := backend.files["/large_tool_result/call_456"]
	if !ok {
		t.Fatalf("expected file at /large_tool_result/call_456, got files: %v", backend.files)
	}

	if savedContent != largeResult {
		t.Errorf("saved content doesn't match original result")
	}
}

func TestToolResultOffloading_CustomPathGenerator(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	customPath := "/custom/path/result.txt"
	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 10,
		PathGenerator: func(ctx context.Context, input *compose.ToolInput) (string, error) {
			return customPath, nil
		},
	}

	middleware := newToolResultOffloading(ctx, config)

	largeResult := strings.Repeat("Large content ", 100)
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: largeResult}, nil
	}

	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_789",
	}
	output, err := wrappedEndpoint(ctx, input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Check custom path is used
	if !strings.Contains(output.Result, customPath) {
		t.Errorf("expected result to contain custom path %q, got %q", customPath, output.Result)
	}

	// File should be written to custom path
	savedContent, ok := backend.files[customPath]
	if !ok {
		t.Fatalf("expected file at %q, got files: %v", customPath, backend.files)
	}

	if savedContent != largeResult {
		t.Errorf("saved content doesn't match original result")
	}
}

func TestToolResultOffloading_PathGeneratorError(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	expectedErr := errors.New("path generation failed")
	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 10,
		PathGenerator: func(ctx context.Context, input *compose.ToolInput) (string, error) {
			return "", expectedErr
		},
	}

	middleware := newToolResultOffloading(ctx, config)

	largeResult := strings.Repeat("Large content ", 100)
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: largeResult}, nil
	}

	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_error",
	}
	_, err := wrappedEndpoint(ctx, input)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestToolResultOffloading_EndpointError(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 100,
	}

	middleware := newToolResultOffloading(ctx, config)

	expectedErr := errors.New("endpoint execution failed")
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return nil, expectedErr
	}

	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_endpoint_error",
	}
	_, err := wrappedEndpoint(ctx, input)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestToolResultOffloading_DefaultTokenLimit(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 0, // Should default to 20000
	}

	middleware := newToolResultOffloading(ctx, config)

	// Create a result smaller than 20000 * 4 = 80000 bytes
	smallResult := strings.Repeat("x", 1000)
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: smallResult}, nil
	}

	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_default",
	}
	output, err := wrappedEndpoint(ctx, input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should pass through unchanged
	if output.Result != smallResult {
		t.Errorf("expected result to pass through unchanged")
	}

	// No file should be written
	if len(backend.files) != 0 {
		t.Errorf("expected no files to be written, got %d files", len(backend.files))
	}
}

func TestToolResultOffloading_Stream(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 10,
	}

	middleware := newToolResultOffloading(ctx, config)

	// Create a streaming endpoint that returns large content
	largeResult := strings.Repeat("Large streaming content ", 100)
	mockStreamEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		// Split the result into chunks
		chunks := []string{largeResult[:len(largeResult)/2], largeResult[len(largeResult)/2:]}
		return &compose.StreamToolOutput{
			Result: schema.StreamReaderFromArray(chunks),
		}, nil
	}

	wrappedEndpoint := middleware.Streamable(mockStreamEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_stream",
	}
	output, err := wrappedEndpoint(ctx, input)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Read the stream
	var result strings.Builder
	for {
		chunk, err := output.Result.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("error reading stream: %v", err)
		}
		result.WriteString(chunk)
	}

	resultStr := result.String()

	// Result should be replaced with a message
	if !strings.Contains(resultStr, "Tool result too large") {
		t.Errorf("expected result to contain 'Tool result too large', got %q", resultStr)
	}

	if !strings.Contains(resultStr, "call_stream") {
		t.Errorf("expected result to contain call ID 'call_stream', got %q", resultStr)
	}

	// File should be written
	if len(backend.files) != 1 {
		t.Fatalf("expected 1 file to be written, got %d files", len(backend.files))
	}

	savedContent, ok := backend.files["/large_tool_result/call_stream"]
	if !ok {
		t.Fatalf("expected file at /large_tool_result/call_stream, got files: %v", backend.files)
	}

	if savedContent != largeResult {
		t.Errorf("saved content doesn't match original result")
	}
}

func TestToolResultOffloading_StreamError(t *testing.T) {
	ctx := context.Background()
	backend := newMockBackend()

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 10,
	}

	middleware := newToolResultOffloading(ctx, config)

	expectedErr := errors.New("stream endpoint failed")
	mockStreamEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.StreamToolOutput, error) {
		return nil, expectedErr
	}

	wrappedEndpoint := middleware.Streamable(mockStreamEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_stream_error",
	}
	_, err := wrappedEndpoint(ctx, input)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, expectedErr) {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestFormatToolMessage(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "single line",
			input:    "single line",
			expected: "1: single line\n",
		},
		{
			name:     "multiple lines",
			input:    "line1\nline2\nline3",
			expected: "1: line1\n2: line2\n3: line3\n",
		},
		{
			name:     "more than 10 lines",
			input:    "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12",
			expected: "1: 1\n2: 2\n3: 3\n4: 4\n5: 5\n6: 6\n7: 7\n8: 8\n9: 9\n10: 10\n",
		},
		{
			name:     "long line truncation",
			input:    strings.Repeat("a", 1500),
			expected: fmt.Sprintf("1: %s\n", strings.Repeat("a", 1000)),
		},
		{
			name:     "unicode characters",
			input:    "你好世界\n测试",
			expected: "1: 你好世界\n2: 测试\n",
		},
		{
			name:     "long unicode line",
			input:    strings.Repeat("你", 1500),
			expected: fmt.Sprintf("1: %s\n", strings.Repeat("你", 1000)),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatToolMessage(tt.input)
			if result != tt.expected {
				t.Errorf("formatToolMessage() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestConcatString(t *testing.T) {
	tests := []struct {
		name        string
		chunks      []string
		expected    string
		expectError bool
	}{
		{
			name:     "single chunk",
			chunks:   []string{"hello"},
			expected: "hello",
		},
		{
			name:     "multiple chunks",
			chunks:   []string{"hello", " ", "world"},
			expected: "hello world",
		},
		{
			name:     "empty chunks",
			chunks:   []string{"", "", ""},
			expected: "",
		},
		{
			name:     "mixed chunks",
			chunks:   []string{"a", "", "b", "c"},
			expected: "abc",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sr := schema.StreamReaderFromArray(tt.chunks)
			result, err := concatString(sr)

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if result != tt.expected {
				t.Errorf("concatString() = %q, want %q", result, tt.expected)
			}
		})
	}

	// Test nil stream
	t.Run("nil stream", func(t *testing.T) {
		_, err := concatString(nil)
		if err == nil {
			t.Error("expected error for nil stream, got nil")
		}
		if !strings.Contains(err.Error(), "stream is nil") {
			t.Errorf("expected 'stream is nil' error, got %v", err)
		}
	})
}

func TestToolResultOffloading_BackendWriteError(t *testing.T) {
	ctx := context.Background()

	// Create a backend that fails on write
	backend := &failingBackend{
		writeErr: errors.New("write failed"),
	}

	config := &toolResultOffloadingConfig{
		FS:         backend,
		TokenLimit: 10,
	}

	middleware := newToolResultOffloading(ctx, config)

	largeResult := strings.Repeat("Large content ", 100)
	mockEndpoint := func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		return &compose.ToolOutput{Result: largeResult}, nil
	}

	wrappedEndpoint := middleware.Invokable(mockEndpoint)

	input := &compose.ToolInput{
		Name:   "test_tool",
		CallID: "call_write_error",
	}
	_, err := wrappedEndpoint(ctx, input)

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !strings.Contains(err.Error(), "write failed") {
		t.Errorf("expected 'write failed' error, got %v", err)
	}
}

// failingBackend is a mock backend that can be configured to fail
type failingBackend struct {
	writeErr error
}

func (f *failingBackend) Write(ctx context.Context, req *WriteRequest) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	return nil
}

func (f *failingBackend) Read(ctx context.Context, req *ReadRequest) (string, error) {
	return "", nil
}

func (f *failingBackend) LsInfo(ctx context.Context, _ *LsInfoRequest) ([]FileInfo, error) {
	return nil, nil
}

func (f *failingBackend) GrepRaw(ctx context.Context, _ *GrepRequest) ([]GrepMatch, error) {
	return nil, nil
}

func (f *failingBackend) GlobInfo(ctx context.Context, _ *GlobInfoRequest) ([]FileInfo, error) {
	return nil, nil
}

func (f *failingBackend) Edit(ctx context.Context, _ *EditRequest) error {
	return nil
}
