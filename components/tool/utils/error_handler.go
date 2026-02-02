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
// This function detects the tool type (InvokableTool, StreamableTool, or both)
// and applies the appropriate error handling wrapper.
// When the wrapped tool returns an error, the error handler function 'h' will be called
// to convert the error into a string result, and no error will be returned from the wrapper.
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
