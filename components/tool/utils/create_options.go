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
	"reflect"

	"github.com/eino-contrib/jsonschema"
)

// UnmarshalArguments is the function type for unmarshalling the arguments.
type UnmarshalArguments func(ctx context.Context, arguments string) (interface{}, error)

// MarshalOutput is the function type for marshalling the output.
type MarshalOutput func(ctx context.Context, output interface{}) (string, error)

type toolOptions struct {
	um         UnmarshalArguments
	m          MarshalOutput
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

// SchemaModifierFn is the schema modifier function for inferring tool parameter from tagged go struct.
// Within this function, end-user can parse custom go struct tags into corresponding json schema field.
// Parameters:
// 1. jsonTagName: the name defined in the json tag. Specifically, the last 'jsonTagName' visited is fixed to be '_root', which represents the entire go struct. Also, for array field, both the field itself and the element within the array will trigger this function.
// 2. t: the type of current schema, usually the field type of the go struct.
// 3. tag: the struct tag of current schema, usually the field tag of the go struct. Note that the element within an array field will use the same go struct tag as the array field itself.
// 4. schema: the current json schema object to be modified.
type SchemaModifierFn func(jsonTagName string, t reflect.Type, tag reflect.StructTag, schema *jsonschema.Schema)

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
