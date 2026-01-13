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

package prompt

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/callbacks"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
)

type mockAgenticTemplate struct {
	err error
}

func (m *mockAgenticTemplate) Format(ctx context.Context, vs map[string]any, formatType schema.FormatType) ([]*schema.AgenticMessage, error) {
	if m.err != nil {
		return nil, m.err
	}
	return []*schema.AgenticMessage{schema.UserAgenticMessage("mocked")}, nil
}

func TestFromAgenticMessages(t *testing.T) {
	t.Run("create template", func(t *testing.T) {
		tpl := schema.UserAgenticMessage("hello")
		ft := schema.FString
		at := FromAgenticMessages(ft, tpl)

		assert.NotNil(t, at)
		assert.Equal(t, ft, at.formatType)
		assert.Len(t, at.templates, 1)
		assert.Same(t, tpl, at.templates[0])
	})
}

func TestDefaultAgenticTemplate_GetType(t *testing.T) {
	t.Run("get type", func(t *testing.T) {
		at := &DefaultAgenticChatTemplate{}
		assert.Equal(t, "Default", at.GetType())
	})
}

func TestDefaultAgenticTemplate_IsCallbacksEnabled(t *testing.T) {
	t.Run("callbacks enabled", func(t *testing.T) {
		at := &DefaultAgenticChatTemplate{}
		assert.True(t, at.IsCallbacksEnabled())
	})
}

func TestDefaultAgenticTemplate_Format(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		// Mock callback handler
		cb := callbacks.NewHandlerBuilder().
			OnStartFn(func(ctx context.Context, info *callbacks.RunInfo, input callbacks.CallbackInput) context.Context {
				assert.Equal(t, "Default", info.Type)
				return ctx
			}).
			OnEndFn(func(ctx context.Context, info *callbacks.RunInfo, output callbacks.CallbackOutput) context.Context {
				assert.Equal(t, "Default", info.Type)
				return ctx
			}).
			OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				assert.Fail(t, "unexpected error callback")
				return ctx
			}).
			Build()

		tpl := schema.UserAgenticMessage("hello {val}")
		at := FromAgenticMessages(schema.FString, tpl)

		ctx := context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
			Type:      "Default",
			Component: "agentic_prompt",
		}, cb)

		res, err := at.Format(ctx, map[string]any{"val": "world"})
		assert.NoError(t, err)
		assert.Len(t, res, 1)
		assert.Equal(t, "hello world", res[0].ContentBlocks[0].UserInputText.Text)
	})

	t.Run("template format error", func(t *testing.T) {
		mockErr := errors.New("mock error")
		mockTpl := &mockAgenticTemplate{err: mockErr}
		at := FromAgenticMessages(schema.FString, mockTpl)

		// Mock callback handler to verify OnError
		cb := callbacks.NewHandlerBuilder().
			OnErrorFn(func(ctx context.Context, info *callbacks.RunInfo, err error) context.Context {
				assert.Equal(t, mockErr, err)
				return ctx
			}).
			Build()

		ctx := context.Background()
		ctx = callbacks.InitCallbacks(ctx, &callbacks.RunInfo{
			Type:      "Default",
			Component: "agentic_prompt",
		}, cb)

		res, err := at.Format(ctx, map[string]any{})
		assert.Error(t, err)
		assert.Nil(t, res)
		assert.Equal(t, mockErr, err)
	})
}
