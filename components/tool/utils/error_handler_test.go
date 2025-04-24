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
