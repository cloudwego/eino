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

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// ErrorHandler converts a tool error into a string response.
type ErrorHandler func(context.Context, error) string

// WrapToolWithErrorHandler wraps any BaseTool with custom error handling.
// This function detects the tool type (InvokableTool, StreamableTool,
// EnhancedInvokableTool, EnhancedStreamableTool, or both variants of a pair)
// and applies the appropriate error handling wrapper.
// When the wrapped tool returns an error, the error handler function 'h' will be called
// to convert the error into a string result, and no error will be returned from the wrapper.
// Enhanced tools receive the same string as a single text part of a [schema.ToolResult].
//
// Parameters:
//   - t: The original BaseTool to be wrapped
//   - h: A function that converts an error to a string
//
// Returns:
//   - A wrapped BaseTool that handles errors internally based on its capabilities
func WrapToolWithErrorHandler(t tool.BaseTool, h ErrorHandler) tool.BaseTool {
	ih := &infoHelper{info: t.Info}
	var s tool.StreamableTool
	if st, ok := t.(tool.StreamableTool); ok {
		s = st
	}
	if it, ok := t.(tool.InvokableTool); ok {
		if s == nil {
			return WrapInvokableToolWithErrorHandler(it, h)
		} else {
			return &combinedErrorWrapper{
				infoHelper: ih,
				errorHelper: &errorHelper{
					i: it.InvokableRun,
					h: h,
				},
				streamErrorHelper: &streamErrorHelper{
					s: s.StreamableRun,
					h: h,
				},
			}
		}
	}
	if s != nil {
		return WrapStreamableToolWithErrorHandler(s, h)
	}
	if ei, ok := t.(tool.EnhancedInvokableTool); ok {
		if es, ok := t.(tool.EnhancedStreamableTool); ok {
			return &combinedEnhancedErrorWrapper{
				infoHelper: ih,
				enhancedErrorHandler: &enhancedErrorHandler{
					i: ei.InvokableRun,
					h: h,
				},
				enhancedStreamErrorHelper: &enhancedStreamErrorHelper{
					s: es.StreamableRun,
					h: h,
				},
			}
		}
		return WrapEnhancedInvokableToolWithErrorHandler(ei, h)
	}
	if es, ok := t.(tool.EnhancedStreamableTool); ok {
		return WrapEnhancedStreamableToolWithErrorHandler(es, h)
	}
	return t
}

// WrapInvokableToolWithErrorHandler wraps an InvokableTool with custom error handling.
// When the wrapped tool returns an error, the error handler function 'h' will be called
// to convert the error into a string result, and no error will be returned from the wrapper.
//
// Parameters:
//   - tool: The original InvokableTool to be wrapped
//   - h: A function that converts an error to a string
//
// Returns:
//   - A wrapped InvokableTool that handles errors internally
func WrapInvokableToolWithErrorHandler(t tool.InvokableTool, h ErrorHandler) tool.InvokableTool {
	return &errorWrapper{
		infoHelper: &infoHelper{info: t.Info},
		errorHelper: &errorHelper{
			i: t.InvokableRun,
			h: h,
		},
	}
}

// WrapStreamableToolWithErrorHandler wraps a StreamableTool with custom error handling.
// When the wrapped tool returns an error, the error handler function 'h' will be called
// to convert the error into a string result, which will be returned as a single-item stream,
// and no error will be returned from the wrapper.
//
// Parameters:
//   - tool: The original StreamableTool to be wrapped
//   - h: A function that converts an error to a string
//
// Returns:
//   - A wrapped StreamableTool that handles errors internally
func WrapStreamableToolWithErrorHandler(t tool.StreamableTool, h ErrorHandler) tool.StreamableTool {
	return &streamErrorWrapper{
		infoHelper: &infoHelper{info: t.Info},
		streamErrorHelper: &streamErrorHelper{
			s: t.StreamableRun,
			h: h,
		},
	}
}

// WrapEnhancedInvokableToolWithErrorHandler wraps an EnhancedInvokableTool with
// custom error handling. When the wrapped tool returns an error, the error
// handler function 'h' will be called to convert the error into a string result,
// which is returned as a single text part of a [schema.ToolResult], and no error
// will be returned from the wrapper.
//
// Parameters:
//   - tool: The original EnhancedInvokableTool to be wrapped
//   - h: A function that converts an error to a string
//
// Returns:
//   - A wrapped EnhancedInvokableTool that handles errors internally
func WrapEnhancedInvokableToolWithErrorHandler(t tool.EnhancedInvokableTool, h ErrorHandler) tool.EnhancedInvokableTool {
	return &enhancedErrorWrapper{
		infoHelper: &infoHelper{info: t.Info},
		enhancedErrorHandler: &enhancedErrorHandler{
			i: t.InvokableRun,
			h: h,
		},
	}
}

// WrapEnhancedStreamableToolWithErrorHandler wraps an EnhancedStreamableTool with
// custom error handling. When the wrapped tool returns an error, the error handler
// function 'h' will be called to convert the error into a string result, which will
// be returned as a single-item stream of a [schema.ToolResult] text part, and no
// error will be returned from the wrapper.
//
// Parameters:
//   - tool: The original EnhancedStreamableTool to be wrapped
//   - h: A function that converts an error to a string
//
// Returns:
//   - A wrapped EnhancedStreamableTool that handles errors internally
func WrapEnhancedStreamableToolWithErrorHandler(t tool.EnhancedStreamableTool, h ErrorHandler) tool.EnhancedStreamableTool {
	return &enhancedStreamErrorWrapper{
		infoHelper: &infoHelper{info: t.Info},
		enhancedStreamErrorHelper: &enhancedStreamErrorHelper{
			s: t.StreamableRun,
			h: h,
		},
	}
}

type errorWrapper struct {
	*infoHelper
	*errorHelper
}

type streamErrorWrapper struct {
	*infoHelper
	*streamErrorHelper
}

type combinedErrorWrapper struct {
	*infoHelper
	*errorHelper
	*streamErrorHelper
}

type enhancedErrorWrapper struct {
	*infoHelper
	*enhancedErrorHandler
}

type enhancedStreamErrorWrapper struct {
	*infoHelper
	*enhancedStreamErrorHelper
}

type combinedEnhancedErrorWrapper struct {
	*infoHelper
	*enhancedErrorHandler
	*enhancedStreamErrorHelper
}

type infoHelper struct {
	info func(ctx context.Context) (*schema.ToolInfo, error)
}

func (i *infoHelper) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return i.info(ctx)
}

type errorHelper struct {
	i func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error)
	h ErrorHandler
}

func (s *errorHelper) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	result, err := s.i(ctx, argumentsInJSON, opts...)
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return result, err
	}
	if err != nil {
		return s.h(ctx, err), nil
	}
	return result, nil
}

type streamErrorHelper struct {
	s func(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error)
	h ErrorHandler
}

func (s *streamErrorHelper) StreamableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (*schema.StreamReader[string], error) {
	result, err := s.s(ctx, argumentsInJSON, opts...)
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return result, err
	}
	if err != nil {
		return schema.StreamReaderFromArray([]string{s.h(ctx, err)}), nil
	}
	return result, nil
}

type enhancedErrorHandler struct {
	i func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error)
	h ErrorHandler
}

func (e *enhancedErrorHandler) InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
	result, err := e.i(ctx, toolArgument, opts...)
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return result, err
	}
	if err != nil {
		return textToolResult(e.h(ctx, err)), nil
	}
	return result, nil
}

type enhancedStreamErrorHelper struct {
	s func(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error)
	h ErrorHandler
}

func (e *enhancedStreamErrorHelper) StreamableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.StreamReader[*schema.ToolResult], error) {
	result, err := e.s(ctx, toolArgument, opts...)
	if _, ok := compose.IsInterruptRerunError(err); ok {
		return result, err
	}
	if err != nil {
		return schema.StreamReaderFromArray([]*schema.ToolResult{textToolResult(e.h(ctx, err))}), nil
	}
	return result, nil
}

func textToolResult(text string) *schema.ToolResult {
	return &schema.ToolResult{Parts: []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: text}}}
}
