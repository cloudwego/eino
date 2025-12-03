/*
 * Copyright 2024 CloudWeGo Authors
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
	"fmt"

	"github.com/bytedance/sonic"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

// StreamFunc is the function type for the streamable tool.
type StreamFunc[T, D any] func(ctx context.Context, input T) (output *schema.StreamReader[D], err error)

// OptionableStreamFunc is the function type for the streamable tool with tool option.
type OptionableStreamFunc[T, D any] func(ctx context.Context, input T, opts ...tool.Option) (output *schema.StreamReader[D], err error)

// InferStreamTool creates an StreamableTool from a given function by inferring the ToolInfo from the function's request parameters
// End-user can pass a SchemaCustomizerFn in opts to customize the go struct tag parsing process, overriding default behavior.
func InferStreamTool[T, D any](toolName, toolDesc string, s StreamFunc[T, D], opts ...Option) (tool.StreamableTool, error) {
	ti, err := goStruct2ToolInfo[T](toolName, toolDesc, opts...)
	if err != nil {
		return nil, err
	}

	return NewStreamTool(ti, s, opts...), nil
}

// InferOptionableStreamTool creates an StreamableTool from a given function by inferring the ToolInfo from the function's request parameters, with tool option.
func InferOptionableStreamTool[T, D any](toolName, toolDesc string, s OptionableStreamFunc[T, D], opts ...Option) (tool.StreamableTool, error) {
	ti, err := goStruct2ToolInfo[T](toolName, toolDesc, opts...)
	if err != nil {
		return nil, err
	}

	return newOptionableStreamTool(ti, s, opts...), nil
}

// NewStreamTool Create a streaming tool, where the input and output are both in JSON format.
// convert: convert the stream frame to string that could be concatenated to a string.
func NewStreamTool[T, D any](desc *schema.ToolInfo, s StreamFunc[T, D], opts ...Option) tool.StreamableTool {
	return newOptionableStreamTool(desc,
		func(ctx context.Context, input T, _ ...tool.Option) (output *schema.StreamReader[D], err error) {
			return s(ctx, input)
		},
		opts...)
}

func newOptionableStreamTool[T, D any](desc *schema.ToolInfo, s OptionableStreamFunc[T, D], opts ...Option) tool.StreamableTool {

	to := getToolOptions(opts...)

	return &streamableTool[T, D]{
		info: desc,

		um: to.um,
		m:  to.m,
		Fn: s,
	}
}

type streamableTool[T, D any] struct {
	info *schema.ToolInfo

	um UnmarshalArguments
	m  MarshalOutput

	Fn OptionableStreamFunc[T, D]
}

// Info returns the tool info, implement the BaseTool interface.
func (s *streamableTool[T, D]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return s.info, nil
}

// StreamableRun invokes the tool with the given arguments, implement the StreamableTool interface.
func (s *streamableTool[T, D]) StreamableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (
	outStream *schema.StreamReader[string], err error) {

	var inst T
	if s.um != nil {
		var val any
		val, err = s.um(ctx, argumentsInJSON)
		if err != nil {
			return nil, fmt.Errorf("[LocalStreamFunc] failed to unmarshal arguments, toolName=%s, err=%w", s.getToolName(), err)
		}

		gt, ok := val.(T)
		if !ok {
			return nil, fmt.Errorf("[LocalStreamFunc] type err, toolName=%s, expected=%T, given=%T", s.getToolName(), inst, val)
		}
		inst = gt
	} else {

		inst = generic.NewInstance[T]()

		err = sonic.UnmarshalString(argumentsInJSON, &inst)
		if err != nil {
			return nil, fmt.Errorf("[LocalStreamFunc] failed to unmarshal arguments in json, toolName=%s, err=%w", s.getToolName(), err)
		}
	}

	streamD, err := s.Fn(ctx, inst, opts...)
	if err != nil {
		return nil, err
	}

	outStream = schema.StreamReaderWithConvert(streamD, func(d D) (string, error) {
		var out string
		var e error
		if s.m != nil {
			out, e = s.m(ctx, d)
			if e != nil {
				return "", fmt.Errorf("[LocalStreamFunc] failed to marshal output, toolName=%s, err=%w", s.getToolName(), e)
			}
		} else {
			out, e = marshalString(d)
			if e != nil {
				return "", fmt.Errorf("[LocalStreamFunc] failed to marshal output in json, toolName=%s, err=%w", s.getToolName(), e)
			}
		}

		return out, nil
	})

	return outStream, nil
}

func (s *streamableTool[T, D]) GetType() string {
	return snakeToCamel(s.getToolName())
}

func (s *streamableTool[T, D]) getToolName() string {
	if s.info == nil {
		return ""
	}

	return s.info.Name
}
