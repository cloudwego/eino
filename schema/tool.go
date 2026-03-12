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

package schema

import (
	"sort"

	"github.com/eino-contrib/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// DataType is the type of the parameter.
// It must be one of the following values: "object", "number", "integer", "string", "array", "null", "boolean", which is the same as the type of the parameter in JSONSchema.
type DataType string

// Supported JSONSchema data types for tool parameters.
const (
	Object  DataType = "object"
	Number  DataType = "number"
	Integer DataType = "integer"
	String  DataType = "string"
	Array   DataType = "array"
	Null    DataType = "null"
	Boolean DataType = "boolean"
)

// ToolChoice controls how the model uses the tools provided to it.
// Pass as part of the model option via [model.WithToolChoice].
type ToolChoice string

const (
	// ToolChoiceForbidden instructs the model not to call any tools, even if
	// tools are bound. The model responds with a plain text message instead.
	// Corresponds to "none" in OpenAI Chat Completion.
	ToolChoiceForbidden ToolChoice = "forbidden"

	// ToolChoiceAllowed lets the model decide: it may generate a plain message
	// or call one or more tools. This is the default when tools are provided.
	// Corresponds to "auto" in OpenAI Chat Completion.
	ToolChoiceAllowed ToolChoice = "allowed"

	// ToolChoiceForced requires the model to call at least one tool. Use this
	// when you want to guarantee structured output via tool calling.
	// Corresponds to "required" in OpenAI Chat Completion.
	ToolChoiceForced ToolChoice = "forced"
)

// ToolInfo describes a tool that can be passed to a ChatModel via
// [ToolCallingChatModel.WithTools] or [ChatModel.BindTools].
//
// Name should be concise and unique within the tool set. Desc should explain
// when and why to use the tool; few-shot examples in Desc significantly improve
// model accuracy. ParamsOneOf may be nil for tools that take no arguments.
type ToolInfo struct {
	// The unique name of the tool that clearly communicates its purpose.
	Name string
	// Used to tell the model how/when/why to use the tool.
	// You can provide few-shot examples as a part of the description.
	Desc string
	// Extra is the extra information for the tool.
	Extra map[string]any

	// The parameters the functions accepts (different models may require different parameter types).
	// can be described in two ways:
	//  - use params: schema.NewParamsOneOfByParams(params)
	//  - use jsonschema: schema.NewParamsOneOfByJSONSchema(jsonschema)
	// If is nil, signals that the tool does not need any input parameter
	*ParamsOneOf
}

// ParameterInfo is the information of a parameter.
// It is used to describe the parameters of a tool.
type ParameterInfo struct {
	// The type of the parameter.
	Type DataType
	// The element type of the parameter, only for array.
	ElemInfo *ParameterInfo
	// The sub parameters of the parameter, only for object.
	SubParams map[string]*ParameterInfo
	// The description of the parameter.
	Desc string
	// The enum values of the parameter, only for string.
	Enum []string
	// Whether the parameter is required.
	Required bool
}

// ParamsOneOf holds a tool's parameter schema using exactly one of two
// representations. Choose the one that best fits your needs:
//
//  1. [NewParamsOneOfByParams] — lightweight: describe parameters as a
//     map[string]*[ParameterInfo]. Covers the most common cases (scalars,
//     arrays, nested objects, enums, required flags).
//
//  2. [NewParamsOneOfByJSONSchema] — powerful: supply a full
//     *jsonschema.Schema (JSON Schema 2020-12). Required when you need
//     features not expressible via ParameterInfo, such as anyOf, oneOf, or
//     $defs references. [utils.InferTool] generates this form automatically
//     from Go struct tags.
//
// You must use exactly one constructor — setting both fields is invalid.
// If ParamsOneOf is nil, the tool takes no input parameters.
type ParamsOneOf struct {
	// use NewParamsOneOfByParams to set this field
	params map[string]*ParameterInfo

	jsonschema *jsonschema.Schema
}

// NewParamsOneOfByParams creates a ParamsOneOf with map[string]*ParameterInfo.
func NewParamsOneOfByParams(params map[string]*ParameterInfo) *ParamsOneOf {
	return &ParamsOneOf{
		params: params,
	}
}

// NewParamsOneOfByJSONSchema creates a ParamsOneOf with *jsonschema.Schema.
func NewParamsOneOfByJSONSchema(s *jsonschema.Schema) *ParamsOneOf {
	return &ParamsOneOf{
		jsonschema: s,
	}
}

// ToJSONSchema parses ParamsOneOf, converts the parameter description that user actually provides, into the format ready to be passed to Model.
func (p *ParamsOneOf) ToJSONSchema() (*jsonschema.Schema, error) {
	if p == nil {
		return nil, nil
	}

	if p.params != nil {
		sc := &jsonschema.Schema{
			Properties: orderedmap.New[string, *jsonschema.Schema](),
			Type:       string(Object),
			Required:   make([]string, 0, len(p.params)),
		}

		keys := make([]string, 0, len(p.params))
		for k := range p.params {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := p.params[k]
			sc.Properties.Set(k, paramInfoToJSONSchema(v))
			if v.Required {
				sc.Required = append(sc.Required, k)
			}
		}

		return sc, nil
	}

	return p.jsonschema, nil
}

func paramInfoToJSONSchema(paramInfo *ParameterInfo) *jsonschema.Schema {
	js := &jsonschema.Schema{
		Type:        string(paramInfo.Type),
		Description: paramInfo.Desc,
	}

	if len(paramInfo.Enum) > 0 {
		js.Enum = make([]any, len(paramInfo.Enum))
		for i, enum := range paramInfo.Enum {
			js.Enum[i] = enum
		}
	}

	if paramInfo.ElemInfo != nil {
		js.Items = paramInfoToJSONSchema(paramInfo.ElemInfo)
	}

	if len(paramInfo.SubParams) > 0 {
		required := make([]string, 0, len(paramInfo.SubParams))
		js.Properties = orderedmap.New[string, *jsonschema.Schema]()
		keys := make([]string, 0, len(paramInfo.SubParams))
		for k := range paramInfo.SubParams {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		for _, k := range keys {
			v := paramInfo.SubParams[k]
			item := paramInfoToJSONSchema(v)
			js.Properties.Set(k, item)
			if v.Required {
				required = append(required, k)
			}
		}

		js.Required = required
	}

	return js
}
