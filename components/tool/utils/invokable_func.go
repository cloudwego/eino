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
	"strings"

	"github.com/bytedance/sonic"
	"github.com/eino-contrib/jsonschema"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/internal/generic"
	"github.com/cloudwego/eino/schema"
)

// InvokeFunc is the function type for the tool.
type InvokeFunc[T, D any] func(ctx context.Context, input T) (output D, err error)

// OptionableInvokeFunc is the function type for the tool with tool option.
type OptionableInvokeFunc[T, D any] func(ctx context.Context, input T, opts ...tool.Option) (output D, err error)

// InferTool creates an InvokableTool from a given function by inferring the ToolInfo from the function's request parameters.
// End-user can pass a SchemaCustomizerFn in opts to customize the go struct tag parsing process, overriding default behavior.
func InferTool[T, D any](toolName, toolDesc string, i InvokeFunc[T, D], opts ...Option) (tool.InvokableTool, error) {
	ti, err := goStruct2ToolInfo[T](toolName, toolDesc, opts...)
	if err != nil {
		return nil, err
	}

	return NewTool(ti, i, opts...), nil
}

// InferOptionableTool creates an InvokableTool from a given function by inferring the ToolInfo from the function's request parameters, with tool option.
func InferOptionableTool[T, D any](toolName, toolDesc string, i OptionableInvokeFunc[T, D], opts ...Option) (tool.InvokableTool, error) {
	ti, err := goStruct2ToolInfo[T](toolName, toolDesc, opts...)
	if err != nil {
		return nil, err
	}

	return newOptionableTool(ti, i, opts...), nil
}

// EnhancedInvokeFunc is the function type for the enhanced tool.
type EnhancedInvokeFunc[T any] func(ctx context.Context, input T) (output *schema.ToolResult, err error)

// OptionableEnhancedInvokeFunc is the function type for the enhanced tool with tool option.
type OptionableEnhancedInvokeFunc[T any] func(ctx context.Context, input T, opts ...tool.Option) (output *schema.ToolResult, err error)

// InferEnhancedTool creates an EnhancedInvokableTool from a given function by inferring the ToolInfo from the function's request parameters.
// End-user can pass a SchemaCustomizerFn in opts to customize the go struct tag parsing process, overriding default behavior.
func InferEnhancedTool[T any](toolName, toolDesc string, i EnhancedInvokeFunc[T], opts ...Option) (tool.EnhancedInvokableTool, error) {
	ti, err := goStruct2ToolInfo[T](toolName, toolDesc, opts...)
	if err != nil {
		return nil, err
	}

	return NewEnhancedTool(ti, i, opts...), nil
}

// InferOptionableEnhancedTool creates an EnhancedInvokableTool from a given function by inferring the ToolInfo from the function's request parameters, with tool option.
func InferOptionableEnhancedTool[T any](toolName, toolDesc string, i OptionableEnhancedInvokeFunc[T], opts ...Option) (tool.EnhancedInvokableTool, error) {
	ti, err := goStruct2ToolInfo[T](toolName, toolDesc, opts...)
	if err != nil {
		return nil, err
	}

	return newOptionableEnhancedTool(ti, i, opts...), nil
}

// GoStruct2ParamsOneOf converts a go struct to a ParamsOneOf.
// if you attempt to use ResponseFormat of some ChatModel to get StructuredOutput, you can infer the JSONSchema from the go struct.
func GoStruct2ParamsOneOf[T any](opts ...Option) (*schema.ParamsOneOf, error) {
	return goStruct2ParamsOneOf[T](opts...)
}

// GoStruct2ToolInfo converts a go struct to a ToolInfo.
// if you attempt to use BindTool to make ChatModel respond StructuredOutput, you can infer the ToolInfo from the go struct.
func GoStruct2ToolInfo[T any](toolName, toolDesc string, opts ...Option) (*schema.ToolInfo, error) {
	return goStruct2ToolInfo[T](toolName, toolDesc, opts...)
}

func goStruct2ToolInfo[T any](toolName, toolDesc string, opts ...Option) (*schema.ToolInfo, error) {
	paramsOneOf, err := goStruct2ParamsOneOf[T](opts...)
	if err != nil {
		return nil, err
	}
	return &schema.ToolInfo{
		Name:        toolName,
		Desc:        toolDesc,
		ParamsOneOf: paramsOneOf,
	}, nil
}

func goStruct2ParamsOneOf[T any](opts ...Option) (*schema.ParamsOneOf, error) {
	options := getToolOptions(opts...)

	r := &jsonschema.Reflector{
		Anonymous:      true,
		DoNotReference: true,
		SchemaModifier: jsonschema.SchemaModifierFn(options.scModifier),
	}

	js := r.Reflect(generic.NewInstance[T]())
	js.Version = ""

	paramsOneOf := schema.NewParamsOneOfByJSONSchema(js)

	return paramsOneOf, nil
}

// NewTool Create a tool, where the input and output are both in JSON format.
func NewTool[T, D any](desc *schema.ToolInfo, i InvokeFunc[T, D], opts ...Option) tool.InvokableTool {
	return newOptionableTool(desc, func(ctx context.Context, input T, _ ...tool.Option) (D, error) {
		return i(ctx, input)
	}, opts...)
}

func newOptionableTool[T, D any](desc *schema.ToolInfo, i OptionableInvokeFunc[T, D], opts ...Option) tool.InvokableTool {
	to := getToolOptions(opts...)

	return &invokableTool[T, D]{
		info: desc,
		um:   to.um,
		m:    to.m,
		Fn:   i,
	}
}

type invokableTool[T, D any] struct {
	info *schema.ToolInfo

	um UnmarshalArguments
	m  MarshalOutput

	Fn OptionableInvokeFunc[T, D]
}

func (i *invokableTool[T, D]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return i.info, nil
}

// InvokableRun invokes the tool with the given arguments.
func (i *invokableTool[T, D]) InvokableRun(ctx context.Context, arguments string, opts ...tool.Option) (output string, err error) {

	var inst T
	if i.um != nil {
		var val any
		val, err = i.um(ctx, arguments)
		if err != nil {
			return "", fmt.Errorf("[LocalFunc] failed to unmarshal arguments, toolName=%s, err=%w", i.getToolName(), err)
		}
		gt, ok := val.(T)
		if !ok {
			return "", fmt.Errorf("[LocalFunc] invalid type, toolName=%s, expected=%T, given=%T", i.getToolName(), inst, val)
		}
		inst = gt
	} else {
		inst = generic.NewInstance[T]()

		err = sonic.UnmarshalString(arguments, &inst)
		if err != nil {
			return "", fmt.Errorf("[LocalFunc] failed to unmarshal arguments in json, toolName=%s, err=%w", i.getToolName(), err)
		}
	}

	resp, err := i.Fn(ctx, inst, opts...)
	if err != nil {
		return "", fmt.Errorf("[LocalFunc] failed to invoke tool, toolName=%s, err=%w", i.getToolName(), err)
	}

	if i.m != nil {
		output, err = i.m(ctx, resp)
		if err != nil {
			return "", fmt.Errorf("[LocalFunc] failed to marshal output, toolName=%s, err=%w", i.getToolName(), err)
		}
	} else {
		output, err = marshalString(resp)
		if err != nil {
			return "", fmt.Errorf("[LocalFunc] failed to marshal output in json, toolName=%s, err=%w", i.getToolName(), err)
		}
	}

	return output, nil
}

func (i *invokableTool[T, D]) GetType() string {
	return snakeToCamel(i.getToolName())
}

func (i *invokableTool[T, D]) getToolName() string {
	if i.info == nil {
		return ""
	}

	return i.info.Name
}

// snakeToCamel converts a snake_case string to CamelCase.
func snakeToCamel(s string) string {
	if s == "" {
		return ""
	}

	parts := strings.Split(s, "_")

	for i := 0; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(string(parts[i][0])) + strings.ToLower(parts[i][1:])
		}
	}

	return strings.Join(parts, "")
}

// NewEnhancedTool Create an enhanced tool, where the input is in JSON format and output is *schema.ToolResult.
func NewEnhancedTool[T any](desc *schema.ToolInfo, i EnhancedInvokeFunc[T], opts ...Option) tool.EnhancedInvokableTool {
	return newOptionableEnhancedTool(desc, func(ctx context.Context, input T, _ ...tool.Option) (*schema.ToolResult, error) {
		return i(ctx, input)
	}, opts...)
}

func newOptionableEnhancedTool[T any](desc *schema.ToolInfo, i OptionableEnhancedInvokeFunc[T], opts ...Option) tool.EnhancedInvokableTool {
	to := getToolOptions(opts...)

	return &enhancedInvokableTool[T]{
		info: desc,
		um:   to.um,
		Fn:   i,
	}
}

type enhancedInvokableTool[T any] struct {
	info *schema.ToolInfo

	um UnmarshalArguments

	Fn OptionableEnhancedInvokeFunc[T]
}

func (e *enhancedInvokableTool[T]) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return e.info, nil
}

func (e *enhancedInvokableTool[T]) InvokableRun(ctx context.Context, toolArgument *schema.ToolArgument, opts ...tool.Option) (*schema.ToolResult, error) {
	var inst T
	var err error

	if e.um != nil {
		var val any
		val, err = e.um(ctx, toolArgument.TextArgument)
		if err != nil {
			return nil, fmt.Errorf("[EnhancedLocalFunc] failed to unmarshal arguments, toolName=%s, err=%w", e.getToolName(), err)
		}
		gt, ok := val.(T)
		if !ok {
			return nil, fmt.Errorf("[EnhancedLocalFunc] invalid type, toolName=%s, expected=%T, given=%T", e.getToolName(), inst, val)
		}
		inst = gt
	} else {
		inst = generic.NewInstance[T]()

		err = sonic.UnmarshalString(toolArgument.TextArgument, &inst)
		if err != nil {
			return nil, fmt.Errorf("[EnhancedLocalFunc] failed to unmarshal arguments in json, toolName=%s, err=%w", e.getToolName(), err)
		}
	}

	resp, err := e.Fn(ctx, inst, opts...)
	if err != nil {
		return nil, fmt.Errorf("[EnhancedLocalFunc] failed to invoke tool, toolName=%s, err=%w", e.getToolName(), err)
	}

	return resp, nil
}

func (e *enhancedInvokableTool[T]) GetType() string {
	return snakeToCamel(e.getToolName())
}

func (e *enhancedInvokableTool[T]) getToolName() string {
	if e.info == nil {
		return ""
	}

	return e.info.Name
}
