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
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/eino-contrib/jsonschema"
	"github.com/getkin/kin-openapi/openapi3"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// DataType is the type of the parameter.
// It must be one of the following values: "object", "number", "integer", "string", "array", "null", "boolean", which is the same as the type of the parameter in OpenAPI v3.0.
type DataType string

const (
	Object  DataType = "object"
	Number  DataType = "number"
	Integer DataType = "integer"
	String  DataType = "string"
	Array   DataType = "array"
	Null    DataType = "null"
	Boolean DataType = "boolean"
)

// ToolChoice controls how the model calls tools (if any).
type ToolChoice string

const (
	// ToolChoiceForbidden indicates that the model should not call any tools.
	// Corresponds to "none" in OpenAI Chat Completion.
	ToolChoiceForbidden ToolChoice = "forbidden"

	// ToolChoiceAllowed indicates that the model can choose to generate a message or call one or more tools.
	// Corresponds to "auto" in OpenAI Chat Completion.
	ToolChoiceAllowed ToolChoice = "allowed"

	// ToolChoiceForced indicates that the model must call one or more tools.
	// Corresponds to "required" in OpenAI Chat Completion.
	ToolChoiceForced ToolChoice = "forced"
)

// ToolInfo is the information of a tool.
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
	//  - use openAPIV3: schema.NewParamsOneOfByOpenAPIV3(openAPIV3)
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

// ParamsOneOf is a union of the different methods user can choose which describe a tool's request parameters.
// User must specify one and ONLY one method to describe the parameters.
//  1. use NewParamsOneOfByParams(): an intuitive way to describe the parameters that covers most of the use-cases.
//  2. use NewParamsOneOfByOpenAPIV3(): a formal way to describe the parameters that strictly adheres to OpenAPIV3.0 specification.
//     See https://github.com/getkin/kin-openapi/blob/master/openapi3/schema.go.
type ParamsOneOf struct {
	// use NewParamsOneOfByParams to set this field
	params map[string]*ParameterInfo

	// use NewParamsOneOfByOpenAPIV3 to set this field
	openAPIV3 *openapi3.Schema

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

// Deprecated: use NewParamsOneOfByJSONSchema instead.
// For more information, see https://github.com/cloudwego/eino/discussions/397.
// NewParamsOneOfByOpenAPIV3 creates a ParamsOneOf with *openapi3.Schema.
func NewParamsOneOfByOpenAPIV3(openAPIV3 *openapi3.Schema) *ParamsOneOf {
	return &ParamsOneOf{
		openAPIV3: openAPIV3,
	}
}

// Deprecated: use ToJSONSchema instead.
// For more information, see https://github.com/cloudwego/eino/discussions/397.
// ToOpenAPIV3 parses ParamsOneOf, converts the parameter description that user actually provides, into the format ready to be passed to Model.
func (p *ParamsOneOf) ToOpenAPIV3() (*openapi3.Schema, error) {
	if p == nil {
		return nil, nil
	}

	if p.params != nil {
		sc := &openapi3.Schema{
			Properties: make(map[string]*openapi3.SchemaRef, len(p.params)),
			Type:       string(Object),
			Required:   make([]string, 0, len(p.params)),
		}

		for k := range p.params {
			v := p.params[k]
			sc.Properties[k] = paramInfoToOpenAPIV3(v)
			if v.Required {
				sc.Required = append(sc.Required, k)
			}
		}

		return sc, nil
	}

	if p.jsonschema != nil {
		sc, err := jsonSchemaToOpenAPIV3(p.jsonschema)
		if err != nil {
			return nil, fmt.Errorf("convert JSONSchema to OpenAPIV3 failed: %w", err)
		}
		return sc.Value, nil
	}

	return p.openAPIV3, nil
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

		for k := range p.params {
			v := p.params[k]
			sc.Properties.Set(k, paramInfoToJSONSchema(v))
			if v.Required {
				sc.Required = append(sc.Required, k)
			}
		}

		return sc, nil
	}

	if p.openAPIV3 != nil {
		js, err := openapiV3ToJSONSchema(p.openAPIV3)
		if err != nil {
			return nil, fmt.Errorf("convert OpenAPIV3 to JSONSchema failed: %w", err)
		}
		return js, nil
	}

	return p.jsonschema, nil
}

func paramInfoToOpenAPIV3(paramInfo *ParameterInfo) *openapi3.SchemaRef {
	js := &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			Type:        string(paramInfo.Type),
			Description: paramInfo.Desc,
		},
	}

	if len(paramInfo.Enum) > 0 {
		js.Value.Enum = make([]any, 0, len(paramInfo.Enum))
		for _, enum := range paramInfo.Enum {
			js.Value.Enum = append(js.Value.Enum, enum)
		}
	}

	if paramInfo.ElemInfo != nil {
		js.Value.Items = paramInfoToOpenAPIV3(paramInfo.ElemInfo)
	}

	if len(paramInfo.SubParams) > 0 {
		required := make([]string, 0, len(paramInfo.SubParams))
		js.Value.Properties = make(map[string]*openapi3.SchemaRef, len(paramInfo.SubParams))
		for k, v := range paramInfo.SubParams {
			item := paramInfoToOpenAPIV3(v)

			js.Value.Properties[k] = item

			if v.Required {
				required = append(required, k)
			}
		}

		js.Value.Required = required
	}

	return js
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
		for k, v := range paramInfo.SubParams {
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

func openapiV3ToJSONSchema(openAPIV3 *openapi3.Schema) (*jsonschema.Schema, error) {
	if openAPIV3 == nil {
		return nil, nil
	}

	js := &jsonschema.Schema{
		Type:          openAPIV3.Type,
		Title:         openAPIV3.Title,
		Format:        openAPIV3.Format,
		Description:   openAPIV3.Description,
		Default:       openAPIV3.Default,
		MaxLength:     openAPIV3.MaxLength,
		UniqueItems:   openAPIV3.UniqueItems,
		ReadOnly:      openAPIV3.ReadOnly,
		WriteOnly:     openAPIV3.WriteOnly,
		Deprecated:    openAPIV3.Deprecated,
		Pattern:       openAPIV3.Pattern,
		MaxItems:      openAPIV3.MaxItems,
		MaxProperties: openAPIV3.MaxProps,
	}

	if openAPIV3.Example != nil {
		js.Examples = []any{openAPIV3.Example}
	}

	if openAPIV3.MinLength > 0 {
		js.MinLength = &openAPIV3.MinLength
	}

	if openAPIV3.MinItems > 0 {
		js.MinItems = &openAPIV3.MinItems
	}

	if openAPIV3.MinProps > 0 {
		js.MinProperties = &openAPIV3.MinProps
	}

	if openAPIV3.OneOf != nil {
		js.OneOf = make([]*jsonschema.Schema, len(openAPIV3.OneOf))
		for i, oneOf := range openAPIV3.OneOf {
			v, err := openapiV3ToJSONSchema(oneOf.Value)
			if err != nil {
				return nil, err
			}
			js.OneOf[i] = v
		}
	}

	if openAPIV3.AnyOf != nil {
		js.AnyOf = make([]*jsonschema.Schema, len(openAPIV3.AnyOf))
		for i, anyOf := range openAPIV3.AnyOf {
			v, err := openapiV3ToJSONSchema(anyOf.Value)
			if err != nil {
				return nil, err
			}
			js.AnyOf[i] = v
		}
	}

	if openAPIV3.AllOf != nil {
		js.AllOf = make([]*jsonschema.Schema, len(openAPIV3.AllOf))
		for i, allOf := range openAPIV3.AllOf {
			v, err := openapiV3ToJSONSchema(allOf.Value)
			if err != nil {
				return nil, err
			}
			js.AllOf[i] = v
		}
	}

	if openAPIV3.Not != nil {
		v, err := openapiV3ToJSONSchema(openAPIV3.Not.Value)
		if err != nil {
			return nil, err
		}
		js.Not = v
	}

	if openAPIV3.Min != nil {
		if openAPIV3.ExclusiveMin {
			js.ExclusiveMinimum = json.Number(strconv.FormatFloat(*openAPIV3.Min, 'g', -1, 64))
		} else {
			js.Minimum = json.Number(strconv.FormatFloat(*openAPIV3.Min, 'g', -1, 64))
		}
	}

	if openAPIV3.Max != nil {
		if openAPIV3.ExclusiveMax {
			js.ExclusiveMaximum = json.Number(strconv.FormatFloat(*openAPIV3.Max, 'g', -1, 64))
		} else {
			js.Maximum = json.Number(strconv.FormatFloat(*openAPIV3.Max, 'g', -1, 64))
		}
	}

	if openAPIV3.MultipleOf != nil {
		js.MultipleOf = json.Number(strconv.FormatFloat(*openAPIV3.MultipleOf, 'g', -1, 64))
	}

	if openAPIV3.Enum != nil {
		js.Enum = make([]any, len(openAPIV3.Enum))
		for i, enum := range openAPIV3.Enum {
			js.Enum[i] = enum
		}
	}

	if openAPIV3.Items != nil {
		v, err := openapiV3ToJSONSchema(openAPIV3.Items.Value)
		if err != nil {
			return nil, err
		}
		js.Items = v
	}

	if openAPIV3.Properties != nil {
		js.Properties = orderedmap.New[string, *jsonschema.Schema]()
		for k, v := range openAPIV3.Properties {
			if v == nil || v.Value == nil {
				continue
			}
			v_, err := openapiV3ToJSONSchema(v.Value)
			if err != nil {
				return nil, err
			}
			js.Properties.Set(k, v_)
		}
	}

	if openAPIV3.Required != nil {
		js.Required = make([]string, len(openAPIV3.Required))
		for i, required := range openAPIV3.Required {
			js.Required[i] = required
		}
	}

	if openAPIV3.AdditionalProperties.Schema != nil {
		v, err := openapiV3ToJSONSchema(openAPIV3.AdditionalProperties.Schema.Value)
		if err != nil {
			return nil, err
		}
		js.AdditionalProperties = v
	}

	if openAPIV3.Extensions != nil {
		for k, v := range openAPIV3.Extensions {
			js.Extras[k] = v
		}
	}

	return js, nil
}

func jsonSchemaToOpenAPIV3(js *jsonschema.Schema) (*openapi3.SchemaRef, error) {
	if js == nil {
		return nil, nil
	}

	openAPIV3 := &openapi3.Schema{
		Type:        js.Type,
		Title:       js.Title,
		Format:      js.Format,
		Description: js.Description,
		Default:     js.Default,
		MaxLength:   js.MaxLength,
		UniqueItems: js.UniqueItems,
		ReadOnly:    js.ReadOnly,
		WriteOnly:   js.WriteOnly,
		Deprecated:  js.Deprecated,
		Pattern:     js.Pattern,
		MaxItems:    js.MaxItems,
		MaxProps:    js.MaxProperties,
	}

	if js.MinLength != nil {
		openAPIV3.MinLength = *js.MinLength
	}

	if js.MinItems != nil {
		openAPIV3.MinItems = *js.MinItems
	}

	if js.MinProperties != nil {
		openAPIV3.MinProps = *js.MinProperties
	}

	if len(js.Examples) > 0 {
		openAPIV3.Example = js.Examples[0]
	}

	if js.OneOf != nil {
		openAPIV3.OneOf = make([]*openapi3.SchemaRef, len(js.OneOf))
		for i, oneOf := range js.OneOf {
			v, err := jsonSchemaToOpenAPIV3(oneOf)
			if err != nil {
				return nil, err
			}
			openAPIV3.OneOf[i] = v
		}
	}

	if js.AnyOf != nil {
		openAPIV3.AnyOf = make([]*openapi3.SchemaRef, len(js.AnyOf))
		for i, anyOf := range js.AnyOf {
			v, err := jsonSchemaToOpenAPIV3(anyOf)
			if err != nil {
				return nil, err
			}
			openAPIV3.AnyOf[i] = v
		}
	}

	if js.AllOf != nil {
		openAPIV3.AllOf = make([]*openapi3.SchemaRef, len(js.AllOf))
		for i, allOf := range js.AllOf {
			v, err := jsonSchemaToOpenAPIV3(allOf)
			if err != nil {
				return nil, err
			}
			openAPIV3.AllOf[i] = v
		}
	}

	if js.Not != nil {
		v, err := jsonSchemaToOpenAPIV3(js.Not)
		if err != nil {
			return nil, err
		}
		openAPIV3.Not = v
	}

	if js.ExclusiveMaximum != "" {
		openAPIV3.ExclusiveMax = true
		max, err := js.ExclusiveMaximum.Float64()
		if err != nil {
			return nil, fmt.Errorf("`ExclusiveMaximum` in JSONSchema must be a number: %w", err)
		}
		openAPIV3.Max = &max
	}

	if js.ExclusiveMinimum != "" {
		openAPIV3.ExclusiveMin = true
		min, err := js.ExclusiveMinimum.Float64()
		if err != nil {
			return nil, fmt.Errorf("`ExclusiveMinimum` in JSONSchema must be a number: %w", err)
		}
		openAPIV3.Min = &min
	}

	if js.MultipleOf != "" {
		multipleOf, err := js.MultipleOf.Float64()
		if err != nil {
			return nil, fmt.Errorf("`MultipleOf` in JSONSchema must be a number: %w", err)
		}
		openAPIV3.MultipleOf = &multipleOf
	}

	if js.Enum != nil {
		openAPIV3.Enum = make([]any, len(js.Enum))
		for i, enum := range js.Enum {
			openAPIV3.Enum[i] = enum
		}
	}

	if js.Items != nil {
		v, err := jsonSchemaToOpenAPIV3(js.Items)
		if err != nil {
			return nil, err
		}
		openAPIV3.Items = v
	}

	if js.Properties != nil {
		openAPIV3.Properties = make(map[string]*openapi3.SchemaRef, js.Properties.Len())
		for pair := js.Properties.Oldest(); pair != nil; pair = pair.Next() {
			v, err := jsonSchemaToOpenAPIV3(pair.Value)
			if err != nil {
				return nil, err
			}
			openAPIV3.Properties[pair.Key] = v
		}
	}

	if js.Required != nil {
		openAPIV3.Required = make([]string, len(js.Required))
		for i, required := range js.Required {
			openAPIV3.Required[i] = required
		}
	}

	if js.AdditionalProperties != nil {
		add, err := jsonSchemaToOpenAPIV3(js.AdditionalProperties)
		if err != nil {
			return nil, err
		}
		openAPIV3.AdditionalProperties = openapi3.AdditionalProperties{
			Schema: add,
		}
	}

	if js.Extras != nil {
		for k, v := range js.Extras {
			openAPIV3.Extensions[k] = v
		}
	}

	return openapi3.NewSchemaRef("", openAPIV3), nil
}
