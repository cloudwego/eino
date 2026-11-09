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

package utils

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

type testErrorTool struct{}

func (t *testErrorTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return nil, nil
}

func (t *testErrorTool) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	return "", errors.New("test error")
}

func (t *testErrorTool) StreamableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	return nil, errors.New("test stream error")
}

func TestErrorWrapper(t *testing.T) {
	ctx := context.Background()

	nt := WrapToolWithErrorHandler(&testErrorTool{}, func(_ context.Context, err error) string {
		return err.Error()
	})
	result, err := nt.(tool.InvokableTool).InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Equal(t, "test error", result)
	streamResult, err := nt.(tool.StreamableTool).StreamableRun(ctx, "")
	assert.NoError(t, err)
	chunk, err := streamResult.Recv()
	assert.NoError(t, err)
	assert.Equal(t, "test stream error", chunk)
	_, err = streamResult.Recv()
	assert.True(t, errors.Is(err, io.EOF))

	wrappedTool := WrapInvokableToolWithErrorHandler(&testErrorTool{}, func(_ context.Context, err error) string {
		return err.Error()
	})
	result, err = wrappedTool.InvokableRun(ctx, "")
	assert.NoError(t, err)
	assert.Equal(t, "test error", result)

	wrappedStreamTool := WrapStreamableToolWithErrorHandler(&testErrorTool{}, func(_ context.Context, err error) string {
		return err.Error()
	})

	streamResult, err = wrappedStreamTool.StreamableRun(ctx, "")
	assert.NoError(t, err)

	chunk, err = streamResult.Recv()
	assert.NoError(t, err)
	assert.Equal(t, "test stream error", chunk)
	_, err = streamResult.Recv()
	assert.True(t, errors.Is(err, io.EOF))
}

type testEnhancedErrorTool struct{}

func (t *testEnhancedErrorTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return nil, nil
}

func (t *testEnhancedErrorTool) InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
	return nil, errors.New("test enhanced error")
}

func (t *testEnhancedErrorTool) StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	return nil, errors.New("test enhanced stream error")
}

type testEnhancedOkTool struct {
	result *schema.ToolResult
	err    error
}

func (t *testEnhancedOkTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return nil, nil
}

func (t *testEnhancedOkTool) InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
	return t.result, t.err
}

func (t *testEnhancedOkTool) StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	if t.err != nil {
		return nil, t.err
	}
	return schema.StreamReaderFromArray([]*schema.ToolResult{t.result}), nil
}

func textResult(text string) *schema.ToolResult {
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: text}}}
}

func assertTextResult(t *testing.T, result *schema.ToolResult, expected string) {
	t.Helper()
	if assert.NotNil(t, result) {
		assert.Equal(t, []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: expected}}, result.Parts)
	}
}

func TestEnhancedErrorWrapper(t *testing.T) {
	ctx := context.Background()

	calls := 0
	handler := func(_ context.Context, err error) string {
		calls++
		return err.Error()
	}

	nt := WrapToolWithErrorHandler(&testEnhancedErrorTool{}, handler)

	enhancedResult, err := nt.(tool.EnhancedInvokableTool).InvokableRun(ctx, &schema.ToolArgument{Text: "{}"})
	assert.NoError(t, err)
	assertTextResult(t, enhancedResult, "test enhanced error")

	enhancedStream, err := nt.(tool.EnhancedStreamableTool).StreamableRun(ctx, &schema.ToolArgument{Text: "{}"})
	assert.NoError(t, err)
	streamChunk, err := enhancedStream.Recv()
	assert.NoError(t, err)
	assertTextResult(t, streamChunk, "test enhanced stream error")
	_, err = enhancedStream.Recv()
	assert.True(t, errors.Is(err, io.EOF))

	assert.Equal(t, 2, calls)

	wrappedEnhancedTool := WrapEnhancedInvokableToolWithErrorHandler(&testEnhancedErrorTool{}, handler)
	enhancedResult, err = wrappedEnhancedTool.InvokableRun(ctx, &schema.ToolArgument{Text: "{}"})
	assert.NoError(t, err)
	assertTextResult(t, enhancedResult, "test enhanced error")

	wrappedEnhancedStreamTool := WrapEnhancedStreamableToolWithErrorHandler(&testEnhancedErrorTool{}, handler)
	enhancedStream, err = wrappedEnhancedStreamTool.StreamableRun(ctx, &schema.ToolArgument{Text: "{}"})
	assert.NoError(t, err)
	streamChunk, err = enhancedStream.Recv()
	assert.NoError(t, err)
	assertTextResult(t, streamChunk, "test enhanced stream error")

	assert.Equal(t, 4, calls)
}

func TestEnhancedErrorWrapperPassesThroughResult(t *testing.T) {
	ctx := context.Background()
	original := textResult("reserved 3 items")
	calls := 0
	handler := func(_ context.Context, _ error) string { calls++; return "should not be used" }

	t.Run("invokable", func(t *testing.T) {
		wrapped := WrapToolWithErrorHandler(&testEnhancedOkTool{result: original}, handler)
		result, err := wrapped.(tool.EnhancedInvokableTool).InvokableRun(ctx, &schema.ToolArgument{Text: "{}"})
		assert.NoError(t, err)
		assert.Same(t, original, result)
	})

	t.Run("streamable", func(t *testing.T) {
		wrapped := WrapToolWithErrorHandler(&testEnhancedOkTool{result: original}, handler)
		stream, err := wrapped.(tool.EnhancedStreamableTool).StreamableRun(ctx, &schema.ToolArgument{Text: "{}"})
		assert.NoError(t, err)
		chunk, err := stream.Recv()
		assert.NoError(t, err)
		assert.Same(t, original, chunk)
	})

	assert.Equal(t, 0, calls)
}

func TestEnhancedErrorWrapperKeepsInterrupt(t *testing.T) {
	ctx := context.Background()
	interruptErr := compose.NewInterruptAndRerunErr("needs confirmation")
	calls := 0
	handler := func(_ context.Context, _ error) string { calls++; return "should not be used" }

	t.Run("invokable", func(t *testing.T) {
		wrapped := WrapToolWithErrorHandler(&testEnhancedOkTool{err: interruptErr}, handler)
		result, err := wrapped.(tool.EnhancedInvokableTool).InvokableRun(ctx, &schema.ToolArgument{Text: "{}"})
		assert.Nil(t, result)
		assert.True(t, errors.Is(err, interruptErr))
		_, ok := compose.IsInterruptRerunError(err)
		assert.True(t, ok)
	})

	t.Run("streamable", func(t *testing.T) {
		wrapped := WrapToolWithErrorHandler(&testEnhancedOkTool{err: interruptErr}, handler)
		stream, err := wrapped.(tool.EnhancedStreamableTool).StreamableRun(ctx, &schema.ToolArgument{Text: "{}"})
		assert.Nil(t, stream)
		assert.True(t, errors.Is(err, interruptErr))
		_, ok := compose.IsInterruptRerunError(err)
		assert.True(t, ok)
	})

	assert.Equal(t, 0, calls)
}

func TestWrapToolWithErrorHandlerKeepsUnsupportedTool(t *testing.T) {
	original := &infoOnlyTool{}
	wrapped := WrapToolWithErrorHandler(original, func(_ context.Context, _ error) string { return "unused" })
	assert.Same(t, tool.BaseTool(original), wrapped)
}

type infoOnlyTool struct{}

func (i *infoOnlyTool) Info(ctx context.Context) (*schema.ToolInfo, error) { return nil, nil }
