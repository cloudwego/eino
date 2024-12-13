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
	"fmt"

	"github.com/getkin/kin-openapi/openapi3"
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

// ToolInfo is the information of a tool.
type ToolInfo struct {
	// The unique name of the tool that clearly communicates its purpose.
	Name string
	// Used to tell the model how/when/why to use the tool.
	// You can provide few-shot examples as a part of the description.
	Desc string

	// The parameters the functions accepts (different models may require different parameter types).
	// can be described in two ways:
	//  - use ParameterInfo: schema.NewParamsOneOfByParams(params)
	//  - use OpenAPIV3: schema.NewParamsOneOfByOpenAPIV3(openAPIV3)
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
//  1. use Params: an intuitive way to describe the parameters that covers most of the use-cases.
//  2. use OpenAPIV3: a formal way to describe the parameters that strictly adheres to OpenAPIV3.0 specification.
//     See https://github.com/getkin/kin-openapi/blob/master/openapi3/schema.go.
type ParamsOneOf struct {
	// deprecated: use NewParamsOneOfByParams instead, Params will no longer be exported in the future.
	Params map[string]*ParameterInfo

	// deprecated: use NewParamsOneOfByOpenAPIV3 instead, OpenAPIV3 will no longer be exported in the future.
	OpenAPIV3 *openapi3.Schema
}

// NewParamsOneOfByParams creates a ParamsOneOf with map[string]*ParameterInfo.
func NewParamsOneOfByParams(params map[string]*ParameterInfo) *ParamsOneOf {
	return &ParamsOneOf{
		Params: params,
	}
}

// NewParamsOneOfByOpenAPIV3 creates a ParamsOneOf with *openapi3.Schema.
func NewParamsOneOfByOpenAPIV3(openAPIV3 *openapi3.Schema) *ParamsOneOf {
	return &ParamsOneOf{
		OpenAPIV3: openAPIV3,
	}
}

// ToOpenAPIV3 parses ParamsOneOf, converts the parameter description that user actually provides, into the format ready to be passed to Model.
func (p *ParamsOneOf) ToOpenAPIV3() (*openapi3.Schema, error) {
	if p == nil {
		return nil, nil
	}

	var (
		useParameterInfo = p.Params != nil
		useOpenAPIV3     = p.OpenAPIV3 != nil
	)

	if !useParameterInfo && !useOpenAPIV3 {
		return nil, fmt.Errorf("ParamsOneOf needs to have at least one method to describe the parameters")
	}

	if useParameterInfo && useOpenAPIV3 {
		return nil, fmt.Errorf("ParamsOneOf can only have one method to describe the parameters, but not multiple methods")
	}

	if p.Params != nil {
		sc := &openapi3.Schema{
			Properties: make(map[string]*openapi3.SchemaRef, len(p.Params)),
			Type:       openapi3.TypeObject,
			Required:   make([]string, 0, len(p.Params)),
		}

		for k := range p.Params {
			v := p.Params[k]
			sc.Properties[k] = paramInfoToJSONSchema(v)
			if v.Required {
				sc.Required = append(sc.Required, k)
			}
		}

		return sc, nil
	}

	return p.OpenAPIV3, nil
}

func paramInfoToJSONSchema(paramInfo *ParameterInfo) *openapi3.SchemaRef {
	var types string
	switch paramInfo.Type {
	case Null:
		types = "null"
	case Boolean:
		types = openapi3.TypeBoolean
	case Integer:
		types = openapi3.TypeInteger
	case Number:
		types = openapi3.TypeNumber
	case String:
		types = openapi3.TypeString
	case Array:
		types = openapi3.TypeArray
	case Object:
		types = openapi3.TypeObject
	}

	js := &openapi3.SchemaRef{
		Value: &openapi3.Schema{
			Type:        types,
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
		js.Value.Items = paramInfoToJSONSchema(paramInfo.ElemInfo)
	}

	if len(paramInfo.SubParams) > 0 {
		required := make([]string, 0, len(paramInfo.SubParams))
		js.Value.Properties = make(map[string]*openapi3.SchemaRef, len(paramInfo.SubParams))
		for k, v := range paramInfo.SubParams {
			item := paramInfoToJSONSchema(v)

			js.Value.Properties[k] = item

			if v.Required {
				required = append(required, k)
			}
		}

		js.Value.Required = required
	}

	return js
}