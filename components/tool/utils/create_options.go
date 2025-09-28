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
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/eino-contrib/jsonschema"
	"github.com/getkin/kin-openapi/openapi3"
)

// UnmarshalArguments is the function type for unmarshalling the arguments.
type UnmarshalArguments func(ctx context.Context, arguments string) (interface{}, error)

// MarshalOutput is the function type for marshalling the output.
type MarshalOutput func(ctx context.Context, output interface{}) (string, error)

type toolOptions struct {
	um         UnmarshalArguments
	m          MarshalOutput
	sc         SchemaCustomizerFn
	scModifier SchemaModifierFn
}

// Option is the option func for the tool.
type Option func(o *toolOptions)

// WithUnmarshalArguments wraps the unmarshal arguments option.
// when you want to unmarshal the arguments by yourself, you can use this option.
func WithUnmarshalArguments(um UnmarshalArguments) Option {
	return func(o *toolOptions) {
		o.um = um
	}
}

// WithMarshalOutput wraps the marshal output option.
// when you want to marshal the output by yourself, you can use this option.
func WithMarshalOutput(m MarshalOutput) Option {
	return func(o *toolOptions) {
		o.m = m
	}
}

// Deprecated, use SchemaModifierFn instead. For more information, see https://github.com/cloudwego/eino/discussions/397.
// SchemaCustomizerFn is the schema customizer function for inferring tool parameter from tagged go struct.
// Within this function, end-user can parse custom go struct tags into corresponding openapi schema field.
// Parameters:
// 1. name: the name of current schema, usually the field name of the go struct. Specifically, the last 'name' visited is fixed to be '_root', which represents the entire go struct. Also, for array field, both the field itself and the element within the array will trigger this function.
// 2. t: the type of current schema, usually the field type of the go struct.
// 3. tag: the struct tag of current schema, usually the field tag of the go struct. Note that the element within an array field will use the same go struct tag as the array field itself.
// 4. schema: the current openapi schema object to be customized.
type SchemaCustomizerFn func(name string, t reflect.Type, tag reflect.StructTag, schema *openapi3.Schema) error

// SchemaModifierFn is the schema modifier function for inferring tool parameter from tagged go struct.
// Within this function, end-user can parse custom go struct tags into corresponding openapi schema field.
// Parameters:
// 1. jsonTagName: the name defined in the json tag. Specifically, the last 'jsonTagName' visited is fixed to be '_root', which represents the entire go struct. Also, for array field, both the field itself and the element within the array will trigger this function.
// 2. t: the type of current schema, usually the field type of the go struct.
// 3. tag: the struct tag of current schema, usually the field tag of the go struct. Note that the element within an array field will use the same go struct tag as the array field itself.
// 4. schema: the current json schema object to be modified.
type SchemaModifierFn func(jsonTagName string, t reflect.Type, tag reflect.StructTag, schema *jsonschema.Schema)

// Deprecated, use WithSchemaModifier instead. For more information, see https://github.com/cloudwego/eino/discussions/397.
// WithSchemaCustomizer sets a user-defined schema customizer for inferring tool parameter from tagged go struct.
// If this option is not set, the defaultSchemaCustomizer will be used.
func WithSchemaCustomizer(sc SchemaCustomizerFn) Option {
	return func(o *toolOptions) {
		o.sc = sc
	}
}

// WithSchemaModifier sets a user-defined schema modifier for inferring tool parameter from tagged go struct.
func WithSchemaModifier(modifier SchemaModifierFn) Option {
	return func(o *toolOptions) {
		o.scModifier = modifier
	}
}

func getToolOptions(opt ...Option) *toolOptions {
	opts := &toolOptions{
		um: nil,
		m:  nil,
	}
	for _, o := range opt {
		o(opts)
	}
	return opts
}

// Deprecated. For more information, see https://github.com/cloudwego/eino/discussions/397.
// defaultSchemaCustomizer is the default schema customizer when using reflect to infer tool parameter from tagged go struct.
// Supported struct tags:
// 1. jsonschema: "description=xxx"
// 2. jsonschema: "enum=xxx,enum=yyy", or "enum=1,enum=2", or "enum=3.14,enum=3.15", etc.
// NOTE: will convert actual enum value such as "1" or "3.14" to actual field type defined in struct.
// NOTE: enum only supports string, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, bool.
// 3. jsonschema: "required"
// 4. can also use json: "xxx,omitempty" to mark the field as not required, which means an absence of 'omitempty' in json tag means the field is required.
// If this defaultSchemaCustomizer is not sufficient or suitable to your specific need, define your own SchemaCustomizerFn and pass it to WithSchemaCustomizer during InferTool or InferStreamTool.
func defaultSchemaCustomizer(name string, t reflect.Type, tag reflect.StructTag, schema *openapi3.Schema) error {
	jsonS := tag.Get("jsonschema")
	if len(jsonS) > 0 {
		tags := strings.Split(jsonS, ",")
		for _, tag := range tags {
			kv := strings.Split(tag, "=")
			if len(kv) == 2 {
				if kv[0] == "description" {
					schema.Description = kv[1]
				}
				if kv[0] == "enum" {
					rawV := kv[1]
					switch t.Kind() {
					case reflect.String:
						schema.Enum = append(schema.Enum, rawV)
					case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
						reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
						v, err := strconv.ParseInt(rawV, 10, 64)
						if err != nil {
							return fmt.Errorf("parse enum value %v to int64 failed: %w", rawV, err)
						}
						schema.Enum = append(schema.Enum, v)
					case reflect.Float32, reflect.Float64:
						v, err := strconv.ParseFloat(rawV, 64)
						if err != nil {
							return fmt.Errorf("parse enum value %v to float64 failed: %w", rawV, err)
						}
						schema.Enum = append(schema.Enum, v)
					case reflect.Bool:
						v, err := strconv.ParseBool(rawV)
						if err != nil {
							return fmt.Errorf("parse enum value %v to bool failed: %w", rawV, err)
						}
						schema.Enum = append(schema.Enum, v)
					default:
						return fmt.Errorf("enum tag unsupported for field type: %v", t)
					}
				}
			} else if len(kv) == 1 {
				if kv[0] == "required" {
					if schema.Extensions == nil {
						schema.Extensions = make(map[string]any, 1)
					}
					schema.Extensions["x_required"] = true
				}
			}
		}
	}

	json := tag.Get("json")
	if len(json) > 0 && !strings.Contains(json, "omitempty") {
		if schema.Extensions == nil {
			schema.Extensions = make(map[string]any, 1)
		}
		schema.Extensions["x_required"] = true
	}

	if name == "_root" {
		if err := setRequired(schema); err != nil {
			return err
		}
	}

	return nil
}

func setRequired(sc *openapi3.Schema) error { // check if properties are marked as required, set schema required to true accordingly
	if sc.Type != openapi3.TypeObject && sc.Type != openapi3.TypeArray {
		return nil
	}

	if sc.Type == openapi3.TypeArray {
		if sc.Items.Value.Extensions != nil {
			if _, ok := sc.Items.Value.Extensions["x_required"]; ok {
				delete(sc.Items.Value.Extensions, "x_required")
				if len(sc.Items.Value.Extensions) == 0 {
					sc.Items.Value.Extensions = nil
				}
			}
		}

		if err := setRequired(sc.Items.Value); err != nil {
			return fmt.Errorf("setRequired for array failed: %w", err)
		}
	}

	for k, p := range sc.Properties {
		if p.Value.Extensions != nil {
			if _, ok := p.Value.Extensions["x_required"]; ok {
				sc.Required = append(sc.Required, k)
				delete(p.Value.Extensions, "x_required")
				if len(p.Value.Extensions) == 0 {
					p.Value.Extensions = nil
				}
			}

		}
		err := setRequired(p.Value)
		if err != nil {
			return fmt.Errorf("setRequired for nested property %s failed: %w", k, err)
		}
	}

	sort.Strings(sc.Required)

	return nil
}
